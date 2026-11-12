---
audience: "AWS の ECS 配備で、ワークスペースの外向きの通信を egress ゲートウェイに通し、記録して絞る配備管理者"
source_of_truth: "`deploy/aws/ecs/cfn/00-network.yaml`（`EgressRoute`・`Gateway*`）、`deploy/aws/ecs/pause.sh`・`standup.sh`・`clear-params.sh`、Control Plane の `af-cp egress-agent` / `egress-relay`"
---

# 外向きの通信をゲートウェイで制御する

[English](egress-gateway.md) | 日本語

ECS の配備で、ワークスペースの外向きの通信を egress ゲートウェイに通します。
すべての接続が宛先の名前つきで記録され、許可リストで絞れるようになります。許可リストの運用は [外向きの通信を制御する](../secure/egress.ja.md) にあります。

## 全体像

| あなたがやること | 起きること |
|---|---|
| ワークスペース用のアドレスを先に割り当て、許可リストに登録してもらう | 切り替えの前から、取引先がそのアドレスを通せる |
| `00-network` を `EgressRoute=gateway` でデプロイし、`update.sh` を実行する | ワークスペースのルートがゲートウェイに向き、Control Plane がワークスペースにプロキシを渡す |
| 管理の「**通信**」でモードと許可リストを決める | ゲートウェイのエージェントがその方針を Squid に書き、接続を記録して Control Plane に送る |
| `pause.sh` で一時停止する | ワークスペースが止まり、記録を送り終えてから、ゲートウェイを 0 台にする |

Control Plane・スロットのインスタンス・推論エンジン・ホーム用のタスクは、どちらの経路でも NAT ゲートウェイから出ます。ゲートウェイを通るのはワークスペースだけです。

## NAT ゲートウェイとの違い

`00-network` の `EgressRoute` が、**ワークスペース**のルートテーブルの既定ルートの行き先を決めます。

| | `nat-gateway`（既定） | `gateway` |
|---|---|---|
| 記録 | なし。管理の「通信」は空のまま | ワークスペースのすべての接続を、名前（SNI / Host）とアドレスで、ワークスペースごとに記録する。「通信」にはホストごとの件数が出る。ワークスペースごとのイベントは `GET /api/admin/egress/events`（super_admin）で読み、Console には出ない |
| 許可リスト | 効かない | log-only か enforce（リストにない名前を拒否） |
| 時間あたりの費用 | NAT ゲートウェイ（リージョンにより約 $0.045〜0.062/時） | NAT ゲートウェイ（Control Plane とホストが使う）＋ゲートウェイのインスタンス（`t3.small` で約 $0.02〜0.03/時）＋その Elastic IP（$0.005/時） |
| GB あたりの費用 | ワークスペースの全通信（S3 も含む）に NAT のデータ処理料（$0.045〜0.062/GB） | ワークスペースの通信にはかからない（通常のデータ転送料だけ。同じリージョンの S3 は無料） |
| 運用 | AWS が管理する | 動かすインスタンスが 1 台増える。AMI の更新で置き換わり、そのあいだ数分ワークスペースが外へ出られない |
| 可用性 | AWS の管理で、1 つの AZ | 1 つの AZ に 1 台。1 台の Auto Scaling グループが置き換える |
| HTTP 以外（SSH の git、ほかのポート） | 通る | enforce では `tcp:` のエントリで名前を挙げた宛先だけ。ほかは落として記録する。log-only（既定のモード）では通して、遮断候補として記録する |
| ワークスペースの送信元アドレス | `NatEip` | ゲートウェイ自身のアドレス。許可リストに 2 つめとして登録する |

## ゲートウェイの構成

