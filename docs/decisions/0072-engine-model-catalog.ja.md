# 0072. エンジンのモデルカタログ——LLM と SDXL のモデルを CloudFormation を回さずに差し替え、LoRA を要求で選ぶ

[English](0072-engine-model-catalog.md) | 日本語

- 状態: **P0・P1・P4 実装済み・実機検証済み（2026-09-08〜09）。P5 のうち HF トークンの
  Console 登録（未解決 12）は実装済みで、**2026-09-10 に実機検証済み**（「P5 の実装」節と
  「P5 を実機で押した」節。確かめたかった 3 点は 3 つとも通り、gated の断り方が 401 だけでは
  ない——403 がある——という欠落を 1 件踏んだ）。P2
  （ComfyUI）は 2026-09-10 に実装済み。**provider の実機検証も同日に完了した**——
  「P2 を実機で押した」節。そこで実装の欠落を 4 件踏み、うち 2 件は本文の記述そのものが
  誤っていた（同節と、その 2 件を指す 🔴 訂正）。**残作業だった取り込み経路と、provider を
  通っていなかった 3 ファミリー（Z-Image・FLUX.1・SD3.5）も同日に実機で押し切った**——
  「P2 の残作業 4・5 を実機で押した」節。3 つとも生成できるようになったが、**SD3.5 は
  テンプレートが誤っており（`--clip_g` が語彙から欠けていた）、直すまで 1 枚も出せなかった**。
  欠落はさらに 6 件（5〜10）。**5 ファミリーすべてがこの配備の GPU で provider を通って絵を返した。**
  **P6（seed と 6 パラメータの撤去）は 2026-09-10 に実装済み・実機未検証**（「P6 の実装」節）。
  P3 と P5 の残りは未着手。** 起草・レビュー・改訂・
  実装のすべてが同日である。**起草時点の数字はすべて** ADR 0071 の実測から引き、上流
  （llama.cpp・stable-diffusion.cpp）の仕様は同日にリポジトリの `tools/server/README.md`・
  `examples/server/api.md`・`docs/lora.md` から読んだ——起草の時点で**この文書のために新しく
  測ったものは無く**、測ってから決めることは「未解決の点」に列挙して決定のどれがそれに
  依存するかを各項に書いた。その後に測ったものは「実測で解けた点」（ComfyUI）と
  「P0 の実測」（実装と実機検証）の 2 節にある。
- 同日に改訂: **vLLM と ComfyUI を検討し、候補のモデル（SD3 / SD3.5 / FLUX.1 / FLUX.2 klein /
  Z-Image / Qwen-Image）のライセンス・gated・ファイルサイズを HF の API で取った**（背景の
  「候補のモデル」「vLLM と ComfyUI」）。帰結が 3 つ——**ComfyUI を image 役の本命にして
  フェーズを前倒しし**（決定 4・5・10、フェーズ P2）、**生成が 60 秒を超えるモデルは同期 API では
  出せない**という規則を決定 4 に足し、**vLLM は却下**（再検討の条件つき）。sd-server 向けの
  LoRA 経路（`<sd_cpp_extra_args>`）は代替に降格した。
- 同日、**開発配備の GPU（g6.xlarge・L4）で ComfyUI を実測した**（「実測で解けた点」節。
  ハーネスは `deploy/aws/ecs/harness/bench-image-engine.sh`）。klein 4B・Z-Image-Turbo・SDXL が
  同じ箱で描け、温まった 1 枚は 4〜11 秒、**切り替えは EBS からの読み直しで 1〜2.5 分**。
  未解決 7・8 を実測で埋め、決定 10 の既定を klein 4B にした。🔴 途中で 2 度測り直した——
  `--cache-none` が毎要求ディスク読み直しにする罠と、同じ seed の 2 回目が出力キャッシュに
  当たって 0.5 秒になる罠。どちらも数字が出てから分かった。
- 同日、**P0 を実装し実機で検証した**（「P0 の実測」節）。カタログの表・種・active set の SSM・
  役に依らない fetch サイドカー・両役のラッパー・`no_model`・admin API とパネル・
  `catalog-changed` の押し通知・宣言された `sizes[]` まで。完了の定義は **1 番目と 3 番目が
  実機で通り**（同じ prompt・同じ seed で SDXL → Juggernaut-XL v9 に切り替わり、
  `describe-stacks` の最終更新時刻は動かない／CP 不在・active set 無しでサービスが安定する）、
  2 番目は単体テストどまり。🔴 途中で 3 つ踏んだ——**YAML の折り畳みブロックがサイドカーを
  空回りさせ**（実測 2）、**RUNNING のまま温まらない箱を誰も止めない穴**が出て（同 4）、
  **素直な active set は 20＋20 で 4,096 文字に入らなかった**（同 1）。本文に無かった追加が
  2 つ（カタログの行を作る口・絵を見るハーネス）で、理由は「P0 の実測」の末尾に書いた。
- 同日、**P1（llm 役のルーターモード）を実装し、CPU と実機で測った**（「P1 の実測」節）。
  preset 生成・全モデル同期・`LlmModelsMax`・`warm` の再定義・モデル毎の窓まで。完了の定義は
  **3 つとも実機で通った**（同じ箱で 2 つの GGUF が使え、交替のリロードが 1 回の試行で答えを
  返し、Console で 2 行目を有効にすると起動メニューに 2 つ出てセッションが起動した）。行を
  作る口だけは AWS の資格情報では駆動できず、そこは人が押した。🔴 本文を 3 か所直した——**`--models-dir` は使わない**
  （カタログに無い名前が並び、`llm/loras/` がモデルとして数えられる）、**`-c` は
  `LlmExtraArgs` から外す**（コマンドラインが preset に勝ち、全モデルが同じ窓になる）、
  **warm は「既定モデルが `loaded`」ではなく「どれか 1 つが `loaded`」**（交替中の箱が
  「答えているのに warm でない」になり、`unwarmed` 規則が会話の最中に止める）。
- 翌日（2026-09-09）、**P4 を実装し実機で押し込んだ**（「P4 の実測」「P4 を実機で押した」）。
  完了の定義は **3 つとも実機で通った**——ungated を 1 本（491 MB・ボタンから行まで 72 秒）、
  gated の断り（タスクを 1 つも起こさない）、gated の取り込み（FLUX.1-dev 23.8 GB・15 分 51 秒）。
  🔴 **押してみて初めて出た欠陥が 6 つあり、うち 1 つは経路が丸ごと死んでいた**——
  `HfTokenSecretArn` を渡しても取り込みタスクは起動しない（ECS は `Secrets` を**実行ロール**で
  解決し、その許可が無かった）。残る 5 つは、取り込み後に行が一覧へ出ない・失敗が日本語の
  画面に英語で出る（ガードが直書きのコードを見ていなかった）・ファイル名の自由入力と
  シャードの混入・同じ id での upsert・**`purge` の入口が Console に無い**。実機で本文に
  無かった追加は 4 つ（候補一覧の API、窓の自動取得と出力上限の分数、`source` 列、削除の
  2 段確認）。
- 同日、**P0 着手前のレビュー**を末尾の「レビュー（2026-09-08・P0 着手前）」に受け、本文を改訂した。
  覆った前提が 4 つ——🔴 **CP のタスクロールに S3 の権限は 1 つも無い**（決定 6・7 が
  マニフェストの読みと S3 の削除を CP に頼っていた）、🔴 **SSM Standard tier の上限は 4,096
  文字**（「数 KB」は誤り）、🔴 **決定 5 の「CP が要求で拒否」は決定 4 の「本文を読まない」と
  両立しない**、🔴 **決定 10 の「既定は klein 4B」は ComfyUI の実測で sd-server の実測ではない**
  （P0 の既定は SDXL）。加えて決定 1 に 3 条件（両役ラッパー・`ParameterNotFound` は空・
  `mode=on` でカタログ空なら起こさない）、決定 2 に 4 KB の規則、決定 6 に PassRole の範囲と
  0071 決定 8 の精密化、決定 7 に `warm_model`、決定 10 に `license_name` と受諾の記録と商用の
  軸を足した。**未解決 1 はレビューが CPU で全部解いた**（R1）。状態を承認済みに改めた。
- 2026-09-10、**P2（ComfyUI）を実装し、g6.xlarge の実機で完了の定義まで通した**
  （「P2 の実装」節）。自前 Dockerfile
  （v0.34.0 固定・Manager 無し）と専用 CI（`comfyui-image.yml`。CI で実際にビルドが通ることは
  確認した）、`20-platform.yaml` の ECR リポジトリ `af-comfyui`、`60-engines.yaml` の
  `ImageEngine`（sdcpp/comfy）による `!If` 切り替え、provider `comfy`（`/prompt` →
  `/history/<id>` → `/view`。generate のみ——edit/inpaint はモデルファミリーごとの image-to-image グラフが
  未検証のため今回は対象外で、完了の定義自体は generate だけで満たせる）、5 ファミリーの
  ワークフローテンプレート（SDXL・Z-Image・FLUX.2 klein は実測で解けた点の GPU 検証済みグラフの
  移植、FLUX.1・SD3.5 は新規で実機未検証、ゴールデンテストで全部固定）、`generate_image` の
  `model` 引数（決定 5・7。今温かいチェックポイントを説明文に出す）。🔴 副産物として
  **warm_model（決定 7）が image ロールでは元から動いていなかった**ことに気づいた——
  image エンジンの応答は usage.model を持たないので、`recordUsage` が `noteServed("", ok)` を
  呼び続けていた。`X-AF-Model` ヘッダ（Workspace 側の provider が宣言）で埋めた（sdcpp・comfy
  両方）。ゲートウェイの `dial()` も comfy 用に直した——ComfyUI のネイティブ API は `/v1` を
  持たないので、provider が comfy のときだけ upstream への `/v1/` 差し込みを止める。

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

### 候補のモデル（2026-09-08 に HF の API で確認）

ライセンスと gated は `https://huggingface.co/api/models/<repo>` の `cardData.license` /
`license_name` / `gated`、サイズは `?blobs=true` の `siblings[].size`。**L4（22.9 GB）での重さは
0071 の実測（SDXL fp16 7.4 GB）以外は推定で、★ を付けた。** 生成時間も同じ——実測は SDXL の
21 秒（sd-server）と 8 秒（ComfyUI）だけである。

| モデル | ライセンス | gated | L4 での重さ | 評価 |
|---|---|---|---|---|
| SD1.5 | OpenRAIL | 無し | 軽い | 出さない |
| SDXL 1.0 | OpenRAIL++-M | 無し | fp16 7.4 GB 実測 | 主力のまま。LoRA の生態系が最大 |
| SD3 Medium | Stability Community | 有り | 軽い | 出さない。3.5 に置き換えられ、DL 数 4.7k |
| SD3.5 Medium（2.5B） | Stability Community（年商 $1M 未満は商用可） | 有り | 本体 5 GB + T5-XXL fp8 約 5 GB★ | 候補。fp16 のまま入る |
| SD3.5 Large（8B） | 同上 | 有り | fp16 16 GB + T5 → fp8 / GGUF 前提★ | 量子化前提。Turbo は 4 ステップ |
| FLUX.1-schnell（12B） | Apache-2.0 | **有り** | 本体 23 GB → fp8 / GGUF 前提★ | 4 ステップで速い。LoRA は dev 向けが多い |
| FLUX.1-dev / Kontext-dev | flux-1-dev-non-commercial | 有り | 同上、20〜28 ステップで 60〜90 秒★ | 画質と LoRA は最強だが**非商用**、**60 秒規則**（決定 4） |
| FLUX.2-dev（32B） | flux-non-commercial | 有り | 入らない | 対象外 |
| **FLUX.2 [klein] 4B** | **Apache-2.0** | **無し** | 本体 7 GB + テキストエンコーダ 7 GB★ | **最初の既定の候補** |
| FLUX.2 [klein] 9B | flux-non-commercial | 有り | 量子化前提★ | 4B に劣後 |
| **Z-Image-Turbo（6B）** | **Apache-2.0** | **無し** | 本体 bf16 約 12 GB★ + Qwen 系エンコーダ | **最初の既定の候補**。8 ステップ |
| Qwen-Image（20B） | Apache-2.0 | 無し | 4bit + 7B エンコーダ★ | 文字描画は強いが重い。後回し |

読み方が 3 つ。**Apache-2.0 かつ gated 無しは FLUX.2 klein 4B・Z-Image-Turbo・Qwen-Image の
3 つだけ**で、FLUX.1-schnell は Apache-2.0 のまま gated に変わっている。**12B 以上は L4 で
量子化前提**で、fp8 や GGUF の別ファイルをカタログが持ち、`text_encoders/`（T5-XXL・CLIP-L）は
SD3.5 と FLUX.1 で共有できる（決定 2）。**生成が 60 秒を超えるモデルは sd-server の同期 API では
出せない**（決定 4）。

stable-diffusion.cpp の対応（README）: SD3/SD3.5、FLUX.1、Qwen-Image（2025-10-12）、
FLUX.2-dev（2025-11-30）、Z-Image（2025-12-01）、FLUX.2-klein（2026-01-18）。**追随はする。
ただし常に後追い**で、公式の手順はどれも ComfyUI のワークフローで出る。

### vLLM と ComfyUI

**vLLM は今は採らない**（却下した案に理由）。要点は、**1 プロセス 1 モデルでルーターが無い**
（切替＝再起動。llama.cpp のルーターは同一プロセス内）、**イメージが 9.7 GB**（Docker Hub
`vllm/vllm-openai:latest` の圧縮サイズ。llama.cpp の 2.47 GB の 4 倍で、0071 の pull 178 秒が
そのまま伸びる）、**L4 に 30B 級が入りにくい**（GGUF ではなく AWQ / GPTQ / FP8 で、
Qwen3-Coder-30B-A3B の公式 FP8 は約 30 GB）、そして**強みが効かない**（連続バッチングと
マルチ LoRA は同じモデルへの同時要求で効く。フリートのエンジンは普段寝ていて、起きている間の
同時利用は数セッション）。

**ComfyUI は image 役の本命にする**（決定 4・5・10）。0071 の実測がそのまま理由になる: 同じ L4 で
**2.5 倍速い**（7.9 秒対 20.8 秒）、**チェックポイントも LoRA もワークフロー毎に選べてロード済みを
キャッシュする**（sd-server の「1 つ・次の起動」と「LoRA 切替コスト未測」はどちらも消える）、
**API が非同期**（`/prompt` が即返り `/history` を短く叩くので、ALB の 60 秒アイドルに当たらない）、
**新モデルの参照実装が先に載る**。払うものは自前イメージ・モデルファミリーごとのワークフローテンプレート・
provider `comfy`・そして API 契約が OpenAI 互換のように版で守られていないこと（ノード名が版で
変わるので、タグを固定しテンプレートをゴールデンテストで固定する）。

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
     エンジンのコンテナは**両役とも同じラッパー**で起きる（レビュー決定 1(a)）: サイドカーが
     active set から `/models/cmdline` を書き、コンテナは
     `sh -c 'if [ -s /models/cmdline ]; then exec <engine> $(cat /models/cmdline); else sleep
     infinity; fi'`。どのイメージにも `sh` はある（R5）。P1 が後回しのあいだ llm 役は `-m` の
     ままで、ルーターに替える日はサイドカーが書く 1 行が変わるだけ。サービスは安定し、
     ゲートウェイは**カタログが空なら起こさない**（`503 engine_unavailable`「このロールに
     有効なモデルが無い」）。パラメータは `LlmEnabled` / `ImageEnabled` の 2 つに縮む。
     成立の条件があと 2 つ（レビュー決定 1(b)(c)）: **サイドカーは SSM の `ParameterNotFound`
     を「空」として扱う**——`60-engines` は `30-ingress` より先に建つので、スタック作成時に
     CP は存在せず active set は無い。`set -e` がそこで拾えば二段階が形を変えて戻る；
     **コントローラは `mode=on` でもカタログが空なら起こさない**（`engineSnapshot.hasModels`、
     `decideEngineAction` が `engineReasonNoModel` で何もしない）——`engine_control.go` は
     `running && !warmed` を 5 秒間隔で永遠に見に行くので（R6）、`sleep infinity` の箱を
     `mode=on` の配備が $1.26/時で買い続ける穴がある。パネルはトグルの代わりに「有効なモデルが
     無い」を出す。消えるのは二段階であって、**安定化のために GPU 箱を 1 回買う 10 分は残る**
     （プレースホルダも `GPU` の `ResourceRequirements` を持つ。レビュー決定 1(d)）。

2. **カタログの真実は 2 層。S3 が「何があるか」、CP の DB が「どう出すか」。エンジンには聞かない。**
   - **S3 のレイアウトは ComfyUI の規約**（0071 決定 6）: `llm/<name>.gguf`（分割は
     `llm/<name>/`）、`llm/loras/`、`image/checkpoints/`、`image/loras/`、`image/vae/`、
     `image/text_encoders/`、`image/diffusion_models/`。既存の `image/sd_xl_base_1.0.safetensors`
     は `image/checkpoints/` へ移す（S3 内のサーバサイドコピーで、NAT を通らない）。
   - **各ファイルの隣にマニフェスト `<file>.json`** を取り込みジョブが書く: `sha256`・`bytes`・
     `source`（URL、HF の repo id とリビジョン、または Civitai の version id）・`license`
     （HF の `cardData.license`）・`kind`（`gguf` / `checkpoint` / `lora` / `vae` /
     `text_encoder` / `diffusion_model`）・`baseModel`（`sdxl` / `sd35` / `flux1` / `flux2-klein` /
     `zimage` / `qwen-image` / …。LoRA の**適合先**であり、取り込み時に運用者が宣言する）・
     `precision`（`fp16` / `fp8` / `q8_0` / `q4_k` …。**12B 以上は L4 で量子化前提**なので、同じ
     モデルの別精度が別ファイルとして並ぶ）・`ingestedAt`。`text_encoders/` はモデルファミリーをまたいで
     共有する——SD3.5 と FLUX.1 は同じ T5-XXL と CLIP-L を読むので、1 回取り込めば両方の
     `files[]` から指せる。「ファイルが
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
     ポリシー）と比べ、**既に持っている権限**で済み（R2(a) で確認）、値が小さく、そして箱が読む
     瞬間に CP が生きている必要が無い。🔴 **値の上限は Standard tier の 4,096 文字**（R2(c):
     4,200 文字は `ValidationException`。今日のエンジン表は 666 バイト）。だから active set には
     **S3 キー・ローカル名・フラグ・preset の材料だけ**を置き、`description` や `license` は
     DB に留める。テストで「モデル 20・LoRA 20 の active set が 4,096 文字に収まる」を固定し、
     収まらない設計変更が来たら Advanced tier（8 KB・$0.05/月）へ上げる判断を**その時に**する。
     サイドカーは `jq` で JSON のまま読む（`aws-cli` のイメージに `jq`・`python3`・`bash` がある。
     R5）。葉 `/af-ws/engines` と子 `/af-ws/engines/<key>/active` は共存する（R2(b)）。
     CP が作るパラメータは CloudFormation の外にあるので、**`teardown.sh` が
     `/af-ws/engines/*/active` を消す**。S3 レイアウトの移行（`image/…` →
     `image/checkpoints/…`）と決定 7 の種（`modelS3Key`）は**同じ手順で**動かす——種が旧キーを
     指せば最初の起動が空振りする。
   - 0071 のエンジン表（SSM 1 本、起動時読み）は**基盤の表として残る**——サービス名・URL・健診・
     capacity provider・idle・deadline・mode。`models[]` と `contextTokens` はカタログへ
     移り、表にあれば決定 7 の種として読む。

3. **llm 役はルーターモードで動かし、モデルは要求の `model` で選ぶ。窓はモデル毎になる。**
   `llama-server --models-preset /models/llm/presets.ini --models-max <LlmModelsMax>`。preset は
   fetch サイドカーが active set から生成する（セクション名＝カタログの id、`model`＝パス、
   `c`・`n-gpu-layers`・固定 LoRA）。`-m` と `--alias` と `-c` はコマンドラインから消える。
   - 🔴 **`--models-dir` は付けない**（P1 の実測 1・3）。起草時の本文は preset と併用して
     いたが、ディレクトリ走査は**ファイル名をモデル名にする**ので、カタログに無い id が
     `/models` に並び（決定 7 と衝突）、`llm/loras/` のサブディレクトリが分割 GGUF として
     モデル 1 つに数えられる。preset のセクションは**ファイルの有無と無関係にモデルを
     定義できる**ので、カタログの id をそのまま名前にできる。`alias` も要らない
     ——ルーターが `--alias <セクション名>` を自分で付ける。
   - 🔴 **`-c` を `LlmExtraArgs` に残してはいけない**（同 2）。ルーターは自分のコマンド
     ラインを子に渡し、コマンドラインは preset に勝つので、`-c 32768` が 1 つあるだけで
     カタログのモデル毎の窓が全部消える（実測: 4096 と 384 を宣言した 2 モデルが両方
     32768 で起動した）。テンプレートの Description と参照文書に禁止として書いた。
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
     `GET /models` の `status.value` を見て、**`loaded` のモデルが 1 つでもあれば** warm と
     する（健診は `/health` のまま）。既定モデル＝カタログで `default` を付けた 1 つで、
     preset の `load-on-startup = true` にする。起動時に載せないと、最初の要求が「箱の起動
     527 秒＋ロード 267 秒」を払う。
     - 🔴 **「既定モデルが `loaded`」ではない**（P1 の実測 4）。`--models-max 1` で別の
       モデルに交替した箱は既定を降ろしているので、既定に縛ると「答えているのに warm で
       ない」になり、`running && !warmed` が 900 秒続けば P0 で足した `unwarmed` 規則が
       **会話の最中に箱を止める**。`sleeping` と `loading` は warm ではない——どちらも
       次の要求が重みを払う状態である。
     - エンジン表に `warmPath`（llm は `/models`、image は空）を足して**宣言**する。
       provider 名から導出しない（ADR 0053）。`GET /models` は autoload を起こさず
       ルーターの idle タイマーも動かさないので、30 秒ごとに叩いてよい（同 5）。
   - **autoload 中の要求は心拍で握る。** ゲートウェイの streaming 経路は上流の最初のバイトまで
     10 秒ごとの SSE コメントを流す（0071 決定 5）ので、追加の機構は要らない——**ルーターは
     要求を待たせる**（R1(b): `ensure_model: waiting until model … is fully loaded` の後に
     200）。`/models/load` を先に叩く分岐は要らない。
   - **未解決 1 の 4 点はレビューが CPU で解いた（R1）**: 空の `--models-dir` で起動して
     `/health` は ok、autoload は待つ、`--models-max 1` は idle LRU を降ろして載せる
     （`evicting idle LRU`）、`usage` と `model`（カタログ id）はルーター経由で届き、窓は
     モデル毎（`/props?model=` の `n_ctx`）。したがって 🔴 **`/health` は warm の根拠に
     ならない**（0 モデルで ok）——今日の `warmProbe` をルーターに向けたまま使えば常に
     true になる。`warm` は本文どおり `GET /models` の `loaded`。細部を 2 つ: 無い id への
     ルーターの答えは **400**、ゲートウェイの `404 model_unknown` は自分の判定で、番号を
     混ぜない；子は**別プロセス**でループバックの空きポートに `--api-key` 無しで立つ
     （タスクの netns の外からは届かない。書いておく）。GPU に残る宿題は 1 つ——「idle LRU」が
     **生成中**のモデルをどう扱うか。それは P1 の完了の定義（交替のリロード）と同じ観測である。
     P1 を後回しにする理由はもう「未測」ではなく、利用者の優先順位（画像が先）だけである。
     🔴 **その宿題も解けた（P1 の実測 7）——退避は生成の終わりを待つ。** 割り込まれた側の
     ストリームは丸ごと届き、待たされた側が「相手の残り＋ロード」を払う（CPU で 48.6 秒の
     待ち）。だから `--models-max 1` の代償は「答えが壊れる」ではなく「待つ」である。
     ストリーミングは心拍で待ち切れるが、**非ストリーミングは待てない**——ゲートウェイの
     45 秒の保持（`AF_ENGINE_PLAIN_HOLD`。ALB の 60 秒より内側）が先に切れ、
     再試行できる `503 engine_waking` になる。

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
   - **生成が 60 秒を超えるモデルは、同期 API のエンジンでは出さない。** 0071 P1 の実測 9 の
     とおり ALB は応答のバイトが 60 秒来ない接続を閉じ、`/v1/images/generations` は JSON 一発で
     心拍を差し込む先が無い。SDXL の 21 秒は当たらなかったが、FLUX.1-dev 級（60〜90 秒★）や
     SD3.5 Large の非 Turbo は**温まったエンジンでも毎回切られる**。sd-server の非同期 API は
     コンテナ内で壊れていた（0071 実測）。したがってカタログの `sizes[]` と同じ場所に
     `syncSafe`（同期 API で出してよいか）を運用者が宣言し、sd-server ではそれ以外を出さない。
     - 仮説を 1 つ残す: その非同期 API が `/proc/1/map_files` で落ちたのは、`--lora-model-dir` の
       既定が**カレントディレクトリ**で、cwd が `/` だと LoRA の走査が `/proc` に入るからかも
       しれない。`--lora-model-dir /models/image/loras` を付けた起動 1 回で確かめられる
       （未解決 6）。直っても本文の規則は変えない——非同期 API が使えるのは代替の話で、
       本命は次の項である。
   - **ComfyUI を image 役の本命にする（フェーズ P2）。この決定の「1 つ」はそこで消える。**
     ComfyUI はワークフロー毎にチェックポイントも LoRA も選び、ロード済みをキャッシュし、
     `/prompt` が即返って `/history` を短く叩くので 60 秒規則にも当たらない。同じカタログ・
     同じ S3 配置のまま `selected` が「要求毎」になる。カタログをエンジン非依存に設計するのは
     そのためで、**sd-server の制約をカタログの形に焼き込まない**。sd-server は「公式イメージ
     2.3 GB・OpenAI 互換・自前イメージ不要」の軽い経路として残す（配備が選ぶ。決定 10）。
     - スタックでは**役を増やさない**。`ImageEngine`（`sdcpp` / `comfy`）のパラメータ 1 つで、
       **同じ**タスク定義・サービス・Cloud Map 名のまま、コンテナ定義（イメージ・entrypoint・
       コマンド）と健診パス（sd-server は `/v1/models`、ComfyUI は `/system_stats`）だけを
       `!If` で切り替える。2 つ目のサービス一式は壁に入らず、同時に動かさない（0071 決定 2）
       のだから 2 つ要らない。エンジン表の `provider` が `comfy` になり、Agent はそれで
       provider を選ぶ。前提が 2 つ（R4）: **`20-platform` に ECR リポジトリ `af-comfyui` を
       1 つ足し**（今は 5 つで、ComfyUI のは無い。22.5 KB の余裕はある）、自前イメージは
       `af-workspace` と同じく CI で焼いて `standup.sh` の images 段が複製する；そして
       **予算は今のままでは入らない**——`60-engines.yaml` は 50,774 バイトで、廃止分 約 3,000
       に対して足すもの 約 5,450（サイドカーを `Mappings` に 1 本置いて両役から引けば 4,350）。
       コメント 15.5 KB のうち、ボリュームの実測注 2 つと `run-task` のレシピ（3 KB 強）を
       `PARAMETERS-60-engines.md` へ移して入れる。**移してから足す**（P0 の最初の手順）。
     - 🔴 **P0 の image 役は sd-server のままなので、P0 の既定は SDXL である**（レビュー決定
       4(b)）。決定 10 の klein 4B は ComfyUI の実測で、sd.cpp が klein を名乗っていても
       分割モデルのフラグ組み立てごと未測——P0 でそれを踏むと完了の定義が sd-server の問題で
       止まる。P0 の完了の定義に使う 2 つ目のチェックポイントは **SDXL 系のもう 1 本**
       （OpenRAIL++-M の fine-tune。決定 10 の規則で選ぶ）を先に取り込む。klein が既定に
       なるのは P2 で ComfyUI が入ってから。

