# 0072. エンジンのモデルカタログ——LLM と SDXL のモデルを CloudFormation を回さずに差し替え、LoRA を要求で選ぶ

[English](0072-engine-model-catalog.md) | 日本語

- 状態: **草案（2026-09-08）。P0 に着手する前にレビューを受けるための文書。** 数字はすべて
  ADR 0071 の実測から引き、上流（llama.cpp・stable-diffusion.cpp）の仕様は同日にリポジトリの
  `tools/server/README.md`・`examples/server/api.md`・`docs/lora.md` から読んだ。**この文書の
  ために新しく測ったものは無い**——測ってから決めることは「未解決の点」に列挙し、決定の
  どれがそれに依存するかを各項に書いた。

## 背景

ADR 0071 は「フリートの箱で推論する」土台を P0・P1 で出荷した。llm 役は Qwen3-Coder-30B-A3B、
image 役は SDXL base 1.0 を**それぞれ 1 つ**載せて動く。要求は「モデルを差し替えられるように
したい。LoRA も適用したい」である。

### いまのコードが言っていること

- **モデルは CloudFormation のパラメータである。** `60-engines.yaml` の `LlmModelS3Key` /
  `LlmModelFile` / `LlmModelIds` / `LlmContextTokens` / `LlmMaxOutputTokens` / `LlmExtraArgs` と、
  image 役の鏡像 `ImageModelS3Key` / `ImageModelFile` / `ImageModelIds` / `ImageExtraArgs`。
  これらはタスク定義のコマンドライン（`-m /models/<file>`・`--alias`・`-c`）、fetch サイドカーの
  `aws s3 cp`、そして CP が読む SSM のエンジン表（`models[]`・`contextTokens`）の 3 箇所へ
  流れる。**差し替え＝`params/60-engines` を編集して取り込みタスクを手で叩き、`standup.sh` か
  `update.sh` でスタックを更新する**（`deploy/aws/ecs/README.md`「Getting a model into the
  catalogue」）。
- **CP はエンジン表を起動時に一度だけ読む**（`engines.go`: 「Read ONCE, at startup, on
  purpose」）。表が変わるのはスタックが変わるときで、スタック更新は CP タスクを置き換えるから、
  という前提である。Agent はブート時に `/internal/engine/catalog` を読んで opencode の
  `provider` ブロックを書き、10 分 TTL でキャッシュする。
- **provider `sdcpp` は `model` を送らない。** `sdcpp.go`: 「the server holds ONE model, chosen
  by a startup flag, and has no way to switch at request time」。`Caps.Sizes` はモデル id の
  文字列（`xl` / `1-5`）から**推定**している。`generate_image` の引数は op / provider / prompt /
  size / aspectRatio / background / count / inputs / mask で、**model も lora も無い**。
- **ゲートウェイはパスと本文を素通しする**（`dial()`: 「passed through verbatim」）。本文を
  読むのは `stream_options.include_usage` を足すときだけである。
- **窓はエンジン毎**（0071「P1 の後の訂正」）: llama-server は 1 プロセス 1 GGUF 1 `-c` なので、
  `LlmModelIds` の複数 id は同じ窓の別名にすぎず、窓の違うモデルは**行を 2 つ・provider id も
  2 つ**にするしかない。
- **51,200 バイトの壁。** `60-engines.yaml` は 1,065 行で、散文を `PARAMETERS-60-engines.md` に
  逃がしてなお壁の近くにいる（`30-ingress` に残った 9.2 KB を理由にエンジン表を SSM へ出した
  のが決定 8）。**モデルごとに CloudFormation の資源やパラメータを足す設計は、最初から無い。**

### 上流が持っているもの（2026-09-08 に確認）

**llama-server（llama.cpp）**

- **ルーターモード。** `--models-dir PATH`（ディレクトリのファイル名がモデル名。mmproj や
  分割 GGUF はサブディレクトリ名がモデル名）、`--models-preset PATH`（INI。`[*]` が全体既定、
  `[<id>]` がモデル毎で、キーはコマンドライン引数の名前——`c`・`n-gpu-layers`・`lora`・
  `alias`・`model`（パス）——に加えて preset 専用の `load-on-startup`・`stop-timeout`・
  `dedup-cache-models`）、`--models-max N`（同時ロード数。**既定 4、0 で無制限**）、
  `--models-autoload`（既定 on。「the model will be loaded automatically if it's not
  loaded」）。要求は本文の `model`（GET は `?model=`）で経路が決まり、`GET /models` が各モデルの
  `status: {value: "loaded" | …, args: […]}` を返す。優先順位はコマンドライン＞モデル毎の
  preset＞`[*]`。