- `PublicSubnet0` に、AL2023 のインスタンス 1 台の Auto Scaling グループを置きます
- インスタンスは起動時に、スタックが持つネットワークインターフェース `GatewayEni` を付けます。ワークスペースのルートテーブルはこれを指し、その Elastic IP（`GatewayEip`）がワークスペースの送信元アドレス、プライベートアドレスが明示プロキシ（`EgressProxyAddr`、`:3128`）になります
- 置き換わったインスタンスも同じインターフェースを付けるので、ルート・プロキシのアドレス・送信元アドレスは動きません
- Squid が、ワークスペースのサブネットからのポート 80 を横取りし、明示プロキシも受けます。横取りした 443 は `af-cp egress-relay` が受け、ワークスペースが接続しようとしたアドレスへの CONNECT として Squid に渡します。ほかの TCP は `af-cp egress-relay --mode tcp` が受けます
- 何も転送しません（`ip_forward=0`）
- `af-cp egress-agent`（Control Plane のイメージからコピーしたバイナリ）が、ユーザー `squid` として動きます。Control Plane の方針から Squid の許可とモードのファイルを書き、ログを `/internal/egress` に送ります。認証には、スタックが Secrets Manager に作る `AF_EGRESS_TOKEN` を使います
- Control Plane は `AF_EGRESS_PROXY_ADDR` と `AF_EGRESS_TOKEN` を受け取ります（`30-ingress` の `EgressProxyAddr` / `EgressTokenSecretArn`）。Fargate と `ecs-ec2` のワークスペースのタスク定義にプロキシの変数が入り、`NO_PROXY` にはタスクロールの資格情報のエンドポイント（`169.254.170.2`）、インスタンスメタデータ（`169.254.169.254`）、Control Plane の内部ホストが入ります
- トークンはワークスペースには届きません。エージェントの資格情報です

## HTTP 以外の TCP を通す

SSH の git やデータベースへの接続です。`GatewaySg` はワークスペースのサブネットからの TCP をすべて受け、通すかどうかは nftables と中継が決めます。

| モード | 起きること |
|---|---|
| enforce | 管理者が **`tcp:` のエントリ**を足すまで、HTTP(S) 以外は出ない。名前のない宛先は落として記録する |
| log-only（既定のモード） | どのポートも通し、拒否される接続として記録する。enforce に切り替えるまで何も閉じない |

- エントリは管理の「**通信**」で足すか、メンバーの申請を管理者が承認します。例: `tcp:git.example.com:22`、`tcp:db.example.com:1433`
- クライアントを別のポートへ向けるサービスには、範囲 `tcp:db.example.com:11000-11999` を使えます。最大 2048 ポートで、ホストは完全一致か IPv4 アドレスです。53・80・443・853・3128 は指定できません
- エージェントは 30 秒ごとにホストを名前解決し、中継は過去 15 分に解決されたアドレスのそのポートへの接続を通します
- どの接続も、アドレスとポートで記録され、ほかと同じようにワークスペースに結び付けられます（ポートも宛先の一部です）
- VPC・リンクローカル（メタデータサービス）・ループバック・このホスト・ポート 53 / 853 は、どちらのモードでも拒否します
- UDP と ICMP は、セキュリティグループで落とし、記録しません
- **切り替えの前に、宛先側のファイアウォールで、ゲートウェイの Elastic IP（ワークスペースの送信元アドレス）をそのポートで通してもらいます。** 別のポートの範囲に向けるマネージドなデータベースは、その範囲を通して、エントリでも名前を挙げます
- 中継のログはインスタンスの `/var/log/squid/af-tcp.log`、サービスは `journalctl -u af-egress-relay-tcp` です
- この経路は、実機の配備ではまだ動かしていません

### 拒否された TCP も、最初はつながったように見える

横取りは中継が判断する前に起きるので、中継はワークスペースの接続をいったん受け、元の宛先（`SO_ORIGINAL_DST`）を読み、そのあとで外へつながずにソケットを閉じて拒否します。

- ワークスペース側のハンドシェイクは終わっています。`nc -z host port` やただの `connect()` は成功します
- そのあとの読み書きが失敗します（EOF か、`Connection reset by peer` などのリセット）。どちらになるかはタイミングと未読のデータによります。最初の小さな書き込みは手元では成功することもあります
- 「つながった」は「許可された」の証拠になりません。中継のログ `af-tcp.log` で確かめます

