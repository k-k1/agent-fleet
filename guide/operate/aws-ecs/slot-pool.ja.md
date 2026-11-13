---
audience: "AWS の ECS 配備で、ワークスペースを Fargate から EC2 スロットのプール（ecs-ec2）に載せ替える配備管理者"
source_of_truth: "`deploy/aws/ecs/cfn/40-ec2-pool.yaml` と `30-ingress.yaml` のパラメータ、`deploy/aws/ecs/cfn/PARAMETERS.md`、Control Plane の ecs-ec2 アダプター（`control-plane/internal/runtime/runtime_ecs_ec2*.go`）"
---

# EC2 スロットプールを使い始める

[English](slot-pool.md) | 日本語

ECS の配備で、ワークスペースを Fargate ではなく EC2 インスタンスのプール（`WsRuntime=ecs-ec2`）で動かします。
ホームが EBS になって小さなファイルの読み書きが速くなり、Fargate の上限より大きなワークスペースも作れます。既定は Fargate（`WsRuntime=ecs`）のままで、このページの設定をしなければ何も変わりません。

## 全体像

| あなたがやること | 起きること |
|---|---|
| `40-ec2-pool` スタックを建てる | スロット用の起動テンプレートとロールができる。インスタンスはまだ 1 台もない |
| `30-ingress` を `WsRuntime=ecs-ec2` で更新する | Control Plane がワークスペースをスロットで起動するようになる |
| 何もしない | 最初の起動でスロットを作り、ユーザーごとの EBS のホームを付ける。使われないスロットは止まる |
| `30-ingress` を `WsRuntime=ecs` に戻す | Fargate に戻る。設定の切り替えはこれだけ |

スロットは誰のものでもありません。起動のたびに Control Plane が空きのスロットを選び、そのユーザーのボリュームを `/dev/sdf` に付けて SSM でマウントし、タスクをそのインスタンスに固定します。1 台のスロットに載るのは、同時に 1 人だけです。

## Fargate と比べる

**乗り換える理由は起動の速さではありません。** 得られるのは、速い I/O と、確実に残るホームと、Fargate より大きなワークスペースです。

| | Fargate（`ecs`） | EC2 プール（`ecs-ec2`） |
|---|---|---|
| ウォームな起動 | 約 105 秒 | 84〜110 秒。乗り換えるほどの差はない |
| ホーム | EFS。小さなファイルは 8〜30 倍遅い | EBS gp3。小さなファイル 2,000 個が 0.04 秒（EFS は 30.7 秒） |
| 大きさ | 決まった CPU とメモリの組だけ。最大 16 vCPU・120 GiB | インスタンスタイプで決まる。タスクは何も予約せず、インスタンスを使う |
| ワークスペースあたりのリソース | 2 つ（サービスと EFS のアクセスポイント） | 6 つ（インスタンス・ボリューム・コンテナインスタンスの登録・タスク定義も） |
| 使われないときの費用 | EFS（使った分） | EBS（**確保した分**）と、動いているスロット |

## 複数の AZ で使う前に

EBS のホームは AZ の外に出られません。Control Plane は、そのユーザーのホームがある AZ でスロットを起動します。

| 決まりごと | 中身 |
|---|---|
| AZ ごとに EFS のマウントターゲットが要る | ログイン情報は EFS に置くので、マウントターゲットのない AZ に載ったタスクは起動しない |
| 新しいホームは AZ に散らばる | ホームの少ない AZ から順に置くので、1 つの AZ を失っても全員は巻き込まれない。その代わり、ホームはほかの AZ の空きスロットを使えず、プールが大きくなりやすい |
| 空きスロットの再利用が優先 | 分散が決めるのは、新しいスロットをどの AZ に作るかだけ |
| AZ にスロットのタイプの容量がない | 新しいホームは別の AZ に作られる。すでにあるホームは AZ を移れないので、起動が失敗する |

