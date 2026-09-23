# 0100. 画像生成スタジオ——セッションのエージェントが下書きを直し、人がボタンで生成する

[English](0100-image-generation-studio.md) | 日本語

- 状態: **proposed**（2026-09-23 起草）。根拠と経緯は [docs/log/113](../log/113-imagegen-studio.md)
  （5 巡の設計と、別セッションによるレビュー [113-review](../log/113-review.md)・🔴 7・🟡 12 を反映済み）。
  実装は無い。
- **改訂 1（2026-09-23）**: 別セッション `semvs2b`（codex / gpt-6-sol）の ADR レビュー
  [113-adr-review](../log/113-adr-review.md)（🔴 9・🟡 12・🔵 1）を反映。決定 3・4・8・9・12 の契約を
  補い、P0 に絵の履歴の最小形を入れ、前提作業を組み替えた。何をどう変えたかは末尾「改訂 1 で
  変えたこと」。
- **改訂 2（2026-09-23）**: 同じレビュー役の再レビュー（[113-adr-review](../log/113-adr-review.md) §5・
  新規 🔴 7・🟡 3）を反映。結び替え直後の呼び出し・番人の適用範囲・試走ツールの待ち・`needs_mask`・
  press の書き順・スタジオ作成の順序・kind 一覧の出所を直した。末尾の対応表に追記。
- **改訂 3（2026-09-23）**: 3 巡目（[113-adr-review](../log/113-adr-review.md) §6・新規 🔴 4・🟡 5）を
  反映。参照画像は**投入時に固定コピー**へ写して provider に原本のパスを渡さない（CLI 子プロセスも
  含む）、試走ツールに heartbeat、版 id は投入前に予約、生成物 root を参照の許可 root に、
  決定 1 の例外は 2 件。末尾の対応表に追記。
- **改訂 4（2026-09-23）**: 4 巡目（[113-adr-review](../log/113-adr-review.md) §7・新規 🔴 4・🟡 3）を
  反映。固定コピーは**ジョブ id でなく入力セット id**で持ち `Request` に原本の記録欄を足す、press は
  **投入前の全文行＋投入後の結果行**の 2 行、初回ターンの配達状態はメタの欄でペインが読む、TUI 候補は
  `terminalDriver !== false`。末尾の対応表に追記。
- **改訂 5（2026-09-23）**: 5 巡目（[113-adr-review](../log/113-adr-review.md) §8・新規 🔴 3・🟡 3）を
  反映。原本の記録欄は `Request` でなく**キューの記録（`jobRec`）**に置く、決定 9 の `press` 行の欄一覧を
  実行順に揃える、`InitialPromptState` に `pending` を足して送信処理中の再送を禁じる。末尾の対応表に追記。
- 番号: `develop` の最大は 0098。0099 は未マージの 2 ブランチ（`temp/sidv2bw`・`temp/sjys6nk`）が
  取っているので 0100。
- 関連: [0081](0081-image-generation-pane.ja.md)（今の画像生成ペイン。本 ADR は決定 6・7 を覆し、
  1〜5・8〜12 を継ぐ）/ [0069](0069-image-generation-providers.ja.md)（決定 8「押していないのに
  モデルを呼ばない」は本 ADR でも守る）/ [0015](0015-agent-managed-driver.ja.md)（Managed 実行方式。
  claude と agy には無い）/ [0022](0022-agent-memory-management.ja.md)（メモリの git スナップショット。
  決定 12 の将来形）/ [0094](0094-instruction-edit-image-models.ja.md)（指示編集。改訂でクロップを
  やめた＝決定 11 の前提）/ [log 111](../log/111-inpaint-mask-canvas-p2.md) §10（マスクのキャンバス）/
  [log 33](../log/33-chat-context-usage.md) 第 5 段（会話の脇に「計画」を原文で持つ＝下書きの原型）

## 背景

ADR 0081 のペインは「LLM を挟まずに」絵を量産する。プロンプト支援は決定 7 の層 B＝ボタン 1 回の
単発問い合わせで、会話は残らず、2 回目は 1 回目を知らない。利用者の要望はその先にある——
**アシスタントと会話しながらプロンプトを直し、会話すると下書きが更新され、生成は人がボタンで押し、
結果を見ながら微調整し、履歴を残す**。2 巡目で「器はアシスタントチャットでなくセッション」が
決まった: **高度な推論**（kind・モデル・effort をその場で選ぶ）と、**リポジトリ内の資料を見ながら
プロンプトを組む**（cwd がリポジトリ）はセッションにしか無い。アシスタントチャットは `chat-wd`
（リポジトリでない）で動き、Bash も Edit も禁じられ、モデルは会話の既定で effort の口が無い
（`chat_providers.go:2210, 2085`）。

設計を決めた事実（2026-09-23・`e55feb37` 以降の木で裏取り。誤っていた主張はレビューで訂正済み）:

- Agent の要求語彙（`jobs_http.go:28-59`）は provider / op / prompt / negativePrompt / size /
  aspectRatio / background / count / inputs / mask / model / loras / seed / strength / params / label /
  out_dir / jobs / seed_policy / trial / full_steps で足りる。**本 ADR は語彙を足さない。** ただし
  `inputs`/`mask` に**パスの番人が無い**（`spec()` が素通しし `comfy.go:1193` が `os.ReadFile` で
  そのまま上げる）。Files ペインの読み口は symlink を辿らない FD ベースの `openat2NoSymlinks`
  （`fs_fd_linux.go:192`）を使っている——番人の手本はそこにある。
