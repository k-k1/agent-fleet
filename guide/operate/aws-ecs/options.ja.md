---
audience: "AWS の ECS（`ecs` / `ecs-ec2`）の配備で、既定ではオフの機能を使いたい配備管理者"
source_of_truth: "パラメータは `deploy/aws/ecs/cfn/30-ingress.yaml` と `10-data.yaml`、詳細は `deploy/aws/ecs/cfn/PARAMETERS.md`"
---

# パラメータで任意の機能をオンにする

[English](options.md) | 日本語

ECS の配備で、既定ではオフの機能をスタックのパラメータでオンにします。プレビュー用のサブドメイン、KMS の保存時の鍵、リモートの MCP、提供ルート、タイマーと外部のエンジン、WAF です。
どのパラメータも、既定のままなら何も足さず、今のスタックは変わりません。

## 全体像

| あなたがやること | 起きること |
|---|---|
| `30-ingress` を、そのパラメータだけを指定してデプロイする | 指定したパラメータだけが変わり、ほかは前の値のまま。Control Plane のサービスが入れ替わる |
| `update.sh` でリリースを上げる | ここのパラメータには触らない。オンにしたものはオンのまま、オフのものはオフのまま |

| パラメータ | スタック | オンにするもの |
|---|---|---|
| `PreviewDomain` | `30-ingress` | ワークスペースのサービスを、起動ごとのサブドメインのルートで見せる |
| `CustodianKmsKey` / `CustodianKmsKeyArn` | `10-data` / `30-ingress` | Control Plane が保存する秘密を KMS の鍵で守る |
| `WorkspaceDekRandom` | `30-ingress` | ホームごとに別の鍵を持たせる（KMS のときだけ） |
| `McpEnabled` / `McpOAuth` | `30-ingress` | `/mcp` を公開し、外のクライアントを OAuth でサインインさせる |
| `ProvidedRoutes` | `30-ingress` | 配備が費用を持つ Bedrock を、予算の中でメンバーに使わせる（受け入れの確認用） |
| タイマー・GitHub サインイン・外部のエンジン | `30-ingress` | Control Plane の時間の設定、GitHub でのサインインの追加の設定、LAN や別の配備のエンジン |
| `WafRateLimitPer5Min` / `WafIpReputation` | `30-ingress` | ALB の前に AWS WAF を置く |

## パラメータを変える

`update.sh` が上書きするのは、`ImageTag` と、ほかのスタックから読む値（`EfsBackup`・`WorkspaceSubnets`・`EgressProxyAddr`・`EgressTokenSecretArn`・`EgressArchiveBucket`）だけです。

- ほかのパラメータは前の値のままです
- スタックにまだないパラメータは、テンプレートの既定の値になります。新しいパラメータを足したリリースに上げても、何もオンになりません

そのため、ここの機能は `30-ingress` を個別にデプロイしてオンにします。

```bash
aws cloudformation deploy --stack-name <ingress stack> --template-file cfn/30-ingress.yaml --s3-bucket "$CFN_BUCKET" --s3-prefix cfn \
  --capabilities CAPABILITY_NAMED_IAM --parameter-overrides McpEnabled=on McpOAuth=on
```

