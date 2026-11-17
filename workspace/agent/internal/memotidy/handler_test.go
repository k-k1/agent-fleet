package memotidy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/chatx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routepolicy"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

// secretReply is text a model reply carries that must never leave the handler on an error.
const secretReply = "SECRET-REPLY-TEXT-7731"

type call struct {
	ctx             context.Context
	feature         string
	tier            chatx.OneShotTier
	persona, prompt string
}

// fakeCall replaces the deny-all call for one test and records what it was given.
func fakeCall(t *testing.T, fn func(c call) (chatx.DenyAllResult, error)) *[]call {
	t.Helper()
	var mu sync.Mutex
	var calls []call
	old := denyAll
	denyAll = func(ctx context.Context, feature string, tier chatx.OneShotTier, persona, prompt string) (chatx.DenyAllResult, error) {
		c := call{ctx, feature, tier, persona, prompt}
		mu.Lock()
		calls = append(calls, c)
		mu.Unlock()
		return fn(c)
	}
	t.Cleanup(func() { denyAll = old })
	return &calls
}

func reply(s string) func(call) (chatx.DenyAllResult, error) {
	return func(call) (chatx.DenyAllResult, error) {
		return chatx.DenyAllResult{Reply: s, Kind: "claude", Model: "m-1"}, nil
	}
}

func validRequest() map[string]any {
	return map[string]any{
		"run": "run-1", "trigger": "auto",
		"queue": map[string]any{"id": "cat-1", "label": "app/impl"},
		"items": []map[string]any{
			{"id": "a", "kind": "text", "text": "first", "file": "", "group": 0, "merge": true, "move": true},
			{"id": "b", "kind": "file", "text": "", "file": "~/repos/app/x.go", "group": 1, "merge": false, "move": true},
		},
		"destinations": []map[string]any{{"id": "cat-2", "name": "app/docs", "queue": false}},
		"limits":       map[string]any{"mergeSources": 10, "splitParts": 5, "textBytes": 16384, "operations": 80},
	}
}

func post(t *testing.T, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return postRaw(string(b))
}

func postRaw(body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/memos/tidy", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	HandleTidy(rec, req)
	return rec
}

func errCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var env struct{ Error struct{ Code string } }
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("answer is not JSON: %v: %s", err, rec.Body)
	}
	return env.Error.Code
}

func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(log.Writer()) })
	return &buf
}

const okReply = `{"operations":[{"op":"merge","ids":["a","b"],"text":"both"},{"op":"split","id":"c","texts":["x","y"]},{"op":"move","id":"d","to":"cat-2"},{"op":"keep","id":"e"}]}`

func TestTidyAnswersWithTheParsedOperations(t *testing.T) {
	calls := fakeCall(t, reply(okReply))
	rec := post(t, validRequest())
	if rec.Code != http.StatusOK {
		t.Fatalf("= %d %s", rec.Code, rec.Body)
	}
	var got Response
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Run != "run-1" || got.Kind != "claude" || got.Model != "m-1" || len(got.Operations) != 4 {
		t.Fatalf("answer = %+v", got)
	}
	if got.Operations[0].Op != "merge" || got.Operations[1].Texts[1] != "y" || got.Operations[2].To != "cat-2" || got.Operations[3].ID != "e" {
		t.Fatalf("operations = %+v", got.Operations)
	}
	if len(*calls) != 1 {
		t.Fatalf("the model was called %d times, want exactly one", len(*calls))
	}
}

// The call carries the usage feature, the prose tier and the fixed persona; the ledger row is
// written under that tag by the deny-all route.
func TestTidyCallIsTaggedForUsage(t *testing.T) {
	calls := fakeCall(t, reply(`{"operations":[]}`))
	if rec := post(t, validRequest()); rec.Code != http.StatusOK {
		t.Fatalf("= %d %s", rec.Code, rec.Body)
	}
	c := (*calls)[0]
	tag, ok := usagex.TagOf(c.ctx)
	if !ok || tag.Feature != "memo.tidy" || tag.Trigger != usagex.TriggerAuto {
		t.Errorf("tag = %+v ok=%v, want memo.tidy / auto", tag, ok)
	}
	if c.feature != usagex.FeatureMemoTidy || c.tier != chatx.OneShotProse || c.persona != persona {
		t.Errorf("call = feature %q tier %v persona fixed %v", c.feature, c.tier, c.persona == persona)
	}
}

