---
audience: "AWS の ECS 配備をすべて消す配備管理者"
source_of_truth: "`deploy/aws/ecs/teardown.sh`（この手順を実行するスクリプト）"
---

# 撤去する

[English](teardown.md) | 日本語

ECS の配備を、途中で止まらない順番ですべて消します。
ふつうは `./teardown.sh --profile <p> --region <r>` を使います。このページは、スクリプトが実行する手順そのものです。どこかの手順が失敗したら、ここに戻ります。

## 全体像

| あなたがやること | 起きること |
|---|---|
| 先に `capture-env.sh` で記録する | 建て直すためのパラメータが残る |
| `teardown.sh` を実行する | 消えるものの一覧と計画が出る |
| `teardown.sh --yes` を実行する | 下の手順 1〜11 を順に実行する |
| `--purge-retained` / `--purge-secrets` を付ける | 残す設定のものと `/af-cp/*` も消す |
| 最後に Elastic IP と S3 Bucket を自分で消す | 配備のものが何も残らない |

## 順番が大事な理由

配備が持つものの多くは、スタックの中に**ありません**。Control Plane が動きながら作ります。

- ワークスペースのサービス、EFS のアクセスポイント、SSM のパラメータ
- `WsRuntime=ecs-ec2` では、スロットのインスタンス、ホームの EBS ボリューム、ゴールデンスナップショット

先にスタックを消すと、これらが残り、それが握る依存関係で**削除が止まります**。下の順番は、`Persistence=delete` と `=retain` の実際の `ecs-ec2` の配備 2 つで最初から最後まで試し、どちらのアカウントでも点検の数がすべて 0 になったものです。

## 手順

### 1. Control Plane を先に止める

手順 2〜7 で消すものは、すべて Control Plane の帳簿にあります。Control Plane は必要に応じてスロットを起動するので、動いていると消したものを作り直します。

```bash
CL=af-af-ecs-platform      # cluster = af-<platform stack name>
aws ecs update-service --cluster $CL --service af-af-ecs-ingress-cp --desired-count 0
```

`teardown.sh` は、このあと読み上げのエンジンと推論エンジンのサービスも 0 台にします。

### 2. ワークスペースのサービスを消す

`af-ws-*` で、ワークスペースを起動したことのあるメンバーごとに 1 つあります。`delete-service --force` はサービスの Cloud Map のエントリも消すので、それを手で消さないでください（`ServiceNotFound` になります）。

```bash
for s in $(aws ecs list-services --cluster $CL --query 'serviceArns[]' --output text | tr '\t' '\n' | grep /af-ws-); do
  aws ecs update-service --cluster $CL --service "$s" --desired-count 0 >/dev/null
  aws ecs delete-service --cluster $CL --service "$s" --force >/dev/null
done
```

### 3. スロットのインスタンスとホームのボリュームを消す（`ecs-ec2` だけ）

スロットを終了するとルートボリュームは消えますが、**ホームのボリュームは消えません**。ホームはわざと外して残すので、残って料金がかかり続けます。

```bash
IDS=$(aws ec2 describe-instances --filters Name=tag:Name,Values='af-slot-*' \
  Name=instance-state-name,Values=pending,running,stopping,stopped \
  --query 'Reservations[].Instances[].InstanceId' --output text)
aws ec2 terminate-instances --instance-ids $IDS && aws ec2 wait instance-terminated --instance-ids $IDS
# then every leftover af-ws-*-home volume
aws ec2 delete-volume --volume-id <vol-id>
```

`teardown.sh` は、Control Plane が買った推論エンジンのインスタンスもここで終了します。

### 4. 登録が残ったコンテナインスタンスを外す

EC2 のインスタンスが消えたあとも、クラスターがそれを一覧に残すことがあります（あるアカウントでは 4 台中 3 台）。そのままだと、プラットフォームのスタックのクラスターの削除が失敗します。1 台ずつ `aws ecs deregister-container-instance --cluster $CL --container-instance <arn> --force` を実行します。

