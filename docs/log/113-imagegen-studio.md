# 113. 画像生成スタジオ——セッションのエージェントと会話でプロンプトを直し、人がボタンで生成し、結果を見ながら微調整する

- 状態: **設計案のみ（実装なし）**。2026-09-23、セッション `s24yagr`。ADR は書き換えていない。
  §8 に利用者へ上げる判断が 7 件ある。合意が得られたら ADR に起こす（番号は起票時に採る——
  別ブランチが 0099 を使っている）。番号 112 は別ブランチ（PR #902・Kontext のクロップ）が
  使う見込みなので飛ばした。
- **改訂（同日）**: 初稿はアシスタントチャット（log 19）を会話の器にしていた。利用者の
  「セッションにしてはどうか。高度な推論が要る。リポジトリ内の資料を見ながらプロンプトを
  組むこともある」を受けて、**器をセッション（Managed）に替えた**。初稿の形は §7 に
  却下理由ごと残す。
- 関連: [ADR 0081](../decisions/0081-image-generation-pane.ja.md)（今のペイン。本稿はその
  決定 6・7 を覆し、1〜5・8〜12 は継ぐ）/ [ADR 0080](../decisions/0080-image-gallery-pane.ja.md)
  （絵を見る場所）/ [ADR 0069](../decisions/0069-image-generation-providers.ja.md)（決定 8
  「押していないのにモデルを呼ばない」は本稿でも守る）/ [ADR 0015](../decisions/0015-agent-managed-driver.ja.md)
  と [27](27-agent-managed-driver.md)（Managed 実行方式＝本稿の器）/ [ADR 0073](../decisions/0073-session-spawned-sessions.ja.md)
  （セッションの出自 `Origin`）/ [33](33-chat-context-usage.md) 第 5 段（会話の脇に「計画」を
  原文で持つ＝下書きの原型）/ [111](111-inpaint-mask-canvas-p2.md)（マスク描画）

---

## 1. 要求

> 画像生成ビューを見直したい。アシスタントとチャットしながらプロンプトを直す、画像生成を
> アシスタントの様な機能を付けたい。モデルの選択、プロンプト、ネガティブ、生成履歴、詳細な設定、
> 会話するとプロンプトが更新される、生成は人がボタンで、生成結果を見ながら微調整、などを
> 盛り込みたい。今の画像生成ビューを土台にしなくてもよい。

> （2 巡目）アシスタントチャットではなくセッションにしてはどうか。アシスタントするなら
> 高度な推論が必要。またレポ内の資料を見ながらプロンプトを組み立てることもありうる。

要求を分解すると 3 つの「主語」がある。

| 主語 | すること | 今の ADR 0081 での扱い |
|---|---|---|
| **エージェント**（セッション） | 会話の相手。利用者の言葉と**リポジトリの資料**を、そのモデルの方言のプロンプトに翻訳し、結果を見て直す | 決定 7 層 B＝ボタン 1 回の単発問い合わせ。会話も資料も無い |
| **人** | モデルを選ぶ。生成ボタンを押す。結果を見て「もっと夕暮れに」と言う | ある（フォーム＋投入） |
| **エンジン** | 絵を作る | ある（Agent のジョブキュー） |

ADR 0081 は「LLM を挟まずに」を表題にした。**本稿でもそれは変わらない**——LLM が挟まるのは
**下書きを直すとき**だけで、**生成の経路には居ない**。ボタンを押すのは人で、押したときに
走るのは今のジョブキューそのものである。変わるのは「下書きをどこに置き、誰が書くか」。

2 巡目の 2 要件が器を決めた。**「資料を見ながら」は cwd がリポジトリであることを要求し、
「高度な推論」はモデル・effort・思考をその場で選べることを要求する。** どちらもセッションが
生まれつき持ち、アシスタントチャットは持たない（§7 の 1 つ目）。

## 2. 今あるもの（2026-09-23 に `e55feb37` で裏取り）

設計を決めた事実だけを挙げる。行番号は当日の木。

### 2.1 画像生成ペイン（ADR 0081・P0 実装済み）

- **下書きはブラウザの `localStorage`**（`af.imagegen-draft.<tenant>`・`draft.ts:106`）。
  変更のたびに書き、`storage` イベントでポップアウトと同期する（`ImagegenView.tsx:92-98`）。
  サーバは下書きを知らない。**エージェントが書ける場所ではない。**
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
  ADR 0098 の qwen-image-2.1 は Agent 側 `2a7a29ab3` と Console 側 `dda33988c` の 2 コミットで
  両方触った。
