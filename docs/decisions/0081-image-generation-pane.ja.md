# 0081. LLM を挟まずに Console から画像を作る——ペイン 1 つ・Agent のジョブキュー・「まず決定論、次にモデル」のプロンプト支援

[English](0081-image-generation-pane.md) | 日本語

- 状態: **受理**（2026-09-13 起草、同日のレビューで訂正を反映して受理。レビューは以下の「ある」「無い」の
  主張を、ADR 0080 のレーン B（#622）が着地した後の `d164739a` で 1 つずつ grep して裏取りした。直した
  箇所には *（レビュー）* と記す——決定 5 の中継フィールド 4 つ、列の無い LoRA の重み、決定 2 の wire 本文、
  PNG チャンクが実際に与える族と seed、サムネイルの小ファイル例外、ライトボックスの `path`、行番号 5 件）。
  **P0 は同日に #625 / #626 / #627 で実装**（GPU 無し）。実装後の wire は末尾の「P0 の実装」、実機 1 回はまだ負っている。
- 関連: [0069](0069-image-generation-providers.ja.md)（このペインが駆動する provider 抽象。
  未解決 1 でジョブ形を先送りにした——本 ADR がそれを引き取る） /
  [0072](0072-engine-model-catalog.ja.md)（カタログ行・`params`・`negative_prompt`・comfy の 5 族） /
  [0080](0080-image-gallery-pane.ja.md)（絵を見る場所。このペインはギャラリーが読むものを書く） /
  [0078](0078-sessions-overview-pane.ja.md)（直近のペイン種別と、その定型） /
  [log 19](../log/19-assistant-chat.md)（`POST api/chat/ask`——プロンプト支援が借りる一発実行の口。
  初稿が書いた ADR 0020 の物ではなくアシスタントチャットの物・*レビュー*） /
  [0071](0071-self-hosted-inference-engines.ja.md)（エンジンの箱・コールドスタート・誰が払うか）

## 背景

今日、絵はエージェントに頼むことでしか作れない。セッションが `generate_image` を呼び、`af` MCP
サーバが Agent の `POST /imagegen/generate` を叩き、Agent が ComfyUI のグラフを組み、CP の
`/engine/image/v1/*` 素通しを経由してエンジンを駆動し、PNG を `~/.cache/agent-fleet/generated/<sid>/`
に置き、絵は転写のファイルカードとして戻る。「この文書に挿絵を 1 枚」にはこれが正しい形である。
**1 つのプロンプトを cfg 3 通り × チェックポイント 2 本で 40 枚**欲しい人には正しくない。往復のたびに
モデルのターンを払い、その人が気にするパラメータ（steps・sampler・seed・LoRA の重み）は見えないか
届かず、結果と自分の間にエージェントの言葉が挟まる。

既にあるもの（`2da2bf28` のツリーで実測し、ADR 0080 の P0 レーン B（#622）の後の `d164739a` で
再確認・2026-09-13）:

- **Agent には MCP 以外の口がある。** `GET /imagegen/status` と `POST /imagegen/generate`
  （`workspace/agent/routes.go:121-122`・`internal/imagegen/http.go`）。ほかのルートと同じく Agent の
  bearer の内側にあり、**CP の中継リストには載っていない**（`http.go:8-9` が意図的にそう書いている）。
  `console/` からは誰も呼んでいない。
- **絵を作るものは全部 `workspace/agent/internal/imagegen/` にある**: `Provider` インタフェースとコア
  （`imagegen.go`）、ComfyUI のグラフテンプレート 5 本（`comfy_workflows.go`: `sdxl` / `sd35` /
  `flux1` / `flux2-klein` / `zimage`）、族の検査と重み付きの LoRA 解決（`comfy.go:303-350`）、
  ネガティブの合成（行＋呼び手＋配備全体、`comfy.go:169-178`）、seed（`comfy.go:713-725`）、
  ストアと 30 日の掃除（`store.go`）、枚数とピクセルを数える使用量行 `tool.imagegen`
  （`imagegen.go:706-726`）。CP はバイトを中継するだけで（`control-plane/engine_gateway.go:866-957`）、
  image 役については使用量行を**書かない**（`engine_usage.go:180-186`）。
- **エンジンの資格情報はワークスペースの中にしか無い。** `AF_ENGINE_ISSUE_TOKEN` はコンテナに注入され
  （`control-plane/workspace_lifecycle.go:401`）、ブラウザは `/engine/{key}/v1/*` が受ける物を何も
  持たない（`engine_gateway.go:417-424`）。Console が ComfyUI を直に叩くには、存在しない資格情報が要る。
- **要求語彙**（`imagegen.go:56-125`）: `op`・`prompt`・`negative_prompt`・`size`・`aspect_ratio` と
  `background`（ベンダー経路の軸。comfy は `transparent` だけ読む・*レビュー*）・`count`
  （→ ComfyUI の `batch_size`、上限 4）・`inputs`・`mask`・`model`・`seed`・`strength`・
  `loras[{name,weight}]`。**無いもの**: `steps`・`cfg`・`sampler`・`scheduler`。これらはカタログ行の
  `params`（ADR 0072・`EngineParams`）が族の recipe にフィールド単位で被さる（`comfy_workflows.go:174-195`）。
  ADR 0069 はこれを意図して MCP ツールの外に置いた（「provider 差が大きすぎる／摘んでも動かない経路が
  ある」）。その理由は**エージェントが見るツール**の話で、フリート自身の provider しか持たないペインを
  縛らない。
- **族が読むもの**（`comfy_workflows.go`・括弧は recipe の既定）:

  | 族 | steps | cfg | sampler | scheduler | ネガティブ |
  |---|---|---|---|---|---|
  | `sdxl` | 読む (20) | 読む (7) | 読む (dpmpp_2m) | 読む (karras) | 効く |
  | `sd35` | 読む (28) | 読む (4.5) | 読む (dpmpp_2m) | 読む (sgm_uniform) | 効く |
  | `flux1` | 読む (20) | **読まない**——FluxGuidance 3.5 | 読む (euler) | 読む (simple) | 効かない |
  | `flux2-klein` | 読む (4) | **読まない**——1 固定 | 読む (euler) | **読まない** | 効かない |
  | `zimage` | 読む (8) | 読む (1) | 読む (res_multistep) | 読む (simple) | 効かない |

  知らない sampler / scheduler 名は**黙って無視**される（`comfy_workflows.go:174-209`——被せる名前が
  リストに無ければ `comfyRecipe.with` は recipe の名前を保つ）。箱が知らない
  名前はコールドスタートの後に `Value not in list` で落ちるので、Agent の許可リストが契約である。
  蒸留族へのネガティブは警告付きで落とされる（`comfy.go:187-204`）。Console はこの表を知らない。
- **ブロックする呼び出しと時計。** `POST /imagegen/generate` は絵がディスクに載ってから返る。鎖は、
  CP の起床予算 900 秒（`engine_gateway.go:87`）と ALB の 60 秒 idle の内側の 45 秒ホールド
  （`:33-52`・`engineManagedPlainHoldSeconds` は `:100`）、Agent の 16 分（`sdcpp.go:191`）、MCP 呼び手の
  18 分（`mcp_imagegen.go:157`）。
  Console の REST 中継（`control-plane/proxy.go:148-180`）はストリームもハートビートも無いバッファ中継で、
  コールドスタートを待つブラウザの呼び出しは ALB 配備では 60 秒で切られる。
