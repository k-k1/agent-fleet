# 61. ログイン IdP — Google 固定から「汎用 OIDC ＋ GitHub」へ

> 状態: **P0・P1 実装済み**（2026-08-14）／P2・P3・P4 は未着手
> 意思決定: [decisions/0043](../decisions/0043-login-idp.md)
> 関連: [dev/07-security.md](../dev/07-security.md) §7.3（AUTH 3 モード＝現行契約） /
> [dev/06-data-model.md](../dev/06-data-model.md)（`identity` / `membership`） /
> [dev/09-deploy.md](../dev/09-deploy.md)（配布物の設定面） / [35-packaging.md](35-packaging.md)（4 ターゲットへ同じ設定を配る） /
> [28-i18n.md](28-i18n.md)（CP 描画ページの言語選択） / [roadmap.md](../roadmap.md) §12.2（各社が握る設定項目）
> 対象: Control Plane（`oauth_*.go` / `main.go` / `routes.go` / migrations）/ Console（アカウント連携 UI・P1）/ `deploy/**`

## 61.1 目的

L1（Console へのログイン）の IdP が **Google 1 種に固定**されている。セルフホスト先は
「グループ各社が自社の社員をホストする」（roadmap §12.2）想定なので、Google Workspace を
使っていない会社は**そもそも設置できない**。M365 の会社が多数派である以上、これは配布の前提条件を欠く。

本ドキュメントは L1 の IdP を複数化する。**L2（Claude / codex / opencode を誰として動かすか＝
ユーザー本人の OAuth）とは無関係**で、`oauth_bitbucket.go` のような Git プロバイダ接続とも別軸
（[dev/08-integrations.md](../dev/08-integrations.md)）。

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
| `AUTH` は `oauth`（CP 内蔵 Google）/ `proxy`（外部ゲートウェイのヘッダ）/ `dev`（固定ユーザー）の 3 モード | `main.go:80`, [dev/07 §7.3](../dev/07-security.md) |
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
| その `user_key` が **workspace の home ディレクトリ名**（`<WS_DATA>/<user>/home`）＝暗号化 secrets の帰属先 | [dev/07 §7.2](../dev/07-security.md) |
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
（＝両方にログインできることが結合の証明・[決定 5](../decisions/0043-login-idp.md)）としていた。
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
AF_PROVISION=invite          # ★ P7-2 で新規インストールの既定に。切り替えの段取りは不要（§61.10.2）
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

★ **P7-2 でここは一本化した。「`auto` で立ち上げてから `invite` へ切り替える」という段取りは
もう無い** — 新規インストールは最初から `invite` で始まる（`.env.example` と ECS の
`AfProvision` パラメータの既定。CP に焼き込まれた既定は `auto` のままなので、既存デプロイは
変わらない）。

1. 情シスの人が `https://af.acme.co.jp/` を開く → Entra でサインイン。
   membership はまだ 1 つも無いが、`SUPER_ADMIN_EMAILS` に載っているので `super_admin` になり、
   `GET /api/tenants` は `{tenants: [], super_admin: true}` を 200 で返す（`tenants.go:35`）。
   アカウントメニューに管理が出る。
2. 管理 → テナント作成で `sales` / `dev` / `it` を作る。
3. 自分を `it` に `tenant_admin` として招待し、各部署の責任者も `tenant_admin` で招待する
   （**`tenant_admin` を付けられるのは `super_admin` だけ** — `tenants.go:280`）。
4. 以降、招待されていない人は入れない。**切り替えの再起動は不要**（最初からその状態）。

★ **かつては「いきなり `invite` で立ち上げてはいけない」だった。** membership を 1 つも持たない人は
`GET /api/tenants` が 403 `not_provisioned` を返し、Console は `data.error` 分岐で `superAdmin` を
立てないまま終わる（既定 `false`）。その結果、管理メニューの表示条件
`superAdmin || tenant_admin` が偽になり、**`super_admin` でも管理画面に到達できなかった**。
→ ✅ **P3 で解消**（`tenantAPI.list` が `not_provisioned` を受けたとき、相手が `super_admin` なら
200 を返す）。P7-2 はその上で**既定を倒しただけ**で、新しい仕組みは足していない。

★ **招待前の人の着地（P7-2 で追加）。** `super_admin` 以外は従来どおり 403 `not_provisioned`
だが、Console はそれを**エラーではなく状態として扱う**ようになった（`tenant.ts` の
`notProvisioned` → `App` が `NotProvisioned` を描く）。以前はこの状態でも通常の Console が開き、
以後すべてのリクエストが 403 で弾かれてトーストが 1 つずつ出るだけで、「自分は何をすればいいのか」
がどこにも書かれていなかった。招待制が既定になる以上、これは例外ではなく**新しい同僚が最初に見る
画面**なので、面として作る必要があった。

★ **その画面が答えるのは 3 つだけ**: ①失敗ではない（サインインは通っている）②次にすることは
管理者に依頼すること ③**そのとき伝えるべき自分のアドレス**。③が最も実務的で、これが読めないと
「どのアドレスで入ったのか分からない」という往復が必ず 1 回増える — 管理者は名簿にアドレスで
人を足すし、サインイン方法が複数ある人ほど自分がどれで入ったかを分かっていない。

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
- ★ **workspace は membership 毎＝部署毎に別**（home も secrets も別・[dev/07 §7.2](../dev/07-security.md)）。
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

- **自分自身の「最後の 1 つ」は外せない**（`self_removal`）。UI からの誤クリックに製品内の
  取り消し手段が無く、残る復旧経路は他の管理者かホストの env になるため。
  **当初は「自分の membership はどのテナントでも外せない」だったが、2026-08-21 に
  「自分の最後の有効な membership だけ拒否する」へ絞った。** 守りたいのは*戻る道*であって
  「自分の行かどうか」ではなく、全部拒否すると **捨てテナントの後始末が製品からできなく
  なる**——golden の焼き直し（docs/64 §64.28）の種は「サインインできる人」でなければ
  起こせない＝自分のアカウントで、管理者が 1 人のデプロイには依頼できる相手がいない。
  判定は「この行以外に有効な membership があるか」で、すでに inactive な行は戻る道に
  数えない。
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

★ **2026-08-22 に 3 段目を足した（§61.18）。** 「外す → 破棄」の後に**行そのものを消す**手が
無く、`SetMembershipStatus` のコメントは *"Hard deletion is deliberately not offered"* だった。
あの文が本当に守っていたのは**履歴**（監査・費用・稼働時間）で、schedules と shares はその人の
ものだから一緒に消えてよい。条件・消える表・残る表は §61.18.5。

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

> 本節は **OIDC の IdP**（Entra / Okta / Keycloak …）を前提に書いてある。テナントが
> **GitHub** を追加する場合は issuer が全テナント共有になり、信頼の根拠そのものが変わる —
> **§61.15**（P5）を読むこと。

§61.9 は「1 社の中を部署で分ける・Entra テナントは 1 つ」という例で書いた。しかし
**グループ各社**（[roadmap](../roadmap.md) §12.2 の本来の想定）や分社・M&A の途中では、
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
  全テナント分が開く**（[dev/07 §7.6](../dev/07-security.md)）。テナント間の暗号学的分離ではない。
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

★ **改訂（2026-08-20）— 管理モーダルも左レールにした（Console IA 刷新の続き）。** 上の表の
「どこ」は、どれも横一列のモードタブではなくレールの項目を指すようになった:
**管理 → テナント › サインイン方法の登録簿**（登録簿・承認/停止）、**管理 → テナント一覧 →
そのテナント → サインイン方式 / ログイン規則**（テナントを開くとレールごとそのテナントに
入れ替わる）。テナント 1 つ分の面は `settings/tenantScope.tsx` に集約して、テナント設定
モーダルと**レールの並びごと**共有する（同じテナントを別の入口から見るだけなので IA を
分けない）。読み取り専用で残したテナント全体の上限も、名簿の頭ではなく
**「テナント › 上限・自動停止」**という 1 つの節に移した（デプロイ管理者にはその同じ節が
編集できる形で出る）。出し分けは従来どおり props の `isSuper` だけ。

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
| **P5**（実装済み）| テナント定義の **GitHub**（§61.15）: `kind` / `allowed_orgs`・行から組む GitHub アダプタ・承認の対象を **(org, ドメイン)** に読み替え・org 追加で再承認・**規則 1.5**（`realm` で「同じ IdP アカウントを別のボタンから」を結合）・**受け入れる方式と出すボタンの分離**（`hidden_providers`） | `0041` / `0042`（pg `0024` / `0025`）|
| **P6**（実装済み）| **本人の同意でサインイン方法を紐づける**（§61.16）: `/oauth2/link` の往復・`GET /api/me/login-methods`・設定の「アカウント」タブ・`email_taken` の案内。★ 開けるのは「別 IdP・**同じ email**」だけで、別 email の結合は引き続きしない | 無し |
| **P7**（未実装）| **サインイン方式をテナントへ寄せる**（§61.17）: env の方式を**既定テナントの方式**として扱い、他テナントはそれを**参照**する・テナントの面を統合リスト＋2 トグルにして `allowed_providers` / `hidden_providers` の自由入力を画面から外す・素の `/login` に既定テナントの **`hidden_providers` だけ**を適用（§61.15.13 が閉じる）。★ **provider id の形と identity 層は 1 行も触らない**（§61.17.3） | 無し |

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

## 61.15 テナント定義の GitHub（P5）— 信頼の根拠が issuer でなくなる場合

§61.11（P4）は「子会社ごとに Entra が違う」を解いた。利用者から出たのはその次の形で、
**テナント管理者が自テナントのサインイン方法として GitHub を追加したい**。
P4 の仕組みに素直には乗らない。乗らない理由が本節の中身で、実装はその帰結でしかない。

### 61.15.1 なぜ P4 の枠に入らないか（実測）

| | P4（OIDC） | GitHub |
|---|---|---|
| 発行元 | 子会社ごとに違う（issuer に tenant guid が入る） | **`github.com` 固定＝全テナント共有** |
| 承認の根拠 | 「issuer がその子会社に固定されているから、そこが名乗る email を信じてよい」（`trust=issuer`・決定 7） | issuer では何も区別できない。**org メンバーシップ ＋ GitHub が検証済みの email** |
| 実装 | 汎用 OIDC クライアント 1 本（`oauth_oidc.go`） | 別アダプタ（`oauth_github.go`・OIDC を通らない。`AF_OIDC_PROVIDERS` に `github` と書いても OIDC には行かない） |

現状のコードが GitHub 行を保存も構築もできないことも実測した:

