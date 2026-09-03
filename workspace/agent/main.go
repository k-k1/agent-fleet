// Command agent is the Workspace Agent: a thin in-container process that the
// Control Plane drives over an internal HTTP/WS API. It manages tmux+claude
// sessions and bridges a PTY to the browser terminal. Internal-only; never
// exposed outside the VPC / docker network. See docs/07-workspace-agent.md.
package main

import (
	"github.com/k-k1/agent-fleet/workspace/agent/internal/memoryx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"
	"log"
	"net/http"
	"os"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/codex"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/copilot"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/cursor"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/kiro"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/opencode"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/bridge"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/browserx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/chatx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpreg"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

// buildVersion is stamped by the release pipeline via
// `-ldflags "-X main.buildVersion=<v>"` (docs/log/35 §35.6.1); dev builds stay "dev".
var buildVersion = "dev"

func main() {
	// Subcommand mode: git invokes this binary as its credential helper
	// (`workspace-agent cred get`), backed by the encrypted store. It prints
	// creds and exits without starting the server. `bitbucket-cred` is kept as
	// an alias for any git config left over from before the unified helper.
	if len(os.Args) > 1 && (os.Args[1] == "cred" || os.Args[1] == "bitbucket-cred") {
		runCredHelper(os.Args[2:])
		return
	}
	// JDK provisioner: `workspace-agent install-jdk <major>` downloads the latest GA
	// Temurin for the container arch into the per-user home volume (temurin-<major>-
	// jdk-<arch>), the common JDK location the toolchain resolver + entrypoint search
	// alongside /usr/lib/jvm. Run by the entrypoint on demand (selected java missing)
	// and available to the agent directly. See jdk.go.
	if len(os.Args) > 1 && os.Args[1] == "install-jdk" {
		runInstallJDK(os.Args[2:])
		return
	}
	// On-demand pinned installers (docs/log/35 §35.7.2): chromium+CJK font for the
	// browser pane, the Go toolchain, and AWS CLI + Session Manager plugin for
	// ssm sessions. Lean rootfs deployments install these into the home on first
	// use; versions come from the versions.json pins. See install_tools.go.
	if len(os.Args) > 1 && os.Args[1] == "install-chromium" {
		runInstallChromium(os.Args[2:])
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "install-go" {
		runInstallGo(os.Args[2:])
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "install-awscli" {
		runInstallAWSCLI(os.Args[2:])
		return
	}
	// On-demand Kiro CLI installer (kind="kiro", docs/log/43 Track B): kiro is ~855MB
	// extracted, so unlike the other agent CLIs it is NOT baked/boot-installed for
	// everyone — it lands in the per-user home only when that user actually uses it
	// (the kiro launch program runs this with --if-needed on every launch, so a
	// versions.json pin bump also reaches the already-installed home copy; the
	// connection card install button does too). See install_kiro.go.
	if len(os.Args) > 1 && os.Args[1] == "install-kiro" {
		runInstallKiro(os.Args[2:])
		return
	}
	// claude hook helper: records session working/idle/question state.
	if len(os.Args) > 1 && os.Args[1] == "session-status" {
		sessionx.RunSessionStatusHook(os.Args[2:])
		return
	}
	// Pane exit recorder: `workspace-agent record-exit <name> <code>`, appended after
	// the agent CLI by startSessionTmux, records why a session terminated (crash / OOM).
	if len(os.Args) > 1 && os.Args[1] == "record-exit" {
		runRecordExit(os.Args[2:])
		return
	}
	// Bounded terminal-output sink, fed by tmux pipe-pane.
	if len(os.Args) > 1 && os.Args[1] == "record-terminal" {
		runRecordTerminal(os.Args[2:])
		return
	}
	// Local stdio MCP server: assistant chat tools (docs/log/19 Q1) or the narrowly scoped
	// interactive-session builtin (docs/log/51 Phase 3 + docs/log/53 §53.8), selected by args.
	if len(os.Args) > 1 && os.Args[1] == "mcp-stdio" {
		mcpx.RunStdio(os.Args[2:])
		return
	}
	// Credential-injecting launcher for external ops MCP servers (docs/log/25): loads
	// the encrypted store, injects the provider key as env, and execs the real MCP
	// server (e.g. uvx pagerduty-mcp). Keeps API keys out of any MCP config file.
	if len(os.Args) > 1 && os.Args[1] == "mcp-run" {
		mcpx.RunSubcommand(os.Args[2:])
		return
	}
	// claude statusLine capture: claude pipes the session JSON (incl. rate_limits) on
	// stdin every render; we persist the 5h/weekly usage locally for the WsBar chip —
	// no network, so the rate-limited /api/oauth/usage endpoint is no longer used.
	if len(os.Args) > 1 && os.Args[1] == "statusline" {
		claude.RunStatusLine(os.Args[2:])
		return
	}
	// Image-only browser verification: exercise the production BrowserManager,
	// pipe CDP, sandbox, two simultaneous Pages and capture pacing without booting
	// the rest of the Agent subsystems. deploy/local/e2e-smoke.sh is the caller.
	if len(os.Args) > 1 && os.Args[1] == "browser-smoke" {
		if err := browserx.RunBrowserImageSmoke(); err != nil {
			log.Fatal(err)
		}
		return
	}

	// Fold any pre-A3 plaintext credential files into the encrypted store.
	migrateLegacySecrets()
	// Seed the CP-injected internal git token (docs/reference/internal-git-provider)
	// into the cred store so clone/push against the tenant's self-hosted repos auth
	// transparently. No-op when the CP didn't inject one.
	seedInternalGit()
	// Record where the git OAuth refresh bridge lives (docs/log/71 §71.8) so the separate
	// `workspace-agent cred` process can reach it without depending on its own env.
	seedGitOAuthBridge()
	// Make claude emit working/idle/question via hooks into the status files.
	claude.EnsureStatusHooks()
	// 持ち越し（docs/log/75）の寿命切れを落とす。ここでしか消えない — セッションが消えた後の
	// 持ち越しを掃除する経路が他に無く、寿命の無い pending-* は実際に 5〜6 週間分たまっていた。
	if n := status.SweepCarried(); n > 0 {
		log.Printf("carried-interaction: dropped %d expired entr(y|ies)", n)
	}
	// Wire claude's statusLine to us so we capture its rate_limits (5h/weekly usage)
	// locally for the WsBar chip. Wraps a user's own statusLine rather than clobbering.
	claude.EnsureStatusLine()
	// Compose the instruction files every session reads: the baked fleet guide, the
	// user's own instructions (docs/log/60) and the rtk block — in that order, through one
	// writer. This replaces the entrypoint's old `cp -f` of workspace-notes.md, which
	// destroyed anything the user had added to those files on every container start.
	// Sessions are started by us, so nothing can read a half-composed file.
	reconcileAgentInstructions()
	// Mint this boot's name for af's own MCP server, BEFORE anything materializes a
	// config. A repository's project-scoped MCP config beats af's user-scope one on
	// every kind but claude (docs/log/48 §8.4), so a repo that happens to define a server
	// called "af" would silently take over self-report, the handoff proposal and
	// Chromium attach; a random suffix makes that collision go away, and rotating it
	// per boot means even a deliberate one is shaken off by a restart.
	log.Printf("mcp: af server name for this boot = %s", mcpreg.RotateAFServerName())
	// Write the MCP registry into each CLI's own config (docs/log/48 P3) so the servers a
	// user registered are live from container start — including for a CLI they launch
	// by hand in a terminal, which never passes through the session launch hook.
	mcpx.MaterializeAll()
	// Pull the tenant-distributed MCP set from the CP and keep it fresh (docs/log/48 P4).
	// Backgrounded and fail-open: boot must not wait on the CP, and an unreachable CP
	// keeps the cached set rather than stripping everyone's servers.
	mcpx.StartTenantSync()
	// Pull the role-scoped docs when the runtime mounted none (ECS — docs/build/04 §4.9).
	// Backgrounded: it is a few hundred KB over the network and nothing at boot waits on
	// it, but the Console's 利用ガイド and every agent's environment answers need it.
	go syncWorkspaceDocs("agent boot")
	startTerminalHistoryJanitor()
	// managed driver（hook を持たない）の turn 完了を、hook 経路と同じ通知/報告
	// （応答あり notice ＋ docs/log/30 のオペレーター報告）へ流す。driver は
	// internal/agents 配下で package main を import できないため、判定の 1 実装を
	// ここで seam に登録する。app-server 起動と reconcile より前に張ること。
	agents.SetStateNotifier(sessionx.RecordSessionNotification)
	// 完了報告の消費判定（docs/log/51 Phase 1 / ADR 0035）。フック・notify seam・
	// record-exit の kick は起床ヒントでしかなく、「指示が完了したか」を決めるのは
	// このリコンサイラの tick だけ。ヒントが死んでも次の tick が同じ状態をレベルで
	// 見て拾うので、取りこぼしは報告の消失ではなく遅延に縮退する。
	// docs/log/51 Phase 2: 旧 arm（1bit）で待っていた指示を台帳の行へ変換してから回す。
	// リコンサイラより前に走らせること — 変換前の tick は「未報告の指示なし」を見る。
	chatx.MigrateReportArms()
	chatx.StartReportReconciler()
	// ブラウザ attach ハンドオフの配送台帳（docs/log/53 完了通知節）: 前回起動時に
	// resolveBrowserHandoff は済んだが deliverBrowserHandoff が完了する前に落ちた
	// 分を拾い直す。busy/idle の settle 判定を持たないので上のリコンサイラとは
	// 無関係に一度きりでよい。
	browserx.SweepUndeliveredBrowserHandoffs()
	// リポジトリ取り込みジョブ（docs/log/78）: 前回の clone / checkout は Agent ごと死ぬ
	// （タスク入れ替え・idle-stop）。生き残った marker を「中断」として復元しないと、
	// 半端な作業コピーだけが普通のリポジトリ顔で一覧に戻る。
	sweepRepoJobMarkers()
	// Codex sessions use a shared local app-server when available（P3 からは
	// codex.Serve() の RuntimeSupervisor が daemon を所有する）。AF attaches
	// a read-only observer per loaded thread: compaction state, rate limits, and
	// the model-switch observation log (docs/log/27 P1).
	// ここで daemon を起こすことはしない（docs/log/27 §7 補遺）: 需要——managed の
	// Resume と TUI の起動——が起こし、需要ゼロが続けば畳む。張るのは継ぎ目と
	// オブザーバだけ。
	startCodexAppServer()
	// managed セッション（docs/log/27 P2: opencode / P3: codex）を再接続する — Agent
	// 再起動を挟んでも tmux の tui セッションが生き残るのと同じ体感にする（§6 の
	// reconciliation）。runtime が必要なら Ensure が起こす。managed メタが無ければ
	// 即 no-op。
	go opencode.ReconcileManaged("agent boot")
	go codex.ReconcileManaged("agent boot")
	go copilot.ReconcileManaged("agent boot")
	go cursor.ReconcileManaged("agent boot")
	go kiro.ReconcileManaged("agent boot")
	// Assistant-conversation slugs (docs/log/38 アシスタント発火): stamp "a…" slugs onto
	// conversations created before the field existed, so schedules/operator tools can
	// address every conversation. One-time per store state; cheap when nothing to do.
	go chatx.BackfillConvSlugs()

	addr := envOr("AGENT_ADDR", ":7700")

	mux := buildMux()

	// Translate the runtime's stop signal (SIGTERM from docker stop / ECS task
	// stop) into a graceful in-container shutdown before the SIGKILL deadline.
	watchShutdownSignals()

	// Keep origin refs fresh in the background so repo rows can badge
	// "origin advanced" without a manual fetch (fetch_loop.go).
	startAutoFetch()

	// エージェントメモリの自動 snapshot（docs/log/39 / ADR 0022 P1）: claude/codex の
	// メモリ md を bare repo へ差分保存する。ポーリング契機（memory_trigger.go）で、
	// 変更が静穏かつ対象セッションが非稼働のときだけ積む。AF_MEMORY_SNAPSHOT=off で無効。
	memoryx.StartMemorySnapshotLoop()

	// 利用上限で止まった claude セッションの自動復帰（docs/log/47 §4-4）: メニューを既定の
	// 「リセットまで待つ」で解除し、上限が解ける時刻に「続けて」を送る一回限りの
	// スケジュールを CP へ預ける。誰も画面を見ていないときに効く必要があるので、
	// 一覧ポーリングではなく専用のループで回す。
	sessionx.StartRateLimitWatch()

	// 再送で直る中断（接続断・一時的なレート制限・ストリームの番犬）からの自動再開
	// （docs/log/47 §4-6）: 転写の末尾が retryable な中断で終わっている claude セッションへ
	// Agent 自身が「続けて」を送る。アシスタント会話を持たないセッションでも効き、
	// 打ち切ったときだけ報告としてアシスタント／利用者へ上がる。
	sessionx.StartAbortResumeWatch()

	// Chat-bridge delivery loop (docs/log/37 P1): drains the on-disk queue that
	// notice.Put / record-exit enqueue into (possibly from hook subprocesses)
	// and pushes to the configured chat providers (Discord first).
	bridge.StartSender()

	// Chat-bridge receive (docs/log/37 P2a): the Discord Gateway supervisor that routes the
	// bound user's thread replies back into sessions. No-op until a user opts into receive
	// (Discord.Receive) — bounds the WSS connection to opted-in users only.
	sessionx.StartBridgeReceiver()

	log.Printf("workspace-agent %s listening on %s", buildVersion, addr)
	if err := http.ListenAndServe(addr, httpx.LogRequests(httpx.Gzip(httpx.RequireToken(mux)))); err != nil {
		log.Fatal(err)
	}
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// --- small helpers ---

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
