package main

// セッションへの MCP 配線（docs/48 P3 → P5 で全 kind）。実効レジストリのうち `targets.session` の
// 定義を、各 CLI 自身のグローバル設定へ書き出す（materialize）。書き出しの中身は
// internal/mcpreg/materialize_*.go にあり、本ファイルは「いつ書くか」だけを持つ。
//
// 契機は 3 つ（docs/48 §8.3）:
//   - agent 起動時（コンテナを起こした直後から、手で叩く CLI にも効くように）
//   - セッション起動直前（登録 → 新規セッション が最短で通る）
//   - レジストリ変更時（CRUD）
//
// **既に走っているセッションには効かない** — どの CLI も起動時に設定を読む。Console は
// 「新規セッションから有効」と明示している（mcp.session_restart_note）。

import (
	"log"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpreg"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// startManagedSession is the managed-driver counterpart of startSessionTmux's
// materialize hook: Resume() is what LAUNCHES a managed session, so the config has to
// be current first. Only the launch sites go through here — the many Resume() calls
// that merely re-attach to a running thread (turn send, bridge, answer) must not pay
// for a registry read on every message.
func startManagedSession(d agents.Driver, m session.Meta) (agents.ThreadHandle, error) {
	materializeMCP(m.Kind)
	ensureClaudeSettingsWiring(m.Kind) // see session_status.go: repairs a stale hook/statusLine path
	return d.Resume(m)
}

// materializeMCP writes the registry into one kind's native CLI config. Failures are
// logged, never fatal: a session must still launch when its MCP config could not be
// updated (the user gets the previously written set, which is the same thing a
// stopped CP gives them for tenant rows).
func materializeMCP(kind string) {
	logMaterializeMCP([]mcpreg.MaterializeResult{mcpreg.Materialize(kind)})
}

// materializeMCPAll writes every implemented kind — used at boot and after a
// registry change, where "which kind" isn't yet known.
func materializeMCPAll() {
	logMaterializeMCP(mcpreg.MaterializeAll())
}

func logMaterializeMCP(res []mcpreg.MaterializeResult) {
	for _, r := range res {
		switch {
		case r.Err != "":
			log.Printf("mcp materialize %s: %s", r.Kind, r.Err)
		case r.Changed:
			log.Printf("mcp materialize %s: %d server(s), removed %v", r.Kind, len(r.Written), r.Removed)
		}
	}
}
