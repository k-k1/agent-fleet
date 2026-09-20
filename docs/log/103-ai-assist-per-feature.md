# 103. AI 補助を機能ごとに指定する（エージェント・モデル・オンオフ）

- 状態: **設計**（2026-09-20）。実装は未着手。**レビュー反映済み**——別セッションに批判的レビューを
  依頼し、指摘 14 件をすべて採って書き直した（[103-review.md](103-review.md)。何をどう変えたかは §103.12）。
- 関連: [84-ai-assist-settings.md](84-ai-assist-settings.md)（AI 補助を設定から切り出した回。
  本稿はその続き）/ [97-mirror-translate.md](97-mirror-translate.md)（8 つ目の補助機能と、
  「要求値を実行結果として画面に出した」事故）/ [46-usage-accounting.md](46-usage-accounting.md)
  （台帳の feature 軸＝本稿が設定の粒度に採用する語彙）/ [44-markdown-code-editor.md](44-markdown-code-editor.md)
  （ファイル編集の提案）/ [30-session-report.md](30-session-report.md)（自動ターン＝本稿の対象外の側）

---

## 103.1 要求

> 「AI補助」系機能を整理したい。それぞれの機能毎に使用するエージェント、モデルを
> 細かく指定できる様にしたい。

84 で「AI 補助生成」を独立タブへ切り出したが、**エージェントは全機能で 1 本の優先順位、
モデルは short / prose の 2 ティア**までしか割っていない。ティアは*実装が何を必要とするか*
であって*利用者が見る面*ではない——84 が自分で立てた分類原則（§84.2）の、最後に残った例外である。

## 103.2 いまの形（棚卸し）

`chatx.OneShotHeadless`（`chat_providers.go:1541`）を通る**単発ヘッドレス生成**が 8 つ。
どれも会話を継続せず、セッションのターンを使わない。台帳（`usagex/ledger.go:29-52`）は
**既に 8 つを別々の `feature` として記録している**。

| 台帳の feature | 面 | ティア | 今のオン・オフ | claude の既定（env） |
|---|---|---|---|---|
| `title.session` | セッション（自動バナー＋改名ダイアログ） | short | `autoTitleSuggest` | `AF_TITLE_MODEL`=haiku |
| `title.chat` | チャットの改名ダイアログ | short | `assistantTitleSuggest` | 同上 |
| `branch.suggest` | worktree 作成 / ブランチ改名 | short | `branchSuggestEnabled` | 同上 |
| `suggest.session` | ミラーの ✨ | short | `replySuggestEnabled`（**2 機能で共有**） | `AF_SUGGEST_MODEL`=haiku |
| `suggest.chat` | チャットの ✨ | short | `replySuggestEnabled`（**共有**） | 同上 |
| `suggest.edit` | File ペイン | prose | `editSuggestEnabled` | `AF_EDIT_SUGGEST_MODEL`=sonnet |
| `plan.update` | チャットの計画 | prose | **無い（常時 ON）** | `AF_PLAN_MODEL`=sonnet |
| `translate.mirror` | ミラーの翻訳 | prose | `mirrorTranslateEnabled`（＋自動翻訳） | `AF_TRANSLATE_MODEL`=sonnet |

**9 つ目は無い**（台帳の feature 定数を全数当たって確認——`usagex/ledger.go:29-52`）。

**境界は面ではなく実効で引く**: 本稿の対象は**優先順位とティアを通る機能**、すなわち
`OneShotHeadless` を通るものだけである。外に出るものは、出る理由がそれぞれ違う。

| | なぜ対象外か |
|---|---|
| `assistant.chat` / `compact` | 会話そのもの。会話のモデルで走る |
| `assistant.autoturn` | 既に専用の設定を持つ（`assistantAutoTurnModel`・claude 専用） |
| `assistant.ask` | **アシスタント定義が主語**で、そのアシスタントの agent / model で走る（`chat_handlers.go:246` の `ResolveChatModel(a.Agent, a.Model)`）。優先順位もティアも**そもそも通らない** |
| `assistant.bridge` | 同上（面は Discord / Slack、`bridge_operator.go:76`） |

