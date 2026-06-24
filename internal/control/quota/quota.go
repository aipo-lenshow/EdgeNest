// Package quota enforces per-client traffic quotas and expiry deadlines.
//
// The enforcement loop is intentionally pure: take a snapshot of clients,
// decide which ones should be disabled now, return the decisions. The caller
// is responsible for persisting Enabled=false back and triggering Apply.
// This split makes the policy unit-testable without DB/HTTP wiring.
package quota

import (
	"fmt"
	"time"

	"github.com/aipo-lenshow/EdgeNest/internal/control/model"
)

// Reason describes why a client was flagged for disabling.
type Reason string

const (
	ReasonQuotaExceeded Reason = "quota_exceeded"
	ReasonExpired       Reason = "expired"
)

// Decision is one client → "disable for reason X".
type Decision struct {
	ClientID uint
	Email    string
	Reason   Reason
	Detail   string
}

// Evaluate returns the disable decisions for the given clients at time now.
// A client is flagged when:
//   - Enabled is currently true AND
//   - QuotaBytes > 0 AND TrafficUp+TrafficDown >= QuotaBytes  (quota), OR
//   - ExpiryAt > 0 AND now.Unix() >= ExpiryAt                  (expiry)
//
// Already-disabled clients are skipped so the enforcement loop doesn't
// re-log them on every tick.
func Evaluate(clients []model.Client, now time.Time) []Decision {
	var out []Decision
	n := now.Unix()
	for _, c := range clients {
		if !c.Enabled {
			continue
		}
		if c.QuotaBytes > 0 && (c.TrafficUp+c.TrafficDown) >= c.QuotaBytes {
			out = append(out, Decision{
				ClientID: c.ID, Email: c.Email,
				Reason: ReasonQuotaExceeded,
				Detail: bytesDetail(c.TrafficUp+c.TrafficDown, c.QuotaBytes),
			})
			continue
		}
		if c.ExpiryAt > 0 && n >= c.ExpiryAt {
			out = append(out, Decision{
				ClientID: c.ID, Email: c.Email,
				Reason: ReasonExpired,
				Detail: expiryDetail(c.ExpiryAt, n),
			})
		}
	}
	return out
}

// EvaluateByUser is the user-centric counterpart to Evaluate. It aggregates
// clients by Email (the share resolver's user identity) and flags EVERY client
// of a user whose summed traffic crosses the user's quota, or whose expiry has
// passed. Quota/expiry are taken as the max non-zero value across the user's
// clients — the multi-user create/update flow writes them uniformly, and the
// max ignores a stray 0 row added before a cap was set.
//
// Traffic is stored on one representative client per email (store.AddUserTraffic),
// so summing TrafficUp+Down across a user's clients yields the correct total.
//
// A user already fully disabled (no enabled clients) is skipped so the loop
// doesn't re-log it every tick.
func EvaluateByUser(clients []model.Client, now time.Time) []Decision {
	n := now.Unix()
	type agg struct {
		used       int64
		quota      int64
		expiry     int64
		anyEnabled bool
		members    []model.Client
	}
	byEmail := map[string]*agg{}
	var order []string
	for _, c := range clients {
		a, ok := byEmail[c.Email]
		if !ok {
			a = &agg{}
			byEmail[c.Email] = a
			order = append(order, c.Email)
		}
		a.used += c.TrafficUp + c.TrafficDown
		if c.QuotaBytes > a.quota {
			a.quota = c.QuotaBytes
		}
		if c.ExpiryAt > a.expiry {
			a.expiry = c.ExpiryAt
		}
		if c.Enabled {
			a.anyEnabled = true
		}
		a.members = append(a.members, c)
	}

	var out []Decision
	for _, email := range order {
		a := byEmail[email]
		if !a.anyEnabled {
			continue
		}
		var reason Reason
		var detail string
		switch {
		case a.quota > 0 && a.used >= a.quota:
			reason, detail = ReasonQuotaExceeded, bytesDetail(a.used, a.quota)
		case a.expiry > 0 && n >= a.expiry:
			reason, detail = ReasonExpired, expiryDetail(a.expiry, n)
		default:
			continue
		}
		for _, c := range a.members {
			if !c.Enabled {
				continue
			}
			out = append(out, Decision{
				ClientID: c.ID, Email: c.Email, Reason: reason, Detail: detail,
			})
		}
	}
	return out
}

func bytesDetail(used, limit int64) string {
	return formatBytes(used) + " used / " + formatBytes(limit) + " quota"
}

func expiryDetail(expiry, now int64) string {
	return "expired at " + time.Unix(expiry, 0).UTC().Format(time.RFC3339) +
		" (now " + time.Unix(now, 0).UTC().Format(time.RFC3339) + ")"
}

func formatBytes(b int64) string {
	const (
		KiB = 1024
		MiB = 1024 * KiB
		GiB = 1024 * MiB
	)
	switch {
	case b >= GiB:
		return fmt.Sprintf("%.2f GiB", float64(b)/float64(GiB))
	case b >= MiB:
		return fmt.Sprintf("%.2f MiB", float64(b)/float64(MiB))
	case b >= KiB:
		return fmt.Sprintf("%.2f KiB", float64(b)/float64(KiB))
	default:
		return fmt.Sprintf("%d B", b)
	}
}
