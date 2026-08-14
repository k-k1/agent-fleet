# 61. ログイン IdP — Google 固定から「汎用 OIDC ＋ GitHub」へ

> 状態: **P0・P1 実装済み**（2026-08-14）／P2・P3・P4 は未着手
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
8. **1 デプロイ内で部署ごとにテナントを分けられる**: テナント毎にログイン画面・使える認証方式・
   email / ドメインの縛りを設定でき、それが**画面の見た目だけでなく実際の認可**として効く（§61.9）。

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
-- migrations/0038_identity_provider.sql（次の空き番号。0037 まで使用済み）
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
| 既存 identity に**別 email で**紐づけたい | **できない。** 別 email は別人として扱う（下記の改訂） |

既存デプロイの移行は、初回ログイン時に `(google, sub)` 行を現 identity へ書くだけで済む
（`user_key` は動かないので home もセッションもそのまま）。

### ★ 改訂（2026-08-14・P1 実装時）— 別 email の結合そのものをやめた

初版は「サインイン済みの状態で Console の『アカウントを追加』からもう一方の IdP を通せば結合する」
（＝両方にログインできることが結合の証明・[決定 5](decisions/0043-login-idp.md)）としていた。
これを**撤回し、結合機構を作らない**。理由:

- 両方にログインできることが証明するのは「その 2 つのアカウントを操作できる」ことまでで、
  **同一人物であること**ではない。弱い検証の IdP が 1 つでも有効なら、そこで取ったアカウントを
  会社アカウントの home（＝会社の secrets が入っている側）へ合流させる経路になる。
- 結合を解く導線は設計に無く、**一度合流すると戻せない**。
- そもそも**入口の許可リストが会社ドメインに限定されていれば、ログインできる email は会社 email だけ**。
  別 email が現れるのは「ドメインを緩めた運用を選んだ場合」で、そのとき WS が分かれるのは設定どおりの結果。
  GitHub（P2）も「**会社 email を GitHub に登録している人専用**」と定義すれば同じ線に収まる。

したがって §61.5 が実装するのは規則 1〜3 だけになる。「押したボタンに関わらず同じ WS」を
成立させているのは**規則 2（email 一致で結合）**で、これは残る。**Console 側の作業はゼロ**
（「アカウントを追加」UI は作らないので、設定モーダルの置き場問題＝§61.14 の 1 項目は消滅した）。

規則 3 の通知（受入条件 3）は結合手段が無くなった分**唯一の担保**になるので残す。実装:

- **CP がログイン直後に挟む 1 画面**（ja/en・ログイン画面と同じ様式）。「別のワークスペースになります。
  以前のものに入るには、いつも使っているメールアドレスでサインインし直してください」。
  Console 側の状態に依存せず、Console が壊れていても届く。
- 出す条件は **IdP が 2 つ以上のデプロイのみ**。provider が 1 つなら新規 identity は
  「新しい同僚」でしかなく、既存デプロイの体験を変えない（受入条件 6）。
- そのために `(provider, subject)` の束縛を**コールバック時**に行う（従来は初回 API リクエスト時の遅延生成）。
  次のリクエストでは行が既にあり「新規かどうか」を答えられないため。

実装で足を引っ張られた点が 2 つ:

- **`prov` / `sub` は `authGate` が捨てていた。** 下流へ渡るのは email ヘッダだけで、
  `resolveIdentity` はそれしか読まない。**request context で運ぶ**ことにした
  （ヘッダを増やすと、CP がエッジである以上「受信時に必ず削除する」処理の追加漏れが
  そのまま provider/sub の詐称になる）。`AUTH=proxy` / `AUTH=dev` には context が付かず、
  従来どおり email だけで解決する。
- ★ **`user_key` と `sanitizeUser(email)` が食い違い得るようになった。** 改名を許した結果で、
  `resolveUser`（＝後者）を直接キーに使っていた Bitbucket 連携の開始（`oauth_bitbucket.go`）は、
  改名後の人のトークンを**別 workspace へ入れてしまう**。解決済み identity の `user_key` を
  使うよう直した。同じ形の利用箇所が増えないか、以後は `resolveUser` を疑うこと。

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

### 実装メモ（P0 で実際にこうした）

設計から動かなかった点は無いが、書いていなかった判断が 5 つある。

- **Google は discovery を引かない。** エンドポイント 3 本を静的に seed した（従来の定数のまま）。
  受入条件 6 を「設定を変えずに動く」だけでなく「**通信要件も増やさない**」まで満たすため
  （egress 制限のあるデプロイで `.well-known` が引けずログイン不能、を作らない）。
  他の provider は discovery を**遅延実行＋24h キャッシュ**にした。起動時に引くと
  IdP の可用性が CP の起動条件になってしまう。
- **discovery の issuer 一致は検証するが、`{tenantid}` テンプレートだけ例外**にした。
  Entra のマルチテナント文書は issuer に literal `{tenantid}` を返すため。その構成は
  そもそも `ALLOWED_TIDS` 必須（決定 7）で、実質の担保はそちら。
- **userinfo の失敗ではログインを落とさない**。同じトークンレスポンスに入っている id_token が
  同等に信頼できる出所だから（決定 9 と同じ論拠）。email は
  userinfo → id_token の `email` → `preferred_username` → `upn` の順で採る（Entra の userinfo は
  email を返さないことがある）。`email_verified` を文字列 `"true"` で返す IdP も受ける。
- **`tid` 不一致は "denied" ではなく "forbidden"** を出す（内部的には `errNotAllowed` 番兵）。
  利用者に見える意味が「キャンセルされた」ではなく「許可されていない」だから。
- **issuer は https 必須、ただしループバックのみ http を許す**（ローカルの Keycloak / Dex 用）。
  http のときは起動ログに警告を出す。

### ログイン画面

- ボタンは有効な provider の数だけ描画（`/oauth2/login?provider=<id>&next=…`）。
- 1 つだけなら現行と同じ見た目（既存デプロイの体験を変えない）。
- 文言は `loginText` に provider ごとのキーを足す（ja/en 両方・[28-i18n.md](28-i18n.md) の規約どおり）。
- エラーコードに `provider`（未知/無効な provider id）を追加。

## 61.7 GitHub アダプタ（P2）

OIDC ではないので専用実装になる。

★ **前提が P1 の改訂で変わった**: 別 email の結合機構は作らないと決めたので、GitHub は
「**会社 email を GitHub アカウントに登録している人専用**」の入口として出す。org メンバーシップを
通っても `primary && verified` の email が会社ドメイン外なら**別 identity＝別 workspace**になり、
本人にはその旨が通知される（§61.5）。運用としては、GitHub の provider にも
`ALLOWED_DOMAINS` を設定して**入口で落とす**方が親切（気付かないまま別 WS で作業するより早い）。

実装上の既知の罠:

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

### 実装メモ（P2 で実際にこうした）

設計から動かなかった点は無いが、書いていなかった判断が 4 つある。

- ★ **キャッシュを失ったセッションは「拒否」ではなく「再ログイン」**（利用者判断）。org の再判定には
  本人の access token が要るが、cookie に載せれば XSS で漏れるので**プロセス内メモリにしか持たない**。
  つまり **CP 再起動でキャッシュも token も消える**。そのとき本人は org のメンバーのままなので、
  既存の `forbidden`（「このアカウントはアクセスを許可されていません」）を出すのは事実と違う。
  エラーコード **`reauth`** を 1 つ足し、「セッションの確認ができなくなりました。もう一度サインイン
  してください」を出す（API には 403 ではなく **401** を返し、SPA の既存の未認証経路に乗せる）。
  GitHub 側のセッションが生きていれば、この往復はたいてい無操作で完了する。
  `sessionAllowed` は `bool` からエラーコードも返す形へ変えた。
- ★ **`GITHUB_OAUTH_CLIENT_ID` は既に使われている**。git 連携の **device flow**（Workspace の Agent が
  実行する）が前から使っていて、CP が各 Workspace コンテナへ注入し、`.env.example` にも載っている。
  1 つの OAuth App で両方のフローを賄えるので env 名は §61.8 のまま共有し、**コールバック URL
  `<PUBLIC_BASE_URL>/oauth2/callback` の追加だけ**を設置手順に足す。分けたい運用（org への App 承認を
  ログイン用だけに絞りたい等）向けに `AF_GITHUB_LOGIN_CLIENT_ID` / `_SECRET` の上書きを用意した。
  **これに伴い「GitHub ログインを有効にする合図」は client_id ではなく `AF_GITHUB_ALLOWED_ORGS`** に
  した。device flow だけを使ってきた既存デプロイを、毎起動 warning で叩かないため。
- **許可は 2 つの門の AND**: ①org メンバーシップ（必須。`AF_GITHUB_ALLOWED_ORGS` が空なら provider ごと
  無効化する — メンバーシップ判定とセットでのみ採用した入口なので §61.3）②email 許可リスト
  （provider 固有 → 無ければ共通 → **どちらも設定が無ければ email の門は無し**＝org が許可リストそのもの）。
  email の門を先に評価するので、ドメイン外の人は API 呼び出しを一切起こさない。
- **GitHub のロゴは出さない**。P0 で決めた「ライセンスの無いサードパーティロゴを同梱しない」に従い、
  ボタンは汎用の鍵アイコンのまま。ラベルだけ `GitHub でサインイン` を既定にする。

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

