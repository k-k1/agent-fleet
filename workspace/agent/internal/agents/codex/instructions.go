package codex

// 指示ファイル（docs/60）の codex 側 artifact。
//
// codex は「追加の指示ファイルを指す設定」を持たない（0.147.0 のキー一覧を実測 —
// project_doc_max_bytes / project_doc_fallback_filenames はあるが instructions_file 系は無い）。
// よってフリート方針もユーザー指示も **$CODEX_HOME/AGENTS.md 1 枚を合成する**しかない。
// マーカーで囲むので、利用者が同じファイルへ書いた文章は残る（cp -f 時代との違い）。
//
// ファイル内の並びは reconcile の呼び出し順どおり fleet → user-notes → rtk。
//
// サイズは気にしなくてよい（実測 2026-08-13）: project_doc_max_bytes（既定 32KiB）は
// **プロジェクト文書チェーンの合計**にのみ効き、$CODEX_HOME/AGENTS.md は予算外で
// 上限が無い（42KB の global が無傷で通ることを codex debug prompt-input で確認）。

import "github.com/k-k1/agent-fleet/workspace/agent/internal/mdblock"

// ApplyFleetNotes composes the baked workspace guide into AGENTS.md as an
// agent-fleet-owned block. An empty guide (image without one) is a no-op rather than
// a removal — dropping the guide because we could not read it would be worse than
// leaving the previous copy in place.
func ApplyFleetNotes(fleet string) error {
	if fleet == "" {
		return nil
	}
	return editAgents(func(s string) string {
		if !mdblock.Has(s, "fleet") {
			s = mdblock.StripLegacyPrefix(s, fleet)
		}
		return mdblock.Set(s, "fleet", fleet)
	})
}

// ApplyUserInstructions writes (or removes, when body is empty) the user-notes block.
func ApplyUserInstructions(body string) error {
	return editAgents(func(s string) string { return mdblock.Set(s, "user-notes", body) })
}