- **LoRA。** `--lora FNAME`（カンマ区切りで複数）、`--lora-scaled FNAME:SCALE`、
  `--lora-init-without-apply`、`GET /lora-adapters`（`id`・`path`・`scale`）、
  `POST /lora-adapters`（全体のスケール）、要求ごとの `lora: [{"id":0,"scale":0.5}]`
  （「Requests with different LoRA configurations will not be batched together」）。
- 🔴 README が言っていないこと: ルーターの `/health` がモデル 0 個で ok を返すか、autoload
  中の要求が**待たされる**のか蹴られるのか、`--models-max` を超えたとき**どれを降ろす**か、
  子プロセスとポートの割り当て。決定 3 はこの 4 点の実測に依存する。

**sd-server（stable-diffusion.cpp）**

- **1 プロセス 1 チェックポイント。実行時に切り替える口は無い。** `/v1/images/generations` に
  `model` の意味は無く、`GET /v1/models` は固定の `sd-cpp-local` を返し、`GET /sdapi/v1/sd-models`
  も載っている 1 つを返すだけで、`POST /sdapi/v1/options` は無い。
- **LoRA は構造化フィールドで渡す。** `--lora-model-dir DIR` に置き、`GET /sdapi/v1/loras` と
  `GET /sdcpp/v1/capabilities` の `loras[]` が `name`・`path` で列挙する。要求は sdcpp API の
  `lora: [{"path":…, "multiplier":…, "is_high_noise":…}]`。**OpenAI 互換の面でも渡せる**——
  prompt の中に `<sd_cpp_extra_args>{"lora":[…]}</sd_cpp_extra_args>` を埋めると、サーバが
  JSON を取り出して sdcpp API と同じ規則で読み、prompt からその部分を除いて生成する
  （`sample_params.sample_steps`・`seed`・`negative_prompt` も同じ穴から通る）。
  🔴 **`<lora:name:1>` の prompt 構文はサーバでは意図的に無効**（api.md: 「intentionally
  unsupported in OpenAI API, sdapi, and sdcpp API」）。CLI の `docs/lora.md` はその構文を
  使うので、CLI の知識で書くと**黙って無視される**。
- **LoRA の適用方式** `--lora-apply-mode immediately | at_runtime`。既定は自動——重みに量子化
  パラメータがあれば `at_runtime`（互換性と精度、遅く VRAM が増える）、なければ `immediately`
  （ロード時に重みへマージ、速い）。SDXL fp16 は後者に落ちる。**要求ごとに LoRA の組が変わる
  ときのコスト（再マージか）は書かれていない**——決定 5 が依存する実測。
- **分割モデル。** FLUX / SD3 は `--diffusion-model` + `--clip_l` + `--t5xxl` + `--vae` の
  複数ファイル。`--type` で実行時量子化、`--offload-to-cpu` で VRAM を逃がす（0071 決定 2 の
  P4 と同じ未測）。
- 出力は `/v1/images/generations` が同期（「synchronous from the HTTP client's
  perspective」）で、非同期の `/sdcpp/v1/img_gen` はコンテナ内で使えない（0071 実測）。

**ComfyUI（P2 予定）** は `models/checkpoints`・`models/loras`・`models/vae`・
`models/text_encoders`・`models/diffusion_models` の配置を読み、ワークフロー毎にチェック
ポイントも LoRA も選べる。0071 決定 6 は**この配置を 3 エンジン共通のモデル置き場の規約**と
決めている。

## 決定

