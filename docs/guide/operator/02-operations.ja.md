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
  **データ領域の外に置いてください** — アーカイブにはテナント定義のサインイン方法の
  client secret（この鍵で封印済み）も入るようになったため、鍵をデータの隣に置くと
  封筒暗号の意味そのものが無くなります。

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

- **image は GHCR から取得します**（`ghcr.io/k-k1/agent-fleet/*`。`REGISTRY` + `VERSION` で解決）。
  pull にレジストリログインは不要です。**Workspace image は compose のサービスではない**ので
  `docker compose pull` では取得されません。別途 `docker pull` するか、最初の「起動」で
  オンデマンドに取得させてください。
- **スキーマ migration は CP に埋め込まれ、起動時に自動適用**されます（**前方互換**）。手動の
  migration 実行は不要です。
- **ダウングレードは非対応**です。新バージョンで適用された migration を古い CP は理解できません。
  したがって**アップグレードの前に必ず `backup.sh` を取る**こと。何かあれば「古い image に戻す」の
  ではなく「バックアップからリストアする」のが正しい後退経路です。
- 破壊的変更の有無はリリースノートで確認してください。

## 閉域網（air-gap）へのインストール

外部ネットワークに出られないホストにも入れられます。ただし
[ADR 0037](../../decisions/0037-registry-policy.md) 以降、image は GHCR で配布し
**リリースに image tar は添付しません**。レジストリに到達できないホストは、
`ghcr.io/k-k1/agent-fleet/*` を社内レジストリにミラーして `REGISTRY` をそこへ向けるか、
image を手で持ち込みます（ネット接続のあるマシンで `release.sh --save` してビルド＆
`docker save` → 対象ホストで `load-images.sh`）。コマンドは runbook の "Air-gapped install" 節。

判断ポイントが 3 つあります。

- **image の取得元**: 社内レジストリのミラーなら `docker compose pull` がそのまま使え、
  保守も軽くなります。手持ち込みの tar は、アップグレードのたびにビルド／コピー／load を
  繰り返すことになり、image 名を `.env` の `REGISTRY` と一致させる必要があります。
- **TLS**: 閉域では Let's Encrypt が使えないので、[01 §4](01-install.ja.md) の `tls internal`（自己署名）
  へ切り替えるか、社内 CA を使います。
- **Claude のインストール**: Workspace image は既定でコンテナ起動時に最新の Claude を取得します。
  完全オフラインのホストでは `CLAUDE_INSTALL=0`（`WS_ENV` 経由）にし、Claude を焼き込んだ image を
  使ってください。

「閉域で動く」の意味を取り違えないでください。image がローカルにあればフリートは**起動**
できますが、エージェント自身はモデルのエンドポイントに到達できなければ何もできません。この
オフライン導入経路は、制限された社内ネットワーク上のホスト向けであって、本当に切り離された
ネットワーク向けではありません。

## フリート方針（全エージェントへの指示）を配る

ワークスペースで動くすべてのエージェントに、**運用者からの方針**を読ませられます。実体は
リポジトリの `workspace/workspace-notes.md` で、Workspace イメージに焼き込まれて各
コンテナへ配られます（claude は `/etc/claude-code/CLAUDE.md` の managed policy として毎セッション
読み込み、codex / opencode は起動ごとに同じ本文が seed されます）。

- **やってはいけないこと**（例: リポジトリを消す、認証情報を平文で書く）、**この環境の制約**
  （root が無い、Docker が無い、メモリが共有）、**ブランチの扱い**など、フリート共通の運用ルールを
  書く場所です。
- **反映にはイメージの再ビルドが必要**です。編集 → イメージを作り直し → 各利用者が
  ワークスペースを「作り直す」か、次にコンテナが作られたときに効きます。
- 利用者個人の書き足しは**この層ではありません**。各自の ⚙設定 →「エージェントへの指示」で行い、
  フリート方針と衝突したときはフリート方針が優先されます（[member/06](../member/06-agents.ja.md#エージェントへの指示自分の働き方を一度だけ書く)）。
- **長さはそのまま全セッションのコンテキスト費用になります。** 全エージェントが毎回読むので、
  増やす前に「本当に全員・毎回必要か」を確認してください。cursor にはこの層を配れません。

## インターネットに公開したとき

Control Plane を公開ホスト名で出すと、**数時間のうちに脆弱性スキャナが来ます**（実測: 公開から
9 時間で `/actuator/heapdump` や `/.env` を狙う探索が 172 回）。慌てる必要はありませんが、
知っておくべきことが 3 つあります。

- **セッション無しで届くのは限られています**: `/healthz`・`/login`・`/oauth2/*`・`/brand/*` と
  旧 URL の転送だけで、それ以外は 401 です。`/internal/*`（egress 取り込み）と `/mcp`・`/git/*` は
  ゲートの外ですが、**それぞれ自前の認証**を持ちます（未有効なら 404）。
- **アクセスログに状態コードが出ます**。`GET /actuator/heapdump 401 0s` のように読めるので、
  「弾いたのか、返してしまったのか」をログだけで判断できます。探索を数えたいなら
  `401` で絞ってください。
- **IP 単位で見たい・遮断したいなら CP の外側で**。送信元別の分析は ALB のアクセスログ、
  遮断は AWS WAF（マネージドルール）が定石です。CP 側で個別に弾く仕組みは、
  認証の外に新しい判断を増やすだけなので入れていません。

## アイドル停止と force-stop

- **アイドル自動停止（scale-to-zero）**: 使われていない claude セッションを **1 時間**で停止し、
  何も動いていない Workspace を **2 時間**で停止します。**これが既定**で、
  `AF_SESSION_IDLE_TIMEOUT` / `AF_WS_IDLE_TIMEOUT` で変えられます（テナント単位の上書きは Admin UI。
  テナントが `0` を入れればそのテナントだけ無効、env に `0` を入れればデプロイ全体で無効）。
  停止した Workspace は、ユーザーが次にターミナルを開くと自動起動します（`AF_AUTOSTART`）。
  資源の節約に有効です。env の意味は [.env.example](../../../deploy/compose/.env.example)、仕組みは
  [dev/09 §9.4](../../dev/09-deploy.md)。
- **force-stop（力業）**: `docker compose down` では**ユーザーの Workspace は止まりません**（compose
  管理外）。特定の Workspace を確実に止めたいときは、super_admin が Console の Admin パネルから
  force-stop します。ホスト全体をメンテナンスで完全に落とす必要があるときは、CP/Caddy を止めた
  うえで、残る `af-ws-*` コンテナを別途 `docker stop` する必要があります（この点は障害対応の
  [04](04-troubleshooting.ja.md) でも触れます）。
