package api

import (
	"encoding/json"
	"strings"
)

// certSelectable are the protocols that terminate TLS with a certificate file
// on the server, so the operator can switch them between the self-signed pair
// and any certificate issued on the certificates page.
var certSelectable = map[string]bool{"hysteria2": true, "tuic": true, "trojan": true, "anytls": true}

// keepCertPrefix marks the current certificate when it isn't one the panel
// manages (a file set by hand, or one whose certificate row was deleted):
// the form shows it as the current choice and saving it changes nothing.
const keepCertPrefix = "keep:"

// inboundCertDomain reports which certificate an inbound uses: "" for the
// self-signed pair, the domain of an issued certificate whose files it points
// at, keepCertPrefix+path for any other file, or ok=false when it has none.
func (h *Handler) inboundCertDomain(nodeID uint, rawSettings string) (string, bool) {
	var s map[string]any
	if json.Unmarshal([]byte(rawSettings), &s) != nil {
		return "", false
	}
	path, _ := s["tls_cert_path"].(string)
	if path == "" || path == defaultWizardCertPath {
		return "", true
	}
	certs, err := h.store.ListCertificates(nodeID)
	if err != nil {
		return "", false
	}
	for _, c := range certs {
		if c.CertPath == path {
			return c.Domain, true
		}
	}
	return keepCertPrefix + path, true
}

// applyCertDomain points settings at the chosen certificate: "" = the
// self-signed pair (clients skip verification), otherwise an issued
// certificate for domain, which also becomes the SNI clients send. Returns a
// user-facing message when no usable certificate exists for domain.
func (h *Handler) applyCertDomain(nodeID uint, settings map[string]any, domain string) string {
	domain = strings.TrimSpace(domain)
	if strings.HasPrefix(domain, keepCertPrefix) {
		return ""
	}
	if domain == "" {
		settings["tls_cert_path"] = defaultWizardCertPath
		settings["tls_key_path"] = defaultWizardKeyPath
		settings["self_signed"] = "true"
		delete(settings, "acme_managed")
		return ""
	}
	certs, err := h.store.ListCertificates(nodeID)
	if err == nil {
		for _, c := range certs {
			if strings.EqualFold(c.Domain, domain) && c.CertPath != "" && c.KeyPath != "" {
				settings["tls_cert_path"] = c.CertPath
				settings["tls_key_path"] = c.KeyPath
				settings["sni"] = c.Domain
				settings["acme_managed"] = "true"
				delete(settings, "self_signed")
				return ""
			}
		}
	}
	return "没有找到 " + domain + " 的证书，请先在「证书」页签发。"
}
