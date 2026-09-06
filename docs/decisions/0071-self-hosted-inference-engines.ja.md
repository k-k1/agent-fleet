# 0071. 自前の推論エンジン（llama.cpp / Stable Diffusion / ComfyUI）を GPU でオンデマンドに動かす——Fargate では買えない GPU を、ADR 0070 の型のまま

[English](0071-self-hosted-inference-engines.md) | 日本語

- 状態: **提案（設計のみ・未実装）**（2026-09-06）。価格はすべて同日に AWS の公開価格データ
  （東京・オンデマンド）から、イメージとモデルの数値は同日にレジストリ API と Hugging Face
  API から取得した。このコンテナで実測したものはそう書いた。**P0 に着手する前にレビューを
  受けるための文書**であり、形が変わりうるのは「未解決の点」である。
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
  | g6f.large | 2 / 8 GiB | L4 の 1/8（≈3 GB） | 0.293 | 0.023 | 0.316 |
  | g6f.xlarge | 4 / 16 GiB | L4 の 1/8（≈3 GB） | 0.344 | 0.027 | 0.371 |
  | g6f.2xlarge | 8 / 32 GiB | L4 の 1/4（≈6 GB） | 0.689 | 0.054 | 0.743 |
  | g6f.4xlarge | 16 / 64 GiB | L4 の 1/2（≈12 GB） | — | 0.107 | — |
  | g4dn.xlarge | 4 / 16 GiB | T4 16 GB | 0.710 | 0.055 | 0.765 |
  | **g6.xlarge** | 4 / 16 GiB | **L4 24 GB** | **1.167** | **0.091** | **1.258** |
  | g6.2xlarge | 8 / 32 GiB | L4 24 GB | 1.418 | — | — |
  | g5.xlarge | 4 / 16 GiB | A10G 24 GB | 1.459 | 0.114 | 1.573 |
  | g6e.xlarge | 4 / 32 GiB | L40S 48 GB | 2.699 | — | — |
  | c8g.4xlarge（比較・CPU） | 16 / 32 GiB | 無し | 0.800 | 0.096 | 0.896 |
  | Fargate 16 vCPU / 32 GiB（比較） | | 無し | x86 0.986 / ARM 0.789 | — | — |

  （g6f の GPU 断片は第三者の集計値。P0 で `describe-instance-types` の
  `GpuInfo.Gpus[].MemoryInfo` を正とする。）
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
   のままで影響を受けない。

2. **エンジン役は 2 つ（`llm`・`image`）。役ごとに capacity provider を分け、同じ箱に載せない。**
   `llm` は VRAM ≥ 20 GB（Qwen3-Coder-30B-A3B Q4 が 17.3 GiB＋KV キャッシュ）、`image` は
   VRAM ≥ 6 GB（SD1.5 と SDXL q8）。既定はどちらも **g6.xlarge（L4 24 GB・$1.26/h 込み）**で、
   `image` は SD1.5 だけなら g6f.2xlarge（$0.74/h）へ下げられる。**インスタンス要件は
   役ごとのスタックパラメータ**（宣言する。導出しない。ADR 0053 の形）。同じ箱に 2 役を
   載せない理由: CUDA の VRAM 不足は遅くなるのではなく **落ちる**。片方が起きているだけの
   時間が大半なので、分けても費用はほぼ増えない。ComfyUI は `image` 役の**もう 1 つの
   エンジン**であり（決定 6）、sd-server と同時には動かさない（同じ VRAM を取り合う）。

3. **モデルは HF → S3 に一度だけ写し、起動のたびに S3 → ローカルディスクへ引く。HF を起動経路に
   置かない。** 17 GB を毎回 HF から引くと NAT で **$1.05**、31 MiB/s なら **9 分**かかり、
   HF のレート制限とゲート用トークンをノードに置くことになる。S3 は保存が $0.025/GB-月
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
   ok になるまで**接続を保持して**から転送する。上限 `AF_ENGINE_WAKE_TIMEOUT`（既定 600 秒）を
   超えたら 503＋`Retry-After`＋人が読める本文（モデルにも見える）。画像の MCP ツールは
   その間 10 秒ごとに `notifications/progress` を出す（opencode の 60 秒を無効化する既存の
   手）。**opencode のチャット要求を CP が何秒まで握れるか**（SDK 側の timeout）は P0 で
   測る（未解決 1）。「1 要求で 30 分の窓を買う」のは、その provider／モデルを選んだ本人の
   opt-in であり、0070 の損益分岐に相当するものは無い（代替は「使えない」だけ）。

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

7. **コントローラは 0070 の設計を N エンジンに一般化して 1 本にし、VOICEVOX もその 1 つに
   する。** `decideEngineAction` は純関数のまま、エンジンの表（サービス名・URL・ヘルスの
   URL・アイドル窓・起動期限・クールダウン）を取る。需要時計は `SettingsStore` に永続化
   （1 分に 1 回）、`DescribeServices` に短い TTL キャッシュ、起動失敗は `events[]` で診断して
   クールダウン、`starting` にもアイドル窓を適用（0070 決定 5・6・9・10）。**MI 固有の
   追加が 1 つ**: desired を 0 にしてもインスタンスは MI のスケールインまで残り、その間も
   管理料と EC2 料金は走る。`running | starting | stopped | draining` の 4 値にし、
   `draining` の長さを P0 で測る。モードは 0070 決定 7 のとおり `off / on / ondemand` を
   エンジンごとに。