- **ジョブ id も取消も進捗も seed も返らない。** `prompt_id` は `comfy.go` の外に出ない。ComfyUI の
  `/interrupt` も `/queue` も誰も呼ばない。唯一の「進捗」は中身の無い MCP ハートビート
  （`mcp_imagegen.go:250-298`）。`comfySeedFor` が引いた乱数 seed は `Result` に無い
  （`imagegen.go:154-171`）——呼び手は得た絵を再現できない。
- **ディスクに由来が無い。** Agent は PNG のバイトをそのまま書き、サイドカーは書かない（`store.go`）。
  ComfyUI の `SaveImage` は箱が `--disable-metadata` で無い限り API グラフを `prompt` テキストチャンクに
  埋めるが、このリポジトリで PNG テキストを読む者も書く者もいない。
- **メンバー向けカタログが無い。** 管理画面（`console/src/features/settings/admin/adminEngines.tsx`・
  `adminEngineModels.tsx`）は super_admin、または `allow_engine_ingest` 下の tenant_admin。メンバーの
  ブラウザが今日持つ唯一の口は `GET /imagegen/status` で、モデル行は `{id, description, warm}` だけ
  ——族も sizes も `params` も行のネガティブも無い（`http.go:61-99`）。
- **トリガー語は読んで捨てている。** Civitai の `trainedWords` は `ingest/resolve` と検索で戻り
  （`control-plane/engine_admin.go:1673-1675`・`engine_ingest.go:497`）ウィザードに出るが、保存する列が
  無い。トリガーが要る LoRA は載っても見た目に何も変えない。
- **Console が既に打てる LLM 一発実行がある。** `askAssistant(prompt, assistant?)`
  （`console/src/core/api/client.ts:880` → `POST api/chat/ask`・`control-plane/routes.go:467` が中継 →
  `workspace/agent/internal/chatx/chat_handlers.go:228-261` の `HandleChatAsk`）:
  使い捨て・非永続の会話、ツール無し、240 秒（`chat.go:487`）、会員自身の CLI ログインで動き、
  `assistant.ask` として記帳される。メモ整理（`MemoTidyModal.tsx:83`）と TTS 要約
  （`useMirrorTts.tsx:160`）が既に使い、適用前に答えをプレビューしている。
- **上流（ComfyUI の `server.py`・2026-09-13 に確認）:** `POST /interrupt` は `{"prompt_id"}` 付きなら
  その 1 件だけ、無しなら箱全体を止める。`POST /queue {"delete":[id]}` で待機中を外せる。`GET /queue`
  は実行中と待機中を返す。ステップ単位の進捗は **WebSocket にしか無く**、CP の中継は接続を upgrade しない。

利用者の言葉は「エージェントを介さずに comfy を叩いて画像を量産する UI」。ここでの「エージェント」は
LLM のセッションを指す。Workspace Agent——コンテナの中の Go デーモン——は経路に残る。グラフも資格情報も
ディスクも台帳もそこにあるからである。

## 決定

### 決定 1 — 画素は今後も Workspace Agent が作る。Console は CP の中継リストを通して Agent に届く

- 何も動かさない。Console は他の Agent 由来の画面と同じ `agentProxyAPI.rest` 中継で `/api/imagegen/*`
  を呼び、CP の `routes.go` は行が増えるだけ（`withResolved` は既に稼働中のワークスペースを要求する。
  それはギャラリーの条件でもある——Agent プロセスが居なければどちらにせよ絵は無い）。
- グラフ組み立てを CP に持ち上げる案は、存在しないブラウザ側のエンジン資格情報、`engine_catalog_test.go`
  が Agent のソースを読んで一致させている族ディスパッチの 2 つ目の写し、ギャラリーが見られない 2 つ目の
  ストアを要る。ComfyUI 自身の Web UI をブラウザペインに埋める案は同じ資格情報の問題を持ち、
  テナントのカタログを迂回する。
- ここで出すのは**フリートの provider だけ**: `comfy` と `sdcpp`。CLI 駆動の provider（`codex`・`agy`）は
  構造上エージェントであり、これらの摘みを 1 つも持たず、プラン枠を消費する——`agents.image_generation`
  の opt-in が存在する理由である。したがってその opt-in は**このペインを門にしない**。ペインは
  `GET /imagegen/status` が ready なフリート provider を報告するときに存在する。既存ルートが要求する
  `session`（出力フォルダの名前になり、セッション自身の CLI と同じ provider を拒むための欄）は
  この経路では意味を持たない（決定 3）。

### 決定 2 — Agent にジョブキューを置く。Console は投入してポーリングする。既存のブロック型ルートは MCP ツールのために残す

ADR 0069 の未解決 1 は、driver モデルからの 1 ポーリングが 1 ターンの費用になるためジョブを先送りにした。
ブラウザのポーリングはバイト以外の費用が無く、60 秒の規則はブロック型を中継越しには使えなくする。

- **Agent のルート**と、CP のリストに載せる 7 行（グループとキューの操作は決定 12・`props` は決定 3）:
  - `POST /imagegen/jobs` — 本文は既存の `generateRequest` から **`session` を除き**（ペインにセッションは
    無い。フォルダは決定 3 の物で、「セッション自身の CLI ではない」の検査には検査する物が無い）、
    `params`（決定 4）・`label`（一覧に出す自由文）・`out_dir`（決定 3）・`jobs`（N ≥ 1・決定 8）・
    `seed_policy`（`random | fixed | sequence`・決定 8）・`trial`（決定 11）を足したもの。Agent が N を
    N ジョブに展開して 1 つの `group` に置き、即座に `{group, jobs: [{id, position}]}` を返す——要求 1 回
    なので下の上限はバッチ全体で判定し、半分だけ受け付けることが無い *（レビュー: 本文の欄が決定 8 と
    11 に散っていた。これが唯一の一覧）*。待機ジョブが `imagegenQueueMax`（200）に達したら 429 で断る
    ——共有の箱に 1 人が 1 日分の GPU をうっかり並べられないように。
  - `GET /imagegen/jobs` — Agent がまだ覚えているジョブ全部（待機・実行中・完了の直近 500）を新しい順に。
    `state` ∈ `queued | waking | uploading | running | fetching | done | failed | cancelled`、待機中は
    `position`、`started_at`・`finished_at`・`elapsed_ms`、解決済みの要求（model・族・seed・実効
    `params`・loras・size）、完了なら `StoredFile` 形の `files[]` と `warnings[]`。同じ状態には同じバイトを
    出し、CP の ETag 層（`control-plane/etag.go`・JSON の GET 全部に弱い ETag）が変化の無いポーリングを
    304 にできるようにする。
  - `DELETE /imagegen/jobs/{id}` — 取消。待機中なら外す。実行中なら provider の任意インタフェース
    `Canceller` に頼む。comfy では、上流でまだ待機中なら `POST /queue {"delete":[prompt_id]}`、
    実行中なら `POST /interrupt {"prompt_id"}`——**必ず id 付き**で、素の `/interrupt` は打たない。
    箱はワークスペース間で共有で、素の interrupt は他人の絵を殺す。`sdcpp` に取消は無い。走り切って
    結果を捨てる。
  - `GET /imagegen/status` — 拡張する（決定 5）。
- **provider ごとにワーカー 1 本、ジョブは 1 つずつ。** 箱は既にサンプリングを直列化しており、先に投げても
  キューが Agent から見えず取り消せない場所へ移るだけで、`engine_waking` を待つ要求が同時に 2 つ
  できる。直列にしてこそ待ち順と見積りに意味が出る。
