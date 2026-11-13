---
audience: "AWS の ECS 配備の送信元 IP アドレスを、取引先のファイアウォールや SaaS の許可リストに登録してもらう配備管理者"
source_of_truth: "`deploy/aws/ecs/cfn/00-network.yaml`（`NatEip`）と `deploy/aws/ecs/capture-env.sh`"
---

# 外向きの IP アドレスを固定する

[English](egress-ip.md) | 日本語

ECS の配備が外へ出るときの送信元 IP アドレスを調べ、作り直しても変わらないように保ちます。
社内のファイアウォールや SaaS の IP 許可リストには、このアドレスを登録してもらいます。

## 全体像

| あなたがやること | 起きること |
|---|---|
| NAT ゲートウェイの Elastic IP を調べて、許可リストに登録してもらう | 配備から外への通信は、すべてこのアドレスから出る |
| 作り直す前に、取得した状態に `NatEipAllocationId` があるか確かめる | 次の構築が同じアドレスを使う。許可リストを変えずに済む |
| 稼働中のスタックには `NatEipAllocationId` を渡さない | アドレスは保持されたまま、NAT ゲートウェイも置き換わらない |

## アドレスを調べる

既定の `EgressRoute=nat-gateway` では、配備は**ちょうど 1 つのアドレス**から外へ出ます。`00-network` の NAT ゲートウェイに付いた Elastic IP です。

- ワークスペースの git・モデルの API・パッケージの取得も、Control Plane 自身の通信も、すべてこのアドレスを通ります
- スロットと Control Plane のタスクは、パブリック IPv4 のないプライベートサブネットにあります。スロットの起動テンプレートも、パブリック IPv4 を付けません
- VPC エンドポイントは、プライベートのルートテーブルの S3 ゲートウェイエンドポイントだけです。これは自分のアドレスを持たずに AWS の S3 に届きます
- `EgressRoute=gateway` では、ワークスペースはゲートウェイのアドレスから出ます。両方を登録してもらいます（[外向きの通信をゲートウェイで制御する](egress-gateway.ja.md#アドレスを-2-つ登録する)）

```bash
alloc=$(aws cloudformation describe-stacks --stack-name af-ecs-network \
  --query "Stacks[0].Outputs[?OutputKey=='NatEipAllocationId'].OutputValue" --output text)
aws ec2 describe-addresses --allocation-ids "$alloc" --query "Addresses[0].PublicIp" --output text
```

> [!WARNING]
> `describe-addresses` は ALB のアドレス（`ServiceManaged: alb`）も表示します。これは受信用で AWS のものであり、変わらない保証もありません。許可リストに載せないでください。受信は FQDN で受けます。

## 作り直してもアドレスを保つ

`NatEip` には `DeletionPolicy: Retain` と `UpdateReplacePolicy: Retain` が付いています。`teardown.sh` で全部を消しても、スタックの操作でアドレスが解放されることはありません。次の構築では、その割り当て ID を渡します。

この ID は `capture-env.sh` が引き継ぎます。

- スタックが自分で EIP を作ったとき、`NatEipAllocationId` は空のパラメータで、本当の ID はスタックの出力にしかありません
- `capture-env.sh` は、**取得した値が空のパラメータと同じ名前の出力があれば、その値を使います**
- そのため ID はほかのパラメータと同じように取得した状態に入り、次の構築で渡されます

### 古い取得結果から建てる前に確かめる

この規則より前に取った取得結果には `NatEipAllocationId` の行がありません。そのまま建てると、2 つめの EIP が作られます。

- パラメータが空なので EIP がもう一度作られ、保持していたアドレスは使われないまま残ります
- 取引先が登録したアドレスが、知らないうちに変わります
- 使われない EIP は 1 つあたり月 $3.6 ほどかかります

建てる前に、取得した状態を見ます。

```bash
grep '^NatEipAllocationId=' ~/.config/agent-fleet/deploy/<p>.<r>/params/00-network
```

行がないか空のときは、配備の状態で対処が変わります。

| 配備 | 対処 |
|---|---|
| まだ動いている | 取得し直す（`capture-env.sh --force`）か、出力を読んで行を手で書く |
| すでに撤去した | 保持されたアドレスはアカウントに残っている。新しく割り当てさせず、タグで探して渡す |

出力を読んで手で書くとき:

```bash
aws cloudformation describe-stacks --stack-name af-ecs-network \
  --query "Stacks[0].Outputs[?OutputKey=='NatEipAllocationId'].OutputValue" --output text
# -> NatEipAllocationId=eipalloc-... into ~/.config/agent-fleet/deploy/<p>.<r>/params/00-network
```

撤去したあとにタグで探すとき:

```bash
aws ec2 describe-addresses --output text --query \
  "Addresses[].[AllocationId,PublicIp,AssociationId,Tags[?Key=='Name']|[0].Value]"
# the row named af-<stack>-nat-eip with no association id is the one that was retained
```

見つけた ID を、新しく建てるときに渡します。

```bash
aws cloudformation deploy --stack-name af-ecs-network --template-file cfn/00-network.yaml \
  --capabilities CAPABILITY_IAM --parameter-overrides NatEipAllocationId=eipalloc-...
```

## 稼働中のスタックにはパラメータを渡さない

> [!CAUTION]
> **動いている配備に `NatEipAllocationId` を設定しないでください。** スタックがすでに作ったアドレスと同じ値でもだめです。

- CloudFormation は、`AllocationId` の出どころが `!GetAtt NatEip.AllocationId` からパラメータに変わったと見て、**NAT ゲートウェイ**の置き換え（`ReplaceAndDelete`）を計画します
- 同じ割り当てが古い NAT ゲートウェイに付いたままなので、外への通信が止まり、作成も成功しません。稼働中の配備で、変更セットを実行せずに確かめた結果です
- このパラメータは、**新しく建てる**ときのためのものです。動いているスタックでは、保持だけでアドレスは守られます

## NAT ゲートウェイは 1 つの AZ にしかない

`00-network` は、費用を抑えるため、NAT ゲートウェイを最初の AZ に 1 つだけ置きます。

- その AZ が落ちると、2 つめの AZ のワークスペースも含め、**すべて**のワークスペースが外へ出られなくなります
- AZ ごとに NAT を置けば直りますが、配備は**2 つ**のアドレスから出るようになります
- 許可リストに登録してもらう前に決めておきます。付いていない EIP は月 $3.6 ほどなので、2 つめを先に割り当てて両方を登録しておくのが、安く選択肢を残す方法です

## 関連

- [外向きの通信をゲートウェイで制御する](egress-gateway.ja.md)
- [スクリプトで構築・一時停止・撤去する](lifecycle.ja.md)：`capture-env.sh` と `standup.sh`
- [撤去する](teardown.ja.md)：撤去しても残るもの
