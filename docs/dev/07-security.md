# 07. セキュリティ — 脅威モデル・認証・暗号・監査

> 正: コード（本書は境界と設計意図）/ 主な更新トリガ: 認証モード・暗号・隔離・監査・egress に触る変更 / 最終確認: 2026-07

## 7.1 脅威モデルと信頼境界

各 Workspace では CLI エージェントが**任意コードを実行**する（`--dangerously-skip-permissions` 運用を含む）。
「ユーザーのセッションが untrusted コードを動かす」前提で境界を設計する。守るのは
**他ユーザーのデータ / CP・ホスト基盤 / シークレット / 情報持ち出し**。

この「承認を全部スキップして起動する」は **2026-08 以降は既定であって固定ではない**——利用者が
kind 毎／セッション毎にオフにできる（[76](../76-tool-permission-choice.md) / [ADR 0056](../decisions/0056-tool-permission-choice.md)）。
ただし**境界の設計は変えない**: オフにできるのは 5 kind だけで、TUI 内でモードを戻すこともでき、
CLI 自身の設定経路まで塞いではいない。つまり承認確認は**事故を減らす手当**であって隔離境界では
なく、依然としてコンテナ境界が唯一の砦である。

```
信頼度 低 ┌──────────────────────────────┐
          │ Workspace コンテナ内部        │ ← 任意コード実行を許容する領域
          └──────────────┬───────────────┘
                         │ 主要な隔離境界
信頼度 高 ┌──────────────▼───────────────┐
          │ Control Plane / ホスト基盤    │ ← Workspace から侵害されてはならない
          └──────────────────────────────┘
```

**前提の限界（正直に明記）**: 1 デプロイ内では CP が `docker.sock`（ホスト root 相当）を持ち平文 DEK を
注入するため、**CP/ホストが侵害されるとそのデプロイ内の分離は一括で破れる**。会社間は別デプロイゆえ
波及しない——これが提供モデルの強み（[decisions/0001](../decisions/0001-self-host-vs-saas.md)）。
緩和候補: rootless Docker / socket-proxy / CP 最小権限（別ワーク・未着手）。

## 7.2 隔離コントロール（local / aws 両対応）

| 対象 | local（Docker・既定） | aws（ECS）🚧 |
|------|----------------------|--------------|
| ユーザー間ファイル | per-membership home を bind mount（`<WS_DATA>/<user>/home`）。他ユーザーの home はマウントされない | EFS アクセスポイントで root dir と uid/gid を固定 |
| プロセス / メモリ | 1 membership = 1 コンテナ。`--memory`（`WS_MEMORY` 既定 1g）| タスク分離（Fargate はホスト共有なし）|
| ネットワーク | 専用 network `af-net-<user>` で相互到達を遮断。Agent はホスト `127.0.0.1` publish 経由で CP のみ到達。egress は NAT 維持（統制は §7.8）| SG / サブネット。IMDS 遮断・Task Role 最小化 |
| コンテナ → ホスト | 非特権コンテナ・capability 最小化 | Fargate ならホスト共有なし |
| 機微状態の退避 | claude の平文状態は 2nd mount `<dataDir>/claude-config:/var/lib/af/claude` + `CLAUDE_CONFIG_DIR` 注入で**ファイルブラウザの範囲外**へ。暗号化済み `secrets.enc` は home 据置 + denylist | 同左（イメージ・Agent は共通）|

限界: 同一 uid のコンテナ内 shell から本人の BYO トークンを完全不可視にはできない（原理的に不可）。
ブラウザ不可視 + at-rest 暗号 + env 注入で実用十分とする設計判断。

## 7.3 L1 Console 認証（AUTH 3 モード）

`AUTH` env で分岐。いずれも解決した email を sanitize（小文字化・非英数→`-`・40 字上限）して
`identity.user_key` にする。

