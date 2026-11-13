---
audience: "配備のデータを守り、壊れたときに戻す人"
source_of_truth: "`deploy/compose/backup.sh` と `restore.sh`（食い違ったらスクリプトが正）"
---

# バックアップして復元する

[English](backup-restore.md) | 日本語

Docker Compose の配備のデータを定期的にアーカイブにまとめ、別のホストやデータを失ったあとのホストに戻せるようにします。

## 全体像

| あなたがやること | 起きること |
|---|---|
| `backup.sh` を cron で定期実行する | `DATA_DIR` がまるごと日時付きの `tar.gz` になり、古い世代は消える |
| `AF_MASTER_KEY` をデータとは別のシークレットストアに置く | アーカイブだけ盗まれても、メンバーの認証情報は読めない |
| 復元するときは、同じキーを戻してから `restore.sh` を実行する | Control Plane が上がり、メンバーが「起動」を押すとワークスペースが戻る |

## アーカイブに入るもの

| 入るもの | 入らないもの |
|---|---|
| `control-plane.db`（テナント・メンバー・ポート・トークン） | `shared/jvm`（取り直せる大きな JDK） |
| 各メンバーのホームディレクトリ（作業ツリー・dotfiles・暗号化された `secrets.enc`） | **`AF_MASTER_KEY`**（`.env` にあり、わざと入れません） |
| 各メンバーの `claude-config`（**平文の Claude のログイン状態**） | |
| Caddy の証明書（復元時に Let's Encrypt のレート制限に当たらないように） | |

これだけあれば、別のホストに復元できます。

> [!WARNING]
> **キーとデータは別々に、両方ともバックアップしてください。**
>
> - アーカイブには平文の Claude の状態が入ります。保管先の権限と暗号化を厳しくしてください
> - アーカイブには、テナントが登録したサインイン方法の client secret も、このキーで暗号化された状態で入ります。キーをデータの隣に置くと、暗号化の意味がなくなります
> - `AF_MASTER_KEY` をなくすと、過去のアーカイブはすべて復号できなくなります
> - `AF_MASTER_KEY` を空のままにすると、メンバーの認証情報はホームディレクトリに暗号化されずに保存され、アーカイブにもそのまま入ります。`AUTH=dev` のほかでは使わないでください（[配備のキーを扱う](../secure/deployment-keys.ja.md#キーのない配備にキーを入れる)）

## バックアップを取る

`deploy/compose/backup.sh` を実行します。本番では cron で定期実行します。

```bash
deploy/compose/backup.sh              # -> deploy/compose/backups/agent-fleet-<ts>.tar.gz
OUT_DIR=/mnt/backups KEEP=14 deploy/compose/backup.sh
```

| 指定 | 意味 |
|---|---|
| `OUT_DIR` | アーカイブを置く場所。別のボリュームやリモートを指定します。既定は `deploy/compose/backups/` です |
| `KEEP` | 残す世代の数。既定は 7 です。`0` を指定すると消しません |
| `--no-stop` | Control Plane を止めずに取ります。呼び出し側で書き込みが止まっていることを保証できるときだけ使います |

取ったあと、`KEEP` を超えた古い世代は自動で消えます。

### メンバーへの影響

- `backup.sh` は既定で **Control Plane と Caddy を数秒だけ止めて** SQLite の整合したスナップショットを取り、すぐに再開します。そのあと `${DATA_DIR}` を tar にまとめます
- **メンバーのワークスペース（`af-ws-*`）は止まりません。** compose の管理外だからです。セッションは切れず、作業は続きます
- 止まっている数秒のあいだ、Console のサインインと API の中継が応答しなくなるだけです

## 復元する

クリーンなホスト、またはデータを失ったあとのホストで行います。

1. `.env` を用意し、**バックアップ元と同じ `AF_MASTER_KEY`** をシークレットストアから戻します
2. `deploy/compose/restore.sh <アーカイブ>` を実行します
3. `docker compose up -d` で Control Plane と Caddy を上げます
4. メンバー（または管理者）が Console でワークスペースを「**起動**」します

```bash
# 1) recreate .env with the SAME AF_MASTER_KEY (from your vault)
# 2) restore the archive:
deploy/compose/restore.sh /mnt/backups/agent-fleet-<ts>.tar.gz
# 3) bring it up; Start each workspace from the Console to rehydrate containers
docker compose up -d
```

気をつけることは 3 つです。

| 注意 | 理由 |
|---|---|
| `AF_MASTER_KEY` は元と同じ値にする | 値が違うか空だと、メンバーの認証情報を復号できません。最初にシークレットストアからの復元を確かめます |
| `DATA_DIR` の**最後のディレクトリ名**を元と同じにする | 親のパスは違ってかまいません（`/srv` → `/mnt` など）。Control Plane が起動時に付け替えます。最後の名前がアーカイブの最上位のディレクトリ名と一致しないと、`restore.sh` は復元を拒否します |
| 復元した直後はワークスペースのコンテナがない | compose の管理外なので、アーカイブには入りません。「起動」を押すと、Control Plane が復元した DB のポートとトークンでコンテナを作り直し、home の `secrets.enc` と `claude-config` から接続と Claude のログインが戻ります |

> [!TIP]
> 復元の手順は、本番で必要になる前に一度試してください。`DATA_DIR` の名前の制約に当たるのは、たいていそのときです。

## ほかの形態

| 形態 | バックアップと復元 |
|---|---|
| ECS（`ecs` / `ecs-ec2`） | [EFS をバックアップ・復元する](../aws-ecs/backup.ja.md) |
| Kubernetes | [バックアップ・復元する（Kubernetes）](../kubernetes/backup.ja.md) |

## 関連

- [アップグレードする](upgrade.ja.md)：アップグレードの前には必ずバックアップを取ります
- [配備のキーを扱う](../secure/deployment-keys.ja.md)：`AF_MASTER_KEY` の紛失が意味すること