5. **LoRA はカタログの項目であり、要求で選ぶ。エージェントが選べるのはカタログにある名前だけ。**
   - **image。** 箱は `image/loras/` のうち**有効なもの**を同期し、`--lora-model-dir
     /models/image/loras` で起動する。`generate_image` に引数を 2 つ足す: `model`（enum＝
     有効なチェックポイント。provider `sdcpp` では**今日は 1 つ**——決定 4——だが、Codex /
     agy 経路と ComfyUI のために引数の形は今から複数を許す）と `loras: [{name, weight}]`
     （`name` の enum＝**選択中のチェックポイントと `baseModel` が一致する** LoRA だけ。
     `weight` は 0〜2、既定 1）。ツールの説明文にカタログの `description` を並べ、
     エージェントが「水彩風なら `watercolor-v2`」と選べるようにする。
     - **本命は ComfyUI**（決定 4）: provider `comfy` がモデルファミリーごとのワークフローテンプレートに
       チェックポイント名と `LoraLoader` の連鎖（name・strength）を差して `/prompt` に投げる。
       LoRA はノードとして差し替わるので、組が変わるコストは ComfyUI のキャッシュの話になり、
       sd-server のマージ方式（未解決 2）を測る必要が無い。
     - **代替は sd-server**（`ImageEngine=sdcpp` の配備）: Agent の provider `sdcpp` が prompt の
       末尾に `<sd_cpp_extra_args>{"lora":[{"path":"<file>","multiplier":<w>}]}</sd_cpp_extra_args>`
       を付ける。**ゲートウェイは触らない**（素通しのまま。決定 4 の「本文を読むのは usage
       のときだけ」を守る）。⚠️ **利用者の prompt に既に `<sd_cpp_extra_args>` があれば拒否
       する**——この穴からは `seed` や `sample_steps` だけでなく、サーバのファイル
       パスに対する `lora.path` が通る。prompt はモデルが書くものであり、モデルが読んだ
       ものは何であれ prompt に現れうる（0071 決定 4(d) と同じ姿勢）。この経路は
       ComfyUI の後に、要る配備があれば作る。
     - `baseModel` が合わない LoRA（SD1.5 の LoRA を SDXL に）は sd.cpp が**黙って崩れた絵**を
       出すか、テンソル名の不一致を警告して無視する。どちらも利用者には「効かない」としか
       見えないので、**Agent が enum で出さず、組み立ての段階で断る**。🔴 拒否は **CP では
       しない**（レビュー決定 5）——LoRA は prompt の `<sd_cpp_extra_args>` かワークフロー
       JSON の中にあり、CP が拒むには本文を読むことになって決定 4 の「素通し」と両立しない。
       もう 1 段の守りは**箱**にある: 有効な LoRA しか同期しないので、enum の外の名前は
       エンジンが失敗させ、`--lora-model-dir` と `models/loras/` がパスの範囲である。
     - **引数の形は 0069 に収まる**（R7）: `imagegen.Request.Model` は既にあり、`Caps` に
       `Loras []{name, description, baseModel}` を足す。規則を 2 つ書く——**引数は本当に
       選べるときだけ出す**（`provider` と同じ。フリートのエンジンが有効なチェックポイントを
       2 つ以上持つときだけ `model` が現れ、値はカタログの id だけ。Codex / agy の固定モデルは
       enum に混ぜず、`model` を指定しつつ provider がフリート以外なら名指しで拒む）；
       **enum は他の引数に依存できない**ので、`loras` の enum は有効な LoRA 全部で、説明に各
       LoRA の `baseModel` を書き、組み合わせの検査は Agent がする。
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
     - 🔴 **gated でもメタデータは匿名で読める**（P4 の実測 1）。`gated: auto` のリポジトリでも
       `api/models/<repo>?blobs=true` はライセンス・gated の別・**全ファイルの sha256 と
       サイズ**を返し、401 になるのは**ダウンロードだけ**である。だから CP は HF トークンを
       持たずに全部解決でき、トークンは取り込みタスクの中に閉じたままにできる——決定 6 の
       「トークンは箱に載せない」が、CP にも当てはまる形で成立する。
     - **改訂（2026-09-09・未解決 12。実装済み）**: ~~トークンは CP の外（CFN パラメータ
       `HfTokenSecretArn`）で設定する~~ → **トークンの正本は CP の DB（`custodian` で封をする）
       であり、Secrets Manager は ECS にしか渡せない値を運ぶための運搬路である。CP はその
       秘密に `PutSecretValue` だけを持ち、`GetSecretValue` は持たない**——書いた値を読み戻せない
       という意味で「CP はトークンを持たない」は**弱まるが消えない**。取り込みタスクだけが値を
       読む点は変わらず、上の「メタデータは匿名で読める」もそのまま生きる（**変わるのは登録の
       経路だけ**）。スタックは秘密を**常に**番兵値 `-` で作るので `Secrets` ブロックは常に
       存在し、CFN の往復は消える。
     - トークンが無い配備で gated を頼まれたら、**タスクを起こす前に断る**
       （`gated_no_token`）。9 分走ってから 401 で落ちるのと、費用も分かりやすさも違う。
   - **`ecs:RunTask` で取り込みタスクを起動**する。CP のタスクロールに `ecs:RunTask` は無い
     （R3: ECS は `CreateService` … `ListTasks` の読み書きで、`RunTask` だけ無い）ので、
     `ecs:RunTask`（取り込みの family に限る）と `iam:PassRole`（**`IngestTaskRole` だけ**——
     実行ロールの PassRole は `PassTaskRoles` に既にある）を足す。0071 決定 8 の「CP の IAM
     追加はゼロ」を**ここで初めて破る**が、置き場は `60-engines` の中の `AWS::IAM::Policy`
     （`Roles:` に `20-platform` の `CpTaskRoleArn` から切り出したロール名）で、**スタックを
     採用しない配備の CP は何も増えない**。0071 決定 8 はこう精密化する: 「CP のロールに他の
     スタックが足してよいのは、**そのスタックの資源にしか効かない権限**（family 限定の
     RunTask・ingest ロール限定の PassRole）だけで、`Resource: *` は足さない」。代替に
     EventBridge（CP が `/af-ws/engines/ingest/job` を書き、`60-engines` の `AWS::Events::Rule`
     がスタック内のロールで RunTask する）を検討したが、配送が at-least-once でジョブが二重に
     走りうるうえ失敗が CloudTrail にしか出ないので採らない（レビュー決定 6(c)）。RunTask に
     要る subnets と SG は `60-engines` がエンジン表の `ingest` ブロックに書く。CP から HF の
     API への egress が前提で、外向きを絞る配備では取り込みは手打ちに落ちる。
   - 🔴 **CP はマニフェストを読まない——読めない**（R3: CP のタスクロールに S3 のアクションは
     1 つも無く、足しもしない）。行は **CP 自身が HF から解決した sha256・license・bytes と、
     `DescribeTasks` の終了コード（`fetch` SUCCESS → `upload` SUCCESS）**から作る。sha256 の
     照合は `fetch` が済ませているのでそれで足りる。マニフェストは**箱が読むもの**である。
     行は `enabled: false` で作る（**取り込めた＝出る、ではない**。管理者が有効にする）。
     進行中・失敗理由（sha256 不一致・401・容量）はパネルの行に出す。
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
     操作は有効/無効・選択・既定・削除。🔴 **削除で S3 のファイルを消すのは CP ではなく
     取り込みタスク**（R3: CP に `DeleteObject` は無い）——同じ `RunTask` に `MODE=delete` を
     持たせ、**マニフェストを先に消す**順序はそこで守る（ファイルが先に消えると候補に残った
     まま同期が失敗する）。P4 までは `harness/ingest-model.sh` の兄弟に任せる。決定 13（0071）の
     「モデルはスタックの宣言」は「モデルはカタログの宣言」に読み替え、`warm` の意味は決定 3 の
     とおり。
   - **いま温かいモデル（`warm_model`）は CP が持つ**——`POST /engine/usage` に載る `model`
     から**最後に成功した要求のモデル**として（エンジンには聞かない。ADR 0053）。カタログの
     行に出し、**`model` 未指定の既定を「温かければそれ、でなければカタログの既定」にサーバ側で
     決める**——エージェントに温度を推論させない（レビューの答え 7。切り替えは EBS 読み直し
     1〜2.5 分、実測で解けた点 5）。切り替えた要求は `warnings` に「モデルを切り替えたので
     +N 秒」を返す。
   - **種（決定 1 の互換）**: CP 起動時にカタログが空で、エンジン表に `models[]` があれば、
     その id・`contextTokens` と、`60-engines` が表に書き足す `modelS3Key` から**1 行だけ**
     作る。今日の配備は CP を上げた瞬間に今日のモデルがカタログにあり、何も変わらない。

8. **由来と使用量はモデルと LoRA を持つ。** `generate_image` の結果の `model` はチェック
   ポイント id、`provenance` に `loras: [{name, weight}]` と `sha256`（マニフェストから。
   0071 決定 10 の「ファイル名と sha256」）。llm の usage 行の `model` はルーターが応答に
   返す名前（＝カタログ id）で、`engine_hourly`（0071 決定 13）には触らない——稼働は箱の
   性質で、モデルの性質ではない。

9. **コールドスタートへの影響は数で言い、同期する範囲は役ごとに違う。** S3 → EBS は
   104〜147 MB/s で一定（0071 実測 8。P1 の実測 9 で 116.7 と 139.7 MB/s がもう 2 点）
   だから、llm の同期時間は**有効な GGUF の合計サイズで決まる**——18.5 GB ごとに 179 秒。
   - **llm（ルーター）は有効な全モデルを同期する。** ルーターはどれを要求されても答える
     ことになっていて、ファイルが無いモデルは「待たされる」のではなく 500 で落ちる
     （P1 の実測 6）。だから有効化は llm 役では文字どおり「次の起動を N 秒延ばす」である。
   - **image（sd-server）は選択中のチェックポイントだけ**を同期する。1 つしか抱えられない
     のだから、他人のチェックポイントを毎回のコールドスタートに乗せる理由が無い。
   - admin パネルは有効化のトグルの横に「同期 +N 秒（推定）」を出す。**サイズは登録時に
     宣言する**——CP は S3 を見られない（R3）ので、マニフェストと同じ姿勢で
     `files[].bytes` に持ち、`observed_secs` のときと同じで**推定と書く**。
   `useLocalStorage`（0071 未解決 1）は据え置き。

10. **モデルの選定方針——最初に出す既定は「Apache-2.0・gated 無し・L4 に量子化なしで入る」から
    選び、gated と非商用は運用者の明示の行為でしか入らない。** 背景の表から:
    - **既定は FLUX.2 [klein] 4B、次点が Z-Image-Turbo**（どちらも Apache-2.0・gated 無し・
      L4 に bf16 のまま入った）と、主力の SDXL。実測（実測で解けた点 3）で決めた: 温まった
      1024px が klein **4.0 秒**（VRAM 11.6 GB）、Z-Image **10.5 秒**（12.2 GB）、SDXL 8.0 秒
      （6.9 GB）。読み込みは klein 106〜110 秒、Z-Image 133〜154 秒で、**切り替えのたびに
      払う**（同 5）ので、軽いほうが既定になる。絵はどちらも目で見て正しく、Z-Image は写真寄り。
    - **SD1.5 と SD3 Medium は出さない。** 前者は SDXL に、後者は SD3.5 に置き換えられている。
    - **gated（SD3.5 全部・FLUX.1 全部・FLUX.2 dev/klein 9B）は、運用者が HF で同意して
      `HF_TOKEN` を置いた配備でだけ取り込める**（0071 決定 3・11 のまま）。gated とは
      「所有者の条項に同意したアカウントにだけ配る」設定（`gated: auto` は同意で即時許可）で、
      匿名や未同意のトークンでは 401/403 になる。マルチテナント配備では**運用者がメンバー全員の
      代わりに条項を引き受ける**ことになるので、取り込みの UI はその一文を出して
      `licenseAccepted` を要求する（決定 6）。FLUX.1-schnell は Apache-2.0 のまま gated に
      変わっている——**ライセンスと gated は別の軸**で、カタログも別の欄に持つ。
    - **非商用（FLUX.1-dev / Kontext-dev / FLUX.2-dev / klein 9B）は既定にしない。** 取り込みは
      拒まないが、カタログの `license` がパネルの行と `generate_image` の由来（決定 8）に出る。
      🔴 **マニフェストと `engine_models` は `license`・`license_name`・URL の 3 つを持つ**
      （R8: FLUX.1-dev と SD3.5 は `cardData.license` が `"other"` で、実体は `license_name`
      にある。`license` だけ写すと非商用の 2 つが「other」とだけ書かれて出る）。取り込み時の
      値は HF の card が変わっても動かない**スナップショット**。受諾は**誰がいつ**を残す
      （`licenseAcceptedBy` / `licenseAcceptedAt` をマニフェストと監査ログに）。そして
      **`commercialUse` の軸**を出す——FLUX.1-dev の非商用は「メンバーに有料で提供する配備」では
      運用者自身の違反になるので、「取り込みは拒まない」の横に「この配備が商用なら入れては
      いけない」をライセンス名から引ける形で出す（Stability の年商 $1M も同じ欄）。`HF_TOKEN`
      は個人のアカウントに紐づき、同意した人が去れば失効する——組織アカウントのトークンを
      推奨に書く（レビュー決定 10）。
    - **12B 以上は量子化ファイルを取り込む**（`precision`、決定 2）。fp16 の本体 23 GB は L4 の
      VRAM に入らず、S3 から引く 180 秒と VRAM へのロードを払ってから落ちる。
    - **60 秒規則**（決定 4）: `syncSafe` を宣言しないモデルは sd-server の配備には出ない。

11. **取り込み元は検索できる。「リポジトリ名を知っている人」しか主経路を通れない形にしない。**
    （2026-09-09 に追加・P5。P4 が「ファイル名の自由入力＝打ち間違いと区別できない拒否」を
    選択式に直したのと**同じ穴がリポジトリ名の側に残っていた**——`owner/name` を別の窓で
    調べてから貼る、が唯一の入口だった。）
    - `POST …/ingest/search`（super_admin）。`q` と、HF と Civitai のどちらを引くかの `source`
      （既定 HF）を受ける。**どちらの API も匿名で引ける**（P4 実測 1・2）ので、検索も解決と
      同じく**トークンを必要としない**——決定 6 の「CP は取り込み元を読むだけ」がそのまま伸びる。
    - **役ごとに絞る**（2026-09-09 に実測）: llm は `filter=gguf`、image は
      `pipeline_tag=text-to-image`。並びは `sort=downloads&direction=-1`、上限 20 件。
      Civitai は `types=Checkpoint`（image）で、LoRA は P3 で `types=LORA` を足す。
      絞りの理由は `ingest/files` と同じ——**行き止まりを見せない**。このエンジンが読めない
      リポジトリを一覧に出すのは、少し後で resolve が断る行き止まりを見せることである。
    - 🔴 **1 回の読みで判断材料は揃うが、素通しはできない。** `expand[]` で `gated`・
      `cardData`・`downloads`・`likes`・`lastModified`（llm は `gguf` も）が取れる。ただし
      **`cardData` には `extra_gated_prompt` が、`gguf` には `chat_template` が丸ごと入る**
      （実測: FLUX.1-dev の gated 文面、Qwen2.5-Coder の chat template は単独で 1 KB を超える）。
      **CP が写すのは `license`・`license_name`・`gguf.total`・`gguf.context_length` だけ**にする。
      20 行を描く画面に、読まれない KB を 20 回運ばない。
    - **検索結果は行き先であって取り込みではない。** 選ぶと既存の `ingest/files` →
      `ingest/resolve` → 受諾 → `ingest` にそのまま入る。**sha256 とライセンスの正本は
      resolve が読んだ値**で、一覧の値は下書きにすぎない（HF の card は動く）。Civitai は
      `modelVersions[0].id` を渡す——取り込みが要求するのは **version id であって model id
      ではない**。
    - **言葉が無いときはランキングである。** `q` が空なら「この役が読めるものの上位」を返す
      ——名前を知らない人にとってはこれが唯一の入口で、`q` 必須は「知っている人だけ」を
      別の形で作り直すことになる。並びは **DL 数・話題・いいね**の 3 つで、**上流ごとに
      写像し、素通ししない**: Civitai は知らない `sort` に 400 を返し（実測）、HF は黙って
      無視する——**並んでいないのに並んで見える一覧**のほうが悪い。
      HF は `downloads` / `trendingScore` / `likes`、Civitai は `Most Downloaded` /
      `Most Downloaded`＋`period=Month`（トレンドの得点を持たないので「今月」が話題である）/
      `Highest Rated`。
    - 🔴 **「新着」は出さない。** 実測 2026-09-09: `filter=gguf` に `sort=lastModified` /
      `createdAt` を掛けると、返ってくるのは**自動再量子化の一括アップロード**
      （`mradermacher/*-i1-GGUF`）ばかりで、DL 数もいいねも 0 である。最初の 1 画面が毎回
      同じ投稿者のロボットになる並びは入口にならない。「新しいものを見たい」に答えるのは
      話題のほうである。
    - **3 つの数はどの並びでも全部出す。** 並び替えに使った 1 つだけを出すと「なぜこれが
      ここに居るのか」が読めない。「みんなが使っている」と「今週みんなが見ている」は
      別の答えである（Civitai は話題の得点を持たないので、その欄は**借りずに空**にする）。
    - 🔴 **閲覧はエンジンを要求しない。** 60-engines を配備していない配備でも、エンジンを
      「無効」にしている配備でも、**見ることはできる**。理由は単純で、この読みには
      トークンもバケットもタスクも要らない（決定 6）——要るのは**取り込む先の役**だけである。
      だから `POST /api/admin/engines/search`（`kind` を明示。エンジンが無いので導出元が無い）を
      別に置き、パネルはエンジン 0 本のときこれを出す。**「ここには何もありません」は
      「何が動かせるのか」への答えとして最悪**で、スタックを立てるか決めようとしている
      管理者こそ、いまカタログを見られない人だった。取り込みだけはできないので、パネルは
      **できないことを名乗る**（ボタンにせず、注記を出す）。Civitai も同じく対象で、
      checkpoint のときだけ選べる。
      - 🔴 **本当の原因は登録の位置だった**（実測 2026-09-09・このコンテナで CP を起こして
        確認）: admin ルート一式が `registerEngineRoutes` の `if reg == nil { return }` の
        **内側**にあり、エンジン表の無い配備では `GET /api/admin/engines` すら 404 で、
        パネルは「エンジンがありません」と言うことすらできなかった。admin だけ guard の
        外へ出す（ハンドラはすべて registry に対して nil 安全で、自分で「そんなエンジンは
        無い」と答える）。
    - 🔴 **`trendingScore` は小数を返す。** 実測 2026-09-10（`search=WAI`・image 役）: 20 行のうち
      2 行が `0.1` と `0.7000000000000001` で、**`int64` で受けていたため配列全体の
      unmarshal が落ち**、パネルは「取り込み元が想定外の応答を返しました: unreadable answer
      from huggingface.co」だけを出した——**前日まで動いていた検索が、行の中身次第で全滅する**。
      得点であって件数ではないので `float64` で受ける。画面側も丸める（生の
      `String(0.7000000000000001)` が行に出る）。narrow decode で型を絞る利得の裏側で、
      **絞った型が上流の実際の値域と違うと失敗が全か無かになる**、が教訓である。
    - 🔴 **narrow decode の効き目は 41 倍**（実測 2026-09-09・同じ 20 行）: 上流の生の応答
      **211,015 バイト**に対し、この経路の応答は **5,125 バイト**。`chat_template` と
      `extra_gated_prompt` を落とすのは「少し軽くなる」ではない。
    - **検索は取り込みの前提にしない。** `owner/name` と URL の直接入力は残る。外向きを絞った
      配備では検索も落ちるが、そこでは決定 6 のとおり手打ちの経路が主経路に戻るだけである。

## 実測で解けた点（2026-09-08・開発配備の g6.xlarge）

ハーネスは `deploy/aws/ecs/harness/bench-image-engine.sh`（+ `.py`）。image 役の capacity
provider に **RunTask で 1 タスク**（fetch → ComfyUI → 計測クライアント → S3 へ upload の
4 コンテナ）を流し、ECS Exec は使わない。ComfyUI はコミュニティイメージ
`ghcr.io/lecode-official/comfyui-docker:latest`（5.36 GB 圧縮・ComfyUI **v0.8.2** 焼き込み）を
ECR に複製し、起動時に **v0.34.0 へ checkout**（1〜2 秒）して `pip install -r requirements.txt`
（19〜24 秒）。テキストエンコーダは fp8（`qwen_3_4b_fp8_mixed`・5.6 GB）。数字は同じ箱で
4 回走らせたうちの最後の 2 フェーズ（既定フラグ・`--highvram`）で、絵は目で確かめた。

1. **klein 4B・Z-Image-Turbo・SDXL は同じ L4 で描ける。** 26 枚すべて成功、落ちなかった。
   ワークフローは公式テンプレートのサブグラフを API 形式に写したもの（`bench-image-engine.py`）。
   klein 4B は distilled を 4 ステップ・cfg 1、Z-Image-Turbo は 8 ステップ・cfg 1・shift 3。
2. **`qwen_3_4b.safetensors` は Z-Image と klein で sha256 が同一**（`6c671498…`）。決定 2 の
   「`text_encoders/` はモデルファミリーをまたいで共有」は実証になった。
3. **温まった 1 枚（1024px）**: SDXL 20 ステップ **8.0 秒**（0071 実測 7 の 7.9 秒と一致）、
   Z-Image-Turbo **10.4〜10.6 秒**、klein 4B **3.7〜4.0 秒**、SDXL 512px 2.1〜2.7 秒。
   SDXL＋LoRA は 7.8〜8.1 秒で、**温まっていれば LoRA は無料**。既定フラグと `--highvram` で
   差は無い。
4. **VRAM**: SDXL 6.9 GB、Z-Image 12.2 GB（既定フラグではテキストエンコーダも残って
   17.5 GB）、klein 11.6〜13.2 GB。3 つを同時に VRAM に置くことはできず、ComfyUI が
   入れ替える。
5. 🔴 **切り替えは毎回 EBS からの読み直しで、1〜2.5 分。** 初回と「戻ってきた」回が同じ:
   SDXL 56〜63 秒、klein 106〜110 秒、Z-Image 133〜154 秒。箱の RAM は 15 GB で 3 モデル
   （合計 27 GB）は残せず、VRAM を出たモデルはディスクへ戻る。**決定 4 の「要求毎に切り替え」は
   成立するが、切り替えた要求は温まった 1 枚の 10〜30 倍を払う。** 支配的なのは EBS gp3 の
   読み出し（12.3 GB を 133 秒＝92 MB/s）で、0071 未解決 1 の `useLocalStorage`（インスタンス
   ストア NVMe）が効く場所がもう 1 つ増えた。
6. **60 秒規則（決定 4）は ComfyUI では切り替えの回に当たる**——温まった 1 枚は全部 11 秒未満
   だが、切り替えを含む要求は 60 秒を超える。`/prompt` → `/history` の非同期 API だから問題に
   ならないだけで、同期 API のエンジンなら切り替えの回は必ず切られる。
7. **コールドスタートの内訳**（RunTask から）: 箱 +33 秒で pull 開始、ECR から 5.4 GB
   （圧縮）の pull **205 秒**、S3 → EBS **33.3 GB を 332〜360 秒**（92〜100 MB/s。0071 実測 8 と
   同じ帯。pull と並走）、checkout＋pip 20〜26 秒、ComfyUI が `/system_stats` に答えるまで
   36〜45 秒、最初の SDXL 63 秒——**最初の絵まで 526 秒**。llm 役の 527 秒（0071）と同じ
   桁で、同期するモデルの合計サイズがそのまま効く（決定 9）。
8. 🔴 **`--cache-none` を付けてはいけない。** ローダーノードの出力＝モデル本体がキャッシュ
   されず、**毎要求ディスクから読み直す**（温まった SDXL が 57 秒、Z-Image が 134〜147 秒、
   実行後の VRAM 使用が 280 MiB に戻る）。`--highvram` を足しても直らない。最初の 2 回は
   これで測っていた。
9. 🔴 **同じグラフを 2 回送ると 2 回目は出力キャッシュに当たり 0.5 秒で「実行なし」。**
   温まった時間を測るなら seed を変える。3 回目はこれで無効になった。
10. **箱の事実**: g6.xlarge は ECS に **15,000 MiB** を登録する。**同じ箱に 2 本目のタスクを
    流すと `No space left`**（匿名 host volume は前のタスクの分が片づかない——0071 決定 7 の
    注のとおり）。停止直後の箱に載ったタスクは pull 開始まで **9.5 分 PENDING** だった（新しい
    箱なら 33 秒）。MI の回収を待つほうが早い。
11. **取り込み（HF → S3）は 7.8〜44 MB/s** で予測できず（12.3 GB を 283 秒、5.6 GB を 722 秒）、
    S3 へは 27〜93 秒——0071 決定 3 の根拠がもう 4 点増えた。

帰結: 決定 10 の既定を **klein 4B** にした（3）。決定 4 の ComfyUI 本命は変わらないが、
「要求毎の切り替え」の価格（5）を本文に足し、CP が持つ `warm_model` で **`model` 未指定の
既定を温かいモデルにサーバ側で決める**（決定 7。レビューの答え 7 で「説明文だけでは足りない」
と直された形）。
未解決 7 は解け、8 は半分解けた（自前イメージの大きさは未測のまま——コミュニティイメージと
同じ PyTorch＋CUDA なら 5 GB 級で pull 200 秒、checkout と pip の 20〜26 秒と NAT 依存が消える）。

## P0 の実測（2026-09-08・開発配備）

