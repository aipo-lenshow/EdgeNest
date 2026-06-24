package warp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGenerateKeypair_ClampedAndPaired(t *testing.T) {
	kp, err := GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair: %v", err)
	}
	priv, err := base64.StdEncoding.DecodeString(kp.PrivateKey)
	if err != nil {
		t.Fatalf("decode private: %v", err)
	}
	if len(priv) != 32 {
		t.Fatalf("private key length = %d, want 32", len(priv))
	}
	// X25519 clamp invariants: bottom 3 bits of byte[0] clear, top 2 bits of
	// byte[31] are 0b01...
	if priv[0]&0b111 != 0 {
		t.Errorf("private key byte[0] not clamped: %08b", priv[0])
	}
	if priv[31]&0b1100_0000 != 0b0100_0000 {
		t.Errorf("private key byte[31] not clamped: %08b", priv[31])
	}
	pub, err := base64.StdEncoding.DecodeString(kp.PublicKey)
	if err != nil {
		t.Fatalf("decode public: %v", err)
	}
	if len(pub) != 32 {
		t.Fatalf("public key length = %d, want 32", len(pub))
	}
}

func TestRegister_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			t.Errorf("Content-Type = %q", ct)
		}
		var body regRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if body.Key == "" {
			t.Error("request missing wg public key")
		}
		// Fake Cloudflare response: 3 reserved bytes encoded as base64.
		resp := regResponse{}
		resp.Config.ClientID = base64.StdEncoding.EncodeToString([]byte{0x10, 0x20, 0x30})
		resp.Config.Interface.Addresses.V4 = "172.16.0.99"
		resp.Config.Interface.Addresses.V6 = "2606:4700:110:abcd:ef01:2345:6789:abcd"
		resp.Config.Peers = append(resp.Config.Peers, struct {
			PublicKey string `json:"public_key"`
			Endpoint  struct {
				Host string `json:"host"`
				V4   string `json:"v4"`
				V6   string `json:"v6"`
			} `json:"endpoint"`
		}{PublicKey: "PEER_PUB_BASE64_xxx="})
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := NewClient().WithEndpoint(srv.URL)
	res, err := c.Register(context.Background())
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if res.PrivateKey == "" {
		t.Error("Result.PrivateKey empty — keypair must be returned")
	}
	if res.PublicKey != "PEER_PUB_BASE64_xxx=" {
		t.Errorf("Result.PublicKey = %q", res.PublicKey)
	}
	if res.Address4 != "172.16.0.99/32" {
		t.Errorf("Result.Address4 = %q, want 172.16.0.99/32", res.Address4)
	}
	if res.Address6 != "2606:4700:110:abcd:ef01:2345:6789:abcd/128" {
		t.Errorf("Result.Address6 = %q", res.Address6)
	}
	if len(res.Reserved) != 3 || res.Reserved[0] != 0x10 || res.Reserved[1] != 0x20 || res.Reserved[2] != 0x30 {
		t.Errorf("Result.Reserved = %v, want [16 32 48]", res.Reserved)
	}
	if res.Endpoint != Endpoint {
		t.Errorf("Result.Endpoint = %q, want %q", res.Endpoint, Endpoint)
	}
}

func TestRegister_4xxSurfacesError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"rate_limited"}`))
	}))
	defer srv.Close()

	c := NewClient().WithEndpoint(srv.URL)
	_, err := c.Register(context.Background())
	if err == nil {
		t.Fatal("expected error on 429")
	}
	if !strings.Contains(err.Error(), "429") {
		t.Errorf("error missing status code: %v", err)
	}
}

func TestRegister_RejectsEmptyPeers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"config":{"client_id":"","peers":[],"interface":{"addresses":{"v4":"1.2.3.4","v6":""}}}}`))
	}))
	defer srv.Close()
	_, err := NewClient().WithEndpoint(srv.URL).Register(context.Background())
	if err == nil || !strings.Contains(err.Error(), "missing peers") {
		t.Errorf("expected missing-peers error, got %v", err)
	}
}
