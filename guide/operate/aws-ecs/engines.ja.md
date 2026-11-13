---
audience: "AWS の ECS 配備に、GPU で動く自前の推論エンジン（`60-engines`）を足す配備管理者"
source_of_truth: "`deploy/aws/ecs/cfn/60-engines.yaml` と `cfn/PARAMETERS-60-engines.md`、`deploy/aws/ecs/standup.sh`、Control Plane の推論エンジンの制御（`control-plane/engines.go`・`engine_gateway.go`）"
---

# 推論エンジンを足す

[English](engines.md) | 日本語

ECS の配備に、GPU で動く自前の推論エンジンのスタック `60-engines` を足します。
GPU のインスタンスは、だれかが使っているあいだだけ存在します。Control Plane がリクエストに合わせて調達し、アイドルになると終了します。

## 全体像

| あなたがやること | 起きること |
|---|---|
| G 系の vCPU のクォータを上げ、取得した状態に `params/60-engines` を置いて `standup.sh` を実行する | イメージを ECR にコピーし、`60-engines` を空のまま構築し、サービスを 0 台にする |
| 管理の「推論エンジン」でモデルを取り込み、有効にする | モデルが S3 Bucket に置かれ、メンバーが選べるようになる |
| 何もしない | 最初のリクエストで Control Plane が GPU のインスタンスを調達してエンジンを起動し、アイドルが続くと終了する |

モードの選び方・モデルの取り込み・状態の見方は、管理画面の側のページにあります。

- [自前の GPU で推論エンジンを動かす](../engines/gpu.ja.md)
- [推論エンジンにモデルを取り込む](../engines/models.ja.md)
- [推論エンジンの状態と履歴を見る](../engines/status.ja.md)

## 2 つの役

| 役 | エンジン | メンバーが得るもの | コールドスタート |
|---|---|---|---|
| `llm` | llama.cpp（`llama-server`） | opencode の起動の選択肢に `llamacpp/<model>` | 約 527〜586 秒 |
| `image` | ComfyUI（`comfy`） | `generate_image` ツールのプロバイダー `comfy`。生成・編集・インペイント | インスタンスしだい（8〜88 秒） |

- それぞれ単独で選べます。`<Role>Enabled=true` がなければ、その役のサービスは作られません
- **2 つの役がインスタンスを共有することはありません。** CUDA は VRAM が足りないと遅くなるのではなく落ちます。30B のモデル（20.9 GB）と SDXL（7.4 GB）は、1 枚の L4 に両方は載りません
- 動いているあいだは安くありません。**g6.xlarge（NVIDIA L4、24 GB）は全部込みで $1.26/時**、止めなければ月 $918 です

Fargate には GPU がないので、エンジンは EC2 のインスタンスで動きます。Control Plane が、このスタックの起動テンプレートを使って `CreateFleet(type=instant)` で買い、エンジンがアイドルになると終了します。タスクがなければインスタンスもない、という状態を Control Plane が保ちます。

## 始める前に

1. **G 系の vCPU のクォータを上げます。** 新しいアカウントは **0** で、引き上げは自動では承認されません。サポートケースが開き、数時間から数日かかります
   - 両方の役を動かすなら **16** を頼みます（`L-DB2E81BA`、「Running On-Demand G and VT instances」）。8 でも g6.xlarge を 2 台起動できますが、終了したばかりのインスタンスの 4 vCPU は、停止にかかる 7〜8 分のあいだクォータに数えられたままです。16 なら 1 台分の余裕があります
   - クォータは上限であって予約ではないので、余裕に料金はかかりません。24 を頼んでも費用は同じで、デプロイし直すときに停止中のインスタンスが重なっても足ります

   ```bash
   aws service-quotas get-service-quota --service-code ec2 --quota-code L-DB2E81BA --query 'Quota.Value'
   ```

