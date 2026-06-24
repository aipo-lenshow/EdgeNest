package store

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aipo-lenshow/EdgeNest/internal/control/model"
	"gorm.io/gorm"
)

// ---- Inbound helpers ----

// CreateInbound inserts a new inbound. Caller must populate NodeID, Tag, Engine,
// Type, Port, etc. Returns an error on uniqueness violation (tag, port).
//
// The model's Enabled field intentionally has no `gorm:"default:true"` tag —
// otherwise the Go zero value would silently flip to true on insert. API
// handlers are the authoritative source for default-enabled semantics.
func (s *Store) CreateInbound(in *model.Inbound) error {
	now := time.Now().Unix()
	in.CreatedAt, in.UpdatedAt = now, now
	return s.db.Create(in).Error
}

// ListInbounds returns every inbound on a node, ordered by id.
// Clients are preloaded so callers can render in one query.
func (s *Store) ListInbounds(nodeID uint) ([]model.Inbound, error) {
	var ins []model.Inbound
	err := s.db.
		Preload("Clients").
		Where("node_id = ?", nodeID).
		Order("id asc").
		Find(&ins).Error
	return ins, err
}

// GetInbound fetches one inbound (with Clients preloaded).
func (s *Store) GetInbound(id uint) (*model.Inbound, error) {
	var in model.Inbound
	if err := s.db.Preload("Clients").First(&in, id).Error; err != nil {
		return nil, err
	}
	return &in, nil
}

// GetInboundByTag fetches one inbound by Tag.
func (s *Store) GetInboundByTag(tag string) (*model.Inbound, error) {
	var in model.Inbound
	if err := s.db.Preload("Clients").First(&in, "tag = ?", tag).Error; err != nil {
		return nil, err
	}
	return &in, nil
}

// UpdateInbound saves changes to an existing inbound.
func (s *Store) UpdateInbound(in *model.Inbound) error {
	in.UpdatedAt = time.Now().Unix()
	return s.db.Save(in).Error
}

// DeleteInbound deletes the inbound and CASCADES its clients via explicit
// delete (GORM ignores SQLite FK cascades by default). Subscriptions that
// referenced the inbound get the ID scrubbed from allowed_inbounds in the
// same transaction so they don't carry dangling references — see
// scrubInboundFromSubscriptions for the "never empty the list" guard.
func (s *Store) DeleteInbound(id uint) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("inbound_id = ?", id).Delete(&model.Client{}).Error; err != nil {
			return err
		}
		if err := scrubInboundFromSubscriptions(tx, id); err != nil {
			return err
		}
		return tx.Delete(&model.Inbound{}, id).Error
	})
}

// ---- Client helpers ----

// CreateClient inserts a client under an existing inbound. Email must be unique
// within the inbound (Invariant I1: stats key).
func (s *Store) CreateClient(c *model.Client) error {
	now := time.Now().Unix()
	c.CreatedAt, c.UpdatedAt = now, now
	if c.Email == "" {
		return errors.New("client.email is required (invariant I1)")
	}
	// Uniqueness check within the inbound (not enforced at column level so we
	// can reuse the same email on a different inbound — e.g. user "alice" has
	// VLESS + Hysteria2 inbounds).
	var n int64
	if err := s.db.Model(&model.Client{}).
		Where("inbound_id = ? AND email = ?", c.InboundID, c.Email).
		Count(&n).Error; err != nil {
		return err
	}
	if n > 0 {
		return errors.New("duplicate client email within inbound")
	}
	return s.db.Create(c).Error
}

// ListClients returns clients of an inbound, ordered by id.
func (s *Store) ListClients(inboundID uint) ([]model.Client, error) {
	var cs []model.Client
	err := s.db.Where("inbound_id = ?", inboundID).Order("id asc").Find(&cs).Error
	return cs, err
}

// ClientsByEmail returns every client (across all inbounds) sharing an email —
// i.e. all credential rows of one logical user. Empty email returns nothing.
func (s *Store) ClientsByEmail(email string) ([]model.Client, error) {
	if email == "" {
		return nil, nil
	}
	var cs []model.Client
	err := s.db.Where("email = ?", email).Order("id asc").Find(&cs).Error
	return cs, err
}

// AllClientEmails returns the distinct set of client emails across all inbounds.
func (s *Store) AllClientEmails() ([]string, error) {
	var emails []string
	err := s.db.Model(&model.Client{}).
		Distinct().Pluck("email", &emails).Error
	return emails, err
}

// NextSeqEmail returns the next sequential default user identity in the
// NNN@EdgeNest.Local form (001, 002, …), one past the highest existing
// sequence number. Non-sequence emails (custom identifiers) are ignored.
func (s *Store) NextSeqEmail() (string, error) {
	emails, err := s.AllClientEmails()
	if err != nil {
		return "", err
	}
	max := 0
	for _, e := range emails {
		var n int
		// case-insensitive domain match; only the NNN@EdgeNest.Local shape counts
		if c, _ := fmt.Sscanf(strings.ToLower(e), "%d@edgenest.local", &n); c == 1 && n > max {
			max = n
		}
	}
	return fmt.Sprintf("%03d@EdgeNest.Local", max+1), nil
}

