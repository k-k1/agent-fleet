---
audience: "AWS の ECS（`ecs` / `ecs-ec2`）の配備で、構築・アップグレード・起動がうまくいかない配備管理者"
source_of_truth: "`deploy/aws/ecs/` のスクリプトとテンプレート、`control-plane/internal/runtime/runtime_ecs.go`"
---

# ECS で困ったとき

[English](troubleshooting.md) | 日本語

ECS の配備で、構築・アップグレード・ワークスペースの起動がうまくいかないときに、症状から原因と対処を探します。
どの形態にも共通する切り分けは [症状から原因を探す](../diagnose/checklist.ja.md) にあります。

## 全体像

| あなたがやること | わかること |
|---|---|
| エラーの文言や症状で、下の表を探す | 原因と、どのページの手順で直すか |
| `/readyz` を見る | Control Plane がデータベースに届くか。`/healthz` はデータベースに触らない |
| Control Plane のロググループ `/af/<ingress-stack>/cp` を読む | 起動が遅れた理由、シークレットの読み直しが失敗した理由など |

## 構築するとき

| 症状 | 原因と対処 |
|---|---|
| クラスターの作成が「ECS Service Linked Role is not ready」で失敗する | 新しいアカウントに `AWSServiceRoleForECS` がありません。サービスにリンクされたロールを作ります（[ECS に構築する](install.ja.md#サービスにリンクされたロールを作る)） |
| 変更セットが `InsufficientCapabilitiesException … Requires capabilities` で拒否される | `--capabilities` が足りません（[スタックに必要な capabilities](install.ja.md#スタックに必要な-capabilities)） |
| `aws cloudformation deploy` がテンプレートのサイズのエラーで終わり、スタックのイベントに何もない | `30-ingress` を S3 経由で渡していません（[テンプレートの大きさの上限に気をつける](template-size.ja.md)） |
| `30-ingress` が `ServiceLimitExceeded` でロールバックする | Control Plane のタスクロールのインラインポリシーが上限を超えました。追加した権限は管理ポリシーにします（[デプロイに必要な IAM 権限](iam.ja.md#control-plane-のタスクロールのポリシーの上限)） |
| `20-platform` をデプロイすると AlreadyExists で失敗する | ECR のリポジトリをスタックの外で作りました。リポジトリは `20-platform` が作ります |
| `30-ingress` をデプロイしても進まず、失敗もしない | プレビュー用のゾーンが `PreviewDomain` を含んでおらず、ACM の DNS 検証が終わりません（[プレビュー用のサブドメインを使う](options.ja.md#ドメインとゾーンを決める)） |
| Entra ID で Control Plane が起動しない | `OidcIssuer` が `/common/` や `/organizations/` です。テナントの GUID で固定します |
| `AuthMode=dev` で構築したスタックが更新できない | `AuthMode=dev` はもうありません。https の URL の裏では、Control Plane が開発用のサインインを拒否します。`AuthMode=oauth` に切り替えます（[開発用サインインを使う](../secure/dev-signin.ja.md)） |
| コスト配分タグの有効化が `ValidationException: Tag keys not found` で拒否される | AWS がまだそのタグキーを検出していません。ワークスペースを 1 つ起動し、最大 24 時間待ちます（[費用をメンバーごとに分ける](install.ja.md#費用をメンバーごとに分ける任意)） |
| ポリシーがあるのに、`CpTaskRole` の `ce:GetCostAndUsage` がすべて失敗する | Billing の「IAM ユーザーとロールによる請求情報へのアクセス」がオフです |

## パラメータを変えるとき

| 症状 | 原因と対処 |
|---|---|
| `--parameter-overrides Key=` で空にしたのに「No changes」になる | この方法では空になりません。`clear-params.sh` を使います（[パラメータを空に戻す](upgrade.ja.md#パラメータを空に戻す)） |
| `ComfyApiKey=on` などのスイッチを入れたら、Control Plane のタスクが起動しない | スイッチが読む SSM パラメータがありません。先に作ります（[秘密はスイッチで読む](options.ja.md#秘密はスイッチで読む)） |
| `WorkspaceDekRandom=on` でデプロイするとロールバックする | `CustodianKmsKeyArn` がありません。両方を一緒に渡します（[ホームごとに鍵を分ける](options.ja.md#ホームごとに鍵を分ける)） |
| 新しいリリースに上げたのに、新しいパラメータの機能がオンにならない | `update.sh` は、これらのパラメータに触りません。`30-ingress` を個別にデプロイします（[パラメータを変える](options.ja.md#パラメータを変える)） |

## アップグレードするとき

| 症状 | 原因と対処 |
|---|---|
| 上げたあと、Control Plane のタスクが `CannotPullContainerError` で落ちる | そのタグが ECR にありません（push を忘れた、別のリージョンに push した）。`update.sh` はこれを先に確かめます |
| デプロイすると「No changes to deploy」になり、Control Plane が古いイメージのまま | 同じタグに push し直したので、テンプレートが変わっていません。`update.sh` を使うか、`--force` を付けます |
| エンジンの取得用コンテナが `CannotPullContainerError` のままなのに、サービスは安定状態と表示される | `af-engine-tools` のイメージが ECR にありません。`update.sh` でやり直します（[`update.sh` がすること](upgrade.ja.md#updatesh-がすること)） |
| `update.sh` が `FAILED:` で終わる | `50-tts` か `60-engines` をデプロイできませんでした。`30-ingress` はデプロイされています。スタックのイベントで原因を直し、もう一度実行します |
| メンバーに「要再起動」が出ない | `CpTaskRole` に ECR を読む権限がありません。`20-platform` をデプロイし直します（[「要再起動」が出ないとき](upgrade.ja.md#要再起動が出ないとき)） |

## 動いているとき

| 症状 | 原因と対処 |
|---|---|
| `/api/` がすべて 500 を返すのに、`/healthz` は `ok`、ALB のターゲットも正常 | Control Plane がデータベースに届いていません。多くは、RDS がパスワードをローテーションしたことが原因です（[データベースに届かないときのアラームを設定する](alarms.ja.md#アラームが鳴ったとき)） |

## ワークスペースの起動

### 起動すると、すぐに `starting` が返る

想定どおりです。`POST /api/workspace/start` は起動を待たずに、ECS のサービスが `desiredCount 1` になった時点で、今の状態（ふつうは `starting`）を返します。

- `running` になったかは `GET /api/workspace` をポーリングして確かめます。Console は 4 秒ごとにポーリングするので、ページを再読み込みしなくても表示が `running` まで進みます
- Control Plane はバックグラウンドで Agent の準備を待ち、ログに `ecs start: service <name> Agent healthy <n>s after Start` を 1 行出します。待つ時間は既定で 300 秒で、どのリクエストよりも長くしてあります
- ALB のアイドルタイムアウトは 60 秒です。起動の完了を待つようなリクエストは、504 になります

### `running` なのに `502 workspace agent unreachable` が返る

ここでの `running` は、ECS の**タスク**が RUNNING になったという意味です。そこからエントリーポイントが終わって Agent が応答するまで、少し間があります。その間に作成のリクエストを送ると 502 になるので、やり直してください。

### 起動に時間がかかる

Fargate はタスクをまたいでイメージをキャッシュしないので、起動のたびにイメージ全体を取得します。

| 場合 | かかった時間（サンドボックスで測った値） |
|---|---|
| 新しいホームの最初の起動 | 約 126 秒 |
| 使ったことのあるホームで停止→起動 | 128〜218 秒 |

別の測定で、使ったことのあるホームの停止→起動（約 101 秒）を段階に分けると、次のとおりでした。イメージの取得は全体の約 35% です。

| 段階 | 時間 |
|---|---|
| ECS がタスクを作る | 4〜8 秒 |
| 準備（ENI の取り付け） | 16 秒 |
| イメージの取得 | 35 秒 |
| EFS のマウントとコンテナの作成 | 約 25 秒 |
| エントリーポイント | 21 秒 |

- 新しいホームでは、エントリーポイントの中で CLI を一度だけインストールします（全部で約 60 秒）。これはイメージの取得ではなく、インストールの通信にかかる時間です。`~/.local` は EFS に残るので、2 回目以降は繰り返しません
- 止めた直後（1〜2 分以内）に起動し直すと、前のタスクの片付けと重なり、タスクを作るだけで 40〜143 秒かかります。ふだんの運用では起きません。時間を比べるときは、止めてから起動するまでの間隔も書き添えてください

どの段階に時間がかかったかは、次のコマンドで確かめます。

```bash
# image pull window (and the surrounding task lifecycle)
aws ecs describe-tasks --cluster <cluster> --tasks <task-arn> \
  --query 'tasks[0].{created:createdAt,pullStart:pullStartedAt,pullStop:pullStoppedAt,started:startedAt}'
# entrypoint cost (boot-install of the pinned CLIs on a fresh home is NOT a pull)
aws logs tail <ws-log-group> --since 10m
```

## 関連

- [症状から原因を探す](../diagnose/checklist.ja.md)
- [ECS に構築する](install.ja.md)
- [アップグレードする](upgrade.ja.md)
- [データベースに届かないときのアラームを設定する](alarms.ja.md)
