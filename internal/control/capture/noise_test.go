package capture

import "testing"

func TestIsNoiseGroup(t *testing.T) {
	noise := []DomainGroup{
		{Registrable: "apple.com", Hosts: []string{"gateway.icloud.com"}},
		{Registrable: "gstatic.com", Hosts: []string{"www.gstatic.com"}},
		{Registrable: "safebrowsing.googleapis.com", Hosts: []string{"safebrowsing.googleapis.com"}},
		{Registrable: "optimizationguide-pa.googleapis.com", Hosts: []string{"optimizationguide-pa.googleapis.com"}},
		{Registrable: "quicwg.org", Hosts: []string{"quicwg.org"}},
		{Registrable: "ip138.com", Hosts: []string{"www.ip138.com"}},
		{Registrable: "baidu.com", Hosts: []string{"sp1.baidu.com"}},
	}
	for _, g := range noise {
		if !isNoiseGroup(g) {
			t.Errorf("%s should be noise", g.Registrable)
		}
	}
	relevant := []DomainGroup{
		{Registrable: "chatgpt.com", Hosts: []string{"chatgpt.com"}},
		{Registrable: "openai.com", Hosts: []string{"api.openai.com"}},
		{Registrable: "oaistatic.com", Hosts: []string{"cdn.oaistatic.com"}},
		{Registrable: "oauth2.googleapis.com", Hosts: []string{"oauth2.googleapis.com"}},
		{Registrable: "netflix.com", Hosts: []string{"www.netflix.com"}},
		{Registrable: "nflxvideo.net", Hosts: []string{"ipv4-c001.nflxvideo.net"}},
	}
	for _, g := range relevant {
		if isNoiseGroup(g) {
			t.Errorf("%s should NOT be noise", g.Registrable)
		}
	}
}
