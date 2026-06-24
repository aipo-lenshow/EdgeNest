package cert

// Curated DNS-01 provider registry. Arbitrary providers are intentionally not
// supported (the full lego aggregator quadruples the binary); operators whose
// host isn't listed use http-01 instead.
//
// Each field maps a friendly config key (what the API/UI speaks, and what we
// persist as a setting "dns_<provider>_<key>" so renewal can reload it) to the
// lego environment variable the provider's constructor reads. Env var names are
// verified against lego v4.35.2 provider sources.

// DNSField is one credential input for a provider.
type DNSField struct {
	Key    string `json:"key"`    // friendly key (config + setting + UI)
	Env    string `json:"env"`    // lego env var it maps to
	Secret bool   `json:"secret"` // render as a masked input in the UI
	Multi  bool   `json:"multi"`  // render as a multi-line textarea (e.g. JSON)
}

// DNSProviderSpec is a curated provider plus the credentials it needs.
type DNSProviderSpec struct {
	Name   string     `json:"name"` // lego provider name
	Fields []DNSField `json:"fields"`
}

// dnsRegistry holds the curated providers keyed by lego name.
var dnsRegistry = map[string]DNSProviderSpec{
	"cloudflare": {Name: "cloudflare", Fields: []DNSField{
		{Key: "api_token", Env: "CLOUDFLARE_DNS_API_TOKEN", Secret: true},
	}},
	"route53": {Name: "route53", Fields: []DNSField{
		{Key: "access_key_id", Env: "AWS_ACCESS_KEY_ID"},
		{Key: "secret_access_key", Env: "AWS_SECRET_ACCESS_KEY", Secret: true},
		{Key: "region", Env: "AWS_REGION"},
	}},
	"gcloud": {Name: "gcloud", Fields: []DNSField{
		{Key: "project", Env: "GCE_PROJECT"},
		{Key: "service_account", Env: "GCE_SERVICE_ACCOUNT", Secret: true, Multi: true},
	}},
	"azuredns": {Name: "azuredns", Fields: []DNSField{
		{Key: "tenant_id", Env: "AZURE_TENANT_ID"},
		{Key: "client_id", Env: "AZURE_CLIENT_ID"},
		{Key: "client_secret", Env: "AZURE_CLIENT_SECRET", Secret: true},
		{Key: "subscription_id", Env: "AZURE_SUBSCRIPTION_ID"},
		{Key: "resource_group", Env: "AZURE_RESOURCE_GROUP"},
	}},
	"alidns": {Name: "alidns", Fields: []DNSField{
		{Key: "access_key", Env: "ALICLOUD_ACCESS_KEY"},
		{Key: "secret_key", Env: "ALICLOUD_SECRET_KEY", Secret: true},
	}},
	"tencentcloud": {Name: "tencentcloud", Fields: []DNSField{
		{Key: "secret_id", Env: "TENCENTCLOUD_SECRET_ID"},
		{Key: "secret_key", Env: "TENCENTCLOUD_SECRET_KEY", Secret: true},
	}},
	"huaweicloud": {Name: "huaweicloud", Fields: []DNSField{
		{Key: "access_key_id", Env: "HUAWEICLOUD_ACCESS_KEY_ID"},
		{Key: "secret_access_key", Env: "HUAWEICLOUD_SECRET_ACCESS_KEY", Secret: true},
		{Key: "region", Env: "HUAWEICLOUD_REGION"},
	}},
	"digitalocean": {Name: "digitalocean", Fields: []DNSField{
		{Key: "auth_token", Env: "DO_AUTH_TOKEN", Secret: true},
	}},
	"godaddy": {Name: "godaddy", Fields: []DNSField{
		{Key: "api_key", Env: "GODADDY_API_KEY"},
		{Key: "api_secret", Env: "GODADDY_API_SECRET", Secret: true},
	}},
	"namecheap": {Name: "namecheap", Fields: []DNSField{
		{Key: "api_user", Env: "NAMECHEAP_API_USER"},
		{Key: "api_key", Env: "NAMECHEAP_API_KEY", Secret: true},
	}},
	"namesilo": {Name: "namesilo", Fields: []DNSField{
		{Key: "api_key", Env: "NAMESILO_API_KEY", Secret: true},
	}},
	"gandiv5": {Name: "gandiv5", Fields: []DNSField{
		{Key: "personal_access_token", Env: "GANDIV5_PERSONAL_ACCESS_TOKEN", Secret: true},
	}},
	"porkbun": {Name: "porkbun", Fields: []DNSField{
		{Key: "api_key", Env: "PORKBUN_API_KEY"},
		{Key: "secret_api_key", Env: "PORKBUN_SECRET_API_KEY", Secret: true},
	}},
}

// dnsProviderOrder fixes the UI display order (maps don't iterate stably):
// Cloudflare first (most common), then the big clouds, then the regional
// clouds, then popular registrars.
var dnsProviderOrder = []string{
	"cloudflare", "route53", "gcloud", "azuredns",
	"alidns", "tencentcloud", "huaweicloud",
	"digitalocean", "godaddy", "namecheap", "namesilo", "gandiv5", "porkbun",
}

// DNSProviders returns the curated specs in display order, for the API to hand
// the front-end (which adds localized labels/hints/tutorials per provider+key).
func DNSProviders() []DNSProviderSpec {
	out := make([]DNSProviderSpec, 0, len(dnsProviderOrder))
	for _, name := range dnsProviderOrder {
		out = append(out, dnsRegistry[name])
	}
	return out
}
