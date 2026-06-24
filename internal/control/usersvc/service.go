// Package usersvc holds the user (multi-credential) create / update / delete
// logic as a transport-agnostic service so both the HTTP API and the Telegram
// management bot drive the SAME code path. A "user" is identified by the shared
// Client.Email — the same email on multiple inbounds is one logical person whose
// traffic is summed and whose quota/expiry are enforced together.
//
// The service follows the same callback shape as quota.Enforcer: it owns the DB
// writes and calls back out for Apply (re-render + push engine config) and Audit
// so it stays free of the orchestrator and gin packages and is testable in
// isolation.
package usersvc

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/aipo-lenshow/EdgeNest/internal/control/model"
	"github.com/aipo-lenshow/EdgeNest/internal/control/store"
)

// Error is a coded service error. Callers map Code to an HTTP status (API) or a
// localized message (bot); Msg is the human-readable detail.
type Error struct {
	Code string
	Msg  string
}

func (e *Error) Error() string { return e.Msg }

func errf(code, msg string) *Error { return &Error{Code: code, Msg: msg} }

// Service owns the user mutations. Apply/Audit are optional callbacks: Apply
// re-renders DesiredConfig and pushes it to the node (nil = skip, e.g. tests);
// Audit records a sensitive operation (nil = no audit).
type Service struct {
	Store *store.Store
	Apply func(ctx context.Context, nodeID uint) error
	Audit func(action, resource string, meta map[string]string)
}

func (s *Service) apply(ctx context.Context, nodeID uint) error {
	if s.Apply == nil {
		return nil
	}
	return s.Apply(ctx, nodeID)
}

func (s *Service) audit(action, resource string, meta map[string]string) {
	if s.Audit != nil {
		s.Audit(action, resource, meta)
	}
}

// CreateParams describes a new logical user.
type CreateParams struct {
	Email      string // optional; blank → next NNN@EdgeNest.Local
	QuotaBytes int64  // 0 = unlimited
	ExpiryDays int    // 0 = never; N = end of (today+N)th day; negatives allowed (test)
	InboundIDs []uint // empty = every eligible enabled inbound
}

// CreateResult is the outcome of a successful create.
type CreateResult struct {
	Email       string
	SubID       uint
	SubToken    string
	InboundTags []string
	Skipped     []string
}

// Create provisions one logical user across the chosen inbounds: a Client per
// inbound (credentials mirrored from each inbound's existing wizard client, so
// the protocol's UUID/password/flow shape is reproduced without re-deriving
// per-protocol rules) plus one bundle subscription. SS / SOCKS inbounds are
// skipped (see skipForMultiUser) and reported back.
func (s *Service) Create(ctx context.Context, nodeID uint, p CreateParams) (*CreateResult, error) {
	inbounds, err := s.Store.ListInbounds(nodeID)
	if err != nil {
		return nil, errf("DB_ERROR", err.Error())
	}

	email := strings.TrimSpace(p.Email)
	if email == "" {
		seq, err := s.Store.NextSeqEmail()
		if err != nil {
			return nil, errf("DB_ERROR", err.Error())
		}
		email = seq
	} else {
		// A brand-new user must not collide with an existing identity (that would
		// silently merge into another user's credential set).
		if existing, _ := s.Store.ClientsByEmail(email); len(existing) > 0 {
			return nil, errf("EMAIL_EXISTS", "a user with this identifier already exists")
		}
	}

	// Resolve target inbounds: explicit ids, else every enabled inbound.
	want := map[uint]bool{}
	for _, id := range p.InboundIDs {
		want[id] = true
	}

	var firstClientID uint
	var createdIDs []uint
	var createdTags, skipped []string
	for i := range inbounds {
		ib := inbounds[i]
		if len(want) > 0 && !want[ib.ID] {
			continue
		}
		if len(want) == 0 && !ib.Enabled {
			continue
		}
		if skipForMultiUser(ib.Type) {
			skipped = append(skipped, ib.Tag)
			continue
		}
		cl := newClientForInbound(&ib, email, p.QuotaBytes, expiryFromDays(p.ExpiryDays))
		if err := s.Store.CreateClient(cl); err != nil {
			return nil, errf("DB_ERROR", err.Error())
		}
		if firstClientID == 0 {
			firstClientID = cl.ID
		}
		createdIDs = append(createdIDs, ib.ID)
		createdTags = append(createdTags, ib.Tag)
	}

	if firstClientID == 0 {
		return nil, errf("NO_INBOUND",
			"no eligible inbound to assign (SS/SOCKS inbounds can't host extra users)")
	}

	// Bundle subscription seeded by the first created client, scoped to exactly
	// the inbounds we just provisioned (IDs, so tag renames don't break it).
	token := randHex(24)
	allowed, _ := json.Marshal(createdIDs)
	sub := &model.Subscription{
		Name:            email,
		Token:           token,
		TokenHash:       store.HashToken(token),
		ClientID:        firstClientID,
		AllowedNodes:    "[]",
		AllowedInbounds: string(allowed),
	}
	if err := s.Store.CreateSubscription(sub); err != nil {
		return nil, errf("DB_ERROR", err.Error())
	}

	if err := s.apply(ctx, nodeID); err != nil {
		return nil, errf("APPLY_FAILED", err.Error())
	}
	s.audit("user.create", "user:"+email, map[string]string{
		"inbounds": strings.Join(createdTags, ","),
	})
	return &CreateResult{
		Email:       email,
		SubID:       sub.ID,
		SubToken:    token,
		InboundTags: createdTags,
		Skipped:     skipped,
	}, nil
}