- Agent の要求語彙は十分に広い（`jobs_http.go:28-59`）: prompt / negativePrompt / size / count /
  inputs / mask / model / loras / seed / strength / params{steps,cfg,sampler,scheduler} / label /
  out_dir / jobs / seed_policy / trial / full_steps。族が読まない値は**警告で返る**
  （`comfy.go:375-399`）。**本稿は語彙を 1 語も足さない。**
- ペインはワークスペースに 1 枚（決定 6「的の欄は無い」）。

### 2.2 セッション（器になる側）

- **Managed の 1 ターンは `POST /sessions/{name}/turn {op:"start", prompt, attachments[]}`**
  （`session_turn.go:49-58`）。`attachments` は**絶対パスの一級の添付**で、Managed のドライバが
  API の添付に変換する（TUI は Console が本文にパスを織り込む）。プロンプトは Agent の
  `sendManagedPrompt`（`session_carried.go:333`）1 関数を通る＝**前置する場所がある**。
- **Managed ドライバは 9 kind 全部にある**（`internal/agents/{agy,claude,codex,copilot,cursor,kiro,lcpp,muse,opencode}`・
  `driverOf`＝`session_turn.go:44`）。モデル・effort・思考モードはセッションのメタ
  （`session.Meta.Model/Effort/Mode`・`session.go:386-391`）で、動的変更が再起動を生きる。
- **セッション側 af MCP サーバは自分のセッション名を知っている**（`AF_SESSION_NAME`→
  `mcpSourceSession`・`mcp_stdio.go` `RunStdio`・`mcpOwningSession` `:3330`）。ツールの
  広告はこの名で引いたメタで決められる。`generate_image` は ui-prefs の「画像生成」トグルが
  `--image-gen` として起動引数に載るときだけ広告（`ui_prefs.go:286`・`mcp_stdio.go:166-170`）。
  MCP 設定は kind ごとに materialize 時に書かれる（`MaterializeAll`・`mcp_materialize.go:46`）。
- **ミラーはどの kind の転写も描く**（思考・ツールカード・共有ファイルカード・添付）。
  本文に織り込まれた添付の指示文は `splitPastedImages` で剥がして描く
  （`transcript/TranscriptTurn.tsx:433`）＝**前置した状態ブロックも同じ場所で剥がせる**。
  `MirrorView` は `session` 名を受ける部品（`MirrorView.tsx:126-146`）。
- セッションのメタに `Origin`/`OriginSession`（ADR 0073・`session.go:192-193`）がある＝
  「何のために生まれたか」を持つ前例。
- アイドル停止（ADR 0055・log 75）: 在席でなく・アイドル時計が古く・machineBusy 無しで止まる。
  **生成ジョブは Agent の物なのでセッションが止まっても走る**。止まったセッションはミラーの
  `onResume` で起こす。
- セッションは**枠**を消費する（子は 6 まで・停止しても枠は戻らない・削除は利用者だけ）。

### 2.3 アシスタントチャット（初稿の器・却下の根拠だけ）

- `claude -p` を `--disallowedTools Agent Task Workflow Bash Edit Write …` で `chat-wd`
  （リポジトリでない）から起動（`chat_providers.go:2210, 2085`）。**cwd にリポジトリが無く、
  Bash も Edit も無い。** モデルは会話の既定（`AF_CHAT_MODEL`＝sonnet）で effort の口が無い。
- メッセージは文字列のみ（parts・添付欄・tool 結果無し・`chat.go:43`）。添付は本文中のパス。
- `generate_image` を呼べない（所有セッションが要る・`mcp_stdio.go:1471-1478`）。
- 会話の脇に構造化した物を持つ前例「計画」（`Plan`・MCP `set_chat_plan`・`mcp_stdio.go:2743-2790`・
  変わったときだけ notice）は、器を替えても**下書きの原型**として借りる。

### 2.4 エンジン側（変えない）

- ジョブキュー・試走・グループ・取消・EMA・サイドカー・`props` は ADR 0081 のまま使う。
- 温まっていないエンジンの 1 枚目は分単位（実測: qwen-image-edit 5.3 分・`engine.go:216-245`）。
  **会話 1 往復より生成 1 枚のほうが遅い**。画面の輪はここで決まる。

## 3. 判断（決定候補）

### D1 — 「スタジオ」を Agent に置く。セッションとは別の id で、セッションを 1 本結ぶ

Agent に**スタジオ**（`~/.config/agent-fleet/imagegen/studios/<id>.json`・会話ファイルと同じ
0600 の JSON 1 個）を置く: `id / title / draft / locks / versions[] / session（結ばれた
セッション名・空可）/ created_at / updated_at`。**下書きの真実はスタジオ**。`localStorage` は
「最後に開いたスタジオ id」だけを覚える。

