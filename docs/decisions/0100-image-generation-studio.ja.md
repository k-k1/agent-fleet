# 0100. 画像生成スタジオ——セッションのエージェントが下書きを直し、人がボタンで生成する

[English](0100-image-generation-studio.md) | 日本語

- 状態: **proposed**（2026-09-23 起草）。根拠と経緯は [docs/log/113](../log/113-imagegen-studio.md)
  （5 巡の設計と、別セッションによるレビュー [113-review](../log/113-review.md)・🔴 7・🟡 12 を反映済み）。
  実装は無い。
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

- Agent の要求語彙（`jobs_http.go:28-59`）は prompt / negativePrompt / size / count / inputs / mask /
  model / loras / seed / strength / params / label / out_dir / jobs / seed_policy / trial / full_steps
  で足りる。**本 ADR は語彙を足さない。** ただし `inputs`/`mask` に**パスの番人が無い**（`spec()` が
  素通しし `comfy.go:1186-1187` が `os.ReadFile` でそのまま上げる）。
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
- チャットのアシスタントは `generate_image` を呼べない（所有セッションが要る・`mcp_stdio.go:166-170`）。
- Managed のターン `POST /sessions/{name}/turn` の `attachments` を読むのは opencode・codex・muse だけ。
  copilot／cursor／kiro／lcpp は捨てる。
- `~/.config/agent-fleet` は Files ペインの拒否リスト（`fs.go:126`）にあり、ワークスペース方針も
  エージェントに触るなと言う。
- 指示編集の中央クロップは廃止済み（ADR 0094 改訂 #907・実装 #913・受け入れ #914）。マスクの
  キャンバスは log 111 §10 の設計で着工できる。

## 決定

### 決定 1 — LLM は下書きを直すときだけ。生成の経路は変えない

「LLM を挟まずに」（0081）は生成については変わらない。エージェントが触るのは**下書き**で、
生成ボタンを押すのは人、押したときに走るのは 0081 のジョブキューそのもの。`POST /imagegen/jobs` の
語彙・検証・ジョブ・試走・グループ・取消・EMA・サイドカー・`props`・使用量・CP の中継 7 行は
そのまま。**唯一の例外**は決定 4 の番人（`inputs`/`mask` のパス検査を `spec()` に足す）。

### 決定 2 — 「スタジオ」を Agent に置く。セッションとは別の id で、セッションを 1 本結ぶ

Agent に**スタジオ**（`~/.config/agent-fleet/imagegen/studios/<id>.json`）を置く: `id / title /
draft / locks / versions[] / session / agent_trial / mask_strokes / created_at / updated_at`。
**下書きの真実はスタジオ**。`localStorage` は「最後に開いたスタジオ id」だけを覚える。

- セッションと別 id にする理由: エージェントを替える（sonnet→opus・claude→codex）、古くて resume
  できない、一覧を整理するために消す——どの場合も下書きと版は残す。スタジオが主で、セッションは
  「いま結ばれている相手」。
- セッションのメタに逆参照 `Studio`（`Origin` の隣）。**fork は引き継がない**（1 スタジオ 1 セッション）。
  作成・fork・recreate の `Meta{…}` リテラル 3 か所、Agent の `wireSession`、**CP の `sessionWire`**、
  停止中の DB ミラーに欄を通す（無い欄は黙って落ちる）。
- 本体は小さく保ち、メモリ上の錠（チャットの `LockConv` と同型）＋tmp→rename で書く（`fstore` に
  錠も原子性も無い）。編集履歴（決定 9）は別ファイル。
- セッションが無いうちは今の `localStorage` 下書きで動き、最初の発言でスタジオを作るときに移す。
  **ADR 0081 の利用者は何も失わない。**

### 決定 3 — 契約はセッション側 af MCP サーバのツール 4 本。`generate_image` は広告しない

