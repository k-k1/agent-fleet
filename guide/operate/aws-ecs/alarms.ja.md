---
audience: "AWS の ECS（`ecs` / `ecs-ec2`）の配備を見守る配備管理者"
source_of_truth: "`deploy/aws/ecs/cfn/30-ingress.yaml` の `CpDbUnavailableFilter` / `CpDbUnavailableAlarm` と `CpAlarmEmail`、`20-platform.yaml` の `CpTaskRole`"
---

# データベースに届かないときのアラームを設定する

[English](alarms.md) | 日本語

Control Plane がデータベースにつながらなくなったとき、メールで知らせが届くようにします。
任意の設定に見えますが、構築したら必ず設定してください。

## 全体像

| あなたがやること | 起きること |
|---|---|
| 何もしない | Control Plane のログの `DB_UNAVAILABLE` が、CloudWatch のメトリクス `AgentFleet/<stack>/DbUnavailable` になる。誰にも知らせは届かない |
| `30-ingress` に `CpAlarmEmail` を設定する | SNS のトピックとアラームができ、そのアドレスにメールが届く |
| 届いた SNS の確認メールで購読を承認する | アラームが鳴ると、メールが届く |
| `20-platform` をデプロイし直す | RDS がパスワードを入れ替えても、Control Plane が新しいパスワードを読み直せる |

## なぜ設定するのか

データベースに届かなくなっても、外からは何も異常に見えません。

- `/healthz` はデータベースに触らないので、`ok` を返し続けます
- そのため ALB のターゲットは正常、ECS のサービスは安定、CloudFormation にもドリフトはありません
- RDS は管理するマスターパスワードを定期的に入れ替えます。ECS はタスク定義の `secrets` を**タスクの起動時に 1 回だけ**解決するので、動き続けている Control Plane は古いパスワードを出し続けます

Control Plane は、Postgres に拒否されるとシークレットを読み直します。このアラームは、それすら効かなかったときに知らせるものです。

## 設定する

1. `30-ingress` に `CpAlarmEmail` を設定してデプロイします（[パラメータを変える](options.ja.md#パラメータを変える)）

   ```bash
   --parameter-overrides CpAlarmEmail=oncall@example.com ...
   ```

2. 届いた SNS の確認メールで、購読を承認します。**承認していない購読は、アラームがないのと同じです**
3. `20-platform` をデプロイし直します（下を参照）

`CpAlarmEmail` の既定は空で、メトリクスはあっても誰にも知らせません。

### `20-platform` もデプロイし直す

`30-ingress` だけでは足りません。パスワードの読み直しは `CpTaskRole` で動き、そのロールに `secretsmanager:GetSecretValue` の権限が要ります。

- 実行ロールにある同じ権限は、起動時に変数を入れるためだけのものです
- タスクロールの権限がないと、Control Plane は起動時に入ったパスワードで問題なく動きます。次の入れ替えまでは

読み直しが失敗していないかは、次のコマンドで確かめます。何も出なければ問題ありません。

```bash
aws logs filter-log-events --log-group-name "/af/<ingress-stack>/cp" \
  --filter-pattern DB_SECRET_REFRESH_FAILED
```

## アラームが鳴ったとき

1. 上のコマンドで `DB_SECRET_REFRESH_FAILED` を探します。出ていれば、`CpTaskRole` の `secretsmanager:GetSecretValue` がない可能性が高いので、`20-platform` をデプロイし直します
2. それでも戻らないときの最後の手段は、Control Plane のサービスを新しくデプロイし直すことです。ブルー/グリーンなので止まらず、約 4 分で終わります

   ```bash
   aws ecs update-service --cluster <cluster> --service <cp-service> --force-new-deployment
   ```

データベースに届くかを外から確かめるときは、`/healthz` ではなく `/readyz` を見ます。`/readyz` はデータベースに問い合わせ、届かなければ `503` を返します。

## 関連

- [ECS に構築する](install.ja.md)
- [ECS で困ったとき](troubleshooting.ja.md)
- [症状から原因を探す](../diagnose/checklist.ja.md)
