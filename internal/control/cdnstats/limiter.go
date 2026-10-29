package cdnstats

import (
	"sync"
	"time"

	"github.com/aipo-lenshow/EdgeNest/internal/control/store"
)

// autoGap is the least time between two automatic rounds pooled from the same
// subscription on the same network: clients check every 30 minutes at most,
// so anything denser is a loop gone wrong, not information.
const autoGap = 10 * time.Minute

// Limiter decides which rounds are pooled. State lives in memory only, keyed by
// a hash of the subscription token and the network, and is gone on restart —
// it exists to stop one device flooding the pool, not to record anyone.
type Limiter struct {
	mu    sync.Mutex
	now   func() time.Time
	state map[string]*limitState
}

type limitState struct {
	lastAuto     time.Time
	lastManual   time.Time
	lastWinner   string
	sameRounds   int
	coolingUntil time.Time
	touched      time.Time
}

// NewLimiter returns an empty limiter.
func NewLimiter() *Limiter {
	return &Limiter{now: time.Now, state: map[string]*limitState{}}
}

// Verdict is the limiter's answer for one round.
type Verdict struct {
	Accept       bool
	Reason       string // "" | too_soon | cooling
	CoolingUntil time.Time
}

// Check decides whether a cleaned round from subKey on its network is pooled,
// and records it when it is. A manual round inside the cooldown is still taken
// when the round itself shows the address in use is no longer the best — the
// user then has a real reason to look.
func (l *Limiter) Check(subKey string, r Report, cleaned []store.CDNSample, p Policy) Verdict {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.sweep(now)
	key := subKey + "\x00" + r.NetKey
	st := l.state[key]
	if st == nil {
		st = &limitState{}
		l.state[key] = st
	}
	st.touched = now
	if !r.Manual {
		if now.Sub(st.lastAuto) < autoGap {
			return Verdict{Reason: "too_soon"}
		}
		st.lastAuto = now
		return Verdict{Accept: true}
	}
	if now.Sub(st.lastManual) < time.Duration(p.IntervalSec)*time.Second {
		return Verdict{Reason: "too_soon"}
	}
	if now.Before(st.coolingUntil) && !currentNotBest(r.CurrentIP, cleaned) {
		return Verdict{Reason: "cooling", CoolingUntil: st.coolingUntil}
	}
	st.lastManual = now
	winner := fastest(cleaned)
	if winner != "" && winner == st.lastWinner {
		st.sameRounds++
	} else {
		st.lastWinner, st.sameRounds = winner, 1
	}
	v := Verdict{Accept: true}
	if st.sameRounds >= p.SameRounds && p.CooldownSec > 0 {
		st.coolingUntil = now.Add(time.Duration(p.CooldownSec) * time.Second)
		st.sameRounds = 0
		v.CoolingUntil = st.coolingUntil
	}
	return v
}

// sweep forgets state nobody has touched for a day, so the map stays small.
func (l *Limiter) sweep(now time.Time) {
	for k, st := range l.state {
		if now.Sub(st.touched) > 24*time.Hour && now.After(st.coolingUntil) {
			delete(l.state, k)
		}
	}
}

func fastest(s []store.CDNSample) string {
	best, ms := "", int64(0)
	for _, x := range s {
		if x.OK && (best == "" || x.Ms < ms) {
			best, ms = x.IP, x.Ms
		}
	}
	return best
}

// currentNotBest: the address in use failed this round, or is slower than the
// fastest by both 30% and 150ms — the same bar the client uses to suggest a
// switch.
func currentNotBest(current string, s []store.CDNSample) bool {
	if current == "" {
		return false
	}
	var cur *store.CDNSample
	for i := range s {
		if s[i].IP == current {
			cur = &s[i]
		}
	}
	if cur == nil {
		return false
	}
	if !cur.OK {
		return true
	}
	best := cur.Ms
	for _, x := range s {
		if x.OK && x.Ms < best {
			best = x.Ms
		}
	}
	return cur.Ms-best >= 150 && float64(cur.Ms) >= 1.3*float64(best)
}