P0 を実装しながら、開発配備（`af-sandbox` / ap-northeast-1）に実際に載せて測った。フェーズ節が
挙げた 3 つの完了の定義のうち **1 番目と 3 番目は実機で通り、2 番目は単体テストどまり**である
（理由は 10）。GPU は g6.xlarge を都合 40 分ほど、$1 未満。

1. **CP はカタログを種から作り、active set を publish した**（決定 2・7）。起動時のログが
   `engines: llm active set published to /af-ws/engines/llm/active (152 bytes)` と
   `engines: image active set published … (132 bytes)`。中身はそれぞれ

   ```
   {"v":1,"key":"llm","start":"qwen3-coder-30b-a3b","models":[{"id":"qwen3-coder-30b-a3b",
    "f":["llm/Qwen3-Coder-30B-A3B-Instruct-Q4_K_M.gguf"],"c":32768}]}
   {"v":1,"key":"image","start":"sdxl-base-1.0","models":[{"id":"sdxl-base-1.0",
    "f":["image/checkpoints/sd_xl_base_1.0.safetensors"]}]}
   ```

   **4,096 文字（R2(c)）に対して 132〜152 文字。** テストで固定した「モデル 20・LoRA 20」は
   3,200 文字だが、そこに至るまでに一度**素直な形では 4,280 文字で入らなかった**——決定 2 の
   「S3 キーとフラグだけ」を守っても足りず、(a) LoRA は素の S3 キーだけにし（箱はディレクトリを
   走査するので名前も説明も要らない）、(b) フラグの無いファイルは裸の文字列にして初めて収まった。
   4 KB は「気をつければ入る」余裕ではなく、**形を決める制約**である。

2. 🔴 **サイドカーが空回りした。YAML の折り畳みブロック（`>-`）は、より深くインデントされた行を
   折り畳まない。** `jq -r` の下に揃えて書いた filter が改行ごと残り、シェルは
   `jq -r --arg s "$START"`（文書を丸ごと吐く）を実行してから `[(.models[]?|…` を
   コマンドとして探した。結果 `/models/cmdline` は空、エンジンはプレースホルダで起動、
   **サービスは steady state に達し、どこにも理由が出ない**。GPU 1 台と 10 分を払って
   「なぜか絵が返らない」で気づく壊れ方である。literal ブロック（`|-`）に直した。
   再発防止は `deploy/local/engine-sidecar-test.sh`——**テンプレートから実際に配布される
   スクリプトを取り出して走らせる**（スタブ `aws`＋本物の `jq`）。CFN の `Mappings` に置いた
   シェルは、型検査もリンタも無く、唯一の反応が 10 分後の GPU 箱だという場所にある。

3. **決定 1(b)(d) はそのまま観測できた。** スタック更新の最中（10:17:25）に始まったタスクは、
   CP がまだ入れ替わっておらず active set が存在しなかったので
   `engine fetch: no active set at /af-ws/engines/image/active - this engine has nothing to load`
   を出して idle し、**サービスは steady state に達した**。「CP 不在・active set 無しで
   両役のサービスが安定する」——完了の定義の 3 番目——の実地の証拠で、狙って作った状況ですら
   ない。二段階スタンドアップはこれで消える。

4. 🔴 **その裏で穴が出た。3 のタスクは RUNNING のまま永久に温まらない。** 決定 1(c) で足した
   `no_model` は効かない——カタログは空ではないからである。起動期限は `starting` にしか効かず
   （R6）、`running && !warmed` は失敗にならないので、$1.26/時が誰にも止められずに流れ続ける。
   **`running` のまま起動期限を超えたら「RUNNING に達しただけの起動失敗」として止め、
   数え、クールダウンさせる**規則を足した。時計の起点は「いま温かくないこと」ではなく、
   デプロイ作成・**この CP が見始めた時刻**・最後に温かかった時刻の最も新しいもの——さもないと
   健診 1 回の失敗や CP の入れ替えで健全なエンジンを止める。

5. **切り替えは実機で通った（完了の定義 1）。** 同じ prompt・同じ seed 42・同じ 512×512 で:

   | | SDXL base 1.0 | Juggernaut-XL v9 |
   |---|---|---|
   | S3 → EBS | 6,938,078,334 B を **64 秒**（108 MB/s） | 7,105,348,188 B を **39 秒**（182 MB/s） |
   | cmdline | `-m /models/image/checkpoints/sd_xl_base_1.0.safetensors` | `-m /models/image/checkpoints/juggernaut_xl_v9.safetensors` |
   | VRAM | 6,624 MB | 同じモデルファミリーなので同程度 |
   | 起動後 1 枚目（512px） | **11.4 秒** | **21.5 秒** |
   | PNG | 455,316 B | 430,587 B（**絵は明らかに別物**） |

   そして **`describe-stacks` の `LastUpdatedTime` は前後とも `2026-09-08T10:49:24.476Z` で
   動いていない**——CloudFormation を触らずにチェックポイントが変わった、が言葉どおり成立した。
   1 枚目の時間（11.4 / 21.5 秒）は 0071 実測の「温まった 512px は 7.8 秒」より長い。読み込み
   直後の 1 枚に乗る一度きりの費用で、2 点しか無いので幅としてだけ書く。
   - 🔴 **正直に書いておく: この切り替えは Console のボタンではなく、active set を手で
     publish して行った。** super_admin の画面を駆動するにはブラウザのセッションが要り、
     この計測は AWS の資格情報だけで行っている。手で書いた文書が **CP の
     `publishActiveSet` が書くものと 1 バイト違わない**ことは
     `TestEngineActiveSetForTheDevDeployment` で固定した。実機で通っていないのは
     「管理 API が行を書く」1 リンクだけで、それは `TestEngineAdminModelLifecycle` の担当。
     CP 自身の publish は 1 のとおり実機で見えている。

6. **2 つ目のチェックポイントの取り込み**（Juggernaut-XL v9・CreativeML OpenRAIL-M・gated 無し・
   単一ファイル）: HF から 7,105,348,188 バイトを **163 秒**（43.6 MB/s）、sha256 一致、
   S3 へ **48 秒**。0071 決定 3 の「HF は 4〜236 MB/s で予測できない」にもう 1 点。
   🔴 **sd-server 向けは単一ファイルでなければならない。** バケットにある klein 4B と Z-Image は
   分割モデルで、sd.cpp のフラグ組み立てが未測（決定 4(b)）——P2 の ComfyUI の担当である。

7. **S3 レイアウトの移行**（`image/sd_xl_base_1.0.safetensors` → `image/checkpoints/…`）は
   サーバサイドコピーで、NAT を通らず一瞬。決定 2(f) のとおり `ImageModelS3Key` を同じ手順で
   更新した——種が旧キーを指したままなら、最初の起動が fetch で空振りする。

8. 🔴 **G 系の vCPU クォータ 8 に当たった。** 止めた直後に起こし直すと、退場中の箱が 4 vCPU を
   握ったままなので `VcpuLimitExceeded: your current vCPU limit of 8` で placement が数分
   失敗し続ける。`PARAMETERS-60-engines.md` の「The G-family quota」がそのまま起きた形で、
   **起動→停止→起動を繰り返す検証は、退場（実測 456 秒）を待たないと進まない**。

9. 🔴 **この配備の image 役は `mode=on` だった**（この作業より前に誰かが設定した）。
   `aws ecs update-service --desired-count 0` は 30 秒で
   `engine image: start (admin_on)` に取り消される。**モードは CP の設定行で、AWS の
   資格情報では変えられない**——止めるには Console のトグルが要る。0071 の「`off` は永続設定で
   あって一時停止ではない」の裏返しで、`on` も同じく永続である。

10. **完了の定義の 2 番目（`mode=on` のエンジンがカタログ空で起きない）は実機で通していない。**
    カタログを空にするには super_admin の画面が要るからで、`TestDecideEngineAction` の 4 件
    （空なら起こさない／上がっていれば止める／需要があっても起こさない／`off` の理由が勝つ）で
    固定してある。4 の `unwarmed` も同じ表に 4 件。

### P0 で本文に無かった追加

- **カタログの行を作る口が要った。** 種は役ごとに 1 行しか作らず（決定 7）、行を作るもう 1 つの
  経路は P4 の取り込み API である。つまりトグルだけでは**選び先が存在せず、完了の定義 1 に
  到達できない**。P4 のうち「HF から取ってくる」ではない半分——既にバケットに在るファイルが
  何かを書き留めるだけ——を `POST /api/admin/engines/{key}/models` として足した。
  `ecs:RunTask` も PassRole も要らず、CP の IAM は増えない。行は必ず無効で作る。
  対になる `DELETE …/models/{id}` は**行だけ**消す（`s3:DeleteObject` は無い。決定 7）。
- **`harness/probe-image-engine.sh`。** エンジンは私設サブネットにいて CP の SG しか通さないので、
  絵を見る手は「メンバーの `generate_image`」か「VPC の中のタスク」しかない。完了の定義 1 は
  前者では観測できなかった（上の 5)）ので後者を用意した。取り込みタスク定義を借りている
  ——2 コンテナ・共有ボリューム・S3 書き込みという形がそのまま要るものだからで、テンプレートに
  資源を 1 つ足す余裕（820 バイト）は無い。

## P1 の実測（2026-09-08・このコンテナの CPU と開発配備）

P1（llm 役のルーターモード）を実装しながら測った。**上流の性質はまず CPU で確かめ、値段の
かかる観測だけを GPU で行った**——ルーターの挙動はコマンドラインの解釈と preset の読み方で
決まるもので、そのほとんどは CUDA を必要としない。CPU は llama.cpp の公式ビルド `b10853`
（レビュー R1 と同じもの）に stories260K と Qwen2.5-0.5B-Instruct Q4_K_M を置いたもの、
実機は `af-sandbox` / ap-northeast-1 の g6.xlarge を 30 分ほど（$0.6 ほど）。

**完了の定義は 3 つとも実機で通った。** 1 つ目と 3 つ目はエンジン層のプローブで（下の 10）、
2 つ目——起動メニューに `llamacpp/` が 2 つ出る——は**利用者が Console で 2 行目を登録して
有効にし、セッションを起動して**通した（下の 15）。行を作る口だけは AWS の資格情報では駆動
できず（P0 の実測 5 と同じ壁）、そこは人が押した。

### CPU で解いたこと

1. **preset だけでルーターは成立し、セクション名がモデル id になる。** 上流 README の
   「キーが既存のモデルに対応しないなら `model` を書く」経路で、`[qwen3-coder-30b-a3b]` に
   `model = /models/llm/Qwen3-…-Q4_K_M.gguf` と書けば、**ファイル名と無関係にカタログの id で
   引ける**。だから 🔴 **`--models-dir` は使わない**——本文の決定 3 は両方を挙げているが、
   ディレクトリ走査はファイル名をそのままモデル名にするので (a) カタログに無い名前が
   `/models` に並び（決定 7 の「カタログに無いものは無い」と衝突する）、(b) `llm/loras/` の
   サブディレクトリが**モデル 1 つとして数えられる**（上流はサブディレクトリを分割 GGUF と
   読む）。preset だけにすれば両方消える。
2. 🔴 **コマンドラインの `-c` は preset のモデル毎の `c` に勝つ。** `-c 32768` を付けて起動
   すると、`c = 4096` と `c = 384` を宣言した 2 モデルが**両方 `--ctx-size 32768`** で spawn
   された（`/models` の `status.args`）。上流の優先順位「コマンドライン ＞ モデル毎 ＞ `[*]`」
   のとおりで、外せば宣言どおり 4096 / 384 になる。この配備の `LlmExtraArgs` は
   `-ngl,99,-c,32768,--jinja` だったので、**P1 の第一歩は `-c` を外すこと**だった。
   1 つのフラグでカタログの窓が全部消え、しかも誰も気づかない。
3. **ルーターのコマンドラインは子に受け継がれる。** `-ngl 99 --jinja` は各インスタンスの
   `status.args` にそのまま現れる。`--alias` は**ルーターが自分で付ける**ので preset に
   書く必要は無い（本文の決定 3 は `alias` を挙げているが、実測では不要）。
4. 🔴 **空のルーターでも `/health` は `{"status":"ok"}`**（R1(a) の再確認）。加えて
   `status.value` は `loaded` / `unloaded` / `loading` / `sleeping` / `downloading` の 5 値で、
   `sleeping`（`--sleep-idle-seconds` の自動退避。既定 -1 ＝ 無効）も「次の要求が重みを
   払う」状態である。だから warm は **`loaded` が 1 つ以上**とした。🔴 **「既定モデルが
   `loaded`」ではない**——本文の決定 3 はそう書いているが、`--models-max 1` で別モデルに
   交替した箱は「答えているのに warm でない」になり、`running && !warmed` が 900 秒続けば
   P0 で足した `unwarmed` 規則が**会話の最中に箱を止める**。
5. **`GET /models` は autoload を起こさず、ルーターの idle タイマーも動かさない**（上流
   README の除外リスト）。30 秒ごとに叩く warmProbe の宛先として安全である。
   一方 `GET /props?model=` は**ロードを起こす**ので、健診の宛先にしてはいけない。
6. **preset のパスが無いモデルはルーターを止めない。** 起動し、`/models` に並び、そのモデル
   への要求だけが `500 model name=… failed to load` になる。無い id は **400**（R1 と同じ）。
7. 🔴 **`--models-max 1` の退避は「生成中」を待つ。** 片方に 900 トークンを流している最中に
   もう片方へ要求を出すと、ログは `models_max reached … queued at position 1` で止まり、
   **48.6 秒**待たされてから `evicting idle LRU` が動いた。**割り込まれた側の 854 チャンクは
   全部届いた**。ADR 未解決 1(c)（「idle LRU が生成中のモデルをどう扱うか」）はこれで解けた
   ——**答えは「待つ」**で、代わりに待たされた側が「相手の残り＋ロード」を払う。
   🔴 **最初の測定は偽陽性だった**: 割り込みで 14 チャンクで切れたように見えたが、対照実験
   （誰も割り込まない同じ要求）も 14 チャンクで終わっていた——モデルが素直に停止していた
   だけである。`ignore_eos` を付けて 854 チャンク・64.8 秒の走行にして初めて意味のある窓に
   なった。**「切れた」も「0 件」と同じで、道具を先に確かめないと読み違える。**

### 実機で測ったこと（開発配備・g6.xlarge）

8. **2 本目の GGUF の取り込み**（Qwen2.5-Coder-1.5B-Instruct Q4_K_M・Apache-2.0・gated 無し）:
   HF から **1,117,320,768 バイトを 35 秒**（31.9 MB/s）、sha256 一致、S3 へ **2 秒**。
   0071 決定 3 の「HF は予測できない」にもう 1 点（4〜236 MB/s の帯に収まる）。
9. **サイドカーは実機でルーター用に書き換えて動いた。** ログがそのまま証拠になる:
   `llm/Qwen3-…-Q4_K_M.gguf 18556689568 bytes in 159s`（116.7 MB/s）、
   `llm/qwen2.5-coder-1.5b-…gguf 1117320768 bytes in 8s`（139.7 MB/s）、
   `preset /models/llm/presets.ini holds 2 model(s), 'qwen3-coder-30b-a3b' loaded at startup`、
   `cmdline = --models-preset /models/llm/presets.ini`。**決定 9 のとおり合計サイズが
   コールドスタートに乗る**（2 本で 167 秒。1 本なら 159 秒）。
10. **交替は 1 回の試行で答えが返る**（完了の定義 1）。2 回走らせて一致した:

    | 要求 | 1 回目 | 2 回目 | 何が起きたか |
    |---|---|---|---|
    | `qwen3-coder-30b-a3b`（温かい） | **0.7 秒** | **0.4 秒** | 起動時に載っている |
    | `qwen2.5-coder-1.5b` | **10.1 秒** | **10.0 秒** | 30B を降ろし 1.1 GB を載せる |
    | `qwen3-coder-30b-a3b`（戻る） | **281.8 秒** | **276.3 秒** | 18.5 GB を VRAM へ載せ直す |

    どれも **200・1 回の試行・再送なし**で、答えの中身も別のモデルのものだった。戻りの
    276〜282 秒は 0071 実測の 267 秒（ロードのみ）に生成を足した数字で、**これがこの設計の
    価格**である（決定 3）。無い id への要求はルーターが **400** で断った——ゲートウェイの
    `404 model_unknown` は自分の判定で、番号は混ぜていない（実機では観測できていない。
    ゲートウェイを叩くにはセッショントークンが要る）。
11. 🔴 **`/health` は 4 分半のあいだ「ok」と言い続けた。** 交替の最中に 20 秒ごとに両方を
    見た 14 サンプルすべてで `/health` が `200 {"status":"ok"}`、同じ瞬間の `/models` は
    `qwen3-coder-30b-a3b: loading`。**P0 の warmProbe を実機のルーターに向けていたら、
    276 秒のあいだパネルは「準備完了」と言い、`unwarmed` 規則は永久に発火しなかった**。
    決定 3 の warm の再定義は、この 14 サンプルが根拠である。
12. **配備のイメージは llama.cpp `b10830`**（`org.opencontainers.image.version`、2026-09-07 に
    ECR へ複製）。ルーターモード・`--models-max`・preset のカスタムエントリ・
    `load-on-startup` はすべてこの版にある（同じコミットの `tools/server/README.md` で確認）。
    タグ `server-cuda` は動くタグなので、**版は複製した日で決まる**。

### 実機で踏んだ穴

13. 🔴 **ondemand のエンジンは、手で起こしても 90 秒で止められる。**
    `aws ecs update-service --desired-count 1` の直後に `engine llm: stop (idle)` が出て
    pending タスクが落ちた——`engine_llm_demand_at` が前の作業のもので、
    `now - lastDemand ≥ idle(1800s)` が最初のティックで真になるからである。0071 の
    「`on` も `off` も永続設定」の 3 つ目の顔で、**ondemand は「手で起こす」が効かない**。
    需要を作るにはゲートウェイを叩く＝セッショントークンが要る。だから実機は
    **`run-task` で llm のタスク定義を capacity provider に直接流し**（コントローラは
    サービスしか見ないので触られない）、タスクの私設 IP を直接叩いた。
    - 代償: **CP 側の warmProbe は実機で通っていない**。CP はサービスが上がっているときしか
      健診しない（`maintainWarm` はサービスの状態で呼ばれる）ので、Cloud Map に A レコードの
      無い単発タスクは CP から見えない。11 は**同じ問い**をプローブ側から測ったもので、
      warm の判定そのものは `TestEngineWarmProbeReadsTheRouterModelList` が固定している。
14. 🔴 **プローブのタスクロールを CP のものに差し替えると、S3 に書けなくなる。**
    エンジンの `--api-key` は SSM の SecureString で、それを読める役は CP のタスクロール
    だけ——だから `run-task --overrides` の `taskRoleArn` でそれを着せた。すると転記の
    アップロードが `AccessDenied … s3:PutObject` で落ちた。**レビュー R3 の「CP に S3 の
    権限は 1 つも無い」を AWS が言い直した形**である。転記はログへ出すことにした（鍵を
    `RunTask` の環境変数で渡せば CloudTrail に永久に残るので、その道は採らない）。

15. **起動メニューの 2 つも実機で通った**（完了の定義 2。利用者が Console を押した）。
    パネルの「バケットのファイルを登録する」で 2 行目（`qwen2.5-coder-1.5b`・窓 32768/4096・
    サイズ 1,117,320,768）を作り、有効にした。CP のログが連鎖をそのまま出している——
    `llm catalogue row registered: qwen2.5-coder-1.5b (1 file(s), disabled)` →
    `llm active set published … (242 bytes)` →
    `catalogue change for llm pushed to 1 workspace(s)` → Agent の
    `GET /internal/engine/catalog 200`。**ピッカーに `llamacpp/` が 2 つ並び、
    2 つ目でセッションが起動した。** その後の CP 入れ替えでも、publish されたのは同じ 2 行
    （`models=llamacpp/qwen2.5-coder-1.5b,llamacpp/qwen3-coder-30b-a3b`）——手で書いた文書では
    なく DB の行が真実だから、という当たり前の観測である。
    - 🔴 **登録フォームには窓の欄が無かった。** P1 で窓がモデル毎になったのに、行を作る唯一の
      UI がそれを宣言できず、登録したモデルは `context_tokens` 0 ＝ opencode では context 0 ＝
      **自動コンパクションが切れる**。窓（context と出力上限。両方揃ったときだけ送る）と
      サイズ（「同期 +N 秒」の唯一の出どころ）を足した。

### P1 で本文に無かった追加

- **`harness/probe-llm-engine.sh`。** 絵ではなく文字なので `probe-image-engine.sh` は使えず、
  同じ作り（取り込みタスク定義を借り、CP の SG を着る）で llm 版を足した。`--watch` は
  交替の最中に `/health` と `/models` を 20 秒ごとに並べて見るモードで、11 はこれで測った。
- **`files[].bytes`（宣言されたサイズ）と、パネルの「同期 +N 秒（推定）」。** 決定 9 の
  「有効な GGUF の合計サイズで決まる」を人が読める形にするには、CP がサイズを知る必要が
  ある。CP は S3 を見られない（R3）ので**登録時に宣言する**——マニフェストと同じ姿勢で、
  移行は要らなかった（`files` は既に JSON 列）。104 MB/s（実測帯の遅いほう）で割る。
- **`warm_model` と「モデル交替 N 回」。** 決定 3 が「隠さず admin パネルに出す」と言って
  いるもの。ゲートウェイが usage 行から最後に答えたモデルを拾い、変わった回数を数える。
  どちらも**この CP のプロセス内**の事実で（需要の窓と同じ）、パネルの文言もそう言う。
- **エンジン表の `warmPath`。** 「重みが載っているか」を「健診」と別の問いにするための宣言。
  provider 名から導出しない（ADR 0053）——同じ API を話す 2 つ目のエンジンが、誰も選んで
  いない健診方法を継ぐことになる。

## P4 の実測（2026-09-09・実装しながら）

Console からの取り込み（決定 6）と、gated・ライセンス受諾（決定 10）、`MODE=delete`（決定 7）。
この節は 2 つに分かれる——**上流 API を測って設計が変わった点と実装で分かったこと**（以下）、
そして**実機で押し込んだときに出たもの**（次の節）。後者のほうが数が多い。

1. 🔴 **gated リポジトリのメタデータは匿名で読める。** FLUX.1-dev と SD3.5 Medium で確認:
   `api/models/<repo>?blobs=true` は `gated: "auto"`・`license: "other"`・
   `license_name`（`flux-1-dev-non-commercial-license` / `stabilityai-ai-community`）・
   **29 ファイル分の sha256 とサイズ**を鍵無しで返し、**401 になるのは
   `resolve/main/<file>` のダウンロードだけ**だった。設計がこれで決まった——**CP は HF の
   トークンを持たない**。解決は CP、取得はトークンを持つ取り込みタスク、という分担が
   そのまま成立する（決定 6 の「トークンは箱に載せない」を CP にも適用できた）。
2. **Civitai の API は生きている**（未解決 4）。`api/v1/model-versions/128713` が匿名で
   `files[].hashes.SHA256`（大文字）・`sizeKB`（**小数のキロバイト**。1024 倍してバイトに
   直す）・`downloadUrl`・`baseModel`・`model.type` を返し、ダウンロードは署名付き R2 への
   302 で鍵無しで 200 だった。ライセンス欄は HF のような形では無いので、カタログには
   「モデルページを見よ」と書く——**推測した名前を他の本物と並べない**。
3. **`commercial_use` はライセンス名から引く**（決定 10）。`non-commercial` / `-nc` を含めば
   `no`、Apache-2.0 / MIT / OpenRAIL++ / CreativeML OpenRAIL-M なら `yes`、それ以外は
   `unknown`。**`unknown` は本物の答え**で、世界中のライセンスの一覧を持つより、間違えて
   `yes` と言わないほうが安い。
4. 🔴 **移行 SQL のコメントに `;` を書いて、また CP を起動不能にした。** 移行の実行側は
   ファイルをセミコロンで素朴に分割するので、コメント中の 1 つが CREATE TABLE を半分に
   切り、`incomplete input` で全テストが落ちた。[[cfn-embedded-shell-yaml-folding]] に
   ある既知の罠を、**その罠を説明する警告文の中でもう一度踏んだ**（`` `;` `` と書いた）。
   ファイル冒頭に「この文書のコメントにセミコロンを書くな」と言葉で書いた。
5. **ジョブは行、作る予定のカタログ行ごと。** ダウンロードは分単位（HF は 4〜236 MB/s）で、
   その最中に CP は入れ替わりうる。最初はプロセス内の map に持っていたが、それだと
   「バイト列はバケットに在るのに、対応する行は誰にも作れない」状態が残る。ジョブ行に
   `spec`（作る予定の行の JSON）を持たせ、**別プロセスが finish しても行が作られる**ことを
   テストで固定した。
6. **失敗理由はタスクの言葉で出す。** `DescribeTasks` は「fetch が 1 で終わった」しか言わない。
   `logs:GetLogEvents`（このスタックのロググループだけ）を CP に足し、
   `ingest: sha256 mismatch: got … want …` をそのままパネルに出す——「sha256 が違う」と
   「gated で 401」は、読んだ人がやることが全く違う。
7. **削除はやはり取り込みタスクの仕事**（決定 7）。CP に `s3:DeleteObject` は無いままで、
   `DELETE …/models/{id}?purge=1` は行を消したあと `MODE=delete` のタスクを起こす。
   行を先に読んでから消す——**キーは行の中にしか無いので、順番を逆にすると「成功」と言って
   何も消さない**。

### P4 で本文に無かった追加

- **「調べる」と「取り込む」を 2 本の API に分けた。** ライセンスも gated も見せる前に
  「同意」を出したら、それは同意ではない。`POST …/ingest/resolve` は何も起こさずに
  ライセンス・サイズ・sha256・gated・`can_ingest` を返し、パネルはそれを描いてから
  チェックボックスを出す。
- **`engine_ingest_jobs` 表**（sqlite `0058` / pg `0043`）と、`engine_models` の
  `license_accepted_by` / `license_accepted_at` / `commercial_use`。受諾は**人の行為の記録**で、
  モデルカードからは後で再現できない。

## P4 を実機で押した（2026-09-09・開発配備）

Console のボタンは利用者に押してもらい、こちらは ECS・S3・両方のロググループで裏を取った
（管理 API は super_admin のブラウザセッションが要り、AWS の資格情報では駆動できない）。
**完了の定義は 3 つとも通った**——ungated が 1 本、gated の断り、gated の取り込み。そして
🔴 **押してみて初めて出た欠陥が 6 つあり、そのうち 1 つは経路が丸ごと死んでいた。**

1. **ungated を 1 本（`qwen2.5-coder-0.5b`・491,400,064 バイト）。ボタンから行まで 72 秒。**
   `POST …/ingest` が 1.198 秒で RunTask、+16 秒で pull 開始、pull 6.7 秒、HF から **13 秒**
   （37.8 MB/s）、sha256 の照合 2.1 秒（234 MB/s）、S3 へ **2 秒**（245 MB/s）、CP が拾うまで
   +26 秒（10 秒ポーリング＋ECS の突き合わせ）。S3 のオブジェクトは**宣言値と 1 バイトも
   違わず**、sha256 も上流の API から独立に読んだ値と一致した。行は**無効で**現れた。