- 下書きは `localStorage`（`draft.ts:106`）にあり、サーバは知らない。履歴は Agent メモリ上のジョブ一覧
  （完了 500 件・再起動で消える）とサイドカーだけ。
- ファミリーの知識は 2 か所に割れている: 読む摘み `knobs` は Agent（`comfy.go:329-358`）、方言・
  品質接頭辞は Console の i18n（`families.ts:42-177`）。
- **Managed ドライバは 7 kind**（`session_turn.go:33-41`）。**claude と agy には無い**。
- **Managed のプロンプトの口は 1 つではない**（`/turn` は `h.Send` 直・bridge・`/input`・`initial_prompt`）
  ＝Agent が毎ターン前置する場所は無い。
- **セッション側 af MCP サーバが `AF_SESSION_NAME` で自分を知るのは Terminal 全 kind・codex Managed
  新規・lcpp だけ**。opencode／copilot／cursor／kiro Managed と muse は cwd で推測する
  （`mcp_stdio.go:3309-3359`）。opencode の af 子はディレクトリ単位で複数セッションが共有する。
- tools/list は**要求ごとに組み直され**、`list_changed` を 1 分ごとに送る（`mcp_stdio.go:286-298, 480-545`）。
- チャットのアシスタントは `generate_image` を呼べない（チャット側の af サーバは `--self-report` を
  持たず `mcpImageGenEnabled` が偽・`mcp_stdio.go:166-170`）。セッション側では ui-prefs の
  「画像生成」が `--image-gen` で入り、`mcpImageGenAdvertise()`（`:1470`）が所有セッションを解決して
  広告する。
- Managed のターン `POST /sessions/{name}/turn` の `attachments` を読むのは opencode・codex・muse だけ。
  copilot／cursor／kiro／lcpp は捨てる。
- `~/.config/agent-fleet` は Files ペインの拒否リスト（`fs.go:126`）にあり、ワークスペース方針も
  エージェントに触るなと言う。
- 指示編集の中央クロップは廃止済み（ADR 0094 改訂 #907・実装 #913・実機受け入れは
  [log 112 §14](../log/112-kontext-crop-necessity.md)＝#914）。マスクのキャンバスは log 111 §10 の
  設計で着工できる。マスクを**パスで渡す**欄は今のペインに在る（PR #854・`GenerateForm.tsx:468-476`・
  空なら押せない判定は `:94-100`）。

## 決定

### 決定 1 — LLM は下書きを直すときだけ。生成の経路は変えない

「LLM を挟まずに」（0081）は生成については変わらない。エージェントが触るのは**下書き**で、
生成ボタンを押すのは人、押したときに走るのは 0081 のジョブキューそのもの。`POST /imagegen/jobs` の
語彙・検証・ジョブ・試走・グループ・取消・EMA・サイドカー・`props`・使用量・CP の中継 7 行は
そのまま。**例外は 2 件**: 決定 4 の番人（`inputs`/`mask` を投入時に検査して固定コピーへ写す——
キューの記録 `jobRec` に原本の記録欄が増え、ジョブを組む順序に「投入前にコピー」が入る）と、決定 9 の
`POST …/press`（同じキュー関数を呼ぶ入口が 1 つ増える）。ワイヤの語彙も、provider の引数型
`Request` も変わらない。

### 決定 2 — 「スタジオ」を Agent に置く。セッションとは別の id で、セッションを 1 本結ぶ

Agent に**スタジオ**（`~/.config/agent-fleet/imagegen/studios/<id>.json`）を置く: `id / title /
draft（provider と model を含む）/ locks / session / agent_trial / mask_strokes / created_at /
updated_at`。**下書きの真実はスタジオ**。`localStorage` は「最後に開いたスタジオ id」だけを覚える。
版と編集履歴は別ファイル（決定 9）。

- セッションと別 id にする理由: エージェントを替える（sonnet→opus・claude→codex）、古くて resume
  できない、一覧を整理するために消す——どの場合も下書きと版は残す。スタジオが主で、セッションは
  「いま結ばれている相手」。
- セッションのメタに逆参照 `Studio`（`Origin` の隣）。**結びの真実はスタジオの `session`**で、メタは
  広告のための写し——2 つは同時には書けないので、ツールの**呼び出し時にも**「呼んだセッションが
  いまの結び相手か」をスタジオ側で照合し、違えば断る。**fork は引き継がない**（1 スタジオ 1 セッション）。
  作成・fork・recreate の `Meta{…}` リテラル 3 か所、Agent の `wireSession`、**CP の `sessionWire`**、
  停止中の DB ミラーに欄を通す（無い欄は黙って落ちる）。
- 本体は小さく保ち、メモリ上の錠（チャットの `LockConv` と同型）＋tmp→rename で書く（`fstore` に
  錠も原子性も無い）。編集履歴（決定 9）は別ファイル。
- セッションが無いうちは今の `localStorage` 下書きで動く。**スタジオは「エージェントを付ける」を
  押した瞬間に作る**（改訂 2）——順序は ① スタジオ作成（`localStorage` の下書きを移す。`provider` は
  画面が解決した ready な行の id、`model` は空でも良い）→ ② `GET …/persona` → ③ セッション作成要求に
  `studio` を渡し、Agent は**起動より前に**メタへ `Studio` を書き、スタジオの `session` を結んでから
  `initial_prompt` を送る。③ が失敗したらスタジオは結び無しで残る（下書きは失わない）。
  初回ターン（人格）の配達は**メタの欄 `InitialPromptState`**（`pending` / `delivered` / `failed` /
  `unknown`）でペインが読む（改訂 4・5）: 作成時に `pending` を書き、配達側が終わりに残り 3 つの
  どれかを書く。Managed は作成処理の中で `h.Send` するので同期に確定する。TUI は応答と独立した
  goroutine で配達し（`session_handlers.go:1050`・`session_io.go:848-920`）、pane を最大 30 秒・
  composer を最大 30 秒待ってから打ち、確認を 12 秒×2 回まで試す（`session_delivery.go:41-47, 108-141`）
  ——**この処理が終わるまでは `pending` のまま**で、ペインは「人格を送っています」を出し**再送を
  出さない**（時間で `unknown` に倒すと、元の goroutine が後から送って人格ターンが二重になる）。
  `failed` と `unknown`（処理が終わったが確認できなかった）で初めて「人格を送れませんでした／
  確認できません・再送」を出す（再送＝初回ターンとして送り直す）。
  **ADR 0081 の利用者は何も失わない。**