2. **クラスターの容量プロバイダーの関連付けを持てるのは、1 つのスタックだけです。** API はリストを足すのではなく置き換えます。計測用のハーネス（`harness/engprobe.yaml`）をデプロイしているなら、先に `deploy/aws/ecs/harness/probe-managed-instances.sh down` で撤去します。エンジンはそのリストに入っていませんが、配備のほかの部分のために `FARGATE` と `FARGATE_SPOT` を持つのは `60-engines` です
3. **サービスにリンクされたロール `AWSServiceRoleForEC2Fleet` を用意します。** `standup.sh` が作ります。手で作るときは次のとおりです（2 回目以降の「has been taken」は無視します）

   ```bash
   aws iam create-service-linked-role --aws-service-name ec2fleet.amazonaws.com || true
   # and, for a deployment that declares a `spot` offer:
   aws iam create-service-linked-role --aws-service-name spot.amazonaws.com || true
   ```

4. **スタックを作る前に、イメージを ECR に入れます。** 理由は `50-tts` と同じで、空のリポジトリを指すサービスがあると、CloudFormation があとから復旧できない状態で止まります（[日本語の読み上げを足す](tts.ja.md#有効にする)）。`standup.sh` は、イメージの段階でこれを行います。手でやるときは次のとおりです

   ```bash
   crane copy ghcr.io/ggml-org/llama.cpp:server-cuda <acct>.dkr.ecr.<region>.amazonaws.com/af-llamacpp:server-cuda
   # the image role:
   crane copy ghcr.io/k-k1/agent-fleet/comfyui:v0.37.0 <acct>.dkr.ecr.<region>.amazonaws.com/af-comfyui:v0.37.0
   ```

   - NAT 経由の GHCR は 12〜14 MB/s でした。527 秒のコールドスタートのうち 178 秒が取得だったので、ECR に置く意味があります
   - `standup.sh` が `image` の役のイメージをコピーするのは、`ImageEnabled=true` のときだけです

### イメージの中身を固定する

| オプション | 固定するもの |
|---|---|
| `standup.sh --llm-digest sha256:<...>` | llama.cpp のコピーの中身。`server-cuda` は中身が入れ替わるタグで、ツール呼び出しの解釈は llama.cpp のビルドで決まる。`LlmImageTag` は変えない。付けなければ、コピーは今までどおり |
| `standup.sh --comfy-digest sha256:<...>` | ComfyUI のコピーの中身。そのタグは `comfyui-image.yml` の再実行で上書きされうる |
| `update.sh --comfy-digest` | 以前の `ImageComfyImageTag` の既定値からスタックを移すときに、`update.sh` が作るコピー |

詳細は `deploy/aws/ecs/cfn/PARAMETERS-60-engines.md` の `LlmImageTag` にあります。

## 有効にする

`standup.sh` は、取得した状態に `params/60-engines` のファイルがあるときだけ `60-engines` を構築します。

```bash
# ~/.config/agent-fleet/deploy/<profile>.<region>/params/60-engines
# Every parameter has a default; standup.sh overrides NetworkStackName /
# PlatformStackName / ServiceConnectNamespace from what it already knows.
ServiceConnectNamespace=af.internal
# Whether each role exists at all: its service, its DNS name, its row in the engine table.
LlmEnabled=true
# What the instance is: the ladder of GPU rungs (the first is the default), or the same list
# with a `buy` column. This is the ONLY place the instance's shape is declared.
LlmInstanceClasses=l4|L4 24GB|22000|g6.xlarge,g5.xlarge|4-8|15000-65536|1.17
# The instance's root volume (GiB). The models land on it, so it has to hold every model the
# role pulls from S3 plus the images.
LlmStorageGiB=120
# The image role, if you want it. Leave this out and no image service exists.
ImageEnabled=true
```

| パラメータ | 中身 |
|---|---|
| `<Role>Enabled` | その役があるかどうか。サービス・DNS 名・エンジン表の行がすべてこれで決まる。何を読み込むかは、ここでは決めない |
| `LlmInstanceClasses` | インスタンスの形。GPU の段のはしごで、最初の段が既定。`buy` の列を足した形でも書ける。インスタンスの形を宣言できるのはここだけで、どちらも宣言しない配備はエンジンを起動できない |
| `LlmStorageGiB` | ルートボリューム（既定は 120 GiB）。S3 から取るモデルとイメージがすべて収まるサイズにする |

- 「エンジンがいつまでも来ない」原因は、1 つの段に複数のタイプを書いたか、1 つの AZ で容量が足りないかのどちらかです。意図したインスタンスがいちばん安くなるようにしておきます。理由は `PARAMETERS-60-engines.md` の「The instance classes」と「The offers」にあります
- `standup.sh` は、イメージを ECR にコピーし、`llm` のエンジンの API キーがなければ SSM に作り（`/af-ws/engine-llm-key`、SecureString）、読み上げのエンジンのあと・`30-ingress` の前に `60-engines` をデプロイし、**新しく作った**サービスを 0 台にして、スタックの出力 `EnginesSsmParam` を `30-ingress` に渡します
- `image` の役には自分のキーがありません。ComfyUI には認証の仕組みがないので、セキュリティグループ（Control Plane からの 8080 だけ）がアクセス制御のすべてです

**最初の構築は 1 回で、空のまま構築されます。** フェッチのサイドカーは「何も置かれていない」を成功として扱い、エンジンは待機します。空の S3 Bucket に `LlmEnabled=true` で構築すると、役は立ち上がって 0 台になり、最初のモデルを待ちます。あとから役を足すのも、同じ 1 行です。

> [!CAUTION]
> **`<Role>Enabled` がなかった頃の配備を上げるときは、更新の前に `<Role>Enabled=true` を足します。** 以前は `LlmModelS3Key` / `ImageModelS3Key` でサービスの有無が決まっていましたが、そのパラメータはなくなりました。`standup.sh` は取得したキーをスイッチに読み替えます。`update.sh` と手で実行する `cloudformation deploy` はパラメータを渡さないので、その行がないと、更新はふつうのスタックの更新として、**動いているエンジンのサービスを警告なしに削除します**。詳細は `PARAMETERS-60-engines.md` の「Upgrading: the model parameters are gone」にあります。

## モデルを手で S3 に置く

カタログに入れるには 2 つの段階があり、わざと分けています。

1. **置く**: ファイルを S3 に入れます。ふつうは管理の「推論エンジン」から取り込みます（[推論エンジンにモデルを取り込む](../engines/models.ja.md)）。下の `run-task` は、Control Plane がインターネットに届かない配備のための、同じことを手でやる方法です
2. **有効にする**: 同じ画面で行をオンにします。どの方法で置いても、行は無効の状態で届きます。「ファイルが S3 Bucket にある」と「メンバーが使ってよい」は別のことです

### 置き場所

キーは ComfyUI の配置に合わせてあり、1 つのツリーをすべてのエンジンが使います。

| キー | 中身 |
|---|---|
| `llm/<name>.gguf`、`llm/loras/` | llama.cpp のモデルと LoRA |
| `image/checkpoints/` | 1 ファイルのチェックポイント（SDXL など） |
| `image/loras/`・`image/vae/`・`image/text_encoders/`・`image/diffusion_models/` | 分かれたモデルの部品 |

インスタンスは S3 Bucket をそのままミラーします。`image/checkpoints/x.safetensors` は `/models/image/checkpoints/x.safetensors` になります。キーがそのままパスなので、別の名前を対応させて管理する必要はありません。

> [!WARNING]
> 古い配備では、`image/sd_xl_base_1.0.safetensors` が最上位に置かれています。サーバー側のコピーで移し（NAT も再ダウンロードも使いません）、**同じ変更で**管理画面のカタログの行のキーも直します。行はキーをそのまま指すので、ファイルのない行があると、デプロイするときではなく次のコールドスタートで、フェッチのサイドカーが失敗します。
>
> ```bash
> OLD=image/sd_xl_base_1.0.safetensors NEW=image/checkpoints/sd_xl_base_1.0.safetensors
> size() { aws s3api head-object --bucket "$BUCKET" --key "$1" --query ContentLength --output text; }
> aws s3 cp "s3://$BUCKET/$OLD" "s3://$BUCKET/$NEW" \
>   && new=$(size "$NEW") && old=$(size "$OLD") \
>   && [[ "$new" =~ ^[0-9]+$ ]] && [ "$new" = "$old" ] \
>   && aws s3 rm "s3://$BUCKET/$OLD"      # removed only after the copy succeeded and the sizes match
> ```
>
> 元のファイルを消すのは、コピーが成功し、コピー先が同じサイズのときだけです。そうでなければ何も消しません。

### 取り込みのタスクを動かす

エンジンが Hugging Face と通信することはありません。モデルは Fargate のタスクがスタックの S3 Bucket に 1 回置き、エンジンは起動時に S3 から取ります。

```bash
# The sha256 is siblings[].lfs.sha256 from https://huggingface.co/api/models/<repo>?blobs=true
aws ecs run-task --cluster <cluster> --launch-type FARGATE \
  --task-definition af-af-ecs-engines-ingest \
  --network-configuration 'awsvpcConfiguration={subnets=[subnet-…],securityGroups=[sg-…],assignPublicIp=DISABLED}' \
  --overrides '{"containerOverrides":[
     {"name":"fetch","environment":[
        {"name":"URL","value":"https://huggingface.co/unsloth/Qwen3-Coder-30B-A3B-Instruct-GGUF/resolve/main/Qwen3-Coder-30B-A3B-Instruct-Q4_K_M.gguf"},
        {"name":"SHA256","value":"<the lfs sha256>"}]},
     {"name":"upload","environment":[{"name":"KEY","value":"llm/Qwen3-Coder-30B-A3B-Instruct-Q4_K_M.gguf"}]}]}'
```

`image` の役も同じタスクで、URL とキーが違うだけです。SDXL の 6.94 GB は、取得と sha256 の検証に 161 秒、S3 への書き込みに 46 秒でした。

```bash
  --overrides '{"containerOverrides":[
     {"name":"fetch","environment":[
        {"name":"URL","value":"https://huggingface.co/stabilityai/stable-diffusion-xl-base-1.0/resolve/main/sd_xl_base_1.0.safetensors"},
        {"name":"SHA256","value":"31e35c80fc4829d14f90153f4c74cd59c90b779f6afe05a74cd6120b893f7e5b"}]},
     {"name":"upload","environment":[{"name":"KEY","value":"image/checkpoints/sd_xl_base_1.0.safetensors"}]}]}'
```

2 つめのチェックポイントも、URL とキーを変えて同じように呼びます。Apache-2.0 か OpenRAIL で、ゲートがなく、量子化せずに L4 に載るものを選びます。

```bash
  --overrides '{"containerOverrides":[
     {"name":"fetch","environment":[
        {"name":"URL","value":"https://huggingface.co/<owner>/<repo>/resolve/main/<file>.safetensors"},
        {"name":"SHA256","value":"<the lfs sha256>"}]},
     {"name":"upload","environment":[{"name":"KEY","value":"image/checkpoints/<file>.safetensors"}]}]}'
```

- 分かれたモデル（FLUX.2 klein、Z-Image など、`--diffusion-model`・テキストエンコーダー・VAE の組）も使えます。それぞれ `image/diffusion_models/`・`image/text_encoders/`・`image/vae/` に置きます
- **上書きするのは、上の例のとおり `environment` だけです。** コンテナに `EntryPoint` はなく、`Command` はスクリプトそのもの（`/opt/af/ingest-fetch.sh`、`/opt/af/ingest-upload.sh`）です。`command` を上書きするときは、シェルの文字列 1 つではなく argv の配列（`["/opt/af/ingest-upload.sh"]`）で渡します
- **取得の時間は読めません。sha256 も必ず渡します。** 同じ NAT を通しても、Hugging Face からは 4〜236 MB/s と幅があり、ファイルにも経路にも関係なく変わりました。S3 からはどの試行でも 104〜147 MB/s でした。モデルを S3 に置くのはこのためです。途中で切れた 17 GB の GGUF も読み込めてしまい、でたらめに答えます

**ゲートのあるリポジトリ**（Hugging Face の API で `gated: auto`。FLUX.1-dev、SD 3.5 など）は、運用担当者が Hugging Face でライセンスに同意し、そのアカウントのトークンを管理画面に登録しておく必要があります（[推論エンジンにモデルを取り込む](../engines/models.ja.md#トークンを登録する)）。

- Control Plane はトークンを自分のデータベースに暗号化して保管し、このスタックが必ず作るシークレットに書きます。CloudFormation の操作は要りません
- トークンを読むのは**取り込みのタスクだけ**で、エンジンのインスタンスには届きません。Control Plane も、書いた値を読み返せません（`PutSecretValue` だけで `GetSecretValue` がありません）

## メンバーから見えるもの

| 役 | 見えるもの |
|---|---|
| `llm` | 設定は要りません。エージェントが Control Plane にエンジンを尋ね、opencode の設定にプロバイダーとして書くので、起動の選択肢に `llamacpp/<model>` が出ます。GPU のインスタンスが止まっているあいだも出ます。選んで最初のメッセージを送るとインスタンスが起動し、エンジンが答えるまでリクエストを保ちます。答えは再送なしで、最初の試行で届きます |
| `image` | 既存の `generate_image` ツールのプロバイダーになります。ツールは**既定でオフで、メンバーごとにオン**にします。エンジンがあれば、`generate_image` は Codex の経路より先にこれを使い、編集とインペイントもできます。起動の選択肢には出ません。止まっているエンジンへの呼び出しは数分かかり、ツールは待つあいだ 10 秒ごとに進捗を通知します。これで opencode の 1 回の呼び出しの上限（60 秒）に切られません |

`llama-server` はルーターとして動き、有効にしたモデルがそれぞれ自分のコンテキストウィンドウで選択肢に並びます。管理者が考えることが 2 つあります。

- 有効にした GGUF はすべてインスタンスに同期されるので、合計のサイズの分だけコールドスタートが延びます（画面がモデルごとに見積もります）
- `LlmModelsMax`（既定は **1**）が、同時に持つモデルの数を決めます。上限に達しているときに別のモデルを頼むと、いまの答えが終わるのを待ってから、モデルを読み込み直します。画面に、VRAM にあるモデルと入れ替えの回数が出ます

## 知っておくこと

- **テンプレートに `DesiredCount` はわざと書いていません。** `50-tts` と同じ理由で、書くとスタックを更新するたびに数が戻り、答えの途中で GPU のインスタンスが止まります
- **既定の容量プロバイダーの戦略は空で、空のままにします。** 戦略があると、`LaunchType: FARGATE` を明記していないサービスが GPU のインスタンスに載ります
- **止めたエンジンは、数分のあいだ料金がかかります。** `desired 0` のあと、GPU のインスタンスでは 427 秒と 463 秒かかりました（CPU のインスタンスでは 93 秒）。アイドルの時間を短くしても縮むのはその時間だけで、この分は減りません
- **コールドスタートは秒ではなく分です。** S3 から、イメージが ECR にある状態で、待ち受けまで 527 秒でした。イメージの取得 178 秒と S3 からの取得 179 秒が並んで進み、そのあと 18.5 GB を VRAM に読み込むのに 267 秒です。Control Plane がそのあいだリクエストを保つので、利用者には最初の答えが遅いだけで、エラーにはなりません
- **`image` の役のアイドルの時間は `llm` の半分（900 秒）です。** 画像は、まわりに会話のない 1 回のリクエストだからです
- **ComfyUI は `/health` ではなく `/system_stats` で確かめます。** 認証なしですぐ答えるので、チェックポイントが読み込まれたかではなく、生きているかの確認です。`comfy` はリクエストごとにワークフローを組み、その中で名前を挙げたチェックポイントを使うので、読み込み済みのチェックポイントというものがありません
- **コントローラーが自動で行った操作は、すべて監査ログに残ります**（`engine.llm.auto` / `engine.image.auto`、実行者 `engine-<role>-controller`）

## 自動の起動と停止を調整する

Control Plane 側のパラメータは `EnginesSsmParam` の **1 つ**だけです。`30-ingress` のテンプレートには余裕が少ないので、エンジンの表は SSM の `/af-ws/` の下に置きます。そこは `CpTaskRole` がもともと読める範囲なので、権限は増えません。ほかはすべて Control Plane の環境変数で、このテンプレートはどれも設定しません。

| 環境変数 | 既定 | 中身 |
|---|---|---|
| `AF_ENGINE_WAKE_TIMEOUT` | 900 | エンジンが立ち上がるあいだ、**ストリーミング**のリクエストを保つ時間。527 秒のコールドスタートに、遅い取得の余裕を足した値 |
| `AF_ENGINE_PLAIN_HOLD` | 45 | **ストリーミングでない**リクエストを保つ時間。ハートビートがないので、前段のプロキシのアイドルのタイムアウトより短くします（`30-ingress` は ALB を 60 秒にします）。超えると `503 engine_waking` と `Retry-After` を返し、呼び出し側がやり直します。`AF_ENGINE_WAKE_TIMEOUT` を超えることはありません |
| `AF_ENGINE_LLM_CONTROL_INTERVAL_SEC` | 30 | 見に行く間隔。**`0` で制御を完全に止めます** |
| `AF_ENGINE_LLM_IDLE_SEC` | スタックの `LlmIdleSec`（1800） | リクエストがないまま、この時間がたつと止める。**`0` で止めません**。起動の期限より短くはなりません |
| `AF_ENGINE_LLM_START_DEADLINE_SEC` | スタックの `LlmStartDeadlineSec`（900） | この時間までに `running` にならない起動は失敗とみなす。**実際のコールドスタートより長くします**。300 秒では、GPU の起動がすべて失敗と記録され、クールダウンが倍々に延びます |
| `AF_ENGINE_LLM_FAIL_COOLDOWN_SEC` | 900 | 失敗した起動が次の起動を止める時間。続けて失敗するたびに倍になります（最大 16 倍） |
| `AF_ENGINE_LLM_WINDOW_SEC` | 300 | 需要を数える時間枠。1 回のリクエストが 1 単位です。推論ではリクエストそのものが意図で、読み上げのように Polly が代わりに読むことはありません |

- `AF_ENGINE_LLM_*` には、同じ意味の `AF_ENGINE_IMAGE_*` があります。既定は `image` の役のスタックのパラメータ（`ImageIdleSec` は 900、`ImageStartDeadlineSec` は 900）から取ります
- 真ん中の名前はエンジンのキーです。3 つめの役ができても同じ形になります
- `AF_ENGINE_WAKE_TIMEOUT` はすべての役で共通です

## 最初のリクエストを保つ

止まっているエンジンの起動には数分かかり、opencode は 300 秒であきらめます。ただし、経過時間ではなく**無音の時間**で切ります。

- Control Plane は、ストリーミングのリクエストにすぐ `200 text/event-stream` を返し、エンジンの最初のバイトが届くまでハートビート（SSE のコメント行を 10 秒ごと）を送ります
- **300 秒は無音の上限で、待ち時間の上限ではありません。** 400 秒待っても、再送なしで答えが届きました
- `503` と `Retry-After` は、ストリーミングでないリクエストと、本当に起動に失敗したときだけに使います。503 はクライアントの限られた再試行を 1 回使ってしまい、その回数はこちらからはわかりません

ストリーミングでないリクエストは、900 秒も保てません。決めるのは Control Plane ではなく前段です。

- ALB の `idle_timeout.timeout_seconds`（`30-ingress` では 60 秒）は、1 分のあいだ応答のバイトが流れない接続を閉じます。止まっていた `image` のエンジンへの最初の `generate_image` は、エンジンが 165 秒かかったために `503` で 60 秒ちょうどに切れました
- そこで、ストリーミングでないリクエストは前段より短い `AF_ENGINE_PLAIN_HOLD`（45 秒）で `503 engine_waking` と `Retry-After` を返します。ワークスペースの `comfy` プロバイダーは自分の 16 分の範囲で尋ね続けるので、1 回のツール呼び出しで 1 枚の画像が返ります
- **この ALB より厳しいプロキシを前に置くときは、`AF_ENGINE_PLAIN_HOLD` をそれに合わせて下げます。** Control Plane はそのタイムアウトを知る方法がありません

## 関連

- [自前の GPU で推論エンジンを動かす](../engines/gpu.ja.md)
- [推論エンジンにモデルを取り込む](../engines/models.ja.md)
- [推論エンジンの状態と履歴を見る](../engines/status.ja.md)
- [日本語の読み上げを足す](tts.ja.md)
- [費用を抑える](cost.ja.md)
