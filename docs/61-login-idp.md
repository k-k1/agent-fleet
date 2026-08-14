# 61. ログイン IdP — Google 固定から「汎用 OIDC ＋ GitHub」へ

> 状態: **設計・未実装**（2026-08-14）
> 意思決定: [decisions/0043](decisions/0043-login-idp.md)
> 関連: [dev/07-security.md](dev/07-security.md) §7.3（AUTH 3 モード＝現行契約） /
> [dev/06-data-model.md](dev/06-data-model.md)（`identity` / `membership`） /
> [dev/09-deploy.md](dev/09-deploy.md)（配布物の設定面） / [35-packaging.md](35-packaging.md)（4 ターゲットへ同じ設定を配る） /
> [28-i18n.md](28-i18n.md)（CP 描画ページの言語選択） / [roadmap.md](roadmap.md) §12.2（各社が握る設定項目）
> 対象: Control Plane（`oauth_*.go` / `main.go` / `routes.go` / migrations）/ Console（アカウント連携 UI・P1）/ `deploy/**`

## 61.1 目的

L1（Console へのログイン）の IdP が **Google 1 種に固定**されている。セルフホスト先は
「グループ各社が自社の社員をホストする」（roadmap §12.2）想定なので、Google Workspace を
使っていない会社は**そもそも設置できない**。M365 の会社が多数派である以上、これは配布の前提条件を欠く。

本ドキュメントは L1 の IdP を複数化する。**L2（Claude / codex / opencode を誰として動かすか＝
ユーザー本人の OAuth）とは無関係**で、`oauth_bitbucket.go` のような Git プロバイダ接続とも別軸
（[dev/08-integrations.md](dev/08-integrations.md)）。

受入条件:

1. Google を使っていない会社が、**CP の設定だけで** Entra ID / Okta / Keycloak などを IdP にできる。
2. 1 デプロイで**複数の IdP を同時に**有効化でき、ログイン画面に有効な分だけボタンが出る。
3. **同じ人が別の IdP でログインしても同じ identity**（＝同じ workspace・同じ home・同じ secrets）になる。
   ならない場合は「別人扱いになる」と本人に見える形で示し、黙って 2 つ目の workspace を作らない。
4. 許可リストは **IdP ごと**に持てる（GitHub は email ではなく org メンバーシップで判定できる）。
5. オフボーディング（毎リクエスト再判定）の性質を落とさない。
6. 既存デプロイは env を 1 行も変えずに今までどおり動く（Google の env 名は据え置き）。
7. 追加の Go 依存を増やさない。

## 61.2 現状（2026-08-14 のコード実測）

| 事実 | 位置 |
|------|------|
| `AUTH` は `oauth`（CP 内蔵 Google）/ `proxy`（外部ゲートウェイのヘッダ）/ `dev`（固定ユーザー）の 3 モード | `main.go:80`, [dev/07 §7.3](dev/07-security.md) |
| Google 部分は 1 ファイルに閉じている（461 行）。プロバイダ抽象は無く、URL も scope もクレーム名も定数直書き | `oauth_google.go:27-34, 182-190` |
| **id_token を検証していない。** 認可コード → トークン → userinfo で `email` / `email_verified` を取る。「トークンエンドポイントから TLS で直接来たから信頼」という設計 | `oauth_google.go:238-256` |
| **JWT ライブラリを持っていない**（`go.mod` に無い）。stdlib のみで完結している | `control-plane/go.mod` |
| セッション cookie の中身は `{email, exp}` のみ。どの IdP で入ったかは残っていない | `oauth_google.go:85-93` |
| 許可リストは email ベース 3 系統（CSV 2 本＋ファイル 1 本）。**全部空なら全拒否**（fail-closed） | `oauth_google.go:120-148`, `main.go:193-195` |
| `authGate` は**毎リクエスト**許可リストを再判定する（＝許可リストから消すのがオフボーディング経路） | `oauth_google.go:299-309` |
| ログイン画面は Go の文字列リテラルに Google ボタンを直書き。文言は ja/en 2 言語 | `oauth_google.go:394-461` |
| ルートは `/login` `/oauth2/{login,callback,logout}` の 4 本、認証除外は `exemptPrefix("/oauth2/")` | `routes.go:102-114` |
| 人の実体は **`identity.user_key = sanitizeUser(email)`**（小文字化・非英数→`-`・40 字上限） | `resolver.go:281-288` |
| `user_key` は UNIQUE かつ**識別子そのもの**。sanitize 衝突は `disambiguateUserKey` が別キーへ逃がす | `store_sqlite.go:300-360` |
| その `user_key` が **workspace の home ディレクトリ名**（`<WS_DATA>/<user>/home`）＝暗号化 secrets の帰属先 | [dev/07 §7.2](dev/07-security.md) |
| SPA 側は 401 をラッチして再ログインモーダルを出す（ページごと飛ばさない） | `console/src/core/auth/authExpired.ts` |

