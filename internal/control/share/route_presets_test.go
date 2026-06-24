package share

import "testing"

func TestRoutePresets_WellFormed(t *testing.T) {
	if len(RoutePresets) == 0 {
		t.Fatal("no presets defined")
	}
	for _, p := range RoutePresets {
		if p.Key == "" || p.Name == "" || len(p.Domains) == 0 {
			t.Errorf("malformed preset: %+v", p)
		}
		seen := map[string]bool{}
		for _, d := range p.Domains {
			if seen[d] {
				t.Errorf("preset %q has duplicate domain %q", p.Key, d)
			}
			seen[d] = true
		}
	}
}

func TestRoutePresetByKey(t *testing.T) {
	if RoutePresetByKey("ai") == nil {
		t.Error("ai preset should exist")
	}
	if RoutePresetByKey("nope") != nil {
		t.Error("unknown key should return nil")
	}
}

func TestInferSource(t *testing.T) {
	cases := []struct {
		ruleType, value, want string
	}{
		{"domain_suffix", "openai.com", "ai"},
		{"domain_suffix", "netflix.com", "streaming"},
		{"domain_suffix", "example.com", SourceCustom},
		{"domain", "openai.com", SourceCustom}, // only domain_suffix is preset-shaped
		{"ip_cidr", "10.0.0.0/8", SourceCustom},
	}
	for _, c := range cases {
		if got := InferSource(c.ruleType, c.value); got != c.want {
			t.Errorf("InferSource(%q,%q)=%q want %q", c.ruleType, c.value, got, c.want)
		}
	}
}
