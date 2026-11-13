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
| `Persistence=retain` で構築する | `10-data` が、EFS 用のバックアップボールトと、毎日実行するバックアッププランを作る |
| 既存の配備では、変更セットで `10-data` を更新する | `EfsBackup*` の 4 つのリソースが追加される（Control Plane のファイルシステムより古いスタックでは、`CpData*` の 5 つも） |
| アップグレードの前などに、その場でバックアップを取る | 復旧ポイントが 1 つ増える |
| 復旧ポイントから 1 人分を戻す | AWS Backup が、同じファイルシステムの別のディレクトリに復元する。元の場所へは自分で戻す |

## 何を守るのか

`Retain` は、スタックを消してもファイルシステムが残る、という意味だけです。**動いている**ファイルシステムの中で消したり上書きしたりしたものは、その瞬間に失われます。

| 形態 | EFS にあるもの |
|---|---|
| `ecs` | 全員のホームディレクトリ（`/home/<membership>`）と Claude の状態（`/claude-config/<membership>`） |
| `ecs-ec2` | Claude の状態と、残すドットファイル（`/home-keep/<membership>`）。ホームそのものは EBS にある |
| どちらも | 専用のファイルシステム（`CpDataFileSystem`）に、Control Plane のデータルート。内部 git のリポジトリと、その LFS オブジェクト。メンバーがそこにだけ push したものは、これが唯一のコピー |

そのため、`Persistence=retain` では `10-data` が次も作ります。

| リソース | 中身 |
|---|---|
| `EfsBackupVault` | ボールト `<data stack>-efs-<stack id の 16 進 8 桁>`（例: `af-ecs-data-efs-1a2b3c4d`、スタックの出力 `EfsBackupVaultName`）。`DeletionPolicy: Retain` で、復旧ポイントが残っているボールトは AWS が削除を拒否する。ボールトの名前は 50 文字までなので、`retain` ではデータスタックの名前は 37 文字まで（`standup.sh` が確かめる） |
| `EfsBackupPlan` | ルールは 1 つ。毎日 17:00 UTC（02:00 JST）で、各ポイントを `EfsBackupRetentionDays`（既定は **7**、RDS と同じ）日残す |
| `EfsBackupSelection` + `EfsBackupRole` | 対象はメンバーのファイルシステムと、Control Plane のファイルシステム（`CpDataFileSystem`）。AWS 管理のバックアップ用・復元用のポリシーを使う |

- `Persistence=delete` では、どれも作りません
- バックアップは、最初に全体をコピーし、そのあとは増分です。ワークスペースが動いたまま取るので、クラッシュ整合性のバックアップになります
- **自動で復元することはありません。** 復元はいつも、下の手順のどれかです

### スタックが消えてもボールトは残る