- **状態の段階は provider から来る。** `sendWithWake` と `awaitHistory` が要求に付いたコールバックで
  `waking`・`uploading`・`running`・`fetching` を報告し、読むのはジョブ一覧だけ。「エンジンが起動中。
  最初の 1 枚は数分待つ」を、説明の無い 5 分間の `running` の代わりに言うためのもの。
- **進捗バーでなく見積り。** ステップ進捗は上流で WebSocket のみ、中継は upgrade しない。Agent は完了
  ジョブの `elapsed_ms` を (provider, model, サイズ帯) ごとに指数移動平均で持ち `typical_ms` として返す。
  Console は「このモデルは通常 30 秒ほど」と出す。正直で安い。本物のバーは P2（未解決 1）。
- **キューの置き場はメモリ。** Agent が再起動すると待機ジョブは消える。完了分はディスクのサイドカー
  （決定 3）で残る。再起動が痛んだら P1 でジャーナルを足す——Agent はコンテナと共に再起動し、
  コンテナの再起動はキューより多くを既に捨てている。
- **Console は未完了のジョブがある間だけ 2 秒ごとにポーリングし、無ければ止める**——静止画面を
  ポーリングしない既存方針。タブが隠れたら止める。ペインを閉じている間に着地したファイルは
  ギャラリー自身の 20 秒の網が拾う。
- MCP ツール `generate_image` は変えない。後で同じキューへの「投入して待つ」に置き換えられるが、
  利用者に見える差は無く、本 ADR の範囲外。

### 決定 3 — 出力はギャラリーが見る場所へ。1 枚ごとにサイドカー、答えには seed

- **既定フォルダ:** `~/.cache/agent-fleet/generated/console/`——セッション別フォルダの隣、ギャラリーの
  「生成した画像」の家族（ADR 0080 未解決 3）が並べる場所。ファイル名は `image-<unixnano>-<n>.<ext>` を
  保ち、ギャラリーの新しい順が効く。**掃除しない**（2026-09-13 決定）: `store.go` の 30 日の掃除は、
  エージェントの絵が誰も残すと言っていない会話の副産物だから在る。ここの絵は成果物そのもので、
  1 枚ごとに人がボタンを押している。掃除は `console` の下を名前で飛ばし、ペインはフォルダの大きさを
  出して残す費用が見えるようにする。
- **`out_dir`（任意）:** 残す物のための browse-root 相対フォルダ。ADR 0080 の `galleryPath` と同じ検証に
  加え `safeWritableBrowsePath`（アップロード経路自身の門: browse root の内側・`fsDeny` の外）を通し、
  初回に作る。生き残りのためでなく整理のためにある——利用者が名付けた、その仕事の隣のフォルダ。
- **サイドカー:** 各画像の隣に Agent が `<name>.json` を書く——解決済みの要求（プロンプト・合成後の
  ネガティブ・model id・族・実際に使った seed・実効 `params`・重み付き loras・size・op・strength・
  入力パス）、`provider`、ジョブ id、`label`、`elapsed_ms`、`warnings`、Agent のビルド。形式に依らず
  （webp や jpeg でも動き、PNG を書き換えない）、ギャラリーには見えず（`imageFormat()` で絞る）、
  ギャラリーのカードのホバー・「画像生成で開く」・後の「X/Y グリッド」が読む物。ComfyUI 自身の
  `prompt` チャンクは PNG の中に触らず残す。それは API グラフであって要求ではなく、
  `--disable-metadata` の箱では無い。
- **`Image`（provider が `Result` の中で返す物）と `StoredFile` に `seed` を足す**（画像ごと: バッチの
  0 番は基底 seed、以降は `seed+i`——ComfyUI がバッチのノイズをそう導く）。MCP ツールの答えにも同じ 1 行。「乱数だったので戻せない」は
  どの画像 UI でも最多の不満である。
- **絵のプロパティの表示とコピーは本 ADR の物で、0080 の物ではない**（2026-09-13 にギャラリーの
  レーンと決着。利用者の要望「seed 等を表示・コピーしたい」に対し、ADR 0080 側では実装しない）。
  実配備で測った 3 件——`generated/` の 251 枚を PNG チャンク単位で走査——が形を決める:
  1. **comfy 経路の PNG には既に `prompt` キーの `tEXt` チャンクがある**（1 枚 1,550〜1,716 バイト。この
     コンテナ自身の `generated/` でも再確認し、comfy 経路の PNG は全部持ち、ベンダー経路の 1 枚は
     持たなかった・*レビュー*）: API グラフで、`KSampler` の `seed`・`steps`・`cfg`・`sampler_name`・
     `scheduler`、`CheckpointLoaderSimple` のチェックポイント名、`EmptyLatentImage` の size、
     `CLIPTextEncode` の正負プロンプト、`LoraLoader` ノード。サイドカーが無かった頃の絵を回収できる
     **唯一**の経路。走査で分かり、初稿が推測で書いていた 2 点 *（レビュー）*: グラフのノード id は
     Agent 自身の物（`ckpt`・`pos`・`neg`・`ks`・`lat`・`save`）で、`SaveImage.filename_prefix` は
     `af-<族>`（`af-sdxl`・`af-sd35`・`af-flux1`・`af-klein`・`af-zimage`——`comfy_workflows.go:444-671`）
     なので族は推定でなく**読める**。バッチの PNG が持つのは**基底** seed と `batch_size` で、その絵自身の
     seed はファイル名の `-<n>` から基底 + (n − 1)。
  2. **ベンダー経路（codex・agy）の PNG にはテキストチャンクが 1 つも無い**（そのフォルダの 0 件）。
     プロパティは復元不能で、UI は空欄を並べずにそう言う。
  3. **サムネイル（`fs/download?thumb=512`）は再エンコードでチャンクが消える**——JPEG、アルファ付きの
     元なら PNG（`fs_thumb.go:132-140`）。どちらのエンコーダもテキストチャンクを書かない。128 KiB 未満
     （`thumbMinSourceBytes`）か 40 MP 超の元は原本がそのまま返るので、サムネイルの口からチャンクが
     戻ることがあっても偶然であり契約ではない *（レビュー）*。プロパティは原本か、ヘッダだけ読む口から
     読む——サムネイルからは決して読まず、フォルダ 1 つのために原本 300 枚を引くこともしない
     （ADR 0080 決定 4 の帯域の前提が壊れる）。

  よって Agent のルート 1 本、**`GET /imagegen/props?path=<browse-root 相対>`**（中継の 7 行目）:
  サイドカーがあればそれ（`source: "sidecar"`）、無ければ PNG の `prompt` チャンクを最初の `IDAT` まで
  読んで——画素はデコードしない——まず Agent 自身のノード id（`ks`・`pos`・`neg`・`ckpt`・`lat`）で、
  Agent が書いていないグラフには予備としてノード種別で、サイドカーの形に写す（`source: "png"`。正の
  プロンプトはサンプラーの `positive` 入力に繋がる `CLIPTextEncode`、族は `filename_prefix` から `af-` を
  除いた物で 5 つのどれでもなければ空、seed は基底 seed にファイル名の番号を足した物）、どちらも
  無ければ `source: "none"`。答えはファイルの mtime で鍵を取り、サムネイルと同じく `Last-Modified` を
  付けるので開き直しは 304。読むのは**求められたときだけ**: 共有ライトボックス（ADR 0080 決定 5・#622 で
  `features/viewer/ImageLightbox.tsx` へ引き上げ済み）が開いたとき、ギャラリーやペインのカードが求めた
  とき。マウント時にフォルダ丸ごとは読まない。ライトボックスが受けるのはパスでなく URL（`src`）なので
  *（レビュー）*、任意の `path` prop——ギャラリー・ミラーのファイルカード・このペインが皆持つ browse-root
  相対パス——を足し、バーは `path` があるときだけ切替を出す。

  出す面は**共有ライトボックスのバー**——ギャラリー・ミラーのファイルカード・このペインの 3 つが
  出会う唯一の場所: 「プロパティ」の切替で解決済みの欄（model・族・seed・size・steps・cfg・sampler・
  scheduler・重み付き LoRA・正負プロンプト・`source`）を行ごとにコピーボタン付きで出し、
  「すべて JSON でコピー」と、`source` が sidecar か png のときは「画像生成で開く」（決定 6 の導線。
  欄をフォームに読み込む）。コピーは `navigator.clipboard.writeText` に Console 既存のトースト、スマホでは
  行がタップの的。ギャラリーのカードのホバー（0080 の P1）は後で同じ口から seed と model を出せるが、
  本 ADR はそれを要求しない。