判断を記録できた接続は、`<verdict> <entry>` で終わる 1 行になります。

| verdict | 意味 |
|---|---|
| `deny` | 拒否。enforce では `tcp:` のエントリにない宛先。**どちらのモードでも**、囲われた宛先（VPC・リンクローカル・ループバック・このホスト・ポート 53 / 853 など）と、方針をまだ読み込んでいないあいだのすべての宛先 |
| `would-deny` | log-only が通した接続 |
| `deny-cap` | 中継の接続数の上限で拒否した接続 |
| `admit` | 許可した接続 |

- 元の宛先を読めなかったときや、ログを書けなかったときは、行を残しません
- 管理画面のイベント（`GET /api/admin/egress/events`、「通信」）は、Control Plane がこのログから非同期に作るものです。同じ行ではありません

### ゲートウェイの input チェーン

ゲートウェイで `nft list ruleset` を実行すると `chain input { ... policy accept; }` と出ますが、インスタンスが開いているわけではありません。

- `input` チェーン（`/etc/af-egress/nft.conf`、テーブル `inet af_egress`）は、ワークスペースのサブネットからゲートウェイのインターフェースに届くパケットのうち、確立済み・関連の通信、`prerouting` チェーンが向け直した通信（`ct status dnat`）、TCP 3128（明示プロキシ）だけを受けます
- それ以外は `af-egress-input-drop ` の接頭辞でログに残し（1 秒に 10 件まで。`journalctl -k` で読む）、最後の `drop` で落とします
- どのルールもワークスペースのサブネットからの送信元にだけ当たるので、`policy accept` はそれ以外（インスタンス自身や VPC の通信）に効きます
- `forward` チェーンは `policy drop` で、`ip_forward` は 0 です。ゲートウェイを経由して何かがルーティングされることはありません

## 有効にする

