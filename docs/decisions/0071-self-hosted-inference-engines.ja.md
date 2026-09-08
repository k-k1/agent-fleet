# 0071. 自前の推論エンジン（llama.cpp / Stable Diffusion / ComfyUI）を GPU でオンデマンドに動かす——Fargate では買えない GPU を、ADR 0070 の型のまま

[English](0071-self-hosted-inference-engines.md) | 日本語

- 状態: **採用（P0・P1 実装済み・develop マージ済み。PR #419）**（2026-09-07。同日のレビューで承認済み・起草は 2026-09-06）。価格はすべて同日に AWS の公開価格データ
  （東京・オンデマンド）から、イメージとモデルの数値は同日にレジストリ API と Hugging Face
  API から取得した。このコンテナで実測したものはそう書いた。**P0 に着手する前にレビューを
  受けるための文書**であり、形が変わりうるのは「未解決の点」である。
- 翌日（2026-09-07）に実測で改訂: 未解決の点 1〜3 を測って「実測で解けた点」へ移し、決定 5・7・8
  にその帰結を足した。opencode の切断は 300 秒、Managed Instances は CPU 箱で起動 109 秒・
  終了 93 秒、G 系クォータは検証アカウントで 0（申請中）。
- 同日、クォータ承認後に **GPU（g6.xlarge・L4）で通した**: llama.cpp が Qwen3-Coder-30B-A3B を
  載せて opencode がツール呼び出しまで完走し、sd-server が SDXL を 1024px 21 秒で描いた。
  「実測で解けた点」4〜6 を追加し、決定 2・3 の根拠を実測で置き換えた。
- さらに同日、S3 経路と ComfyUI を実測して 7〜9 を追加。🔴 **一度「`-hf` はダウンローダが
  遅い」と書いたのは誤りだった**——同じファイルを Fargate の curl で引いても 4.2 MB/s で、
  **遅いのは HF がそのファイルを配る速度**である。決定 3 の理由は「HF は起動経路に置けない
  ほど遅く、しかもファイルごとに 50 倍違う」に改めた（実測で解けた点 5 を書き直した。
  凍結ジャーナルの流儀に倣い、誤った版の要旨も残す）。
- 同日、**P0 着手前のレビュー**を末尾の「レビュー（2026-09-07・P0 着手前）」に追記した。
  レビューの実測で前提が覆ったのは 2 箇所——🔴 **opencode の 300 秒は「経過時間の壁」ではなく
  「本文が 300 秒無音」の切断で、SSE のコメント行を 10 秒ごとに流せば 400 秒でも 1 回の試行で
  答えが届いた**（決定 5 の差し替え提案）、🔴 **8 vCPU で g6.xlarge は 2 台建ち、建たなかった
  のはドレイン中の箱がクォータを握っていたから**（決定 2）。決定 3 の訂正後の版「遅いのは HF が
  そのファイルを配る速度」も、同じ SDXL が Fargate からは 39.6 MB/s だった 3 点目で崩れた。
  承認（P0 着手可）を提案しているが、状態行の変更は起草側の判断に委ねる。
- 同日、そのレビューを受けて本文を改訂: 決定 5 を「心拍で握る」形へ差し替え（300 秒は経過時間の
  壁ではなく本文が無音の上限）、決定 2 の「8 vCPU では 1 台」と決定 3 の「ファイルで決まる」を
  直し（どちらも 2 点からの一般化だった）、決定 1・6・7・8 と未解決・フェーズにレビューの指摘を
  足した。状態を承認済みに改めた。
- 同日、**P0 を実装・実機検証**（「P0 の実測」節）に続けて **P1（`image` 役と provider
  `sdcpp`）を実装し、エンジン側を実機で測った**（「P1 の実測」節）。フェーズ節の P1 に
  実装で足した 3 点を書いた。🔴 P0 の実測 10 が「GPU 0 台」の根拠にしていた
  `describe-instances` は、**MI の箱を一覧に出さない**——P1 の実測 2 で訂正した。
- 翌日（2026-09-08）に訂正を 1 件追記した。🔴 **P0・P1 が出荷した provider ブロックは、モデルの
  窓を一切宣言していなかった**——`limit` の無いモデルを opencode は context 0 として読み、
  **0 のとき自動コンパクションを止める**。`LlmContextTokens` / `LlmMaxOutputTokens` を
  `LlmExtraArgs` から切り出して直した（「P1 の後の訂正」節）。
- 翌日（2026-09-08）に **P1.5 の残り——Console にエンジンの現況を出す**——を実装し、決定 13 を
  足した。トグル（同 P1.5）だけでは「切っていいのか」を判断できないため、起動時刻・自動停止の
  予定・直近の使用状況・動いているモデル・稼働実績のヒートマップを出す。🔴 実装で分かったのは
  **エンジンの稼働時間はどこにも記録されていなかった**こと——コントローラは 30 秒ごとに
  サービスを読んで見たものを捨てていたので、そのティックをサンプラにして `engine_hourly` を
  足した。決定 13 の大半は「分からないことを書かない」ための規則である。
- 関連: [0070-tts-ondemand-engine.ja.md](0070-tts-ondemand-engine.ja.md)（写す型: 需要で建てて
  アイドルで落とす・Cloud Map 名・純関数のコントローラ・共有費用）/
  [0069-image-generation-providers.ja.md](0069-image-generation-providers.ja.md)（画像生成の
  provider 抽象。自前エンジンはその 4 つ目の層になる）/
  [0045-ec2-persistent-workspace.ja.md](0045-ec2-persistent-workspace.ja.md)（スロットプールの
  不変条件。エンジンをそこに置かない理由は 0070 と同じ）/
  [0048-member-cloud-cost.ja.md](0048-member-cloud-cost.ja.md)（共有費用は按分しない）/
  [0041-cross-session-messaging.ja.md](0041-cross-session-messaging.ja.md)（opt-in ゲートの型）

## 背景

要求は 3 つで、どれも「Workspace の中から使える、フリートが自分で動かす推論エンジン」である。

1. **Stable Diffusion** を、`generate_image`（ADR 0069）の provider として。
2. **llama.cpp** を、opencode が接続できるモデル提供者として。
3. **ComfyUI** を、同じ箱で。画面も触れること。

モデルは Hugging Face から取得して動かす。ADR 0070 は VOICEVOX を「求められたら建て、静かに
なったら落とす」形にした。ここではその型を写すが、**最初の決定がそのままでは写せない**。
0070 の決定 1 は Fargate に置くことだが、**Fargate に GPU は無い**（AWS 公式 FAQ、
containers-roadmap #88 は 2019 年から開いたまま）。llama.cpp は CPU でも動くが、コーディング
エージェントの用途では prefill（長い文脈の取り込み）が CPU では桁で遅く、L4 1 枚と同じ値段の
16 vCPU を買っても価値が出ない（下の却下案）。したがってこの ADR の中心は
**「scale-to-zero できる GPU をどう手に入れるか」** と **「17 GB のモデルを起動のたびに
どこから読むか」** の 2 点で、エンジン自体の選定は従属的である。

### 実測（2026-09-06）

**GPU の入手経路（東京）**

- **Fargate に GPU は無い。** 代替は ECS on EC2 か、**ECS Managed Instances**（以下 MI）。
  MI は AWS がインスタンス・AMI・NVIDIA ドライバ・パッチを管理し、タスクが無くなれば
  インスタンスを終了する。**東京で利用可**（GA 告知の 6 リージョンに含まれる）。
  AWS 自身が 2026-08 に「GPU 推論を scale-to-zero で」という参照構成を公開しており、
  そこでの実測は **14 GB のイメージで投入から初回出力まで約 13 分**（CloudWatch アラームの
  2 分と CUDA graph のコンパイルを含む）、スケールインは「キューが 5 分空なら desired 0」。
- **オンデマンド価格（Linux・時）**と **MI の管理料（時）**。管理料は 2026-07-01 に G 系が
  35% 下がった後の値。

  | インスタンス | vCPU / RAM | GPU | EC2 $/h | MI 管理料 $/h | 合計 $/h |
  |---|---|---|---|---|---|
  | g6f.large | 2 / 8 GiB | L4 の 1/8（2,861 MiB） | 0.293 | 0.023 | 0.316 |
  | g6f.xlarge | 4 / 16 GiB | L4 の 1/8（2,861 MiB） | 0.344 | 0.027 | 0.371 |
  | g6f.2xlarge | 8 / 32 GiB | L4 の 1/4（5,722 MiB） | 0.689 | 0.054 | 0.743 |
  | g6f.4xlarge | 16 / 64 GiB | L4 の 1/2（11,444 MiB） | — | 0.107 | — |
  | g4dn.xlarge | 4 / 16 GiB | T4 16 GB | 0.710 | 0.055 | 0.765 |
  | **g6.xlarge** | 4 / 16 GiB | **L4 24 GB** | **1.167** | **0.091** | **1.258** |
  | g6.2xlarge | 8 / 32 GiB | L4 24 GB | 1.418 | — | — |
  | g5.xlarge | 4 / 16 GiB | A10G 24 GB | 1.459 | 0.114 | 1.573 |
  | g6e.xlarge | 4 / 32 GiB | L40S 48 GB | 2.699 | — | — |
  | c8g.4xlarge（比較・CPU） | 16 / 32 GiB | 無し | 0.800 | 0.096 | 0.896 |
  | Fargate 16 vCPU / 32 GiB（比較） | | 無し | x86 0.986 / ARM 0.789 | — | — |

  （VRAM はレビューで `describe-instance-types` から取った公式値。g6.xlarge は 22,888 MiB、
  インスタンスストア 250 GB、EBS ベースライン 125 MB/s。）
- **保存の単価（東京・GB-月）**: S3 標準 **$0.025**、EFS 標準 **$0.36**（IA $0.0272＋読み
  $0.012/GB）、EBS gp3 **$0.096**（第三者の転記。要再確認）。NAT のデータ処理は **$0.062/GB**。
  `00-network` の S3 ゲートウェイエンドポイントは無料で、S3 との往復は NAT を通らない。

**イメージ（圧縮サイズ・レジストリ API）**

| イメージ | サイズ | アーキ | 備考 |
|---|---|---|---|
| `ghcr.io/ggml-org/llama.cpp:server-cuda` | **2.47 GB** | amd64 / arm64 | 公式 |
| `ghcr.io/ggml-org/llama.cpp:server`（CPU） | 297 MB | amd64 / arm64 / s390x | 公式 |
| `ghcr.io/leejet/stable-diffusion.cpp:master-cuda` | **2.31 GB** | **amd64 のみ** | 公式・`sd-server` 同梱 |
| ComfyUI | **公式イメージは無い** | | `lecode-official/comfyui-docker:latest` 5.1 GB、`yanwk/comfyui-boot` 3.9〜10.8 GB |

**モデル（Hugging Face API・`blobs=true`）**

| 用途 | リポジトリ | ファイル | サイズ | ゲート |
|---|---|---|---|---|
| コード LLM | `unsloth/Qwen3-Coder-30B-A3B-Instruct-GGUF` | `…-Q4_K_M.gguf` | **17.3 GiB** | 無し |
| LLM（小） | `ggml-org/gpt-oss-20b-GGUF` | `gpt-oss-20b-MXFP4.gguf` | 11.3 GiB | 無し |
| LLM（小） | `bartowski/Meta-Llama-3.1-8B-Instruct-GGUF` | `…-Q4_K_M.gguf` | 4.6 GiB | 無し |
| 画像 | `stabilityai/stable-diffusion-xl-base-1.0` | `sd_xl_base_1.0.safetensors` | **6.5 GiB** | 無し |
| 画像（小） | `stable-diffusion-v1-5/stable-diffusion-v1-5` | `v1-5-pruned-emaonly.safetensors` | 4.0 GiB | 無し |
| 画像 | `black-forest-labs/FLUX.1-schnell` | `flux1-schnell.safetensors` | 22.2 GiB | **auto**（同意＋トークン） |
| 画像 | `stabilityai/stable-diffusion-3.5-medium` | `sd3.5_medium.safetensors` | 4.8 GiB | **auto** |

**エンジンの面（各 README・2026-09-06）**

- `llama-server`: `-hf <repo>[:quant]` で HF から直接取得（キャッシュは `LLAMA_CACHE`、
  ゲートは `HF_TOKEN`）。`--api-key`。**router モード**（`--models-dir`、要求に応じて
  ロード、`--models-max` で同時数）。エンドポイントは `/v1/chat/completions`・
  `/v1/responses`・**`/v1/messages`（Anthropic 互換。2026-01 に merge）**・`/health`。
- `sd-server`（stable-diffusion.cpp）: **OpenAI 互換の `/v1/images/generations` と
  `/v1/images/edits`（`mask` あり）**、A1111 互換の `/sdapi/v1/txt2img` / `img2img`、
  ネイティブの非同期 job API（`/sdcpp/v1/img_gen` → `/sdcpp/v1/jobs/{id}`）。SD1.x/SDXL/SD3/
  FLUX を **safetensors のまま**読み、`--offload-to-cpu` で VRAM を節約する。モデルは起動時の
  フラグで決め、README に実行時切替の記述は無い。
- ComfyUI: `/prompt`（ワークフロー JSON）・`/history/{id}`・`/view`・WebSocket `/ws`。
  `--listen` で外に出す。認証は無い。モデル置き場は
  `models/{checkpoints,diffusion_models,text_encoders,vae,loras}`。

**opencode（このコンテナ・1.18.29 で実測）**: `opencode.json` に
`provider.llamacpp = { npm: "@ai-sdk/openai-compatible", options.baseURL, models }` を
書いて `OPENCODE_CONFIG` で指すと、`opencode models` が **`llamacpp/qwen3-coder-30b-a3b`
を一覧に出した**。Agent の `models.go` は同じコマンドで一覧を作るので、**起動メニューに
出すまでにバックエンドの改修は要らない**。鍵は `apiKey: "{env:…}"` で環境変数から渡せる。

### いまのコードが言っていること

- **Workspace の外向きは CP の egress proxy を通る**（`main.go`: `http_proxy` を注入し、
  `no_proxy` は loopback だけ）。VPC 内の名前へ直接繋いでも proxy を経由し、enforce では
  allowlist に無ければ落ちる。Workspace が CP に届く経路は既にある——`AF_CP_BASE_URL` と
  git 資格情報ヘルパー（`cred_helper.go`）、`/mcp`。