1. **モデルはスタックの資源ではなく、データである。** スタックが持つのは役の**器**——capacity
   provider・サービス・SG・バケット・タスク定義・取り込みタスク——だけで、**何を載せるかは
   カタログが言う**。`*ModelS3Key` / `*ModelFile` / `*ModelIds` / `*ContextTokens` /
   `*MaxOutputTokens` / `*ExtraArgs` の 12 パラメータは廃止方向（互換のため 1 リリース残し、
   カタログが空のときの**種**として読む。決定 7）。理由が 3 つ: (a) 51,200 バイトの壁——
   モデル 1 つに 6 パラメータ、行を分ける方式ならサービス一式で、どちらも足せない；
   (b) 「差し替え」は運用の動作であって配備の動作ではない——GPU が寝ている夜に管理者が
   Console で切り替えるものを、CloudFormation の変更セットにしない；(c) ADR 0053 の
   「宣言し、導出しない」は**誰が宣言するか**を言っていない。宣言の場所を CFN から CP のカタログへ
   移すだけで、エンジンに聞かない原則はそのまま守る（決定 2）。
   - **二段階スタンドアップ（0071 P0 の実測）が消える。** 今は「モデルがバケットに無いと
     サービスが安定しない」ために `<Role>ModelS3Key` 空＝サービス無しで一度建て、取り込んで
     から建て直す。カタログ方式では fetch サイドカーが「載せるものが無い」を**正常終了**とし、
     llm 役は空の `--models-dir` で listen（ルーターはモデル 0 個で起動できる——未解決 1 で
     確認）、image 役は `sleep infinity` のプレースホルダで RUNNING になる。サービスは安定し、
     ゲートウェイは**カタログが空なら起こさない**（`503 engine_unavailable`「このロールに
     有効なモデルが無い」）。パラメータは `LlmEnabled` / `ImageEnabled` の 2 つに縮む。

2. **カタログの真実は 2 層。S3 が「何があるか」、CP の DB が「どう出すか」。エンジンには聞かない。**
   - **S3 のレイアウトは ComfyUI の規約**（0071 決定 6）: `llm/<name>.gguf`（分割は
     `llm/<name>/`）、`llm/loras/`、`image/checkpoints/`、`image/loras/`、`image/vae/`、
     `image/text_encoders/`、`image/diffusion_models/`。既存の `image/sd_xl_base_1.0.safetensors`
     は `image/checkpoints/` へ移す（S3 内のサーバサイドコピーで、NAT を通らない）。
   - **各ファイルの隣にマニフェスト `<file>.json`** を取り込みジョブが書く: `sha256`・`bytes`・
     `source`（URL、HF の repo id とリビジョン、または Civitai の version id）・`license`
     （HF の `cardData.license`）・`kind`（`gguf` / `checkpoint` / `lora` / `vae` /
     `text_encoder` / `diffusion_model`）・`baseModel`（`sdxl` / `sd15` / `flux` / …。
     LoRA の**適合先**であり、取り込み時に運用者が宣言する）・`ingestedAt`。「ファイルが
     ある＝使える」ではなく、**マニフェストが揃っているものだけ**がカタログの候補になる
     （0071 決定 3 の「増えた＝成功を信じない」と同じ）。
   - **CP の DB に新表 `engine_models`**: `id`（利用者が選ぶ名前——`qwen3-coder-30b-a3b`・
     `sdxl-base-1.0`）、`role`（`llm` / `image`）、`kind`、`files[]`（S3 キーと、分割モデルなら
     どのフラグに渡すか）、`enabled`、`args[]`（モデル毎の追加フラグ——`--vae`・`--type q8_0`・
     `--offload-to-cpu`）、`contextTokens` / `maxOutputTokens`（llm。**モデル毎**になる。
     決定 3）、`sizes[]`（image。`sdcppSizes()` の推定を置き換える宣言）、`description`
     （**エージェントが読む 1 行**——LoRA なら「何の絵になるか」）、`vramMiB`（任意。運用者の
     実測値で、無ければ無い）、`lastUsedAt`。
   - **「いま箱に載せるもの」（active set）は CP が SSM に書く**: `/af-ws/engines/<key>/active`
     に、有効なモデルと LoRA の S3 キー・生成する preset の材料を JSON で。CP のタスクロールは
     **既に** `/af-ws/*` への `ssm:PutParameter` を持つ（`20-platform` の
     `SsmWorkspaceParams`）ので CP 側の IAM 追加は無く、箱側は `EngineTaskRole` に
     `ssm:GetParameter`（同じパスのみ）を **`60-engines` の中で**足す（0071 決定 8 の
     「新しい IAM はスタックの中で閉じる」）。fetch サイドカーはこれを読んで同期し、preset を
     組み、コマンドラインを作る。SSM を選んだ理由: CP に S3 の書きを与える案（バケット
     ポリシー）と比べ、**既に持っている権限**で済み、値が小さく（数 KB）、そして箱が読む瞬間に
     CP が生きている必要が無い。
   - 0071 のエンジン表（SSM 1 本、起動時読み）は**基盤の表として残る**——サービス名・URL・健診・
     capacity provider・idle・deadline・mode。`models[]` と `contextTokens` はカタログへ
     移り、表にあれば決定 7 の種として読む。

