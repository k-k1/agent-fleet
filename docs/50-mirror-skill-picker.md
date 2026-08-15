# 50. ミラーのスキルピッカー — セッションのスキル/コマンドを認識して 1 操作で呼ぶ

- 状態: **✅ 実装済み**（v1 claude 2026-07-28 / **v2 クロスエージェント同日** — codex・opencode・cursor 追加、実測記録は §7）。意思決定は [decisions/0034](decisions/0034-mirror-skill-picker.md)。
- 関連: [29](29-keyboard-system.md)（キーボード体系 — sel-index リストの流儀）/ [27](27-agent-managed-driver.md)（turn 経路）/ [40](40-cursor-agent-kind.md)・[43](43-kiro-agent-kind.md)（ACP）/ 起動モーダルのテンプレ集約（`workspace/agent/repo_prompts.go`）

---

## 0. 目的 / 非目的

**目的**: セッション（主に claude）に定義されたスキル（`.claude/skills/*/SKILL.md`）と
カスタムスラッシュコマンド（`.claude/commands/**/*.md`）を Console が**認識**し、
ミラービューのコンポーザーから**キーボードだけ・マウスだけ・タップだけ**のいずれでも
1〜2 操作で呼び出せるようにする。

これまでスキルを使うには (a) 名前を正確に記憶してフルタイプする、(b) ターミナル側の
TUI 補完へ行く、(c) 起動モーダルのテンプレ（新規セッション時のみ）を使う、の三択で、
「走行中セッションのミラーから呼ぶ」動線が無かった。

**非目的**（積み残し、§6）:
- claude の組み込みコマンド（/compact 等）の列挙 — CLI 版依存の契約になるため見送り
  （cursor は例外: CLI 広告リスト自体が builtin 込みで、それが正 — §7）。
- プラグイン由来スキル（`plugins/<marketplace>/…/skills`、`/plugin:skill` 起動形）。
- kiro（広告ペイロードの user 定義形が未検証 — §7.4）・copilot・agy（ユーザー起動可能な
  仕組み自体が未確認/未検証 — §7.5）。
- アシスタントチャット（ChatView）への同型ピッカー。

## 1. 現状（コード実測 2026-07-28）

- 走査の既存実装は起動モーダル用の `workspace/agent/repo_prompts.go` だけ。**repo 名 →
  `~/repos/<name>` 固定**なので worktree セッション（`~/repos/<repo>@<branch>`）の実体を
  見られず、ユーザーレベル（`$CLAUDE_CONFIG_DIR` 相当）も見ない。frontmatter は
  `name`/`description` のみ解釈（`argument-hint`・`user-invocable` を落とす）。
- スラッシュ入力自体は両ドライバとも素通し（tui = tmux type、managed = RPC prompt）で、
  サーバ側 `slashCmdRe`（session_io.go）が「turn を始めない」扱いにする — 送る側の
  基盤は既にある。ミラーは `<command-name>` タグ転写を CmdChip として描画済み。

## 2. 契約

### 2.1 REST（Agent → CP 中継）

`GET /sessions/{name}/skills`（CP: `GET /api/sessions/{name}/skills`・読み取りのみ・監査対象外）

```json
{ "skills": [ { "name": "proofread-a", "description": "…", "argumentHint": "<章番号>",
                "source": "project", "type": "skill", "invoke": "/proofread-a " } ] }
```

- **`invoke` がコンポーザーへ差し込む起動文字列そのもの**（末尾空白込み）。起動形は
  kind 依存（claude/opencode/cursor は `/name`、codex は `$name` メンション — §7）なので、
  UI は invoke を機械的に使うだけで kind を知らない。
- kind 別ソース（v2。全て 2026-07-28 実測 — §7）:

| kind | project | user | cli（同梱/広告） | 起動形 |
|---|---|---|---|---|
| claude | `.claude/skills`＋`commands` | `claude.ConfigDir()` 配下同 | — | `/name` |
| codex | `.codex/skills` | `$CODEX_HOME/skills` | `…/skills/.system` | `$name` |
| opencode | `.opencode/command(s)` | `~/.config/opencode/command(s)` | — | `/name` |
| cursor | （FS フォールバック: `.cursor/commands`＋`skills`） | — | **ACP 広告リストが正** | `/name` |

