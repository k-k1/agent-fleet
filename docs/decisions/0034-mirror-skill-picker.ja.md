# 0034. ミラーのスキルピッカー — セッション単位 API ＋コンポーザー内補完

[English](0034-mirror-skill-picker.md) | 日本語

- 状態: **採用・実装済み**（2026-07-28）。設計と実装記録は [50-mirror-skill-picker.md](../log/50-mirror-skill-picker.md)。
- 関連: [0017](0017-keyboard-system.ja.md)（キーボード体系）/ [0015](0015-agent-managed-driver.ja.md)（driver 抽象 — turn 素通しの前提）

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

## 追記（同日 v2）: クロスエージェント化

利用者要望「Claude 以外でも使いたい」を受け、codex / opencode / cursor を追加した。
全ソース・起動形をライブ実測してから実装（docs/50 §7 が根拠）。

### 4. 起動文字列は API が `invoke` として返す（UI は kind を知らない）

起動形が kind で割れた（claude/opencode/cursor `/name`・codex は `$name` メンション）。
UI に kind 分岐を持ち込まず、Agent が `invoke`（差し込む文字列そのもの）を返す契約に
した。タイプで開くトリガ文字だけ registry（`skillTrigger`）に持つ。

### 5. cursor は CLI 広告リストが正（FS 走査ではなく）

ACP `available_commands_update` が builtin スキル＋global＋project の完全な一覧を
流してくる（実測）。driver の onNotify（従来読み捨て）から in-memory ストア
（`agents.PublishCommands`）へ publish し handler が読む。**GET から driver を
Resume しない**（runtime を起こす副作用が出る）— 未着時は project FS へフォールバック。

### 6. 未検証の経路・kind は立てない

- opencode の managed（server API）経由 /command 発火は未検証 → `slashSkillsManaged:
  false` でミラー側をゲート（TUI セッションのみ表示）。
- kiro は広告の `prompts`（ユーザー定義）が実データ 0 件で形未検証、組み込みだけでは
  雑音 → 見送り。copilot / agy は機構自体が未確認/suspect → 見送り。

## 追記（同日 v3・**v4 で撤回**）: スキルブリッジ＝起動時のマーカー付きコピー同期

利用者要望「リンクを置かず実行時に橋渡し」「両方のフォルダのスキルをどちらの
エージェントからも」「シンボリックリンクはしたくない」を受け、一度は
`.claude/skills` ⇄ `.codex/skills` の**マーカー付きコピー双方向同期**
（`internal/skillbridge`・info/exclude で status 非汚染）を実装した。しかし
**git には見えなくても実ファイルをプロジェクトディレクトリに置く**方式であり、
利用者が「プロジェクトを汚すのか」と指摘 → 撤回（コードごと削除。実装の要点と
安全規約はこの節の git 履歴に残る）。教訓: 「status を汚さない」と「ディレクトリを
汚さない」は別の要件。

## 追記（同日 v4）: クロススキル注入 — ファイルに触らない橋渡し（採用）

利用者提案「他のエージェントのスキルもピッカーの候補に出し、選択されたら
プロンプトにして注入すればいい」をそのまま採った（docs/50 §8）。

### 7. 橋渡しはファイル操作でなくプロンプト注入

- API が他規約（`.claude/skills` / `.codex/skills` / `.agents/skills`）の SKILL.md を
  **foreign エントリ**（`path`＋`origin`・`invoke` 空）として一覧に混ぜ、Console が
  選択時に「`{path} を読んで、そのスキルの指示に従って実行して。`」を差し込む。
- 捨てた案との比較: symlink（利用者却下）／コピー同期（v3 — ディレクトリ汚染で撤回）
  ／codex `skills/extraRoots/set` RPC（無書込だが codex 片方向のみ・claude に相当
  機構なし・TUI ドライバに届かない可能性）。注入は**無書込・双方向・全 kind・
  全ドライバ**を一度に満たす唯一の案だった。
- 副産物: ネイティブのスキル機構を持たない kiro / copilot / agy でもピッカーが成立
  （foreign のみ・ボタン起点）。opencode の managed ゲートはネイティブ項目限定になり、
  managed opencode でも foreign は使える。
- 限界: ネイティブ起動と違い CLI のスキルランタイム（claude の context: fork や
  allowed-tools 制約等）は通らず、本文解釈はモデル任せ。正確さが要る場面はネイティブ
  規約の側にスキルを置く。

## 残る非対称（既知・本 ADR の範囲外）

- managed 経路はスラッシュでも `markSessionWorking` する（tui のガードの managed 版が
  無い）— 既存の非対称。直すなら別タスク。
- kiro の広告リスト（`_kiro.dev/commands/available`）は引き続き読み捨て。取り込みは
  cursor と同じ publish 経路に流すだけ（docs/50 §7.4）。
- ブリッジは repo 内の 2 規約のみ（`.agents/skills` と user レベルは対象外 — codex は
  `.agents/skills` をネイティブに読むので不要、claude 側から見えないのは既知）。

## 追記（2026-07-30 v5）: 引数入力中は閉じずに受動表示・修飾キー即送信は廃止

利用者指摘「引数を入力しようとスペースを打つと候補が消える。引数を参照したいので
出したままにしたい」「Ctrl＋クリックの即送信は要らない」を受けた 2 点の変更（docs/50 §2.2）。

### 8. 「補完」と「引数ヒント」を 1 つのリストの 2 モードに分ける

- `slashTokenAt` はキャレットが先頭トークンの右にある間も `args=true` のトークンを
  返す（従来は null＝閉じる）。表示は名前完全一致の 1 件だけ（`exactSkills`）に絞る —
  目的が「打ち終えたコマンドの `argument-hint` を見ながら引数を書く」ことなので、
  部分一致の別候補はノイズ。一致 0 件（ただの `/` 始まりの文章）は描画しない。
- 受動表示では**キーボードを横取りしない**のが要（Enter＝送信・↑/↓＝キャレット/履歴）。
  横取りしたままリストを生かすと、引数を書いた後 Enter で送れないという致命的な劣化に
  なる。選択ハイライト（sel）も出さず、クリックだけを生かす＝「Enter で確定できそう」
  という誤解も与えない。
- 差し込み直後のキャレットは `invoke` 末尾空白の右＝引数位置なので、選んだ瞬間から
  この受動表示に入る（＝選択 → ヒントを見ながら引数、が一続きになる）。

### 9. 修飾キー＋クリックの即送信を廃止

v1 で「返信サジェストと同じイディオム」として残した例外（`ADR §2`）を撤回。利用者は
使っておらず、引数付きスキルでの誤爆リスクだけが残る。§8 で引数を書く導線が主動線に
なった以上、ピッカーは**差し込み一択**で首尾一貫させる。