| モード | 仕組み | 用途 |
|--------|--------|------|
| `oauth`（実運用の既定）| **CP ネイティブ OIDC ログイン**（Google ＋ 任意の OIDC IdP・§7.3.1）。`/oauth2/{login,callback,logout}` + `/login` を CP が所有。ログイン成功で署名 cookie（HMAC-SHA256・`AF_COOKIE_SECRET`・HttpOnly/Secure/Lax・TTL `AF_SESSION_TTL` 既定 168h）発行 | セルフホスト本命。HTTPS 前提（エッジは Caddy/Funnel）|
| `proxy` | 外部ゲートウェイ（oauth2-proxy / ALB OIDC）の `X-Forwarded-Email`（`AUTH_EMAIL_HEADER` で変更可）を信頼。**ヘッダ欠落は 401**（フォールバック無し）。CP は loopback 束縛前提 | ALB OIDC（aws）/ 既存ゲート流用。**SAML IdP（HENNGE One / TrustLogin / CloudGate 等）の正式な答えもこれ** — oauth2-proxy / Keycloak でブリッジする（[61](../61-login-idp.md) 決定 10）|
| `dev` | 固定 `DEV_USER`（既定 `dev`）| ローカル開発のみ。`AUTH=oauth` は素の HTTP では Secure cookie が保存されず使えない |

**authGate の要点**（`oauth` モード）:
- 全リクエストを検査し、**受信した `X-Forwarded-Email` を必ず削除**してから検証済み email を自ら注入
  （エッジがヘッダ素通しでも成りすまし不可）。以降の identity/membership 解決は `proxy` と共通経路。
- 除外パス（`routes.go` の `exemptExact`/`exemptPrefix` 宣言が正）: ログイン導線と死活の
  `/oauth2/*`・`/login`・`/healthz`・`/brand/*`、自前認証を持つ `/mcp`・`/mcp/*`（Bearer PAT）・
  `/git/*`（Basic git token）・`/internal/*`（デプロイ内部の Bearer トークン: egress ingest /
  schedule bridge / mcp-servers poll）、旧パス互換リダイレクトの `/agent-fleet[/…]`。
- 許可リスト 3 系統の併用可: `AF_OAUTH_ALLOWED_EMAILS`（CSV）/ `AF_OAUTH_ALLOWED_DOMAINS`（CSV）/
  `AF_OAUTH_ALLOWED_EMAILS_FILE`（1 行 = メール or `@domain`・ログイン毎に再読込＝**追加は再起動不要**）。
  provider ごとに `AF_OIDC_<ID>_ALLOWED_{EMAILS,DOMAINS}` を置くと、その provider ではデプロイ共通
  リストの**代わりに**それが使われる（per-provider の絞り込み）。
- ★ **入口の判定は email 軸の「和」**（[61](../61-login-idp.md) §61.9.6・P3）:

  ```
  ( provider 固有リスト | デプロイ共通リスト )  ∪  ( tenant.auto_join_domains | membership 保有 )
  ```

  後半 2 項が DB 由来。**招待された人は `AF_OAUTH_ALLOWED_*` に載っていなくても入口を通る**ので、
  招待運用のデプロイは名簿を membership 1 箇所に寄せられる。
  **すべて空（許可リストも membership も auto_join も無い）なら全拒否（fail-closed）は維持**。
  和を取るのは **email 軸の中だけ**で、種類の違う判定（GitHub の org・下記②の①側）は AND のまま
  — さもないと membership を持つだけで org 判定を迂回できる。
  参照は `tenant` テーブル ＋ **30 秒 TTL のメモリキャッシュ**（管理 API の書き込みで破棄）。
- **判定はログイン時だけでなく毎リクエスト**（許可リストから消す／membership を無効化する＝
  オフボーディング経路。セッション cookie の TTL を待たずに次のリクエストで締め出される）。
  セッション cookie は stateless なので**個別失効は無く**、全セッションを即時に切る唯一の手段は
  `AF_COOKIE_SECRET` のローテーション（[ADR0043](../decisions/0043-login-idp.md) 決定 27）。
- ★ **テナントの門は authGate に置かない**（決定 13）。`authGate` はテナントを知らない
  （`X-AF-Tenant` を読むのはその先）ので、テナント規則を持ち込むと「どのテナントで判定するか」が
  決まらない。テナント側の判定（`tenant.allowed_providers` とセッションの `prov` の突合）は
  `resolveFull` / `resolveMembership` で行い、外れたら **`provider_required`** を返して
  Console から再サインインへ誘導する（403 で終わらせない）。