func TestTidyErrorCodes(t *testing.T) {
	unavailable := fmt.Errorf("%w: the probe failed", chatx.ErrDenyAllUnavailable)
	for name, tc := range map[string]struct {
		err    error
		status int
		code   string
	}{
		"nothing reached a model":      {unavailable, 503, "deny_all_unavailable"},
		"a real run that had tools":    {errors.New("claude deny-all run had tools: the reply was discarded"), 502, "tidy_failed"},
		"the call failed":              {errors.New("claude execution failed (exit 1)"), 502, "tidy_failed"},
		"the policy forbids the route": {fmt.Errorf("%w: claude would run on x", chatx.ErrRoutePolicy), 403, "route_forbidden"},
		"the policy is unconfirmed":    {fmt.Errorf("%w: %w", chatx.ErrRoutePolicy, routepolicy.ErrUnconfirmed), 503, "policy_unconfirmed"},
	} {
		t.Run(name, func(t *testing.T) {
			fakeCall(t, func(call) (chatx.DenyAllResult, error) { return chatx.DenyAllResult{}, tc.err })
			rec := post(t, validRequest())
			if rec.Code != tc.status || errCode(t, rec) != tc.code {
				t.Errorf("= %d %s, want %d %s", rec.Code, rec.Body, tc.status, tc.code)
			}
		})
	}
}

// A failure that is not ErrDenyAllUnavailable is never a 503, whatever its text says.
func TestTidyOnlyTheSentinelIsUnavailable(t *testing.T) {
	fakeCall(t, func(call) (chatx.DenyAllResult, error) {
		return chatx.DenyAllResult{}, errors.New(chatx.ErrDenyAllUnavailable.Error())
	})
	if rec := post(t, validRequest()); rec.Code != http.StatusBadGateway {
		t.Errorf("= %d %s, want 502", rec.Code, rec.Body)
	}
}

func TestTidyTimeout(t *testing.T) {
	old := handlerBudget
	handlerBudget = 50 * time.Millisecond
	t.Cleanup(func() { handlerBudget = old })
	fakeCall(t, func(c call) (chatx.DenyAllResult, error) {
		<-c.ctx.Done()
		return chatx.DenyAllResult{}, fmt.Errorf("%w: the probe did not finish in time", chatx.ErrDenyAllUnavailable)
	})
	rec := post(t, validRequest())
	if rec.Code != http.StatusGatewayTimeout || errCode(t, rec) != "tidy_timeout" {
		t.Errorf("= %d %s", rec.Code, rec.Body)
	}
}

// A reply that is refused, and a call that failed, leave neither the reply nor an error text in
// the answer or the log.
func TestTidyNeverReturnsOrLogsTheReplyOnAnError(t *testing.T) {
	for name, fn := range map[string]func(call) (chatx.DenyAllResult, error){
		"malformed":   reply(secretReply),
		"unknown key": reply(`{"operations":[],"` + secretReply + `":1}`),
		"call failed": func(call) (chatx.DenyAllResult, error) {
			return chatx.DenyAllResult{Reply: secretReply}, errors.New("claude returned an invalid response/error")
		},
	} {
		t.Run(name, func(t *testing.T) {
			buf := captureLog(t)
			fakeCall(t, fn)
			rec := post(t, validRequest())
			if rec.Code < 400 {
				t.Fatalf("= %d, want an error", rec.Code)
			}
			if strings.Contains(rec.Body.String(), secretReply) || strings.Contains(buf.String(), secretReply) {
				t.Errorf("the reply leaked: body %s log %s", rec.Body, buf)
			}
		})
	}
}

