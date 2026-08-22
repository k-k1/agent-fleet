// Package claude は claude CLI 種別の縦割りパッケージ（docs/23 残① Wave F: 最大の
// 縦割り）。Agent 実装・起動コマンド組み立て・jsonl transcript 解析・auth/settings/
// usage の Connections/Console ハンドラ・status hook 配線・コンテキスト充填率・
// バックグラウンド実行検知を package main から移設した。挙動・ワイヤ・ディスクは
// main 時代とバイト同一を維持すること。session-status サブコマンドの入口
// （hook stdin の解読と pending payload 適用）は main に残る。
package claude

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

// New returns the claude Agent implementation for the kind registry.
func New() agents.Agent { return agentImpl{} }

// agentImpl — claude 種別の Agent 実装（docs/23 P1残: CLI 縦割りファイル分割）
type agentImpl struct{ agents.NoGenericTranscript }

func (agentImpl) Kind() string { return session.KindClaude }

// CanForkAt: 発言時点からの分岐（docs/55）。claude だけは公式の口が無く、転写 jsonl を
// 切り詰めて分岐先を作る（forkat.go）。TUI 起動しか無いので、他の kind と違って managed の
// 条件は付かない — 経路の可否は ResolveForkAt が kind ごとに答える。
func (agentImpl) Caps() agents.Caps {
	return agents.Caps{CanFork: true, CanForkAt: true, CanTranscript: true, UsesLabel: true}
}

// ForkSource resolves this session's conversation id as the fork source, refusing when
// the jsonl holds no real conversation yet — `claude --resume` would die with "No
// conversation found". The id must be the one claude actually writes under (LiveSID),
// not our slot sid: the fork command resumes it verbatim (sid.go).
func (agentImpl) ForkSource(m session.Meta) (string, error) {
	sid := LiveSID(session.UUID(m.Dir, m.Name))
	if !JSONLResumable(sid) {
		return "", errors.New("分岐できる会話がまだありません")
	}
	return sid, nil
}

// ResolveForkAt validates the anchor against this session's own transcript. The value
// travels unchanged: unlike codex, claude's cut is expressed as "the line to stop before",
// which is exactly what the mirror handed out. The work here is refusing the anchors that
// would produce a transcript claude launches but cannot answer in (a tool_use whose
// tool_result fell on the other side of the cut) — see forkat.go for why that matters.
//
// Validating here, at request time, rather than only at launch is deliberate: a refusal
// must reach the user as "この分岐点は使えません", not as a session that starts and dies.
func (agentImpl) ResolveForkAt(m session.Meta, at agents.ForkPoint) (string, error) {
	sid := session.UUID(m.Dir, m.Name)
	lines, path, _ := TranscriptRead(sid)
	if len(lines) == 0 || path == "" {
		return "", errors.New("分岐できる会話がまだありません")
	}
	anchor := at.Anchor
	if at.Include {
		// 「この発言の続きから」= 次のユーザープロンプトの手前で切る。ForkAt の意味
		// （この uuid の手前まで残す）は変えないので、材料化も起動もそのまま通る。
		next, err := nextPromptUUID(lines, anchor)
		if err != nil {
			return "", err
		}
		if next == "" {
			// 最後のやり取り = 全部残す。ForkAt を空にすると会話まるごと分岐の経路
			// （--fork-session）に落ちるが、結果は同じ「全部入り」なので正しい。
			return "", nil
		}
		anchor = next
	}
	// Dry run of the real surgery (destination sid is irrelevant to the checks), so the
	// answer here and the behaviour at launch can never disagree.
	if _, err := buildForkLines(lines, anchor, sid); err != nil {
		return "", err
	}
	return anchor, nil
}