- **`SUPER_ADMIN_EMAILS` は起動時に 1 度だけ読み、それが唯一の正**（決定 24）。
  `UpsertIdentity` の roleHint は upgrade-only のまま、**起動時に一括で降格**する
  （リストに無い `super_admin` を `user` へ。email が空の identity は env で名指せないので対象外）。
  ログイン時同期にしないのは、**退職者は二度とログインしない**ため。

### 7.3.1 ログイン IdP（`oauth` モード）

Google 固定ではなく**汎用 OIDC クライアント 1 本**で、Entra ID / Okta / Keycloak / Auth0 /
Cognito / GitLab が設定だけで載る（[61](../61-login-idp.md) P0 + [ADR0043](../decisions/0043-login-idp.md)）。
Google も同実装の 1 インスタンスで、**env 名（`GOOGLE_OAUTH_*`）は据え置き**＝既存デプロイは無変更。

- `AF_OIDC_PROVIDERS`（CSV）＋ `AF_OIDC_<ID>_{ISSUER,CLIENT_ID,CLIENT_SECRET,TRUST,LABEL_JA,
  LABEL_EN,SCOPES,PROMPT,ALLOWED_EMAILS,ALLOWED_DOMAINS,ALLOWED_TIDS}`。
  ログイン画面には有効な provider の数だけボタンが出る（1 つなら現行と同じ見た目）。
- **redirect_uri は `/oauth2/callback` の 1 本のまま**。どの provider かは署名済み state cookie で
  運び、コールバックでは**設定済みの集合と突合してから**分岐する（決定 8）。
- **`TRUST` に既定値を置かない**（§61.4）: `email_verified`（IdP が検証済みと言う場合のみ受理）/
  `issuer`（issuer が単一テナントに固定済み。**Entra ID は `email_verified` を出さない**のでこちら）。
  宣言の無い provider は起動時に無効化される（fail-closed）。
- ★ **issuer が `common` / `organizations` / `consumers` で `ALLOWED_TIDS` が空なら起動を止める**
  （決定 7）。許すと「Microsoft アカウントを持つ全人類」が入口に立ち、個人 MSA は email を
  付け替えられるので email 許可リストが無意味になる。
- **1 つの IdP の設定ミスで全員を締め出さない**（決定 11）: 個々の provider は設定不足なら
  無効化＋警告で、**有効な provider がゼロのときだけ fatal**。
- セッション cookie には `prov` / `sub` が入る（JSON なので旧 cookie は欠損フィールドとして読め、
  移行時のログアウトは不要）。毎リクエストの再判定はその provider の許可判定を呼ぶ。
- **id_token の署名検証は行わない**（決定 9）。認可コードフロー・client_secret 付き・トークン
  エンドポイントから TLS 直受けのため（OIDC Core §3.1.3.7 の注記と同じ論拠）。userinfo に出ない
  `tid` は同一レスポンス内の id_token ペイロードから読む。**フロントチャネル（implicit /
  form_post）で id_token を受ける経路を足すなら JWKS 検証が必須**。JWT ライブラリ依存はゼロ。

**GitHub だけは専用アダプタ**（[61](../61-login-idp.md) §61.7 P2）。OIDC ではないので上の 1 本には
載らない。許可は**独立した 2 つの門の AND**:

- ①**org メンバーシップ**（必須）: `GET /user/memberships/orgs/{org}` が `active`。
  `AF_GITHUB_ALLOWED_ORGS` が空なら provider ごと無効化する（決定 2 — メンバーシップ判定と
  セットでのみ採用した入口）。**この env が GitHub ログインを有効にする合図**でもある
  （`GITHUB_OAUTH_CLIENT_ID` 単体ではログインを有効にしない。★ 歴史的には「git 連携の
  device flow が先に使っていた env だから」だったが、[71](../71-tenant-git-oauth.md) で
  git 側はテナントの行を読むようになったので、この env は**サインイン専用**になった。
  合図が org 一覧である理由は今も同じ——許可を与えているのは org メンバーシップである）。
