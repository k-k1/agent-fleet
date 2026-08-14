# 0043. ログイン IdP はプロバイダ別実装を増やさず「汎用 OIDC 1 本 ＋ GitHub だけ専用」にし、同一人物の保証を GitHub より先に入れる

- 状態: 採用・**未実装**（2026-08-14。設計は docs/61）
- 関連: [61-login-idp.md](../61-login-idp.md) /
  [dev/07-security.md](../dev/07-security.md) §7.3（AUTH 3 モード＝現行契約） /
  [dev/06-data-model.md](../dev/06-data-model.md)（`identity` / `user_key`） /
  [0001-self-host-vs-saas.md](0001-self-host-vs-saas.md)（各社が自社でセルフホスト＝IdP は各社のもの）

## 背景

L1 ログインの IdP が Google 固定（`control-plane/oauth_google.go`）。セルフホスト先は
「グループ各社が自社の社員をホストする」前提（roadmap §12.2）なので、Google Workspace を
使っていない会社は設置自体ができない。想定顧客の多数派は M365 で、これは配布の前提条件を欠く。

コードを実測して分かった、設計を決めた 3 点:

- **OAuth の配線は軽い。** Google 実装は id_token を検証せず、認可コード → トークン → userinfo で
  email を取るだけ（`oauth_google.go:238-256`）。JWT ライブラリを持っておらず（`go.mod`）、
  stdlib で完結している。汎用 OIDC も discovery JSON を 1 回読む差しかない。
- **重いのは identity 側。** `identity.user_key = sanitizeUser(email)`（`resolver.go:281`）が
  **workspace の home ディレクトリ名であり、暗号化 secrets の帰属先**（dev/07 §7.2）。
  つまり email が identity そのものになっている。既に `disambiguateUserKey`
  （`store_sqlite.go:337`）が sanitize 衝突による identity 併合を防いでいて、この一意性は
  意図的に守られている。
- **email の信頼根拠は IdP ごとに違う。** 現行は `email_verified` を見ている（Google は出す）が、
  **Entra ID はこのクレームを出さない**。一律 `email_verified == true` を必須にすると Entra が通らず、
  逆に外すと GitHub で「他人の会社 email を登録して許可リストを通過」が成立する。

## 決定

1. **プロバイダ別実装を増やさず、汎用 OIDC クライアントを 1 本作る。** Entra ID / Okta / Keycloak /
   Auth0 / Cognito / GitLab はこれ 1 本で載る。Google も内部的にこの実装の 1 インスタンスへ移す
   （**env 名は据え置き** — 既存デプロイは設定を 1 行も変えずに動く）。
2. **専用アダプタを書くのは GitHub だけ。** GitHub は OIDC 非対応（OIDC は Actions 用トークンで、
   ユーザーログインには無い）ため避けられない。**org メンバーシップ判定とセットでのみ**入れる —
   個人 GitHub アカウントは会社の統制外なので、email 許可リストだけで開けてはいけない。
3. ★ **同一人物の保証（P1）を GitHub（P2）より先に入れる。** GitHub の登録 email は会社 email と
   違うのが普通で、`user_key` が email 由来である以上、リンク機構なしに出すと
   **identity が 2 つ・home が 2 つ・secrets が別**になる。ユーザーからは「リポジトリが消えた」に見える。
   順番は **P0 汎用 OIDC → P1 リンク → P2 GitHub**。P0 だけで「Microsoft でログインしたい」は満たせる。
4. **`user_key` は不変とし、`(provider, subject)` を横に足す。** 新テーブル `identity_provider`
   （`0031`）を作り、`identity` 本体は触らない。`user_key` を `sub` ベースへ作り替える案は、
   home ディレクトリ名なので**既存デプロイ全員のデータ移行**が要る上、`af-ws-<user>` が人に読めなくなる。
   既存デプロイの移行は初回ログイン時に `(google, sub)` 行を書くだけで済む（移行ゼロ）。
5. **別 email の結合はログイン画面から行わない。** サインイン済みの状態で Console の
   「アカウントを追加」から 2 つ目の IdP を通したときだけ結合する（＝両方にログインできることが証明）。
   管理者が対応表で結合する案は、表が保守されないし、弱い検証の IdP が混ざると乗っ取り経路になる。
6. **email の信頼根拠を provider ごとの宣言（`trust`）にする。** `email_verified` /
   `issuer`（テナント固定で担保・Entra） / `api`（別 API の検証フラグ・GitHub）の 3 種。
   **宣言の無い provider は起動時に拒否**して fail-closed を落とさない。
