package sessionx

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/chatx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/uiprefs"
)

// createRun is one create as it passes through createSession's stages, in order. Each stage
// either moves the create on or writes its refusal and returns false; the caller then returns
// at once. The order is the contract (session_create_stages_test.go pins it), not the layout.
type createRun struct {
	// w is the response writer. Once claim owns an idempotency key it is the outcome writer, so
	// every answer after the claim carries the outcome headers.
	w   http.ResponseWriter
	r   *http.Request
	req *CreateReq

	// spawnParent names the session that raised this create (ADR 0073), "" for every other route.
	spawnParent string
	// idemKey is the idempotency key this create owns ("" when nothing is de-duplicated), and
	// committed says its answer has been recorded under it.
	idemKey   string
	committed bool

	// slot is this create's claim on one of the parent's child slots. Empty for every route
	// that is not a spawn, and then every method is a plain meta write.
	slot *spawnSlot
	// title is the cleaned request title, and driver the validated driver ("" = tui).
	title  string
	driver string
	// subdir is the cleaned subdir, checked to exist beneath the resolved working copy.
	subdir string

	// Stage 7's answers: who the session is.
	name                              string
	kind                              string
	label                             string
	ssm                               *session.SSMMeta
	studio                            string
	origin, originConv, originSession string
}

// validateKind is stage 1. It runs first of all, ahead of the idempotency claim: a refused kind
// must leave no ledger entry, and every guard below goes through NormalizeKind, which would turn
// it into claude.
func (c *createRun) validateKind() bool {
	if err := ValidateCreateKind(c.req.Kind); err != nil {
		httpx.WriteErr(c.w, http.StatusBadRequest, "bad_kind", err.Error())
		return false
	}
	return true
}

// claim is stage 2, the idempotency guard (session_idempotency.go): collapse a retried or
// concurrent create onto the first one so a client that timed out mid-launch can't spawn a
// duplicate. A duplicate is answered here from the ledger. When claim returns true and owns a
// key, the caller defers settleClaim.
func (c *createRun) claim() bool {
	// A create raised by another SESSION (ADR 0073) carries the parent's name, and the whole
	// spawn regime hangs off it. Resolved here; the refusals run further down.
	if origin, _, parent := CreateOrigin(c.req); origin == session.OriginSession {
		c.spawnParent = parent
		// The child has to be able to tell this instruction from one its user typed
		// (ADR 0073 decision 14). Applied before the idempotency key is derived, so the key
		// covers what is actually delivered, and here rather than in the calling tool so it
		// cannot be omitted or forged.
		c.req.InitialPrompt = SpawnEnvelope(parent, c.req.InitialPrompt)
	}
	idemKey := createIdempotencyKey(c.req)
	if idemKey == "" {
		return true
	}
	durable := c.req.IdempotencyDurable && strings.TrimSpace(c.req.IdempotencyKey) != ""
	if prev, dup := createLedger.begin(idemKey, durable); dup {
		writeLedgerAnswer(c.w, idemKey, prev)
		return false
	}
	c.idemKey = idemKey
	// This request owns the key, so its answer is the outcome under it: say so in headers
	// a proxy in between would not write (createOutcomeWriter).
	c.w = &createOutcomeWriter{ResponseWriter: c.w, key: idemKey}
	return true
}

// writeLedgerAnswer answers a create whose key the ledger already holds.
func writeLedgerAnswer(w http.ResponseWriter, idemKey string, prev createLedgerEntry) {
	switch prev.state {
	case createDone:
		// The identical create already succeeded — replay that session verbatim.
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set(createKeyHeader, idemKey)
		w.Header().Set(createOutcomeHeader, "created")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(prev.body)
	case createAbandoned:
		// The caller gave this key up (abandon) and may have started over.
		httpx.WriteErr(w, http.StatusConflict, "create_abandoned", "this create was abandoned by its caller")
	case createRefused:
		// A refused durable key stays refused, so this answer is as true as the first.
		w.Header().Set(createKeyHeader, idemKey)
		w.Header().Set(createOutcomeHeader, "refused")
		httpx.WriteErr(w, http.StatusConflict, "create_refused", "a create under this key was refused; start over under a new key")
	case createUnknown:
		httpx.WriteErr(w, http.StatusServiceUnavailable, "create_outcome_unknown", "the outcome of an earlier create under this key is unknown")
	default:
		// The first create is still launching (the timed-out client's retry, or a
		// genuine double-fire). Refuse with a distinct code the MCP tool reconciles
		// into the eventual session rather than surfacing as a retryable failure.
		httpx.WriteErr(w, http.StatusConflict, "create_in_progress",
			"同じ内容のセッションを作成中です。完了までお待ちください")
	}
}