3. **llm 役はルーターモードで動かし、モデルは要求の `model` で選ぶ。窓はモデル毎になる。**
   `llama-server --models-dir /models/llm --models-preset /models/llm/presets.ini
   --models-max <LlmModelsMax>`。preset は fetch サイドカーが active set から生成する
   （セクション名＝カタログの id、`model`＝パス、`c`・`n-gpu-layers`・`alias`・固定 LoRA）。
   `-m` と `--alias` と `-c` はコマンドラインから消える。
   - **0071「P1 の後の訂正」の制約が消える。** 窓はモデル毎に `c` で宣言でき、カタログは
     モデル毎に `contextTokens` / `maxOutputTokens` を持ち、Agent は opencode の `models.<id>.limit`
     を**モデル毎に**書く（`engineProviderEntry` は既にモデル単位で書いているので、値を
     モデルから取るだけ）。「窓の違うモデルは行を 2 つ・provider id も 2 つ」は不要になる。
   - **`LlmModelsMax` の既定は 1。** ルーターは VRAM を知らない。L4（22.9 GB）に 30B Q4
     （20.9 GB）は 1 つしか入らず、2 つ目を載せれば CUDA は遅くなるのではなく**落ちる**
     （0071 決定 2）。1 なら交替は「降ろして載せる」＝ **267 秒のリロード**（0071 実測: VRAM へ
     18.5 GB）で、落ちはしない。2 セッションが別モデルを交互に使えば毎回 267 秒——それは
     **この設計の価格**であり、隠さず admin パネルに「モデルの交替が N 回」を出す。小さい
     モデルを複数載せる配備は運用者が 2 以上にする（カタログの `vramMiB` の和が箱に入るかは
     運用者の判断で、CP は足し算を手伝う表示だけする）。
   - **`warm` の定義が変わる。** 今は `/health` ok＝重みが VRAM にある。ルーターでは
     `GET /models` で**既定モデルが `loaded`** を warm とし、健診は `/health` のまま
     （未解決 1 で `/health` の意味を測る）。既定モデル＝カタログで `default` を付けた 1 つで、
     preset の `load-on-startup = true` にする。起動時に載せないと、最初の要求が「箱の起動
     527 秒＋ロード 267 秒」を払う。
   - **autoload 中の要求は心拍で握る。** ゲートウェイの streaming 経路は上流の最初のバイトまで
     10 秒ごとの SSE コメントを流す（0071 決定 5）ので、ルーターが要求を待たせる実装なら
     追加の機構は要らない。蹴る実装（未解決 1）なら、ゲートウェイが `/models/load` を叩いて
     `loaded` まで待ってから転送する——どちらでも心拍の外へ出ない。
   - この決定は未解決 1（4 点）の実測に依存する。ルーターが要件を満たさなければ、代替は
     「active set のうち**選択中の 1 つ**を `-m` で載せ、差し替えは image 役と同じく次回起動」
     （決定 4 の形）——モデル毎の窓はカタログに残り、同時に載るのが 1 つになるだけで、
     カタログの設計は変わらない。