- `tenant_idp_api.go` の `switch b.Trust` が `email_verified` / `issuer` しか通さない。同じ検証が
  `tenant_idp.go` の行 → provider 組み立て側**にも二重にある**（片方だけ直すと「保存はできたのに
  承認後に落ちる」になる）
- `allowed_orgs` に相当するカラムが無い
- 行 → provider は `*oidcProvider` を返す 1 本道
- `newGitHubProvider` は env（`AF_GITHUB_ALLOWED_ORGS` / `GITHUB_OAUTH_CLIENT_*`）からしか組めない

### 61.15.2 ★ 承認（決定 30）は何に対して与えるのか

決定 30 が塞いだ乗っ取りは「tenant_admin が自分の支配下の IdP を登録し、`email=<情シス>` を
主張するトークンを自分で発行する」だった。**GitHub ではこれが成立しない** — email を検証するのは
GitHub で（`/user/emails` の `primary && verified`＝メールボックス到達性の証明）、
テナント管理者にその検証は偽造できない。issuer を握っているのは攻撃者ではなく GitHub である。

では承認は何のために要るのか。2 つ残る。**どちらもデプロイ全体の行為**で、だから承認は据え置く:

1. **ドメインの取得**。`allowed_domains` は 1 ドメイン 1 テナントの台帳（§61.11.8）で、行が
   ドメインを 1 つ取るとそのドメインは他テナントが取れなくなる。**なりすませなくても、
   他社の登録を塞ぐことはできる。**
2. **その入口を許すかどうか**。GitHub の org は個人アカウントの集合で、入退室を握るのは
   org のオーナー（その会社の情シスとは限らない）。会社の入口として許すかはデプロイの判断。

したがって承認の対象は **(allowed_orgs, allowed_domains)** に読み替える —
「**この org のメンバーを、このドメイン範囲で**信じてよい」。issuer は対象に入らない（全テナント共通）。

**承認のやり直し（`repend`）** も同じ枠で決める。★ **org の追加は `pending` へ戻す**:
承認は「この org のメンバーを」に与えたので、org が増えれば対象の人の集合が変わる。
削除は戻さない（入れる人が減って危険にはならない）。`kind` の変更も戻す。
`client_secret` の更新は戻さない（P4 と同じ理由 — 再承認を強いるとローテーションしなくなる）。

### 61.15.3 `allowed_domains` は GitHub でも必須（据え置き）

「A 社のテナントが B 社のドメインを名乗る org を登録できないか」を検討した。答え:
**org ではなくドメイン台帳が止める。** A が org `b-corp` と書くのは自由だが、`@b.co.jp` と
書いた瞬間に B の行と衝突して 409。B がまだ行を持たない場合に A が先に取れるのは P4 の
現状と同じ露出で、緩和も同じ（承認者がドメインを読む・§61.15.2 の 1）。

そのうえで必須を維持する理由は 3 つ:

1. 上の台帳に参加させるため（空の行は台帳の外に出てしまう）
2. §61.7 の実務理由 — GitHub が渡すのは主アドレス 1 件で、それが会社ドメイン外の人は
   **既存ではなく新しい workspace に着地する**。入口で落とす方が親切
3. 空は「デプロイの外で管理されている org の全員」であり、承認が無際限になる

### 61.15.4 同じ org を 2 つのテナントが登録するのは許可

弁別子は org ではなく**ドメイン**で、ドメインはデプロイ全体で排他。よって 1 人（＝1 つの
verified email）が満たせる行は高々 1 つになり、着地は一意に決まる。
「グループ共通の GitHub org ＋ 子会社ごとの email ドメイン」という実在の形がこれで通る。

### 61.15.5 ★ env の GitHub と併存したときの identity — 規則 1.5 を足した

id は衝突しない（`github` と `t:<slug>:github`）。**壊れるのは identity の層**だった（実測）:
`linkAfterLogin` はテナント定義 provider に `emailJoin=false` を渡す（決定 32）ので、
**env の `github` で先にサインインした人が、後からテナントの GitHub ボタンを押すと
`email_taken` で拒否される**。同じ GitHub アカウント・同じ email なのに `(provider, subject)` が
別キーになるためで、GitHub 同士だと現実に踏む。

決定 32 を緩めるのではなく、**偽造できない鍵に限って開ける**規則を足した:

| 順 | 規則 | 鍵 |
|----|------|----|
| 1 | `(provider, subject)` が既知 | 従来どおり |
| **1.5** | **同じ `realm` の同じ `subject`** を持つ行がある | **その identity**。realm は「どこで身元が証明されたか」＝ OIDC は issuer、GitHub は `https://github.com` |
| 2 / 2' / 2'' | email 一致（テナント定義は claim か拒否） | 従来どおり |
| 3 | どれでもない | 新規 identity |

★ **realm はアダプタが名乗るのであって、行が書くのではない。** `providerRealm()` は
`issuerURL()`（`login_provider_api.go` の任意インターフェース）を読むだけで、コールバックが
provider オブジェクトから押す。GitHub アダプタの `webBase` / `apiBase` は定数のままにしてあり、
**行から差し替えられない** — ここが動くと、テナントが自分のサーバを立てて任意の subject を
名乗れる＝規則 1.5 の鍵が偽造できる。

これで通るようになるのは「**同じ IdP アカウントを別のボタンから**」だけ:

| 経路 | 結果 |
|---|---|
| Google ⇄ Entra ⇄ env の GitHub（同じ email） | 従来どおり通る（規則 2・env 同士は `emailJoin=true`） |
| env の GitHub ⇄ テナントの GitHub（同じアカウント） | **通る**（規則 1.5） |
| env の Entra ⇄ テナントの Entra（同じ issuer・同じ `sub`） | **通る**（規則 1.5） |
| 別々の IdP ⇄ テナント定義の行（email だけ同じ） | **拒否のまま**（決定 32） |

★ **最後の行は意図的に残した**（利用者判断）。ここを開けると、承認済みドメインの範囲内で、
そのテナントの管理者が**別の権威で作られた既存アカウント**（本社の Google で作った identity＝
workspace・secrets）になりすませる。運用上の帰結は次の 1 点で、**画面とガイドに書く**:

> 兼務の人（本社と子会社の両方に所属）が本社の IdP でサインインしている場合、子会社が
> 「使えるサインイン方法」を自社の方式だけに絞ると、テナントを切り替えた瞬間に
> `provider_required` になり、案内された方式を押すと（別 IdP なら）拒否される。
> **兼務がいるテナントは、その人が使う方式を `allowed_providers` に残す。**
> ★ 「残すと使わないボタンが並ぶ」は §61.15.9 で解いた（受け入れたまま、ボタンだけ隠せる）。

Entra の `sub` は (app, user) のペアワイズなので、client_id が違えば規則 1.5 は当たらない
（当たらないだけで、害は無い）。実際に効くのは GitHub と、同じアプリ登録を共有する場合。

### 61.15.6 OAuth App は行が持つ（デプロイのものを共有しない）

`client_id` / `client_secret` は**テナントが自分の OAuth App を用意する**（列も封印も P4 のまま）。
子会社は自社 org に OAuth App を作り、コールバック `<PUBLIC_BASE_URL>/oauth2/callback` を足し、
**org のオーナーが OAuth App access restrictions を承認する**（承認前は membership が見えず
「正しい設定なのに全員拒否」になる・§61.7）。

デプロイの App を共有しない理由: 各子会社の org オーナーに**情シスの App**（git 連携の
device flow と同一）を承認させることになり、しかも情シスの鍵ローテーションが全子会社を
無言で壊す。

★ **`AF_GITHUB_ALLOWED_ORGS` が無いデプロイでも動く。** env の GitHub ログインは無効なまま、
テナント定義の GitHub だけが有効、という状態を許す。決定 29（子会社の追加にホストのファイル編集も
CP 再起動も要らない）の当然の帰結で、env 側の「orgs が空なら provider ごと無効」は
**env の provider にだけ**掛かる規則。行側は `allowed_orgs` 必須（保存時 400）が同じ役割を果たす。

### 61.15.7 実装メモ（P5 で実際にこうした）

- **スキーマ**: `tenant_idp` に `kind`（`oidc` | `github`・既定 `oidc`）と `allowed_orgs`、
  `identity_provider` に `realm` を足した（`migrations/0041` / pg `0024`）。
  `kind` が空の行は `oidc` として読む（0041 以前の行）。
- ★ **`realm` の埋め戻しは移行 SQL ではなく起動時に行う**（`FillProviderRealm`）。
  どの provider id がどの IdP かは**その時に組んだ provider 集合にしかない**。
  移行 SQL で「`provider='github'` は GitHub アダプタ」と決め打つと、
  `AF_OIDC_PROVIDERS=github` と書いたデプロイ（警告は出るが OIDC 側は生きる）で間違える。
  規則 1.5 は**推測した realm では動かしてはいけない**ので、確実に分かる場所でだけ書く。
- ★ **`linkAfterLogin` の早期 return をやめた**。provider が 1 つのデプロイでは
  `len(c.providers) < 2` で何もせず、行はリゾルバ側（provider オブジェクトを持たない＝realm を
  書けない）が作っていた。realm の無い行は規則 1.5 に参加できないので、記録は必ず行い、
  **抑止するのは「新しいアカウントを作りました」の案内だけ**にした（ボタンが 1 つなら
  取り違えようがなく、出しても行動につながらない）。
- **`LinkIdentity` は引数を構造体（`IdentityLink`）にした**。文字列 6 個の位置引数に realm を
  足すと、取り違えがそのまま「他人の workspace に着地する」になる。
- レジストリのスナップショットは `*oidcProvider` から `loginProvider` へ広げた。
  ★ 行の内容が変わらない限り**同じインスタンスを使い回す**のは P4 のままで、GitHub 行では
  これが「org メンバーシップのキャッシュを捨てない」ことを意味する（捨てると全員が
  `reauth` になる）。行を編集すると作り直されるので、そのときは 1 回再サインインが要る。
- **画面**: 種類の選択で欄が入れ替わる（GitHub は issuer / tid / trust を出さず、代わりに
  組織）。一覧に出す「身元の出どころ」も kind で切り替える — GitHub 行に `github.com` と
  出しても全テナント同じで何も区別できないので、**組織名**を出す。
- `trust` は github 行では `api`（§61.4）、`issuer` は `https://github.com` を**サーバ側で**入れる。
  行が名乗れると登録簿と監査ログが嘘をつくため、フォームからは受け取らない。

### 61.15.9 ★ 受け入れる方式と、ボタンに出す方式を分けた