- `skills/*/SKILL.md` は frontmatter `name`（無ければディレクトリ名）・`description`・
  `argument-hint` を読み、**`user-invocable: false` は除外**（ユーザーから呼べない）。
  `disable-model-invocation` は「モデルが勝手に呼ばない」の意でユーザー起動は可 —
  除外しない（cursor 同梱 review スキル実測）。
- `commands/**/*.md` はファイル名（拡張子抜き）が起動名 — サブディレクトリは claude の
  名前空間表示に使われるだけで起動名には入らない。
- 重複は起動名で先勝ち: project > user > cli、同一ルート内では skill > command。
  name 昇順・全体 200 件で頭打ち。
- cursor の ACP 広告リストは driver が受信のたび `agents.PublishCommands`（in-memory の
  sync.Map・`internal/agents/commands.go`）へ publish し、handler が読む。未着
  （runtime 未起動・agent 再起動直後）は project FS へフォールバック。
- **未対応 kind は「エラーでなく空」**: Console 側の caps が第一防壁で、API 契約は
  将来 kiro の広告リスト等を同じ形で返せるよう前方互換に保つ。
- キャッシュ無し（ピッカーを開いた時に 1 回走る数十ファイルの read。セッション途中で
  SKILL.md を書かせる使い方が普通にあるので、都度走査が正）。

実装: `workspace/agent/session_skills.go`（frontmatter パーサは repo_prompts.go の
`splitFrontmatter` を共用）。登録は agent `routes.go` ＋ **CP `routes.go` の両方**
（明示許可リスト方式 — 漏れ再発防止のパリティテストを両側に追加:
`session_skills_test.go` / `session_skills_routes_test.go`）。

### 2.2 Console（ミラーコンポーザー）

kind ゲートは `AgentCaps.slashSkills` ＋ managed セッションでは `slashSkillsManaged`
（v2: claude/codex/cursor は両方 true、opencode は TUI のみ — §7.3）。タイプで開く
トリガ文字は registry の `skillTrigger`（claude/opencode/cursor `/`、codex `$`）。

開き方は 2 系統・閉じ方は 3 系統（表は claude の例。codex は `/` を `$` に読み替え）:

| 操作系 | 開く | 選ぶ | 確定 | 送信 |
|---|---|---|---|---|
| キーボードのみ | 入力欄先頭でトリガ文字（タイプで絞り込み） | ↑ / ↓ | Enter または Tab（`invoke` を差し込み） | 引数を打って Enter（設定に応じ Ctrl+Enter） |
| マウスのみ | コンポーザー左のトリガ文字ボタン | ホバー | クリックで差し込み | 送信ボタン |
| タップのみ | トリガ文字ボタンをタップ | — | タップで差し込み（**フォーカスは奪わない** — GBoard が画面を覆う既存規約に従う） | 送信ボタンをタップ |

- 閉じる: Esc / 外クリック / `/` ボタン再押下 / トークンが死ぬ（先頭 `/` を消す・
  送信や履歴呼び出しで下書きが差し替わる）。Esc 後は**同じトークンのままなら再表示
  しない**（skillDismissRef — 打ち直せばまた開く）。
- **引数入力中は閉じない（受動表示）**: 空白を打ってコマンドの右へ進むと
  `slashTokenAt` は `args=true` のトークンを返し、リストは**名前が完全一致する 1 件だけ**
  （`exactSkills`）に絞って出したまま残る — `argument-hint` と説明を見ながら引数を
  書けるようにするため。受動表示では**キーボードを横取りしない**（Enter＝送信・↑/↓＝
  キャレット移動／履歴・Tab＝従来どおり。Esc だけは閉じる）。差し込み直後もキャレットは
  末尾空白の右＝引数位置なので、選んだ直後からこの受動表示に入る。一致 0 件（ただの
  `/` 始まりの文章など）は描画しない。
- トリガは**全角エイリアス**（`／`・`＄` — JP IME で半角のつもりが全角になる）も受ける。
  確定時は invoke（半角の正しい起動形）で丸ごと置換されるので全角のまま送られない。