⚠️ 当初ここを「面がチャットだから」と書いていたが、それは誤り（`assistant.ask` は MCP から
セッションが呼ぶツールで、会話は永続化されずチャット画面に出ない）。**84 の分類原則は
「設定の並べ方」の原則であって、「どのコードが対象か」の原則ではない。**

## 103.3 見つかった食い違い

84 が 5 つの食い違いを潰した同じ場所に、まだ 5 つ残っていた（5 件ともレビューで裏取り済み）。

1. **モデルの分類軸が実装ティアのまま。**「短文生成」「文章生成」は
   `OneShotShort` / `OneShotProse` という*実装の共有先*の名前で、利用者が見る面ではない。
   要求はここを畳む話そのもの。
2. **1 機能 1 キーの穴が 2 つ。** `replySuggestEnabled` は台帳が 2 機能に分けている
   `suggest.session` と `suggest.chat` を 1 キーで止める。`plan.update` にはトグルが無い
   （`HandleChatPlanRefresh`＝`chat_plan.go:425` にゲートが 1 行も無い）——84 がブランチ名・
   編集提案で埋めた欠落の、取りこぼし。
3. 🔴 **返信候補のサーバ側ゲートが初日から効いていない。** Console は `replySuggestEnabled`
   を保存し（`console/src/lib/settings.ts:539`／ui-prefs は device-local を除く全オブジェクトを
   そのまま PUT する）、Agent は `replySuggest` を読む（`session_suggest_reply.go:111`）。
   **キー名が違う**ので `ok=false` ＝ 常に true に倒れ、オフにしてもボタンが消えるだけで
   `POST /sessions/{name}/suggest-replies` とチャット側（`chat_suggest_reply.go:59`）は
   生成し続ける。`replySuggest` の**書き手はリポジトリ全体で 0 件**（レビューで再確認）。
   `43907e7f`（返信サジェスト v2）の**コミットメッセージ自身が両方の綴りを並べて書いており**、
   一度も一致したことがない。84 の 3 番目の原則（表示と実効を一致させる／サーバの拒否は防御として
   残す）が、この機能だけ成立していない。
4. **同じ機能の呼び名が画面ごとに違う。** 使用量ビューは「セッション件名の提案」
   「変更提案（エディタ）」（`locales/ja/usage.ts:108,113`）、AI補助タブは
   「セッションのタイトル提案」「ファイル編集の提案」（`locales/ja/aiassist.ts`）。
   機能ごとの設定を作ると、**同じ機能が 2 つの名前で 2 画面に並ぶ**。
5. **`migrateAiAssistPrefs` のコメントが実装と逆。**`settings.ts:924-929` の関数ドキュメントは
   「prose は継がず用途に合った推奨へ戻す」と書き、`:943-946` の行内コメントは逆を書き、
   実装 `:949-950` は行内コメントに従う。正しいのは 84 §84.3 の ★（一度そう決めて、
   **リリースの瞬間に利用者が触っていないモデルが変わる**ので覆した）。関数ドキュメントだけが
   覆す前の版のまま残っている。

## 103.4 決めたこと

