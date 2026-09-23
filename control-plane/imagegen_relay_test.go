// imagegen_relay_test.go — the image routes reach the Agent untouched.
//
// The CP keeps no image types of its own (ADR 0081 decision 1, ADR 0100): the catalogue's
// family facts, the studio's draft and its If-Match version are the Agent's vocabulary. A
// relay that decoded into a CP struct would drop whatever the struct lacks — the trap
// sessionWire already fell into — so these pin byte-for-byte passage both ways.
package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestImagegenRoutesRelayVerbatim(t *testing.T) {
	type seen struct{ method, path, query, ifMatch, body string }
	var got seen
	var reply struct {
		status int
		etag   string
		body   string
	}
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = seen{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("If-Match"), string(b)}
		if reply.etag != "" {
			w.Header().Set("ETag", reply.etag)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(reply.status)
		_, _ = io.WriteString(w, reply.body)
	}))
	t.Cleanup(agent.Close)

	env := newBrowserTestEnv(t, browserTestRuntime{endpoint: agent.URL, token: "agent-secret", state: "running"})
	mux := http.NewServeMux()
	registerImagegenRoutes(mux, config{mgr: env.mgr})
	cp := httptest.NewServer(mux)
	t.Cleanup(cp.Close)

	const studio = "0b9d1f2e-7c4a-4e1b-9a3d-5f6e7a8b9c0d"
	cases := []struct {
		name, method, path, ifMatch, body string
		status                            int
		etag, reply                       string
	}{
		// The family table's facts ride modelStatus (ADR 0100 decision 7). The keys here are
		// deliberately ones no CP type knows: the relay must not care what they are called.
		{name: "status keeps keys the CP never declared", method: "GET", path: "/imagegen/status",
			status: 200, reply: `{"models":[{"id":"m","family":"sdxl","x_future_facts":{"range":[4,9]},"x_prefix":["a"]}]}`},
		// A draft patch: If-Match in, the Agent's conflict and its new version out.
		{name: "studio PUT carries If-Match and the 409", method: "PUT", path: "/imagegen/studios/" + studio,
			ifMatch: `"2026-09-23T10:00:00.000000001Z"`, body: `{"draft":{"prompt":"a cat","seed":null},"author":"human"}`,
			status: 409, etag: `"2026-09-23T10:00:01Z"`, reply: `{"error":{"code":"stale"}}`},
		{name: "press answers as the Agent does", method: "POST", path: "/imagegen/studios/" + studio + "/press",
			body: `{"mode":"trial"}`, status: 202, reply: `{"version":"v1","recorded":false}`},
		{name: "history query survives", method: "GET", path: "/imagegen/history?studio=" + studio + "&before=x&limit=20",
			status: 200, reply: `{"items":[]}`},
		{name: "knowledge append", method: "POST", path: "/imagegen/knowledge",
			body: `{"scope":"family","key":"sdxl","note":"n"}`, status: 501, reply: `{"error":{"code":"not_implemented"}}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got = seen{}
			reply.status, reply.etag, reply.body = c.status, c.etag, c.reply
			req, _ := http.NewRequest(c.method, cp.URL+"/api"+c.path, strings.NewReader(c.body))
			if c.ifMatch != "" {
				req.Header.Set("If-Match", c.ifMatch)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			b, _ := io.ReadAll(resp.Body)

			wantPath, wantQuery, _ := strings.Cut(c.path, "?")
			if got.method != c.method || got.path != wantPath || got.query != wantQuery {
				t.Errorf("Agent saw %s %s?%s, want %s %s", got.method, got.path, got.query, c.method, c.path)
			}
			if got.ifMatch != c.ifMatch {
				t.Errorf("Agent If-Match = %q, want %q", got.ifMatch, c.ifMatch)
			}
			if got.body != c.body {
				t.Errorf("Agent body = %q, want %q", got.body, c.body)
			}
			if resp.StatusCode != c.status || string(b) != c.reply {
				t.Errorf("CP answered %d %q, want %d %q", resp.StatusCode, b, c.status, c.reply)
			}
			if e := resp.Header.Get("ETag"); e != c.etag {
				t.Errorf("ETag = %q, want %q", e, c.etag)
			}
		})
	}
}