- foreign エントリ（§8）には**出所バッジ**: kind 色（tokens.css `--kind-*` 1 ソース）の
  ミニチップで `Claude` / `Codex`、`.agents` 由来は中立の「共有」。ネイティブ項目は
  従来どおりバッジ無し。
- 1 項目は **2 行**: 1 行目＝起動文字列＋`argument-hint`＋出所バッジ（右端）、
  2 行目＝説明。説明を独立行にしたのは、1 行に詰めていた頃は名前と引数に幅を食われて
  説明がほぼ読めなかったから。行数は倍でも**高さは 25px → 31px**（上下 padding
  5→2px・行間 1.15・説明を 11px に落として吸収。headless 実測）で、項目の切れ目は
  リスト側の 2px の隙間で示す。説明の無い項目は 1 行のまま（18px）。
- タイプ起点は**該当ゼロなら描画しない** — `/plan` など列挙外コマンドの手打ちを
  覆い隠さない。ボタン起点は空でも「無い」ことを見せる。
- 選択リストは CommandPalette と同型の **sel-index 方式**（フォーカスは textarea に
  残す・`onMouseMove` で追従・`onMouseDown` は `preventDefault`）。返信サジェスト
  チップのフォーカス移動式にしなかったのは、スマホでソフトキーボードが落ちるから。
- キー横取りは `onKeyDown` の**最上段**（Tab→チップ・↑↓履歴・Enter 送信より先）、
  IME 変換中（`isComposing`）は触らない。Ctrl/⌘+Enter と Shift+Enter は素通し
  （ピッカーを無視してそのまま送信/改行できる逃げ道）。
- 確定は**差し込みのみで送信しない**（引数を確認してから送る — 修飾キー＋クリックの
  即送信も廃止。引数付きスキルで誤爆する上、受動表示で引数を書く導線と噛み合わない）。
  `argument-hint` はリスト行に薄く表示。既存の下書きは引数として `/name ` の後ろへ残す
  （引数入力中に別コマンドを選び直した場合も、置き換わるのは先頭コマンドだけ）。
- 純ロジック（トリガ判定 `slashTokenAt`・絞り込み `filterSkills`・差し込み
  `applySkillToDraft`）は `features/mirror/skillPicker.ts` に分離し vitest で固定
  （mirrorParts / pendingEcho の家風）。

## 3. 実装ファイル

| 層 | ファイル |
|---|---|
| Agent | `workspace/agent/session_skills.go`（+ `_test.go` — foreign 列挙 `appendForeignSkills` 含む）・`routes.go`・`internal/agents/commands.go`（広告リスト共有ストア）・`internal/agents/cursor/driver.go`（onNotify で publish） |
| CP | `control-plane/routes.go`・`session_skills_routes_test.go` |
| Console API | `core/api/client.ts`（`SessionSkill` / `sessionSkills`） |
| Console UI | `features/mirror/MirrorView.tsx`・`mirror.css`・`skillPicker.ts`（+ `.test.ts`）・`agents/registry.ts`（`caps.slashSkills` / `slashSkillsManaged` / `skillTrigger`） |
| i18n | `lib/i18n/locales/ja.ts` / `en.ts`（`mirror.skills_*`） |

## 4. 検証（2026-07-28）

- Go: workspace/agent 全 pkg ok・control-plane ok（走査 / handler / ルート登録の
  パリティ両側。v2 で codex/opencode/cursor の走査＋広告リスト優先のテスト追加）。
- Console: `tsc --noEmit` ok・vitest 688 件 ok（skillPicker はトリガ文字/invoke 対応で
  拡張）・`i18n:lint` ok・`vite build` ok。
- クロスエージェントのソース・起動形はライブ実測（§7）。cursor は managed 発火まで実測。
- 実機目視は未実施（次のフリート再ビルド後に確認する）。

## 5. 挙動の根拠メモ

- スラッシュ送信は既存経路がそのまま使える: tui は `typeLineAndSubmit`（docs/38 の
  `/scout` 定時発火で実証済み）、managed は prompt 素通し。サーバの `slashCmdRe` が
  turn 扱いを抑止するのも従来どおりで、本機能は**送信経路に一切手を入れていない**。
