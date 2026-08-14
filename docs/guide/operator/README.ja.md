# operator — セルフホスト運用者ガイド

[English](README.md) | 日本語

このガイドは、Agent Fleet を自社インフラに**デプロイし、運用し、守る**人（情報システム部門・SRE
など、ホスト OS への SSH と `super_admin` 権限を持つ担当者）向けです。開発フローの知識は前提と
しません。必要なのは Docker・DNS・OAuth・バックアップ運用の一般的な素養です。

「壊れたとき自分しか直せない」立場に向けて、**何を・なぜ・どの判断で**を日本語で説明します。
一方、**実際のコマンド手順の正は [deploy/compose/README.md](../../../deploy/compose/README.md)（英語 runbook）**
です。このガイドはコマンドを複製せず、「どの節を見ればよいか」へ誘導します。内部の仕組みを
知りたいときは開発者向け [dev/09 デプロイ](../../dev/09-deploy.md) と [dev/07 セキュリティ](../../dev/07-security.md)
を参照してください（リンクは guide → dev の一方向）。

## この分冊の構成

| ファイル | 内容 |
|----------|------|
| [README.md](README.ja.md)（本書）| 全体像・構成要素・運用者の責務・導入検討者向けの要約 |
| [01-install.md](01-install.ja.md) | 初期構築（秘密の生成・OAuth 設定・起動・初回ログイン・最初のテナント）|
| [02-operations.md](02-operations.ja.md) | 日常運用（バックアップ・リストア・アップグレード・閉域・停止）|
| [03-security.md](03-security.ja.md) | セキュリティ運用（脅威モデル・残存リスク・egress 統制・報告窓口）|
| [04-troubleshooting.md](04-troubleshooting.ja.md) | 障害対応と FAQ（DooD 3制約の診断・切り分けフロー）|

---

## 導入を検討している方へ

「何ができて、何が必要で、セキュリティ姿勢はどうか」をここに要約します。技術評価の入口として
まずこの節を読んでください。

### できること

- 社内メンバーが **ブラウザから** Claude Code などの CLI コーディングエージェントを使えます。
  各自が隔離された環境（**Workspace** = 専用コンテナ）を持ち、リポジトリを clone して AI
  セッションや shell を起動・操作します。ターミナルに不慣れなメンバー向けのチャット中心の
  使い方もあります。
- 管理者（`super_admin` / `tenant_admin`）は、メンバーの追加、資源上限（メモリ・アイドル停止）、
  利用量の可視化、監査ログの閲覧、通信先の観測をブラウザの Admin パネルから行えます。
- 部署ごとに**テナント**を分ければ、Workspace は互いに完全分離されます（既定は単一テナント）。

### 前提（導入に必要なもの）

- **Docker が動く Linux ホスト**（Docker Engine + `docker compose`）。1 台で完結します。
- **公開ドメイン**と、そのホストを指す DNS の A/AAAA レコード（自動 TLS のため）。
  社内限定・公開 DNS を用意できない場合は自己署名 TLS の代替手段があります（[01](01-install.ja.md) 参照）。
- **ログイン用 IdP のクライアント**。Google OAuth 2.0 の Web クライアント、または
  Microsoft Entra ID / Okta / Keycloak / Auth0 / Cognito / GitLab の OIDC アプリ登録。
  どの IdP を何個有効にしても、登録するリダイレクト URI は 1 本だけです。
- チームの **Claude シートは各自が持ち込み**（BYO）。初回起動後、各メンバーが Console から
  自分のシートでログインします。個人の Pro/Max より会社の Team/Enterprise シートを推奨します。

### 提供モデルとセキュリティ姿勢（要約）

- **1 社 = 1 デプロイ**。各社が自社インフラに独立したインスタンスを立てます。会社間の分離は
  「プロセス内の境界」ではなく「**別デプロイ**」で担保します。これにより、万一の侵害の影響範囲
  （blast radius）は**そのデプロイ 1 つの内側に限定**されます。