- `teardown.sh` は `--purge-retained` なしではボールトを残します。スタックを `retain` から `delete` に切り替えても残ります
- 名前にスタックの ID が入るので、あとで同じスタック名で構築しても衝突しません。`teardown.sh` は、`Persistence` の値に関係なく、すべての `<data stack>-efs-*` のボールトを計画と点検の対象に挙げます
- 1 つだけ衝突する場合があります。**同じ**スタックを `retain` → `delete` → `retain` と切り替えたとき（スタックの ID が変わらない）です。残っているボールトを先に消します（[撤去する](teardown.ja.md#9-retain-の配備で残ったものを消す)）

## 動いている配備でバックアップを有効にする

`update.sh` は `10-data` を触りません。そのため、バックアップはこのスタックを手で更新したときにだけ有効になります。`--parameter-overrides` を付けなければ、パラメータはすべて今の値のままです。変更セットをまず実行せずに作り、中身を確かめてから実行します。

```bash
aws cloudformation deploy --stack-name af-ecs-data --template-file cfn/10-data.yaml \
  --capabilities CAPABILITY_AUTO_EXPAND CAPABILITY_IAM --no-execute-changeset \
  --profile <p> --region <r>
# the command prints the change set's ARN
aws cloudformation describe-change-set --change-set-name <arn> \
  --query 'Changes[].ResourceChange.[Action,LogicalResourceId,Replacement]' --output table \
  --profile <p> --region <r>
aws cloudformation execute-change-set --change-set-name <arn> --profile <p> --region <r>
aws backup list-backup-plans --query "BackupPlansList[].BackupPlanName" \
  --profile <p> --region <r>   # af-ecs-data-efs-daily
```

> [!WARNING]
> 変更セットが次のどれかの形で、それ以外を含まないときだけ実行します。特に `FileSystem` と `Db` の `Modify` や `Replacement` があってはいけません。
>
> - ボールトがまだない（`no-vault`）: `EfsBackup*` の 4 つのリソースの *Add*
> - プランがすべてのファイルシステムを選んでいない（`no-plan`）: `EfsBackupSelection` の *Modify*（置き換えになることもあります。復旧ポイントはボールトに残ります）
> - どちらの場合も、Control Plane のファイルシステムより古いデータスタックでは `CpData*` の 5 つのリソースの *Add* も出ます。そのアップグレードの前に、読めるリポジトリを外へ写してください（[Control Plane のデータルート](upgrade.ja.md#control-plane-のデータルート)）
>
> スタックを更新すると、動いているファイルシステムのスループットモードが `EfsThroughputMode` の値に変わります（`10-data.yaml` の警告を参照）。`FileSystem` に `Replacement` があれば、新しい空のファイルシステムに置き換わります。

### 管理画面の通知

`update.sh` と `standup.sh` は、データスタックの状態を `30-ingress` の `EfsBackup` パラメータに書きます。

| 値 | 意味 |
|---|---|
| `present` | バックアップがある |
| `missing` | `retain` なのにバックアップがない。`ecs` / `ecs-ec2` では、super_admin が見る管理画面のすべてのセクションに通知が出る |
| `off` | `retain` ではない |
| 空 | 一度も書かれていない。何も表示しない |

- Control Plane は `AF_EFS_BACKUP` として受け取ります。環境変数を読むだけで、CloudFormation や AWS Backup は読まないので、権限は要りません
- 通知は、最後に `update.sh` を実行した時点の状態を表します。上の変更セットを実行したあと、`update.sh` をもう一度実行すると通知が消えます
- データスタックを読めないときは、パラメータは前の値のまま変わりません

## その場でバックアップを取る

アップグレードの前や、だれかのホームを手で触る前に取ります。Control Plane のリポジトリは、`EfsId` の代わりに `CpDataEfsId` で同じジョブを実行します。

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

## 復旧ポイントを選ぶ

新しいものほど後ろに表示されます。

```bash
aws backup list-recovery-points-by-backup-vault --backup-vault-name "$VAULT" \
  --query 'sort_by(RecoveryPoints,&CreationDate)[].[CreationDate,Status,RecoveryPointArn]' --output text
```

## 1 人分のディレクトリを戻す

この節と次の節は、メンバーのファイルシステム（`EfsId`）のためのものです。Control Plane のリポジトリは戻しかたが違います（[Control Plane のリポジトリを戻す](#control-plane-のリポジトリを戻す)）。

AWS Backup は、動いているデータの上には復元しません。同じファイルシステムに復元すると、元のディレクトリと並ぶ新しい最上位ディレクトリ `/aws-backup-restore_<timestamp>/` の下に、元のパスのまま置かれます。1 つのジョブで指定できるパスは 5 つまでです。

```bash
aws backup start-restore-job --recovery-point-arn <rp-arn> \
  --iam-role-arn "arn:aws:iam::$ACCT:role/$ROLE" --resource-type EFS \
  --metadata '{"file-system-id":"<efs-id>","newFileSystem":"false","Encrypted":"true",
    "PerformanceMode":"generalPurpose",
    "ItemsToRestore":"[\"/home/<membership>\",\"/claude-config/<membership>\"]"}'
aws backup describe-restore-job --restore-job-id <id>    # wait for COMPLETED
```

復元したコピーはどのアクセスポイントの範囲にも入らないので、どのワークスペースからも見えません。元の場所に戻すには、ファイルシステムのルートをマウントする必要があります。そのために、root で動く使い捨ての Fargate タスクを使います。タスクはプライベートサブネットで動かし、ワークスペースのセキュリティグループを付けます。EFS ボリュームには `$EFS` のルートディレクトリ `/` を指定し、転送中の暗号化をオンにします。

1. そのメンバーのワークスペースを止め、起動しないよう頼みます。置き換えるあいだに、だれもディレクトリに書かないようにします
2. タスクの中で、`mv /mnt/home/<membership> /mnt/home/<membership>.before-restore` を実行し、次に `cp -a /mnt/aws-backup-restore_<ts>/home/<membership> /mnt/home/` を実行します（`claude-config` / `home-keep` も同じ）。所有者はバックアップしたときのまま、つまりアクセスポイントの uid/gid で戻ります
3. ワークスペースを起動し、メンバーに確かめてもらってから、`.before-restore` と `aws-backup-restore_*` のディレクトリを消します。消すまでは、両方のディレクトリに EFS の料金がかかります

## ファイルシステム全体を戻す

- `ItemsToRestore` なしの同じコマンドで、すべてを動いているファイルシステムの `/aws-backup-restore_<timestamp>/` に復元します。ワークスペースを止めてから、必要なものを同じ使い捨てのタスクで戻します
- 新しいファイルシステムへの復元（`"newFileSystem":"true"`、`file-system-id` なし）は、動いているファイルシステムそのものがなくなったときだけ使います
- 配備を新しいファイルシステムに移すことは、テンプレートの変更だけではできません。`EfsId` は上位のスタックに export されていて、Control Plane は古い ID の上にアクセスポイントを作っています。新しいファイルシステムに復元した場合も、スタックが指しているファイルシステムへディレクトリをコピーします

## Control Plane のリポジトリを戻す

内部 git のリポジトリは `CpDataFileSystem` の `/cp-data/git/<tenant slug>/<repo>.git` にあり、各リポジトリの LFS オブジェクトはその中（`lfs/objects/`）にあります。上のメンバー向けの手順では届きません。このファイルシステムのマウントターゲットは Control Plane のセキュリティグループからの NFS しか受けず、マウントは必ずアクセスポイントを通す必要があり、Control Plane 自身の許可はそのアクセスポイント（`/cp-data`）だけに限られるからです。復元先の `/aws-backup-restore_<timestamp>/` はそのアクセスポイントの外なので、Control Plane からも見えません。

**復元したファイルを Control Plane のパスへ直接コピーしてはいけません。** リポジトリの一部は RDS にあります。行、既定ブランチ、そして LFS オブジェクトをテナントの上限に数える台帳です。リポジトリが一覧に残っていても、台帳の行が消えていることがあります（git の GC は、消した LFS オブジェクトの台帳の行も消します）。そのため、ファイルをそのまま写すと、二度と数えられないオブジェクトがディスクに置かれることがあります。復元したリポジトリは、必ず Control Plane を通した push で戻します。push なら台帳が書かれます。

1. **同じファイルシステムに復元する。** `"file-system-id"` はデータスタックの出力 `CpDataEfsId`、`ItemsToRestore` は `["/cp-data/git/<slug>/<repo>.git"]` のように必要なリポジトリ（または `["/cp-data/git"]`）を指定します。`aws backup describe-restore-job --restore-job-id <id> --query Status --profile <p> --region <r>` が `COMPLETED` になるまで待ちます。Control Plane のパスには何も触れないので、Control Plane は動かしたままで構いません
2. **コピーに届くようにする。** `CpDataFileSystem` に、ルートディレクトリ `/aws-backup-restore_<ts>/cp-data`、POSIX ユーザー `0:0` の一時的なアクセスポイントを作ります。あわせて、そのファイルシステムへの `elasticfilesystem:ClientMount` と `ClientRootAccess`、それに自分で決めた S3 の場所への `s3:PutObject` を許す一時的なタスクロールを作ります
3. **使い捨ての Fargate タスクで書き出す。** そのロールを使い、Control Plane のセキュリティグループ（`CpSgId`）とプライベートサブネットで、`tar` と AWS CLI のあるイメージを動かします。EFS ボリュームは 1 つで、転送中の暗号化と IAM 認可をオンにし、一時的なアクセスポイントを `/restore` にマウントします。リポジトリごとに `tar -C /restore/git/<slug> -czf - <repo>.git` をその S3 の場所へ書き出します
4. **それぞれの tar を、そのテナントの管理者のワークスペースに入れ**、ホームの下の専用のディレクトリに展開します（下の `rescue` と `putback` は今のディレクトリの `<repo>.git` と `<repo>.refs` を使うので、復元したコピーと `rescue` のコピーを同じディレクトリに置かないでください）。そこでは git が内部 git に認証できるよう設定済みです
5. **Control Plane を通して戻す。** そのリポジトリがまだ一覧にあって応答するなら、先に [ECS でのアップグレード](upgrade.ja.md#v045x-以前から上げる) の `rescue`（手順 3）で写しておきます。復元すると、今の中身は置き換わるからです。次に Console で削除し、同じ名前で作り直して、復元したコピーを push します。Console で作ったリポジトリは `main` から始まります。元の既定ブランチが別だったなら、メンバーに伝えてください

   ```bash
   cd <tar を展開したディレクトリ>
   git -C <repo>.git for-each-ref --format='%(objectname)%09%(refname)' | sort > <repo>.refs
   putback <repo> <clone URL>    # アップグレードのページの手順 7 の関数
   ```

   `putback` は LFS オブジェクトを先に、ref をその後に push し、push のあとに Control Plane が返す ref が復元したものと一致しなければ止まります。LFS オブジェクトは、`git-lfs` がベアリポジトリで期待する場所（`lfs/objects/`）にあるので、1 つずつアップロードされ、台帳に数えられます

6. **片付ける。** 一時的なアクセスポイントの下に復元したファイル、そのアクセスポイント、タスク定義、ロール、S3 のオブジェクトを消します。消すまでは、復元したコピーに EFS の料金がかかります

- 新しいファイルシステムへの復元（`"newFileSystem":"true"`）は、`CpDataFileSystem` そのものを失ったときのためのものです。新しいファイルシステムに一時的なアクセスポイントを作り、同じように書き出します。スタックの export を別のファイルシステムに向けることはできません

## 費用

| 項目 | 料金（ap-northeast-1） |
|---|---|
| ウォームのバックアップストレージ | $0.06/GB 月 |
| 復元 | $0.024/GB と、項目単位の復元ジョブ 1 回あたり $0.60 |

- 変更の少ないファイルシステムなら、7 日分の復旧ポイントの費用は、全体のコピー 1 つと 1 週間分の変更の分ほどです。20 GB のファイルシステムで月 $1.2〜2 ほどです
- スタックを消したあとも、すでに取ったポイントはそれぞれの予定で期限切れになります。`teardown.sh --purge-retained` は、ポイントとボールトをまとめて消します

> [!NOTE]
> 上の復元の手順は AWS Backup のドキュメントをもとに書いたもので、実際の配備ではまだ最初から最後まで試していません。

## 関連

- [バックアップして復元する](../run/backup-restore.ja.md)：ほかの形態のバックアップ
- [費用を抑える](cost.ja.md#データを残すかを決める)：`Persistence` の選び方
- [撤去する](teardown.ja.md)
