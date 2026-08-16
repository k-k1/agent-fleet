# EC2 スロットプール（`AF_RUNTIME=ecs-ec2`）の実機ハーネス

`control-plane/runtime_ecs_ec2_live_test.go` を **実 AWS** に対して回すための最小基盤。
docs/64 §64.16 の計測はこれで取った。

```bash
./setup.sh                    # 基盤を作る（state.env を書き出す）
source ~/af-ec2c/state.env    # setup.sh の出力先
cd ../../../../control-plane && go test -run TestECSEC2Live -v -timeout 40m .
./teardown.sh                 # 全消去 → 残存 0 を表示
```

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