func TestTidyParsesTheReplyStrictly(t *testing.T) {
	big := `{"operations":[{"op":"keep","id":"a"}],"pad":"` + strings.Repeat("x", maxReply) + `"}`
	for name, tc := range map[string]struct {
		reply  string
		status int
	}{
		"a bare object":                  {`{"operations":[]}`, 200},
		"surrounding whitespace":         {"\n  {\"operations\":[]}\n", 200},
		"one code fence":                 {"```json\n{\"operations\":[]}\n```", 200},
		"one plain fence":                {"```\n{\"operations\":[]}\n```", 200},
		"prose before":                   {"Here you go: {\"operations\":[]}", 422},
		"prose after":                    {"{\"operations\":[]} done", 422},
		"fence with prose after":         {"```json\n{\"operations\":[]}\n``` thanks", 422},
		"two objects":                    {`{"operations":[]}{"operations":[]}`, 422},
		"two fenced objects":             {"```json\n{\"operations\":[]}\n{\"operations\":[]}\n```", 422},
		"trailing data":                  {`{"operations":[]} 1`, 422},
		"an unknown top-level key":       {`{"operations":[],"note":"x"}`, 422},
		"an unknown key in an operation": {`{"operations":[{"op":"keep","id":"a","why":"x"}]}`, 422},
		"a field the op does not take":   {`{"operations":[{"op":"keep","id":"a","to":"cat-2"}]}`, 422},
		"no operations key":              {`{}`, 422},
		"null operations":                {`{"operations":null}`, 422},
		"not an object":                  {`[{"op":"keep","id":"a"}]`, 422},
		"an unknown op":                  {`{"operations":[{"op":"rewrite","id":"a","text":"x"}]}`, 422},
		"merge of one":                   {`{"operations":[{"op":"merge","ids":["a"],"text":"x"}]}`, 422},
		"merge of eleven":                {`{"operations":[{"op":"merge","ids":["1","2","3","4","5","6","7","8","9","10","11"],"text":"x"}]}`, 422},
		"split of one":                   {`{"operations":[{"op":"split","id":"a","texts":["x"]}]}`, 422},
		"split of six":                   {`{"operations":[{"op":"split","id":"a","texts":["1","2","3","4","5","6"]}]}`, 422},
		"an empty text":                  {`{"operations":[{"op":"merge","ids":["a","b"],"text":""}]}`, 422},
		"a text over the limit":          {`{"operations":[{"op":"merge","ids":["a","b"],"text":"` + strings.Repeat("x", 16385) + `"}]}`, 422},
		"a move without a target":        {`{"operations":[{"op":"move","id":"a"}]}`, 422},
		"more operations than allowed":   {`{"operations":[` + strings.Repeat(`{"op":"keep","id":"a"},`, 80) + `{"op":"keep","id":"a"}]}`, 422},
		"an oversized reply":             {big, 422},
		"an empty reply":                 {``, 422},
	} {
		t.Run(name, func(t *testing.T) {
			fakeCall(t, reply(tc.reply))
			rec := post(t, validRequest())
			if rec.Code != tc.status {
				t.Errorf("= %d %.200s, want %d", rec.Code, rec.Body, tc.status)
			}
			if tc.status == 422 && errCode(t, rec) != "tidy_malformed" {
				t.Errorf("code = %s", errCode(t, rec))
			}
		})
	}
}

// The limits of the request apply to the reply: a request that allows fewer parts refuses a
// reply that a looser one takes.
func TestTidyChecksTheReplyAgainstTheRequestsLimits(t *testing.T) {
	r := `{"operations":[{"op":"split","id":"a","texts":["1","2","3"]}]}`
	fakeCall(t, reply(r))
	if rec := post(t, validRequest()); rec.Code != 200 {
		t.Fatalf("default limits = %d %s", rec.Code, rec.Body)
	}
	req := validRequest()
	req["limits"] = map[string]any{"mergeSources": 2, "splitParts": 2, "textBytes": 4, "operations": 1}
	if rec := post(t, req); rec.Code != 422 {
		t.Errorf("tight limits = %d %s, want 422", rec.Code, rec.Body)
	}
}

