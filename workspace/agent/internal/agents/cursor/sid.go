package cursor

import (
	"os"
	"path/filepath"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// cursor は「押し付け型」— 我々が採番した v4 UUID を `--resume` で渡して新規チャットを
// 作らせ、以後それが使われている前提で転写も状態も引く。CLI がその id を使わなくなると
// ミラーは静かに空のまま固まる。cursor には status hook が無いので名乗りを聞く口も無い
// （internal/agents/imposedsid.go に経緯）。手掛かりはディスクだけ。
//
// resolveSid は「押し付けた id の転写が無い」ときにだけ、この cwd の会話を拾い直して
// 台帳を差し替える。読みの hot path（Transcript/LiveState が呼ぶ ChatID）には入れない。
func resolveSid(m session.Meta) string {
	return agents.ResolveImposedSID(sids, m, cliSessions)
}

// cliSessions enumerates cursor's own chats launched in dir.
//
// cursor は転写を projects/<cwdSlug(dir)>/agent-transcripts/<chatID>/<chatID>.jsonl に
// 置く — **cwd がパスに入っている**ので、帰属はディレクトリを読むだけで足りる。
//
// 作成時刻は転写ディレクトリの mtime を代理に使う。cursor は created_at を残さないが、
// このディレクトリは中の .jsonl を作った時に一度だけ更新され、以後の追記では動かない
// （実測: 手元 10 チャットで dir mtime ≤ file mtime、活動のあるものほど乖離する）。
// つまり mtime ≒ チャット作成時刻で、スロット作成時刻との突き合わせに使える。
func cliSessions(dir string) []agents.CLISession {
	root := filepath.Join(projectsDir(), cwdSlug(dir), "agent-transcripts")
	ents, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var out []agents.CLISession
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		id := e.Name()
		// 転写ファイルがまだ無いディレクトリは会話として数えない（起動直後の器）。
		if _, err := os.Stat(filepath.Join(root, id, id+".jsonl")); err != nil {
			continue
		}
		s := agents.CLISession{ID: id}
		if fi, err := e.Info(); err == nil {
			s.Created = fi.ModTime()
		}
		out = append(out, s)
	}
	return out
}