// GetClient fetches one client by id.
func (s *Store) GetClient(id uint) (*model.Client, error) {
	var c model.Client
	if err := s.db.First(&c, id).Error; err != nil {
		return nil, err
	}
	return &c, nil
}

// UpdateClient saves changes to a client.
func (s *Store) UpdateClient(c *model.Client) error {
	c.UpdatedAt = time.Now().Unix()
	if c.Email == "" {
		return errors.New("client.email is required (invariant I1)")
	}
	return s.db.Save(c).Error
}

// SetClientEnabled toggles just the Enabled flag (and UpdatedAt). This is the
// minimum write surface for quota/expiry enforcement — we deliberately avoid
// loading + Save'ing the full row so a concurrent traffic counter update
// can't be clobbered.
func (s *Store) SetClientEnabled(id uint, enabled bool) error {
	return s.db.Model(&model.Client{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"enabled":    enabled,
			"updated_at": time.Now().Unix(),
		}).Error
}

// AddClientTraffic atomically adds up/down bytes to the client's counters.
// Used by the stats poller so two concurrent writers can't lose increments.
func (s *Store) AddClientTraffic(id uint, up, down int64) error {
	return s.db.Exec(
		`UPDATE clients SET traffic_up = traffic_up + ?, traffic_down = traffic_down + ?, updated_at = ? WHERE id = ?`,
		up, down, time.Now().Unix(), id,
	).Error
}

// AddUserTraffic adds up/down bytes to a user's running total, keyed by email.
// The bytes land on the lowest-id client of that email (the representative);
// summing a user's clients then yields the correct per-user total without
// double-counting across the user's other inbound credentials. Used by the
// traffic poller, which only knows the inbound auth user (email), not which
// specific client row a connection used. No-op (nil) if the email has no
// clients — a connection's user can disappear mid-poll after a delete.
func (s *Store) AddUserTraffic(email string, up, down int64) error {
	var c model.Client
	if err := s.db.Where("email = ?", email).Order("id asc").First(&c).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	}
	return s.AddClientTraffic(c.ID, up, down)
}

// ResetClientTraffic zeroes the counters; called when an admin resets a quota.
func (s *Store) ResetClientTraffic(id uint) error {
	return s.db.Model(&model.Client{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"traffic_up":   0,
			"traffic_down": 0,
			"updated_at":   time.Now().Unix(),
		}).Error
}

// DeleteClient removes a client by id.
func (s *Store) DeleteClient(id uint) error {
	return s.db.Delete(&model.Client{}, id).Error
}

// ---- Firewall helpers ----

// ListFirewallRules returns all firewall rules for a node, sorted by port.
func (s *Store) ListFirewallRules(nodeID uint) ([]model.FirewallRule, error) {
	var rs []model.FirewallRule
	err := s.db.Where("node_id = ?", nodeID).Order("port asc").Find(&rs).Error
	return rs, err
}