### 決定 4 — 要求にカタログと同じ形の `params` を被せる。何を読むかは族が決め、言葉で言う

- `Request.Params *EngineParams`（`steps`・`cfg`・`sampler`・`scheduler`。`clip_skip` と `weight` は要求
  フィールドにしない——`clip_skip` はどのテンプレートも読まず、LoRA の重みは LoRA ごとに既にある）。
  合成順は **族の recipe ← カタログ行 ← 要求**、フィールド単位、既存の `comfyRecipe.with` で。
- **MCP ツールに `params` は付けない。** ADR 0069 の理由はエージェントに対して立つ。ペインの provider は
  フリート自身のもので、摘みは Agent 自身が作った。
  - 🔴 **2026-09-15 に撤回した**（ADR 0069 の同日の追記）。この行が引いている「ADR 0069 の理由」の半分
    ——「摘んでも何も動かない」——は、下の `comfyFamilyKnobs` を数えれば偽である: **7 族すべてが
    `steps` と `sampler` を読む**。ツールは入れ子の `params` を受け、読まない 2 つはこの決定が作った
    警告がそのまま名指しする。**この決定の残り（検証・警告・族ごとの表）は全部そのまま効いている**
    ——ブロッキングルートにも `validateRequestParams` を足したので、2 つの経路の厳しさが揃った。
- **Agent が検証し、コールドスタートの後に箱が不正値を見ることは無い。** sampler / scheduler は
  `comfySamplerNames` / `comfySchedulerNames` で検査し 400 `bad_params` で断る（カタログの被せ方の
  ように黙って無視しない——人が打った値は大きく落ちる）。steps 1〜150、cfg 0〜30、size は各辺 8 の倍数で
  ピクセル上限（`imagegenMaxPixels`・4 M——`l4` での SDXL 2048² は 5 分待った後の OOM で、箱の 400 は
  `engine_waking` ですらない裸の 400 で戻る）。
- **族が無視する物は報告し、飲まない。** `flux1`・`flux2-klein` への `cfg`、`flux2-klein` への
  `scheduler`、蒸留 3 族へのネガティブは、ネガティブが既にそうしている通りジョブに警告を出す。
  Console **も**その欄を灰にするが、自前の表でなく Agent の言葉から（決定 5）——2 つが食い違えない形にする。

### 決定 5 — メンバー向けカタログは `GET /imagegen/status` の拡張。CP に新ルートは作らない

- モデルごとに `family`（行の `base_model`）、`sizes`（行か既定 5 つ）、`params`（recipe ← 行の
  **実効**既定。フォームのプレースホルダが「実際に走る値」になる）、`negative`（行の物。利用者が外せない
  固定チップとして出す——管理者の物であり、`negative_always` も同様に出す）、`knobs`（族が読む
  `steps cfg sampler scheduler negative` の部分集合——テンプレートと同じ表から Agent が計算する。
  表が在る唯一の場所）、`warm`、`description`、`license_name`、`license_url`、`source_url`。
  LoRA ごとに `base_model`・`trained_words`——そして**既定の重みは無い** *（レビュー）*: 持つ列が無く、
  Agent は無指定なら 1 を使い（`comfy.go:342`）、`comfyMaxLoraWeight`（2・`comfy.go:292`）超を拒む。
  status はこれを `lora_weight_max` として 1 度だけ返す。エンジン単位で `samplers[]`・`schedulers[]`
  （許可リスト。フォームが Agent の拒む名前を出せないように）・`typical_ms`。
- 読む行は Agent が `GET /internal/engine/catalog`（`engineCatalogModelRow`）で既に受け取っている物
  ——MCP 経路が使う口、ワークスペースの発行トークン。CP にブラウザ認証の 2 つ目のカタログを作れば同じ行の
  2 つ目の投影を同期し続けることになる（`sessionWire` の教訓: 中継に無いフィールドは黙って消える）。
  その教訓はここで既に噛んでいる *（レビュー）*: `engineCatalogModelRow`（`engine_catalog.go:571`）が
  中継するのは `id`・`description`・`sizes`・`base_model`・`negative`・`params`・`selected`・`default`・
  `warm`・`files` で、`store.EngineModel` が持つ `LicenseName`・`LicenseURL`・`Source` は**載っていない**。
  よってこの 3 つを `license_name`・`license_url`・`source_url` として `trained_words` の隣に載せる——
  中継関数 1 つにフィールド 4 つ、新ルートは無し。Agent 側の読み手（`engines.go`）にも同じ 4 つを足す。
- `base_model_missing`・`files_missing`・`vae_missing` の行は**出さない**——カタログが既に生成から
  外している行で、ツールチップ付きの無効項目は管理者の画面であってメンバーの画面ではない。
- **`trained_words` を `engine_models` の列にする**（JSON 配列。両方言の移行——`d164739a` 時点で
  `migrations/` は `0067`、`migrations-pg/` は `0052`。番号はマージ直前に開いているレーン全部と
  突き合わせる。同じ番号のファイル 2 つを git は黙ってマージし、後で全テストが赤くなるから——*レビュー*:
  初稿は ADR 0072 に無い注記を指していた）。取り込みが Civitai の `trainedWords` から
  書き、管理者の行で編集でき、`engineCatalogModelRow` が中継する。列 1 つ・場所 3 つ。これ無しでは
  LoRA 利用者の誰もが求める 1 つのこと（決定 7）ができない。

### 決定 6 — ペイン種別 1 つ `imagegen`。下書きはローカル、真実はジョブ一覧

```ts
| { kind: "imagegen" }
```

- **モーダルでなくペイン**。ADR 0080 決定 1 の理由（ギャラリーやミラーと並べる・タブ・ポップアウト・
  レイアウト永続化・スマホの 1 ペイン）に 1 つ足す: 量産は 1 時間開けっぱなしにする画面である。
- **的の欄は無い。** `sameTarget` は「同じ種別」。2 度開けば在る 1 枚にフォーカスする。モデル 2 本で
  スタジオ 2 枚は、まだ出ていない要望（未解決 4）。