- **セッション名でなく別 id にする理由**: エージェントを替えたい（sonnet で組んで opus に
  上げる・claude から codex へ）、セッションが古くて resume できない、枠を空けるために
  セッションを消す——どの場合も**下書きと版は残したい**。スタジオが主で、セッションは
  「いま結ばれている相手」。結び替えは `session` を書き換えるだけ。
- セッションのメタには逆参照 `Studio string`（`Origin` の隣）。セッション側 af サーバは
  これでスタジオを引く（D2）。
- 会話（セッション）が無いうちは今の `localStorage` 下書きで動き、**最初の発言でスタジオと
  セッションを作るときに PUT して移す**（二重の真実は「出来る瞬間」だけ）。

### D2 — エージェントが下書きを動かす契約は、セッション側 af サーバの MCP ツール 2 本

`get_image_studio`（下書き・錠・モデルの事実＝族／読める摘み／サイズ／既定値／LoRA と
トリガー語・直近の結果と警告・版の要約）と `set_image_draft`（**部分更新**。書いた欄だけ
変え、`null` で消す。検証は投入と同じ `spec()`。錠の欄は落として結果に理由を返す。結果は
適用後の下書きと「生成するのは利用者です。押してもらってください」の 1 行）。

**広告はそのセッションがスタジオに結ばれているときだけ**（`mcpOwningSession()` のメタに
`Studio` があるか＝`--image-gen` のような起動引数は要らない。結び直しは tools/list の
次の接続で効く）。**`generate_image` はスタジオのセッションには広告しない**（P0）——
「生成は人がボタンで」を、人格の口約束でなく**広告集合**で保証する（log 19 Q2「広告集合が
境界」）。エージェントが試走まで頼めるようにする案は §8-2。

初稿の「返事末尾の fenced block」を退けて MCP にした理由:

1. **セッションは全 kind が af MCP で話す**のが既定の形で、転写の解析は kind ごとに 9 通り
   ある。fenced block を転写から拾うと 9 通りのパーサに手を入れることになる。
2. **書き込みがターン途中の実時間で起きる**。ツール呼び出しはその瞬間にスタジオへ書け、
   ペインは 2 秒ポーリングで拾う。fenced block はターン末尾まで待つ。
3. ツール説明文の毎ターン固定費は、**スタジオのセッションだけ**が払う（他のセッションには
   広告しない）。

罠: muse は MCP 子の環境を洗うため af サーバが 401（[muse kind の実測]）。**直るまで muse は
スタジオの選択肢から外す**（起動ダイアログの kind 一覧で理由付きに無効）。

### D3 — エージェントが書ける欄と、人しか書けない欄を分ける

| エージェントが書ける | 人だけ |
|---|---|
| `prompt` `negative` `params{steps,cfg,sampler,scheduler}` `size` `loras[{name,weight}]` `strength` | `model` `seed`/`seed_policy` `jobs`(N) `count`(batch) `out_dir` `label` `op` `inputs` `mask` |

- `model` を人側に置く理由: 切替は**族の切替**＝読める摘み・サイズ・ネガティブの可否が全部
  変わり、冷えたエンジンなら 1 枚目に数分かかる。エージェントは `set_image_draft` に
  `suggest_model` を書ける——ペインは**提案カード**（「切り替える」ボタン付き）で出す。押すのは人。
- `seed` `N` `out_dir` は「生成のボタン」の一部であり下書きの内容ではない。
- **欄ごとの錠**（🔒）をフォームに置く。錠の掛かった欄への書き込みは Agent が落とし、ツールの
  結果に「cfg は固定のため無視」と返し、次のターンの前置（D4）にも「locked: cfg」と書く。
  自動の錠（手編集で自動ロック）は作らない——利用者が意図しない錠が増えて「言うことを
  聞かない」になる。

### D4 — 文脈は Agent が Managed のターンに毎回前置する。ミラーは剥がして描く

`sendManagedPrompt` の手前（`POST /sessions/{name}/turn` の `op:"start"`）で、結ばれた
スタジオがあれば次の 1 ブロックを前置する:

```
[image studio state]
draft: {"model":"illustrious-v2","prompt":"…","negative":"…","params":{…},"size":"1216x832","loras":[…]}
locked: cfg
model facts: family sdxl; dialect tag-list; quality prefix "masterpiece, best quality"; negative read;
  knobs steps cfg sampler scheduler negative; sizes 1024x1024 1216x832 …; defaults steps 28 cfg 5 euler_ancestral normal
  (sent when the model changed since the last turn; otherwise: "unchanged — get_image_studio for details")
since last turn: human edited prompt; generated v4 → #12 seed 815723004 21s warnings [lora_trigger_missing], #13 …
rule: change the draft only through set_image_draft; the human presses Generate.
```

