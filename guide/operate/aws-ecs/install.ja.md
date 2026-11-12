---
audience: "AWS の ECS（`ecs` / `ecs-ec2`）に新しく配備を建てる配備管理者"
source_of_truth: "テンプレートは `deploy/aws/ecs/cfn/*.yaml`、スクリプトは `deploy/aws/ecs/release-ecr.sh` と `standup.sh`、パラメータの詳細は `deploy/aws/ecs/cfn/PARAMETERS.md`"
---

# ECS に構築する

[English](install.md) | 日本語

AWS の ECS に Agent Fleet を建て、Control Plane を起動して、IdP でサインインできるところまで進めます。
スタックの構成と順番は [ECS 構成の全体像](overview.ja.md) にあります。

## 全体像

| あなたがやること | 起きること |
|---|---|
| アカウントごとに 1 回、サービスにリンクされたロールを作る。費用を分けるなら、Billing の請求情報へのアクセスもオンにする | ECS のクラスターを作れるようになる。Control Plane が費用を読めるようになる |
| `00-network`・`10-data`・`20-platform` をデプロイする | ネットワーク、EFS と RDS、ECR・クラスター・IAM ロールができる |
| イメージを ECR に push する | ECS がイメージを取れるようになる |
| 使うなら、`40-ec2-pool`・`50-tts`・`60-engines` をデプロイする | スロットプール・読み上げ・自前の推論エンジンができる |
| Control Plane の秘密を SSM に入れ、IdP にリダイレクト URI を登録する | Control Plane が秘密を読み、IdP がサインインを戻せるようになる |
| `30-ingress` をデプロイする | 証明書・ALB・DNS・Control Plane のサービスができ、Console にサインインできる |
| 費用を分けるなら、構築した日にワークスペースを 1 つ起動する | Control Plane がタグを書き、AWS がキーを見つけたあと（最大 24 時間）、Control Plane がコスト配分タグを有効にする |

コマンドは、リポジトリの `deploy/aws/ecs/` で実行します。`standup.sh` も同じ順番で建てますが、`capture-env.sh` で取っておいた既存の配備のパラメータ（`params/30-ingress` など）から**建て直す**ためのスクリプトです。それがない新しい環境では、すぐに終わります。初めて建てるときは、このページの手順で建てます。

## はじめる前に（アカウントごとに 1 回）

### サービスにリンクされたロールを作る

新しいアカウントには `AWSServiceRoleForECS` がなく、Service Connect の既定の名前空間を持つクラスターを作ると「ECS Service Linked Role is not ready」で失敗します。

自前の推論エンジン（`60-engines`）を動かすなら、`CreateFleet` の前に AWS が求める `AWSServiceRoleForEC2Fleet` も作っておきます。なくても呼び出しが通ったアカウントはありましたが、作っておく方が安全です。

`standup.sh` はこの 2 つを作ります。手で作るときは次のとおりです。何度実行しても同じで、2 回目の「has been taken」のエラーは無視して構いません。

```bash
aws iam create-service-linked-role --aws-service-name ecs.amazonaws.com || true
aws iam create-service-linked-role --aws-service-name ec2fleet.amazonaws.com || true
# and, for a deployment that declares a `spot` offer:
aws iam create-service-linked-role --aws-service-name spot.amazonaws.com || true
```

`spot` のオファーを宣言する配備では、3 行目も実行します。

### 請求情報へのアクセスをオンにする（任意）

費用をメンバーごとに分けるなら、支払いアカウントの Billing のコンソールで「IAM ユーザーとロールによる請求情報へのアクセス」をオンにします。テンプレートでは設定できません。
オフのままだと、ポリシーが付いていても `CpTaskRole` の `ce:GetCostAndUsage` はすべて失敗します。

