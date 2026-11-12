---
audience: "AWS の ECS 配備で、メンバーのホームと Claude の状態を EFS のバックアップから戻す配備管理者"
source_of_truth: "`deploy/aws/ecs/cfn/10-data.yaml`（`EfsBackup*`）、`deploy/aws/ecs/update.sh`・`efs-backup-check.sh`、AWS Backup のドキュメント"
---

# EFS をバックアップ・復元する

[English](backup.md) | 日本語

ECS の配備で、EFS に置いたメンバーのデータを毎日バックアップし、消したり壊したりしたときに戻します。
`Persistence=retain` の配備だけの作業です。

## 全体像

| あなたがやること | 起きること |
|---|---|
| `Persistence=retain` で建てる | `10-data` が、EFS のバックアップのボールトと毎日のプランを作る |
| 既存の配備では、変更セットで `10-data` を更新する | 4 つの `EfsBackup*` のリソースだけが足される |
| アップグレードの前などに、その場でバックアップを取る | 復元ポイントが 1 つ増える |
| 復元ポイントから 1 人分を戻す | AWS Backup が、同じファイルシステムの別のディレクトリに復元する。元の場所へは自分で戻す |

## 何を守るのか

`Retain` は、スタックを消してもファイルシステムが残る、という意味だけです。**動いている**ファイルシステムの中で消したり上書きしたりしたものは、その瞬間に失われます。

| 形態 | EFS にあるもの |
|---|---|
| `ecs` | 全員のホーム（`/home/<membership>`）と Claude の状態（`/claude-config/<membership>`） |
| `ecs-ec2` | Claude の状態と、残すドットファイル（`/home-keep/<membership>`）。ホームそのものは EBS にある |

そのため、`Persistence=retain` では `10-data` が次も作ります。

| リソース | 中身 |
|---|---|
| `EfsBackupVault` | ボールト `<data stack>-efs-<stack id の 16 進 8 桁>`（例: `af-ecs-data-efs-1a2b3c4d`、スタックの出力 `EfsBackupVaultName`）。`DeletionPolicy: Retain` で、AWS は復元ポイントが残っているボールトを消させない。ボールトの名前は 50 文字までなので、`retain` ではデータスタックの名前は 37 文字まで（`standup.sh` が確かめる） |
| `EfsBackupPlan` | ルールは 1 つ。毎日 17:00 UTC（02:00 JST）で、各ポイントを `EfsBackupRetentionDays`（既定は **7**、RDS と同じ）日残す |
| `EfsBackupSelection` + `EfsBackupRole` | このファイルシステムだけを、AWS が管理するバックアップと復元のポリシーで |

- `Persistence=delete` では、どれも作りません
- バックアップは、最初の全体のコピーのあとは増分です。ワークスペースが動いたまま取るので、クラッシュ整合です
- **自動で復元することはありません。** 復元はいつも、下の手順のどれかです

### スタックが消えてもボールトは残る