4. **image 役は 1 チェックポイントを保持し、差し替えは「次の起動」で効く。同時に 1 つ。**
   sd-server に切替の口は無く、役を増やす案は壁で、同居は VRAM で退けられる。だから
   image のカタログのうち **`selected` は 1 つ**で、管理者が選ぶ。
   - **停止中**（普段の状態——$1.26/時の箱は寝ている）に変えれば、次の要求が新しい
     チェックポイントで起きる。**追加の待ちは無い**——起動のたびに S3 から引く（0071 決定 3）
     のは今日と同じで、6.9 GB は 45〜60 秒である。
   - **起動中**に変えたときは、admin パネルが選ばせる: 「次に止まったとき」（既定）か
     「いま再起動する——生成中の要求は失敗する」。黙って再起動しない: 誰かの 21 秒の生成を
     管理者の 1 クリックが殺すのを、パネルは事前に言う。実装は `desired 0 → 1` ではなく
     `UpdateService --force-new-deployment`（CP は既に `UpdateService` を持つ）で、controller
     の状態機械（`starting` の期限・クールダウン）をそのまま通す。
   - **分割モデルは 1 エントリ。** FLUX は `files[]` に `diffusion_model` / `clip_l` / `t5xxl` /
     `vae` の役割付きで並び、サイドカーが `--diffusion-model` 以下のフラグを組む。`--type` と
     `--offload-to-cpu` は `args[]`。g6f への縮小（0071 P4）はこの `args[]` に書く 1 行に
     なる。
   - **ComfyUI（0071 P2）が入れば、この決定の「1 つ」は消える。** ComfyUI はワークフロー毎に
     チェックポイントを選ぶので、同じカタログ・同じ S3 配置のまま `selected` が
     「要求毎」になる。カタログをエンジン非依存に設計するのはそのためで、**sd-server の制約を
     カタログの形に焼き込まない**。

5. **LoRA はカタログの項目であり、要求で選ぶ。エージェントが選べるのはカタログにある名前だけ。**
   - **image。** 箱は `image/loras/` のうち**有効なもの**を同期し、`--lora-model-dir
     /models/image/loras` で起動する。`generate_image` に引数を 2 つ足す: `model`（enum＝
     有効なチェックポイント。provider `sdcpp` では**今日は 1 つ**——決定 4——だが、Codex /
     agy 経路と ComfyUI のために引数の形は今から複数を許す）と `loras: [{name, weight}]`
     （`name` の enum＝**選択中のチェックポイントと `baseModel` が一致する** LoRA だけ。
     `weight` は 0〜2、既定 1）。ツールの説明文にカタログの `description` を並べ、
     エージェントが「水彩風なら `watercolor-v2`」と選べるようにする。
     - Agent の provider `sdcpp` が prompt の末尾に
       `<sd_cpp_extra_args>{"lora":[{"path":"<file>","multiplier":<w>}]}</sd_cpp_extra_args>`
       を付ける。**ゲートウェイは触らない**（素通しのまま。決定 4 の「本文を読むのは usage
       のときだけ」を守る）。⚠️ **利用者の prompt に既に `<sd_cpp_extra_args>` があれば拒否
       する**——この穴からは `seed` や `sample_steps` だけでなく、サーバのファイル
       パスに対する `lora.path` が通る。prompt はモデルが書くものであり、モデルが読んだ
       ものは何であれ prompt に現れうる（0071 決定 4(d) と同じ姿勢）。
     - `baseModel` が合わない LoRA（SD1.5 の LoRA を SDXL に）は sd.cpp が**黙って崩れた絵**を
       出すか、テンソル名の不一致を警告して無視する。どちらも利用者には「効かない」としか
       見えないので、**Agent が enum で出さず、CP が要求で拒否する**。
     - この決定は未解決 2 に依存する: `immediately` モードで要求ごとに LoRA の組が変わる
       コスト（再マージなら秒単位で、生成 21 秒に対して払える額か）と、`at_runtime` の
       VRAM 増分（7.4 GB＋α が L4 に入るのは確実だが、g6f には入らないかもしれない）。
       結果で `--lora-apply-mode` をカタログの `args[]` の既定に書く。
   - **llm。** 段階を 2 つに分ける。**先に preset の固定 LoRA**（カタログのモデル毎に
     `loras[]`＝`lora = <path>` を preset に書く。「このモデルはこの微調整込み」という宣言で、
     opencode からは普通のモデル id に見える）。**後に仮想モデル id**（`<base>+<lora-set>` を
     カタログの別エントリとし、ゲートウェイが `model` を base に書き換えて要求の
     `lora: [{id, scale}]` を足す）。後者を後にする理由: AI SDK の openai-compatible provider は
     `lora` を送らないので**ゲートウェイが本文を書き換える**ことになり、`dial()` の
     「素通し」を破る最初の例になる。usage のために本文を読む前例はあるが、書き換えは別の
     約束で、必要が測られてから破る。
   - **LoRA は取り込みの対象**（決定 6）。HF にも Civitai にもあり、SDXL の LoRA は 50〜400 MB
     で、同期の時間には効かない。ライセンスはチェックポイントと同じ扱い（0071 決定 11）。