8. **スタックは `60-engines.yaml`（任意採用）。`30-ingress` へ渡すのは SSM パラメータ名 1 つ。**
   `50-tts` の型で `00-network` と `20-platform` を import し、`30-ingress` には依存しない。
   中身: capacity provider ×2（属性ベースの選択）、MI 用の infrastructure role とインスタンス
   プロファイル、S3 バケット（モデル）、取り込みタスク定義、エンジンのタスク定義 ×3 と
   サービス ×3（`DesiredCount` は**書かない**。0070 実測 2）、Cloud Map の A レコード ×3、
   SG、ログ。**`30-ingress` にエンジンごとの 2 パラメータを足す余裕は無い**（残り 9.2 KB に
   6 パラメータと条件と env で 3 KB 弱）。代わりに `60-engines` がサービス名と URL を
   まとめた JSON を SSM に書き、`30-ingress` は `AF_ENGINES_SSM_PARAM` 1 本を渡す。CP は
   起動時に読む（SSM の読みは CP タスクロールに既にある）。**新しい IAM が要る**——MI の
   infrastructure role、インスタンスプロファイル、エンジンの S3 読み、取り込みの S3 書きと
   秘密の読み。0070 の「IAM 追加ゼロ」はここでは成り立たない。

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

## 未解決の点——P0 を書く前に潰すこと

1. **CP が opencode の要求を何秒握れるか。** opencode（Bun の fetch、`@ai-sdk/openai-compatible`）
   のクライアント側 timeout を実測する。600 秒より短ければ、決定 5 の「起こして待つ」は
   llm では成立せず、初回は 503 を返して**モデルに「N 分後に再試行」と言わせる**形に落とす。
2. **MI のコールドスタートとドレイン。** desired 1 → インスタンス起動 → 2.3〜5 GB の pull →
   S3 から 17 GB → `/health` ok までの各区間、および desired 0 → インスタンス終了までの
   時間（管理料が止まるのはそこ）。AWS の参照構成は 13 分と言うが、それはアラームと
   14 GB のイメージ込みである。
3. **G 系の vCPU クォータ。** 新しいアカウントは「Running On-Demand G and VT instances」が
   **0** のことがある。standup の前提条件として書き、0 なら standup がそう言う。
4. **`image` 役の既定サイズ。** g6f.2xlarge（6 GB）で SDXL が `--offload-to-cpu` 込みで何秒か、
   g6.xlarge で何秒か。ComfyUI の SDXL fp16 は 8 GB 前後を使うので、ComfyUI を選ぶ配備は
   g6.xlarge が下限になりうる。
5. **Workspace 発の資格情報の再利用か、エンジン専用トークンか。** `/git/*` の PAT を
   そのまま使うと、メンバー単位の計上はできるがセッション単位はできない。セッション単位が
   要るなら、Agent が起動時に発行する短命トークンにする。

## 却下した案

- **CPU（Fargate 16 vCPU / c8g.4xlarge）で llama.cpp。** 値段は L4 1 枚と同じ
  （$0.79〜0.99/h 対 $1.26/h）で、MoE（3B active）の生成は使えても、**コーディング
  エージェントの 20k トークンの prefill が CPU では分単位**になる。安くもならず遅い。
  llama.cpp の CPU イメージが 297 MB であることは、この判断を変えない。
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

## フェーズ

- **P0 — 土台と llm。** `60-engines.yaml`（capacity provider・S3・取り込みタスク・llm の
  サービス）、standup / update / teardown / capture-env、ECR への公式イメージ複製、CP の
  ゲートウェイ `/engine/llm/v1/*`（起こして待つ・streaming・usage の記録）、一般化した
  コントローラ、opencode の provider 注入。完了の定義は、**opencode の起動メニューに
  `llamacpp/<model>` が出て、停止中のエンジンに最初の要求を投げると数分後に答えが返り、
  30 分黙るとインスタンスが消えること**。ここで未解決 1〜3 の数字が出る。llm を先にする
  のは、要求されたのが画像だからではなく、**アプリ側のコードが最も少なく土台を検証できる**
  からである（opencode 側はゼロ改修）。
- **P1 — image（sd-server）。** `image` 役のサービス、Agent の provider `sdcpp`（transport は
  CP ゲートウェイ。0069 決定 3 の「テナント層は CP 経由」と同じ）、`Caps` は (provider,
  モデルファイル) 単位、generate / edit / inpaint、進捗通知。未解決 4。
- **P2 — ComfyUI。** 自前イメージ、`/engine/comfy/` のペイン、provider `comfy` とワーク
  フローテンプレート、sd-server との排他。
- **P3 — llm を codex と claude へ。** codex は `model_providers` に `base_url`＋
  `wire_api = "responses"`、claude は `ANTHROPIC_BASE_URL`。どちらも**モデル選択の意味が
  変わる**（利用者のログインでなくフリートの箱に課金が移る）ので、起動メニューの表示と
  同意を先に設計する。llama.cpp の `/v1/messages` は thinking ブロックを落とす既知の問題
  （#20090）があり、Claude Code は背景で Haiku 宛の要求を多数出す。
- **P4 — 根拠つきで安くする。** MI の Spot、`image` の g6f への縮小、router モードでの
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
  （opencode 1.18.29）。
- 本リポジトリ: `control-plane/main.go`（proxy env）、`egress_policy.go`、
  `preview_host_serve.go`、`tts_ecs.go`、`internal/runtime/runtime_ecs.go`（awsvpc・`WsSg`）、
  `workspace/agent/internal/imagegen/imagegen.go`、`internal/agents/opencode/{auth,models}.go`、
  `deploy/aws/ecs/cfn/{00-network,20-platform,30-ingress,50-tts}.yaml`。
