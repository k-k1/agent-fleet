---
audience: "AWS の EC2 1 台に、Docker Compose の配備を建てる人"
source_of_truth: "`deploy/aws/ec2-single/cfn.yaml` と `deploy/compose/release.sh`（食い違ったらこのページのバグ）"
---

# EC2 1 台に構築する

[English](aws-ec2-single.md) | 日本語

CloudFormation で EC2 のインスタンスを 1 台建て、その上で Docker Compose の配備を動かします。
要るのは AWS CLI だけで、Terraform は使いません。片付けもコマンド 1 つです。

## 全体像

| あなたがやること | 起きること |
|---|---|
| SSH の鍵を手元で作り、スタックをデプロイする | Ubuntu 24.04 のインスタンス（cloud-init で Docker 入り）、Elastic IP、Route 53 の A レコード、取り込んだキーペアができる |
| 手元でリリースのバンドルとイメージを作り、インスタンスに送る | インスタンスで git をクローンせずに、バンドルだけで配備を建てられる |
| インスタンスで `.env` を書き、`docker compose up -d` する | Caddy が Let's Encrypt の証明書を取り、Console が `https://<Fqdn>` で開く |
| いらなくなったらスタックを消す | スタックの中のものがすべて消える |

ECS の配備ではなく、1 台の VM の上の Docker Compose です。ECS は [デプロイ先を選ぶ](choose.ja.md) で比べます。

## 始める前に