- ②**email 許可リスト**: `AF_GITHUB_ALLOWED_{EMAILS,DOMAINS}` → 無ければデプロイ共通 →
  どちらも未設定なら email の門は無し（org が許可リストそのもの）。P3 の DB 由来の項
  （auto_join / membership）は**この②にだけ**足される — ①は別軸なので AND のまま。
  email はアカウントの
  **`primary && verified`** のみを `GET /user/emails` から採る（`GET /user` の `email` は検証
  フラグを持たず、非公開設定で `null`）。`subject` は**数値 id**（`login` は改名できる）。
- 毎リクエスト再判定は API 呼び出しになるため、`(provider, subject)` キーの TTL キャッシュ
  （既定 10 分・`AF_GITHUB_MEMBERSHIP_TTL`）で間引き、GitHub 到達不能時は**最後の肯定結果を
  猶予（既定 1 時間・`AF_GITHUB_MEMBERSHIP_GRACE`）だけ延命**して超えたら拒否する（決定 12）。
- ★ **access token はプロセス内メモリにしか置かない**（cookie に載せれば XSS で漏れる）。
  したがって **CP 再起動で判定材料が消える**。その人は org のメンバーのままなので
  `forbidden` ではなく **`reauth`（再ログイン要求・API には 401）** を返す。fail-closed は
  保ったまま、事実と違う「許可されていません」を出さないための区別。

**テナント定義のサインイン方法**（[61](../61-login-idp.md) §61.11 P4）。子会社ごとに Entra が違う
場合に、IdP の定義そのものをテナントが持てる（`tenant_idp`）。env の provider と決定的に違うのは
**誰が有効化するか**で、そこが安全性の全体を支えている:

- **書くのは tenant_admin、`active` にできるのは super_admin だけ**（決定 30）。IdP の登録は
  「誰であるかを宣言する」権限で、`user_key` もデプロイ役割も **email をキーにデプロイ全体で 1 つ**
  なので、自分の支配下の IdP を単独で有効化できると情シスの email を名乗るトークンを自分で発行できる。
  `trust: issuer` は防波堤にならない（issuer が攻撃者自身）。悪意が無くても、セルフサインアップ有効な
  Auth0 テナントを善意で登録した瞬間にデプロイ全体が開く。
- 承認前は**ログイン画面にボタンも出ず、callback もセッションも通らない**（レジストリが active 行しか
  返さない）。`issuer` / `client_id` / `trust` の変更、許可ドメイン・tid の**拡大**で `pending` へ戻る。
  停止（`suspended`）は tenant_admin も打てる。承認と停止は監査に残る（`tenant_idp.*`）。
- **provider id は `t:<tenant-slug>:<name>`** と名前空間を分ける（決定 33）。混ぜるとテナントが
  `google` という行を作って env の Google を上書きできる。
- **デプロイ役割は取れない**（決定 31）: `roleHintFor` はテナント定義 provider のログインでは効かない。
  `UpsertIdentity` が never downgrade である以上、ここを抜かれると不正 provider を消しても role が残る。
- **email 一致で既存 identity へ結合しない**（決定 32）。一度もサインインされていない identity
  （招待の placeholder）だけを claim し、ログイン実績のあるアドレスは**拒否**する。
- **入口の門は行の `allowed_domains`（必須）だけ**で、デプロイ共通の許可リストにも他テナントの
  名簿にもフォールバックしない。ドメインは**1 ドメイン 1 テナント**で、これがその issuer の
  名乗ってよいアドレスの範囲を縛る。`allowed_tids` は issuer が `common` / `organizations` のとき必須。
- **セッションは自テナントにしか入れない**（`t:<slug>:` の prefix をテナント解決時に突合）。
- `client_secret` は**テナント鍵で封印して DB**（`custodian.Wrap`・AES-256-GCM・AAD=keyRef。
  `mcp_server.headers_enc` と同形）。UI へは返さず、更新時に空なら既存値を維持し、
  **復号不能は空にせず明示エラー**。★ 秘密が `DATA_DIR` に入るので、**`AF_MASTER_KEY` を
  データ領域の外に置く**既存ルールの重みが増す（§7.6・バックアップ運用）。

認可は [05 §5.4](05-api-contracts.md): 自分のリソースのみ + membership 検証、admin は role gate
（super_admin=デプロイ全体 / tenant_admin=自テナント）。role の階層は `identity.role` と
`membership.role` の 2 段（[06 §6.2](06-data-model.md)）。