- Workspace の中では AI エージェントが**任意コードを実行**する前提で境界を設計しています。守る
  対象は「他ユーザーのデータ・CP/ホスト基盤・シークレット・情報持ち出し」です。
- 正直に開示すべき残存リスクが 4 点あります（`docker.sock` = ホスト root 相当、`AF_MASTER_KEY`
  紛失 = crypto-shred、バックアップの機微性、ホストアクセス = 全権）。詳細は [03-security.md](03-security.ja.md)
  と対外向け [SECURITY.md](../../../SECURITY.md) にまとめてあります。導入判断の前に必ず目を通して
  ください。

より広い全体像はプロジェクトの [README](../../../README.md) と [dev/01 アーキテクチャ](../../dev/01-architecture.md)
にあります。

---

## 構成要素（運用者が把握すべき最小モデル）

1 台のホスト上で、`docker compose` が **2 つのサービス**を管理します。

- **Control Plane（CP）** — 頭脳。ログイン認証、テナント/メンバー管理、Workspace の起動・停止、
  すべての API 中継を担います。CP はコンテナですが、**ホストの Docker デーモンを外から駆動して**
  Workspace コンテナを起こします（この方式を DooD = docker-out-of-docker と呼びます）。
- **Caddy** — 入口（reverse proxy）。公開ドメインの TLS を Let's Encrypt から自動取得・更新し、
  背後の CP へ転送します。

一方、**ユーザーの Workspace コンテナ（`af-ws-<user>`）は compose の管理対象ではありません**。
CP が実行時に `docker run` で起こします。これは運用上とても重要な性質です。

- `docker compose down` や CP の再起動で **Workspace は止まりません**（ユーザーは切断されない）。
- バックアップ時に CP を一瞬止めても、稼働中の Workspace は影響を受けません。
- 一方で「compose を止めれば全部止まる」わけではないので、力業で全 Workspace を止めたいときは
  別の操作が要ります（[02](02-operations.ja.md) の force-stop）。

永続データは**すべて `DATA_DIR`（既定 `/srv/agent-fleet/data`）の下**にあります。DB・各ユーザーの
home・封筒暗号された資格情報・Caddy の証明書。バックアップはこのディレクトリを対象にします。
唯一の例外が `AF_MASTER_KEY` で、これは `DATA_DIR` にもバックアップにも**入りません**（後述）。

DooD 方式には破ると静かに壊れる 3 つの制約（host ネットワーク・`DATA_DIR` 同一絶対パス・docker
グループ GID）があります。compose 定義がこれを封じ込めていますが、意味は [04](04-troubleshooting.ja.md)
で診断の観点から説明します。仕組みの背景は [dev/09](../../dev/09-deploy.md) にあります。

## 運用者の責務チェックリスト

- [ ] `AF_MASTER_KEY` を**データとは別の金庫**に保管し、独立してバックアップした（紛失 = 全資格
      情報が永久に復号不能）。
- [ ] `backup.sh` を cron で定期実行し、アーカイブの保管先を権限・暗号化で保護している。
- [ ] リストア手順を一度は実地で試し、`DATA_DIR` の basename 制約を理解している。
- [ ] アップグレード前に必ずバックアップを取る運用にしている（**ダウングレード不可**）。
- [ ] ログイン許可リスト（`AF_OAUTH_ALLOWED_*`）を適切に設定した（空 = 全拒否の fail-closed）。
- [ ] Entra ID でサインインする場合、`AF_OIDC_<ID>_ISSUER` を自社のテナント GUID に固定した
      （`/common/` だと Microsoft アカウントを持つ全人類がログイン画面に立つ）。
- [ ] ホスト OS への SSH・sudo・docker 実行権限を持つ人を最小限に絞っている（= ホスト root 相当）。
- [ ] egress 統制を入れる場合、log-only で観測してから enforce へ段階的に進める方針を理解している。
- [ ] 脆弱性を見つけたときの報告手順（[SECURITY.md](../../../SECURITY.md)）を把握している。