// UpsertManagedFirewallRule ensures the panel-managed allow rule for (port, proto)
// exists on this node. User-created (Managed=false) rules at the same port are
// left untouched.
func (s *Store) UpsertManagedFirewallRule(nodeID uint, port int, proto, note string) error {
	var existing model.FirewallRule
	err := s.db.
		Where("node_id = ? AND port = ? AND proto = ? AND managed = ?", nodeID, port, proto, true).
		First(&existing).Error
	if err == nil {
		// Update note if changed.
		if existing.Note != note {
			existing.Note = note
			return s.db.Save(&existing).Error
		}
		return nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	return s.db.Create(&model.FirewallRule{
		NodeID: nodeID, Port: port, Proto: proto, Note: note, Managed: true,
	}).Error
}

// DeleteManagedFirewallRule removes panel-managed rules for (port, proto)
// on a node. User-managed rules are kept.
func (s *Store) DeleteManagedFirewallRule(nodeID uint, port int, proto string) error {
	return s.db.
		Where("node_id = ? AND port = ? AND proto = ? AND managed = ?", nodeID, port, proto, true).
		Delete(&model.FirewallRule{}).Error
}

// ListRouteRules returns route rules for a node, in declared order.
func (s *Store) ListRouteRules(nodeID uint) ([]model.RouteRule, error) {
	var rs []model.RouteRule
	err := s.db.Where("node_id = ?", nodeID).Order(`"order" asc, id asc`).Find(&rs).Error
	return rs, err
}

// GetWarp returns the WARP config for a node, or nil if not configured.
func (s *Store) GetWarp(nodeID uint) (*model.WarpConfig, error) {
	var w model.WarpConfig
	if err := s.db.First(&w, "node_id = ?", nodeID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &w, nil
}

// UpsertWarp inserts or updates the per-node WARP config. UpdatedAt is set
// to wall-clock seconds.
func (s *Store) UpsertWarp(w *model.WarpConfig) error {
	w.UpdatedAt = time.Now().Unix()
	var existing model.WarpConfig
	err := s.db.First(&existing, "node_id = ?", w.NodeID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return s.db.Create(w).Error
	}
	if err != nil {
		return err
	}
	w.ID = existing.ID
	return s.db.Save(w).Error
}

// DeleteWarp removes the WARP config for a node (used when the operator
// disables WARP entirely; orchestrator skips emitting the outbound).
func (s *Store) DeleteWarp(nodeID uint) error {
	return s.db.Where("node_id = ?", nodeID).Delete(&model.WarpConfig{}).Error
}

// CreateRouteRule inserts a new routing rule. Caller sets NodeID, Type,
// Value, Outbound, Enabled, Order.
func (s *Store) CreateRouteRule(r *model.RouteRule) error {
	return s.db.Create(r).Error
}

// GetRouteRule fetches a single route by id.
func (s *Store) GetRouteRule(id uint) (*model.RouteRule, error) {
	var r model.RouteRule
	if err := s.db.First(&r, id).Error; err != nil {
		return nil, err
	}
	return &r, nil
}

// UpdateRouteRule overwrites all mutable fields of a route by id.
func (s *Store) UpdateRouteRule(r *model.RouteRule) error {
	return s.db.Save(r).Error
}

// DeleteRouteRule removes a routing rule by id.
func (s *Store) DeleteRouteRule(id uint) error {
	return s.db.Delete(&model.RouteRule{}, id).Error
}

// BulkDeleteRouteRules removes many rules in one transaction, scoped to nodeID
// so a stray foreign id can't delete another node's rule.
func (s *Store) BulkDeleteRouteRules(nodeID uint, ids []uint) error {
	return s.db.Where("node_id = ? AND id IN ?", nodeID, ids).
		Delete(&model.RouteRule{}).Error
}

// BulkSetRouteEnabled flips Enabled on many rules at once, scoped to nodeID.
func (s *Store) BulkSetRouteEnabled(nodeID uint, ids []uint, enabled bool) error {
	return s.db.Model(&model.RouteRule{}).
		Where("node_id = ? AND id IN ?", nodeID, ids).
		Update("enabled", enabled).Error
}

// ReorderRouteRules sets Order on each (id, order) pair within nodeID.
// IDs not belonging to nodeID are silently skipped (defensive).
func (s *Store) ReorderRouteRules(nodeID uint, idsInOrder []uint) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		for i, id := range idsInOrder {
			if err := tx.Model(&model.RouteRule{}).
				Where("id = ? AND node_id = ?", id, nodeID).
				Update("order", i).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// ListCertificates returns certs on a node.
func (s *Store) ListCertificates(nodeID uint) ([]model.Certificate, error) {
	var cs []model.Certificate
	err := s.db.Where("node_id = ?", nodeID).Order("id asc").Find(&cs).Error
	return cs, err
}

// SetCertAutoRenew flips a cert's auto-renew flag. The renewal scheduler only
// touches certs with auto_renew=true, so turning it off lets an operator pause
// background renewal (e.g. to avoid burning ACME rate limits while testing, or
// for a cert they manage by hand). Returns gorm.ErrRecordNotFound if no row.
func (s *Store) SetCertAutoRenew(id uint, autoRenew bool) (*model.Certificate, error) {
	var c model.Certificate
	if err := s.db.First(&c, id).Error; err != nil {
		return nil, err
	}
	c.AutoRenew = autoRenew
	c.UpdatedAt = time.Now().Unix()
	if err := s.db.Save(&c).Error; err != nil {
		return nil, err
	}
	return &c, nil
}

// GetAdvanced returns the advanced opt-in config for a node, or nil if absent.
func (s *Store) GetAdvanced(nodeID uint) (*model.AdvancedConfig, error) {
	var a model.AdvancedConfig
	if err := s.db.First(&a, "node_id = ?", nodeID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &a, nil
}

// UpsertAdvanced creates-or-updates the per-node advanced config.
// a.NodeID is required.
func (s *Store) UpsertAdvanced(a *model.AdvancedConfig) error {
	if a.NodeID == 0 {
		return errors.New("advanced.node_id required")
	}
	var existing model.AdvancedConfig
	err := s.db.Where("node_id = ?", a.NodeID).First(&existing).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return s.db.Create(a).Error
	}
	if err != nil {
		return err
	}
	a.ID = existing.ID
	return s.db.Save(a).Error
}

// DeleteAdvanced removes the per-node advanced row (turns everything off).
func (s *Store) DeleteAdvanced(nodeID uint) error {
	return s.db.Where("node_id = ?", nodeID).Delete(&model.AdvancedConfig{}).Error
}