### 7.3.2 テナントの接続元制限（`allowed_cidrs`）

「誰か」の次に「どこから」を見る門（[docs/66](../66-tenant-network-restriction.md)・
[ADR 0047](../decisions/0047-tenant-network-restriction.md)）。テナント管理者が Console から
CIDR を並べると、そのテナントの解決経路（`resolveFull` / `resolveMembership`）で照合される。

⚠️ **これはネットワーク防御ではない。** 要求は ALB を通り CP に届き、セッションが検証された
**あとで** 403 になる。認証前の脆弱性・DoS・探索には効かない（そこは `AlbIngressCidr` と WAF）。
守れるのは「資格情報を持った人が、許されていない場所からデータに触る」だけである。

- **送信元 IP は `AF_TRUSTED_PROXY_HOPS`（既定 0）で決まる。** 0 = `RemoteAddr`、
  N = `X-Forwarded-For` を**右から N 番目**。XFF は誰でも付けられるが、信頼するホップは
  その**右に**追記するので、右から数える限り偽装が効かない。**左端を読む実装だけが危険**。
  読むのは最外周ミドルウェア 1 箇所だけ（`authGate` が識別ヘッダを `Del` するのと同じ理由）。
- **`/mcp` と `/git/*` は対象外。** 送信元が本人の Workspace コンテナであり、人の所在を
  表さない。入れると自テナントのワークスペースからの MCP と git を全部塞ぐ。
- **締め出しの逃げ道**: super_admin は対象外／保存時に編集者の現在 IP を必ず通す／
  プロキシ未申告（`hops=0` なのに XFF 到着）は**保存そのものを拒否**する。

## 7.4 L2 エージェント認証との分離

L2（Claude/codex/opencode を誰として動かすか）はユーザー本人の OAuth で、CP は関与しない
（可視化と接続 UI のみ）。フローと保存先は [08](08-integrations.md)。Workspace を跨いだ認証情報の
共有は設計上禁止（home 分離がそのまま境界）。

## 7.5 CP ↔ Agent 認証

- per-container の `AGENT_TOKEN` を CP が起動時に `-e` 注入し DB に永続（[06](06-data-model.md)）。
  CP 再起動時は既存コンテナから inspect で採用（再作成しない）。
- CP の全中継（REST/SSE/WS/preview）と Bitbucket callback が `Authorization: Bearer` を付与。
  Agent の `requireToken` が `/healthz` 以外を**定数時間比較**で検証（未設定時は dev 用に開放）。
- ネットワーク分離（§7.2）との多層防御。

## 7.6 シークレット管理と封筒暗号

**原則: 秘密はユーザー領域に閉じ、CP は平文を保持・解釈しない。ログに秘密を出さない。**

| シークレット | 保管 | 露出範囲 |
|-------------|------|----------|
| git 資格情報（GitHub PAT/Device、Bitbucket OAuth/token）・opencode env キー | Workspace home の **`secrets.enc`**（AES-256-GCM・0600）| 当該ユーザーのみ。統一 cred helper（`workspace-agent cred`）が都度復号して出力＝**平文ファイルを作らない** |
| Claude `.credentials.json`（claude 自身が書く）| `CLAUDE_CONFIG_DIR`（home 外・browse 範囲外, §7.2）| 当該ユーザーのみ |
| システム秘密（ログイン IdP の client secret＝`GOOGLE_OAUTH_CLIENT_SECRET` / `AF_OIDC_<ID>_CLIENT_SECRET` / `GITHUB_OAUTH_CLIENT_SECRET`・`AF_MASTER_KEY`・`AF_COOKIE_SECRET`）| `oauth.env` / compose `.env`（git 管理外）。aws=Secrets Manager/SSM 🚧 | CP のみ。`AF_MASTER_KEY` はデータ領域の外で保管（バックアップに含めない＝失えば crypto-shred）|
| GitHub ログイン中の人の access token（org 再判定用・§7.3.1）| **CP プロセス内メモリのみ**（永続化しない）| CP のみ。再起動で消え、その人は再ログインを求められる |
| PAT | DB に SHA-256 ハッシュのみ（[06](06-data-model.md)）| 平文は発行時 1 回だけ表示 |

