package argo

import "os/exec"

// goBuild constructs the `go build -o out src` command used by the fake
// cloudflared helper in supervisor_test.go. Lives in its own file so the
// helper has access to *exec.Cmd without dragging os/exec into the main
// supervisor_test.go (keeping the test file focused on the supervisor
// scenarios themselves).
func goBuild(src, out string) *exec.Cmd {
	return exec.Command("go", "build", "-o", out, src)
}
