package sessionx

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/watch"
)

type cpCall struct{ method, path, body string }

func fakeCP(t *testing.T, answer func(c cpCall) (string, error)) *[]cpCall {
	t.Helper()
	var calls []cpCall
	prev := cpWakeDo
	cpWakeDo = func(method, path string, body []byte) (string, error) {
		c := cpCall{method, path, string(body)}
		calls = append(calls, c)
		return answer(c)
	}
	t.Cleanup(func() { cpWakeDo = prev })
	return &calls
}

func TestCPWakerBooksAndCancelsOnTheWakeRoutes(t *testing.T) {
	calls := fakeCP(t, func(c cpCall) (string, error) {
		if c.method == http.MethodPost {
			return `{"id":"sch_x"}`, nil
		}
		return "", &mcpx.CPScheduleError{Status: 404, Body: `{}`}
	})
	at := time.Date(2026, 11, 9, 15, 0, 0, 0, time.FixedZone("JST", 9*3600))
	id, err := cpWaker{}.Book(context.Background(), at)
	if err != nil || id != "sch_x" {
		t.Fatalf("Book = %q, %v", id, err)
	}
	if c := (*calls)[0]; c.path != "/internal/wakes" || c.body != `{"at":"2026-11-09T06:00:00Z"}` {
		t.Fatalf("booking request = %+v (the instant must be sent in UTC, to the wake route and not /internal/schedules)", c)
	}
	if err := (cpWaker{}).Cancel(context.Background(), "sch_x"); err != nil {
		t.Fatalf("a 404 is the booking already gone: %v", err)
	}
	if c := (*calls)[1]; c.method != http.MethodDelete || c.path != "/internal/wakes/sch_x" {
		t.Fatalf("cancel request = %+v", c)
	}
}

func TestCPWakerCancelKeepsOtherFailures(t *testing.T) {
	for _, status := range []int{500, 502, 401, 403} {
		fakeCP(t, func(cpCall) (string, error) { return "", &mcpx.CPScheduleError{Status: status, Body: "x"} })
		if err := (cpWaker{}).Cancel(context.Background(), "sch_x"); err == nil {
			t.Errorf("status %d was read as cancelled; the booking may still be there", status)
		}
	}
	fakeCP(t, func(cpCall) (string, error) { return "", context.DeadlineExceeded })
	if err := (cpWaker{}).Cancel(context.Background(), "sch_x"); err == nil {
		t.Error("a timeout was read as cancelled")
	}
}

func TestCPWakerBookRefusalsAreRefusals(t *testing.T) {
	fakeCP(t, func(cpCall) (string, error) {
		return "", &mcpx.CPScheduleError{Status: 409, Body: `{"error":{"code":"scheduler_disabled","message":"the Control Plane scheduler is not running on this deployment, so a wake would never fire"}}`}
	})
	_, err := cpWaker{}.Book(context.Background(), time.Now().Add(time.Hour))
	var rf *watch.ErrRefused
	if !asRefused(err, &rf) || !strings.Contains(rf.Reason, "scheduler is not running") {
		t.Fatalf("err = %v", err)
	}
	fakeCP(t, func(cpCall) (string, error) { return "", &mcpx.CPScheduleError{Status: 502, Body: "bad gateway"} })
	if _, err := (cpWaker{}).Book(context.Background(), time.Now().Add(time.Hour)); err == nil || asRefused(err, &rf) {
		t.Fatalf("a Control Plane outage is not a refusal of the watch: %v", err)
	}
}

func asRefused(err error, out **watch.ErrRefused) bool {
	for err != nil {
		if r, ok := err.(*watch.ErrRefused); ok {
			*out = r
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

func TestWatchRoutesTimeWatchWithWake(t *testing.T) {
	newWatchFixture(t)
	session.WriteMeta(session.Meta{Name: "a", Kind: session.KindClaude, Dir: t.TempDir()})
	eng := watch.New(watchDiskStore{}, newWatchSink(), &watch.Time{})
	eng.SetWaker(cpWaker{})
	useWatchEngine(t, eng)
	live := map[string]bool{}
	calls := fakeCP(t, func(c cpCall) (string, error) {
		switch {
		case c.method == http.MethodPost:
			live["sch_1"] = true
			return `{"id":"sch_1"}`, nil
		default:
			delete(live, strings.TrimPrefix(c.path, "/internal/wakes/"))
			return `{}`, nil
		}
	})
	mux := http.NewServeMux()
	mux.HandleFunc("POST /sessions/{name}/watches", HandleWatchCreate)
	mux.HandleFunc("GET /sessions/{name}/watches", HandleWatchList)
	mux.HandleFunc("DELETE /sessions/{name}/watches/{id}", HandleWatchDelete)
	do := func(method, path, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
		return rec
	}

	rec := do("POST", "/sessions/a/watches", `{"source":"time","ref":"in 2h","wake":true}`)
	if rec.Code != 200 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	var created struct{ Watch watch.Subscription }
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	if !created.Watch.Wake || created.Watch.Due.IsZero() || !live["sch_1"] {
		t.Fatalf("created = %+v live = %v (the wake flag must reach the engine)", created.Watch, live)
	}
	if rec := do("GET", "/sessions/a/watches", ""); !strings.Contains(rec.Body.String(), `"due"`) {
		t.Fatalf("list_watches does not show the timer: %s", rec.Body)
	}
	if rec := do("DELETE", "/sessions/a/watches/"+created.Watch.ID, ""); rec.Code != 200 {
		t.Fatalf("delete: %d", rec.Code)
	}
	eng.Tick(context.Background())
	if live["sch_1"] {
		t.Fatalf("unwatch left the booking on the Control Plane (calls %+v)", *calls)
	}
}

// A Control Plane that predates /internal/wakes answers 404: that is an outage of the feature, not
// a verdict on the watch.
func TestCPWakerBookOn404IsNotARefusal(t *testing.T) {
	fakeCP(t, func(cpCall) (string, error) {
		return "", &mcpx.CPScheduleError{Status: 404, Body: "404 page not found"}
	})
	var rf *watch.ErrRefused
	if _, err := (cpWaker{}).Book(context.Background(), time.Now().Add(time.Hour)); err == nil || asRefused(err, &rf) {
		t.Fatalf("err = %v", err)
	}
}
