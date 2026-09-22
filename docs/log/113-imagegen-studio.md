# 113. 画像生成スタジオ——会話でプロンプトを直し、人がボタンで生成し、結果を見ながら微調整する

- 状態: **設計案のみ（実装なし）**。2026-09-23、セッション `s24yagr`。ADR は書き換えていない。
  §8 に利用者へ上げる判断が 7 件ある。合意が得られたら ADR に起こす（番号は起票時に採る——
  別ブランチが 0099 を使っている）。番号 112 は別ブランチ（PR #902・Kontext のクロップ）が
  使う見込みなので飛ばした。
- 関連: [ADR 0081](../decisions/0081-image-generation-pane.ja.md)（今のペイン。本稿はその
  決定 6・7 を覆し、1〜5・8〜12 は継ぐ）/ [ADR 0080](../decisions/0080-image-gallery-pane.ja.md)
  （絵を見る場所）/ [ADR 0069](../decisions/0069-image-generation-providers.ja.md)（決定 8
  「押していないのにモデルを呼ばない」は本稿でも守る）/ [19](19-assistant-chat.md)（アシスタント
  チャット＝会話の器）/ [33](33-chat-context-usage.md) 第 5 段（会話の脇に「計画」を原文で持つ
  ＝本稿の下書きの原型）/ [103](103-ai-assist-per-feature.md)（機能ごとのエージェント・モデル
  指定＝スタジオのアシスタントもこの粒度で指定する）/ [111](111-inpaint-mask-canvas-p2.md)
  （マスク描画。本稿の画面に入る場所を §4 に書く）

---

## 1. 要求

> 画像生成ビューを見直したい。アシスタントとチャットしながらプロンプトを直す、画像生成を
> アシスタントの様な機能を付けたい。モデルの選択、プロンプト、ネガティブ、生成履歴、詳細な設定、
> 会話するとプロンプトが更新される、生成は人がボタンで、生成結果を見ながら微調整、などを
> 盛り込みたい。今の画像生成ビューを土台にしなくてもよい。

要求を分解すると 3 つの「主語」がある。

| 主語 | すること | 今の ADR 0081 での扱い |
|---|---|---|
| **アシスタント** | 会話の相手。利用者の言葉を、そのモデルの方言のプロンプトに翻訳し、結果を見て直す | 決定 7 層 B＝ボタン 1 回の単発問い合わせ。会話は無い |
| **人** | モデルを選ぶ。生成ボタンを押す。結果を見て「もっと夕暮れに」と言う | ある（フォーム＋投入） |
| **エンジン** | 絵を作る | ある（Agent のジョブキュー） |

ADR 0081 は「LLM を挟まずに」を表題にした。**本稿でもそれは変わらない**——LLM が挟まるのは
**下書きを直すとき**だけで、**生成の経路には居ない**。ボタンを押すのは人で、押したときに
走るのは今のジョブキューそのものである。変わるのは「下書きをどこに置き、誰が書くか」。

## 2. 今あるもの（2026-09-23 に `e55feb37` で裏取り）

設計を決めた事実だけを挙げる。行番号は当日の木。

### 2.1 画像生成ペイン（ADR 0081・P0 実装済み）

- **下書きはブラウザの `localStorage`**（`af.imagegen-draft.<tenant>`・`draft.ts:106`）。
  変更のたびに書き、`storage` イベントでポップアウトと同期する（`ImagegenView.tsx:92-98`）。
  サーバは下書きを知らない。**アシスタントが書ける場所ではない。**
- **履歴は無い。** 真実は Agent のジョブ一覧（未完了＋完了 500 件・メモリ上・Agent 再起動で消える
  `jobs.go:41-57`）。永続なのは 1 枚ごとのサイドカー `<name>.png.json`（`props.go:46-120`）と
  使用量台帳の行だけ。「あの設定に戻す」はライトボックスの**プロパティ → 画像生成で開く**を
  1 枚ずつ（`viewer/ImageProps.tsx:123`）。結果カードにあるのは seed の 2 ボタンのみ
  （`parts/ResultCards.tsx:84-134`）。