§61.15.5 の運用上の注意（兼務の人が使う方式を `allowed_providers` から外さない）を書いた
時点で、要件の方を曲げていた。**子会社は「自社の GitHub だけ」で運用したい**のに、
本社から来ている兼務の人のために本社の方式を残せ、と言っていることになる。しかも
その人が GitHub アカウントを持っていない場合、「本人の同意で紐づける」導線（§61.15.10 の
未決 1 つ目）を作っても解けない — 紐づける相手が存在しない。

整理すると、`allowed_providers` は**2 つの仕事を 1 つの欄で**やっていた:

1. **受け入れる方式**（`resolver.go` の `checkTenantProvider`・毎リクエスト強制）
2. **ログイン画面に出すボタン**（`loginButtons`・表示）

分ける。`tenant.hidden_providers`（`0042` / pg `0025`）は **2 だけ**を落とす:

```
allowed_providers = t:sub:github, google   受け入れる（意味は従来どおり）
hidden_providers  = google                 受け入れるが、この画面にボタンは出さない
```

これで子会社のログイン画面は GitHub だけになり、兼務の山田さんは今までどおり素の
`/login` から本社の Google で入って、テナントを切り替えられる。

★ **受け入れることは、入れる人を増やすことではない。** 誰がそのテナントに入れるかを
決めるのは**名簿（membership）**で（§61.9.8 の規則 1）、`allowed_providers` は
「その人が名乗るときに、どの身元の出どころを認めるか」でしかない。`google` を受け入れても、
招待されていない Google ユーザーが子会社テナントに入れるようにはならない。
（例外は `auto_join_domains` — こちらは名簿を自動で作る欄なので、意図の確認が要る。）

★ **これは表示であって門ではない**（決定 14）。隠した方式で入った人は**通る**。それが
この欄の目的そのもので、`providerAllowed` は `hidden_providers` を一切見ない。
テストで固定してある（`TestHiddenProvidersHideTheButtonButNotTheDoor`）。

★ **全部隠したら、隠す指定の方を無視する。** ボタンの無いログイン画面は行き止まりで、
テナント側の設定ミスがそれを作れてはいけない。保存時に弾かないのは、そのテナントの
方式が実行時に増減する（承認・停止）ため、保存の瞬間に「全部隠したか」を確定できないから。

運用として子会社に残る選択は 2 つで、どちらも正当:

- **(a) 兼務の人を自社の GitHub org に招く** — 「GitHub 限定」を貫く。org から外せば入口も閉じる
- **(b) 本社の方式を受け入れたまま、ボタンだけ隠す** — 本社の人は本社の身元のまま受け入れる

### 61.15.10 残る未決

- ✅ 別 IdP 同士（email だけ同じ）の結合 → **§61.16（P6）で解いた**。本人の同意で紐づける
  導線を作り、開けたのは「同じ email」だけ。§61.5 の「別 email の結合はしない」は据え置き。
- ✅ 規則 1.5 を OIDC でどこまで使うか → **§61.15.11 で解いた**（決定 38）。安定クレームを
  `realm_subject` として別の列に持ち、`subject` は `sub` のまま据え置いた。
- ✅ env の GitHub とテナントの GitHub のボタン文言 → **テナント行の既定ラベルに
  テナント名を入れた**（§61.15.12）。行が `label_ja` / `label_en` を書いていればそちらが勝つ
  優先順位は変えていない。
- ✅ `hidden_providers` の範囲 → **実装では変えられない**と結論した（§61.15.13）。
  素の `/login` はテナントを知らないので、デプロイ共通の方式はそこに出続ける。
  効くのは「テナントの入口を `/login/<slug>` に寄せる案内」までで、あとは運用と文書の仕事。

### 61.15.11 規則 1.5 の 2 本目の鍵（決定 38）

規則 1.5（§61.15・決定 35）は `(realm, subject)` で「同じ IdP アカウントを別のボタンから」を
結合する。GitHub ではこれで足りる — 数値 id は全 OAuth App で同じだからだ。**Entra は違う**。
`sub` が **(アプリ登録, 人) のペアワイズ**なので、同じ Entra テナントでも本社のアプリ登録と
子会社のアプリ登録では同じ人が別 subject になり、規則 1.5 が当たらない。

★ **素直に「`subject` を `oid` に差し替える」は事故る。** 既存行の鍵が変わって規則 1（対の
一致）が外れる。env provider は規則 2（email 結合）で救われるが、**テナント定義の行は規則 2'
で `email_taken` になり、いま使っている人が締め出される**。移行のためのコードが、移行できない
人を作る。

なので **`subject` は `sub` のまま**にして、鍵を**増やす**:

```
identity_provider.realm          どこで証明されたか（従来どおり・アダプタが名乗る）
identity_provider.subject        sub（従来どおり・規則 1 の鍵）
identity_provider.realm_claim    規則 1.5 で読んだクレームの「名前」
identity_provider.realm_subject  そのクレームが運んでいた値
```

規則 1.5 は `(realm, subject)` を先に見て、外れたら `(realm, realm_claim, realm_subject)` を見る。
両方効く IdP では従来と同じ結果になる（順序がそう保証している）。

- ★ **照合にクレーム名も含める。** 片方が `oid`、片方が別のクレームのとき、値がたまたま
  一致しても当ててはいけない — 同じ問いの答えではないから。**空は一切マッチしない**ので、
  この列より前に書かれた行も、クレームを名乗らない provider も参加しない。
- ★ **設定できるのはクレーム「名」だけで、値は必ずトークンから読む**（`realm` と同じ作法）。
  行から値を書けたら、テナントが他人の `oid` を名乗って規則 1.5 を偽造できる。

宣言の場所は 2 つあり、**許す範囲が違う**:

| どこ | 書けるもの | なぜ |
|---|---|---|
| env `AF_OIDC_<ID>_LINK_CLAIM` | 任意のクレーム名 | 話し手がオペレーター自身。許可リストを直せば同じことができる立場（危険性はガイドに明記） |
| テナント行 `tenant_idp.link_claim` | `oid` のみ（ホワイトリスト） | `email` / `upn` / `preferred_username` を書けると、**共有 realm の中に email 結合**を作れる＝決定 32 が拒否した乗っ取りが別の扉から入る |

- ★ **検証は 2 箇所**: 保存時（`validateTenantIdPBody`）と実行時（`buildTenantProvider`）。
  片方だけ直すと「保存はできたのに承認後に落ちる」行が作れる。
- **`link_claim` の変更は `pending` へ戻す。** 誰が入れるかは変わらないが、**誰に着地するか**が
  変わる（既存アカウントに届くボタンが増える）＝承認者が見るべき変更。`kind` と同じ扱い。
- **紐づけ（§61.16）の拒否条件にもこの鍵を足す。** `realm`+`subject` だけ見ていると、
  ペアワイズ `sub` のせいで「対は空いている」ように見えて、実際にその方式で入ると他人に着地する。

### 61.15.12 同じ画面に並ぶ 2 つの GitHub ボタン

`/login/<slug>` には env のボタンとテナントの行が並ぶ。id は衝突しない（`github` と
`t:sub:github`）が、**id はボタンに出ない** — 既定ラベルはどちらも「GitHub でサインイン」で、
見分けのつかない 2 つのボタンが別々の OAuth アプリへ人を送っていた。

**テナント行の既定ラベルにだけテナント名を足す**（表示名、無ければ slug）:

```
env の行          GitHub でサインイン        / Sign in with GitHub
テナントの行      GitHub でサインイン（子会社） / Sign in with GitHub (子会社)
```

★ **行が `label_ja` / `label_en` を書いていればそちらが勝つ**（優先順位は変えない）。これは
既定を補うためのもので、管理者が選んだ文言の上書きは別のこと。GitHub 行の土台は env の文言
（`AF_GITHUB_LABEL_*`）のままにしてある — "GitHub" の大文字小文字を保つため。

### 61.15.13 `hidden_providers` の範囲（実装では変えられない）

`hidden_providers`（§61.15.9）が効くのは**そのテナントのログイン画面**だけで、素の `/login` は
テナントを知らないので、デプロイ共通の方式はそこに出続ける。**出さないという選択肢は無い** —
テナントに属さない人（新しい super_admin、招待前の人）が誰も入れなくなる。

実装で変えられるのは「テナントの入口を `/login/<slug>` に寄せる案内」までで、そこから先は
運用と文書の仕事。したこと:

★★ **撤回した（P7-1・§61.17.6）。** 「変えられない」の根拠は*素の `/login` がどのテナント
でもない*ことだったが、そこに**既定テナントの `hidden_providers` を適用**したので根拠ごと
消えた。下の運用回避は**不要になり、ガイドから撤去した**（二言語 3 面 6 ファイル）。

- ~~テナント設定に出している `loginURL` の直下に一文を置いた。~~ → 代わりに「ボタンに出す」を
  外した行があるときだけ出る注記を、トグルと同じ面に置いた（P7-0）。素の `/login` にも
  効くようになったので、この注記自体も P7-1 で落とした。
- ~~管理者ガイドとオペレーターガイドに「隠しても素の `/login` には出る。テナントの人には
  `/login/<slug>` を配る」を明記した。~~ → 「素の `/login` にも効く」に書き換え、あわせて
  **「受け入れる」の方は素の `/login` には効かない**（＝ここでボタンを 0 にできない）を
  各ガイドに明記した。これは仕様であって制限の言い訳ではないので、理由まで書いてある。

★ 教訓として残す価値があるのは、**「実装では変えられない」と書いた結論が、1 つ上の層の
帰属を変えたら消えた**こと。あのときの制約は `handleLogin` の中にあったのではなく、
「素の `/login` は誰のものでもない」という**決めごとの側**にあった。

## 61.16 本人の同意でサインイン方法を紐づける（P6）

§61.15.10 の未決 1 つ目。**別々の IdP が同じメールアドレスを名乗る**組み合わせは、
いまも `email_taken` で拒否される（決定 32・規則 2'）。規則 1.5 が結合するのは
「**1 つの IdP を 2 つのボタンから**」だけなので、本社の Google で入っている人が
子会社の GitHub org にも居る、という形はどちらの規則にも当たらない。

### 61.16.1 なぜ email 一致で開けないのか（再掲）と、何なら開けるのか

開けない理由は §61.15.5 のとおりで、**承認済みドメインの範囲内なら、そのテナントの管理者が
別の権威で作られた既存アカウント（＝ workspace・secrets）になりすませる**から。
危険なのは「アドレスが同じこと」ではなく「**誰がそれを主張したか**」である。

そこで主張する人を変える。**アカウントの持ち主が、ログイン済みの状態で自分で押す**なら、