**要点**: OAuth の配線を増やすのは難しくない。難所は最後の 3 行 —
**email が identity そのもので、その identity が home ディレクトリと暗号鍵の帰属先**である点にある。

## 61.3 プロバイダの選定

| 候補 | 判断 | 理由 |
|------|------|------|
| **Entra ID（Microsoft 職場/学校アカウント）** | **採用（最優先）** | 想定顧客の多数派。OIDC 準拠なので汎用実装にそのまま載る |
| **汎用 OIDC**（Okta / Keycloak / Auth0 / Cognito / GitLab） | **採用（Entra と同じ 1 実装）** | discovery JSON ＋ token ＋ userinfo だけで足りる。「うちは Okta」に設定だけで答えられる |
| **GitHub** | **採用（別アダプタ・条件付き）** | OIDC 非対応（GitHub の OIDC は Actions 用トークンで、ユーザーログインには無い）。**org メンバーシップ判定とセットでのみ**入れる（§61.7） |
| SAML（HENNGE One / TrustLogin / CloudGate 等） | **実装しない** | CP に SAML SP を持たせる労力に見合わない。**既存の `AUTH=proxy` ＋ oauth2-proxy / Keycloak でブリッジ**するのが答え（文書化で対応） |
| Apple / LINE / Slack / Atlassian / Discord | 不採用 | 会社が入退社を統制する手段にならない。B2B の入口として意味を持たない |
| メール magic link / パスワード | 不採用 | SMTP 依存と資格情報管理を CP が背負う。IdP を持たない会社向けの価値はあるが、本件の範囲外 |
| パスキー単体 | 不採用 | 「誰か」を決めるのは IdP 側の役目で、第 2 要素は IdP に任せる |

**設計上の含意**: 「Microsoft を足す」ではなく **OIDC クライアントを 1 本作る**。プロバイダ別コードを
書くのは GitHub だけにする。Google も内部的にはこの汎用実装の 1 インスタンスへ移す（env 名は据え置き）。

## 61.4 email をいつ信じてよいか — IdP ごとに根拠が違う

許可リストが email ベースである以上、**「その IdP がその email を検証しているか」**が
そのまま認可の強度になる。ここは IdP ごとに事情が違い、一律 `email_verified == true` では通らない。

| IdP | email の信頼根拠 | 落とし穴 |
|-----|-----------------|---------|
| Google | `email_verified` クレーム（現行実装がすでに確認） | 無し（現状維持） |
| **Entra ID** | **`email_verified` クレームが無い。** テナント固定（issuer を `.../{tenant-guid}/v2.0` にする、または `tid` クレームを許可リストと突合）で担保する | **`common` エンドポイントを使うと「Microsoft アカウントを持つ全人類」が入口に立つ。** 個人 MSA の `email` は本人が付け替えられるので、テナント固定なしに email 許可リストを当てると成りすましが通る |
| Okta / Keycloak / Auth0 | 多くは `email_verified` を出す。出さない構成もある | issuer を固定すれば実質そのテナントの利用者に閉じる |
| **GitHub** | **`/user/emails`（`user:email` スコープ）の `primary && verified` のみ**。`/user` の `email` は非公開設定だと `null` | 検証フラグを見ないと、他人の会社 email を自分のアカウントに登録して許可リストを通過できる |

したがってプロバイダ定義には **「email をどう信頼するか」を宣言として持たせる**:

- `trust: "email_verified"` — そのクレームが true のときだけ受理（Google / Okta 等）
- `trust: "issuer"` — issuer（＋`tid`）が固定済みなので、その IdP の払い出す email を信じる（Entra のテナント固定）
- `trust: "api"` — 別 API で検証フラグを取る（GitHub）

