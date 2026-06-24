package share

import (
	"os"
	"testing"

	"github.com/aipo-lenshow/EdgeNest/internal/control/model"
)

// TestEncoderDump renders Clash / sing-box / QX / Surge / Loon outputs to
// /tmp/edgenest-encoder-dump/ for manual inspection (paste into Mihomo / Stash /
// QX import). Run with `EDGENEST_DUMP_ENCODERS=1 go test ./internal/control/share/ -run TestEncoderDump`.
func TestEncoderDump(t *testing.T) {
	if os.Getenv("EDGENEST_DUMP_ENCODERS") == "" {
		t.Skip("set EDGENEST_DUMP_ENCODERS=1 to run")
	}

	uuid := "11111111-1111-1111-1111-111111111111"
	bundles := []Bundle{
		{
			Inbound: &model.Inbound{
				Tag: "vless-reality", Type: "vless", Port: 8443, Remark: "VLESS-Reality",
				Settings: `{"reality_public_key":"3sNHsR7Br1n0iV6BfFcoxYK_8VqI-_ZHcVWxnSXxNXk","short_ids":["beef0001"],"sni":"www.microsoft.com","flow":"xtls-rprx-vision"}`,
			},
			Client: model.Client{Email: "u@x", UUID: uuid, Flow: "xtls-rprx-vision", Enabled: true},
		},
		{
			Inbound: &model.Inbound{
				Tag: "hy2", Type: "hysteria2", Port: 41020, Remark: "Hysteria2",
				Settings: `{"obfs":"salamander","obfs_password":"OBFSPW","sni":"www.bing.com","self_signed":"true"}`,
			},
			Client: model.Client{Email: "u@x", Password: "hy2pw", Enabled: true},
		},
		{
			Inbound: &model.Inbound{
				Tag: "trojan", Type: "trojan", Port: 8843, Remark: "Trojan",
				Settings: `{"sni":"www.bing.com","self_signed":"true"}`,
			},
			Client: model.Client{Email: "u@x", Password: "trojanpw", Enabled: true},
		},
		{
			Inbound: &model.Inbound{
				Tag: "ss", Type: "shadowsocks", Port: 8388, Remark: "SS-2022",
				Settings: `{"method":"2022-blake3-aes-128-gcm"}`,
			},
			// SS-2022 aes-128-gcm needs base64(16 bytes) — proper 22+padding form.
			Client: model.Client{Email: "u@x", Password: "QUFBQUJCQkJDQ0NDRERERA==", Enabled: true},
		},
		{
			Inbound: &model.Inbound{
				Tag: "tuic", Type: "tuic", Port: 50000, Remark: "TUIC",
				Settings: `{"sni":"www.bing.com","self_signed":"true","congestion_control":"bbr"}`,
			},
			Client: model.Client{Email: "u@x", UUID: uuid, Password: "tuicpw", Enabled: true},
		},
	}

	host := "1.2.3.4"
	dir := "/tmp/edgenest-encoder-dump"
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	writeOut := func(name, body string) {
		path := dir + "/" + name
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s (%d bytes)", path, len(body))
	}

	writeOut("sub.clash.yaml", EncodeClash(bundles, host))
	writeOut("sub.singbox.json", EncodeSingbox(bundles, host))
	writeOut("sub.qx.conf", EncodeQuantumultX(bundles, host))
	writeOut("sub.surge.conf", EncodeSurge(bundles, host))
	writeOut("sub.loon.conf", EncodeLoon(bundles, host))

	for _, b := range bundles {
		b.Inbound.Clients = []model.Client{b.Client}
		uris, err := BuildURIs(b.Inbound, host)
		if err != nil {
			t.Errorf("BuildURIs %s: %v", b.Inbound.Type, err)
			continue
		}
		for i, u := range uris {
			writeOut("uri."+b.Inbound.Type+"."+string(rune('0'+i))+".txt", u)
		}
	}
}