// settleClaim clears the key this create owns on any exit that did not commit, so a real
// failure doesn't wedge the key (a completed entry is kept for replay by ledger.fail).
func (c *createRun) settleClaim() {
	if c.idemKey != "" && !c.committed {
		createLedger.fail(c.idemKey)
	}
}

// writeCreated finalizes a successful launch: cache the session for idempotent replay, mark the
// key committed (so settleClaim won't clear it), then write the response.
func (c *createRun) writeCreated(m session.Meta) {
	body := wireSession(m, true)
	if c.idemKey != "" {
		if b, err := json.Marshal(body); err == nil {
			createLedger.complete(c.idemKey, b)
			c.committed = true
		}
	}
	httpx.WriteJSON(c.w, http.StatusCreated, body)
}

// refuseStart is stage 3: the refusals that answer from state that changes, so they run after
// the claim and before any side effect.
func (c *createRun) refuseStart() bool {
	c.slot = &spawnSlot{}
	// A kind whose CLI failed to install at start and is still absent cannot launch: say why
	// (typically a full home) instead of the generic launch failure further down. After the
	// idempotency claim, like the spawn refusals below and for the same reason: the answer
	// depends on state that changes, so a retry of a create that already succeeded must replay
	// its session (the ledger answered in claim), and a durable key must end as refused through
	// the outcome writer and settleClaim.
	if ref := startRefusal(c.req.Kind); ref != nil {
		httpx.WriteErr(c.w, ref.Status, ref.Code, ref.Message)
		return false
	}
	// The spawn refusals (ADR 0073) run AFTER the idempotency claim and before any side effect
	// (clone / worktree), so a refused spawn leaves nothing behind.
	//
	// After the claim, because they must not answer a RETRY. A client whose create timed out
	// re-sends the same request, and by then the first child exists: run the budget first and
	// the retry is refused as "you already have three" instead of being replayed the session it
	// actually created. The same for a create still in flight — the ledger answers that with
	// create_in_progress, which the calling tool reconciles into the eventual session.
	//
	// The working-copy check is not here: it needs the RESOLVED dir (stage 6), not the raw one.
	if c.spawnParent == "" {
		return true
	}
	if ref := SpawnCreateRefusal(c.spawnParent, c.req.Kind); ref != nil {
		httpx.WriteErr(c.w, ref.Status, ref.Code, ref.Message)
		return false
	}
	if err := reserveSpawnSlot(c.spawnParent); err != nil {
		httpx.WriteErr(c.w, http.StatusConflict, "spawn_budget", err.Error())
		return false
	}
	// The slot is handed over to the child's meta as that meta is written (spawnSlot.publish
	// — the two are one atomic step). The caller's deferred release is the failure net.
	c.slot = &spawnSlot{parent: c.spawnParent}
	return true
}

// validateOptions is stage 4's first half: the request fields that need no state at all.
func (c *createRun) validateOptions() bool {
	title, ok := CleanTitle(c.req.Title)
	if !ok {
		httpx.WriteErr(c.w, http.StatusBadRequest, "bad_title", "title is too long (max 80) or contains control characters")
		return false
	}
	c.title = title
	if c.req.Mode != "" && c.req.Mode != "normal" && c.req.Mode != "plan" {
		httpx.WriteErr(c.w, http.StatusBadRequest, "bad_mode", `mode must be "plan" or "normal"`)
		return false
	}
	if c.req.SpendCapUSD != nil {
		if _, ok := session.NormalizeSpendCap(*c.req.SpendCapUSD); !ok {
			httpx.WriteErr(c.w, http.StatusBadRequest, "bad_spend_cap", spendCapRangeMsg)
			return false
		}
	}
	// "Ask for tool approval" is only accepted for kinds whose pending approvals can be
	// answered from the Console (docs/log/76). Ignoring it silently would leave the caller
	// believing the session runs with approvals on, so refusing is the honest answer.
	// Skipping (true) is the historical default and stays allowed for every kind.
	if c.req.SkipPermissions != nil && !*c.req.SkipPermissions && !AgentOf(NormalizeKind(c.req.Kind)).Caps().PermissionChoice {
		httpx.WriteErr(c.w, http.StatusBadRequest, "permission_choice_unsupported",
			"this agent kind cannot surface tool approvals to the Console; skip_permissions must stay true")
		return false
	}
	return true
}

