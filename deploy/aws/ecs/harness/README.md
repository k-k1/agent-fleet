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