7. **Entra を `common` エンドポイントで受けさせない。** issuer をテナント固定にするか、
   `tid` 許可リストを必須にする。`common` かつ `ALLOWED_TIDS` 未設定は **fatal**。
   これを許すと「Microsoft アカウントを持つ全人類」が入口に立ち、個人 MSA は email を
   付け替えられるので email 許可リストが意味を失う。
8. **redirect_uri は `/oauth2/callback` 1 本のまま**にし、provider は署名済み state cookie に載せる。
   プロバイダごとに URI を分けると、設置する会社が IdP 側へ登録する手数が IdP の数だけ増える。
   コールバックでは state の provider id を**設定済みの集合と突合してから**分岐する。
9. **id_token の署名検証は引き続き行わない。** 認可コードフロー・client_secret 付き・
   トークンエンドポイントから TLS 直受けだから（OIDC Core §3.1.3.7 の注記と同じ論拠）。
   `tid` のように userinfo に出ないクレームは、**同一レスポンス内の id_token ペイロードを
   署名検証なしで読む**。この前提は `oauth_oidc.go` 冒頭に固定し、
   **フロントチャネルで id_token を受ける経路を足すなら JWKS 検証が必須**と明記する。
   これにより Go の依存追加はゼロのまま。
10. **SAML は CP に実装しない。** 日本企業の IdP（HENNGE One / TrustLogin / CloudGate 等）は
    SAML 前提が多いが、SP 実装は OIDC の数倍の面積で stdlib に収まらない。
    **既存の `AUTH=proxy` ＋ oauth2-proxy / Keycloak のブリッジ**を正式な回答として文書化する。
11. **1 つの IdP の設定ミスで全員を締め出さない。** 個々の provider は設定不足なら無効化＋警告、
    **有効な provider がゼロのときだけ fatal**（現行の挙動を維持）。許可リストが全部空なら
    現行どおり警告つき全拒否。
12. **オフボーディングの性質（毎リクエスト再判定）を落とさない。** GitHub の org 判定だけは
    ローカル判定できないので、`(provider, subject)` キーの TTL キャッシュ（既定 10 分）＋
    API 障害時は最後の肯定結果を猶予（既定 1 時間）だけ延命し、超えたら拒否する。

## 却下した案

- **Entra 専用実装を足すだけ。** 最短だが、Okta / Keycloak の要望のたびに同じ作業を繰り返す。
  汎用 OIDC との差は discovery を読む数十行しかない（決定 1）。
- **`AUTH=proxy` で全部済ませる（CP は何もしない）。** 実際 SAML の答えはこれ（決定 10）だが、
  これを既定にすると「compose を上げれば動く」というセルフホストの前提が崩れ、
  設置する会社に oauth2-proxy の運用を丸ごと負わせることになる。
- **email が一致したら常に自動結合し、別 email は管理者の対応表で結合。** 決定 5 のとおり却下。
- **magic link / パスワードログイン。** IdP を持たない小さな会社には効くが、CP が資格情報と
  SMTP を背負う。需要が出てから別 ADR で判断する。
- **Apple / LINE / Slack / Atlassian / Discord。** 会社が入退社を統制する手段にならず、B2B の入口に値しない。

## 影響

- `oauth_google.go`（461 行）は `oauth.go` / `oauth_oidc.go` / `oauth_github.go` に分割される。
  ファイル名の Google 色は消える（`dev/90-code-map.md` の更新が要る）。
- `sessionClaims` に `prov` / `sub` が増える。JSON なので**既存 cookie は欠損フィールドとして読め**、
  移行時の強制ログアウトは不要。ただし `prov` 欠損を `"google"` とみなす暫定規則を 1 版だけ置く。
- `oauthState` に provider id が増える（state cookie は署名済みなので追加は安全）。
- `authGate` の毎リクエスト再判定が provider 分岐を持つ（`oauth_google.go:299-309`）。
- 起動時バリデーション（`main.go:278-284`）が provider 単位になる。
- 配布物 6 箇所に設定例が増える: `deploy/compose/.env.example` / `deploy/local/oauth.env.example` /
  `deploy/aws/ecs/cfn/30-ingress.yaml` / `deploy/aws/ec2-single/README.md` /
  `deploy/compose/README.md` / `docs/guide/operator/*`（guide は**二言語とも**）。
- `dev/07-security.md` §7.3 の「AUTH 3 モード」表は、`oauth` 行が「Google」ではなくなるので書き換え。
- GitHub を入れる会社には **org の OAuth App 承認**という設置手順が 1 つ増える
  （org が OAuth App access restrictions を有効にしていると、承認前は membership が見えず全員拒否になる）。