2. **gated の断りは、タスクを 1 つも起こさない。** `hasToken:false` の配備で FLUX.1-dev を
   「調べる」と `can_ingest:false`、赤い非商用と「トークンがありません」の 2 行、同意
   チェックは押せない。**ingest family のタスクは RUNNING も STOPPED も 0 件のまま**だった。
   ついでに「調べるは何も起こさない」も同じ観測で確かめられた。
3. 🔴 **gated のメタデータが匿名で読めることが、実機の CP でも証明された**（実測 1 の裏取り）。
   トークンを持たない CP が `POST …/image/ingest/resolve` に **186 ミリ秒で 200** を返した。
   ungated の解決は 264 ミリ秒。設計の前提が机上でなく配備で成り立っている。
4. **gated を本当に取り込んだ（FLUX.1-dev・23,802,932,552 バイト）。ボタンから行まで
   15 分 51 秒。** pull 7.4 秒、HF から **536 秒**（44.4 MB/s・トークン付き）、sha256 の照合
   179 秒（133 MB/s）、S3 へ **178 秒**（134 MB/s）、CP が拾うまで +2 秒。`fetch` / `upload`
   とも exit 0。
5. **取り込みタスクは Fargate（2 vCPU / 4 GB）で走り、GPU の箱は 1 台も起きない。**
   `launchType: FARGATE`、capacity provider は無し。23.8 GB の取り込みに $1.26/時の箱を
   買わない——決定 6 が「取り込みは起動の経路の外」と書いたことが、そのまま観測できる。
   ディスクは `IngestDiskGiB=80` で足りた。
6. 🔴 **`HfTokenSecretArn` を渡しても、取り込みタスクは起動すらしない。パラメータも文書も
   あるのに、それを動かす IAM が無かった。** ECS はコンテナの `Secrets` を**コンテナが存在
   する前に**解決するので、使うのは**タスクロールではなく実行ロール**である。20-platform は
   その実行ロールの `secretsmanager:GetSecretValue` を `secret:rds!*`（DB のパスワード）に
   絞っており、付いている管理ポリシー `AmazonECSTaskExecutionRolePolicy` に Secrets Manager は
   含まれない。渡していれば `ResourceInitializationError` で死んでいた——**ダウンロードの 401
   ですらなく、取り込みのログには何も出ない**（コンテナが 1 つも走らないため）。
   `ExecHfTokenPolicy` を 60-engines に足した（ARN を渡したときだけ作られ、その 1 つの ARN
   だけを対象にし、`CpIngestPolicy` と同じやり方で取り込んだ実行ロールに名前で付ける）。
   決定 6 の「そのスタックの資源にしか効かない権限だけ」に収まる。**押し込む前に見つかった
   のは運が良かった**。
7. 🔴 **取り込みが完了しても、そのとき作られた行が一覧に出ない。** ジョブは「完了」になる
   のに、モデルの一覧は前のまま。再取得する者が誰もいなかった——ジョブのポーリングは
   `running`/`pending` の間だけ回ってジョブ一覧しか読まず、エンジンのポーリングは
   `starting`/`stopping` か `ondemand` かつ `running` のときだけで、**停止中のオンデマンドの
   エンジンはどちらにも当たらない**。取り込みが作る行は設計上いつも無効なので、これは
   「管理者が有効にしに来たまさにその行」が見えないということで、P4 の完了の定義に直接
   当たる。
8. 🔴 **取り込みの失敗が、日本語の画面に英語で出る。** ファイル名を 1 文字短く打ったら
   `the repository does not list flux1-dev.safetensor` と CP の開発者向け message がそのまま
   出た。`errText` は `err.<code>` を i18n から引き、無ければ message に落ちる。取り込みが
   出しうる**コード 15 個すべてに翻訳が無く**、しかも既存のガード
   （`TestCPEmittedErrCodesHaveConsoleCatalogEntry`）は **`errcodes.go` の定数しか見ない**ので、
   `engine_ingest.go` に文字列直書きされたそれらは**一度も検査されていなかった**。定数に
   上げてガードの視界に入れ、翻訳を足し、パネルを `errText` から `errDetail` に替えた
   ——「どのファイルが」は message 側にしか無く、訳を足すだけだと理由が消える。
9. 🔴 **ファイル名の自由入力が、打ち間違いと区別できない拒否を生む。** 上の 1 文字は
   正しく 404 になるが、「存在しないファイル」と見分けが付かない。HF の応答には全ファイルの
   名前・サイズ・sha256 が既に入っていて、**CP はそれを受け取って捨てていた**。
   `POST …/ingest/files` を足して選択式にした。🔴 **ただし最初の絞りは甘く、FLUX.1-dev で
   9 個並んだうち 5 個が `…-00001-of-00003.safetensors` のシャードだった**——1 つだけ
   取り込んでもモデルにならない。「行き止まりを見せない」と書いた絞りが、まさに行き止まりを
   見せていた。シャードを外し、最上位のファイルを先に並べて 4 個になった。
10. 🔴 **同じ id で取り込むと、既存の行が黙って置き換わる。** 行を書く `PutEngineModel` は
    `(role, id)` の upsert で、種の投入と手動登録では正しいが、取り込みでは既存の行の
    ファイル・ライセンス・sha256 を置き換えたうえ `enabled=false` にする。数分の
    ダウンロードのあとにエンジンが起動時のチェックポイントを失い、**2 つの出来事を結び付け
    られる者は誰もいない**。`RunTask` の前に 409 で断るようにした。
11. 🔴 **取り込んだファイルを消す口が無かった。** 決定 7 の `?purge=1` は CP 側に完全に
    実装されていた（行を先に読み、`MODE=delete` を起こし、監査に残す）のに、**Console 側に
    `purge` という文字列が 1 つも無かった**。実機で「登録を消す」を押すと行は消え、S3 の
    491 MB は残り、`MODE=delete` のタスクは 1 つも起きなかった。サーバにあって入口が無い。
    2 段の確認にして、「バケットのファイルも削除する」を明示的に選ばせる形にした。

### 実機で本文に無かった追加

- **`POST …/ingest/files`**（候補の一覧）。resolve と同じ 1 回の読みから、そのエンジンが
  読み込めて sha256 のあるファイルだけを返す。シャードと LFS ポインタの無いファイルは
  外す——出せば、少し後で resolve が断る行き止まりを見せることになる。
- **窓の自動取得と、出力上限の分数。** HF は GGUF ヘッダを解析して `gguf.context_length` に
  載せている（3 publisher の 4 リポジトリで確認）。🔴 **ただしこれはアーキテクチャの上限で、
  この配備で回せる窓ではない**——30B は 262144 と申告するが L4 に入らないので 32768 で
  走らせている。「モデルの上限 N」と誰の数字かを明示して**空の欄にだけ**入れる。出力上限は
  どこにも公開されておらず（モデルの属性ではなく配備の方針である）、窓からの分数
  （1/4・1/8・1/16）の選択にした。1/8 は今の 32768 → 4096 と一致する。
- **`engine_models.source`**（移行 `0060` / pg `0045`）。行はライセンスのスナップショットを
  持つのに、それが何の属性であるか——どのリポジトリから取ったか——を持っていなかった。
  id は 1 つの配備の 1 つの役の中でだけ一意であればよく、起動メニューで読む名前でもあるので
  短いままにし、**別ベンダーの同名は 409 で断って、出所は行に残す**。
- **削除の 2 段確認。** 「登録を消す」と「ファイルも消す」は別の行為で、前者だけだと
  バケットに誰も到達できないバイト列が残って課金され続ける（実測: 491 MB が行より長生き
  した）。既定は安全側で、開くたびに明示的に戻す。

## P5 の実装——HF トークンを Console から登録する（2026-09-09）

未解決 12 の決定（DB を正本に、Secrets Manager を運搬路に）を、レビューが薦めた**形 (b)**
——スタックが**常に**秘密を作る——で実装した。P5 の他の項目には手を付けていない。

1. **着手条件（テンプレートの余白）を先に払った。** 資源 1 つを足すのに 51,200 バイトの壁が
   足りない、というのが着手条件だった。**パラメータ `HfTokenSecretArn` そのものを消した**
   （説明を含めて約 370 バイト）ことと、スタックの `Description` の散文を
   `PARAMETERS-60-engines.md` の「What this stack is」へ移したことで払えた。結果は
   **50,869 バイト**（余白 331）。「コメントに移す」では 1 バイトも減らない、は今回も同じ。
   🔴 **消したパラメータは既存の配備を止めうる**——`cloudformation deploy` はテンプレートに
   無いキーを `--parameter-overrides` で渡されると拒否し、`params/60-engines` は当時のスタックの
   スナップショットなので行が残る。`env.sh` に `af_param_drop` を足し、standup が
   60-engines を配備する直前に落とす（`update.sh` は元々パラメータを渡さないので影響が無い）。

2. **秘密は常に在る。** `HfTokenSecret` を無条件で作り、`SecretString: "-"` の番兵で始める。
   `Secrets: [{ Name: HF_TOKEN, ValueFrom: !Ref HfTokenSecret }]` は `!If` を外して常設にし、
   fetch のシェルが `[ "$HF_TOKEN" != - ]` を「トークン無し」と読む。タスク定義は CFN のもので
   静的なので、**トークンの有無で `Secrets` ブロックが現れる形だと結局 CFN を 1 往復する**——
   利用者の不満はそこにあった。名前は付けない（付けると削除の復旧期間中に同名で作り直せない）。

3. **「読み戻さない」は IAM で縛った。** `CpIngestPolicy` に `secretsmanager:PutSecretValue` を
   **その 1 つの ARN だけ**に足し、`GetSecretValue` は与えない。`ExecHfTokenPolicy`（ECS は
   `Secrets` を**実行ロール**で解決する。20-platform はそれを `secret:rds!*` に絞っているので、
   無いとタスクが起動時に死ぬ）は条件付きをやめて常設にした。

4. **正本は DB。** 設定ストアの 4 行（`engine_hf_token` ＝ 封をした値・`_key_ref`・`_by`・
   `_at`）で、封は既存の `custodian`（テナントの IdP client secret と同じ）。keyRef は
   テナント id ではなく固定の `deployment`——この値はテナントより長生きする。**配備全体で
   1 つ**（決定 6 のまま。テナント毎にすると、A の同意で staged したモデルを B が使う）。

5. **秘密を先に書き、それから DB に保存する。** 逆順だと、`PutSecretValue` が IAM で落ち続ける
   配備で**パネルは「登録済み」と言い、取り込みは毎回匿名で出て、返ってくる 401 はライセンスの
   話をする**。逆の失敗（秘密は書けたが DB に保存できなかった）は次の登録で上書きされるだけで、
   その秘密を読むのはこの CP が起こす取り込みしか無い。

6. **取り込みのたびに書き直す。** 「古くなっていたら」ではない——**古いかどうかを知る方法が無い**
   （`GetSecretValue` が無い）。加えてスタックを作り直すと秘密は番兵に戻るのに DB にはトークンが
   残る、という組み合わせが実在する。症状は 401 だけなので、`start` の先頭で毎回 `stage` する。

7. **gated の判定が「スタックの申告」から「DB の事実」へ移った。** エンジン表の
   `ingest.hasToken` は `ingest.tokenSecret`（ARN）に置き換わった。CP はスタックより先に
   上がるので、**古い表（`tokenSecret` 空・`hasToken` あり）はそのまま動く**: 登録はできない
   が gated は通る、とパネルが名乗る（`stack_token`）。

8. **パネルは値を持たない。** 入力は `type="password"` の書き込み専用で、状態は
   「登録済みか・誰が・いつ」だけ。**表示できる現在値が存在しない**（CP が読めない）ので、
   埋まっているように見える欄は配備が裏付けられない嘘になる。

**試験**（Go 7 本・DOM 3 本）。退行を実際に入れて捕まることまで確かめたのは 5 件——
順序を逆にする（登録が残る）、`clear` が番兵を書かない（消したのにトークンが生きている）、
`start` が `stage` しない（作り直したスタックで 401）、保存後に欄を消さない（画面に値が残る）、
`available: false` でも欄を出す（古いスタックで押せるボタン）。

**実機では未検証**である。確かめたいのは 3 つ: `PutSecretValue` が実際に通ること（IAM の
`Roles:` はロール名の切り出しに依存している）、番兵の `-` で取り込みが**匿名として**成功する
こと、そして gated（SD3.5 Medium・FLUX.1-dev）が登録後に 401 なしで取り込めること。3 つ目は
未解決 7 の「gated の 2 つを `HF_TOKEN` のある配備で測る」がそのまま残っている宿題でもある。

（**この 3 つは 2026-09-10 に実機で押した**——次節。3 つとも通った。）

## P5 を実機で押した——HF トークンの Console 登録（2026-09-10・開発配備）

前節が「実機では未検証」として挙げた 3 点を、**GPU を起こさずに**押した（取り込みは Fargate の
タスクだけで、image の役は `mode: off` のまま一度も起きていない）。**3 つとも通った。** 時刻は
すべて UTC、秒数とバイト数は取り込みタスクのログそのままである。

1. **`PutSecretValue` は実配備でも通る。** Console の管理パネルから登録すると
   `GET /api/admin/engines/hf-token` が
   `{"available":true,"configured":true,"updated_by":"…","updated_at":"2026-09-10T13:50:14Z"}`
   を返し、秘密の `LastChangedDate` も同じ 13:50:14Z。**IAM の `Roles:`——インポートした ARN から
   ロール名を切り出す形——は実配備で解けている。** さらに項目 6（取り込みのたびに書き直す）が
   実際に走っていることが版の並びで見えた: `list-secret-version-ids` は版を 4 本返し、スタックが
   作った番兵と 13:50:14Z の登録のあとに **13:52:59Z と 13:53:05Z** が並ぶ。この 2 つは下の
   `POST …/ingest` を出した時刻そのもので、`stage` が取り込み 1 回につき 1 版増やしている。

2. **番兵 `-` の取り込みは匿名として成功する。** `DELETE hf-token` は
   `{"available":true,"configured":false}` を返し、秘密は 14:02:56Z に番兵へ戻る。その状態で
   公開リポジトリ（`madebyollin/taesdxl` の `taesdxl_decoder.safetensors`・4,895,612 B）を
   取り込むとジョブは `done` になり、ログは 3 行だけ:

   ```
   ingest: fetched 4895612 bytes in 2s
   ingest: sha256 ok f6013131e7eb412ef20113f1acc2ea7d3e47e53196ca0530fa65d9b61d814b61
   ingest: uploaded image/vae/r1-taesdxl-decoder.safetensors in 1s
   ```

   Authorization 由来の行は 1 つも無い。**この取り込みで秘密の `LastChangedDate` は動かなかった**
   （14:02:56Z のまま）——トークンが無いとき `stage` は何も書かない、が実測で裏づいた。番兵の下では
   gated の判定も反転する: `resolve` が `can_ingest:false` / `deployment_token:false` を返し、
   `POST …/ingest` は **タスクを起こす前に** 400 `gated_no_token` で断る。

3. **gated は 401 なしで取り込める（未解決 7 の残り半分）。** SD3.5 Medium と FLUX.1-dev の
   gated リポジトリから、それぞれ**最小のファイル**を 1 つ。どちらも
   `vae/diffusion_pytorch_model.safetensors`（167,666,902 B）で、FLUX.1-dev では
   `ae.safetensors`（335,304,388 B）より小さい。gated はリポジトリ単位の判定なので 1 ファイルで
   足りる——**23.8 GB の `flux1-dev.safetensors` は L4 に載らないので取り込んでいない。**

   ```
   13:53:50  ingest: fetched 167666902 bytes in 5s          # FLUX.1-dev
   13:53:51  ingest: sha256 ok f5b59a26851551b67ae1fe58d32e76486e1e812def4696a4bea97f16604d40a3
   13:53:53  ingest: uploaded image/vae/r1-flux1-dev-vae.safetensors in 1s
   14:00:27  ingest: fetched 167666902 bytes in 8s          # SD3.5 Medium
   14:00:28  ingest: sha256 ok 8f53304a79335b55e13ec50f63e5157fee4deb2f30d5fae0654e2b2653c109dc
   14:00:29  ingest: uploaded image/vae/r1-sd35-medium-vae.safetensors in 1s
   ```

   どちらのログにも 401 は無い。**匿名の解決は gated でもそのまま効いている**——CP は
   `?blobs=true` から sha256・サイズ・ライセンス（`stabilityai-ai-community` /
   `flux-1-dev-non-commercial-license`）・`gated:true` を鍵無しで取り、落とすのはタスクだけ、
   という決定 6 の分業が実経路で確かめられた。

🔴 **gated が断るのは 401 とは限らない——403 があり、意味が違う。** SD3.5 Medium の最初の試行は
失敗し、ログはこの 1 行だけだった:

```
curl: (22) The requested URL returned error: 403
```

同じトークンで FLUX.1-dev が同じ分のうちに通っていたので、**トークンは届いている**。403 は
「認証は通ったが、このリポジトリへのアクセスが無い」——操作者のアカウントが
`stabilityai-ai-community` に同意していなかった（fine-grained トークンで「public gated repos の
内容を読む」権限が欠けている場合も同じ姿になる）。同意を入れてから同じファイルを再試行して通った。
本 ADR と `PARAMETERS-60-engines.md` は 401 を「トークンが無い」の症状として書いているが、
**401 と 403 は読む人に別の宿題を出す**: 401 は配備にトークンが無い（または末尾改行が混じった）、
403 はそのアカウントがそのリポジトリに同意していない。パネルはログの行をそのまま出すので、
区別できるのは読む人だけである。

**消したもの・残したもの。** 検証で作った 3 行（`r1-flux1-vae-probe`・`r1-sd35-vae-probe2`・
`r1-anon-probe`）はどれも `enabled` にせず、検証後に**行だけ**消した——`?purge=1` は使っていない
（同じ S3 キーを他の行が参照していると purge が黙って消す欠陥の修正が別にある）。バケットには
3 つのバイト列が残っている: `image/vae/r1-flux1-dev-vae.safetensors` と
`image/vae/r1-sd35-medium-vae.safetensors`（各 167,666,902 B）、
`image/vae/r1-taesdxl-decoder.safetensors`（4,895,612 B）——合計約 340 MB。

## P2 の実装——ComfyUI（2026-09-10）

未解決の点 8・9 が解けた翌々日、フェーズ節の P2 一覧をそのまま実装した。CI での検証は
できたが、**GPU での実機検証はまだ**（完了の定義は次の実機セッションの宿題）。

1. **自前 Dockerfile とCI。** `deploy/aws/ecs/comfyui/Dockerfile` は `pytorch/pytorch:
   2.5.1-cuda12.4-cudnn9-runtime`（→ 下記 6 で `2.9.1-cuda12.8` に上げた。torch 2.5.1 では
   `comfy-kitchen` が import で落ちる）の上に ComfyUI を `v0.34.0` 固定でクローンし、Manager は
   入れない（0071 決定 6）。焼くのは `dev-image.yml`/`release.sh` に統合せず、専用の
   `workflow_dispatch`（`.github/workflows/comfyui-image.yml`）にした——ComfyUI のピン留めは
   アプリのリリース周期と無関係で、統合すると毎リリースで同じ内容を焼き直すことになる。
   🔴 **`gh workflow run` はデフォルトブランチに無いワークフローを起動できない**（このセッション
   では develop に無い状態で叩こうとして気づいた）。このサンドボックスに Docker も無いので、
   Dockerfile と CI ファイルだけ先に develop へ小さく PR してマージしてもらい、そこで初めて
   ビルドを確認した——**通った**（`v0.34.0-test1` タグで push まで成功）。
2. `20-platform.yaml` に ECR リポジトリ `af-comfyui`（R4 の前提）。`standup.sh` の images 段は
   `ImageEngine` を読んで `af-sdcpp`/`af-comfyui` のどちらか一方だけを複製する。
3. `60-engines.yaml` に `ImageEngine`（`sdcpp`/`comfy`、既定 `sdcpp`）。image ロールのコンテナ
   （名前を `sd` から `engine` に改名）の `Image`/`Command` と、engine table の
   `health`/`provider` を `!If` で切り替える——役もタスク定義もサービスも増やさない
   （決定 4）。🔴 **51,200 バイトの壁**は、決定 12 のときと同じ手口（重複した長文の
   Description/コメントを `PARAMETERS-60-engines.md` へ寄せる）で空きを作ってから足した
   （足す前の空き 331 バイト → 作業後 1,094 バイト）。`cfn-lint`・
   `ecs-lifecycle-stub-test.sh`・`engine-sidecar-test.sh` は green（バイト超過を実際に
   起こしてから捕まえることも確認した）。
   - fetch サイドカーは**無改造で足りた**——PRESET_FILE が空のときの経路は元々 role を
     区別しておらず、有効な全モデルの files を同期する（「start」を先に、「rest」を後で）。
     decision 9 の文章が「image は selected だけ同期する」と書いていたのは 2026-09-09 の
     変更（「rest」をバックグラウンドで足す）で既に事実と食い違っていたので、ついでに直した。
     🔴 **訂正（2026-09-10）: 無改造では足りていなかった。** 「rest」を書く行は
     `if [ -n "$PRESET_FILE" ]` の**中**にあり、`else` は `keys.rest` を空にする。image ロールは
     `PRESET_FILE=""` なので必ず `else` を通る。当時これが正しかったのは、エンジンが
     llama.cpp（ルータ・preset あり）と sd.cpp（どちらも無し・1 枚しか載せない）の 2 つだけで、
     「preset を持つ＝ルータ＝他のモデルも要る」という等式が成り立っていたから。**comfy は
     preset を持たないルータで、その等式を破る。** start 以外のモデルは箱に降りて来ず、
     ComfyUI は `Value not in list: unet_name: 'flux-2-klein-4b.safetensors' not in []` を返す
     ——ファイルは active set にも S3 にもあり、グラフ中の名前も正しいのに。しかもサイドカーは
     事実に反して `every enabled model is on this box` と出力しており、それが取り込みログを
     健全に見せていた。`SYNC_ALL` で直した（下の「P2 を実機で押した」の 4）。
   - comfy の起動コマンドは `/models/cmdline` の**中身**を読まない（`-m` に相当するものが
     無い——チェックポイントは要求ごとに provider が組む グラフ JSON の中で選ぶ）。
     ファイルが空でないことだけを「有効なモデルがある」ゲートとして使う。統合は
     `ln -sfn /models/image /ComfyUI/models` の 1 行——0071 決定 6 の S3 配置がそのまま
     ComfyUI の規約なので、これだけで済む。
     🔴 **訂正（2026-09-10）: 1 行では済まなかった。** ComfyUI のリポジトリは `models/` を
     **実ディレクトリとして追跡している**（`checkpoints/` などが `put_..._here` 付きで存在する）
     ので、Dockerfile の `git clone` がそれを焼き込む。リンク名が実ディレクトリのとき `ln -sfn`
     は**その中に** `/ComfyUI/models/image` を作るだけで、`models/checkpoints/` は同梱の空
     プレースホルダのまま残る（`-n` は「ディレクトリへの symlink」にしか効かない）。エンジンは
     起動し `/system_stats` に答えヘルスも通り、全リクエストが
     `ckpt_name: 'sd_xl_base_1.0.safetensors' not in []` で 400 になる。`rm -rf /ComfyUI/models`
     が先に要る——**末尾スラッシュ厳禁**（2 回目以降は symlink なので、付けると共有モデル
     ボリュームの中身を消す）。bench が見逃したのは `--baked` がボリュームを `/ComfyUI/models`
     に**直接 bind mount** するため。mount はディレクトリを置き換えるが symlink は置き換えない。
4. **provider `comfy`**（`workspace/agent/internal/imagegen/comfy.go`）。`/prompt`
   （sdcpp と同じ 503 engine_waking リトライ）→ `/history/<id>`（ポーリング）→ `/view`
   の3段。**generate のみ**——edit/inpaint はモデルファミリーごとの image-to-image グラフ（LoadImage +
   VAEEncode 系）が誰にも測られていないので、今回のスコープから明示的に外した
   （完了の定義自体が generate だけで満たせるため）。
5. **5 ファミリーのワークフローテンプレート**（`comfy_workflows.go`）。SDXL・Z-Image-Turbo・
   FLUX.2 klein は bench-image-engine.py（実測で解けた点、GPU 検証済み）からの移植で、
   入力（プロンプト・seed）も同一——ゴールデンテストは実測と同じグラフを固定している。
   FLUX.1・SD3.5 は公開されている標準レシピからの新規実装で、**このセッションでは
   実機未検証**。ゴールデンテストは「今の形」を固定するだけで、正しさの証明ではない。
   カタログの `files[]` は sd.cpp 由来の `Flag` 語彙（`--diffusion-model`・`--clip_l`・
   `--t5xxl`・`--vae`）をそのまま再利用し、ComfyUI 用の第二の語彙を作らなかった——
   klein/Z-Image のような sd.cpp が対応していないモデルファミリーでも、取り込み時に同じ4値から選べる。
6. **`generate_image` の `model` 引数**（決定 5・7）。`provider` と同じ「本当に選べるときだけ
   出す」規則で、有効なチェックポイントが2つ以上のときだけ enum が現れる。説明文に
   カタログの `description` と、今ロードされているモデル（`warm`）を書く。
   🔴 **副産物の発見**: `warm_model`（決定 7）は image ロールでは今まで一度も動いていなかった
   ——sd-server も ComfyUI も応答に `usage.model` を持たないので、`recordUsage` の早期
   return が常に `noteServed("", ok)` を呼んでいた。sdcpp にとっても意味のある修正で
   （今まで image エンジンの `warm_model` パネル表示は常に空だった）、`X-AF-Model` ヘッダを
   Workspace 側の両 provider が送るようにして直した。
   もう1つ、`engine_gateway.go` の `dial()` は upstream への path 組み立てで常に `/v1/` を
   差し込んでいたが、ComfyUI のネイティブ API はそれを持たない——provider が `comfy` の
   ときだけ差し込みを止める `engineUpstreamPrefix` を追加した（sdcpp/llamacpp は無変更）。

7. 🔴 **実機で押してすぐ壊れた——自前イメージが起動すらしなかった。** `bench-image-engine.sh`
   に `--baked`（自前イメージをコミュニティイメージの代わりに測るモード。checkout/pip をせず
   `/ComfyUI` を直接ワークディレクトリにする）を足して g6.xlarge に流したところ、ComfyUI が
   import の時点でクラッシュ: `comfy-kitchen==0.2.31`（`requirements.txt` の依存）が
   `list[int]` 型引数を持つカスタム op を登録し、**torch 2.5.1 の
   `torch.library.infer_schema` がその PEP 585 ジェネリック綴りを認識しない**
   （`ValueError: infer_schema(func): Parameter kernel_size has unsupported type list[int]`）。
   ComfyUI 本家の README は「torch 2.7 が最低限のサポート」と明記していた——CI は
   「ビルドが通る」しか確認しておらず、**ビルドが通ることと起動することは別**だった。
   ベースイメージを `pytorch/pytorch:2.9.1-cuda12.8-cudnn9-runtime` に上げて再ビルド・
   再実機検証し、**通った**。