`trust` を宣言しないプロバイダは**起動時に拒否**する（fail-closed を落とさない）。

## 61.5 同一人物の判定 — ここが本体

複数 IdP を足した瞬間に、いまの `user_key = sanitizeUser(email)` は 2 つの壊れ方をする。

1. **同じ人の email が IdP 間で違うと、別人になる。** Google で `a@corp.com`、GitHub で
   `a@personal.dev` なら identity が 2 つ・**workspace も home も secrets も別**。
   ユーザーからは「リポジトリが消えた」に見える。GitHub は登録 email が会社 email と違うのが
   普通なので、**リンク機構より先に GitHub を出すと必ず踏む**。
2. **IdP 側で email が変わると identity が変わる。** 姓変更・ドメイン統合で `user_key` が変われば
   新しい home が生える。現状は Google だけなので稀だが、IdP が増えれば確率が上がる。

### 決めごと

- **`user_key` は不変**にする。一度 identity に紐づいた `user_key` は、email が変わっても動かさない。
  home ディレクトリ名でもあるため、変えるとデータ移行が要る（＝やらない）。
- **`(provider, subject)` を一次キーにする**（P1）。IdP の `sub` は email と違い不変。
  新テーブル `identity_provider` を足し、`identity` 本体（`user_key` / home / secrets）は触らない。

```sql
-- migrations/0031_identity_provider.sql（次の空き番号）
CREATE TABLE identity_provider (
  provider      TEXT NOT NULL,          -- "google" / "entra" / "github" ...（設定の provider id）
  subject       TEXT NOT NULL,          -- IdP の sub（GitHub は数値 id。login 名は改名され得るので使わない）
  identity_id   TEXT NOT NULL REFERENCES identity(id),
  email         TEXT NOT NULL DEFAULT '', -- 最後に見た値（表示用。判定には使わない）
  created_at    TEXT NOT NULL,
  last_login_at TEXT,
  PRIMARY KEY (provider, subject)
);
CREATE INDEX idx_identity_provider_identity ON identity_provider(identity_id);
```

解決規則:

| 状況 | 動作 |
|------|------|
| `(provider, subject)` が既にある | その `identity_id` を使う。email が変わっていれば表示用に更新するだけ |
| 無い、かつ email が既存 identity と一致 | **その identity へ結合**し、行を追加（＝いままでどおりの体験） |
| 無い、かつ email も一致しない | 許可リストを通っていれば**新規 identity**。ログイン後に「これは新しいアカウントです」と明示する |
| 既存 identity に**別 email で**紐づけたい | **ログイン画面からは行わない。** サインイン済みの状態で Console の「アカウントを追加」からもう一方の IdP を通す（＝両方にログインできることが結合の証明） |

既存デプロイの移行は、初回ログイン時に `(google, sub)` 行を現 identity へ書くだけで済む
（`user_key` は動かないので home もセッションもそのまま）。

## 61.6 プロバイダ抽象と配線

`oauth_google.go` を 3 つに割る。**redirect_uri は `/oauth2/callback` 1 本のまま**にし、
どの provider かは**署名済み state cookie に載せる**（プロバイダごとに URI を登録させると、
設置する会社の手数が IdP の数だけ増える）。

```
oauth.go        provider インタフェース / state / cookie / セッション / authGate / ログイン画面
oauth_oidc.go   汎用 OIDC（discovery → authorize → token → userinfo）: google / entra / okta / …
oauth_github.go GitHub 専用アダプタ（P2）
```

```go
type loginProvider interface {
    ID() string                                     // "google" / "entra" / "github"
    Label(lang string) string                       // ログイン画面のボタン文言
    AuthorizeURL(state, redirectURI string) string
    Exchange(ctx context.Context, code, redirectURI string) (principal, error)
    Allowed(ctx context.Context, p principal) (bool, error) // 許可リスト判定（プロバイダ固有）
}

type principal struct {
    Provider string
    Subject  string // IdP の sub（不変）
    Email    string
    Verified bool   // §61.4 の trust 判定を通ったか
}
```

- `oauthState` に `p`（provider id）を足す。コールバックで**設定済み provider の集合と突合**してから使う
  （state は署名済みだが、無効な provider id で分岐させない）。