func TestTidyRefusesABadRequest(t *testing.T) {
	items := func(n int) []map[string]any {
		var out []map[string]any
		for i := 0; i < n; i++ {
			out = append(out, map[string]any{"id": fmt.Sprintf("m%d", i), "kind": "text", "text": "x", "file": "", "group": 0, "merge": true, "move": true})
		}
		return out
	}
	for name, edit := range map[string]func(map[string]any){
		"no run":                     func(r map[string]any) { r["run"] = "" },
		"a trigger it does not know": func(r map[string]any) { r["trigger"] = "cron" },
		"no queue id":                func(r map[string]any) { r["queue"] = map[string]any{"id": "", "label": "x"} },
		"no items":                   func(r map[string]any) { r["items"] = items(0) },
		"41 items":                   func(r map[string]any) { r["items"] = items(41) },
		"96 KiB and a byte":          func(r map[string]any) { it := items(1); it[0]["text"] = strings.Repeat("x", 96<<10+1); r["items"] = it },
		"text split over items": func(r map[string]any) {
			it := items(2)
			it[0]["text"] = strings.Repeat("x", 60<<10)
			it[1]["text"] = strings.Repeat("x", 40<<10)
			r["items"] = it
		},
		"a duplicate id":   func(r map[string]any) { it := items(2); it[1]["id"] = "m0"; r["items"] = it },
		"an unknown kind":  func(r map[string]any) { it := items(1); it[0]["kind"] = "image"; r["items"] = it },
		"an unknown field": func(r map[string]any) { r["instruction"] = "do as I say" },
		"limits over the ceiling": func(r map[string]any) {
			r["limits"] = map[string]any{"mergeSources": 11, "splitParts": 5, "textBytes": 16384, "operations": 80}
		},
		"limits of zero": func(r map[string]any) {
			r["limits"] = map[string]any{"mergeSources": 0, "splitParts": 0, "textBytes": 0, "operations": 0}
		},
		"a destination without an id": func(r map[string]any) { r["destinations"] = []map[string]any{{"id": "", "name": "n"}} },
	} {
		t.Run(name, func(t *testing.T) {
			calls := fakeCall(t, reply(`{"operations":[]}`))
			req := validRequest()
			edit(req)
			rec := post(t, req)
			if rec.Code != 400 || errCode(t, rec) != "bad_tidy_request" {
				t.Errorf("= %d %.200s", rec.Code, rec.Body)
			}
			if len(*calls) != 0 {
				t.Error("a refused request reached the model")
			}
		})
	}
	t.Run("exactly at the bounds", func(t *testing.T) {
		fakeCall(t, reply(`{"operations":[]}`))
		req := validRequest()
		it := items(40)
		it[0]["text"] = strings.Repeat("x", 96<<10-39)
		req["items"] = it
		if rec := post(t, req); rec.Code != 200 {
			t.Errorf("= %d %.200s", rec.Code, rec.Body)
		}
	})
	t.Run("not JSON", func(t *testing.T) {
		fakeCall(t, reply(`{"operations":[]}`))
		if rec := postRaw("not json"); rec.Code != 400 {
			t.Errorf("= %d", rec.Code)
		}
	})
}

// One tidy call at a time: while one holds the model, a second is refused without reaching it.
func TestTidyOneCallAtATime(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	calls := fakeCall(t, func(call) (chatx.DenyAllResult, error) {
		close(started)
		<-release
		return chatx.DenyAllResult{Reply: `{"operations":[]}`, Kind: "claude", Model: "m"}, nil
	})
	first := make(chan int)
	go func() { first <- post(t, validRequest()).Code }()
	<-started
	rec := post(t, validRequest())
	if rec.Code != http.StatusConflict || errCode(t, rec) != "tidy_busy" {
		t.Errorf("second = %d %s", rec.Code, rec.Body)
	}
	close(release)
	if code := <-first; code != 200 {
		t.Errorf("first = %d", code)
	}
	if len(*calls) != 1 {
		t.Errorf("the model was called %d times, want 1", len(*calls))
	}
	// The slot is free again.
	fakeCall(t, reply(`{"operations":[]}`))
	if rec := post(t, validRequest()); rec.Code != 200 {
		t.Errorf("after the first = %d %s", rec.Code, rec.Body)
	}
}

// A call that ends in an error frees the slot too.
func TestTidySlotIsFreedAfterAFailure(t *testing.T) {
	fakeCall(t, func(call) (chatx.DenyAllResult, error) { return chatx.DenyAllResult{}, errors.New("x") })
	if rec := post(t, validRequest()); rec.Code != 502 {
		t.Fatalf("= %d", rec.Code)
	}
	fakeCall(t, reply(`{"operations":[]}`))
	if rec := post(t, validRequest()); rec.Code != 200 {
		t.Errorf("next = %d %s", rec.Code, rec.Body)
	}
}