| 何を証明するか | 誰が証明するか |
|---|---|
| いまのアカウントの持ち主であること | CP のセッション（そのアカウントでサインイン済み） |
| 足そうとしている IdP アカウントの持ち主であること | その IdP のコールバック（通常のログインと同じ） |

の 2 つが揃い、**他人についての主張が 1 つも要らない**。これが決定 37。

### 61.16.2 効かせる条件（1 つでも欠けると上の性質が崩れる）

1. **ライブなセッションが要る。** ★ `/oauth2/` は `authGate` の**除外プレフィックス**なので
   （`routes.go`）、この門は `handleOAuthLink` 自身が持つ。ここが抜けると
   「セッション不要で `identity_provider` を書けるエンドポイント」になる。
2. **2 本目の脚で同じ人であることを再確認する。** 署名済みの state は「CP が書いた」しか
   言わない。開始時の (identity, email, provider, subject) を state に入れ、コールバックで
   **生きているセッションと突き合わせる** — 途中で別タブでサインインし直したら拒否。
3. **その方式自身の門を通る。** `Allowed()` をログインとまったく同じに走らせる
   （GitHub なら org メンバーシップ＋許可ドメイン）。**紐づけは門の迂回路ではない。**
4. **同じメールアドレスを名乗る方式だけ。** 別アドレスの結合は §61.5 の
   「両方にサインインできることは、同一人物であることの証明ではない」に当たり、取り消せない。
5. **相手の IdP アカウントが誰かのものなら拒否**（`errLinkTaken`）。3 経路すべて:
   ① `(provider, subject)` がすでに他人のもの ② **規則 1.5** で他人に当たる
   （別ボタン・同じ IdP アカウント）③ 名乗ったアドレスが他人の identity のもの。
   付け替えも結合もしない。
6. **デプロイ役割は動かさない**（決定 31）。`AttachProvider` は `identity` 行を一切触らない
   ＝ `roleHint` の経路がそもそも無い。`last_login_at` も書かない（紐づけはログインではない）。

### 61.16.3 面と経路

```
設定 → 個人設定 → アカウント        GET /api/me/login-methods
  紐づけ済みの方式（現在使用中の印つき）＋ 足せる方式
  ［方式を押す］→ GET /oauth2/link?provider=…&next=…
                     → IdP → GET /oauth2/callback（state に紐づけ印）
                     → 結果ページ（成功/失敗の理由）→ Console に戻る
```

- **redirect_uri は増やさない。** コールバックは `/oauth2/callback` のままで、
  紐づけかどうかは署名済み state が持つ（決定 8 と同じ理由 — IdP 側の登録を増やさない）。
- **足せる方式の一覧**は env の provider ＋ **自分が名簿に載っているテナント**の方式。
  ★ 一覧は VIEW で、開始側（`linkableFor`）が同じ規則を持つ（決定 14）。テナント定義の
  方式を全員に見せないのは、それがグループ子会社の一覧になるため（決定 32-4）。
- **結果は CP が描く小さなページ**に出す。Console に `?link=…` を返して解釈させるより、
  失敗の理由（門で落ちた／他人のもの／アドレスが違う）をその場で 1 文で言える。
- **入口は 2 つ。** 設定タブと、ログイン画面の `email_taken` の文面。後者が無いと、
  拒否された人が「次に何をすればよいか」を読める場所がどこにも無い。

### 61.16.4 解除（P7 で入れた）

足せるものは外せないと片道になる。異動で子会社の GitHub org を抜けた人の行が残り続けると、
「まだ使える方法」と「もう使えない方法」が同じ一覧に並ぶ。

```
DELETE /api/me/login-methods?provider=…&subject=…
```

★ **provider / subject はパスセグメントではなくクエリ**。テナント定義の provider id は
`t:<slug>:<name>` で `:` を含み、パスに載せると（エンコードすれば）前段のプロキシ次第で壊れ、
エンコードしなければ黙って別のセグメントに割れる。

**効かせるガードは 3 つで、どれか 1 つでも抜けると別々の壊れ方をする:**

1. **残り 1 つの解除は拒否**（`errLastLoginMethod`）。入れないアカウントには復旧経路が無い —
   パスワードも SMTP も持たない（決定 28）。★ **数えるのは `DELETE` 文の中**
   （`AND (SELECT COUNT(*) … ) > 1`）。API で数えてから消すと、タブを 2 枚開いた人が
   両方で「まだ 2 つある」を見て 0 にできる。
2. **いま使っているセッションの方式は解除できない**（`current_login_method`）。
   `sessionLoginRef` と突き合わせる。★ `sub` を持たない古い cookie は
   **その provider の行すべてを「使用中」とみなす** — 特定できないなら守る側に倒す。
3. **他人の行に届かない**。`identity_id` を必ず WHERE に入れる（対を当てても消えない）。

- 一覧（`GET /api/me/login-methods`）は行ごとに **`subject`** と **`removable`** を返す。
  ★ **外せるかどうかを答えるのはサーバ**で、Console はその写しでしかない（決定 14）。
  `subject` を足したのは、同じ provider に行が 2 つある形が理屈のうえでありうるため —
  React の key も `provider + subject` にした。
- **追加も解除も監査に残す**（`identity_provider.attach` / `.detach`）。片方だけだと
  「いつからその扉が開いていたか」が読めない。`tenant_id` は空 — テナントの中の操作ではなく、
  デプロイ全体でのそのアカウントの事実だから。
- Console 側は解除できない行でも**ボタンを消さない**。消すと「なぜ外せないのか」を読む場所が
  無くなる。`disabled` にして理由を `title` に出す。

### 61.16.5 入れなかったもの

- **ログイン画面でその場で紐づける**（拒否 → いつもの方法でログイン → 自動で紐づけ）。
  保留中の紐づけを署名 cookie で持ち回すため検証面が増える。踏むのは兼務の発生時だけなので、
  設定タブ＋案内で足りる。
- **別アドレスの紐づけ**（相手アドレスが未使用なら許す）。実質アドレスをまたぐ結合で、
  §61.5 の説明を書き換えることになる。

★ **これでも解けないもの**: 兼務の人が**子会社の GitHub アカウントを持っていない**場合
（紐づける相手が存在しない）。そちらは §61.15.9 の `hidden_providers` ＋運用（org に招くか、
本社の方式を受け入れたままボタンだけ隠す）で解決済みで、混同しないこと。

★ **規則 1.5 が当たらない IdP（Entra のペアワイズ `sub` など）でも、この導線なら通る**。
§61.15.10 の 2 つ目の未決（`oid` のような IdP 固有クレームを使うか）に手をつける必要が
さらに薄くなった — 当たらないのは「自動で結合されない」だけで、本人が押せば足せる。

## 61.17 サインイン方式はテナントが持つ（P7）— 既定テナントを「デプロイの方式」の置き場にする

§61.11（P4）でテナントが自分の方式を持てるようになり、§61.15（P5）で GitHub も載った。
それでも「デプロイの方式」と「テナントの方式」は**別の層に住んだまま**で、その境目が
3 つの形で表に出ている:

1. **管理画面のどこにも Google が出ない。** デプロイの方式を出す面は 1 つだけ
   （`GET /api/admin/providers`・§61.11.8）で、しかも super_admin がテナントのログイン規則を
   **編集している時にしか**出ない（`console/src/features/settings/tenantLogin.tsx:203`）。
   一方テナント管理者の「サインイン方式」は自テナントの行だけを並べるので、
   **Google で毎日入っている会社でも空**（「まだ登録されていません」）になる。
2. **`hidden_providers` が素の `/login` に効かない**（§61.15.13 で「実装では変えられない」と結論した）。
3. **デプロイの既定方式を廃止できない**（§61.17.1 の 3 つ目）。

3 つとも同じ 1 つのことから出ている: **方式がテナントに属していない。**

> ★ **本節は 2026-08-20 に設計レビュー済み**（実装前）。レビューで変わったのは 4 点:
> ①決定 42 の適用範囲を `hidden_providers` だけに狭めた（規則を丸ごと適用すると
> **デプロイ全体を締め出せる**・§61.17.6）②決定 41-a から「参照を追加する」という操作を外し、
> トグル 1 本に統一した（§61.17.5）③(b) のペアワイズで起きるのは分裂ではなく**拒否**、
> 救済には古い方式が生きている必要がある（§61.17.4）④P7-2 の `AF_PROVISION` 自動判定は
> **毎起動やり直されて黙って開く**ので否決（§61.17.7）。ほかに、id の形で分岐している箇所は
> 2 つではなく **10 箇所**だった（§61.17.3）。

### 61.17.1 出発点は既に半分ある（実測 2026-08-20）

- **既定テナントは必ず 1 つある。** `EnsureDefaultTenant` が起動時に走り（`main.go:149`）、
  `id=slug='default'` を `ON CONFLICT DO NOTHING` で入れる（`store_sqlite.go:247`）。
  消しても次の起動で戻る。
- **「デプロイ直後は super_admin だけが入れる」も今日の設定で作れる。** `AF_PROVISION=invite`
  なら membership の無い人は `not_provisioned`（`resolver.go:172`）、super_admin だけは
  `{tenants:[], super_admin:true}` を 200 で受け取る（`tenants.go:35`＝§61.10.2 の ✅）。
  既定値が `auto` なだけ（`main.go:93`）。
- **super_admin はどのテナントで入っても管理できる。** 管理 API は役割ベースで、どのテナントの
  セッションかを見ない: テナント作成（`routes.go:150`）・上限（`:156`）・ログイン規則（`:157`）は
  `withSuperAdmin`、テナントの IdP 行の作成・編集・**承認**は `tenantAdminFor` 経由で
  super_admin が全テナント通過（`resolver.go:106`）。
  ★ 例外は**運用**（そのテナントのワークスペースを開く）だけで、`checkTenantProvider` には
  super_admin 免除が無い（`resolver.go:222` — `checkTenantIP` の `:252` とは非対称）。
  **設定はできるが中身は開けない**、という形になる。
