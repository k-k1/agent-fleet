# 103. AI 補助を機能ごとに指定する（エージェント・モデル・オンオフ）

- 状態: **設計**（2026-09-20）。実装は未着手。
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

**境界**: 会話の中で走るもの——`assistant.chat` / `assistant.autoturn`（`assistantAutoTurnModel`
を既に持つ・claude 専用）/ `compact` / `assistant.ask` / `assistant.bridge`——は面がチャットなので
**アシスタントタブ側で正しい**。本稿は触らない。84 §84.2 の分類原則をそのまま適用した結果である。

## 103.3 見つかった食い違い

84 が 5 つの食い違いを潰した同じ場所に、まだ 5 つ残っていた。

1. **モデルの分類軸が実装ティアのまま。**「短文生成」「文章生成」は
   `OneShotShort` / `OneShotProse` という*実装の共有先*の名前で、利用者が見る面ではない。
   要求はここを畳む話そのもの。
2. **1 機能 1 キーの穴が 2 つ。** `replySuggestEnabled` は台帳が 2 機能に分けている
   `suggest.session` と `suggest.chat` を 1 キーで止める。`plan.update` にはトグルが無い
   ——84 がブランチ名・編集提案で埋めた欠落の、取りこぼし。
3. 🔴 **返信候補のサーバ側ゲートが初日から効いていない。** Console は `replySuggestEnabled`
   を保存し（`console/src/lib/settings.ts:539`／ui-prefs は device-local を除く全オブジェクトを
   そのまま PUT する）、Agent は `replySuggest` を読む（`session_suggest_reply.go:111`）。
   **キー名が違う**ので `ok=false` ＝ 常に true に倒れ、オフにしてもボタンが消えるだけで
   `POST /sessions/{name}/suggest-replies` とチャット側（`chat_suggest_reply.go:59`）は
   生成し続ける。`git log -S` で確認したところ**両方が同じコミット `43907e7f`（返信サジェスト v2）
   で入っており、一度も一致したことがない**。84 の 3 番目の原則（表示と実効を一致させる／
   サーバの拒否は防御として残す）が、この機能だけ成立していない。
4. **同じ機能の呼び名が画面ごとに違う。** 使用量ビューは「セッション件名の提案」
   「変更提案（エディタ）」（`locales/ja/usage.ts:108,113`）、AI補助タブは
   「セッションのタイトル提案」「ファイル編集の提案」（`locales/ja/aiassist.ts`）。
   機能ごとの設定を作ると、**同じ機能が 2 つの名前で 2 画面に並ぶ**。
5. **`migrateAiAssistPrefs` のコメントが実装と逆。**`settings.ts:924-929` は「prose は継がず
   用途に合った推奨へ戻す」と書くが、:949-950 は両方継いでいる。正しいのは 84 §84.3 の ★
   （一度そう決めて、**リリースの瞬間に利用者が触っていないモデルが変わる**ので覆した）。
   コメントだけが覆す前の版のまま残っている。

## 103.4 決めたこと

| # | 決定 | 理由 |
|---|---|---|
| 1 | **機能ごとに「エージェント 1 つ＋そのモデル」**を指定できる。優先順位リストごとの上書きではない | 要求は「どの CLI のどのモデルで動かすか」。リストを機能ごとに持つと UI は 1 機能あたり並べ替え＋CLI 数ぶんのモデル行になり、得られるのは「フォールバックの順序」という滅多に使わない自由だけ |
| 2 | **未設定は「既定に従う」**。既定は現行の優先順位＋ 2 ティア | 84 §84.3 と同じ原則——アップグレードで挙動を変えない。移行コードは 1 行も要らない |
| 3 | **設定の粒度＝台帳の feature**（`title.session` 等の文字列をそのままキーにする） | 利用者は使用量ビューで `title.session` の額を見てから設定を開く。**見た行と直す行が同じ名前**になる。実装側も、呼び出し元が既にその文字列を ctx に載せている（§103.5） |
| 4 | **モデルは kind スコープで保存する**（`機能 → CLI → モデル`） | この木の他のモデル設定（`hiddenModels` / `assistantModels` / `aiShortModels`）と同形。ピン先が未接続で別 CLI に落ちたとき、**他 CLI のモデル ID を渡さない**ために必須 |
| 5 | **「いまこの機能が使うのは X / Y」は Agent が答える**。画面側で計算しない | 97 の実機で踏んだ通り（応答は `model: sonnet`、台帳は `kind=agy`）。Console の `conns` と Agent の `headlessAgentAvailable`（CLI ログイン実測）は別物で、画面側の再計算は必ずいつか嘘になる |
| 6 | 穴 4 つ（§103.3 の 2・3・4）を同じ回で塞ぐ | 「1 機能 1 行」の画面を作る以上、1 キーが 2 機能を止め、1 機能にキーが無く、同じ機能が 2 つの名前で出る状態は、その画面の上で目に見える |
| 7 | 自前エンジン（lcpp）は**今回の指定先に入れない** | `OneShotHeadless` に lcpp 分岐が無く、いま指定すると claude 経路に落ちる。ADR 0093 phase 1 は「明示ピンでのみ到達可」なので機能別ピンとは相性が良い——が、それは 1 本の実装であって整理ではない。別建て |
| 8 | **翻訳のキャッシュ鍵に、解決済みの kind とモデルを足す** | 鍵は原文ハッシュ＋言語だけ（97 §97.2）。機能ごとにモデルを変えられるようにした瞬間、「前のモデルの訳」が出続ける。**何で訳したかは訳文の同一性の一部**である。詳細と注意は §103.8-2 |

