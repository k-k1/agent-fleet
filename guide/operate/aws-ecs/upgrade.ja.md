---
audience: "AWS の ECS（`ecs` / `ecs-ec2`）の配備を新しいリリースに上げる配備管理者"
source_of_truth: "`deploy/aws/ecs/update.sh`・`release-ecr.sh`・`efs-backup-check.sh`・`clear-params.sh` と `cfn/*.yaml`（食い違ったらスクリプトが正）"
---

# アップグレードする

[English](upgrade.md) | 日本語

ECS の配備を新しいリリースに上げ、問題があれば前のリリースに戻します。
どの形態にも共通する注意（バックアップ、メンバーに見えるもの）は [アップグレードする](../run/upgrade.ja.md) にあります。このページは ECS の手順です。

## 全体像

| あなたがやること | 起きること |
|---|---|
| 先に RDS と EFS のバックアップを取る | 戻る道ができる |
| `update.sh` を実行する | イメージを確かめ、`20-platform` などを順に更新し、`30-ingress` の `ImageTag` を変える。Control Plane のサービスがブルー/グリーンで入れ替わる |
| 何もしない | 動いているワークスペースは古いイメージのまま。次に停止して起動したときに新しいイメージになる |
| 前のタグでもう一度上げる | 前のリリースに戻る |

Control Plane とワークスペースのイメージは、同じ版で一緒に上げます。`30-ingress` のパラメータ `ImageTag`（既定は `dev`）が、両方のタグです。

## 上げる前に

### バックアップを取る

本番の配備では、上げる直前にバックアップを取ります。

- `Persistence=retain` なら RDS には日次の自動バックアップがありますが、最新のものはほぼ 1 日前かもしれません
- EFS に日次のバックアップがあるのは、データスタックにバックアップのボールトとプランがあるときだけです（次で確かめます）

RDS のスナップショットと、EFS のオンデマンドの復旧ポイントを取ってください。EFS の復旧ポイントにはボールトが要るので、古い配備では先に次の確認をします。

### EFS のバックアップがあるか確かめる

`Persistence=retain` の配備だけの確認です。`update.sh` も同じ確認を実行して警告します。

```bash
./efs-backup-check.sh --profile <p> --region <r> --stack af-ecs-ingress   # read-only
aws cloudformation describe-stacks --stack-name af-ecs-data \
  --query "Stacks[0].Outputs[?OutputKey=='EfsBackupVaultName'].OutputValue" --output text
```

`EfsBackupVaultName` が出ない（`None`）なら、データスタックは v0.26.0 より前のままです。`Persistence` が何であっても、**EFS のバックアップはありません**。

`update.sh` は `10-data` に触らないので、手でオンにします。直接デプロイせず、変更セットを使います。

1. `deploy --no-execute-changeset` で変更セットを作ります。パラメータを渡さなければ、保存されている値のままです
2. `describe-change-set` で中身を見ます
3. 足されるのが `EfsBackup*` の 4 つのリソースだけのときだけ、`execute-change-set` で実行します。`FileSystem` か `Db` に `Modify` や `Replacement` があれば、実行せずに止めます。`retain` ではどちらも残す設定ですが、作り直しは新しい空のリソースです

v0.26.0 より前に建てた `Persistence=retain` の配備は、それ以来 EFS のバックアップがなく、ほかに知らせるものもありません。v0.26.0 を越えて上げたら、この確認を 1 回実行してください。データスタックの出力に `EfsBackupVaultName` が出て、翌朝に日次の復旧ポイントがあれば（`aws backup list-recovery-points-by-backup-vault`）完了です。

## `update.sh` で上げる

ふつうはこのスクリプトを使います。手で上げる手順を 1 つにまとめたもので、compose の `docker compose pull && docker compose up -d` にあたります。

```bash
VERSION=<v> ./update.sh --profile <p> --region <r>          # deploy an already-pushed tag
VERSION=<v> ./update.sh --profile <p> --region <r> --push   # push to ECR first
VERSION=<v> ./update.sh --profile <p> --region <r> --dry-run
```

| オプション | 中身 |
|---|---|
| なし | ECR に push 済みのタグをデプロイする |
| `--push` | 先に ECR に push する |
| `--dry-run` | 計画を表示するだけで、何も変えない |
| `--force` | 変更がなくても、Control Plane のサービスを新しくデプロイし直す |
| `--pool-stack <stack>` | `ecs-ec2` で、スロットプールのスタックの名前を指定する |

### `update.sh` がすること