- Workspace タスクは Fargate でも EC2 プールでも awsvpc＋`WsSg`（`AF_ECS_SECURITY_GROUP`）。
- CP には **WebSocket を通す `httputil.ReverseProxy` の前例**がある（`preview_host_serve.go`）。
- `tts_ecs.go` の `state()` / `setEnabled()`（desired 0↔1）と 0070 の設計（需要時計・純関数・
  失敗クールダウン・`DescribeServices` キャッシュ）は、エンジンが 1 つであることに依存して
  いない。
- MCP ツール呼び出しの上限は kind ごとに違う（0069 決着 1）: claude は事実上無限、codex は
  `tool_timeout_sec`（af には 600 秒を書く）、opencode は進捗通知で時計が戻る。
- `30-ingress.yaml` は **41,988 バイト**。上限 51,200 まで **9.2 KB**。

## 決定

1. **GPU は ECS Managed Instances で買う。自前の EC2 は建てない。スロットプールには置かない。**
   Fargate に GPU が無い以上、0070 の決定 1 は「**タスクが無ければインスタンスが存在しない
   置き方**」と読み替える。MI はそれを AWS が保証する（タスク 0 → インスタンス終了）。
   自前 EC2 の stop/start は却下（後述）: AMI と NVIDIA ドライバの所有、停止中の EBS 課金、
   そして ADR 0045 の走査（`af-role=slot`）と `Ec2MaxSlots` の外に置く配慮が要る。
   MI の capacity provider をクラスタに**足すだけ**にし、**`defaultCapacityProviderStrategy` は
   決して設定しない**——設定した瞬間、`LaunchType` を書き忘れたサービスが GPU 箱に乗る
   （0070 決定 1 の「1 行の欠落で破れる」の裏返し）。既存のスロットプールは `LaunchType: EC2`
   のままで影響を受けない。ADR 0045 決定 6 は Workspace について MI を退けた（ライフサイクルの
   所有者が ECS 側にあり stop が無く、ボリュームはサイズしか指定できない）が、**エンジンは失って
   困る状態を持たない**ので同じ性質がここでは利点になる——矛盾ではなく対象の違いである。
   ただしスロットプールの container instance 走査（`registeredSlots`・`sweepGhostInstances`）は
   クラスタの箱を条件なしに全部歩くので MI の箱も見る。P0 は `DescribeContainerInstances` の
   `capacityProviderName` で MI の箱を除外し、テストでそれを固定する（レビュー R7）。

2. **エンジン役は 2 つ（`llm`・`image`）。役ごとに capacity provider を分け、同じ箱に載せない。**
   `llm` は VRAM ≥ 20 GB（Qwen3-Coder-30B-A3B Q4 が 17.3 GiB＋KV キャッシュ）、`image` は
   VRAM ≥ 8 GB（SDXL fp16 が実測 7.4 GB。g6f.2xlarge の **5,722 MiB** には fp16 を offload 無しで
   載せる形では入らない——`--offload-to-cpu` と量子化は未測で P4）。既定はどちらも
   **g6.xlarge（L4 24 GB・$1.26/h 込み）**で、`image` を安くする下限は測ってから決める。**インスタンス要件は
   役ごとのスタックパラメータ**（宣言する。導出しない。ADR 0053 の形）。同じ箱に 2 役を
   載せない理由: CUDA の VRAM 不足は遅くなるのではなく **落ちる**。実測でも 30B Q4 が
   **20.9 GB**、SDXL が **7.4 GB** で、L4 の 23 GB に両方は入らない。片方が起きているだけの
   時間が大半なので、分けても費用はほぼ増えない。**8 vCPU で 2 台は建つ。ただし落とした直後の
   7〜8 分は前の箱が 4 vCPU を握る**ので、片役の建て直しがもう片役の起動と重なると
   `VcpuLimitExceeded` になる（実測で解けた点 6 とレビュー R2）。両役を同時に使う配備は
   ドレイン 1 台ぶんの余白として 16 vCPU を申請する。
   ComfyUI は `image` 役の**もう 1 つの
   エンジン**であり（決定 6）、sd-server と同時には動かさない（同じ VRAM を取り合う）。

3. **モデルは HF → S3 に一度だけ写し、起動のたびに S3 → ローカルディスクへ引く。HF を起動経路に
   置かない。** 実測がこの決定の理由を費用から**速度と予測不能さ**へ移した（実測で解けた点 5）:
   HF は同じ NAT の先で **4〜236 MB/s**、**同じファイルでも 6 倍違った**（SDXL: MI の箱の curl
   236 MB/s・Fargate の curl 39.6 MB/s。GGUF: `-hf` 9.6 MB/s・Fargate の curl 4.2 MB/s）。ファイル・
   経路・時刻のどれで決まるかは 4 点では言えず、言えるのは**引くまで分からない**ことだけで
   ある。起動経路に置ける速度ではない。S3 は 3 回とも **104〜147 MB/s**（実測で解けた点 8）
   だった。費用（17 GB あたり NAT $1.05）とゲート用トークンをノードに置く話は、その上に
   乗るもう 2 つの理由にすぎない。S3 は保存が $0.025/GB-月
   （100 GB のカタログで $2.5/月）で、ゲートウェイエンドポイント経由の読みは無料。
   **取り込みは非同期ジョブ**（Fargate の CPU タスク: `huggingface-cli download` →
   `aws s3 cp`）で、管理者が HF の repo id とファイルを指定して起動し、HF の LFS メタデータの
   sha256 と**照合してから**成功にする——「ファイルが増えた＝成功」を信じない。`HF_TOKEN`
   は**運用者の秘密**で、取り込みタスクだけが読む（Secrets Manager 参照）。エンジンは
   起動時に `aws s3 sync` で役のプレフィックスを MI の EBS（`storageConfiguration`、サイズは
   パラメータ）へ落とし、それが済んでから `/health` が ok になる。EFS は却下（後述）。

4. **Workspace はエンジンに直接届かない。CP のゲートウェイを通る。** CP に `/engine/llm/v1/*`
   と `/engine/image/v1/*` を生やし、`/mcp` と `/git/*` が使っている Workspace 発の資格情報で
   認証する（テナントの接続元 IP 規則の対象外という扱いも同じ）。エンジンのセキュリティ
   グループは **CP の SG からだけ**ingress を許す（0070 決定 14）。理由が 4 つ:
   (a) `no_proxy` を触らず、allowlist も増えない——Workspace が CP に届く経路は今日ある。
   (b) **起こして待つ**（決定 5）は要求を握る場所が要り、それは CP しかない。
   (c) 誰がどれだけ使ったかを応答の `usage` から CP が数えられる（決定 9）。
   (d) llama-server も sd-server も ComfyUI も**無認証で変更系を持つ**。到達性が防御の全部である
   ところに、Workspace を並べない。`llama-server --api-key` は CP だけが知る鍵で二重にする。
   opencode の provider は `baseURL = $AF_CP_BASE_URL/engine/llm/v1`、`apiKey = {env:…}` で、
   Agent が起動時に env を注入する（`auth.go` の `env()` と同じ場所）。

5. **需要は要求そのものであり、最初の要求はエンジンが起きるまで CP が握る（起こして待つ）。**
   0070 と違って**代読する Polly が無い**ので、意図と結果を分ける必要が無い——要求が来た
   ことが意図である。desired 0 のエンジンへの要求は、CP が desired を 1 にし、`/health` が
   ok になるまで**接続を保持して**から転送する。上限 `AF_ENGINE_WAKE_TIMEOUT`（既定 **900 秒**。実測 527 秒に対して 600 では余白が 73 秒しか無く、GHCR からの pull 178 秒がそのまま入る P0 の配備では足りない——ECR 複製後に測り直す）を
   超えたら 503＋`Retry-After`＋人が読める本文（モデルにも見える）。画像の MCP ツールは
   その間 10 秒ごとに `notifications/progress` を出す（opencode の 60 秒を無効化する既存の
   手）。**opencode は「本文のバイトが 300 秒来ない」と切って送り直す**（実測で解けた点 1、
   レビュー R1）。経過時間の壁ではない。したがってゲートウェイは、ストリーミング要求には
   **即座に 200 と `text/event-stream` のヘッダを返し、上流の最初のバイトが届くまで 10 秒ごとに
   SSE のコメント行を流す**。エンジンが答え始めたらそのストリームに繋ぐ。同じ心拍を prefill の
   無音にも当てる（上流のヘッダを待つ間も流す）。**300 秒は試行の上限ではなく、心拍の間隔の
   上限である。** 503＋`Retry-After` は非ストリーミング要求と、起動が本当に失敗したときの経路で
   あって通常経路ではない——503 はクライアントの有限な再試行を 1 つ消費するので、通常経路に
   置かない（再送の回数を決めているのは opencode／AI SDK 側で上限は不明）。初稿の「290 秒で
   503・再送に乗せる」は、この機構を壁と読んだことから出た設計で、再送が有限であることに
   賭けていた。CPU を却下する理由は「壁」ではなく prefill の 17 分そのものである。「1 要求で 30 分の窓を買う」のは、その provider／モデルを選んだ本人の
   opt-in であり、0070 の損益分岐に相当するものは無い（代替は「使えない」だけ）。

   🔴 **「CP が握る」は非ストリーミング要求では 60 秒しか成立しない（P1 の実測 9・2026-09-07）。**
   握れる長さを決めているのは CP ではなく**前段**である——30-ingress の ALB は
   `idle_timeout.timeout_seconds: "60"` で、応答のバイトが 60 秒来ない接続を閉じる。
   ストリーミング経路が 900 秒握れていたのは 10 秒ごとの心拍が**同時に ALB のアイドルも
   無効化していた**からで、opencode の 300 秒のために書いた機構の副産物だった。画像は JSON
   応答で心拍を差し込む先が無いので、**冷えたエンジンへの初回は必ず 60 秒で死ぬ**（実測: CP が
   `503 59.998s`、エンジン自身は 165 秒で ready）。したがってこの決定は役ごとに分かれる:
   - **ストリーミング**（`chat`）は本文のとおり `AF_ENGINE_WAKE_TIMEOUT` まで握る。
   - **非ストリーミング**（`images`）は前段のアイドルより**手前で畳んで**
     `503 engine_waking`＋`Retry-After` を返す（`AF_ENGINE_PLAIN_HOLD`、既定 **45 秒**。
     `AF_ENGINE_WAKE_TIMEOUT` を上回らない）。握り続けるのは呼び出し側で、provider `sdcpp` が
     自分の 16 分の予算の中で聞き直す。利用者から見た「1 回の呼び出しで絵が返る」は変わらない
     ——MCP の進捗通知がクライアントの時計を生かすので、再試行はツールの上には現れない。
   503 の**コード**が retry するかどうかを決める（`engine_waking` は再試行、`engine_off` と
   `engine_unavailable` は拒否）。番号だけ見て再試行すると、切られたエンジンへの明確な拒否が
   16 分の沈黙に化ける。502／504 も再試行する: **前段が代わりに答えた形**であり、
   `engine_waking` を知らない古い CP もここからはそう見える。
   ALB の `idle_timeout` を伸ばす手は採らなかった: 全経路に効くうえ、AF が前段を持たない
   配備（CloudFront・利用者自前の nginx）では同じ穴が再発し、**その値を CP は問い合わせられない**。
   前段が何であれ壊れない側に機構を置く。

6. **エンジンは llama.cpp・stable-diffusion.cpp・ComfyUI の 3 つで、前 2 つは公式イメージを
   ECR に複製し、ComfyUI は自前で焼く。** 選定の根拠:
   - `llama-server`: 公式 CUDA イメージ、OpenAI 互換に加えて `/v1/responses` と
     `/v1/messages` があり、**codex と claude への道が閉じていない**（P3）。router モードで
     複数モデルを要求ごとにロードできる。
   - `sd-server`: 公式 CUDA イメージが 2.3 GB で、OpenAI 互換の `generations` / `edits`
     （mask）が **0069 の `Op` のうち generate / edit / inpaint に一対一で写る**。safetensors を
     変換なしで読むので、S3 のカタログは ComfyUI と共用できる。
   - ComfyUI: 公式イメージが無いので、**ComfyUI のタグを固定した Dockerfile** を
     `workspace/` と同じ流儀で持つ（コミュニティイメージは 4〜11 GB で、何が入っているかを
     フリートが保証できない）。**ComfyUI-Manager は入れない**——任意のカスタムノード＝任意の
     コードをフリートの箱で実行する入口になる。画面は CP のリバースプロキシ
     （`/engine/comfy/`、WebSocket 込み。`preview_host_serve.go` の型）で Console のペインに
     出す。`generate_image` の provider `comfy` は、フリートが持つワークフロー JSON
     テンプレートに prompt / size / seed を差して `/prompt` に投げ、`/history` を待つ。
   - 3 つは同じ**モデル置き場の規約**（ComfyUI の `models/…` 配置を正とする）を読む。
     sd-server のフラグはその中のファイルを指す。

7. **コントローラは `tts_control.go`（0070 P1: `ttsDemand` / `decideEngineAction` /
   `ttsController`）を N エンジンに一般化して 1 本にし、VOICEVOX もその 1 つにする。**
   `decideEngineAction` は純関数のまま、エンジンの表（サービス名・URL・ヘルスの
   URL・アイドル窓・起動期限・クールダウン）を取る。需要時計は `SettingsStore` に永続化
   （1 分に 1 回）、`DescribeServices` に短い TTL キャッシュ、起動失敗は `events[]` で診断して
   クールダウン、`starting` にもアイドル窓を適用（0070 決定 5・6・9・10）。**MI 固有の
   追加が 1 つ**: desired を 0 にしてもインスタンスは MI のスケールインまで残り、その間も
   管理料と EC2 料金は走る。0070 決定 7 の状態集合（`stopping` の取り消し窓と `tts_ecs.go` の
   `none` を含む）に `draining` を**足す**。起動期限はエンジンごとに持ち、**実測のコールド
   スタート以上**にする——0070 の既定 300 秒のままなら GPU の起動はすべて「失敗」になり、
   クールダウンが倍々に伸びる。`draining` は CPU 箱で **93 秒**、**GPU 箱では 427 秒と 463 秒**
   （実測で解けた点 4）——ただしこれは MI の `infrastructureOptimization.scaleInAfter`（null＝
   既定・−1＝片づけない・0〜3,600 秒）を既定のまま測った値で、MI 固有の固定費と決めるのは早い。
   P0 で 0 と −1 を 1 回ずつ測り、−1 は「箱を残す」設計の材料にする: desired 0 のあと箱が残って
   いる間に来た要求は、イメージもモデルファイルも箱にあるので S3 取得なしで起きるはずである。
   モードは 0070 決定 7 のとおり `off / on / ondemand` を
   エンジンごとに。