| ツール | 何をするか |
|---|---|
| `get_image_studio` | 下書き・錠・**since last call**（人の編集・巻き戻し・新しい結果を最大 5 件＋「他 N 件」）・モデルの事実（ファミリー・読める摘み・サイズ・既定値・LoRA とトリガー語）・知識の「要約」節・版の要約。上限 8 KB |
| `set_image_draft` | **部分更新**。書いた欄だけ変え、`null` で消す。検証は投入と同じ `spec()`。錠の欄は落として理由を返す |
| `run_image_trial` | **引数を持たない**。いまペインに見えている下書きそのもので試走（1 枚・キュー先頭・ファミリーの試走 steps・待ち 3 枚まで）。結果はパス・seed・警告・所要時間。試走枠にも出る |
| `add_image_knowledge` | 決定 12 の「記録」節へ追記（scope・key・note・evidence） |

- 広告は**そのセッションがスタジオに結ばれているときだけ**（`mcpOwningSession()` のメタに `Studio`）。
  tools/list は要求ごと・`list_changed` 1 分なので、結び直しは通知を尊重する kind なら 1 分以内に効く
  （尊重の有無は kind ごとに未測定＝受け入れで測る）。判定は status と切り離し、メタの読みだけで
  決める（Agent が遅いときにツールが点滅しない）。
- **`generate_image` はスタジオのセッションに広告しない**——「生成は人がボタンで」を人格の口約束でなく
  広告集合で保証する。**N 枚の投入はツールに無い。** 試走だけをエージェントに許すのは、
  「`generate_image` の不満はプロンプトが見えないこと」への答えで、引数を持たないツールなら
  走るものは常に画面に見えている。スタジオごとに「エージェントの試走を許す」（既定 ON）。
- 識別が曖昧なとき（決定 8）は広告から消すのでなく**呼ばれたら理由付きで断る**。
- MCP にして転写の fenced block を退けた理由: セッションは全 kind が af MCP で話し、転写の解析は kind
  ごとに 9 通り。ツールはターン途中の実時間で効く。説明文の固定費はスタジオのセッションだけが払う。

### 決定 4 — エージェントが書ける欄と、人しか書けない欄

| エージェント | 人だけ |
|---|---|
| `prompt` `negative` `params{steps,cfg,sampler,scheduler}` `size` `loras` `strength` `op` `inputs`（番人が入ってから） | `model` `seed`/`seed_policy` `jobs` `count` `out_dir` `label` `mask` |

- `model` を人側に置く: 切替はファミリーの切替＝読める摘み・サイズ・ネガティブの可否が全部変わり、
  冷えたエンジンなら 1 枚目に数分。エージェントは `suggest_model` を書け、ペインは提案カードで出す。
- `op`・`inputs` をエージェント側に置く: 「この絵の看板の文字を CLOSED にして」は op・参照・指示文の
  1 手。**前提は投入側の番人**（browse root の内側＋Files ペインの拒否リスト）。番人が入るまで解放しない。
- `mask` は人だけ（塗るのは人の手）。エージェントは `needs_mask: true` を書け、ペインが導線を出す。
- **欄ごとの錠**（🔒）。錠の欄への書き込みは落として理由を返す。自動の錠は作らない。

### 決定 5 — 文脈はエージェントが引く（pull）。Console は末尾に合図 1 行。前置はしない

人格は「発言を受けたらまず `get_image_studio` を呼ぶ」。Console は発言の**末尾**に
`[studio v4 · 下書きが変わった · 新しい結果 2 · 巻き戻し #9 → get_image_studio]` の 1 行（30 トークン）
を添える。合図の有無で呼ぶ／呼ばないを分けない（Terminal ペインから打った発言に合図は無い）。

- 前置しない理由: 入れる 1 か所が無く（`/turn` は `h.Send` 直）、剥がす 1 か所も無く
  （`splitPastedImages` は末尾の添付指示文専用・自動タイトルは先頭 400 字を読む）、転写に残って
  毎ターン再送される（lcpp は 10 ターンで窓の大半）。
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
  `managedDrivers` から引く。
- **worktree 既定 ON** は、af サーバが自分のセッションを cwd で推測する kind で推測を一意にする
  唯一の手段。`AF_SESSION_NAME` を受け取れる kind（claude／codex 新規／lcpp／Terminal）では OFF を
  選べる（未コミットの資料を読ませたいとき）。
- **opencode の Managed はスタジオから外す**（Terminal は可）: af 子を複数セッションが共有し、別の
  セッションから `set_image_draft` が走り得る。copilot／cursor／kiro／muse は `AF_SESSION_NAME` を
  子へ届ける改修を P0 の前提作業にし、届くまで Terminal 限定。