## 103.5 解決順とデータモデル

```
① 機能ごとの指定（新）   エージェント: 自動 | claude | codex | opencode | cursor | agy
                         モデル:       既定 | 推奨 | <そのCLIのカタログ>
② 既定（現行 2 ティア）   aiAssistOrder ＋ aiShortModels[kind] / aiProseModels[kind]
③ AF_*_MODEL（配備）／推奨／CLI 既定
```

①が②を飛ばし、②が③を飛ばす。**いまの①↔②の関係（ティア pref が呼び出し元の既定を上書きする）
をもう一段上に伸ばしただけ**で、既存の分岐の形は変えない。

設定キー（ui-prefs / Console settings、いずれも**移行なし**）:

```ts
aiFeatureAgents: Record<FeatureId, string>;            // "" = 自動（優先順位に従う）
aiFeatureModels: Record<FeatureId, Record<string, string>>;  // 機能 → CLI → モデル
```

**Agent 側の要点——呼び出し元は 1 行も触らない。**
`OneShotHeadless` の呼び出し元 7 箇所は、**呼ぶ直前に必ず `usagex.WithTag(ctx, Tag{Feature: …})`
を通している**（`session_title.go:122,590,789` / `session_suggest_reply.go:324` /
`chat_plan.go:353` / `fs_suggest_edit.go:239` / `chat_title.go:107` /
`chat_suggest_reply.go:74` / `session_translate.go:461`）。台帳のためにそうなっているだけだが、
結果として**「この呼び出しは何の機能か」は既に ctx に載っている**。
`OneShotHeadless` の中で `usagex.TagOrUnknown(ctx).Feature` を読めば、機能別解決は
**この関数 1 つの改修で入る**。

```go
// chat_providers.go:1550 付近
feature := usagex.TagOrUnknown(ctx).Feature
kind := PreferredAssistAgent()
if pin, ok := aiFeatureAgentPref(feature); ok && pin != "" && headlessAgentAvailable(pin) {
    kind = pin
}
selected, configured := aiFeatureModelPref(feature, kind) // ← 無ければ従来の oneShotModelPref(kind, tier)
```

- ピン先が**未接続なら優先順位へ落ちる**。そのとき①のモデルは*落ちた先の kind のもの*だけを読む
  （決定 4）。指定した CLI のモデル ID が別 CLI へ渡ることは構造上起きない。
- **タグ無し（`feature=unknown`）は今日と同じ**。将来タグを忘れた呼び出しが増えても、
  設定が効かないだけで壊れない。
- `chatx.Deps` に seam を 2 本足す（`AiFeatureAgentPref` / `AiFeatureModelPref`）。
  `Configure` の反射チェックが未配線を弾くので、足した時点で `deps_test.go` の網に入る。
- **署名は変えない。**翻訳だけは「実際に走った kind / model」を要る（決定 8・§103.8-2）ので、
  それを返す `OneShotHeadlessRun` を足し、`OneShotHeadless` はその薄いラッパにする。
  **要るようになった 1 箇所だけが新しい方を呼ぶ**——残り 7 箇所は本当に 1 行も動かない。

## 103.6 配線