既存のスタックは、この手順を踏むまで `nat-gateway` のままです。先にワークスペース用のアドレスを割り当てて、許可リストに登録してもらいます（[アドレスを 2 つ登録する](#アドレスを-2-つ登録する)）。

1. `00-network` をデプロイし、`update.sh` を実行します

   ```bash
   aws cloudformation deploy --stack-name af-ecs-network --template-file cfn/00-network.yaml \
     --capabilities CAPABILITY_IAM --parameter-overrides EgressRoute=gateway \
     GatewayEipAllocationId=eipalloc-... GatewayEipAddress=<its public address>
   ./update.sh --profile <p> --region <r>    # 20-platform (the token read) and 30-ingress
   ```

2. ゲートウェイが通信を通せるようになるまで待ちます（下の表）
3. 動いているワークスペースは、次の起動でプロキシの変数を受け取ります。それまでは横取りで通ります

`GatewayEip*` の 2 つのパラメータを渡さないと、スタックが自分でアドレスを割り当てます（保持もします）。その場合、ワークスペースがすでにそのアドレスから出始めてから、アドレスがわかります。

最初のコマンドが終わった時点で、ルートは `GatewayEni` に向きます。そのあとの流れは次のとおりです。

| 段階 | ワークスペースの外への通信 |
|---|---|
| インスタンスが `GatewayEni` を付けるまで（数分。インスタンスの `/var/log/af-egress-gateway.log`） | 出られない |
| `update.sh` がエージェントの設定（`/af-egress/<network stack>/agent`）を書き、エージェントが最初の方針を取るまで | Squid がすべて拒否する。Control Plane に届かないゲートウェイは、開かずに閉じたままになる |
| 最初の方針を取ったあと | 方針どおりに通る |

- 最初の方針を取るまで、`af-egress-install` は失敗して 30 秒後にやり直します（設定がないときはすぐ、方針がないときはエージェントを 30 秒待ってから失敗します）。`update.sh` が設定を書いてからの空白は 1 分ほどに、Control Plane のイメージの最初の取得と Control Plane の起動の時間を足したものです
- そのあとは 10 分ごとに動きます。新しい Control Plane のイメージは、これでエージェントに届きます
- インスタンスの置き換えのあとも、最初の方針を取るまではすべて拒否します

エージェントは Control Plane のイメージからコピーしたバイナリなので、ゲートウェイはそのイメージを取得します。

- `GatewayInstanceType` は、いつも `GatewayAmiId` のアーキテクチャに合わせます
- Control Plane のイメージが単一アーキテクチャのときだけ、その CPU アーキテクチャにも合わせます。マルチアーキテクチャのイメージなら、ゲートウェイは自分のアーキテクチャの版を取ります。x86_64 のゲートウェイを arm64 の Control Plane（`30-ingress` の `CpArch`）の隣で動かせます

## アドレスを 2 つ登録する

ゲートウェイを使う配備は、2 つの固定のアドレスから外へ出ます。

| 送信元 | アドレス |
|---|---|
| Control Plane・スロットのホスト・推論エンジン | NAT ゲートウェイの `NatEip`（[外向きの IP アドレスを固定する](egress-ip.ja.md)） |
| すべてのワークスペース | ゲートウェイの `GatewayEip` |

配備を許可リストに載せてもらう組織には、両方を登録してもらいます。ワークスペースのアドレスも、NAT のアドレスと同じように扱います。

### 切り替えの前に割り当てる

何も使っていないうちに、許可リストに登録してもらえます。

```bash
aws ec2 allocate-address --domain vpc --tag-specifications 'ResourceType=elastic-ip,Tags=[{Key=Name,Value=af-<network stack>-egress-gateway-eip}]'
```

- 出てきた `AllocationId` と `PublicIp` を、`GatewayEipAllocationId` と `GatewayEipAddress` に渡します
- 2 つは両方渡すか、どちらも渡さないかです。片方だけだと、テンプレートの Rules が拒否します
- ALB は `GatewayEipAddress` でゲートウェイを受け入れます。自分が作っていない割り当てのアドレスは CloudFormation が読めないので、アドレスも渡します
- 渡したアドレスを、スタックが作ったり、置き換えたり、解放したりすることはありません

### 作り直しても残る

- スタックが自分で割り当てたアドレスは保持されます（`DeletionPolicy: Retain`）
- 出力の `GatewayEipAllocationId` と `GatewayEipAddress` は、誰が割り当てたかにかかわらずアドレスを運びます。`capture-env.sh` は、空で取得した 2 つのパラメータをこの出力で埋めます（`NatEipAllocationId` と同じ規則）
- `EgressRoute=gateway` のとき、`standup.sh` は建てる前に、割り当てがまだあること、アドレスが取得したものと同じこと、この配備のゲートウェイのインターフェース以外が持っていないことを確かめます

### 保持されたアドレスを渡し直す

`nat-gateway` に戻したゲートウェイや撤去した配備では、アドレスが割り当てられたまま、どこにも付かずに残ります（$0.005/時がかかり続けます）。次に `gateway` に切り替えるときや建て直すときは、新しく割り当てさせず、タグで探して渡します。

```bash
aws ec2 describe-addresses --output text --query \
  "Addresses[?Tags[?Key=='Name' && Value=='af-<network stack>-egress-gateway-eip']].[AllocationId,PublicIp,AssociationId]"
# one row with AssociationId None: pass AllocationId and PublicIp as
# GatewayEipAllocationId= GatewayEipAddress= (with EgressRoute=gateway), or write both lines
# into ~/.config/agent-fleet/deploy/<p>.<r>/params/00-network before standup.sh
```

> [!CAUTION]
> **2 つのパラメータは、ゲートウェイを作るとき（`gateway` への切り替えか、構築）に渡します。** すでにゲートウェイが動いているスタックに渡すと、`GatewayEipAssoc` が置き換わり、ワークスペースが使っているあいだにインターフェースのアドレスが付け直されます。その影響は測っていません。スタックが作ったアドレスは、渡さなくても保持されています。

### アドレスを 1 つにしたいとき

2 つめのアドレスをどうしても登録できない組織のための案です。

| 案 | 中身 | 状態 |
|---|---|---|
| ゲートウェイを NAT 経由のサブネットに置く | Squid の接続が `NatEip` から出る。ワークスペースの全通信に、NAT のデータ処理料（$0.045〜0.062/GB）がまたかかる | 作っていない |
| `NatEip` をゲートウェイに移す | NAT に新しいアドレスが要るので、結局許可リストが変わる。そのうえ外へ出られない時間ができる | 採らない |

## NAT ゲートウェイに戻す

逆の 2 コマンドでは戻せません。メンテナンスの時間を取ります。動いているワークスペースは、再起動するまで `HTTP(S)_PROXY` がゲートウェイを指したままで、ゲートウェイがなくなるとプロキシを使うクライアントがすべてタイムアウトします。先にプロキシを外します。

1. `30-ingress` のプロキシを空にします。ほかのパラメータは前の値のままです

   ```bash
   ./clear-params.sh --profile <p> --region <r> --stack <ingress stack> EgressProxyAddr EgressTokenSecretArn
   ```

   変更セットを作って表示します。`TaskDef`（Control Plane のタスク定義）と、新しいリビジョンに従う `Service` の `Modify`、2 つのキーが空になることだけを確かめ、表示された `--execute` のコマンドを実行します（`--yes` で確認を省けます）。
   - `update.sh` は使いません。ゲートウェイがあるあいだは、その出力を渡し直してしまいます
   - `deploy --parameter-overrides EgressProxyAddr= EgressTokenSecretArn=` も使いません。これでは空になりませんでした
2. 動いているワークスペースをすべて止めて起動し直します（`pause.sh --status` で一覧が出ます）。タスク定義に変数が残っていないことを確かめます。そのあいだも、通信はゲートウェイが横取りしています

   ```bash
   aws ecs describe-task-definition --task-definition <family> --query "taskDefinition.containerDefinitions[0].environment[?name=='HTTPS_PROXY']"
   ```

   空なら外れています。
3. `00-network` を `EgressRoute=nat-gateway` でデプロイします（`--capabilities CAPABILITY_IAM`）。ワークスペースのルートが NAT ゲートウェイに戻ります。`GatewayEip` は保持されます
4. `update.sh` を実行します。ネットワークの出力も `30-ingress` の値も空なので、ほかには何も変えません

## 一時停止する

`pause.sh` は、`00-network` の `GatewayEnabled` でゲートウェイを止めます。`false` で Auto Scaling グループを 0 台にします。

- `aws autoscaling` で手で 0 台にしないでください。次のスタックのデプロイで元に戻ります
- `GatewayEni` と `GatewayEip` は残ります。`--up` で、同じルートの行き先・プロキシのアドレス・送信元アドレスが戻ります。Elastic IP の料金（$0.005/時）はかかり続けます
- ゲートウェイを止めているあいだ、ワークスペースのルートは**行き先がなく捨てられます**。ワークスペースが動いていないから害がないだけです。`GatewayEnabled=false` のスタックでワークスペースを起動しないでください

止める順番:

1. ワークスペースを止めます。ゲートウェイを止めるのは Control Plane より前で、次の 2 つを確かめてからです
   - サービスの desired count にかかわらず、クラスターにワークスペースのタスクが残っていない（`AF_WS_TASK_WAIT_SEC`、既定は 600 秒）
   - Squid を止めてすべてのトンネルを記録させたうえで、Control Plane がログを全部受け取っている（ログのファイルの大きさが、エージェントが `state.json` に記録したオフセットより小さいかで確かめます。`AF_GATEWAY_DRAIN_SEC`、既定は 300 秒）
2. 0 台にするとインスタンスのディスクが消えるので、どちらかを確かめられないときは、`pause.sh` は失敗し、ゲートウェイと Control Plane を動かしたままにします

`--up` は、NAT ゲートウェイと同じデプロイでゲートウェイを起動し、2 回待ちます。

| いつ | 待つもの |
|---|---|
| Control Plane を起動する前 | インスタンスが `GatewayEni` を付け、Squid と `af-egress-agent` が動く |
| Control Plane が起動したあと | エージェントのいまの実行が方針を適用した（モードのファイルを書いて `policy applied` を記録した。Control Plane が止まっているあいだに始まった実行なら `reloaded Squid with the policy on disk`。どちらも Squid の再読み込みが成功したあと）。その最後の行が方針のファイルより古くない |

- その方針を渡せるのは Control Plane だけで、それまで Squid はすべて拒否します
- ゲートウェイが立ち上がらないと、Control Plane も止まったままです。それぞれの待ちは `AF_GATEWAY_WAIT_SEC`（既定は 1200 秒）で打ち切ります
- インスタンスでの確認は SSM Run Command で動きます。実行する人には `ssm:SendCommand`（ドキュメント `AWS-RunShellScript`、タグ `af-egress-gateway=<network stack>` のインスタンス）と `ssm:GetCommandInvocation` が要ります。インスタンスのロールは `AmazonSSMManagedInstanceCore` をすでに持っています
- `--status` はゲートウェイも表示します
- `standup.sh` は、取得した `GatewayEnabled=false` を `true` に置き換え、最後に最初の方針を待ちます

## 通信の記録を S3 に残す

Control Plane は、生の通信のイベントを消す前に、過ぎた UTC の 1 日分ずつ S3 に書き出せます（`AF_EGRESS_ARCHIVE_URL`）。`10-data` がそのための S3 Bucket を持つまでは無効です。

```bash
aws cloudformation deploy --stack-name af-ecs-network --template-file cfn/00-network.yaml \
  --capabilities CAPABILITY_IAM            # the S3 endpoint policy admits the bucket
aws cloudformation deploy --stack-name af-ecs-data --template-file cfn/10-data.yaml \
  --capabilities CAPABILITY_AUTO_EXPAND CAPABILITY_IAM --parameter-overrides EgressArchive=create
./update.sh --profile <p> --region <r>     # 20-platform's CP grant, then 30-ingress's URL
```

- 取得した状態に `EgressArchive=create` があれば、`standup.sh` も建て直しで同じことをします
- S3 Bucket の名前は `af-<data stack>-egress-archive-<account id>-<region>` です。暗号化され、TLS だけを受け、バージョニングはなく、書き出した日は `EgressArchiveRetentionDays`（既定は 365）日で消えます
- スタックを消しても S3 Bucket は**保持されます**。建て直しでは `EgressArchive=existing` で使い直します。`standup.sh` はこれを自分で設定します
- Control Plane のタスクロールが得るのは、その S3 Bucket の `egress/*` への `s3:PutObject` / `s3:GetObject` と、`s3:ListBucket` だけです。エンドポイントのポリシーも、このアカウントの同じ S3 Bucket だけを通します
- この方法で作った S3 Bucket でしか動きません。自分の S3 Bucket はエンドポイントで拒否されます
- パラメータと権限の詳細は `deploy/aws/ecs/cfn/PARAMETERS.md` の「The egress archive」にあります
- 実際のアカウントではまだ動かしていません

## 制約

Squid・エージェント・中継・この構成（一時停止と再開も含む）は、テスト用の配備でだけ確かめています。**本番ではまだ使っていません。**

- **生のイベントは 30 日で消えます。** Control Plane は、`AF_EGRESS_EVENT_RETENTION_DAYS`（既定は 30。`0` でずっと残す）より古い生の通信のイベントと、それを結び付けるアドレスの期間を消します。ホストごとの日ごとの件数は残ります。ECS のテンプレートにはこのパラメータがないので、配備は既定のままです。長く残すときは、上の S3 への書き出しを使います
- **enforce は、アドレスではなく TLS の名前（SNI）で判断します。** ワークスペースは、許可された SNI を付ければ、ポート 443 のどの公開アドレスにも送れます（明示プロキシでも横取りでも）。ほかのポート・ループバック・リンクローカル・VPC にはこの方法で届かず、許可リストにない名前はどのアドレスに送っても拒否されます。名前とアドレスを結び付けるには復号が要り、ゲートウェイは復号しません
- **横取りした 443 は `af-cp egress-relay` を通ります。** Squid 自身の横取り用の HTTPS ポートは SNI を名前解決し直し、ワークスペースのアドレスがその答えにないと接続を拒否します（409、`cache.log` に「Host header forgery」）。DNS の答えが入れ替わる名前（S3・ECR・STS）は、それでほとんど失敗していました
- 中継は Control Plane のイメージに入っていて、エージェントと一緒に起動します。最初のインストールまで、新しいゲートウェイは横取りした 443 を拒否します（方針を取るまでは、どのみちすべて拒否します）
- 新しい Control Plane のイメージは中継を再起動し、そのとき開いている横取りの TLS 接続を切ります
- 中継が持つ接続は最大 4,096 です。Squid が自分の側を閉じて 10 秒たった接続と、書き込みが 2 分止まった接続を閉じます

## ゲートウェイが動いているか確かめる

`EgressRoute=gateway` の配備で確かめます。

1. インスタンスを探して、シェルを開きます

   ```bash
   aws ec2 describe-instances --filters Name=tag:af-egress-gateway,Values=<network stack> Name=instance-state-name,Values=running --query 'Reservations[].Instances[].InstanceId' --output text
   aws ssm start-session --target <id>
   ```

2. 起動とサービスを見ます
   - `sudo tail -50 /var/log/af-egress-gateway.log`
   - `systemctl status squid af-egress-agent af-egress-relay af-egress-relay-tcp af-egress-nft af-egress-install.timer`
   - `sudo squid -k parse`（読み込んだ設定を表示し、誤りがあれば失敗します）
   - `ip route show default`（最初の既定ルートが、付けたインターフェースです）
3. 方針を見ます
   - `sudo cat /var/lib/af-egress-agent/af-allow.txt /var/lib/af-egress-agent/af-mode.conf` に、エージェントのヘッダーと Control Plane の許可リストが出ます。起動時の仮の内容（`# bootstrap: closed until the first policy`、すべて拒否）のままなら、まだ方針を取っていません
   - `journalctl -u af-egress-agent` に再読み込みが出ます
4. ワークスペースの端末から、`curl -sI https://example.com`（明示プロキシ）と `curl -sI --noproxy '*' https://example.com`（横取り）を実行します。ゲートウェイでは次を確かめます
   - `sudo tail /var/log/squid/af-egress.log` に、ワークスペースのアドレス・`TCP_TUNNEL`・SNI の付いた `af1` の行がそれぞれ 1 行
   - `sudo getfattr -n user.af-egress.gen /var/log/squid/af-egress.log` に世代、`sudo cat /var/lib/af-egress-agent/state.json` に送り終えたオフセット
   - 管理の「通信」にそのホストの件数、`GET /api/admin/egress/events` に 2 つのイベントが、ワークスペースに結び付いて出る
5. ログの切り替え: `sudo squid -k rotate` のあと 1 回リクエストし、エージェントが `af-egress.log.0` を読み終えて新しいファイルに進んだこと（イベントの重複も抜けもないこと）を確かめます
6. メタデータ: ワークスペースから次を実行し、拒否されることを確かめます。通ってしまうと、ワークスペースがゲートウェイのロールの資格情報を読めます

   ```bash
   curl -s -x <EgressProxyAddr> -X PUT -H 'X-aws-ec2-metadata-token-ttl-seconds: 60' http://169.254.169.254/latest/api/token
   ```

   Squid の `to_linklocal` が拒否し、nftables も Squid 自身の `169.254.0.0/16` への接続を落とします。
7. enforce: 管理の「通信」でモードを切り替え、エージェントの再読み込み（`af-mode.conf` に `ssl_bump terminate all` が入る）を待ちます。許可リストにない名前が、ワークスペースから明示プロキシでも横取りでも失敗し、拒否として記録されることを確かめます

## 関連

- [外向きの通信を制御する](../secure/egress.ja.md)：許可リストとモードの運用
- [外向きの IP アドレスを固定する](egress-ip.ja.md)
- [スクリプトで構築・一時停止・撤去する](lifecycle.ja.md)
- [費用を抑える](cost.ja.md)