コスト配分タグの有効化は、構築したあとに行います（[費用をメンバーごとに分ける](#費用をメンバーごとに分ける任意)）。

## 土台のスタックをデプロイする

### スタックに必要な capabilities

| スタック | `--capabilities` | 理由 |
|---|---|---|
| `00-network` | `CAPABILITY_IAM` | 出口のゲートウェイのインスタンスロールを宣言している |
| `10-data` | `CAPABILITY_AUTO_EXPAND CAPABILITY_IAM` | `Transform: AWS::LanguageExtensions` を宣言している。`Persistence=retain` では、EFS のバックアップに使う名前のない IAM ロールも作る |
| `20-platform` | `CAPABILITY_NAMED_IAM` | 名前付きの IAM ロールを作る |
| `40-ec2-pool`・`60-engines`・`30-ingress` | `CAPABILITY_NAMED_IAM` | Control Plane のタスクロールに、名前付きの管理ポリシーを付ける |

足りないと、変更セットは `InsufficientCapabilitiesException … Requires capabilities : [CAPABILITY_IAM]` などで拒否されます。

### 順番にデプロイする

1. `00-network` をデプロイします

   ```bash
   aws cloudformation deploy --stack-name af-ecs-network --template-file cfn/00-network.yaml \
     --capabilities CAPABILITY_IAM \
     --profile af-sandbox --region ap-northeast-1
   ```

2. `10-data` をデプロイします。データを残す配備では `Persistence=retain` にします

   ```bash
   aws cloudformation deploy --stack-name af-ecs-data \
     --template-file cfn/10-data.yaml --capabilities CAPABILITY_AUTO_EXPAND CAPABILITY_IAM \
     --parameter-overrides Persistence=retain \
     --profile af-sandbox --region ap-northeast-1
   ```

3. `20-platform` をデプロイします

   ```bash
   aws cloudformation deploy --stack-name af-ecs-platform \
     --template-file cfn/20-platform.yaml --capabilities CAPABILITY_NAMED_IAM \
     --profile af-sandbox --region ap-northeast-1
   ```

`Persistence` の既定は `delete` で、スタックを消すと EFS と RDS も消えます。

## イメージを ECR に push する

ECR のリポジトリは `20-platform` が持っています。先に `20-platform` をデプロイしてください。リポジトリを別に作ると、あとで `20-platform` のデプロイが AlreadyExists で失敗します。

`release-ecr.sh` はリポジトリがあることを確かめるだけで、作りません。

```bash
# images already local (built by deploy/release/build.sh):
VERSION=<v> ./release-ecr.sh --profile af-sandbox --region <region>
# or from the air-gap images tar (B) when the build host is a different machine:
VERSION=<v> ./release-ecr.sh --profile af-sandbox --region <region> \
    --images-tar agent-fleet-images-<v>.tar.gz
```

- 1 行目は、`deploy/release/build.sh` でビルドしたイメージがこのマシンにあるとき、2 行目は別のマシンで作ったイメージの tar から push するときです
- `release-ecr.sh` が `af-engine-tools` を ECR に入れるのは、`60-engines` のスタックが**すでにある**ときだけです（アップグレード）。初めて `60-engines` を建てるときは、次の [任意のスタックをデプロイする](#任意のスタックをデプロイする) で自分で入れます
- `af-engine-tools` は GHCR にしかなく、ローカルでビルドも配布もしないので、タグを付けて push するのではなく、レジストリ間で `crane copy` します。ECR にも GHCR にもそのタグがないと、スクリプトは止まります。先に `engine-tools-image.yml` をそのタグで実行してください

### 手で push する

スクリプトは次のコマンドを並べたものです。

```bash
aws ecr get-login-password --region <region> | docker login --username AWS \
    --password-stdin <acct>.dkr.ecr.<region>.amazonaws.com
# tag + push the images already built for local (no heavy rebuild here):
docker tag agent-fleet/control-plane:<tag> <acct>.dkr.ecr.<region>.amazonaws.com/af-control-plane:<tag>
docker tag agent-fleet/workspace:<tag>     <acct>.dkr.ecr.<region>.amazonaws.com/af-workspace:<tag>
docker push <acct>.dkr.ecr.<region>.amazonaws.com/af-control-plane:<tag>
docker push <acct>.dkr.ecr.<region>.amazonaws.com/af-workspace:<tag>
```

## 任意のスタックをデプロイする

イメージを push したあと、`30-ingress` の前に、使うものだけを `40-ec2-pool`（`ecs-ec2`）・`50-tts`（読み上げ）・`60-engines`（自前の推論）の順にデプロイします。

- `50-tts` と `60-engines` のサービスは、エンジンのイメージ（`af-voicevox`、`af-engine-tools`・`af-llamacpp`・`af-comfyui`）が ECR にないと起動できず、スタックが `CREATE_IN_PROGRESS` のまま進みません。スタックより**先に** ECR に入れます
- 初めての `60-engines` では、`af-engine-tools` を `60-engines` の `EngineToolsImageTag` のタグで GHCR から写します

  ```bash
  crane copy ghcr.io/k-k1/agent-fleet/engine-tools:<EngineToolsImageTag> <acct>.dkr.ecr.<region>.amazonaws.com/af-engine-tools:<EngineToolsImageTag>
  ```

- `standup.sh` はこの順番でイメージを写してからデプロイしますが、取っておいたパラメータがあるときの建て直し専用です。初めて建てるときは手で写します。読み上げの `af-voicevox` は [日本語の読み上げを足す](tts.ja.md#有効にする) の「有効にする」、推論エンジンの `af-llamacpp`・`af-comfyui` は [推論エンジンを足す](engines.ja.md#有効にする) の「有効にする」の手順 3 にあります
- 各スタックのパラメータとイメージは、[ECS の手順書](../../../deploy/aws/ecs/README.md) の索引から、EC2 スロットプール・読み上げ・推論エンジンのページにあります

## Control Plane を起動する（`30-ingress`）

サインインは Control Plane 自身が行います（`AuthMode=oauth`）。Google、または任意の OIDC の IdP（Entra ID・Okta・Keycloak・Auth0・Cognito・GitLab）を使えます。ALB は TLS を終端するだけです。
Control Plane の状態は RDS の Postgres に、ワークスペースのホームは EFS に置きます。どちらも `10-data` のエクスポートから配線されるので、`10-data` が先に要ります。

### 1. Control Plane の秘密を SSM に入れる

秘密は SSM の SecureString に置き、実行ロールが読みます。平文の環境変数には入れません。

```bash
RG=ap-northeast-1
aws ssm put-parameter --profile af-sandbox --region $RG --type SecureString \
  --name /af-cp/cookie-secret       --value "$(openssl rand -hex 32)"
aws ssm put-parameter --profile af-sandbox --region $RG --type SecureString \
  --name /af-cp/master-key          --value "$(openssl rand -hex 32)"
# one of these two, or both — only the IdPs you enable are wired in
aws ssm put-parameter --profile af-sandbox --region $RG --type SecureString \
  --name /af-cp/google-client-secret --value "<GOOGLE_OAUTH_CLIENT_SECRET>"   # your terminal, not shared
aws ssm put-parameter --profile af-sandbox --region $RG --type SecureString \
  --name /af-cp/oidc-client-secret   --value "<AF_OIDC_<ID>_CLIENT_SECRET>"
```

- IdP のシークレットは、使う IdP の分だけ入れます。値は自分の端末で入力し、共有の画面には出さないでください
- `master-key` を `aws/ssm` ではなく自前の KMS 鍵で暗号化するときは、その `put-parameter` に `--key-id <鍵の ARN>` を足し、**先に** `20-platform` の `MasterKeyKmsKeyArn` を同じ ARN にします。実行ロールの `kms:Decrypt` が、Control Plane のタスクが起動する前に要るからです。詳しくは [保存時の鍵を AWS KMS にする](../keys/kms.ja.md#afmasterkey-のパラメータを自前の鍵で暗号化する) にあります

GitHub と Bitbucket の OAuth アプリは、配備の設定ではありません。CloudFormation のパラメータも SSM の項目もなく、テナント管理者が Console で登録します（[連携に使う OAuth アプリを選ぶ](../../admin/access/oauth-apps.ja.md)）。

### 2. IdP にリダイレクト URI を登録する

IdP のクライアントの許可するリダイレクト URI に、`https://<your-fqdn>/oauth2/callback` を足します。有効にするすべての IdP で、この 1 つの URI を使います。
IdP ごとのクライアントの作りかたは [サインインの方法を設定する](../signin/overview.ja.md) にあります。

### 3. `30-ingress` をデプロイする

`30-ingress.yaml` は 51,200 バイトの上限のすぐ下にあり、超えてもよいことになっています。将来のリリースで超えても止まらないよう、いつも `20-platform` が作った S3 Bucket を経由して渡します（[テンプレートの大きさの上限に気をつける](template-size.ja.md)）。

Google でサインインする配備では次のとおりです。

```bash
CFN_BUCKET=$(aws cloudformation describe-stacks --stack-name <platform stack> --query "Stacks[0].Outputs[?OutputKey=='CfnTemplatesBucket'].OutputValue" --output text --profile <p> --region <r>)
aws cloudformation deploy --stack-name af-ecs-ingress \
  --template-file cfn/30-ingress.yaml --s3-bucket "$CFN_BUCKET" --s3-prefix cfn --capabilities CAPABILITY_NAMED_IAM \
  --parameter-overrides GoogleClientId=<id> \
    Fqdn=af.example.com HostedZoneId=<your-zone-id> \
    AllowedEmails=you@example.com SuperAdminEmails=you@example.com \
  --profile af-sandbox --region $RG
```

Microsoft Entra ID（や Okta・Keycloak など）の配備では、`GoogleClientId` の代わりに `Oidc*` のパラメータを渡します。`GoogleClientId` が空なら、Google は選択肢に出ないだけです。

```bash
  --parameter-overrides OidcProviderId=ENTRA \
    OidcIssuer=https://login.microsoftonline.com/<tenant-guid>/v2.0 \
    OidcClientId=<application-client-id> OidcTrust=issuer \
    OidcLabelJa="Microsoft でサインイン" OidcLabelEn="Sign in with Microsoft" \
    Fqdn=af.example.com HostedZoneId=<your-zone-id> \
    AllowedDomains=example.co.jp SuperAdminEmails=you@example.co.jp
```

| パラメータ | 中身 |
|---|---|
| `Fqdn` / `HostedZoneId` | Console の名前と、それを含む Route 53 のホストゾーン。ゾーンはこのアカウントにある必要があります |
| `AllowedEmails` / `AllowedDomains` | サインインを許すメールアドレスとドメイン |
| `SuperAdminEmails` | 配備管理者（super_admin）になる人 |
| `OidcIssuer` | Entra ID ではテナントの GUID で固定します。`/common/` や `/organizations/` では世界中の Microsoft アカウントがサインインの画面に届き、個人のアカウントは自分のメールアドレスを書き換えられます。そのため Control Plane は、`OidcAllowedTids` がないかぎり起動しません |
| `ImageTag` | リリースのイメージでは `ImageTag=<v>` を足します。既定の `dev` は、開発用に push した `:dev` に合わせたものです |

ACM の DNS 検証と Route 53 のエイリアスは自動です。最初のデプロイは、証明書の発行で数分長くかかります。

### 4. 動いたことを確かめる

1. `https://<your-fqdn>/readyz` が 200 を返すことを確かめます。`/healthz` はデータベースが死んでいても 200 を返すので、確かめるのは `/readyz` です
2. Console を開き、`SuperAdminEmails` の人でサインインします
3. 費用をメンバーごとに分けるなら、ここでワークスペースを 1 つ起動します（[費用をメンバーごとに分ける](#費用をメンバーごとに分ける任意)）

## 費用をメンバーごとに分ける（任意）

任意ですが、あとから取り戻せないので、使うなら構築した日に始めます。

**スタックをデプロイしただけでは何も起きません。** テンプレートが付けるのは `CpTaskRole` の `ce:GetCostAndUsage` の権限だけです。タグを書くのは動いている Control Plane で、コスト配分タグを有効にする CloudFormation のリソースはありません。順番は次のとおりです。

1. 構築する前に、請求情報へのアクセスをオンにしておきます（[請求情報へのアクセスをオンにする](#請求情報へのアクセスをオンにする任意)）
2. スタックをデプロイし、Control Plane を起動します（このページの上の手順）
3. **ワークスペースを 1 つ起動します。** Control Plane がリソースにタグを書きます。誰も使っていなくても、構築した日に起動してください
4. AWS がタグのキーを見つけるのを待ちます（最大 24 時間）
5. コスト配分タグを有効にします。**ふだんは Control Plane が自動で行います**（下を参照）

| こと | 中身 |
|---|---|
| さかのぼらない | 有効にする前の日の費用は、誰の分かを後から分けられません。待つ間の費用も同じです |
| キーごとにアカウントで 1 回 | 有効にするのはタグのキーだけで、値の区別はありません。テナントやメンバーが増えても、何もいりません |
| 支払いアカウントだけ | AWS Organizations では、管理（支払い）アカウントだけが有効にできます |
| 見たことのないキーは有効にできない | AWS がまだ実際のリソースで見ていないキーは `ValidationException: Tag keys not found` で拒否されます |
| `af-workspace` は有効にしない | 値がメンバーのメールアドレスから作られるので、有効にすると請求データ（CUR・Cost Explorer・請求書の CSV）にそれが写ります。Control Plane が結び付けに使うのは、ランダムな ID の `af-membership` です |

Control Plane は、AWS が各キーを見つけるまで、ポーリングのたびにタグの有効化をやり直します。`af-workspace` は飛ばし、人がオフにしたタグには触りません。

手で有効にするのは、`CpTaskRole` から `CostAllocationTagActivation` の文を外したときか、Console が有効にできなかったと伝えてきたときだけです。

```bash
aws ce update-cost-allocation-tags-status --region us-east-1 \
  --cost-allocation-tags-status '[{"TagKey":"af-membership","Status":"Active"},
    {"TagKey":"af-tenant","Status":"Active"},{"TagKey":"af-role","Status":"Active"},
    {"TagKey":"af-pool","Status":"Active"},{"TagKey":"af-slot-size","Status":"Active"}]'
```

AWS が何を見つけたかは `list-cost-allocation-tags` でわかります。`Inactive` で載っているキーはすぐ `Active` にでき、載っていないキーはまだ何もタグを書いていません。待つのは、そのキーが初めて現れるときだけです。

## 関連

- [ECS 構成の全体像](overview.ja.md)
- [データベースに届かないときのアラームを設定する](alarms.ja.md)：構築したら、続けて設定してください
- [パラメータで任意の機能をオンにする](options.ja.md)
- [デプロイに必要な IAM 権限](iam.ja.md)
- [ECS で困ったとき](troubleshooting.ja.md)
