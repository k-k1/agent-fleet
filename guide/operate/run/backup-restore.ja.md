---
audience: "配備のデータを守り、壊れたときに戻す人"
source_of_truth: "`deploy/compose/backup.sh` と `restore.sh`（食い違ったらスクリプトが正）"
---

# バックアップして復元する

[English](backup-restore.md) | 日本語

Docker Compose の配備のデータを定期的にアーカイブに固め、別のホストやデータを失ったあとのホストに戻せるようにします。
コマンドは [compose の手順書の「Backup & restore」](../../../deploy/compose/README.md#backup--restore) にあります。

## 全体像

| あなたがやること | 起きること |
|---|---|
| `backup.sh` を cron で定期実行する | `DATA_DIR` がまるごと日時付きの `tar.gz` になり、古い世代は消える |
| `AF_MASTER_KEY` をデータとは別の金庫に置く | アーカイブだけ盗まれても、メンバーの資格情報は読めない |
| 復元するときは、同じ鍵を戻してから `restore.sh` を実行する | Control Plane が上がり、メンバーが「起動」を押すとワークスペースが戻る |

## アーカイブに入るもの

| 入るもの | 入らないもの |
|---|---|
| `control-plane.db`（テナント・メンバー・ポート・トークン） | `shared/jvm`（取り直せる大きな JDK） |
| 各メンバーの home（作業ツリー・dotfiles・暗号化された `secrets.enc`） | **`AF_MASTER_KEY`**（`.env` にあり、わざと入れません） |
| 各メンバーの `claude-config`（**平文の Claude のログイン状態**） | |
| Caddy の証明書（復元のときに Let's Encrypt のレート制限に当たらないように） | |

これだけあれば、別のホストに復元できます。

> [!WARNING]
> **鍵とデータは別々に、両方ともバックアップしてください。**
>
> - アーカイブには平文の Claude の状態が入ります。保管先の権限と暗号化を厳しくしてください
> - アーカイブには、テナントが登録したサインイン方法の client secret も、この鍵で封じた形で入ります。鍵をデータの隣に置くと、暗号化の意味がなくなります
> - `AF_MASTER_KEY` をなくすと、過去のアーカイブはすべて復号できなくなります

## バックアップを取る

`deploy/compose/backup.sh` を実行します。本番では cron で定期実行します。

| 指定 | 意味 |
|---|---|
| `OUT_DIR` | アーカイブを置く場所。別のボリュームやリモートを指定します。既定は `deploy/compose/backups/` です |
| `KEEP` | 残す世代の数。既定は 7 です。`0` を指定すると消しません |
| `--no-stop` | Control Plane を止めずに取ります。呼び出し側で書き込みが止まっていることを保証できるときだけ使います |

cron のエントリの例は手順書にあります。取ったあと、`KEEP` を超えた古い世代は自動で消えます。

### メンバーへの影響

- `backup.sh` は既定で **Control Plane と Caddy を数秒だけ止めて** SQLite の整合したスナップショットを取り、すぐに再開します
- **メンバーのワークスペース（`af-ws-*`）は止まりません。** compose の管理外だからです。セッションは切れず、作業は続きます
- 止まっている数秒のあいだ、Console のサインインと API の中継が応答しなくなるだけです

## 復元する

クリーンなホスト、またはデータを失ったあとのホストで行います。

1. `.env` を用意し、**バックアップ元と同じ `AF_MASTER_KEY`** を金庫から戻します
2. `deploy/compose/restore.sh <アーカイブ>` を実行します
3. `docker compose up -d` で Control Plane と Caddy を上げます
4. メンバー（または管理者）が Console でワークスペースを「**起動**」します

気をつけることは 3 つです。

| 注意 | 理由 |
|---|---|
| `AF_MASTER_KEY` は元と同じ値にする | 違う値やなしでは、メンバーの資格情報を復号できません。最初に金庫からの復元を確かめます |
| `DATA_DIR` の**最後のディレクトリ名**を元と同じにする | 親のパスは違ってかまいません（`/srv` → `/mnt` など）。Control Plane が起動のときに付け替えます。最後の名前はアーカイブの先頭のディレクトリ名と一致しないと、`restore.sh` が拒否します |
| 復元した直後はワークスペースのコンテナがない | compose の管理外なので、アーカイブには入りません。「起動」で Control Plane が復元した DB のポートとトークンで作り直し、home の `secrets.enc` と `claude-config` から接続と Claude のログインが戻ります |

> [!TIP]
> 復元の手順は、本番で必要になる前に一度試してください。`DATA_DIR` の名前の制約に当たるのは、たいていそのときです。

## ほかの形態

| 形態 | バックアップと復元 |
|---|---|
| ECS（`ecs` / `ecs-ec2`） | [ECS の手順書の「EFS backup and restore」](../../../deploy/aws/ecs/README.md#efs-backup-and-restore) |
| Kubernetes | [バックアップ・復元する](../kubernetes/backup.ja.md) |

## 関連

- [アップグレードする](upgrade.ja.md)：アップグレードの前には必ずバックアップを取ります
- [配備の鍵を扱う](../secure/deployment-keys.ja.md)：`AF_MASTER_KEY` の紛失が意味すること
