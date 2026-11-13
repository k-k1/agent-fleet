---
audience: "配備の様子がおかしいときに切り分ける配備管理者（ホストのターミナルを扱う人）"
source_of_truth: "コマンドは `deploy/` 配下の runbook とスクリプト（記述が食い違ったら、このページのバグ）"
---

# 症状から原因を探す

[English](checklist.md) | 日本語

「立ち上がったのに動かない」「使えないと言われた」ときに、症状から原因の見当を付けます。
直すためのコマンドは、各形態の手順のページにあります（compose なら [Docker Compose で構築する](../compose/install.ja.md)）。

## 全体像

| あなたがやること | 何が起きているか |
|---|---|
| Control Plane のログとヘルスチェックを見る | 起動の失敗やサインインの拒否の理由は、たいていログに出ている |
| 1 人だけの問題か、全員の問題かを分ける | 全員なら配備の側、1 人ならその人のワークスペースかアカウントの側 |
| 症状の表から原因を絞り、各形態の手順で直す | |

コマンドは compose の例で、作業ディレクトリは `deploy/compose/` です。

## まず見る 3 か所

| 見るところ | コマンド |
|---|---|
| Control Plane のログ | `docker compose logs -f cp`。起動の失敗とサインインの拒否の理由は、ほぼここに出ます |
| ヘルス | `curl -s http://127.0.0.1:8099/healthz` が `ok` を返すか |
| レディネス | `curl -s http://127.0.0.1:8099/readyz` が `ok` を返すか |

> [!WARNING]
> **`/healthz` の `ok` は「製品が動いている」という意味ではありません。** 報告するのはプロセスが動いていることだけで、DB は見ません。
> API が全部 500 を返し続けている間も、`/healthz` は `ok`、ロードバランサーのターゲットは healthy のままだった例があります。
> DB まで見るのは `/readyz` です。「動かない」と言われていて `/healthz` が `ok` なら、信頼するべきは利用者の声です。

## 症状から探す