- **フォームの下書きは `localStorage`**（`af.imagegen-draft.<workspace>`）。コンポーザーの下書きと同じく
  変更のたびに書き、再読み込みで書きかけのプロンプトが残る。ペインの内容にはしない——レイアウト
  ストアは 2 KB のプロンプトの置き場でなく、下書きはレイアウトでなくブラウザ単位の物。
- **ジョブ一覧は Agent の物**（決定 2）。タブ切替でビューは unmount されるが、再マウントで一覧は
  1 ポーリング先にあり何も失わない——0078 の「タブ切替を生きるものは内容に置く」規則は、サーバに
  置くことで満たす。
- ペイン種別の登録 8 か所（union・`migrate.ts`・`sameTarget`・描画 switch・`paneTitle` 2 か所・
  `LayoutMap` 略号——`img` は #622 からギャラリーの物なので `gen`（*レビュー*）——・ポップアウト・i18n）に
  9 か所目: **コマンド表（ADR 0017）に `open.imagegen`
  （`g i`）を登録する**。ADR 0080 はフォルダが要るので登録できなかった。このペインは `open.sessions`
  と同じく引数が無い。
- i18n の接頭辞は `imggen.*`、新しいドメインファイルの対（`ja/imggen.ts`・`en/imggen.ts`）——
  カタログ試験が接頭辞 1 つをファイル 1 つに縛る。
- 導線は既存の並びに 1 行ずつ: 操作バーと `LayoutMap` のボタン（「セッション」の隣）、ギャラリーの
  ヘッダ「ここに生成」（本 ADR の P1——ギャラリーのペインは #622 から在る。`out_dir` をそのフォルダに
  してペインを開く）、ギャラリーのカード「画像生成で開く」（P1・サイドカーをフォームに読む）。

### 決定 7 — プロンプト支援は 2 層。常にある決定論の層と、利用者が押したときだけのモデル 1 回

**層 A——モデル無し・通信無し・常に在る。**

- **族のカード。** 選んだモデルの族について折り畳み 1 行で: 方言（`sdxl` とその Pony / Illustrious /
  NooBAI 系はタグ列、`flux1`・`flux2-klein`・`zimage`・`sd35` は自然文）、方言が期待する品質接頭辞
  （`masterpiece, best quality` / `score_9, score_8_up`——チップとして提示し、黙って挿さない）、
  ネガティブがこの族に届くか、推奨 steps / cfg の範囲、サイズのプリセット。族 5 つ・Console の i18n
  内容。チェックポイントごとのカードは未解決 2。
- **トリガー語。** LoRA を選ぶと `trained_words` がプロンプト欄のチップになる。LoRA を外すと、
  それが足したチップだけ消える。「LoRA が効かない」の最多はトリガー不足で、モデル呼び出しでは直らない。
- **管理者のネガティブと行の `params`** はフォームの固定部として出所付き（「管理者がこのモデルに
  宣言」）で出し、見えないところで混ぜない。
- **sampler と scheduler** は Agent の一覧からの select。族が読まない物は理由付きで無効表示。

**層 B——モデルを 1 回、ボタンで、着地前にプレビュー。**

- 「プロンプトを書いて」は Console が組んだメッセージ 1 通を `askAssistant()` に送る: 族のカード、
  モデルの `description`、選んだ LoRA のトリガー語、行のネガティブ、利用者が打った言語のままの意図。
  JSON `{prompt, negative, note}` を求める。答えは提案として見せ、「使う」「プロンプトだけ使う」
  「捨てる」——勝手には当てない。同じボタンの変種: 「3 案」「このモデルの方言に書き直す」（文→タグ列、
  またはその逆）、参照画像があるときは「画像をプロンプトとして記述」（vision——P2。アシスタント経路は
  今日ファイルを添付できない）。
- **なぜテナントの `llm` エンジンでなく `api/chat/ask` か。** ブラウザは `/engine/llm/*` に届かず
  （資格情報が無い）、その役が無い配備もあり、冷えた llama.cpp の箱は一文のために数分の GPU を使う。
  `askAssistant` は会員自身の CLI ログインで動き、メモ整理と TTS 要約が既にやっていることで、記帳される。
  「LLM が経路に居る」のは押したときだけで、アシスタントの名前はボタンに出る。CLI ログインが無い
  配備のために、ワークスペースのエンジントークンで `llm` を叩く Agent 側 `POST /imagegen/suggest` は
  P1 の選択肢（未解決 3）。
- **押さずにモデルを呼ぶことは無い。** ADR 0069 決定 8 の理由（見えないプラン消費）はそのまま当たる。

### 決定 8 — 量産は「N ジョブ」であって 1 つの大バッチではない。seed の方針とグループを持つ

- **フォームの「枚数」は N ジョブ**、1 枚ずつ、seed は下の方針、**グループ**として投入（全ジョブに
  1 つの `group` id。一覧はグループを 1 行に畳み「7 / 40 完了」。取消はジョブ単位でもグループ単位でも）。
  ComfyUI の `batch_size` > 1 は上級欄として残す（要求の `count`・上限 4）: 余裕のあるカードでは
  1 枚あたり速く、無いカードでは 5 分待った後の OOM で、1 枚ずつの取消も無い。
- **seed の方針:** `random`（既定）／`fixed`（全ジョブ同じ seed——「同じ絵で cfg を変える」の摘み）／
  `sequence`（基底 + i）。全結果に seed を出し、「この seed でもう一度」「新しい seed でもう一度」が
  結果の 2 ボタン。
- **キャッシュ警告は機能。** ComfyUI は同一グラフに約 0.5 秒でキャッシュの絵を返す（`comfyCacheWarning`）。
  ペインはこれを警告でなく「以前の実行と同一」として出す。`fixed` seed では「何か変わったか」への
  期待どおりの答えだから。
- **スイープとプロンプト行列は P1**（1 軸だけ違うジョブのグループ: cfg・steps・model・LoRA の重み、
  またはプロンプト内の `{a|b|c}` 択一。ギャラリーの P1「X/Y」配置がサイドカーの軸フィールドを読む）。
  グループとサイドカーは、P1 が wire フィールドを足さずに済む形にしておく。

### 決定 9 — 参照画像はワークスペースのディスクから。ブラウザのメモリからではない

- `edit` と `inpaint` は既に `inputs[]` を browse-root パスで取る。ペインは: ギャラリーから選ぶ
  （ADR 0080 のカードに「参照にする」・P1）、ファイルをドロップ（既存の `POST /fs/upload` で
  `generated/console/inputs/` に上げ、パスで参照）、パスを打つ。`strength` は既存の `Slider` が描く
  スライダー。マスクは P2（塗るには Console に無いキャンバスが要る。パスで渡すマスクファイルは初日から動く）。
- `edit` では入力自身の寸法が size 欄に勝ち、既存の警告が出る。フォームは size 欄を入力の寸法入りで
  無効表示し、利用者が警告でなく規則を読めるようにする。

### 決定 10 — エンジンの状態と費用は画面に、メンバーの言葉で

- ヘッダは次のいずれか: **準備済み**（温かいモデルがある）／**冷えている**（「最初のジョブで
  エンジンが起動。通常 N 分」——最後に観測した `waking` の長さを Agent が `typical_ms` と同様に持つ）／
  **起動中**（ジョブが `waking`）／**使えない**（`engine_off`・`engine_unavailable`・フリート provider
  無し——コード自身の文言付き）。拡張した status とジョブの段階から来る。管理者のエンジン行への
  メンバー向けルートは無く、このペインには要らない。
