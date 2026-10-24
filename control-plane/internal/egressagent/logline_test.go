package egressagent

import "testing"

func TestParseLine(t *testing.T) {
	cases := []struct {
		name string
		line string
		ok   bool
		want logLine
	}{
		{
			name: "spliced TLS with SNI",
			line: "af1 1761300000.123 10.0.1.5 140.82.112.3 TCP_TUNNEL splice api.github.com 140.82.112.3",
			ok:   true,
			want: logLine{at: 1761300000123, source: "10.0.1.5", dest: "140.82.112.3", host: "api.github.com"},
		},
		{
			name: "plain HTTP: request domain, no SNI",
			line: "af1 1761300000.007 10.0.1.5 151.101.0.223 TCP_MISS - - Deb.Debian.org.",
			ok:   true,
			want: logLine{at: 1761300000007, source: "10.0.1.5", dest: "151.101.0.223", host: "deb.debian.org"},
		},
		{
			name: "terminated in enforce",
			line: "af1 1761300000.000 10.0.1.5 - NONE_NONE terminate evil.example 203.0.113.9",
			ok:   true,
			want: logLine{at: 1761300000000, source: "10.0.1.5", host: "evil.example", denied: true},
		},
		{
			name: "denied plain HTTP",
			line: "af1 1761300000.000 10.0.1.5 - TCP_DENIED - - evil.example",
			ok:   true,
			want: logLine{at: 1761300000000, source: "10.0.1.5", host: "evil.example", denied: true},
		},
		{
			name: "bare address: no SNI, request domain is the address",
			line: "af1 1761300000.000 10.0.1.5 203.0.113.9 TCP_TUNNEL splice - 203.0.113.9",
			ok:   true,
			want: logLine{at: 1761300000000, source: "10.0.1.5", dest: "203.0.113.9"},
		},
		{
			name: "URL-encoded SNI with a space stays one field",
			line: "af1 1761300000.000 10.0.1.5 203.0.113.9 TCP_TUNNEL splice a%20b.example -",
			ok:   true,
			want: logLine{at: 1761300000000, source: "10.0.1.5", dest: "203.0.113.9", host: "a b.example"},
		},
		{
			name: "IPv4-mapped source is unmapped",
			line: "af1 1761300000.000 ::ffff:10.0.1.5 - TCP_TUNNEL splice x.example -",
			ok:   true,
			want: logLine{at: 1761300000000, source: "10.0.1.5", host: "x.example"},
		},
		{
			name: "damaged tail keeps time and source, no name, not allowed",
			line: "af1 1761300000.000 10.0.1.5 203.0.113.9 TCP_TUNNEL",
			ok:   true,
			want: logLine{at: 1761300000000, source: "10.0.1.5", dest: "203.0.113.9", denied: true},
		},
		{name: "another format", line: "1761300000.000 10.0.1.5 TCP_TUNNEL/200 0 CONNECT x:443", ok: false},
		{name: "bad time", line: "af1 1761300000 10.0.1.5 - TCP_TUNNEL splice x -", ok: false},
		{name: "bad source", line: "af1 1761300000.000 - - TCP_TUNNEL splice x -", ok: false},
		{name: "empty", line: "", ok: false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := parseLine(c.line)
			if ok != c.ok {
				t.Fatalf("ok = %v, want %v", ok, c.ok)
			}
			if ok && got != c.want {
				t.Fatalf("got %+v\nwant %+v", got, c.want)
			}
		})
	}
}

func TestParseLineDropsOverlongName(t *testing.T) {
	long := make([]byte, maxHost+1)
	for i := range long {
		long[i] = 'a'
	}
	got, ok := parseLine("af1 1761300000.000 10.0.1.5 203.0.113.9 TCP_TUNNEL splice " + string(long) + " -")
	if !ok || got.host != "" || got.dest != "203.0.113.9" {
		t.Fatalf("got %+v ok=%v: want the event by address with no host", got, ok)
	}
}
