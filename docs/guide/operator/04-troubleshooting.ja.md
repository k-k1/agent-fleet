# 04. 障害対応と FAQ

[English](04-troubleshooting.md) | 日本語

「立ち上がったのに動かない」「ユーザーから使えないと言われた」ときの切り分けを、症状ベースで
まとめます。**復旧コマンドの正は [deploy/compose/README.md](../../../deploy/compose/README.md) の
"Troubleshooting" 節**で、ここはそれを日本語化・拡充し、診断の観点を足したものです。ログ確認や
ヘルスチェックの 1〜2 行は例外的にここにも載せます。作業ディレクトリは `deploy/compose/`。

## まず見る 2 か所

- **CP のログ**: `docker compose logs -f control-plane`（起動失敗・認証拒否の理由はほぼここ）。
- **CP のヘルス**: `curl -s http://127.0.0.1:8099/healthz` が `ok` を返すか。

## 症状別・確認表

| 症状 | 確認すること |
|------|-------------|
| CP が起動しない | `docker compose logs control-plane`。`curl -s http://127.0.0.1:8099/healthz` が `ok` を返すか |
| docker.sock で "permission denied" | `DOCKER_GID` がホストの docker グループ GID と一致しているか（DooD 制約 C）|
| Workspace は起動するのに home が空 | `DATA_DIR` が CP の内外で同一絶対パスか。リストア時も同じパスか（DooD 制約 B）|
| 起動した Workspace に到達できない | CP と Caddy が両方 `network_mode: host` か（DooD 制約 A）|
| ログインが常に拒否される | 許可リストが空（fail-closed）。`AF_OAUTH_ALLOWED_DOMAINS` / `_EMAILS` を設定 |
| TLS 証明書が発行されない | DNS A/AAAA がこのホストを指すか。80/443 が到達可能か。Let's Encrypt のレート制限 |
| redirect URI mismatch | IdP に登録した URI が `<PUBLIC_BASE_URL>/oauth2/callback` と一致しているか |
| サインインのボタンが出ない | その provider が起動時に無効化された。`docker compose logs control-plane \| grep -i "login provider"` に不足している設定名が出る |
| マルチテナント issuer を理由に CP が起動しない | Entra の `/common/` `/organizations/` issuer で `AF_OIDC_<ID>_ALLOWED_TIDS` が空。issuer を自社テナント GUID に固定する |

## DooD の 3 制約の診断（「起動するのに静かに動かない」）

CP はコンテナですが、ホストの Docker デーモンを外から駆動します（docker-out-of-docker）。この方式
には破ると**エラーを出さずに静かに壊れる** 3 つの制約があり、compose 定義が封じ込めています。
自分で compose をカスタマイズしたときや、症状から当たりをつけたいときはここを見ます。仕組みの
背景は [dev/09](../../dev/09-deploy.md)。

- **(A) host ネットワーク** — CP はワークスペースをホストデーモン経由で `127.0.0.1:<port>` に
  publish するので、ホストの loopback を共有していないと到達できません。CP と Caddy の両方が
  `network_mode: host` である必要があります。**症状: 起動済みの Workspace にブラウザから繋がらない。**
- **(B) `DATA_DIR` の同一絶対パス bind** — CP はホストパスをホストデーモンに渡して Workspace の
  `-v` マウントを作るので、`DATA_DIR` は CP の内側でも同じ絶対パスに解決されなければなりません。
  ずれると**空の home がマウント**されます。**症状: Workspace は起動するのに home が空・作業が
  見当たらない。**リストア後にこの症状が出たら、復元先の `DATA_DIR` が元とパス（少なくとも
  basename）で食い違っていないか確認します（[02](02-operations.ja.md)）。
- **(C) `user: "1000:1000"` + `group_add: <DOCKER_GID>`** — home は uid 1000（Workspace の `dev`
  ユーザー）所有で作られ、CP は docker ソケットを使うためにホストの docker グループが要ります。
  `DOCKER_GID` が違うと**ソケットで permission denied**。**症状: Workspace を起動しようとすると
  permission denied、または起動そのものが失敗。**

## ログインできない

- **常に拒否される** → 許可リスト（`AF_OAUTH_ALLOWED_EMAILS` / `_DOMAINS` / `_EMAILS_FILE`）が
  **すべて空だと全拒否**です（fail-closed = 安全側に倒す設計）。少なくとも 1 つ設定します。
  `_EMAILS_FILE` はログインごとに再読込されるので追加は再起動不要です。
- **redirect URI mismatch** → IdP 側（Google Cloud Console、Entra のアプリ登録など）に登録した
  承認済みリダイレクト URI が `<PUBLIC_BASE_URL>/oauth2/callback` と**完全一致**しているか。
  `PUBLIC_BASE_URL` を変えたら IdP 側も合わせます（[01 §3](01-install.ja.md)）。有効にする provider が
  何個でも、この URI は 1 本だけです。
- **設定したはずのサインインボタンがログイン画面に出ない** → 設定が不完全なため起動時に無効化
  されています（1 つの IdP の設定ミスで全員が締め出されないための挙動）。
  `docker compose logs control-plane | grep -i "login provider"` に不足している変数名が出ます。
  多いのは、既定値を持たない `AF_OIDC_<ID>_TRUST` の未設定です。
- **マルチテナント issuer を理由に CP が終了する** → Entra ID の issuer が `/common/` または
  `/organizations/` で `AF_OIDC_<ID>_ALLOWED_TIDS` が空です。これらのエンドポイントでは Microsoft
  アカウントを持つ全人類がログイン画面に立て、個人アカウントは自分の email を付け替えられるため、
  許可リストが意味を失います。issuer を自社テナント GUID
  （`https://login.microsoftonline.com/<tenant-guid>/v2.0`）に固定するか、受け入れるテナントを
  列挙してください。