- **プロンプト支援（層 B）は `askAssistant()` 1 発**（`prompthelp.ts:41-87` が英語の指示文
  1 通を組み、JSON `{prompt,negative,note}` を求め、`parseProposal` が解析、モーダルで
  「使う／プロンプトだけ／捨てる」）。会話は残らず、2 回目は 1 回目を知らない。
- **族の知識は 2 か所に割れている**。「その族が読む摘み」は Agent（`knobs`・`comfy.go:329-358`）、
  「その族の方言・品質接頭辞・推奨範囲」は Console の i18n（`families.ts:42-177`・11 族）。
  アシスタントに読ませる文は Console が組んでいる（`buildPromptHelpMessage`）。
- Agent の要求語彙は十分に広い（`jobs_http.go:28-59`）: prompt / negativePrompt / size / count /
  inputs / mask / model / loras / seed / strength / params{steps,cfg,sampler,scheduler} / label /
  out_dir / jobs / seed_policy / trial / full_steps。族が読まない値は**警告で返る**
  （`comfy.go:375-399`）。**本稿は語彙を 1 語も足さない。**
- ペインはワークスペースに 1 枚（決定 6「的の欄は無い」）。

### 2.2 アシスタントチャット（log 19）

- **会話は Agent 側のファイル 1 個**（`~/.config/agent-fleet/chats/<id>.json`・
  `chat_store.go:35`）。メッセージは `role/content/ts/agent/model/steps` の**文字列**で、
  部品（parts）も添付欄も tool 結果も無い（`chat.go:43`）。添付は本文中のパス（claude は
  「Read tool で開け」、他は「Look at the following file(s)」・`lib/pastedImages.ts:14-19`）。
- **会話の脇に構造化された物を持つ前例が「計画」**（`ChatConversation.Plan`・`chat.go:147-154`）。
  圧縮時の `<<<PLAN>>>` 区切り解析（`chat_plan.go:46-47`）、更新ボタン（単発ヘッドレス）、
  手編集、アシスタント自身の MCP `set_chat_plan`（`mcp_stdio.go:2743-2790`・
  `PUT /chat/conversations/{id}/plan` に `notice:true`）の 4 経路。**変わったときだけ**
  通知カードを積む（`setPlan` の真偽・`chat_plan.go:216-232`）。新しいプロバイダセッションの
  最初のターンだけ前置する（`InjectPlan`）。
- **人格（system prompt）は会話の作成時に固まる**: アシスタント定義（`assistant_id`）か
  動詞人格（`VerbPersona`・`chat_handlers.go:58`）。ターン単位の上書き口は無い。
- **バックエンド 7 種**（claude / codex / opencode / agy / cursor / lcpp / muse・
  `chat_providers.go:44`）。lcpp（フリート自前の llama-server）はツール無し・ストリーム無し
  （`chat_providers_lcpp.go:60,128`）。muse は MCP 子の環境を洗うため af サーバが 401。
- **チャットのアシスタントは `generate_image` を呼べない**（セッション所有者が要る
  ゲート・`mcp_stdio.go:166-170, 1471-1478`）。要求の「生成は人がボタンで」と一致する。
- ChatView は `conversationId` を受ける部品（`ChatView.tsx:73`）。下書き→実会話の昇格で
  **自分のペインを `kind:"chat"` に書き換える**（`:77-78`）——他ペインに埋めるならここを
  コールバックに外す。ストリーム・停止・添付・ライトボックス・進行中の再接続は部品が持つ。
- 1 ターンの上限は 240 秒（`chat.go:492`）。台帳は 1 ターン 1 行、`Feature=assistant.chat`・
  `Ref=会話 id`（`chat_wiring.go:81`）。

### 2.3 エンジン側（変えない）

