# EC2 スロットプール（`AF_RUNTIME=ecs-ec2`）の実機ハーネス

`control-plane/runtime_ecs_ec2_live_test.go` を **実 AWS** に対して回すための最小基盤。
docs/log/64 §64.16 の計測はこれで取った。

```bash
# 作業ディレクトリ（~/af-ec2c）へ写して使う想定なので、checkout の場所は渡す
cp deploy/aws/ecs/harness/*.sh ~/af-ec2c/
AF_HARNESS_REPO_DIR=$PWD AF_HARNESS_NAT=1 ~/af-ec2c/setup.sh   # 基盤を作る（state.env を書き出す）
(set -a; . ~/af-ec2c/state.env; set +a; cd control-plane && go test -run TestECSEC2Live -v -timeout 40m .)
~/af-ec2c/teardown.sh                                          # 全消去 → 残存 0 を表示
```

⚠️ **`AF_HARNESS_REPO_DIR` は checkout の場所**（`cfn/20-platform.yaml` と
`cfn/40-ec2-pool.yaml` を読む）。repo の中でそのまま実行するときだけ省略できる。
以前はここが**特定の worktree への絶対パス**で埋められており、その worktree が消えた後は
誰が動かしても動かなかった。**自分の作業場のパスを焼き込まないこと。**

守ること:

- **deploy → 検証 → teardown を 1 セッションで閉じる。** 置き忘れたスロットは時間課金で、
  ボリュームは確保した分だけ課金され続ける。`teardown.sh` の最後は残存確認の一覧で、
  **すべて空**になるまで終わりではない。
- **基盤は `cfn/40-ec2-pool.yaml` をそのまま立てる。** 手書きの launch template を置かない
  ——検証したいのは出荷するテンプレートそのものだから。同スタックが参照する
  00-network / 20-platform の export だけ、ダミースタック（`exports.yaml`）で供給する。
- NAT / ALB / RDS は作らない（デフォルト VPC のパブリックサブネット 1 本）。タスク ENI に
  パブリック IP は付かないので、**タスクからの外向き通信は無い**（entrypoint の boot-install は
  WARN で流れる）。起動と永続の検証には足りるが、CLI の導入まで見たいなら NAT が要る。
- 失敗して作り直すときは **`AF_ECS_EC2_LIVE_SUFFIX=b`** を付ける。ECS は削除直後の同名サービス
  作成を `Create service is not idempotent` で拒む。

## `bench-tts-engine.sh` —— VOICEVOX エンジンのサイズを、同じ文章で比べる

[ADR 0070](../../../../docs/decisions/0070-tts-ondemand-engine.ja.md) P3 の実測に使ったもの。
`<vCPU 単位>x<メモリ MiB>` ごとに **RunTask でエンジンを 1 本だけ建て**、VPC 内から合成を
測り、表にして落とす。測るのは 4 つ: 短文・中文・長文の 1 文、**回答 1 本を文単位で**（実
クライアントが作る唯一の形）、**実クライアントの要求サイズでの同時実行**、そして
**単発要求の OOM の壁**（これがコントロールプレーンの 1 要求上限＝決定 17 が下回るべき数字）。

```bash
AWS_PROFILE=af-sandbox AWS_REGION=ap-northeast-1 \
  deploy/aws/ecs/harness/bench-tts-engine.sh --sizes 2048x4096,1024x2048
```

⚠️ **サービスにもスタックのパラメータにも触らない。** エンジンの desired count は CP の
オンデマンドコントローラのものなので、サービスを 1 に上げて測ろうとすると**アイドル規則に
1〜2 分で止められ**、測定は「来ないタスク」を待って固まる（実際に踏んだ）。だからサイズは
RunTask のタスクレベル override で与え、Cloud Map にもサービスにも入らないタスクとして建てる。

⚠️ **最後の段はエンジンを殺す**（OOM の壁を探すため）。サイズごとにエンジンは 1 本なので、
順番は「直列 → 同時実行 → 単発の梯子」で固定してある。

