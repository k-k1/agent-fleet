# 50. ミラーのスキルピッカー — セッションのスキル/コマンドを認識して 1 操作で呼ぶ

- 状態: **✅ 実装済み**（2026-07-28）。意思決定は [decisions/0034](decisions/0034-mirror-skill-picker.md)。
- 関連: [29](29-keyboard-system.md)（キーボード体系 — sel-index リストの流儀）/ [27](27-agent-managed-driver.md)（turn 経路）/ 起動モーダルのテンプレ集約（`workspace/agent/repo_prompts.go`）

---

## 0. 目的 / 非目的

**目的**: セッション（主に claude）に定義されたスキル（`.claude/skills/*/SKILL.md`）と
カスタムスラッシュコマンド（`.claude/commands/**/*.md`）を Console が**認識**し、
ミラービューのコンポーザーから**キーボードだけ・マウスだけ・タップだけ**のいずれでも
1〜2 操作で呼び出せるようにする。

これまでスキルを使うには (a) 名前を正確に記憶してフルタイプする、(b) ターミナル側の
TUI 補完へ行く、(c) 起動モーダルのテンプレ（新規セッション時のみ）を使う、の三択で、
「走行中セッションのミラーから呼ぶ」動線が無かった。

**非目的**（v1 の積み残し、§6）:
- 組み込みコマンド（/compact 等）の列挙 — CLI 版依存の契約になるため見送り。
- プラグイン由来スキル（`plugins/<marketplace>/…/skills`、`/plugin:skill` 起動形）。
- cursor / kiro の ACP `available_commands_update`（CLI 自身がスキル一覧を流してくるが
  現状読み捨てている）をこの API へ流し込むこと。
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
                "source": "project", "type": "skill" } ] }