- ジョブキュー・試走・グループ・取消・EMA・サイドカー・`props` は ADR 0081 のまま使う。
- 温まっていないエンジンの 1 枚目は分単位（実測: qwen-image-edit 5.3 分・`engine.go:216-245`）。
  **会話 1 往復（数秒〜数十秒）より生成 1 枚のほうが遅い**。画面の輪はここで決まる。

## 3. 判断（決定候補）

### D1 — 下書きはサーバに置き、会話に結び付ける（ADR 0081 決定 6 を覆す）

`ChatConversation` に `image_draft`（構造化 JSON）と `image_draft_updated_at` を足す。
「計画」と同じ位置・同じ 4 経路（アシスタントの返事／手編集／—／—）で、**下書きの真実は
会話**。`localStorage` は「最後に開いたスタジオの会話 id」だけを覚える。

- 理由: アシスタントが書ける場所はサーバにしか作れない。ブラウザ 2 枚（ポップアウト・
  スマホ）で同じ下書きを見るのも、サーバに置けば `storage` イベントの同期が要らなくなる。
- 会話が無いうちは今の `localStorage` 下書き（`draft.ts`）で動き、**最初の発言で会話を作る
  ときに PUT して移す**（チャットの `moveDraft` と同じ形・二重の真実は「会話が出来る瞬間」
  だけ）。会話を作らずに生成だけする使い方は今のまま残る（§3 D9）。

### D2 — アシスタントが下書きを動かす契約は「返事の末尾の fenced block」1 つ

返事の末尾に ` ```imagedraft ` の fenced block で**下書きの全文 JSON** を書かせる。Agent が
ターン完了時に切り出し（`parseCompactOutput` と同じ位置・同じ縮退＝区切りが無ければ触らない）、
保存する本文からは剥がし、変わった欄だけの **notice カード**（「下書きを更新: cfg 7→5・
prompt +3 語」）を会話に積む。

MCP ツール（`set_image_draft`）にしない理由:

1. **7 バックエンド全部で動く**。lcpp はツールを持たず、muse は af サーバに届かない。
   lcpp で動くことは ADR 0081 未解決 3（CLI ログインの無い会員）の答えそのものになる。
2. ツールの説明文は**毎ターン固定費**（`docs/log/99-lcpp-agent-kind.md:268`・`mcp_stdio.go:1204-1210`
   「引数は使いにくいときだけ言葉を貰う」）。fenced block の指示は人格に 1 回書くだけ。
3. `tools=none` のアシスタント定義（`assistant.ask` と同じ経路）でも動く。

MCP でないと失う物: ツール呼び出しの型検査。代わりに Agent 側の解析が **`jobs_http.go` の
`spec()` と同じ検証**（sampler の許可表・steps≤150・cfg≤30・size の 8 の倍数）を通し、
落ちた欄は notice に理由付きで出す。**下書きに入る値は投入時に 400 にならない値だけ**、が
不変条件——ADR 0081 決定 9 の追記が言う「番人はペイン側にも要る」を、下書きの入口に置く。

区切り欠落・JSON 崩れ・空: 下書きは触らず、通知は出さず、ペインに「今回の返事には下書きの
更新が含まれていません」を 1 行。「崩れた出力で消さない」（log 33 第 5 段）を継ぐ。

### D3 — アシスタントが書ける欄と、人しか書けない欄を分ける

| アシスタントが書ける | 人だけ |
|---|---|
| `prompt` `negative` `params{steps,cfg,sampler,scheduler}` `size` `loras[{name,weight}]` `strength` | `model` `seed`/`seed_policy` `jobs`(N) `count`(batch) `out_dir` `label` `op` `inputs` `mask` |

- `model` を人側に置く理由: 切替は**族の切替**＝読める摘み・サイズ・ネガティブの可否が全部
  変わり、冷えたエンジンなら 1 枚目に数分かかる。アシスタントは「このモデルなら〜」と**提案
  カード**（「切り替える」ボタン付き）まで。押すのは人。
- `seed` `N` `out_dir` は「生成のボタン」の一部であり下書きの内容ではない。
- **欄ごとの錠**（🔒）をフォームに置く。錠の掛かった欄への書き込みは Agent が落とし、
  notice に「cfg は固定のため無視」と出し、次のターンの文脈（D4）にも「固定: cfg」と書く。
  「手で直した直後に戻される」を防ぐ最小の道具で、自動の錠（手編集で自動ロック）は
  作らない——利用者が意図しない錠が増えて「アシスタントが言うことを聞かない」になる。

### D4 — 文脈は Agent が毎ターン前置する。Console は組まない

`InjectPlan` と同じ位置（`InjectCarryover`・`chat_plan.go:256`）に `InjectStudio` を足し、**送るたび**に
次の 1 ブロックを前置する（会話には保存しない）:

```
[image studio state]
model: illustrious-v2 (family sdxl; dialect: tag list; quality prefix: "masterpiece, best quality";
  negative: read; knobs: steps cfg sampler scheduler negative; sizes: 1024x1024 1216x832 ...;
  defaults: steps 28 cfg 5 euler_ancestral normal)