// resolveDriver is stage 4's second half: driver validation up-front, before any side effect
// (clone / worktree). The default tui is normalized to "" when persisted, keeping metas
// byte-identical to existing ones — except for a kind with no Terminal(CLI) route at all
// (ADR 0093 decision 2), where an unspecified driver has to default to managed instead: leaving
// it "" would silently try to launch a tui session with no pane program.
func (c *createRun) resolveDriver() bool {
	driver := strings.TrimSpace(c.req.Driver)
	if driver == "" && AgentOf(NormalizeKind(c.req.Kind)).Caps().ManagedOnly {
		driver = session.DriverManaged
	}
	switch driver {
	case "", session.DriverTUI:
		driver = ""
	case session.DriverManaged:
		// docs/log/27 P2/P3: only kinds registered in driverOf (opencode / codex); claude
		// is out of scope (ADR 0015). A raw req.Kind can still turn into claude through
		// NormalizeKind further down, so never decide on it — normalize here and keep
		// using the normalized value.
		if _, ok := managedDrivers[NormalizeKind(c.req.Kind)]; !ok {
			httpx.WriteErr(c.w, http.StatusBadRequest, "driver_unsupported",
				"managed ドライバはこの kind では利用できません")
			return false
		}
	default:
		httpx.WriteErr(c.w, http.StatusBadRequest, "bad_driver", "unknown driver: "+c.req.Driver)
		return false
	}
	c.driver = driver
	return true
}