| # | 決定 | 理由 |
|---|---|---|
| 1 | **機能ごとに「エージェント 1 つ＋そのモデル」**を指定できる。優先順位リストごとの上書きではない | 要求は「どの CLI のどのモデルで動かすか」。リストを機能ごとに持つと UI は 1 機能あたり並べ替え＋CLI 数ぶんのモデル行になり、得られるのは「フォールバックの順序」という滅多に使わない自由だけ |
| 2 | **未設定は「既定に従う」**。既定は現行の優先順位＋ 2 ティア | 84 §84.3 と同じ原則——アップグレードで挙動を変えない |
| 3 | **設定の粒度＝台帳の feature**（`title.session` 等の文字列をそのままキーにする） | 利用者は使用量ビューで `title.session` の額を見てから設定を開く。**見た行と直す行が同じ名前**になる |
| 4 | **モデルは kind スコープで保存する**（`機能 → CLI → モデル`）。**具体的なモデルを選べるのは、その機能にエージェントを明示ピンしたときだけ** | 他のモデル設定（`hiddenModels` / `assistantModels` / `aiShortModels`）と同形。「自動」の kind は実行時まで決まらない（`preferredFrom`）ので、書き込み先が無い＝選ばせてはいけない。この不変条件が無いと、素直な実装は「自動なら 5 CLI ぶんのモデル行」に落ちる＝決定 1 が退けた形に戻る |
| 5 | **「いま使うのは X / Y」の X（kind）は Agent が答える。Y（モデル名）は Console が持っているカタログで描く** | kind の解決はログイン判定だけで済み、しかも**いちばん嘘になりやすいのがここ**（97 で踏んだのは「モデル名は合っていたが kind が違った」側）。モデル名の解決は CLI のカタログ列挙＝最大 15 秒で、同期で払う値段ではない（§103.8-3） |
| 6 | 穴 4 つ（§103.3 の 2・3・4）を同じ回で塞ぐ | 「1 機能 1 行」の画面を作る以上、1 キーが 2 機能を止め、1 機能にキーが無く、同じ機能が 2 つの名前で出る状態は、その画面の上で目に見える |
| 7 | 自前エンジン（lcpp）は**今回の指定先に入れない** | `OneShotHeadless` に lcpp 分岐が無く、いま指定すると claude 経路に落ちる。ADR 0093 phase 1 は「明示ピンでのみ到達可」なので機能別ピンとは相性が良い——が、それは 1 本の実装であって整理ではない。別建て |
| 8 | **翻訳のキャッシュ鍵に、解決済みの kind とモデルを足す**（旧鍵は読み続ける） | 鍵は原文ハッシュ＋言語だけ（97 §97.2）。機能ごとにモデルを変えられるようにした瞬間、「前のモデルの訳」が出続ける。**何で訳したかは訳文の同一性の一部**である。詳細と後方互換は §103.8-2 |
| 9 | **feature は明示引数で渡す。台帳のタグ（ctx）を設定解決の入力にしない** | ctx のタグは**観測軸**である（`usagex/call.go:8`——「呼び出し場所を変えても、何として記録されるかは変わってはならない」）。それを読んで**何が走るか**を決めると、以後「記録用のラベルを動かすと走る CLI が変わる」になる。しかも台帳の都合でタグを付け替えるのは正当な操作で、現に `chat_compact.go:123` がターン途中で上書きしている。明示引数なら触る行は 8 行で、**コンパイラが守る** |

## 103.5 解決順とデータモデル

```
① 機能ごとの指定（新）   エージェント: 自動 | claude | codex | opencode | cursor | agy
                         モデル:       既定 | 推奨 | <ピンした CLI のカタログ>
② 既定（現行 2 ティア）   aiAssistOrder ＋ aiShortModels[kind] / aiProseModels[kind]
③ AF_*_MODEL（配備）／推奨／CLI 既定
```

①が②を飛ばし、②が③を飛ばす。**いまの①↔②の関係（ティア pref が呼び出し元の既定を上書きする）
をもう一段上に伸ばしただけ**で、既存の分岐の形は変えない。

設定キー（ui-prefs / Console settings）:

```ts
aiFeatureAgents: Record<FeatureId, string>;                  // "" = 自動（優先順位に従う）
aiFeatureModels: Record<FeatureId, Record<string, string>>;  // 機能 → CLI → モデル
```

**不変条件**（決定 4）: `aiFeatureModels[feature][kind]` を書くには kind が要る。よって
**「自動」のカードで選べるのは「既定」か「推奨」だけ**。ピンを別の CLI に変えても前の CLI の
モデル指定は**消さない**（戻したときに復活する。kind スコープで持つ意味がそこにある）。