- `teardown.sh` は `--purge-retained` なしではボールトを残します。スタックを `retain` から `delete` に切り替えても残ります
- 名前にスタックの ID が入るので、あとで同じスタック名で建てても衝突しません。`teardown.sh` は、残す設定に関係なく、すべての `<data stack>-efs-*` のボールトを計画と点検に挙げます
- 1 つだけ衝突する場合があります。**同じ**スタックを `retain` → `delete` → `retain` と切り替えたとき（スタックの ID が変わらない）です。残っているボールトを先に消します（[撤去する](teardown.ja.md#9-retain-の配備で追加の-4-手順)）

## 動いている配備でバックアップを有効にする

`update.sh` は `10-data` を触りません。バックアップは、そのスタックを手で更新したときにだけ入ります。`--parameter-overrides` を付けなければ、パラメータはすべて今の値のままです。変更セットを、実行せずに作って中身を読んでから実行します。

```bash
aws cloudformation deploy --stack-name af-ecs-data --template-file cfn/10-data.yaml \
  --capabilities CAPABILITY_AUTO_EXPAND CAPABILITY_IAM --no-execute-changeset \
  --profile <p> --region <r>
# the command prints the change set's ARN
aws cloudformation describe-change-set --change-set-name <arn> \
  --query 'Changes[].ResourceChange.[Action,LogicalResourceId,Replacement]' --output table
aws cloudformation execute-change-set --change-set-name <arn>
aws backup list-backup-plans --query "BackupPlansList[].BackupPlanName"   # af-ecs-data-efs-daily
```

> [!WARNING]
> 変更セットが 4 つの `EfsBackup*` のリソースを足すだけのときに実行します。特に `FileSystem` の `Modify` があってはいけません。スタックの更新は、動いているファイルシステムを `EfsThroughputMode` の値に動かします（`10-data.yaml` の警告を参照）。そこに `Replacement` があれば、新しい空のファイルシステムになります。

### 管理画面の通知

`update.sh` と `standup.sh` は、データスタックの状態を `30-ingress` の `EfsBackup` パラメータに書きます。

| 値 | 意味 |
|---|---|
| `present` | バックアップがある |
| `missing` | `retain` なのにバックアップがない。`ecs` / `ecs-ec2` では、super_admin の管理画面のすべての節に通知が出る |
| `off` | `retain` ではない |
| 空 | 一度も書いていない。何も出さない |

- Control Plane は `AF_EFS_BACKUP` として受け取ります。環境変数を読むだけで、CloudFormation や AWS Backup は読まないので、権限は要りません
- 通知は最後の `update.sh` の時点の状態です。上の変更セットを実行したら、`update.sh` をもう一度実行すると消えます
- データスタックが読めないときは、パラメータを前の値のままにします

## その場でバックアップを取る

アップグレードの前や、だれかのホームを手で触る前に取ります。

```bash
EFS=$(aws cloudformation describe-stacks --stack-name af-ecs-data \
  --query "Stacks[0].Outputs[?OutputKey=='EfsId'].OutputValue" --output text)
ROLE=$(aws cloudformation describe-stack-resource --stack-name af-ecs-data \
  --logical-resource-id EfsBackupRole --query StackResourceDetail.PhysicalResourceId --output text)
VAULT=$(aws cloudformation describe-stacks --stack-name af-ecs-data \
  --query "Stacks[0].Outputs[?OutputKey=='EfsBackupVaultName'].OutputValue" --output text)
ACCT=$(aws sts get-caller-identity --query Account --output text)
aws backup start-backup-job --backup-vault-name "$VAULT" \
  --resource-arn "arn:aws:elasticfilesystem:<r>:$ACCT:file-system/$EFS" \
  --iam-role-arn "arn:aws:iam::$ACCT:role/$ROLE" --lifecycle DeleteAfterDays=7
```

## 復元ポイントを選ぶ

新しいものが最後に出ます。

```bash
aws backup list-recovery-points-by-backup-vault --backup-vault-name "$VAULT" \
  --query 'sort_by(RecoveryPoints,&CreationDate)[].[CreationDate,Status,RecoveryPointArn]' --output text
```

## 1 人分のディレクトリを戻す

AWS Backup は、動いているデータの上には復元しません。同じファイルシステムに復元すると、元のディレクトリの隣の新しい最上位ディレクトリ `/aws-backup-restore_<timestamp>/` に、下のパスを保ったまま置かれます。1 つのジョブで 5 つのパスまでです。

```bash
aws backup start-restore-job --recovery-point-arn <rp-arn> \
  --iam-role-arn "arn:aws:iam::$ACCT:role/$ROLE" --resource-type EFS \
  --metadata '{"file-system-id":"<efs-id>","newFileSystem":"false","Encrypted":"true",
    "PerformanceMode":"generalPurpose",
    "ItemsToRestore":"[\"/home/<membership>\",\"/claude-config/<membership>\"]"}'
aws backup describe-restore-job --restore-job-id <id>    # wait for COMPLETED
```

復元したコピーはどのアクセスポイントの外にもあるので、どのワークスペースからも見えません。元に戻すには、ファイルシステムのルートをマウントする必要があります。プライベートサブネットで、ワークスペースのセキュリティグループを付け、`$EFS` のルートディレクトリ `/` を転送中の暗号化ありで EFS ボリュームにした、root で動く使い捨ての Fargate タスクを使います。

1. そのメンバーのワークスペースを止め、起動しないよう頼みます。置き換えるあいだに、だれもディレクトリに書かないようにします
2. タスクの中で、`mv /mnt/home/<membership> /mnt/home/<membership>.before-restore` を実行し、次に `cp -a /mnt/aws-backup-restore_<ts>/home/<membership> /mnt/home/` を実行します（`claude-config` / `home-keep` も同じ）。所有者はバックアップしたときのまま、つまりアクセスポイントの uid/gid で戻ります
3. ワークスペースを起動し、メンバーに確かめてもらってから、`.before-restore` と `aws-backup-restore_*` のディレクトリを消します。消すまで、EFS は両方に料金をかけます

## ファイルシステム全体を戻す

- `ItemsToRestore` なしの同じコマンドで、すべてを動いているファイルシステムの `/aws-backup-restore_<timestamp>/` に復元します。必要なものを、ワークスペースを止めて、同じ使い捨てのタスクで戻します
- 新しいファイルシステムへの復元（`"newFileSystem":"true"`、`file-system-id` なし）は、動いているファイルシステムそのものがなくなったときだけ使います
- 配備を新しいファイルシステムに移すのは、テンプレートの変更ではできません。`EfsId` は上のスタックに export されていて、Control Plane は古い ID にアクセスポイントを作っています。その場合も、スタックが指すファイルシステムにディレクトリをコピーします

## 費用

| 項目 | 料金（ap-northeast-1） |
|---|---|
| ウォームのバックアップストレージ | $0.06/GB 月 |
| 復元 | $0.024/GB と、項目単位の復元ジョブ 1 回あたり $0.60 |

- あまり変わらないファイルシステムの 7 日分のポイントは、全体のコピー 1 つと 1 週間分の変更くらいです。20 GB のファイルシステムで月 $1.2〜2 ほどです
- スタックを消したあとも、すでに取ったポイントはそれぞれの予定で期限切れになります。`teardown.sh --purge-retained` は、ポイントとボールトをまとめて消します

> [!NOTE]
> 上の復元の手順は AWS Backup のドキュメントから書いたもので、実際の配備で最初から最後まではまだ試していません。

## 関連

- [バックアップして復元する](../run/backup-restore.ja.md)：ほかの形態のバックアップ
- [費用を抑える](cost.ja.md#データを残すかを決める)：`Persistence` の選び方
- [撤去する](teardown.ja.md)
