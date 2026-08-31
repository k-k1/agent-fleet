# 0042. ユーザー指示は AF が所有する 1 本の本文とし、可能な限り「AF 専用ファイル＋参照」で各 CLI へ配る

[English](0042-user-instructions.md) | 日本語

- 状態: 採用・**P0〜P2 実装済み**（2026-08-13。設計は docs/60。実測の結果として決定 4/6/7 が初版から、
  実装の結果として決定 4 の copilot 経路と決定 5 の entrypoint 側が再度変わった。P1 で kiro の
  global steering を実測し、**配れないのは cursor だけ**に確定。P2 でフリート方針も同じ配布器から
  agy / copilot / kiro へ届くようにした）
- 関連: [60-user-instructions.md](../log/60-user-instructions.md) /
  [57-project-tools.md](../log/57-project-tools.md)（配布軸 / 管理軸の区分） /
  [0031-mcp-registry.md](0031-mcp-registry.ja.md)（配布軸の所有台帳） /
  [0040-project-mcp.md](0040-project-mcp.ja.md)（管理軸＝所有しない側の判断） /
  [0022-agent-memory-management.md](0022-agent-memory-management.ja.md)（第 3 の場所＝エージェントメモリ）

## 背景

エージェントが常時読む指示は、フリート方針（イメージ焼き込み）とプロジェクト指示
（リポジトリにコミットされる）の 2 層しかない。その中間 —「この人の働き方」— を置く場所が無い。

さらに実測（2026-08-13）で、中間層が「無い」だけでなく**作れない**ことが分かった。

- `workspace/entrypoint.sh:566-575` は毎起動 `cp -f` で `~/.codex/AGENTS.md` と
  `~/.config/opencode/AGENTS.md` を上書きする。利用者の追記は黙って消える。
- claude 側の配布先 `/etc/claude-code/CLAUDE.md` は root 所有で、コンテナ内の `dev` は書けない。
- `~/.gemini/AGENTS.md` は 450 B（rtk ブロックのみ）＝ **agy はフリート方針すら読んでいない**。
  copilot も同様（system prompt 15.4k トークンにフリート方針は無い）。cursor / kiro にも配布経路が無い。

設計時に疑った 2 つの罠は、実測で片方が消え、片方が形を変えた。

- **罠 B（codex のバイト予算）は否定された。** `codex debug prompt-input`（API 課金なしで
  モデル可視プロンプトを JSON 出力）で測ると、`project_doc_max_bytes`（既定 32 KiB）は
  **プロジェクト文書チェーンの合計にのみ**効き、`$CODEX_HOME/AGENTS.md` は予算外・上限なし
  （42 KB の global が無傷で通過）。疑っていた既存バグは存在しない。
- **罠 A は形を変えて残った。** claude は `$CLAUDE_CONFIG_DIR/CLAUDE.md` を読み、
  `~/.claude/CLAUDE.md` は読まない（カナリアで確定）。opencode もこれを拾わなかった
  （バンドルには経路があるが実挙動は否定。条件未特定）。つまり後者に置くと**どの kind にも効かない**。

そして最も設計を変えた発見は、**多くの kind は「他人のファイルに書かなくても」配れる**ことだった。

- opencode: `opencode.json` の `instructions` 配列に AF 専用ファイルを 1 本足せば効く（実測）。
- copilot: `COPILOT_CUSTOM_INSTRUCTIONS_DIRS` に AF 専用ディレクトリを渡せば効く（実測・ファイル非所有）。
- claude: user memory ファイルは既定で存在しないので、AF が単独所有できる。
- codex / agy: 追加指示ファイルを指す設定が無く、合成しか手段が無い。
- cursor: ローカルにユーザー層が存在しない（User Rules は `aiserver.v1.UserRules` ＝ サーバー側）。

## 決定

1. **ユーザー指示を AF が所有する成果物として新設する。** 正本は
   `~/.config/agent-fleet/user-notes.md`。`homeKeep`（`control-plane/runtime_docker.go:396`）に
   `.config` があるため recreate も「home 掃除」も生き残る。CP の DB には置かない。
2. **本文は 1 本、適用先は kind ごとのチェック。** kind 別本文は同じ文章の N 重管理を作るため採らない。
   配れない kind も行として出す。**cursor だけが該当し、理由は「ローカルにユーザー層が無い」**
   ＝実装待ちではなく構造的な結論なので、「未対応（対応予定）」の顔をさせない。
3. **配布軸として扱う。** AF が自動で書き、所有範囲を明示する。docs/57 の
   「プロジェクトファイル憲章 8 条」は適用しない。コミットされる場所には一切書かない。