**完了の定義（フェーズ節）を実機で満たした**（2026-09-10・g6.xlarge・`--baked`）。同じ
ComfyUI プロセス 1 つ（サービス／タスクの再起動無し）の中で SDXL → Z-Image-Turbo →
FLUX.2 klein 4B → SDXL → Z-Image-Turbo → klein 4B と切り替え、13 シナリオすべて成功:

| | 温まった1枚 | 実測で解けた点（コミュニティイメージ）との比較 |
|---|---|---|
| SDXL 1024px（20 step） | **8.02 秒** | 実測で解けた点3の 8.0 秒・0071 実測7の「8 秒台」と一致 |
| klein 4B（4 step） | **4.01 秒** | 実測で解けた点3の 3.7〜4.0 秒と一致 |
| Z-Image-Turbo（8 step） | **10.74 秒** | 実測で解けた点3の 10.4〜10.6 秒と一致 |
| SDXL 512px | 3.01 秒 | — |
| SDXL＋LoRA（温まった状態） | 8.02 秒（LoRA 無しと同じ） | 実測で解けた点3の「温まっていれば LoRA は無料」と一致 |

コールドの1枚（切り替えを含む）は SDXL 26 秒・Z-Image 55〜57 秒・klein 39〜40 秒で、
実測で解けた点5の「切り替えは1〜2.5分」より速かった——`LlmUseLocalStorage`/
`ImageUseLocalStorage` の既定が `true`（未解決9・10で解決済み）になっている分、EBS では
なくインスタンスストアから読んでいるためと考えられる（今回はモデルがそもそも1本目の
フェッチで載ったばかりで、キャッシュ済みではない状態からの初回ロード）。13 シナリオ
すべて `ok: true`、エラー無し。自前イメージ・CFN の `ImageEngine=comfy` 切り替え・
S3 レイアウト（`ln -sfn` 相当のマウント）・ワークフローグラフの4点が実機で噛み合うことを
確認した——確認していないのは Go の `comfy` provider 自体（CP ゲートウェイ経由の
`/prompt`→`/history`→`/view`）で、これは単体・結合テスト止まり（次回への持ち越し）。

## P2 を実機で押した（2026-09-10・af-sandbox）

P2 の宿題——**Go の `comfy` provider を、CP ゲートウェイ経由で、メンバーセッションの
`generate_image` から実際に叩く**——を果たした。結論から言うと provider は正しく、
`sdxl-base-1.0`（1024×1024）も `flux2-klein-4b`（3 枚）も生成できた。ただしそこへ到達する
までに**実装の欠落を 4 件**踏んだ。4 件とも CI では緑で、bench では緑で、実機でだけ落ちた。

### 何が動いたか

| 計測 | 実測 | 対照 |
|---|---|---|
| SDXL 1 枚・コールド | 42.33 秒 | `SDXLClipModel`/`SDXL`/`AutoencoderKL` を EBS から読む |
| SDXL 1 枚・warm | **8.42 秒** | ComfyUI 単独検証（実測で解けた点）は 8.02 秒 |
| klein 3 枚・SDXL からの切り替え込み | 35.89 秒 | klein warm は単独検証で 4.01 秒／枚 |

**Go provider のオーバーヘッドは測れないほど小さい。** warm の SDXL が 8.42 秒に対し
ComfyUI を直接叩いたときが 8.02 秒——差 0.4 秒が `/prompt`→`/history`→`/view` の往復と
グラフ組み立ての全部である。provider が余計なことをしていないことの、いちばん素直な証拠。

**切り替え警告の見積もりは、この構成では悲観的すぎる。** `comfySwitchWarning` は実測で
解けた点 5 に従って「1〜2.5 分（EBS 再読み込み）」と言うが、実際は 35.89 秒で、しかも
そこに 3 枚分の生成が入っている。klein warm 4.01 秒／枚で 3 枚 ≒ 12 秒なので、**切り替え
自体は約 24 秒**。7.75 GB の klein での話であり、FLUX.1 dev（22.2 GB）には当てはまらない
可能性が高いので、警告の文面はそのままにしてある。

### 踏んだ 4 件

1. **`base_model` をモデルファミリーの綴りで書く経路が存在しなかった。** comfy は 5 つの
   テンプレートを `base_model` で選び、ID からの推測を設計上拒否する（決定 2 がそのために
   ある）。ところが `sdxl` / `flux2-klein` … を書き込む経路がどこにも無かった——
   `seedEngineCatalog` は `BaseModel` を設定せず（種にはファミリーが分からない）、取り込みは
   HF / Civitai の**表示名**（`"SDXL 1.0"`）をそのまま格納し、Console には入力欄が無い
   （表示のみ）。
   つまり **Console だけを使う限り comfy は 1 枚も生成できない**。今回は管理 API を手で
   叩いてカタログを書いた。CP に語彙と検証を入れ、Console に選択欄を足して直した。
2. **Console のカタログ UI が ADR 以前の世界のままだった。** 登録フォームはファイル 1 本
   固定で、klein や Z-Image のような「拡散モデル＋テキストエンコーダ＋VAE」の 3 本構成を
   **登録することすらできない**。API と wire は最初から対応していたので、欠けていたのは
   UI だけ。複数ファイル行（役割 flag 付き）を足して直した。
3. **`ln -sfn` が焼き込み済みの `models/` の中にリンクを作っていた**（上の訂正 2）。
   全リクエストが 400。
4. **`PRESET_FILE` をルータ判定の代理に使っていた**（上の訂正 1）。start 以外のモデルが
   永久に降りて来ない。`SYNC_ALL`（`ImageIsComfy` のとき `"1"`）に分離して直した。

ついでに、エンジンのモード変更が稼働中ワークスペースにカタログの無効化を押し込んで
いなかったことも判った。`notifyEngineCatalogChanged` の発火は `putModel` と `deleteModel`
だけで、モードのルートには無い。Agent はカタログを 10 分キャッシュするので、`mode=off`
の後もセッションは最大 10 分エンジンを提供し続け、呼ぶと `503 engine_off`（リトライ対象の
`engine_waking` ではなく拒否）が返る。`mode=ondemand` にしたのにツールに現れなかったのが
その裏返しで、これが今回いちばん最初の足止めだった。

### この 4 件から取るべき教訓

**「実機の GPU で 13/13 成功」は、出荷される配線を測った証拠ではなかった。** bench
（`bench-image-engine.sh --baked`）と本番タスク定義の差は 2 箇所しかない——モデルの
渡し方（bind mount か symlink か）と、取り込みの経路（bench は自前で fetch する）。
そして**落ちたのは、まさにその 2 箇所だけ**である。3 と 4 は偶然どちらも「bench が
迂回していた部分」であり、偶然ではない。

ハーネスは「エンジンが動くか」を測るには十分だったが、「この配備でエンジンが動くか」は
測っていなかった。次に同種のハーネスを書くときは、**モデルの渡し方だけはタスク定義と
同じにする**（bind mount で楽をしない）のが最小の防御になる。1 と 2 については、
**API と wire が対応していることは UI が対応していることを意味しない**という、もっと
単純な話——決定 2 は「取り込み時に運用者が宣言する」と書いてあったのに、宣言する場所が
作られないまま P2 が完了扱いになりかけていた。


## P2 の残作業 4・5 を実機で押した（2026-09-10・af-sandbox）

前節が残した 2 つ——**取り込み（ingest）を実際に HF / CivitAI から走らせる**（残作業 4）と、
**provider 経由で一度も動かしていない 3 ファミリー**（残作業 5: Z-Image・FLUX.1・SD3.5）——を
同じ日のうちに押した。結論から言うと **3 つすべてが動くようになった**が、SD3.5 だけは
テンプレートが誤っており、直すまで 1 枚も出せなかった。加えて欠落を 6 件（5〜10）踏んだ——
うち 1 件（8）は未解決 3 がそのまま出たもので新発見ではなく、最後の 1 件（10）は欠落 1 の
修正が使われるのを見て後から分かったものである。

### 残作業 4——取り込みは通る。ただし「通らない」の伝え方に穴が 2 つある

**拒否は運用者にとって行動可能だった。** ファミリー未宣言のチェックポイントを CivitAI から
取り込もうとすると、タスクを起こす前に 400 で断り、メッセージは 3 つを同時に言う:

```
declare base_model as one of sdxl, sd35, flux1, flux2-klein, zimage: this engine runs comfy,
which picks a workflow by family and will not guess one (the repository calls it "SDXL 1.0")
```

ファミリーの綴り一覧・なぜ要るのか・**上流が何と呼んでいるか**。最後の 1 つが効く——運用者の手元に
あるのは「SDXL 1.0」という表示名だけで、それを `sdxl` に対応づけるのが唯一の仕事だからである。
`base_model` に表示名をそのまま入れても同じ 400 になる。どちらの場合もジョブ行は 1 本も
作られない（ジョブ一覧で確認した）。

**完走も確認した。** Hugging Face から 4 件（clip_l 246 MB / t5xxl_fp8 4.89 GB /
flux1-dev-fp8 11.9 GB / sd3.5_medium 5.11 GB）、CivitAI から 1 件（DetailedEyes_XL の
LoRA 93 MB）。そして **CivitAI 由来の行の `base_model` は空**だった——上流は
`"SDXL 1.0"` を返しているのに格納していない。P2 の変更が実経路で効いていることの、
いちばん直接の証拠である。

🔴 **欠落 5——CivitAI の「ログインが要る資産」が `resolve` から見えない。** 最初に選んだ
LoRA は `resolve` が `gated: false` / `can_ingest: true` と答え、ジョブが走り、Fargate タスクが
`curl: (22) The requested URL returned error: 401` で落ちた。手で叩くと CivitAI は
`{"error":"Unauthorized","message":"The creator of this asset requires you to be logged in to
download it"}` と言う——**投稿者ごとの設定**である。5 資産を試したら 200 / 401 / 403 に割れた。
Hugging Face の gated には事前に断る経路がある（`ingest_gated_no_token`、決定 6・P5）のに、
CivitAI には概念もトークン欄も無い。`resolve` が「この資産は匿名で取れるか」を
（`HEAD` 1 本で）確かめないかぎり、運用者が受け取るのは 9 分後の裸の curl 終了コードになる。

🔴 **欠落 6——取り込みはファイルの Flag を書けない。** `engineIngester` が作る行は
`Files: [{S3Key, Bytes}]` の 1 要素で、Flag は常に空＝「まるごとのチェックポイント」である。
つまり **分割モデルは取り込みだけでは組み立てられない**。FLUX.1 の 4 ファイル行を作るのに、
部品を 3 つ捨て行として取り込み（S3 に置くためだけ）、`POST /models` で Flag 付きの本番行を
作り直し、捨て行を忘れる、という手順を踏んだ。決定 2 は「取り込み時に運用者が宣言する」と
言うが、宣言できるのはモデルファミリーだけで、**ファイルの役割は宣言できない**。

🔴 **欠落 6 の帰結——`?purge=1` は他の行が使っているファイルを黙って消せる。** 行を忘れる
ときの `purge` は、その行の `files[]` の S3 キーをそのまま ingest タスクに渡す
（`deleteModel`）。**他の行が同じキーを参照していないかは見ていない。** 欠落 6 のせいで
「部品を捨て行として取り込み、本番行から同じキーを参照する」が分割モデルの通常手順に
なった今、これは踏みやすい: 今回の検証でも `clip_l.safetensors` は捨て行
`tmp-flux-clip-l` と本番行 `flux1-dev-fp8` の両方が指しており、前者を purge 付きで忘れると
後者が黙って壊れる。今回は purge 無しで忘れた。

### 残作業 5——Z-Image と FLUX.1 は通った。SD3.5 はテンプレートが誤っていた

| モデルファミリー | 結果 | 実測（ComfyUI 側の `Prompt executed`） |
|---|---|---|
| Z-Image-Turbo | ✅ 生成 | 1 回目は下の欠落 7 で 400、2 回目に成功 |
| FLUX.1 dev（fp8 分割） | ✅ 生成（初回） | **78.19 秒**（切り替え込みのコールド） |
| SD3.5 medium | ❌ → テンプレート修正 → ✅ 生成 | **46.90 秒**（切り替え込みのコールド） |

**これで 5 ファミリーすべてが、この配備の GPU で、Go の provider を通って絵を返した。**

FLUX.1 は S3 にあった 22.2 GiB の fp16 transformer（P4 の gated 検証で入れたもの）を
**使わなかった**。flux1 テンプレートは `UNETLoader` ＋ `DualCLIPLoader` ＋ `VAELoader` の
分割構成を要求するのに、その行は `checkpoints/` に置かれた単一ファイルで、text encoder が
1 つも無いからである。ファイルの basename が ComfyUI のローダに渡る名前で、S3 キーの
**ディレクトリ**がどのローダの一覧に載るかを決める（`engineImageFiles`）——つまり
`image/checkpoints/` にある物は `UNETLoader` からは永久に見えない。fp8 の分割一式
（unet 11.9 GB ＋ clip_l ＋ t5xxl_fp8、VAE は Z-Image が使っている `ae.safetensors` を共有）を
新たに取り込んだ。L4 24 GB に fp16 の 23.8 GB を載せる賭けを避ける意味もある。

🔴 **SD3.5 のテンプレートは、走らせるまで誤っていることが分からなかった。**

```
Value not in list: clip_name1: 'sd3.5_medium.safetensors'
  not in ['clip_l.safetensors', 'qwen_3_4b_fp8_mixed.safetensors', 't5xxl_fp8_e4m3fn.safetensors']
```

`comfyGraphSD35` は「Stability の公式リリースは UNet+VAE+CLIP-L+CLIP-G を 1 ファイルに
束ねる」という前提で、`TripleCLIPLoader` の `clip_name1`/`clip_name2` に**チェックポイント
自身のファイル名**を渡し、「ローダが必要なテンソルだけ読むだろう」と書いていた。読まない。
**読めない**——`TripleCLIPLoader` の 3 入力は `models/text_encoders` の列挙であって、
`models/checkpoints` にある名前は候補ですらない。前提そのものが誤りだった。

修正は語彙に `--clip_g` を足すこと。これは第 5 の語彙の発明ではなく**取りこぼしの回収**である:
`EngineFile` が借りている stable-diffusion.cpp の語彙には元から `--clip_g` があり、写す際に
落ちていた。SD3.5 の 3 つのエンコーダは 3 つの別ファイルで、clip_g を名指す手段が無い以上、
このモデルファミリーは最初から生成できなかった。CP 側の一覧（Console に配る正本）にも同じ 1 語を足し、
ドリフト検査が両者を揃えることを——片側だけ直して実際に落として——確かめた。

**ゴールデンテストはこれを捕まえられない。** 固定していたのは「誰も走らせたことの無い
グラフの形」で、ADR 本文が P2 の時点でそう断っていたとおりである。形の固定は差分を
読ませるためのもので、正しさの証明ではない——今回それが具体例になった。

### 配備そのものの欠落 3 件（モデルファミリーとは無関係に、誰でも踏む）

🔴 **欠落 7——エンジンは「まだ箱に無いモデル」への要求を受け付けてしまう。** fetch
サイドカーは start モデル（selected な 1 本）を落とした時点で `engine may start; 6 file(s)
still to sync` と宣言し、残りを裏で落とし続ける。Z-Image の 1 回目はその最中に届き、
ComfyUI が「ファイルが無い」と 400 を返した:

```
Value not in list: unet_name: 'z_image_turbo_bf16.safetensors' not in ['flux-2-klein-4b.safetensors']
```

エンジンは health を通しており、ゲートウェイの `engine_waking`（リトライ対象）でもない。
**運用者にも呼び出し側にも「まだ降りて来ていない」と分かる手掛かりが無い。** 2 箱目では
12 ファイル・48 GB を約 270 秒（≒180 MB/s）かけて同期しており、その 270 秒はまるごと
この窓である。要求されたモデルの files が箱に揃っているかは CP が知っている事実なので、
`engine_waking` 相当で待たせるのが素直な直し方に見える。

**欠落 8——稼働中の箱にモデルを足しても同期されない。これは新発見ではない**——未解決 3
（「サービス起動後の追加同期」）がそのまま出たもので、P5 に送られている既知の穴である。
ただし image 役で実際に踏むと何が起きるかは初めて測った: `flux1-dev-fp8` と `sd35-medium` を
有効化してもファイルは降りて来ず、`mode` を `off` → `on` して**箱を作り直す**しかなかった。
llm 役では「次の起動まで待つ」で済むが、image 役では**有効化したモデルが箱に無いまま
`generate_image` の `model` enum に出る**ので、欠落 7 と重なって「選べるのに 400 が返る」に
なる。P5 でこれを解くときは、enum に出す条件を「有効」ではなく「箱にある」に寄せるか、
欠落 7 側で待たせるかのどちらかが要る。

🔴 **欠落 9——`/history` の 503 はリトライされない。** provider は `/prompt` の 503
（`engine_waking`）を待って再送するが、その後のポーリングは待たない。箱がポーリング中に
入れ替わると、呼び出し側に届くのは
`the image engine's /history answered 503 Service Unavailable: the fleet's own inference
engine is starting; retry` という、**自分で retry と言っておきながら retry しない**メッセージに
なる（実測）。生成はコールドで 47〜78 秒かかるので、その窓は現実に開く。

**運用上の注意（今回自分で踏んだ）**: `mode=on` で箱を起こしてから `ondemand` に戻すと、
`last_demand` が古いままなので**コントローラが即座にその箱を止める**。48 GB 同期しなおしに
なるので、箱を温めたいなら `ondemand` のまま要求で起こすのが正しい。

### 欠落 10——モデルファミリーを宣言すると印は消えるが、行が使えるようになったわけではない

後から分かった。この PR で足した「行内のファミリー修正欄」を運用者が実際に使うのを見て気づいた。

`flux1-dev`——2026-09-09 に「gated な Hugging Face からのダウンロードが通るか」だけを確かめる
ために取り込んだ 22.2 GiB のチェックポイント（P4）で、有効化されたことも生成に使われたことも
無い——は `base_model_missing` が立っており、だから行に選択欄が出ていた。何か選ぶと印は消える。

**それでもこの行は生成できず、しかもそれを言うものが無くなる。** 検査は
`engineBaseModelValid`（**5 つの綴りのどれか**）だけで、**宣言したファミリーのテンプレートが
要求するファイルが行に揃っているか**は誰も見ていない。`flux1-dev` は `image/checkpoints/` に
ある Flag 無しの 1 ファイルで、`flux1` テンプレートは `--diffusion-model` ＋ `--clip_l` ＋
`--t5xxl` ＋ `--vae` を要求する——つまり**選択欄にどの正解を入れてもこの行は動かない**。
（実際に入っていたのは `flux2-klein` で、これは別世代の別モデルだが、そこは本質ではない。
`flux1` でも救われない。）

失敗の仕方は安全ではある: `comfyBuildGraph` が `errComfyMissingFile` で断るのは HTTP を叩く
**前**なので、誤った絵が出ることはなく、読める拒否が返る。しかしそれは生成時の拒否であり、
パネルはもう印を出していない——**ファミリーを宣言する前より状態が悪い**。ファミリーも各
ファイルの Flag も CP が持っている事実なので、検査は宣言する場所か有効化する場所に置ける。

`flux1-dev` はこのあと `?purge=1` 付きで削除し、22.2 GiB を回収した。P4 の役目は 2026-09-09
に果たしており、生成できる FLUX.1 の行は別に取り込んだ `flux1-dev-fp8` である。

### ついでに測れたこと

- `warm_model` が `z-image-turbo` を返した。P2 の `X-AF-Model` 修正は実配備で効いている。
- ADR 0074 の VRAM 門は正しく作動した（`flux1-dev-fp8 wants at least 16571 MiB … the l4 class
  declares 8000 MiB`）。ただし **`l4` の宣言値 8000 MiB は実機と合っていない**——エンジンは
  `Total VRAM 22563 MB` と言う。`l40s` は 48 GB のカードに 44000 を宣言しているので、
  L4 の段だけ桁が違う。結果として 8 GB を超える画像モデルすべてで `confirm_vram` が要る。
- **測っていないこと**: モード変更時のカタログ押し込みが稼働セッションへ 10 分待たずに届くか。
  モードは 4 回変えたが、セッション側のツール表示は観測していない。
- **測っていないこと**: Console の取り込みフォームの実描画。CP が `base_models` と
  `file_flags` を wire に載せていることと、`resolve` がヒントに差し込む表示名を返すことは
  実測したが、ヘッドレスのクリック導線が管理モーダルに辿り着けず、画面そのものは見ていない。

## 却下した案

- **vLLM を llm 役のエンジンにする（今は）。** 1 プロセス 1 モデルでルーターが無く、切替＝
  再起動——**この ADR の目的に対して llama.cpp より後退する**。イメージ 9.7 GB は pull を
  4 倍にし、L4 に 30B 級を入れる 4bit の MoE 量子化は GGUF ほど枯れておらず、強み（連続
  バッチング・マルチ LoRA の動的ロード）は同じモデルへの同時要求で効くもので、普段寝ている
  エンジンでは出番が無い。**再検討の条件は 2 つ**: 同じモデルに同時 5 セッション以上が常態に
  なったとき、GGUF が無いモデルを載せたいとき。カタログの `kind` は `safetensors` の LLM を
  持てるので、その日に vLLM を 2 つ目の `llm` エンジンとして同じ S3 配置に足せる。

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

## 未解決の点——P2 の前に 8 を測る（**9・10・11・12 は 2026-09-09 に決着**。2 と 6 は sd-server の配備が要るときだけ）

1. ~~**ルーターモードの 4 点**（決定 3 が依存）~~ **解けた（レビュー R1・CPU）**: 空で
   `/health` ok、autoload は待つ、`--models-max 1` は idle LRU を降ろして載せる、`usage` と
   `model` はルーター経由で届き、窓はモデル毎。~~残るのは GPU で 1 回——「idle LRU」が生成中の
   モデルをどう扱うか~~ **これも解けた（P1 の実測 7・CPU）——待つ**。割り込まれた生成は
   最後まで届き、待たされた要求が 48.6 秒後にロードへ進んだ。
2. **sd-server の LoRA**（決定 5 が依存）: `immediately` で要求ごとに組が変わるコスト、
   `at_runtime` の VRAM、`<sd_cpp_extra_args>` が `/v1/images/edits`（multipart）でも効くか、
   `baseModel` 不一致のときの挙動（黙って崩れるのか、警告か）。CPU の SD1.5 Q4 で
   `<sd_cpp_extra_args>` の経路と不一致の挙動は測れる。
3. **サービス起動後の追加同期。** 有効化したモデルを、走っている箱に**次の起動を待たず**
   足せるか——サイドカーを常駐させて active set を再同期し、ルーターが `--models-dir` を
   再走査するか（しないなら `/models` の再読込の口があるか）。無ければ「llm も次の起動」で
   始め、これは P4。
4. ~~**Civitai の API**（決定 6）~~ **解けた（P4 の実測 2・2026-09-09）**: 開発者サイトは
   相変わらず 404 だが、`GET https://civitai.com/api/v1/model-versions/<id>` が**匿名で**
   `files[].hashes.SHA256`（大文字 hex）・`files[].sizeKB`（**キロバイトの小数**）・
   `files[].downloadUrl`・`baseModel`（`"SD 1.5"` のような表示名）・`model.type`
   （`Checkpoint` / `LORA`）を返す。ダウンロードは署名付きの R2 URL へ 302 で、
   試した公開モデルは**鍵無しで 200** だった（鍵が要るモデルもある）。
5. ~~**`engine_models` を DB に置くか設定ストアに置くか。**~~ **表にする（レビューの答え 6）**。
   書き手が 2 人いる（管理者のトグルと取り込みジョブの状態遷移）ので JSON 1 本の全読み全書きは
   CAS が無く片方が消える；`files[]` / `args[]` / `sizes[]` は行ごとに形が違い、一覧・種・削除は
   どれも行の操作；2 方言の migration は `engine_hourly` の前例がある。`lastUsedAt` は **P0 では
   持たない**（要求ごとの UPDATE を避ける。`engine_<key>_demand_at` の 1 分抑制と同じ形で P4 に）。
6. **sd-server の非同期 API の故障原因**（決定 4 の仮説）: `--lora-model-dir` を明示した
   起動で `/sdcpp/v1/img_gen` が通るか。通れば sd-server の配備でも 60 秒規則を外せるが、
   本命（ComfyUI）の順序は変えない。
7. ~~**既定モデルの実測**（決定 10）~~ **解けた**（実測で解けた点 3〜5）。残るのは gated の
   2 つ——SD3.5 Medium と FLUX.1-dev——で、`HF_TOKEN` のある配備で測る。
   **2026-09-10 に取り込みの側を解いた**（「P5 を実機で押した」節）: 登録したトークンで
   `stabilityai/stable-diffusion-3.5-medium` と `black-forest-labs/FLUX.1-dev` の両方から
   401 なしで取り込めた。ただし取ったのは各リポジトリの**最小ファイル 1 つ**（どちらも
   167,666,902 B の VAE）で、gated の判定がリポジトリ単位だから足りる、という確認である——
   **本体を入れて描かせたわけではない**（23.8 GB の `flux1-dev.safetensors` は L4 に載らない）。
   この 2 族の生成そのものは、ungated の鏡から入れたファイルで「P2 の残作業 4・5 を実機で
   押した」節が済ませている。
8. **ComfyUI の自前イメージ**（フェーズ P2）——半分解けた（実測で解けた点 7・8）: コミュニティ
   イメージは 5.36 GB 圧縮で pull 205 秒、焼き込みが v0.8.2 なので起動時の checkout＋pip
   （20〜26 秒・NAT 依存）が要った。自前で焼くなら v0.34.0 を固定し、Manager を入れない。
   GGUF を読むノード（`ComfyUI-GGUF`）は今回要らなかった（fp8 の safetensors で足りた）。
   同梱するなら**リビジョンを固定して Dockerfile に書く**のが条件（0071 決定 6）。
