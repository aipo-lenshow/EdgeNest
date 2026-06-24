// Package warp implements one-click Cloudflare WARP registration so operators
// don't have to run wgcf / wireguard-tools separately to obtain credentials.
//
// Flow: generate a WireGuard X25519 keypair locally → POST to Cloudflare's
// public registration endpoint with the public key + a synthetic device
// payload → parse the response into the same fields WarpConfig already stores
// (private/public keys, IPv4/IPv6 addresses, reserved bytes, endpoint).
//
// Only the free WARP tier is requested. WARP+ requires a separate license
// flow which is out of scope for v0.04.
package warp

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"

	"golang.org/x/crypto/curve25519"
)

// WGKeypair holds the base64-encoded WireGuard keys. Base64 uses the standard
// padded encoding (with `=`) because that's what every WireGuard implementation
// and the Cloudflare API expect — distinct from Reality's raw URL encoding.
type WGKeypair struct {
	PrivateKey string
	PublicKey  string
}

// GenerateKeypair returns a fresh WireGuard-style X25519 keypair. The private
// scalar is clamped per RFC 7748 §5 (which `golang.org/x/crypto/curve25519`
// already enforces internally when deriving the public key, but client tools
// like `wg pubkey` expect to see a clamped raw byte representation).
func GenerateKeypair() (WGKeypair, error) {
	var priv [32]byte
	if _, err := rand.Read(priv[:]); err != nil {
		return WGKeypair{}, fmt.Errorf("read random bytes: %w", err)
	}
	// RFC 7748 X25519 clamp.
	priv[0] &= 248
	priv[31] &= 127
	priv[31] |= 64

	pub, err := curve25519.X25519(priv[:], curve25519.Basepoint)
	if err != nil {
		return WGKeypair{}, fmt.Errorf("derive public key: %w", err)
	}

	return WGKeypair{
		PrivateKey: base64.StdEncoding.EncodeToString(priv[:]),
		PublicKey:  base64.StdEncoding.EncodeToString(pub),
	}, nil
}