func (agentImpl) BuildLaunch(m session.Meta, _ agents.LaunchOpts) (agents.LaunchPlan, error) {
	// A claude session must launch in its real working dir: if the dir is gone (its
	// repo was deleted) we refuse rather than resume the conversation in an unrelated
	// cwd. wireSession reports this as non-resumable.
	if !session.DirExists(m.Dir) {
		return agents.LaunchPlan{}, agents.DirGoneErr(m.Dir)
	}
	// Pre-trust the launch dir so claude doesn't stall on the folder-trust dialog
	// (not skippable via --dangerously-skip-permissions).
	ensureFolderTrusted(m.Dir)
	sid := session.UUID(m.Dir, m.Name)
	// A jsonl can exist yet hold no real conversation — e.g. only a Remote Control
	// "bridge-session" line when RC connected but nothing was said. claude --resume
	// then dies with "No conversation found". Drop such a stub so buildProgram
	// starts fresh (--session-id) instead of resuming.
	if !JSONLResumable(sid) {
		for _, p := range jsonlPaths(sid) {
			_ = os.Remove(p)
		}
	}
	// First launch of a POINT fork (docs/55): write our own truncated transcript before
	// the pane starts. buildProgram then finds a jsonl for sid and resumes it — the fork
	// is invisible from there on, exactly like the whole-conversation fork becomes a
	// plain resume after its first launch. A failure must not fall through to
	// `--fork-session`, which would copy the WHOLE conversation the user asked to cut.
	if m.ForkAt != "" && m.ForkFrom != "" && !SessionJSONLExists(sid) {
		if err := MaterializeForkAt(m.ForkFrom, sid, m.ForkAt); err != nil {
			return agents.LaunchPlan{}, fmt.Errorf("発言時点からの分岐を作成できませんでした: %w", err)
		}
	}
	// No env token is injected: the interactive TUI authenticates from claude's own
	// .credentials.json, written by `claude auth login` via the Connections flow
	// (auth.go). CLAUDE_CODE_OAUTH_TOKEN is headless-only.
	return agents.LaunchPlan{Program: buildProgram(sid, m.Model, m.Effort, m.Mode, m.Label, m.ForkFrom), Cwd: m.CWD()}, nil
}