// checkModel is stage 5: the model checks, all before any side effect (clone / worktree). It
// leaves c.req.Model resolved against the kind's live catalog where there is one.
func (c *createRun) checkModel() bool {
	// lcpp can be turned off by the user's own display setting (ui-prefs lcppEnabled,
	// docs/log/105 §106.2). registry.ts's available() is only the signpost that hides it from
	// the launch menus; this is the actual gate, reached the same way as the model-hidden
	// guard below (Console, the schedule, MCP create_session, or a direct POST /sessions all
	// funnel through here) so turning lcpp off cannot be bypassed by any of them.
	if kind := NormalizeKind(c.req.Kind); kind == session.KindLcpp && !uiprefs.LcppEnabled() {
		httpx.WriteErr(c.w, http.StatusForbidden, "lcpp_disabled",
			"llama.cpp は設定でオフになっています。設定 > エージェント > llama.cpp でオンにしてください。")
		return false
	}
	// lcpp has no CLI-picked "own default" the way codex/opencode do. This is not because
	// llama-server itself demands a model name on every request — measured live
	// (docs/log/107's 2026-09-21 addendum), a single-model instance ignores the request's own
	// `model` field entirely (any string, or none, answers 200) — it is the Agent's own
	// requirement: a router deployment (`--models-max`, ADR 0093 decision 7's own measured
	// `role: "router"`) DOES dispatch on this field, the id pins the session to one entry of
	// the model list the launch menu itself is built from (there is no vendor "tier alias" the
	// way claude has), and it is the Console's only record of which model a conversation
	// believes it is talking to (engines.go's lcppMemberReachable doc comment: a member
	// swapping the LAN box under an unchanged URL is served by whatever loaded now, silently,
	// unless something recorded the name it meant to use). So unlike every other kind,
	// req.Model=="" is never a valid launch here, and letting it through used to reach worktree
	// creation and the managed driver before failing, with the session left holding only the
	// user's own first message (docs/log/109). Refused up front, before any side effect, the
	// same place the disabled/hidden-model guards already sit.
	if kind := NormalizeKind(c.req.Kind); kind == session.KindLcpp && strings.TrimSpace(c.req.Model) == "" {
		httpx.WriteErr(c.w, http.StatusBadRequest, "bad_model",
			"llama.cpp セッションにはモデルの指定が必須です（CLI 自身の既定は存在しません）")
		return false
	}
	// Models the user disabled (ui-prefs hiddenModels — model_deny.go) are refused for every
	// kind, before any side effect (clone / worktree). This create is reached not only from
	// the Console launch flow but from the schedule (CP scheduler) and MCP create_session,
	// and narrowing the catalog alone lets an explicit id through, so this guard is the
	// real one.
	if kind := NormalizeKind(c.req.Kind); ModelHidden(kind, c.req.Model) {
		httpx.WriteErr(c.w, http.StatusBadRequest, "model_hidden", hiddenModelError(strings.TrimSpace(c.req.Model)))
		return false
	}
	// For kinds with a live catalog, drop the hidden models from the candidate set too:
	// resolveLiveModel expands an unambiguous short name into the full id, so without that
	// a short name like "fab" would resolve to an excluded model.
	if NormalizeKind(c.req.Kind) == session.KindCodex && strings.TrimSpace(c.req.Model) != "" {
		model, err := resolveLiveModel(c.req.Model, FilterVisibleModels(session.KindCodex, codexModelsFn()))
		if err != nil {
			httpx.WriteErr(c.w, http.StatusBadRequest, "bad_model", err.Error())
			return false
		}
		c.req.Model = model
	} else if NormalizeKind(c.req.Kind) == session.KindCopilot && strings.TrimSpace(c.req.Model) != "" {
		model, err := resolveLiveModel(c.req.Model, FilterVisibleModels(session.KindCopilot, copilotModelsFn()))
		if err != nil {
			httpx.WriteErr(c.w, http.StatusBadRequest, "bad_model", err.Error())
			return false
		}
		c.req.Model = model
	} else if NormalizeKind(c.req.Kind) == session.KindOpencode && strings.TrimSpace(c.req.Model) != "" {
		ids := VisibleModelIDs(session.KindOpencode, opencodeModelsFn())
		choices := make([]agents.ModelChoice, 0, len(ids))
		for _, id := range ids {
			choices = append(choices, agents.ModelChoice{ID: id, Label: id})
		}
		model, err := resolveLiveModel(c.req.Model, choices)
		if err != nil {
			// A retired model needs its own wording: with the typo message the user never
			// reaches the cause, since the id and the billing route are both right and
			// only opencode.ai stopped offering it (models.go Retired).
			if requested := strings.TrimSpace(c.req.Model); opencodeRetiredFn(requested) {
				httpx.WriteErr(c.w, http.StatusBadRequest, "bad_model", retiredModelError(requested, choices))
				return false
			}
			httpx.WriteErr(c.w, http.StatusBadRequest, "bad_model", err.Error())
			return false
		}
		c.req.Model = model
	} else if NormalizeKind(c.req.Kind) == session.KindLcpp && strings.TrimSpace(c.req.Model) != "" {
		// Membership check only, for consistency with the other live-catalog kinds above —
		// never a rejection of an explicit id just because the catalog could not be reached
		// (LcppLiveModels nil, or the member/deployment engine not answering): resolveLiveModel
		// itself passes an empty catalog straight through unchanged (docs/log/109), which is
		// the right call here too — the create-time guard above already refused "no model at
		// all"; this only catches a typo'd or since-retired id when the catalog IS known.
		var choices []agents.ModelChoice
		if LcppLiveModels != nil {
			choices = FilterVisibleModels(session.KindLcpp, LcppLiveModels(c.r.Context()))
		}
		model, err := resolveLiveModel(c.req.Model, choices)
		if err != nil {
			httpx.WriteErr(c.w, http.StatusBadRequest, "bad_model", err.Error())
			return false
		}
		c.req.Model = model
	}
	return true
}

