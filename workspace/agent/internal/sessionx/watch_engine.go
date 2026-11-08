package sessionx

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/fstore"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/watch"
)

// The watch engine of ADR 0133 as the Agent runs it: one engine, its state on disk, the
// github source, and a sink that injects notices into the owning session the way the PR
// feedback watch does (idle and ready on two looks, a stopped owner started one at a time).

// watchInterval is the engine's tick. Reads are scheduled per subject by the source's hints; a
// tick only finds the ones that are due, so a short tick costs no requests.
var watchInterval = 5 * time.Second

// Waits for starting a stopped owner, as the PR feedback watch uses them.
const (
	watchWakeWait  = 5 * time.Minute
	watchWakeRetry = 15 * time.Minute
)

var watchStateStore = fstore.JSON[watch.State](paths.AgentStateDir, "watch", ".json")

const watchStateKey = "state"

type watchDiskStore struct{}

func (watchDiskStore) Load() (watch.State, bool) { return watchStateStore.Read(watchStateKey) }
func (watchDiskStore) Save(s watch.State) error  { return watchStateStore.Write(watchStateKey, s) }

// Side effects, replaceable by tests.
var (
	watchAlive  = SessionAlive
	watchReady  = func(m session.Meta) bool { ready, _ := peerDeliveryReady(m); return ready }
	watchBusy   = managedBusy
	watchInject = injectSessionPrompt
	watchStart  = startSessionInProcess
)

var (
	watchEngineOnce sync.Once
	watchEngineInst *watch.Engine
)

// watchEngine is the Agent's engine, built on first use.
func watchEngine() *watch.Engine {
	watchEngineOnce.Do(func() {
		gh := watch.NewGitHub(githubToken, func(rejected string) (string, error) {
			return gitx.RenewRejectedGitHubToken(context.Background(), rejected)
		})
		watchEngineInst = watch.New(watchDiskStore{}, newWatchSink(), gh)
	})
	return watchEngineInst
}

// StartWatchEngine runs the engine for the life of the agent.
func StartWatchEngine() {
	go func() {
		time.Sleep(40 * time.Second) // after tmux and the drivers are up
		for {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			watchEngine().Tick(ctx)
			cancel()
			time.Sleep(watchInterval)
		}
	}()
}

// watchSink delivers into the owning session.
type watchSink struct {
	mu        sync.Mutex
	readyOnce map[string]bool // owners that looked ready on the previous look
	waking    string
	wakingAt  time.Time
	wokeAt    map[string]time.Time
}

func newWatchSink() *watchSink {
	return &watchSink{readyOnce: map[string]bool{}, wokeAt: map[string]time.Time{}}
}

func (s *watchSink) OwnerExists(owner string) bool {
	m, ok := session.ReadMeta(owner)
	return ok && !m.Archived
}

func (s *watchSink) Deliver(sub watch.Subscription, ev watch.Event, now time.Time) watch.Delivery {
	s.mu.Lock()
	defer s.mu.Unlock()
	name := sub.Owner
	m, ok := session.ReadMeta(name)
	if !ok || m.Archived || !peerTargetAllowed(NormalizeKind(m.Kind)) {
		// A watch a session made ends with that session; one that cannot take a prompt could
		// not have called watch.
		return watch.Gone
	}
	if !watchAlive(m) {
		s.wake(m, now)
		delete(s.readyOnce, name)
		return watch.NotYet
	}
	if m.InitialPromptState == session.InitialPromptPending || watchBusy(m) || !watchReady(m) {
		delete(s.readyOnce, name)
		return watch.NotYet
	}
	if s.waking == name {
		s.waking = ""
	}
	if !s.readyOnce[name] {
		s.readyOnce[name] = true
		return watch.NotYet // the second look, a tick from now, sends it
	}
	if fresh, ok := session.ReadMeta(name); !ok || fresh.Archived || !watchReady(fresh) {
		delete(s.readyOnce, name)
		return watch.NotYet
	}
	text := watch.Notice(sub, ev)
	recordInjection(name, text, TurnSourceNotice)
	if err := watchInject(name, text, agents.Origin{Kind: agents.OriginNotice}); err != nil {
		log.Printf("watch: could not tell %s about %s: %v", name, ev.Subject, err)
		return watch.NotYet
	}
	delete(s.readyOnce, name) // its turn starts now
	recordFleetGraphInstruct(name, TurnSourceNotice, "", "", text)
	log.Printf("watch: told %s about %s (%s)", name, ev.Subject, ev.Type)
	return watch.Delivered
}