- **毎ターン**にする理由: 下書きは 1 KB 弱で、**人が生成ボタンを押すたびに変わる**（結果・
  警告・手編集）。差分だけ送ると「前回何を送ったか」の帳簿が要り、狂うと古い下書きを
  自信を持って上書きする。**下書き・錠・直近の結果は毎回全文**、モデルの事実（変わらない
  物）だけ「モデルが替わったとき」に送る——帳簿は「最後に送ったモデル id」1 つで済む。
- 1 ターン 400〜700 トークンの固定費。**転写にはそのまま載る**（CLI が記録する user メッセージ
  は送った物）ので、ミラーは `splitPastedImages` と同じ位置で `[image studio state]` を
  剥がし、代わりに「状態を送りました（v4・結果 2 件）」の小さなチップにする。
- **TUI（Terminal）実行方式では前置しない**（打鍵がそのまま画面に出る）。スタジオは
  **Managed 専用**（D9）。
- 前置と `get_image_studio` の役割分担: 前置は「いま何が変わったか」、ツールは「全部」。
  人格（D10）は「編集の前に `get_image_studio` を呼べ」とは**言わない**——前置で足りる
  ターンにツール往復 1 回を余計に払わせない。

### D5 — 族の方言・品質接頭辞・推奨範囲を Agent の族表に移す（Console の `FAMILY_CARDS` を畳む）

`comfyFamilyRow`（`comfy_workflows.go:460-556`）に `Dialect`（tags / prose）・
`QualityPrefixes []string`・`StepsRange`・`CFGRange` を足し、`GET /imagegen/status` の
`modelStatus` に出す。Console の族カードはそれを描くだけになり（i18n には方言名の訳語だけ
残る）、`get_image_studio` と D4 の「model facts」も同じ行から組む。

- 理由: ADR 0081 決定 4「何を読むかは族が決め、言葉で言う」を、読む摘み以外にも広げるだけ。
  今は 2 か所（§2.1）。
- 却下: Console が族カードの文を送る。Console が文脈を組む形に戻る。

### D6 — 「結果を見ながら」の「見る」は 2 段: 人は常に、エージェントは押したときだけ

- 人: 結果カードと試走枠はフォームの隣（今のまま）。**変更点のハイライト**——エージェントの
  `set_image_draft` で動いた欄は次に人が触るまで縁取り。カードから「この絵の設定に戻す」。
- エージェント: 結果カードの **「この絵をエージェントに見せる」** で、次のターンの
  `attachments[]` にその絵の絶対パスを積む（Managed の一級の添付・`session_turn.go:53-56`）。
  vision の無い kind／モデルでは理由付きで無効（ミラーの `canAttach` 相当の判定を借りる）。
  **押していないのに絵を送ることは無い**（ADR 0069 決定 8）——絵 1 枚は入力トークン
  1,000 前後で、毎ターン自動で付けると 40 枚の量産中に 40 回払う。
- 「この絵を言葉で記述」（絵→プロンプト）は同じ添付で「この絵をプロンプトにして」と
  言えば済むので、別機能にしない。ADR 0081 P2 のエンジン側 vision は要らなくなる。

### D7 — 履歴は「押した 1 回」を単位に、サイドカーから作る。Agent 再起動を跨ぐ

- **版**（version）: 生成ボタン（試走・投入）を押した瞬間の下書きの写し。スタジオの
  `versions[]`（下書き・押した時刻・ジョブ／グループ id・上限 200）。エージェントの編集だけ
  では版にならない——「押した」が人の判断の単位で、比べたいのはそこ。
- **絵の履歴**: Agent に `GET /imagegen/history?studio=<id>&before=&limit=` を足す。
  裏は `generated/console/history.jsonl`（`store.go` がサイドカーを書くときに 1 行追記・
  無ければサイドカーを走査して再生成）。行はサイドカーの要約（パス・seed・サイズ・model・
  prompt の先頭・warnings・elapsed・`studio`・`version`）。**ジョブ一覧は「走っている物」の面、
  履歴は「出来た物」の面。**
- 履歴パネルの操作: 「この設定に戻す」（版→下書き）・「参照にする」（`inputs[0]`＋`op=edit`）・
  「エージェントに見せる」（D6）・「並べる」（最大 4 枚のピン留め比較・版の差分を横に）。
