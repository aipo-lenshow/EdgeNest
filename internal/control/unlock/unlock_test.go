package unlock

import (
	"context"
	"errors"
	"testing"
)

// stubProber lets us drive probeOne without real HTTP — each URL maps to a
// (status, body, err) tuple.
type stubProber struct {
	resp map[string]struct {
		status int
		body   string
		err    error
	}
}

func (s *stubProber) Probe(_ context.Context, url string) (int, string, error) {
	r, ok := s.resp[url]
	if !ok {
		return 0, "", errors.New("no stub for " + url)
	}
	return r.status, r.body, r.err
}

// TestRun_ClassifiesStates covers the matrix probeOne uses:
//   - 200 with OK marker → unlocked
//   - 200 with block marker → blocked
//   - 200 without markers → unlocked
//   - 401 → unlocked (auth required, region OK)
//   - 403 → blocked
//   - 451 → blocked
//   - network error → error
//   - timeout error → timeout
func TestRun_ClassifiesStates(t *testing.T) {
	targets := []Target{
		{ID: "ok_marker", URL: "https://a", OKSub: "loc="},
		{ID: "block_marker", URL: "https://b", BlockSub: "Not Available"},
		{ID: "plain_200", URL: "https://c"},
		{ID: "auth", URL: "https://d"},
		{ID: "forbidden", URL: "https://e"},
		{ID: "geo_blocked", URL: "https://f"},
		{ID: "tcp_error", URL: "https://g"},
		{ID: "deadline", URL: "https://h"},
	}
	stub := &stubProber{resp: map[string]struct {
		status int
		body   string
		err    error
	}{
		"https://a": {status: 200, body: "loc=US\n"},
		"https://b": {status: 200, body: "<title>Not Available</title>"},
		"https://c": {status: 200, body: "ok"},
		"https://d": {status: 401, body: ""},
		"https://e": {status: 403, body: ""},
		"https://f": {status: 451, body: ""},
		"https://g": {err: errors.New("dial tcp: connection refused")},
		"https://h": {err: context.DeadlineExceeded},
	}}
	res := Run(context.Background(), stub, targets)
	want := map[string]string{
		"ok_marker":    "unlocked",
		"block_marker": "blocked",
		"plain_200":    "unlocked",
		"auth":         "unlocked",
		"forbidden":    "blocked",
		"geo_blocked":  "blocked",
		"tcp_error":    "error",
		"deadline":     "timeout",
	}
	for _, s := range res {
		if s.State != want[s.ID] {
			t.Errorf("%s: state = %q, want %q (detail=%q)", s.ID, s.State, want[s.ID], s.Detail)
		}
	}
}

// TestRun_PreservesOrder verifies output order matches input even though
// probes run concurrently.
func TestRun_PreservesOrder(t *testing.T) {
	targets := []Target{
		{ID: "first", URL: "https://1"},
		{ID: "second", URL: "https://2"},
		{ID: "third", URL: "https://3"},
	}
	stub := &stubProber{resp: map[string]struct {
		status int
		body   string
		err    error
	}{
		"https://1": {status: 200},
		"https://2": {status: 200},
		"https://3": {status: 200},
	}}
	res := Run(context.Background(), stub, targets)
	if len(res) != 3 {
		t.Fatalf("got %d results, want 3", len(res))
	}
	if res[0].ID != "first" || res[1].ID != "second" || res[2].ID != "third" {
		t.Errorf("order broken: %+v", []string{res[0].ID, res[1].ID, res[2].ID})
	}
}

// TestRun_BlockMarkerWinsOver200 guards a subtle case: streaming sites
// often return HTTP 200 with a "Not Available in your region" body, so a
// pure status-code check would incorrectly label them unlocked.
func TestRun_BlockMarkerWinsOver200(t *testing.T) {
	targets := []Target{
		{ID: "netflix-mock", URL: "https://x", BlockSub: "Not Available"},
	}
	stub := &stubProber{resp: map[string]struct {
		status int
		body   string
		err    error
	}{
		"https://x": {status: 200, body: "...Not Available..."},
	}}
	res := Run(context.Background(), stub, targets)
	if res[0].State != "blocked" {
		t.Errorf("200 + block marker should be blocked, got %q", res[0].State)
	}
	if res[0].HTTPStatus != 200 {
		t.Errorf("HTTPStatus should be preserved (200), got %d", res[0].HTTPStatus)
	}
}