| 用意するもの | 補足 |
|---|---|
| AWS CLI と、デプロイする IAM の権限 | EC2・Route 53・CloudFormation。SSM の書き込みも IAM の capability も要りません |
| Route 53 のパブリックホストゾーン | ドメインがそのゾーンに委任されていないときは、下の [DNS の注意](#dns-の注意) |
| 手元の Docker | リリースのバンドルとイメージを作ります |
| サインイン用の IdP のクライアント | [サインインの方法を設定する](signin/overview.ja.md) |

## 1. スタックを建てる

SSH の鍵は手元で作ります。秘密鍵は手元から出ません。取り込むのは公開鍵だけなので、`ssm:PutParameter` の権限は要りません。

```bash
ssh-keygen -t ed25519 -N '' -f agent-fleet-test-key      # -> agent-fleet-test-key(.pub)

aws cloudformation deploy \
  --stack-name agent-fleet-test \
  --template-file cfn.yaml \
  --parameter-overrides \
      HostedZoneName=example.com. \
      Fqdn=af.example.com \
      SSHLocation=<your-ip>/32 \
      PublicKey="$(cat agent-fleet-test-key.pub)"

# read the outputs (public IP, FQDN, ssh command)
aws cloudformation describe-stacks --stack-name agent-fleet-test \
  --query 'Stacks[0].Outputs' --output table
```

コマンドは `deploy/aws/ec2-single/` で実行します。

| パラメータ | 既定 | 意味 |
|---|---|---|
| `HostedZoneName` | （必須） | 既存の Route 53 のパブリックホストゾーン。末尾のドットを付けます（`example.com.`） |
| `Fqdn` | （必須） | Console のホスト名。TLS と OAuth のリダイレクトに使います |
| `SSHLocation` | `0.0.0.0/0` | SSH を許す CIDR。自分の `<ip>/32` にして絞ります |
| `PublicKey` | （必須） | 取り込む SSH の公開鍵（`*.pub` の中身） |
| `InstanceType` | `t3.large` | `t3.medium`・`t3.large`・`t3.xlarge`。`t3.medium`（4 GB）なら下の [メモリの小さいインスタンス](#メモリの小さいインスタンス) |
| `VolumeSizeGb` | `30` | ルートボリュームの大きさ |
| `LatestUbuntuAmi` | Canonical の SSM パラメータ | 最新の Ubuntu 24.04 の AMI。そのままにします。どのリージョンでも使えます |

出力には `PublicIp`・`Fqdn`・`ConsoleUrl`・`OAuthRedirectUri`・`SshCommand` が出ます。セキュリティグループは 22 番を `SSHLocation` に、80 番と 443 番を全体に開きます。

### DNS の注意

Route 53 の A レコードが効くのは、ドメインがそのホストゾーンに委任されているときだけです（レジストラの NS がゾーンの `awsdns-*` のサーバー）。
ドメインがほかの DNS にあるときは、どちらかにします。

- その DNS に A レコードを足します
- DNS を使わず、Elastic IP の **sslip.io** を使います。`Fqdn`・`PUBLIC_DOMAIN` を `<ダッシュ区切りの IP>.sslip.io`（例: `203-0-113-7.sslip.io`）にすると、設定なしでその IP に解決され、本物の Let's Encrypt の証明書も取れます

## 2. バンドルをインスタンスに送る

手元でイメージとバンドルを作り、インスタンスにコピーします。インスタンスでは git をクローンしません。

```bash
cd ../../..                                  # repo root
VERSION=0.1.0 deploy/compose/release.sh --save
scp -i deploy/aws/ec2-single/agent-fleet-test-key \
    deploy/compose/dist/agent-fleet-images-0.1.0.tar.gz \
    deploy/compose/dist/agent-fleet-0.1.0.tar.gz \
    ubuntu@<public-ip>:~
```

`0.1.0` は例です。作る版に置き換えます。`--save` を付けると、バンドル（`agent-fleet-<版>.tar.gz`）に加えて、イメージを `docker save` した tar（`agent-fleet-images-<版>.tar.gz`）もできます。

## 3. インスタンスで起動する

```bash
ssh -i deploy/aws/ec2-single/agent-fleet-test-key ubuntu@<public-ip>
# on the VM:
tar xzf agent-fleet-0.1.0.tar.gz && cd agent-fleet-0.1.0
./load-images.sh ~/agent-fleet-images-0.1.0.tar.gz    # or: gunzip|docker load
cp .env.example .env
#   set DATA_DIR=/srv/agent-fleet/data, DOCKER_GID=$(getent group docker|cut -d: -f3),
#   PUBLIC_DOMAIN / PUBLIC_BASE_URL=https://<Fqdn>, the login IdP (GOOGLE_OAUTH_*,
#   AF_OIDC_PROVIDERS + AF_OIDC_<ID>_*, and/or AF_GITHUB_ALLOWED_ORGS +
#   GITHUB_OAUTH_CLIENT_ID/_SECRET), AF_COOKIE_SECRET,
#   AF_MASTER_KEY (generate!), AF_OAUTH_ALLOWED_DOMAINS, SUPER_ADMIN_EMAILS
sudo mkdir -p /srv/agent-fleet/data && sudo chown 1000:1000 /srv/agent-fleet/data
docker compose up -d
docker compose logs -f caddy cp
```

- `.env` の各値の意味と秘密の作り方は [Docker Compose で構築する](compose/install.ja.md#1-設定を書いてソースから起動する) と同じです
- 80 番と 443 番が開いているので、Caddy が `<Fqdn>` の本物の Let's Encrypt の証明書を取ります
- IdP のクライアントに `https://<Fqdn>/oauth2/callback` を登録します。有効にするどの IdP でも、この 1 つで足ります
- `https://<Fqdn>` を開いてサインインします

最初の管理者について:

- 最初の管理者を通すのは `AF_OAUTH_ALLOWED_DOMAINS` です。そのあとは、管理パネルでテナントに招待した人は、そこに入っていなくてもサインインできます
- テナント専用のサインインページは `https://<Fqdn>/login/<slug>` です
- `SUPER_ADMIN_EMAILS` は起動のときに 1 回だけ読まれ、これだけが正です。アドレスを外して再起動すると、データベースのロールも外れます

### メモリの小さいインスタンス

`t3.medium`（4 GB）では、ワークスペースを起動する前に `.env` の `WS_MEMORY` を下げます（例: `2g`）。下げないと、Claude のインストールの途中でインスタンスがメモリ不足になります。

## インスタンスのメタデータを守る

インスタンスは、ホップ数 1 の IMDSv2 を必須にしています（`cfn.yaml` の `MetadataOptions`）。

- テンプレートはインスタンスプロファイルを付けません。あとから付けたときに、ホストのネットワークの Control Plane（1 ホップ）には届き、docker のブリッジにいるワークスペース（2 ホップ）には届かないようにするための設定です
- ワークスペースは `AWS_EC2_METADATA_DISABLED=true` 付きで起動します。Control Plane に `AF_WS_WORKLOAD_AWS=1` があるときは付けませんが、ホップ数の制限は外れないので、どちらでもインスタンスプロファイルには届きません
- このテンプレートは、配備が費用を払う Bedrock の経路（provided routes）を提供しません。Control Plane が経路のロールを引き受けるための AWS の身元がないからです。ECS のスタックの `ProvidedRoutes=on` を使います

この設定より前に作ったスタックには、スタックを更新するのではなく、動いているインスタンスに直接適用します。`LatestUbuntuAmi` は更新のたびに解決し直され、新しい AMI ならインスタンスが作り直されるからです。

```bash
aws ec2 modify-instance-metadata-options --instance-id <id> \
  --http-tokens required --http-put-response-hop-limit 1 --http-endpoint enabled
```

動いていたワークスペースは、Console で停止して起動するまで、古いコンテナと環境のままです（`docker compose up -d` では作り直されません）。停止して起動したあと、ワークスペースのシェルで確かめます。

| 確かめること | `AF_WS_WORKLOAD_AWS` がオフ（既定） | `AF_WS_WORKLOAD_AWS=1` |
|---|---|---|
| `echo ${AWS_EC2_METADATA_DISABLED:-unset}` | `true` と出る | 意図して `unset` |
| プロファイルなしの `aws sts get-caller-identity` | 「Unable to locate credentials」で失敗する | — |
| 下のトークンの要求 | `200` が出ない | `200` が出ない |

```bash
curl -s -o /dev/null -m 3 -w '%{http_code}' -X PUT -H 'X-aws-ec2-metadata-token-ttl-seconds: 60' http://169.254.169.254/latest/api/token
```

ほかの形態の扱いは [そのほかの運用上の制御](secure/controls.ja.md#ワークスペースにホストのクラウドの身元を渡さない) にあります。

## 4. 片付ける

スタックの中のものは、すべて消えます。

```bash
aws cloudformation delete-stack --stack-name agent-fleet-test
aws cloudformation wait stack-delete-complete --stack-name agent-fleet-test
```

Elastic IP・セキュリティグループ・インスタンス・Route 53 のレコード・キーペアが、スタックと一緒に消えます。
試験に使った **サインインの IdP のクライアント**（Google Cloud Console の OAuth クライアントや、Entra・Okta などのアプリの登録）も消します。そのシークレットは試験のあいだ使われていました。

### キーペアが消えずに `DELETE_FAILED` になるとき

`AWS::EC2::KeyPair` のハンドラは、取り込んだ鍵でも SSM の Parameter Store に触れます。デプロイしたユーザーに `ssm:*` がない（`AmazonSSMReadOnlyAccess` だけなど）と、キーペアが `InternalFailure` で消えず、スタックが `DELETE_FAILED` になります。
そのリソースだけ残してスタックを消し、キーペアを直接消します。

```bash
aws cloudformation delete-stack --stack-name agent-fleet-test --retain-resources KeyPair
aws cloudformation wait stack-delete-complete --stack-name agent-fleet-test
aws ec2 delete-key-pair --key-name agent-fleet-test-key
```

デプロイするユーザーに `/ec2/keypair/*` の `ssm:PutParameter`・`ssm:DeleteParameter` を与えておけば、この問題は起きません。

## 関連

- [Docker Compose で構築する](compose/install.ja.md)
- [インストールする](install.ja.md)
- [バックアップして復元する](run/backup-restore.ja.md)・[アップグレードする](run/upgrade.ja.md)