// UpdateParams patches one user. Nil fields are left untouched.
type UpdateParams struct {
	NewEmail   *string // rename the user across every inbound
	QuotaBytes *int64
	ExpiryDays *int   // re-derived to end-of-day, server TZ (relative)
	ExpiryAt   *int64 // absolute unix expiry; 0 = never. Used when ExpiryDays is nil.
	Enabled    *bool
	ResetUsage *bool
	InboundIDs *[]uint // reconcile which inbounds this user is on
}

// UpdateResult is the outcome of a successful update.
type UpdateResult struct {
	Email   string
	Clients int
}

// Update patches quota / expiry / enabled across every client of one user
// (keyed by email). Re-enabling a user that was auto-disabled is best paired
// with ResetUsage=true or a raised quota, else the next enforcement tick
// disables them again.
func (s *Service) Update(ctx context.Context, nodeID uint, email string, p UpdateParams) (*UpdateResult, error) {
	clients, err := s.Store.ClientsByEmail(email)
	if err != nil {
		return nil, errf("DB_ERROR", err.Error())
	}
	if len(clients) == 0 {
		return nil, errf("NOT_FOUND", "user not found")
	}

	// Rename (global): the identifier is the user identity. Renaming rewrites
	// every client's Email; subscriptions reference ClientID (not email) and the
	// resolver reads the seed client's CURRENT email, so a coordinated rename
	// keeps every bundle working. Reject collisions with another existing user.
	rename := ""
	if p.NewEmail != nil {
		rename = strings.TrimSpace(*p.NewEmail)
	}
	idSet := map[uint]bool{}
	for _, cl := range clients {
		idSet[cl.ID] = true
	}
	if rename != "" && rename != email {
		if existing, _ := s.Store.ClientsByEmail(rename); len(existing) > 0 {
			return nil, errf("EMAIL_EXISTS", "another user already uses this identifier")
		}
	} else {
		rename = "" // no-op rename
	}

	// Re-enabling a user whose expiry has already passed must actually stick:
	// without clearing the stale past expiry, the next enforcement tick disables
	// them again. So when the request enables the user and doesn't set a new
	// expiry, drop any already-passed expiry to 0 (never). A still-future expiry
	// is left alone — it isn't what's blocking the account.
	now := time.Now().Unix()
	clearStaleExpiry := p.Enabled != nil && *p.Enabled && p.ExpiryDays == nil && p.ExpiryAt == nil

	for i := range clients {
		cl := clients[i]
		if rename != "" {
			cl.Email = rename
		}
		if p.QuotaBytes != nil {
			cl.QuotaBytes = *p.QuotaBytes
		}
		switch {
		case p.ExpiryDays != nil:
			cl.ExpiryAt = expiryFromDays(*p.ExpiryDays)
		case p.ExpiryAt != nil:
			cl.ExpiryAt = *p.ExpiryAt
		case clearStaleExpiry && cl.ExpiryAt > 0 && cl.ExpiryAt <= now:
			cl.ExpiryAt = 0
		}
		if p.Enabled != nil {
			cl.Enabled = *p.Enabled
		}
		if err := s.Store.UpdateClient(&cl); err != nil {
			return nil, errf("DB_ERROR", err.Error())
		}
		if p.ResetUsage != nil && *p.ResetUsage {
			_ = s.Store.ResetClientTraffic(cl.ID)
		}
	}

	// Carry the rename onto subscription display names that mirrored the email.
	if rename != "" {
		if subs, err := s.Store.ListSubscriptions(); err == nil {
			for _, sub := range subs {
				if idSet[sub.ClientID] && sub.Name == email {
					sub.Name = rename
					_ = s.Store.UpdateSubscriptionName(sub.ID, rename)
				}
			}
		}
	}

	// Membership reconcile: add the user to newly-checked inbounds (mirroring
	// each inbound's credential shape) and remove from unchecked ones. Keeps the
	// user's bundle subscription pointed at a surviving client and scoped to the
	// new inbound set.
	finalEmail := email
	if rename != "" {
		finalEmail = rename
	}
	if p.InboundIDs != nil {
		if err := s.reconcileUserInbounds(finalEmail, *p.InboundIDs, nodeID); err != nil {
			return nil, errf("MEMBERSHIP", err.Error())
		}
	}

	if err := s.apply(ctx, nodeID); err != nil {
		return nil, errf("APPLY_FAILED", err.Error())
	}
	s.audit("user.update", "user:"+finalEmail, nil)
	return &UpdateResult{Email: finalEmail, Clients: len(clients)}, nil
}