| 層 | 何を足すか |
|---|---|
| Agent | `chat_providers.go`: `OneShotHeadless` の kind / model 解決（上記）。`OneShotTier` は**残す**——③の推奨と既定モデルを決めるのは今後もティア |
| Agent | `ui_prefs.go`: `aiFeatureAgentPref` / `aiFeatureModelPref`（`assistantModelPref` の hidden-models 除外を通す）。`chat_wiring.go` に 2 行 |
| Agent | `uiprefs/prefs.go`: `PlanUpdate()` 新設、`ChatReplySuggest()` 新設、`ReplySuggestEnabled()` を `replySuggestEnabled` へ（旧 `replySuggest` は読み側のフォールバックに残す）。§103.3-3 |
| Agent | `GET /ai-assist/resolution` — 8 機能ぶんの `{feature, enabled, kind, model, source}`。**解決は本番と同じ関数を通す**（決定 5） |
| Agent | `session_translate.go`: キャッシュ鍵に解決済みの kind とモデルを足す（決定 8）。鍵の実装は Go / TS の 2 本なので `console/src/features/mirror/translate.ts` と対で動かす |
| CP | `routes.go` の所有者側 `rest` に 1 行（`GET /api/ai-assist/resolution`）。共有側には置かない——他人のワークスペースの設定である |
| Console | `lib/aiAssistFeatures.ts`（新）＝ 8 機能のカタログ（id・面・ティア・トグルのキー・ラベルのキー）。**タブと使用量ビューが同じラベルを引く**（§103.3-4） |
| Console | `AiAssistTab` を 2 段構成へ（§103.7）。`settings.ts` に 2 キー＋トグル 2 キー、`migrateAiAssistPrefs` のコメント訂正 |
| Console | ゲートを見る側: ミラー ✨ / チャット ✨ を別キーに、計画更新のボタンを `planUpdateEnabled` で描かない |

## 103.7 画面

設定 > 個人設定 > AI補助 を 2 段にする。

- **§1 既定（全機能共通）** — 現行のまま（優先順位・短文/文章のモデル）。
  注記に「機能ごとに上書きできます」を足す。
- **§2 機能ごと** — 8 枚のカード。既定では「既定に従う」なので、平常時に見えるのは
  1 機能 1 行。

```
▼ セッションのタイトル提案                        [ON]
   エージェント  [ claude            ▾]
   モデル        [ 推奨（現在: Haiku）▾]
   いま使うのは: claude / claude-haiku-4-5

▼ 回答の翻訳（ミラー）                            [ON]
   エージェント  [ 自動（優先順位）    ▾]
   モデル        [ 既定（文章生成）    ▾]
   いま使うのは: agy / Gemini 3.5 Flash
```

- 「いま使うのは」は**Agent の答え**（決定 5）。未取得のあいだは行ごと出さない
  ——84 の `pollyAvailable() === null` と同じ、「未取得で決めつけない」経路。
- オフにした機能はカードを畳み、エージェント / モデルの行を出さない。オフの機能の
  モデルを選べる画面は、84 の 3 番目の原則に反する。

## 103.8 踏みどころ

1. **opencode はカタログ＝権利ではない**（`chat_providers.go:1583-1588` の実測——`claude-haiku-4-5`
   が一覧に出て、実行すると "Unexpected server error"）。機能ごとに明示指定できると、その罠を
   踏む経路が増える。codex にある「自分で選んだモデルを外して 1 回だけ再試行」
   （`CodexOneShotWithRetry`）は**利用者の明示指定には効かせない**——明示した選択を黙って
   別のモデルに差し替えるのは、84 の 1 番目の食い違いと同じ種類の裏切りである。注記で明示する。