- **費用はテナントの GPU 時間で、ペインはそう言う**——`$0.00` を印字しない。comfy の `CostUSD` は
  構造上 0 で、帰属は管理者の時間別表にある。使用量ペインは既に受け取っていて出していない 2 つの
  カウンタ（`tool.imagegen` の `Images`・`Pixels`）を出し、メンバーが自分の量を見られるようにする。
  `usage_series.go` の畳み込み 1 つとラベル 2 つ。「影響」に列挙。

### 決定 11 — 試走はビューの中に置く。速い 1 枚を、キューの先頭で、フォームの隣に

量産するかどうかは、まず 1 枚を見て決める。その 1 枚が待機中の 40 ジョブの後ろに並んだり、ギャラリーに
探しに行く物だったりすると輪が切れ、人は見ずに投入する。だからペインには動詞が 2 つあり、同じボタンではない:

- **「試走」**（`Ctrl+Enter`・頻繁な方の操作）: 今のフォームから 1 ジョブ、`POST /imagegen/jobs` に
  `trial: true` を付けて。Agent は**キューの先頭に差し込む**——実行中のジョブの後ろ、待機中の全部の前——
  待つのは 1 枚分であってバッチ分ではない。試走の待機はワークスペースあたり 3 まで（4 つ目は 429
  `trial_pending` で断る）。先頭がそれ自体キューにならないように。
- **「N 枚を投入」**（`Ctrl+Shift+Enter`）: 決定 8 のグループ。末尾に、これまでどおり。

試走が要求を変えるのは次の点だけ:

- **`count` 1・`batch_size` 1。** 1 枚が目的。
- **steps は族の試走値**でフォームの値ではない——`sdxl` 10・`sd35` 12・`flux1` 8・`zimage` 4・
  `flux2-klein` 4（既に最小）。フォーム自身の steps は要求の `params.steps` に残し、サイドカーが
  「走った値」と「本番なら走る値」の両方を記録する。「フル steps」のチェックで減らさない——試走が
  そのまま本番の絵である場合のために。
- **size・seed・cfg・sampler・LoRA・ネガティブはフォームの値そのまま。** 構図は seed と size で決まり、
  別の size の試走は別の絵の予告になる。seed 方針が `random` のとき試走は 1 つ引いて**見せる**——
  「この seed を使う」でフォームに `fixed` として写す。スイープの前に試走する意味はまさにここにある。
- **出力は `generated/console/trial/`**——ギャラリーが他と同じく並べるサブフォルダ。試走の絵は定義上
  使い捨て（本番のバッチがフル steps で残す絵を作り直す）なので、**`trial/` だけは掃除が今も走り、
  7 日で消す**——決定 3 の例外で、矛盾に読まれないようここに書く。試走の結果の「残す」は同じ要求を
  フル steps で本フォルダへ投入し直すのであって、下書きを移動しない。

出る場所: **ペインの中、フォームの隣**——「直近の試走」枠にサムネイル（`downloadURL(path, 512)`。
ギャラリーとミラーと同じキャッシュ鍵）・seed・所要時間・警告を出し、クリックで共有ライトボックス。
その下のジョブ一覧は、このペインの全結果を同じカードで新しい順に出す。ビューはギャラリー無しで使え、
ギャラリーはフォルダとセッションを**横断して**見る面であって、今作った物を探す面ではない。一覧が
持つのはパスでありバイトではなく、カードはギャラリーと同じく遅延描画。

見積り（`typical_ms`）は model とサイズ帯に加えて steps でも鍵を取る。でないと試走が平均に
「バッチは速い」と教える。

### 決定 12 — バッチは進捗を見せ、一時停止・再開・飛ばし・中断ができる——グループ単位で、ジョブの境界で

40 枚は温かい箱でも 15 分かかる。見ている人は、どこまで来たかを知り、作った物を失わずに止められる
必要がある——1 分だけ（何か試すため）でも、それきりでも。4 つの動詞はすべて**グループ**の操作
（決定 8 の `group` id）。グループが、その人が投入した単位だから。キュー全体版は同じ操作を全グループに
掛けるだけ。

- **進捗は P0、WebSocket 無しで。** Agent はグループごとに `done`・`failed`・`total`・`running`
  （実行中のジョブと段階と `elapsed_ms`）・`eta_ms`（残り × その (model, サイズ帯, steps) の
  `typical_ms`、箱が冷えていれば観測済みの起床時間を足す）を返す。ペインはグループごとにバー 1 本
  ——完了・失敗・実行中を区分で描き、実行中の区分は `typical_ms` に対して時間で満ち、ジョブが本当に
  終わるまで 95 % で止まる。見ていない完了をバーが主張しないため。バーの上に「12 / 40・残り約 9 分・
  13 枚目をサンプリング中 18 秒」。1 枚の中のステップ単位のバーは **P1**（次項）で、初稿の P2 ではない。
- **ステップ単位の進捗は P1、中継した WebSocket で。** 上流は `progress`（ノードごとの step/total）と
  プレビュー画を `/ws` にしか流さない。CP はターミナルとブラウザペインのために既に WebSocket を中継
  している（`proxy.go` の `upgrader`）ので、ゲートウェイに `/engine/{key}/v1/ws` の upgrade 分岐を
  足すのは既存の型の上の新しいコードであって新しい基盤ではない。Agent はジョブ実行中だけ provider
  ごとにソケット 1 本を持ち、`progress` をジョブの `step`/`steps` に畳む。ブラウザへの運び手は
  ジョブ一覧のポーリングのまま——2 秒は人がバーを読むより細かい。
- **一時停止と再開はキューの境界の操作。** `POST /imagegen/groups/{id}` に `{"op":"pause"}` で
  グループに印を付け、ワーカーはそのジョブを飛ばして他の待機中（別グループ・試走）を回す。実行中の
  ジョブは**走り切る**——ComfyUI に pause は無く、中断したサンプリングは再開できず同じ seed で
  ステップ 0 からやり直すだけで、それは再開でなく繰り返しである。`"resume"` で印を消し、ジョブは
  位置を保つ。`POST /imagegen/queue {"op":"pause"}` / `"resume"` は全グループに同時に同じことをする。
  キューが止まっていても試走は走る——止める理由がそれだから。
- **飛ばす**（`{"op":"skip"}`）はそのグループの実行中ジョブを id 付き `/interrupt`（決定 2）で
  中断し、次を始める。飛ばしたジョブは `cancelled` で、再試行しない。
- **中断**（`{"op":"cancel"}`）は実行中を同じく中断し、そのグループの待機中を全部外す。できた絵は
  残る——グループは「40 枚中 12 枚」を付けて `cancelled`。1 枚だけの取消には決定 2 のジョブ単位の
  `DELETE` が残る。
- **止めたバッチは箱を冷やす。** エンジンのアイドル窓は管理者の物でこのペインの物ではない。窓を
  過ぎれば再開は起床になる。グループの行は決定 10 の起床見積りから「停止 6 分——エンジンは
  眠ったかもしれない」と言い、「再開」の後に続く数分に驚かせない。
- **wire。** `GET /imagegen/jobs` に `groups[]`（`id`・`label`・`state` ∈ `running | paused | done |
  cancelled`・件数・`eta_ms`・`paused_at`）。ジョブは `group` を保つ。`POST /imagegen/groups/{id}` と
  `POST /imagegen/queue` が決定 2 のリストに足す 2 ルート——決定 3 の `props` と合わせて中継は
  4 行でなく 7 行。