// DeleteResult is the outcome of a successful delete.
type DeleteResult struct {
	Email          string
	DeletedClients int
}

// Delete removes every client of a user and any bundle subscription seeded by
// one of those clients.
func (s *Service) Delete(ctx context.Context, nodeID uint, email string) (*DeleteResult, error) {
	clients, err := s.Store.ClientsByEmail(email)
	if err != nil {
		return nil, errf("DB_ERROR", err.Error())
	}
	if len(clients) == 0 {
		return nil, errf("NOT_FOUND", "user not found")
	}
	idSet := map[uint]bool{}
	for _, cl := range clients {
		idSet[cl.ID] = true
	}
	// Drop subscriptions seeded by any of this user's clients first.
	if subs, err := s.Store.ListSubscriptions(); err == nil {
		for _, sub := range subs {
			if idSet[sub.ClientID] {
				_ = s.Store.DeleteSubscription(sub.ID)
			}
		}
	}
	for _, cl := range clients {
		if err := s.Store.DeleteClient(cl.ID); err != nil {
			return nil, errf("DB_ERROR", err.Error())
		}
	}
	if err := s.apply(ctx, nodeID); err != nil {
		return nil, errf("APPLY_FAILED", err.Error())
	}
	s.audit("user.delete", "user:"+email, map[string]string{
		"clients": strconv.Itoa(len(clients)),
	})
	return &DeleteResult{Email: email, DeletedClients: len(clients)}, nil
}

// reconcileUserInbounds makes the user `email` present on exactly `targetIDs`:
// adds a mirrored client on newly-checked inbounds (skipping SS/SOCKS), deletes
// the client on unchecked ones, then keeps the user's bundle subscription
// pointed at a surviving client and scoped to the new inbound set. No-op-safe.
func (s *Service) reconcileUserInbounds(email string, targetIDs []uint, nodeID uint) error {
	target := map[uint]bool{}
	for _, id := range targetIDs {
		target[id] = true
	}
	inbounds, err := s.Store.ListInbounds(nodeID)
	if err != nil {
		return err
	}
	inboundByID := map[uint]*model.Inbound{}
	for i := range inbounds {
		inboundByID[inbounds[i].ID] = &inbounds[i]
	}
	cur, err := s.Store.ClientsByEmail(email)
	if err != nil {
		return err
	}
	curByInbound := map[uint]model.Client{}
	var quota, expiry int64
	for _, cl := range cur {
		curByInbound[cl.InboundID] = cl
		if cl.QuotaBytes > quota {
			quota = cl.QuotaBytes
		}
		if cl.ExpiryAt > expiry {
			expiry = cl.ExpiryAt
		}
	}
	// Additions.
	for id := range target {
		if _, ok := curByInbound[id]; ok {
			continue
		}
		ib := inboundByID[id]
		if ib == nil || skipForMultiUser(ib.Type) {
			continue
		}
		if err := s.Store.CreateClient(newClientForInbound(ib, email, quota, expiry)); err != nil {
			return err
		}
	}
	// Removals.
	for id, cl := range curByInbound {
		if !target[id] {
			if err := s.Store.DeleteClient(cl.ID); err != nil {
				return err
			}
		}
	}
	// Keep the user's bundle subscription valid: re-point its seed client if we
	// deleted the one it referenced, and rescope it to the surviving inbounds.
	survivors, err := s.Store.ClientsByEmail(email)
	if err != nil || len(survivors) == 0 {
		return err
	}
	survivingIDs := map[uint]bool{}
	var survivingInboundIDs []uint
	for _, cl := range survivors {
		survivingIDs[cl.ID] = true
		survivingInboundIDs = append(survivingInboundIDs, cl.InboundID)
	}
	subs, err := s.Store.ListSubscriptions()
	if err != nil {
		return nil // subscription rescope is best-effort
	}
	allowed, _ := json.Marshal(survivingInboundIDs)
	for _, sub := range subs {
		// Match the user's bundle: seed client belongs to this user (alive or
		// just-deleted). We track by the surviving set plus the pre-reconcile set.
		owned := survivingIDs[sub.ClientID]
		if !owned {
			wasCur := false
			for _, cl := range cur {
				if cl.ID == sub.ClientID {
					wasCur = true
					break
				}
			}
			if !wasCur {
				continue
			}
		}
		newSeed := sub.ClientID
		if !survivingIDs[newSeed] {
			newSeed = survivors[0].ID
		}
		_ = s.Store.UpdateSubscriptionScope(sub.ID, newSeed, string(allowed))
	}
	return nil
}