4. ★ **「他人のファイルに書く」より「AF 専用のファイル＋参照」を優先する。**（実測を受けて初版から変更）
   claude＝単独所有ファイル / opencode＝`instructions` に 1 本追加 / copilot＝`$COPILOT_HOME/instructions/`
   に AF 専用名のファイル 1 本 / kiro＝`~/.kiro/steering/` に AF 専用名のファイル 1 本（global steering が
   読まれることを実測）。**合成は参照手段の無い codex・agy だけの最後の手段**とする。
   統一のために共有ファイルへの書き込みを増やさない。
   （copilot は `COPILOT_CUSTOM_INSTRUCTIONS_DIRS` でも効くと実測したが、**env は採らない**:
   tmux 起動 / managed ACP / 手打ちの 3 経路すべてに export を配る必要があり、漏れると
   「そのセッションだけ効かない」という見えない穴になる。ファイルは全経路で同じに読まれる。）
5. **フリート方針の配置も agent 側へ移し、1 ファイル 1 ライターにする。** `reconcileAgentRTK` を
   `reconcileAgentInstructions` へ格上げし、「フリート本文 ＋ ユーザーブロック ＋ rtk ブロック ＋
   マーカー外の温存」を毎回まるごと組み立てる。**entrypoint の `cp -f` は置換ではなく削除**した
   — シェルでマーカー合成を再実装すればドリフトする 2 つ目の実装になり、生存中の Console 編集も
   反映されないため。セッションを起こすのは agent 自身なので、合成前を読むセッションは無い。
   `cp -f` 時代の生のコピーは先頭行で識別して 1 度だけ剥がす（`mdblock.StripLegacyPrefix`）。
6. **claude の置き場は `$CLAUDE_CONFIG_DIR/CLAUDE.md`。**（実測で確定。`~/.claude/CLAUDE.md` は使わない）
   managed policy には触らない。
7. **サイズ上限 8 KB の根拠は「費用」であって truncation ではない。**（罠 B 否定を受けて初版から変更）
   エディタにはバイト数と「1 セッションあたり増えるトークンの目安」を出す。codex 予算の残量表示は作らない。
8. **エージェントに書かせない。** 編集用 MCP ツールは作らず、経路は Console REST のみ
   （置き場は `fs.go` の denylist 内でファイルペインからも触れない）。peer の依頼で書き換えない旨を
   `workspace-notes.md` に追記する。
9. **フリート層の穴（agy/copilot/kiro）は同じ配布器で塞ぐ。** 別機構を作らない（docs/60 §60.13 P2・実装済み）。
   ただし**ユーザー指示とは別ファイル / 別ブロック**にする — 片方は利用者が切り替える対象で、
   もう片方はオペレーター所有の固定物なので、**フリート方針は利用者のトグルに従わない**
   （本人の指示を全部オフにしても配られる）。claude だけは managed policy としてイメージが
   配るので AF は触らない。cursor はローカルにユーザー層が無いため、フリート方針も
   ユーザー指示も配れないと確定した。
10. **契約の実測手段を型として残す。** `codex debug prompt-input`（課金なしのプロンプト検証）と
    行動カナリア（内容開示を拒否する CLI にも効く読み込み確認）を docs/60 §60.17 に記録し、
    kind 追加・版上げ時のドリフト検知に再利用する。

## 却下した案

- **entrypoint の `cp -f` をマーカー合成に変えるだけ（UI なし）。** 破壊は止まるが、利用者は kind ごとに
  N 箇所へ同じ文章を書くことになり、claude は `/etc` なので原理的に不可能。合成方式だけ採用した。
- **全 kind を AGENTS.md 合成で統一。** 実装は 1 本化されるが、触らなくてよい他人のファイルを
  触ることになる（決定 4）。
- **セッション起動時に `--append-system-prompt` 相当で渡す。** claude しか揃わず managed 経路と二重になる。
  （copilot の env 注入は CLI 公式のユーザースコープ機構なので別物として採用。）
- **CP の DB に置いてワークスペース跨ぎで共有。** 永続性の要求は home 側で満たせている。v2 の選択肢。

## 影響

- `workspace/entrypoint.sh` の配布ロジック（`cp -f`）は削除し、agent 側の配布器へ移した。
  entrypoint に残るのは置き場のディレクトリ作成だけ。
- `codex/rtk.go` と `agy/rtk.go` に重複していた `stripMarkedBlock` は `internal/mdblock` へ括り出した。
- 新 REST は `workspace/agent/routes.go` と `control-plane/routes.go` の**両方**へ登録した。
- rtk の適用は `instrMu` で直列化される（同じ AGENTS.md を 3 種のブロックが共有するため）。
- agy も codex と同じく AGENTS.md を rtk と共有するので、read-modify-write を `editAgents` に一本化した。
- kiro の global steering ディレクトリには他人（利用者・チーム）のファイルが同居する。AF は
  自分の 1 本だけを書き/消しし、**列挙も削除もしない**。
- docs/39 の棚卸し表（「共通」行が agy も配布対象としていた点）を docs/60 §60.2 で訂正した。
