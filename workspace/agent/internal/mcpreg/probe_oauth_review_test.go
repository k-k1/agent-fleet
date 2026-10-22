package mcpreg

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

func TestPRMCandidatesKeepQueryAndTrailingSlash(t *testing.T) {
	const wk = "https://h.example/.well-known/oauth-protected-resource"
	for in, want := range map[string][]string{
		"https://h.example/mcp":            {wk + "/mcp", wk},
		"https://h.example/mcp/":           {wk + "/mcp/", wk}, // only a slash right after the host is dropped
		"https://h.example/mcp?tenant=one": {wk + "/mcp?tenant=one", wk},
		"https://h.example?tenant=one":     {wk + "?tenant=one", wk},
		"https://h.example":                {wk},
		"https://h.example/":               {wk},
	} {
		if got := prmCandidates(in); strings.Join(got, " ") != strings.Join(want, " ") {
			t.Errorf("%s: got %v want %v", in, got, want)
		}
	}
}

func TestProbeOAuthQueryRegisteredURL(t *testing.T) {
	var srvURL string
	var wkHit atomic.Value
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no", http.StatusUnauthorized) // bare Bearer: no resource_metadata
	})
	mux.HandleFunc("/.well-known/oauth-protected-resource/mcp", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "tenant=one" {
			http.NotFound(w, r)
			return
		}
		wkHit.Store(r.URL.String())
		_ = json.NewEncoder(w).Encode(map[string]any{"resource": srvURL + "/mcp?tenant=one", "authorization_servers": []string{srvURL}})
	})
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(asDoc(srvURL, nil))
	})
	s := httptest.NewServer(mux)
	defer s.Close()
	srvURL = s.URL
	res := Probe(context.Background(), ServerDef{Transport: TransportHTTP, URL: s.URL + "/mcp?tenant=one", Enabled: true})
	if res.OAuth == nil || len(res.OAuth.Problems) != 0 || res.OAuth.Selected != s.URL {
		t.Fatalf("a PRM that exists only with the query must be found: %+v %+v", res, res.OAuth)
	}
	if wkHit.Load() == nil {
		t.Error("the query form was never fetched")
	}
}

// laterFake answers the MCP request sequence with a scripted verdict per JSON-RPC method.
// "401" is a Bearer challenge naming the PRM; "public" is a success shaped for the method.
func laterFake(t *testing.T, script map[string]string) *httptest.Server {
	var s *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var m rpcMsg
		_ = json.Unmarshal(b, &m)
		w.Header().Set("Content-Type", "application/json")
		switch script[m.Method] {
		case "401":
			w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+s.URL+`/prm"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		case "notfound":
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": m.ID, "error": map[string]any{"code": errMethodNotFound, "message": "nope"}})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": map[string]any{"serverInfo": map[string]any{"name": "x", "version": "1"}, "tools": []any{}}})
		}
	})
	mux.HandleFunc("/prm", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"resource": s.URL + "/mcp", "authorization_servers": []string{s.URL}})
	})
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(asDoc(s.URL, nil))
	})
	s = httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

func TestProbeOAuthOnALater401(t *testing.T) {
	cases := map[string]map[string]string{
		"stateless tools/list":   {"tools/list": "401"},
		"legacy initialize":      {"server/discover": "notfound", "initialize": "401"},
		"legacy tools/list":      {"server/discover": "notfound", "tools/list": "401"},
		"legacy initialized ack": {"server/discover": "notfound", "notifications/initialized": "401"},
	}
	for name, script := range cases {
		s := laterFake(t, script)
		res := Probe(context.Background(), ServerDef{Transport: TransportHTTP, URL: s.URL + "/mcp", Enabled: true})
		if res.OK || res.OAuth == nil || !res.OAuth.Required || res.OAuth.Selected != s.URL {
			t.Errorf("%s: want the OAuth report, got %+v / %+v", name, res, res.OAuth)
		}
	}
}

func TestMetadataGetterRefusesPlainHTTPBeforeTheNetwork(t *testing.T) {
	var trips atomic.Int32
	old := http.DefaultTransport
	http.DefaultTransport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		trips.Add(1)
		return nil, io.EOF
	})
	defer func() { http.DefaultTransport = old }()
	var g metadataGetter
	for _, u := range []string{
		"http://metadata.example/prm",
		"http://user:pw@127.0.0.1/prm",
		"https://user:pw@metadata.example/prm",
		"ftp://metadata.example/prm",
	} {
		var out map[string]any
		if err := g.get(context.Background(), u, &out); err == nil {
			t.Errorf("%s must be refused", u)
		}
	}
	if trips.Load() != 0 {
		t.Fatalf("a refused URL reached the transport %d times", trips.Load())
	}
	// A loopback http URL is the one allowed plain-http case: it reaches the transport.
	_ = g.get(context.Background(), "http://127.0.0.1:1/prm", new(map[string]any))
	if trips.Load() != 1 {
		t.Errorf("loopback http should be attempted, trips=%d", trips.Load())
	}
	if u, _ := url.Parse("https://metadata.example/prm"); !metadataURLUsable(u, nil) {
		t.Error("https must be usable")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