AZ を移す方法と、AZ を失ったときの手順は [AZ を移す・失う](#az-を移す失う) にあります。

## プールを建てる

1. `40-ec2-pool` をデプロイし、`30-ingress` を `WsRuntime=ecs-ec2` で更新します。`Ec2SlotLaunchTemplate` には `40-ec2-pool` の出力 `SlotLaunchTemplateId` を渡します

   ```bash
   aws cloudformation deploy --stack-name af-ecs-ec2-pool \
     --template-file cfn/40-ec2-pool.yaml --capabilities CAPABILITY_NAMED_IAM \
     --parameter-overrides NetworkStackName=af-ecs-network PlatformStackName=af-ecs-platform
   # then point the CP at it (this is the whole switch, and the whole rollback):
   aws cloudformation deploy --stack-name af-ecs-ingress --template-file cfn/30-ingress.yaml --s3-bucket "$CFN_BUCKET" --s3-prefix cfn \
     --capabilities CAPABILITY_NAMED_IAM --parameter-overrides WsRuntime=ecs-ec2 Ec2SlotLaunchTemplate=lt-0123456789abcdef0 ...
   ```

2. ワークスペースを 1 つ起動し、「**管理**」→「**スロット**」にスロットが出ることを確かめます。画面の見かたは [スロットを入れ替える・止める](slot-maintenance.ja.md) にあります
3. 戻すときは、`30-ingress` を `WsRuntime=ecs` で更新します

**ホームの中身は、どちらの向きにも移りません。** Fargate のホームは EFS に、`ecs-ec2` のホームは EBS に、別々に作ります。

| 切り替え | メンバーのホーム |
|---|---|
| Fargate → `ecs-ec2` | 新しい EBS のホーム（golden があればそのコピー）から始まる。EFS のホームは残るが、使われない |
| `ecs-ec2` → Fargate | 前の EFS のホームに戻る。EBS のホームは残って課金が続くが、使われない |

- 切り替える前に、メンバーに作業をコミットして push してもらいます。ホームの中のファイルは自分で移します

- `30-ingress` が 51,200 バイトを超えるので、S3 経由でデプロイします（[テンプレートの大きさの上限に気をつける](template-size.ja.md)）
- `Ec2SlotLaunchTemplate` が空のままだと、Control Plane はこのプロファイルでは起動しません
- 一度建てたあとは、`update.sh` がプールのスタックも一緒に更新します（[アップグレードする](upgrade.ja.md)）

## パラメータ

`30-ingress` のパラメータです。「—」の行は、Control Plane の環境変数だけで変えられます。

| パラメータ | 環境変数 | 既定 | 中身 |
|---|---|---|---|
| `WsRuntime` | `AF_RUNTIME` | `ecs` | `ecs-ec2` でアダプターを切り替える。戻すときもこの値を使う |
| `Ec2SlotLaunchTemplate` | `AF_ECS_EC2_LAUNCH_TEMPLATE` | — | `40-ec2-pool` の出力 `SlotLaunchTemplateId`。このプロファイルでは必須 |
| `Ec2SlotTypes` | `AF_ECS_EC2_SLOT_TYPES` | Intel の 2 クラス | `instanceType:memoryMiB[:vcpu]` を小さい順に。名前つきのクラスも書ける（[スロットのマシンの種類を選ぶ](slot-types.ja.md)） |
| `Ec2SlotAmiArm64` | `AF_ECS_EC2_AMI_ARM64` | 空 | arm64 のクラスを使うときだけ必須（[スロットのマシンの種類を選ぶ](slot-types.ja.md)） |
| `Ec2DefaultSlotClass` | `AF_ECS_EC2_DEFAULT_SLOT_CLASS` | 空（最初のクラス） | 自分にもテナントにも指定がないメンバーが載るクラス |
| `Ec2MaxSlots` | `AF_ECS_EC2_MAX_SLOTS` | `8` | すべてのクラスを合わせたインスタンスの上限。上限に達すると、費用が増える代わりに起動が失敗する |
| `Ec2SlotSleepSec` | `AF_ECS_EC2_SLOT_SLEEP_SEC` | `900` | タスクのないインスタンスを**停止**するまでの秒数。コンピューティングの課金が止まり、ルートボリュームの課金は続く。`0` で止めない |
| `Ec2SlotTerminateAfterSec` | `AF_ECS_EC2_SLOT_TERMINATE_AFTER_SEC` | `0`（オフ） | 同じ時計の次の段階。過ぎるとインスタンスを**終了**し、ルートボリュームも消える。`14400`（4 時間）がおすすめ |
| `Ec2HomeGiB` | `AF_ECS_EC2_HOME_GB` | `50` | ユーザーごとのホームのボリューム（gp3）の GiB |
| `Ec2HibernateAfterSec` | `AF_ECS_EC2_HIBERNATE_AFTER_SEC` | `0`（オフ） | 使われないホームを退避するまでの、配備の既定（[ホームの休止と予備のコピーを設定する](slot-homes.ja.md)） |
| `Ec2HostReserveMb` | `AF_ECS_EC2_HOST_RESERVE_MB` | `auto` | スロットのメモリのうち、ワークスペースに渡さない分（[ワークスペースが使えるメモリ](#ワークスペースが使えるメモリ)） |
| — | `AF_ECS_EC2_SLOT_LOST_AFTER_SEC` | `300` | EC2 では `running` なのにクラスターからエージェントに到達できないスロットを、あきらめてワークスペースをほかで作り直すまでの秒数。`0` で呼び出し側が許すかぎり待つ |
| — | `AF_ECS_EC2_GOLDEN_AUTOBAKE` | `1`（オン） | golden snapshot を自動で焼く（[golden snapshot](#golden-snapshot)） |
| — | `AF_ECS_EC2_GOLDEN_BAKE_SEC` | `60` | 焼き込みを見に行く間隔。1 回に 1 段階ずつ進むので、焼き込みの速さでもある |

応答しないスロットを探す設定（`AF_ECS_EC2_DARK_SLOT_ACTION`・`AF_ECS_EC2_DARK_AFTER_SEC`・`AF_ECS_EC2_DARK_BOOT_GRACE_SEC`）は [応答しないスロットに気づく](slot-maintenance.ja.md#応答しないスロットに気づく) にあります。

`40-ec2-pool` には、スロットのルートボリュームの大きさ `SlotRootVolumeGiB`（既定 100）と、アラームの宛先 `PoolAlarmEmail` があります。

## ワークスペースが使えるメモリ

スロットのメモリは、全部がワークスペースのものではありません。同じインスタンスで dockerd・containerd・ECS エージェント・SSM・EFS の stunnel が動いています。また、段の値は実際より大きめです（「8192 MiB」の m7i.large が報告するのは 7784 MiB）。

そのため、コンテナの上限は段の値から予約を引いた値です。Console には、ワークスペースが実際に使える量がインスタンスと並んで `6 GiB (of 8 GiB)` のように出ます。

| `Ec2HostReserveMb` | ワークスペースの上限 |
|---|---|
| `auto`（既定） | どの段でも 2 GiB を引く。8 GiB のスロットなら 6 GiB |
| 数値 | その MiB を引く。`0` は何も引かず、段の値そのものが上限になる（`off` とは違う） |
| `off` | 上限なし |

- **`off` にしないでください。** 上限がないと、1 つのワークスペースが数 GB の匿名メモリを使っただけで、インスタンスがページの再読み込みを繰り返すスラッシングに陥ることがあります。スワップがないのでカーネルがページキャッシュを捨て、各デーモンが自分の実行ファイルをディスクから読み直し続けます。EC2 からは正常に見えるまま、スロットはクラスターに応答しなくなります。実際の配備で起き、そのワークスペースは何時間も起動できませんでした
- 段が小さすぎて予約を引けないときは、上限をかけません（ログに出ます）
- **予約を変えると、動いているタスクが入れ替わります。** タスク定義の一部なので、変えたあとの最初の起動で新しいリビジョンになります。イメージを変えたときと同じです
- 認識できない値は `auto` として扱います

## I/O のアラームを受け取る

`40-ec2-pool` の `PoolAlarmEmail` にメールアドレスを入れると、SNS トピックと 2 つの CloudWatch アラームができます。既定ではオフです。

| アラーム | 鳴るとき |
|---|---|
| `af-<スタック名>-slot-io-queue` | どれかの EBS ボリュームのキューの深さが 2 を超えた状態が 15 分（5 分を 3 回）続いた |
| `af-<スタック名>-slot-io-credits` | どれかのインスタンスの EBS バイトクレジット（`EBSByteBalance%`）が 80% を下回った状態が 10 分続いた |

- 健全なスロットはクレジットを 99% に保ちます。どちらのアラームも、ワークスペースがインスタンスをスラッシングに追い込んだときにははっきり鳴ります
- **このスタックだけには絞れません。** メトリクスはボリュームとインスタンスの ID ごとで、それらはプールが動きながら作ります。Metrics Insights はタグで絞れないので、クエリはそのリージョンのアカウント全体を見ます。アカウントがこの配備専用のときにオンにします
- メールの購読は、届いた確認メールで承認するまで有効になりません
- 鳴ったら、[応答しないスロットを止める](slot-maintenance.ja.md#応答しないスロットを止める) を見ます

## golden snapshot

新しいホームは、初回の起動で 4 つの CLI などの導入（boot-install、約 48 秒）と、空の npm キャッシュの分だけ遅くなります。それを済ませたホームを 1 つ焼いておくと、以後の新しいユーザーはそのコピーから始まります。これが golden snapshot です。

### 自動で焼く

既定では、Control Plane が自分で焼きます。

1. いま動いているイメージの golden がないと、Control Plane は予約したシード用のメンバーをふつうの起動の経路で起動します
2. そのホームを候補として取り込みます
3. 候補から 2 人目の予約メンバーを起動し、起動できたら公開します

| 状況 | 起きること |
|---|---|
| 空きのスロットが 2 台ない | 焼き始めない。誰かを立ち退かせてまでは焼かない |
| 同じイメージで候補が 2 回失敗した | そのイメージでは焼くのをやめる |
| golden がないか、古い | 新しいホームは空から作る。初回の起動が遅いだけで、壊れはしない |

どの状況かは、「**管理**」→「**スロット**」の「**golden snapshot**」の欄に出ます。golden はアーキテクチャごとに 1 つで、プールの全員で共有します（[Graviton（arm64）のクラスを足す](slot-types.ja.md#gravitonarm64のクラスを足す)）。`af-membership` タグを持たないので、ワークスペースを破棄しても消えません。

### 手で焼く

`AF_ECS_EC2_GOLDEN_AUTOBAKE=0` にした配備では、リリースのたびに自分で焼きます。自動で焼く配備でも、その場で焼きたいときに使えます。

1. シード用のメンバーを用意し、Console からそのワークスペースを起動して、起動が終わるまで待ちます
   - シードは**自分でサインインできるアカウント**にします。管理者が他人のワークスペースを起動する方法はありません。使い捨てのテナントに自分のアカウントを足すのが手軽です
   - シードに**リポジトリをクローンしないでください**。`~/repos` はホームにあるので、すべての新しいユーザーに配られます
2. ワークスペースを止め、スイーパーが**スロットを止める**まで待ちます。ログに `stopping slot <id> (home stays attached)` と出ます
   - ボリュームが外れるのを待たないでください。止めてもホームは付いたままなので、`available` にはなりません。止まったスロットから焼くのが正しい手順です
3. 焼きます。`--image` は Control Plane の `AF_ECS_WORKSPACE_IMAGE` と完全に同じ文字列にします

   ```bash
   ./bake-golden.sh --workspace af-ws-<tenant>-<seed> --image <the exact AF_ECS_WORKSPACE_IMAGE>
   ```

4. シードのワークスペースを破棄します。Console で使い捨てのテナントから自分を外すとき、確認の「**ワークスペースと home も破棄する（取り消せません）**」に印を付けます。API なら `DELETE /api/admin/workspaces {tenant_slug,user_key}` です
   - 名簿から外せないのは、自分の**最後の**所属だけです
   - シードを破棄しないとスロットが空かず、空きが 2 台そろわないと自動の焼き込みも始まりません

- **イメージや CLI の版が変わるリリースのたびに焼き直します。** Control Plane は `af-image` タグを動いているイメージと比べ、古い golden は使わずに空のホームを作ります。焼き忘れても古い CLI が配られることはありません
- `bake-golden.sh` には `--pool`（既定は `AF_ECS_EC2_POOL`、なければ `AF_ECS_CLUSTER`）と `--region` もあります

## AZ を移す・失う

### ユーザーを別の AZ に移す

EBS のボリュームを別の AZ に動かす操作はありません。移すとは、snapshot から作り直すことです。snapshot は AZ を持たないので、ホームの退避がそのまま使えます。

1. そのワークスペースを止めます
2. ホームを退避させます。super_admin が「**管理**」でテナントの「**使われない home の退避**」に短い値（`1h` など）を入れ、スイーパーに拾わせます。**この値はテナントの全員に効きます**。終わったら値を戻します
3. 移したい AZ にだけ空きのスロットがある状態で起動します。ホームは空いているスロットの AZ（なければスロットを作れた AZ）に戻ります

この方法では、行き先の AZ を指定できません。1 人だけを決まった AZ に移したいときは、AWS CLI で作り直します。各コマンドが成功したことを確かめてから次に進み、失敗したらそこで止めます。どこで止めても、元のボリュームは残っています。

1. ワークスペースを止め、「**スロット**」の画面の home の一覧でそのホームが「**未接続**」になっていることを確かめます。終わるまで起動しないよう、メンバーに伝えます（テナントで「**サインイン時の自動起動**」がオンなら、サインインでも起動します）
2. 元のボリュームを特定し、`available` で、どこにも付いていないこと（`Attachments` が空）を確かめます

   ```bash
   vol=$(aws ec2 describe-volumes --filters Name=tag:af-workspace,Values=<ws> Name=tag:af-role,Values=home \
     --query 'Volumes[0].VolumeId' --output text)
   aws ec2 describe-volumes --volume-ids "$vol" --query 'Volumes[0].[State,AvailabilityZone,Attachments]'
   ```

3. snapshot を作り、完了を待ちます

   ```bash
   snap=$(aws ec2 create-snapshot --volume-id "$vol" --query SnapshotId --output text)
   aws ec2 wait snapshot-completed --snapshot-ids "$snap"
   ```

4. 行き先の AZ に新しいボリュームを作り、`available` になるまで待ちます。`af-role` を `home-moving` にしておくので、まだ Control Plane からは見えません

   ```bash
   new=$(aws ec2 create-volume --availability-zone <target-az> --snapshot-id "$snap" --volume-type gp3 --encrypted \
     --tag-specifications 'ResourceType=volume,Tags=[{Key=af-pool,Value=<pool>},{Key=af-role,Value=home-moving},
       {Key=af-workspace,Value=<ws>},{Key=af-membership,Value=<membership>},{Key=af-tenant,Value=<tenant>},{Key=Name,Value=<ws>-home}]' \
     --query VolumeId --output text)
   aws ec2 wait volume-available --volume-ids "$new"
   ```

5. 元のボリュームの役割を外し、新しいボリュームをホームにします。この順にすると、Control Plane からホームが 2 つ見えることがありません

   ```bash
   aws ec2 create-tags --resources "$vol" --tags Key=af-role,Value=home-retired
   aws ec2 create-tags --resources "$new" --tags Key=af-role,Value=home
   ```

6. メンバーがワークスペースを起動し、ファイルがそろっていることと、home の一覧の場所が行き先の AZ であることを確かめます
7. 確かめ終えてから、元のボリュームと snapshot を消します

   ```bash
   aws ec2 delete-volume --volume-id "$vol"
   aws ec2 delete-snapshot --snapshot-id "$snap"
   ```

- **Control Plane がホームを探すのは `af-membership` と `af-role=home` の組です。** 同じメンバーに `af-role=home` のボリュームが 2 つあると、最初に見つかったほうを使います。手順 5 の順を守ってください
- 手順 5 より前に止めたときは、新しいボリュームを消すだけで元に戻ります
- 手順 5 のあと、**起動する前**なら、新しいボリュームを `home-moving` に戻してから、元のボリュームを `home` に戻します。この順なら、ホームが 2 つ見えることはありません
- **起動したあとは、タグを戻さないでください。** 新しいボリュームにはメンバーの新しい作業が書かれていて、元のボリュームは古いままです。戻すときは、まずワークスペースを止め、新しい作業をコミットして push するなどで保全してから、同じ手順で新しいボリュームを元の AZ に移します
- 45 GiB のホームの snapshot には 30〜40 分かかります。戻した直後は、触ったところが遅くなります

### AZ を失ったとき

ホームはほかの AZ へ移せないので、障害が起きる前に備えておく必要があります。[予備のコピー](slot-homes.ja.md#ホームの予備のコピーを取る) をオンにしておいてください。

| 対象 | 起きること |
|---|---|
| その AZ にホームがあるユーザー | 起動できない。「起動中」が続いたあと、停止に戻る |
| ログイン情報 | 無事。EFS はリージョンのリソースで、どの AZ にもマウントターゲットがある |
| スイーパー | 応答しないだけのインスタンスには触らない。マウントを外せなかったホームは外さない |
| 新しいユーザー | `RunInstances` が容量エラーを返したときだけ、別の AZ に作られる |

1. 障害の AZ のサブネットを Control Plane の `AF_ECS_SUBNETS` から外し、Control Plane を再起動します。新しいホームとスロットがその AZ を選ばなくなります
   - 参照用のスタックでは、`AF_ECS_SUBNETS` は `00-network` の `PrivateSubnets` から入ります。パラメータでは外せず、タスク定義を手で直すと次のデプロイで元に戻ります
2. AZ が戻らないときは、予備のコピーからホームを作り直します（[予備からホームを戻す](slot-homes.ja.md#予備からホームを戻す)）

## 使い始める前に知っておくこと

### 止めたワークスペースとスロット

- **ワークスペースを止めても、スロットはそのユーザーのものです。** ホームは付いたままで、同じ人が戻ると付け直しもマウントもなしに同じスロットに戻ります
- `Ec2SlotSleepSec`（既定 15 分）たつと、スイーパーがそのスロットを**停止**します。イメージのキャッシュはルートボリュームに残ります。止まったスロットの費用はルートボリュームだけです（既定の 100 GiB で月約 $9.6。動いていれば約 $95）
- ホームを外された空のスロット（立ち退き・大きさやクラスの変更・破棄・golden の焼き込みのあと）も、同じ時間で止まります

### 2 つのアイドルの時計

| 時計 | 見ているもの | 止めるもの |
|---|---|---|
| `AF_WS_IDLE_TIMEOUT` / テナントの自動停止 | 人 | ワークスペース（どのランタイムにもある） |
| `Ec2SlotSleepSec` | ワークスペースが止まってからの時間 | スロットのインスタンス |

人が離れると、テナントで決めた時間でワークスペースが止まり、その 15 分後にインスタンスが止まります。テナントの自動停止は [放置されたワークスペースを止める](../../admin/resources/idle-stop.ja.md) にあります。

### 3 つの上限

| パラメータ | 決めるもの |
|---|---|
| `Ec2MaxSlots` | 同時に**働ける**人数 |
| `Ec2SlotSleepSec` | **動いている**インスタンスの数 |
| `Ec2SlotTerminateAfterSec` | **存在する**インスタンスの数。オフだと、これも `Ec2MaxSlots` になる |

- **止めるとコンピューティングの課金が終わり、終了してはじめてルートボリュームの課金が終わります。** `Ec2SlotTerminateAfterSec` がオフだと、残るルートボリュームは `Ec2MaxSlots` まで増える一方です。`Ec2MaxSlots` を上げると、その台数分のルートボリュームを誰も働いていなくても払い続けます。実際の配備では、手でインスタンスを終了するしか解決策がありませんでした
- **`14400`（4 時間）がおすすめです。** 利用者の待ち時間は 25 秒ほど増えるだけです。同じ日に戻ればインスタンスを起こし（約 110 秒）、次の日なら新しく作ります（約 135 秒）
- スロットを取り戻すのは上限に達したときだけです。上限より下なら、新しいユーザーには新しいスロットを作ります。上限では、**同じインスタンスタイプの**いちばん長く休んでいる人を立ち退かせます。タスクが動いているワークスペースには触りません
- **上限で、合わない大きさのインスタンスは渡さずに消します。** 上限がすべて、そのメンバーが使えない大きさで埋まっているときは、使えない大きさの休んでいるインスタンスを 1 台終了し（空のものから、なければいちばん長く休んでいるもの）、代わりを作ります。前の持ち主が失うのはイメージのキャッシュだけで（戻ったときの起動が約 110 秒から約 135 秒に）、ホームは先に外して消しません。これは `Ec2SlotTerminateAfterSec` と関係なく動きます
- 何も空けられないとき（すべて動いているか、起動中のものが押さえている）は、起動が「プールが満杯」というエラーで失敗します

### 予備のスロットは持たない

朝いちばんの人は、止まったスロットを起こすか（約 110 秒）、なければ作ります（約 135 秒）。

- 空のスロットを動かしたままにすると、約 92 秒（43 秒と 135 秒の差）を、1 時間ごとにインスタンス 1 時間分の費用で買うことになります。それでもよければ `Ec2SlotSleepSec=0` にします
- 止めたインスタンスを何台か残しておく設定はありません。止まったインスタンスを使えるのはその持ち主だけで（ホームが付いたまま）、ほかの人に回すと起こす・付ける・マウントするで 123〜143 秒かかり、新しく作るのと変わりません

### スロットについて

- **ユーザーは AZ を移れません。** ホームを作った AZ に固定されます。そこでスロットを動かせなければ、そのユーザーは起動できません
- **ルートボリュームは前の利用者と共有です。** `/tmp` は tmpfs（ディスクに書かない・大きさに上限・`exec,nosuid,nodev`、`AF_ECS_EC2_TMP_OPTS`）で、ECS のタスクの後片付けは 5 分に縮めてあります。それでも、イメージのキャッシュとコンテナの書き込み層は共有です
- **スロットにパッチを当てるには、このスタックを更新して古いスロットを手放します。** スロットは起動した AMI のカーネルのまま、その場でパッチされません。セキュリティの方針が決める周期で入れ替えます。タスクに `SYS_ADMIN` を足しているので、名前空間や netfilter のカーネルの修正はほかより大事です。入れ替えかたは [スロットを入れ替える・止める](slot-maintenance.ja.md) にあります
- **ログイン情報は EFS に残ります。** `.config`・`.ssh`・`.git-credentials`・`.gitconfig`・`.claude`・`.claude.json`・`.codex`（合わせて 100 MiB 未満）は EFS のアクセスポイントに置き、エントリーポイントがホームにリンクします。単一 AZ のボリュームを失っても、ログインは消えません
- **作業ディスク（`WsDiskGiB`）は効きません。** ホームがもともとローカルの EBS なので、`AF_WS_SCRATCH` を渡しません

## 関連

- [スロットのマシンの種類を選ぶ](slot-types.ja.md)
- [ホームの休止と予備のコピーを設定する](slot-homes.ja.md)
- [スロットを入れ替える・止める](slot-maintenance.ja.md)
- [スロットプールで困ったとき](slot-troubleshooting.ja.md)
- [ワークスペースの大きさを決める](workspace-size.ja.md)：Fargate の大きさ
- [費用を抑える](cost.ja.md)