| 症状 | 確かめること |
|---|---|
| Control Plane が起動しない | `docker compose logs cp`。`/healthz` が `ok` を返すか |
| 全部 500 なのにヘルスチェックは `ok` | `/readyz` が `503` なら DB です。パスワードがローテーションされるマネージド DB では、ローテーション前から動いている Control Plane が古いパスワードを持ったままのことがあります。ログを `DB_UNAVAILABLE` と `DB_SECRET_REFRESH_FAILED` で探し、Control Plane のタスクを作り直してパスワードを読み直させます |
| docker.sock で permission denied | `DOCKER_GID` がホストの docker グループの GID と同じか（[制約 C](#コンテナから-docker-を動かす-3-つの制約)） |
| ワークスペースは起動するのにホームディレクトリが空 | `DATA_DIR` が Control Plane の内と外で同じ絶対パスか。リストアのときも同じパスか（[制約 B](#コンテナから-docker-を動かす-3-つの制約)） |
| 起動したワークスペースに届かない | Control Plane と Caddy の両方が `network_mode: host` か（[制約 A](#コンテナから-docker-を動かす-3-つの制約)） |
| ホストからは届く Control Plane の URL に、ワークスペースから届かない | 下の [ワークスペースから公開 URL に届かない](#ワークスペースから公開-url-に届かない) |
| TLS の証明書が発行されない | 下の [TLS の証明書が発行されない](#tls-の証明書が発行されない) |
| サインインできない・ボタンが出ない・外したはずの人が使える | [ログインできない](cannot-login.ja.md) |
| 起動時に `AF_WORKSPACE_DEK=random needs AF_KEY_CUSTODIAN=kms` で終わる | ホームディレクトリごとのキーには KMS のキー管理が要ります。`AF_KEY_CUSTODIAN=kms`（と `AF_KMS_KEY_ID`）を設定するか、`AF_WORKSPACE_DEK` を外します（[ホームごとに鍵を分ける](../keys/home-keys.ja.md)） |
| ワークスペースの起動が `open the home's credential-store key` で失敗する | KMS に届かないか、キーが無効か、拒否されています。master キーにフォールバックする経路はありません。KMS へのアクセスを直してから起動し直します（[ホームごとに鍵を分ける](../keys/home-keys.ja.md)） |
| リストアのあと、ワークスペースが認証情報の書き込みを拒み、ログに認証情報ストアが `unreadable` と出る | ストアがホームディレクトリのキーに移る前の状態から復元されています。Control Plane と同じ環境で `af-cp home-dek-status --remigrate <membership-id>` を実行し、ワークスペースを起動し直します（[ホームごとに鍵を分ける](../keys/home-keys.ja.md#移す前の写しを戻したとき)） |
| ワークスペースのログに `[entrypoint] WARN: npm boot-install failed (retrying next start)` と出る | CLI のインストールに失敗しました（ホームディレクトリが満杯・ネットワーク・チェックサム）。次の起動でやり直します。名簿のカードには「直近の起動で CLI のインストールに失敗しました」と出ます（[メンバーの状況を見る](../../admin/members/roster.ja.md#要対応のカードに手を打つ)） |

## コンテナから Docker を動かす 3 つの制約

Control Plane はコンテナですが、ホストの Docker を外から動かします。この方式には、破っても**エラーにならずに静かにおかしくなる**制約が 3 つあります。
compose の定義はこれらをすでに満たしているので、compose を自分で変えたときや、症状から見当を付けたいときに読みます。
Mac では A の扱いが変わります（[macOS（Apple Silicon）で動かす](../compose/macos.ja.md#重ね合わせが変えること)）。

| 制約 | 守ること | 破ったときの症状 |
|---|---|---|
| A. ホストのネットワーク | Control Plane と Caddy の両方を `network_mode: host` にする。ワークスペースはホストの `127.0.0.1:<port>` に公開されるため | 起動したワークスペースにブラウザから届かない |
| B. `DATA_DIR` の絶対パス | Control Plane の内側でも、`DATA_DIR` が同じ絶対パスになるようにマウントする。ワークスペースの `-v` はホストのパスで作られるため | ワークスペースは起動するのにホームディレクトリが空・作業が見当たらない |
| C. ユーザーと docker グループ | `user: "1000:1000"` と `group_add: <DOCKER_GID>` にする。ホームディレクトリは uid 1000 で作られ、Control Plane は docker のソケットを使うため | ソケットで permission denied になる、またはワークスペースが起動しない |

リストアのあとで B の症状が出たら、戻した先の `DATA_DIR` が元とパスで（少なくとも最後のディレクトリ名で）食い違っていないかを確かめます（[バックアップして復元する](../run/backup-restore.ja.md#復元する)）。

## ワークスペースから公開 URL に届かない

ホストからは届くのに、ワークスペースからは `PUBLIC_BASE_URL` への `curl` が拒否・リセットされます。ドキュメントの取得、ブランチ名の規則、MCP のレジストリ、
Control Plane 経由の git が一斉に失敗します。

公開 URL を受け付けるもの（トンネルやリバースプロキシ）が、Docker のブリッジからの接続を受け付けなくなっています（特定のインターフェースに bind し直したなど）。
ワークスペース専用のリスナーを設定し（`AF_CP_INTERNAL_LISTEN` を docker0 のアドレスに、`AF_CP_INTERNAL_URL=http://host.docker.internal:<port>`）、
各ワークスペースを止めて起動し直します。手順は [ワークスペースから CP に届かないとき](../compose/workspace-listener.ja.md) にあります。

## TLS の証明書が発行されない

Caddy が Let's Encrypt から証明書を取れないときは、次のどれかです。

- DNS の A・AAAA レコードがこのホストを指していない
- 80・443 番ポートに外から届かない（ファイアウォール）
- Let's Encrypt のレート制限に当たった

公開の DNS を用意できない閉じたネットワークでは、ACME を使わずに `tls internal`（自己署名）に切り替えます（[テナントとプロビジョニングの方式を決める](../install-decisions.ja.md#tls-internal-を使うか)）。

## 別の人にメールアドレスが付いていないか確かめる

`af-cp identity-email-check` は、複数のテナントで有効なメンバーシップを持つ人を**すべて**、ユーザーキー・メールアドレス・テナント・フラグ付きで並べる読み取り専用のレポートです。
`af-cp rewrap-keys` と同じ方法でメタデータのストアを開きます。

| フラグ | 意味 |
|---|---|
| `no_login` | サインインの方法が一度も結び付いていない。アドレスが本人のサインインから来ていない |
| `email_audited` | `identity.email_set`・`identity.email_change` の監査記録がある |
| `key_not_derived_from_email` | ユーザーキーが、そのアドレスから作られる形をしていない。ほかの人の招待にアドレスが付いたときの見え方 |

終了コード 0 はフラグなし、終了コード 1 はフラグの付いた人がいる、終了コード 2 はストアを読めなかったことを表します。
フラグは手がかりであって、結論ではありません。何かを変える前に、本人たちに確かめてください。

## 問い合わせを切り分ける

「使えない」と言われたら、まず**その人だけの問題か、配備全体の問題か**を分けます。

1. **ほかの人も同時に困っているか**
   - はい：配備の側を疑います。Control Plane のログとヘルスチェック、TLS、サインインの許可リスト、ホストの負荷（メモリ）を見ます。全員がサインインできないなら許可リストか IdP の設定、全員が届かないなら入口（Caddy・TLS）か制約 A です
   - いいえ：その人の側を疑います。次へ進みます
2. **その人だけの問題を分ける**
   - サインインできない：その人が許可リストに入っているか、`AF_PROVISION=invite` なら名簿に追加済みか
   - サインインはできるがワークスペースがおかしい：その人のワークスペース（`af-ws-<user>`）の状態を見ます。ホームディレクトリが空なら制約 B（ただし全員に出るはずです）。エージェントがつながらないなら、その人自身のエージェントのログインの問題で、本人が Console からログインし直します
   - Console の使いかた：[やりたいことから探す](../../user/README.ja.md) の範囲です
3. それでも分けられないときは、Control Plane のログで、その人のメールアドレス（ログでは `user_key` に置き換えて出ます）に何が起きているかを見ます

## よくある質問

### AF_MASTER_KEY をなくすとどうなる？

保存済みのすべての認証情報と、**過去のすべてのバックアップが二度と復号できなくなります**。取り戻す方法はありません。
データとは別の安全な保管場所に、別々にバックアップしておきます（[脅威モデルと残るリスクを知る](../secure/threat-model.ja.md#残るリスクに備える)）。

### バックアップには何が入る？

入るのは DB・各ユーザーのホームディレクトリ・平文の Claude の状態・Caddy の証明書です。入らないのは `shared/jvm`（取り直せます）と `AF_MASTER_KEY` です（[バックアップして復元する](../run/backup-restore.ja.md#アーカイブに入るもの)）。

### インターネットにつながらないネットワークに入れられる？

入れられます。GHCR のイメージを社内のレジストリにミラーして `REGISTRY` をそこに向けるか、`release.sh --save` と `load-images.sh` で持ち込みます。
TLS は `tls internal`、Claude は `CLAUDE_INSTALL=0` でイメージに入っているものを使います（[閉じたネットワークに入れる](../run/air-gapped.ja.md)）。
ただし、エージェントはモデルのエンドポイントに届かなければ動きません。

### ダウングレードしたい

できません。マイグレーションは前方互換で自動的に適用され、古い Control Plane は新しいスキーマを読めません。
戻すときは、古いイメージに戻すのではなく、アップグレードの前に取ったバックアップからリストアします（[アップグレードする](../run/upgrade.ja.md)）。

### docker compose down したのに、ワークスペースが残っている

正常です。ワークスペース（`af-ws-*`）は compose の管理外で、Control Plane が `docker run` で起動したものです。
止めるには管理パネルの強制停止を使うか、ホストごと止めるなら残った `af-ws-*` を別に `docker stop` します（[アイドル停止と強制停止を使う](../run/idle-stop.ja.md#ワークスペースを強制停止する)）。

### 複数のホストに分散（HA・水平スケール）できる？

1 社に 1 配備が前提です。compose では Control Plane が 1 台のホストの Docker を動かすので、ワークスペースは複数のホストに分散しません。
ecs・ecs-ec2 ではワークスペースを AWS 上に置きます（[デプロイ先を選ぶ](../choose.ja.md)）。Control Plane はどの形態でも 1 インスタンスで動き、HA の構成はありません。

### Google 以外（Microsoft 365・LDAP・SAML など）でサインインしたい

Microsoft Entra ID・Okta・Keycloak・Auth0・Cognito・GitLab は、設定だけで使えます（[サインインの方法を設定する](../signin/overview.ja.md)）。
SAML しかない IdP と LDAP は Control Plane では扱いません。oauth2-proxy・Keycloak・ALB の OIDC などのゲートウェイを前段に置き、`AUTH=proxy` でそのゲートウェイが付けるメールアドレスのヘッダーを信頼させます。

## 関連

- [ログインできない](cannot-login.ja.md)
- [配備を運用する](../README.ja.md)
- [ロール — 誰が何をできるか](../../ref/roles.ja.md)