// resolveWorkingCopy is stage 6: the directory the session will run in — a fresh worktree, a
// clone, or the dir as sent, resolved against home — then the spawn working-copy check against
// that RESOLVED dir, then the subdir beneath it. It leaves c.req.Dir resolved and sets c.subdir.
func (c *createRun) resolveWorkingCopy() bool {
	// Worktree-then-start: spin a git worktree off an existing working copy (req.Dir =
	// the parent) and use it as the CWD, so a decided task branches into its own dir +
	// session without touching the parent's running sessions.
	if c.req.Worktree {
		parent := strings.TrimSpace(c.req.Dir)
		if parent == "" {
			httpx.WriteErr(c.w, http.StatusBadRequest, "bad_dir", "worktree requires dir (the parent working copy)")
			return false
		}
		if !filepath.IsAbs(parent) {
			parent = filepath.Join(homeDir(), parent)
		}
		// SVN has no worktree analog (docs/log/41): isolate parallel work by checking out a
		// different path into its own folder instead. Refuse a worktree launch here with a
		// clear message rather than letting ensureWorktree fail on the missing .git.
		if isSvnRepo(parent) {
			httpx.WriteErr(c.w, http.StatusBadRequest, "svn_no_worktree",
				"svn working copies have no worktree; check out a different path into its own folder instead")
			return false
		}
		var dir string
		var err error
		folderSeg := strings.TrimSpace(c.req.Folder) // "" => folder derives from the branch
		if c.req.UseExisting {
			// "Work on the existing branch": check out req.Branch (local or DWIM-tracked
			// remote) into the worktree. Reached from the launch dialog's existing-branch mode
			// and from the SCM branch actions, as well as from a name collision.
			base := strings.TrimSpace(c.req.Branch)
			// git holds a branch in one worktree at a time. Refuse BEFORE any directory or
			// fetch side effect, naming the copy that has it — worktree add would fail
			// halfway through otherwise, with a message that doesn't say what to do next.
			if occ := gitx.WorktreeBranches(parent)[base]; occ != "" {
				gitx.WriteBranchInUse(c.w, base, occ)
				return false
			}
			gitx.EnsureBranchRef(parent, base) // branch pushed since our last fetch => fetch once
			dir, err = gitx.EnsureWorktree(parent, base, "", "")
			if err == nil {
				gitx.FastForwardWorktree(dir) // start at the branch's current tip, not a stale one
			}
		} else {
			// Branch naming is deferred: unless the client sends an explicit name we
			// start on a throwaway branch temp/<slug> in a wip-<slug> folder (same
			// slug). The user (or the LLM suggestion) renames the branch later — the
			// folder stays put, so the session id holds.
			nb := strings.TrimSpace(c.req.NewBranch)
			if nb == "" {
				slug := randSlug() // random → skip the collision check
				nb = "temp/" + slug
				folderSeg = "wip-" + slug
			} else if local, remote := gitx.BranchNameStatus(parent, nb); local {
				// A same-named local branch: -b would fail anyway, but stop with a clear
				// message rather than git's raw error, and let the user pick another name.
				httpx.WriteErr(c.w, http.StatusConflict, "branch_exists",
					fmt.Sprintf("branch %q already exists locally; choose another name", nb))
				return false
			} else if remote {
				// A same-named PAST remote branch: -b would silently create a divergent
				// local branch that collides at push. Refuse and let the user rename or
				// work on the existing one (use_existing).
				httpx.WriteErr(c.w, http.StatusConflict, "branch_exists_remote",
					fmt.Sprintf("a remote branch %q already exists; rename it or work on the existing branch", nb))
				return false
			}
			var start string
			start, err = gitx.NewWorktreeStartPoint(parent, c.req.Branch) // base pushed since our last fetch => fetch once
			if err == nil && strings.TrimSpace(c.req.NewBranch) != "" {
				// The fetch above may have just brought in a same-named remote branch the
				// check before it could not see; ask again (local refs only, no network).
				if _, remote := gitx.BranchNameStatus(parent, nb); remote {
					httpx.WriteErr(c.w, http.StatusConflict, "branch_exists_remote",
						fmt.Sprintf("a remote branch %q already exists; rename it or work on the existing branch", nb))
					return false
				}
			}
			if err == nil {
				dir, err = gitx.EnsureWorktree(parent, start, nb, folderSeg)
			}
			if err == nil {
				// Start at origin's tip, the same intent as fastForwardWorktree on the
				// existing-branch path. An empty base means the parent's HEAD is the start
				// point, so pull using the parent's current branch name.
				base := strings.TrimSpace(c.req.Branch)
				if base == "" {
					// GitCurrentBranch answers "(detached)" when detached: there is
					// nothing to pull from, so do nothing (passing that as a branch name
					// only produces a confusing log line).
					if b := gitx.GitCurrentBranch(parent); b != "(detached)" {
						base = b
					}
				}
				gitx.FastForwardNewWorktreeToOrigin(dir, base)
			}
		}
		if err != nil {
			httpx.WriteErr(c.w, http.StatusBadGateway, "worktree_failed", err.Error())
			return false
		}
		c.req.Dir = dir
	} else if strings.TrimSpace(c.req.RemoteURL) != "" {
		// Clone-then-start: ensure the repo exists and use it as the working dir.
		dir, err := gitx.EnsureRepo(c.req.RemoteURL, c.req.Branch, c.req.NewBranch, c.req.RepoName)
		if err != nil {
			httpx.WriteErr(c.w, http.StatusBadGateway, "clone_failed", err.Error())
			return false
		}
		c.req.Dir = dir
	}
	if c.req.Dir == "" {
		c.req.Dir = homeDir()
	} else if !filepath.IsAbs(c.req.Dir) {
		// A relative dir (e.g. "projects/x" from the New Session directory picker) is
		// resolved against home, mirroring the fs/tree browser's home-relative paths.
		c.req.Dir = filepath.Join(homeDir(), c.req.Dir)
	}
	if fi, err := os.Stat(c.req.Dir); err != nil || !fi.IsDir() {
		httpx.WriteErr(c.w, http.StatusBadRequest, "bad_dir", "dir does not exist: "+c.req.Dir)
		return false
	}
	// Two agents in one working copy (ADR 0073 decision 7). Checked HERE, against the resolved
	// req.Dir, because that is the directory the session will actually run in: an empty dir
	// becomes home and a relative one is joined onto home a few lines above, so comparing the
	// raw request would let `dir: ""` walk straight past a session already running in home.
	// A worktree launch has replaced req.Dir with the fresh worktree by now and cannot collide,
	// which is why the check is scoped to the non-worktree case rather than the raw flag.
	if c.spawnParent != "" && !c.req.Worktree {
		ref, sharedDir := spawnWorkingCopyCheck(c.req.Dir, c.spawnParent, c.req.AllowSharedWorkingCopy)
		if ref != nil {
			httpx.WriteErr(c.w, ref.Status, ref.Code, ref.Message)
			return false
		}
		if sharedDir != "" {
			// Launch through the canonical spelling so Meta.Dir matches what the delete and
			// checkout guards compare, and tell the child it is not alone in the checkout. Taken
			// from the check's own answer: a second liveness read could disagree.
			c.req.Dir = sharedDir
			c.req.InitialPrompt = SharedWorkingCopyWarning(c.spawnParent, c.req.InitialPrompt)
		}
	}
	// Subdir (optional): the CWD narrows to a folder beneath the resolved working copy.
	// Validated AFTER Dir so a worktree launch checks the path inside the fresh worktree
	// — the parent may well have a folder the new branch doesn't (or vice versa).
	subdir, subdirOK := session.CleanSubdir(c.req.Subdir)
	if !subdirOK {
		httpx.WriteErr(c.w, http.StatusBadRequest, "bad_subdir",
			"subdir must be a relative path inside the working copy: "+c.req.Subdir)
		return false
	}
	if subdir != "" && !session.DirExists(filepath.Join(c.req.Dir, filepath.FromSlash(subdir))) {
		httpx.WriteErr(c.w, http.StatusBadRequest, "bad_subdir",
			"subdir does not exist: "+filepath.Join(c.req.Dir, filepath.FromSlash(subdir)))
		return false
	}
	c.subdir = subdir
	return true
}