8. **スタックは `60-engines.yaml`（任意採用）。`30-ingress` へ渡すのは SSM パラメータ名 1 つ。**
   `50-tts` の型で `00-network` と `20-platform` を import し、`30-ingress` には依存しない。
   中身: capacity provider ×2（属性ベースの選択）、MI 用の infrastructure role とインスタンス
   プロファイル、S3 バケット（モデル）、取り込みタスク定義、エンジンのタスク定義 ×3 と
   サービス ×3（`DesiredCount` は**書かない**。0070 実測 2）、Cloud Map の A レコード ×3、
   SG、ログ。**`30-ingress` にエンジンごとの 2 パラメータを足す余裕は無い**（残り 9.2 KB に
   6 パラメータと条件と env で 3 KB 弱）。代わりに `60-engines` がサービス名と URL を
   まとめた JSON を SSM に書き、`30-ingress` は `AF_ENGINES_SSM_PARAM` 1 本を渡す。CP は
   起動時に読む（CP タスクロールの SSM 読みは **`/af-ws/*` の下に限られる**ので、名前を
   `/af-ws/engines` の形にするか `20-platform` のポリシーを広げるかを P0 で決める）。**CP 側の
   IAM 追加はここでもゼロ**（UpdateService / DescribeServices / DescribeInstances / container
   instance の読みはすべて既存・無条件）で、**新しい IAM は `60-engines` の中で閉じる 3 ロール**
   ——MI の infrastructure role、インスタンスプロファイル、そしてタスクロール（エンジンは S3
   読み、取り込みは S3 書きと秘密の読みで分ける。ハーネスは 1 つで共有しているが本番は分け、
   `ssmmessages:*` は入れない）。実測で分かった契約が 3 つ: capacity provider は**クラスタ固有**で
   `ClusterName` が必須（無いと「The cluster provided is invalid」）、AWS 管理ポリシー
   `AmazonECSInfrastructureRolePolicyForManagedInstances` の `iam:PassRole` は
   **`ecsInstanceRole*` という名前のロールにしか効かない**（フリート名のロールを使うなら
   infrastructure role に PassRole を明示する）、`ClusterCapacityProviderAssociations` は
   **クラスタのリストを置き換え**、`DefaultCapacityProviderStrategy` は必須プロパティ
   （FARGATE / FARGATE_SPOT を含む完全なリストと**空の戦略**を渡す）。0070 の「IAM 追加ゼロ」は
   ここでは成り立たない。

9. **使用量は数え、費用は共有として出す。** llm はゲートウェイが応答の `usage` から
   トークンを、image は枚数とピクセルを、メンバー単位で記録する（`usagex` に
   `engine.llm` / `tool.imagegen` の provider 別。ADR 0029 の列挙に追記）。単価は付けない
   （ここでの 1 トークンにドル建ての値は無い）。インスタンス時間は `af-role=engine-llm` /
   `engine-image` の費用配分タグで**コンポーネント費用として表示し、按分しない**
   （ADR 0048）。

10. **由来は結果の一部（0069 決定 11）。** provider は `llamacpp` / `sdcpp` / `comfy`、モデルは
    **ファイル名と sha256**（HF の repo id とリビジョンも取り込みジョブが残す）。プロンプトは
    コンテナの外へ出るが、宛先はフリートの箱である——それも書く。

11. **ライセンスはカタログの属性であり、取り込み時に運用者が受け入れる。** SDXL は
    CreativeML OpenRAIL++-M、Llama は Llama ライセンス、FLUX.1-schnell は Apache-2.0 だが
    FLUX.1-dev は非商用、Qwen は Apache-2.0。取り込みジョブは HF の `cardData.license` を
    カタログに写し、**ゲート付き（`gated: auto`）は運用者が HF で同意した後**でしか通らない
    ——ジョブはそれを 401/403 で知り、その文言で失敗を名乗る。0070 未解決 1 と同じ性質の
    問題で、マルチテナント配備では「運用者＝使う人」でない。

12. **x86_64 だけ。** `stable-diffusion.cpp` の CUDA イメージは amd64 のみで、G 系に arm64 は
    無い。0070 決定 11 の「arm64 は測ってから」に相当する分岐がそもそも無い。

13. **現況は「分からないことを書かない」形で出し、稼働実績はコントローラのティックを
    サンプラにして新しい表に貯める。** super_admin の「推論エンジン」に、いつ起動したか・
    いつ自動停止するか・直近の使用状況・動いているモデル・稼働実績のヒートマップを出す
    （P1.5）。トグルだけの画面では「切っていいのか」が判断できない——$1.26/時の箱は普段
    寝ているので、**押す前に見えるのは全部この画面が言うことだけ**である。設計のほとんどは
    「出さない」側の規則になった:
    - **起動時刻は箱の `registeredAt`**（ECS の `describe-container-instances`）を第一とし、
      無いときだけサービスの `lastStart` に落ちる。両者は**別の事実**で、`lastStart` は
      スタック更新やタスクの差し替えでも動く——箱を買い直していなくても。
      🔴 `ec2 describe-instances` は使わない（P1 の実測 2: **MI の箱は一覧に出ない**ので、
      EC2 側で実装したら「動いている GPU について箱は無い」と答える）。
    - **自動停止の時刻は、答えが無いときは出さない。** `mode=on` のエンジンは止まらないので
      出さない（出せば「そのうち安くなる」という約束になる）。`off`・停止中・需要マークが
      まだ無いときも同じで、`decideEngineAction` がその回に何も判断しないのと同じ理由である。
      しきい値は `engineIdleWindow()` に 1 本化して、パネルの秒読みとコントローラが同じ瞬間を
      指すようにした——別々に持てば、起動デッドラインへのクランプを片方が忘れた日にずれる。
    - 🔴 **直近の要求数は CP のプロセス内メモリにしか無い。** 入れ替われば 0 に戻る
      （永続化されているのは `lastAt` だけ。決定 6）。だから窓ぶん数え切っていないときは
      `window_counted_secs` でそう言い、UI が「この CP が数えているのは直近 N 分だけ」と
      添える。**数え直せない過去を 0 と書かない**——この画面で自信を持って間違えられる数字は
      これ 1 つである。永続化されている「最後の要求」を隣に置くと、0 が読めるようになる。
    - **モデルはスタックの宣言**（ADR 0053）。エンジンには聞かない——寝ているので、まさに
      人が見に来た瞬間に答えられない。読み込み済かどうかはコントローラが既に持っている
      `warmed()` で言う（ECS の RUNNING は「ポートが開いた」であって、llama-server は重みが
      VRAM に載る 267 秒前にそれを満たす）。
    - **稼働実績は新しい表 `engine_hourly` に、コントローラのティックが書く。** それまで
      エンジンの稼働時間はどこにも記録されていなかった——コントローラは 30 秒ごとに
      サービスを読み、見たものを捨てていた。CP の中でこれだけの頻度で見ているものは他に無く、
      AWS への呼び出しは既に払っているので、記録は INSERT 1 回で済む。
      - セルは **3 状態**で、**行が無い時間は「未観測」＝空白**。`usage_hourly` と違って
        心拍の行を分けないのは、コントローラが見るのは 1 エンジンだけで「半分だけ観測した」が
        ありえないからである（ワークスペースの掃引は全テナントを歩くのでありうる）。
      - ⚠️ **分母 `observed_secs` は保存する。** ティックの間隔は一定ではない（起動中と
        暖機中は 5 秒）ので、`samples × 公称間隔`で復元すると**忙しい時間ほど 100% を
        超える**——人が見に来るのはまさにその時間である。
      - ⚠️ **プロセス最初のティックは何も書かない**し、1 ティックは高々 1 間隔しか主張
        しない。CP が 1 時間落ちて戻ってくると、経過時間は大きく状態は正しく見えるので、
        その穴を「いま見えている状態」で塗ることになる。代償は CP 起動ごとに 30 秒の
        取りこぼしで、対価は**空白が空白のまま残る**ことである。
      - ⚠️ **`running` / `starting` / `draining` を別の列で持つ。** 後ろ 2 つは課金されるが
        何も答えていない（実測: 起動 165〜197 秒、ドレイン 427〜477 秒）。足せば「動いて
        いた」と言うことになり、落とせば使った金が消える。ヒートマップはどちらの読み方も
        できる（「応答できた時間」と「箱があった時間」）。
    - **金額は描かない**（ADR 0048 決定 2）。時間別の費用は誰かが打ち込んだ単価×秒数＝
      見積りにしかならない。だから既存も「稼働」ビューであって「費用」ビューではない。

## 実測で解けた点（2026-09-07）

1. **opencode は 300.1 秒で切り、数秒後に送り直す。** このコンテナの opencode 1.18.29 に、
   応答を N 秒握るだけの OpenAI 互換スタブを `@ai-sdk/openai-compatible` provider として
   繋いだ。150 秒の保持は成功（要求 2 本——タイトル生成用の tools=0 と本体の tools=21——
   とも答えが届き、exit 0）。400 秒と 700 秒の保持は **300.1 秒で Connection reset**、その
   3〜5 秒後に**同じ要求（tools=21）を再送**し、これを 1200 秒の打ち切りまで**4 回**繰り返した。
   帰結は決定 5 に書いた: 試行ごとに 290 秒で 503、起動は試行と切り離す、最初のトークンまで
   300 秒。🔴 **レビュー R1 で前提が覆った**: 切られるのは「本文のバイトが 300 秒来ない」ときで
   あって経過時間ではない。ヘッダだけ先に返しても 306.9 秒で再送されるが、ヘッダ＋10 秒ごとの
   SSE コメント行を流せば **400 秒握っても再送なしで答えが届く**。再送の回数は上限不明
   （1,900 秒で 6 試行・指数バックオフ）。起草側も同じスタブで再現した: 200＋SSE ヘッダを
   即返し 10 秒ごとに `: keepalive` を流して 400 秒握ると、**要求 2 本（tools=0・tools=21）とも
   再送なしに 405 秒で答えが届き exit 0**。決定 5 は心拍で握る形に差し替えた。
2. **Managed Instances のコールドスタートは CPU 箱で 109 秒、ドレインは 93 秒。**
   検証アカウント（`af-sandbox`）の共有クラスタに使い捨てスタック
   （`deploy/aws/ecs/harness/engprobe.yaml`、手順は `probe-managed-instances.sh`）を建て、
   c6a.large（AMI `ecs-managed-instances-standard-x86_64-20260827`）で llama.cpp の CPU
   イメージ（297 MB）に 1.1 GB のモデルを HF から取らせた。desired 1 から **+6 秒でタスク、
   +10 秒でインスタンス起動、+32 秒で pull 開始、+68 秒で RUNNING（pull 35 秒）、+109 秒で
   モデルロード完了・listen**。desired 0 から **+10 秒でタスク消滅、+79 秒で shutting-down、
   +93 秒で terminated**。AWS の参照構成の 13 分は、アラームの 2 分と 14 GB のイメージが
   作った数字である。GPU 箱（2.3 GB の CUDA イメージ、17 GB のモデル）は未実測——3 を参照。
3. **G 系の vCPU クォータは検証アカウントで 0 だった。** 増加申請（8 vCPU）は自動承認され
   ず**サポートケース**になった（`CASE_OPENED`）。standup の前提条件として README に書き、
   0 なら standup がそう言う。GPU 上の数値（prefill、pull、S3 → ローカル、VRAM へのロード）は
   承認後に同じハーネスで取る。

4. **GPU（g6.xlarge・L4 24 GB）でのコールドスタートとドレイン。** AMI は
   `ecs-managed-instances-nvidia-x86_64-20260827`（NVIDIA ドライバ入り。何も焼かなくてよい）。
   desired 1 から **+15 秒でインスタンス、+43 秒で pull 開始、+214 秒で pull 完了（`server-cuda`
   2.47 GB に 171 秒）、+232 秒でタスク RUNNING**。ここまでは CPU 箱と同じ形である。その後が
   問題で、**モデルの取得に 1,846 秒、VRAM へのロードに 272 秒、合計 2,351 秒（39 分）**
   かかった——取得を S3 に替える理由が実測で解けた点 5 である。**ドレインは 427 秒と 463 秒**
   （CPU 箱の 93 秒に対して 4〜5 倍）。desired 0 のあとも 7〜8 分は g6.xlarge の料金と管理料が
   走るので、**アイドル窓を切り詰めても回収できるのはその分だけ**である（窓 1 回 $0.65 に対し
   ドレインは $0.15）。sd-server 側は同じ箱で **タスク作成から listen まで 195 秒**（pull 135
   秒＋6.9 GB の取得 28 秒）。
5. 🔴 **HF からの取得速度はファイルで 50 倍違い、Qwen3-Coder の GGUF は何で引いても遅い。**
   同じ NAT の先で、`llama-server -hf` は 18.5 GB を **9.6 MB/s（1,846 秒）**、SDXL 6.9 GB は
   `curl` で **236 MB/s（28 秒）**だった。この 2 つを見て一度「24 倍の差はダウンローダにある」と
   書いたが、**同じ GGUF を Fargate の `curl` で引くと 4.2 MB/s（4,467 秒）**で、`-hf` より
   さらに遅かった。遅いのはクライアントではなく **HF がそのファイル（unsloth のリポジトリ）を
   配る速度**である。stabilityai の SDXL が速かったのは、そのファイルがそう配られているからに
   すぎない。決定 3 は元々「毎回 $1 と 9 分を払うな」という費用の話だったが、実際には
   **起動が 39〜74 分になるか 4 分で済むかが、引くまで分からない**という話だった。S3 からの
   `sync` に置き換える設計は変わらないが、理由が変わる。誤った版を残すのは、同じ 2 点から
   同じ結論を引きたくなる次の人のためである。🔴 **レビュー R6: この訂正後の版もまた 2 点からの
   一般化だった**——同じ SDXL を Fargate の取り込み curl は **39.6 MB/s** で引いている（MI の箱の
   curl は 236 MB/s）。「unsloth のリポジトリが遅い」「ファイルで決まる」とは言えず、4 点から
   言えるのは「4〜236 MB/s で、引くまで分からない」までである。決定 3 はそう書き直した。
