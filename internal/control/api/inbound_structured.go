package api

// Structured inbound payload — the contract the panel UI uses so end users
// never type Reality private keys, Shadowsocks PSKs or sing-box field names by
// hand. Each protocol has a small whitelist of fields the user can *tune*; all
// other fields (cryptographic secrets, engine-specific knobs, TLS cert paths)
// are filled in by [autofillInboundSettings].
//
// Flow:
//   1. UI submits {type, advanced: {…protocol-specific user fields…}}.
//   2. [BuildInboundSettings] copies whitelisted fields from advanced into a
//      fresh settings map, drops the rest, then runs autofill for defaults.
//   3. The fully-populated settings map is what hits the DB / engine.
//
// Reverse path (edit form):
//   1. UI fetches existing inbound → [ParseInboundAdvanced] reads the stored
//      settings, strips secrets, and returns the same `advanced` map shape so
//      the form can prefill cleanly.

import (
	"encoding/json"
	"errors"
)

// ErrUnparseableSettings is returned when ParseInboundAdvanced can't decode
// the stored settings JSON. The UI falls back to a raw textarea + warning.
var ErrUnparseableSettings = errors.New("settings json is not valid")

// advancedFieldsByType lists the per-protocol field whitelist the panel UI
// is allowed to set via the structured form. Anything outside this list must
// either come through the legacy `settings` path (?raw=1 / API-only) or be
// generated server-side by autofill.
var advancedFieldsByType = map[string][]string{
	"vless": {
		// Only SNI and server_port_target are operator choices; private/
		// public keys, short_ids and flow are autofilled.
		"sni", "server_port_target",
	},
	"vless-xhttp": {
		"security", "sni", "xhttp_path", "xhttp_host",
		// cdn_mode + argo_bound are meaningful only on TLS-mode XHTTP
		// (Reality cannot be CDN-fronted nor tunneled). UI hides both
		// when security=reality.
		"cdn_mode", "argo_bound",
	},
	"vless-ws": {
		"ws_path", "ws_host", "cdn_mode", "argo_bound",
	},
	"vmess": {
		"ws_path", "ws_host", "cdn_mode", "argo_bound",
	},
	"vmess-ws": {
		"ws_path", "ws_host", "cdn_mode", "argo_bound",
	},
	"hysteria2": {
		// obfs is a boolean toggle at the UI layer; autofill turns it into
		// (obfs, obfs_password) when on.
		"obfs", "up_mbps", "down_mbps", "sni",
	},
	"trojan": {
		"sni", "acme_managed",
	},
	"shadowsocks": {
		"method",
	},
	"tuic": {
		"congestion_control", "sni", "acme_managed",
	},
	"anytls": {
		"sni", "acme_managed",
	},
	"socks": {
		"require_auth", "username",
	},
}

// secretSettingsKeys are fields the engine needs but the UI must never see —
// both [ParseInboundAdvanced] and the existing API scrubber drop them on the
// way out.
var secretSettingsKeys = []string{
	"reality_private_key",
	"obfs_password",
	"password",
}

// BuildInboundSettings turns a structured `advanced` payload from the panel
// form into the full settings map the engine renders against. Unknown
// protocols pass advanced through verbatim — caller (handler) controls
// whether that is allowed.
//
// The autofill step is invariant-load-bearing: it generates Reality keypairs,
// Shadowsocks PSKs and Hysteria2 obfs passwords for protocols that need them,
// and seeds wizard self-signed certs for TLS-bearing protocols.
func BuildInboundSettings(typ string, advanced map[string]any) (map[string]any, error) {
	settings := map[string]any{}
	allowed, knownProto := advancedFieldsByType[typ]
	if knownProto {
		for _, key := range allowed {
			if v, ok := advanced[key]; ok && v != nil {
				settings[key] = v
			}
		}
	} else {
		// Unknown protocol — pass advanced through so the API stays usable
		// for operators experimenting with sing-box / xray types we haven't
		// modelled yet. Autofill will still run and no-op.
		for k, v := range advanced {
			settings[k] = v
		}
	}
	return autofillInboundSettings(typ, settings, nil)
}

// ApplyAdvancedUpdate merges a structured `advanced` patch onto an existing
// inbound's settings. Secrets carried in `existing` are preserved (the UI
// never sends them back); whitelisted user fields are replaced by the new
// values, then autofill runs to seed any newly required defaults (e.g. when
// the operator just flipped vless-xhttp from reality→tls and we now need to
// drop reality fields).
func ApplyAdvancedUpdate(typ string, advanced map[string]any, existing map[string]any) (map[string]any, error) {
	if existing == nil {
		existing = map[string]any{}
	}
	merged := map[string]any{}
	for k, v := range existing {
		merged[k] = v
	}
	allowed, knownProto := advancedFieldsByType[typ]
	if knownProto {
		for _, key := range allowed {
			if v, ok := advanced[key]; ok && v != nil {
				merged[key] = v
			}
		}
	} else {
		for k, v := range advanced {
			merged[k] = v
		}
	}
	return autofillInboundSettings(typ, merged, existing)
}

// ParseInboundAdvanced is the reverse direction: given the JSON the engine
// has on disk, return the trimmed `advanced` map the structured edit form
// should prefill. Secret keys are stripped; unrecognised keys for known
// protocols are dropped (no point showing them in the form if the form
// can't write them back). Unknown protocols return whatever non-secret keys
// exist so a manual operator can still see the shape.
func ParseInboundAdvanced(typ, rawSettings string) (map[string]any, error) {
	if rawSettings == "" {
		return map[string]any{}, nil
	}
	var s map[string]any
	if err := json.Unmarshal([]byte(rawSettings), &s); err != nil {
		return nil, ErrUnparseableSettings
	}
	for _, k := range secretSettingsKeys {
		delete(s, k)
	}
	allowed, knownProto := advancedFieldsByType[typ]
	if !knownProto {
		return s, nil
	}
	out := map[string]any{}
	allowSet := map[string]struct{}{}
	for _, k := range allowed {
		allowSet[k] = struct{}{}
	}
	for k, v := range s {
		if _, ok := allowSet[k]; ok {
			out[k] = v
		}
	}
	// Hysteria2 stores obfs as the string "salamander" (engine name) but the
	// structured form represents it as a bool toggle. Normalize on the way out.
	if typ == "hysteria2" {
		switch v := out["obfs"].(type) {
		case string:
			out["obfs"] = v != "" && v != "false" && v != "off"
		case bool:
			// already correct
			_ = v
		}
	}
	return out, nil
}
