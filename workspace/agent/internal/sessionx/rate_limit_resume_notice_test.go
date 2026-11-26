package sessionx

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/uiprefs"
)

// The resume reaches the session behind the notice line, in both locales and whether the session
// was started by its user or by a message queue (whose first prompt is a queue envelope): without
// the line the nudge reads as the member's own instruction.
func TestRateLimitResumeInputCarriesTheNotice(t *testing.T) {
	const notice = "[agent-fleet:notice kind=limit-resume reply=none] "
	for _, tc := range []struct {
		name   string
		queued bool
		locale string
	}{
		{"user session ja", false, "ja"}, {"user session en", false, "en"},
		{"queue session ja", true, "ja"}, {"queue session en", true, "en"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, m := useOriginFake(t, "resume_dst")
			if tc.queued {
				m.Origin, m.QueueCategoryID = session.OriginQueue, "cat-1"
				session.WriteMeta(m)
			}
			prompt := rateLimitResumePromptFor(tc.locale)
			body, _ := json.Marshal(map[string]any{"prompt": prompt, "source": "schedule"})
			if rec := postInput(t, "resume_dst", string(body)); rec.Code != http.StatusOK {
				t.Fatalf("status %d %s", rec.Code, rec.Body.String())
			}
			got := h.next(t).Prompt
			if !strings.HasPrefix(got, notice) || got != prompt {
				t.Fatalf("injected %q, want the notice line then the resume wording", got)
			}
			if !isRateLimitResumePrompt(got) {
				t.Fatal("the Agent no longer recognises its own resume")
			}
		})
	}
}

// Wordings booked before the notice line still count as a resume (a booking spans an upgrade).
func TestRateLimitResumeRecognisesTheUnprefixedWording(t *testing.T) {
	for _, l := range []string{"ja", "en"} {
		if !isRateLimitResumePrompt(rateLimitResumeBodyFor(l)) {
			t.Errorf("%s: the headerless wording is not recognised", l)
		}
	}
}

// A CP without the dedicated route answers 404 or 405 for it; the booking then goes the old way.
// Any other failure is not retried on the generic route, which could create a second row.
func TestCreateRateLimitScheduleFallsBackOnlyWhenTheRouteIsAbsent(t *testing.T) {
	m := rlMeta()
	for _, tc := range []struct {
		name      string
		status    int
		wantPaths []string
	}{
		{"route absent 404", 404, []string{"/internal/schedules/limit-resume", "/internal/schedules"}},
		{"route absent 405", 405, []string{"/internal/schedules/limit-resume", "/internal/schedules"}},
		{"server error", 500, []string{"/internal/schedules/limit-resume"}},
		{"forbidden", 403, []string{"/internal/schedules/limit-resume"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var paths []string
			var legacy map[string]any
			prev := scheduleDo
			t.Cleanup(func() { scheduleDo = prev })
			scheduleDo = func(method, path string, body []byte) (string, error) {
				paths = append(paths, path)
				if path == "/internal/schedules/limit-resume" {
					return "", &mcpx.CPScheduleError{Status: tc.status}
				}
				_ = json.Unmarshal(body, &legacy)
				return `{"id":"sch_1"}`, nil
			}
			id, err := createRateLimitSchedule(m, time.Now().Add(time.Hour))
			if strings.Join(paths, ",") != strings.Join(tc.wantPaths, ",") {
				t.Fatalf("calls %v, want %v", paths, tc.wantPaths)
			}
			if len(tc.wantPaths) == 2 {
				if err != nil || id != "sch_1" || legacy["reuse_target"] != m.Name || legacy["session_mode"] != "reuse" ||
					legacy["prompt"] != rateLimitResumeBodyFor(uiprefs.Locale()) || !isRateLimitResumePrompt(legacy["prompt"].(string)) {
					t.Fatalf("fallback id=%q err=%v body=%v", id, err, legacy)
				}
			} else if err == nil {
				t.Fatal("a failure other than a missing route was swallowed")
			}
		})
	}
}