// ── credential-shape helpers (moved from api.user_handlers) ─────────────────

// randHex returns n random bytes hex-encoded (2n chars).
func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// skipForMultiUser: inbound types that cannot host an additional per-user
// credential, so the multi-user "assign to inbound" flow skips them:
//   - shadowsocks: SS-2022 is single-user per inbound (one PSK).
//   - socks: the credential lives in inbound settings (one shared user/pass),
//     not per-client, so a second client wouldn't authenticate distinctly.
func skipForMultiUser(typ string) bool {
	return typ == "shadowsocks" || typ == "socks"
}

// SkipForMultiUser reports whether an inbound type can't host extra shared users
// (SS/SOCKS are single-credential). Exported so callers building an inbound
// picker (the bot's create wizard) can pre-filter the same way Create does.
func SkipForMultiUser(typ string) bool { return skipForMultiUser(typ) }

// expiryFromDays converts a "from today" day count into a unix expiry at the
// end of that day (23:59:59) in the SERVER's local timezone — matching the
// "expires the moment the next day starts" semantics. 0 → never (0). Negative
// values yield a past instant (already expired), which is how an operator tests
// expiry without waiting: set -1 and run the check now.
func expiryFromDays(days int) int64 {
	if days == 0 {
		return 0
	}
	t := time.Now().AddDate(0, 0, days)
	eod := time.Date(t.Year(), t.Month(), t.Day(), 23, 59, 59, 0, t.Location())
	return eod.Unix()
}

// inboundNeedsUUID / inboundNeedsPassword are the fallback credential-shape
// rules used only when an inbound has no existing client to mirror.
func inboundNeedsUUID(typ string) bool {
	switch typ {
	case "vless", "vless-ws", "vless-xhttp", "vmess", "vmess-ws", "tuic":
		return true
	}
	return false
}

func inboundNeedsPassword(typ string) bool {
	switch typ {
	case "trojan", "hysteria2", "anytls", "tuic":
		return true
	}
	return false
}

// newClientForInbound builds a Client for `email` on inbound `ib`, mirroring the
// credential shape (UUID/password/flow presence) of the inbound's existing
// client so the protocol's needs are reproduced without re-deriving per-protocol
// rules. Falls back to type-based shape when the inbound has no client to copy.
func newClientForInbound(ib *model.Inbound, email string, quota, expiryAt int64) *model.Client {
	cl := &model.Client{
		InboundID:  ib.ID,
		Email:      email,
		QuotaBytes: quota,
		ExpiryAt:   expiryAt,
		Enabled:    true,
	}
	if len(ib.Clients) > 0 {
		tmpl := ib.Clients[0]
		if tmpl.UUID != "" {
			cl.UUID = uuid.NewString()
		}
		if tmpl.Password != "" {
			cl.Password = randHex(16)
		}
		cl.Flow = tmpl.Flow
	} else {
		if inboundNeedsUUID(ib.Type) {
			cl.UUID = uuid.NewString()
		}
		if inboundNeedsPassword(ib.Type) {
			cl.Password = randHex(16)
		}
	}
	return cl
}