```

- 走査ルートは 2 つ: **project** = `meta.Dir/.claude`（セッションの worktree 実パス）、
  **user** = `claude.ConfigDir()`（`$CLAUDE_CONFIG_DIR` 尊重 — テストが差し替える）。
- `skills/*/SKILL.md` は frontmatter `name`（無ければディレクトリ名）・`description`・
  `argument-hint` を読み、**`user-invocable: false` は除外**（ユーザーから呼べない）。
- `commands/**/*.md` はファイル名（拡張子抜き）が起動名 — サブディレクトリは claude の
  名前空間表示に使われるだけで起動名には入らない。
- 重複はスラッシュ名で先勝ち: project > user、同一ルート内では skill > command
  （claude のスラッシュ名前空間は 1 つ）。name 昇順・全体 200 件で頭打ち。
- **kind ゲートは「エラーでなく空」**: claude 以外は `{"skills": []}`。Console 側の
  caps が第一防壁で、API 契約は将来 ACP 由来の一覧を同じ形で返せるよう前方互換に保つ。
- キャッシュ無し（ピッカーを開いた時に 1 回走る数十ファイルの read。セッション途中で
  SKILL.md を書かせる使い方が普通にあるので、都度走査が正）。

実装: `workspace/agent/session_skills.go`（frontmatter パーサは repo_prompts.go の
`splitFrontmatter` を共用）。登録は agent `routes.go` ＋ **CP `routes.go` の両方**
（明示許可リスト方式 — 漏れ再発防止のパリティテストを両側に追加:
`session_skills_test.go` / `session_skills_routes_test.go`）。

### 2.2 Console（ミラーコンポーザー）

kind ゲートは `AgentCaps.slashSkills`（registry — v1 は claude のみ true）。

開き方は 2 系統・閉じ方は 3 系統:

| 操作系 | 開く | 選ぶ | 確定 | 送信 |
|---|---|---|---|---|
| キーボードのみ | 入力欄先頭で `/`（タイプで絞り込み） | ↑ / ↓ | Enter または Tab（`/name ␣` を差し込み） | 引数を打って Enter（設定に応じ Ctrl+Enter） |
| マウスのみ | コンポーザー左の「/」ボタン | ホバー | クリックで差し込み | 送信ボタン。**Ctrl/⌘/Alt＋クリックで即送信** |
| タップのみ | 「/」ボタンをタップ | — | タップで差し込み（**フォーカスは奪わない** — GBoard が画面を覆う既存規約に従う） | 送信ボタンをタップ |

- 閉じる: Esc / 外クリック / `/` ボタン再押下 / トークンが死ぬ（空白を打って引数へ
  進む・先頭 `/` を消す）。Esc 後は**同じトークンのままなら再表示しない**
  （skillDismissRef — 打ち直せばまた開く）。
- タイプ起点は**該当ゼロなら描画しない** — `/plan` など列挙外コマンドの手打ちを
  覆い隠さない。ボタン起点は空でも「無い」ことを見せる。
- 選択リストは CommandPalette と同型の **sel-index 方式**（フォーカスは textarea に
  残す・`onMouseMove` で追従・`onMouseDown` は `preventDefault`）。返信サジェスト
  チップのフォーカス移動式にしなかったのは、スマホでソフトキーボードが落ちるから。
- キー横取りは `onKeyDown` の**最上段**（Tab→チップ・↑↓履歴・Enter 送信より先）、
  IME 変換中（`isComposing`）は触らない。Ctrl/⌘+Enter と Shift+Enter は素通し
  （ピッカーを無視してそのまま送信/改行できる逃げ道）。
- 確定は**差し込みのみで送信しない**（引数を確認してから送る）。`argument-hint` は
  リスト行に薄く表示。既存の下書きは引数として `/name ` の後ろへ残す。
- 純ロジック（トリガ判定 `slashTokenAt`・絞り込み `filterSkills`・差し込み
  `applySkillToDraft`）は `features/mirror/skillPicker.ts` に分離し vitest で固定
  （mirrorParts / pendingEcho の家風）。

## 3. 実装ファイル

| 層 | ファイル |
|---|---|
| Agent | `workspace/agent/session_skills.go`（+ `_test.go`）・`routes.go` |
| CP | `control-plane/routes.go`・`session_skills_routes_test.go` |
| Console API | `core/api/client.ts`（`SessionSkill` / `sessionSkills`） |
| Console UI | `features/mirror/MirrorView.tsx`・`mirror.css`・`skillPicker.ts`（+ `.test.ts`）・`agents/registry.ts`（`caps.slashSkills`） |
| i18n | `lib/i18n/locales/ja.ts` / `en.ts`（`mirror.skills_*`） |

## 4. 検証（2026-07-28）

- Go: workspace/agent 全 18 pkg ok・control-plane ok（走査 / handler / ルート登録の
  パリティ両側）。
- Console: `tsc --noEmit` ok・vitest 686 件 ok（skillPicker 7 件含む）・`i18n:lint` ok・
  `vite build` ok。
- 実機目視は未実施（次のフリート再ビルド後に確認する）。

## 5. 挙動の根拠メモ

- スラッシュ送信は既存経路がそのまま使える: tui は `typeLineAndSubmit`（docs/38 の
  `/scout` 定時発火で実証済み）、managed は prompt 素通し。サーバの `slashCmdRe` が
  turn 扱いを抑止するのも従来どおりで、本機能は**送信経路に一切手を入れていない**。
- managed 経路には tui の「スラッシュは working を付けない」ガードが無い
  （`handleManagedInputPrompt` は無条件 `markSessionWorking`）— 既存の非対称で、
  本機能では悪化も改善もしない。直すなら別タスク。

## 6. 積み残し

- 組み込みコマンド・プラグインスキルの列挙（§0）。
- cursor / kiro: ACP `available_commands_update` を読み捨てず、この API の形へ正規化
  して `caps.slashSkills` を立てる（UI は無改修で点く設計にしてある）。
- ChatView（アシスタント）への同型ピッカー — コンポーザーがほぼ双子なので移植は容易。
- 実機目視（スマホの「/」ボタン導線含む）。