| すること | 中身 |
|---|---|
| EFS のバックアップがないと警告する | `Persistence=retain` だけ。読み取りだけで確かめ、ないときは直す変更セットのコマンドを、デプロイの前と最後に表示する。上げるのは止めず、`10-data` も更新しない。ファイルシステムの ARN をそのまま名指すバックアップの選択だけを数え、読めなかったときや、`NotResources`・`Conditions`・タグで選んでいるときは「確認できない」と報告する |
| ECR にないタグを拒否する | CloudFormation は文字列を保存するだけなので、push を忘れてもデプロイは成功し、Control Plane のタスクが `CannotPullContainerError` で落ちる。両方のリポジトリを先に確かめる |
| 空の変更セットに気づく | 同じタグに push し直すとテンプレートが変わらず、デプロイは「No changes to deploy」になり、古いイメージのまま動き続ける。そのときは `ecs update-service --force-new-deployment` に切り替え、サービスが落ち着くまで待つ |
| `ecs-ec2` でスロットプールをデプロイし直す | スロットのユーザーデータにセキュリティの設定があるため。起動テンプレートのバージョンを足すだけで、今あるスロットは起動したときのユーザーデータのまま。プールのスタックが見つからないと**止まる**（探すのに `cloudformation:ListExports` が要る）。`--pool-stack <stack>` で名指しできる |
| 失敗した追加のスタックを越えて進み、最後に失敗する | `50-tts` と `60-engines` のデプロイが失敗しても、`30-ingress` までデプロイして Control Plane を上げ、最後に `FAILED:` でスタックを名指して 0 以外の終了コードで終わる。スタックのイベントで原因を直し、もう一度実行する。それより前の失敗（イメージの確認、`20-platform`、スロットプール）では止まる |
| リポジトリ・イメージ・スタックの順を守る | `20-platform`（ECR のリポジトリを持つ）を変更セットで先にデプロイし、作り直すものがないときだけ実行する。次に `af-engine-tools:<EngineToolsImageTag>` を GHCR から ECR に入れ、それから `60-engines` をデプロイする。逆の順では、スタックは成功してもエンジンの取得のコンテナが `CannotPullContainerError` のままになる。GHCR にもタグがなければ止まるので、先に `engine-tools-image.yml` を実行する |
| 空になった値を消す | ネットワークやデータのスタックにもうない値（ダウングレード後の `WorkspaceSubnets` など）を、`30-ingress` と `20-platform` で変更セットを使って消し、読み戻す。ネットワークスタックを読めなければ、何も消さずに止まる |
| 古いイメージのワークスペースを一覧する | 自動では動かないため。ワークスペースは止めない。止めるとそのメンバーのセッションが切れるので、いつにするかはメンバーが決める |
| プライベートサブネットに残るワークスペースを一覧する | ネットワークスタックにワークスペース用のサブネットがあるとき。`S3EndpointEnabled` がすでに `true` なら警告になる。`00-network` はデプロイしない |
| 残ったゴールデンスナップショットを知らせる | `ecs-ec2` だけ。前のイメージで作ったゴールデンは使われず、新しいメンバーのホームは空から作られる（最初の起動が遅くなるだけ）。自動の作り直し（既定でオン）なら、空きスロットが 2 つあれば数分で置き換わる。オフなら `bake-golden.sh` で作り直す |

## 手で上げる

`update.sh` を使わないときの手順です。