- 掃除の方針は ADR 0081 決定 3 のまま（`console/` は消さない・`trial/` は 7 日）。
  `trial/` の絵が消えた行は「絵は消えました（設定は残っています）」で残す。

### D8 — スタジオは複数持てる。ペインはスタジオ id を持つ。セッション一覧には杖のアイコン

`{ kind: "imagegen"; studioId: string | null }`。`sameTarget` はスタジオ id（`null` 同士は
同じ的＝会話無しのペインはワークスペースに 1 枚のまま）。ADR 0081 未解決 4（スタジオを
同時に複数）は、スタジオが単位になった瞬間に**追加の仕組み無しで**成立する。

- スタジオに結ばれたセッションは通常のセッション一覧（左レール・俯瞰図 ADR 0096）に
  **魔法の杖のアイコン付き**で並び、そこから開くと mirror ペインでなく imagegen ペインが
  開く（`open.ts` の分岐 1 つ）。スタジオの一覧はペイン頭のピッカー（`GET /imagegen/studios`）。
  別のレールは作らない。
- スタジオの削除＝下書きと版の削除（絵は残る）。セッションの削除は結びを外すだけ（D1）。

### D9 — セッションは要るときだけ。Managed 専用。会話無しの量産は今のまま動く

ペインを開いただけではセッションを作らない。左の会話欄は「エージェントを付ける」の空状態で、
フォームは `localStorage` の下書きで動き、生成もできる（今日の使い方）。
**「エージェントを付ける」で起動ダイアログの部分集合**（kind・モデル・effort／思考・
リポジトリ＝cwd・subdir・permission skip）を出し、**Managed** で起動する。

- Managed 専用の理由: D4 の前置は Managed の口にしか無い。D6 の添付も Managed だけ一級。
  ミラーが画面なので TUI のペインは要らない。
- **cwd はリポジトリ**（`list_repos` の物・任意・既定は前回のスタジオと同じ）。「資料を
  見ながら」はここで成立する。**worktree は既定で作らない**——スタジオは読む側で、書くのは
  頼まれたときだけ（人格・D10）。同じ作業コピーで別セッションが動いているときは起動ダイアログが
  今どおり警告する。
- **ADR 0081 の利用者は何も失わない。**

### D10 — 人格は初回指示（initial prompt）＋ツール説明文＋毎ターンの 1 行

セッションには会話ごとの system prompt の口が無い（user 指示層は人単位・ADR 0042）。
人格は**初回指示**として送る（`create_session` の `initial_prompt` と同じ経路）:
役割、**「生成はしない。できない。利用者が押す」**、方言に従う、リポジトリの資料は頼まれたら
読む（キャラクター設定・スタイルガイド・過去のプロンプト）、ファイルは頼まれない限り書かない、
利用者の言語で話す、変更は `set_image_draft` で。圧縮で初回指示が薄れても、**D4 の末尾 1 行と
ツール説明文**が契約を毎ターン言い直す。

- モデル・effort・思考はセッションの物（起動ダイアログ・実行中の動的変更）。「高度な推論」は
  ここで選ぶ。log 103 の機能別指定には**乗せない**（単発生成でなくセッションのターン）。
- 台帳: セッションのターンとして今の記帳（kind・モデル・費用）。スタジオのセッションだと
  分かるように `Ref` にスタジオ id を足すのは P1。
- lcpp kind（ADR 0093）のセッションは CLI ログインの無い会員の答え（ADR 0081 未解決 3）。
  冷えた lcpp の 45 秒ホールド（`engine_waking`）は Managed のドライバが既に扱う範囲。

### D11 — 層 B（`PromptHelpModal`）は畳む。層 A（族カード・トリガー語チップ・錠付きネガティブ）は残す

「プロンプトを書いて」は会話の最初の発言そのもの。モーダルと `prompthelp.ts` は消す
（`askAssistant` の他の利用者＝メモ整理・TTS 要約は無関係）。層 A は D5 で出所が Agent に
移るだけで、画面の振る舞いは同じ。

### D12 — 生成の経路は 1 バイトも変えない

`POST /imagegen/jobs` の語彙・検証・ジョブ・試走・グループ・取消・EMA・サイドカー・
`props`・使用量の `Images/Pixels`・CP の中継 7 行は**そのまま**。ジョブに `studio`（id）と
`version` を持たせるのは D4 の「since last turn」と D7 の履歴のためで、`label` と同じ
「Console が付ける印」の 2 つ。

## 4. 画面

3 列。狭い幅（`@container paneview (max-width: 720px)`）ではタブ **会話 / 設定 / 結果** に畳む。

