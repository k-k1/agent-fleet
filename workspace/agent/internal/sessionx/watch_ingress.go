package sessionx

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/cpurl"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/watch"
)

// Webhook ingress, Agent side (ADR 0133 §4). The Control Plane receives provider webhooks and
// sends this Agent a poke: (source, subject) and nothing else. The Agent registers the subjects
// of its live watches with the Control Plane and polls a poked subject at once with the member's
// own token, so every event still comes from a poll.

// Registration timing. The Control Plane expires a registration after an hour, so renewing every
// twenty minutes survives two lost renewals.
const (
	watchRegisterRenew = 20 * time.Minute
	watchRegisterRetry = time.Minute
	watchRegisterMax   = 200
)

var watchSubjectRE = regexp.MustCompile(`^(github|bitbucket):[a-z0-9._~-]{1,100}/[a-z0-9._~-]{1,100}#[1-9][0-9]{0,8}$`)

// watchRegistrar keeps the Control Plane's copy of the watched-subject set current. It sends the
// whole set (replace semantics), so a lost request, a Control Plane restart or a withdrawn last
// subscription all heal on the next send. The first call after a start always sends, even an
// empty set, which clears registrations a previous run left behind.
type watchRegistrar struct {
	mu       sync.Mutex
	last     string
	lastAt   time.Time
	retryAt  time.Time
	put      func(ctx context.Context, subjects []string) error
	subjects func() []string
}

// sync sends the set when it changed, when the renewal is due, or on the first call.
func (r *watchRegistrar) sync(ctx context.Context, now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if now.Before(r.retryAt) {
		return
	}
	subs := r.subjects()
	key := strings.Join(subs, "\n")
	if key == r.last && now.Sub(r.lastAt) < watchRegisterRenew { // lastAt is zero until the first send
		return
	}
	if err := r.put(ctx, subs); err != nil {
		r.retryAt = now.Add(watchRegisterRetry)
		return
	}
	r.last, r.lastAt = key, now
}

// watchSubjectList is the engine's watched subjects in the form the Control Plane accepts. An
// Actions subject is registered as its poke key (a workflow's floor stays here), and several
// watches of one workflow share one key, so the list is deduplicated.
func watchSubjectList(e *watch.Engine) []string {
	var out []string
	seen := map[string]bool{}
	for _, s := range e.Subjects() {
		key := string(s.Subject)
		switch s.Source {
		case watch.ActionsKind:
			k, ok := watch.ActionsPokeKey(s.Subject)
			if !ok {
				continue
			}
			key = k
		default:
			if !watchSubjectRE.MatchString(key) {
				continue
			}
		}
		if seen[key] || len(out) >= watchRegisterMax {
			continue
		}
		seen[key] = true
		out = append(out, key)
	}
	return out
}

// putWatchSubjects is PUT /internal/watch-subjects. Without the bridge (no public URL or no
// token) it does nothing, as the other bridges do.
func putWatchSubjects(ctx context.Context, subjects []string) error {
	base, token := cpurl.Request(), os.Getenv("AF_WATCH_TOKEN")
	if base == "" || token == "" {
		return nil
	}
	if subjects == nil {
		subjects = []string{}
	}
	body, _ := json.Marshal(map[string][]string{"subjects": subjects})
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, base+"/internal/watch-subjects", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		return fmt.Errorf("control plane answered %d", resp.StatusCode)
	}
	return nil
}

var (
	watchRegistrarOnce sync.Once
	watchRegistrarInst *watchRegistrar
)

func watchSubjectRegistrar() *watchRegistrar {
	watchRegistrarOnce.Do(func() {
		watchRegistrarInst = &watchRegistrar{put: putWatchSubjects, subjects: func() []string { return watchSubjectList(watchEngine()) }}
	})
	return watchRegistrarInst
}

// HandleWatchPoke (POST /watch/poke) is the Control Plane's poke. The body names a source and a
// subject and is trusted for nothing else: an unknown subject is simply not held, and a held one
// is read sooner by the engine, never delivered from the poke.
func HandleWatchPoke(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	var in struct {
		Source  string `json:"source"`
		Subject string `json:"subject"`
	}
	if !httpx.DecodeJSON(w, r, &in) {
		return
	}
	// The Control Plane names the provider ("github"); a key in an Actions form belongs to the
	// Actions source, which the engine keeps apart from the pull request source.
	var held bool
	switch {
	case watchSubjectRE.MatchString(in.Subject) && strings.HasPrefix(in.Subject, in.Source+":"):
		held = watchEngine().Poke(in.Source, watch.SubjectID(in.Subject))
	case in.Source == "github" && watch.IsActionsPokeKey(in.Subject):
		held = watchEngine().PokeMatching(watch.ActionsKind, func(s watch.SubjectID) bool {
			k, ok := watch.ActionsPokeKey(s)
			return ok && k == in.Subject
		})
	default:
		httpx.WriteErr(w, http.StatusBadRequest, "bad_subject", "source and subject do not match a pull request or workflow run reference")
		return
	}
	httpx.WriteJSON(w, http.StatusAccepted, watchPokeResp{Held: held})
}

// watchPokeResp is POST /watch/poke's answer.
type watchPokeResp struct {
	Held bool `json:"held"`
}
