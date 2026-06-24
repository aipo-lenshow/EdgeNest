package share

// RoutePreset is a named bundle of domain suffixes the operator can route
// through a chosen outbound (direct / warp / block) with one click, instead of
// hand-entering each rule. Keeps the "minimise user input" promise: pick a
// category, pick where it goes, done.
//
// These are curated domain_suffix bundles — a hand-picked "geosite subset" —
// rather than engine geosite rules: sing-box 1.12+ removed the legacy `geosite`
// rule field, so emitting one would make the whole config invalid. Domain
// suffixes are version-independent and always valid.
type RoutePreset struct {
	Key     string   `json:"key"`
	Name    string   `json:"name"`    // i18n-friendly label key suffix; UI maps to a string
	Domains []string `json:"domains"` // domain_suffix values
	// Recommend is the outbound the UI pre-selects for this category: "warp"
	// for geo-restricted services, "direct" for things best left local, "block"
	// for ads/trackers. The operator can always override before applying.
	Recommend string `json:"recommend"`
}

// RoutePresets is the curated catalogue. Suffixes are deliberately broad (whole
// registrable domain) so subdomains and CDN hosts ride the same outbound. The
// list is opinionated but practical — operators can always add their own rules
// on the Routes page.
var RoutePresets = []RoutePreset{
	{
		Key:       "ai",
		Name:      "AI services",
		Recommend: "warp",
		Domains: []string{
			"openai.com",
			"chatgpt.com",
			"oaistatic.com",
			"oaiusercontent.com",
			"anthropic.com",
			"claude.ai",
			"gemini.google.com",
			"generativelanguage.googleapis.com",
			"aistudio.google.com",
			"ai.google.dev",
			"x.ai",
			"perplexity.ai",
			"poe.com",
			"midjourney.com",
		},
	},
	{
		Key:       "streaming",
		Name:      "Streaming media",
		Recommend: "warp",
		Domains: []string{
			"netflix.com",
			"nflxvideo.net",
			"nflximg.net",
			"disneyplus.com",
			"disney-plus.net",
			"dssott.com",
			"max.com",
			"hbomax.com",
			"primevideo.com",
			"hulu.com",
			"spotify.com",
			"scdn.co",
		},
	},
	{
		Key:       "google",
		Name:      "Google",
		Recommend: "warp",
		Domains: []string{
			"google.com",
			"googleapis.com",
			"gstatic.com",
			"googleusercontent.com",
			"ggpht.com",
			"googlevideo.com",
			"youtube.com",
			"youtu.be",
			"ytimg.com",
			"withgoogle.com",
		},
	},
	{
		Key:       "social",
		Name:      "Social networks",
		Recommend: "warp",
		Domains: []string{
			"x.com",
			"twitter.com",
			"twimg.com",
			"t.co",
			"facebook.com",
			"fbcdn.net",
			"instagram.com",
			"cdninstagram.com",
			"whatsapp.com",
			"telegram.org",
			"t.me",
			"tiktok.com",
			"reddit.com",
			"redd.it",
		},
	},
	{
		Key:       "dev",
		Name:      "Developer tools",
		Recommend: "warp",
		Domains: []string{
			"github.com",
			"githubusercontent.com",
			"githubassets.com",
			"ghcr.io",
			"docker.com",
			"docker.io",
			"npmjs.org",
			"pypi.org",
			"pythonhosted.org",
			"gitlab.com",
		},
	},
	{
		Key:       "cn",
		Name:      "China direct",
		Recommend: "direct",
		Domains: []string{
			"baidu.com",
			"qq.com",
			"taobao.com",
			"tmall.com",
			"jd.com",
			"bilibili.com",
			"weibo.com",
			"163.com",
			"alipay.com",
			"alicdn.com",
			"aliyun.com",
			"bdstatic.com",
		},
	},
	{
		Key:       "ads",
		Name:      "Ads & trackers",
		Recommend: "block",
		Domains: []string{
			"doubleclick.net",
			"googlesyndication.com",
			"googleadservices.com",
			"google-analytics.com",
			"googletagmanager.com",
			"googletagservices.com",
			"adnxs.com",
			"adsrvr.org",
			"scorecardresearch.com",
			"criteo.com",
			"taboola.com",
			"outbrain.com",
		},
	},
}

// RoutePresetByKey returns the preset with the given key, or nil.
func RoutePresetByKey(key string) *RoutePreset {
	for i := range RoutePresets {
		if RoutePresets[i].Key == key {
			return &RoutePresets[i]
		}
	}
	return nil
}

// SourceCustom labels a hand-entered routing rule (not from any preset).
const SourceCustom = "custom"

// SourceCaptured labels a rule created from the domain-capture flow (visit a
// site, route every domain it touched). Kept distinct from custom so the Routes
// page can filter captured rules separately.
const SourceCaptured = "captured"

// InferSource guesses a rule's source from preset membership. Used to backfill
// legacy rows that predate RouteRule.Source: a domain_suffix matching a preset's
// domain list is tagged with that preset's key, everything else is "custom".
// New rules set Source explicitly, so this only ever runs on old data.
func InferSource(ruleType, value string) string {
	if ruleType == "domain_suffix" {
		for i := range RoutePresets {
			for _, d := range RoutePresets[i].Domains {
				if d == value {
					return RoutePresets[i].Key
				}
			}
		}
	}
	return SourceCustom
}