### 決定 3 — 契約はセッション側 af MCP サーバのツール 4 本。`generate_image` は広告しない

| ツール | 何をするか |
|---|---|
| `get_image_studio` | 下書き・錠・**since last call**（人の編集・巻き戻し・新しい結果を最大 5 件＋「他 N 件」）・モデルの事実（ファミリー・読める摘み・サイズ・既定値・LoRA とトリガー語）・知識の「要約」節・版の要約。上限 8 KB |
| `set_image_draft` | **部分更新**。書いた欄だけ変え、`null` で消す。**保存時の検証は欄ごと**（型・許可表・上限・錠・パスの番人）で、**未完成の下書きを許す**（prompt が空でも保存できる）。完成した要求の検証（`spec()` の `bad_prompt` 等）は投入と試走のときに掛かる。錠の欄は落として理由を返す |
| `run_image_trial` | **引数を持たない**。走るのはスタジオに保存された下書き（`provider`・`model` を含む＝画面が選んだ行そのもの。Agent の既定 provider や warm モデルへ**落とさない**）。`model` が無い（「モデルを選んでください」）・`op=inpaint` で `mask` が空・`spec()` を通らないときは理由付きで断る。1 枚・キュー先頭・ファミリーの試走 steps・待ち 3 枚まで。**待つのは最長 120 秒**（温かいエンジンは 8〜21 秒で返る。codex の 600 秒より短い）で、`generate_image` と同じ **10 秒ごとの progress heartbeat** を送る（opencode は通知が無いと 60 秒で切る・`mcp_imagegen.go:222-228`）。間に合えばパス・seed・警告・所要時間、間に合わなければジョブ id と「結果は `get_image_studio` の since に出る」を返す。ジョブと版は残って試走枠に出る |
| `add_image_knowledge` | 決定 12 の「記録」節へ追記（scope・key・note・evidence） |

- 広告は**そのセッションがスタジオに結ばれているときだけ**（`mcpOwningSession()` のメタに `Studio`）。
  tools/list は要求ごと・`list_changed` 1 分なので、結び直しは通知を尊重する kind なら 1 分以内に効く
  （尊重の有無は kind ごとに未測定＝受け入れで測る）。判定は status と切り離し、メタの読みだけで
  決める（Agent が遅いときにツールが点滅しない）。
- **`generate_image` はスタジオのセッションに広告しない**——`mcpImageGenAdvertise()` が所有セッションの
  メタに `Studio` を見たら除外する。ただし呼び出し側の照合は**最後に覚えた tools/list**を見る
  （`mcp_stdio.go:547-565`）ので、結び替えから次の tools/list までは古い集合に残る。**境界は
  呼び出し時の再検査**——`generate_image` の実行前にも所有セッションのメタを読み直し、`Studio` が
  あれば理由付きで断る（広告からの除外は利用者への見え方、再検査が保証）。指紋は MCP 子が
  tools/list から計算する（`mcp_stdio.go:445-476`）ので Agent から進める口は無く、広告の更新は
  1 分の watcher に揃える。**保証の範囲は af の経路**（スタジオのジョブキューへの N 枚投入と
  `generate_image`）で、CLI 自身の組み込み画像ツール（codex の `image_gen` 等・ADR 0069）は
  広告集合の外＝本 ADR は制限しない。**N 枚の投入はツールに無い。** 試走だけをエージェントに許すのは、
  「`generate_image` の不満はプロンプトが見えないこと」への答えで、引数を持たないツールなら
  走るものは常に画面に見えている。スタジオごとに「エージェントの試走を許す」（既定 ON）。
- 識別の 3 状態: 所有セッションが**確定してスタジオに結ばれている**→広告する／**確定して結ばれて
  いない**→広告しない／**確定できない**（cwd の推測が曖昧・決定 8）→スタジオのツールは広告し、
  **呼ばれたら理由付きで断る**（消えるとエージェントは「そんなツールは無い」と言うだけ）。
- MCP にして転写の fenced block を退けた理由: セッションは全 kind が af MCP で話し、転写の解析は kind
  ごとに 9 通り。ツールはターン途中の実時間で効く。説明文の固定費はスタジオのセッションだけが払う。

### 決定 4 — エージェントが書ける欄と、人しか書けない欄

| エージェント | 人だけ |
|---|---|
| `prompt` `negative` `params{steps,cfg,sampler,scheduler}` `size` `loras` `strength` `op` `inputs`（番人が入ってから） | `model` `seed`/`seed_policy` `jobs` `count` `out_dir` `label` `mask` |

- `model` を人側に置く: 切替はファミリーの切替＝読める摘み・サイズ・ネガティブの可否が全部変わり、
  冷えたエンジンなら 1 枚目に数分。エージェントは `suggest_model` を書け、ペインは提案カードで出す。
