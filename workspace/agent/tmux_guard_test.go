package main

// tmux 破壊操作ガード（tripwire）: docs/log/32 M1 E2E で、テスト用 agent インスタンスの
// shutdown が共有デフォルトソケットへ `tmux kill-server` を実行し、Workspace 内の
// 全セッションを 4 回落としたインシデントの再発防止。
//
//  1. `kill-server` はサーバ丸ごと（自分が作っていない pane も）を殺すため、agent の
//     製品コードでの使用を全面禁止する。停止は必ず自メタ管理下セッションへの
//     `kill-session`（shutdown.go / halt）で行う。
//  2. tmux の exec は tmuxx.Cmd に集約する。直接 exec.Command("tmux", …) を書くと
//     AF_TMUX_SOCKET によるソケット分離（dev / E2E の第 2 インスタンス隔離）を
//     すり抜けるため、これも禁止する。
//
// _test.go は対象外（テストは自前のソケット/セッションを扱ってよい。ただし
// e2e-tmux-socket-isolation の教訓どおり `tmux -L` で隔離すること）。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func scanAgentSources(t *testing.T, visit func(path, src string)) {
	t.Helper()
	err := filepath.WalkDir(".", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "testdata" || d.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		visit(path, string(b))
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
}

// TestNoKillServer: 製品コードのコード行に "kill-server" が現れたら即失敗。
// コメント行（経緯の記述）は許す — 再導入は必ずコード行に現れる。
func TestNoKillServer(t *testing.T) {
	scanAgentSources(t, func(path, src string) {
		for i, line := range strings.Split(src, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "//") {
				continue
			}
			if strings.Contains(line, "kill-server") {
				t.Errorf("%s:%d: kill-server is banned (kills sessions this instance does not own; use kill-session against owned sessions — see shutdown.go)", path, i+1)
			}
		}
	})
}

// TestTmuxExecFunnel: tmux の exec.Command 直呼びは tmuxx.Cmd（AF_TMUX_SOCKET を
// 反映する唯一の入口）以外で禁止。
func TestTmuxExecFunnel(t *testing.T) {
	scanAgentSources(t, func(path, src string) {
		if filepath.ToSlash(path) == "internal/tmuxx/tmuxx.go" {
			return // the funnel itself
		}
		for i, line := range strings.Split(src, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "//") {
				continue
			}
			if strings.Contains(line, `exec.Command("tmux"`) {
				t.Errorf(`%s:%d: exec.Command("tmux", …) bypasses AF_TMUX_SOCKET scoping — use tmuxx.Cmd`, path, i+1)
			}
		}
	})
}