### 呼び出し元と feature の渡し方

`OneShotHeadless` の**テストでない呼び出しは 8 箇所**、そこへ `usagex.WithTag` でタグを載せる行は
**9 行**（`session_title.go` の自動バナーと手動ボタンが同じ関数へ合流するため）。数が違うのは
そのためで、当初の文面が並べていた行番号は**`WithTag` の行**だった。

タグ付き ctx は 8 経路すべてで `OneShotHeadless` まで素通りする（差し替え・`Background` の
作り直し・goroutine 跨ぎは 0 件。レビューで全経路を追った）。**つまり ctx から feature を読むことは
技術的には可能**である——それでも読まない。決定 9 のとおり、観測軸を挙動軸に流用しないため。

```go
// OneShotHeadless(ctx, feature, tier, persona, prompt, claudeModel)
kind, model, _ := resolveOneShot(feature, tier)
```

- **引数は位置引数。構造体にはしない。**`OneShotSpec{…}` は読みやすいが、
  **Go の複合リテラルはフィールドを省いてもコンパイルが通る**——`deps.go:6-14` がまさにその罠を
  書いており、あの 40 フィールドは反射チェックで補っている。ここは引数 6 個なので、
  コンパイラに数えさせるほうが安い。
- **ピンを先に見る。**`PreferredAssistAgent()` は `headlessAgentAvailable` を順に舐めるので、
  ピンが生きているなら走査そのものが無駄（`preferredFrom`＝`chat_providers.go:126-133`）。
- ピン先が**未接続なら優先順位へ落ちる**。そのとき①のモデルは*落ちた先の kind のもの*だけを読む
  （決定 4）。指定した CLI のモデル ID が別 CLI へ渡ることは構造上起きない。
- **解決は `resolveOneShot(feature, tier) (kind, model, source)` に切り出す。**
  `OneShotHeadless` と `/ai-assist/resolution` と翻訳の鍵が**同じ関数を共有する**（同じ答えが
  3 箇所に出る唯一の作り方）。`source` は「①②③のどれが勝ったか」で、画面の説明と、
  未解決（カタログ未取得）の表明に使う。
- **署名を増やすのは 1 本だけ。**翻訳は「実際に走った kind / model」を要る（決定 8）ので、
  それを返す `OneShotHeadlessRun` を足し、`OneShotHeadless` はその薄いラッパにする。
  97 が消したのは*クライアントに返す* `model` であって、呼び出し元に真実を渡すことではない。
- **`chatx.Deps` の seam は 3 本。**`AiFeatureAgentPref` / `AiFeatureModelPref` に加え、
  **`ChatReplySuggestEnabled`** が要る——いま `Deps.ReplySuggestEnabled`（`deps.go:83`）の 1 本が
  ミラーとチャットの両方を兼ねており、`chatx` から `uiprefs` を直接呼ぶ道は無い（逆依存は
  `Deps` 1 本に閉じる、`deps.go:3-17`）。`Configure` の反射チェックが未配線を panic で弾くので
  配線漏れは黙って通らないが、`deps_test.go` と `sessionx/deps_stub_test.go` の両方が動く。

## 103.6 配線

