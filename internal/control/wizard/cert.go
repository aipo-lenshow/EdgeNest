package wizard

import "github.com/aipo-lenshow/EdgeNest/internal/control/selfsigned"

// WriteSelfSignedCert is the exported entry point for callers outside the
// wizard package. Both wizard internals and bootstrap delegate to the same
// implementation in internal/control/selfsigned.
func WriteSelfSignedCert(domain, certPath, keyPath string) error {
	return selfsigned.Write(domain, certPath, keyPath)
}

// writeSelfSignedCert is the lowercase alias kept for the original wizard
// call sites inside this package.
func writeSelfSignedCert(domain, certPath, keyPath string) error {
	return selfsigned.Write(domain, certPath, keyPath)
}