- ★ **だから「デプロイの既定方式を全部やめる」は選べない。** 2 つの一行が止める:
  `upsertIdentity` はセッションの provider がテナント定義なら **`roleHint` を無条件に空にする**
  （`resolver.go:57-60`・`isTenantProviderID`・決定 31）ので、全方式がテナント定義になると
  `SUPER_ADMIN_EMAILS` に載っていても**新しく super_admin になれる人が居なくなる**。加えて
  `AUTH=oauth` は provider ゼロで `log.Fatalf`（`main.go:361` の `oauthConfigured()` 判定・
  Fatalf は `:362`）。承認する super_admin が居ないので最初のテナント行も有効化できない
  （決定 30）＝循環する。
  → 廃止ではなく **「デプロイの方式に置き場を与える」** ＝ 既定テナントに属させる。
  ★ **「誰も super_admin になれない」は言い過ぎだった（レビューで訂正）。** 昇格する SQL は
  デプロイ全体で 2 本しかなく（`store_sqlite.go:371` の `UpsertIdentity` と `:870` の
  `touchIdentity`）、どちらも `roleHint == "super_admin"` を条件にするので、上の抑止が効くと
  **昇格経路は 0 になる**。一方 **降格は起動時の `DemoteSuperAdmins` 1 本だけ**
  （`main.go:162`）で、これは `SUPER_ADMIN_EMAILS` に**載っていない**行を落とす。つまり
  すでに super_admin の行は、env に載り続ける限り再起動を跨いでも残る。正しくは
  **「新規インストールが詰む／新任の super_admin を後から足せない」**であって、
  稼働中のデプロイが即座に管理不能になるわけではない。それでも結論は変わらない —
  最初の 1 人を作れない仕組みは採れない。

### 61.17.2 決めごと

- **サインイン方式は「テナントが持つもの」に一本化する（決定 39）。** env で書かれた方式は
  **既定テナントの方式**として扱い、他のテナントはそれを**参照**して受け入れる。
  他テナントの面に「デプロイの方式」という別カテゴリを置かない。
- ★ **帰属を移すのは表示と規則だけ。provider id の形と identity 層は触らない（決定 40）。**
  → §61.17.3。ここを取り違えると全社が壊れる。
- **「同じプロバイダの別設定」は許すが、ペアワイズ IdP では警告か `link_claim` が要る（決定 41）。**
  → §61.17.4。
- **素の `/login` は既定テナントのページとして描画する（決定 42）。** 描画のみで、認可の根拠には
  しない（§61.9.2 の ★ をそのまま守る）。これで §61.15.13 の穴が閉じる → §61.17.6。
  ★ **レビューで範囲を狭めた**: 適用するのは `hidden_providers` **だけ**。`allowed_providers`
  まで適用すると、素の `/login` からボタンが全部消える経路ができ、デプロイ全体が締め出される。

### 61.17.3 ★ provider id の形を変えてはいけない

「既定テナントの方式」を素直に `t:default:google` へ改名すると壊れる。★ **当初は「2 つ」と
書いたが、レビューで数え上げたら分岐点は 10 箇所あった**（`rg 'isTenantProviderID|parseTenantProviderID'`・
テストを除く。定義そのものは `tenant_idp.go:47-68`）。id の形は 2 箇所のガードではなく、
**識別子の意味そのもの**として全層に散っている:

| # | 場所 | 何を id の形で決めているか | 改名したときの壊れ方 |
|---|---|---|---|
| 1 | `oauth.go:124`（`providerFor`）＋ `tenant_idp.go:210` | `t:` なら **env の集合ではなく DB のレジストリ**（active 行のみ）から解決する | `t:default:google` に一致する DB 行は無い → `provider == nil` → **そのボタンでログインできない**。最初に当たるのはここ |
| 2 | `resolver.go:224`（`checkTenantProvider`・決定 32-3） | テナント定義セッションを自テナントへ固定 | 共有の Google で入った人が**既定テナント以外を一切使えなくなる**（兼務も部署テナントも全滅） |
| 3 | `resolver.go:57`（`upsertIdentity`・決定 31） | `roleHint` を空にする＋`EmailJoin` を落とす | その入口からは**新規に super_admin になれない**（既存行は降格しない＝`UpsertIdentity`/`touchIdentity` は昇格のみ。「昇格できない」だけ、という非対称） |
| 4 | `oauth.go:591`（`linkAfterLogin`） | 同じ抑止をコールバック側でも二重に適用 | 同上（choke point が 2 つあるので、片方だけ直しても意味が無いことの裏返し） |
| 5 | `tenants.go:183`（`setTenantLogin` の検証） | `t:` id は **自テナントの行**としてしか書けない（slug 不一致・行なしは 400 `unknown_provider`） | ★ **他テナントが `t:default:google` を `allowed_providers` に書けない** ＝ **決定 41-a の「参照」が成立しない**。改名は P7 の主役の機能そのものを壊す |
| 6 | `oauth.go:425`（`handleOAuthLogin`） | 押されたボタンが `t:` なら、その slug で `tenant` を上書きして state に載せる | 素の `/login` の Google ボタンが常に `tenant=default` を運び、ログイン後の Console のテナント preselect が全員 `default` になる |
| 7 | `oauth_link.go:134`（`linkableFor`・決定 32-4） | `t:` はそのテナントのメンバーにしか提示しない | §61.16 の「サインイン方法を追加」の候補から Google が消える（既定テナントの非メンバーには出なくなる） |
| 8 | `oauth_link.go:335`（`GET /api/me/login-methods`） | `t:` の行に `tenant` フィールドを付ける（表示） | 「デプロイ共通の方式」が特定テナント所属として表示される |
| 9 | `oauth_link.go:426`（`providerByID`） | 表示用の解決先を env / レジストリで振り分け | #1 と同じ理由で解決できず、リンク済み方式の表示名が出ない |

したがって移すのは**帰属の表示と規則の適用先だけ**で、id（`google` / `github`）はそのまま。
判定は「id の形」のまま据え置き、意味づけだけを **「誰が issuer を握っているか（オペレーターか
テナント管理者か）」** と読み替える。§61.9.2 の 3 層でいえば、P7 が触るのは
**ログイン画面**と**規則の帰属**だけで、**入口の門**と**テナントの門**は 1 行も変えない。

★ この読み替えの結果、いま Go の 1 行に隠れている不変条件が画面に出る:
**デプロイが持つ方式（＝既定テナントの方式）だけが super_admin を運べる。**

### 61.17.4 「同じプロバイダの別設定」を足すとき

利便性として 2 つの追加操作を用意する。安全性が全く違う。

**(a) 既定テナントと同じ方式を追加＝参照**（主）。id は `google` のまま、そのテナントの
受け入れ集合に足すだけ。identity 層に触らないので兼務も super_admin も壊れない。
実体は今日の `allowed_providers` を**自由入力からピッカーに変える**だけ。

**(b) 同じプロバイダの別設定＝新しい行**（`t:<slug>:<name>`・別のアプリ登録）。上級操作。
同じ人が**別 subject** になるかどうかは IdP 次第で、そこが分かれ目になる:

| IdP | `subject_types_supported`（2026-08-20 実測） | 帰結 |
|---|---|---|
| Google | `["public"]` | `sub` はクライアントをまたいで同じ → 規則 1.5 が当たり自動的に同一人物。安全 |
| Entra（`/common`） | `["pairwise"]` | アプリ登録ごとに別 subject（§61.15.11）→ 規則 2' で **`email_taken`**。`link_claim=oid` が要る |
| GitHub | （OIDC ではない） | 数値 id が全 OAuth App で同じ → 規則 1.5 が当たる。安全 |

- ★ **判定は discovery で読む**（`subject_types_supported`）。issuer のホスト名で当てない。
  読むのは「同じ issuer の行が既にある」＝(b) そのものの場合だけでよい。
- `pairwise` なら **`link_claim` を必須**にする（保存時 400）。今の UI は
  初期値が「既定（`sub` で見分ける）」なので、そのままでは事故る。
  ★★ **当初は「`pairwise` かつ `oid` が使えるなら」と書いたが、後半は判定できない（P7-3 で実測）。**
  標準の答えは `claims_supported` だが、**Entra の discovery は `oid` を 1 つも挙げていない**
  （2026-08-20 実測。挙がるのは `sub, iss, aud, exp, iat, tid, ver, preferred_username, email` ほか）。
  それでも v2 のトークンには必ず `oid` が載る — つまり `claims_supported` は**過少申告**で、
  これを条件にすると必要な場面でこそ発火しない。
  → 条件は **`pairwise` かつ「その issuer が既にこのデプロイで使われている」だけ**にした。
  ★ 出ないクレームを指定してしまっても**無害**なのが効いている: トークンに無ければ
  `realm_subject` が空のまま記録され、`identityIDForRealmClaim` は空の subject を
  即座に弾く（`store_sqlite.go:624`）ので、誤って別人に繋がることはない。
  **効かないときの代償が無い**なら、必須にして困る場面が無い。
- `pairwise` で名乗れる安定クレームが無い場合は**保存を拒否しない**。§61.16（本人の同意で
  紐づける）が後から救えるので、代わりに**保存時と承認画面の両方に警告を出す**
  （誰に着地するかが変わる＝承認者が見るべき情報）。
- (b) には副作用が 2 つ残る。`t:` id なので**そのセッションはそのテナントに固定される**（決定 32-3）
  ＝兼務の人は他テナントを使えない。そして `client_secret` が DB に複製される。

★ **「分裂する」は誤りだった（レビューで訂正）。実際に起きるのは拒否で、そちらの方が重い。**
コードを追うと、既にこのデプロイでログインしたことがある人が (b) の新しいボタンを押した場合:

1. `(provider, subject)` の一致なし（別アプリ登録＝別 `sub`）
2. 規則 1.5 も当たらない（`realm` は同じでも `subject` が違う。`link_claim` 未設定なら
   `realm_claim` の第 2 鍵も無い・`store_sqlite.go:484-495`）
3. email で既存 identity が見つかるが、テナント定義なので `EmailJoin=false` → **規則 2'**。
   その identity は既に IdP 行を持つ（`identityHasProvider`）ので `errIdentityClaimed`
   （`store_sqlite.go:505-512`）
4. コールバックはセッションを発行せず `/login?error=email_taken` へ（`oauth.go:526-531`）

つまり **identity は 2 つに割れない — その人はそのボタンからは入れないだけ**。新規の人
（このデプロイで初ログイン）は普通に作られるので、被害は「既存の利用者だけが弾かれる」形になる。

- **救済導線は文言としては存在する。** `email_taken` の画面が「いつも使っている方式でログイン
  したうえで、設定 → アカウント → サインイン方法 からこの方式を追加してください」と
  日英で書いている（`oauth.go:933` / `:973`）。押せるリンクではないが、そもそも一度
  古い方式で入らないと始まらない導線なので、リンクにしても意味が無い。
- ★ **ただし前提は「古い方式がまだ生きている」こと。** (b) の典型は**アプリ登録の差し替え**で、
  移行後に古い行を停止したくなる。停止した時点で、まだ紐づけていない人は**自力で復旧できない**
  （`AttachProvider` は既にどこかに結びついた `(provider, subject)` を受け取らないので
  `linkErrTaken`・`oauth_link.go:53`。そもそも紐づけを始めるセッションが作れない）。
