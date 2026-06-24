package cert

import "testing"

// TestDNSProviderFor locks the dns-01 wiring: cloudflare with a token builds a
// provider, a missing token errors clearly, and unknown providers are rejected
// (rather than silently doing nothing).
func TestDNSProviderFor(t *testing.T) {
	t.Run("cloudflare with token", func(t *testing.T) {
		p, err := dnsProviderFor("cloudflare", map[string]string{"api_token": "tok-123"})
		if err != nil {
			t.Fatalf("cloudflare provider: %v", err)
		}
		if p == nil {
			t.Fatal("provider is nil")
		}
	})

	t.Run("cloudflare missing token", func(t *testing.T) {
		_, err := dnsProviderFor("cloudflare", map[string]string{})
		if err == nil {
			t.Fatal("want error when api_token is absent")
		}
	})

	t.Run("unknown provider", func(t *testing.T) {
		_, err := dnsProviderFor("aliyun", map[string]string{"x": "y"})
		if err == nil {
			t.Fatal("want error for an unwired provider")
		}
	})
}