9. ~~**切り替えの読み直し（実測で解けた点 5）を縮められるか——P2 の前に 1 GPU 時間で 2 案を測る**~~
   **(1) は解けた（レビュー 2026-09-09 の 1）——`useLocalStorage` で交替は 276〜282 秒 →
   98.5 秒、既定を `true` にした。(2)（RAM で解く・`ImageMemMinMiB` を上げて g6.2xlarge）は
   未測のまま残るが、(1) が効いたので P2 の完了の定義を変えるほどの前提ではなくなった。**
   以下は当時の記述:
   （レビューの答え 7・R9）。(1) `useLocalStorage`（0071 未解決 1）: capacity provider の
   `LocalStorageConfiguration` は create-only ではなく、`ImageUseLocalStorage=true` の更新 1 回で
   入る（R9）。効くのは切り替え（12.3 GB を 92 MB/s → NVMe）だけでなく、**コールドスタートの
   S3 → EBS 33 GB・332〜360 秒**も——これは EBS の書き込み上限（g6.xlarge のベースライン
   125 MB/s）に張り付いた数字である。(2) **RAM で解く**: 3 モデル 27 GB がページキャッシュに
   残らないのは箱の RAM が 15 GB だからで、`ImageMemMinMiB` を 30,000 に上げれば g6.2xlarge
   （32 GiB）が選ばれ、コードは 1 行も変わらない。切り替えが RAM → VRAM（数秒）になれば
   `useLocalStorage` は起動時間だけの話に戻る。どちらも `bench-image-engine.sh` がそのまま測れ、
   P2 の完了の定義（切り替えの価格）を変えるので **P2 の前**に測る。
10. ~~**モデルの置き場そのものを問い直す**~~ **解けた（レビュー 2026-09-09 の 1〜3）**:
    **(a) 採用**（`useLocalStorage`——コールドスタート 527〜586 秒 → 275 秒、交替
    276〜282 秒 → 98.5 秒、既定を `true` に）、**(b) 否定**（温かい箱は MI では作れない。
    Bottlerocket の読み取り専用ルートに阻まれ、名前付き `SourcePath` は 3.1 GB にしか落ちない）、
    **(c) 却下維持**（ただし根拠を「スループットの仮定」から検証済みの単価に差し替え——
    Elastic の $0.04/GB が 1 回 $0.74 で、浮く GPU 時間の 17〜21 倍）。以下は当時の記述で、
    **「S3 は律速ではない」は半分だけ正しかった**（外すと約 160 MB/s の次の壁が現れる）:
    出発点は「同期が毎回のコールドスタートを支配している」という実測で、
    **S3 は律速ではない**——0071 の未解決 1 が既に「g6.xlarge の EBS ベースライン 125 MB/s は
    S3 取得（104〜147 MB/s＝**EBS 書きの上限に見える**）と VRAM ロードの両方を縛っており、
    インスタンスストアに替えれば両方が動く」と書いている。安い順に:
    - **(a) `useLocalStorage`**（未解決 9 の (1) と同じ）。**パラメータ 1 つの更新**で入り、
      EBS の書き込み上限が消えるので同期と切り替えの両方に効く。**最初に測る。**
    - **(b) 温かい箱。** 0071 決定 7(c) は**未証明のまま**で、失敗が記録されている
      （`PARAMETERS-60-engines.md`「The model volume」）——匿名ホストボリュームはタスク毎に
      新しいディレクトリを作るので、**MI が保持した同じインスタンスへの再起動が 18.5 GB を
      取り直した**（126 秒）。名前付き `SourcePath` はルートに落ちて `No space left` で死ぬ。
      塞がっているのは「MI のデータボリュームが実際にどこにマウントされているか」だけで、
      **GPU 1 時間の調査**である。
    - **(c) EFS を測り直す（0071 の却下を解く候補）。** 却下の条件文が自分で再考を予約して
      いる——「sync が P0 で痛いと分かったら再考する」。**P0 で痛いと分かった。** 加えて
      🔴 費用の根拠 $0.36/GB-月 には「第三者の転記。要再確認」と注記があり**検証されて
      おらず**、スループットの根拠「NFS のそれで桁で遅い」は**実測ではなく仮定**である。
      律速が EBS の**書き込み**なら、EFS は書き込み自体を無くすので構造的に有利でありうる。
      概算（$0.36 が正しい前提）: バケットは実測 99.5 GB で S3 $2.49/月 に対し EFS 標準
      $35.8/月、差額 $33/月＝**26 GPU 時間**。同期 350 秒を毎回のコールドスタートから消せる
      なら 1 回 $0.12 で、**1 日 9 回以上起動するなら GPU 時間だけで元が取れる**（人が待つ
      時間は別勘定）。IA / Archive のライフサイクルで保管費はさらに下がる。
11. ~~**テナント軸をどこに入れるか**~~ **決まった（レビュー 2026-09-09 の 4）——中間案を採る。**
    **実装済み（2026-09-10・末尾「追記 — テナント軸を実装した」節。実機未検証）。**
    実測で**壁の順番が下の記述と違う**ことが分かった: 効くのは**時間（約 5 モデル）→
    ディスク（約 13）→ SSM（約 38）**の順で、4,096 文字は最初ではなく最後である。以下は
    当時の記述:
    テナント軸が無く、「テナントが HF から選んで配置する」は使い勝手として正しい方向だが、
    **共有の箱の上では 4 つが掛け算になる**: active set はエンジンに 1 つなので全テナントの
    有効モデルの**和集合**になり（ここで初めて 4,096 文字が効く。いま 6%）、コールドスタートは
    「有効な全モデルを同期」なのでテナント数に比例し、`LlmModelsMax=1` なので交替が増えて
    1 回 1〜2.5 分払い、同じ active set を共有するのでテナント間でモデル id が見える。
    箱をテナント毎にすれば全部消えるが $1.26/時 × テナント数で、0071 が「1 箱を共有して
    寝かせる」で組み立てた前提を捨てることになる。**中間案**: カタログは配備で 1 つのまま、
    **「取り込める人」と「誰が同意したか」をテナント軸にする**。掛け算は起きず、使い勝手の
    大半は取れる。`source` と `license_accepted_by` が既にその形の半分である。
    - **テナント毎の S3 バケットは薦めない。** バケットは意味のある境界ではない——どの
      バケットから来てもモデルは**同じ箱の同じディスクに載り、同じプロセスが読む**。境界は
      GPU の箱であってバケットではない。加えて共有モデルがテナント数だけ複製され、いま
      バケット 1 つの ARN に絞ってあるエンジンのタスクロールの許可が緩くなる。分離が要るなら
      **同一バケットのプレフィックス**（IAM はプレフィックスで絞れ、複製も起きない）。
12. ~~**HF トークンを Console から登録できるようにする**~~ **決まった（レビュー 2026-09-09 の
    5）——「DB を正本に、Secrets Manager を運搬路に」を採り、「読み戻さない」を IAM
    （`PutSecretValue` だけ・`GetSecretValue` は与えない）で縛る。実装済み（2026-09-09・
    「P5 の実装」節。形 (b)＝常に秘密を作る。**実機検証は 2026-09-10**——「P5 を実機で押した」
    節）。** 着手条件だった
    **`60-engines.yaml` の余白**は、パラメータ 1 つを消し、スタックの `Description` の散文を
    `PARAMETERS-60-engines.md` へ移して空けた（50,842 → 50,869 バイト／壁 51,200）。
    以下は当時の記述:
    `HfTokenSecretArn` の CFN パラメータで、入れるのに **CloudFormation を回して CP を
    再起動する**——実機で確かめたとおり、それ自体が実測 6 の IAM の穴を通る道でもあった。
    「秘密だから Secrets Manager」は理由にならない（この製品は git の OAuth や MCP の
    ヘッダを自前の暗号化ストアに持っている）。本当の制約は**運搬**で、ECS がコンテナに値を
    渡す道は `secrets[].valueFrom`（**Secrets Manager か SSM の ARN しか受け付けない**）か
    `environment`（平文）の 2 本しかない。🔴 実測: `DescribeTasks` は RunTask の環境変数を
    **そのまま平文で返す**（`ecs:DescribeTasks` を持つ誰でも読める）ので、長命な PAT を
    env で渡すのは割に合わない。**形は「DB を正本にし、Secrets Manager を運搬路に使う」**
    ——Console で登録し、取り込みを起こすときに CP がその値を秘密へ書いてから ARN で
    参照する。代償は決定 6 の「CP はトークンを持たない」が「CP は書けるが読み戻さない」に
    変わることで、**ADR の改訂を伴う判断**である。なお**テナント毎のトークンにはしない**
    ——取り込みの結果（バケットも行も）は配備全体のもので、テナント A の個人的な同意で
    staged したモデルをテナント B が使う形になり、決定 10 が警戒している当のものになる。

## フェーズ

- **P0 — カタログの土台。実装済み・実機検証済み（「P0 の実測」）。** 最初の手順は `60-engines.yaml` のコメント 3 KB を
  `PARAMETERS-60-engines.md` へ移すこと（R4。移してから足す）。そのうえで `engine_models`
  （表。未解決 5）と種、マニフェスト、S3 レイアウトの移行（種と同じ手順で）、active set の SSM
  （4,096 文字の規則とテスト）と fetch サイドカーの一般化（役に依らない 1 本——同期・
  `/models/cmdline`・`ParameterNotFound` は空）、両役のラッパー entrypoint、コントローラの
  「カタログが空なら起こさない」、`LlmEnabled` / `ImageEnabled` への縮退と旧パラメータの互換、
  `teardown.sh` の SSM 掃除、admin API（一覧・有効化・選択）とパネルの `models[]`、
  `/internal/engine/catalog` のカタログ化と `catalog-changed` の押し通知、`sdcpp` の `Caps.Sizes` を
  宣言から、SDXL 系の 2 つ目のチェックポイントの取り込み。**完了の定義: image のチェック
  ポイントを Console で SDXL からもう 1 本へ選び直し、CloudFormation を触らずに次の起動で
  新しい絵が返る（`describe-stacks` の最終更新時刻が動いていないことがその証拠）；`mode=on` の
  エンジンがカタログ空で起きない；スタック作成時（CP 不在・active set 無し）に両役のサービスが
  安定する。**
- **P1 — llm のルーターモード。実装済み・実機検証済み（「P1 の実測」）。** preset 生成
  （`--models-dir` は付けない）、モデル毎の窓、`LlmModelsMax`、`warm` の再定義（`warmPath` と
  `/models` の `loaded`）、opencode の provider をモデル毎の `limit` で、有効な全モデルの同期と
  「同期 +N 秒（推定）」、`warm_model` と交替回数。**完了の定義: 起動メニューに `llamacpp/` の
  モデルが 2 つ出て、片方ずつ使え、交替のリロードが 1 回の試行で答えを返す**（0071 P0 の
  完了の定義と同じ観測を交替で行う）。**3 つとも実機で通った**（同じ箱で 2 つの GGUF が
  0.4〜0.7 秒／10 秒／276〜282 秒で答え、Console で 2 行目を登録・有効化するとピッカーに
  `llamacpp/` が 2 つ並び、2 つ目でセッションが起動した）。行を作る口だけは AWS の資格情報では
  駆動できず、そこは人が押した。
- **P2 — ComfyUI（0071 P2 をここへ前倒し）。実装済み・実機検証済み（2026-09-10・
  「P2 の実装」節と「P2 を実機で押した」節。後者で欠落 4 件を修正）。** 自前イメージ（タグ固定・Manager 無し。`20-platform` の ECR リポジトリと
  専用 CI）、`ImageEngine=comfy` の `!If`（決定 4）、
  provider `comfy`（**generate のみ**。edit/inpaint はモデルファミリーごとの image-to-image グラフが
  未検証のため今回は対象外——完了の定義自体は generate だけで満たせる。`/prompt` →
  `/history` → `/view` を進捗通知つきで回す）、テンプレートは SDXL / SD3.5 / FLUX.1 /
  FLUX.2 klein / Z-Image の 5 ファミリーをリポジトリに置いてゴールデンテストで固定（SDXL・
  Z-Image・klein は実測で解けた点の GPU 検証済みグラフの移植、FLUX.1・SD3.5 は新規で
  実機未検証）、`generate_image` の `model` 引数（enum＝有効なチェックポイント。ここで
  初めて複数になる。**いま温まっているモデルを説明に出す**——切り替えは 1〜2.5 分の
  読み直しなので、エージェントが「既定でよければ温かいほう」を選べるようにする。実測で
  解けた点 5）。ペイン（`/engine/comfy/` の WebSocket）は**含めない**——
  `generate_image` に要るのは API だけで、画面は P5。**完了の定義: 同じ箱で
  SDXL と klein 4B（または Z-Image-Turbo）を要求毎に切り替えて絵が返り、その間にサービスの
  再起動が無く、1024px の SDXL が 0071 実測 7 の 8 秒台で出る。実機で通った**
  （SDXL 8.02 秒・klein 4.01 秒・Z-Image 10.74 秒、切り替え含め 13 シナリオすべて成功。
  「P2 の実装」節7）。Go の `comfy` provider 自体（CP ゲートウェイ経由の実呼び出し）も
  2026-09-10 に実機で通した（「P2 を実機で押した」節）。**残っていた取り込み経路と 3 ファミリー
  （Z-Image・FLUX.1・SD3.5）も同日に押し切り、P2 は実機で閉じた**——「P2 の残作業 4・5 を
  実機で押した」節。SD3.5 だけはテンプレートが誤っていたので直した（`--clip_g`）。
- **P3 — LoRA。** ComfyUI の上で: image の `loras/` 同期、`generate_image` の `loras`、
  テンプレートの `LoraLoader` 連鎖、baseModel 不一致の拒否；llm の preset 固定 LoRA。
  sd-server の `<sd_cpp_extra_args>` 経路は `ImageEngine=sdcpp` の配備が要るときだけ、
  未解決 2 を測ってから。**完了の定義: 同じ prompt・同じ seed で LoRA の有無が絵を変え、
  SD1.5 の LoRA が SDXL で enum に出ない。**
- **P4 — Console からの取り込み。実装済み・実機検証済み（「P4 の実測」「P4 を実機で押した」）。** `ingest` API（解決と開始を
  分けた 2 本＋ジョブ一覧）、RunTask の IAM（`60-engines` 内。PassRole は ingest ロールだけ、
  ＋失敗理由を読む `logs:GetLogEvents`）、HF の sha256 / `license` / `license_name` / サイズ /
  gated 解決、Civitai（未解決 4 が解けたので同時に入れた）、gated の一文とライセンス受諾 UI
  （誰がいつ・商用の軸。決定 10）、進行と失敗の表示、取り込みタスクの `MODE=delete`（決定 7）。
  ジョブは表（`engine_ingest_jobs`）で、作る予定の行ごと持つ——ダウンロードの最中に CP が
  入れ替わっても、バケットに落ちたバイト列に対応する行が作られる。
- **P5 — 走行中の追加同期（未解決 3）、llm の仮想モデル id（決定 5 の後半）、ComfyUI の
  ペイン、sd-server の非同期 API（未解決 6）、テナント軸（未解決 11）。**
  **テナント軸（未解決 11）は実装済み**（2026-09-10・末尾「追記 — テナント軸を実装した」節。
  実機未検証）——取り込みの権限ゲートと、受諾の 4 つ組。**完了の定義: 許可を与えたテナントの
  tenant_admin が取り込みを起こせ、許可の無いテナントの tenant_admin とそのテナントの
  平メンバーは 403 で断られ、できた行に「どのテナントの・誰が・いつ・どのライセンスに」が残る。**
  **HF トークンの Console 登録（未解決 12）だけ先に実装済み**——ここだけ他に依存が無く、
  gated（SD3.5 全部・FLUX.1 全部）が取り込めるかどうかを直接決めるため。**完了の定義:
  Console でトークンを登録し、CloudFormation を触らずに gated のリポジトリが取り込め、
  取り込みタスクのログに 401 が出ない。**（**2026-09-10 に実機で満たした**——「P5 の実装」節と
  「P5 を実機で押した」節。P5 の他の項目は未着手のまま。）
- **P6 — seed と残り 4 パラメータの撤去。実装済み・実機未検証（補遺「P6 の実装」）。**
  （2026-09-10 に追加。理由・罠・移行の窓は本 ADR 末尾の
  追記節）。新しい案ではなく**決定 1 の仕上げ**である——`*ModelFile` は 0.18.0 で消し、残るのは
  `<役>ModelS3Key` / `ModelIds` / `ContextTokens` / `MaxOutputTokens`、`seedEngineCatalog`、
  エンジン表の `models` / `contextTokens`。
  **完了の定義: `LlmEnabled=true` だけでモデル系パラメータを 1 つも書かずに配備し、その役の
  サービスが安定し、空のカタログへ Console からモデルを登録して、エンジンがそれで起動する。**
  🔴 加えてアップグレードノートが、`<役>ModelS3Key` だけを書いていた配備に対して**先に
  `<役>Enabled=true` を足せ**と言っていること——サービスを作る条件が今はその鍵を読んでいる。

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
- 同日の改訂で: HF の `api/models/<repo>`（`cardData.license` / `license_name` / `gated`、
  `?blobs=true` の `siblings[].size`）を SD3 / SD3.5 Medium・Large / FLUX.1-dev・schnell・
  Kontext-dev / FLUX.2-dev・klein 4B・9B / Qwen-Image / Z-Image-Turbo の 11 件、Docker Hub の
  `vllm/vllm-openai:latest`（`full_size` 9.7 GB）、stable-diffusion.cpp `README.md` の対応
  モデルの節（日付つきの更新履歴）

## レビュー（2026-09-08・P0 着手前）

0071 のレビューと同じ流儀で、「決定 → 根拠 → 実測」の筋と、実測から引けない結論を引いて
いないかを疑った。起草者とは別のセッションで、本文の「既に持っている権限」「数 KB」「CP が
拒否する」の類は**コードと AWS の API で読み直した**。結論から書く: **承認（P0 着手可）を
提案する。ただし決定 2・5・6・7・10 の 5 箇所は、レビューで前提が覆ったか、CP に無い権限を
前提にしているので、P0 のコードを書く前に本文を下の提案どおり改めること。** 起草者が「測って
から決める」に回した未解決 1 の 4 点は、**このコンテナの CPU で全部解けた**（R1）——利用者の
判断で P1 は後回しだが、P1 を止める理由はもう無い。本文で最も重い見落としは、**CP のタスク
ロールに S3 の権限が 1 つも無い**ことで、決定 6（マニフェストを読む）と決定 7（S3 のファイルを
消す）がそれに気づかずに書かれている（R3）。

### レビューで測ったこと・確かめたこと

- **R1. ルーターモードの 4 点（未解決 1）を CPU で測った。** llama.cpp の公式ビルド
  `b10853`（`llama-b10853-bin-ubuntu-x64`、`version: 0.4.0-dev`）に、`ggml-org/models` の
  `tinyllamas/stories260K.gguf`（**1.1 MB**）を `alpha.gguf` / `beta.gguf` の 2 名で置き、
  `--models-dir` + `--models-preset`（`[*] c = 512`、`[alpha] c = 256, load-on-startup = true`）+
  `--models-max 1` で起動した。数字はすべてこの構成での実測である。
  1. **(a) 空の `--models-dir` で起動し、`/health` は `{"status":"ok"}`**。`/models` と
     `/v1/models` は `{"data":[]}`。無い id への要求は **`400 "model 'nope' not found"`**
     （404 ではない）、`model` の無い要求は `400 "model name is missing from the request"`。
     決定 1 の「llm 役は空で listen」は成立する。🔴 同時に、**`/health` は warm の根拠に
     ならない**（0 モデルで ok を返す）——決定 3 の「`GET /models` の `loaded` を warm とする」は
     正しく、逆に `/health` を warm と読む今日のコードはルーターでは嘘になる。
  2. **(b) autoload 中の要求は待たされる。** 未ロードの `beta` へ要求すると、ログに
     `ensure_model: waiting until model name=beta is fully loaded...` と出て、ロード後に
     **200 で本文が返った**（蹴られない）。決定 3 の「心拍で握る」だけで足り、`/models/load` を
     先に叩く分岐は要らない。
  3. **(c) `--models-max 1` で 2 つ目を要求すると、1 つ目を降ろして載せる。** ログ:
     `models_max reached, request for name=beta queued at position 1` →
     `tick: evicting idle LRU name=alpha for a queued request` → `stopping model instance
     name=alpha`。`/models` の `status` は `alpha: unloaded / beta: loaded` に入れ替わり、
     `alpha` を要求すれば元に戻る（両方向とも 200）。降ろしてから載せるので、VRAM が二重に
     なる区間は無いはずだが、**GPU での確認は残る**（「idle LRU」が生成中のモデルをどう扱うかも
     未測）。子は**別プロセス**で、ルーターがループバックの空きポート（`--port 51219`）に
     `--alias alpha --ctx-size 256 --model …` で spawn する——`/models` の `status.args` に
     そのまま出る。ルーターに `--api-key` を渡しても**子には渡らず**、子のポートの `/health` は鍵無しで 200 を返した——届くのはタスクの netns の中だけである。
  4. **(d) `stream_options.include_usage` の `usage` はルーター経由でも最後のチャンクに来る**
     （`"usage":{"completion_tokens":4,"prompt_tokens":43,…}` が 1 回、`[DONE]` の前）。
     ストリーミングでも非ストリーミングでも **`model` はカタログ id（`beta` / `alpha`）**で
     返る。**窓はモデル毎に効いた**: `/props?model=alpha` が `n_ctx: 256`、`beta` が `512`
     （`[*]` から）。
- **R2. SSM の 3 点。** (a) CP のタスクロールは `parameter/af-ws/*` に
  `ssm:PutParameter` / `GetParameter` / `GetParameters` / `DeleteParameter` /
  `AddTagsToResource` を持つ（`20-platform.yaml` の `SsmWorkspaceParams`）——決定 2 の「既に
  持っている」は正しい。(b) **葉と子は共存できる**: `/af-ws/<x>` を作ってから `/af-ws/<x>/child`、
  逆順の両方が通った（開発配備で作って消した）。`/af-ws/engines` の下に
  `/af-ws/engines/<key>/active` を置く形は成立する。(c) 🔴 **Standard tier の上限は 4,096
  文字**——4,200 文字の `PutParameter` は `ValidationException: Standard tier parameters
  support a maximum parameter value of 4096 characters` で断られた。Advanced tier は 8 KB で
  1 本 $0.05/月。今日のエンジン表は 666 バイトである。本文の「数 KB」は 4 KB の壁を知らずに
  書かれている。
- **R3. CP タスクロールの現物（`20-platform.yaml`）。** ECS は `CreateService` / `UpdateService` /
  `DeleteService` / `DescribeServices` / `ListServices` / `RegisterTaskDefinition` /
  `DeregisterTaskDefinition` / `DescribeTaskDefinition` / `DescribeTasks` / `ListTasks` /
  `TagResource`（資源 `*`）で、**`ecs:RunTask` は無い**。`iam:PassRole` は `ExecRole` と
  `WsTaskRole`（`ecs-tasks` 宛）と `af-*-slot`（`ec2` 宛）。**S3 のアクションは 1 つも無い**
  ——モデルバケットの `GetObject` も `DeleteObject` も無い。したがって決定 6 に要るのは
  `ecs:RunTask`（family 限定）と `iam:PassRole`（**`IngestTaskRole` だけ**——実行ロールの
  PassRole は既にある）で、本文の「取り込みのタスクロールと実行ロール」は半分余計。一方、
  決定 6 の「`upload` コンテナがマニフェストを書いてから `engine_models` に行を作る」（CP が
  マニフェストを読む）と決定 7 の「削除（S3 のファイルも消す）」は、**CP に無い権限を使って
  いる**。
- **R4. テンプレートの予算（問い 3）。** `60-engines.yaml` は **50,774 バイト（残り 426）**。
  廃止する 12 パラメータは **2,558 バイト**（llm 6 つ 1,475・image 4 つ 1,083）、旧コマンド
  ライン（`-m` / `--alias` / `-c`）とエンジン表の `models` / `contextTokens` で約 450——
  **空く余白は約 3,400 バイト**。足すものの概算: `LlmEnabled` / `ImageEnabled` 350、
  `EngineTaskRole` の `GetParameter` 250、`ImageEngine` と `ImageComfyTag` のパラメータ＋条件＋
  `!If` ×4（イメージ・entrypoint・コマンド・表の `health`）1,100、CP 向け `AWS::IAM::Policy`
  800 と表の `ingest` ブロック 250、一般化した fetch サイドカー（SSM 読み・`jq`・ループ・
  preset・コマンドライン）は今日の 648 に対して約 1,800 で **2 役に複製すると +2,300**、
  プレースホルダのラッパー ×2 で 400——**合計 約 5,450**。差し引き **約 2,000 バイトの超過**。
  サイドカーの本文を `Mappings` に 1 本置いて両役から `!FindInMap` で引けば +1,200 に縮み、
  超過は約 950 になる。**変更セットは壁に入らない**が、コメントが **15,568 バイト**、
  パラメータの Description が約 5,500 バイトあるので、ボリュームの 2 つの実測注とハーネス向けの
  `run-task` レシピ（合計 3 KB 強）を `PARAMETERS-60-engines.md` へ移せば入る。`env.sh` の
  `af_cfn_deploy` は 51,200 超で S3 経由に切り替わるが、CI の case 3b-2 が壁で落とすので
  それは経路ではない。もう 1 つ: **`EcrComfyUri` は `20-platform` に無い**（ECR は
  `af-control-plane` / `af-workspace` / `af-voicevox` / `af-llamacpp` / `af-sdcpp` の 5 つ）。
  決定 4 の `!If` は `20-platform`（22,530 バイト。余裕はある）にリポジトリ 1 つと、
  `standup.sh` の images 段に自前イメージの複製を足して初めて成立する。
- **R5. イメージの中身。** sd-server の `master-cuda` は `nvidia/cuda:*-cudnn-runtime-ubuntu*`
  ベース（上流 `docker/Dockerfile.cuda`。`ENTRYPOINT /sd-cli`、`libgomp1` を apt で足すだけ）
  なので **`sh` はある**——決定 1 のプレースホルダ（`sh -c '… sleep infinity'`）は同じ
  イメージで書ける。fetch サイドカーの `public.ecr.aws/aws-cli/aws-cli:latest` には
  **`jq`・`python3`・`bash`・`sh` が入っている**（`crane export` で確認）ので、active set は
  JSON のままサイドカーで読める。
- **R6. コントローラの現物（`engine_control.go`）。** `decideEngineAction` は `mode=on` で
  desired ≥ 1 なら何もしない（`engineReasonOn`）。起動期限は **ondemand かつ `starting`** に
  しか効かず、`running` で `warmed()` が false のままの状態は**失敗にならず、5 秒間隔で
  永遠に見に行く**（`tick` の `engineControlBusyInterval`）。決定 1 のプレースホルダ
  （`sleep infinity`）が RUNNING になると、ondemand ではゲートウェイが起こさない限り無害だが、
  **`mode=on` の配備では管理者が「モデル無し」の箱を $1.26/時で買い続け、パネルは永遠に
  「準備中」**を出す。