## 却下した案

- **既存のブロック型 `POST /imagegen/generate` をそのまま中継する。** コールドスタートで ALB の
  60 秒に死に、取り消せず、順番も出ず、キューをブラウザのメモリで回すことになる（決定 2）。
- **CP でグラフを組み、ブラウザにエンジン資格情報を渡す。** 存在しない資格情報、族ディスパッチの
  2 つ目、ギャラリーが見られない 2 つ目のストア（決定 1）。
- **ComfyUI の Web UI をブラウザペインに埋める。** 同じ資格情報の問題、カタログの迂回、スマホで使えず、
  テナントのネガティブと params が効かない。
- **Console から生の ComfyUI グラフを投げさせる**（「カスタムワークフロー」）。中継は運ぶ——CP は設計上
  本文を検閲しない——が、Agent のテンプレートこそが `base_model`・`params`・行のネガティブ・LoRA の
  族検査に意味を与える契約である。上級者向けのワークフロー機能は方針の問いを伴う別の ADR。
- **ジョブの置き場をペインの内容に。** レイアウトのリセットで消え、ポップアウト間で重複し、真実でない
  ——真実は Agent（決定 6）。
- **P0 で WebSocket のステップ進捗。** 中継の upgrade 分岐と Agent が持つソケットが要る。グループの
  バーと時間で満ちる区分が価値の大半を先に出す（決定 12）。
- **サンプリングの途中で pause。** ComfyUI にはできない。interrupt はステップ 0 からのやり直し。
  境界で止め、今の 1 枚に価値が無ければ飛ばす（決定 12）。
- **P0 のプロンプト支援をテナントの `llm` エンジンで。** ブラウザから届かない。Agent からなら P1 の
  選択肢（決定 7）。
- **品質タグやトリガー語をプロンプト本文に自動挿入。** 利用者の文への黙った編集は ADR 0069 決定 7 が
  サイズについて禁じた失敗。見えて外せるチップが形。
- **要求の不正な sampler 名を、カタログの被せ方と同じく黙って無視。** 人が打った値は大きく落ちる。
  被せ方の寛容さは管理者の古い行のためで、メンバーのフォームのためではない（決定 4）。
- **素の `POST /interrupt`。** 共有の箱で他のワークスペースの絵を殺す（決定 2）。
- **モデルごと・出力フォルダごとの別ペイン。** 要望が無い（未解決 4）。
- **プロパティをサムネイルから、またはマウント時に原本全部から読む。** サムネイルは JPEG 再エンコードで
  チャンクが無い。原本 300 枚は 0080 決定 4 が断った帯域（決定 3）。
- **0080 側にもプロパティの面を作る。** 記録 1 つ・読み手 1 つ・バー 1 つ——共有ライトボックスは既に
  3 つの面が出会う場所（決定 3）。
- **試走を末尾の普通のジョブにする。** バッチの後ろではバッチと同時に届き、そのバッチこそ試走で
  決めるはずの物だった（決定 11）。
- **速くするために試走を小さい size で。** 別の size は別の構図で、予告が何も予告しない。同じ size で
  steps を減らすのが正直な近道（決定 11）。
- **一時停止をジョブごとのフラグに。** 40 個の旗を立てて下ろすことになる。グループが投入した単位で、
  人が考える単位（決定 12）。

## 影響

- **Agent**（`workspace/agent`）: `internal/imagegen/` に `jobs.go`（キュー・ワーカー・グループ・EMA・
  取消）、`Request.Params`、`Result/StoredFile.Seed`、`store.go` のサイドカー、要求の検証、`comfy.go` の
  `sendWithWake` / `awaitHistory` の段階コールバック、comfy が実装する任意インタフェース `Canceller`、
  `knobs` / `samplers` / `schedulers` / `typical_ms` を持つ拡張 `statusResponse`、グループの状態とワーカーの
  pause / skip、`props` の読み手（サイドカー、無ければ PNG の `tEXt` を `IDAT` まで）。`routes.go` に 7 ルート
  （`testdata/routes.golden` が動く——ADR 0080 と違いここでは想定内）。`usage_series.go` が
  `Images` / `Pixels` を畳む。
- **CP**: `routes.go` の中継 7 行。`engine_models.trained_words`（両方言の移行）を取り込みが書き、
  管理者の行で編集でき、`engineCatalogModelRow` が `license_name` / `license_url` / `source_url`——ストアは
  既に持ち、行が運んでいなかった 3 つ *（レビュー）*——と共に中継。ゲートウェイの変更無し——取消は
  素通しのパスが 2 つ増えるだけ。
- **Console**: `features/imagegen/`（ビュー・フォーム・ジョブ一覧・族のカード・プロンプト支援モーダル・
  `open.ts`・CSS・下書きとグループ畳み込みの純関数）、登録 9 か所、i18n の対 `imggen.*`、導線
  ボタン 2 つ、`usage` の枚数とピクセルのラベル、`features/viewer/ImageLightbox.tsx` の `path` prop と
  プロパティバー。ギャラリーのペインは在る（#622）。その繋ぎ「画像生成で開く」「参照にする」は本 ADR の P1。
- **ドキュメント**: `guide/ref/features.{md,ja.md}` の行と `guide/member/` の手順、
  `workspace/agent/knowledge/af-usage.{md,coverage.tsv}`（docs-check が両方を強制する。
  セッションペインが踏んだとおり）。
- **新しい依存は無い。** スライダー・select・ライトボックス・サムネイル・一発実行のアシスタント呼び出し・
  アップロード経路は全部ある。
- 試験: 純関数（下書きの往復・グループの畳み込み・seed 方針・族カードの選択・プロンプト支援が
  解析する JSON）／DOM（`knobs` に無い欄が無効になる・チップが LoRA 選択に追従・提案はプレビューで
  自動適用しない・ジョブ単位とグループ単位の取消）／Go（キューの順序・上限の 429・待機中と実行中の
  取消と id 付き `/interrupt`・検証の拒否・サイドカーの中身・答えの seed・status のフィールド・
  ETag が効くバイトの安定・EMA）、そして P0 を完了と呼ぶ前に GPU の箱への実機 1 回——golden が pin
  するのはグラフの形で、走らせたことの無いグラフで緑の golden が何の価値だったかは ADR 0072 が記録している。

## フェーズ

- **P0**（決定 1〜12 から、各決定が P1 と名指した項目を除く）: ペイン、取消・グループ・試走・
  一時停止／再開／飛ばし／中断とグループの進捗バー付きのキュー、
  `params`、拡張 status、サイドカーと seed、族のカード、トリガー語のチップ、`api/chat/ask` 経由の
  「プロンプトを書いて」、パスかドロップによる edit、`out_dir`。ファイルを共有しない 3 レーン:
  - **レーン A（Agent）**: `jobs.go`（先頭差し込みと試走の上限込み）・`Request.Params`・検証・seed とサイドカー・`props` の読み手・段階と取消・
    status 拡張・ルートと golden・使用量の畳み込み。
  - **レーン B（CP）**: 中継の行・`trained_words` 列を端から端まで・`engineCatalogModelRow` の
    ライセンス／出典の 3 フィールド。
  - **レーン C（Console）**: ペイン種別と `features/imagegen/`（フォーム・試走枠・結果カード・ジョブ一覧）・
    共有ライトボックスのプロパティバーと `path` prop（引き上げは #622 で着地済み）・i18n・導線・使用量ラベル。
    C は A の wire のスタブ（上の形が契約）に対して組み、A の後に仕上げる。
