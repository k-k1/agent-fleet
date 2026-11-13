---
audience: "AWS の ECS（`ecs` / `ecs-ec2`）のスタックを手でデプロイする配備管理者"
source_of_truth: "`deploy/aws/ecs/env.sh` の `af_cfn_deploy`、`cfn/20-platform.yaml` の `CfnTemplatesBucket`"
---

# テンプレートの大きさの上限に気をつける

[English](template-size.md) | 日本語

CloudFormation は、51,200 バイトを超えるテンプレートを直接受け取りません。`30-ingress.yaml` は上限ぎりぎりの大きさで、上限を超えてもよいことにしているので、いつも S3 を経由して渡します。
このページのとおりにすれば、テンプレートが大きくなったリリースでも、手動のデプロイが上限で止まりません。

## 全体像

| あなたがやること | 起きること |
|---|---|
| `standup.sh`・`update.sh` でデプロイする | スクリプトが毎回テンプレートの大きさを測り、上限を超えるものは自動で S3 経由にする。何もしなくてよい |
| `30-ingress` を手でデプロイする | いつも `--s3-bucket "$CFN_BUCKET" --s3-prefix cfn` を付ける。上限を超えたテンプレートでは、付けないと AWS CLI が拒否する |
| 撤去する | `teardown.sh` が、`20-platform` を消す前に S3 Bucket を空にする |

## 手でデプロイするとき

`30-ingress` を `aws cloudformation deploy` でデプロイするときは、いつも `--s3-bucket` と `--s3-prefix` を付けます。

```bash
CFN_BUCKET=$(aws cloudformation describe-stacks --stack-name <platform stack> --query "Stacks[0].Outputs[?OutputKey=='CfnTemplatesBucket'].OutputValue" --output text)
aws cloudformation deploy --stack-name <ingress stack> --template-file cfn/30-ingress.yaml --s3-bucket "$CFN_BUCKET" --s3-prefix cfn \
  --capabilities CAPABILITY_NAMED_IAM --parameter-overrides ...
```

- `CFN_BUCKET` は、`20-platform` の出力 `CfnTemplatesBucket` です
- 上の S3 Bucket は `20-platform` が作ります。`30-ingress` はいつも `20-platform` のあとにデプロイするので、S3 Bucket はすでにあります
- S3 Bucket に置いたテンプレートは、7 日で消えます
- デプロイする人には、この S3 Bucket への `s3:PutObject` と `s3:GetObject` が要ります（[デプロイに必要な IAM 権限](iam.ja.md)）

ほかのテンプレートは上限を超えない大きさに保っているので、S3 を経由せずに渡せます。

## スクリプトがしていること

| スクリプト | すること |
|---|---|
| `env.sh` の `af_cfn_deploy` | `cloudformation deploy` を呼ぶ唯一の入り口。呼ぶたびにファイルの大きさを測り、51,200 バイトを超えれば S3 経由にする。S3 Bucket は環境変数 `AF_CFN_BUCKET`、なければ `20-platform` の出力 `CfnTemplatesBucket` から決める |
| `standup.sh`・`update.sh` | `af_cfn_deploy` を通してデプロイする |
| `teardown.sh` | `20-platform` を消す前に S3 Bucket を空にする。CloudFormation は空でない S3 Bucket を消せないため |

どのテンプレートが大きいかを名前で覚えるのではなく、毎回測ります。そのため、今後ほかのテンプレートが大きくなっても同じように動きます。

## テンプレートの説明を短くしている理由

上限はテンプレートの本文全体にかかります。パラメータの `Description:` も YAML のコメントも、同じように数に入ります。

- そのため、テンプレートの `Description:` は 1〜2 行だけにしています
- 測った値・トレードオフ・落とし穴は [`cfn/PARAMETERS.md`](https://github.com/k-k1/agent-fleet/blob/main/deploy/aws/ecs/cfn/PARAMETERS.md) にあります

## うまくいかないとき

| 症状 | 原因と対処 |
|---|---|
| `aws cloudformation deploy` が、テンプレートが大きすぎるというエラーで終わる。スタックのイベントには何もない | テンプレートが上限を超えていて、AWS CLI が API を呼ぶ前に、サイズを理由に拒否しました。`--s3-bucket "$CFN_BUCKET" --s3-prefix cfn` を付けてやり直します |
| `af_cfn_deploy` が「needs to go via S3, but no hand-off bucket can be resolved」で止まる | `20-platform` がまだないか、出力 `CfnTemplatesBucket` を読めません。先に `20-platform` をデプロイするか、`AF_CFN_BUCKET` で S3 Bucket を指定します |
| `teardown.sh` で `20-platform` の削除が失敗する | S3 Bucket が空になっていません。その直前に、S3 Bucket を空にする手順が表示したエラーを確認します |

## 関連

- [ECS に構築する](install.ja.md)
- [アップグレードする](upgrade.ja.md)
- [ECS 構成の全体像](overview.ja.md)