loras available: add-detail (triggers: "add_detail"), ...
locked: cfg
draft: {"prompt": "...", "negative": "...", "params": {...}, "size": "1216x832", "loras": [...]}
last results: #12 seed 815723004 1216x832 21s warnings: [lora_trigger_missing]; #11 ...
reply rule: when you change the draft, end with ```imagedraft <full json> ```
```

- **毎ターン**にする理由: 「計画」は数 KB で新規セッションの初回だけだったが、下書きは
  1 KB 弱で、**人が生成ボタンを押すたびに変わる**（結果・警告・手編集）。差分送りは
  「前回何を送ったか」の帳簿が要り、それが狂うと古い下書きを自信を持って上書きする
  （原文キャリーフォワードの「強く間違える」）。1 ターン 500〜800 トークンの固定費で買う。
- **Console は組まない**理由: 族の知識（D5）と `knobs`・許可表は Agent が持ち、結果
  （seed・警告）も Agent が持つ。Console が組むと、`prompthelp.ts` が今そうであるように
  「Agent の表と Console の表が二重」になる。
- 「last results」は**その会話に紐づくジョブ**（`label` でなくジョブ要求に `studio` 欄——
  これは Agent 内部の欄で wire の語彙ではない）の直近 3 件。絵そのものは送らない（D6）。

### D5 — 族の方言・品質接頭辞・推奨範囲を Agent の族表に移す（Console の `FAMILY_CARDS` を畳む）

`comfyFamilyRow`（`comfy_workflows.go:460-556`）に `Dialect`（tags / prose）・
`QualityPrefixes []string`・`StepsRange`・`CFGRange` を足し、`GET /imagegen/status` の
`modelStatus` に出す。Console の族カードはそれを描くだけになり（i18n には方言名の訳語だけ残る）、
アシスタントの文脈（D4）も同じ行から組む。

- 理由: ADR 0081 決定 4「何を読むかは族が決め、言葉で言う」を、読む摘み以外にも広げるだけ。
  今は `knobs` は Agent・方言は Console で、**新しい族を足すとき 2 か所**（ADR 0098 の
  qwen-image-2.1 は Agent 側 `2a7a29ab3` と Console 側 `dda33988c` の 2 コミットで両方触った）。
- 却下: Console から族カードの文を毎ターン送る。文脈の組み立てが Console に残り、
  D4 が崩れる。

### D6 — 「結果を見ながら」の「見る」は 2 段: 人は常に、アシスタントは押したときだけ

- 人: 結果カードと試走枠はフォームの隣（今のまま）。**変更点のハイライト**——アシスタントの
  ターンで動いた欄は 1 ターンのあいだ縁取りし、カードから「この絵の設定に戻す」（サイドカー
  →下書き・履歴 §3 D7）を出す。
- アシスタント: 結果カードの **「この絵をアシスタントに見せる」** で、次の発言にその絵の
  パスを添付として付ける（チャットの添付と同じ経路＝claude / codex のみ・
  `ChatView.tsx:453-456` の `canAttach`）。lcpp・opencode 等では理由付きで無効。
  **押していないのに絵を送ることは無い**（ADR 0069 決定 8）——絵 1 枚は入力トークン
  1,000 前後で、毎ターン自動で付けると 40 枚の量産中に 40 回払う。
- 「この絵を言葉で記述」（vision で絵→プロンプト）は同じ添付で「この絵をプロンプトにして」と
  言えば済むので、別機能にしない。エンジン側 vision（ADR 0081 P2）は要らなくなる。

### D7 — 履歴は「押した 1 回」を単位に、サイドカーから作る。Agent 再起動を跨ぐ

- **版**（version）: 生成ボタン（試走・投入）を押した瞬間の下書きの写し。会話に
  `image_versions[]`（下書き・押した時刻・ジョブ／グループ id）。アシスタントの編集だけでは
  版にならない——「押した」が人の判断の単位で、比べたいのはそこ。
- **絵の履歴**: Agent に `GET /imagegen/history?studio=<conv>&before=&limit=` を足す。
  裏は `generated/console/history.jsonl`（`store.go` がサイドカーを書くときに 1 行追記・
  無ければサイドカーを走査して再生成）。行はサイドカーの要約（パス・seed・サイズ・model・
  prompt の先頭・warnings・elapsed・`studio`・`version`）。**ジョブ一覧は今まで通り
  「走っている物」の面**、履歴は「出来た物」の面。
- 履歴パネルの操作: 「この設定に戻す」（版→下書き）・「参照にする」（`inputs[0]`＋`op=edit`）・
  「アシスタントに見せる」（D6）・「並べる」（最大 4 枚のピン留め比較・版の差分を横に）。
- 掃除の方針は ADR 0081 決定 3 のまま（`console/` は消さない・`trial/` は 7 日）。
  `trial/` の絵が消えた行は「絵は消えました（設定は残っています）」で残す。

### D8 — スタジオは複数持てる。ペインは会話 id を持つ

`{ kind: "imagegen"; conversationId: string | null }`。`sameTarget` は会話 id。
ADR 0081 未解決 4（スタジオを同時に複数）は、会話が単位になった瞬間に**追加の仕組み無しで**
成立する（モデル 2 本でスタジオ 2 枚＝会話 2 つ）。

- スタジオの会話は通常の会話一覧（左レール）に**魔法の杖のアイコン付き**で並び、そこから
  開くと chat ペインでなく imagegen ペインが開く（`open.ts` の分岐 1 つ）。別の一覧は作らない。
- 会話の削除＝スタジオの削除（下書きと版が消える。絵は残る）。

### D9 — 会話は要るときだけ。会話無しの量産は今のまま動く

ペインを開いただけでは会話を作らない。左の会話欄は「話しかければアシスタントが下書きを
直します」の空状態で、フォームは `localStorage` の下書きで動き、生成もできる（今日の使い方）。
最初の発言で会話を作り（D1）、以後は会話が真実。**ADR 0081 の利用者は何も失わない。**

### D10 — 人格は組み込みのアシスタント定義 1 本。エージェント／モデルは 103 の粒度で指定

`VerbPersona` の隣に **`studio` 人格**を組み込みで持つ（役割・D2 の返事規則・「勝手に
生成しない／できない」・「方言に従う」・「利用者の言語で話す」）。実行するエージェントと
モデルは log 103 の機能別指定に `imagegen.chat` を 1 行足して選ぶ——台帳の feature も
`imagegen.chat`（使用量ビューで「見た行と直す行が同じ名前」）。既定は会話の推奨モデル
（claude なら `claude-sonnet-5`）。

- lcpp を指定できる（103 決定 7 は「今回入れない」だったが、chat 経路には lcpp 分岐が
  ある・`chat_providers_lcpp.go`）。ただし冷えた lcpp は非ストリームの 45 秒ホールドで
  `engine_waking` に落ちる（[lcpp-agent-kind-survey]）——スタジオでは**「エンジン起動中・
  もう一度送ってください」**の notice にして落とさない。

### D11 — 層 B（`PromptHelpModal`）は畳む。層 A（族カード・トリガー語チップ・錠付きネガティブ）は残す

「プロンプトを書いて」は会話の最初の発言そのもの。モーダルと `prompthelp.ts` は消す
（`askAssistant` の他の利用者＝メモ整理・TTS 要約は無関係）。層 A は D5 で出所が Agent に
移るだけで、画面の振る舞いは同じ。

### D12 — 生成の経路は 1 バイトも変えない

`POST /imagegen/jobs` の語彙・検証・ジョブ・試走・グループ・取消・EMA・サイドカー・
`props`・使用量の `Images/Pixels`・CP の中継 7 行は**そのまま**。Agent 内部でジョブに
`studio`（会話 id）と `version` を持たせるのは、D4 の「last results」と D7 の履歴の
ためで、要求語彙に足すのは `label` と同じ「Console が付ける印」の 2 つ。

## 4. 画面

3 列。狭い幅（`@container paneview (max-width: 720px)`）ではタブ **会話 / 設定 / 結果** に畳む。

```
┌ 画像生成スタジオ ─ [モデル ▾ ●warm] [すぐ描けます] [履歴] [ギャラリー] ┐
│ 会話                 │ 設定（下書き）           │ 結果                │
│ ───────────────────  │ 族カード（折り畳み）      │ 試走枠  seed 8157…  │
│ 🧑 港の夕暮れ、女性1人 │ プロンプト        🔒     │ [この seed] [見せる] │
│ 🤖 Illustrious は…    │ ┌ 1girl, harbour, … ┐    │ ───────────────     │
│    ```imagedraft は   │ │ (今回の変更を縁取り)│    │ 12/40 ▓▓▓░░ 残 8分  │
│    カードに畳まれる   │ └───────────────────┘    │ [一時停止][飛ばす]  │
│ ┌ 下書きを更新 ─────┐ │ ネガティブ        🔒     │ ───────────────     │
│ │ prompt +3 語      │ │ steps 28 cfg 5🔒 …      │ [絵][絵][絵][絵]    │
│ │ cfg 7 → 5         │ │ size 1216x832           │  seed / 21s / ⚠1    │
│ └───────────────────┘ │ LoRA ☑ add-detail 0.8   │  [戻す][参照][見せる]│
│ 🧑 もっと暗く         │ ▸ 上級（op・参照・mask・ │ ───────────────     │
│                      │   batch・出力先・label）  │ 履歴 v4 v3 v2 …     │
│ [＋絵] [_________] ⏎ │ seed [random▾] N [40]    │  （並べる: 最大 4）   │
│                      │ [試走 ⌃⏎] [40 枚を投入]  │                     │
└──────────────────────┴─────────────────────────┴─────────────────────┘
```