6. **取り込みは CP の仕事になり、Console から起動する。手打ちの `run-task` は残るが主経路ではない。**
   `POST /api/admin/engines/models/ingest`（super_admin）に `source`（`{url}` /
   `{hf: {repo, file, revision}}` / `{civitai: {versionId, file}}`）・`role`・`kind`・
   `id`・`baseModel`・`licenseAccepted: true` を受け、CP が:
   - **HF API から `sha256` と `license` を解決**（`?blobs=true` の `siblings[].lfs.sha256`、
     `cardData.license`。0071 実測で照合済み）。gated（`gated: auto`）は 401/403 で知り、
     「HF でライセンスに同意してから」と**その文言で**失敗を名乗る（0071 決定 11）。
   - **`ecs:RunTask` で取り込みタスクを起動**する。IAM は CP のロールに `ecs:RunTask`
     （取り込みの family に限る）と `iam:PassRole`（取り込みのタスクロールと実行ロール）が要る。
     0071 決定 8 の「CP の IAM 追加はゼロ」を**ここで初めて破る**が、置き場は
     `60-engines` の中の `AWS::IAM::Policy`（`Roles:` に `20-platform` の `CpTaskRoleArn` から
     切り出したロール名）で、**スタックを採用しない配備の CP は何も増えない**。RunTask に要る
     subnets と SG は `60-engines` がエンジン表の `ingest` ブロックに書く。
   - 完了は `DescribeTasks` で追い、`upload` コンテナがマニフェスト（決定 2）を書いてから
     `engine_models` に行を作る（`enabled: false`——**取り込めた＝出る、ではない**。管理者が
     有効にする）。進行中・失敗理由（sha256 不一致・401・容量）はパネルの行に出す。
   - **Civitai**（SDXL の LoRA の事実上の出所）: ダウンロードは `Authorization: Bearer` の
     API キー、sha256 は `model-versions` API の `files[].hashes.SHA256`。キーは HF と同じく
     Secrets Manager の運用者の秘密で、取り込みタスクだけが読む。🔴 **Civitai の API 仕様は
     本日確認できなかった**（開発者サイトが 404）——未解決 4。確認できるまで `{url}` に
     sha256 を添える経路で通す。
   - 途中段階として `harness/ingest-model.sh <role> <hf-repo> <file>` を先に置く——sha256 と
     license の解決と `run-task` の組み立てを自動化するだけの薄いもので、Console 経路が
     できるまでの運用者の道具。

7. **カタログは動的で、読者が 3 人いる。エンジン表は静的のまま。**
   - **ゲートウェイ**は要求のたびにカタログを読む（DB。プロセス内キャッシュ 10 秒）。llm では
     `model` がカタログに無ければ `404 model_unknown`（ルーターにディスクのファイル名で
     当てさせない——**カタログに無いものは無い**）。
   - **Agent** の `/internal/engine/catalog` はカタログから答え、行にモデル毎の
     `context_tokens` / `max_output_tokens` / `description` と、image では `models[]` に加えて
     `loras[]`（name・description・baseModel）を持つ。10 分の TTL は残すが、**CP が変更時に
     各 Workspace の Agent へ `POST /engine/catalog-changed` を打つ**（usage を返す
     `POST /engine/usage` と同じ逆経路・同じ認証）。Agent は `syncEngineProviders()` を
     再実行し、`ApplyEngineChange()` で走行中の serve デーモンに config を読み直させる——
     この関数は「for a catalogue that changes later」のために**既に**ある。押し通知が届か
     なかった Workspace は TTL で追いつく。
   - **admin パネル**（`GET /api/admin/engines`）の行に `models[]` を**カタログの姿**で出す:
     id・enabled・selected（image）・default（llm）・窓・`vramMiB`・最終使用・ライセンス。
     操作は有効/無効・選択・既定・削除（S3 のファイルも消す。**マニフェストを先に消す**——
     ファイルが先に消えると候補に残ったまま同期が失敗する）。決定 13（0071）の「モデルは
     スタックの宣言」は「モデルはカタログの宣言」に読み替え、`warm` の意味は決定 3 のとおり。
   - **種（決定 1 の互換）**: CP 起動時にカタログが空で、エンジン表に `models[]` があれば、
     その id・`contextTokens` と、`60-engines` が表に書き足す `modelS3Key` から**1 行だけ**
     作る。今日の配備は CP を上げた瞬間に今日のモデルがカタログにあり、何も変わらない。

