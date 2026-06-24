package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"time"

	"github.com/aipo-lenshow/EdgeNest/internal/control/model"
	"gorm.io/gorm"
)

// HashToken returns the hex-encoded SHA-256 of a subscription token. We store
// hashes only so a panel DB leak does not expose live tokens.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// CreateSubscription inserts a new subscription row.
func (s *Store) CreateSubscription(sub *model.Subscription) error {
	if sub.TokenHash == "" {
		return errors.New("subscription.token_hash required")
	}
	if sub.ClientID == 0 {
		return errors.New("subscription.client_id required")
	}
	sub.CreatedAt = time.Now().Unix()
	return s.db.Create(sub).Error
}

// RotateSubscriptionToken atomically replaces a subscription's token (both
// plaintext + hash) and clears the revoked flag. Returns the updated row.
//
// Used by the admin "rotate" action — old URL stops resolving immediately,
// new URL takes its place against the same subscription id.
func (s *Store) RotateSubscriptionToken(id uint, token, tokenHash string) (*model.Subscription, error) {
	if token == "" || tokenHash == "" {
		return nil, errors.New("token + token_hash required")
	}
	if err := s.db.Model(&model.Subscription{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"token":      token,
			"token_hash": tokenHash,
			"revoked":    false,
		}).Error; err != nil {
		return nil, err
	}
	return s.GetSubscription(id)
}

// ListSubscriptions returns all subscriptions (admin view).
func (s *Store) ListSubscriptions() ([]model.Subscription, error) {
	var subs []model.Subscription
	err := s.db.Order("id asc").Find(&subs).Error
	return subs, err
}

// GetSubscription fetches one by id.
func (s *Store) GetSubscription(id uint) (*model.Subscription, error) {
	var sub model.Subscription
	if err := s.db.First(&sub, id).Error; err != nil {
		return nil, err
	}
	return &sub, nil
}

// GetSubscriptionByTokenHash resolves a token (hashed) to its subscription.
// Returns nil + nil if not found (caller maps to 404 without leaking existence).
func (s *Store) GetSubscriptionByTokenHash(hash string) (*model.Subscription, error) {
	var sub model.Subscription
	err := s.db.First(&sub, "token_hash = ?", hash).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &sub, nil
}

// DeleteSubscription removes a subscription row.
func (s *Store) DeleteSubscription(id uint) error {
	return s.db.Delete(&model.Subscription{}, id).Error
}

// UpdateSubscriptionName sets the display name (used when a user is renamed and
// the bundle name mirrored the old identifier).
func (s *Store) UpdateSubscriptionName(id uint, name string) error {
	return s.db.Model(&model.Subscription{}).
		Where("id = ?", id).
		Update("name", name).Error
}

// UpdateSubscriptionScope re-points a subscription's seed client and resets its
// allowed-inbounds list — used when multi-user membership changes add/remove
// inbounds for the bound user.
func (s *Store) UpdateSubscriptionScope(id, clientID uint, allowedInbounds string) error {
	return s.db.Model(&model.Subscription{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"client_id":        clientID,
			"allowed_inbounds": allowedInbounds,
		}).Error
}

// RevokeSubscription flips the revoked flag.
func (s *Store) RevokeSubscription(id uint) error {
	return s.db.Model(&model.Subscription{}).
		Where("id = ?", id).
		Update("revoked", true).Error
}