- **R7. `generate_image` の現物。** `imagegen.Request` には **`Model` が既にあり**（「Empty
  means the provider's own default」）、`Caps(model)` は (provider, model) 毎——決定 5 の
  `model` 引数は 0069 の形に収まる。`loras` に相当するものは `Request` にも `Caps` にも無い。
  ツールの引数は「**本当に選べるときだけ出す**」規則で作られており（`provider` は経路が
  2 つ以上のときだけ、`aspect_ratio` は経路の和集合）、`model` 引数は今日**無い**。
  Agent のカタログは tools/list（**毎ターン**）の経路で 10 分 TTL、Agent の `POST /engine/usage`
  （`routes.go`）は CP → Agent の逆経路として**既にある**——決定 7 の `catalog-changed` は
  同じ形で作れる。`opencode.ApplyEngineChange` と `engineProviderEntry`（モデル単位）も本文の
  とおり在る。
- **R8. HF の現況（本日）。** FLUX.1-schnell は `license: apache-2.0`・`gated: auto`（本文の
  とおり）、FLUX.2 klein 4B と Z-Image-Turbo は `apache-2.0`・`gated: false`。🔴 **FLUX.1-dev と
  SD3.5 Medium は `cardData.license` が `"other"`** で、実体は `license_name`
  （`flux-1-dev-non-commercial-license` / `stabilityai-ai-community`）にある。決定 2 の
  マニフェストが `license`（`cardData.license`）だけを写すと、**非商用の 2 つが「other」と
  だけ書かれて出る**。
- **R9. `useLocalStorage` は置き換えではなく更新で効く。** `AWS::ECS::CapacityProvider` の
  スキーマで create-only なのは `Name`・`ClusterName`・`InstanceLaunchTemplate/FipsEnabled`
  （`CapacityOptionType` は条件付き）で、`LocalStorageConfiguration` は含まれない。
  `ImageUseLocalStorage=true` はスタック更新 1 回で入り、関連付けリストは動かない——未解決 9 の
  計測は「パラメータを 1 つ変えてハーネスを 1 回」で済む。
- **R10. 開発配備は起こしていない。** GPU 実測は無し（費用 $0）。R2 で作った SSM パラメータは
  消した。

### 決定ごとの改訂提案

- **決定 1** — 5 点。(a) **P1 を後回しにした以上、P0 の llm 役は `-m` のままで「空で listen」
  できない**（R1(a) はルーターの性質）。P0 は両役とも同じラッパーで揃える: サイドカーが
  active set から `/models/cmdline` を書き、エンジンのコンテナは `sh -c 'if [ -s
  /models/cmdline ]; then exec … $(cat /models/cmdline); else sleep infinity; fi'` で起きる
  （R5: どのイメージにも `sh` はある）。llm 役をルーターに切り替える日（P1）は、サイドカーが
  書く 1 行が `--models-dir … --models-preset …` に変わるだけで、ラッパーは同じ。(b)
  **`ParameterNotFound` を「空」として扱う**——`60-engines` は `30-ingress` より先に建つので、
  スタック作成時に CP は存在せず、active set は書かれていない。ここで `set -e` が拾えば
  二段階スタンドアップが形を変えて戻る。(c) **コントローラに「カタログが空なら起こさない」を
  足す**（R6）: `engineSnapshot` に `hasModels` を持ち、`mode=on` でも `decideEngineAction` が
  `engineReasonNoModel` で何もしない。パネルはトグルの代わりに「有効なモデルが無い」を出す。
  ゲートウェイの `503 engine_unavailable` だけでは `mode=on` の穴が残る。(d) スタンドアップは
  **今日と同じく安定化のために GPU 箱を 1 回買う**（プレースホルダも `GPU` の
  `ResourceRequirements` を持つ）——消えるのは二段階で、初回の 10 分と $0.2 ではない。本文に
  書く。(e) 以上の条件で、二段階は本当に消える。
- **決定 2** — (a) IAM の前提は正しく（R2(a)）、箱側の `GetParameter` を `60-engines` の
  `EngineTaskRole` に足す形は 0071 決定 8 の「新しい IAM はスタックの中で閉じる」と整合する。
  (b) 🔴 **「数 KB」を「4,096 文字」に改める**（R2(c)）。active set には S3 キー・ローカル名・
  フラグ・preset の材料だけを置き、`description` や `license` は DB に留める。テストで
  「モデル 20・LoRA 20 の active set が 4,096 文字に収まる」を固定し、収まらない設計変更が
  来たら Advanced tier（$0.05/月）に上げる判断を**その時に**する。(c) 葉と子は共存する
  （R2(b)）ので名前はそのままでよい。(d) CP が作るパラメータは CloudFormation の外にある
  ——`teardown.sh` が `/af-ws/engines/*/active` を消すことを書く。(e) サイドカーは `jq` で
  JSON を読める（R5）。行志向にする必要は無い。(f) S3 レイアウトの移行（`image/…` →
  `image/checkpoints/…`）と決定 7 の種（`modelS3Key`）は**同じ手順の中で**動かす——種が旧キーを
  指せば、最初の起動が空振りする。
- **決定 3** — 「未解決 1 の実測に依存する」の但し書きは**外せる**（R1）。ルーターは要件を
  満たす: 空で ok、autoload は待つ、`--models-max 1` は LRU を降ろして載せる、`usage` と
  `model` は届く、窓はモデル毎。本文の `warm` の再定義（`GET /models` の `loaded`）は R1(a) で
  必須になった——`/health` を warm と読む今日の `warmup` はルーターでは常に true を返す。
  細部を 2 つ足す: `404 model_unknown` はゲートウェイ自身の判定で、ルーター自身は **400** を
  返す（どちらでもよいが、番号を混ぜない）；子プロセスはループバックの空きポートに
  `--api-key` 無しで立つ（タスクの netns の外からは届かないので許容。書いておく）。
  残る GPU の宿題は 1 つ——「idle LRU」が**生成中**のモデルをどう扱うか（待つのか）。
- **決定 4** — (a) `ImageEngine` の `!If` は R4 の予算を通してからで、**`20-platform` の
  ECR リポジトリ 1 つと `standup.sh` の images 段の追加**が前提（自前イメージは
  `af-workspace` と同じく CI で焼いて複製する。0071 決定 6）。(b) 🔴 **決定 10 の「既定は
  klein 4B」は ComfyUI の実測であって sd-server の実測ではない**。P0 の image 役は sd-server
  なので、**P0 の既定は SDXL**（唯一 sd-server で測った 1 ファイルのチェックポイント）。klein は
  sd.cpp が対応を名乗るが、分割モデルのフラグ組み立て（決定 4 の「分割モデルは 1 エントリ」の項）ごと未測で、P0 で
  それを踏むと完了の定義が sd-server の問題で止まる。(c) **P0 の完了の定義に使う「2 つ目の
  チェックポイント」が決まっていない。** バケットにある sd-server 向けの 1 ファイルは SDXL
  base だけで、Z-Image・klein は分割モデルである。SDXL 系のもう 1 本（OpenRAIL++ の
  fine-tune か、Refiner）を先に取り込む——ライセンスは決定 10 の規則で選ぶ。(d)
  `UpdateService --force-new-deployment` は CP の既存権限で足りる（R3）。
- **決定 5** — 🔴 **「CP が要求で拒否する」は、決定 4 の「ゲートウェイは本文を読まない」と
  両立しない。** sd-server 経路では LoRA は prompt の `<sd_cpp_extra_args>` の中にあり、
  ComfyUI 経路ではワークフロー JSON のノードの中にある——CP が拒否するには prompt かグラフを
  読むことになる。拒否の場所を**Agent**（enum 外の名前と `baseModel` 不一致を組み立ての
  段階で断る）と**箱**（有効な LoRA しか同期しないので、無い名前はエンジンが失敗させる。
  `--lora-model-dir` と `models/loras/` がパスの範囲）に移し、CP の行を消す。`model` /
  `loras` の形は 0069 に収まる（R7）: `Request.Model` は既にあり、`Caps` に `Loras []{name,
  description, baseModel}` を足す。ただし規則を 2 つ書く: **引数は本当に選べるときだけ出す**
  （`provider` と同じ——フリートのエンジンが有効なチェックポイントを 2 つ以上持つときだけ
  `model` が現れ、値はカタログの id のみ。Codex / agy の固定モデルは enum に混ぜない。
  `model` を指定しつつ provider がフリート以外なら名指しで拒む）；**enum は他の引数に依存
  できない**ので、`loras` の enum は有効な LoRA 全部で、説明に各 LoRA の `baseModel` を書き、
  組み合わせの検査は Agent がする。
- **決定 6** — (a) `ecs:RunTask` は本当に無い（R3）ので必要。`iam:PassRole` は
  **`IngestTaskRole` だけ**——`ExecRole` の PassRole は `PassTaskRoles` に既にある。本文の
  「取り込みのタスクロールと実行ロール」を直す。(b) 🔴 **CP はマニフェストを読めない**
  （S3 の権限が無い）。行は **CP 自身が HF から解決した sha256・license・bytes と、
  `DescribeTasks` の終了コード（`fetch` SUCCESS → `upload` SUCCESS）**から作る——それで
  足りる（sha256 の照合は `fetch` が済ませている）。マニフェストは箱が読むもので、CP は
  読まない、と書く。(c) 「CP の IAM 追加ゼロ」を守る代替は **EventBridge**: CP が
  `/af-ws/engines/ingest/job` を書き（既存の PutParameter）、`60-engines` の
  `AWS::Events::Rule`（`aws.ssm` の Parameter Store Change）がスタック内のロールで `RunTask`
  し、CP は `ListTasks --family` で追う。CP 側ゼロは守れるが、配送が at-least-once で
  ジョブが二重に走りうる上、失敗が CloudTrail にしか出ない。**推奨は本文の
  `AWS::IAM::Policy` を受け入れ、0071 決定 8 を精密に改訂すること**: 「CP のロールに他スタックが
  足してよいのは、**そのスタックの資源にしか効かない権限**（family 限定の RunTask・ingest
  ロール限定の PassRole）だけで、`Resource: *` は足さない」。(d) CP から HF API への egress が
  前提（30-ingress の NAT の先）。自前の前段で外向きを絞る配備では取り込みは手打ちに落ちる。
- **決定 7** — 🔴 **「削除（S3 のファイルも消す）」は CP にできない**（R3）。取り込みタスクに
  `MODE=delete` を持たせて同じ `RunTask` で消す（マニフェストを先に消す順序はそこで守る）か、
  P4 まで `harness/ingest-model.sh` の兄弟に任せる。押し通知の逆経路は在る（R7）。
- **決定 10** — (a) 🔴 マニフェストと `engine_models` は **`license` と `license_name` と
  URL の 3 つ**を持つ（R8: 非商用の 2 つは `license: other`）。取り込み時の値は HF の
  card が変わっても動かない**スナップショット**で、それは本文のとおり。(b) 受諾は
  **誰がいつ**を残す（`licenseAcceptedBy` / `At` をマニフェストと監査ログに）。(c)
  マルチテナント配備の妥当性（問い 8）: 「運用者がメンバー全員の代わりに条項を引き受ける」は
  正しい読みで、それをそのまま UI の一文にする。加えて **`commercialUse` の軸**を出す——
  FLUX.1-dev の非商用は「メンバーに有料で提供する配備」では運用者自身の違反になるので、
  「取り込みは拒まない」の横に「この配備が商用なら入れてはいけない」をライセンス名から
  引ける形で出す（Stability の年商 $1M も同じ欄）。(d) `HF_TOKEN` は個人のアカウントに
  紐づく——同意した人が去れば失効する。組織アカウントのトークンを推奨に書く。
- **未解決 1** — **解けた**（R1）。残るのは (c) の GPU 確認 1 回だけで、それは P1 の完了の
  定義（交替のリロード）と同じ観測である。
- **未解決 5** — **表にする**。理由が 3 つ: (1) 書き手が 2 人いる（管理者のトグルと、
  取り込みジョブの状態遷移 `ingesting → ready / failed`）。`SettingsStore` の JSON 1 本は
  全体を読んで全体を書く形で、CAS が無く、同時に来れば片方が消える；(2) `files[]` /
  `args[]` / `sizes[]` は行ごとに形が違い、パネルの一覧・種・削除はどれも行の操作である；
  (3) 2 方言の migration は `engine_hourly`（`migrations/0055` と `migrations-pg/0040`）の
  前例があり、費用は既知。`lastUsedAt` は **P0 では持たない**（要求ごとの UPDATE を避ける。
  `engine_<key>_demand_at` の 1 分抑制と同じ形で P4 に足せる）。
- **未解決 9** — **測る価値はある。1 GPU 時間で 2 つ動く。** (1) `useLocalStorage`: R9 の
  とおりパラメータ 1 つで入る。効くのは切り替え（12.3 GB を 92 MB/s → NVMe）だけでなく、
  **コールドスタートの S3 → EBS 33 GB・332〜360 秒**もで、これは EBS の書き込み上限
  （g6.xlarge のベースライン 125 MB/s）に張り付いた数字である。(2) **RAM で解く案**を
  並べて測る: 3 モデル 27 GB がページキャッシュに残らないのは箱の RAM が 15 GB だから
  で、`ImageMemMinMiB` を 30,000 に上げれば g6.2xlarge（32 GiB）が選ばれ、コードは 1 行も
  変わらない。切り替えが RAM → VRAM（数秒）になれば、`useLocalStorage` は起動時間だけの
  話に戻る。どちらも `bench-image-engine.sh` がそのまま測れる（フェーズ 2 回で切り替えを含む）。
  順序は P2 の前——ComfyUI のイメージ作りと独立で、P2 の完了の定義（切り替えの価格）を
  変えるからである。
- **フェーズ** — P0 に足す: ラッパー entrypoint、`ParameterNotFound` の扱い、コントローラの
  「空なら起こさない」、コメントの `PARAMETERS-60-engines.md` への移動（R4）、2 つ目の
  チェックポイント、`teardown.sh` の SSM 掃除、S3 移行と種の同時性。P1 は未解決 1 に
  縛られない（R1）。P2 は `20-platform` のリポジトリと自前イメージの CI を含む。P4 は
  `MODE=delete` を含む。

### 問いへの答え

1. **決定 2 の SSM は閉じる**（R2(a)）——CP 側は追加ゼロ、箱側は `60-engines` の中の
   `GetParameter` 1 文で、0071 決定 8 と整合する。閉じないのは**値の大きさ**で、4,096 文字が
   上限（R2(c)）。
2. **安定はする。ただし 3 条件つき**（決定 1 の改訂 (a)(b)(c)）: 両役ともラッパー、
   `ParameterNotFound` は空、`mode=on` の穴を塞ぐ。ヘルスチェックはコンテナに無く（ECS の
   RUNNING は「essential が起動した」）、CFN が待つのはそこまでなので、`sleep infinity` で
   安定する。二段階は消えるが、安定化のための GPU 箱 1 回は残る。
3. **入らない。** 余白 3,400 に対して追加 5,450（`Mappings` で共有しても 4,350）で、
   約 1〜2 KB の超過（R4）。コメント 15.5 KB のうち 3 KB を `PARAMETERS-60-engines.md` へ
   移せば入る。加えて `20-platform` に ECR リポジトリが要る。
4. **拒否の場所が違う**（決定 5 の改訂）——CP は本文を読まないので Agent と箱で拒む。
   `model` は `Request.Model` に、`loras` は `Caps` の追加で 0069 に収まる（R7）。引数は
   「本当に選べるとき」だけ、値はフリートのカタログ id だけ。
5. **代替はある**（EventBridge）が推奨しない。`AWS::IAM::Policy` を受け入れ、0071 決定 8 を
   「そのスタックの資源にしか効かない権限は足してよい」に改訂する。PassRole は ingest ロール
   だけ（R3）。S3 の読み書きは**足さない**——CP はマニフェストを読まず、削除は取り込みタスクが
   する（決定 6・7 の改訂）。
6. **表**（未解決 5 の項）。`lastUsedAt` は P0 では持たない。
7. **説明文だけでは足りない。** 「いま温かいモデル」は CP が `POST /engine/usage` の
   `model` から**最後に成功した要求のモデル**として持てる（エンジンには聞かない。0053）。
   これをカタログの行に `warm_model` で出し、**`model` 未指定の既定を「温かければそれ、
   でなければカタログの既定」**にサーバ側で決める——エージェントに温度を推論させない。
   切り替えた要求は `warnings` に「モデルを切り替えたので +N 秒」を返す。`useLocalStorage`
   は測る価値がある（未解決 9 の項。RAM 案と並べて 1 GPU 時間）。
8. **妥当。ただし 3 つ足す**（決定 10 の改訂）: `license_name` を持つ、受諾の記録、
   `commercialUse` の軸と「この配備が商用なら」の一文。

### 状態行の提案

上の決定 2・5・6・7・10 を本文に反映したうえで、状態行を
**「承認済み（P0 着手可）」（2026-09-08 レビュー）** に改める。未解決 1 は「解けた（レビュー
R1）」に打ち消す。P0 の完了の定義に **「`mode=on` のエンジンがカタログ空で起きないこと」と
「スタック作成時（CP 不在・active set 無し）に両役のサービスが安定すること」** を足す——前者は
R6 の穴、後者は決定 1 の主張そのものの観測である。

## レビュー（2026-09-09・未解決 10・11・12）

P4 の実機検証中に利用者から出た 3 つ——**モデルの置き場（10）・テナント軸（11）・HF トークンの
登録（12）**——を別セッションで詰めた。10 は実機で測り、11 と 12 は箱を起こさずに設計として
決めた。GPU の実費は約 35 分＝**$0.74**。

出発点だった「**S3 は律速ではない、受け取る先のディスクが遅い**」は**半分だけ正しかった**。
EBS の書き込み上限は確かに縛っていたが、外すと**次の壁（S3 と CLI の約 160 MB/s）がすぐ後ろに
いた**。本当に効いたのは取得ではなく**ディスクから VRAM への読み**である。

### 1. 未解決 10(a)——`useLocalStorage` は効いた。既定を `true` にした

まず **`LocalStorageConfiguration` が create-only ではない**ことを API で確認した（R9 の主張の
裏取り）。`ImageUseLocalStorage=true` の**更新 1 回・約 2 分**で `UPDATE_COMPLETE` になり、
capacity provider は `storageConfiguration: null` ／ `localStorageConfiguration.useLocalStorage:
true` に変わった。CFN を回し直す必要も、capacity provider を作り直す必要も無い。

測ったのは**配備の `llm` 役そのもの**——ベンチ用の箱ではなく、決定 3 が値段を付けた当の経路で
ある。同じ 2 モデル、同じファイル、EBS 設定で記録済みの数字と並べる:

| | EBS（記録済み） | インスタンスストア | |
|---|---|---|---|
| S3 → ディスク 1.1 GB | 8 秒（140 MB/s） | **5 秒（223 MB/s）** | 1.6 倍 |
| S3 → ディスク 18.5 GB | 159 秒（117 MB/s） | **117 秒（159 MB/s）** | 1.4 倍 |
| ディスク → VRAM 18.5 GB | 267 秒 | **91 秒** | **2.9 倍** |
| RunTask → model loaded | 527〜586 秒 | **275 秒** | 約 2 倍 |
| 交替 → 1.1 GB のモデル | 10.0〜10.1 秒 | **3.3 秒** | 3.0 倍 |
| 交替 → 18.5 GB へ戻る | 276〜282 秒 | **98.5 秒** | **2.8 倍** |

image 役でも 1 点取れている（ベンチは別の理由で落ちたが fetch は走った）: SDXL 6.94 GB を
**29 秒＝239 MB/s**、実測で解けた点 7 の 92〜100 MB/s に対して 2.4 倍。

🔴 **読み取るべきは取得ではなく交替である。** 決定 3 は「1 箱 1 モデル・要求で交替」に
**276〜282 秒**という値段を付け、実測 5 は image 役の切り替えを 1〜2.5 分と書いた。その値段が
**98.5 秒**になった。人が待つのはここで、コールドスタート（275 秒）は次である。

代償は 2 つとも本物だが、どちらも新しくない: インスタンスストアは箱と一緒に消える——が
**EBS のデータボリュームも MI が消す**ので、コールドスタートが毎回 S3 から取るのは前と同じ。
そして `*StorageGiB` が効かなくなり、箱は機種が持つものをそのまま得る——g6.xlarge で
**245 GB の ext4**、つまり EBS 設定の 120 GiB より**広い**。⚠️ 帰結として
`*AllowedInstanceTypes` は**インスタンスストアを持つ機種しか書けない**（g6・g5 は全サイズが
持つ）。

**採った**: `LlmUseLocalStorage` / `ImageUseLocalStorage` の既定を `true` にし、開発配備も
その状態にした。戻すのはパラメータ 1 つである。

### 2. 未解決 10(b)——温かい箱は「未証明」ではなく**否定された**

0071 決定 7(c) は 2 セッション「未証明」のまま残り、`PARAMETERS-60-engines.md` は「MI の
データボリュームがどこにマウントされているかが分かれば解ける、GPU 1 時間の調査」と書いていた。
**約 4 分で終わり、答えは「解けない」だった。** 新しい道具は
`harness/probe-warm-volume.sh` と、ベンチの fetch が吐くようにした `mountinfo:` の 1 行である。

- **匿名ボリュームが取り直す理由**が、観測から機構になった。コンテナは自分の bind mount の
  ホスト側パスを `df` では見られないが `/proc/self/mountinfo` は見られる:
  `/._mnt_task/volumes/<タスク ID>/volumes/models → /models  ext4 /dev/nvme1n1`。
  **パスにタスク ID が入っている。** 匿名ボリュームは**構造上**タスク毎に空の新しい
  ディレクトリになり、設定では変えられない。変えられるのは名前付きだけである。
- **名前付き `SourcePath` は確かに残った。** 同じインスタンスで 2 本続けて走らせ、
  `/var/lib/af-warm-models` を mount して: 1 本目 MISS で取得、2 本目 **HIT**（mtime も同じ）。
  難しいと思われていた「持続」の側は問題なく働く。
- 🔴 **しかし落ちる先が 3.1 GB である。** その mount は `/dev/nvme0n1p8`——**ルート**ボリューム
  上の小さなパーティションで、匿名ボリュームが載る 245 GB の `/dev/nvme1n1` ではない。1.1 GB の
  試験ファイルは 38% で収まったが、**18.5 GB のモデルは記録済みの `No space left` を寸分違わず
  再現する**。この問いが 3 セッション進歩と取り違えていたのは、**容量の無い持続**だった。
- **そしてデータボリュームには名前で届くパスが無い。** AMI は **Bottlerocket** で、ホストの
  `/` をマウントすると **2.7 GB・100% 使用・読み取り専用**の dm-verity イメージが見える。
  `SourcePath` が書ける最上位のディレクトリは**どれも**——`/local`・`/mnt`・`/data`・`/opt`・
  `/var`——そのイメージの中に解決され、生きたホストのマウントには入らない。`/._mnt_task` は
  作ることすらできない（`read-only file system`）。**`useLocalStorage` でも変わらない**:
  変わるのはデータボリュームが**何であるか**で、`SourcePath` が**どこを指せるか**ではない。

**帰結**: MI の上では、温かいモデルボリュームは**ホストボリュームでは作れない**。箱を保持して
買えるのはイメージ層だけで、`*ScaleInAfter: -1` は $1.26/時で何も買わない設定である。0071
決定 7(c) は**否定**として畳み、`PARAMETERS-60-engines.md` をそう書き換えた。

### 3. 未解決 10(c)——EFS は却下を維持する。ただし**根拠を差し替える**

却下の条件文（「sync が P0 で痛いと分かったら再考する」）は満たされていたので、再考した。
**単価を先に確認した**——ADR が 🔴「第三者の転記。要再確認」と注記していた数字である。
AWS Pricing API・ap-northeast-1・2026-09-09:

| | 単価 | |
|---|---|---|
| EFS 標準（General Purpose） | **$0.36/GB-月** | 🔴 **ADR の数字は正しかった** |
| EFS One Zone | $0.192/GB-月 | |
| EFS IA / One Zone-IA | $0.0272 / $0.0145/GB-月 | 読み書き $0.012/GB |
| **EFS Elastic Throughput のデータ転送** | **読み $0.04/GB・書き $0.07/GB** | ADR が見ていなかった項 |
| EFS Provisioned Throughput | $7.20/MiBps-月 | |
| S3 標準 | $0.025/GB-月 | ADR の $2.49/月 と一致 |

**スループットが「NFS のそれで桁で遅い」という却下理由は間違いだった**——Elastic Throughput の
EFS は速い。だが却下は**別の、いま検証された理由で立つ**。EFS には課金モードが 3 つしかなく、
**どれも成立しない**:

- **Elastic**: コールドスタート 1 回が 18.5 GB の読み＝**$0.74**。それで消せる GPU 時間は
  多く見積もって 100〜125 秒＝**$0.035〜0.044**。**17〜21 倍の赤字**で、回数を増やしても
  好転しない（損は 1 回ごとに比例して増える）。**損益分岐は存在しない。**
- **Bursting**（読みが無料になる唯一のモード）: ベースラインは蓄積 1 TiB あたり 50 MiB/s で、
  実測 99.5 GB では **約 5 MiB/s**。バーストは 100 MiB/s だが——**いま逃げてきた EBS の
  125 MB/s より遅い**——18.5 GB 読むとその分のクレジットを使い、貯め直すのに約 63 分かかる。
  1 時間に 2 回目のコールドスタートは 5 MiB/s に落ちる（18.5 GB に 1 時間超）。
- **Provisioned**: NVMe に並ぶ 250 MiB/s を買うと **$1,800/月**。

保管費も 99.5 GB で EFS 標準 **$35.8/月** 対 S3 **$2.49/月**（14.4 倍）。One Zone-IA なら
$1.44/月 と S3 より安いが、読みが $0.012/GB＝**1 回 $0.22** で、やはり浮く GPU 時間の 5〜6 倍。

**FSx for Lustre も見た**（EFS より形は合う）が、SSD の persistent が $0.188〜0.848/GB-月 で
最小容量があり、Intelligent-Tiering はスループットに $0.656/MBps-月。**HPC クラスタの値段で、
寝ている GPU 1 箱の値段ではない。**

🔴 **そして決定的なのは、EFS が直すはずだった当のもの（350 秒の同期）を `useLocalStorage` が
$0 でほぼ直したことである。** 残り約 100 秒を 1 回 $0.74 で追う話になった。**却下を維持**し、
0071 の却下節の理由を「スループットの仮定」から「**検証された単価**」に差し替える。唯一 EFS が
勝つ軸は人の待ち時間で、それを買いたい配備は 1 日 9 回の起動で月 $200 を払うことになる——
**判断できる形にはなった**というのがこの節の成果である。

### 4. 未解決 11——テナント軸は「取り込みと受諾」に入れる（ADR の推奨を採る）

ADR の中間案（カタログは配備で 1 つ、**「取り込める人」と「誰が同意したか」をテナント軸に**）を
**採る**。理由は ADR の 4 点に加えて、**壁の順番が本文と違う**ことが今日の実測で分かったから
である。

