---
audience: "Docker Compose で配備を建てる人（ホストの端末を扱う情シス / SRE）"
source_of_truth: "`deploy/compose/` の `docker-compose.yml`・`.env.example`・スクリプト（食い違ったらこのページのバグ）"
---

# Docker Compose で構築する

[English](install.md) | 日本語

1 台の Linux ホストに Docker Compose で Agent Fleet を建て、最初の管理者がサインインできるところまでのコマンドをまとめます。
各手順で何を決めるかは [インストールする](../install.ja.md) にあります。このページは、その手順を実際に打つときに開きます。

## 全体像

| あなたがやること | 起きること |
|---|---|
| `.env` を作り、秘密・`DOCKER_GID`・公開 URL・IdP を書く | Control Plane がこの値で起動する |
| ワークスペースのイメージを用意する（ビルドか取得） | Control Plane がメンバーごとに `docker run` でワークスペースを起動できる |
| `docker compose up -d` で起動する | Control Plane（`cp`）と Caddy が動き、Caddy が TLS の証明書を取る |
| `SUPER_ADMIN_EMAILS` のアカウントでサインインする | そのアカウントが配備管理者になり、ワークスペースを起動できる |

作業するディレクトリは `deploy/compose/` です（リリースのバンドルなら、展開したディレクトリ）。

## このディレクトリにあるもの

| ファイル | 役目 |
|---|---|
| `docker-compose.yml` | `cp`（Control Plane）と `caddy`（TLS の自動取得）のサービス |
| `Caddyfile` | リバースプロキシと Let's Encrypt（ACME）。自己署名に切り替える `tls internal` もここにあります |
| `.env.example` | すべての設定。`.env` にコピーして使います |
| `backup.sh`・`restore.sh` | バックアップと復元（[バックアップして復元する](../run/backup-restore.ja.md)） |
| `load-images.sh` | ローカルの tar からイメージを読み込む。レジストリに届かないホスト専用です（[閉じたネットワークに入れる](../run/air-gapped.ja.md)） |
| `release.sh` | 版の付いたリリースのバンドルを作る。メンテナー向けです |
| `docker-compose.macos.yml` | Mac で動かすための重ね合わせ（[macOS（Apple Silicon）で動かす](macos.ja.md)） |

リリースのイメージは GHCR に公開されています。

- `ghcr.io/k-k1/agent-fleet/control-plane:<版>` と `ghcr.io/k-k1/agent-fleet/workspace:<版>` です
- `.env` の `REGISTRY` と `VERSION` で決まります。公開のパッケージなので、レジストリへのログインはいりません
- 社内のミラーから取るときは、`REGISTRY` をそこに向けます

## 始める前に

| 用意するもの | 補足 |
|---|---|
| Docker（Engine と `docker compose`）が動く Linux ホスト | Apple Silicon の Mac なら 1 人で使えます（[macOS（Apple Silicon）で動かす](macos.ja.md)） |
| 公開ドメインと、このホストを指す DNS の A / AAAA レコード | TLS に使います。社内だけなら `Caddyfile` の `tls internal` を使います |
| サインイン用の IdP のクライアント | Google の OAuth 2.0 ウェブクライアント、または Entra ID・Okta・Keycloak・Auth0・Cognito・GitLab の OIDC アプリ。登録するリダイレクト URI は、いくつ使っても 1 つです |
| エージェントの席 | 各メンバーが最初の起動のあとに Console から自分の席でログインします。個人の Pro / Max より、会社の Team / Enterprise の席をおすすめします |
| （`agy` を使うなら）RDRAND を出す CPU | 下の「Antigravity CLI と RDRAND」 |

SAML しかない IdP（HENNGE One・TrustLogin・CloudGate など）は、oauth2-proxy か Keycloak を前に置いて `AUTH=proxy` で使います。

### Antigravity CLI と RDRAND

`agy`（Antigravity CLI）は FIPS のビルドで、CPU の RDRAND 命令がないと起動の時点で止まります。`grep -w rdrand /proc/cpuinfo` で確かめます。

- カーネルが命令を取り下げているホスト（RDRAND が定数を返す AMD Ryzen Embedded など）では、Agent が OpenSSL の CPU の検出から RDRAND を外して agy を動かします。乱数は CPU ではなくカーネルから取ることになり、Console の agy のカードにそう出ます
- FIPS モジュール自身の乱数の経路が必須の配備では、`AF_AGY_RDRAND_MASK=0` を設定します。そのときは、エージェントの選択肢から `agy` が消えます

## 公開リリースから始めるとき