| 層 | 何を足すか |
|---|---|
| Agent | `chat_providers.go`: `resolveOneShot()` の新設と `OneShotHeadless` の feature 引数化（呼び出し 8 箇所）。`OneShotTier` は**残す**——③の推奨と既定モデルを決めるのは今後もティア |
| Agent | `ui_prefs.go`: `aiFeatureAgentPref` / `aiFeatureModelPref`（`assistantModelPref` の hidden-models 除外を通す）。`chat_wiring.go` に 3 行（seam 3 本） |
| Agent | `uiprefs/prefs.go`: `PlanUpdate()` / `ChatReplySuggest()` 新設、`ReplySuggestEnabled()` を `replySuggestEnabled` へ。**旧 `replySuggest` のフォールバックは書かない**——書き手が一度も存在しないので、残すと「昔このキーで保存していた時期がある」という嘘の証言になる。代わりに doc コメントに 1 文だけ根拠を残す |
| Agent | **止める場所を名指しで足す**: `chat_plan.go:425`（`HandleChatPlanRefresh`）の先頭に `PlanUpdate()` ゲート（`fs_suggest_edit.go:224` / `chat_title.go:92` と同じ形）、`chat_suggest_reply.go:59` を新しいチャット専用ゲートへ。**画面で消して server で止めないのは §103.3-3 と同じ失敗の形** |
| Agent | `GET /ai-assist/resolution` — 8 機能ぶんの `{feature, enabled, kind, model, source}`。**外部プロセスを絶対に起動しない契約**（§103.8-3）。キャッシュに無いものは `source:"unknown"` |
| Agent | `session_translate.go`: キャッシュ鍵に解決済みの kind とモデルを足し、**model 空文字＝旧鍵として読み続ける**（決定 8・§103.9）。鍵の実装は Go / TS の 2 本なので `console/src/features/mirror/translate.ts` と対で動かす |
| CP | `routes.go` の所有者側 `rest` に 1 行（`GET /api/ai-assist/resolution`）。共有側には置かない——押すと所有者のワークスペースでモデルが走る（`routes.go:410-413` の既存の前例どおり） |
| Console | `lib/aiAssistFeatures.ts`（新）＝ 8 機能のカタログ（id・面・ティア・トグルのキー・ラベルのキー）。**タブと使用量ビューが同じラベルを引く**（§103.3-4） |
| Console | `AiAssistTab` を 2 段構成へ（§103.7）。`settings.ts` に 2 キー＋トグル 2 キー、`migrateAiAssistPrefs` の関数ドキュメント訂正 |
| Console | **`aiModelRow.tsx:22-37` の `recommendedModelId` を消す**——Agent ロジックの写しで、hidden models の扱いが既にずれている（§103.8-7）。推奨の解決は Agent が答え、Console はラベルを描くだけにする |
| Console | ゲートを見る側: ミラー ✨ / チャット ✨ を別キーに、計画更新のボタンを `planUpdateEnabled` で描かない |
| guide | `guide/member/12-settings.ja.md` / `guide/ref/settings.ja.md` / `guide/ref/features.ja.md` を **ja / en 両方**（CONVENTIONS §5）。利用者から見える変更は features に行が在るまで終わっていない（CONVENTIONS §8）。翻訳キャッシュの約束（`aiassist.note_mirror_translate` の「同じ本文なら二度目以降は無料です」）も、モデルを変えたら作り直しになる旨へ直す |

## 103.7 画面

設定 > 個人設定 > AI補助 を 2 段にする。

- **§1 既定（全機能共通）** — 優先順位・短文/文章のモデル。注記に「機能ごとに上書きできます」。
- **§2 機能ごと** — 8 枚のカード。既定では「既定に従う」なので、平常時に見えるのは 1 機能 1 行。

```
▼ セッションのタイトル提案                        [ON]
   エージェント  [ claude            ▾]
   モデル        [ 推奨（現在: Haiku）▾]
   いま使うのは: claude / claude-haiku-4-5

▼ 回答の翻訳（ミラー）                            [ON]
   エージェント  [ 自動（優先順位）    ▾]
   モデル        [ 既定（文章生成）    ▾]     ← 自動では具体モデルを選べない（決定 4）
   いま使うのは: agy / …
```

- 「いま使うのは」の kind は**Agent の答え**（決定 5）。未取得のあいだは行ごと出さない
  ——84 の `pollyAvailable() === null` と同じ、「未取得で決めつけない」経路。
- オフにした機能はカードを畳み、エージェント / モデルの行を出さない。オフの機能の
  モデルを選べる画面は、84 の 3 番目の原則に反する。
