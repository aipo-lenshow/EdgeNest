package portowner

import (
	"os"
	"path/filepath"
	"testing"
)

// fakeProc lays out the slice of /proc that Find reads: a LISTEN socket on
// :443 (0x01BB) in net/tcp6 owned by pid 4242 named comm.
func fakeProc(t *testing.T, comm string) string {
	t.Helper()
	root := t.TempDir()
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(root, "net"), 0o755))
	hdr := "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n"
	must(os.WriteFile(filepath.Join(root, "net/tcp"), []byte(hdr+
		"   0: 00000000:0050 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 111 1\n"), 0o644))
	must(os.WriteFile(filepath.Join(root, "net/tcp6"), []byte(hdr+
		"   0: 00000000000000000000000000000000:01BB 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 999 1\n"), 0o644))
	must(os.MkdirAll(filepath.Join(root, "4242/fd"), 0o755))
	must(os.Symlink("socket:[999]", filepath.Join(root, "4242/fd/7")))
	must(os.WriteFile(filepath.Join(root, "4242/comm"), []byte(comm+"\n"), 0o644))
	return root
}

func TestFind(t *testing.T) {
	o := find(fakeProc(t, "caddy"), 443)
	if o == nil || o.Process != "caddy" || o.Tool != ToolCaddy {
		t.Fatalf("got %+v", o)
	}
	if find(fakeProc(t, "caddy"), 8443) != nil {
		t.Fatal("free port reported as taken")
	}
	// :80 listens (inode 111) but no process holds it: owner unknown.
	if o := find(fakeProc(t, "caddy"), 80); o == nil || o.Tool != ToolOther || o.Process != "" {
		t.Fatalf("unknown owner: %+v", o)
	}
}

func TestClassify(t *testing.T) {
	for name, want := range map[string]string{
		"caddy": ToolCaddy, "nginx": ToolNginx, "openresty": ToolNginx, "lucky": ToolLucky,
		"httpd": ToolApache, "apache2": ToolApache, "xray": ToolOther, "": ToolOther,
	} {
		if got := Classify(name); got != want {
			t.Errorf("%q: got %s want %s", name, got, want)
		}
	}
}