- **P1**: スイープとプロンプト行列。ギャラリーの繋ぎ（「画像生成で開く」「参照にする」「ここに生成」）。
  プリセット（名前付きパラメータ集合・まずローカル）。再起動が痛んだらキューのジャーナル。`llm` 役経由の
  `POST /imagegen/suggest`。「エージェントに送る」（`chatCreate({attachPath})` は在る）。グループ完了の
  通知。族が粗すぎたらチェックポイントごとのプロンプト注記。**ステップ単位の進捗とプレビュー画**
  （中継した WebSocket・決定 12）。
- **P2**: マスクの塗り。
  「この画像を記述」（vision）。77 トークンの族向け CLIP トークン計数（ブラウザ内トークナイザ——
  バンドル量を秤にかける）。op としての `upscale`（カタログにアップスケーラの行種別が要る）。

## 未解決

1. **時間で満ちる区分は P1 まで正直でいられるか。** 実機の後に決める。`flux1` のコールドスタートの
   1 枚が、温かいときに測った「通常 40 秒ほど」のせいで 95 % に 50 秒座るなら、起床の見積りは ETA
   だけでなく最初のジョブの区分にも足す。
2. ~~族のカードか、チェックポイントごとのカードか。~~ **決着（2026-09-13）: 族のカード。**
   Pony・Illustrious・素の SDXL は 1 族に 3 方言。最初の本物の Pony 行で族カードが誤導するなら、
   P1 で行に `prompt_notes` を足す（管理者が書く。取り込みが `params_hint` の正規表現が既に読む
   Civitai の説明から種を蒔ける）。
3. **CLI ログインの無い会員。** 層 B は `api/chat/ask` で動き、それはワークスペースの中で会員自身の
   CLI（`claude -p`・`codex exec`・opencode・agy・cursor——選んだアシスタントが結び付いている物）を
   ヘッドレスに実行し、その CLI が既に持っているログインを使う。どれにもサインインしたことの無い
   会員、あるいは会員が自前エンジンしか使わないフリートには、走らせるアシスタントが無く、`llm` 役を
   通る P1 の Agent ルートができるまで層 A だけになる。そのルートを P0 に入れるかは、そういう
   フリートが実在するかで決まる。
4. **スタジオを同時に複数。** P0 の答えはワークスペースに 1 枚。モデル 2 本を並べる要望が出たら
   ペインに `slot` を足し `sameTarget` がそれを比べる。
5. ~~`generated/console/` の保持。~~ **決着（2026-09-13）: 掃除しない**（決定 3）。
6. **キューの上限と完了一覧の大きさ。** 200 と 500 は当て推量。制約は共有ホストのメモリ（完了ジョブが
   持つのはパスでありバイトでないので一覧は小さく、サイドカーがアーカイブ）。
7. **試走の steps（10 / 12 / 8 / 4 / 4）は当て推量。** 実機で構図が分かる最小の数にする。`trial/` の
   7 日掃除は**決着**（2026-09-13）。

## P0 の実装（2026-09-13）

3 レーン・3 本の PR。互いのファイルには触っていない: **A**（Agent）#626 `feat/0081-agent-jobs`、**B**（CP）#625
`feat/0081-cp-relay`、**C**（Console と docs）#627 `feat/0081-console-pane`。3 本とも GPU 無しで組んで試験した。
P0 の完了条件にある実機 1 回はまだ負っている。**契約は今やコードである**: Console が読むのは
`workspace/agent/internal/imagegen/{jobs.go,http.go}` の JSON タグで、上の決定と食い違う所はこの節が勝つ。

決定 2・3・5・11・12 に書いた wire から実装がずれた所と、その理由:

1. **走行中のジョブに `elapsed_ms` は無い**。`started_at` だけで、完了したジョブには `elapsed_ms` が載る。本文の中で
   動く数字はバッチが走っている間ずっと CP の ETag を外す——ミラーの電池の教訓。ブラウザが引き算する。
2. **LoRA の族は `baseModel` のまま**（camelCase・status が既に持っていた欄）。隣に `base_model` を足せば同じ事実の
   2 つ目の綴りになる。`trained_words` は書いたとおり snake_case。
3. **足したフィールド**: `wake_ms`（ジョブと status——決定 10 が欲しかった観測済みの冷間起動の置き場）、
   `full_steps`（ジョブとサイドカー——試走で steps を落としたときの本番の steps。`params.steps` に両方は入らない）、
   `GET /imagegen/jobs` の `queued` / `queue_max` / `trial_pending` / `trial_max`（429 の後でなく前にフォームがボタンを止める）。
4. **`count` が 4 を超えたら 400 `bad_count`**。決定は上限を書き、拒み方を書いていなかった。
5. **試走は `out_dir` を無視**して常に `generated/console/trial/` に置く。決定 11 のフォルダと決定 3 の欄が
   出会い、掃除される方が勝った。
6. **エンジン単位の欄**（`samplers[]`・`schedulers[]`・`typical_ms`・`wake_ms`・`lora_weight_max`）は status の
   根でなく provider の項に載る——status はもともと provider ごとである。
7. **`props`** は摘みを `params` に入れ子にし、ネガティブは `negative`。投入本文のネガティブは既存の
   `generateRequest` どおり `negativePrompt`。グループの `running` は件数でなく走っているジョブの id。
   止まったグループは `paused` と言う。
8. **「出さない行」**は Agent では「グラフが組めない行」（族の宣言が無い、または族が要る宣言済みファイルが
   足りない）。管理者の 3 フラグは Agent に届かず、`vae_missing` は宣言からは判別できない——フラグを中継する
   なら CP の変更で、今回はしていない。
9. **`source_url` は URL か無し**（B）: `civitai:<id>` は version id で `/models/<id>` は別モデルなので、CP だけが
   知る事実でリンクを組む。`url:` の出所は意図的に非リンク（その click は 22 GB の取得）。
10. **新しい誤り符号 17 個**を Console の `err.<code>` 目録に足した（`queue_full`・`trial_pending`・`bad_params`・
    `bad_count`・`no_job`・`no_group`・`cancel_failed`・`imagegen_no_provider`……）——この経路は中継されたことが
    無く、1 つも無かった。
11. **使用量ペインの `Images` / `Pixels` ラベルは未着手**（決定 10 の最後の項）: C を組んだ時点で A の
    `usage_series.go` の畳み込みが `develop` に無く、series にその欄が無かった。#626 が入った後に `UsageAgg` の
    2 欄とラベル 2 つ。

使った移行番号: `0067`（sqlite）と `0052`（pg）——マージ時に開いているレーンと再確認する。

実機だけが答える物（3 本の PR 本文から）: 試走の steps（未解決 7）。冷間起動を跨ぐ段階遷移・`wake_ms`・バーが
95 % に座る時間（未解決 1）。実行中の取消が本当に prompt を止めるか、そのとき `/history` が何と言うか。上流
`GET /queue` の要素配置（`server.py` 読み・実機では未見）。待機 200・保持 500 を抱えたときのメモリ（未解決 6）。
実応答に対する `knobs` と族カード。走行中バッチでの 304 の連鎖。サイドカーが無い過去の絵での `source: "png"`。
`generated/console/inputs/` への `POST /fs/upload`。
