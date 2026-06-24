package argo

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"testing"
)

// TestBinaryManager_DownloadAndVerify exercises the full download path against
// an in-process server that serves a fake binary blob whose hash we inject
// into pinnedHashes for the current platform. Works on Linux (production
// target) AND macOS (dev workstation) even though darwin isn't in the
// production pin table — the test patches it in for the duration of the run.
func TestBinaryManager_DownloadAndVerify(t *testing.T) {
	key := platformKey()
	body := []byte("FAKE-CLOUDFLARED-BINARY-BLOB-" + key)
	got := sha256.Sum256(body)
	expected := hex.EncodeToString(got[:])

	// Inject (or override) the pin for the duration of this test so the
	// verifier accepts our fake body. Restored on cleanup.
	original, had := pinnedHashes[key]
	pinnedHashes[key] = expected
	t.Cleanup(func() {
		if had {
			pinnedHashes[key] = original
		} else {
			delete(pinnedHashes, key)
		}
	})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	// Swap the download URL by patching m.http to route to our test server
	// via a Transport that rewrites the request URL.
	c := &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		newURL, _ := req.URL.Parse(srv.URL)
		req.URL = newURL
		return http.DefaultTransport.RoundTrip(req)
	})}

	dir := t.TempDir()
	mgr := NewBinaryManager(dir).WithHTTPClient(c)
	path, err := mgr.Path(context.Background())
	if err != nil {
		t.Fatalf("Path: %v", err)
	}
	if !strings.HasSuffix(path, "cloudflared") {
		t.Errorf("unexpected path %q", path)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat downloaded binary: %v", err)
	}
	if info.Mode().Perm()&0o100 == 0 && runtime.GOOS != "windows" {
		t.Errorf("binary not executable: %v", info.Mode())
	}

	// Second call must hit the fast path (no re-download) and return the
	// same path.
	path2, err := mgr.Path(context.Background())
	if err != nil {
		t.Fatalf("second Path: %v", err)
	}
	if path2 != path {
		t.Errorf("second call returned different path: %q vs %q", path, path2)
	}
}

func TestBinaryManager_RejectsHashMismatch(t *testing.T) {
	key := platformKey()
	// Inject a deliberately wrong pin so the verifier has something to
	// compare against, regardless of whether the host platform happens to be
	// in the production pin table.
	original, had := pinnedHashes[key]
	pinnedHashes[key] = "deadbeef" + strings.Repeat("0", 56)
	t.Cleanup(func() {
		if had {
			pinnedHashes[key] = original
		} else {
			delete(pinnedHashes, key)
		}
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("this content does NOT match the pinned hash"))
	}))
	defer srv.Close()

	c := &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		newURL, _ := req.URL.Parse(srv.URL)
		req.URL = newURL
		return http.DefaultTransport.RoundTrip(req)
	})}

	dir := t.TempDir()
	mgr := NewBinaryManager(dir).WithHTTPClient(c)
	_, err := mgr.Path(context.Background())
	if err == nil {
		t.Fatal("expected hash mismatch error")
	}
	if !strings.Contains(err.Error(), "hash mismatch") {
		t.Errorf("unexpected error: %v", err)
	}
	// Make sure the failed download did not leave a partial cloudflared
	// behind to be picked up by the fast path next time.
	if _, err := os.Stat(dir + "/cloudflared"); err == nil {
		t.Error("partial download left a cloudflared file in place")
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
