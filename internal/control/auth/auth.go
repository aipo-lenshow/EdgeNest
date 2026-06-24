// Package auth provides JWT issuing/verification, bcrypt password hashing and
// secure random helpers for first-run provisioning.
package auth

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

// Claims is the JWT payload for an authenticated admin session.
type Claims struct {
	Username string `json:"username"`
	jwt.RegisteredClaims
}

// HashPassword returns a bcrypt hash of the password.
func HashPassword(password string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// CheckPassword reports whether password matches the bcrypt hash.
func CheckPassword(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

// IssueToken creates a signed JWT for username valid for ttl.
func IssueToken(secret, username string, ttl time.Duration) (string, error) {
	now := time.Now()
	claims := Claims{
		Username: username,
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
		},
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return tok.SignedString([]byte(secret))
}

// ParseToken verifies a JWT and returns its claims.
func ParseToken(secret, tokenStr string) (*Claims, error) {
	claims := &Claims{}
	tok, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("unexpected signing method")
		}
		return []byte(secret), nil
	})
	if err != nil {
		return nil, err
	}
	if !tok.Valid {
		return nil, errors.New("invalid token")
	}
	return claims, nil
}

// RandomHex returns n random bytes as a hex string (length 2n).
func RandomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// RandomPanelPath returns a random URL-safe panel path like "/ENPanel-ab12cd34".
func RandomPanelPath() (string, error) {
	s, err := RandomHex(4)
	if err != nil {
		return "", err
	}
	return "/ENPanel-" + s, nil
}

// GenerateRealityKeypair returns a fresh X25519 keypair encoded as base64-url
// (no padding) — the format sing-box / xray expect for Reality.
func GenerateRealityKeypair() (priv, pub string, err error) {
	curve := ecdh.X25519()
	k, err := curve.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	enc := base64.RawURLEncoding.EncodeToString
	return enc(k.Bytes()), enc(k.PublicKey().Bytes()), nil
}

// RandomBase64 returns n random bytes base64-std-encoded. Used to mint
// Shadowsocks-2022 PSKs (16 bytes for aes-128, 32 for aes-256 / chacha20).
func RandomBase64(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b), nil
}