本文は 4,096 文字の SSM を最初の壁として挙げていた（「ここで初めて 4,096 文字が効く。いま 6%」）。
実測すると**それは 3 番目**である。active set の 1 モデルは実測 **約 105 文字**
（llm は 2 モデルで 242 文字、image は 216 文字）なので:

| 壁 | 限界 | 効き始め |
|---|---|---|
| **時間**（同期は直列・実測 159 MB/s） | 10 分のコールドスタートで **約 5 モデル**（95 GB） | **最初** |
| **ディスク**（インスタンスストア 245 GB） | **約 13 モデル**（EBS 120 GiB なら 6） | 2 番目 |
| SSM（4,096 文字・封筒 32 文字） | **約 38 モデル** | 最後 |

つまりテナント毎にカタログを持たせると、**1 テナント 1 モデルでも約 5 テナントで
コールドスタートが 10 分を超える**。文字数の壁（38）を見ていると 7 倍楽観する。有効な全モデルを
毎回同期する設計である以上、**掛け算を避ける以外に手は無い**——そして `useLocalStorage` は
ディスクの壁を 6 → 13 に広げたが、**時間の壁は動かしていない**（1.4 倍しか速くなっていない）。

決めたこと:

- `engine_models` の主キーは **`(role, id)` のまま**——配備で 1 つ。変更なし。
- テナント軸が付くのは **(1) 取り込みを起こせるか**（権限）と **(2) 誰がライセンスに同意したか**
  （`license_accepted_by` を `(tenant_id, member_id, accepted_at, license)` に広げる）。`source`
  は既に出所を持っており、この形の半分は P4 で入っている。
- 🔴 **代償を隠さない**: カタログは配備で 1 つなので、**どのテナントからも全モデルの id が
  見える**。これは受け入れる費用であり、利用者向けガイドに書く。隠すと「見えないはず」と
  誤解した運用が生まれる。
- **テナント毎の S3 バケットは薦めない**（ADR の理由をそのまま維持——境界は GPU の箱であって
  バケットではない）。分離が要るなら同一バケットのプレフィックス。

### 5. 未解決 12——DB を正本に、Secrets Manager を運搬路に。**「読み戻さない」は IAM で縛る**

ADR の推奨を**採る**。加えて 3 点を足す。

1. 🔴 **「CP は書けるが読み戻さない」を規約ではなく IAM で強制する。** CP のタスクロールに
   `secretsmanager:PutSecretValue` を**その 1 つの ARN だけ**に与え、`GetSecretValue` は
   **決して与えない**。決定 6 が手放す性質（「CP はトークンを持たない」）が、口約束ではなく
   **監査できる境界**として残る。置き場は決定 6 が既に `ecs:RunTask` と `iam:PassRole` のために
   `60-engines` の中に作る `AWS::IAM::Policy` で、新しい資源は要らない（0071 決定 8 の精密化
   ——「そのスタックの資源にしか効かない権限だけ」——にそのまま収まる）。
2. **env で渡す道は最初から無い。** P4 の実測どおり `DescribeTasks` は RunTask の環境変数を
   平文で返すので、運搬は `secrets[].valueFrom` の一択である。CP が「取り込みのたびに override で
   渡す」形は取れない。
3. 🔴 **形が 2 つあり、費用が違う。** いまは `HfTokenSecretArn` が空なら `Secrets` ブロック自体が
   `!If` で消える。トークンが DB のものになると `hasToken` は DB の事実になるが、**タスク定義は
   CFN のもので静的**である。だから:
   - **(a) 小さい改修**: 秘密は運用者が作ったまま、Console は**値の更新だけ**を行う。
     `PutSecretValue` の 1 文で済むが、**一度も `HfTokenSecretArn` を設定していない配備は
     結局 CFN を 1 往復する**——利用者の不満（「入れるのに CloudFormation を回す」）は半分しか
     消えない。
   - **(b) 要求を満たす改修**: スタックが**常に**秘密を番兵値で作り、`Secrets` ブロックが常に
     存在する。`hasToken` は純粋に DB の事実になり、**CFN の往復は消える**。取り込みスクリプトは
     番兵値を「未設定」として扱う。代償は `60-engines` に**新しい資源 1 つ**である。
   - **(b) を薦める**——利用者の要求はそちらでしか満たされない。⚠️ ただし
     **`60-engines.yaml` の余白は元々 29 バイト**だった。今回 `UseLocalStorage` の説明を
     縮めて **96 バイト**まで戻したが、資源 1 つには足りない。**着手時に散文を
     `PARAMETERS-60-engines.md` へ移すのが先**で、これは見積もりに入れておく。
4. **テナント毎のトークンにはしない**（ADR の理由をそのまま維持）。取り込みの結果は配備全体の
   ものだからで、未解決 11 の結論（カタログは配備で 1 つ）と同じ向きである。

### 決定 6 の改訂

決定 6 の「**トークンは取り込みタスクの中に閉じ、CP は持たない**」を、こう改める:

> **トークンの正本は CP の DB（`custodian` で封をする——git の OAuth や MCP のヘッダと同じ
> 置き場）であり、Secrets Manager は ECS にしか渡せない値を運ぶための運搬路である。CP は
> その秘密に `PutSecretValue` だけを持ち、`GetSecretValue` は持たない**——書いた値を読み戻せない
> という意味で、決定 6 の「CP はトークンを持たない」は**弱まるが消えない**。取り込みタスクだけが
> 値を読む点は変わらない。

P4 の実測 1（gated でもメタデータは匿名で読める）は**そのまま生きる**——CP は今後も解決の
ためにトークンを必要としない。変わるのは**登録の経路だけ**である。

### 6. 追試——S3 を直接マウントする（Mountpoint for Amazon S3・2026-09-09）

10(c) で EFS を落としたのは**課金**（Elastic の $0.04/GB＝1 回 $0.74）であって、共有ストレージという
**形**ではなかった。S3 には読みの $/GB が無く、ゲートウェイ VPC エンドポイントで転送料も 0 なので、
**EFS を殺した論拠がここには存在しない**。だから測った。ハーネスは `harness/probe-s3-mount.sh`。

🔴 **条件 1 は通った。** Bottlerocket ＋ Managed Instances で `/dev/fuse` が存在し、ECS は
`linuxParameters.capabilities.add: [SYS_ADMIN]` を実際に与え、`mount-s3 1.24.0` が
**マウントに成功した**（`fuse mountpoint-s3 ro,...`）。ここが塞がっていれば話は終わりだったので、
一番安い検証から始めた意味があった。

**実測（同じ箱・同じタスク・cli を先に流して page cache が mount に有利にならないようにした）**:

| | 18.5 GB の GGUF | |
|---|---|---|
| 対照：`aws s3 cp`（いまサイドカーがやっていること） | **88 秒＝210 MB/s** | |
| **Mountpoint を通した逐次読み** | **32.7 秒＝567 MB/s** | **2.7 倍** |

読んだバイト数は dd 自身が報告した 18,556,689,568——**全量**であり、短読みを速さと取り違えていない。
なお同じ `aws s3 cp` が実測 1 では 158 MB/s、ここでは 210 MB/s だった。箱と時刻で振れるということで、
**対照を同じタスクに入れた理由がこれである**。

🔴 **条件 2 は「代償がある」ではなく「自前イメージが要る」だった。** RPM を `--nodeps` で入れたら
`libfuse.so.2: cannot open shared object file` で起動しなかった——**mount-s3 は依存の無い静的
バイナリではなく libfuse2 に依存する**（`fuse-libs-2.9.9`）。FUSE のマウントは別コンテナから
見えないので `mount-s3` は**エンジンのコンテナ自身**で動かすしかなく、つまり
**llama.cpp / sd-server のイメージに mount-s3 と libfuse2 を焼く**ことになる。タスク定義の
変更では済まない。決定 1 の「両役とも同じラッパーで起きる」も失う。

**条件 3 は未検証**である。567 MB/s は `dd` の逐次読みであって、**llama.cpp が mmap で
GGUF を開いてロードする経路ではない**。

**見積もり（実測ではない）**: いまのコールドスタート 275 秒のうち、コピー 117 秒は消える。ただし
**VRAM ロードの 91 秒は NVMe でも純粋なディスク律速ではない**（NVMe は 2 GB/s 級なのに 91 秒＝
実効 204 MB/s なので、大半は GGUF の処理と PCIe 転送）。だからマウントにしても 33 秒にはならず、
**合計 150〜175 秒**あたりに落ちると見る。交替の 98.5 秒も同様に下がる可能性がある。

**条件 3 も測った（同じ日・`harness/probe-llm-mount-load.sh`）。結論は「採らない」である。**
同じ箱・同じタスクで、ローカルへコピーしてからのロードを対照に置いた:

| | コピー | ロード | 合計 |
|---|---|---|---|
| A いまの経路（S3 → インスタンスストア → VRAM） | 114 秒 | **94 秒** | **208 秒** |
| B マウント・mmap（llama.cpp の既定） | 0 | 150 秒 | **150 秒** |
| C マウント・`--no-mmap` | 0 | **130 秒** | **130 秒** |

読み取れることが 3 つある。**(1) llama.cpp はマウントから 123〜142 MB/s しか出せない**——`dd` の
567 MB/s は出ない。ロードの読み方が Mountpoint の並列性を使い切らないからで、**条件 3 の懸念は
的中した**。**(2) FUSE 上では mmap が高くつく**（150 対 130 秒）ので、やるなら `--no-mmap` が必須。
**(3) それでもコールドスタート単体では勝つ**——コピーが消えるので 208 秒 → 130 秒。

🔴 **しかし交替で負ける。そしてそちらのほうが重い。** いまの設計は有効な全モデルをローカルに
置くので、交替は**ローカルから**の読み直しで実測 98.5 秒だった。マウントにするとローカルの複製が
無くなるので、**交替も毎回 S3 から 130〜150 秒**になる。コールドスタートを約 57 秒縮める代わりに
（pull が fetch と並走できなくなる分を引くと 275 秒 → 約 218 秒）、**交替を 98.5 → 130 秒に悪化
させる**取引である。決定 3 が値段を付けたのは交替のほうで、人が待つのもそこである。

加えて条件 2 の代償（エンジンのイメージに mount-s3 と libfuse2 を焼く＝自前イメージ、決定 1 の
共通ラッパーを失う）を払うことになる。**割に合わない。** `useLocalStorage` が $0 で
527〜586 秒 → 275 秒を出したのに対し、これは自前イメージと引き換えに約 20% で、しかも交替は
悪くなる。**採らない**——ただし「一度ロードしたら交替しない」役なら答えは変わりうるので、
判断ではなく数字として残す。

なお実測中に **g6.xlarge が ap-northeast-1 の両 AZ で在庫切れ**になり、この配備の
`LlmAllowedInstanceTypes` が `g6.xlarge` 1 機種に縮んでいたため箱が建たなかった。テンプレートの
既定（`g6.xlarge,g5.xlarge`）に戻して通した——0071 が「候補を 1 機種にしていた」で書いたことが、
そのまま再演した。

### 状態行の提案

未解決 **10 を「解けた」**（(a) 採用・(b) 否定・(c) 却下維持）、**11 と 12 を「決まった」**に
改める。11 と 12 の実装は P5 とし、**12 の着手条件に「`60-engines.yaml` の余白を資源 1 つ分
空けること」**を書く。未解決 9(1) は 10(a) と同じものなので、あわせて閉じる。

## 追記 — seed とその 4 パラメータの撤去（2026-09-10）

`llamacpp` のパネルを見ながら出た問い: **そもそも seed は要るのか。カタログは空のまま許容し、
使いたくなったら管理モーダルから登録すればよいのではないか。**

それはこの ADR が既に決めたことであり、残っているのは時期だけである。決定 1 は十二個の
パラメータを「**互換のために 1 リリースだけ残し、空のカタログの seed として読む**（決定 7）」と
し、**パラメータは `LlmEnabled` / `ImageEnabled` に縮む**と書いている。空のカタログが一級の状態
であることも決定 1 の小項目にある——fetch サイドカーは「読むものが無い」を成功として扱い、
ラッパーは待機し（`if [ -s /models/cmdline ]; … else sleep infinity`）、ゲートウェイはカタログが
空の役を起こさない（`503 engine_unavailable`）。前半は既に出荷済みで、**`LlmModelFile` /
`ImageModelFile` は 0.18.0 で削除した。**

撤去が残っているもの:

- `LlmModelS3Key` / `LlmModelIds` / `LlmContextTokens` / `LlmMaxOutputTokens` と、image 役の
  `ImageModelS3Key` / `ImageModelIds`。**`*ExtraArgs` は残す**——あれは箱側のフラグ
  （`-ngl 99`・`--diffusion-fa`）であって、モデルのものではない。
- Control Plane の `seedEngineCatalog` と `engineSeedKind`、およびエンジン表の `models` /
  `contextTokens`。

**「いつか」ではなく今やる理由。** `60-engines.yaml` は**いま 51,119 バイトで、51,200 バイトの
壁まで 81**。この ADR 自身の見積りでは十二個のパラメータと古いコマンド行で**約 3,000 バイト**
空く。P2 の残り（族ごとの image-to-image グラフ）も ADR 0074 の梯子も、同じテンプレートの中に
収めなければならない。

🔴 **罠: `*ModelS3Key` は seed だけの欄ではない。** `HasLlmModel` / `HasImageModel` は
`!Or [ <役>Enabled = "true", <役>Enabled = "" かつ <役>ModelS3Key ≠ "" ]` なので、この鍵は
**その役のサービスを作るかどうか**も決めている。`LlmEnabled` / `ImageEnabled` を唯一の判定に
してからでないと、鍵だけを書いていた配備では**役ごと消える**——ふつうのスタック更新として、
黙って。撤去とアップグレードノートは 2 つの変更ではなく 1 つである。

**移行の窓は狭い。** seed が走るのはカタログが空のときだけなので、seed を持つリリースを一度
通った配備には行があり、撤去に気づくことはない。書くべき条件は「先に 0.18.0 を通ること」。
カタログ以前の Control Plane から seed の無い版へ直接飛んだ配備だけが、待機状態で立ち上がる。

副産物として、**ライセンスも `source` も VRAM も無い行**ができる 2 経路のうち 1 つが消える。
もう 1 つは手登録のフォームで、`POST …/engines/{key}/models` はライセンス欄を受け付けるのに
**Console が送っていない**——決定 10 がライセンスをパネルの行に出すと決めており、パネルは記録
されたものしか出せない以上、同じ回で塞ぐ価値がある。

## 追記 — テナント軸を実装した（2026-09-10）

未解決 11 は 2026-09-09 のレビューで「中間案を採る」と決着していたが、実装が入っていなかった。
入れたのは決着どおりの 2 点だけである——**主キーは `(role, id)` のまま**、カタログは配備で 1 つ。

**1. 取り込みを起こせるかの権限。** テナントの `limits` に `allow_engine_ingest` を足した
（`tenantLimits`。super_admin が管理モーダルのテナント設定で切り替える）。エンジンの admin
経路のうち**取り込み系の 6 本だけ**が新しい門を通る（`engine_ingest_perm.go` の
`ingestAdminFor`）——`POST …/{key}/ingest`・`GET …/{key}/ingest`・`…/ingest/resolve`・
`…/ingest/files`・`…/ingest/search`・`POST /api/admin/engines/search`。通れるのは super_admin か、
`allow_engine_ingest` が立っているテナントの **tenant_admin** で、その 2 つだけである。
**有効化・選択中チェックポイント・行の削除・HF トークンは super_admin のまま**——どれも
「他の全テナントが何を走らせるか」を決めるからで、取り込みだけが「カタログに足す」で止まる。

門は**要求ごとに**読む（`ListMemberships` は active な行しか返さないので、名簿から外れた
tenant_admin は次の要求で落ちる）。許可を取り消したら次の要求で 403 になることを試験で固定した。

**2. 受諾の 4 つ組。** `license_accepted_by` / `_at` に **`license_accepted_tenant` と
`license_accepted_license`** を足し、レビュー 2156〜2159 が書いた
`(tenant_id, member_id, accepted_at, license)` にした。移行は sqlite `0061` と postgres `0046` の
両方に書いてある（`TestMigrationSeriesDeclareTheSameSchema` が Postgres 無しで両者の一致を見る。
`AF_TEST_DATABASE_URL` を要る `TestSchemaDialectParity` は飛ぶ）。

- **テナントが空欄なのは欠落ではない。** super_admin は配備全体に対して同意しており、
  演じているテナントが無い。ここに Console が送ってきたテナント見出しを書き込むと、
  **やっていない行為にそのテナントの名前が付く。**
- **ライセンスをもう一度持つのは重複ではない。** 隣の `license` / `license_name` は
  **モデルの説明**でモデルカードが直れば直る。こちらは**過去の行為の証拠**で、上流が
  再ライセンスしても動いてはいけない。
- 監査行もこのとき初めてテナント付きになる（`auditFor`）。他のエンジン操作はモードもクラスも
  配備全体のものなので、そちらは空欄のままにした。

**3. 代償は隠さない。** レビュー 2160〜2162 が「どのテナントからも全モデル id が見える」を
書けと言っているので、利用者向けガイドの `guide/admin/04`（ja / en）に節を足し、
`guide/admin/02` のテナント上限の一覧からもそこへ送っている。理由も数字で書いた——
有効な全モデルを毎回同期する以上、テナント毎のカタログは**1 テナント 1 モデルでも約 5 テナントで
コールドスタートが 10 分を超える**。

**残っているもの。** Console の**エンジンパネル自体は super_admin のまま**である
（`GET /api/admin/engines` は `withSuperAdmin`）。許可されたテナントの tenant_admin は
**API では取り込めるが、画面はまだ無い**——パネルを非 super_admin へ開くには行から
モード・クラス・箱の状態を落とした縮小版が要り、それは同じ回でやるには広すぎる。
super_admin 側の運用（許可の付与と、その結果の受諾記録の閲覧）は今回で閉じている。
実機検証も残っている（この節の変更は配備を触っていない）。

## P6 の実装——seed と 6 パラメータを撤去した（2026-09-10）

上の追記節が挙げた撤去を実装した。**実機未検証。**

1. **テンプレート。** `LlmModelS3Key` / `LlmModelIds` / `LlmContextTokens` /
   `LlmMaxOutputTokens` / `ImageModelS3Key` / `ImageModelIds` を消し、`HasLlmModel` /
   `HasImageModel` を `<役>Enabled = "true"` の 1 条件にした。エンジン表からは `models` /
   `contextTokens` / `maxOutputTokens` / `modelS3Key` の 4 欄が消えた。`*ExtraArgs` は残した。
   **50,997 → 49,298 バイト**（1,699 空き、壁まで 1,902）。**空いた分は使っていない**——P2 の
   残り（族ごとの image-to-image グラフ）と ADR 0074 の梯子の取り分である。

2. 🔴 **罠への手当ては 3 か所で、ノートだけでは足りなかった。**
   - アップグレードノートは `cfn/PARAMETERS-60-engines.md` の「Upgrading: the model parameters
     are gone」（0.18.0 で `*ModelFile` を消したときと同じ場所）。`<役>ModelS3Key` だけを
     書いていた配備は**先に `<役>Enabled=true` を足せ**と書いた。
   - `standup.sh` は `af_param_drop` で 6 つを落とす**前に**、捕捉に `<役>ModelS3Key` があって
     `<役>Enabled` が空なら `<役>Enabled=true` へ翻訳する。落とすだけでは、ノートを読まなかった
     配備で役が黙って消える——`deploy` が「そんなパラメータは無い」と拒否して止まるのではなく、
     ふつうの更新として**成功して**サービスが消える。
   - 同じ翻訳を **sd-server イメージの `crane copy` 側でも読む**（standup の前半）。ここだけ
     `ImageEnabled` を素直に読むと、役は作られるのに ECR が空で `CannotPullContainerError`。
   `update.sh` と手打ちの `cloudformation deploy` はパラメータを渡さないので翻訳のしようがない。
   ノートが要るのはそのためである。

3. **Control Plane。** `seedEngineCatalog` / `engineSeedKind` と `engineDef` の 4 欄を削除した。
   古いスタックが書いた表は**今も読める**（欄は黙って無視される）——CP はスタックより先に上がる
   ので、その形は現に生きている。空カタログの挙動（`503 engine_unavailable`、`no_model`、
   `TestDecideEngineAction` の固定）は変えていない。

4. **手登録フォームのライセンス欄は、このレーンが着手する前に閉じていた**（同日の commit
   `2a998f11`）。フォームは `license_name` / `license_url` を送り、CP が商用可否をそこから読む。
   追記節が挙げた副産物は済みである。

5. **検証。** control-plane と workspace/agent の Go テスト、Console のテスト、
   `deploy/local/ecs-lifecycle-stub-test.sh`（3b-2 のサイズ検査を含む）、`cfn-ascii-test.sh`、
   `engine-sidecar-test.sh`、`check-cfn-exports.py`。stub テストに pre-P6 の捕捉を通す場合を
   足し、(a) 撤去したキーが `deploy` に渡らないこと、(b) `<役>Enabled=true` が渡ることを見る。
   翻訳を外す陽性対照で、この検査が実際に落ちることを確かめた。

**実機で確かめること（完了の定義）。** モデル系パラメータを 1 つも書かずに `LlmEnabled=true`
だけで配備し、その役のサービスが安定すること。空のカタログへ Console からモデルを登録し、
エンジンがそれで起動すること。加えて移行の側——`<役>ModelS3Key` を持つ捕捉から standup を
通し、役が消えずに `<役>Enabled=true` へ翻訳されること。

## 追記 — P3 の LoRA、Agent 側の実装（2026-09-10）

**この回で入ったのは Agent 側だけである。** フェーズ節の P3 の完了の定義——「同じ prompt・
同じ seed で LoRA の有無が絵を変え、SD1.5 の LoRA が SDXL で enum に出ない」——は**まだ
満たしていない**。前半は実機でしか測れず、この回では GPU を一度も起こしていない。

### 入ったもの

- `generate_image` に `loras: [{name, weight}]`（weight は 0〜2、既定 1、1 要求あたり 4 本まで）。
  `Caps` に `Loras []{name, description, baseModel}`（決定 5 が R7 で予告していた形そのまま）。
- 族ごとのテンプレート 5 つすべてに `LoraLoader` の連鎖。要求されなければノードは 1 つも
  増えないので、LoRA 無しのグラフはゴールデンと 1 バイトも変わらない。
- baseModel 不一致の拒否を **Agent 側**に置いた（レビュー決定 5）。拒否は 3 種類——名前が
  カタログに無い／族がチェックポイントと違う／LoRA が族を宣言していない——で、どれも
  ゲートウェイを通らず、GPU を起こす前に返る。
- CP → Agent の catalog wire は**既に足りていた**。`loras` は `model_rows` と別の配列で出ており
  （`engine_gateway.go` の `engineCatalogRowFor`）、各行は `engineCatalogModelRow` を通るので
  `base_model`・`description`・`files` を持つ。CP には 1 行も足していない。落ちないことを
  試験で固定した（`TestEngineCatalogRowSeparatesLorasFromCheckpoints`）。
- LoRA が `model` の enum に出ないことも、同じ分離の裏返しとして固定した。CP が LoRA 行を
  `models` にも `model_rows` にも入れないので、Agent の `engineImageModelIDs` は構造上それを
  見ない。

### 決定 5 の 2 つの規則が、ここで衝突して見える

フェーズ節の完了の定義は「SD1.5 の LoRA が SDXL で **enum に出ない**」だが、決定 5 の改訂は
「**enum は他の引数に依存できない**ので、`loras` の enum は有効な LoRA 全部で、説明に各 LoRA の
`baseModel` を書き、組み合わせの検査は Agent がする」と書いている。両立しない。

**後者を採った。** ツールのスキーマは tools/list の時点で作られ、そこには `model` の値がまだ
無い。`Caps(model)` は (provider, model) 毎なので技術的には絞れるが、絞ると
「**いま温まっているモデルに合う LoRA しか見えない**」になり、呼び出し側が名指しできる
別のチェックポイント用の LoRA が黙って消える。決定 5 が禁じているのはまさにこれである。

なので `loras` の enum は有効な LoRA 全部、説明の各行が `watercolor-v2（sdxl 用）— …` の形で
族を名乗り、合わない組は組み立ての段階で名指しで断る。完了の定義の後半は「enum から消す」
ではなく「**合わない組は絵にならず、理由が返る**」として満たす。

### 実機に残したこと（この回では触っていない）

- **同じ prompt・同じ seed で LoRA の有無が絵を変える**——P3 の完了の定義の本体。実機のみ。
  🔴 ここが特に危ないのは、残作業 5 の教訓がそのまま当てはまるからである: **ゴールデンは
  「誰も走らせたことの無いグラフの形」を固定しているだけ**で、正しさの証明ではない。
  SD3.5 はそれで落ちた。今回は同じ轍を避けるために、`LoraLoader` のノード定義を ComfyUI
  v0.34.0 の上流ソースから読んで突き合わせた（`nodes.py`: 必須入力は
  `model`・`clip`・`lora_name`・`strength_model`・`strength_clip`、返りは MODEL と CLIP）。
  それでも「走らせた」ことにはならない。
- 🔴 **`lora_name` は `<models>/loras` の再帰列挙で、各要素はそのディレクトリからの相対パス**
  （`folder_paths.py` の `recursive_search`）。箱は `/ComfyUI/models` を `/models/image` に
  張っている（`60-engines.yaml`）ので、`image/loras/x.safetensors` は `x.safetensors` として
  出る——Agent が渡している basename と一致する。**ただし 1 段でも深い鍵は
  `sub/x.safetensors` として出るので、basename では `Value not in list` になる**。
  LoRA は `image/loras/` に平置きする、が前提である。
- **サイドカーの同期は既に通っている**（この回の確認、コード変更なし）。`60-engines.yaml` の
  fetch サイドカーは `.loras[]?` を `keys.start` に入れており、active set 側も
  `buildEngineActiveSet` が有効な LoRA を S3 鍵の裸配列として積んでいる。つまり
  **「LoRA が箱に降りない」という穴は無い**。降りた後に ComfyUI が列挙できるかだけが未検証。
- **llm 側の preset 固定 LoRA と仮想モデル id**（決定 5 の後半）は手つかず。
- sd-server の `<sd_cpp_extra_args>` 経路も手つかず（`ImageEngine=sdcpp` の配備が要るときだけ、
  未解決 2 を測ってから、という本文の条件のまま）。

### ついでに直した——欠落 9

`/history` の 503 を待って再試行するようにした（同じ回の別コミット）。あわせて、待っても
取り戻せない状態を 1 つ足している: 再起動した ComfyUI は前のプロセスが受け付けた prompt id の
history を持たないので、一度 `engine_waking` を見た後に prompt id を含まない 200 が返ったら、
その場で「キューが失われた」と返す。再試行だけを足すと、**欠落 9 は「retry と言って retry
しない」から「16 分黙る」に化ける**——ポーリングは 200 を受け取り続け、要求の予算を使い切る
までどこにも報告しない。