- **左＝ChatView をそのまま埋める**（`variant="studio"`: 計画パネル・TTS・改名・返信候補を
  隠し、昇格を `onPromoted(convId)` に外す）。ストリーム・停止・添付・進行中の再接続を
  書き直さないため。
- **中＝今の `GenerateForm` を「下書きの編集面」として再利用**（`Knobbed`・族カード・
  トリガー語チップ・`InputPicker`・スライダー）。足すのは欄ごとの 🔒 と、変更ハイライト。
  **モデル選択はペイン頭に上げる**——スタジオの「主語」で、D3 の「人だけ」を位置で言う。
- **右＝試走枠・グループ行・結果カード（今の部品）＋履歴**。履歴は下に続く一覧で、
  「並べる」でピン留め 4 枚を横並び・版の差分を脇に。
- マスク描画（log 111）は中列の「上級 → 参照画像」の隣（111 §3 の P2a と同じ場所）。
- スマホ（ミラー）は対象外のまま。タブ畳みで 1 列にはなる。

## 5. データと配線

### 5.1 会話に足す物（Agent・`chatx`）

```go
ImageDraft          *ImageDraft `json:"image_draft,omitempty"`
ImageDraftUpdatedAt int64       `json:"image_draft_updated_at,omitempty"`
ImageLocks          []string    `json:"image_locks,omitempty"`     // field names
ImageVersions       []ImageVersion `json:"image_versions,omitempty"` // capped (e.g. 200)
```