- **ON/OFF の表示は「フォールバック後の実効値」を出す。** 84 より前の prefs を持つ利用者では
  `branchSuggestEnabled` / `assistantTitleSuggest` が `autoTitleSuggest` を継ぐ
  （`uiprefs/prefs.go:129-134,302-307`）ので、サーバの実効は OFF なのに Console の `DEFAULTS` は
  true ＝**カードだけ ON に見える**。表示と実効を一致させる（これも 84 の 3 番目の原則の系）。

## 103.8 踏みどころ

1. **opencode はカタログ＝権利ではない**（`chat_providers.go:1583-1588` の実測——`claude-haiku-4-5`
   が一覧に出て、実行すると "Unexpected server error"）。機能ごとに明示指定できると、その罠を
   踏む経路が増える。codex の「自分で選んだモデルを外して 1 回だけ再試行」
   （`CodexOneShotWithRetry`）は、**今日すでに利用者の明示指定には効かない**
   （`chat_providers.go:1566-1567`——`configured && !autoRecommended` なら `autoPicked=false`）。
   これは**壊さないこと**が仕事で、そのためには**機能別ピンが `configured=true` を立てる**のが条件。
   ここを `false` のまま通すと、リトライが復活するうえに `AF_TITLE_MODEL_CODEX` が勝ってしまう。
2. **翻訳のキャッシュ鍵にモデルが入っていない**（97 §97.2——鍵は原文ハッシュ＋言語）。機能ごとに
   モデルを変えられるようにすると「前のモデルの訳」が出続ける。**鍵に解決済みの kind とモデルを
   足す**（決定 8）。**何で訳したかは訳文の同一性の一部**である。ただし保存の実体
   `sessionTranslation{Hash, Lang, Text, CreatedAt}`（`session_translate.go:161-166`）には model 欄が
   無いので、素直に足すと**既存の訳が全件ミス**になる——それは移行である（§103.9）。
   ⚠️ 鍵は Go と TS の 2 実装で固定されている（`session_translate_test.go` / `translate.test.ts` の
   ベクタ）ので、**両方を同時に動かす**こと。
   ⚠️ 鍵に載せるのは*予測*ではなく**実際に走った値**（`OneShotHeadlessRun` の戻り値）。
   可用性キャッシュは 1 分で切れるので、予測で保存すると「走ったのと違う鍵」が残る。
3. **`/ai-assist/resolution` の高い部分はログイン判定ではなく、モデルカタログの列挙である。**
   `headlessAgentAvailable` は 1 分キャッシュだが、未設定機能の「推奨」を具体名へ解決するには
   `codex.Models()`（**15 秒**タイムアウト・`models.go:35`）/ `opencode.Models()`（**10 秒**・`:52`）/
   `agy.Models()`（**15 秒**・`:54`）に落ちる。8 機能が 5 CLI にばらけていれば 5 本ぶん全部払う
   ——**1 リクエストにまとめても CLI の数は減らない**。よって:
   - エンドポイントは**外部プロセスを絶対に起動しない**契約にする。キャッシュにあるものだけ答え、
     無ければ `source:"unknown"`。
   - 返すのは **kind まで**。モデル名は Console が `useModelOptions(kind)`
     （`lib/agentModels.ts:169`）で**どのみち引いている**一覧から描く（追加費用 0）。
   - それでも Y を Agent に答えさせたくなったら、**非同期で暖めて次のポーリングで出す**。
     同期で最大 40 秒待たせる形だけは採らない。
4. **hidden models（`model_deny.go`）を必ず通す。** 除外されたモデルは「未設定」に落として
   推奨へ戻る——`assistantModelPref` の既存経路をそのまま使う。
5. **ui-prefs は 64KiB・全オブジェクト PUT・`ShrunkKeys` のバックアップ経路に乗る。**
   新キーは小さいが、`aiFeatureModels` は入れ子なので「空の入れ子を書かない」こと。