- → **(b) の面に順序を書き、UI にも守らせる**: 新しい行を有効にしても**古い行は止めない**。
  古い行を停止できるのは、そのテナントの利用者が新しい方式を紐づけ終わってから。
  ★ この順序は §61.17.5 末尾の「順序を UI が守る」と同じ性質の規則で、
  **後から効いてくる制約は必ず操作の側に置く**（文書に書くだけでは守られない）。

★ **実装（P7-3）で決めたこと。**

- **数えるのは「その方式しか使ったことのない現役メンバー」**（`CountMembersOnlyOnProvider`）。
  ★ 「その他の方式」は**このテナントの中に限らない** — `identity_provider` の行は*証明済みの
  ログイン*なので、他テナントの方式であってもそこから入り直せる。逆に、行が 1 つも無い人
  （招待だけされてまだ入っていない人）は数えない。その人が失うものはこの方式ではない。
- ★ **拒否ではなく 409 の確認**（`?confirm=1` で通る）。停止は「漏れた IdP を止める」手段でも
  あり、**止めるのは常に始めるより速くてよい**（§61.11.6 の役割分担）。順序を守らせたいのは
  移行の場面であって、事故の場面ではない。
- ★ **人数は CP しか知らないので、error の隣に `members` として返す**。共有の error 封筒は
  `{code, message}` で、そこを広げると全ハンドラに波及するため、この 1 応答だけ手で書いた。
  Console は数だけ受け取って**自分の言語の文言に差す** — CP の英文をそのまま出すと、
  表示言語が CP のものになってしまう（§28 の「生成物の言語軸≠表示言語」と同じ話）。

### 61.17.5 UI — 行が 1 本のリストになり、CSV が 2 本消える

テナントの「サインイン方式」を**行のリスト 1 本**にし、各行に 2 つのトグルを置く:

```
[受け入れる] [ボタンに出す]   Microsoft でサインイン（子会社）  t:sub:entra   承認待ち
[受け入れる] [ボタンに出す]   GitHub でサインイン               github        デプロイ共通
```

- 自前の行（作成・編集可・承認が要る）と、既定テナントの方式（バッジ「デプロイ共通」・
  編集不可）が同じリストに並ぶ。**その画面が門の全体を映す**ようになる。
- ★ **［方式を追加］は (b)「新しく作る」だけにする（レビューで変更）。** 当初案は 2 択
  （①既定テナントの方式から選ぶ＝参照 ②新しく作る）だったが、①は**「受け入れる」トグルを
  ON にすることと同じ 1 ビット**で、同じことに 2 つの名前と 2 つの操作を与えていた。
  「参照行を編集できると思った」「追加したのに一覧に増えない（元から在る行が ON になるだけ）」
  という誤解は、この二重化から出る。→ **既定テナントの方式は常に行として並べ**、未参照なら
  「受け入れる」が OFF なだけにする。追加＝(b) だけ、参照＝トグル、と語彙を 1 対 1 にする。
  ★ これで「今まで置き場の無かった、このデプロイで使えるサインイン方法の一覧」という
  §61.17 の出発点（Google が画面のどこにも出ない）は、**ピッカーの中ではなく一覧そのもの**で
  果たされる。
- DB 表現は据え置き（`allowed_providers` / `hidden_providers` の CSV）。**画面から CSV が
  消えるだけ**で、スキーマ変更は無い。§61.11.8 が足した「打ち込む id の一覧」も、
  自由入力ごと不要になる（400 `unknown_provider` も、存在しない方式を例示する
  placeholder も同時に消える）。

★ **実装上の罠**（いずれも「空＝全部」という既存の意味と、既存の安全弁から出る）:

- **「受け入れる」の最後の 1 つは OFF にできない。** `allowed_providers` は空＝全部受け入れ
  （§61.9.4・`providerInList`・`providerAllowed` の `len(...)==0` 早期 return）なので、
  全部 OFF にすると保存の結果は「全部 ON」になる。トグルを全部落とせる UI は、
  **絞ったつもりで全開にする**。最後の 1 行は `disabled` にして理由を出す。
- ★ **「出す」側にも同じ罠がある（レビューで追加）。** `hidden_providers` にも
  「全部隠したら無視する」安全弁がある（`oauth.go:794-804` の `if len(kept) > 0`）。だから
  全行の「出す」を OFF にする操作は**保存できてしまい、そして効かない** — 画面は全部 OFF、
  ログイン画面には全部出る、という**嘘**になる。「出す」も最後の 1 つは `disabled`。
- ★ **矛盾する組み合わせは、そもそも表現されない（レビューで追加）。** ボタンの描画は
  hidden の判定の中でも `providerInList(allowed, …)` を要求する（`oauth.go:797` と `:808`）。
  つまり **受け入れる=OFF の行の「出す」は、ON にしても何も起きない**。→ 「出す」は
  「受け入れる」の**従属トグル**にして、親が OFF のときは `disabled` ＋
  「受け入れていないので出ません」と出す。DB 側は `hidden ⊄ allowed` を許したままでよい
  （既存データを壊さないため）。矛盾を弾くのではなく、作れなくする。
- **未設定のテナントは「全部 ON」と描く。** ★ ただし**最初の操作で明示リストに固めない**
  （レビューで変更）。「空」は*デプロイに追従する*という意味を持っていて、明示リストに
  固めると**以後 env に足した新しい方式をそのテナントだけ受け入れなくなる** — トグルを
  1 つ触っただけの人が、将来の設定変更を黙って拒否する状態を作ってしまう。
  → 正規化規則を **「全部 ON なら空で保存する」** にする。追従は保たれ、画面の見た目は同じ。

★ **順序を UI が守る。** 先に絞ってからテナント管理者を招くと、その人が入れない。
最後の 1 つを OFF にできない規則（上）と、既定テナントの方式を全部 OFF にする操作は
**そのテナントに active な自前の行が 1 つ以上あるときだけ**許す規則で守る。
最初の行は super_admin が作って承認できる（§61.17.1）ので、循環はしない。

★ **実装（P7-0）で分かったこと。**

- **2 つの規則は 1 本の関数で足りた。** `ruleLocks(knownIds, usableIds, …)` の
  `usableIds` を「デプロイの方式＋**active かつ usable な自前の行**」にすると、
  「最後の 1 つは OFF にできない」がそのまま**順序**の規則になる — 承認前の行は
  usable でないので、自前の行が動き出すまで既定テナントの方式を外せない。
  順序のためのコードは 1 行も要らなかった。
- **トグルを倒せるのは super_admin だけ**（`PUT .../login` は `withSuperAdmin` 固定＝
  決定 19 は変えていない）。テナント管理者には同じ状態を**静的なチップ**で見せる。
  押せないトグルを出すのは「できる」と言って断ることになる。
- ★ **保存は 4 列を丸ごと置き換える PUT なので、この面が持っていない 2 列
  （`auto_join_domains` / `allowed_domains`）を読んだ値のまま送り返す**必要がある。
  逆にログイン規則の面も、方式の 2 列を同じように送り返す。片方でも落とすと、
  もう片方の面での設定が黙って消える。
- ★ **トグルを触らせてよいのは、デプロイの方式の一覧が読めているときだけ。**
  読めていないと `knownIds` が本物でなく、「全部 ON なら空」の正規化が**知らない id を
  落とした結果**で走って、絞ったつもりのないテナントを絞る。§61.17.9 ② の 3 状態が、
  表示だけでなく**保存の可否**にも要る。

### 61.17.6 素の `/login` に効かせるのは「出さない指定」だけ

`handleLogin` は slug 無しを「テナントを知らない画面」として扱い、有効な方式を全部出す
（`oauth.go:730-765`）。P7 では slug 無しを**既定テナントとして描画**する。

- §61.15.13 の穴が閉じる。「隠す指定は素の `/login` に効かない」は、素の `/login` が
  どのテナントでもなかったから成立していた。既定テナントのページになれば、その規則が効く。
- ★ **描画だけ。** どのテナントに入れるかは membership とテナント規則が決める（§61.9.2 の ★）。
  未知の slug が汎用ページに落ちる挙動も変えない（slug の存在を教えないため）。

★★ **レビューで見つけた穴 — このまま実装するとデプロイ全体を締め出す。**
「全部隠したら無視」の安全弁は **`hidden_providers` にしか無い**。`loginButtons` を読むと:

- hidden の絞り込みには弁がある（`oauth.go:794-804`。`kept` が空なら `visible` を元に戻す）
- **`allowed_providers` の絞り込みには弁が無い**（`:808` の `continue`）。全部落ちると
  `shown == 0` で `errTenantNoProvider` の**ボタン 0 のページ**になる（`:824-828`）

つまり P7-1 を素直に入れると、**既定テナントの `allowed_providers` を絞った瞬間に
素の `/login` からボタンが消え得る**。そこは「どのテナントにも属さない人」（新任 super_admin・
招待前の人）の唯一の入口で、しかも回復手段が無い — `PUT /api/admin/tenants/{slug}/login` は
`withSuperAdmin`（`routes.go:157`）＝セッションが要り、そのセッションはログインでしか作れない。
既存の super_admin セッションが `AF_SESSION_TTL` で切れたら、**DB 直編集以外に戻す道がない**。
今日この事故が起きないのは、まさに素の `/login` が「どのテナントでもない」からで、
決定 42 はその安全性を代償にしている。

★ **したがって決定 42 は範囲を狭める（レビューでの変更）: 素の `/login` には
`hidden_providers` だけを適用し、`allowed_providers` は適用しない。**

- 目的（§61.15.13 が言っていた「隠す指定が素の `/login` に効かない」を閉じる）はこれで
  完全に達成される。閉じたかった穴は hidden の側にしか無い。
- 締め出しは**構造的に起きなくなる** — 弁のある側しか使わないので、素の `/login` の
  ボタンが 0 になる経路が消える。運用の注意書きで守る必要も無い。
- 門は今日と 1 行も変わらない（`checkTenantProvider` はテナント解決時にそのまま効く）。
  ここで `allowed_providers` を描画に使わないことは、§61.9.2 の「ログイン画面は表示、
  門は resolver」をむしろ素直になぞる。

★ **もう 1 つの実装注（同じく描画の話）。** 「既定テナントのページとして描く」を
`loginButtons(..., tenant: "default", ...)` で実装すると、表示以外に 2 つ動く:

1. ボタンの URL に `tenant=default` が乗り（`oauth.go:816-818`）、state cookie の `Tnt`
   （`:432`）を経て、ログイン後の遷移先に `?tenant=default` が付く（`withTenantHint`）。
   認可ではない（`oauthState.Tnt` のコメントどおり、Console は自分の membership に
   在るときしか従わない）が、**兼務の人の初期選択が全員 `default` に倒れる**。
2. `providersForSlug(ctx, "default")` が呼ばれ、**既定テナント自身の `t:default:*` 行が
   素の `/login` に並ぶ**（決定 32-4 が避けたかった形に一歩近づく）。

→ `loginButtons` は **「規則の出どころ」と「URL に載せる slug」を別の引数に分ける**。
素の `/login` では前者だけ既定テナントにし、後者は空のまま。どちらの副作用も起きない。

★ **実装（P7-1）で分かったこと。**

- **引数を分ける必要は無かった。** `loginButtons(ctx, next, lang, tenant, allowed, hidden)` は
  最初から slug と規則が別の引数で、素の `/login` は `tenant=""` のまま `hidden` にだけ
  既定テナントの値を入れれば済んだ。実装は `handleLogin` の 3 行。副作用 2 つ
  （`tenant=default` の持ち回りと `t:default:*` の露出）は、slug を空に保つだけで両方消える。
- ★ **未知の slug のページは、素の `/login` と 1 バイトも変わってはいけない。** 未知の slug は
  汎用ページに落ちる（slug の存在を教えないため）が、**片方だけに既定テナントの規則を効かせると、
  2 つを見比べれば slug の存否が分かってしまう**。同じ分岐に入れて同じ描画にする。
  テストでも「両者が完全一致」を固定した。
- 「既定テナントが存在しない方式を受け入れると宣言している」状態（`allowed_providers=okta`
  だが okta は env に無い）は、テナントページなら「使える方式が無い」の行き止まりになる。
  素の `/login` では**全部のボタンが出る**ことをテストで固定した — これが決定 42 を
  狭めた理由そのものなので、回帰したら気付けるようにしておく。

### 61.17.7 段階

| 段階 | 内容 | スキーマ |
|---|---|---|
| **P7-0a**（実装済み）| ★ 先に `GET /api/admin/providers` を **tenant_admin にも読ませる**（ただし `issuer` は super_admin 限定・§61.17.9）＋ Console の 403 潰しを直す。P7-0 の前提で、単独でも「Google が画面に出ない」が半分解ける | 無し |
| **P7-0**（実装済み）| テナントの「サインイン方式」を統合リストにする（自前の行＋既定テナントの行・2 トグル・従属関係・「全部 ON なら空で保存」）。ログイン規則の面からは方式の 2 列が消え、ドメインの 2 列だけになる | 無し |
| **P7-1**（実装済み）| 素の `/login` に**既定テナントの `hidden_providers` だけ**を適用（決定 42・§61.17.6）。§61.15.13 の運用回避を 3 面 6 ファイルから撤去 | 無し |
| **P7-2**（実装済み）| 新規インストールの既定を `AF_PROVISION=invite` に（**テンプレートの既定**として＝`.env.example` と ECS の `AfProvision`。CP の既定は `auto` のまま）。§61.10.2 を「super_admin だけが入れる状態から始める」に一本化し、**招待前の着地面**（`NotProvisioned`）を新設 | 無し |
| **P7-3**（実装済み）| (b) の `link_claim` 必須化と discovery による `pairwise` 判定、古い行を止める順序のガード（409＋`?confirm=1`） | 無し |

★ **意味の反転はしない。** 既存テナントの「空＝全部」はそのまま。厳密になるのは、
新規テナント作成時に選んだ方式を**明示で書き込む**分だけ。

★ **`AF_PROVISION` の既定を倒すのは破壊的変更**（P7-2）。`auto` で回っている単一テナントの
会社が、更新した瞬間に super_admin 以外締め出される。

★★ **「membership が 0 行の新規インストールだけ invite」は、起動ごとに判定すると逆に危ない
（レビューで否決）。** 判定自体は可能で、条件も妥当ではある — membership 行は削除されず
`status` を落とすだけ（`store_sqlite.go:1080`）なので、0 行はほぼ新規インストールに限られ、
`EnsureDefaultTenant`（`main.go:149`）の直後に数えれば起動シーケンス上も無理がない。
問題は**判定が固定されないこと**:

1. 新規インストール（0 行）→ `invite` で起動する
2. super_admin が最初のテナントとメンバーを作る → membership が 1 行以上になる
3. **次の再起動で条件が「0 行ではない」に転び、既定が `auto` に戻る** ＝ invite で
   始めたはずのデプロイが、誰も何も変えていないのに**黙って開く**

派生させるなら**初回起動で解決した値を永続化**する必要があり、そのために設定行を 1 本
足すのは「env もスキーマも増やさない」という P7 の性質を崩す。→ **インストーラと
インストールガイドが生成する env の既定を `AF_PROVISION=invite` にする**方を採る。
実行時の魔法がゼロで、既存デプロイは 1 バイトも変わらない。

### 61.17.8 残る未決

- ~~「デプロイ直後にログインできるのは super_admin だけ」を**字義どおり**にするか。~~
  → ✅ **P7-2 で決着（着地画面の方を採った）。** サインイン自体は今までどおり通り、
  membership が無ければ `not_provisioned`（`resolver.go:172`）。`authGate` で membership を
  要求すれば字義どおりになるが、それは §61.9.2 の「テナントの門を `authGate` に置くな」に
  正面から抵触するので採らない。代わりに**着地を「まだ招待されていません」の面にした**
  （§61.10.2）。★ 字義どおりでないことに実害は無い — サインインが通っても、membership が
  無ければ入れる面は 1 つも無い。**変わったのは「拒否の見え方」だけで、門は 1 つも動かしていない。**
- 既定テナントの方式に **step-up（昇格）** を要求するか。「デプロイ所有の方式だけが super_admin を
  運べる」（§61.17.3）が画面に出ると、その 1 本に管理者権限が集中していることも同時に見える。
  短命な昇格 cookie で `withSuperAdmin` を締める案は別途。★ ただし **PAT の
  `dangerous`（`pat.go:41-46`）と役割別 docs（`workspace_docs.go:48`）は昇格を持たない**ので、
  そこを塞がない昇格は迂回路つきになる。
- 既定テナントの表示名。`Default` のままだと、そこが「オペレーターのテナント」だと読めない。

### 61.17.9 周辺（レビューで足した観点）

**① `GET /api/admin/providers` の閲覧を広げるときは `issuer` を落とす。**（★ P7-0a で実装済み。
ゲートは新設の `anyTenantAdminFor`（`httpapi.go`）＝ super_admin または**どこか 1 つでも
active な tenant_admin** を持つ人。素のメンバーは 403 のまま。`issuer` は `ident.Role` を見て
**キーごと落とす**（空文字にすると空セルが設定漏れに見える）。`routes.go` の登録を
`withAnyTenantAdmin` に差し替え、`tenant_login_test.go` は「メンバーは 403 / tenant_admin は
200 だが issuer 無し / super_admin は issuer 有り」を固定した。★ この gate は**書き込みには
使えない** — slug を受け取らないのでどのテナントの管理者かを検査しておらず、全テナントで
同じ値を返す読み取りにしか使えない。）
P7-0 では他テナントの管理者が「デプロイの方式」を読む必要があり、今は `withSuperAdmin` 固定
（`routes.go:177`）で、`tenant_login_test.go:657-660` が「tenant_admin には 403」を明文で
固定している。ADR は「機密性の根拠は元々薄い（id とボタン文言は未認証の `/login` に出ている）」
と書いたが、★ **この API は `issuer` も返す**（`login_provider_api.go:60-63`）。Entra の
`https://login.microsoftonline.com/<tenant GUID>/v2.0` や `https://acme.okta.com` は
`/login` には出ない。→ **編集は super_admin、閲覧は tenant_admin。ただし tenant_admin に
返すのは `id` と 2 言語のラベルだけ**にし、`issuer` は super_admin の応答にのみ載せる。
テストもその形（403 → 200 だが列が減る）へ書き換える。

**② Console の 403 潰しを先に直す。**（★ P7-0a で実装済み。）`DeploymentSignInMethods` は
`res?.providers || []` で 403 を空配列に潰し、「このデプロイにはサインイン方法が設定されて
いません」と**嘘を表示していた**。`api()` は 403 を `{error:{code,message}}` として返す
（`core/api/client.ts:210` / `:217`）ので分岐は書ける。この嘘が今まで見えなかったのは、
この部品が super_admin の編集フォームの中にしか置かれていなかったからで、
**読める相手を広げた瞬間に表に出る**性質だった。
→ 状態を `null`（読み込み中）／`"error"`（読めなかった）／配列（読めた・空配列は本当に 0 件）
の 3 通にし、判定は **`Array.isArray(res?.providers)`**（`res.error` の有無ではなく、
欲しい形が来たかどうかで見る）＋ `.catch`（通信断は reject で来る）。
i18n は `admin.providers_unreadable` を ja/en に新設。

**③ 監査ログは足りている — 意味づけだけ変わる。** 2 トグルは既存の
`PUT /api/admin/tenants/{slug}/login` を叩くので、`tenant.login_rules` の監査が
そのまま残る（`tenants.go:238-244`・Detail に 4 列の CSV が入る）。★ ただし Detail は
CSV の羅列なので「参照を外した」も「絞った」も同じ形で残る。P7 で**画面の語彙が変わっても
監査の語彙は変わらない**ことを承知して読む必要がある（テナント IdP 行の作成・承認は
別イベント）。新しい監査アクションは要らない。

**④ i18n。** 新しい文言は ja/en 両方に足す（裸和文 lint がある・docs/28）。既存の
`admin.providers_*` 系に揃える。②で 3 通に分けるので `admin.providers_none` は
1 キーでは足りない。

**⑤ ガイドへの波及（P7-1）。** §61.15.13 の運用回避は**二言語 3 面 6 ファイル**に書かれている:
`docs/admin/README(.ja).md`（「素の `/login` には効きません」）・
`docs/operate/02-install(.ja).md`（表の行＋2 か所の注記）・
`docs/operate/05-signin(.ja).md`（ボタンが出る場所の表と手順 5）。P7-1 で全部
書き換える。★ `.md`（英語が正）と `.ja.md` の**両方必須**。

**⑥ 既存デプロイの移行。** スキーマ変更が無いので移行スクリプトは不要。★ ただし
**既定テナントの `allowed_providers` が既に絞られているデプロイ**があり得る（今日は素の
`/login` に効かないので、絞ったことに誰も気付いていない）。§61.17.6 の縮小案
（素の `/login` には hidden だけ適用）を採ればここは何も起きない — **移行の心配が
消えることも、あの案を採る理由の 1 つ**。