`ImageDraft` は `jobRequest` の**アシスタントが書ける欄の部分集合**（D3）＋ `model`。
型は `imagegen` パッケージの物を借り、検証は `spec()` の関数を切り出して共有する
（二重実装にしない）。

Agent の口（全部 CP の中継リストに 1 行ずつ・`registerChatRoutes` の隣）:

| 口 | 用途 |
|---|---|
| `GET/PUT /chat/conversations/{id}/image-draft` | 読み・手編集（PUT は `{draft, locks}`・検証は投入と同じ） |
| `POST /chat/conversations/{id}/image-versions` | 生成ボタンが押された写し（Console が投入の直前に呼ぶ。応答に `version`） |
| `GET /imagegen/history` | D7 |

会話作成: `POST /chat/conversations` に `kind: "studio"`（`seed_verb` の隣）。人格は D10。

### 5.2 ターンの流れ

1. 人が発言 → `POST …/messages/stream`（本文だけ。今と同じ）。
2. Agent `InjectStudio`（D4）が状態ブロックを前置（保存しない）。
3. バックエンドの返事 → 完了時に ` ```imagedraft ` を切り出し → 検証 → 錠を除いて適用 →
   変わった欄があれば notice（`notice_key: image_draft_updated`・`notice_args: {changed: [...],
   dropped: [...]}`）→ 保存本文は block を剥がした物。
4. SSE `done` の `conversation` に `image_draft` が乗る → フォームが更新・ハイライト。
5. 人がフォームを直す → `PUT …/image-draft`（デバウンス 500 ms）。
6. 人が生成 → `POST …/image-versions`（写し）→ `POST /imagegen/jobs`（`studio`・`version` 付き）。
7. 結果は今の 2 秒ポーリング。次の発言で D4 の「last results」に載る。

### 5.3 費用

- 会話 1 ターン＝アシスタントのモデル 1 ターン（`imagegen.chat`・Ref=会話 id）。
  D4 の前置 500〜800 トークンが入力に乗る。絵の添付は押したときだけ（D6）。
- 生成＝今まで通り GPU 時間（`tool.imagegen`・Images/Pixels）。

## 6. 段階

- **P0（芯）**: D1 / D2 / D3 / D4 / D9 / D10 / D12 と、画面の 3 列（ChatView 埋め込み・
  錠・ハイライト・notice カード）。履歴は**版の一覧**（会話の `image_versions`）だけ。
  D5（族表の移設）は P0 に入れる——入れないと D4 の文脈を Console が組む形に戻る。
  D11（層 B の撤去）も P0（残すと「書いて」ボタンと会話の 2 経路が並ぶ）。
- **P1**: D7 の絵の履歴（`history.jsonl`・`GET /imagegen/history`・戻す／参照／並べる）、
  D6 の「見せる」、モデル提案カード（D3）、D8 のレール導線、スマホのタブ畳み。
- **P2**: 会話からのスイープ（「cfg を 3 通り」→ アシスタントが**行列の提案**を書き、人が
  「投入」）、マスク描画（111）との接続、lcpp の起床 notice（D10）。

## 7. 却下した案

- **アシスタントに `generate_image` を持たせ、会話の中で生成させる**（「画像生成 GPT」型）。
  要求の「生成は人がボタンで」に反し、そもそもチャットの MCP は所有セッションが無く呼べない。
  40 枚の量産で 40 ターン払う——ADR 0081 の背景がそのまま当たる。
- **MCP `set_image_draft`**（D2）。lcpp / muse で動かず、説明文が毎ターンの固定費。
- **下書きを `localStorage` に置いたまま、返事から Console が JSON を拾う**。ポップアウト・
  スマホ・再読み込みで真実が割れ、Agent の検証を通らない値が下書きに入る。
- **会話の全文を毎ターンでなく圧縮時だけ前置**。下書きは生成のたびに変わる（D4）。
- **アシスタントに `model` を書かせる**（D3）。族の切替と冷えたエンジンの起床を、人が
  気付かないまま起こす。
- **アシスタントの編集ごとに版を切る**（D7）。比べたいのは「押した」単位。編集ごとだと
  1 会話で数十版になり、一覧が意味を失う。
- **履歴を Agent のジョブ一覧の拡大で作る**。メモリ上で再起動に消える物を伸ばしても
  「あの日の設定」に届かない。
- **スタジオ専用の会話一覧**（D8）。一覧が 2 つになる。
- **生成結果を毎ターン自動で添付**（D6）。見えないプラン消費。
- **ペインを ChatView と別に書き直す**。56 KB の部品が持つ再接続・停止・添付を失う。

## 8. 利用者への確認

1. **D3 の線引き**——`model` を人だけにする（提案カード止まり）で良いか。逆に「アシスタントに
   モデルまで任せたい」なら、切替時にエンジン起床の見積りを notice に出す形で開ける。
2. **D2 の契約**——fenced block（全バックエンド）で良いか。claude 限定で良ければ MCP でも
   組めるが、lcpp で動くことを本稿は価値と見た。
3. **D7 の履歴の単位**——「押した 1 回＝版」で良いか。
4. **D8**——スタジオを会話一覧に混ぜて出す（杖アイコン）で良いか。
5. **D11**——「プロンプトを書いて」モーダルを消して良いか（会話の最初の発言に統合）。
6. **D5**——族の方言・品質接頭辞を Console i18n から Agent の族表へ移して良いか
   （訳語は i18n に残る。方言の文そのものは英語 1 本になる）。
7. **段階**——P0 に D5・D11 を含める重さ（Agent・CP・Console の 3 レーン）で良いか。
   軽くするなら D5 を P1 に落とし、P0 は Console が文脈を組む暫定（`prompthelp.ts` の流用）。

## 9. 踏みどころ（実装前に知っておく罠）

- **fenced block の剥がし忘れ**: 保存本文にも SSE の delta にも block が流れる。delta は
  ストリーム中に見えて良い（カードに畳むのは完了後）が、**保存本文から剥がすのは 1 か所**
  （`acc` 統一の教訓・[assistant-chat]「result が正」に戻すと再発）。
- **錠と検証の順**: 検証（400 相当）→ 錠の除外 → 適用。錠の欄に不正値が来ても落とすのは
  同じなので順序は結果に効かないが、notice の理由は「固定のため」と「不正な値」を分ける。
- **`sameTarget` を会話 id にすると、会話無し（`null`）のペインが 2 枚開ける**。null 同士は
  同じ的と見なす（D9 の「ワークスペースに 1 枚」を null に限って残す）。
- **ChatView の昇格結合**（`ChatView.tsx:77-78`）を外さずに埋めると、最初の発言で
  ペインが chat に化ける。
- **`engineCatalogModelRow` は運ばない欄を黙って落とす**（[cp-session-wire-relay-drops-fields]）。
  D5 は Agent の族表（CP を通らない）に置くので当たらないが、行に `prompt_notes` を足す
  P1 案（ADR 0081 未解決 2）を採るなら中継に足す。
- **モジュールスコープのキャッシュは dom テスト間で漏れる**（[module-scope-cache-leaks-across-dom-tests]）
  ——`available.ts` を会話 id 付きで拡張するなら鍵に会話 id を入れる。
- **純関数の試験は `api.ts` を import できない**（ADR 0081 レーン C）——`wire.ts` に型、
  `api.ts` に fetch の分離を守る。
- 撮影スタブ（`console/scripts/shots/server.mjs`）に `chat/conversations/*/image-draft` と
  `imagegen/history` を足さないと、ガイドの絵が壊れ画像になる。