- `op`・`inputs` をエージェント側に置く: 「この絵の看板の文字を CLOSED にして」は op・参照・指示文の
  1 手。**前提は投入側の番人**（改訂 3 で形を変えた）——`spec()` の文字列検査だけでは検査後に
  symlink を差し替えられる（TOCTOU）。しかも provider は自分で読むとは限らない: codex は参照パスを
  `-i` で**別プロセス**に渡し（`codex.go:173-180`）、agy はプロンプトの文字列で渡す（`agy.go:449-458`）
  ので、Agent 側の open をどう固めても子が後で原本を開く。したがって**投入の前に**（改訂 4:
  ジョブ id は `Enqueue` の中で採番される `jobs.go:297-327` ので、ジョブ単位にはできない）、要求の
  `inputs`／`mask` を root 固定の open（`openat2NoSymlinks` と同型）で読み、**入力セット**
  `~/.cache/agent-fleet/generated/console/inputs/<set>/`（set id は Agent が採番・生成物 root の絶対
  パス・利用者のアップロード先と親は同じだが別階層）に写す。**`Request`（provider の引数型・
  `imagegen.go:53-85`）にはコピーのパスだけ**（`Inputs`・`Mask`）を入れ、**原本のパスはキューの
  記録 `jobRec` の記録専用欄（`InputOrigins`・`MaskOrigin`）**に置く（改訂 5: `JobSpec` は `Request` を
  丸ごと持ち worker が同じ値を `prov.Generate` に渡す `jobs.go:214-229, 318-326, 426-437` ので、
  `Request` に原本欄を足すと provider にも渡る）。サイドカーは `jobRec` の原本欄から書く（今は
  `Request` の同じ値を両方に使っている `jobs.go:477-511`）。provider は原本のパスを一度も見ない
  （comfy の事前読取り `comfy.go:1045`・アップロード `:1193`・`openai_compat.go:404`・codex・agy の
  どれも）。読める元は **browse root と生成物 root** の 2 つ（既定の出力先は browse root の外
  ＝`store.go:28-34`・`AF_BROWSE_ROOT` が home でない配備で「参照にする」が自分の絵を拒まないため）、
  拒否リストは Files ペインと共有。掃除: 投入が失敗したら同期に消す、グループの最後のジョブが
  終わったら消す（**待機中の個別取消・グループ取消は `q.finish` を通らない** `jobs.go:701-726, 781-800`
  ので、その 2 経路にも終了判定を付ける）、起動時は `inputs/` の全セットを消す（キューはメモリなので
  生き残りは無い・今の掃除は `trial/` しか見ない `store.go:170-195` ため足す）。番人が入るまで解放しない。
- `mask` は人だけ（塗るのは人の手）。**`needs_mask` は保存する旗ではなく導出値**（`op=inpaint` かつ
  `mask` が空）で、`get_image_studio` が読みだけで返す——人がマスクを置けば消える（改訂 2）。
  エージェントは `op=inpaint` を書くだけで良く、ペインが導線を出す。P0 の導線は**既存のマスクの
  パス欄**（PR #854＝人が既にあるマスク画像を指定する）、キャンバスは P1（決定 11）。したがって
  P0 でも `op=inpaint` は書けるが、走るのは人がマスクを置いてから（人のボタンは今どおり `mask` の
  空だけで止まる）。
- **欄ごとの錠**（🔒）。錠の欄への書き込みは落として理由を返す。自動の錠は作らない。

### 決定 5 — 文脈はエージェントが引く（pull）。Console は末尾に合図 1 行。前置はしない

人格は「発言を受けたらまず `get_image_studio` を呼ぶ」。**人格の出所は Agent**（`GET
/imagegen/studios/{id}/persona`・利用者の言語で組む）で、Console は「エージェントを付ける」の
作成要求に `initial_prompt` としてそのまま渡す（順序は決定 2＝スタジオを先に作り、メタに `Studio` を
書いてから初回ターン。逆だと最初のターンで `get_image_studio` が広告されない）。結び替えでは新しい
セッションの最初のターンとして送り、resume では送らない。Console は発言の**末尾**に
`[studio v4 · 下書きが変わった · 新しい結果 2 · 巻き戻し #9 → get_image_studio]` の 1 行（30 トークン）
を添える。合図の有無で呼ぶ／呼ばないを分けない（Terminal ペインから打った発言に合図は無い）。

- 前置しない理由: 入れる 1 か所が無く（`/turn` は `h.Send` 直）、剥がす 1 か所も無く
  （`splitPastedImages` は末尾の添付指示文専用・自動タイトルは先頭 400 字を読む）、転写に残って
  毎ターン再送される（lcpp は 10 ターンで窓の大半）。
- 合図が付くのはミラーの composer から送る発言だけ（メモの流し込み・peer・予約実行には付かない）。
  TUI では添付のパスを織り込む `buildImagePrompt` の**後**に付けて末尾を保つ。
- 末尾にする理由: 自動タイトルに効かない。剥がし手は転写モデル層（`mirror/transcript/model.ts`）と
  Go 側（返信候補・ブランチ名）の 2 か所、見出しは定数 1 つ。`<` で始めない（`isNoise`）。
- 帳簿「最後に読んだ位置」は（スタジオ, セッション）の組（スタジオに付けると結び替えた新しい
  エージェントが取りこぼす）。
- lcpp は毎ターン system prompt を組み直すので、lcpp に限り人格と「要約」節をそこへ置ける（P1）。

### 決定 6 — 「見る」は 2 段。人は常に、エージェントは押したときだけ