- `CFN_BUCKET` は `20-platform` の出力 `CfnTemplatesBucket` です（[ECS に構築する](install.ja.md#3-30-ingress-をデプロイする)）
- `--parameter-overrides` には、変えたいパラメータだけを書きます
- 値を空に戻すときは、`--parameter-overrides Key=` では空になりません。`clear-params.sh` を使います（[パラメータを空に戻す](upgrade.ja.md#パラメータを空に戻す)）

## プレビュー用のサブドメインを使う

メンバーがワークスペースで動かすサービスを、起動ごとの URL で見せます。

```bash
--parameter-overrides PreviewDomain=pv.example.com ...
```

- 起動のたびにランダムなラベルができ、許可したポート（既定は `3000, 8080`）が `https://{slug}-{port}.pv.example.com/` で届きます
- **ルートで届く**ので、React・Next.js・Spring Boot の組み合わせのように、サブパスの `/preview/{port}` では動かないアプリも見せられます
- 空（既定）なら何も作らず、プレビューはサブパスだけです

| パラメータが足すもの | 中身 |
|---|---|
| ACM の証明書 | `*.{PreviewDomain}` |
| `ListenerCertificate` | その証明書を、今の 443 のリスナーに SNI で付ける |
| Route 53 | ワイルドカードのエイリアス 1 つ |
| Control Plane の環境 | プレビュー用のドメイン |

今の証明書には触りません。SAN を足すと証明書が作り直されるので、プレビュー用に別の証明書を作ります。
ワークスペースごとに DNS に書くものはありません。1 つのワイルドカードと 1 つの証明書が、すべてのワークスペースとポートに使われるので、URL を発行するたびに API を呼んだり、DNS の反映を待ったりすることもありません。

### ドメインとゾーンを決める

| 置き場所 | 例 | `PreviewHostedZoneId` |
|---|---|---|
| Console のゾーンの中 | `pv.af.example.com` | 空のまま。DNS の作業はない |
| Console の兄弟で、Console と同じゾーン | `pv.example.com`（ゾーンが `example.com`） | 空のまま |
| Console の兄弟で、Console の名前だけが委任されたゾーン | `pv.example.com`（ゾーンが `af.example.com`） | 兄弟のための別のホストゾーンを作り、親から `NS` で委任して、そのゾーンの ID を渡す |

```bash
# sibling, delegated zone
--parameter-overrides PreviewDomain=pv.example.com PreviewHostedZoneId=<pv zone id> ...
# child of the Console's zone — nothing to delegate
--parameter-overrides PreviewDomain=pv.af.example.com ...
```

- 使うゾーンは、`PreviewDomain` を**含む**必要があります。含まないと ACM の DNS 検証が終わらず、スタックは失敗せずに止まったままになります
- Console の FQDN の子（`*.af.example.com`）ではなく、**兄弟**（`af.example.com` の隣の `pv.example.com`）にしてください。プレビューのアプリはメンバー自身のコードで、子に置くと、そのアプリが `.af.example.com` の cookie を書けてしまいます
- 兄弟でも、登録可能ドメインが同じなら、`.example.com` の cookie は書けます。完全に分けるには、別の登録ドメインを使います（[そのほかの運用上の制御](../secure/controls.ja.md#プレビュー用ドメインを別のドメインに置く)）

どのポートを見せるか、URL を起動をまたいで同じにするか、サインインなしで見せるか（既定はオフで、起動のたびにオフに戻ります）は、メンバーが決めます。

## 保存時の鍵を KMS にする

Control Plane が封じて保存する秘密を、`AF_MASTER_KEY` から導いた鍵ではなく KMS の鍵で守ります。何が変わり、何に気をつけるかは [保存時の鍵を AWS KMS にする](../keys/kms.ja.md) にあります。ここではパラメータだけを書きます。

```bash
# 10-data: make the key (it follows Persistence, like RDS)
--parameter-overrides CustodianKmsKey=create ...
# 30-ingress: hand the CP the key from 10-data's CustodianKmsKeyArn output
--parameter-overrides CustodianKmsKeyArn=arn:aws:kms:<region>:<account>:key/<key-id> ...
```

1. `10-data` を `CustodianKmsKey=create` でデプロイします。鍵は RDS と同じく `Persistence` に従います
2. `10-data` の出力 `CustodianKmsKeyArn` を、`30-ingress` の `CustodianKmsKeyArn` に渡します

### ホームごとに鍵を分ける

```bash
# 30-ingress, together with CustodianKmsKeyArn above
--parameter-overrides CustodianKmsKeyArn=arn:aws:kms:<region>:<account>:key/<key-id> WorkspaceDekRandom=on ...
```

- `CustodianKmsKeyArn` なしで `WorkspaceDekRandom=on` にすると、テンプレートのルールがスタックの操作を失敗させ、ロールバックします。変更セットは作れて、失敗はスタックのイベントに出ます
- オンにする前に確かめることと、オフに戻したときに起きることは、[ホームごとに鍵を分ける](../keys/home-keys.ja.md) にあります
- ワークスペースごとの SSM パラメータ `secret-key-next` は `/af-ws/*` の下にあり、Control Plane のロールとワークスペースの実行ロールがすでに読めます

### Control Plane のデータベースに対して `af-cp` を実行する

`af-cp rewrap-keys`（KMS に切り替える前の値を封じ直す）と `af-cp home-dek-status`（ホームごとの鍵を数える・一覧する・消す）は、Control Plane と同じ環境で動かします。同じ `AF_MASTER_KEY`・`AF_KMS_KEY_ID`・データベースの設定・タスクロールが要ります。

ECS では、Control Plane のタスク定義を使い、コマンドだけを差し替えた単発のタスクとして、Control Plane のサブネットとセキュリティグループで動かします。まず `--dry-run` で実行します。

```bash
aws ecs run-task --cluster <cluster> --launch-type FARGATE \
  --task-definition <the Control Plane task definition> \
  --network-configuration 'awsvpcConfiguration={subnets=[<private subnet>],securityGroups=[<Control Plane security group>]}' \
  --overrides '{"containerOverrides":[{"name":"cp","command":["rewrap-keys","--dry-run"]}]}'
```

出力は Control Plane のロググループに出ます。各コマンドが何をし、いつ実行するかは、[保存時の鍵を AWS KMS にする](../keys/kms.ja.md#切り替える前に保存された値を封じ直す) と [ホームごとに鍵を分ける](../keys/home-keys.ja.md) にあります。

## リモートの MCP を公開する

```bash
# 30-ingress: serve /mcp, then (optionally) let hosted MCP clients sign in with OAuth
aws cloudformation deploy --stack-name <ingress stack> --template-file cfn/30-ingress.yaml --s3-bucket "$CFN_BUCKET" --s3-prefix cfn \
  --capabilities CAPABILITY_NAMED_IAM --parameter-overrides McpEnabled=on McpOAuth=on
```

| パラメータ | 中身 |
|---|---|
| `McpEnabled=on` | `/mcp` を公開する |
| `McpOAuth=on` | ホストされた MCP クライアントを OAuth でサインインさせる。`McpEnabled=on` が要り、ないとテンプレートのルールが操作を失敗させる |
| `McpOAuthTrustedHosts` | 信頼するクライアントのホストの、予備の一覧（カンマ区切り）。空なら Control Plane の既定 |

3 つとも既定はオフ（空）です。何を公開し、どのホストを信頼するかは [MCP と外部接続のサインインを運用する](../secure/mcp.ja.md) にあります。

## 提供ルートを使う

配備が費用を持つ Bedrock を、管理者が決めた予算の中でメンバーに使わせます。受け入れの確認のためのスイッチです。

```bash
# 30-ingress: Bedrock the deployment pays for (acceptance only)
aws cloudformation deploy --stack-name <ingress stack> --template-file cfn/30-ingress.yaml --s3-bucket "$CFN_BUCKET" --s3-prefix cfn \
  --capabilities CAPABILITY_NAMED_IAM --parameter-overrides ProvidedRoutes=on
```

`ProvidedRoutes=on` は 4 つのことをします。

| すること | 中身 |
|---|---|
| Control Plane のスイッチ | 提供ルートをオンにする |
| ルートのロールを作る | Bedrock のモデルを呼ぶことだけができ、メンバーのセッションタグを付けた Control Plane のタスクロールだけが引き受けられる |
| Control Plane に権限を与える | タスクロールが、そのロールを引き受けられる |
| 内部の経路を開く | エージェントからのモデル呼び出しが通る、Service Connect の `af-cp-internal:8098`。タイムアウトは 15 分の期限より長く、60 秒の ALB は経由しない |

オンにしたら、次のことをします。

1. スタックの出力 `DeploymentRouteRoleArn` を、`PUT /api/admin/provided-routes/deployment` で登録します
2. テナントに許可を与え、予算を REST で設定します。テナントに上限を付けて許可し、メンバーに予算を設定するまで、誰も使えません
3. 動いているワークスペースを停止して起動し直します。起動したときの環境のままなので、そのままではルートを使えません

オフなら何も足しません。予算の決めかたと、登録のリクエスト本文は、[`cfn/PARAMETERS.md` の「Provided routes」](https://github.com/k-k1/agent-fleet/blob/main/deploy/aws/ecs/cfn/PARAMETERS.md#provided-routes) にあります。

## タイマー・GitHub サインイン・外部のエンジンを設定する

```bash
aws cloudformation deploy --stack-name <ingress stack> --template-file cfn/30-ingress.yaml --s3-bucket "$CFN_BUCKET" --s3-prefix cfn \
  --capabilities CAPABILITY_NAMED_IAM \
  --parameter-overrides WsIdleTimeout=4h ComfyUrl=http://10.0.0.5:8188 ComfyApiKey=on
```

運用のガイドが設定できるとしている Control Plane の変数には、それぞれパラメータがあります。空（既定）なら環境に何も足しません。`update.sh` も、これらを設定したり消したりしません。

| 種類 | パラメータ |
|---|---|
| タイマー | `SessionIdleTimeout`・`InteractionIdleTimeout`・`WsIdleTimeout`・`PresenceIdleTimeout`・`BackgroundHoldMax`・`WorkspaceStartDeadline`・`UsageSampleInterval`・`AgentSilentStopAfter` |
| GitHub でのサインイン | `GithubLoginClientId`・`GithubLabelJa`・`GithubLabelEn`・`GithubMembershipTtl`・`GithubMembershipGrace` |
| 外部のエンジン | `LlmUrl`・`ComfyUrl`・`RemoteEngineUrl`・`RemoteEngineKeys` |

タイマーの意味と既定の値は [アイドル停止と強制停止を使う](../run/idle-stop.ja.md) と [上限と既定値](../../ref/limits.ja.md) にあります。

### 秘密はスイッチで読む

3 つの秘密は、それぞれのスイッチを `on` にしたときだけ、SSM の SecureString から読みます。**先に SSM パラメータを作ってから**スイッチをオンにします。存在しないパラメータを指していると、タスクが起動しません。

| スイッチ（既定は `off`） | 読む SSM パラメータ |
|---|---|
| `GithubLoginSecret` | `<SsmPrefix>/github-login-client-secret` |
| `ComfyApiKey` | `<SsmPrefix>/comfy-api-key` |
| `RemoteEngineToken` | `<SsmPrefix>/remote-engine-token` |

- `SsmPrefix` の既定は `/af-cp` で、実行ロールはすでにこの下を読めます。`/af-cp/` と `/af-ws/` の外に変えたときは、実行ロールの `ssm-read` ポリシーをそのプレフィックスに広げます
- LAN のエンジンには、VPC からの経路（VPN・ピアリング・Transit Gateway）が要ります。スタックは作りません
- `WS_ENV` はあえて公開していません。タスク定義に平文で秘密が入るのを避けるためです

詳細は [`cfn/PARAMETERS.md` の「Control Plane timers, GitHub sign-in extras and LAN / borrowed engines」](https://github.com/k-k1/agent-fleet/blob/main/deploy/aws/ecs/cfn/PARAMETERS.md#control-plane-timers-github-sign-in-extras-and-lan--borrowed-engines) にあります。

## WAF を置く

`30-ingress` は、ALB の前に AWS WAF の Web ACL を置けます。設定はあえて 2 つだけです。

```bash
--parameter-overrides WafRateLimitPer5Min=3000 WafIpReputation=on ...
```

| パラメータ | 中身 |
|---|---|
| `WafRateLimitPer5Min` | 1 つの IP から 5 分間に何回のリクエストでブロックするか。`0`（既定）なら Web ACL を作らない。Console のポーリングも含めて数えるので、1 つの NAT の裏にいるオフィス全体が出す量よりずっと上にする |
| `WafIpReputation` | AWS が管理する IP の評価リスト。本文ではなく送信元で判定する。`WafRateLimitPer5Min` が 0 より大きいときだけ使える |

- 上限を 100 にして 150 回のリクエストを送ると、約 45 秒で `403` になりました。上限を上げると、次の評価でその IP のブロックが解除されます
- IP の評価リストは、オンにして数分で、`/.env` などを探るスキャナーを止めました

### シグネチャのルールは使わない

Core rule set・SQLi・XSS・LFI のルールは、あえて用意していません。

- この製品では、チャットのメッセージ、ファイルの書き込み、ターミナルの入力など、ふつうのリクエストの本文にソースコードやシェルのコマンドが入ります。`'; DROP TABLE`・`../../etc/passwd`・`<script>` は正しい通信です
- これらのルールは、実際の作業をランダムに 403 で拒否します。誰かが WAF を疑うより先に、製品の不具合に見えてしまいます
- WAF は既定で本文の先頭 8 KB しか見ないので、シグネチャのルールは見た目ほどの効果がありません

閉じた配備では、上流で絞る方が安く確実です。`00-network` の `AlbIngressCidr` を自社の範囲にすれば、通信は ALB に届きません。

WAF の費用は、Web ACL が月に約 5 ドル、ルール 1 つにつき月 1 ドル、100 万リクエストにつき 0.60 ドルです。

## 関連

- [ECS に構築する](install.ja.md)
- [データベースに届かないときのアラームを設定する](alarms.ja.md)
- [アップグレードする](upgrade.ja.md)
- [`cfn/PARAMETERS.md`](https://github.com/k-k1/agent-fleet/blob/main/deploy/aws/ecs/cfn/PARAMETERS.md)