- **cookie が保存されない/ログイン直後に戻される** → `AUTH=oauth` は Secure cookie を使うため
  **HTTPS 必須**です。素の HTTP（TLS 未終端）では保存されません。`PUBLIC_BASE_URL` が `https://` か、
  TLS が実際に発行されているか（下記）を確認します。

## TLS が発行されない

Caddy が Let's Encrypt から証明書を取れないときの定番は、DNS の A/AAAA がこのホストを指していない、
80/443 が外部から到達できない（ファイアウォール）、Let's Encrypt のレート制限に当たった、の 3 つ
です。閉域網など公開 DNS を用意できない環境では、そもそも ACME を使わず `tls internal`（自己署名）へ
切り替えます（[01 §4](01-install.ja.md)）。

## ユーザー問い合わせの切り分けフロー

「使えない」と言われたら、まず **member 個別の問題か、CP/デプロイ全体の問題か**を切り分けます。

1. **他のユーザーも同時に困っているか？**
   - はい → **CP/デプロイ側**を疑う。CP のログとヘルス、TLS、ログイン許可リスト、ホストの負荷
     （メモリ）を確認。全員がログインできないなら許可リストや OAuth 設定、全員が繋がらないなら
     入口（Caddy/TLS）や DooD (A)。
   - いいえ（その人だけ）→ **member 個別**を疑う。次へ。
2. **その人だけの問題の切り分け:**
   - ログインできない → その人が許可リストに入っているか、`AF_PROVISION=invite` なら追加済みか。
   - ログインはできるが Workspace が変 → その人の Workspace（`af-ws-<user>`）の状態。home が空なら
     DooD (B)（ただし全員に出るはず）、Claude が繋がらないならその人自身の Claude ログイン（BYO）
     の問題で、運用者ではなく本人が Console から再ログインします。
   - Console の操作方法そのもの → member 分冊 / lite 分冊の範囲（運用者の対応外）。
3. どうしても切り分かないときは、CP のログにそのユーザーのメール（sanitize 済みの `user_key`）で
   何が起きているかが出ます。

## FAQ（例外系・よくある疑問）

**Q. `AF_MASTER_KEY` を無くすとどうなる？**
A. 保存済みの全資格情報と、**すべての過去バックアップが永久に復号不能**になります（crypto-shred）。
復旧手段はありません。だからこそデータとは別の金庫に、独立してバックアップします（[03](03-security.ja.md)）。

**Q. バックアップに何が入って、何が入らない？**
A. 入るのは DB・各ユーザーの home・平文の Claude 状態・Caddy 証明書。入らないのは `shared/jvm`
（再取得可能）と **`AF_MASTER_KEY`**。詳細は [02](02-operations.ja.md)。

**Q. Workspace は起動するのに home が空。**
A. ほぼ DooD 制約 (B)。`DATA_DIR` が CP の内外で同一絶対パスか、リストア時に basename が一致して
いるかを確認します（本書の DooD 診断）。

**Q. 閉域網（インターネット非接続）に入れられる？**
A. 入れられますが、条件があります。リリースに image tar は添付しなくなったので、GHCR の image を
社内レジストリへミラーして `REGISTRY` をそこへ向けるか、`release.sh --save` ＋ `load-images.sh` で
持ち込みます。TLS は `tls internal`、Claude は `CLAUDE_INSTALL=0` で焼き込み image を使います
（[02](02-operations.ja.md) の air-gap）。なお、フリートは起動できてもエージェント自身は
モデルのエンドポイントに到達できなければ動きません。

**Q. ダウングレードしたい。**
A. 非対応です。migration は前方互換で自動適用され、古い CP は新スキーマを理解できません。後退は
「古い image に戻す」ではなく「アップグレード前に取ったバックアップからリストアする」で行います
（[02](02-operations.ja.md)）。

**Q. `docker compose down` したのに Workspace が残っている。**
A. 正常です。Workspace（`af-ws-*`）は compose 管理外で、CP が `docker run` で起こしたものです。
確実に止めるには Admin パネルの force-stop、またはホスト全体を落とすなら残る `af-ws-*` を別途
`docker stop` します（[02](02-operations.ja.md)）。

**Q. 複数ホストに分散（HA・水平スケール）できる？**
A. 提供モデルは 1 社 = 1 デプロイ = 1 ホストです。CP はホストの Docker デーモンを駆動する前提で、
複数ホストへの分散や HA 構成は現行の対象外です。大規模化の設計方向は [dev/09](../../dev/09-deploy.md)
（aws ターゲットは実装済みだが実運用実績なし）を参照してください。

**Q. Google 以外の認証（Microsoft 365 / LDAP / SAML など）を使いたい。**
A. CP ネイティブ（`AUTH=oauth`）は OIDC を話すので、**Microsoft Entra ID・Okta・Keycloak・Auth0・
Cognito・GitLab は設定だけで使えます** — `AF_OIDC_PROVIDERS` と数個の `AF_OIDC_<ID>_*`、そして
IdP 側にリダイレクト URI を 1 本（[01 §3](01-install.ja.md)）。同時に複数有効化でき、その場合は
ログイン画面に provider の数だけボタンが並びます。
SAML のみの IdP（HENNGE One / TrustLogin / CloudGate など）と LDAP は CP には実装していません。
既存のゲートウェイ（oauth2-proxy / Keycloak / ALB OIDC）を前段に置き、`AUTH=proxy` で上流のメール
ヘッダを信頼させてください（[dev/07 §7.3](../../dev/07-security.md)）。