- `sessionClaims` に `prov` と `sub` を足す。JSON なので**既存 cookie は欠損フィールドとして読める**
  （移行時のログアウトは不要）。ただし `prov` 欠損は "google" とみなす暫定規則を 1 版だけ置く。
- `authGate` の毎リクエスト再判定は `prov` を見て**そのプロバイダの許可判定**を呼ぶ。
- **id_token の署名検証は引き続き行わない**。認可コードフローで client_secret 付き、
  トークンエンドポイントから TLS で直接受け取るため（OIDC Core §3.1.3.7 の注記と同じ論拠）。
  `tid` など userinfo に出ないクレームは、**同じレスポンスに入っている id_token のペイロードを
  署名検証なしで読む**。これが許されるのは「同一 TLS レスポンス由来」だからで、
  **フロントチャネル（implicit / form_post）で id_token を受ける経路を将来足すなら JWKS 検証が必須**になる。
  この前提を `oauth_oidc.go` の先頭コメントに固定する。

### ログイン画面

- ボタンは有効な provider の数だけ描画（`/oauth2/login?provider=<id>&next=…`）。
- 1 つだけなら現行と同じ見た目（既存デプロイの体験を変えない）。
- 文言は `loginText` に provider ごとのキーを足す（ja/en 両方・[28-i18n.md](28-i18n.md) の規約どおり）。
- エラーコードに `provider`（未知/無効な provider id）を追加。

## 61.7 GitHub アダプタ（P2）

OIDC ではないので専用実装になる。実装上の既知の罠:

- トークン交換は `Accept: application/json` を付けないと**フォームエンコードで返る**
  （現行 Google 実装の `http.PostForm` + JSON デコードをそのまま使うと空になる）。
- email は `GET /user/emails`（`user:email` スコープ）から **`primary && verified` の 1 件**だけを採る。
  `GET /user` の `email` は非公開設定で `null` になるので使わない。
- `subject` は**数値 `id`**。`login`（ユーザー名）は改名できるので識別子に使わない。
- 許可は **org メンバーシップ**で行う: `GET /user/memberships/orgs/{org}`（`read:org` スコープ）が
  `state: "active"` を返すこと。email 許可リストは GitHub では補助に留める。
- **OAuth App access restrictions**: org 側でサードパーティ OAuth App を制限していると、
  org 管理者の承認前は membership が見えず「正しい設定なのに全員拒否」になる。
  設置手順書にこの承認ステップを必ず書く。
- **毎リクエスト再判定は API 呼び出しになる**。`(provider, subject)` キーの TTL 付きキャッシュ（既定 10 分）を持ち、
  キャッシュミス時は同期問い合わせ。API 障害時は**最後の肯定結果を猶予期間（既定 1 時間）だけ延命**し、
  それを超えたら拒否する（可用性と fail-closed の折衷。値は env で調整可能にする）。

## 61.8 設定面（4 配布ターゲットすべてに同じ形で配る）

Google は**既存の env 名を据え置く**（受入条件 6）。追加分:

```sh
# 汎用 OIDC — 有効化する provider id の CSV。id ごとに <ID> を大文字で埋める
AF_OIDC_PROVIDERS=entra
AF_OIDC_ENTRA_ISSUER=https://login.microsoftonline.com/<tenant-guid>/v2.0
AF_OIDC_ENTRA_CLIENT_ID=<client-id>
AF_OIDC_ENTRA_CLIENT_SECRET=<client-secret>
AF_OIDC_ENTRA_LABEL_JA=Microsoft でサインイン        # 任意（既定は provider id から生成）
AF_OIDC_ENTRA_LABEL_EN=Sign in with Microsoft
AF_OIDC_ENTRA_TRUST=issuer                           # email_verified | issuer（§61.4）
AF_OIDC_ENTRA_ALLOWED_DOMAINS=example.co.jp          # 未設定なら共通の許可リストを使う
AF_OIDC_ENTRA_ALLOWED_TIDS=<tenant-guid>             # issuer を common にする場合は必須

# GitHub（P2）
GITHUB_OAUTH_CLIENT_ID=
GITHUB_OAUTH_CLIENT_SECRET=
AF_GITHUB_ALLOWED_ORGS=acme,acme-labs
AF_GITHUB_MEMBERSHIP_TTL=10m
AF_GITHUB_MEMBERSHIP_GRACE=1h
```