6. **VRAM と G 系クォータ。** Qwen3-Coder-30B-A3B Q4_K_M は **20,943 MiB**、SDXL fp16 は
   **7,379 MiB**（params 6,624 MB）を使った。したがって **L4 1 枚に 2 役は入らず**、`image` を
   g6f.2xlarge（6 GB）へ落とす案は成立しない（未解決 2 の答えの半分）。さらに **8 vCPU の
   クォータでは g6.xlarge が 1 台しか建たない**: llm を起こしたまま image を desired 1 にすると
   `VcpuLimitExceeded: your current vCPU limit of 8` を繰り返し、1 台目が terminate してから
   **408 秒後**に placement した。両役を同時に起こす配備は 16 vCPU を申請する。🔴 **レビュー R2 で
   「1 台しか建たない」は覆った**: CloudTrail を並べ直すと、`VcpuLimitExceeded` の 3 回
   （07:19〜07:20）は直前に MI が片づけた箱（07:17:13 終了）がまだ 4 vCPU を数えられていた間で、
   llm の箱が shutting-down のうちに image の箱が 07:26:04 に起動している。**8 vCPU で 2 台は
   建つ。** 16 vCPU の理由は「ドレイン 1 台ぶんの余白」に変わり、決定 2 をそう直した。

7. **ComfyUI は同じ L4 で SDXL を 8 秒で描き、VRAM は sd-server と同じ 6.9 GB。** コミュニティ
   イメージ `lecode-official/comfyui-docker:latest`（5.1 GB・ComfyUI 0.8.2）を計測用に使い、
   S3 から引いた SDXL を `models/checkpoints` に置いて `/prompt` → `/history` → `/view` を回した。
   desired 1 から **+504 秒で listen**（うち **5.1 GB の pull に 437 秒＝12 MB/s**。GHCR→NAT は
   `server-cuda` の 171 秒でも 14 MB/s で、**イメージは ECR に複製しないとコールドスタートの
   大半が pull になる**）。最初のワークフローは 42.6 秒（モデルのロード込み）、**温まった後は
   1024px・20 steps が 7.9 秒と 8.3 秒**——sd-server の 20.8 秒の **2.5 倍速い**。VRAM は
   6.9 GB。絵は目で見て正しい。同梱の ComfyUI-Manager は import に失敗していた（決定 6 で
   入れないものなので支障なし）。決定 6 の順序（P1 sd-server → P2 ComfyUI）を実測で読み直すと、
   sd-server の利点は**面とコードの小ささ**（OpenAI 互換が 0069 の `Op` に一対一で写り、
   ワークフローテンプレート・WebSocket のペイン・自前イメージのどれも要らない。5.1 GB は
   計測用のコミュニティイメージの大きさで、フリートが焼く ComfyUI の大きさは未測）、ComfyUI の
   利点は**1 枚あたりの速度（ステップ数とサンプラーが同条件かは未確認）とワークフローの
   自由度**で、費用は窓で決まる（決定 5）ので 1 枚 13 秒の差は請求書には出ない。順序は据え置くが、絵を大量に作る配備では ComfyUI が本命になりうる。
8. **S3 → ローカルは 104〜147 MB/s で一定（`aws s3 cp` の既定並列）。** 6.9 GB を 45 秒、
   合成した 20.8 GB を 198 秒、本物の GGUF 18.5 GB を 179 秒。HF 直の速い側（236 MB/s）より
   遅いが NAT を通らず、遅い側（4〜10 MB/s）の 10〜25 倍で、**ファイルによらない**。
9. **S3 から起動する llm の本番相当コールドスタートは 527 秒（8.8 分）。** desired 1 から
   **+8 秒でインスタンス、+64 秒で S3 取得開始、+224 秒で pull 完了（178 秒）、+243 秒で
   18.5 GB 取得完了（179 秒）——取得とイメージの pull は並行して走る——+260 秒で llama-server
   起動、+527 秒でロード完了・listen**（VRAM へのロード 267 秒）。HF 直の 2,351 秒の 4.5 分の 1。
   内訳を見ると残る大物は **pull 178 秒（ECR への複製で縮む）** と **ロード 267 秒（EBS gp3 の
   読み出し。ローカル NVMe か EBS のスループット指定で縮む可能性）** で、S3 取得はもう支配的
   ではない。決定 5 への帰結: llm の初回要求は 300 秒の壁を **1 回は必ず越える**（opencode が
   1〜2 回送り直す間に温まる）。image 役は sd-server なら 195 秒で 1 回目の試行に収まる。

その他、同日に測ったこと:

- **HF の API は照合に要るものを返す。** `?blobs=true` の `siblings[].lfs.sha256` と
  `cardData.license`（SDXL は `openrail++`、FLUX.1-schnell は `apache-2.0` で `gated: auto`）。
  1.1 GB の GGUF を落として `sha256sum` を取ると API の値と一致した——決定 3 の照合は
  この 2 つの値で書ける。
- **llama-server（CPU・1.1 GB モデル）**: 起動から `/health` ok まで 3 秒、`/v1/messages`
  が 200、`--api-key` 無しの要求は 401。opencode から `apiKey: "{env:AF_ENGINE_TOKEN}"` で
  鍵を渡し、streaming で応答が届くところまで通った。**ただし opencode の要求は、フリートの
  AGENTS.md を含めると 18.7k トークン、最小構成でも 7.3k トークン**あり、このコンテナ
  （8 vCPU・共有）の prefill は 1.5B Q4 で **18〜23 tok/s**＝18.7k トークンに 17 分。
  0.5B Q8 で最小構成にすると 170 秒で完走したが、モデルはツール呼び出しを JSON の**文章**
  として出した（経路ではなくモデルの限界）。**GPU の本命モデルでは成立した**——下記。
- ✅ **opencode → llama.cpp（L4・Qwen3-Coder-30B-A3B Q4）はツール呼び出しまで完走した。**
  CP ゲートウェイの代わりに SSM のポートフォワードでエンジンへ繋ぎ、`opencode run --auto` に
  「hello.txt を作り、読み返してバイト数を答えよ」と与えた。**11 秒で `write` と `bash` を
  呼び、ファイルが実在し（`hello fleet`・11 バイト）、答えも「11 bytes」だった。**
  性能は **prefill 23,226 トークンを 11.85 秒＝1,960 tok/s**（ピーク 2,523）、
  **最初のトークンまで 12.1 秒**、生成 **67.6 tok/s**。同じ prefill が CPU では 18〜23 tok/s
  だったので **85〜100 倍**であり、opencode の 300 秒の壁に対して 25 倍の余裕がある。
- ✅ **sd-server（L4・SDXL fp16 6.9 GB）**: `/v1/images/generations` が **512px 7.8 秒**、
  **1024px 20.8 秒と 21.0 秒**（既定ステップ）、`/v1/images/edits`（画像＋mask）が
  **512px 6.4 秒**。1024px の出力は目で見て正しい絵だった（CPU の 4 ステップはノイズだった）。
  CPU 比で 1024px は **28 倍**速い。⚠️ ネイティブの非同期 job API `/sdcpp/v1/img_gen` は
  コンテナ内で **`filesystem error: /proc/1/map_files … Operation not permitted`** を返して
  使えない（OpenAI 互換の面は問題なく動く）。決定 6 が OpenAI 互換面を採る理由がもう 1 つ増えた。
- **sd-server（CPU・SD1.5 Q4_0 1.67 GB）**: `/v1/images/generations` は 256px・4 steps で
  **98 秒**、512px・4 steps で **587 秒**、`/v1/images/edits`（画像＋mask）は 256px で
  **307 秒**（VAE デコードだけで 51 秒）。応答は `data[].b64_json`。CPU で画像は無理という
  前提を、API 契約の確認と一緒に数字で押さえた。

## 未解決の点——P0 を書く前に潰すこと

1. **VRAM へのロード 267 秒を縮められるか。** S3 化で取得は 179 秒になり、残る大物は EBS gp3
   から 18.5 GB を読む時間である。レビューの推奨は **`localStorageConfiguration.useLocalStorage`
   を最初に測る**こと: g6.xlarge の EBS ベースライン 125 MB/s は S3 取得（104〜147 MB/s＝EBS 書きの
   上限に見える）と VRAM ロード（18.5 GB は 125 MB/s でも 148 秒以上）の両方を縛っており、
   インスタンスストア 250 GB に替えれば両方が動く。gp3 のスループット指定は MI には無い
   （`storageConfiguration` は `storageSizeGiB` だけ）。
2. **Workspace 発の資格情報の再利用か、エンジン専用トークンか。** `/git/*` の PAT を
   そのまま使うと、メンバー単位の計上はできるがセッション単位はできない。レビューの推奨は
   **セッション単位の専用トークン**: (1) `usagex` の行はセッションで切られている、(2) `{env:…}` に
   置く値はモデルから見えるので、git と MCP に効く PAT より `engine:llm` にしか効かない短命の値の
   ほうが漏れたときの損が小さい、(3) 0069 決定 3 のレビュー訂正（テナント層は git の bridge を
   借りず自分のエントリを持つ）と同じ形になる。起草側も同じ見立てで、P0 でそう作る。

## 却下した案

- **CPU（Fargate 16 vCPU / c8g.4xlarge）で llama.cpp。** 値段は L4 1 枚と同じ
  （$0.79〜0.99/h 対 $1.26/h）で、MoE（3B active）の生成は使えても、**コーディング
  エージェントの 20k トークンの prefill が CPU では分単位**になる。安くもならず遅い。
  llama.cpp の CPU イメージが 297 MB であることは、この判断を変えない。実測が裏書きする:
  このコンテナで 1.5B の prefill が 18〜23 tok/s なのに対し、**L4 1 枚では 30B が 1,960 tok/s**。
  opencode の要求は 18.7k トークンで、opencode は 300 秒で切る。CPU では最初のトークンが
  間に合わない。
- **自前の EC2（GPU）を CP が stop/start する。** スロットプールが持つ `StartInstances` /
  `StopInstances` の道具は流用できる（復帰 110 秒の実測もある）が、AMI と NVIDIA ドライバを
  フリートが所有し、停止中も EBS（200 GB で $19/月）が走り、ADR 0045 の走査と
  `Ec2MaxSlots` の外へ置く配慮が全経路に要る。しかも g6 のローカル NVMe は停止で消えるので、
  温かいのはイメージ層だけである。MI が東京に無い配備のための**代替案として残す**。
- **エンジンを 1 箱に同居させる。** 費用は最大で $1.26/h 減るが、VRAM の取り合いは
  クラッシュとして現れる（決定 2）。
- **EFS にモデルを置く（既存の 10-data を流用）。** マウントするだけで sync が要らないのは
  魅力だが、標準クラスは $0.36/GB-月（100 GB で $36/月＝S3 の 14 倍）、IA でも読み $0.012/GB で
  17 GB のロードごとに $0.20、そしてスループットは NFS のそれで、17 GB を VRAM へ入れる時間が
  ローカル NVMe より桁で長い。sync が P0 で痛いと分かったら再考する。
- **Workspace からエンジンへ直接繋ぐ（Cloud Map 名＋`WsSg` を ingress に）。** 経路は最短だが、
  `no_proxy` に名前空間を足す変更（`main.go`）、enforce 時の allowlist、無認証エンジンの
  到達性を Workspace に開くこと、起こして待つ場所が無いこと、使用量を数える場所が無いこと
  ——決定 4 の 4 つが全部欠ける。
- **モデルをイメージに焼く（AWS の参照構成が薦める形）。** コールドスタートの分散は減るが、
  モデルを足すたびに 20 GB 超のイメージを焼いて ECR に置き、pull が起動時間の主因になる。
  「HF から取得して動かす」という要求とも噛み合わない。
- **ComfyUI のコミュニティイメージをそのまま使う。** 中身をフリートが保証できず、
  ComfyUI-Manager 同梱のものは任意コード実行の入口になる（決定 6）。
- **`llama-server -hf` で HF から直接。** 起動のたびに NAT を通って $1 と 9 分、ゲート用の
  `HF_TOKEN` がエンジンの箱に載る（決定 3）。
- **Lambda / SageMaker Serverless / Bedrock カスタムモデル。** 17 GB の GGUF と ComfyUI の
  画面は関数ではなく、Bedrock は ADR 0069 の第 1 層として既にある（自前で動かす要求とは
  別物）。

## P0 の実測（2026-09-07）

P0 を書きながら `af-sandbox` の共有クラスタに `60-engines` を実際に建てて測った。フェーズ節が
「P0 で測る」と挙げた 5 点はここで決着し、**そのうち 2 つは本文の予想を外した**——`Host: {}` の
volume は箱を残しても効かず（下の 4）、ECR 複製の効果は placement のばらつきに埋もれた（1）。
実測にかかった費用は g6.xlarge 約 1.4 時間で $2 程度。

1. **本番相当のコールドスタートは 586 秒**（`desired 1` → listen）。イメージは ECR、モデルは
   S3、箱は無し。内訳: **+88 秒で task 作成と placement**、pull **79 秒**（GHCR の 178 秒に対して
   99 秒短い）、S3 取得 **161 秒**（18.5 GB＝115 MB/s）、+350 秒で RUNNING、**+586 秒で
   `model loaded` と listen**（VRAM ロード 236 秒）。🔴 **「ECR 複製後に測り直せば 527 秒より
   縮む」という見込みは外れた**——pull は確かに 99 秒縮んだが、同じ日の別の起動では placement が
   8 秒だったところが今回は 88 秒で、支配的なのは pull ではなく**箱が現れるまでの時間の
   ばらつき**である。決定 5 の `AF_ENGINE_WAKE_TIMEOUT` 既定 900 秒は据え置く（586 秒に対して
   余白 314 秒）。2 点からの一般化を避けて言えるのは「**500〜600 秒台で、下振れも上振れもする**」
   までである。
2. **ドレインは既定の `scaleInAfter` で 456 秒**（shutting-down の開始は +95〜104 秒）。
   0070 のハーネスで測った 427 秒・463 秒と同じ範囲で、3 点目として一致した。
3. **`scaleInAfter: -1` は本当に箱を残す。** `desired 0` から **583 秒後も `running`** のまま
   （既定なら 456 秒で terminated）。🔴 ただし **`scaleInAfter` を後から変えても、既に `-1` の
   下で idle になった箱は回収されない**——`0` に変えて 497 秒待っても `running` のままだった。
   残した箱を片づける手は `ecs update-container-instances-state --status DRAINING` で、これは
   **90 秒で shutting-down** に入った（`ec2 terminate-instances` は MI のリソースベース
   ポリシーが明示的に拒否する）。CFN の `AWS::ECS::CapacityProvider` は
   `InfrastructureOptimization.ScaleInAfter` と
   `InstanceLaunchTemplate.LocalStorageConfiguration.UseLocalStorage` の**両方を持っている**
   （レビュー R5 は API にあるとだけ書いていた）ので、どちらもスタックのパラメータにできた。
