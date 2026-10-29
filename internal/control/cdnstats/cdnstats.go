// Package cdnstats pools what EdgeNest clients measure when they probe
// Cloudflare addresses from their own network, so a client joining a network
// others have already measured starts on an address that works there instead
// of probing from scratch.
//
// What is kept is deliberately thin: a network label the client derives from
// its own egress (country, carrier, region — never the address itself), and
// per address how often it answered and how fast. Nothing identifies the
// reporter: the subscription token only authorises a report and drives the
// in-memory rate limit, and is never written down.
package cdnstats

import (
	"net"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/aipo-lenshow/EdgeNest/internal/control/store"
)

// Sample is one address's result in a probe round.
type Sample struct {
	IP string `json:"ip"`
	OK bool   `json:"ok"`
	Ms int64  `json:"ms"`
}

// Report is a client's probe round.
type Report struct {
	NetKey    string   `json:"net"`        // e.g. "CN|联通|北方", "HK|AS4760"
	NetLabel  string   `json:"net_label"`  // display name, e.g. "联通·北方"
	Link      string   `json:"link"`       // wifi | cell | other
	ControlMs int64    `json:"control_ms"` // latency to a non-Cloudflare reference; <=0 = failed
	Manual    bool     `json:"manual"`     // the user pressed "re-optimise"
	CurrentIP string   `json:"current_ip"` // address the CDN nodes dialled before this round
	Results   []Sample `json:"results"`
}

const (
	maxNetKeyLen   = 64
	maxNetLabelLen = 40
	maxResults     = 40
	maxMs          = 5000
	// controlSlowMs: a reference this slow means the network itself is
	// struggling, and every address would look bad for reasons of its own.
	controlSlowMs = 1500
)

// ValidNetKey accepts "CC|part" or "CC|part|part": a two-letter upper-case
// country code, then one or two short printable parts.
func ValidNetKey(k string) bool {
	if k == "" || len(k) > maxNetKeyLen || !utf8.ValidString(k) {
		return false
	}
	parts := strings.Split(k, "|")
	if len(parts) < 2 || len(parts) > 3 || len(parts[0]) != 2 {
		return false
	}
	for _, r := range parts[0] {
		if r < 'A' || r > 'Z' {
			return false
		}
	}
	for _, p := range parts[1:] {
		if p == "" {
			return false
		}
		for _, r := range p {
			if !unicode.IsPrint(r) {
				return false
			}
		}
	}
	return true
}

// Rejection reasons; empty means accepted.
const (
	RejectBadNet      = "bad_net"
	RejectEmpty       = "empty"
	RejectNetworkPoor = "network_poor"
	RejectMostlyFail  = "mostly_failed"
)

// Clean checks a round and keeps only the samples worth pooling: addresses in
// the candidate set the server handed out, plausible latencies. A round taken
// while the network itself is poor — the reference failed or crawled, or most
// addresses failed — says nothing about the addresses and is dropped whole.
func Clean(r Report, candidates []string) ([]store.CDNSample, string) {
	if !ValidNetKey(r.NetKey) {
		return nil, RejectBadNet
	}
	if r.ControlMs <= 0 || r.ControlMs > controlSlowMs {
		return nil, RejectNetworkPoor
	}
	allowed := make(map[string]bool, len(candidates))
	for _, ip := range candidates {
		allowed[ip] = true
	}
	seen := map[string]bool{}
	var out []store.CDNSample
	failed := 0
	for i, s := range r.Results {
		if i == maxResults {
			break
		}
		ip := strings.TrimSpace(s.IP)
		if !allowed[ip] || seen[ip] || net.ParseIP(ip).To4() == nil {
			continue
		}
		seen[ip] = true
		ok := s.OK && s.Ms > 0 && s.Ms <= maxMs
		ms := int64(0)
		if ok {
			ms = s.Ms
		} else {
			failed++
		}
		out = append(out, store.CDNSample{IP: ip, OK: ok, Ms: ms})
	}
	if len(out) == 0 {
		return nil, RejectEmpty
	}
	if failed*2 > len(out) {
		return nil, RejectMostlyFail
	}
	return out, ""
}

// Label trims a client-supplied network label to something safe to show.
func Label(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsPrint(r) {
			return r
		}
		return -1
	}, strings.TrimSpace(s))
	if utf8.RuneCountInString(s) > maxNetLabelLen {
		s = string([]rune(s)[:maxNetLabelLen])
	}
	return s
}

// Day is the bucket a report at t lands in.
func Day(t time.Time) string { return t.UTC().Format("2006-01-02") }

// Window is how far back recommendations and the panel look.
const Window = 7 * 24 * time.Hour

// Recommendation rules: an address must have been tried enough times on a
// network, by enough rounds, and answer nearly always before it is ranked
// there — so a single unlucky round can neither crown nor sink an address.
const (
	minSamples = 3
	minOKRate  = 0.8
)

// AddrStat is one address on one network over the window.
type AddrStat struct {
	IP      string  `json:"ip"`
	Samples int64   `json:"samples"`
	OKRate  float64 `json:"ok_rate"`
	AvgMs   int64   `json:"avg_ms"` // over the answers; 0 when none
	Ranked  bool    `json:"ranked"` // meets the recommendation rules
}

