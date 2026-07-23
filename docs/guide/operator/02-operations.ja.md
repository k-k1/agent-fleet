# 02. 日常運用

[English](02-operations.md) | 日本語

構築後の定常運用 — バックアップ、リストア、アップグレード、閉域網、Workspace の停止 — を、
判断ポイントとともに説明します。**実際のコマンド（`backup.sh` / `restore.sh` / upgrade /
air-gapped の各手順）は [deploy/compose/README.md](../../../deploy/compose/README.md) が正**です。
ここではコマンドを複製せず、「何が起きるか・何に注意するか」を補います。作業ディレクトリは
`deploy/compose/`。設計上の前提を深掘りしたいときは [dev/09 §9.7](../../dev/09-deploy.md)。

## バックアップ

`deploy/compose/backup.sh` が `DATA_DIR` を丸ごと timestamped な `tar.gz` に固めます。コマンドと
オプション（`OUT_DIR` / `KEEP` / `--no-stop`）は runbook の "Backup & restore" 節を参照。

### 何が入り、何が入らないか

アーカイブに**入る**もの（= これだけあれば別ホストへ復元できる）:

- `control-plane.db` — テナント / メンバー / ポート / トークンのグラフ。
- 各ユーザーの home（作業ツリー・dotfiles・封筒暗号された `secrets.enc`）。
- 各ユーザーの `claude-config`（**平文の Claude ログイン状態**）。
- Caddy の証明書（復元時に Let's Encrypt のレート制限を避けるため）。

**入らない**もの:

- `shared/jvm`（再取得可能で巨大な Temurin JDK）は意図的に除外。
- **`AF_MASTER_KEY` は入りません。** これは `.env` にあり、設計上アーカイブに含めません。

> この 2 点が運用の肝です。バックアップアーカイブは**平文の Claude 状態を含む機微データ**なので、
> 保管先の権限・暗号化を厳格にしてください。同時に、アーカイブだけを持っていても
> `AF_MASTER_KEY` が無ければ封筒暗号された資格情報は復号できません。逆に `AF_MASTER_KEY` を
> 失えば、すべての過去アーカイブが復号不能になります（crypto-shred・[03](03-security.ja.md)）。**鍵と
> データは別々に、しかし両方をバックアップする**のが正解です。

### ユーザーへの影響

`backup.sh` は既定で **CP と Caddy を一瞬だけ停止**して SQLite の整合スナップショットを取り、
すぐ再開します。このとき**ユーザーの Workspace（`af-ws-*`）は compose 管理外なので止まりません** —
セッションは切れず、作業は継続します。停止中の数秒は Console のログインや API 中継が一時的に
応答しなくなる程度です。呼び出し側で静止を保証済みなら `--no-stop` で無停止取得もできます。

### cron 化

本番では `backup.sh` を cron で定期実行します。`OUT_DIR` で社内のバックアップ領域（別ボリューム／
リモート）を指定し、`KEEP` で世代数を制御します。cron エントリの例は runbook の "Backup & restore"
節にあります。取得後は世代のプルーニングまで自動で行われます。

## リストア

クリーンなホスト、またはデータ喪失後の復旧手順です。コマンドは runbook の "Backup & restore"。

流れは「`.env` を用意（**バックアップ元と同一の `AF_MASTER_KEY`** を金庫から復元）→ `restore.sh
<archive>` → `docker compose up -d` → Console から各 Workspace を**起動**」です。要点を 3 つ。

1. **`AF_MASTER_KEY` は元と同一値でなければならない。** 違う／欠落していると wrapped DEK を
   unwrap できず、資格情報は復号できません。金庫からの復元を最初に確認します。
2. **`DATA_DIR` の basename 制約。** 復元先の親パスは元と違ってよく（例 `/srv` → `/mnt`）、CP が
   起動時に各 Workspace の on-disk root を現在の `DATA_DIR` へ付け替えます。ただしアーカイブ先頭の
   ディレクトリ名（= 元 `DATA_DIR` の basename）と、復元先 `DATA_DIR` の basename は**一致**させて
   ください。`restore.sh` がこれを検証し、不一致なら拒否します。
3. **Workspace は Console からの「起動」で再水和する。** リストア直後には Workspace コンテナは
   存在しません（compose 管理外だから）。ユーザー（または管理者）が Console で「起動」を押すと、CP が
   復元済み DB のポート/トークンで `af-ws-*` を再作成し、home の `secrets.enc` と `claude-config`
   からユーザーの接続と Claude ログインが復活します。

## アップグレード

`.env` の `VERSION` を新しいタグに変え、image を pull（またはビルド）して `up -d` するだけです。
コマンドは runbook の "Upgrade" 節。

- **スキーマ migration は CP に埋め込まれ、起動時に自動適用**されます（**前方互換**）。手動の
  migration 実行は不要です。
- **ダウングレードは非対応**です。新バージョンで適用された migration を古い CP は理解できません。
  したがって**アップグレードの前に必ず `backup.sh` を取る**こと。何かあれば「古い image に戻す」の
  ではなく「バックアップからリストアする」のが正しい後退経路です。
- 破壊的変更の有無はリリースノートで確認してください。

## 閉域網（air-gap）へのインストール

外部ネットワークに出られないホストにも入れられます。ネット接続のあるマシンで image を
`docker save` して持ち込み、対象ホストで `load-images.sh` して `up -d` します。コマンドは runbook の
"Air-gapped install" 節。

判断ポイントが 2 つあります。

- **TLS**: 閉域では Let's Encrypt が使えないので、[01 §4](01-install.ja.md) の `tls internal`（自己署名）
  へ切り替えるか、社内 CA を使います。
- **Claude のインストール**: Workspace image は既定でコンテナ起動時に最新の Claude を取得します。
  完全オフラインのホストでは `CLAUDE_INSTALL=0`（`WS_ENV` 経由）にし、Claude を焼き込んだ image を
  使ってください。

## アイドル停止と force-stop

- **アイドル自動停止（scale-to-zero）**: `AF_SESSION_IDLE_TIMEOUT` / `AF_WS_IDLE_TIMEOUT` を設定すると、
  一定時間使われていない Workspace を自動で止めます（テナント単位の上書きは Admin UI）。既定は
  無効。停止した Workspace は、ユーザーが次にターミナルを開くと自動起動します（`AF_AUTOSTART`）。
  資源の節約に有効です。env の意味は [.env.example](../../../deploy/compose/.env.example)、仕組みは
  [dev/09 §9.4](../../dev/09-deploy.md)。
- **force-stop（力業）**: `docker compose down` では**ユーザーの Workspace は止まりません**（compose
  管理外）。特定の Workspace を確実に止めたいときは、super_admin が Console の Admin パネルから
  force-stop します。ホスト全体をメンテナンスで完全に落とす必要があるときは、CP/Caddy を止めた
  うえで、残る `af-ws-*` コンテナを別途 `docker stop` する必要があります（この点は障害対応の
  [04](04-troubleshooting.ja.md) でも触れます）。