### 5. EFS のアクセスポイントを消す

`describe-access-points --file-system-id <efs>` で一覧し、1 つずつ消します。

- **これを忘れると、`delete-stack af-ecs-data` が止まります**
- ファイルシステムの ID は `af-ecs-data` の出力 `EfsId` です。`FileSystemId` ではありません。名前を間違えた点検のスクリプトは、何も言わずに何も消しません

### 6. SSM の `/af-ws/*` を消す

ワークスペースごとの DEK とトークンです。`/af-cp/*` は、`30-ingress` の前に自分で作った Control Plane のシークレットです。同じアカウントに建て直すなら残し、前提条件から試し直すなら消します。

### 7. ゴールデンスナップショットを消す

`describe-snapshots --owner-ids self` で一覧し、1 つずつ消します。焼いたホームごとに 50 GiB で、イメージのタグごとに 1 つあります。`teardown.sh` は、休止とバックアップのスナップショットも消します。

### 8. スタックを逆の順に 1 つずつ消す

```bash
for s in af-ecs-ingress af-ecs-engines af-ecs-tts af-ecs-pool af-ecs-platform af-ecs-data af-ecs-network; do
  aws cloudformation describe-stacks --stack-name $s >/dev/null 2>&1 || { echo "skip $s (not deployed)"; continue; }
  aws cloudformation delete-stack --stack-name $s
  aws cloudformation wait stack-delete-complete --stack-name $s || break   # stop on the first failure
done
```

> [!WARNING]
> **1 つずつ消して、待ちます。** CloudFormation は、export しているスタックの削除を、import しているスタックが残っているあいだは**取り消します**（`Cannot delete export … as it is in use by …`）。まとめて消すと、最後のほうは何も起きず、待つループだけが、もう取り消された削除を待ち続けます。

- 順番は `teardown.sh` と同じです。ingress → 推論エンジン（`60-engines`）→ 読み上げ（`50-tts`）→ スロットのプール（`40-ec2-pool`）→ platform → data → network
- 推論エンジン・読み上げ・プールのスタックは任意です。`af-ecs-engines` は推論エンジンを足したときだけ、`af-ecs-tts` は読み上げを有効にしたときだけ、`af-ecs-pool` は `ecs-ec2` のときだけあります。ループは、存在しないスタックを飛ばします
- 上の名前は既定の例です。実際のスタック名（`60-engines.yaml` / `50-tts.yaml` / `40-ec2-pool.yaml` などをデプロイしたときの名前）に置き換えます。`teardown.sh` は名前を配備から読みます
- `50-tts` を消す前に、エンジンのサービスを 0 台にしておきます。タスクが登録されているあいだは Cloud Map のサービスを消せず、止まりかけのタスクもまさにそれに当たります
- かかる時間は、`delete` で約 10 分、`retain` で約 16 分（RDS のスナップショットを取るため）です

### 9. `retain` の配備で追加の 4 手順

`Persistence=retain` では、ただの `delete-stack` は最初の手順で失敗します。

```bash
aws rds modify-db-instance --db-instance-identifier <db> --no-deletion-protection --apply-immediately
# after af-ecs-data is deleted:
aws rds delete-db-snapshot --db-snapshot-identifier af-ecs-data-snapshot-db-<suffix>  # DeletionPolicy: Snapshot leaves one
aws efs delete-file-system --file-system-id <efs>                                     # DeletionPolicy: Retain keeps it
# each EFS backup vault (DeletionPolicy: Retain) cannot be deleted until its recovery points are:
aws backup list-backup-vaults --query "BackupVaultList[?starts_with(BackupVaultName,'af-ecs-data-efs-')].BackupVaultName"
for rp in $(aws backup list-recovery-points-by-backup-vault --backup-vault-name <vault> \
    --query 'RecoveryPoints[].RecoveryPointArn' --output text); do
  aws backup delete-recovery-point --backup-vault-name <vault> --recovery-point-arn "$rp"
done
aws backup delete-backup-vault --backup-vault-name <vault>                            # once the points are gone
```

