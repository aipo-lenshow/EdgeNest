package clashapi

import "testing"

func TestParseConnections(t *testing.T) {
	body := []byte(`{
		"downloadTotal": 1, "uploadTotal": 2,
		"connections": [
			{"metadata": {"network":"tcp","sourceIP":"203.0.113.5","destinationIP":"1.2.3.4","destinationPort":"443","host":"www.netflix.com"}},
			{"metadata": {"network":"udp","sourceIP":"203.0.113.5","destinationIP":"5.6.7.8","destinationPort":"443","host":"nflxvideo.net"}},
			{"metadata": {"network":"tcp","sourceIP":"203.0.113.9","destinationIP":"9.9.9.9","destinationPort":"53","host":""}}
		]
	}`)
	conns, err := parseConnections(body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(conns) != 3 {
		t.Fatalf("want 3 conns, got %d", len(conns))
	}
	if conns[0].Host != "www.netflix.com" || conns[0].SourceIP != "203.0.113.5" {
		t.Errorf("conn0 wrong: %+v", conns[0])
	}
	if conns[1].Host != "nflxvideo.net" || conns[1].Network != "udp" {
		t.Errorf("conn1 wrong: %+v", conns[1])
	}
	// A connection with no sniffed host still parses (caller decides to fall back
	// to DestIP or skip it).
	if conns[2].Host != "" || conns[2].DestIP != "9.9.9.9" {
		t.Errorf("conn2 wrong: %+v", conns[2])
	}
}