8. **由来と使用量はモデルと LoRA を持つ。** `generate_image` の結果の `model` はチェック
   ポイント id、`provenance` に `loras: [{name, weight}]` と `sha256`（マニフェストから。
   0071 決定 10 の「ファイル名と sha256」）。llm の usage 行の `model` はルーターが応答に
   返す名前（＝カタログ id）で、`engine_hourly`（0071 決定 13）には触らない——稼働は箱の
   性質で、モデルの性質ではない。

9. **コールドスタートへの影響は数で言い、同期は有効なものだけにする。** S3 → EBS は
   104〜147 MB/s で一定（0071 実測 8）だから、llm の同期時間は**有効な GGUF の合計サイズで
   決まる**——18.5 GB ごとに 179 秒。有効化は「載せる」であって「取り込む」ではないので、
   カタログに 5 つあっても有効が 1 つなら今日と同じ 527 秒である。admin パネルは有効化の
   トグルの横に「同期 +N 秒（推定）」を出す（サイズはマニフェストにある。`observed_secs` の
   ときと同じで、**推定と書く**）。`useLocalStorage`（0071 未解決 1）は据え置き。

## 却下した案

- **モデルごとに `60-engines` の行（サービス一式）を足す。** 壁で入らず、入っても寝ている
  サービスが役の数だけ並び、2 モデル同時に起きれば箱も 2 台。llm はルーターが、image は
  「次の起動」が、同じ箱で答える。
- **差し替えをタスク定義の新リビジョンで行う。** CP に `RegisterTaskDefinition` を与え、
  CloudFormation が持つタスク定義とドリフトする。CFN の資源を CP が上書きすると、次の
  スタック更新が黙って戻す。
- **`/v1/images/generations` の `model` で切り替える。** sd-server にその意味は無い（上流 api.md）。
  `model` を送れば「切り替えられる」と示唆することになる——今日の `sdcpp.go` が送らない理由。
- **prompt の `<lora:name:1>` 構文。** サーバは意図的に無視する（上流 api.md）。CLI の文書が
  その構文で書かれているので、**最初に踏むのはこれ**である。
- **エージェントに LoRA のパスや任意の重みを自由に書かせる。** `lora.path` はサーバの
  ファイルパスで、カタログの外を指せる。enum だけ。
- **カタログを S3 の一覧から導出する（`ListObjects` で `/v1/models` を作る）。** 「ある」と
  「出す」は別で（取り込めたが未検証・ライセンス未受諾・VRAM に入らない）、ADR 0053 の
  「導出しない」に反する。マニフェストと `enabled` の 2 段。
- **箱が HF / Civitai から直接引く。** 0071 決定 3 のまま（速度が 4〜236 MB/s で読めず、
  トークンが箱に載る）。
- **ゲートウェイでカタログを SSM から読む。** 要求ごとの SSM 呼び出し（`engines.go` が起動時
  1 回にした理由）。SSM に書くのは**箱が読む active set** だけで、CP 自身は DB を読む。
- **EFS にカタログを置く。** 0071 で却下済み。

## 未解決の点——P0 の前に 1・2 を、P1 の前に 3 を測る

1. **ルーターモードの 4 点**（決定 3 が依存）: (a) `--models-dir` が空でも起動して `/health` が
   ok か（決定 1 の「空で安定」も依存）；(b) autoload 中の要求は待つのか蹴られるのか、待つなら
   最初のバイトまでの無音が心拍で覆えるか；(c) `--models-max 1` で 2 つ目を要求したとき、
   1 つ目が降りて 2 つ目が載るか（落ちないか）；(d) `stream_options.include_usage` の `usage`
   と応答の `model` がルーター経由でも届くか（決定 8）。CPU の 1.1 GB モデルで (a)(b)(d) は
   このコンテナで測れる。(c) は GPU が要る。
2. **sd-server の LoRA**（決定 5 が依存）: `immediately` で要求ごとに組が変わるコスト、
   `at_runtime` の VRAM、`<sd_cpp_extra_args>` が `/v1/images/edits`（multipart）でも効くか、
   `baseModel` 不一致のときの挙動（黙って崩れるのか、警告か）。CPU の SD1.5 Q4 で
   `<sd_cpp_extra_args>` の経路と不一致の挙動は測れる。
