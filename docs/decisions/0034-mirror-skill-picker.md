# 0034. ミラーのスキルピッカー — セッション単位 API ＋コンポーザー内補完

- 状態: **採用・実装済み**（2026-07-28）。設計と実装記録は [50-mirror-skill-picker.md](../50-mirror-skill-picker.md)。
- 関連: [0017](0017-keyboard-system.md)（キーボード体系）/ [0015](0015-codex-app-server.md)（driver 抽象 — turn 素通しの前提）

## 背景

セッションに定義されたスキル（`.claude/skills`）とカスタムコマンド（`.claude/commands`）を
ミラービューから呼ぶ動線が無く、名前のフルタイプかターミナル側 TUI 補完に頼っていた。
起動モーダルには repo 単位のテンプレ集約（`repo_prompts.go`）が既にあるが、repo 名 →
`~/repos/<name>` 固定で worktree セッションの実体もユーザーレベルも見えない。
要件は「キーボードだけ・マウスだけ・タップだけ、いずれの操作系でも完結」。

## 決定

### 1. 一覧はセッション単位の新 API（repo 単位の流用ではなく）

`GET /sessions/{name}/skills`。`session.ReadMeta(name).Dir` で worktree 実パスを、
`claude.ConfigDir()` でユーザーレベルを走査する。repo_prompts の流用案は
worktree（`repo@branch`）とユーザーレベルを表現できず棄却。frontmatter パーサ
（`splitFrontmatter`）だけ共用する。`argument-hint` を新たに解釈し、
`user-invocable: false` を除外する。重複はスラッシュ名で project > user・skill > command
の先勝ち。claude 以外の kind は**エラーでなく空**（前方互換 — 将来 cursor/kiro の
ACP `available_commands` を同じ形で返せる）。キャッシュ無し（都度走査で十分安い＋
セッション途中で SKILL.md を書かせる使い方に即応）。

### 2. UI はコンポーザー内インライン補完＋常設「/」ボタンの 2 系統

- キーボード派: 入力欄先頭の `/` タイプで開き、タイプで絞り込み、↑/↓ → Enter/Tab。
- マウス/タップ派: コンポーザー左の「/」ボタン（＋添付ボタンと同寸で並ぶ）。
- 確定は**差し込みのみ**（`/name ␣` ＋既存下書きを引数として温存）で送信しない —
  引数を確認してから送る。修飾キー＋クリックの即送信だけ例外（返信サジェストと同じ
  イディオム）。
- 選択リストは CommandPalette 同型の sel-index 方式（フォーカスを textarea に残す）。
  タッチ確定時はフォーカスを奪わない（GBoard 既存規約）。
- kind ゲートは `AgentCaps.slashSkills`（kind 三項演算子の禁止 — registry 一元）。

### 3. 送信経路には手を入れない

スラッシュ文字列は tui / managed とも既存経路で素通しされ、turn 抑止（`slashCmdRe`）も
既存挙動のまま。本機能は「認識と入力補助」だけを足す。

## 捨てた案

- **`repoPromptTemplates(sessionMeta.repo)` の流用**: worktree 名（`repo@branch`）が
  `resolveRepoDir` の repo 名検証を通らない／ユーザーレベル・argument-hint が無い。
- **選択で即送信**: 引数付きスキル（argument-hint 持ち）で誤爆する。差し込み一択にし、
  即送信は修飾キーの明示操作だけに残した。
- **返信サジェスト式のフォーカス移動リング**: スマホで textarea の blur → ソフト
  キーボード落ちが起きる。sel-index 方式へ。
- **タイプ起点でも空リストを表示**: `/plan` 等、列挙外コマンドの手打ちを覆い隠す。
  該当ゼロ時は非表示（ボタン起点だけ「無い」ことを見せる）。
- **agent 側 TTL キャッシュ**: 走査コストが小さく、鮮度（セッション中の SKILL.md
  追加）の方が価値が高い。

## 残る非対称（既知・本 ADR の範囲外）

- managed 経路はスラッシュでも `markSessionWorking` する（tui のガードの managed 版が
  無い）— 既存の非対称。直すなら別タスク。
- cursor / kiro は CLI が ACP でスキル一覧を流してくるのに読み捨てている。API 契約は
  受け皿になるよう設計済みだが配線は積み残し（docs/50 §6）。