起動時チェック（`main.go:278` を拡張）:

- 有効な provider が **1 つも無ければ現行どおり fatal**。
- 個々の provider は設定不足なら**無効化＋警告**（1 つの IdP の設定ミスで全員が締め出されない）。
- provider ごとの許可リストも共通の許可リストも空なら、現行と同じ**警告つき全拒否**。
- `AF_OIDC_*_ISSUER` が `common` / `organizations` で `ALLOWED_TIDS` が空なら **fatal**（§61.4 の事故防止）。

更新する配布物: `deploy/compose/.env.example` / `deploy/local/oauth.env.example` /
`deploy/aws/ecs/cfn/30-ingress.yaml` / `deploy/aws/ec2-single/README.md` /
`deploy/compose/README.md` / `docs/guide/operator/*` / `docs/dev/07-security.md` §7.3。

## 61.9 段階

| 段階 | 内容 | スキーマ変更 |
|------|------|------------|
| **P0** | プロバイダ抽象 ＋ 汎用 OIDC（Entra / Okta / Keycloak / Auth0 / Cognito）。Google を同実装の 1 インスタンスへ移す。ログイン画面の複数ボタン・`sessionClaims` 拡張・設定と文書 | 無し |
| **P1** | `identity_provider` テーブルと解決規則（§61.5）。Console の「アカウントを追加」導線 | `0031` |
| **P2** | GitHub アダプタ（org 判定・TTL キャッシュ・猶予） | 無し |
| **P3**（任意） | 管理 UI から許可リストを編集（現在は env とファイルのみ） | 未定 |

**P1 を P2 より先に置くのが本設計の要点**。GitHub は email 不一致が常態なので、リンク機構なしに
出すと §61.5-1 の workspace 分裂を必ず起こす。P0 だけでも「Microsoft でログインしたい」は満たせる。

## 61.10 却下した代替案

- **CP に SAML SP を実装する。** 日本企業の IdP は SAML 前提が多いが、SP 実装（メタデータ・署名検証・
  暗号化アサーション・リプレイ防止）は OIDC の数倍の面積で、stdlib では収まらない。
  **`AUTH=proxy` ＋ oauth2-proxy / Keycloak のブリッジ**を正式な回答として文書化する方が総量が小さい。
- **Entra 専用実装を足すだけ。** 最短だが、Okta・Keycloak の要望が来るたびに同じ作業を繰り返す。
  汎用 OIDC との差は discovery を読む数十行しかない。
- **provider ごとに redirect_uri を分ける**（`/oauth2/callback/entra`）。実装は素直だが、
  設置する会社が IdP 側に登録する URI が増える。署名済み state に載せれば 1 本で足りる。
- **`user_key` を `sub` ベースに変える。** identity としては正しいが、`user_key` は home ディレクトリ名なので
  既存デプロイ全員のデータ移行が要る上、`af-ws-<user>` が人間に読めなくなる。
  `identity_provider` を横に足せば移行ゼロで同じ効果が得られる。
- **email が一致したら常に自動結合（別 email も管理者が対応表で結合）。** 対応表は誰も保守しないし、
  弱い検証の IdP が混ざった瞬間に乗っ取り経路になる。結合は本人の二重ログインでのみ行う。
- **magic link / パスワードログイン。** IdP を持たない小さな会社には効くが、CP が資格情報と SMTP を
  背負う。需要が出てから別 ADR で判断する。

## 61.11 残る未決

- Console のアカウント連携 UI をどのグループに置くか（設定モーダルの IA・[settings-modal](../docs/README.md) 参照の再編と衝突しないか）。
- `audit.go` に provider を残すか（監査上は残したいが、DTO を広げる前に既存カラムから導出できないか確認する）。
- `SUPER_ADMIN_EMAILS`（`main.go:85`・email ベース）を provider 付きにするか。当面は email のままで足りる。
- IdP 側のグループ（Entra の `groups` / GitHub の team）を tenant / role へ自動マッピングするか。
  P3 以降の別テーマ。現状は membership を CP 側で持つ設計（[dev/06](dev/06-data-model.md)）。
- `prompt=select_account` 相当を各 IdP でどう揃えるか（Entra は `prompt=select_account` が使えるが、
  IdP によっては未対応で無視される）。
