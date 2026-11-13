---
audience: "AWS の ECS（`ecs` / `ecs-ec2`）の配備をデプロイする人や CI に、最小の IAM 権限を与えるアカウント管理者"
source_of_truth: "`deploy/aws/ecs/cfn/*.yaml` が作るリソースと、`release-ecr.sh`・`standup.sh`・`update.sh`・`pause.sh`・`teardown.sh` が呼ぶ API"
---

# デプロイに必要な IAM 権限

[English](iam.md) | 日本語

`release-ecr.sh` と `cloudformation deploy` を実行する人（または CI）に、どの権限を与えればよいかを決めます。
タスクが使う実行時のロールは `20-platform` が作るので、ここには含みません。

## 全体像

| あなたがやること | 起きること |
|---|---|
| 使い捨てのサンドボックスで試す | VPC・ECS・EFS・RDS・ALB・IAM・ECR・SSM を広く作れる権限でよい |
| 本番のアカウントでデプロイする | 下の表のサービスごとの権限に絞る。リソースは、そのサービスが対応していれば `af-` の名前のプレフィックスで絞る |

## サービスごとの権限

| サービス | 何のため | アクション（要約） |
|---|---|---|
| cloudformation | スタックの作成・更新・削除 | スタックと変更セットの Create/Update/Delete/Describe |
| ec2 | `00-network`（VPC・サブネット・NAT・SG・S3 のゲートウェイエンドポイント。`EgressRoute=gateway` では egress ゲートウェイ） | VPC・サブネット・IGW・NAT・EIP・ルートテーブルとルート・セキュリティグループ・VPC エンドポイント（ポリシー付きの S3 のゲートウェイエンドポイント: `ec2:CreateVpcEndpoint`/`ModifyVpcEndpoint`/`DeleteVpcEndpoints`）の Create/Delete/Describe。ゲートウェイには、ネットワークインターフェース、`AssociateAddress`/`DisassociateAddress`（その Elastic IP。渡した IP には `DescribeAddresses`）、起動テンプレート（`CreateLaunchTemplate`/`CreateLaunchTemplateVersion`/`DeleteLaunchTemplate`）、`RunInstances`/`CreateTags`（起動テンプレートから Auto Scaling グループを作るときに確かめられる） |
| autoscaling | `00-network` の egress ゲートウェイ（`EgressRoute=gateway`。`pause.sh` はスタックを通してスケールする） | AutoScalingGroups の Create/Update/Delete/Describe。アカウントで 1 回、`autoscaling.amazonaws.com` の `iam:CreateServiceLinkedRole` |
| secretsmanager | `00-network` の egress ゲートウェイのエージェントのトークン（`EgressRoute=gateway`） | CreateSecret/DeleteSecret/DescribeSecret、GetRandomPassword（値はスタックが作る） |
| s3 | `10-data` の egress のイベントのアーカイブの S3 Bucket（`EgressArchive=create`、既定はオフ） | CreateBucket/DeleteBucket、Put/GetBucketPolicy、PutBucketEncryption、PutLifecycleConfiguration、PutBucketPublicAccessBlock、PutBucketTagging。`20-platform` の `CfnTemplatesBucket` には `s3:PutObject` / `s3:GetObject`（`--s3-bucket` を付けてデプロイするとき、CLI がテンプレートをアップロードする。`30-ingress` はいつも付ける） |
| ecr | `20-platform` のリポジトリとイメージの push | CreateRepository/DeleteRepository/Describe*、GetAuthorizationToken、BatchCheckLayerAvailability、InitiateLayerUpload、UploadLayerPart、CompleteLayerUpload、PutImage |
| ecs | `20-platform` のクラスターと `30-ingress` のサービス | クラスターとサービスの Create/Delete/Describe、RegisterTaskDefinition。アカウントで 1 回 `iam:CreateServiceLinkedRole` |
| servicediscovery | Service Connect の名前空間 | 名前空間の Create/Delete/Get |
| efs | `10-data` のファイルシステムとマウントターゲット | CreateFileSystem/DeleteFileSystem/CreateMountTarget/DeleteMountTarget/Describe* |
| backup | `10-data` の EFS のバックアップボールトとバックアッププラン（`Persistence=retain`）、バックアップと復元、撤去 | CreateBackupVault/DescribeBackupVault/DeleteBackupVault/ListBackupVaults、BackupPlan の Create/Get/Update/Delete、BackupSelection の Create/Get/Delete、ListBackupPlans。既定の `aws/backup` キーへの `backup-storage:MountCapsule` と `kms:CreateGrant`/`DescribeKey`。StartBackupJob/StartRestoreJob/DescribeRestoreJob、ListRecoveryPointsByBackupVault/DeleteRecoveryPoint、`10-data` のバックアップのロールへの `iam:PassRole` |
| kms | `10-data` の Control Plane のキー（`CustodianKmsKey=create` のときだけ） | CreateKey、PutKeyPolicy、EnableKeyRotation、DescribeKey、TagResource、CreateAlias/DeleteAlias、ScheduleKeyDeletion（`Persistence=delete` でスタックを消すとき） |
| rds | `10-data` のインスタンス | CreateDBInstance/DeleteDBInstance/CreateDBSubnetGroup/Describe*（ManageMasterUserPassword には、RDS が管理するシークレットへの `secretsmanager:*` と `kms:DescribeKey` も要る） |
| elasticloadbalancing | `30-ingress` の ALB・ターゲットグループ・リスナー | ロードバランサー・ターゲットグループ・リスナーの Create/Delete/Describe/Modify |
| acm | `30-ingress` の証明書 | RequestCertificate/DeleteCertificate/DescribeCertificate |
| route53 | DNS 検証とエイリアス | ChangeResourceRecordSets/GetHostedZone/ListResourceRecordSets（そのゾーンに対して） |
| logs | ロググループ | CreateLogGroup/DeleteLogGroup/PutRetentionPolicy/Describe* |
| iam | `20-platform` の名前付きロール、`10-data` の EFS のバックアップのロール、`00-network` の egress ゲートウェイのロールとインスタンスプロファイル、`30`・`40`・`60` が Control Plane のタスクロールに付ける管理ポリシー | CreateRole/DeleteRole/Get/PassRole、Put/Delete/Attach/DetachRolePolicy、CreatePolicy/DeletePolicy/GetPolicy、PolicyVersion の Create/Delete/List、InstanceProfile の Create/Delete と Add/RemoveRoleFromInstanceProfile（→ `CAPABILITY_NAMED_IAM`。`10-data` と `00-network` は `CAPABILITY_IAM`） |
| ssm | Control Plane のシークレット（スタックの外で作る）、egress ゲートウェイの準備完了とドレインの確認（`pause.sh`・`standup.sh`、`EgressRoute=gateway` のときだけ） | `/af-cp/*` の下の PutParameter/DeleteParameter。`arn:aws:ssm:<region>::document/AWS-RunShellScript` と、`af-egress-gateway=<network stack>` のタグが付いたインスタンス（`ssm:resourceTag/af-egress-gateway`）への `SendCommand`、`GetCommandInvocation` |
| sts | `release-ecr.sh` がアカウント ID を調べるため | GetCallerIdentity |

## Control Plane のタスクロールのポリシーの上限

Control Plane のタスクロール（`CpTaskRole`）に権限を足すときに、知っておくことです。

| 上限 | 中身 |
|---|---|
| インラインポリシー | ロールのインラインポリシーは合計 10,240 文字まで（空白は数えない）。`20-platform` のインラインの `cp-runtime` が約 7,000 を使う |
| 管理ポリシー | 1 つにつき 6,144 文字まで。ロールに付けられる数はクォータで決まり、引き上げられる |

- ほかのスタックが足す権限（`40-ec2-pool` のスロットの起動、`60-engines` の取り込み、`30-ingress` のホームの操作）は、それぞれ別の `AWS::IAM::ManagedPolicy` です。インラインに入れると上限を超え、`30-ingress` が `ServiceLimitExceeded` でロールバックします
- 管理ポリシーの名前はアカウント全体で一意なので、リージョンを含みます（`af-<stack>-cp-ingest-<region>` など）

## 関連

- [ECS に構築する](install.ja.md)
- [ECS 構成の全体像](overview.ja.md)
- [テンプレートの大きさの上限に気をつける](template-size.ja.md)