⚠️ **実行中のこのスクリプトを編集しないこと。** bash はスクリプトをオフセットで読み進める
ので、走っている最中に長さが変わると残りが化ける（この作業中に 1 回踏んだ）。編集したいときは
`/tmp` へ写して走らせるか、終わるのを待つ。

## `bench-image-engine.sh` / `bench-image-engine.py` —— 同じ L4 で ComfyUI に複数のチェックポイントを描かせる

[ADR 0072](../../../../docs/decisions/0072-engine-model-catalog.ja.md) の実測に使ったもの。
`60-engines` の **image 役の capacity provider に RunTask で 1 タスクだけ流し**、4 コンテナで
測って S3 に落とす: fetch（バケットから ComfyUI の `models/` 配置へ。ファイルごとに秒数）→
comfy（コミュニティイメージの焼き込みは古いので起動時に `--comfy-tag` へ checkout＋pip。
`--phases` の `label:flags` ごとに ComfyUI を建て直す）→ bench（SDXL / Z-Image-Turbo /
FLUX.2 klein 4B を各 2 回、往復、LoRA の有無を同 seed、512px）→ upload（絵と
`results.jsonl` を `s3://<bucket>/bench/<run>/` へ）。

```bash
AWS_PROFILE=af-sandbox AWS_REGION=ap-northeast-1 \
  deploy/aws/ecs/harness/bench-image-engine.sh --phases "default:,highvram:--highvram"
```

⚠️ **サービスにもスタックにも触らない**（`bench-tts-engine.sh` と同じ理由）。モデルは先に
取り込みタスクでバケットに入れておく（README の「Getting a model into the catalogue」。
必要なキーはスクリプトの `MODELS`）。ComfyUI のイメージは公式が無いので、コミュニティ
イメージを `crane copy` で ECR の `af-engbench:comfyui` に写してから走らせる（手順は
スクリプトの冒頭。GHCR から NAT 越しに毎回 5.4 GB を引かないため）。終わったらリポジトリごと消す。

⚠️ **使った箱では走らない。** タスクの匿名 host volume は前のタスクの分が片づかないので、
同じ 60 GB の箱に 2 本目を流すと fetch が `No space left` で落ちる（実測）。スクリプトは
Managed Instances が箱を回収する（最後のタスクから約 8 分でクラスタの一覧から消える）のを
待ってから RunTask する。停止直後の箱に載ったタスクは pull 開始まで **9.5 分** PENDING
だった（新しい箱なら 30 秒）——待つほうが早い。

⚠️ **ComfyUI に `--cache-none` を付けない。** ローダーノードの出力＝モデル本体がキャッシュ
されず、**毎要求ディスクから読み直す**（warm の SDXL 1024px が 19 秒でなく 57 秒。実行後の
VRAM 使用が 280 MiB に戻るのがその印）。`--highvram` を足しても直らない。

⚠️ **実行中のこのスクリプトを編集しないこと**（上と同じ）。`bench-image-engine.py` は起動時に
S3 へ写されるので、こちらは編集しても走行中の回には効かない。

## `probe-rtk.sh` —— rtk は「ロードする」だけでなく**使えるか**

上の基盤とは独立した単体の検査で、**AWS を何も作らない**。ワークスペースのコンテナの中で
走らせ、rtk が実際に子プロセスを起こし、claude の PreToolUse フックが Bash を書き換え、
削減が実際に計上されるところまでを 6 項目で見る（[ADR 0068](../../../../docs/decisions/0068-debian-13-base.ja.md) ⑥）。

```bash
docker exec -i <workspace container> sh -s < deploy/aws/ecs/harness/probe-rtk.sh
```

⚠️ **素のイメージに対して走らせても答えにならない。** `BAKE_AGENT_CLIS=0` で焼いた
イメージには rtk が入っておらず、entrypoint が `~/.local/bin` へ導入する。**boot-install を
通った home** の側で走らせること（実機での踏み方は ADR 0068 の ⑤⑥ 実測を参照）。