// identify is stage 7: the session's name, kind, title default, label, SSM target, studio and
// origin.
func (c *createRun) identify() bool {
	req := c.req
	// Identity is a freshly allocated random slug — NOT the client's name. It (and the
	// sid it derives) can't collide with an archived/pruned session's jsonl, so a new
	// session never accidentally --resumes a past conversation.
	c.name = req.name
	if c.name == "" {
		c.name = allocSessionName(req.Dir)
	} else if _, taken := session.ReadMeta(c.name); taken {
		// A move's booked name is held against every allocation; a meta under it anyway is
		// another session, which a create must never overwrite.
		httpx.WriteErr(c.w, http.StatusConflict, "name_taken", "a session named "+c.name+" already exists")
		return false
	}

	c.kind = NormalizeKind(req.Kind)
	// An SSM session with no client title defaults to "{host alias} @MMDD-HHMM" — a
	// human-meaningful "target + timestamp" name (vs the generic {home-basename} @… fallback).
	if c.kind == session.KindSSM && c.title == "" {
		c.title = ssmDefaultTitle(req.SSMAlias, req.SSMTarget, time.Now())
	}
	if AgentOf(c.kind).Caps().UsesLabel {
		c.label = sessionLabelFor(req.Dir, c.title, c.name)
	}
	if c.kind == session.KindSSM {
		if strings.TrimSpace(req.SSMTarget) == "" {
			httpx.WriteErr(c.w, http.StatusBadRequest, "bad_ssm", "ssm_target (instance id) is required")
			return false
		}
		c.ssm = &session.SSMMeta{
			Profile: req.SSMProfile, Target: req.SSMTarget, Document: req.SSMDocument,
			Region: req.SSMRegion, StartURL: req.SSOStartURL, SSORegion: req.SSORegion,
			AccountID: req.SSOAccountID, RoleName: req.SSORoleName,
			RoleARN: req.SSMRoleARN, SourceProfile: req.SSMSourceProfile, ExternalID: req.SSMExternalID,
			RoleSessionName: req.SSMRoleSessionName, DurationSeconds: req.SSMDurationSeconds,
		}
		if c.ssm.Region == "" {
			c.ssm.Region = c.ssm.SSORegion
		}
	}
	c.studio = strings.TrimSpace(req.Studio)
	if c.studio != "" {
		// A studio is bound by a person pressing "attach an agent" in the studio pane. A session
		// raising a child must not hand it someone's studio: the binding is what the studio's
		// draft tools trust.
		if c.spawnParent != "" {
			httpx.WriteErr(c.w, http.StatusBadRequest, "bad_studio", "a session cannot start a session bound to an image studio")
			return false
		}
		if !paths.ValidIDSegment(c.studio) {
			httpx.WriteErr(c.w, http.StatusBadRequest, "bad_studio", "invalid studio id: "+c.studio)
			return false
		}
	}
	c.origin, c.originConv, c.originSession = CreateOrigin(req)
	return true
}