# GitHub（P2）— CLIENT_ID は git 連携（device flow）と共有。既存アプリに
# コールバック URL <PUBLIC_BASE_URL>/oauth2/callback を足すだけでよい
GITHUB_OAUTH_CLIENT_ID=
GITHUB_OAUTH_CLIENT_SECRET=
AF_GITHUB_ALLOWED_ORGS=acme,acme-labs                # 必須。これが GitHub ログインを有効にする合図
AF_GITHUB_ALLOWED_DOMAINS=example.co.jp              # 強く推奨（§61.7）。未設定なら共通の許可リスト
AF_GITHUB_ALLOWED_EMAILS=                            # 任意
AF_GITHUB_LABEL_JA=GitHub でサインイン                # 任意
AF_GITHUB_LABEL_EN=Sign in with GitHub
AF_GITHUB_MEMBERSHIP_TTL=10m
AF_GITHUB_MEMBERSHIP_GRACE=1h
AF_GITHUB_LOGIN_CLIENT_ID=                           # 任意。ログイン専用の OAuth App を分ける場合
AF_GITHUB_LOGIN_CLIENT_SECRET=
```

起動時チェック（`main.go:278` を拡張）:

- 有効な provider が **1 つも無ければ現行どおり fatal**。
- 個々の provider は設定不足なら**無効化＋警告**（1 つの IdP の設定ミスで全員が締め出されない）。
- provider ごとの許可リストも共通の許可リストも空なら、現行と同じ**警告つき全拒否**
  （GitHub の `AF_GITHUB_ALLOWED_ORGS` も「許可リスト有り」として数える）。
- `AF_OIDC_*_ISSUER` が `common` / `organizations` で `ALLOWED_TIDS` が空なら **fatal**（§61.4 の事故防止）。
- GitHub は `AF_GITHUB_ALLOWED_ORGS` が空なら**無効化＋警告**。ただし `GITHUB_OAUTH_CLIENT_ID` だけが
  設定されている状態（＝ git 連携の device flow を使っているだけ）は**警告を出さない**。

更新する配布物: `deploy/compose/.env.example` / `deploy/local/oauth.env.example` /
`deploy/aws/ecs/cfn/30-ingress.yaml` / `deploy/aws/ec2-single/README.md` /
`deploy/compose/README.md` / `docs/guide/operator/*` / `docs/dev/07-security.md` §7.3。

★ **P4（§61.11）は env を 1 つも増やさない。** テナント定義の認証方式は DB に入り、
Console から編集するので、4 ターゲットの env 例に足すものは無い（承認を省略する
`AF_ALLOW_TENANT_IDP` のような env は意図的に作らない・§61.13）。配布物側で要るのは
**運用ガイドの記述だけ** — 承認という 1 段が挟まること、そして P4 以降は
**`client_secret` が `DATA_DIR` の DB に入る**ので、`AF_MASTER_KEY` をデータ領域の外に置く
既存ルールの重みが増すこと（§61.11.4）。

## 61.9 テナント毎のログイン（1 デプロイ内を部署で分ける）

会社ごとに別デプロイ（roadmap §12.2）は維持したまま、**1 社の中で部署ごとにテナントを分ける**ケース。
テナント毎に「ログイン画面」「使える認証方式」「email / ドメインの縛り」を持たせる。

### 61.9.1 現状（2026-08-14 実測）— テナント軸は入口に存在しない

| 事実 | 位置 |
|------|------|
| 許可リストは**デプロイ全体で 1 枚**。`emailAllowed(email)` はテナントを受け取らない | `oauth_google.go:120` |
| `Tenant` に許可ドメイン相当のフィールドは無い（`ID/Slug/Name/Status/Limits/Isolation/KeyRef/CreatedAt`） | `store.go:15` |
| テナント帰属は `AF_PROVISION` の 2 択のみ。`auto`＝既定テナントへ自動参加 / `invite`＝membership 無しは拒否 | `main.go:84`, `resolver.go:69` |
| 実行時のテナント選択は `X-AF-Tenant`。非メンバーは 403 `forbidden_tenant`、複数所属で未指定は 409 | `httpapi.go:34`, `resolver.go:128-142` |
| テナントの管理 API は既にある（list / create / limits） | `routes.go:129-137` |
| ★ **テナント毎の「ログインできる ID の名簿」は既に存在する。** `POST /api/admin/memberships` は email（または user_key）で **未ログインの人の identity を先に作って** membership を張る＝招待そのもの | `tenants.go:254-293`, `routes.go:134` |
| その招待 UI も既にある（設定 → 管理 → メンバー追加。email / user_key / role を送る） | `console/src/features/settings/AdminTab.tsx:1593` |

つまり今は「**入口の門（デプロイ全体で 1 枚）**」と「**どのテナントに属するか（membership）**」が
完全に分離していて、**両者をつなぐ規則だけが無い**。名簿の器は既に揃っている。

### 61.9.2 3 つの層を混ぜない（本節の要）

| 層 | 問い | 決めるもの | いつ |
|----|------|-----------|------|
| **入口の門** | この人はこのデプロイにサインインできるか | 許可リスト（デプロイ ∪ 全テナント規則） | ログイン時＋**毎リクエスト**（`authGate`） |
| **テナントの門** | この人はいま**このテナント**を使ってよいか | membership ＋ テナント規則 ＋ 使った IdP | テナント解決時（`selectMembership` / `resolveFull`） |
| **ログイン画面** | どのボタンを見せるか | テナントの `allowed_providers` | `/login/<slug>` 描画時 |

★ **URL のテナント指定は利用者が自由に書き換えられるので、認可の根拠にしてはならない。**
`/login/<slug>` は「どの画面を出すか」のヒントに過ぎない。実際にどのテナントへ入れるかは
**サーバ側の membership とテナント規則**だけで決める。

★ **テナントの門は `authGate` には置けない。** `authGate` はテナントを知らない（`X-AF-Tenant` を読むのは
その先の `httpapi.go:34`）。テナント規則を `authGate` に持ち込むと「どのテナントで判定するのか」が
決まらず、必ず穴か過剰拒否になる。**入口＝`authGate`、テナント＝`resolveFull` 側**と役割を分ける。
これにより毎リクエスト再判定というオフボーディングの性質（受入条件 5）も両層で保たれる。

### 61.9.3 ログイン画面の分割 — パス方式

- **`/login/<tenant-slug>`**（採用）。認可導線は `/oauth2/login?provider=<id>&tenant=<slug>&next=…` で、
  **tenant も署名済み state cookie に載せる**（§61.6 の provider と同じ扱い）。
- **サブドメイン方式（`<tenant>.af.example.com`）は不採用**: ワイルドカード DNS と証明書が要り、
  Tailscale Funnel は 1 ホスト名しか出せない。`PUBLIC_BASE_URL` が単一である前提も崩れ、
  redirect_uri がホストごとに増えて §61.6 の「callback は 1 本」が壊れる。
- **未知の slug は 404 にせず汎用ログイン画面を返す**。404 で区別すると、未認証者にテナント slug の
  存在有無を教えることになる。表示するのはテナント表示名と有効な IdP ボタンだけで、
  メンバー数などは出さない（部署名が出る程度の露出は受容する）。
- **`/login`（slug 無し）は従来どおり**有効な IdP を全部出す。既存デプロイの体験は変わらない。

### 61.9.4 テナント毎の認証方式

- `tenant.allowed_providers`（空 = デプロイで有効な全 provider）。
- ログイン画面はそのテナント分のボタンだけを出す — **ただしこれは見た目**。
- ★ **強制はテナント解決時に行う。** セッションの `prov` クレーム（§61.6 で追加）が
  `allowed_providers` に無ければそのテナントは使わせない。これを省くと、
  **汎用 `/login` で GitHub ログイン → `X-AF-Tenant` を差し替え**るだけで
  「Entra 限定」のテナントに入れてしまう。§61.6 で `prov` を足す価値の半分はここにある。
- ★ **403 で終わらせず再サインインへ誘導する。** セッションは provider を 1 つしか持たない
  （複数同時保持はしない — cookie が認可状態の集合になり、失効の意味が曖昧になる）。
  よってテナント A（GitHub 可）から B（Entra 限定）へ切り替えると再ログインが要る。
  この場合は `provider_required` を返し、Console のテナント切替から
  `/oauth2/login?provider=entra&tenant=b&next=…` へ誘導する。**「このテナントには Microsoft
  でのサインインが必要です」と画面に出す**（黙って 403 にしない）。

### 61.9.5 ★ 名簿は membership。テナント側に email リストを持たせない

「テナント毎にログイン可能な ID を管理する」——これが本設計の中心で、**新しい台帳は要らない**。
`membership` がその名簿そのもので、招待 API（`tenants.go:254`）が未ログインの人の identity を
先に作れるようになっている。テナントに `allowed_emails` カラムを足すと、
**同じ「誰が入れるか」を 2 箇所で管理する二重台帳**になり、必ずずれる。

したがってテナント側に持たせる規則は次の 2 つだけにする。

| | `auto_join_domains` — 自動参加 | `allowed_domains` — 招待のガード |
|---|---|---|
| 意味 | 「`@sales.acme.co.jp` が来たら sales へ自動で membership を作る」 | 「sales に招待してよいのは `@sales.acme.co.jp` だけ」 |
| 効くとき | **初回ログイン時** | **membership 追加時（招待 API）** |
| 目的 | 招待運用を省きたい小規模／単一テナント向けの省力化 | tenant_admin が自部署ドメイン外を勝手に足すのを防ぐ |
| 空のとき | 現行どおり（`auto`＝既定テナント / `invite`＝拒否） | ガードなし＝**既定** |

★ **`allowed_domains` を「毎リクエストの制約」にしない。** 制約にすると、正規に招待した
業務委託（別ドメイン）が締め出され、それを救うために例外リスト（`allowed_emails`）が要り、
結局二重台帳に戻る。**継続的な可否は membership が持つ**（外したい人は membership を消す＝
現行の運用のまま）。`allowed_domains` は「誰を名簿に載せてよいか」の上限だけを縛る。

★ **全社共通ドメインの会社ではこれが本命**になる。`@acme.co.jp` しか無ければ
`auto_join_domains` では部署を分けられないが、名簿（membership）は最初からドメインに依存しない。
このため **IdP のグループ（Entra の `groups` / GitHub の team）連携は必須ではなくなった**（§61.14）。

★ **招待は email に紐づくので、GitHub 単独ログインとは噛み合わない。** `addMembership` は
`sanitizeUser(email)` で identity を先に作る（`tenants.go:271-286`）。GitHub の登録 email が
会社 email と違えば招待に一致せず、別 identity が生まれる。よって **GitHub を使う人の順序は
「まず会社 IdP でログイン → Console で GitHub をリンク（§61.5）」**になる。ここでも P1 が P2 の前提。
（副作用として、typo した email への招待は孤児 identity 行を残す。管理 UI から消せるようにする。）

### 61.9.6 入口の門は「和」にし、そこに membership を含める

入口の判定は **デプロイ全体の許可リスト ∪ 各テナントの `auto_join_domains` ∪ 「membership を持つこと」**。

★ 最後の項が、いま欠けている接続そのもの。現状は招待されていても
`AF_OAUTH_ALLOWED_*`（env）に載っていなければ `authGate` が入口で弾く。ここを繋ぐと
**招待運用のデプロイでは env の許可リストが不要**になり、名簿が membership 1 箇所に寄る。

- 「積」（デプロイ全体にも載っていること）にすると、招待するたびに env にも足す**二重管理**になる。
- 和にしても危険が増えないのは、**入口を通ったことは「どこかに入れる」を意味しないから**
  （§61.9.2 のテナントの門が別にある）。どのテナントにも入れない人は、現行の
  `not_provisioned`（`resolver.go:69`）と同じ画面で止まる。
- ★ **すべて空なら現行どおり全拒否**（fail-closed を維持）。membership も許可リストも
  `auto_join_domains` も無ければ誰も入れない。

#### ★ 改訂（2026-08-14・P3 実装時）— 「和」は *email 軸の中だけ*。しかも 2 項に分ける

初版の書き方（「入口の判定はデプロイ全体の許可リスト ∪ auto_join_domains ∪ membership」）を
P0/P2 の現行構造にそのまま乗せると、**2 通りの壊し方**がある。実装ではどちらも避けた。

**壊し方 1 — 別軸まで和にすると GitHub の org 判定を迂回できる。**
P2 の GitHub は「org メンバーシップ **∧** email 許可リスト」の 2 門 AND（§61.7）。
`provider.Allowed(...) || hasMembership(...)` と素直に和を取ると、
**どこかのテナントに招待されているだけで org 外の人が GitHub から入れる**（決定 2 を壊す）。
→ **和は email 軸の中でだけ取り、種類の違う判定は AND のまま**にする。実装上は
`githubProvider.emailAllowed`（＝門 2）にだけ DB 由来の項を足し、門 1 には触らない。

**壊し方 2 — provider 固有リストを「和」にすると、意図した絞り込みが黙って広がる。**
P0 では provider 固有の許可リストは**共通リストを置き換える**（`hasOwnAllowlist()` なら
`deployAllowed` を見ない）。これは「Google は全社ドメイン、Entra は子会社ドメインだけ」という
**per-provider の絞り込み**として使える仕様で、ここを共通リストとの和にすると
Entra が全社ドメインまで受け入れてしまう。P0 の後退になる。

したがって**採用した形は「置き換え」でも「和」でもなく、項を 2 つに分ける**:

```
入口の email 軸 =
      ( provider 固有リスト  … 設定があれば
      | デプロイ共通リスト    … 無ければこちら )    ← P0 のまま（置き換え）
   ∪  ( auto_join_domains 一致 | membership 保有 )  ← P3 が足す DB 由来の項（常に和）
```

- 「二重管理の解消」（本節の目的）は達成される — **招待された人は、どちらの形式の許可リストを
  使っていても入口を通る**。env に足し直す作業は消える。
- P0 の「未設定なら共通の許可リストを使う」という記述は**そのまま有効**（改訂不要）。
  変わったのは「その結果に DB 由来の項が常に OR される」ことだけ。
- コード上は `deployAllowed`（デプロイ共通リスト・意味は据え置き）と
  `dbAllowed`（auto_join ∪ membership）の**2 本のクロージャ**になる。
  `config.tenantEmailAllowed` が後者で、`mgr.tenantLogin`（短 TTL キャッシュ）を見る。

**この 3 点は回帰テストで固定した**（`tenant_login_test.go`）:
`TestMembershipDoesNotBypassTheGitHubOrgGate` /
`TestProviderOwnAllowlistStillReplacesTheDeploymentWideOne` /
`TestEntryGateAdmitsAnInvitedPersonWithNoDeploymentAllowlist`。

### 61.9.7 置き場は DB（env ではない）

テナントは DB 行で管理 API も既にある（`routes.go:129-137`）ので、規則も `tenant` のカラムにして
admin から編集する。env に `AF_TENANT_<SLUG>_…` を生やす案は、テナントが実行時に増える以上ずれる。

```sql
-- migrations/0039_tenant_login.sql（0038 は §61.5 の identity_provider）
ALTER TABLE tenant ADD COLUMN allowed_providers TEXT NOT NULL DEFAULT ''; -- CSV。空=デプロイの全 provider
ALTER TABLE tenant ADD COLUMN auto_join_domains TEXT NOT NULL DEFAULT ''; -- CSV。自動参加（§61.9.5）
ALTER TABLE tenant ADD COLUMN allowed_domains   TEXT NOT NULL DEFAULT ''; -- CSV。招待時のガードのみ
-- allowed_emails は作らない。「誰が入れるか」の名簿は membership が持つ（§61.9.5）
```

実装注: 現行の `emailAllowed` は `AF_OAUTH_ALLOWED_EMAILS_FILE` 指定時に**毎リクエスト
`os.ReadFile` している**（`oauth_google.go:130`）。つまり「毎リクエスト I/O」は既に許容済みの水準で、
**DB 参照＋短 TTL（30 秒）のメモリキャッシュはこれより軽い**。管理 API の書き込みでキャッシュを
破棄すれば「消したら即効く」性質も落ちない。

### 61.9.8 帰属の優先順位

1. **既に membership がある → それが正**（招待済み＝名簿に載っている人。§61.9.5）。
2. 無く、`auto_join_domains` に一致するテナントがある → そのテナントへ membership 作成。
3. どちらも無し ＋ `AF_PROVISION=invite` → `not_provisioned`（現行どおり）。
4. どちらも無し ＋ `auto` → 既定テナント（現行どおり）。

招待運用（部署分割）は 1 だけで回り、2 は使わなくてよい。逆に単一テナントの会社は
2 だけで回り、招待画面を触らずに済む。

同じドメインを 2 つのテナントが `auto_join_domains` に持つのは設定ミスなので、
**管理 API の保存時に弾く**。それでも競合した場合は決定性のため slug 昇順の先頭を採る
（黙って「どちらか」に入らないよう、監査ログに残す）。

### 61.9.9 段階への位置づけ

この節は **P0（`prov` クレーム）さえ済めば着手でき、P1 / P2 とは独立**。GitHub を入れない会社でも
「部署ごとにテナントを分け、Entra 限定にする」だけで価値が出る。

### 実装メモ（P3 で実際にこうした）

- **migration は `0039_tenant_login.sql`（pg `0022`）**。`tenant` に 3 カラム（CSV・
  `NOT NULL DEFAULT ''`）。`allowed_emails` は作っていない（§61.9.5）。前方互換
  — 旧 CP バイナリは知っているカラムだけ SELECT するので、バイナリだけ戻しても動く。
- **キャッシュは `tenant_login.go` の `tenantLoginCache`**。テナント規則は 30 秒 TTL の
  スナップショット 1 本、membership の有無は**アドレス単位**で同じ TTL。
  ロスター全体を毎回読まないので、常駐メモリはログインしている人数に比例する。
  管理 API の書き込み（規則保存・招待・除名）で**必ず破棄**する。
  DB を一時的に読めないときは**古いスナップショットを使う** — ここで「誰もメンバーでない」に
  倒すと、入口を閉じるだけでなく auto_join が membership を二重に作りかねない。
- **`prov` は `sub` が無くても context に載せる。** P0 以前の cookie は `sub` を持たないが
  provider は分かる（暫定規則で `google`）。identity 解決は従来どおり `sub` があるときだけ
  pair を使い（`loginRefFrom`）、テナントの門は `sessionProviderFrom` で provider だけを見る。
- ★ **`EnsureMembership` は「復活」させない。** auto_join / `AF_PROVISION=auto` の自動採番経路が
  同じ関数を通るので、ここで `status='active'` に戻すと**除名した人が次のログインで自動的に
  戻ってしまう**（§61.10.6 の運用が成立しない）。復活は招待 API（`addMembership`）が
  明示的に行う。あわせて **auto_join は「行が存在するなら（inactive でも）参加させない」** —
  §61.9.8 の規則 1「membership があればそれが正」には「外した」という答えも含まれる。
- **`provider_required` は `{code,message}` のまま**返す。再サインインのリンクに要る
  provider id は、Console が `/api/tenants` の `allowed_providers`（membership ごとに追加）
  から引く。`apiError` に details を足す案は、**positional な composite literal が
  249 箇所ある**ため見送った（1 フィールド足すだけで全部コンパイルエラーになる）。
- **`/login/<slug>` は `GET /login/{slug}` の 1 ルート**。未知の slug は generic ページを返し、
  **tenant を先へ持ち回さない**（誤字が存在しない部署に固定されないように）。
  ログイン後は `next` に `?tenant=<slug>` を足すだけ — Console はそれを
  **サーバが返した membership の中にある場合だけ**初期選択に使う（決定 14）。

## 61.10 運用イメージ（部署分割の一連の流れ）

例: acme 社が 1 デプロイを持ち、中を **営業（sales）/ 開発（dev）/ 情シス（it）** に分ける。
IdP は Entra ID、email は全社共通の `@acme.co.jp` だけ（＝ドメインでは部署を分けられない）。
以下は **P0＋P3 まで実装した後**の姿。

### 61.10.1 設置（情シス・1 回だけ）

Entra 側にアプリ登録を **1 つ**。リダイレクト URI は `https://af.acme.co.jp/oauth2/callback` の
**1 本だけ**で、部署テナントを何個増やしても増えない（§61.6・決定 8）。

```sh
AUTH=oauth
PUBLIC_BASE_URL=https://af.acme.co.jp
AF_COOKIE_SECRET=<base64-32-bytes>
AF_OIDC_PROVIDERS=entra
AF_OIDC_ENTRA_ISSUER=https://login.microsoftonline.com/<tenant-guid>/v2.0   # common にしない
AF_OIDC_ENTRA_CLIENT_ID=... / AF_OIDC_ENTRA_CLIENT_SECRET=...
AF_OIDC_ENTRA_TRUST=issuer
SUPER_ADMIN_EMAILS=joho@acme.co.jp
AF_PROVISION=auto            # ★ 最初は auto。招待運用へ切り替えるのは 61.10.2 の後
```

★ **`AF_OAUTH_ALLOWED_*` は書かなくてよい**（P3 後）。招待した人は membership で入口を通るため
（§61.9.6）。ここが「名簿を 1 箇所に寄せる」の実利。

★ **`super_admin` はここ（ホスト側の env）で決めたままにする。** Console からの昇格は作らない。
デプロイ全体を動かせる権限は、**そのデプロイを設置した人＝ホストのファイルに触れる人**だけが
持つべきで、アプリ内の操作で増やせるようにすると設置者以外が権限を作れてしまう。
`SUPER_ADMIN_EMAILS`（`main.go:85`）は **env で、起動時に 1 度だけ読む** —
許可リストの `AF_OAUTH_ALLOWED_EMAILS_FILE` のような live-read ではないので、
**変更には CP の再起動が要る**（実体は compose の `.env` / `oauth.env` / ECS の SSM パラメータ）。

★ **ただし剥奪ができない（現状の穴）。** `UpsertIdentity` は
「Upgrade (never downgrade) the deployment role」（`store_sqlite.go:314-317`）で、
`SUPER_ADMIN_EMAILS` から消して再起動しても **`identity.role` は `super_admin` のまま残る**。
テナント側の `setMembershipRole`（`tenants.go:434`）が触るのは `member`/`tenant_admin` だけで、
デプロイ役割を降格する API は無い。＝**DB を直接触る以外に外す手段が無い**。
→ **P3 の作業項目**: env を単一の正にし、ログイン時に「リストに無ければ `user` へ落とす」。
実装注意 — **`UpsertIdentity` の `roleHint` に降格を持たせてはいけない**。
`addMembership`（`tenants.go:285`）・`cleanHome`（`:195`）・`stopWorkspace`（`:149`）は
`roleHint=""` で呼ぶので、管理者が誰かをテナントに追加しただけで super_admin が降格してしまう。
**本人の email が確定しているログイン経路（`identityFor` / `resolveFull`）だけ**が
役割を同期するよう、専用の store メソッドに分ける。

### 61.10.2 最初の 1 人（＝ブートストラップ）

1. 情シスの人が `https://af.acme.co.jp/` を開く → Entra でサインイン → `auto` なので既定テナントに入る。
2. `SUPER_ADMIN_EMAILS` に載っているので `super_admin`（`resolver.go:29`）。アカウントメニューに管理が出る。
3. 管理 → テナント作成で `sales` / `dev` / `it` を作る。
4. 自分を `it` に `tenant_admin` として招待し、各部署の責任者も `tenant_admin` で招待する
   （**`tenant_admin` を付けられるのは `super_admin` だけ** — `tenants.go:280`）。
5. `AF_PROVISION=invite` に変えて CP を再起動。以降、招待されていない人は入れない。

★ **いきなり `invite` で立ち上げてはいけない。** コードを追う限り、membership を 1 つも持たない人は
`GET /api/tenants` が 403 `not_provisioned` を返し（`resolver.go:69`）、Console は
`data.error` 分岐で `superAdmin` を立てないまま終わる（`tenant.ts:93-100`、既定 `false`）。
その結果、管理メニューの表示条件 `superAdmin || tenant_admin`（`TopBar.tsx:319`）が偽になり、
**`super_admin` でも管理画面に到達できない**。API 直叩き（`POST /api/admin/tenants` は
`identityFor` だけで通る）なら可能だが、運用手順としては成立しない。
→ ✅ **P3 で解消**: `tenantAPI.list` が `not_provisioned` を受けたとき、相手が `super_admin` なら
`{tenants: [], super_admin: true}` を 200 で返す（`tenants.go`）。それ以外の人には従来どおり
403 `not_provisioned` のまま。**つまり `AF_PROVISION=invite` で最初から立ち上げてよくなった**
（上の 5 手順の「`auto` で立ち上げてから切り替える」は不要）。

### 61.10.3 部署を 1 つ増やす（情シス）

1. 管理 → テナント作成（`slug=sales` / 名前=営業部）。
2. そのテナントの設定（P3 で追加する面）:
   - 使える認証方式 = Entra のみ（`allowed_providers`）
   - 招待ガード = `@acme.co.jp`（`allowed_domains`。他社ドメインを誤って招待しない保険）
   - 専用ログイン URL = `https://af.acme.co.jp/login/sales`
3. 営業部長を `tenant_admin` で招待。以降のメンバー追加は部長が自部署だけできる
   （`tenantAdminFor` — `resolver.go:38`）。

### 61.10.4 新しい人が入る（部長の操作 → 本人）

1. 部長: 管理 → メンバー追加に `yamada@acme.co.jp` / role=member を入れて送信。
   この時点で **identity 行と membership が先に作られる**（本人はまだ一度もログインしていない。
   `tenants.go:285-291`）。
2. 部長: 本人に `https://af.acme.co.jp/login/sales` を伝える。
3. 本人: その URL を開くと **Entra のボタンだけ**が出る（`allowed_providers`）。サインイン。
4. 入口の門は membership があるので通る。テナントの門も membership があるので通る。
   `sales` の workspace が初回アクセスで作られる。
5. ★ **情シスは何もしていない**（env も再起動も無し）。これが現状との一番の違い
   — 今は `AF_OAUTH_ALLOWED_EMAILS_FILE` に 1 行足す作業が別に要る。

### 61.10.5 兼務（1 人が 2 部署）

- `sales` と `dev` の両方に招待する。ログイン後、Console のテナント切替に 2 つ出る
  （`tenant.ts:107-110` は 2 件以上でピッカーを出す）。
- ★ **workspace は membership 毎＝部署毎に別**（home も secrets も別・[dev/07 §7.2](dev/07-security.md)）。
  同じ人でも営業の作業と開発の作業は混ざらない。これは既存の性質で、部署分割と相性が良い。
- 部署で使える認証方式が違う場合（例: `dev` だけ GitHub 可）、切り替え時に
  `provider_required` で再サインインを促す（§61.9.4）。

### 61.10.6 異動・退職 — ★ ここに現状の穴がある

運用としては「**名簿から外す**」＝ membership を消す（または無効化する）だけのはずだが、
**その API が存在しない**。`MembershipStore`（`store.go:383-402`）にあるのは
`EnsureMembership`（挿入のみ）/ `SetMembershipRole` / 各種 Get だけで、削除も無効化も無い。
`routes.go` にも `DELETE /api/admin/memberships` は無い。

- **今そうなっている理由**: 現行のオフボーディングは **env / ファイルの許可リストから消す**ことで、
  `authGate` が毎リクエスト再判定して即座に締め出す設計だった（`oauth_google.go:299-309`）。
  membership は「入れる人の名簿」ではなく「入った人の記録」でよかった。
- **P3 で名簿を membership に寄せると、これが効かなくなる。** IdP 側で無効化しても
  **署名済みセッション cookie は最大 `AF_SESSION_TTL`（既定 168h＝7 日）有効**なので、
  membership を消せないと最大 7 日間アクセスが残る。
- ★ **したがって P3 のスコープに membership の削除／無効化 API と管理 UI を必ず含める。**
  これが無いと部署分割の運用は回らない（異動＝旧部署から外す、退職＝全部から外す、が実行できない）。
  `Membership` には `Status` があり `GetMembershipByID` も「missing/inactive」を想定しているので、
  **論理削除（`status='inactive'`）で足りる** — workspace / home を残したまま締め出せる。
  **実行できるのは `tenant_admin`（自テナント分）と `super_admin`**（下記の責務分担に合わせる）。

→ ✅ **P3 で実装**: `DELETE /api/admin/memberships {tenant_slug,user_key}`（`tenantAdminFor` ゲート）。
Console は管理 → メンバー詳細の「メンバーを外す」。実装で足した判断が 3 つある。

- **自分自身は外せない**（`self_removal`）。UI からの誤クリックに製品内の取り消し手段が無く、
  残る復旧経路は他の管理者かホストの env になるため。
- **戻すのは「もう一度招待する」**。`EnsureMembership` は復活させない（上の実装メモの理由）ので、
  招待 API だけが `status='active'` に戻す。
- **除名は監査に残す**（`membership.remove`）。`clean-home` も tenant_admin へ開いたので
  `workspace.clean_home` を追加した（決定 26 の「権限を広げるなら監査」）。

実装中に見つけた**追随が要る 2 箇所**（どちらも「active であること」の確認漏れ）:

- ★ **`tenantAdminFor` が status を見ていなかった。** `GetMembership` は inactive な行も返す
  （オフボーディングの残り 2 手が必要とするので、これは正しい）。status を見ないと、
  **外された tenant_admin が手元の cookie（最大 `AF_SESSION_TTL`）で管理を続けられ、
  自分を名簿に戻せる** — §61.10.7 の穴 2 そのもの。`mem.Status == "active"` を足した。
- ★ **`ListMembersByTenant` は active のみなので、除名した相手が管理画面から消えて
  「停止 → home 削除」に到達できなくなる。** ただしこの関数は共有先の候補・MCP の
  admin ツール・セッション一覧でも「このテナントのメンバーは誰か」として使われており、
  そこに外した人を混ぜてはいけない。よって**別メソッド `ListRemovedMembersByTenant` を足し、
  管理 API の名簿だけが両方を出す**（`status: "active" | "removed"` を付けて返す）。

#### workspace / home の削除は tenant_admin の責務

部署の人員を把握しているのは部署側なので、**外した人の workspace / home を消すのは
tenant_admin の仕事**とする。情シス（super_admin）に毎回頼む形にしない。

- ★ **現状 `clean-home` は super_admin 限定**（`routes.go:136` の `adm.withSuperAdmin(adm.cleanHome)`。
  ハンドラ自身も `_ Identity` を受けるだけで、テナント所属を見ていない）。
  → **P3 で `tenantAdminFor` によるハンドラ内ゲートへ付け替える。**
  同じ形の前例が既にある: `stopWorkspace` は `adm.tenantAdminFor(w, r, body.TenantSlug)`
  で自テナントに絞っている（`tenants.go:145`）。同じ形にすれば、tenant_admin は
  **自部署のメンバーの home しか消せない**。
- 順序は「membership を無効化 → workspace 停止 → home 削除」。
  停止（`stopWorkspace`）は既に tenant_admin ができるので、揃うのは `clean-home` だけ。
- 即時削除にしない扱い（棚 / 猶予）は [45-deletion-lock](45-deletion-lock.md) と掃除の段階制に合わせる。

### 61.10.7 `super_admin` の移譲・退職

`super_admin` はホスト側の env（決定 24）なので、**移譲そのものはホストのファイルを書き換えて
再起動するだけ**。難しいのは前任を確実に落とす側で、ここに 2 つの穴がある。

#### 手順

1. **情シス（ホストに触れる人）**が `SUPER_ADMIN_EMAILS` を書き換える（後任を追加・前任を削除）→ CP 再起動。
2. **後任**がログインする。`roleHintFor`（`resolver.go:29`）→ `UpsertIdentity` で `super_admin` に昇格。
   これは初回ログインで自動的に効く。
3. **前任を締め出す**: IdP 側でアカウント無効化 ＋ 全テナントの membership を無効化（P3・決定 22）。
4. **前任の workspace / home** を、所属していたテナントの tenant_admin（または後任 super_admin）が
   停止 → home 削除（決定 26）。★ **その前に本人に push させる** — home が消えると `~/repos` の
   未コミット作業は戻らない。
5. **membership に紐づく資産を引き継ぐ**（下記）。

#### 前任が持っていたものはどうなるか（実測）

| 資産 | 紐づき | 移譲時の扱い |
|------|--------|------------|
| **内部 git リポジトリ** | **`git_repo.tenant_id`＝テナント所有**（`created_by` は監査用の membership id だけ・`migrations/0014_git_repo.sql:8-16`） | ★ **消えない。** 人が抜けてもチームのリポジトリは残る |
| **定時実行** | `Schedule.MembershipID`（`store.go:150`）＝**個人所有** | ★ **止まる。** 前任が仕込んだ夜間ジョブは後任が作り直す。退職前の棚卸し項目 |
| workspace / home（BYO トークン含む） | membership | 引き継がない。後任は自分の workspace を持つ（暗号化された secrets は本人の鍵に紐づく） |
| メモ / SSM ブックマーク / セッション共有 | membership | 個人資産。消えてよい |

#### ★ 穴 1: DB 上の `super_admin` が落ちない

決定 24 の修正を「**ログイン時に同期**」で作ると、**二度とログインしない前任は
`identity.role='super_admin'` のまま DB に残る**。退職者はまさにログインしないので、
この形の修正では退職ケースが直らない。

→ **起動時に一括同期する**（決定 24 を強化）。CP 起動時に env を読んだ直後、
`SUPER_ADMIN_EMAILS` に無い `super_admin` を `user` に落とす。移譲手順の 1（再起動）と
同じタイミングで効くので運用と噛み合い、`roleHint` を経由しないので
「`addMembership` が `roleHint=""` で呼んで降格させてしまう」罠（§61.10.1）も構造的に回避できる。

→ ✅ **P3 で実装**: `store.DemoteSuperAdmins(ctx, keep)` を `main.go` の起動直後に 1 回。
`UpsertIdentity` の「never downgrade」は**維持したまま**別メソッドにしたので上の罠は起きない。
★ 実装で 1 つ絞った — **`email` が空の identity は降格しない**。`SUPER_ADMIN_EMAILS` は
email の列挙なので、email を持たない行（`AUTH=dev` の固定ユーザー等）を落とすと
**文書化された手順（env を書いて再起動）では戻せなくなる**。落とした相手は CP ログに出す。

#### ★ 穴 2: セッションを即時失効させる手段が無い

セッション cookie は **stateless**（HMAC over `{email, exp}`・`oauth_google.go:85-93`）で、
サーバ側にセッションストアが無い。個別失効も「全端末からログアウト」も存在しない。
いまはそれで足りていた — **許可リストから消せば `authGate` が毎リクエスト弾く**（`:299-309`）
のが実質の失効機構だったため。

P3 で名簿を membership へ寄せると、その役割は「membership を無効化する」が担う。
**ただし `AF_OAUTH_ALLOWED_DOMAINS` を併用しているデプロイでは塞がらない**:

> 前任は membership を全部外されても、**ドメイン一致だけで入口を通る**（§61.9.6 の和）。
> 管理 API は `identityFor` だけで通り membership を要求しない。
> `UpsertIdentity` は降格しないので `role` は `super_admin` のまま
> → `withSuperAdmin` を通過 → **`POST /api/admin/memberships` で自分を復帰させられる**。
> IdP 無効化で新しいセッションは取れないが、**手元の cookie は最大 `AF_SESSION_TTL`（既定 168h）有効**。

対策は 2 段:

- 穴 1 の**起動時同期**で `role` を落とす（再起動を挟む移譲手順ではこれが効く）。
- 即時に切りたい場合の最終手段として **`AF_COOKIE_SECRET` のローテーション**を runbook に書く。
  署名鍵が変われば全 cookie が無効になる（＝全員が再ログイン）。乱暴だが、
  stateless セッションで唯一の即時 kill switch。**現状どこにも文書化されていない。**

#### 前任が唯一の `super_admin` のまま退職した場合

`super_admin` がホスト側 env である以上、**ホストに触れる人が env を書き換えて再起動すれば復旧できる**
（決定 24 の副次的な利点 — アプリ内に閉じていたら詰んでいた）。
逆に言えば **ホストに触れる人が社内に 1 人も居ない状態を作らない**ことが唯一の前提条件で、
これは runbook に書く。

### 61.10.8 責務分担のまとめ

| やること | 誰が | 触る場所 | 再起動 |
|---------|------|---------|-------|
| 設置 | 情シス（ホスト） | env（IdP 1 つ分）＋ Entra のアプリ登録 1 つ | 初回のみ |
| **`super_admin` の指定** | **情シス（ホスト）** | **`SUPER_ADMIN_EMAILS`（env・ホスト側のファイル）** | **要** |
| IdP を増やす | 情シス（ホスト） | env に `AF_OIDC_PROVIDERS` を 1 つ足す | 要 |
| 部署テナントを作る | super_admin | 管理 → テナント作成＋設定（`routes.go:133`） | 不要 |
| 部署の管理者を任命 | super_admin | 管理 → メンバー追加で `tenant_admin`（`tenants.go:280`） | 不要 |
| 人を入れる | tenant_admin | 管理 → メンバー追加（member） | 不要 |
| 招待 URL を伝える | tenant_admin | **口頭 / 社内チャットなど CP の外**（§61.10.4） | 不要 |
| 異動 | tenant_admin | 新部署に追加＋旧部署の membership 無効化（**P3 で追加**） | 不要 |
| 退職 | tenant_admin ＋ IdP 側 | membership 無効化 → workspace 停止 → home 削除（**`clean-home` の再ゲートは P3**） | 不要 |

**線引き**: ホスト（＝設置者）が握るのは「デプロイ全体に効くもの」だけ — IdP の設定と
`super_admin` の指定。テナントの中の人と物（メンバー・workspace・home）は tenant_admin が持つ。
super_admin が毎日の運用に出てくるのは、部署テナントの新設と管理者の任命だけになる。

## 61.11 テナント定義の認証方式（P4）— 子会社ごとに Entra が違う場合

§61.9 は「1 社の中を部署で分ける・Entra テナントは 1 つ」という例で書いた。しかし
**グループ各社**（[roadmap](roadmap.md) §12.2 の本来の想定）や分社・M&A の途中では、
**テナントごとに Entra テナント自体が違う**（tenant guid が違う＝issuer が違う＝アプリ登録も
client_id / secret も別）。「部署」と「別法人」の境目は運用上つながっている。

### 61.11.1 P0 でも「並べる」だけならできる（実測）

provider id は任意の文字列で、issuer / client_id / secret / `ALLOWED_TIDS` / 許可リストは
**すべて provider 単位**なので、Entra を 2 つ env に並べれば動く。実測済み:

```sh
AF_OIDC_PROVIDERS=entra_sales,entra_sub
AF_OIDC_ENTRA_SALES_ISSUER=https://login.microsoftonline.com/<guid-acme>/v2.0
AF_OIDC_ENTRA_SUB_ISSUER=https://login.microsoftonline.com/<guid-sub>/v2.0
# → providers=[entra_sales entra_sub]、ログイン画面にボタン 2 つ
```

P3 の `allowed_providers` がこれを選び分ければ「テナント A は EntraA、テナント B は EntraB」は
成立する。**新しい機構は要らない。** ただしこの形には 3 つの穴がある。

| 穴 | 内容 |
|----|------|
| ★ 部署追加が再起動作業に戻る | §61.10.3〜4 は「テナント新設は super_admin・再起動不要／情シスは何もしていない」と書いたが、**新テナントが自前の IdP を持つ場合は env 追加＋CP 再起動**が要る（§61.10.8 の責務分担表とは整合するが、運用イメージとは矛盾する） |
| ボタンが見分けられない | 素の `/login` は有効な IdP を全部出す（§61.9.3）ので「Microsoft でサインイン」が 2 つ並ぶ。`LABEL_JA` で `Microsoft（営業部）` と書き分けられる（P0 で対応済み）が、**英語ラベル未設定時の自動生成は `Sign in with Entra_sales`** になり、そのままでは出せない |
| `allowed_providers` の既定が緩い | 「空＝デプロイの全 provider」は issuer が 1 つの前提。**複数 issuer のデプロイでは `allowed_providers` を必須**にする方が意図に合う |

### 61.11.2 ★ tenant_admin に「認証方式の追加」を単独でやらせてはいけない

秘密を DB に持つこと自体は既に前例がある（§61.11.4）。問題は**誰が有効化できるか**で、
ここが本節の核心。プロジェクト MCP（[48](48-mcp-registry.md)）は「そのテナントのエージェントが
**外へ**叩きに行く先」だが、**IdP は「誰であるか」を宣言する主体**で、追加はデプロイ全体に効く。

現行コードでの乗っ取り経路（すべて実測）:

1. `identity.user_key = sanitizeUser(email)`（`resolver.go:281`）は**デプロイ全体で 1 つの名前空間**。
2. デプロイ役割は **email の一致**で決まる — `roleHintFor`（`resolver.go:28`）が `SUPER_ADMIN_EMAILS` と
   突合し、ログインのたびに `identityFor` → `UpsertIdentity` で反映される。
3. よって **tenant_admin が自分の支配下の IdP（自前 Keycloak を立てれば済む）を登録できると、
   `email=<情シスの super_admin>` を主張するトークンを自分で発行し、自テナントのボタンから
   サインインできる。** `authGate` はそれを通し、`UpsertIdentity` が role を `super_admin` に上げる。
4. しかも **`UpsertIdentity` は never downgrade**（`store_sqlite.go:314-317`）なので、
   不正な provider を後から消しても **role は残る**。
5. `trust: "issuer"` は防波堤にならない。「issuer を固定したから信じる」の issuer が攻撃者自身。

★ **悪意が無くても起きる。** tenant_admin がセルフサインアップ有効な Auth0 テナントを善意で登録した
瞬間、自テナントではなく**デプロイ全体**が開く。

つまり **「設定行はテナント所有でよいが、*有効化*はデプロイ全体の行為」**。
§61.9.2 で引いた「入口の門とテナントの門を混ぜない」線の、**入口側**に属する。

### 61.11.3 決めごと — 保存と入力はテナント、有効化は super_admin の承認

- tenant_admin が自テナントの認証方式を Console で作成・編集する（`client_secret` もここで入力し、
  テナント鍵で封印して保存）。作成直後の状態は **`pending`**。
- **super_admin が承認して `active`。** 承認前は**ログイン画面にボタンが出ず、セッションも発行しない**。
  子会社の受け入れは 1 回きりなので運用負荷はほぼゼロで、「日常の運用に情シスが出てこない」
  （§61.10.8）は保てる。
- 無効化（`suspended`）は tenant_admin も super_admin もできる。**止める方向は誰でも早く**打てる。
- 承認を省略できる env（`AF_ALLOW_TENANT_IDP=1` のような opt-out）は**作らない**（§61.13）。

これと**必ずセットで**要る 4 点。承認モデルでも省略できない。

1. ★ **テナント定義の provider からは `super_admin` を取れない。** `roleHintFor` は
   **env 由来 provider のログインにだけ**効かせる。承認を通った後でも、IdP の管理者は
   その会社の情シスであってこのデプロイの設置者ではない。
2. ★ **P1（`identity_provider`・`0038`）が前提条件。** テナント定義の provider は
   `(provider, subject)` で identity を作り、**§61.5 の「email が一致したら既存 identity へ結合」を
   無効化する**。これが無いと、email を騙るだけで既存の identity＝home＝secrets を乗っ取れる。
   **順序として P1 が先**で、ここは P2 より強い依存。
3. **テナント定義の provider は自テナントにしか入れない。** セッションの `prov` を
   `resolveFull` で突合し、他テナントには `provider_required`（§61.9.4）。入口の許可リストも
   そのテナントのものを使い、デプロイ共通リストへはフォールバックしない。
4. **素の `/login` には出さない**（`/login/<slug>` 限定）。全子会社のボタンが未認証者に並ぶと
   組織構成が漏れる（§61.9.3 の「未知 slug は 404 にしない」と同じ配慮）。

### 61.11.4 秘密の保存 — 前例は `mcp_server`

「テナント管理者が UI で入力した秘密を、テナント鍵で封印して DB に置く」は
[48](48-mcp-registry.md) で既に production の形になっている。そのまま踏襲する。

| | 実装（実測） |
|---|---|
| 保存 | `mcp_server.headers_enc` ＋ `key_ref`（`store.go:203-217`） |
| 封印 | `sealHeaders` → `custodian.Wrap(tenantID, …)`（`mcp_server.go:146`）。AES-256-GCM・**AAD = keyRef でテナントに束縛**（`custodian.go`） |
| 書き手 | tenant_admin（`adminUpsert` → `tenantAdminFor`・`mcp_server.go:409`） |
| 読み出し | UI へは `***` でマスク、更新時は `mergeHeaders` で**触っていない値が生き残る** |
| 復号不能 | 空マップにせず `mcp_headers_unreadable` で明示エラー（「全部入れ直せ」と言う） |

正直に添える限界が 2 つ:

- `localCustodian` の KEK は `HMAC(master, "af-kek:"+tenantID)` なので、**`AF_MASTER_KEY` を持てば
  全テナント分が開く**（[dev/07 §7.6](dev/07-security.md)）。テナント間の暗号学的分離ではない。
- env から DB へ移すと、秘密が **`DATA_DIR`＝バックアップの中**に入る（env は外）。
  `AF_MASTER_KEY` をデータ領域の外に置く既存ルールが守られていれば中身は暗号文のままだが、
  posture は変わる。MCP のトークンで既に受容済みの水準ではある。

### 61.11.5 スキーマと名前空間

```sql
-- migrations/0040_tenant_idp.sql（0038 は §61.5、0039 は §61.9.7）
CREATE TABLE tenant_idp (
  id            TEXT PRIMARY KEY,
  tenant_id     TEXT NOT NULL REFERENCES tenant(id),
  name          TEXT NOT NULL,            -- テナント内の provider id（"entra" 等）
  label_ja      TEXT NOT NULL DEFAULT '',
  label_en      TEXT NOT NULL DEFAULT '',
  issuer        TEXT NOT NULL,
  client_id     TEXT NOT NULL,
  secret_enc    TEXT NOT NULL DEFAULT '', -- client_secret（テナント鍵で封印・§61.11.4）
  key_ref       TEXT NOT NULL DEFAULT '',
  trust         TEXT NOT NULL,            -- email_verified | issuer（§61.4・既定なし）
  allowed_tids  TEXT NOT NULL DEFAULT '', -- CSV
  allowed_domains TEXT NOT NULL DEFAULT '',
  status        TEXT NOT NULL DEFAULT 'pending', -- pending | active | suspended
  approved_by   TEXT NOT NULL DEFAULT '', -- 承認した super_admin の identity_id（監査）
  approved_at   TEXT,
  created_at    TEXT NOT NULL,
  updated_at    TEXT NOT NULL,
  UNIQUE (tenant_id, name)
);
```

★ **provider id の名前空間を分ける。** env 由来は今までどおり `entra`、DB 由来は
**`t:<tenant-slug>:<name>`** とする。混ぜると、テナントが `google` という名前で行を作った瞬間に
env の Google を上書きできてしまう。`sessionClaims.prov` にもこの形で入るので、
`resolveFull` は prefix を見るだけで「これはテナント定義か」を判定できる。

`common` / `organizations` issuer ＋ `ALLOWED_TIDS` 空の禁止（決定 7）は **API のバリデーションとして
同じく効かせる**。env 側は起動時 fatal だが、DB 側は保存時 400 で弾く（実行中に落とせないため）。

### 61.11.6 UI

- **テナント詳細に「認証方式」欄**（tenant_admin が見える）。追加フォームは issuer / client_id /
  client_secret / trust / ラベル ja・en / 許可ドメイン / tid。保存すると `pending` で並ぶ。
- **super_admin 側に承認キュー**（管理タブ）。「どのテナントが、どの issuer を、誰の申請で」を出し、
  承認・却下する。**承認は監査ログに必ず残す**（`approved_by` / `approved_at` はその写し）。
- 承認済み行の secret は `***` 表示。編集して空のままなら既存値を維持（`mergeHeaders` と同じ規則）。
- ★ **issuer を変更したら `pending` へ戻す。** 承認は「この issuer を信じてよい」に対して与えたもので、
  issuer が変われば承認の対象そのものが変わる。client_id / trust の変更も同じ扱いにする。

### 61.11.7 段階への位置づけ

**P4。P1（`0038`）が前提**で、P3 とは独立に見えるが `/login/<slug>` と `allowed_providers` の
強制（P3）が無いと 61.11.3 の 3・4 が成立しないため、**実質 P1 → P3 → P4** の順になる。
P4 を入れない会社は §61.11.1 の env 併記で足りる（再起動が要るだけ）。

### 61.11.8 実装メモ（P4 で実際にこうした）

**provider 集合を実行時に足し引きできる形へ変えた。** `buildLoginProviders` は起動時に 1 回走り、
`config` は**値でコピーされて**ハンドラに渡るので、そこに置いた集合は再起動しないと変わらない。
承認・停止が即座に効くことは決定 29 の前提なので、**DB 由来だけを `manager`（ポインタ）側の
`tenantIdPRegistry` に置き、env 由来は起動時固定のまま**にした（`tenant_idp.go`）。
キャッシュの作法は P3 の `tenantLogin` をそのまま踏襲 — 30 秒 TTL、管理 API の書き込みで破棄、
**DB が読めないときは古いスナップショットを使う**（一時的な DB 不調で全子会社がログアウトしない）。
行が壊れていて provider を組めない場合は、その行だけ落として**警告を出す**（1 つの子会社の
設定ミスで他社が止まらない・決定 11）。UI 側は「承認済みだが不備あり」を別状態として出す —
そうしないと「承認されたのにボタンが無い」が「まだ承認されていない」と区別できない。

★ **承認前の拒否は callback で行う。** ログイン画面のボタンを出さないのは表示の話で、
`/oauth2/login?provider=t:<slug>:<name>` は誰でも叩ける。`providerFor` が registry（＝ active 行のみ）を
引くので、pending / suspended は authorize も callback もセッション発行も通らない（決定 14 の型）。
`sessionAllowed` も同じ経路なので、**停止すると既存セッションが TTL 内に失効する**。

★ **改訂 — 「email 一致で結合を無効化」だけでは塞がらなかった（決定 32 の実装時の発見）。**
§61.11.3-2 は「テナント定義 provider は `(provider, subject)` で identity を作り、規則 2 を
無効化する」と書いたが、**規則 2 を切って規則 3（新規作成）へ落とすと同じ行に戻ってくる**:
`user_key = sanitizeUser(email)` なので `UpsertIdentity` の `ON CONFLICT(user_key)` が既存の
identity を更新して返し、しかも `identity.email` は UNIQUE（`idx_identity_email`）なので
別行を作ることもできない。つまり「無効化」は実装できず、**拒否**にするしかない。採用した形:

| 順 | 規則 | 挙動 |
|----|------|------|
| 1 | `(provider, subject)` が既知 | その identity（従来どおり） |
| 2' | email が既存 identity のもので、**その identity が一度もサインインされていない**（`identity_provider` 行が無い＝招待の placeholder） | **claim する**。招待 → 初回ログインという子会社受け入れの本線がこれ |
| 2'' | email が既存 identity のもので、**ログイン実績がある** | **拒否**（`email_taken`）。セッションは発行しない |
| 3 | どれでもない | 新規 identity |

拒否はログイン画面のエラー（「このメールアドレスは、すでに別のサインイン方法で使われています」）で
返す。API 側にも `identityErr` で 403 `email_taken` への写像を置いたが、正常系では callback で
止まるのでそこには来ない。

★ **改訂 — 入口の門は「そのテナントの許可リスト」ではなく `allowed_domains` 必須にした
（§61.14 の未決 1 の答え）。** 決定 32-3 のとおり `deployAllowed` は nil にした（デプロイ共通リストへ
フォールバックしない）。残る「`dbAllowed`（membership）を足すか」は**足さない**を選び、代わりに
**行の `allowed_domains` を保存時必須（400）**にした。理由は「承認したのに誰も入れない」の回避より
むしろこちらが本命で、**allowed_domains はその issuer が名乗ってよいアドレスの範囲を縛る唯一の
手段**だから。空にできると、承認された子会社の IdP が親会社のアドレスを名乗れてしまう
（identity は email でデプロイ全体に 1 つなので、これは決定 30 の乗っ取りの続き）。
あわせて **1 ドメイン 1 テナント**（他テナントの行が主張済みなら 409）を保存時に効かせた —
`auto_join_domains` の §61.9.8 と同じ規則で、根拠も同じ。

★ **承認のやり直し条件を 2 つ足した。** §61.11.6 は issuer / client_id / trust の変更で `pending` へ
戻すと書いたが、**`allowed_domains` / `allowed_tids` の「拡大」も戻す**ことにした。承認は
「この issuer を**この範囲で**信じてよい」に対して与えたものなので、範囲が広がれば対象が変わる。
縮小は戻さない（入れる人を減らして危険にはならない）。`client_secret` の更新も戻さない —
同じ issuer・同じアプリ登録であり、鍵のローテーションのたびに再承認を要求すると
**ローテーションしなくなる**方が高くつく。

★ **`tenant.allowed_providers` にテナント自身の `t:<slug>:<name>` を書けるようにした。**
P3 の保存時検証は env 由来の provider id しか通さなかったので、そのままだと
**子会社が「自社 Entra だけ」に絞れない**（P4 の主目的が達成できない）。自テナントの行に
存在する名前だけを許し、他テナントの id は拒否する（テナントの門がどのみち弾くので、
書けても意味の無いボタンが増えるだけ）。

★ **`roleHintFor` の抑止は呼び出し側 3 箇所ではなく `upsertIdentity` の 1 箇所に置いた**（決定 31）。
`identityFor` / `resolveFull` / `resolveMembership` はいずれも `roleHintFor(email)` を渡すので、
3 箇所に同じ規則を書くと 4 箇所目で漏れる。callback の `linkAfterLogin` だけは
`store.LinkIdentity` を直接呼ぶので、そこにも同じ抑止を書いてある。

★ **改訂 — 置き場を「テナント設定」モーダルへ移した（2026-08-14・Console IA 刷新）。**
§61.11.6 は 2 つの面を「テナント詳細（tenant_admin）」「管理タブ（super_admin）」と書いたが、
前者の実体は super_admin の管理モーダルの中にあり、tenant_admin は自分のものを見るために
デプロイ管理者の画面へ入る必要があった。読み手ごとに面を分けた:

| 面 | 誰が | どこ |
|----|------|------|
| サインイン方式（自テナントの行の作成・編集・停止・削除） | tenant_admin | **テナント設定**モーダル（アカウントメニュー） |
| 同じ欄＋**承認して有効化** | super_admin | 管理モーダル → そのテナント（従来どおり。承認は決定 30 の一手なのでここに残す） |
| ログイン規則の 3 欄（編集） | super_admin | 管理モーダル → そのテナント（PUT が `withSuperAdmin` 固定＝決定 19） |
| 同じ 3 欄（**読み取り専用**） | tenant_admin | テナント設定モーダル。招待が弾かれた理由を自分で読めるようにするため、値は見せて操作は置かない |
| サインイン方法の登録簿（デプロイ全体）＋**承認して有効化 / 停止する** | super_admin | 管理モーダル → テナント一覧 |
| デプロイ共通のサインイン方法の一覧（**読み取り専用**） | super_admin | 管理モーダル → そのテナント → ログイン規則のパネル内（欄の直下） |

★ **改訂（2026-08-15）— 承認は登録簿の行から打てるようにした。** 上の表の最終行は当初
「一覧するだけ」で、承認するにはテナント詳細まで降りる必要があった。承認待ちの件数が
見えている場所と、待たせている人が動ける場所が違うのは遠回りでしかない。行の
`tenant_slug` から `POST /api/admin/tenants/{slug}/idp/{id}/status` を組み立てる（テナント詳細の
面と同じ 1 本）。テナント詳細側の面もそのまま残す — あちらは 1 テナントの行を issuer・
受け入れドメインまで含めて読める面で、登録簿は横断の台帳という役割の違いがある。

★ **改訂（2026-08-15）— 管理モーダルを super_admin 専用にした（Console IA 刷新の続き）。**
上の表で「管理モーダル」と書いた行は、どれも super_admin のものだけになった。tenant_admin に
意味のある面（メンバー名簿と詳細・セッション・使用量・監査・MCP 配布）は
**テナント設定モーダルの「運用」グループへ移し**、アカウントメニューの「管理」は
`superAdmin` だけに出す。CP はもともとデプロイ全体の面をすべて `withSuperAdmin` で
閉じていた（テナント作成・上限・ログイン規則の PUT・egress・ホスト・読み上げ辞書）ので、
**閉じたのは入口だけで、権限の実装は何も変えていない**。移した先の面は逆にどれも CP が
tenant_admin へ開いていたもの（`/api/admin/{sessions,usage,audit}`・`/api/admin/mcp-servers*`・
per-member の stats / sessions・`clean-home`・membership の DELETE）。
テナント全体の上限は編集できないが**読めた**（管理モーダルのテナントカード）ので、
テナント設定の名簿の頭に読み取り専用で残した。

★ 実装は 1 つ（`console/src/features/settings/tenantLogin.tsx`）を両モーダルから差す形にした。
**出し分けは props（`isSuper`）だけで、サーバ側のゲートは何も緩めていない** — 承認は CP の
`setStatus` が `ident.Role != "super_admin"` を見て 403 を返す。`isSuper` の出どころも従来と同じ
`GET /api/admin/tenants` の `super_admin` フラグのまま（面を分けても取得元を変えない）。
移設したパネルの i18n キーは `admin.*` のまま据え置き（改名は移設と別コミットで行う方針）。

★ **改訂（2026-08-15）— 「使えるサインイン方法」に何が書けるかを画面から読めるようにした。**
`tenant.allowed_providers` は自由入力で、書ける値はデプロイの env（`AF_OIDC_PROVIDERS` と Google の
歴史的な env）にしか無く、間違えれば保存が 400 `unknown_provider` で弾かれるだけだった —
**弾かれた人が次に何を打てばいいかは、画面のどこにも書いていない**。集合そのものは
`manager.knownProviderIDs` にあったが、**それを外に出すハンドラが 1 本も無かった**ので、
まず CP に読み取り API を足した:

- `GET /api/admin/providers`（`withSuperAdmin`・`control-plane/login_provider_api.go`）。返すのは
  **id・ボタンの文言（ja/en）・issuer** だけで、`client_id` も `client_secret` も載せない。
  「設定をそのまま出す」管理 API はスクリーンショットに秘密が写る道なので、画面に出す情報から
  逆算して決めた。issuer だけは残した — 「entra がどの Entra か」は登録簿がテナント定義の行に
  対して答えているのと同じ問いだから。
- `issuerURL()` は `loginProvider` インターフェースには足さず、組み込みの実装型（`oidcProvider` /
  `githubProvider`）だけが持つ任意インターフェースにした。インターフェースに足すと、テストの
  偽 provider が全部メソッドを生やすことになる。GitHub は OIDC ではないが（アダプタは REST を
  読む）、身元の出どころという意味では `https://github.com` 固定で答えは同じ。
- テナント定義の `t:<slug>:<name>` は**この一覧に混ぜない**（実行時に増減し、全部並べると
  グループ会社の名簿になる・決定 32-4）。自テナントの分は同じ画面の下に出ているので、
  ヒントで「承認済みなら `t:テナント名:方法名` と書ける」とだけ言う。

Console 側は**規則のパネルの中**（欄の直下）に置いた。別の面に置くと、打ち間違えて弾かれた人が
そこへ辿り着けない。表示は**表示名が主で、打ち込む id は `<code>`** — provider id は技術識別子で、
主役にすると「entra とは何か」を別の場所で聞くことになる（`t:<slug>:<name>` に対する扱いと同じ）。
読み取り専用版（テナント設定・tenant_admin）には**出さない**: これはデプロイ全体の情報で、
GET 自体が `withSuperAdmin`、そもそも規則を編集できるのは super_admin だけ（決定 19）。

★ **`/api/tenants` の DTO は広げていない。** 承認状態を返したくなるが、`apiError` に
フィールドを足せない（positional な composite literal が 249 箇所）のと同じ判断で、
承認状態は管理 API（`/api/admin/tenants/{slug}/idp`・`/api/admin/idp`）から読む。

## 61.12 段階

| 段階 | 内容 | スキーマ変更 |
|------|------|------------|
| **P0**（実装済み）| プロバイダ抽象 ＋ 汎用 OIDC（Entra / Okta / Keycloak / Auth0 / Cognito）。Google を同実装の 1 インスタンスへ移す。ログイン画面の複数ボタン・`sessionClaims` 拡張（`prov` / `sub`）・設定と文書 | 無し |
| **P1**（実装済み）| `identity_provider` テーブルと解決規則（§61.5）。★ **Console の「アカウントを追加」導線は撤回**（§61.5 の改訂・ADR 決定 5）ので Console 側の作業はゼロ | `0038`（pg `0021`）|
| **P2**（実装済み）| GitHub アダプタ（org 判定・TTL キャッシュ・猶予・再ログイン要求） | 無し |
| **P3**（実装済み）| テナント毎のログイン（§61.9）: `/login/<slug>`・`allowed_providers` の強制・入口の門に membership を含める・`auto_join_domains` / `allowed_domains`・admin 編集 UI・`provider_required` の再サインイン導線。★ **membership の削除／無効化 API**（§61.10.6）・**`super_admin` のブートストラップ**（§61.10.2）・**`super_admin` の起動時降格**（§61.10.7）・**`clean-home` の tenant_admin 開放**（§61.10.6）を含む | `0039`（pg `0022`）|
| **P4**（実装済み）| テナント定義の認証方式（§61.11）: `tenant_idp` テーブル・秘密の封印保存・tenant_admin の編集 UI・**super_admin の承認フロー**・provider の名前空間分離・**実行時 provider レジストリ**（再起動なしで承認/停止が効く）・**規則 2'**（招待の claim だけ許し、ログイン実績のあるアドレスは拒否・§61.11.8） | `0040`（pg `0023`）|

- **P1 を P2 より先に置くのが本設計の要点**。GitHub は email 不一致が常態なので、リンク機構なしに
  出すと §61.5-1 の workspace 分裂を必ず起こす。★ 改訂後はリンク機構そのものを作らないので、
  P1 が先である意味は「**分裂したときに本人へ通知が出る**」ことと、`AF_GITHUB_ALLOWED_DOMAINS` で
  入口から落とせることに変わった（§61.7）。
- **P3 は P1 / P2 と独立**で、P0 の直後に着手してよい（依存は `prov` クレームだけ）。
- **P4 は P1 が必須**（§61.11.3-2）。email 一致で identity を結合する規則が生きたままテナント定義の
  IdP を許すと、email を騙るだけで既存 identity を乗っ取れる。
- P0 だけでも「Microsoft でログインしたい」は満たせる。

## 61.13 却下した代替案

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
- **テナント毎のログインをサブドメインで分ける。** 見た目は最も自然だが、ワイルドカード DNS と
  証明書が要り、Funnel は 1 ホスト名しか出せない（§61.9.3）。
- **URL / cookie のテナント指定を認可の根拠にする。** 実装は最短だが利用者が書き換えられる。
  テナントの門はサーバ側の membership ＋ 規則だけで判定する（§61.9.2）。
- **テナント規則を `authGate` に置く。** 「どのテナントで判定するか」が決まらないので、
  穴か過剰拒否のどちらかになる。入口とテナントで層を分ける（§61.9.2）。
- ★ **テナントに `allowed_emails` カラムを持たせる。** 「テナント毎にログインできる ID を管理する」の
  最も素直な実装だが、**membership が既に同じ名簿**（招待 API は未ログインの人の identity も作れる・
  `tenants.go:254`）なので、同じ事実を 2 箇所で管理する二重台帳になる（§61.9.5）。
- **`allowed_domains` を毎リクエストの制約にする。** 正規に招待した業務委託（別ドメイン）が
  締め出され、例外リストが要り、結局二重台帳に戻る。招待時のガードに留める（§61.9.5）。
- **`auto_join_domains` を唯一の帰属手段にする。** 全社共通ドメイン（`@acme.co.jp` だけ）の会社では
  部署を分けられない。名簿はドメインに依存しない membership が持つ（§61.9.5）。
- **テナント規則を env（`AF_TENANT_<SLUG>_…`）に置く。** テナントは実行時に増えるので必ずずれる。
  管理 API のある DB 側に置く（§61.9.7）。
- **セッションに複数 provider を同時に持たせる。** テナント間移動の再ログインは消えるが、
  cookie が「認可状態の集合」になり、失効とオフボーディングの意味が曖昧になる（§61.9.4）。
- ★ **tenant_admin が単独で認証方式を有効化できるようにする。** 「テナントのことはテナントで完結」
  という §61.10.8 の線には最も忠実で、子会社の受け入れに情シスが出てこない。だが
  **IdP を足せる人はそのデプロイの誰にでもなれる**（§61.11.2）。`user_key` が email 由来で
  デプロイ全体に 1 つ、デプロイ役割も email 一致で決まる以上、これは権限昇格そのもので、
  tenant_admin の任命が super_admin の任命と同義になってしまう。承認を 1 段挟む
  （子会社あたり 1 回）方が、失うものに対して圧倒的に安い。
- **承認を省略する env（`AF_ALLOW_TENANT_IDP=1`）を用意する。** 「自社の tenant_admin は信頼できる」
  デプロイ向けの逃げ道だが、**fail-closed を env 1 行で外せる形にすると、それが既定の設置手順に
  なる**（許可リストを空にしたまま運用しないのと同じ理由）。承認は 1 回きりの操作なので、
  外す価値が無い。
- **テナントの `client_secret` を env に置いたまま、DB にはテナントの選択だけ持たせる。**
  秘密がバックアップに入らない利点はある（§61.11.4）。ただしテナント追加のたびに
  ホストのファイル編集＋CP 再起動が必要で、§61.11.1 の穴がそのまま残る。
  MCP のヘッダで既に同じ posture を受容しているため、ここだけ厳しくする理由が無い。
- **テナント定義の provider に env と同じ id 空間を使う。** 実装は短いが、テナントが `google` という
  名前の行を作った瞬間に env の Google を上書きできる。`t:<slug>:<name>` に分ける（§61.11.5）。

## 61.14 残る未決

- ~~Console のアカウント連携 UI をどのグループに置くか~~ → **消滅**（§61.5 の改訂で結合機構を作らないため、置くべき UI が無い）。
  設定モーダルの IA 自体は別途「**テナント設定モーダルを新設し、現在の管理モーダルは super_admin 専用にする**」と決めたが、
  これは Console 側の作業で本ドキュメントとは独立（P4 のテナント定義 IdP の置き場になる）。
- `audit.go` に provider を残すか（監査上は残したいが、DTO を広げる前に既存カラムから導出できないか確認する）。
- `SUPER_ADMIN_EMAILS`（`main.go:85`・email ベース）を provider 付きにするか。当面は email のままで足りる。
  （**置き場はホスト側の env のまま**で決着済み — §61.10.1。剥奪できない穴の解消は P3 の作業項目。）
- IdP 側のグループ（Entra の `groups` / GitHub の team）を tenant へ同期するか。
  **必須ではなくなった**（§61.9.5 で名簿＝membership に寄せたため、全社共通ドメインの会社でも
  部署分割は成立する）。残るのは**異動の自動追従**という利便性だけで、入れると
  「membership が正」という単一の正が崩れて同期の衝突を扱うことになる。当面は入れない。
  入れるとしても、membership を上書きするのではなく**管理画面に差分を出して人が承認する**形にする。
  なお Entra の `groups` クレームには overage（数が多いと Graph 参照に化ける）があり、実装は見た目より重い。
- テナントの門で弾いた理由（規則違反 / provider 不一致 / 非メンバー）を、本人にどこまで見せるか。
  `provider_required` は見せないと再ログインに誘導できないが、`allowed_domains` 違反を詳細に見せると
  他部署のドメイン構成が漏れる。
- テナント規則による拒否を `audit.go` に残すか（§61.9.8 の競合解決も含めて）。
- membership を外した人の workspace / home を**いつ**消すか。**誰が**は決着済み（tenant_admin・§61.10.6）。
  残るのは猶予の長さと棚の見せ方で、[45-deletion-lock](45-deletion-lock.md) と掃除の段階制に合わせて決める。
- `prompt=select_account` 相当を各 IdP でどう揃えるか（Entra は `prompt=select_account` が使えるが、
  IdP によっては未対応で無視される）。
- ~~**P4**: テナント定義の provider で許可リストが空のときの扱い~~ → **決着（P4 実装）**:
  **`allowed_domains` を保存時必須（400）**にした。「承認したのに誰も入れない」の回避が動機だったが、
  実装してみると理由はもっと強く、**`allowed_domains` はその issuer が名乗ってよいアドレスの範囲を
  縛る唯一の手段**で、空にできること自体が穴だった。あわせて 1 ドメイン 1 テナントを保存時に強制
  （§61.11.8）。`allowed_tids` は issuer が `common` / `organizations` のときだけ必須（決定 7 と同じ）。
- ~~**P4**: 承認待ち（`pending`）の間、tenant_admin に何を見せるか~~ → **決着（P4 実装）**:
  行は状態チップ（承認待ち／有効／停止中／承認済みだが不備あり）つきで見せ、
  **ログイン URL は有効な方式が 1 つできるまで出さない**。押しても入れない URL を先に配ると
  問い合わせになるだけなので、URL の出現そのものを「今なら配ってよい」の合図にした。
- ~~**P4**: 承認した issuer の**その後**を誰が見るか~~ → **決着（P4 実装）**:
  super_admin 側の画面を**空になるキューではなく登録簿**にした。承認済みの行も
  「どのテナントが・どの issuer を・どのドメイン範囲で・誰がいつ承認したか」を残したまま並べ続けるので、
  定期的な見直しの置き場ができる。自動の再確認（issuer の再検証や通知）は入れていない —
  IdP 側の設定変更を CP から観測する手段が無く、観測できないものを「確認済み」と表示する方が危ない。