`teardown.sh` は削除保護を外しますが、最後のスナップショット・EFS・バックアップのボールトは、`--purge-retained` を付けたときだけ消します。付けなければ、ボールトの復元ポイントは `EfsBackupRetentionDays` のとおりに自然に期限切れになります。

### 10. タスク定義を消す

料金はかかりませんが、どのスタックよりも長く残ります。

- `list-task-definitions --family-prefix af-ws` は、ACTIVE のリビジョンが 9 つあるのに 0 と答えたことがあります。接頭辞なしで一覧して数えます
- ACTIVE のリビジョンをすべて登録解除し、そのあと INACTIVE のものを 10 個ずつ `delete-task-definitions` で消します
- `--max-items` を `--output text` と使うと、ページ送りのトークン（`None`）が 1 行として付きます。それを `delete-task-definitions` に渡すと、まとめて失敗します。`grep '^arn:'` で絞ります

### 11. ACM の検証用 CNAME を消す

`30-ingress` は証明書と ALB のエイリアスのレコードを消しますが、`_<hash>.<fqdn>` はホストゾーンにずっと残ります。

- 害はありません。ただ、残すと次の配備の証明書が、自分で作っていないレコードで検証されてしまい、発行の不具合に気づけません
- `change-resource-record-sets` で消します。DELETE には**正確な** TTL と値が要ります。TTL は 300 です

## 撤去しても残るもの

次のものは、上のどの手順でも消えません。

| 残るもの | 理由 |
|---|---|
| ホストゾーン | 配備のものではない |
| `/af-cp/*` のシークレット（残したとき） | 同じアカウントに建て直すため。`--purge-secrets` で消える |
| **NAT ゲートウェイの Elastic IP** | 建て直しで同じアドレスから出るため（[外向きの IP アドレスを固定する](egress-ip.ja.md)） |
| **egress ゲートウェイの Elastic IP**（`GatewayEip`、`EgressRoute=gateway` でスタックが割り当てたとき） | 同上（[外向きの通信をゲートウェイで制御する](egress-gateway.ja.md#作り直しても残る)） |
| **通信の記録の S3 Bucket**（`EgressArchiveBucket`、`EgressArchive=create` のとき） | 中身がスタックより長く残るように |
| `60-engines` のモデルの S3 Bucket | 同上。大きなモデルを Hugging Face から取り直すと時間が読めない |

- どれも `Retain` で、`teardown.sh` は触りません
- 付いていない EIP は、解放するまで 1 つあたり月 $3.6 ほどかかります。S3 Bucket は中身の分がかかります
- 配備を完全にやめるときは、アドレスと S3 Bucket ごとに 1 手順ずつ足します

  ```bash
  aws ec2 release-address --allocation-id eipalloc-...   # each EIP
  aws s3 rb s3://<bucket> --force                        # empty then delete each bucket
  ```

- 残りがないことを点検します: EC2 のインスタンス・ボリューム・スナップショット・AMI・EIP、ECS のクラスターとタスク定義、EFS、RDS のインスタンスと手動のスナップショット、ALB、ACM、NAT、ECR、Cloud Map の名前空間、`/af` のロググループ、`af-*` の IAM ロールとインスタンスプロファイル、デフォルトでない VPC

> [!WARNING]
> **ECR は残せません。** `af-control-plane` と `af-workspace` は `20-platform` のリソースで、`EmptyOnDelete: true` です。スタックを消すと、リポジトリと**その中のすべてのイメージ**が消えます。建て直すときは、`release-ecr.sh` か `crane copy` からやり直します。

## 関連

- [スクリプトで構築・一時停止・撤去する](lifecycle.ja.md)
- [EFS をバックアップ・復元する](backup.ja.md)
- [アイドル停止と強制停止を使う](../run/idle-stop.ja.md)：一時停止と削除の比べ方