// armMeta is stage 8: the meta as it will be published, and the launch task armed for it. The
// studio is bound here, as the last step before the launch.
func (c *createRun) armMeta() (session.Meta, bool) {
	req := c.req
	meta := session.Meta{
		Name: c.name, Dir: req.Dir, Subdir: c.subdir, Model: req.Model, Effort: req.Effort, Mode: req.Mode, Kind: c.kind, Driver: c.driver, Title: c.title, Color: req.Color, Label: c.label,
		SkipPermissions: req.SkipPermissions,
		Repo:            filepath.Base(req.Dir), Branch: gitx.GitCurrentBranch(req.Dir),
		CreatedAt: time.Now().Format(time.RFC3339), SSM: c.ssm,
		Origin: c.origin, OriginConv: c.originConv, OriginSession: c.originSession,
		Studio: c.studio, WorkItem: cleanWorkItemRef(req.WorkItem),

		// From the member's setting only, never the request: a session can call create_session,
		// and turning contributor mode on is the member's decision (ui_prefs.go's mark gate).
		MuseContributorMode: c.kind == session.KindMuse && uiprefs.MuseContributorMode(),
	}
	// pending from the first write of the meta: the pane must not offer a resend while the
	// delivery in launch may still be typing (ADR 0100 decision 2).
	if strings.TrimSpace(req.InitialPrompt) != "" {
		meta.InitialPromptState = session.InitialPromptPending
	}
	// Armed here, before either launch path delivers the initial prompt (docs/log/85): the
	// arm's instant is also the lower bound its completion evidence is cut by, and the launch
	// task is precisely the work it is waiting for the end of.
	if req.StopAfterTurn {
		meta.StopAfterTurnAt = meta.CreatedAt
	}
	meta.SpendCapUSD = session.DefaultSpendCap()
	if req.SpendCapUSD != nil {
		meta.SpendCapUSD, _ = session.NormalizeSpendCap(*req.SpendCapUSD) // validated in validateOptions
	}
	// docs/log/51 Phase 3, the self-report fast path: add one line to the launch task saying
	// "call af_report when you are done" — only for an instruction that owes a report, i.e.
	// one with report_to. Placed before the managed / tui split so both launch paths carry
	// the same line.
	if req.ReportTo != "" || req.delivery() != nil {
		req.InitialPrompt = withSelfReportHint(req.InitialPrompt, meta)
	}
	// The studio's own `session` is written BEFORE the session is published or launched (ADR
	// 0100 decision 2 ③): a Managed create sends the persona synchronously in launch, and its
	// first get_image_studio is refused unless the studio already names this session back.
	if c.studio != "" {
		if ref := bindStudioOnCreate(c.studio, meta); ref != nil {
			httpx.WriteErr(c.w, ref.Status, ref.Code, ref.Message)
			return session.Meta{}, false
		}
	}
	return meta, true
}

