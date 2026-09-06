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
