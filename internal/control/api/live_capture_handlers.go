package api

import (
	"context"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/aipo-lenshow/EdgeNest/internal/control/capture"
	"github.com/aipo-lenshow/EdgeNest/internal/control/clashapi"
	"github.com/aipo-lenshow/EdgeNest/internal/control/orchestrator"
	"github.com/aipo-lenshow/EdgeNest/internal/core"
)

// Live domain capture: while the operator uses a service through their own
// client (the only way to reach login/playback-gated domains a headless visit
// can't), the panel polls sing-box's clash_api for every active connection's
// sniffed host and accumulates them. This is the ground-truth capture: the
// domains that a real, successful session actually touched.

const livePollInterval = 1500 * time.Millisecond

// connStat is the last-seen state of one connection (by clash id). We key by id
// and keep the latest cumulative byte counters so summing per host doesn't
// double-count across polls; closed connections retain their final value.
type connStat struct {
	host   string
	source string
	bytes  int64
}

// liveSession is the single in-flight capture (one at a time, mirrors biJob).
type liveSession struct {
	mu      sync.Mutex
	running bool
	startTS time.Time
	cancel  context.CancelFunc
	conns   map[string]connStat // accumulated by connection id (for domains+bytes)
	// curSources is the source IPs seen in the MOST RECENT poll (currently
	// connected devices), so the filter list reflects reality — a device that
	// disconnects drops off, rather than lingering from history.
	curSources map[string]int // source IP -> distinct hosts in the last poll
	pollErr    string
}

var live = &liveSession{}

func (s *liveSession) record(conns []clashapi.Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := map[string]map[string]bool{}
	for _, c := range conns {
		if c.Host == "" { // IP-only / DNS lookups carry no domain — skip as noise
			continue
		}
		key := c.ID
		if key == "" {
			key = c.Host + "|" + c.DestIP + "|" + c.DestPort
		}
		s.conns[key] = connStat{host: c.Host, source: c.SourceIP, bytes: c.Upload + c.Download}
		if c.SourceIP != "" {
			if cur[c.SourceIP] == nil {
				cur[c.SourceIP] = map[string]bool{}
			}
			cur[c.SourceIP][c.Host] = true
		}
	}
	s.curSources = map[string]int{}
	for ip, hs := range cur {
		s.curSources[ip] = len(hs)
	}
}

// snapshot returns grouped domains (optionally filtered to one source IP) ranked
// by traffic volume, plus the distinct sources seen so the UI can offer a "my
// device only" filter. Volume = the "you actually used this" signal.
func (s *liveSession) snapshot(source string) (running bool, elapsed int, groups []capture.DomainGroup, sources []gin.H, pollErr string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	hostBytes := map[string]int64{}
	hostSeen := map[string]bool{}
	for _, st := range s.conns {
		if source != "" && st.source != source {
			continue
		}
		hostBytes[st.host] += st.bytes
		hostSeen[st.host] = true
	}

	hs := make([]string, 0, len(hostSeen))
	for h := range hostSeen {
		hs = append(hs, h)
	}
	groups = capture.GroupHosts(hs)
	for i := range groups {
		var b int64
		for _, h := range groups[i].Hosts {
			b += hostBytes[h]
		}
		groups[i].Bytes = b
	}
	// Rank by traffic so the domains the session actually used float to the top;
	// alphabetical tiebreak keeps it stable.
	sort.SliceStable(groups, func(i, j int) bool {
		if groups[i].Bytes != groups[j].Bytes {
			return groups[i].Bytes > groups[j].Bytes
		}
		return groups[i].Registrable < groups[j].Registrable
	})

	ips := make([]string, 0, len(s.curSources))
	for ip := range s.curSources {
		ips = append(ips, ip)
	}
	sort.Slice(ips, func(i, j int) bool { return s.curSources[ips[i]] > s.curSources[ips[j]] })
	for _, ip := range ips {
		sources = append(sources, gin.H{"ip": ip, "count": s.curSources[ip]})
	}

	el := 0
	if s.running {
		el = int(time.Since(s.startTS).Seconds())
	}
	return s.running, el, groups, sources, s.pollErr
}

// StartLiveCapture begins polling clash_api and accumulating sniffed hosts.
//
// POST /api/v1/routes/capture/live/start
func (h *Handler) StartLiveCapture(c *gin.Context) {
	secret, err := h.orch.ClashSecret()
	if err != nil {
		core.Fail(c, http.StatusInternalServerError, "NO_SECRET", err.Error())
		return
	}
	client := clashapi.New(orchestrator.ClashController, secret)

	live.mu.Lock()
	if live.running {
		live.mu.Unlock()
		core.Fail(c, http.StatusConflict, "ALREADY_RUNNING", "a live capture is already running")
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	live.running = true
	live.startTS = time.Now()
	live.cancel = cancel
	live.conns = map[string]connStat{}
	live.curSources = map[string]int{}
	live.pollErr = ""
	live.mu.Unlock()

	h.auditLog(c, "route.capture.live.start", "route", nil)

	go func() {
		t := time.NewTicker(livePollInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				conns, err := client.Connections(ctx)
				if err != nil {
					live.mu.Lock()
					live.pollErr = err.Error()
					live.mu.Unlock()
					continue
				}
				live.record(conns)
			}
		}
	}()

	core.OK(c, gin.H{"running": true})
}

// LiveCaptureStatus returns the live (growing) capture state. ?source=<ip>
// filters to one device.
//
// GET /api/v1/routes/capture/live/status
func (h *Handler) LiveCaptureStatus(c *gin.Context) {
	running, elapsed, groups, sources, pollErr := live.snapshot(c.Query("source"))
	core.OK(c, gin.H{
		"running": running, "elapsedSec": elapsed,
		"domains": groups, "sources": sources, "pollError": pollErr,
	})
}

// ClearLiveCapture wipes the accumulated domains without stopping, so the
// operator can start a fresh window (e.g. switch to a different service) without
// re-arming the capture.
//
// POST /api/v1/routes/capture/live/clear
func (h *Handler) ClearLiveCapture(c *gin.Context) {
	live.mu.Lock()
	live.conns = map[string]connStat{}
	live.curSources = map[string]int{}
	if live.running {
		live.startTS = time.Now()
	}
	live.mu.Unlock()
	core.OK(c, gin.H{"cleared": true})
}

// StopLiveCapture stops polling and returns the final accumulated domains.
//
// POST /api/v1/routes/capture/live/stop
func (h *Handler) StopLiveCapture(c *gin.Context) {
	live.mu.Lock()
	if live.cancel != nil {
		live.cancel()
	}
	live.running = false
	live.mu.Unlock()

	h.auditLog(c, "route.capture.live.stop", "route", nil)
	_, elapsed, groups, sources, pollErr := live.snapshot(c.Query("source"))
	core.OK(c, gin.H{
		"running": false, "elapsedSec": elapsed,
		"domains": groups, "sources": sources, "pollError": pollErr,
	})
}