人は結果カードと試走枠を常に見る。エージェントの `set_image_draft` で動いた欄は次に人が触るまで
縁取る。**「この絵をエージェントに見せる」**を押したときだけ絵を渡す（ADR 0069 決定 8）。渡し方は
kind の能力で決める: Managed で一級の添付を読むのは opencode・codex・muse、それ以外と TUI は本文に
パスを織り込む今の形。vision の無い kind／モデルでは理由付きで無効。判定は `agent.caps.imagePaste`
の隣に添付可否を 1 欄足し、能力表は `guideTable.test.ts` の突き合わせに載せる。

### 決定 7 — ファミリーの方言・品質接頭辞・推奨範囲は Agent のファミリー表に

`comfyFamilyRow` に `Dialect`・`QualityPrefixes`・`StepsRange`・`CFGRange` を足し、`GET /imagegen/status`
の `modelStatus` に出す。Console のファミリーカードはそれを描くだけ（i18n には訳語だけ残る）。
`get_image_studio` の「モデルの事実」も同じ行から組む。0081 決定 4「何を読むかはファミリーが決め、
言葉で言う」を読む摘み以外に広げる。ADR 0098 の qwen-image-2.1 は Agent 側と Console 側の
2 コミットで両方触った——その二重を畳む。層 B（`PromptHelpModal`・`prompthelp.ts`）は撤去する
（「プロンプトを書いて」は会話の最初の発言そのもの）。

### 決定 8 — セッションは要るときだけ。Managed と TUI を同じ P0 で。worktree は既定 ON

ペインを開いただけではセッションを作らない。「エージェントを付ける」は起動ダイアログの部品
（`ModelPicker`／`EffortPicker`／`SubdirPicker`／`BranchList`）で作る: kind・モデル・effort／思考・
実行方式・リポジトリ＝cwd・subdir・worktree（既定 ON）・permission skip。prompt 欄は無し。

- **claude は TUI しか無い。** 「高度な推論」を claude（Opus）で満たすには TUI が P0 に要る。
  決定 5 が pull なので Managed と TUI の差は添付の渡し方と起動・resume の手順だけ。kind 一覧は
  **実行方式ごとに出所が違う**（改訂 2）: Managed の候補は `managedDrivers`、TUI の候補は Console の
  `repoLaunchKinds`（`agents/registry.ts:760`）のうち **`terminalDriver !== false`** の kind から
  shell を除いた物（欄は省略＝可で、false を持つのは lcpp と muse だけ・`registry.ts:134-148, 566, 614`。
  真偽や欄の有無で絞ると claude と agy が落ちる）。1 つの表から引くと claude が落ちる。
- **worktree 既定 ON** は、af サーバが自分のセッションを cwd で推測する kind で推測を一意にする
  唯一の手段。OFF を選べるのは **kind × 実行方式のすべての経路で `AF_SESSION_NAME` が届く組**
  だけ: Terminal（全 kind）と lcpp。**codex Managed は不可**——新規スレッドには届くが、Agent の
  デーモンが差し替わった後の resume で cwd 推測へ落ちる（`mcp_stdio.go:3311-3340`）。起動 UI は
  「worktree では未コミットの資料は見えない」と明示する。
- **opencode の Managed はスタジオから外す**（Terminal は可）: af 子を複数セッションが共有し、別の
  セッションから `set_image_draft` が走り得る。copilot／cursor／kiro／muse は `AF_SESSION_NAME` を
  子へ届ける改修を P0 の前提作業にし、届くまで Terminal 限定。

### 決定 9 — 履歴は 3 つ。編集履歴・版・絵

- **編集履歴** `draft_log`: 下書きが変わるたび 1 件（時刻・書き手＝エージェントのターン／人／
  `rewind ← #n`・変わった欄と前後の値・その時点の全文）。別ファイルの追記専用 JSONL
  （`studios/<id>.log.jsonl`）。転写では `set_image_draft` のツールカードを「下書きを更新: cfg 7→5」の
  専用カードに描き、そこと中列の一覧から**「この時点に戻す」**（`POST …/rewind`・履歴は消さない・
  次の `get_image_studio` の since に出る）。巻き戻しは**人の操作**なので錠の欄も含めて全文を戻し、
  錠の旗はそのまま残す（錠はエージェントに対する物）。巻き戻し行の before/after は「いま」と
  「戻した先」。
- **版**: 生成ボタン（人の試走・投入・エージェントの試走）を押した瞬間の写し。同じ JSONL に
  **独立した追記イベント 2 行**として積む: `kind: "press"`（版 id・下書きの全文・seed 方針・書き手・
  押した時刻＝投入前に分かる物だけ）と `kind: "press_result"`（版 id・ジョブ／グループ id、失敗なら
  `error`＝投入後に分かる物）——編集せずに 2 回押せば press が 2 件。スタジオがあるときの押下は
  Console が `POST /imagegen/studios/{id}/press {trial|enqueue…}` を 1 回呼ぶ。Agent の順序（改訂 4）:
  ① **`press` 行を書く**（版 id・下書きの全文・seed 方針・書き手・押した時刻＝「この設定に戻す」に
  要る物は全部ここ）→ ② 固定コピー（決定 4）→ ③ `studio` と `version` を `JobSpec` に載せて今の
  キュー関数へ投入（worker は応答前に走り出す・`jobs.go:297-342`。サイドカーは `JobSpec` の
  `version` から書く）→ ④ **`press_result` 行を書く**（版 id・ジョブ／グループ id、失敗なら
  `error`）。④が失敗したら（投入は成功し worker は走っている）応答に `recorded: false` を返し、
  ペインはその版を「記録保留」で出し、Agent はすぐ 1 度書き直しを試みる。版の状態は `press_result`
  の有無と中身から導く。①の後に落ちたら `press_result` の無い版が残る——起動時に、サイドカーを走査して（`history.jsonl` の有無や末尾欠けに依らない）その
  `version` の絵があれば `press_result` を合成し、無ければ `lost` の `press_result` を書く。
  サイドカーは下書き全文を持たない（`props.go:42-90`・負の指示は合成後の値）ので、復元の元は
  常に①の行。`POST /imagegen/jobs` の語彙も検証もキューも変わらない（決定 1 の例外 2）。
  スタジオ無しの押下は今どおり `/imagegen/jobs`。「押した印」を編集行に付けることはしない。