- managed 経路には tui の「スラッシュは working を付けない」ガードが無い
  （`handleManagedInputPrompt` は無条件 `markSessionWorking`）— 既存の非対称で、
  本機能では悪化も改善もしない。直すなら別タスク。

## 6. 積み残し

- claude の組み込みコマンド・プラグインスキルの列挙（§0）。
- kiro: 広告リスト（`_kiro.dev/commands/available` — **cursor の
  `available_commands_update` とは別の専用メソッド**）の `prompts` にユーザー定義が
  載るはずだが実データ 0 件で形が未検証（§7.4）。取り込みは cursor と同じ
  `agents.PublishCommands` 経路に流し込むだけ。
- opencode の managed（server API）経由の /command 発火検証 → `slashSkillsManaged` 解禁。
- copilot / agy のユーザー定義コマンド機構の実測（§7.5）。
- ChatView（アシスタント）への同型ピッカー — コンポーザーがほぼ双子なので移植は容易。
- 実機目視（スマホのトリガボタン導線含む）。

## 7. クロスエージェント実測記録（2026-07-28・v2 の根拠）

repo 内の既存ドキュメントには claude 以外の「スキル相当」の実測が皆無だったため、
全 kind をこの環境でライブ検証した。

### 7.1 codex（0.145.0）

- バイナリ文字列（Rust バイナリの system prompt/スキル文言）から確定:
  - user ルート = **`$CODEX_HOME/skills`（未設定時 `~/.codex/skills`）で auto-discover**、
    同梱スキルは `skills/.system/<name>/SKILL.md`（imagegen / openai-docs / review-agent 等）。
  - repo 側ルートあり（"failed to stat repo skills root" ＋ eval 文中の `.codex/skills/…`）。
  - SKILL.md は **claude 互換 frontmatter**（name / description。
    `disable-model-invocation` は false 必須のバリデーション文字列あり）。
  - **起動はスラッシュではなく `$SkillName` メンション**: 「If the user names an
    available skill (with `$SkillName` or plain text) … you must use that skill」。
    テキストメンションなので TUI / managed どちらの経路でも成立する。
- 注意: `~/.codex/memories/skills/<name>/SKILL.md`（docs/39 の記憶成果物）は別物 —
  ピッカーのソースにしない。

### 7.2 opencode（1.18.8）

- バイナリ文字列から確定: project `.opencode/command/deploy.md` と `.opencode/commands/`
  の**単複両方**、`.opencode/skills/<name>/SKILL.md`、global `~/.config/opencode/command`。
- v2 は command のみ列挙。`.opencode/skills` は model 起動用でユーザーのスラッシュ起動が
  未検証のため対象外。
- **managed の /command 発火は未検証** — /command は TUI 機能で、server API に素の
  "/name" prompt を流して展開される保証が無い。`slashSkillsManaged: false` でゲート。

### 7.3 cursor（2026.07.23）

- `cursor-agent acp --trust` を素の JSON-RPC で叩いて実キャプチャ:
  - `session/update` の `{"sessionUpdate":"available_commands_update",
    "availableCommands":[{"name","description"}]}`。**builtin スキル（~/.cursor/skills-cursor）
    ＋ global コマンド＋ project の `.cursor/commands`・`.cursor/skills` が全部入り**
    （テスト用に置いた af-probe-cmd / af-probe-skill が両方載った）。description に
    "(global)" "(project)" "(builtin skill)" が埋め込まれて届く。
  - **発火実測**: `session/prompt` に text `"/af-probe-cmd"` を送ると project コマンドが
    実行された（"probe-ok." 応答）→ managed 経路 OK。
- よって cursor は広告リストが唯一の完全ソース。driver の onNotify（従来この update を
  黙って落としていた）で `agents.PublishCommands` へ流す。

### 7.4 kiro（見送りの根拠）

- `kiro-cli acp --agent-engine v2` の実キャプチャ: 専用メソッド
  **`_kiro.dev/commands/available`**（docs/50 v1 の記述「available_commands_update」は
  cursor の名前で不正確だった — 本節で訂正）で
  `{sessionId, commands[24], prompts[], tools[14], mcpServers[]}`。
  `commands` は `{name:"/agent", description, meta{subcommands…}}` の**組み込みのみ**、
  ユーザー定義が載るはずの `prompts` はこの環境で 0 件・要素形が未検証。