func (agentImpl) WireLive(m session.Meta, alive bool) agents.LiveInfo {
	li := agents.LiveInfo{Resumable: true}
	sid := session.UUID(m.Dir, m.Name)
	li.RemoteURL = RemoteSessionURL(sid)
	li.Context = latestContext(sid)
	if alive {
		// Default a live claude with no recorded event yet to idle (it sits at the
		// prompt waiting for input). Hook events refine it.
		//
		// EffectiveModal: AskUserQuestion / ExitPlanMode 自身の permission_prompt が
		// state を "permission" へ上書きするので、捕捉済みペイロードのほうを正とする。
		// 素の state だと、質問カードが出ているセッションのチップだけが「許可待ち」を
		// 名乗る（バッジと本文の食い違い）。
		li.State = status.EffectiveModal(sid, status.LiveState(sid))
		// ペイン由来の判定は 1 フレームを 1 回だけ読んでまとめて下す（tmuxx.ReadPane）。
		// 述語ごとに capture-pane を叩くと、セッション数 × ポーリング間隔でそのまま効く。
		pane := tmuxx.ReadPane(m.Name)
		// 利用上限メニューで人間待ちに固定されている（tmuxx.AtRateLimitModal）。ターンは
		// もう終わっているのに、そのメニューは "Esc to cancel" を含みモード表示フッタごと
		// 入力欄を置き換えるので、下の AtIdlePrompt 経由の自己修復は恒久的に空振りする。
		// ここで先に拾わないと 進行中 に永久に貼り付く（実測 2026-07-31・約16時間）。
		//
		// HealIdle は非 idle のときだけ呼ぶ — 下の自己修復と同じガードで、MarkTurnEndErr が
		// idle を永続化するので 2 回目以降の poll では走らない（メニューは人が消すまで出た
		// ままなので、これが無いと毎 poll 通知と完了報告を撃ち続ける）。
		//
		// 認証切れ（docs/47 §4-8）を上限メニューより先に見るのは、両方が同時に立ちうる中で
		// **待っても直らない方**だから: 上限は時刻が来れば解けるが、期限切れは再認証するまで
		// 何も動かない。メニューの自動解除はペインを直接読む（rate_limit_resume.go）ので、
		// ここで auth を返してもその回復経路は塞がらない。
		// 走っていたことになっているターンを HealIdle で畳んでから名乗るのは上限と同じ形 —
		// 401 で切れたターンでは Stop hook が鳴らず、進行中 のまま貼り付くとバッジが出る前に
		// 一覧が嘘をつく。
		if AuthExpired() {
			if li.State != "idle" {
				HealIdle(sid)
			}
			li.State = agents.StateAuth
			return li
		}
		if pane.RateLimitMenu {
			if li.State != "idle" {
				HealIdle(sid)
			}
			li.State = agents.StateBlocked
			return li
		}
		// Self-heal a stale cache: a non-idle state that no longer matches the terminal
		// (killed+resumed, rejected permission, abandoned question) — if the pane is
		// back at the ready prompt, it's idle. HealIdle additionally recognises the one
		// case that IS a real turn end — an API error cut the turn off, so no Stop hook
		// ever fired — and routes it through the notifier instead of silently dropping
		// the completion (docs/47).
		if li.State != "idle" && pane.Idle {
			li.State = "idle"
			HealIdle(sid)
		}
		// Idle by hook, but the pane may actually be mid-turn: the "working" status file
		// can go missing (never written, or removed by the self-heal above during a
		// transient prompt frame) and no mid-turn hook rewrites it in bypass mode, so a
		// busy session would wrongly read idle. IsBusy trusts the live TUI (interrupt
		// affordance shown) and persists working — self-limiting to one capture per turn,
		// since the next poll then reads "working" from the file.
		if li.State == "idle" && pane.Busy {
			li.State = "working"
			status.Persist(sid, "working")
		}
		// Still idle: background work may yet be running — surface it so 入力待ち isn't
		// mistaken for "done". BackgroundBusy sees run_in_background worker processes under
		// the pane; SubagentBusy sees in-process background subagents / Workflow agents
		// (which spawn no such process) via their transcript freshness; BackgroundShellBusy
		// sees a Monitor / sleep- or I/O-bound background shell that sits in S state and so
		// slips past both.
		if li.State == "idle" {
			li.BackgroundBusy = BackgroundBusy(m.Name) || SubagentBusy(sid) || BackgroundShellBusy(m.Name)
		}
	} else if !session.DirExists(m.Dir) {
		// A stopped claude whose working dir was removed (its repo deleted) can't be
		// resumed there; the Console marks it non-resumable (archive only).
		li.Resumable = false
	}
	return li
}

func (agentImpl) ClearResume(string) {}

// RemoteSessionURL derives the claude.ai Remote Control page for sid from its
// jsonl "bridge-session" line (written when RC connects). The web URL is
// "…/code/session_<bridgeSessionId without the cse_ prefix>". We read only the
// head of the log (the bridge line is written at session start) to stay cheap on
// the polled list. Returns "" when there is no bridge (RC off / not yet connected).
func RemoteSessionURL(sid string) string {
	for _, p := range jsonlPaths(sid) {
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		buf := make([]byte, 64*1024)
		n, _ := f.Read(buf)
		f.Close()
		for _, line := range strings.Split(string(buf[:n]), "\n") {
			if !strings.Contains(line, `"type":"bridge-session"`) {
				continue
			}
			var b struct {
				BridgeSessionID string `json:"bridgeSessionId"`
			}
			if json.Unmarshal([]byte(line), &b) == nil && b.BridgeSessionID != "" {
				return "https://claude.ai/code/session_" + strings.TrimPrefix(b.BridgeSessionID, "cse_")
			}
		}
	}
	return ""
}