4. 🔴 **決定 7(c) の「温かい箱」は P0 では成立させられなかった。未証明のまま残す。** 二段構えで
   外れた:
   - まず、`-1` で残した**同じインスタンスに戻った**再起動が 18.5 GB を S3 から**引き直した**
     （126 秒。再起動そのものは 410 秒で、コールド 586 秒との差 176 秒は placement と pull）。
     原因は `Host: {}`——ECS/Docker は host パラメータが空だと**タスクごとに新しい匿名
     ディレクトリ**を割り当てるので、新しいタスクから見た `/models` は空である。
   - そこで `Host: { SourcePath: /var/lib/af-engine-models }` に直したところ、**新品の 120 GiB の
     箱でも `No space left on device`** でサービスが一度も起動しなくなった。🔴 つまり
     `StorageConfiguration.storageSizeGiB` が決めるのは MI が**付けるデータボリューム**
     （コンテナランタイムが使う側）の大きさで、`/var/lib/…` のような任意の host パスは
     **ルートファイルシステム**に落ちる。両方を測ったうえで `Host: {}` に戻した。
   おまけに、匿名ディレクトリは**片づけられない**。`-1` で残した 1 台の上で 4 回起動したら
   18.5 GB × 4 でディスクが埋まった。したがって P0 の答えは「**`scaleInAfter` は既定のまま**」で、
   温かい箱を成立させるには MI のデータボリュームが実際にマウントされているパスが要る——
   文書化されておらず、GPU 1 時間を追加で払う価値は無いと判断した。P4 の
   `useLocalStorage`（インスタンスストア 250 GB がディスクそのもの）で拾い直す。
5. **プール走査が MI の箱を見ること**は本番のクラスタで現物を確認した。
   `DescribeContainerInstances` は同じクラスタで
   `i-0abeb…/None`・`i-0075e…/None`（スロット、`agentConnected=false`）と
   `i-059d9…/af-af-ecs-engines-llm`（エンジン、`agentConnected=true`）を並べて返す。フィルタが
   無ければ `registeredSlots` はエンジンの箱を「空きスロット」と数え、ドレインに入った時点で
   `sweepGhostInstances` がそれを deregister する。`capacityProviderName` が空かどうかが唯一の
   区別で、P0 はそこで切っている（決定 1・レビュー R7(a)）。
6. **エンジンの面は本番の経路で通った。** CP の SG に置いた使い捨ての Fargate タスクから
   （SSM ポートフォワードは使わない——本番のタスクロールに `ssmmessages:*` を入れないため）:
   `http://llm.af.internal:8080/health` が **200**、`/v1/chat/completions` が
   `--alias` の `qwen3-coder-30b-a3b` として答え、**鍵無しの要求は 401**。SG（CP からのみ）と
   `--api-key`（SSM SecureString）の二重の鍵が両方効いている。
7. 🔴 **`usage` はストリームに黙って現れるわけではない。** 同じエンジンに
   `stream_options.include_usage` を付けると `[DONE]` の直前に
   `{"choices":[],"usage":{...}}` が来るが、**付けなければ usage のチャンクは一切来ない**。
   AI SDK は付けるが、数えるのは CP の仕事（決定 9）なので**ゲートウェイが自分で付ける**
   ことにした。付けなかった場合に 0 を書かず `measured="none"` にするのは、0069 が画像で
   採った「測れないものは 0 と書かない」と同じ扱いである。
8. **CFN の 2 つの契約が P0 で増えた。**
   (a) **`DesiredCount` を書かない効果は実測どおり**——`ScaleInAfter` を変える更新も、
   タスク定義を差し替える更新も、`desiredCount` を 0 のまま残した。
   (b) 🔴 **モデルがまだ無いうちにサービスを作ると、スタックは永久に固まる。** 初回の create で
   実際に踏んだ: 取得サイドカーが `Key … does not exist` で落ち、タスクが 60 秒ごとに
   crash-loop する間スタックは `CREATE_IN_PROGRESS` のまま——20-platform が ECR リポジトリを
   持っている理由とまったく同じ形である。S3 のバケットを作るのも、それを読むサービスを作るのも
   同じスタックなので、**`LlmModelS3Key` が空ならサービスを作らない**という条件にし、
   初回だけ「空で建てる → 取り込む → キーを入れて建て直す」の 2 回に分けた。
9. **standup の秘密の渡し方。** エンジンの `--api-key` は機械が作る値なので standup が
   **無ければ作る**（チェックだけの他の秘密と違う）。最初の実装は
   `ssm put-parameter --value <秘密>` で、自分で足したスタブテストの表明に引っかかった——
   引数は `/proc/<pid>/cmdline` から誰にでも読め、シェルのトレースにも残る。`--cli-input-json
   file://…`（0600・直後に削除）に直した。

10. ✅ **完了の定義の後半——「停止中のエンジンへの最初の要求が 1 回の試行で答えを返す」——を
    実物で通した。** af-sandbox に P0 のコードを配備し（`0.16.1-dev-5b62a9b4`）、GPU の箱が
    **1 台も無い**状態（desired 0・`describe-instances` で 0 台・Cloud Map の名前も未登録）から
    `POST https://<fqdn>/engine/llm/v1/chat/completions`（`stream:true`）を **curl で 1 回だけ**
    投げた:
    - **即座に `HTTP/2 200` と `content-type: text/event-stream`**（要求と同じ秒）。
    - CP が同じ秒に `engine llm: started on demand` を書き、**10 秒ごとに `: af-engine waking` を
      51 回**流した。バイトが途切れた最長は 10 秒で、opencode の 300 秒に対して 30 倍の余裕。
    - **512 秒後にモデルの答えが同じ接続に流れ、`ADR0071-ONE-ATTEMPT` と返って `[DONE]`。**
      CP のログは `POST /engine/llm/v1/chat/completions 200 8m31.915s` の 1 行だけで、
      **再送も 503 も無い**。決定 5 の差し替えは、これで実測に裏打ちされた。
    - 最後のチャンクに `usage`（prompt 24 / completion 12）が乗っていた。**curl は
      `stream_options` を送っていない**ので、これはゲートウェイが自分で付けた分である（7）。
11. 🔴 **その 1 回で、CP→Agent の使用量 POST が落ちるのも見つかった。**
    `dial tcp: lookup af-ws-… on 10.20.0.2:53: no such host`——Service Connect の別名は DNS では
    なく、ECS agent が CP のタスク起動時に一度 `/etc/hosts` に書くだけなので、**CP より後に
    作られた Workspace は解決しない**。`agent_dial.go` はまさにそのために Cloud Map への
    fallback を持っているのに、こちらは素の `http.Client` を使っていた。`newAgentTransport()` に
    直した。落ちても静かなので（行が 1 本消えるだけ）、実物を通さなければ見つからない類である。

12. ✅ **取り込みタスクを実物で 1 回通した。** SDXL 6.94 GB を HF から **161 秒（43 MB/s）**で
    引き、**HF が申告する sha256 と一致**（`31e35c80…`）してから S3 へ **46 秒**で上げた。
    照合が効くこと・`DependsOn: SUCCESS` で照合を通らないものが上がらないことを、実際の
    ファイルで確かめたことになる。HF の速度はこれで 5 点目で、**43 MB/s** はやはり
    4〜236 MB/s の帯の中に落ちた——「引くまで分からない」は 5 点でも崩れていない。
    ⚠️ 実運用の注意が 1 つ: `run-task` でこのタスクのコマンドを上書きするときは、
    `EntryPoint` が既に `["sh","-c"]` なので**上書きは 1 本の文字列**にすること。
    `["sh","-c",<script>]` を渡すと `sh -c sh -c <script>` になって**何もせず exit 0** する
    （＝成功に見えるが実行されていない）。

13. 🔴 **managed セッションでは 401 になった。P0 の実装が opencode の既定経路を外していた。**
    実際に Console から opencode のセッションを作って「こんにちは」と送ったら
    `APIError (HTTP 401) invalid engine session token`。起動メニューには
    `qwen3-coder-30b-a3b` がちゃんと出ていたので、設定の書き込みまでは効いていた。
    CP のログには `GET /internal/engine/catalog 200` はあるのに **`POST /internal/engine/token`
    が 1 度も無い**——つまり `BuildLaunch` を通っていない。opencode の **managed 経路は
    ワークスペースに 1 本の `opencode serve` デーモンを共有**しており、その env は
    `auth.go` の `env()` から来る。`LaunchPlan.Env` に載せたトークンは tmux 経路にしか
    届かず、`{env:AF_ENGINE_TOKEN}` は空のままだった。`env()` に入れて直した。
    ついでに `env()` には**保存済みキーが無ければ nil を返す早期 return** があり、これは
    フリーティア／Console ログインのワークスペース（＝普通の状態）でトークンを丸ごと落と
    していた。保存キーと自前エンジンは独立なので、その early return も外した。
    🔴 **帰結として、未解決 2 が前提にした「セッション単位」は opencode の既定経路では
    成立しない。** デーモンにセッションは無いので、managed 経路のトークンは
    **ワークスペース単位**（session 空）になり、使用量の行はメンバーには紐づくが
    セッションには紐づかない。tmux 経路だけはセッション単位のままにしてある。
    トークンの寿命も 24 時間から **30 日**へ延ばした——デーモンは `{env:…}` を起動時に
    一度しか読まないので、24 時間で切れると作業の途中で 401 になり、デーモンを再起動
    しないと直らないからである。

その他、P0 で確かめたこと:

- **S3 → 箱は 115〜147 MB/s**（18.5 GB を 126・161 秒）。0070 のハーネスの 104〜147 MB/s と
  同じ範囲で、5 点目でも「ファイルによらず一定」は崩れていない。
- **S3 → S3 のサーバサイドコピーは 17.3 GiB を 52 秒**（同一リージョン・無料）。取り込み済みの
  モデルを別のバケットへ移すのは、HF から引き直す 31〜74 分と比べる対象にならない。
- **`crane copy` で GHCR → ECR は 2.59 GB を 179 秒**（このコンテナから）。
- **起動メニューの半分**は実物の opencode 1.18.29 に対して固定した:
  `WriteEngineProviders` が書いた設定をそのまま渡すと `opencode models` が
  `llamacpp/qwen3-coder-30b-a3b` を出す。**エンジンの host は実在しないもの**を指定してあり、
  CLI が provider に接続しないこと——起動メニューが描かれる時点でエンジンは眠っている——が
  確かめたい性質そのものである（`clicontract` タグのテスト）。
- 🔴 **CFN が desired を 1 に戻していないことは CloudTrail で確かめた。** `UpdateService` を
  並べると、タスク定義を差し替えた 2 回はどちらも `desiredCount` が**未指定**で、`1` を書いたのは
  こちらの計測スクリプトだけだった。「一度は自分で起きたのでは」と疑ったとき、サービスの
  `createdAt`（置換されていない）と CloudTrail の 2 つが要った。
- スタブテスト（`deploy/local/ecs-lifecycle-stub-test.sh`）に case 3g を足し、順序
  （20 → イメージ → 60 → 30）、`CAPABILITY_NAMED_IAM`、鍵の生成、新規サービスの desired 0、
  既存サービスに触らないこと、生成した鍵を引数に置かないことを固定した。

## P1 の実測（2026-09-07）

P1（`image` 役と provider `sdcpp`）を書きながら、af-sandbox の同じスタックに image 役を足して
実際に測った。P0 の予想が当たったのが 1 つ——**image 役は 1 回目の試行に収まる**——外れたのが
1 つ: 🔴 **MI の箱は `describe-instances` の一覧に出ない**ので、P0 が「GPU 0 台」の根拠に
していた確認手段は、実は根拠になっていなかった。かかった費用は g6.xlarge を 15 分ぶん（起動 13:32:05Z →
終了 13:47:04Z）で $0.31 程度。

同じ日の夜、CP と Workspace を焼き直して**残りの半分（`generate_image` の経路）も実機で
通した**——9 以降がそれで、そこで**予想が 2 つ外れた**: 決定 5 の「CP が要求を握る」は
非ストリーミングでは 60 秒しか成立せず（9）、その失敗は黙って利用者の課金枠に落ちた（10）。
2 回目の g6.xlarge は 15:26 起動で、費用はやはり $0.3 程度。直したうえでの measurement 13、
および利用者が普通に使った claude 経路の 14 で、完了の定義は実機で閉じた。

1. **image 役のコールドスタートは 197 秒**（`execute-change-set` から
   `listening on: http://0.0.0.0:8080` まで。イメージは ECR、チェックポイントは S3、箱は
   無し）。内訳: **+57 秒でタスク作成**、+61 秒で箱が container instance として登録、
   pull **55 秒**（ECR。P0 のハーネスが GHCR から引いた 135 秒に対して）、S3 取得は
   **6.94 GB を 65 秒＝107 MB/s**、そして **+197 秒で listen**。llm 役の 586 秒の
   3 分の 1 で、**opencode の 300 秒の中に収まる**——実測で解けた点 9 の「image 役は
   sd-server なら 195 秒で 1 回目の試行に収まる」は当たった。サービス作成を含むスタック更新
   全体は 5 分 4 秒。
2. 🔴 **MI の箱は `ec2 describe-instances` の一覧に現れない。id で名指しすれば返る。**
   タスクが RUNNING で `describe-container-instances` が
   `i-08a9…/af-af-ecs-engines-image` を返している最中に、`describe-instances` を
   **フィルタ無しで**呼んで生 JSON を数えると 3 台（スロットプールの m8g/m7i）しか無く、その
   id は含まれない。ところが `describe-instances --instance-ids i-08a9…` は
   **g6.xlarge / running / `af-role=engine-image`** を返す（`OwnerId` は自分のアカウント、
   `RequesterId` は AWS 側、タグに `aws:ec2:fleet-id` と
   `aws:ec2:managed-launch=ecs-managed-instances`）。帰結が 2 つ:
   - **「`describe-instances` が 0 台だから GPU は動いていない」は成り立たない。** P0 の
     実測 10 と運用手順はその形で書いてあるので訂正する。止まったことを確かめる手は
     **`describe-instances --instance-ids <id>`**（id は ECS の
     `describe-container-instances` から取る）か、ECS 側の container instance が消えることで
     ある。「0 件」を道具ごと疑う話がまた出た。
   - 費用配分タグは効いている——箱に `af-role=engine-image` が付いていた（決定 9）。