[配布リポジトリ](https://github.com/k-k1/agent-fleet-dist) の `install-compose.sh` が、バンドルの取得・検証・展開と、GHCR からのイメージの取得までを行います。
終わると、下の手順 1 の `cp .env.example .env` から続けられます。このページの手順は、手で（またはソースから）建てる道です。

## 1. 設定を書いてソースから起動する

```bash
cd deploy/compose
cp .env.example .env
# 1) generate secrets and put them in .env:
head -c 32 /dev/urandom | base64   # -> AF_MASTER_KEY   (store a copy in a vault!)
head -c 32 /dev/urandom | base64   # -> AF_COOKIE_SECRET
# 2) set DOCKER_GID to the host docker group:
getent group docker | cut -d: -f3  # -> DOCKER_GID
# 3) fill in PUBLIC_DOMAIN / PUBLIC_BASE_URL / your IdP (GOOGLE_OAUTH_* and/or
#    AF_OIDC_*) / AF_OAUTH_ALLOWED_DOMAINS / SUPER_ADMIN_EMAILS / DATA_DIR in .env
mkdir -p "$(grep -E '^DATA_DIR=' .env | cut -d= -f2)"

# 4) build the per-user workspace image (compose builds only the CP; the CP
#    launches workspaces from WS_IMAGE, agent-fleet/workspace:dev by default):
docker build -t agent-fleet/workspace:dev ../../workspace

docker compose up -d --build     # from source
docker compose logs -f cp
```

- `AF_MASTER_KEY` は、作ったその場でパスワード金庫に控えます。`DATA_DIR` にもバックアップにも入りません（[配備の鍵を扱う](../secure/deployment-keys.ja.md)）
- `DOCKER_GID` を間違えると、docker のソケットで permission denied になります
- 各変数の意味は `.env.example` 自体に書いてあります。何を選ぶかは [インストールする](../install.ja.md) の手順 1〜4 です

## 2. 公開リリースのイメージで起動する

ビルドせずに公開リリースを動かすときは、`REGISTRY`・`VERSION`・`WS_IMAGE` をバンドルに入っている値のままにし、手順 1 の 4) を飛ばしてイメージを取得します。

```bash
docker compose pull                                  # cp + caddy
docker pull "$(grep -E '^WS_IMAGE=' .env | cut -d= -f2)"   # workspace (see below)
docker compose up -d
```

ワークスペースのイメージは **compose のサービスではありません**。Control Plane がメンバーごとに `docker run` で起動するので、`docker compose pull` では取れません。

- 先に取っておかなくても、最初のワークスペースの起動で取得されます
- 先に取っておくと、最初に「起動」を押した人が数分待たずに済みます

## 3. サインインの IdP を書く

`.env` に書くキーと、IdP の側で作るものは IdP ごとのページにあります。

| IdP | ページ |
|---|---|
| Google | [Google でサインインする](../signin/google.ja.md) |
| Microsoft Entra ID | [Microsoft Entra ID でサインインする](../signin/entra.ja.md) |
| GitHub | [GitHub でサインインする](../signin/github.ja.md) |
| Okta・Keycloak・Auth0・Cognito・GitLab | [そのほかの OIDC でサインインする](../signin/oidc.ja.md) |

どの IdP でも、登録するリダイレクト URI は次の 1 つです。

```
https://<PUBLIC_DOMAIN>/oauth2/callback
```

リポジトリをクローンするための GitHub・Bitbucket の「OAuth で接続」は、`.env` ではなく、テナント管理者が Console で登録します（[連携に使う OAuth アプリを選ぶ](../../admin/access/oauth-apps.ja.md)）。

## 4. 最初の管理者としてサインインする

1. `https://<PUBLIC_DOMAIN>` を開きます
2. `SUPER_ADMIN_EMAILS` に書いたアカウントでサインインします
3. ワークスペースを起動します

- `SUPER_ADMIN_EMAILS` は起動のときに 1 回だけ読まれ、これだけが正です。リストから外して再起動すると、その人の配備管理者のロールも外れます
- `.env.example` は `AF_PROVISION=invite` で始まります。管理者が追加するまで、ほかの人にはワークスペースができません。追加された人は、`AF_OAUTH_ALLOWED_*` に入っていなくてもサインインできます
- テナント・メンバー・テナント専用のサインインページ（`<PUBLIC_BASE_URL>/login/<slug>`）の決め方は [テナントとプロビジョニングの方式を決める](../install-decisions.ja.md) にあります
- 人を外す方法は [アクセスを取り消す・super_admin を引き継ぐ](../secure/offboarding.ja.md) にあります

## うまくいかないとき

| 症状 | 見るところ |
|---|---|
| Control Plane が起動しない | `docker compose logs cp`。`curl -s http://127.0.0.1:8099/healthz` が `ok` を返すか |
| docker のソケットで permission denied | `DOCKER_GID` が `getent group docker` と同じか |
| サインインがいつも拒否される | 許可リストがすべて空で、まだ誰も招待していない（[ログインできない](../diagnose/cannot-login.ja.md)） |
| ワークスペースから公開 URL に届かない | [ワークスペースから CP に届かないとき](workspace-listener.ja.md) |

ほかの症状は [症状から原因を探す](../diagnose/checklist.ja.md) にあります。

## 関連

- [インストールする](../install.ja.md)
- [macOS（Apple Silicon）で動かす](macos.ja.md)
- [ワークスペースから CP に届かないとき](workspace-listener.ja.md)
- [バックアップして復元する](../run/backup-restore.ja.md)・[アップグレードする](../run/upgrade.ja.md)
- [EC2 1 台に構築する](../aws-ec2-single.ja.md)