```
┌ 画像生成スタジオ「港の夕暮れ」▾ ─ [モデル ▾ ●warm] [すぐ描けます] [履歴] [ギャラリー] ┐
│ 会話  claude · opus · high  ⟳  │ 設定（下書き）           │ 結果                │
│ ─────────────────────────────  │ 族カード（折り畳み）      │ 試走枠  seed 8157…  │
│ 🧑 docs/chars/aoi.md を読んで   │ プロンプト        🔒     │ [この seed] [見せる] │
│    その子を港の夕暮れに         │ ┌ 1girl, blue hair, … ┐  │ ───────────────     │
│ 🤖 ▸ Read docs/chars/aoi.md    │ │ (変更を縁取り)       │  │ 12/40 ▓▓▓░░ 残 8分  │
│    ▸ set_image_draft           │ └───────────────────┘    │ [一時停止][飛ばす]  │
│    青髪と制服の指定を入れました │ ネガティブ        🔒     │ ───────────────     │
│    生成は押してください         │ steps 28 cfg 5🔒 …      │ [絵][絵][絵][絵]    │
│ 🧑 もっと暗く                  │ size 1216x832           │  seed / 21s / ⚠1    │
│                                │ LoRA ☑ add-detail 0.8   │  [戻す][参照][見せる]│
│ [状態を送りました v4・結果 2]  │ ▸ 上級（op・参照・mask・ │ ───────────────     │
│ [＋絵] [_________________] ⏎   │   batch・出力先・label）  │ 履歴 v4 v3 v2 …     │
│                                │ seed [random▾] N [40]    │  （並べる: 最大 4）   │
│                                │ [試走 ⌃⏎] [40 枚を投入]  │                     │
└────────────────────────────────┴─────────────────────────┴─────────────────────┘
```

- **左＝`MirrorView` をそのまま埋める**（`session` を渡す）。思考・ツールカード（Read した
  資料・`set_image_draft` の呼び出し）・添付・停止・resume を書き直さないため。頭に kind・
  モデル・effort のチップと「エージェントを替える」（新しいセッションを結び直す）。
- **中＝今の `GenerateForm` を「下書きの編集面」として再利用**（`Knobbed`・族カード・
  トリガー語チップ・`InputPicker`・スライダー）。足すのは欄ごとの 🔒 と、変更ハイライト。
  **モデル選択はペイン頭に上げる**——スタジオの「主語」で、D3 の「人だけ」を位置で言う。
- **右＝試走枠・グループ行・結果カード（今の部品）＋履歴**。履歴は下に続く一覧で、
  「並べる」でピン留め 4 枚を横並び・版の差分を脇に。
- マスク描画（log 111）は中列の「上級 → 参照画像」の隣（111 §3 の P2a と同じ場所）。
- スマホ（ミラー）は対象外のまま。タブ畳みで 1 列にはなる。

## 5. データと配線

### 5.1 Agent

スタジオのストア `internal/imagegen/studio.go`（`fstore` の read-modify-write を使う・
[fstore-no-read-modify-write]）:

```go
type Studio struct {
    ID, Title, Session string
    Draft   ImageDraft   // assistant-writable subset of jobRequest + model (D3)
    Locks   []string
    Versions []ImageVersion // capped 200
    LastSentModel string     // D4: the one ledger the preamble keeps
    CreatedAt, UpdatedAt int64
}
```

`ImageDraft` の型と検証は `jobs_http.go` の `spec()` から**切り出して共有**（二重実装にしない）。

Agent の口（全部 CP の中継リストに 1 行ずつ）:

| 口 | 用途 |
|---|---|
| `GET /imagegen/studios` / `POST` / `DELETE /imagegen/studios/{id}` | 一覧・作成・削除 |
| `GET/PUT /imagegen/studios/{id}` | 読み・手編集（`{draft, locks, title}`・検証は投入と同じ） |
| `POST /imagegen/studios/{id}/bind {session}` | セッションを結ぶ／外す（メタの `Studio` も書く） |
| `POST /imagegen/studios/{id}/versions` | 生成ボタンが押された写し（応答に `version`） |
| `GET /imagegen/history` | D7 |

MCP（`mcp_stdio.go`・セッション側・スタジオに結ばれたときだけ広告）: `get_image_studio`・
`set_image_draft`。中身は上の口を `agentGET/agentDo` で叩くだけ（`set_chat_plan` と同じ形）。

`sendManagedPrompt` の手前に `injectStudioState(m, prompt)`（D4）。

### 5.2 ターンの流れ