### 決定 9 — 履歴は 3 つ。編集履歴・版・絵

- **編集履歴** `draft_log`: 下書きが変わるたび 1 件（時刻・書き手＝エージェントのターン／人／
  `rewind ← #n`・変わった欄と前後の値・その時点の全文）。別ファイルの追記専用 JSONL
  （`studios/<id>.log.jsonl`）。転写では `set_image_draft` のツールカードを「下書きを更新: cfg 7→5」の
  専用カードに描き、そこと中列の一覧から**「この時点に戻す」**（`POST …/rewind`・履歴は消さない・
  錠は対象外・次の `get_image_studio` の since に出る）。
- **版**: 生成ボタン（人の試走・投入・エージェントの試走）を押した瞬間の写し。版＝編集履歴のうち
  「押した」印の付いた 1 件。
- **絵の履歴**: `GET /imagegen/history?studio=&before=&limit=`。裏は `generated/console/history.jsonl`
  （サイドカーを書くときに 1 行追記・無ければ走査して再生成）。操作は「この設定に戻す」（同じ
  `rewind`）・「参照にする」・「見せる」・「並べる」（最大 4 枚）。掃除は 0081 決定 3 のまま。

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

- 層 2 は **`~/imagegen-knowledge/{families,models}/<key>.md`**（拒否リスト外・Files ペインで開ける・
  recreate は `~/repos` しか消さない）。1 モデル 1 本・1 ファミリー 1 本、**4 節**（要約 1 KB・設定・
  プロンプト・記録＝追記のみ）。「記録」は `add_image_knowledge`、他の節はエージェントの Edit と人の
  Files ペイン。**消すのは人**。エージェントが書くのは「覚えて」と言われたときと、結果に良し悪しを
  言ったときだけ。読みは `get_image_studio` が「要約」を毎回、他は求められたとき。
- リポジトリへは「共有したいときに明示で書き出す」（スタジオごとの書き先切替は他のセッションの
  `git status` を汚すので却下）。
- **将来は ADR 0022 の git スナップショットの roots に加える**（利用者の要望「claude のメモリの
  ように git で差分管理」）。差分・復元・書き出し・取り込みがそのまま手に入る（P2）。

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

- **Agent**: `internal/imagegen/studio.go`（ストア・錠・JSONL の編集履歴・版・巻き戻し・知識の読み書き・
  試走の口）、`spec()` の番人、`comfyFamilyRow` の 4 欄と `modelStatus`、`history.jsonl`、
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

- **P0 の前提作業**: ① `inputs`/`mask` の番人、② copilot／cursor／kiro／muse の `AF_SESSION_NAME` 配達、
  ③ 合図 1 行の剥がし手（転写モデル層・Go）。
- **P0**: 決定 1〜5・7・8・10・12（層 2）と、決定 9 の編集履歴と版。画面の 3 列。層 B の撤去。
  指示編集（参照あり・マスク無し）は決定 4 の解放で入る。
- **P1**: 決定 9 の絵の履歴と「並べる」、決定 6 の「見せる」、モデル提案カード、杖アイコン、台帳の
  `Ref`、lcpp の system prompt 経由の人格、下書きのファイル保存／読込、**決定 11 の inpaint**
  （キャンバスは log 111 §10）。
- **P2**: 会話からのスイープ（行列の提案→人が投入）、層 1 への昇格、**知識の git 差分管理**
  （ADR 0022 の roots）、claude の Managed ドライバ（別 ADR）。

## 未解決

1. **`list_changed` を尊重する kind**（claude 以外は未測定）。尊重しない kind では結び直し・トグルが
   resume まで効かない。P0 の受け入れで測る。
2. **試走の枠 3 はワークスペース共通**。エージェントの試走をスタジオごとに 1 枚に絞るか。429 の文言は
   スタジオ文脈に直す。codex の MCP 上限 600 秒は冷えたエンジンの 16 分に足りない。
3. **段 2（ワークスペース停止）は生成中でも止める**（reaper は imagegen のジョブを見ない）。
   0081 からの穴で、別件で起票する。
4. **知識の「要約」の上限**（1 KB）と `get_image_studio` の 8 KB は当て推量。実機で直す。