// scrubInboundFromSubscriptions removes a deleted inbound's ID from every
// subscription's allowed_inbounds list, inside the caller's transaction.
//
// Guard: the scrub never leaves an EMPTY list behind. decodeAllowedInbounds
// treats an empty filter as "all inbounds allowed" (historical semantics), so
// writing "[]" here would silently flip a curated wizard subscription into an
// all-inbounds one — including inbounds of the other address family, which is
// exactly the v6-shows-v4 class of bug this scrub exists to fix. When the
// deleted inbound is the LAST entry, the dangling ID stays in place: it
// matches nothing, the subscription serves an empty bundle, and the panel
// view flags the row as orphaned so the operator rebuilds it deliberately.
//
// Legacy tag-string rows are skipped — tags don't dangle by numeric ID.
func scrubInboundFromSubscriptions(tx *gorm.DB, inboundID uint) error {
	var subs []model.Subscription
	if err := tx.Find(&subs).Error; err != nil {
		return err
	}
	for _, sub := range subs {
		raw := sub.AllowedInbounds
		if raw == "" || raw == "null" || raw == "[]" {
			continue
		}
		var ids []uint
		if json.Unmarshal([]byte(raw), &ids) != nil {
			continue // legacy tag-string shape
		}
		next := make([]uint, 0, len(ids))
		removed := false
		for _, id := range ids {
			if id == inboundID {
				removed = true
				continue
			}
			next = append(next, id)
		}
		if !removed || len(next) == 0 {
			continue // not referenced, or last entry (keep dangling — see guard above)
		}
		newRaw, _ := json.Marshal(next)
		if err := tx.Model(&model.Subscription{}).
			Where("id = ?", sub.ID).
			Update("allowed_inbounds", string(newRaw)).Error; err != nil {
			return err
		}
	}
	return nil
}

// MigrateAllowedInboundsToIDs walks every subscription row whose
// AllowedInbounds field is the legacy tag-string JSON shape and rewrites it
// to the modern inbound_id JSON shape. Idempotent: rows already on the ID
// shape, rows with empty/`null`/`[]` lists, and rows whose tag refs no
// longer match any inbound are left alone.
//
// Why: subscriptions historically referenced inbounds by tag string. Editing
// an inbound's tag (which IS the engine identifier in sing-box.json + URI
// share) silently broke every existing subscription pointing at the old tag.
// The decoder still accepts both shapes for safety (see resolver.go), but we
// rewrite legacy rows on startup so the panel UI consistently shows the
// stable ID form.
//
// Returns the count of rows rewritten + first error encountered (if any).
// Callers log + ignore the error so a partial migration doesn't keep the
// panel down.
func (s *Store) MigrateAllowedInboundsToIDs() (rewritten int, firstErr error) {
	var subs []model.Subscription
	if err := s.db.Find(&subs).Error; err != nil {
		return 0, err
	}
	// Build a tag → id index once instead of N queries.
	var inbounds []model.Inbound
	if err := s.db.Find(&inbounds).Error; err != nil {
		return 0, err
	}
	tagToID := make(map[string]uint, len(inbounds))
	for _, in := range inbounds {
		if in.Tag != "" {
			tagToID[in.Tag] = in.ID
		}
	}
	for _, sub := range subs {
		raw := sub.AllowedInbounds
		if raw == "" || raw == "null" || raw == "[]" {
			continue
		}
		// Already the ID shape (numeric array) — skip.
		var idsProbe []uint
		if json.Unmarshal([]byte(raw), &idsProbe) == nil {
			continue
		}
		// Legacy tag-string shape: resolve each tag to id; preserve missing
		// tags by dropping them (a renamed inbound's old tag has no id, and
		// the new row keeps a clean ID list rather than an unresolvable mix).
		var tags []string
		if err := json.Unmarshal([]byte(raw), &tags); err != nil {
			// Neither shape — log + skip, don't trash unknown content.
			log.Printf("migrate allowed_inbounds: subscription id=%d malformed json, skipping (%v)", sub.ID, err)
			continue
		}
		ids := make([]uint, 0, len(tags))
		for _, tag := range tags {
			if id, ok := tagToID[tag]; ok {
				ids = append(ids, id)
			}
		}
		newRaw, _ := json.Marshal(ids)
		if err := s.db.Model(&model.Subscription{}).
			Where("id = ?", sub.ID).
			Update("allowed_inbounds", string(newRaw)).Error; err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		rewritten++
	}
	return rewritten, firstErr
}

// ListEnabledClientsByEmail returns all enabled clients that share an email,
// across enabled inbounds. Used by the subscription resolver to compose a
// multi-protocol bundle for one human user.
func (s *Store) ListEnabledClientsByEmail(email string) ([]model.Client, error) {
	if email == "" {
		return nil, errors.New("email required")
	}
	var cs []model.Client
	err := s.db.
		Where("email = ? AND enabled = ?", email, true).
		Order("id asc").
		Find(&cs).Error
	return cs, err
}