- **絵の履歴**: `GET /imagegen/history?studio=&before=&limit=`。裏は `generated/console/history.jsonl`
  （サイドカーを書くときに 1 行追記・無ければ走査して再生成）。サイドカーとこの行は `studio` と
  `version`（press の id）を持つ＝**絵と版の対応は永続**で、Agent 再起動を跨ぐ。操作は「この設定に
  戻す」（同じ `rewind`）・「参照にする」・「見せる」・「並べる」（最大 4 枚）。掃除は 0081 決定 3 の
  まま。**一覧と「この設定に戻す」は P0**（利用者の「履歴を残す」は絵を含む）、「並べる」は P1。

### 決定 10 — スタジオは複数持てる。ペインはスタジオ id を持つ

`{ kind: "imagegen"; studioId: string | null }`。`sameTarget` はスタジオ id（`null` 同士は同じ的）。
結ばれたセッションはセッション一覧に杖のアイコン付きで並び、そこから開くと imagegen ペイン。
ミラーペインで同じセッションを開こうとしたら imagegen ペインへ寄せる（入力・添付の下書きと
送信エコーが後勝ちで潰し合うため）。スタジオの削除＝下書きと版の削除（絵は残る）。

### 決定 11 — inpaint はスタジオの中で完結する。マスクは人が塗り、指示はエージェントが書く

入口は結果カードと履歴の「この部分を直す」（`op=inpaint`・`inputs[0]`・キャンバス）。キャンバスは
**log 111 §10 の設計**（帯も比率表も無い・書き出しは原寸で上限超だけ縮小・筆は長辺比・EXIF 実測済み）
を中列に置く。ストロークはスタジオの `mask_strokes` に残す。マスク無しの inpaint は投入と同じく拒む。

### 決定 12 — モデル・ファミリーごとの知識を貯める。1 モデル 1 文書。置き場は見えるファイル

4 層: 0 ファミリーの事実（Agent の表・決定 7）／1 テナントの注記（カタログ行 `prompt_notes`・0081
未解決 2・P1）／**2 ワークスペースの知識（本決定）**／3 スタジオの会話。

- 層 2 は **`~/imagegen-knowledge/{families,models}/<key>.md`**（home 直下に固定・拒否リスト外・
  recreate は `~/repos` しか消さない）。**存続を可視性より優先する**（改訂 2）——browse root から
  導くと `AF_BROWSE_ROOT` が `~/repos` の下を指す配備で知識ごと消える。browse root が home でない
  配備では Files ペインに出ない——そのときは**ペインの「メモ」が 4 節すべてを編集する面**になり、
  エージェントは Read／Edit で読み書きする（Files ペインでの直接編集だけを失う。以下の「人の
  Files ペイン」は browse root が home のときの案内）。1 モデル 1 本・1 ファミリー 1 本、**4 節**（要約 1 KB・設定・
  プロンプト・記録＝追記のみ）。「記録」は `add_image_knowledge`、他の節はエージェントの Edit と人の
  Files ペイン。**消すのは人**。エージェントが書くのは「覚えて」と言われたときと、結果に良し悪しを
  言ったときだけ。読みは `get_image_studio` が「要約」を毎回、他は求められたとき。
- リポジトリへは「共有したいときに明示で書き出す」（スタジオごとの書き先切替は他のセッションの
  `git status` を汚すので却下）。
- **将来は ADR 0022 の git スナップショットの roots に加える**（利用者の要望「claude のメモリの
  ように git で差分管理」）。差分・復元・書き出し・取り込みの**仕組み**は流用できるが、0022 の
  glob の許可表・symlink 不追跡・取り込みの範囲検査・全履歴の secret 走査に**この root を
  宣言する設計**（`families/*.md`・`models/*.md` だけを取る・復元範囲・UI の名前）が要る＝
  P2 の 0022 改訂として別に起票する。

## 却下した案

- **アシスタントチャットを器にする。** リポジトリも Bash も Edit も effort の口も無い（背景）。
- **返事末尾の fenced block を転写から拾う。** kind ごとに 9 通りのパーサ・ターン末尾まで待つ。
- **Agent が Managed のターンに状態ブロックを前置する。** 入れる 1 か所も剥がす 1 か所も無く、転写に
  残って累積する（決定 5）。
- **エージェントに `generate_image` を持たせる。** 生成は人のボタン。広告しないことで構造的に守る。
- **スタジオ＝セッション名。** エージェントを替えた瞬間に下書きが消える。
- **生きている下書きをリポジトリのファイルにする。** 作業コピーが常に汚れ、検証と錠を守れない。
- **エージェントに `model` を書かせる。** ファミリーの切替と起床を人が気付かず起こす。
- **編集ごとに版を切る。** 比べたいのは「押した」単位。編集は編集履歴（決定 9）。
- **知識を `~/.config/agent-fleet` に置く。** Files ペインが拒否し、方針がエージェントに禁じる。
- **Managed 専用。** claude が選べない。
- **ペインを MirrorView と別に書き直す。** 思考・ツールカード・添付・resume を失う。