6. **全 CLI 未接続のとき、ピンした CLI ではなく `order[0]` のエラーが返る**
   （`preferredFrom` が意図としてそう書いている）。「codex にピンしたのに claude のエラーが出る」は
   素直に混乱するので、機能別ピンの画面ではエラー文言にピン先を含める。
7. **§1 の「推奨（現在: X）」は今も Console の自前計算で、hidden models で既に Agent とずれている。**
   Agent は `visibleModel(kind,"haiku")` が空なら CLI 既定へ落ちる（`chat_providers.go:1427-1442`）が、
   Console（`aiModelRow.tsx:57`）は `live` から外れたときフォールバックで `"haiku"` をそのまま
   ラベルにする。**haiku を「使わないモデル」に入れた人の画面には「推奨（現在: haiku）」と出て、
   実際には claude の既定が走る。** §2 に Agent の答えを置くと同じタブに 2 つの真実が並ぶので、
   `recommendedModelId` は消して Agent に寄せる（§103.6）。
8. **返信候補のキー名を直すと、オフにしていた利用者の挙動が初めて変わる**（ボタンが消えるだけ
   だったのが、本当に生成が止まる）。これは意図した修正だが、リリースノートに 1 行要る。

## 103.9 移行

**2 件だけある。**①のキーが無ければ②へ、②が無ければ③へ落ちる——ここは今日の経路そのもので、
移行コードは要らない（84 §84.3 の「足りないキーを埋めるだけ」を「そもそも埋めない」まで進めた形）。
だが**踏みどころ 2 と 8 は、利用者が設定を 1 つも触っていないのに挙動が変わる**。

| # | 何が変わるか | どうするか |
|---|---|---|
| 1 | 翻訳キャッシュの鍵に model が入る＝**既存の訳が全件ミス**。「同じ本文なら二度目以降は無料」という約束（`aiassist.note_mirror_translate`）が破れ、`mirrorAutoTranslate` が ON の人は**押さずに**払う。`trimTranslations` は件数とバイトで古い順に捨てるので、新旧 2 系統が同居すると実質の保持件数も減る | **model が空文字の鍵＝旧鍵として読み続ける**（書くときだけ新鍵）。既存の訳は生き、モデルを変えた人だけが作り直しになる |
| 2 | 返信候補のゲートが初めて効く＝オフにしていた人の ✨ が本当に止まる | 意図した修正。リリースノートと guide に 1 行 |

新設するトグル 2 つの既定と継ぎ元:

| 新キー | 既定 | 継ぐ元 |
|---|---|---|
| `assistantReplySuggestEnabled`（チャットの ✨） | true | `replySuggestEnabled`（明示 OFF は両方へ） |
| `planUpdateEnabled`（計画の更新） | true | （無し＝常時 ON だった＝欠落は「従来の挙動」） |

## 103.10 検証計画

- **Go**: 機能ごとのピンが優先順位に勝つ／ピン先が未接続なら順位へ落ち、**そのとき①のモデルを
  使わない**／`feature` 空（将来の呼び出し漏れ）で従来経路のまま／hidden model が推奨へ落ちる／
  **新トグル 2 つとも、オフで 400 が返ることを直接叩く**（片方だけ測る計画は、片方だけ直った状態を
  緑にする）／翻訳の旧鍵が読めて新鍵で書かれる。**陽性対照**: 解決関数を「常にピンを無視」に
  潰すと落ちる本数を先に数える（変異の戻しは Edit で）。
- **Console**: カタログの 8 機能がタブと使用量ビューで同じラベルを引く／「自動」で具体モデルが
  選べない／オフの機能にエージェント・モデル行が出ない／ON/OFF がフォールバック後の実効値を出す／
  「いま使うのは」が未取得のとき出ない。
- **実機**: 1 機能だけ codex にピンして ✨ を押し、**台帳の行が `feature=suggest.session` /
  `kind=codex`** になることを見る。画面の表示ではなく台帳で確かめる——97 で嘘を暴いたのが
  台帳だったのと同じ理由。