3. **sd-server の面は本番の経路で通った。** CP の SG に置いた使い捨ての Fargate タスクから
   （本番のタスクロールに `ssmmessages:*` を入れないので、P0 と同じ手）:
   `GET /v1/models` が **200 を 4.6 ms**、`/v1/images/generations` が
   **512px 11.2 秒（初回）・1024px 17.5 秒**、`"n":2` の 512px が **10.7 秒で 2 枚**
   ——暖まれば 1 枚 5.4 秒で、初回の 11.2 秒には暖機が入っている——、
   `/v1/images/edits`（image＋mask の multipart）が **512px 5.2 秒**。**鍵は要らない**
   （何のヘッダも付けずに 200）。P0 の 512px 7.8 秒 / 1024px 20.8 秒とは別の日の別の箱なので、
   言えるのは「512px 5〜11 秒、1024px 17〜21 秒」の帯までである。
4. ✅ **`size` は効く。** 1024x1024 を頼んだ応答の PNG は IHDR が
   `00 00 04 00 00 00 04 00`＝**1024×1024**、edits で 512x512 を頼んだ応答は **512×512**
   だった。0069 決定 7 の「size は希望であって保証ではない」は Codex 経路の恒久的な姿だが、
   **この provider では頼んだとおりに返る**——だから `Caps.Sizes` を空（＝選べない）に
   せず、チェックポイントごとの一覧を出す形にした。
5. **VRAM は params 6,624 MB**（P0 と同じ値）。auto-fit は DiT 4,897 MiB・Conditioner
   1,559 MiB・VAE 159 MiB を全部 CUDA0 に載せた（L4 の空きは 22,369 MiB）。llm 役の
   20.9 GB と足すと L4 には収まらないという決定 2 の根拠が、別の日の別の箱でもう一度出た。
6. **ドレインは shutting-down まで 71〜164 秒、terminated まで 477 秒。** `desired 0` の
   あと +71 秒の時点ではまだ ACTIVE、+164 秒には `shutting-down` だった。P0 の
   「shutting-down の開始は +95〜104 秒」と同じ帯で、3 点目として一致した。
7. **CFN の変更は追加 4・変更 2 で、llm 役には触れない。** 変更セットは
   `ImageCapacityProvider` / `ImageTaskDef` / `ImageDiscovery` / `ImageService` を Add、
   `Associations`（capacity provider のリストは置換なので毎回出る）と `EnginesParam` を
   Modify。`LlmService` は現れず、llm の desired は 0 のままだった——決定 8(a) の
   「`DesiredCount` を書かない」が、2 つ目の役を足す更新でも効いている。
8. **エンジン表は 2 行になった。** SSM に書かれた**実物の文字列**を CP のパーサのテストに
   そのまま入れてある——CloudFormation の折りたたみスカラーが残す空白ごと。テストが守るべき
   形は、テストを書く人間が書く形ではなく CloudFormation が出す形だからである。

9. 🔴 **`generate_image` を実機で通したら、完了の定義の残り半分——「1 回の呼び出しの中で絵が
   返る」——は最初の試行で成立しなかった。** CP と Workspace を `0.16.1-dev-f4a12675` に焼き
   直し、af-sandbox の実物のワークスペース（opencode セッション `sh7gxia`）から
   `workspace-agent mcp-stdio --image-gen` を叩いた。停止中の image への最初の呼び出しの記録
   （すべて 2026-09-07 UTC）:
   - 15:26:49 要求。**sdcpp は非ストリーミング**なのでゲートウェイは `plain` 経路。
   - 15:26:50 CP `engine image: started on demand`。
   - **15:27:49 CP が `POST /engine/image/v1/images/generations` を 503 で返す。所要 `59.998s`。**
   - 15:29:35 CP `engine image: warmed up (ready)`——**起動自体は 165 秒で終わっていた**
     （P1 の実測 1 の 197 秒と同じ帯。2 点目）。
   60 秒ちょうどの出所は CP ではない。CP 自身の上限は 900 秒で、切ったのは
   **30-ingress の ALB の `idle_timeout.timeout_seconds: "60"`**（実物の LB でも 60 を確認）。
   応答のバイトが 60 秒来ない接続は閉じられる。**llm で起きなかったのは 10 秒ごとの SSE
   コメント行のおかげ**で、あれは opencode の 300 秒対策として書いたものだが、**同時に ALB の
   アイドルも無効化していた**——P0 の「512 秒 1 発成功」はその副産物に乗っていたことになる。
   直し（決定 5 の 🔴）: 非ストリーミングの保持を `AF_ENGINE_PLAIN_HOLD`（既定 45 秒）で
   前段より手前に畳み、`503 engine_waking`＋`Retry-After` を返す。聞き直すのは provider
   `sdcpp` 側で、16 分の予算の中で再構築して投げ直す（**再送ではなく再構築**——edit の本体は
   一度読み切った multipart なので、replay すると JSON でない唯一の面だけが空で飛ぶ）。
   **「1 回の呼び出しの中で絵が返る」という利用者から見た性質は、実装の場所を移して守る。**

10. 🔴 **その 60 秒の失敗は、黙って利用者の課金枠に落ちた。** 同じツール呼び出しの台帳に
    2 行が並んでいる: `kind:"sdcpp"` `ok:false` `ms:60000`、その 21 秒後に
    `kind:"agy"` `ok:true` `ms:20880` `images:1` `pixels:1048576`。`auto` は ready な
    provider の**並び**なので、1 番目が落ちれば 2 番目が走る——設計どおりだが、**自前の
    ハードウェアがあと 2 分で答えるところで、メンバーの Antigravity 枠を 1 枚ぶん使った**。
    sdcpp を並びの先頭に置いた理由（0069 の「誰の財布か」）が、フォールバックによって
    ちょうど裏返る。決定 5 の直しはこの経路も塞ぐ（60 秒で落ちなくなる）。
    ⚠️ フォールバックそのものは残した（エンジンが本当に落ちている配備では正しく、拒否したら
    絵が出ないだけである）。代わりに**黙っているのをやめた**——落ちた先の provider で成功したら
    `warnings` に「並びの前の provider が失敗したので落ちた・別のアカウントの枠で走った」と
    出す。誰の財布かは内蔵の並びが決めている当のものなので、それを黙って裏返すのが駄目な部分で
    ある。なお決定 5 の直しで「待てば自前で済んだ」ケースはそもそも起きにくくなった:
    provider が 16 分の予算を使い切るまで聞き直すので、落ちたときは本当に駄目なときである。

11. ✅ **暖まっていれば経路は全部通る。** 同じセッションから、CP ゲートウェイ→provider→
    MCP ツールを通した実測:
    - **generate 1024×1024 が 23.4 秒**（`auto` が sdcpp を選び、返った PNG は 1,073,204 バイト・
      IHDR が 1024×1024）。**進捗通知が 10 秒間隔でちょうど 2 本**（gaps 10.0 / 10.0 / 3.4 秒）。
    - **edit（multipart）が 5.3 秒**、**inpaint（multipart＋mask）が 5.0 秒**、どちらも 512×512。
      **ゲートウェイの `Content-Type` 素通しが実物に当たったのはここが初めて**で、boundary は
      壊れずに届いた（P1 で本文に足した 3 点目が実測に裏打ちされた）。
    - CP のログはそれぞれ `200 23.396s` / `200 5.236s` の 1 行だけ。
    - **エンジンを起こさずに答える面も確認**: `/imagegen/status` は箱が 0 台のまま
      `provider:"sdcpp"` `ready:true` `model:"sdxl-base-1.0"` `ops:[generate,edit,inpaint]`
      `order:["sdcpp","agy","codex"]` を返す。`tools/list` の `provider` enum は
      `["sdcpp","agy"]`、`op` enum は `["generate","edit","inpaint"]`。

12. ✅ **台帳は決定 9 のとおりだった。** 1 日ぶんの生ファイルで
    **`tool.imagegen` が 5 行・`engine.image` が 0 行**。画像の行は
    `images` と `pixels` を持ち（1024² は `1048576`、512² は `262144`）、`ref` はセッション名、
    `measured:"none"`（このルートにトークンは存在しないので 0 と書かない。0069 と同じ扱い）。
    ✅ **ついでに P0 の宿題が 1 つ片づいた——`engine.llm` の行は台帳に届いていた**（同じ日で
    **55 行**、`in`／`out` が入って `measured:"exact"`、`kind:"opencode"`）。P0 の実測 11 で
    `newAgentTransport()` に直した CP→Agent の POST が、実物で効いていることの確認である。

13. ✅ **直したうえで測り直したら、完了の定義の残り半分が実物で成立した（2026-09-08）。**
    `0.16.1-dev-2e501835`（決定 5 の分岐入り）を配備し、**箱 0 台**の状態から
    `generate_image` を 1 回だけ呼んだ（provider は `sdcpp` を名指し——外れたときに 10 の
    フォールバックでまた利用者の枠を使わないため）。時刻は UTC:
    - 01:03:30 要求 → `engine image: started on demand`
    - 01:04:15 CP `503 45.012s` ／ 01:05:06 `503 45.007s` ／ 01:05:57 `503 45.007s`
      ——**前段の 60 秒より手前で 3 回畳んだ**
    - 01:06:22 `warmed up (ready)`。**コールドスタートは 172 秒**（実測 1 の 197 秒・
      実測 9 の 165 秒と同じ帯で、3 点目）
    - 01:06:41 CP **`200 38.547s`**
    - **ツール呼び出しは 191.6 秒で 1 回**。進捗通知は **19 本すべて間隔 10.0 秒**、
      返ったのは 1024×1024・1,073,204 バイト・`provider:"sdcpp"`。
    再試行は 3 回ともツールの上には現れていない。**「1 回の呼び出しの中で絵が返る」は、
    握る場所を CP から呼び出し側へ移したうえで成立する。**
    同じ配備で edit **5.2 秒**・inpaint **5.0 秒**（どちらも 512×512）も通り、
    その日の台帳は **`tool.imagegen` が 3 行、すべて `kind:"sdcpp"`・`ok:true`**
    ——**agy の行は無い**（10 のフォールバックが起きていない）。`engine.image` は 0 行のまま。

14. ✅ **同じ機構が claude 経路でも効いた（2026-09-08・利用者が実際に使った 1 回）。**
    P1.5 のトグルを配備した直後（`0.16.1-dev-c346ad66`）、利用者が **claude のセッションから
    MCP 経由で**画像を作らせた。エンジンは停止中で、CP のログは 13 と同じ形になった:
    02:22:39 `started on demand` → **`503 45.004s` / `45.002s` / `45.003s` の 3 回**
    → 02:25:32 `warmed up (ready)` → 02:25:38 **`200 26.325s`**（要求からおよそ 179 秒）。
    続けて暖まったあとの 3 枚が `200 5.545s` / `5.436s` / `5.423s`。
    - **コールドスタートは 165 / 172 / 173 / 197 秒で 4 点目**。帯は崩れていない。
    - 意味があるのは **kind が違うこと**である。claude は MCP のツール呼び出しに上限が無く
      （実測: claude は無し・codex 300 秒・opencode 60 秒）、**進捗通知に頼らない経路**である。
      それでも同じ 3 回の畳みと同じ 1 回の答えになった。つまり決定 5 の直しが効いているのは
      **クライアントの都合にではなく前段の 60 秒に対して**であり、13 の結果が opencode 固有の
      何かに乗っていた可能性はこれで消えた。
    - ⚠️ これは仕込んだ計測ではなく**利用者が普通に使った 1 回**なので、ツール側の所要時間や
      進捗通知の本数はこちらでは見ていない。見たのは CP のログだけである。

その他、P1 で確かめたこと:

- **ECS Exec は検証の道具として使えるが、pty が stdin の EOF で落ちる。**
  `aws ecs execute-command --interactive` に `</dev/null` を与えると、長い呼び出しは**要求を
  送った直後にセッションごと消える**（実際に 1 回目の計測をこれで失った）。`setsid` で
  切り離して出力をファイルに落とし、短い exec で覗く形にした。`nohup` だけでは足りない。
  Python は `-u` にすること（バッファされた出力は、死んだプロセスとともに消える）。
  なお **Workspace のタスクロールには `ssmmessages:*` が無い**（付いていない側が正しい）ので、
  この検証のあいだだけインラインポリシー `af-adr0071-p1-temp-exec` を足し、
  `enableExecuteCommand` と一緒に**検証後に外す**。
- **`describe-instances` を一覧で見るなの続き**（実測 2）: 今回もエンジンの箱は ECS 側
  （`describe-container-instances`）でしか見つからなかった。
- **GHCR → ECR の複製は 2.42 GB を 177 秒**（このコンテナから `crane copy`）。P0 の
  llama.cpp（2.59 GB を 179 秒）と同じ速さ。
- **`image` 役の ECR リポジトリ（`af-sdcpp`）は 20-platform に置いた。** 60-engines の中に
  作ると、そのスタックが自分で作ったリポジトリから pull しようとして CREATE が収束しない
  ——`af-llamacpp` と `af-voicevox` がそこにある理由と同じで、3 つ目の同じ形である。
  ただし複製は `ImageModelS3Key` が入っているときだけ走らせる: LLM しか使わない配備に
  2.3 GB を引かせる理由が無い。
- **`60-engines.yaml` が 51,200 バイトの壁に当たった。** image 役を足した時点で 55,832
  バイトになり、スタブテストの門番（case 3b-2）が落ちた。30-ingress と同じ手当てで、
  パラメータの長文を `cfn/PARAMETERS-60-engines.md` に移して 50,779 バイトに戻した
  （中身は削っていない）。`af_cfn_deploy` は S3 経由に切り替えて通してくれるが、通るかどうかを
  デプロイの日に知るのは遅い。


## P1 の後の訂正——エンジンの窓を誰にも言っていなかった（2026-09-08）

P0・P1 が出荷した provider ブロックは、モデルに `{"name": …}` しか書いていない。実機で使って
分かったのは、**それが「窓の宣言が無い」ではなく「窓は 0」として読まれる**ことである。

🔴 **opencode 1.18.29 で実測した**（隔離した HOME に af と同じ形の provider を書き、
`GET /config/providers` を読んだ）。`qwen3-coder-30b-a3b` は
`limit={context:0, output:0}` で返る。帰結が 2 つあり、どちらも黙って効く:

- **`context===0` のとき opencode は自動コンパクションを止める。** 会話は `-c 32768` で
  建てた llama-server が要求を蹴るまで伸びる。画面には何も出ない。
- **使える枠は `context − 出力上限`。そして出力上限の 0 は「未設定」ではなく 32000 に化ける**
  （`var M7=32000`）。だから **context だけ入れると 32768 − 32000 = 768 トークン**になり、
  1 ターン目からコンパクションが暴れる。**2 つ揃わないなら入れないほうがまし**である。

直し方は決定 8 の形のまま——**宣言し、導出しない**。`-c` は `LlmExtraArgs` の中に埋まっていて
CloudFormation からは読み出せず、だからエンジン表に書けなかった。`LlmContextTokens` /
`LlmMaxOutputTokens` を切り出し、**同じ値をコマンドラインの `-c` とエンジン表の両方へ**流す。
CP は `contextTokens`>0 のときだけカタログに載せ、Agent は 2 つ揃ったときだけ opencode の
`limit` を書く（片方だけなら今までどおり何も書かない＝古い挙動）。

**窓はモデル毎ではなくエンジン毎である。** llama-server は 1 プロセスで GGUF 1 個・`-c` 1 個
なので、`LlmModelIds` に並ぶ id は同じ窓の別名にすぎない。窓の違うモデルを 2 つ出すなら
**行を 2 つ**にする——そのとき ⚠️ **provider id も分けること**。Agent は provider 名をキーに
opencode の `provider` ブロックを書くので（`opencode/engine.go`）、2 つ目の `llamacpp` は
1 つ目を黙って上書きする。

見つかった経緯は「sandbox の qwen3-coder-30b-a3b で WebFetch ができない」という報告で、
**その報告の原因は別**だった: opencode はツール出力を 2000 行 / 51,200 バイトで切り、
**行の途中では切らない**。Google News の RSS は改行が 1 つも無い 1 行 89 KB なので、モデルに
渡った本文は **0 バイト**——残ったのは「全文はファイルに保存した、Task ツールで explore
サブエージェントに読ませろ」という指示だけで、30B のローカルモデルはその復旧フローを完走できず
「インターネット接続を確認してください」という事実と違う結論を書いた。fetch 自体は 200 で
成功している。opencode の仕様どおりの動作でこの ADR の範囲外だが、**窓の件はその調査の途中で
見つかった、別の・実在する穴**である。

## フェーズ

- **P0 — 土台と llm。** `60-engines.yaml`（capacity provider・S3・取り込みタスク・llm の
  サービス）、standup / update / teardown / capture-env、ECR への公式イメージ複製、CP の
  ゲートウェイ `/engine/llm/v1/*`（起こして待つ・streaming・usage の記録）、一般化した
  コントローラ、opencode の provider 注入。完了の定義は、**opencode の起動メニューに
  `llamacpp/<model>` が出て、停止中のエンジンへの最初の要求が 1 回の試行で（再送なしに）
  答えを返し、30 分黙るとインスタンスが消えること**——「1 回の試行で」が決定 5 の差し替えを
  検証する唯一の観測である。P0 で測るもの: `useLocalStorage` のコールドスタート、
  `scaleInAfter` 0 / −1 のドレイン、ドレイン中の再要求、ECR からの pull、プール走査が MI を
  除外すること。llm を先にする
  のは、要求されたのが画像だからではなく、**アプリ側のコードが最も少なく土台を検証できる**
  からである（opencode 側はゼロ改修）。
- **P1 — image（sd-server）。実装済み**（2026-09-07。「P1 の実測」節）。`image` 役のサービス、
  Agent の provider `sdcpp`（transport は CP ゲートウェイ。0069 決定 3 の「テナント層は
  CP 経由」と同じ）、`Caps` は (provider, モデルファイル) 単位、generate / edit / inpaint、
  進捗通知。実装で本文に足したことが 3 つ:
  - **エンジン表に `api`（`chat` / `images`）を足した。** 役の性質を key から導かず宣言する
    ためで、これが 2 つを決める——Agent が opencode の provider として書くかどうか（image
    エンジンを書くと「会話できないモデル」が起動メニューに出る）と、使用量をゲートウェイが
    数えるか（`chat`）Agent の `tool.imagegen` が数えるか（`images`。決定 9）。
  - **ゲートウェイは要求の `Content-Type` を素通しする。** `/v1/images/edits` は
    multipart で、boundary はそのヘッダの中にある——`application/json` を上から書くと、
    JSON でない唯一の面だけが壊れる。
  - **`image` 役に `--api-key` は無い。** sd-server に認証の仕組みがそもそも無いので
    （上流の `examples/server/api.md`）、SG が access control の全部である。決定 4(d) の
    「二重の鍵」は llm 役だけの話になる。
  完了の定義は**実機で全部通した**——エンジン側が下の 1・3・4、CP のゲートウェイと Agent の
  provider が 9〜12（`0.16.1-dev-f4a12675` を af-sandbox へ配備し、実物のワークスペースの
  opencode セッションから `generate_image` を呼んだ）。ただし🔴**最初の試行は失敗した**
  ——非ストリーミング要求を ALB が 60 秒で切る（実測 9）——ので、決定 5 を役ごとに分ける
  直しが P1 に 1 つ足された。
- **P1.5 — 実機で見つかった 2 つの穴（2026-09-08）。** 決定 5 の分岐（実測 9）、
  フォールバックを黙らせない（実測 10）、そして**Console のエンジン on/off**。
  最後のものは新しい機構ではない: モードは前から `engine_<key>_mode` のストア設定で、
  ゲートウェイ（`503 engine_off`）・カタログ（消える）・コントローラ（止めて放置）が
  すべてそれを読んでいた。**書く側が無かった**だけで、切る手段はスタックパラメータ
  （`LlmMode` / `ImageMode`）＝CloudFormation を回すことしか無かった——GPU が暴れている
  ときに人が取る手ではない。`GET /api/admin/engines` と `PUT /api/admin/engines/{key}` を
  superadmin に生やし、TTS の `/api/admin/tts` と同じ形にした。TTS と違うのは 1 点、
  **「無効」はその場で箱を止める**こと: TTS の undo 窓は誤爆した OFF→ON が 2 GB の pull と
  80 秒で済むから正しいが、GPU は $1.26/時で、窓の間ずっと払うことになる。
  🔴 実装中に見つけた欠陥が 1 つ——`engineRuntimeState.mode` は**コントローラがある場合しか
  ストア設定を読んでいなかった**。本番では必ずコントローラがあるので実害は無かったが、
  「このエンジンのモードは何か」の答えが別の協力者の有無に依存していた。設定を状態そのものに
  持たせて直した。
  **同じ画面に現況を足した（決定 13）**——トグルだけでは「切っていいのか」が分からないため。
  `GET /api/admin/engines` の行に箱の起動時刻・自動停止の予定・直近の使用状況・warm を足し、
  `GET /api/admin/engines/{key}/hourly` と新しい表 `engine_hourly` で稼働実績のヒートマップを
  出す。サンプラはコントローラのティックそのもので、**それまでエンジンの稼働時間はどこにも
  記録されていなかった**。細部（未観測を停止と描かない・分母を保存する・答えの無い欄は出さない）
  は決定 13 に書いた。
- **P2 — ComfyUI。** 自前イメージ、`/engine/comfy/` のペイン、provider `comfy` とワーク
  フローテンプレート、sd-server との排他。
- **P3 — llm を codex と claude へ。** codex は `model_providers` に `base_url`＋
  `wire_api = "responses"`、claude は `ANTHROPIC_BASE_URL`。どちらも**モデル選択の意味が
  変わる**（利用者のログインでなくフリートの箱に課金が移る）ので、起動メニューの表示と
  同意を先に設計する。llama.cpp の `/v1/messages` は thinking ブロックを落とす既知の問題
  （#20090）があり、Claude Code は背景で Haiku 宛の要求を多数出す。
- **P4 — 根拠つきで安くする。** MI の Spot、`image` の g6f への縮小（g6f.2xlarge は
  `--offload-to-cpu` と量子化を測ってから）、router モードでの
  複数モデル、そして P0 の数字が pull 支配と言ったときだけイメージの縮小。

## 確認した出典（2026-09-06）

- AWS の公開価格データ: EC2 オンデマンド（東京・Linux）の meteredUnitMaps、`AmazonECS` の
  ap-northeast-1 オファー（`APN1-ECS-Managed-Instances:<type>-management-hours`）、
  `AmazonEFS` / `AmazonS3` の ap-northeast-1 オファー。EBS gp3 と NAT の単価は第三者の転記。
- AWS Fargate FAQ（GPU 非対応）、containers-roadmap #88、ECS Managed Instances の GA 告知
  （リージョン）、2026-07 の GPU 管理料改定、2026-08 の「GPU batch inference … with scale to
  zero」ブログ（13 分・5 分でスケールイン・イメージに焼く推奨）。
- GHCR / Docker Hub のレジストリ API: `ggml-org/llama.cpp`、`leejet/stable-diffusion.cpp`、
  `lecode-official/comfyui-docker`、`yanwk/comfyui-boot` のマニフェスト。
- Hugging Face API（`?blobs=true`）: 上のモデル表のサイズとゲート。
- `llama.cpp` の `tools/server/README.md`、`stable-diffusion.cpp` の `examples/server/api.md`
  と README、ggml-org の「Anthropic Messages API in llama.cpp」、`opencode.ai/docs/providers`。
- このコンテナでの実測: `OPENCODE_CONFIG` で指した provider が `opencode models` に出ること
  （opencode 1.18.29）、遅延スタブに対する opencode の切断と再送、llama.cpp `b10825` の
  Linux x64 ビルドと `stable-diffusion.cpp` `master-841` の Linux x86_64 ビルドを CPU で
  動かした各数値、HF から取った GGUF の sha256 照合。
- 検証アカウント `af-sandbox` での実測: `deploy/aws/ecs/harness/engprobe.yaml` と
  `probe-managed-instances.sh`（Managed Instances の起動・ドレイン、IAM と capacity provider の
  契約、GPU での llama.cpp と sd-server の通し）、`service-quotas` の L-DB2E81BA、
  SSM のポートフォワード（`AWS-StartPortForwardingSession` を ECS Exec の target に）。
  この GPU 実測にかかった費用は g6.xlarge 約 1 時間ぶんと NAT の 25.4 GB＝合計 $3 程度。
- 本リポジトリ: `control-plane/main.go`（proxy env）、`egress_policy.go`、
  `preview_host_serve.go`、`tts_ecs.go`、`internal/runtime/runtime_ecs.go`（awsvpc・`WsSg`）、
  `workspace/agent/internal/imagegen/imagegen.go`、`internal/agents/opencode/{auth,models}.go`、
  `deploy/aws/ecs/cfn/{00-network,20-platform,30-ingress,50-tts}.yaml`。

## レビュー（2026-09-07・P0 着手前）

0070 のレビューと同じ流儀で、「決定 → 根拠 → 実測」の筋と、実測から引けない結論を引いて
いないかを疑った。結論から書く: **承認（P0 着手可）を提案する。ただし決定 5 と決定 2 の 2 箇所は、
レビューの実測で前提が覆ったので、P0 のコードを書く前に本文を下の提案どおり改めること。**
起草側が一度訂正した「2 点だけ見て一般化」は、決定 3 の訂正後の版と決定 2 の 8 vCPU の文に
**あと 2 つ**あった。どちらも既に取ってあったデータ（CloudWatch ログと CloudTrail）が反証して
いた——測り直しは要らず、読み直しで足りた。

### レビューで測ったこと・確かめたこと

- **R1. opencode の 300 秒は「壁」ではなく「無音の上限」である。** このコンテナの opencode 1.18.29
  （バイナリの文字列に `BUN_1.2`＝Bun 製）に、OpenAI 互換のスタブを 3 通りの振る舞いで繋いだ
  （`/tmp` の使い捨て。本体の要求は tools=21・70 KB、先にタイトル生成の tools=0 が来る）:
  1. **何も返さず握る**（ADR の実測 1 の再現）: 要求 5.7 秒 → **308.0 秒・612.5 秒・…に同じ要求を
     再送**（間隔 302〜304 秒。ADR の「300.1 秒で切って数秒後」と一致）。
  2. **ヘッダだけ先に返す**（200・`text/event-stream`、本文は 400 秒無音）: **306.9 秒で再送**。
     ヘッダでは持たない。
  3. **ヘッダ＋10 秒ごとに SSE のコメント行 `: ping`** を流し、400 秒後に本物のチャンクと
     `[DONE]` を返す: **再送なし。opencode は 405 秒後の答え（`PROBE-OK after 400s`）を表示して
     正常終了した**（タイトル要求も同様に届いた）。
  つまり切られるのは **「本文のバイトが 300 秒来ない」** ときで、経過時間そのものではない。
  ゲートウェイが接続を握ったまま 10 秒に 1 行流せば、起動の 527 秒も、prefill の無音も、
  **1 回の試行の中で越えられる**。決定 5 の「290 秒で 503・起動は試行と切り離す・再送に乗せる」
  「最初のトークンまで 300 秒」は、この機構を「壁」と読んだことから出た設計で、**再送の回数が
  有限であることに賭けている**（再送の回数を決めているのは opencode／AI SDK 側で、ADR の
  「4 回」は 1,200 秒で打ち切ったハーネスの都合であり上限ではない。レビューの再現では 1,900 秒の打ち切りまでに
  **6 回の試行**（再送 5 回。間隔は 302・310・316・330 秒と伸び、切断からの待ちは 2・4・8・16・
  32 秒＝AI SDK の指数バックオフ）で、諦める気配は無かった——上限があるかは分からないまま
  である）。
- **R2. 8 vCPU で g6.xlarge は 2 台建つ。1 台しか建たなかったのは、ドレイン中の箱がクォータを
  握っていたからである。** CloudTrail（読み取り専用）の `RunInstances` / `TerminateInstances`
  を並べ直した（JST）: 06:42:25 A 起動（llm・HF 直）→ 07:12:32 B 起動（image の 1 回目と
  見られる——取得サイドカーが uid 100 で書けず死んだ回。`engprobe.yaml` のコメントにある実測）→ **07:17:13 B を MI が終了** → 07:19:24・
  07:20:01・07:20:43 `VcpuLimitExceeded`（このとき A は稼働中、**B は shutting-down でまだ
  4 vCPU を数えられている**＝A 4＋B 4＋新規 4 ＞ 8）→ 07:25:05 A を終了 → **07:26:04 C 起動
  成功**——A の EC2 がまだ消えていない（ドレインは 427〜463 秒）うちに、である。したがって
  同時に 2 台（8 vCPU）は建ち、建たないのは**直前に落とした箱が消えるまでの 7〜8 分**である。
  「両役同時なら 16 vCPU」という結論は残るが、理由は「1 台しか建たない」ではなく
  「**ドレイン 1 台ぶんの余白**」に変わる。B の 07:12:32 → 07:17:13（281 秒）は、タスクが
  死んだ箱を MI が既定の時計で片づけた実測でもある（R5）。