- 組み込みだけ出しても雑音なので v2 は見送り。取り込む時は kiro driver の onNotify
  （現在 `method != "session/update"` で早期 return している箇所）で publish するだけ。

### 7.5 copilot / agy（見送りの根拠）

- copilot: スラッシュ確定の仕組み自体は実測済み（docs/36 §GracefulStop）だが、
  **ユーザー定義コマンドの置き場が未確認**。
- agy: ADR0008 の `.agents/skills/*.md`（スラッシュコマンド）は**実装前の外部調査のまま
  未再検証**（docs/32 は AGENTS.md の読み込みですら ADR0008 の想定と違った実績あり）。
  suspect 扱いで見送り。

### 7.6 claude スキルの codex 流用（`codex exec` 実測）

- codex は repo の **`.codex/skills` と `.agents/skills` を読む**が **`.claude/skills` は
  読まない**（3 規約同時設置の exec 実測＋バイナリにパス文字列なし）。
- claude 固有 frontmatter（`argument-hint` / `user-invocable` / `allowed-tools` /
  `disable-model-invocation`）を全部付けた SKILL.md も codex の認識は壊れない（実測）
  — **形式は相互に読める**。§8（クロススキル注入）が素直に効く前提。
- 補: codex app-server には `skills/extraRoots/set` RPC が存在する（バイナリ実測・
  ファイル無書込でルート追加できる口）。claude 側に相当機構が無く双方向要件を満たせ
  ないため §8 では採らなかったが、codex 片方向だけ軽くやる時の代替として記録する。

## 8. クロススキル注入 — 他規約のスキルを「読んで従え」プロンプトで呼ぶ

**要件の変遷（利用者指定）**: ①リポジトリへリンクやコピーを自分で置かずに橋渡し →
②シンボリックリンク不使用 → ③両フォルダのスキルをどちらのエージェントからも →
④**プロジェクトディレクトリを一切汚さない**。①〜③で一度「マーカー付きコピーの
双方向同期＋info/exclude」（`internal/skillbridge`）を実装したが、④で撤回した
（git status には出ないが実ファイルは置く方式だったため。経緯は ADR0034 追記 v3/v4）。

**採った方式 — プロンプト注入**（利用者提案）: ファイルには一切触らない。

- API（§2.1）が、その kind の CLI が自力で発見しない**他規約の SKILL.md** を
  **foreign エントリ**（`invoke` 空・`path`＝repo 相対 SKILL.md・`origin`＝規約 dir）
  として一覧に混ぜる。foreign として見るのは repo 内の `.claude/skills` /
  `.codex/skills` / `.agents/skills`（SKILL.md ツリーのみ。commands 系は対象外）。
  ネイティブと同名はネイティブ勝ち。`user-invocable: false` は foreign でも除外。
- ピッカーで foreign を選ぶと、Console が
  「`{path} を読んで、そのスキルの指示に従って実行して。`」（i18n:
  `mirror.skills_use_foreign`・現在ロケール）を差し込む — **ただの指示文なので
  kind もドライバも選ばない**。引数はプロンプトの後ろに打ち足す。リスト行には
  origin バッジ（`.claude` 等）が付く。
- これにより **kiro / copilot / agy でもピッカーが点く**（ネイティブ列挙は無し・
  foreign のみ。`skillTrigger: ""` ＝タイプでは開かずボタンのみ）。opencode の
  managed ゲート（slashSkillsManaged=false）は**ネイティブ項目だけ**に効く —
  foreign は注入なので managed でも出す。
- 限界（正直に）: ネイティブ起動と違い、スキル本文の解釈精度はモデル任せ
  （claude の `context: fork` や `allowed-tools` 等のランタイム挙動は再現されない）。
  codex ⇄ claude は SKILL.md 形式互換（§7.6）なので実用上は素直に動く見込み。

**効き方**: repo に `.claude/skills/proofread` しか無くても、codex / opencode /
cursor / kiro / copilot / agy のミラーでピッカーに `proofread`（`.claude` バッジ）が
並び、選ぶと「.claude/skills/proofread/SKILL.md を読んで指示に従え」が入力欄に入る。
スキルの正本はどちらか片方に置けばよい。