- **文書**: `python3 scripts/docs-check.py`（guide の ja / en 対を含む）。

## 103.11 やらないこと

- **アシスタントの自動ターン・要約引き継ぎ・`ask_assistant`** は対象外（§103.2 の境界）。
- **自前エンジン（lcpp）を指定先に加える**のは別建て（決定 7）。
- **自動発火の軸**（`title.session` の自動バナー、`translate.mirror` の自動翻訳）は今回いじらない。
  「押したら動く／勝手に動く」は機能ごとのエージェント・モデルとは別の軸で、混ぜると
  1 枚のカードが 2 つの質問に答えることになる。
- **feature → tier の表への畳み込み**（feature が明示引数になると tier は導出できる）は、
  今回はやらない。呼び出し元の既定モデル（`TitleModel()` ほか）が `Deps` 越しに来ている構造ごと
  動かす話になるため。

## 103.12 レビューで変えたこと

別セッション（claude / opus）に批判的レビューを依頼した（[103-review.md](103-review.md)）。
**事実の申告 5 件は 5 件とも裏取りで正しかった**が、**設計の判断は 14 件の指摘を受けて直した**。
そのうち 3 件は「この文書が自分で立てた原則」に反していたもので、採らない理由が無かった。

| 指摘 | 変更 |
|---|---|
| 重大 1: 台帳のタグ（観測軸）を設定解決の入力にしている | **決定 9 を追加し、feature を明示引数に**。「呼び出し元を 1 行も触らない」で節約できるのは 8 行、手放すのはコンパイラの強制——割に合わない |
| 重大 2: 新トグル 2 つの**サーバ側で止める場所**が無い | §103.6 に enforcement point を名指しで追加（`chat_plan.go:425` ほか）。§103.10 も両方測る |
| 重大 3: seam は 2 本では足りない | 3 本（`ChatReplySuggestEnabled`）に訂正 |
| 重大 4: `/ai-assist/resolution` の費用見積りが 1 桁小さい | 高いのはカタログ列挙（15/10/15 秒）。**外部プロセスを起動しない契約**に変更し、モデル名は Console 側のカタログで描く（決定 5 を書き直し） |
| 重大 5: 「自動」のときモデルの保存先が未定義 | 決定 4 に**不変条件**を明記（自動では具体モデルを選べない／ピンを変えても前の値は残す） |
| 中 6: 「移行は無い」は成立しない | §103.9 を「2 件だけある」に。翻訳の旧鍵フォールバックを採用 |
| 中 7: `assistant.ask` を外す**理由**が事実と違う | §103.2 の境界を「面」から「**優先順位とティアを通るか**」へ書き換え |
| 中 8: 「呼び出し元 7 箇所」は誤り | 呼び出し 8・`WithTag` 9 行と数え方を明記。9 つ目の機能が無いことも根拠つきで |
| 中 9: 「推奨（現在: X）」が Console の自前計算で hidden models でずれる | §103.8-7 に記録し、`recommendedModelId` を消す行を配線表へ |
| 中 10: `guide/` が配線表に無い | guide 行を追加（ja / en 両方・CONVENTIONS §8） |
| 軽 11: 旧 `replySuggest` のフォールバックは死にコード | 書かない。doc コメントに 1 文だけ根拠を残す |
| 軽 12: ピンの判定順が逆 | ピンを先に見る |
| 軽 13: codex のリトライ抑止は既にそうなっている | 「これから決める」から「**壊すな**」へ。`configured=true` を立てる条件を明記 |
| 軽 14: 84 以前の prefs では 1 キーが 3 機能に効く | §103.7 に「ON/OFF はフォールバック後の実効値を出す」 |

レビューが「問題ではなかった」として記録した 4 点（共有セッション・MCP 経路・台帳との整合・
`Deps` の反射チェック）は [103-review.md](103-review.md) §2 にある。**調べて何も出なかったことも
記録が要る**——次に同じ心配をした人が、同じ調査を繰り返さずに済む。