- **R3. g6f の VRAM は公式値で 5,722 MiB（2xlarge）・11,444 MiB（4xlarge）・2,861 MiB（xlarge
  と large）、g6.xlarge は 22,888 MiB。** `describe-instance-types` で取り、表の「第三者の集計値
  （≈3 / ≈6 / ≈12 GB）」を置き換える。同じ呼び出しから: g6.xlarge のインスタンスストアは
  **250 GB**、**EBS のベースライン帯域は 125 MB/s**（g6f.2xlarge 250、g6f.4xlarge 750）。
- **R4. 単価は AWS 価格 API で一致した。** g6.xlarge $1.1672/h（東京・Linux・オンデマンド）、
  MI 管理料 g6.xlarge $0.0910・g6f.2xlarge $0.0537・g6f.4xlarge $0.1075・c6a.large $0.0116。
  合計 $1.258/h、窓 30 分 $0.63（本文の $0.65 はわずかに多め）、ドレイン 427〜464 秒 $0.15、
  常時起動 730 時間 $918/月、NAT 17 GB $1.05、EFS 100 GB $36、EFS IA 読み 17 GB $0.20——
  計算違いは無い。GPU 実測の請求は CloudTrail 上の 6 箱の合計が約 1.5 時間（本文の「約 1 時間」
  より多い）で、NAT 込みおよそ $3.5。
- **R5. MI の API には ADR が触れていないつまみが 2 つある**（aws-cli 2.36.40 のサービス定義）:
  `infrastructureOptimization.scaleInAfter`（**アイドルな箱を片づけるまでの秒数**。null＝既定、
  −1＝片づけない、0〜3,600）と `instanceLaunchTemplate.localStorageConfiguration.useLocalStorage`
  （**インスタンスストアをデータボリュームに使い、EBS を作らない**）。一方 `storageConfiguration`
  は **`storageSizeGiB` だけ**で、スループットや IOPS は指定できない——未解決 1 の「gp3 の
  スループット指定」は MI では選べない。ドレイン 427〜463 秒は `scaleInAfter` 既定（未文書）
  のままの値であり、**MI 固有の固定費と決めるのは早い**。
- **R6. HF の速度は「ファイルで決まる」とも言えない。** 同じログ群（`/af/af-ecs-engprobe/engine`）
  に 3 点目があった: **同じ SDXL（6,938,078,334 バイト）を、取り込みタスク（Fargate の curl）は
  175 秒＝39.6 MB/s で引いている**——MI の箱のサイドカー（同じ curl・同じ NAT）が 236 MB/s
  で引いた同じファイルである。GGUF は 9.6 と 4.2 MB/s、SDXL は 236 と 39.6 MB/s。4 点から
  言えるのは「**4〜236 MB/s の範囲で、何で決まるかは分からない**」までで、「unsloth の
  リポジトリが遅い」は 2 点からの一般化である（訂正した文がまた同じ形をしていた）。S3 経路が
  3 回とも 104〜147 MB/s だったこと（実測で解けた点 8）だけが、決定 3 の根拠として残る。
- **R7. コードの現物。** (a) スロットプールの `registeredSlots` と `sweepGhostInstances`
  （`runtime_ecs_ec2.go`）は **クラスタの container instance を条件なしに全部歩く**——MI の箱も
  並ぶ（ハーネスの最初の 1 回がスロットの箱を測ってしまったのはこの裏返し）。EC2 タグ起点の
  走査（`af-role=slot`・`Ec2MaxSlots`）は無関係だが、この 2 箇所は決定 1 の「影響を受けない」
  の外にある。(b) CP タスクロールの SSM 読みは **`parameter/af-ws/*` に限定**されている
  （`20-platform.yaml` の `SsmWorkspaceParams`）。(c) `ec2:DescribeInstances` と
  `ecs:ListContainerInstances` / `DescribeContainerInstances` は CP ロールに**無条件で**ある
  （`Ec2SlotPool` / `EcsContainerInstances`）ので、`draining` を観測する権限は足りている。
  (d) `/git/*` も `/mcp` も Bearer の PAT（メンバー単位）で認証している（`git_http.go`・
  `mcpsrv`）——未解決 2 の前提どおり。(e) `30-ingress.yaml` は 41,988 バイト（残り 9,212）、
  ハーネスの 3 契約（`ClusterName` 必須・`ecsInstanceRole*` 限定の PassRole・関連付けはリスト
  置換＋空の既定戦略）は `engprobe.yaml` の記述と一致する。(f) `af-sandbox` は llama / sd / comfy
  とも desired 0・G 系の箱 0 台・クォータ 8 のまま（起こしていない）。

### 決定ごとの改訂提案

- **決定 1** — 末尾に足す: 「ADR 0045 決定 6 は Workspace について MI を退けた（ライフサイクルの
  所有者が ECS 側にあり stop が無く、ボリュームはサイズしか指定できない）。**エンジンは失って
  困る状態を持たない**ので、同じ性質がここでは利点になる——矛盾ではなく対象の違いである。」
  さらに R7(a) を受けて: 「スロットプールの container instance 走査（`registeredSlots`・
  `sweepGhostInstances`）は MI の箱も見る。P0 は `DescribeContainerInstances` の
  `capacityProviderName` で MI の箱を除外し、テストでそれを固定する。」
- **決定 2** — 「8 vCPU では 1 台しか建たず……408 秒待った」を R2 のとおりに書き換える:
  「8 vCPU で 2 台は建つ。ただし**落とした直後の 7〜8 分は前の箱が 4 vCPU を握る**ので、
  片役の建て直しがもう片役の起動と重なると `VcpuLimitExceeded` になる。両役を同時に使う配備は
  ドレイン 1 台ぶんの余白として 16 vCPU を申請する。」表の g6f の VRAM を R3 の公式値に
  置き換え、「g6f.2xlarge の 6 GB では入らない」は「**5,722 MiB に fp16 を offload 無しで載せる
  形では入らない**（`--offload-to-cpu` と量子化は未測・P4）」に弱める——元の未解決 2 が問うた
  のはまさにその 2 つで、それを測らずに「案は成立しない」と閉じるのは同じ種類の飛躍である。
- **決定 3** — 設計は据え置き、理由の文を R6 で直す: 「HF は同じ NAT の先で **4〜236 MB/s**、
  **同じファイルでも 6 倍違った**（SDXL: MI の curl 236 MB/s・Fargate の curl 39.6 MB/s）。
  ファイル・経路・時刻のどれで決まるかは 4 点では言えず、言えるのは**引くまで分からない**こと
  だけである。S3 は 3 回とも 104〜147 MB/s だった。」実測で解けた点 5 の「遅いのは HF がその
  ファイル（unsloth のリポジトリ）を配る速度」は 🔴 として残し、「ファイルで決まる」も 2 点
  からの一般化だったと注記する。
- **決定 5** — 差し替える（R1）: 「desired 0 のエンジンへの要求が来たら、CP は desired を 1 に
  し、**ストリーミング要求には即座に 200 と `text/event-stream` のヘッダを返して、上流の最初の
  バイトが届くまで 10 秒ごとに SSE のコメント行を流す**。エンジンが答え始めたらそのストリームに
  繋ぐ。同じ心拍を prefill の無音にも当てる（上流のヘッダを待つ間も流す）。したがって
  **300 秒は試行の上限ではなく、心拍の間隔の上限**である。`AF_ENGINE_WAKE_TIMEOUT` を超えたら
  503＋`Retry-After`＋本文——これは非ストリーミング要求と、起動が本当に失敗したときの経路で
  あって、通常経路ではない。503 はクライアントの有限な再試行を 1 つ消費するので、通常経路に
  置かない。」既定 600 秒は実測 527 秒に対して 73 秒しか余白が無く、GHCR からの pull（178 秒・
  14 MB/s）がそのまま入る P0 の配備では足りない——**ECR 複製後に測り直すまで 900 秒**にし、
  コントローラの起動期限（決定 7）はこの値以上にする。「CPU を却下するもう 1 つの理由」は
  「壁」ではなく「17 分」に書き換える（却下は変わらない）。
- **決定 6** — 順序は据え置く。ただし理由から「小さいイメージ」を外す: 5.1 GB は計測に使った
  コミュニティイメージで、フリートが焼く ComfyUI の大きさは未測である。残る理由は**面とコードの
  小ささ**——OpenAI 互換が 0069 の `Op` に一対一で写り、ワークフローテンプレート・WebSocket の
  ペイン・自前イメージのどれも要らない——で、P0 が llm を先にした理由と同じ形になる。
  「2.5 倍速い」はステップ数とサンプラーが同条件かを確かめていないので、「同条件は未確認」を
  添える。
- **決定 7** — 3 点。(a) 状態は 0070 決定 7 の `stopping`（OFF の取り消し窓）と `tts_ecs.go` の
  `none` を落とさず、`draining` を**足す**（4 値ではない）。(b) **起動期限はエンジンごとで、
  実測のコールドスタート以上**にする——0070 の既定 300 秒のままなら GPU の起動は全部
  「失敗」になり、クールダウンが倍々に伸びる（`createdAt` の事故と同じ現れ方）。(c) ドレインは
  `scaleInAfter` の既定で測った値なので（R5）、**P0 で 0 と −1 を 1 回ずつ測る**（$0.15 ずつ）。
  −1 は「箱を残す」設計の材料になる: desired 0 のあと箱が残っている間に来た要求は、
  **イメージもモデルファイルも箱にある**ので S3 取得なしで起きるはずである（VRAM ロードだけ）。
  「アイドル窓を切り詰めても回収できるのはドレインぶんだけ」は、ドレインを純粋な無駄と読んで
  いる——温かい箱の窓として設計に組み込めるかを、P0 で「停止 → ドレイン内に再要求」の 1 回で
  確かめる。
- **決定 8** — (a) 「SSM の読みは CP タスクロールに既にある」は **`/af-ws/*` の下に限る**（R7(b)）。
  パラメータ名を `/af-ws/engines` の形にするか、`20-platform` のポリシーを広げるかを決めて書く。
  (b) 0070 との対比は正確で、しかも精密に言える: **CP 側の IAM 追加はここでもゼロ**
  （UpdateService / DescribeServices / DescribeInstances / container instance の読み / SSM 読みは
  すべて既存・無条件）で、新しい IAM は `60-engines` の中で閉じる 3 ロール（infra・instance
  profile・タスク）である。(c) ハーネスは取り込みとエンジンで 1 つの TaskRole を共有しているが、
  本番は本文どおり取り込み（書き＋秘密）とエンジン（読み）に分ける。`ssmmessages:*` は
  ハーネス専用で本番のロールには入れない。
- **未解決 1** — 推奨は **`useLocalStorage: true` を最初に測る**こと。g6.xlarge の EBS ベース
  ライン 125 MB/s は、S3 取得の 104〜147 MB/s（S3 ではなく EBS 書き込みの上限に見える）と
  VRAM ロード（18.5 GB は 125 MB/s でも 148 秒以上）の両方を縛っている。インスタンスストア
  250 GB に替えれば両方が動くはずで、gp3 のスループット指定は MI には無い（R5）。
- **未解決 2** — 推奨は **セッション単位の専用トークン**。理由は 3 つ: (1) `usagex` の行は
  セッションで切られており、PAT では合わない、(2) `{env:…}` に置く値はモデルから見える——
  git と MCP に効く PAT をそこへ出すより、`engine:llm` にしか効かない短命の値のほうが、漏れた
  ときの損が小さい、(3) 0069 決定 3 のレビュー訂正（テナント層は git の bridge を借りず自分の
  エントリを持つ）と同じ形になる。
- **フェーズ** — P0 の「未解決 1〜3 の数字」と P1 の「未解決 4」は改訂前の番号で、いまの
  未解決は 1・2 だけである。P0 の計測に次を明記する: `useLocalStorage` のコールドスタート、
  `scaleInAfter` 0 / −1 のドレイン、ドレイン中の再要求、ECR からの pull、プール走査が MI を
  除外すること。
- **実測で解けた点 1・6** — R1 と R2 の帰結を 🔴 で追記する（本文は消さない）。

### 問いへの答え

1. 推定のままの決定: 決定 2 の g6f の下限（offload・量子化が未測）、決定 6 の「小さいイメージ」
   （自前イメージ未測）、決定 7 のドレイン（`scaleInAfter` 既定のまま）、決定 5 の 600 秒
   （余白 73 秒）。**P0 で測ればよいのは 6・7、P0 の前に本文を直すのは 2・5**。
2. 決定 5 は導けない。実測 1 は「300 秒無音で切る」であって「300 秒で切る」ではなく（R1）、
   再送が有限だった場合の設計は書かれていない。心拍で握れば再送に依存しない。
3. 「HF を起動経路に置かない」までは妥当。「ファイルで 50 倍」「unsloth が遅い」は 3 点目
   （同じ SDXL が 39.6 MB/s）で崩れる（R6）。
4. 順序は据え置きでよいが、据え置く理由は「面とコードの小ささ」であって「小さいイメージ」では
   ない。
5. 未解決 1 は `useLocalStorage`、未解決 2 は専用トークン（上）。
6. 対比は正確（CP 側ゼロ・新規 3 ロール）。契約 3 つはハーネスと一致。SSM の前提だけ `/af-ws/*`
   の条件付き。
7. 計算違いは無い（R4）。
8. 矛盾は無い。0045 決定 6（MI は Workspace に不適）は対象が違うと明記し、0070 の状態集合と
   起動期限を落とさないこと、プール走査の 2 箇所（R7(a)）を P0 で塞ぐこと。

### 状態行の提案

上の決定 2 と決定 5 を本文に反映したうえで、状態行を
**「承認済み（P0 着手可）」（2026-09-07 レビュー）** に改める。P0 の完了の定義に「停止中の
エンジンへの最初の要求が **1 回の試行で** 答えを返す」を足す——それが決定 5 の差し替えを検証する
唯一の観測である。
