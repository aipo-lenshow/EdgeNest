package warpprobe

import (
	"encoding/base64"
	"testing"

	"github.com/aipo-lenshow/EdgeNest/internal/control/model"
)

func TestParseReserved(t *testing.T) {
	got, err := parseReserved("[1,2,3]")
	if err != nil || got != [3]byte{1, 2, 3} {
		t.Fatalf("got %v err %v", got, err)
	}
	// Empty is tolerated as zero reserved.
	if z, err := parseReserved(""); err != nil || z != [3]byte{} {
		t.Fatalf("empty: got %v err %v", z, err)
	}
	if _, err := parseReserved("not json"); err == nil {
		t.Error("expected error on malformed reserved")
	}
}

func TestKeyToHex(t *testing.T) {
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = byte(i)
	}
	b64 := base64.StdEncoding.EncodeToString(raw)
	h, err := keyToHex(b64)
	if err != nil {
		t.Fatal(err)
	}
	if len(h) != 64 {
		t.Errorf("want 64 hex chars, got %d", len(h))
	}
	// Wrong length key is rejected.
	if _, err := keyToHex(base64.StdEncoding.EncodeToString([]byte("short"))); err == nil {
		t.Error("expected error on short key")
	}
}

func TestLocalAddrs(t *testing.T) {
	addrs, err := localAddrs(&model.WarpConfig{
		Address4: "172.16.0.2/32",
		Address6: "2606:4700:110:abcd::2/128",
	})
	if err != nil || len(addrs) != 2 {
		t.Fatalf("got %v err %v", addrs, err)
	}
	if addrs[0].String() != "172.16.0.2" {
		t.Errorf("v4 not stripped of mask: %s", addrs[0])
	}
	// No address at all is an error.
	if _, err := localAddrs(&model.WarpConfig{}); err == nil {
		t.Error("expected error when no address assigned")
	}
}

func TestOpen_RejectsUnconfigured(t *testing.T) {
	if _, err := Open(nil); err == nil {
		t.Error("nil config should error")
	}
	if _, err := Open(&model.WarpConfig{}); err == nil {
		t.Error("empty config should error")
	}
}