3. **サービス起動後の追加同期。** 有効化したモデルを、走っている箱に**次の起動を待たず**
   足せるか——サイドカーを常駐させて active set を再同期し、ルーターが `--models-dir` を
   再走査するか（しないなら `/models` の再読込の口があるか）。無ければ「llm も次の起動」で
   始め、これは P4。
4. **Civitai の API**（決定 6）: 認証の形（ヘッダか `?token=` か）、`files[].hashes.SHA256`、
   `model.type` と `baseModel` の値。
5. **`engine_models` を DB に置くか設定ストアに置くか。** 行数は数十、参照は要求毎——
   `SettingsStore` の JSON 1 本でも足りる。表にするのは `lastUsedAt` を要求のたびに書くから
   で、それが設定ストアの書き込み頻度として許されるかで決まる（`engine_<key>_demand_at` は
   1 分に 1 回に抑えている前例）。

## フェーズ

- **P0 — カタログの土台。** `engine_models` と種、マニフェスト、S3 レイアウトの移行、active set
  の SSM と fetch サイドカーの一般化（役に依らない 1 本のスクリプト——同期・preset・
  コマンドライン）、`LlmEnabled` / `ImageEnabled` への縮退と旧パラメータの互換、admin API
  （一覧・有効化・選択）とパネルの `models[]`、`/internal/engine/catalog` のカタログ化と
  `catalog-changed` の押し通知、`sdcpp` の `Caps.Sizes` を宣言から。**完了の定義: image の
  チェックポイントを Console で選び直し、CloudFormation を触らずに次の起動で新しい絵が返る。
  `describe-stacks` の最終更新時刻が動いていないことがその証拠。**
- **P1 — llm のルーターモード。** 未解決 1 を測ってから。preset 生成、モデル毎の窓、
  `LlmModelsMax`、`warm` の再定義、opencode の provider をモデル毎の `limit` で。**完了の
  定義: 起動メニューに `llamacpp/` のモデルが 2 つ出て、片方ずつ使え、交替のリロードが
  1 回の試行で答えを返す**（0071 P0 の完了の定義と同じ観測を交替で行う）。
- **P2 — LoRA。** 未解決 2 を測ってから。image の `loras/` 同期・`generate_image` の `model` /
  `loras`・`sdcpp` の `<sd_cpp_extra_args>`・拒否規則；llm の preset 固定 LoRA。**完了の定義:
  同じ prompt・同じ seed で LoRA の有無が絵を変え、SD1.5 の LoRA が SDXL で enum に出ない。**
- **P3 — Console からの取り込み。** `ingest` API、RunTask の IAM（`60-engines` 内）、HF の
  sha256 / license 解決、ライセンス受諾 UI、進行と失敗の表示、Civitai（未解決 4 の後）。
  それまでは `harness/ingest-model.sh`。
- **P4 — 走行中の追加同期（未解決 3）、llm の仮想モデル id（決定 5 の後半）、ComfyUI との
  統合（0071 P2 と同じカタログを読ませる）。**

## 確認した出典（2026-09-08）

- llama.cpp `tools/server/README.md`（ルーターモードの節、LoRA の節、`--alias`）
- stable-diffusion.cpp `examples/server/api.md`（3 つの API 面、`sd_cpp_extra_args`、
  `lora[]` の形、`<lora:>` 構文の不採用、`/v1/models` の固定応答）、`examples/server/README.md`、
  `docs/lora.md`（`--lora-model-dir`、`--lora-apply-mode`）、`README.md`（`--diffusion-model` /
  `--clip_l` / `--t5xxl` / `--vae` / `--type` / `--offload-to-cpu`）
- このリポジトリ: `deploy/aws/ecs/cfn/60-engines.yaml`、`PARAMETERS-60-engines.md`、
  `control-plane/engines.go`、`engine_gateway.go`、`engine_admin.go`、`workspace/agent/engines.go`、
  `internal/imagegen/sdcpp.go`、`internal/agents/opencode/engine.go`、`20-platform.yaml`
  （`SsmWorkspaceParams`）、ADR 0053・0069・0071
- Civitai の REST API リファレンスは wiki が移転先を指し、移転先が 404 だった（未解決 4）
