package alerts

import (
	"strings"
	"testing"
	"time"

	"github.com/aipo-lenshow/EdgeNest/internal/control/model"
)

func TestDetectorFailingRenewal(t *testing.T) {
	st := openStore(t)
	day := func(n int) int64 { return testNow.Add(time.Duration(n) * 24 * time.Hour).Unix() }
	for _, c := range []model.Certificate{
		{Domain: "failing.example.com", ExpiresAt: day(20), AutoRenew: true, LastError: "port 80 is in use by caddy"},
		{Domain: "healthy.example.com", ExpiresAt: day(20), AutoRenew: true},
		{Domain: "manual.example.com", ExpiresAt: day(20), AutoRenew: false, LastError: "x"},
		{Domain: "far.example.com", ExpiresAt: day(60), AutoRenew: true, LastError: "x"},
		{Domain: "soon.example.com", ExpiresAt: day(5), AutoRenew: true, LastError: "x"},
	} {
		c := c
		c.NodeID = 1
		auto := c.AutoRenew
		if err := st.DB().Create(&c).Error; err != nil {
			t.Fatal(err)
		}
		// The column defaults to true: GORM skips a zero-value false on create
		// (and writes the default back into c).
		if !auto {
			st.DB().Model(&c).Update("auto_renew", false)
		}
	}
	got := NewDetector(st, "1").Attention(testNow, Default())
	var renew, cert []string
	for _, a := range got {
		switch a.Kind {
		case KindCertRenew:
			renew = append(renew, a.Target)
		case KindCert:
			cert = append(cert, a.Target)
		}
	}
	if strings.Join(renew, ",") != "failing.example.com" {
		t.Errorf("renewal-failure alerts = %v, want only failing.example.com", renew)
	}
	// Inside the expiry window the plain expiry alert covers it; no double line.
	if strings.Join(cert, ",") != "soon.example.com" {
		t.Errorf("expiry alerts = %v, want only soon.example.com", cert)
	}
	line := Line(Alert{Kind: KindCertRenew, Target: "failing.example.com", Days: 20}, "zh")
	if !strings.Contains(line, "自动续签失败") || !strings.Contains(line, "20 天后到期") {
		t.Errorf("unexpected line: %q", line)
	}
}
