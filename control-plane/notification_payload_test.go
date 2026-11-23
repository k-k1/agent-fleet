package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

func nested(depth int) string {
	return `{"a":` + strings.Repeat(`[`, depth-1) + strings.Repeat(`]`, depth-1) + `}`
}

const truncatedOnly = `{"` + payloadTruncatedKey + `":true}`

func TestBoundedPayload(t *testing.T) {
	long := strings.Repeat("x", maxNotificationPayload)
	cases := []struct{ name, in, want string }{
		{"empty", "", "{}"},
		{"null", " null ", "{}"},
		{"object", `{"a":1,"b":[1,{"c":"d"}]}`, `{"a":1,"b":[1,{"c":"d"}]}`},
		{"trimmed", " \n{\"a\":1}\t\r", `{"a":1}`},
		// Only JSON's whitespace is trimmed: a no-break space is not, and the payload is invalid.
		{"no-break space", " {\"a\":1}", omittedPayload},
		{"string", `"x"`, omittedPayload},
		{"array", `[1,2]`, omittedPayload},
		{"number", `12`, omittedPayload},
		{"invalid", `{"a":`, omittedPayload},
		{"trailing garbage", `{"a":1}x`, omittedPayload},
		// Invalid UTF-8 inside a string becomes U+FFFD, as the map decode did.
		{"invalid utf-8", "{\"body\":\"a\xffb\"}", "{\"body\":\"a�b\"}"},
		{"at depth bound", nested(maxNotificationPayloadDepth), nested(maxNotificationPayloadDepth)},
		// Past a bound the members that fit are kept and the rest dropped.
		{"past depth bound", `{"id":"k",` + nested(maxNotificationPayloadDepth + 1)[1:],
			`{"id":"k","payloadTruncated":true}`},
		{"oversized string cut", `{"spec_label":"L","status":"skipped_overlap","spec":"` + long + `"}`,
			`{"spec_label":"L","status":"skipped_overlap","spec":"` + long[:maxNotificationPayloadValue] + `","payloadTruncated":true}`},
		{"oversized list dropped", `{"many":[` + strings.Repeat(`0,`, maxNotificationPayload) + `0],"repo":"r"}`,
			`{"repo":"r","payloadTruncated":true}`},
		// The sender's own mark never survives as anything but true.
		{"mark kept true", `{"payloadTruncated":false,"a":"` + long + `"}`,
			`{"a":"` + long[:maxNotificationPayloadValue] + `","payloadTruncated":true}`},
		// Brackets inside strings, escaped quotes included, are not nesting.
		{"brackets in strings", `{"a":"` + strings.Repeat(`[{`, 100) + `\"` + strings.Repeat(`[`, 100) + `"}`,
			`{"a":"` + strings.Repeat(`[{`, 100) + `\"` + strings.Repeat(`[`, 100) + `"}`},
		// An escaped backslash ends the string, so the depth count resumes right after it.
		{"escaped backslash ends string", `{"a":"\\","b":` + nested(maxNotificationPayloadDepth + 1)[5:],
			`{"a":"\\","payloadTruncated":true}`},
	}
	for _, c := range cases {
		if got := boundedPayload([]byte(c.in)); got != c.want {
			if len(got) > 120 {
				got = got[:120] + "…"
			}
			t.Errorf("%s: got %s", c.name, got)
		}
	}
	// A payload exactly at the size bound is kept.
	fit := `{"a":"` + strings.Repeat("x", maxNotificationPayload-8) + `"}`
	if got := boundedPayload([]byte(fit)); got != fit {
		t.Errorf("a payload of %d bytes was not kept", len(fit))
	}
	// Many members that each fit still stop at the size bound.
	var b strings.Builder
	b.WriteString(`{`)
	for i := 0; i < 64; i++ {
		b.WriteString(fmt.Sprintf(`"k%02d":"%s",`, i, strings.Repeat("v", maxNotificationPayloadValue-16)))
	}
	b.WriteString(`"z":1}`)
	got := boundedPayload([]byte(b.String()))
	if len(got) > maxNotificationPayload || !json.Valid([]byte(got)) || !strings.HasSuffix(got, `"payloadTruncated":true}`) {
		t.Errorf("many members: %d bytes, valid=%v", len(got), json.Valid([]byte(got)))
	}
}

// A long question list keeps each question's text, which Web Push reads (pushsrv.questionText).
func TestBoundedPayloadKeepsTheQuestionText(t *testing.T) {
	preview := strings.Repeat("p", maxNotificationPayload)
	in := `{"questions":[{"question":"Which way?","header":"Way","options":[{"label":"A","preview":"` + preview + `"}]},{"question":"Second?"}]}`
	got := boundedPayload([]byte(in))
	want := `{"questions":[{"question":"Which way?","header":"Way"},{"question":"Second?"}],"payloadTruncated":true}`
	if got != want {
		t.Fatalf("got %.200s", got)
	}
	var p struct {
		Questions []struct {
			Question string `json:"question"`
		} `json:"questions"`
	}
	if json.Unmarshal([]byte(got), &p) != nil || len(p.Questions) == 0 || p.Questions[0].Question != "Which way?" {
		t.Fatalf("the first question's text is not readable the way pushsrv reads it: %s", got)
	}
}