// publish writes the meta. A move's target is written checked: the move commits, and stops
// its source, on the strength of that meta, and a target running without one is listed
// nowhere. Its first prompt follows the write, so a failed write leaves a target that has
// not been told to work.
func (c *createRun) publish(m session.Meta) bool {
	if c.req.name == "" {
		c.slot.publish(m)
		return true
	}
	if err := writeMoveTargetMeta(m); err != nil {
		log.Printf("move target %s: writing its meta: %v", c.name, err)
		httpx.WriteErr(c.w, http.StatusInternalServerError, errCodeMetaWriteFailed, "the session started but its meta could not be written: "+err.Error())
		return false
	}
	return true
}

// launch is stage 9: start the session, publish its meta, record where its launch task came
// from, deliver that task, and answer the create.
func (c *createRun) launch(meta session.Meta) {
	req, name := c.req, c.name
	if meta.DriverKind() == session.DriverManaged {
		// managed (docs/log/27 P2): no tmux pane — the driver opens a thread on the shared
		// runtime. The first prompt needs no boot-screen scraping and can just be sent
		// (§10.2-9 — idempotent through ClientMessageID).
		d, _ := driverOf(meta)
		h, err := mcpx.StartManagedSession(d, meta)
		if err != nil {
			unbindStudioAfterFailedLaunch(c.studio, name)
			writeRuntimeErr(c.w, err)
			return
		}
		if !c.publish(meta) {
			return
		}
		recordFleetGraphBirth(meta)
		req.managed = true
		noteCreateOrigin(name, req, c.spawnParent, c.origin)
		// The row stays either way: a failed launch task is reported as before.
		defer chatx.MarkInstrSent(name, req.instr)
		if p := strings.TrimSpace(req.InitialPrompt); p != "" {
			if err := h.Send(agents.TurnInput{Prompt: p, Origin: createTurnOrigin(req, c.spawnParent),
				Instr: req.instr, Schedule: scheduleRefOf(req.Source, req.ScheduleID, req.ScheduleSlot)}); err != nil {
				log.Printf("managed initial prompt %s: %v", name, err)
				meta.InitialPromptState = session.InitialPromptFailed
			} else {
				markSessionWorking(name)
				meta.InitialPromptState = session.InitialPromptDelivered
			}
			settleInitialPrompt(name, meta.InitialPromptState)
		}
		c.writeCreated(meta)
		return
	}
	if err := launchTmuxFn(meta, req.SSMForceLogin); err != nil {
		unbindStudioAfterFailedLaunch(c.studio, name)
		httpx.WriteErr(c.w, http.StatusInternalServerError, "tmux_failed", err.Error())
		return
	}
	if !c.publish(meta) {
		return
	}
	recordFleetGraphBirth(meta)

	// BEFORE the delivery below, not after: the record is what the mirror matches the turn
	// against, and the turn can appear first (see noteCreateOrigin).
	noteCreateOrigin(name, req, c.spawnParent, c.origin)
	// Optional launch task: deliver it once the CLI has booted (async — the create
	// response returns the session immediately so the caller can start polling).
	if strings.TrimSpace(req.InitialPrompt) != "" {
		go deliverInitialPromptFn(name, req.InitialPrompt)
	}

	c.writeCreated(meta)
}