## 影響

- **Agent**: `internal/imagegen/studio.go`（ストア・錠・JSONL の編集履歴と press・巻き戻し・知識の
  読み書き・試走の口・人格の口）、投入時の固定コピー（root 固定 open・許可 root 2 つ・入力セット・
  `jobRec` の原本欄・取消経路を含む掃除）、`mcpImageGenAdvertise` の除外と実行前の再検査、
  メタの `InitialPromptState`、`comfyFamilyRow` の 4 欄と `modelStatus`、`history.jsonl`、
  `session.Meta.Studio`（5 か所）、`mcp_stdio.go` のツール 4 本（名前は文字列リテラル）、
  copilot／cursor／kiro／muse への `AF_SESSION_NAME` 配達、合図 1 行の Go 側の剥がし手、routes と golden。
- **CP**: スタジオ・履歴・知識の中継、`sessionWire` の `studio` 欄、`routes.golden`。
- **Console**: `features/imagegen/` を 3 列に（MirrorView 埋め込み・起動部品・錠・ハイライト・
  編集履歴・履歴・知識のメモ・スタジオ設定）、ペイン種別に `studioId`、セッション一覧の杖と分岐、
  転写モデル層の剥がし手、`caps` の添付可否、`FAMILY_CARDS`・`PromptHelpModal` の撤去、i18n、撮影スタブ。
- **ドキュメント**: `guide/member/04-files` の画像生成節、`guide/ref/features`、af-usage の知識。
- **試験**: 純関数（下書きの検証・錠・巻き戻し・since の畳み）／DOM（ハイライト・錠・カード）／Go
  （番人の陰性対照・錠・JSONL・広告の切替・tools/list の指紋）／実機 1 回（Managed と TUI で各 1 kind・
  `list_changed` の尊重を kind ごとに測る）。

## フェーズ

- **P0 の前提作業**: ① `inputs`/`mask` の番人（投入時の root 固定 open と固定コピー・全 provider）＝
  決定 4 の `op`/`inputs` 解放の前提、② 合図 1 行の剥がし手（転写モデル層・Go）＝合図を出す前提。
  copilot／cursor／kiro／muse の `AF_SESSION_NAME` 配達は **P0 全体の前提ではなく、その kind の
  Managed をスタジオに開放する条件**（届くまで Terminal 限定）——claude TUI・codex Managed 新規・
  Terminal 全 kind・lcpp だけで P0 の輪は閉じる。
- **P0**: 決定 1〜5・7・8・10・12（層 2）と、決定 9 の編集履歴・press・**絵の履歴の一覧と
  「この設定に戻す」**。画面の 3 列。層 B の撤去。指示編集（参照あり）と、既存のパス欄でマスクを
  置く inpaint は決定 4 の解放で入る。**P0 のエージェントは絵を見ない**（人の観察を言葉で渡す）
  ——見せるのは P1（決定 6）。
- **P1**: 決定 9 の「並べる」、決定 6 の「見せる」、copilot／cursor／kiro／muse Managed の開放、モデル提案カード、杖アイコン、台帳の
  `Ref`、lcpp の system prompt 経由の人格、下書きのファイル保存／読込、**決定 11 のキャンバス**
  （log 111 §10・対象は ComfyUI のファミリー・受け入れは Chromium と iOS Safari で各 1 回、
  `openai_compat` 経路は未測定のまま対象外）。
- **P2**: 会話からのスイープ（行列の提案→人が投入）、層 1 への昇格、**知識の git 差分管理**
  （ADR 0022 の改訂として起票）、claude の Managed ドライバ（別 ADR）。

## 未解決

1. **`list_changed` を尊重する kind**（claude 以外は未測定）。尊重しない kind では結び直し・トグルが
   resume まで効かない。P0 の受け入れで測る。
2. **試走の枠 3 はワークスペース共通**。エージェントの試走をスタジオごとに 1 枚に絞るか。429 の文言は
   スタジオ文脈に直す。codex の MCP 上限 600 秒は冷えたエンジンの 16 分に足りない。
3. **段 2（ワークスペース停止）は生成中でも止める**（reaper は imagegen のジョブを見ない）。
   0081 からの穴で、別件で起票する。
4. **知識の「要約」の上限**（1 KB）と `get_image_studio` の 8 KB は当て推量。実機で直す。
5. **`generate_image` 以外の CLI 組み込み画像ツール**（codex の `image_gen`・agy）はスタジオでも
   使える。制限したければ kind ごとの構成の話で、本 ADR の外。
6. **スタジオとメタの片側だけ書けたとき**の修復（決定 2）: スタジオを正として起動時に突き合わせる。
   手順は実装で決める。

## 改訂 1 で変えたこと（2026-09-23・[113-adr-review](../log/113-adr-review.md)）