**封筒暗号 + custodian 抽象**（[decisions/0005](../decisions/0005-envelope-custodian.md)）:
- per-workspace **DEK** を per-tenant **KEK** で wrap し `wrapped_dek` に保存。CP が Workspace 起動時に
  unwrap して `AF_SECRET_KEY` としてコンテナへ注入（Agent は暗号方式に無関心）。
- custodian は interface（`KeyCustodian{Wrap,Unwrap}`）。現実装は **localCustodian**
  （KEK = `HMAC(master, "af-kek:"+tenantID)`・AES-GCM・AAD=keyRef）。
- ⚠️ **honest な限界**: localCustodian は KEK が master 由来のため、実効強度は単一 `AF_MASTER_KEY` と
  同等。テナント鍵 disable による**真の per-tenant crypto-shred は Vault/KMS custodian 採用時**に達成
  （📋 seam のみ・[decisions/0005](../decisions/0005-envelope-custodian.md)）。

## 7.7 監査

- 器は `audit_log`（[06](06-data-model.md)）。`actor_kind` = user / admin / mcp / system（将来 claude）。
- **記録範囲は「変更・破壊操作」のみ**（読み取りは既定オフ、**ターミナル生ストリームは保存しない**——
  秘密混入リスク。[docs/20 E.1](../20-container-audit-egress.md)）。
- 書き込み点: CP proxy 層（fs/git/repo/session の変更系・[05 §5.5](05-api-contracts.md)）、admin API、
  MCP write ツール（`actor_kind=mcp`・PAT id 記録・role は呼び出し時に live 再解決）。
- 読み取り面: `GET /api/admin/audit`（tenant scope・RBAC）+ Console admin タブ。
- 第 2 段（claude PreToolUse hook で `actor_kind=claude`）は 📋（[docs/20 A-第2段](../20-container-audit-egress.md)）。

## 7.8 egress 統制 🚧（log-only 運用まで実装・enforce は後続）

設計と決定は [docs/20](../20-container-audit-egress.md)。実装済みの器:

- **forward proxy 方式**（CP バイナリのサブコマンド、`AF_EGRESS_LISTEN` 既定 `:3128`）。
  FQDN（CONNECT/SNI）で allow/deny を判定し、**TLS は復号しない**。
- イベントは `/internal/egress`（`AF_EGRESS_TOKEN`）で CP に集約 → `egress_daily` に日次集計。
  policy 配布は `/internal/egress/policy`。
- **allowlist は版管理**（`egress_allowlist`: active/proposed/retired）+ デプロイ全体モード
  （`deployment_setting`）。admin API/UI（一覧・承認・mode 切替）実装済み。
  AI は**提案のみ・人間が承認**（自動適用しない）。
- 段階運用が設計の核: **log-only で実測 → allowlist を固める → enforce へ切替**。
  enforce 化とコンテナ側配線（`--internal` 網 + proxy env 注入）の常時有効化は後続
  🚧、aws 側（Network Firewall / DNS Firewall）は P3-7 と同時 📋。

## 7.9 リスクと残課題

1. **`--dangerously-skip-permissions` の既定運用** — コンテナ境界が唯一の砦。§7.2 を厳格に。
   利用者はオフにできる（[76](../76-tool-permission-choice.md)）が、既定は従来どおりスキップで、
   オフも隔離境界の代わりにはならない（§7.1）。
2. **CP/ホスト侵害 = デプロイ内一括崩壊**（§7.1 の前提の限界）。会社間非波及が緩和。
3. **長期保持する L2 認証情報の失効・ローテーション** — 封筒暗号で枠組みは入ったが、真の失効は
   Vault/KMS 待ち（§7.6）。
4. **サプライチェーン** — Workspace イメージ同梱ツールの出所管理・定期更新（[04](04-workspace-agent.md)）。
5. **egress enforce 未了** — log-only の観測は入ったが遮断はまだ（§7.8）。
6. 対外的な脅威モデル・脆弱性報告窓口は [SECURITY.md](../../SECURITY.md)（英語・対外向け）。
