package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

func nested(depth int) string {
	return `{"a":` + strings.Repeat(`[`, depth-1) + strings.Repeat(`]`, depth-1) + `}`
}

func TestBoundedPayload(t *testing.T) {
	big := `{"a":"` + strings.Repeat("x", maxNotificationPayload) + `"}`
	cases := []struct{ name, in, want string }{
		{"empty", "", "{}"},
		{"null", " null ", "{}"},
		{"object", `{"a":1,"b":[1,{"c":"d"}]}`, `{"a":1,"b":[1,{"c":"d"}]}`},
		{"trimmed", " \n{\"a\":1}\t", `{"a":1}`},
		{"string", `"x"`, omittedPayload},
		{"array", `[1,2]`, omittedPayload},
		{"number", `12`, omittedPayload},
		{"invalid", `{"a":`, omittedPayload},
		{"trailing garbage", `{"a":1}x`, omittedPayload},
		{"oversized", big, omittedPayload},
		{"at depth bound", nested(maxNotificationPayloadDepth), nested(maxNotificationPayloadDepth)},
		{"past depth bound", nested(maxNotificationPayloadDepth + 1), omittedPayload},
		// Brackets inside strings, escaped quotes included, are not nesting.
		{"brackets in strings", `{"a":"` + strings.Repeat(`[{`, 100) + `\"` + strings.Repeat(`[`, 100) + `"}`,
			`{"a":"` + strings.Repeat(`[{`, 100) + `\"` + strings.Repeat(`[`, 100) + `"}`},
		{"escaped backslash ends string", `{"a":"\\"}`, `{"a":"\\"}`},
	}
	for _, c := range cases {
		if got := boundedPayload([]byte(c.in)); got != c.want {
			if len(got) > 80 {
				got = got[:80] + "…"
			}
			t.Errorf("%s: got %s", c.name, got)
		}
	}
	// One byte under the size bound is kept.
	fit := `{"a":"` + strings.Repeat("x", maxNotificationPayload-8) + `"}`
	if got := boundedPayload([]byte(fit)); got != fit {
		t.Errorf("a payload of %d bytes was not kept", len(fit))
	}
}

// A payload of the wrong type or past a bound used to fail the entry's decode, which returned
// nothing and left the entry queued ahead of every later one. Now the entry is read with its
// payload replaced, and the entries after it are read too.
func TestReadOutboxEntriesBoundsEachPayload(t *testing.T) {
	big := `{"many":[` + strings.Repeat(`0,`, maxNotificationPayload) + `0]}`
	body := `{"notifications":[` +
		`{"id":"a","payload":"not an object"},` +
		`{"id":"b","payload":` + big + `},` +
		`{"id":"c","payload":` + nested(maxNotificationPayloadDepth+1) + `},` +
		`{"id":"d","payload":{"conversation_id":"c1"}},` +
		`{"id":"e"}]}`
	got, more, ok := readOutboxEntries(strings.NewReader(body), 64<<20)
	if !ok || more || len(got) != 5 {
		t.Fatalf("got %d entries more=%v ok=%v, want 5, false, true", len(got), more, ok)
	}
	want := []string{omittedPayload, omittedPayload, omittedPayload, `{"conversation_id":"c1"}`, `{}`}
	for i, in := range got {
		if string(in.Payload) != want[i] {
			t.Errorf("entry %s payload = %.80s, want %s", in.ID, in.Payload, want[i])
		}
		if row := agentNotificationRow(in, "M1"); row.Payload != want[i] {
			t.Errorf("entry %s stored payload = %.80s, want %s", in.ID, row.Payload, want[i])
		}
	}
}

// A row stored before the bound existed, or by a path that does not bound, still lists: its
// payload is an object, and the list marshals.
func TestNotificationToDTOBoundsTheStoredPayload(t *testing.T) {
	for stored, want := range map[string]string{
		"":                                      `{}`,
		`{"repo":"r"}`:                          `{"repo":"r"}`,
		`not json`:                              omittedPayload,
		`["a"]`:                                 omittedPayload,
		nested(maxNotificationPayloadDepth + 1): omittedPayload,
		`{"a":"` + strings.Repeat("x", maxNotificationPayload) + `"}`: omittedPayload,
	} {
		dto := notificationToDTO(store.Notification{EventID: "e", Payload: stored})
		b, err := json.Marshal(dto)
		if err != nil {
			t.Fatalf("marshal with stored payload %.40q: %v", stored, err)
		}
		var back struct {
			Payload map[string]any `json:"payload"`
		}
		if err := json.Unmarshal(b, &back); err != nil || back.Payload == nil {
			t.Fatalf("payload is not an object for stored %.40q: %s (%v)", stored, b, err)
		}
		if string(dto.Payload) != want {
			t.Errorf("stored %.40q: payload %.80s, want %s", stored, dto.Payload, want)
		}
	}
}

// End to end: an entry whose payload is past the bound is stored and acked with the payload
// omitted, and does not hold back the entry behind it.
func TestDrainAgentOutboxStoresAnOversizedPayloadOmitted(t *testing.T) {
	st, mem := drainFixture(t)
	var acked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/notifications/ack" {
			var b struct {
				IDs []string `json:"ids"`
			}
			_ = json.NewDecoder(r.Body).Decode(&b)
			acked = append(acked, b.IDs...)
			return
		}
		_, _ = w.Write([]byte(`{"notifications":[` +
			`{"id":"big","kind":"question","sessionName":"s","createdAt":"2026-11-23T00:00:00Z","payload":{"questions":"` + strings.Repeat("q", maxNotificationPayload) + `"}},` +
			`{"id":"next","kind":"question","sessionName":"s","createdAt":"2026-11-23T00:00:01Z","payload":{"body":"hi"}}]}`))
	}))
	defer srv.Close()
	if got := drainAgentOutbox(context.Background(), st, drainFakeRT{state: "running", endpoint: srv.URL}, mem); got != "ready" {
		t.Fatalf("drain = %q", got)
	}
	if strings.Join(acked, ",") != "big,next" {
		t.Fatalf("acked %v, want big,next", acked)
	}
	rows, _ := st.ListNotifications(context.Background(), mem, "2000-01-01T00:00:00Z", 50)
	got := map[string]string{}
	for _, n := range rows {
		got[n.EventID] = n.Payload
	}
	if got["agent:big"] != omittedPayload || got["agent:next"] != `{"body":"hi"}` {
		t.Fatalf("stored payloads %v", got)
	}
}
