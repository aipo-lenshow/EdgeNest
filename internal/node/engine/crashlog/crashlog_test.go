package crashlog

import (
	"strings"
	"testing"

	"github.com/aipo-lenshow/EdgeNest/internal/logredact"
)

func TestReason(t *testing.T) {
	cases := []struct {
		name, out, want string
	}{
		{
			name: "sing-box bind failure",
			out: "+0000 2026-10-30 18:11:31 \x1b[36mINFO\x1b[0m inbound/vless[x]: tcp server started\n" +
				"\x1b[31mFATAL\x1b[0m[0000] start service: start inbound: listen tcp 203.0.113.7:8443: bind: cannot assign requested address\n",
			want: "start service: start inbound: listen tcp 203.0.113.7:8443: bind: cannot assign requested address",
		},
		{
			name: "xray failed to start",
			out:  "Xray 26.3.27 started\n2026/10/30 18:11:31 Failed to start: main: failed to listen TCP on 8447 > bind: address already in use\n",
			want: "main: failed to listen TCP on 8447 > bind: address already in use",
		},
		{
			name: "fatal line wins over later noise",
			out:  "FATAL[0000] decode config: unknown field\nsome trailing line\n",
			want: "decode config: unknown field",
		},
		{
			name: "no fatal line: last line",
			out:  "a\nb\n\n",
			want: "b",
		},
		{name: "nothing printed", out: "", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var tl Tail
			_, _ = tl.Write([]byte(tc.out))
			if got := tl.Reason(); got != tc.want {
				t.Errorf("Reason() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestReason_RedactsNonFatalLine(t *testing.T) {
	logredact.SetEnabled(true)
	defer logredact.SetEnabled(false)
	var tl Tail
	_, _ = tl.Write([]byte("ERROR inbound connection from 198.51.100.4: eof\n"))
	if got := tl.Reason(); strings.Contains(got, "198.51.100.4") {
		t.Errorf("client address leaked: %q", got)
	}
}

func TestTail_KeepsOnlyTheEnd(t *testing.T) {
	var tl Tail
	_, _ = tl.Write([]byte(strings.Repeat("x", keep*2)))
	_, _ = tl.Write([]byte("\nFATAL[0000] the end\n"))
	if len(tl.buf) > keep {
		t.Fatalf("buffer grew to %d", len(tl.buf))
	}
	if got := tl.Reason(); got != "the end" {
		t.Errorf("Reason() = %q", got)
	}
}
