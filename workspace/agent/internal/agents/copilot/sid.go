package copilot

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// copilot は「押し付け型」— 我々が採番した UUID を `--session-id` で渡し、以後それが
// 使われている前提で転写も状態も引く。CLI がその id を使わなくなると（自己再起動で
// フラグを落とす／TUI 内で新セッションを始める）ミラーは静かに空のまま固まる。claude で
// 実際に起きた壊れ方で、copilot には status hook が無いので名乗りを聞く口も無い
// （internal/agents/imposedsid.go）。手掛かりはディスクだけ。
//
// resolveSid は「押し付けた id の session-state が一つも無い」ときにだけ、この cwd の
// 会話を拾い直して台帳を差し替える。読みの hot path（Transcript/LiveState が呼ぶ
// SessionID）には入れない — ListMetas を舐めるので、起動時と生存ポーリングだけに乗せる。
func resolveSid(m session.Meta) string {
	return agents.ResolveImposedSID(sids, m, cliSessions)
}

// cliSessions enumerates copilot's own sessions launched in dir.
//
// 帰属と作成時刻は session-state/<sid>/workspace.yaml から読む。v1.0.73 実測で
// `id:` / `cwd:` / `created_at:`（RFC3339・ミリ秒・Z）を持ち、この環境に残る 14 セッション
// （7/21〜8/13）すべてで同じ形だった。fork の材料化でも我々が触るファイルなので
// （forkat.go の retargetFile）、形の前提はそこと共有している。
//
// 素朴な行スキャンで読むのは、必要なのが最上位の 3 キーだけで、YAML 依存を足す理由が
// 無いため。値にコロンを含む行（name: …）があるので分割は最初のコロンのみ。
func cliSessions(dir string) []agents.CLISession {
	root := filepath.Join(Home(), "session-state")
	ents, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	want := filepath.Clean(dir)
	var out []agents.CLISession
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		id, cwd, created := readWorkspaceYAML(filepath.Join(root, e.Name(), "workspace.yaml"))
		if id == "" {
			id = e.Name() // workspace.yaml が未生成/壊れている場合はディレクトリ名が id
		}
		if filepath.Clean(cwd) != want {
			continue
		}
		out = append(out, agents.CLISession{ID: id, Created: created})
	}
	return out
}

// readWorkspaceYAML pulls id / cwd / created_at out of a copilot workspace.yaml.
// 未知の版で形が変わったら cwd が空になり、そのセッションは候補から外れる — 誤採用より
// 「拾えない」に倒す（固まったままは現状維持だが、他人の会話を映すのは実害）。
func readWorkspaceYAML(path string) (id, cwd string, created time.Time) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", "", time.Time{}
	}
	for _, line := range strings.Split(string(b), "\n") {
		key, val, ok := strings.Cut(line, ":")
		if !ok || strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			continue // ネストした値は見ない（必要なのは最上位の 3 キーだけ）
		}
		val = strings.TrimSpace(val)
		switch strings.TrimSpace(key) {
		case "id":
			id = val
		case "cwd":
			cwd = val
		case "created_at":
			if t, err := time.Parse(time.RFC3339, val); err == nil {
				created = t
			}
		}
	}
	return id, cwd, created
}
