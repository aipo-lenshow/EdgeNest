package capture

import "strings"

// Live capture records every domain the device touched, which includes a lot of
// background traffic unrelated to the service the operator was exercising: OS /
// vendor push, the browser's own telemetry, analytics, connectivity probes, IP
// lookups. We don't drop these (the operator might want one) — we flag them so
// the UI can fold them into a collapsed "background" bucket and pre-select only
// the relevant ones.
//
// Curated from real captures + well-known telemetry. Deliberately conservative:
// login endpoints (oauth) and the service's own domains must NOT be in here.
var noiseSuffixes = []string{
	// OS / vendor push & services
	"apple.com",
	"icloud.com",
	"mzstatic.com",
	"push.apple.com",
	// Browser (Chrome) telemetry / infra — NOT the oauth login endpoints
	"gstatic.com",
	"clientservices.googleapis.com",
	"content-autofill.googleapis.com",
	"safebrowsing.googleapis.com",
	"update.googleapis.com",
	"play.googleapis.com",
	"clients.google.com",
	"clients2.google.com",
	"gvt1.com",
	"gvt2.com",
	// Generic analytics / crash / attribution
	"google-analytics.com",
	"googletagmanager.com",
	"doubleclick.net",
	"googlesyndication.com",
	"app-measurement.com",
	"crashlytics.com",
	"sentry.io",
	"segment.com",
	"amplitude.com",
	"branch.io",
	"adjust.com",
	"appsflyer.com",
	// Connectivity / QUIC probes
	"quicwg.org",
	"cloudflare-quic.com",
	// IP-lookup services
	"ip138.com",
	"ipshudi.com",
	"mainlandip.com",
	"rdnsdb.com",
	"ip-api.com",
	"ipify.org",
	"ipinfo.io",
	// Common embedded-SDK background
	"baidu.com",
	"bcebos.com",
}

// isNoiseHost reports whether a single host is background/telemetry noise.
func isNoiseHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	// Google's "-pa" (private API) hosts are telemetry, e.g.
	// optimizationguide-pa.googleapis.com.
	if strings.Contains(host, "-pa.googleapis.com") {
		return true
	}
	for _, s := range noiseSuffixes {
		if host == s || strings.HasSuffix(host, "."+s) {
			return true
		}
	}
	return false
}

// isNoiseGroup classifies a grouped domain as noise when its registrable matches
// the list, or when every observed host under it is noise.
func isNoiseGroup(g DomainGroup) bool {
	if isNoiseHost(g.Registrable) {
		return true
	}
	for _, h := range g.Hosts {
		if !isNoiseHost(h) {
			return false
		}
	}
	return len(g.Hosts) > 0
}