## 61.18 後始末 — 消す操作をどこまで作るか（2026-08-22）

管理画面には「作る」しかない操作が 3 つ残っていた。golden スナップショットの自動焼き直し
（0.9.2・docs/64 §64.28）を実デプロイ 2 本（開発配備 / 本番配備）で実走させたときに、
3 つとも同時に表に出た。

1. 自動焼きが作る**予約テナント `af-golden`**（表示名 `golden snapshot (system)`）が、人の
   テナントと並んで一覧に出る。中の `af-golden-seed` / `af-golden-probe` は**製品の通常の
   Start 経路**で workspace を作る（そうでなければ焼けた golden は「製品が実際に作る home」の
   複製ではなくなる）ので、`af-membership` タグが付き、人のメンバーとして費用画面にも出る。
2. **テナントは作成しかできない。** 本番配備で手焼き時代の捨てテナント 2 つがスロットを
   塞ぎ、自動焼きまで止めた（利用者が Console から除名＋破棄して解消）。
3. **除名は論理削除だけ**で、行を消す手段が無い。`SetMembershipStatus` のコメントが
   *"Hard deletion is deliberately not offered — schedules, audit rows and shares reference
   the membership id"* と言っていた。

### 61.18.1 通した筋 — 守るのは「戻る道」と「実体への手掛かり」

§61.10.6 で 2026-08-21 に直した「自分の*最後の*有効な membership だけ拒否する」と同じ考え方で
3 件を揃えた。**守るのは"戻る道"であって「自分の行かどうか」ではない**——同じ言い方をすると、
ここで守るのは次の 2 つで、それ以外は消してよい。

- **戻る道**（サインインできなくなる／管理者が居なくなる）
- **実体への手掛かり**。DB の行は、クラウドやディスクに残った資源（home・EBS ボリューム・
  EFS アクセスポイント・bare リポジトリ）に対する**唯一のハンドル**であることがある。
  行を先に消すと、資源は課金され続け、誰もそれを指せなくなる。ADR 0045 決定 13-2 が
  「破棄できるのは inactive なメンバーだけ」と言っているのと同じ理由。

その裏返しとして、**履歴は消さない**。監査ログ・クラウド費用・稼働時間の 3 つは、消す操作の
副作用で変わってはいけない。とくに:

- **監査**は、除名の後始末で除名の記録を消せてはいけない。幸い `audit_log` は
  `membership_id` を持たない（actor は identity・`0007_audit.sql`）ので、放っておけば残る。
- **費用**は `memberCloudCost` が membership_id だけで引く（`cloudcost.go`）。これは
  「workspace を破棄された人の支出も消えない」ようにするための設計で、行を消すと
  **過去月の合計が後から変わる**。

### 61.18.2 予約テナントは「隠す」（消す物ではなく入れ物）

`af-golden` は焼き直しのたびに**使い回される**。毎回破棄されるのは workspace と home と
スロットだけで、テナントとメンバーシップの行は残る。だから正しい扱いは削除ではなく非表示。

- 判定は `system_tenant.go` の `isSystemTenantSlug` に 1 か所だけ置く。同じ判定が
  「一覧から外す」「削除させない」「費用を寄せる」の 3 面に効くので、slug を直書きすると
  次に予約テナントが増えたとき 1 つだけ取り残される。
- **落とすのは API 層**（`adminAPI.listTenants` と `adminAPI.usage`）で、
  **`store.ListTenants` は素通しのまま**にする。★ あの store 呼び出しには監査ビューの
  `tenant_id → slug` 解決（`audit.go`）と費用ポーラーの `membership → tenant` 解決
  （`cloudcost.go`）が乗っている。store で消すと、そちらが「テナントの分からない行」を
  作りはじめ、症状は管理画面ではなく監査と請求に出る。
- Console は横断ビュー（セッション／稼働時間／費用／監査／MCP 配布）のテナントフィルタにも
  この一覧をそのまま渡しているので、API 1 か所で全部の面から消える。
- **プール画面の golden 表示（`pool.golden_*`）は別物**なので残す。あちらは
  「いまの golden が実行中のイメージと合っているか」で、テナントの話ではない。

### 61.18.3 予約メンバーシップの費用は SHARED へ（タグは打ったまま）

`af-membership$<値>` でグループ化し、**値が空＝SHARED バケット**（ADR 0048・docs/67）。
種と probe の分をそこへ寄せる。やり方は 2 つ考えられて、**タグを打たない案は採らない**。

- ★ `af-membership` は**コスト配分キーであると同時に照合キー**でもある。ランタイムは
  `tagValue(ap.Tags,"af-membership") == e.base.membershipID` で EFS アクセスポイントと home
  ボリュームを引き当てる（`runtime_ecs_ec2.go` の `ensureAccessPoint` ほか）。値を空にすると
  引き当てが壊れるか、次に現れた無タグ資源と衝突する。
- → **タグは製品の通常経路のまま打ち、Cost Explorer から取り込む段で `""` へ畳む**
  （`foldSystemMemberships`）。golden スナップショット自体は元から `af-membership` 無し＝
  すでに shared（`deploy/aws/ecs/README.md`）なので、これで種・probe・スナップショットの
  3 つが揃って共有インフラに入る。
- ⚠️ **畳んだら Go 側で合算する。** `PutCloudCost` は `(day, membership_id, service)` を
  **置き換える**実装（`ON CONFLICT ... DO UPDATE SET unblended=excluded.unblended`）なので、
  足さずに 2 行渡すと**後の行が前の行の金額を消す**。CE は種の行と無タグの共有行を、同じ
  (day, service) の別グループとして日常的に返す。
- 取り込みの窓は既定 7 日なので、**それより古い既存行は畳まれない**。読み側
  （`adminAPI.cloudCost`）でも同じ畳み方をする。財務データを書き換えるマイグレーションは
  書かない。

### 61.18.4 テナントの削除 — 空のものだけ

`DELETE /api/admin/tenants/{slug}`（super_admin）。拒否は 5 つで、どれも §61.18.1 の
「実体への手掛かりを先に消さない」の言い換え。

| 条件 | code | 理由 |
|---|---|---|
| 予約テナント | `system_tenant` | 次のベイクで作り直されるだけ |
| 既定テナント | `default_tenant` | `EnsureDefaultTenant` が起動時に作り直す |
| active な membership がある | `tenant_not_empty` | 先に除名する。これは退職処理の道具ではない |
| workspace 行がある | `workspace_present` | home・EBS・EFS が宛先を失って課金され続ける |
| 内部 git リポジトリがある | `git_repos_present` | bare とその LFS がディスクに残る |

⚠️ **内部 git リポジトリには順序の罠がある。** `DELETE /api/internal-git/repos/{name}` は
`withMembership` ゲート（`internal_git.go`）なので、**最後のメンバーを外した後は誰も消せない**。
だから拒否のメッセージが「メンバーが名簿に残っているうちに消してください」と順序を言う。
ここから消してしまう案は採らなかった——「テナントを削除」という名前の操作が、黙って
リポジトリを破壊してはいけない。

消えるのは、残っていた inactive な membership（下の cascade と同じ）と、テナント設定の行
（`mcp_server` / `tenant_idp` / `egress_allowlist`、そして tenant の列にあるログイン規則と
`allowed_cidrs`）。⚠️ 監査ビューは `tenant_id → slug` を `ListTenants` で引くので、消えた
テナントの過去行は**テナント欄が空**になる。したがって `tenant.delete` の監査行は
Target に slug、Detail に表示名を入れる（削除**後**に書く）。

### 61.18.5 「外したメンバー」を消す

`DELETE /api/admin/tenants/{slug}/members/{key}`。ゲートは `tenantAdminFor` — すでに home ごと
消せる `destroyWorkspace` と同じ線で、**その前提条件（破棄済み）を作れるのも同じ人**だから。

後始末は 3 段で、画面に出る危険操作は常にそのうちの 1 つだけにする（`member.state` が
`"none"`＝workspace 行が無い、で出し分ける）:

```
メンバーを外す（status='inactive'）→ Workspace を破棄 → メンバーを完全に削除
```

拒否は 3 つ: 予約メンバーシップ（`system_membership`・次のベイクで作り直される。焼いている
最中ならスロットを掴んだまま宙に浮く）／active（`membership_active`）／workspace 行が残っている
（`workspace_present`）。

消えるのは `membershipCascade`（`store_sqlite.go`）が並べる表——`user_limit` / `pat` /
`ssm_host` / `ssm_profile` / `schedule` / `schedule_run` / `memo` / `memo_category` /
`notification` / `notification_usage_state` / セッション共有 4 表 / `workspace_stop_intent` /
`membership`。★ `ON DELETE CASCADE` に頼らず明示的に並べるのは `DeleteWorkspace` と同じ理由で、
宣言しているのは一部の表だけであり、スキーマ依存の「半分だけ消えた」は本番でしか出ない。

**残るのは `audit_log` / `cloud_cost_daily` / `usage_daily`、そして `identity` 行**。
identity を消さないのは、その人が別テナントの名簿に居るかもしれないことと、居なくても監査行が
指しているため。membership は席であって人ではない。

⚠️ **表名を直に並べるということは、2 つのマイグレーション系列が一致しているという前提に
賭けるということで、その前提は外れていた。** `memo_category` は `migrations/0020` にあって
`migrations-pg` には無く、**Postgres デプロイ（＝ECS/RDS の実デプロイ）ではカテゴリの API が
ずっと 500 を返していた**——しかも Console は配列でない応答を空リストに畳むので、症状は
「エラー」ではなく「カテゴリが出ない」で、誰も障害として報告しようがなかった。この cascade を
書かなければ、たぶん今も見つかっていない。

→ **その場しのぎ（SQLite のときだけ 1 文足す）で出したあと、2026-08-22 に本体を直した**:
`migrations-pg/0030` で写し、`TestSchemaDialectParity`（`store_schema_parity_test.go`）が
**両系列の着地スキーマを実測で突き合わせる**。cascade は 1 本の平らなリストに戻した——
方言分岐が消えたのは、分岐の理由が消えたからであって、諦めたからではない。
実 Postgres への往復は `TestPostgresStore`（memo カテゴリの CRUD を追加）と
`TestPostgresDeleteCascade`。3 本とも `AF_TEST_DATABASE_URL` が無ければ skip なので、
**マイグレーションを足したら実 Postgres で 1 度回す**（docs/dev/06 §6.4・docs/dev/10 §10.4）。
