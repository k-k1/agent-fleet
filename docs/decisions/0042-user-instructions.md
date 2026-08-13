# 0042. ユーザー指示は AF が所有する 1 本の本文とし、各 CLI の global 指示へマーカー合成で配る

- 状態: 採用・未実装（設計は docs/60。P0 の実測 3 点は未了＝docs/60 §60.15）
- 関連: [60-user-instructions.md](../60-user-instructions.md) /
  [57-project-tools.md](../57-project-tools.md)（配布軸 / 管理軸の区分） /
  [0031-mcp-registry.md](0031-mcp-registry.md)（配布軸の所有台帳） /
  [0040-project-mcp.md](0040-project-mcp.md)（管理軸＝所有しない側の判断） /
  [0022-agent-memory-management.md](0022-agent-memory-management.md)（第 3 の場所＝エージェントメモリ）

## 背景

エージェントが常時読む指示は、フリート方針（イメージ焼き込み）とプロジェクト指示
（リポジトリにコミットされる）の 2 層しかない。その中間 —「この人の働き方」— を置く場所が無い。

さらに実測（2026-08-13）で、中間層が「無い」だけでなく**作れない**ことが分かった。

- `workspace/entrypoint.sh:566-575` は毎起動 `cp -f` で `~/.codex/AGENTS.md` と
  `~/.config/opencode/AGENTS.md` を上書きする。利用者の追記は黙って消える。
- claude 側の配布先 `/etc/claude-code/CLAUDE.md` は root 所有で、コンテナ内の `dev` は書けない。
- `~/.gemini/AGENTS.md` は 450 B（rtk ブロックのみ）＝ **agy はフリート方針すら読んでいない**。
  cursor / kiro / copilot には配布経路自体が無い。

加えて 2 つの罠が測れた。①opencode は `<home>/.claude/CLAUDE.md` を global 指示として読むが、
AF は claude に `CLAUDE_CONFIG_DIR=/var/lib/af/claude` を渡しているので claude はそこを読まない
（置き場を間違えると別 kind にだけ効く）。②codex の `AGENTS.md` はバイト予算制
（`core/src/agents_md.rs` の `remaining_bytes` / `project doc exceeds remaining budget; truncating`、
上流既定 32 KiB）で、フリート方針だけで 29.9 KB ＝ 予算の 91% を使っている。

## 決定

1. **ユーザー指示を AF が所有する成果物として新設する。** 正本は
   `~/.config/agent-fleet/user-notes.md`。`homeKeep`（`control-plane/runtime_docker.go:396`）に
   `.config` があるため recreate も「home 掃除」も生き残る。CP の DB には置かない。
2. **本文は 1 本、適用先は kind ごとのチェック。** kind 別本文は同じ文章の N 重管理を作るため採らない。
   未対応 kind も行として出し「未対応 / 未検証」バッジを付ける（docs/57 §2 の作法）。
3. **配布軸として扱う。** AF が自動で書き、マーカーで所有を示す。docs/57 の
   「プロジェクトファイル憲章 8 条」（自動契機を作らない・所有マーカーを置かない）は**適用しない**。
   逆に、コミットされる場所には一切書かない。
4. **1 ファイルにライターは 1 人にする。** 追記者を増やさず、`reconcileAgentRTK` を
   `reconcileAgentInstructions` へ格上げして「フリート本文 ＋ ユーザーブロック ＋ rtk ブロック ＋
   マーカー外の温存」を毎回まるごと組み立てる。entrypoint の `cp -f`（全消し）はマーカー合成に置換する。
   ユーザーブロックの適用は必ず agent 側（entrypoint だと生存中の Console 編集が反映されない）。
5. **優先順位は本文に散文で書く。** フラットな 1 ファイルに合成する以上、階層の信号は文章でしか伝わらない。
   ユーザーブロック先頭に「衝突時はワークスペース方針が優先」を AF が固定文で入れる。
6. **claude だけは合成しない。** native な user memory 層があるので独立ファイルとして書き、
   managed policy には触らない。ただし**実パスは実測してから配線する**（罠①）。
7. **サイズ上限を機能として持つ。** ハード上限 8 KB、エディタにバイト数と codex 予算の残量を表示する。
   「保存できません」ではなく「どの kind で何が切られるか」を出す。
8. **エージェントに書かせない。** 編集用 MCP ツールは作らず、経路は Console REST のみ
   （置き場は `fs.go` の denylist 内でファイルペインからも触れない）。peer の依頼で書き換えない旨を
   `workspace-notes.md` に追記する。
9. **フリート層の穴（agy/cursor/kiro/copilot）は同じ合成器で塞ぐ。** 別機構を作らない（docs/60 §60.13 P3）。

## 却下した案

- **entrypoint の `cp -f` をマーカー合成に変えるだけ（UI なし）。** 破壊は止まるが、利用者は kind ごとに
  N 箇所へ同じ文章を書くことになり、claude は `/etc` なので原理的に不可能。合成方式だけ採用した。
- **セッション起動時に `--append-system-prompt` 相当で渡す。** claude しか揃わず、managed 経路と二重になる。
- **CP の DB に置いてワークスペース跨ぎで共有。** 永続性の要求は home 側で満たせている。v2 の選択肢。

## 影響

- `workspace/entrypoint.sh` の配布ロジックが agent 側の合成器へ移る（entrypoint は基底配置のみ）。
- `codex/rtk.go` と `agy/rtk.go` に重複している `stripMarkedBlock` を `internal/mdblock` へ括り出す。
- 新 REST は `workspace/agent/routes.go` と `control-plane/routes.go` の**両方**へ登録が必要。
- docs/39 の棚卸し表（「共通」行が agy も配布対象としている点）を docs/60 §60.2 で訂正した。
- 罠②の実測次第では、**フリート方針そのものを痩せさせる**判断が別途必要になる。