1. バックアップを取ります（[上げる前に](#上げる前に)）
2. 新しいタグを push します

   ```bash
   VERSION=<v> ./release-ecr.sh --profile <p> --region <r>
   ```

3. `30-ingress` を、タグだけを変えてデプロイし直します。ほかのパラメータは前の値のままです

   ```bash
   CFN_BUCKET=$(aws cloudformation describe-stacks --stack-name <platform stack> --query "Stacks[0].Outputs[?OutputKey=='CfnTemplatesBucket'].OutputValue" --output text --profile <p> --region <r>)
   aws cloudformation deploy --stack-name af-ecs-ingress \
     --template-file cfn/30-ingress.yaml --s3-bucket "$CFN_BUCKET" --s3-prefix cfn --capabilities CAPABILITY_NAMED_IAM \
     --parameter-overrides ImageTag=<v> \
     --profile <p> --region <r>
   ```

## 上げたあとに起きること

- Control Plane と Console のサービスが新しいイメージに入れ替わります。ALB の裏で、短いブルー/グリーンの入れ替えです
- ワークスペースには触りません。各ワークスペースは**次に起動したとき**に新しいイメージになり、動いているワークスペースは停止して起動するまで今のタスクのままです
- EFS と RDS は変わらず、状態はそのまま引き継がれます

メンバーには、Console の「新しいバージョンがあります」と、ワークスペースのバーの「要再起動」が出ます。違いは [アップグレードする](../run/upgrade.ja.md) にあります。

### 「要再起動」が出ないとき

「要再起動」は、ワークスペースが起動したときのイメージと、タグが今指すイメージを、Control Plane が ECR で比べて出します。版の番号は比べません。

- そのために、`CpTaskRole` に、ワークスペースのリポジトリに対する `ecr:BatchGetImage` / `ecr:DescribeImages` が要ります。古い配備では、`20-platform` を 1 回デプロイし直してください。権限がないと比べられず、エラーも出ないまま、バッジが出ません
- `AF_ECS_WORKSPACE_IMAGE` が ECR のイメージでない配備（GHCR を直接使うなど）には、バッジは出ません

## 戻す

前のタグで同じ操作をすれば戻ります。

- 一度出したリリースのタグに、別のイメージを push し直さないでください
- リポジトリは `MUTABLE` なので、上書きは止められません。`:dev` を開発用に上書きできるようにするためで、リリースのタグを書き換えないのは運用の約束です

## `00-network` と `10-data` は上げない

`00-network` と `10-data` は、この手順にも `update.sh` にも含まれません。どちらかが要るリリースは、リリースノートにそう書きます。下の [v0.34.x から上げる](#v034x-から上げる) がその例です。

## パラメータを空に戻す

`aws cloudformation deploy --parameter-overrides Key=` では、動いているスタックの値が空になりませんでした（「No changes」）。CLI は空の値を送っていて、原因はわかっていません。
`update.sh` と `standup.sh` は、空にする値を自分で空にして読み戻します。手で空にするときは `clear-params.sh` を使います。

```bash
./clear-params.sh --profile <p> --region <r> --stack <stack> <Key>...
```

スタックの今のテンプレートで、指定したキーを外した変更セットを作ります。キーはテンプレートの `Default` が正確に `""` のときだけ空になり、ほかのパラメータは前の値のままです。

| 実行のしかた | 中身 |
|---|---|
| オプションなし | 変更セットを作り、リソースの変更と空になるキーを表示し、変更セットを残して、実行するコマンドを表示して止まる |
| `--execute <change-set-name> <Key>...` | 表示を読んで良ければ実行する。変更セットがない、`AVAILABLE` でない、そのキーを空にするだけのものでないときは拒否する（なくなっていたり実行中だったりしなければ、その変更セットを消す）。実行後に各値を読み戻す |
| `--yes` | 作る・表示する・実行するを 1 回で行う |
| `--dry-run` | 作って表示し、変更セットを消す |

- マスクされた（`NoEcho`）パラメータは `UsePreviousValue` のときだけ通します。SSM の型のパラメータは、同じ値に解決されるときだけです。変わらないと確かめられないものは拒否します
- 作れなかった変更セット（「didn't contain changes」など）は、報告して消します

## `ecs-ec2`: スロットの AMI は Amazon の SSM パラメータに従う

`WsRuntime=ecs-ec2` では、Control Plane が Amazon の公開している ECS 最適化の AL2023 の AMI のパラメータ（x86_64 と arm64）を約 1 時間ごとに読み、その AMI でスロットを起動します。デプロイしなくても、スロットは Amazon の更新に追随します。

- `SlotAmiId`・`SlotAmiIdArm64`・`Ec2SlotAmiArm64` は、最初の値と、読めないときの予備の値です
- Control Plane のロールに、`arn:aws:ssm:<region>::parameter/aws/service/ecs/optimized-ami/amazon-linux-2023/*` への `ssm:GetParameter` と `ec2:DescribeLaunchTemplateVersions` が要ります。どちらも `20-platform` にあるので、新しい Control Plane の**前か同時に** `20-platform` をデプロイします
- それまでは、Control Plane はログに 1 行出して静かに予備の値を使い、AMI を理由にスロットを古いとはしません
- 上げたあと最初の新しい AMI で、すべてのスロットが一斉に古くなり、次の起動で入れ替わります

古いスロットがどう入れ替わるかは [スロットを入れ替える・止める](slot-maintenance.ja.md) にあります。

## v0.34.x から上げる

v0.34.x の `00-network` には、ポリシーのない S3 のゲートウェイエンドポイントがあり、ワークスペース用のサブネットがありません。今のリリースでは、ワークスペースが専用のサブネットを持ち、エンドポイントには、許可していない S3 のリクエストを拒否するポリシーが付きます（AccessDenied、NAT への切り替えもなし）。プライベートサブネットで動いているワークスペースの S3 も拒否されます。

`update.sh` は `00-network` をデプロイしないので、手で行います。動いているワークスペースをすべて再起動するメンテナンスの時間を取ってください。

**新しい `00-network` を既定値のままデプロイしないでください。** `S3EndpointEnabled` の既定は `true` で、変更セットはワークスペース用の 6 つのリソースの *Add* と `S3Endpoint` の *Modify*（ポリシー）になります。実行すると、プライベートサブネットに残るすべてのワークスペースの S3 のリクエストが、一斉に拒否されます。

1. **バックアップを取ります**（[上げる前に](#上げる前に)）
2. **`00-network` を `S3EndpointEnabled=false` で、変更セットを使ってデプロイします。** スタックに `CAPABILITY_IAM` が要るようになりました（`EgressRoute=gateway` のときだけ作る出口のゲートウェイのインスタンスロールを宣言しているため）

   ```bash
   aws cloudformation deploy --stack-name af-ecs-network --template-file cfn/00-network.yaml \
     --capabilities CAPABILITY_IAM --parameter-overrides S3EndpointEnabled=false \
     --no-execute-changeset --profile <p> --region <r>
   aws cloudformation describe-change-set --change-set-name <the ARN it printed> \
     --query 'Changes[].ResourceChange.[Action,LogicalResourceId,Replacement]' --output text
   ```

   - 期待するのは、`WorkspaceSubnet0`・`WorkspaceSubnet1`・`WorkspaceRt`・`WorkspaceDefaultRoute`・`WorkspaceAssoc0`・`WorkspaceAssoc1` の 6 つの *Add* と、`S3Endpoint` の *Remove* です
   - *Remove* は想定どおりです。手順 5 までは、プライベートサブネットの S3（`ecs-ec2` のイメージの取得、モデルの取得）が NAT ゲートウェイを通り、そのデータ処理の料金がかかります
   - ほかに足される・変わる・作り直されるものがあれば、止めます
   - 問題なければ `execute-change-set --change-set-name <ARN>` を実行し、`wait stack-update-complete --stack-name af-ecs-network` で待ちます

3. **`update.sh` を実行します**（`VERSION=<v> ./update.sh --profile <p> --region <r>`）。ネットワークスタックの `WorkspaceSubnets` を `30-ingress` に渡すので、これ以後の起動はワークスペース用のサブネットに入ります。最後に、プライベートサブネットに残る動いているワークスペースを一覧します。あれば `WARNING:` が、確かめて 1 つもなければ「none」がはっきり出ます
4. **動いているワークスペースをすべて停止して起動し直します**（その一覧か `pause.sh --status`）。動いているタスクは今の ENI のままです。それぞれ次のコマンドでワークスペース用のサブネットにいることを確かめます。`update.sh --dry-run` は何も変えずに一覧を出し直します

   ```bash
   aws ecs describe-tasks --cluster <c> --tasks <t> --query "tasks[].attachments[].details[?name=='subnetId'].value"
   ```

5. **`00-network` を `S3EndpointEnabled=true` で、もう一度変更セットを使ってデプロイします。** 変更は `S3Endpoint` の *Add* 1 つです。プライベートサブネットの S3 が NAT からエンドポイントに移り、開いている TCP の接続は切れるので、大きなモデルの取得やイメージの取得が動いていないときに行います

### v0.34.x に戻す

| どこまで進めたか | 戻しかた |
|---|---|
| 手順 5 の前 | プライベートサブネットの S3 は、すでに NAT を通っています |
| 手順 5 のあと | `S3EndpointEnabled=false` にすると、`S3Endpoint` の *Remove* だけになります |
| `00-network` を v0.34.x のテンプレートに戻す | 6 つのリソースが消え、`S3Endpoint` はポリシーなしに戻ります。先にワークスペース用のサブネットで動くワークスペースをすべて止めます。そのあと `update.sh` が `30-ingress` の `WorkspaceSubnets` を消します（ID がなくなるため） |

v0.34.x に戻すと、v0.34.x が知らないパラメータ（`GatewayEipAllocationId` と `GatewayEipAddress` など）も消えます。次に `EgressRoute=gateway` に切り替えるときは、両方をもう一度渡してください。

## 関連

- [アップグレードする](../run/upgrade.ja.md)：どの形態にも共通する注意
- [ECS に構築する](install.ja.md)
- [テンプレートの大きさの上限に気をつける](template-size.ja.md)
- [スロットを入れ替える・止める](slot-maintenance.ja.md)
- [ECS で困ったとき](troubleshooting.ja.md)
