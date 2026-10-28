package cert

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/aipo-lenshow/EdgeNest/internal/control/portowner"
)

func TestValidDomain(t *testing.T) {
	for _, d := range []string{"s1.example.com", "S1.Example.COM", "a.b.c.example.co.uk", "*.example.com", "xn--fiqs8s.xn--p1ai"} {
		if !ValidDomain(d) {
			t.Errorf("%q should be valid", d)
		}
	}
	for _, d := range []string{"", "s1", "1.2.3.4", "https://s1.example.com", "s1.example.com/", "-a.example.com", "s1..example.com", "example.com."} {
		if ValidDomain(d) {
			t.Errorf("%q should be rejected", d)
		}
	}
}

func TestIssue_RejectsBareLabelWithoutCallingCA(t *testing.T) {
	iss := &fakeIssuer{}
	mgr, _, nodeID := newMgr(t, iss)
	if _, err := mgr.Issue(context.Background(), nodeID, IssueRequest{Domain: "s1", Email: "ops@example.com"}); err == nil {
		t.Fatal("bare label accepted")
	}
	if iss.calls != 0 {
		t.Fatalf("CA contacted %d times for an invalid domain", iss.calls)
	}
}

func TestIssue_Port80BusyFailsFastAndRecords(t *testing.T) {
	iss := &fakeIssuer{}
	mgr, st, nodeID := newMgr(t, iss)
	mgr.portOwner = func(p int) *portowner.Owner { return &portowner.Owner{Port: p, Process: "caddy"} }
	_, err := mgr.Issue(context.Background(), nodeID, IssueRequest{Domain: "s1.example.com", Email: "ops@example.com"})
	if !errors.Is(err, ErrPort80Busy) {
		t.Fatalf("want ErrPort80Busy, got %v", err)
	}
	if iss.calls != 0 {
		t.Fatal("CA contacted although :80 is taken")
	}
	cs, _ := st.ListCertificates(nodeID)
	if len(cs) != 1 || cs[0].LastError == "" {
		t.Fatalf("failure not recorded for the certs page: %+v", cs)
	}
	// DNS validation and a non-default HTTP port are unaffected.
	if _, err := mgr.Issue(context.Background(), nodeID, IssueRequest{Domain: "s2.example.com", Email: "ops@example.com", Mode: "dns-01"}); err != nil {
		t.Fatalf("dns-01 blocked by :80 check: %v", err)
	}
	if _, err := mgr.Issue(context.Background(), nodeID, IssueRequest{Domain: "s3.example.com", Email: "ops@example.com", HTTPPort: 8080}); err != nil {
		t.Fatalf("custom HTTP port blocked by :80 check: %v", err)
	}
}

type reloadSpy struct {
	mu    sync.Mutex
	nodes []uint
}

func (r *reloadSpy) hook(id uint) { r.mu.Lock(); r.nodes = append(r.nodes, id); r.mu.Unlock() }
func (r *reloadSpy) count() int   { r.mu.Lock(); defer r.mu.Unlock(); return len(r.nodes) }

func waitCount(t *testing.T, r *reloadSpy, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && r.count() < want {
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond) // catch any extra call
	if got := r.count(); got != want {
		t.Fatalf("reload hook ran %d times, want %d", got, want)
	}
}

func TestReload_OnlyWhenFilesReplaced(t *testing.T) {
	mgr, _, nodeID := newMgr(t, &fakeIssuer{})
	spy := &reloadSpy{}
	mgr.OnReplaced(spy.hook)
	row, err := mgr.Issue(context.Background(), nodeID, IssueRequest{Domain: "s1.example.com", Email: "ops@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	waitCount(t, spy, 0) // a brand-new certificate isn't in use yet
	if _, err := mgr.Renew(context.Background(), row.ID); err != nil {
		t.Fatal(err)
	}
	waitCount(t, spy, 1)
	if spy.nodes[0] != nodeID {
		t.Fatalf("reloaded node %d, want %d", spy.nodes[0], nodeID)
	}
}

func TestReload_OncePerRenewalPass(t *testing.T) {
	iss := &fakeIssuer{notAfter: time.Now().Add(5 * 24 * time.Hour)}
	mgr, _, nodeID := newMgr(t, iss)
	for _, d := range []string{"a.example.com", "b.example.com"} {
		if _, err := mgr.Issue(context.Background(), nodeID, IssueRequest{Domain: d, Email: "ops@example.com"}); err != nil {
			t.Fatal(err)
		}
	}
	spy := &reloadSpy{}
	mgr.OnReplaced(spy.hook)
	if n := mgr.CheckAndRenew(context.Background()); n != 2 {
		t.Fatalf("renewed %d, want 2", n)
	}
	waitCount(t, spy, 1)
}