// Network is one network's view.
type Network struct {
	Key       string     `json:"key"`
	Label     string     `json:"label"`
	Samples   int64      `json:"samples"`
	UpdatedAt int64      `json:"updated_at"`
	CDNDown   bool       `json:"cdn_down"` // enough tries, nothing answers
	Addrs     []AddrStat `json:"addrs"`    // ranked first (fastest first), then the rest
}

// Summarise turns window totals into per-network views, busiest first.
func Summarise(rows []store.CDNStatTotal) []Network {
	by := map[string]*Network{}
	var order []string
	for _, r := range rows {
		n := by[r.NetKey]
		if n == nil {
			n = &Network{Key: r.NetKey}
			by[r.NetKey] = n
			order = append(order, r.NetKey)
		}
		if r.NetLabel != "" {
			n.Label = r.NetLabel
		}
		n.Samples += r.Samples
		if r.UpdatedAt > n.UpdatedAt {
			n.UpdatedAt = r.UpdatedAt
		}
		a := AddrStat{IP: r.IP, Samples: r.Samples}
		if r.Samples > 0 {
			a.OKRate = float64(r.OK) / float64(r.Samples)
		}
		if r.OK > 0 {
			a.AvgMs = r.MsSum / r.OK
		}
		a.Ranked = r.Samples >= minSamples && a.OKRate >= minOKRate
		n.Addrs = append(n.Addrs, a)
	}
	out := make([]Network, 0, len(order))
	for _, k := range order {
		n := by[k]
		sort.SliceStable(n.Addrs, func(i, j int) bool {
			a, b := n.Addrs[i], n.Addrs[j]
			if a.Ranked != b.Ranked {
				return a.Ranked
			}
			if !a.Ranked && a.OKRate != b.OKRate {
				return a.OKRate > b.OKRate
			}
			// Answered addresses fastest first; ones that never answered last.
			if (a.AvgMs > 0) != (b.AvgMs > 0) {
				return a.AvgMs > 0
			}
			return a.AvgMs < b.AvgMs
		})
		answered := false
		for _, a := range n.Addrs {
			if a.OKRate > 0 {
				answered = true
				break
			}
		}
		n.CDNDown = !answered && n.Samples >= minSamples*3
		out = append(out, *n)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Samples > out[j].Samples })
	return out
}

// Recommended is the ranked addresses for a network, fastest first.
func (n Network) Recommended(limit int) []string {
	var out []string
	for _, a := range n.Addrs {
		if !a.Ranked || len(out) == limit {
			break
		}
		out = append(out, a.IP)
	}
	return out
}

// Order puts a network's recommended addresses at the head of the candidate
// list, keeping the rest in the order the server already chose.
func Order(candidates, recommended []string) []string {
	in := make(map[string]bool, len(candidates))
	for _, ip := range candidates {
		in[ip] = true
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(candidates))
	for _, ip := range recommended {
		if in[ip] && !seen[ip] {
			seen[ip] = true
			out = append(out, ip)
		}
	}
	for _, ip := range candidates {
		if !seen[ip] {
			seen[ip] = true
			out = append(out, ip)
		}
	}
	return out
}

// Policy limits how often a user may run a manual round.
type Policy struct {
	IntervalSec int `json:"interval_sec"` // shortest gap between manual rounds
	SameRounds  int `json:"same_rounds"`  // consecutive rounds with the same winner before a cooldown
	CooldownSec int `json:"cooldown_sec"`
}

// DefaultPolicy is what a server starts with.
var DefaultPolicy = Policy{IntervalSec: 120, SameRounds: 3, CooldownSec: 6 * 3600}

// Setting keys.
const (
	keyInterval = "cdn_manual_interval_sec"
	keySame     = "cdn_manual_same_rounds"
	keyCooldown = "cdn_manual_cooldown_sec"
)

// Valid reports whether every field is within bounds.
func (p Policy) Valid() bool {
	return p.IntervalSec >= 30 && p.IntervalSec <= 86400 &&
		p.SameRounds >= 1 && p.SameRounds <= 20 &&
		p.CooldownSec >= 0 && p.CooldownSec <= 7*86400
}

// Header is the policy as the subscription response carries it.
func (p Policy) Header() string {
	return "interval=" + strconv.Itoa(p.IntervalSec) +
		";same=" + strconv.Itoa(p.SameRounds) +
		";cooldown=" + strconv.Itoa(p.CooldownSec)
}

type settings interface {
	GetSetting(key string) (string, error)
	SetSetting(key, value string) error
}

// LoadPolicy reads the stored policy, falling back field by field.
func LoadPolicy(st settings) Policy {
	p := DefaultPolicy
	read := func(key string, dst *int) {
		if v, err := st.GetSetting(key); err == nil && v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				*dst = n
			}
		}
	}
	read(keyInterval, &p.IntervalSec)
	read(keySame, &p.SameRounds)
	read(keyCooldown, &p.CooldownSec)
	if !p.Valid() {
		return DefaultPolicy
	}
	return p
}

// SavePolicy stores a validated policy.
func SavePolicy(st settings, p Policy) error {
	for k, v := range map[string]int{keyInterval: p.IntervalSec, keySame: p.SameRounds, keyCooldown: p.CooldownSec} {
		if err := st.SetSetting(k, strconv.Itoa(v)); err != nil {
			return err
		}
	}
	return nil
}