// A payload of the wrong type or past a bound used to fail the entry's decode, which returned
// nothing and left the entry queued ahead of every later one. Now the entry is read with its
// payload replaced, and the entries after it are read too.
func TestReadOutboxEntriesBoundsEachPayload(t *testing.T) {
	big := `{"many":[` + strings.Repeat(`0,`, maxNotificationPayload) + `0],"conversation_id":"c0"}`
	body := `{"notifications":[` +
		`{"id":"a","payload":"not an object"},` +
		`{"id":"b","payload":` + big + `},` +
		`{"id":"c","payload":` + nested(maxNotificationPayloadDepth+1) + `},` +
		`{"id":"d","payload":{"conversation_id":"c1"}},` +
		"{\"id\":\"u\",\"payload\":{\"body\":\"a\xffb\"}}," +
		`{"id":"e"}]}`
	got, more, ok := readOutboxEntries(strings.NewReader(body), 64<<20)
	if !ok || more || len(got) != 6 {
		t.Fatalf("got %d entries more=%v ok=%v, want 6, false, true", len(got), more, ok)
	}
	want := []string{omittedPayload, `{"conversation_id":"c0","payloadTruncated":true}`, truncatedOnly,
		`{"conversation_id":"c1"}`, "{\"body\":\"a�b\"}", `{}`}
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
// payload is a UTF-8 object, and the list marshals.
func TestNotificationToDTOBoundsTheStoredPayload(t *testing.T) {
	longSpec := "0 9 * * *" + strings.Repeat(" ", maxNotificationPayload)
	for stored, want := range map[string]string{
		"":                                      `{}`,
		`{"repo":"r"}`:                          `{"repo":"r"}`,
		`not json`:                              omittedPayload,
		`["a"]`:                                 omittedPayload,
		"{\"body\":\"\xff\"}":                   "{\"body\":\"�\"}",
		nested(maxNotificationPayloadDepth + 1): truncatedOnly,
		// A schedule's spec is stored as written, so a long one must not take the status with it.
		`{"schedule_id":"s1","status":"skipped_overlap","spec_label":"L","spec":"` + longSpec + `"}`: `{"schedule_id":"s1","status":"skipped_overlap","spec_label":"L","spec":"` +
			longSpec[:maxNotificationPayloadValue] + `","payloadTruncated":true}`,
	} {
		dto := notificationToDTO(store.Notification{EventID: "e", Payload: stored})
		b, err := json.Marshal(dto)
		if err != nil {
			t.Fatalf("marshal with stored payload %.40q: %v", stored, err)
		}
		if !utf8.Valid(b) {
			t.Fatalf("list body is not UTF-8 for stored %.40q", stored)
		}
		var back struct {
			Payload map[string]any `json:"payload"`
		}
		if err := json.Unmarshal(b, &back); err != nil || back.Payload == nil {
			t.Fatalf("payload is not an object for stored %.40q: %s (%v)", stored, b, err)
		}
		if string(dto.Payload) != want {
			t.Errorf("stored %.40q: payload %.120s, want %.120s", stored, dto.Payload, want)
		}
	}
}

// End to end: an entry past the bound, and one with invalid UTF-8, are stored and acked, and do
// not hold back the entry behind them.
func TestDrainAgentOutboxStoresAnOversizedPayloadShrunk(t *testing.T) {
	st, mem := drainFixture(t)
	var mu sync.Mutex
	var acked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/notifications/ack" {
			var b struct {
				IDs []string `json:"ids"`
			}
			_ = json.NewDecoder(r.Body).Decode(&b)
			mu.Lock()
			acked = append(acked, b.IDs...)
			mu.Unlock()
			return
		}
		_, _ = w.Write([]byte(`{"notifications":[` +
			`{"id":"big","kind":"question","sessionName":"s","createdAt":"2026-11-23T00:00:00Z","payload":{"questions":[{"question":"Q?","options":"` + strings.Repeat("q", maxNotificationPayload) + `"}]}},` +
			"{\"id\":\"bad\",\"kind\":\"answer-ready\",\"sessionName\":\"s\",\"createdAt\":\"2026-11-23T00:00:01Z\",\"payload\":{\"body\":\"a\xffb\"}}," +
			`{"id":"next","kind":"question","sessionName":"s","createdAt":"2026-11-23T00:00:02Z","payload":{"body":"hi"}}]}`))
	}))
	defer srv.Close()
	if got := drainAgentOutbox(context.Background(), st, drainFakeRT{state: "running", endpoint: srv.URL}, mem); got != "ready" {
		t.Fatalf("drain = %q", got)
	}
	mu.Lock()
	gotAcked := strings.Join(acked, ",")
	mu.Unlock()
	if gotAcked != "big,bad,next" {
		t.Fatalf("acked %v, want big,bad,next", gotAcked)
	}
	rows, _ := st.ListNotifications(context.Background(), mem, "2000-01-01T00:00:00Z", 50)
	got := map[string]string{}
	for _, n := range rows {
		got[n.EventID] = n.Payload
	}
	if got["agent:big"] != `{"questions":[{"question":"Q?"}],"payloadTruncated":true}` ||
		got["agent:bad"] != "{\"body\":\"a�b\"}" || got["agent:next"] != `{"body":"hi"}` {
		t.Fatalf("stored payloads %v", got)
	}
}