1. 人が発言 → Console `POST /sessions/{name}/turn {op:"start", prompt, attachments}`（今と同じ）。
2. Agent がメタの `Studio` を見て状態ブロックを前置（D4）。
3. エージェントが資料を読み、`set_image_draft` を呼ぶ → Agent が検証・錠・適用・
   `UpdatedAt` 更新 → ツール結果に適用後の下書きと落ちた欄。
4. ペインは 2 秒ポーリング（ジョブ一覧と同じ周期・`ETag` 304）で `GET /imagegen/studios/{id}`
   を読み、`UpdatedAt` が進んでいればフォームを更新・ハイライト。
5. 人がフォームを直す → `PUT`（デバウンス 500 ms・`If-Match` で版競合を検出）。
6. 人が生成 → `POST …/versions`（写し）→ `POST /imagegen/jobs`（`studio`・`version` 付き）。
7. 結果は今の 2 秒ポーリング。次のターンの前置「since last turn」に載る。

### 5.3 費用

- 会話 1 ターン＝そのセッションの kind・モデル 1 ターン（今の記帳）。D4 の前置 400〜700
  トークンと、資料を読んだ分。絵の添付は押したときだけ（D6）。
- 生成＝今まで通り GPU 時間（`tool.imagegen`・Images/Pixels）。
- セッション枠 1 つ。使い終わったら**停止**でなく**削除／アーカイブ**しないと枠は戻らない——
  ペインの「エージェントを外す」はアーカイブまで行う（下書きはスタジオに残る・D1）。

## 6. 段階

- **P0（芯）**: D1 / D2 / D3 / D4 / D9 / D10 / D12 と、画面の 3 列（MirrorView 埋め込み・
  起動ダイアログの部分集合・錠・ハイライト・状態チップ）。履歴は**版の一覧**だけ。
  D5（族表の移設）は P0 に入れる——入れないと `get_image_studio` の「model facts」を
  Console の表から写すことになる。D11（層 B の撤去）も P0。
- **P1**: D7 の絵の履歴（`history.jsonl`・`GET /imagegen/history`・戻す／参照／並べる）、
  D6 の「見せる」、モデル提案カード（D3）、D8 のレール導線・杖アイコン、台帳の `Ref`、
  スマホのタブ畳み、**下書きをリポジトリのファイルに保存／読込**（`*.imagedraft.json`＝
  プロンプト集を git で持つ導線。生きている下書きをリポジトリに置かないのは §7）。
- **P2**: 会話からのスイープ（「cfg を 3 通り」→ エージェントが**行列の提案**を書き、人が
  「投入」）、エージェントに試走を許す任意設定（§8-2）、マスク描画（111）との接続、
  TUI 実行方式の対応（前置無し・pull だけ）。

## 7. 却下した案

- **アシスタントチャット（log 19）を会話の器にする（初稿）。** 器として軽い（即時・枠を
  食わない・sonnet で安い）が、**cwd にリポジトリが無く、Bash / Edit / Agent を禁じられ、
  モデルは会話の既定で effort の口が無い**（§2.3）。2 巡目の 2 要件はどちらも満たせない。
  「資料を見ながら」に `--add-dir` で知識ディレクトリを渡す案は、リポジトリ 1 本を丸ごと
  読ませることになり、cwd を持つセッションの劣化版でしかない。
- **返事末尾の fenced block を転写から拾う契約（初稿の D2）。** チャットなら本文は文字列
  1 本で Agent が切り出せたが、セッションの転写は kind ごとに 9 通りのパーサ。ツール呼び出しは
  全 kind が同じ口で、しかもターン途中に効く（D2）。
- **エージェントに `generate_image` を持たせ、会話の中で生成させる**（「画像生成 GPT」型）。
  要求の「生成は人がボタンで」に反する。40 枚の量産で 40 ターン払う——ADR 0081 の背景が
  そのまま当たる。広告しないことで**構造的に**できなくする（D2）。
- **スタジオ＝セッション名（別 id を持たない）。** エージェントを替えた瞬間・枠を空けるために
  消した瞬間に下書きと版が消える（D1）。
- **生きている下書きをリポジトリのファイルにし、エージェントは Edit で直す。** 版管理も
  プロンプト集も git に乗る魅力はあるが、500 ms デバウンスの書き込みで作業コピーが常に汚れ、
  `session-changed-files` の追跡が下書きの往復で埋まり、検証と錠がファイルの読み側に回って
  「投入時に 400 にならない値だけ」（D2）を守れない。**保存／読込は P1 の明示操作**に留める。
- **TUI 実行方式も対象にする。** 前置が打鍵として画面に出る。Managed 専用（D9）。
- **下書きを `localStorage` に置いたまま、ペインがツール結果を拾う。** ポップアウト・スマホ・
  再読み込みで真実が割れる（D1）。