2. **翻訳のキャッシュ鍵にモデルが入っていない**（97 §97.2——鍵は原文ハッシュ＋言語）。機能ごとに
   モデルを変えられるようにすると「前のモデルの訳」が出続ける。**鍵に解決済みの kind とモデルを
   足す**（決定 8）。設定変更でキャッシュを落とす案は採らない——捨てると、モデルを戻した人の
   訳まで消える。鍵を増やせば既存の訳はその鍵のまま残り、次に押したときだけ作り直される。
   97 の判断（鍵は「押した本文」＋言語という*安定しているもの*だけで作る）をそのまま延長した形で、
   **何で訳したかは訳文の同一性の一部**である。⚠️ 鍵は Go と TS の 2 実装で固定されている
   （`session_translate_test.go` / `translate.test.ts` のベクタ）ので、**両方を同時に動かす**こと。

   ここで 97 の罠に正面から当たる——**`OneShotHeadless` は実際に走った backend / model を返さない**
   （内側で台帳に書くだけ。だから 97 は応答から `model` を消した）。鍵に載せるのが*予測*では、
   可用性キャッシュが 1 分で切れた瞬間に「走ったのと違う鍵」で保存される。よって:

   - 解決を `resolveOneShot(feature, tier) (kind, model, source)` として**関数に切り出す**。
     これを `OneShotHeadless`・`/ai-assist/resolution`・翻訳の 3 つが共有する（同じ答えが
     3 箇所に出る唯一の作り方）。
   - **`OneShotHeadless` は実際に走った kind / model を返すようにする**。翻訳の鍵はその戻り値で
     作る（＝保存する直前に確定する）。台帳に書くのと同じ値なので、**画面と台帳が食い違わない**。
     97 が消したのは*クライアントに返す* `model` であって、呼び出し元に真実を渡すことではない。
3. **`/ai-assist/resolution` は冷えていると CLI を最大 5 本叩く**（`headlessAgentAvailable` の
   キャッシュは 1 分）。タブを開いた瞬間に 5 プロセスなので、**1 リクエストで 8 機能ぶんを返す**
   （機能ごとに叩かせない）。タイムアウトしたら行を出さない（踏みどころ上の「未取得」）。
4. **hidden models（`model_deny.go`）を必ず通す。** 除外されたモデルは「未設定」に落として
   推奨へ戻る——`assistantModelPref` の既存経路をそのまま使う。
5. **ui-prefs は 64KiB・全オブジェクト PUT・`ShrunkKeys` のバックアップ経路に乗る。**
   新キーは小さいが、`aiFeatureModels` は入れ子なので「空の入れ子を書かない」こと
   （空オブジェクトが 8 機能ぶん積まれると、差分が読めなくなる）。
6. **返信候補のキー名を直すと、オフにしていた利用者の挙動が初めて変わる**（ボタンが消えるだけ
   だったのが、本当に生成が止まる）。これは意図した修正だが、リリースノートに 1 行要る。

## 103.9 移行

**無い。** ①のキーが無ければ②へ、②が無ければ③へ落ちる——これは今日の経路そのもの。
84 §84.3 が「足りないキーを埋めるだけで既存の値には触らない」で解いたのと同じ形を、
「そもそも埋めない」まで進めたもの。新設するトグル 2 つだけは既定と継ぎ元を決める:

| 新キー | 既定 | 継ぐ元 |
|---|---|---|
| `assistantReplySuggestEnabled`（チャットの ✨） | true | `replySuggestEnabled`（明示 OFF は両方へ） |
| `planUpdateEnabled`（計画の更新） | true | （無し＝常時 ON だった＝欠落は「従来の挙動」） |

## 103.10 検証計画

- **Go**: 機能ごとのピンが優先順位に勝つ／ピン先が未接続なら順位へ落ち、**そのとき①のモデルを
  使わない**／タグ無し ctx が従来経路のまま／hidden model が推奨へ落ちる／新トグル 2 つが
  それぞれ 1 機能だけを止める。**陽性対照**: 解決関数を「常にピンを無視」に潰すと落ちる本数を
  先に数える（変異の戻しは Edit で）。
- **Console**: カタログの 8 機能がタブと使用量ビューで同じラベルを引く／オフの機能に
  エージェント・モデル行が出ない／「いま使うのは」が未取得のとき出ない。
- **実機**: 1 機能だけ codex にピンして ✨ を押し、**台帳の行が `feature=suggest.session` /
  `kind=codex`** になることを見る。画面の表示ではなく台帳で確かめる——97 で嘘を暴いたのが
  台帳だったのと同じ理由。
- 返信候補のゲート修正は、**オフで 400 が返ること**（＝サーバが本当に止めていること）を
  直接叩いて確認する。いまは 200 が返る。

## 103.11 やらないこと

- **アシスタントの自動ターン・要約引き継ぎのモデル**はアシスタントタブに残す（§103.2 の境界）。
- **自前エンジン（lcpp）を指定先に加える**のは別建て（決定 7）。
- **自動発火の軸**（`title.session` の自動バナー、`translate.mirror` の自動翻訳）は今回いじらない。
  「押したら動く／勝手に動く」は機能ごとのエージェント・モデルとは別の軸で、混ぜると
  1 枚のカードが 2 つの質問に答えることになる。