## `engprobe.yaml` / `probe-managed-instances.sh` —— ECS Managed Instances の起動とドレインを測る

[ADR 0071](../../../../docs/decisions/0071-self-hosted-inference-engines.ja.md)（自前の推論
エンジンを GPU でオンデマンドに動かす）の未解決 2 を測るための使い捨てスタック。**共有クラスタ
に capacity provider を 1 本足す**ので、`60-engines.yaml` の本番と同じ 2 つの約束を守る:
関連付けは FARGATE / FARGATE_SPOT を含む完全なリストで渡し（API はリストを置き換える）、
`DefaultCapacityProviderStrategy` は空のまま。

```bash
AWS_PROFILE=af-sandbox deploy/aws/ecs/harness/probe-managed-instances.sh up
AWS_PROFILE=af-sandbox deploy/aws/ecs/harness/probe-managed-instances.sh measure
AWS_PROFILE=af-sandbox deploy/aws/ecs/harness/probe-managed-instances.sh down   # platform の teardown より先に
```

2026-09-07 の実測（c6a.large・CPU イメージ 297 MB・モデル 1.1 GB を HF から取得）:
desired 1 → **+10 秒でインスタンス起動、+68 秒でタスク RUNNING、+109 秒で listen**。
desired 0 → **+10 秒でタスク消滅、+93 秒で terminated**。箱の同定は**タスク経由**で行うこと
——`list-container-instances` にはスロットプールも並ぶ。

同日、G 系クォータの承認後に **GPU（g6.xlarge）で通した**。パラメータは
`GpuCount=1 AllowedInstanceTypes=g6.xlarge ImageUri=…:server-cuda`
`ModelRef=unsloth/Qwen3-Coder-30B-A3B-Instruct-GGUF:Q4_K_M ExtraArgs="-ngl,99,-c,32768,--jinja"`
`TaskCpu=4096 TaskMemory=14336 StorageGiB=80 VCpuMin/Max=4 MemMin/Max=16384`、
画像側は `SdEnabled=true`。実測は **+232 秒でタスク RUNNING、+2,351 秒で listen**
（うち 1,846 秒が `-hf` のダウンロード）、**ドレイン 427 秒／463 秒**。

S3 経路（`LlamaModelS3Key`、取り込みは `IngestTaskDef` を Fargate で `run-task`）と ComfyUI
（`ComfyEnabled=true`・コミュニティイメージ・計測専用）も同日に通した: **S3 → 箱は 105〜147 MB/s**
（20.8 GB を 198 秒、6.9 GB を 45 秒）、HF は**ファイルで 50 倍違う**（SDXL 236 MB/s、Qwen3-Coder
GGUF は `-hf` 9.6 MB/s・Fargate の curl 4.2 MB/s）。ComfyUI は listen まで 504 秒（うち 5.1 GB の
pull が 437 秒）、SDXL 1024px が温まって 8 秒、VRAM 6.9 GB。**S3 から起動する llama は
desired 1 → +527 秒で listen**（取得 179 秒は pull 178 秒と並行、VRAM ロード 267 秒）。

CloudFormation の作法で踏むもの 3 つ:

- **capacity provider の `InstanceRequirements` は排他規則がある。** `InstanceGenerations:
  [current]` と `AllowedInstanceTypes: [g6.xlarge]` は同時に書けず（世代付きの型名は不可）、
  `AcceleratorCount` を非 0 にするなら `AcceleratorTypes` も要る。
- **`DesiredCount` はスタック更新のたびに宣言値 0 へ戻る**（Service リソースが更新されるとき）。
  更新のあとは `update-service --desired-count 1` を自分で撃ち直す。
- **エンジンへは SSM のポートフォワードで届く**（SG を触らない）:
  `aws ssm start-session --target ecs:<cluster>_<task-id>_<runtime-id> --document-name
  AWS-StartPortForwardingSession --parameters '{"portNumber":["8080"],"localPortNumber":["18100"]}'`。
  `EnableExecuteCommand: true` とタスクロールの `ssmmessages:*` が前提。
