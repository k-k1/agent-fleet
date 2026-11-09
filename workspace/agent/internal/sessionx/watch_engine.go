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
	"github.com/k-k1/agent-fleet/workspace/agent/internal/branchpr"
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
		gh.Threads = watchThreads
		bb := &watch.Bitbucket{Read: watchBitbucketPR}
		watchEngineInst = watch.New(watchDiskStore{}, newWatchSink(), gh, bb)
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

// watchThreads reads a PR's unresolved review threads through the session list's PR cache: the
// answer refreshes the row's entry too, so the list does not ask GitHub for the same PR again
// (ADR 0133 rule 6.2).
func watchThreads(ctx context.Context, repo string, number int) (int, error) {
	k := branchpr.Key{Repo: repo, Number: number}
	got, err := sessionPRs.Fetch(ctx, []branchpr.Key{k})
	switch {
	case branchpr.ErrNotConnected(err):
		return 0, &watch.ErrRefused{Reason: "GitHub is not connected (Settings > Connections)"}
	case err != nil:
		return 0, err
	}
	pr, ok := got[k]
	if !ok {
		return 0, errors.New("GitHub did not answer for the PR's review threads")
	}
	if pr == nil {
		// Not "no threads": GitHub could not show the PR, which must not reset the count.
		return 0, errors.New("GitHub did not return the PR for its review threads")
	}
	return pr.UnresolvedThreads, nil
}

// watchBitbucketPR reads one Bitbucket PR through the session list's Bitbucket cache, so a watch
// and the list's rows share one answer and one budget.
func watchBitbucketPR(ctx context.Context, repo string, number int) (*branchpr.PR, error) {
	k := branchpr.Key{Repo: repo, Number: number}
	got, err := sessionBBPRs.Fetch(ctx, []branchpr.Key{k})
	if err != nil {
		return nil, err
	}
	pr, ok := got[k]
	if !ok {
		return nil, errors.New("Bitbucket did not answer for the pull request this time")
	}
	return pr, nil
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

func (s *watchSink) Ready(name string, now time.Time) watch.Readiness {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := session.ReadMeta(name)
	if !ok {
		s.forgetLocked(name)
		return watch.Gone
	}
	if m.Archived || !peerTargetAllowed(NormalizeKind(m.Kind)) {
		// A watch a session made ends with that session; one that cannot take a prompt could
		// not have called watch. A built-in falls back to the member.
		s.forgetLocked(name)
		return watch.Unreachable
	}
	if !watchAlive(m) {
		s.wake(m, now)
		delete(s.readyOnce, name)
		return watch.Wait
	}
	if m.InitialPromptState == session.InitialPromptPending || watchBusy(m) || !watchReady(m) {
		delete(s.readyOnce, name)
		return watch.Wait
	}
	if s.waking == name {
		s.waking = ""
	}
	if !s.readyOnce[name] {
		s.readyOnce[name] = true
		return watch.Wait // the second look, a tick from now, sends it
	}
	// The last look before the engine claims the event: a turn may have started since.
	if fresh, ok := session.ReadMeta(name); !ok || fresh.Archived || !watchReady(fresh) {
		delete(s.readyOnce, name)
		return watch.Wait
	}
	delete(s.readyOnce, name) // its turn starts with this notice; the next one waits for two looks
	return watch.Ready
}

func (s *watchSink) Inject(sub watch.Subscription, evs []watch.Event) error {
	if len(evs) == 0 {
		return nil
	}
	name := sub.Owner
	var text string
	if sub.Builtin != "" {
		render, ok := builtinNoticeText[sub.Builtin]
		if !ok {
			return errors.New("no notice wording for built-in " + sub.Builtin)
		}
		text = render(sub, evs)
	} else {
		parts := make([]string, 0, len(evs))
		for _, ev := range evs {
			parts = append(parts, watch.Notice(sub, ev))
		}
		text = strings.Join(parts, "\n\n")
	}
	recordInjection(name, text, TurnSourceNotice)
	if err := watchInject(name, text, agents.Origin{Kind: agents.OriginNotice}); err != nil {
		return err
	}
	recordFleetGraphInstruct(name, TurnSourceNotice, "", "", text)
	log.Printf("watch: told %s about %s (%s)", name, evs[0].Subject, watchTypes(evs))
	return nil
}

// Notify puts a built-in's events into the member's notification outbox. Each purpose words its
// own notifications under once keys of its own, so a repeat after a released claim is harmless.
func (s *watchSink) Notify(sub watch.Subscription, evs []watch.Event) error {
	send, ok := builtinMemberNotice[sub.Builtin]
	if !ok {
		return errors.New("no member notification for built-in " + sub.Builtin)
	}
	return send(sub, evs)
}

// The wording of each built-in purpose: into the owner session, and for the member. Filled from
// init functions only and read-only afterwards, so the sink reads them without a lock.
var (
	builtinNoticeText   = map[string]func(watch.Subscription, []watch.Event) string{}
	builtinMemberNotice = map[string]func(watch.Subscription, []watch.Event) error{}
)

func watchTypes(evs []watch.Event) string {
	t := make([]string, 0, len(evs))
	for _, ev := range evs {
		t = append(t, ev.Type)
	}
	return strings.Join(t, ", ")
}

func (s *watchSink) Forget(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.forgetLocked(name)
}

// forgetLocked drops an owner's looks and, when it was the one being started, frees the start
// slot so the next stopped owner does not wait out watchWakeWait for nothing.
func (s *watchSink) forgetLocked(name string) {
	delete(s.readyOnce, name)
	delete(s.wokeAt, name)
	if s.waking == name {
		s.waking = ""
	}
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
	Notify         string   `json:"notify"`
	Ref            string   `json:"ref"`
	Types          []string `json:"types"`
	Until          string   `json:"until"`
	ExpiresInHours int      `json:"expires_in_hours"`
}

// HandleWatchCreate (POST /sessions/{name}/watches) subscribes the session to a subject. Like
// every /sessions/{name}/ route, it trusts the Agent token's holder to name itself; the MCP
// tools always pass the calling session's own name.
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
	// Checked before the multiplication: a huge value would overflow time.Duration into a short,
	// accepted lifetime.
	if req.ExpiresInHours < 0 || req.ExpiresInHours > int(watch.MaxLifetime/time.Hour) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_expiry", "expires_in_hours must be between 1 and 720")
		return
	}
	owner, requester := name, ""
	switch strings.TrimSpace(req.Notify) {
	case "", "self":
	case "parent":
		// The parent is the session that started this one; a handoff's predecessor is not.
		p := stalledTreeParent(m)
		if p == "" {
			httpx.WriteErr(w, http.StatusBadRequest, "no_parent", "notify parent needs a session started by another session")
			return
		}
		if pm, ok := session.ReadMeta(p); !ok || pm.Archived {
			httpx.WriteErr(w, http.StatusBadRequest, "no_parent", "the parent session "+p+" is gone")
			return
		}
		owner, requester = p, name
	default:
		httpx.WriteErr(w, http.StatusBadRequest, "bad_notify", "notify must be self or parent")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	sub, err := watchEngine().Subscribe(ctx, watch.Request{
		Owner: owner, Requester: requester, Source: src, Ref: strings.TrimSpace(req.Ref), DefaultRepo: linkRepoOf(m.Dir, src),
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

// watchingPR reports whether name watches the provider's PR "owner/repo#N" for typ, so the PR
// feedback watch does not tell it the same thing a second time.
func watchingPR(name, provider, linkKey, typ string) bool {
	return watchEngine().Watching(name, watch.SubjectID(provider+":"+strings.ToLower(linkKey)), typ)
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
