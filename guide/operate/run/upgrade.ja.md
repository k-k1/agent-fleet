---
audience: "配備を新しいバージョンに上げる人"
source_of_truth: "`deploy/compose/docker-compose.yml`、ECS の手順書と `deploy/aws/ecs/update.sh`（食い違ったら手順書とスクリプトが正）"
---

# アップグレードする

[English](upgrade.md) | 日本語

配備を新しいバージョンに上げ、メンバーのワークスペースを新しいイメージへ移します。
コマンドは形態ごとの手順書にあります。ここでは、何が起きて何に気をつけるかをまとめます。

## 全体像

| あなたがやること | 起きること |
|---|---|
| 先に `backup.sh` でバックアップを取る | 戻る道ができる。ダウングレードはできないので、戻すときは復元します |
| バージョンを変えてイメージを取得し、Control Plane を起動し直す | スキーマの移行は Control Plane が起動時に自動で行う |
| ワークスペースの入れ替えをメンバーに任せる | 動いているワークスペースは古いイメージのまま。Console に「要再起動」が出る |

| 形態 | 手順書 |
|---|---|
| Docker Compose | 下の [Docker Compose で上げる](#docker-compose-で上げる) |
| ECS（`ecs` / `ecs-ec2`） | [アップグレードする（ECS）](../aws-ecs/upgrade.ja.md) |
| Kubernetes | [CP をアップグレードする](../kubernetes/upgrade.ja.md) |

## 上げる前に

- **必ずバックアップを取ってください。** 新しいバージョンで移行したスキーマは、古い Control Plane には読めません。うまくいかなかったときは、古いイメージに戻すのではなく、バックアップから復元します（[バックアップして復元する](backup-restore.ja.md)）
- 互換性のない変更がないか、リリースノートで確かめます
- スキーマの移行は Control Plane に組み込まれていて、起動時に自動で行われます。手で実行するものはありません

## Docker Compose で上げる

`.env` の `VERSION` を新しいタグに変え、イメージを取得（またはビルド）して `docker compose up -d` します。

```bash
# edit .env: VERSION=<new tag>, and point WS_IMAGE at the matching workspace tag
# (WS_IMAGE is an independent variable — it is not derived from VERSION)
docker compose pull            # or rebuild: docker compose build
# the workspace image is not a compose service — pull/rebuild it separately:
docker pull <WS_IMAGE>         # or rebuild: docker build -t <WS_IMAGE> ../../workspace
docker compose up -d
```

- イメージは GHCR（`ghcr.io/k-k1/agent-fleet/*`）から取得します。`REGISTRY` と `VERSION` で決まり、レジストリへのログインはいりません
- **ワークスペースのイメージは compose のサービスではありません。** `docker compose pull` では取得できないので、別に `docker pull` するか、ビルドし直します。`WS_IMAGE` は `VERSION` から作られない別の変数なので、合うバージョンを指すように変えます
- `docker compose up -d` は動いているワークスペースに触りません。Docker を再起動しても、作ったときの環境のままです。上げたあと（`AF_WS_WORKLOAD_AWS` を変えたあとも）、各ワークスペースを停止して起動します（[ワークスペースを新しいイメージに移す](#ワークスペースを新しいイメージに移す)）
- クラウドの VM の上なら、そのあとでホストのクラウドの ID がワークスペースに渡らないようになっているかを確かめます（[そのほかの運用上の制御](../secure/controls.ja.md#ワークスペースにホストのクラウドの身元を渡さない)）
- ネイティブパッケージの手順は [ネイティブパッケージを更新する](../native/update.ja.md) にあります

## ECS で上げる

`deploy/aws/ecs/update.sh` が 1 回で行います。イメージを ECR に push し、ingress スタックの `ImageTag` だけを変えてデプロイし、Control Plane のサービスが入れ替わるのを待ちます。

| 気をつけること | 起きること |
|---|---|
| バージョンによって、あるスタックを先に更新する必要がある | `update.sh` が最初に確かめ、何にも触れずに止まります。単独で実行する `aws cloudformation deploy` のコマンドを表示するので、実行してから `update.sh` をもう一度実行します |
| データスタックは `update.sh` では更新されない | `Persistence=retain` で日次の EFS バックアップがない配備を、`update.sh` が読み取りだけで見つけて警告し、変更セットのコマンドを表示します。手順は [EFS のバックアップがあるか確かめる](../aws-ecs/upgrade.ja.md#efs-のバックアップがあるか確かめる) にあります |
| ネットワークスタック（`00-network`）も `update.sh` では更新されない | 手で更新します。新しいテンプレートを既定値のまま入れると、その S3 エンドポイントが、まだ再起動していないワークスペースの S3 リクエストをすべて拒否します（AccessDenied）。動いているワークスペースをすべて再起動するメンテナンスの時間を取ってください。手順は [v0.34.x から上げる](../aws-ecs/upgrade.ja.md#v034x-から上げる) にあります |
| `ecs-ec2` で、リリースがスロットの起動テンプレートを変えた | 既存のスロットには、入れ替えたときにしか反映されません（[スロットを入れ替える・止める](../aws-ecs/slot-maintenance.ja.md#古いスロット)） |

### EFS バックアップがないという通知

データスタックに日次の EFS バックアップがないと、Console の管理画面のすべてのセクションに、配備管理者向けの通知が出ます。
「この配備はメンバーのデータを EFS に保持（Persistence=retain）していますが、データスタックに日次の EFS バックアップがありません。」で始まるものです。

- `update.sh` が結果を ingress スタックのパラメータ `EfsBackup` に記録し、データスタックにバックアップが入ったことを確かめた時点で消えます
- `update.sh` がデータスタックを読めなかったときは前の判定を残すので、修正を確かめられる実行があるまで、通知は残ります
- 一度も記録していない配備には出ません
- 同じ確認は `deploy/aws/ecs/efs-backup-check.sh` だけでも実行できます。バックアップがあるか、配備にバックアップがいらない（`Persistence=delete`）ときだけ、終了コード 0 で終わります

## ワークスペースを新しいイメージに移す

動いているワークスペースは、自動では新しくなりません。次に作られたときから新しいイメージになります。

| 方法 | 起きること |
|---|---|
| メンバーが Console で停止して起動する | 新しいイメージと環境でコンテナが作り直されます。古いワークスペースのメンバーには「**要再起動**」のバッジが出るので、止めるタイミングは本人に任せます |
| メンバーが「**イメージの自動更新**」をオンにしておく | 使われていないときに、新しいイメージへ自動で再起動します（[新しいイメージへ自動で再起動する](../../admin/resources/idle-stop.ja.md#新しいイメージへ自動で再起動する)） |

自動更新について、配備管理者が知っておくこと：

- 静かだったとみなす時間は、テナントの上限です。配備の既定は `AF_IMAGE_REFRESH_QUIET` で、15 分です。`0` を指定すると自動更新を行いません
- アイドル停止の活動のシグナルは、別の Control Plane のレプリカが持つものも含めてすべて尊重します
- 再作成はせず、再起動だけです。同じイメージで試すのは、ワークスペースごとに 1 回だけです。起動に失敗したら停止したままです
- 毎回、監査ログに `workspace.image_refresh` が残り、メンバーに通知が 1 回届きます

## 関連

- [バックアップして復元する](backup-restore.ja.md)
- [閉じたネットワークに入れる](air-gapped.ja.md)：レジストリに届かないホストでイメージを用意する