| 指摘 | 変更 |
|---|---|
| 🔴1 空の下書きに部分更新できない | 決定 3: 保存時は欄ごとの検証、完成の検証は投入・試走で |
| 🔴2 引数無し試走の「画面どおり」が保証されない | 決定 2・3: 下書きが `provider`・`model` を持ち、既定へ落とさない。断る条件と上限超過の答えを定義 |
| 🔴3 `generate_image` を隠す配線と保証範囲 | 決定 3: `mcpImageGenAdvertise` の除外・保証は af の経路のみ・CLI 組み込みは未解決 5 |
| 🔴4 版＝押した印は追記 JSONL と食い違う | 決定 9: press を独立イベントに |
| 🔴5 P0 に絵の永続履歴が無い | 決定 9・フェーズ: 一覧と「戻す」を P0 に。絵と版の対応はサイドカーで永続 |
| 🔴6 codex resume で worktree OFF が曖昧 | 決定 8: OFF は Terminal と lcpp だけ |
| 🔴7 人格の注入契約が無い | 決定 5: Agent が組み、Console が `initial_prompt` に渡す。結び替えは初回ターン |
| 🔴8 番人に symlink／TOCTOU 対策が無い | 決定 4・前提作業 ①: 読む場所で root 固定 open |
| 🔴9 P0 で inpaint を書けるがマスク画面は P1 | 決定 4: P0 は既存のマスクパス欄、キャンバスは P1 |
| 🟡1〜12 | 合図の付く範囲・巻き戻しと錠・結びの真実・識別の 3 状態・語彙の列挙・行番号・browse root・前提作業の組み替え・P0 でエージェントは絵を見ない・キャンバスの受け入れ・0022 改訂・log 112 §14 の直接参照 |

## 改訂 2 で変えたこと（2026-09-23・[113-adr-review](../log/113-adr-review.md) §5）

| 指摘 | 変更 |
|---|---|
| 🔴A 結び替え直後は古い広告集合から `generate_image` を呼べる | 決定 3: 実行前に所有セッションのメタを読み直して断る。広告の除外は見え方、再検査が保証 |
| 🔴B root 固定 open が comfy のアップロードだけ | 決定 4: 要求のパスを読む全箇所を 1 つのヘルパーへ。AST 走査の試験で固定 |
| 🔴C 600 秒上限後の返答はクライアントに届かない | 決定 3: 待ちは最長 120 秒、超えたらジョブ id を返し結果は `get_image_studio` の since |
| 🔴D `needs_mask` を消す規則が無い | 決定 4: 保存する旗でなく導出値（`op=inpaint` かつ `mask` 空） |
| 🔴E press 1 行に id と失敗を書けない | 決定 9: `POST …/press` 1 要求で投入してから書く（決定 1 の 2 つ目の例外） |
| 🔴F スタジオ作成と `initial_prompt` の順序 | 決定 2・5: 「付ける」押下でスタジオ作成→persona→`studio` 付きでセッション作成→メタと結びの後に初回ターン |
| 🔴G kind 一覧を `managedDrivers` だけから引くと claude が落ちる | 決定 8: Managed と TUI で出所を分ける |
| 🟡A〜C | マスク欄の行番号・知識 root は home 固定（存続優先）・移行時の `provider` は解決済みの行 id、`model` 空は試走だけ断る |

## 改訂 3 で変えたこと（2026-09-23・[113-adr-review](../log/113-adr-review.md) §6）

| 指摘 | 変更 |
|---|---|
| 🔴H CLI 子に渡した参照パスは `openRequestFile` を通らない | 決定 4: 投入時に固定コピーへ写し、provider（子プロセス含む）は原本のパスを見ない。型で守る |
| 🔴I 120 秒は opencode の 60 秒上限より長い | 決定 3: 試走にも 10 秒ごとの heartbeat |
| 🔴J 応答後の press 追記では絵に版 id が渡らない | 決定 9: 版 id を投入前に予約し `JobSpec` に載せる。落ちたら起動時に合成 |
| 🔴K browse root が repo の配備で自分の絵を参照に戻せない | 決定 4: 読める元は browse root＋生成物 root |
| 🟡E〜I | 指紋は子が計算＝watcher に揃える／home 固定時の編集面は「メモ」／TUI 候補は `repoLaunchKinds` の `terminalDriver`／初回ターン送信失敗は `warning`＋再送／決定 1 の例外は 2 件 |

## 改訂 4 で変えたこと（2026-09-23・[113-adr-review](../log/113-adr-review.md) §7）

| 指摘 | 変更 |
|---|---|
| 🔴L ジョブ別コピーは採番の順序で作れず、provider 用と記録用が同じ欄 | 決定 4: 入力セット id で投入前に写す。`Request` に原本の記録欄（`InputOrigins`・`MaskOrigin`）。決定 1 の例外に明記 |
| 🔴M `recovered` 行をサイドカーから合成できない | 決定 9: press は投入前の全文行と投入後の結果行の 2 行。復元の元は常に前者。起動時はサイドカーを走査 |
| 🔴N TUI の初回ターン失敗は作成応答に載らない | 決定 2: メタの `InitialPromptState` を配達側が書き、ペインが読む（60 秒で unknown なら再送） |
| 🔴O `terminalDriver` の真偽で絞ると claude・agy が落ちる | 決定 8: `terminalDriver !== false` から shell を除く |
| 🟡J〜L | コピーの掃除（失敗時同期・グループ終了・起動時全消し）／置き場を生成物 root の絶対パスで／日英対応は確認済み |

## 改訂 5 で変えたこと（2026-09-23・[113-adr-review](../log/113-adr-review.md) §8）

| 指摘 | 変更 |
|---|---|
| 🔴P `Request` の原本欄は provider にも渡る | 決定 4・1・影響: 原本欄はキューの記録 `jobRec` に。`Request` はコピーだけ |
| 🔴Q `press` 行の欄一覧に投入後の値が残る | 決定 9: `press`（投入前の物）と `press_result`（投入後の物）の 2 行に欄を分けて書き直し |
| 🔴R 送信処理中の 60 秒 `unknown` で再送すると二重ターン | 決定 2: `pending` を足し、配達処理が終わるまで再送を出さない |
| 🟡M〜O | 影響欄の型を揃える／掃除に取消 2 経路を含める／`press_result` の追記失敗は `recorded: false` と「記録保留」 |