// wake starts a stopped owner so it can be told: one start at a time across the Agent, never the
// same owner twice within watchWakeRetry.
func (s *watchSink) wake(m session.Meta, now time.Time) {
	if s.waking != "" && s.waking != m.Name && now.Sub(s.wakingAt) < watchWakeWait {
		return
	}
	if at, ok := s.wokeAt[m.Name]; ok && now.Sub(at) < watchWakeRetry {
		return
	}
	s.waking, s.wakingAt = m.Name, now
	s.wokeAt[m.Name] = now
	for n, at := range s.wokeAt {
		if now.Sub(at) >= watchWakeRetry {
			delete(s.wokeAt, n)
		}
	}
	if err := watchStart(m.Name); err != nil {
		log.Printf("watch: could not start %s to deliver a watch event: %v", m.Name, err)
		s.waking = ""
		return
	}
	log.Printf("watch: started %s to deliver a watch event", m.Name)
}

// watchReq is POST /sessions/{name}/watches.
type watchReq struct {
	Source         string   `json:"source"`
	Ref            string   `json:"ref"`
	Types          []string `json:"types"`
	Until          string   `json:"until"`
	ExpiresInHours int      `json:"expires_in_hours"`
}

// HandleWatchCreate (POST /sessions/{name}/watches) subscribes the session to a subject.
func HandleWatchCreate(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	m, ok := watchOwner(w, name)
	if !ok {
		return
	}
	var req watchReq
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	src := strings.ToLower(strings.TrimSpace(req.Source))
	if src == "" {
		src = "github"
	}
	if req.ExpiresInHours < 0 {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_expiry", "expires_in_hours must be positive")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	sub, err := watchEngine().Subscribe(ctx, watch.Request{
		Owner: name, Source: src, Ref: strings.TrimSpace(req.Ref), DefaultRepo: linkRepoOf(m.Dir, src),
		Types: req.Types, Until: strings.TrimSpace(req.Until), Lifetime: time.Duration(req.ExpiresInHours) * time.Hour,
	})
	if err != nil {
		var ref *watch.ErrRefused
		if errors.As(err, &ref) {
			httpx.WriteErr(w, http.StatusBadRequest, "refused", ref.Reason)
			return
		}
		httpx.WriteErr(w, http.StatusBadGateway, "source_failed", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, watchCreateResp{Watch: sub})
}

// HandleWatchList (GET /sessions/{name}/watches) lists the session's watches.
func HandleWatchList(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := watchOwner(w, name); !ok {
		return
	}
	httpx.WriteJSON(w, http.StatusOK, watchListResp{Watches: watchEngine().List(name)})
}

// HandleWatchDelete (DELETE /sessions/{name}/watches/{id}) ends one of the session's watches.
func HandleWatchDelete(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := watchOwner(w, name); !ok {
		return
	}
	id := r.PathValue("id")
	if !watchEngine().Unsubscribe(name, id) {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "this session has no watch "+id)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, watchDeleteResp{Ended: id})
}

// watchCreateResp is POST /sessions/{name}/watches's answer.
type watchCreateResp struct {
	Watch watch.Subscription `json:"watch"`
}

// watchListResp is GET /sessions/{name}/watches's answer.
type watchListResp struct {
	Watches []watch.Listed `json:"watches"`
}

// watchDeleteResp is DELETE /sessions/{name}/watches/{id}'s answer.
type watchDeleteResp struct {
	Ended string `json:"ended"`
}

func watchOwner(w http.ResponseWriter, name string) (session.Meta, bool) {
	if !session.ValidName(name) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_name", "invalid session name")
		return session.Meta{}, false
	}
	m, ok := session.ReadMeta(name)
	if !ok || m.Archived {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "no such session: "+name)
		return session.Meta{}, false
	}
	return m, true
}