- **エージェントに `model` を書かせる**（D3）。族の切替と冷えたエンジンの起床を、人が
  気付かないまま起こす。
- **エージェントの編集ごとに版を切る**（D7）。比べたいのは「押した」単位。
- **履歴を Agent のジョブ一覧の拡大で作る。** メモリ上で再起動に消える物を伸ばしても
  「あの日の設定」に届かない。
- **スタジオ専用のセッション一覧**（D8）。一覧が 2 つになる。
- **生成結果を毎ターン自動で添付**（D6）。見えないプラン消費。
- **ペインを MirrorView と別に書き直す。** 思考・ツールカード・添付・resume を失う。

## 8. 利用者への確認

1. **D3 の線引き**——`model` を人だけにする（提案カード止まり）で良いか。
2. **D2 の `generate_image` 不広告**——エージェントは一切生成できない、で良いか。
   「試走 1 枚だけ許す」をスタジオごとの任意設定で開ける案は P2 に置いた。
3. **D9 の既定**——Managed 専用・cwd はリポジトリ・worktree は既定で作らない、で良いか
   （エージェントにプロンプト集をリポジトリへ書かせたい運用なら worktree 既定に倒す）。
4. **D1**——スタジオ id をセッションと別に持つ（エージェントを替えても下書きが残る）で良いか。
5. **D7 の履歴の単位**——「押した 1 回＝版」で良いか。
6. **D11**——「プロンプトを書いて」モーダルを消して会話に統合して良いか。
7. **D5**——族の方言・品質接頭辞を Console i18n から Agent の族表へ移して良いか
   （訳語は i18n に残る。方言の文そのものは英語 1 本になる）。

## 9. 踏みどころ（実装前に知っておく罠）

- **前置ブロックは転写に載る。** ミラーが剥がすのは描画だけで、転写・圧縮・要約には残る。
  ブロックの語彙は毎回同じ見出し `[image studio state]` にし、剥がす側は見出しから空行までを
  1 塊として落とす（`splitPastedImages` と同じ位置・同じ試験の形）。
- **ツールの広告集合は接続時のスナップショット**（[imagegen-model-enum-snapshot]）。
  「エージェントを付ける」の順は **スタジオ作成 → メタに `Studio` → セッション起動**。逆だと
  最初の接続で 2 本のツールが無い。結び直しはセッションの再起動（resume）を伴う。
- **muse は af サーバに届かない**（環境を洗う）——直るまで kind 一覧で無効（D2）。
- **`If-Match` 無しの PUT は競合を黙って潰す。** 人のデバウンス書き込みとエージェントの
  `set_image_draft` が同じ 500 ms に重なる。`UpdatedAt` を版として `If-Match` に載せ、
  負けた側はフォームを読み直す（[fstore-no-read-modify-write]）。
- **錠と検証の順**: 検証（400 相当）→ 錠の除外 → 適用。結果の理由は「固定のため」と
  「不正な値」を分ける。
- **`sameTarget` をスタジオ id にすると `null` のペインが 2 枚開ける。** null 同士は同じ的
  （D8）。
- **枠は停止で戻らない。** 「エージェントを外す」はアーカイブまで（§5.3）。俯瞰図（ADR 0096）
  にスタジオのレーンが並ぶのは正しい振る舞いで、隠さない。
- **アイドル停止**（ADR 0055）: 生成中は machineBusy でない（ジョブは Agent の物）。
  40 枚を眺めている 20 分でセッションが止まるのは仕様で、次の発言で resume する。
  止まっている間の `set_image_draft` は無いので下書きは動かない——ペインは「エージェントは
  停止中（発言で起きます）」を出す。
- **`engineCatalogModelRow` は運ばない欄を黙って落とす**（[cp-session-wire-relay-drops-fields]）。
  D5 は Agent の族表（CP を通らない）に置くので当たらない。
- **モジュールスコープのキャッシュは dom テスト間で漏れる**（[module-scope-cache-leaks-across-dom-tests]）
  ——`available.ts` をスタジオ id 付きで拡張するなら鍵に id を入れる。
- **純関数の試験は `api.ts` を import できない**（ADR 0081 レーン C）——`wire.ts` に型、
  `api.ts` に fetch の分離を守る。
- 撮影スタブ（`console/scripts/shots/server.mjs`）に `imagegen/studios*` と `imagegen/history`
  を足さないと、ガイドの絵が壊れ画像になる。
- **MCP ツール名は文字列リテラルで**（[imagegen-providers-adr0069]・AST 走査の試験は
  `*ast.BasicLit` しか拾わない）。
