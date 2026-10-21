# 0118. `/mcp` の OAuth 対応：Control Plane が自分の MCP の認可サーバーになり、claude.ai・ChatGPT・各 CLI は PAT を貼る代わりに Console へのサインインで接続する

[English](0118-mcp-oauth.md) | 日本語

- 状態：**提案**（2026-10-20）。ドキュメントのみ。以下はまだ何も実装していない。コードの事実はこの日付の
  ツリーから読んだもの。クライアントの挙動は「備考」に挙げた公開文書から読んだもので、実際のクライアントで
  **測ってはいない**。
- フォローアップ：#2433、#2434、#2435
- 関連：[0006](0006-mcp-unified.ja.md) 決定 2（PAT 認証。「ネイティブ OAuth2.1」を後の段階としていた。
  本書がその段階）/ [0032](0032-mcp-2026-07-28.ja.md)（OAuth を対象外にした）/
  [0116](0116-dev-oidc-idp.ja.md)（部品を再利用する開発用 IdP。そのまま使わない理由も）/
  [0047](0047-tenant-network-restriction.ja.md) 決定 3（`/mcp` を IP で制限しない理由）/
  [0043](0043-login-idp.ja.md)（認可ステップが頼るログイン）

## 背景

`/mcp` が受け付けるのは Personal Access Token だけ（`internal/mcpsrv/mcp.go` の `authMCP`）。メンバーは
Console から `af_pat_…` をコピーして、クライアントごとに貼る。CLI ならそれで動くが、メンバーが本当に
使いたいホスト型クライアントでは全く動かない。claude.ai（Web・Desktop・モバイル）と ChatGPT は、リモート
MCP サーバーへの接続を OAuth でしか行わない。しかもいまの `/mcp` の 401 は素の `WWW-Authenticate: Bearer`
で、何も教えてくれない。

0006 が OAuth を見送ったのは、オンプレの oauth2-proxy が MCP クライアントの OAuth フローに参加できなかった
から。その前提はもうない。`AUTH=oauth` では CP 自身がログインを担い（0043）、0116 で認可サーバーに要る
サーバー側の部品がそろった。1 回限りのハッシュ化された認可コード、S256 PKCE、リダイレクトの正規形一致、
失効できる不透明トークン、`form-action` を持たない同意ページ（`dev_idp_page.go`）、発行前に監査を書く型
（`dev_idp.go`・`dev_idp_client.go`）。

プロトコルがいま求めること（MCP 2026-07-28「Authorization」）：

- MCP サーバーは Protected Resource Metadata（RFC 9728）を公開しなければならず（**MUST**）、クライアントは
  そこから認可サーバーを見つけなければならない（**MUST**）。
- 認可サーバーは RFC 8414 か OIDC Discovery を提供し（**MUST**）、公開クライアント向けの OAuth 2.1（PKCE）を
  実装しなければならない（**MUST**）。
- クライアント登録の優先順は **Client ID Metadata Documents**（CIMD、SHOULD）→ 事前登録 → Dynamic Client
  Registration（RFC 7591）。DCR は**非推奨**で、後方互換のために残されている。
- クライアントは両方のリクエストで `resource`（RFC 8707）を送らなければならず（**MUST**）、サーバーは自分宛てに
  発行されたトークンだけを受け付けなければならない（**MUST**）。
- 認可応答の `iss`（RFC 9207）は SHOULD で、MUST に上がる見込み。

ホスト型クライアント 2 つが加える条件（公開文書。「備考」参照）：

- **ChatGPT** は `code_challenge_methods_supported` に S256 を要求し、CIMD を優先する
  （`client_id` = `https://chatgpt.com/oauth/client.json`）。**固定の**リダイレクト
  `https://chatgpt.com/connector_platform_oauth_redirect` を使うのは、サーバーが
  `authorization_response_iss_parameter_supported: true` を広告し、すべての応答でバイト一致の `iss` を返すとき
  だけ。そうでなければ、リダイレクトに接続ごとの ID が付く。
- **claude.ai** は `https://claude.ai/api/mcp/auth_callback` を使う（`claude.com` に移る可能性があると明記）。
  CIMD を選ぶのは、トークンエンドポイントの認証方式に `none` があるときだけ。**Claude Code** はポートが毎回
  変わるループバックのリダイレクトを使う。
- どちらも `/mcp` とトークンエンドポイントを、メンバーのマシンからではなく**ベンダーのサーバーから**呼ぶ。

## 決定

### 1. `/mcp` 用に別の認可サーバーを立てる。`/dev-idp` は開発用ツールのまま

発行者は Console のオリジン上の `{PUBLIC_BASE_URL}/mcp-oauth`。`/dev-idp` と並べて置き、統合はしない。0116 は
正反対の前提で作られている。ワークスペースごとの機密クライアント、プレビューホストへのリダイレクト、
リフレッシュトークンなし、`af_dev: true`、「本番では決して信用しない」。本番の MCP トークンをあの発行者から
出せば、その防御がすべて無効になる。コードは共有し、発行者・テーブル・監査アクションは共有しない。

エンドポイント：

| パス | 認証 | 用途 |
|---|---|---|
| `/.well-known/oauth-protected-resource/mcp` | なし | リソース `{PUBLIC_BASE_URL}/mcp` の RFC 9728 メタデータ |
| `/.well-known/oauth-authorization-server/mcp-oauth` | なし | RFC 8414 メタデータ（パス挿入形式） |
| `/mcp-oauth/authorize` | Console セッション（`authGate`） | サインイン済みのメンバーが認可する。同意ページ |
| `/mcp-oauth/consent` | Console セッション、同一オリジンの POST | 同意フォームの送信先。0116 のペルソナページと同じ形 |
| `/mcp-oauth/token` | クライアント認証 `none` ＋ PKCE | `authorization_code` と `refresh_token` |
| `/mcp-oauth/register` | なし、レート制限あり | RFC 7591。CIMD を持たないクライアント向け |
| `/mcp-oauth/revoke` | トークン自身 | RFC 7009 |

`/mcp` は 401 で
`WWW-Authenticate: Bearer resource_metadata="…/.well-known/oauth-protected-resource/mcp", scope="read"` を返す。
メタデータには `code_challenge_methods_supported: ["S256"]`、
`authorization_response_iss_parameter_supported: true`、`client_id_metadata_document_supported: true`、
`token_endpoint_auth_methods_supported: ["none"]`、`scopes_supported: ["read", "write"]` を載せる。最初の段階
では OIDC のスコープも userinfo も出さない。af の外でメンバーの身元を必要とするものはなく、それを必要とする
ChatGPT のワークスペースドメイン制限は目的に入れない。

### 2. 公開クライアントだけ。PKCE S256・`resource`・`iss` は常に

- クライアントはすべて公開クライアント。シークレットはベンダーのクラウドかノート PC に置くしかなく、PKCE が
  すでに与えるもの以上は何も足さない。
- PKCE S256 は必須（Spring のために任意のままにした 0116 決定 8 とは違う）。ここには退避先になる機密
  クライアントがない。
- `resource` は両方のリクエストで `{PUBLIC_BASE_URL}/mcp` と完全一致しなければならず、それ以外は
  `invalid_target`。トークンはこれに束縛され、`/mcp` は他に束縛されたトークンを拒否する。この発行者が
  `/mcp` 以外のトークンを作れないようにしているのはこの一線。
- `iss` は成功・エラーを問わずすべての認可応答で返し、メタデータの `issuer` とバイト一致させる（ChatGPT は
  不一致を拒否し、`iss` があるときだけ固定のリダイレクトを使う）。
- implicit・password・client-credentials・device の各グラントは提供しない。

### 3. 誰がクライアントになれるか：CIMD とループバックは自由、それ以外のリダイレクトはオペレーターが認めたホストだけ

登録が特定するのは**アプリ**であって、決して人ではない。メンバーが Console にサインインして同意しない限り、
トークンは出ない（決定 4）。登録にできるのは、認可コードの送り先を選ぶことで、脅威はそこにある。攻撃者が
リダイレクト先を自分のサーバーにしたクライアントを登録し、メンバーに認可リンクを踏ませれば、まったく正規の
ログインのあとで、そのメンバーのアカウントの認可コードを受け取れる。フローを始めたのは攻撃者で verifier も
攻撃者が持っているので、PKCE では防げない。

したがって規則はリダイレクトにかけ、CIMD と DCR で同じにする：

- **ループバック**（`http://127.0.0.1/…`、`http://localhost/…`、`http://[::1]/…`）は**ポートを問わず**受け付け
  （RFC 8252 §7.3）、パスは完全一致。そこに送られたコードはメンバー自身のマシンに届き、第三者には届かない。
  Claude Code と Codex はこれを使う。
- **それ以外のリダイレクトは、オペレーターの信頼クライアント一覧にあるホストの `https` に限る**。0116 決定 7
  と同じく正規形で照合し、大文字、末尾のドット、既定ポートの明示、userinfo、フラグメント、バックスラッシュは
  拒否する。既定の一覧は `claude.ai`、`claude.com`、`chatgpt.com`。オペレーターはどれでも外せるし、足せる。
- **CIMD の `client_id` は同じ一覧にあるホストからしか取得しない**。他人が選んだアドレスへ認可サーバーが外向き
  リクエストを出すことはない（CIMD が招きがちな SSRF）。取得は短いタイムアウトとサイズ上限付きで、
  `Cache-Control` の期間を上下限の範囲でキャッシュし、`redirect_uris` を上の規則で検査する。
- **DCR** の登録も同じリダイレクト規則に従い、送信元ごとのレート制限と件数の上限を設け、グラントのないまま
  30 日たったものは削除する。削除されたクライアントは `token` で `invalid_client` を受け取る。claude.ai は
  これを再登録の合図として文書化している。
- 事前登録（オペレーターがクライアント ID を入力する方式）は作らない。ホスト型クライアント 2 つは自分で登録
  でき、CLI にはループバックがある。

### 4. 同意ページでテナントとスコープを決め、コードの送り先を見せる

`authorize` はまずクライアントとリダイレクトを検査し、どちらかが信頼できなければリダイレクトせずに 400 の
ページを返す（0116 決定 7）。そのあと、サインイン済みのメンバーに 1 枚のページで次を見せる：

- **リダイレクト先のホスト**を大きく。クライアントの自称の名前は自称だと分かるように示す。名前は CIMD でも
  DCR でも自由記述で、何も証明しない。
- **テナント**：有効な membership ごとに 1 つの選択肢。1 つしかなければ選択済み。トークンは PAT と同じく
  その membership に固定され（0006 決定 2）、クライアントがテナントを切り替えることはできない。
- **スコープ**：`read` と `write`。リクエストに合わせて選択済みにし、メンバーは狭められる。狭めた結果は
  トークン応答の `scope` で返す。`admin:dangerous` はロールにかかわらず **OAuth では決して許可しない**。
  ベンダーのクラウドに置かれるトークンに、フリート全体に効く段は渡さない。管理者の `write` トークンは、
  管理者の `write` PAT とまったく同じく管理者向け書き込みツールに届く。

`prompt=none` を受け付けるのは、同じメンバー・テナントで、同じかより広いスコープの有効なグラントをすでに
持つクライアントだけ。それ以外は `consent_required`。ページは 0116 のヘッダーと「キャンセル」
（`access_denied`）をそのまま使う：`frame-ancestors 'none'`、`form-action` なし（Chromium は POST 後の
リダイレクトにも適用するので、claude.ai や ChatGPT への戻りを止めてしまう）、`no-store`、
`Referrer-Policy: same-origin`（`no-referrer` だとフォームの POST が `Origin: null` になり、同一オリジン検査が
拒否する。0116 の #2243 の追記）。

`authorize` はメンバーを通常の経路で解決するので、テナントのネットワーク制限（0047）はブラウザの前にいる人に
かかる。`/mcp` と `/token` への呼び出しはベンダーのサーバーから来るため IP では制限しない。いまの PAT の
呼び出しと同じ（0047 決定 3）。

### 5. トークン：接続ごとのグラント、短命の不透明アクセストークン、ローテーションするリフレッシュトークン

- **グラント**の行はクライアント・identity・membership・スコープ・作成日時を記録する。メンバーが目にして
  失効させるのはこれ。アクセストークンとリフレッシュトークンはここにぶら下がる。
- **アクセストークン**：不透明、1 時間、ハッシュで保存、リソースに束縛。JWT にしない理由は 0116 と同じで、
  失効が次の呼び出しから効き、`/mcp` が鍵を必要としない。
- **リフレッシュトークン**：不透明、ハッシュ、**使うたびにローテーション**。使用済みのものが提示されたら
  グラントごと失効させる（公開クライアントに対する OAuth 2.1 §4.3.1）。30 日使われなかったグラントは失効し、
  グラントの寿命は最長 90 日。その後はメンバーがサインインし直す。
- 認可コードは 60 秒で、0116 と同じく 1 回の条件付き更新で消費する。
- グラントとトークンは **PAT の行にしない**。PAT はメンバーが名前を付けて持つ資格情報で、identity ごとに 20 件の
  上限がある（`maxActivePATs`）。OAuth のトークンは 1 時間ごとに入れ替わり、クライアントに属する。両者が
  合流するのはプリンシパル。`authMCP` はどちらの種類も同じ `mcpPrincipal`（identity・membership・スコープ。
  PAT ID の位置にはグラント ID）に解決するので、呼び出しごとのロール解決、テナントの固定、ツールの絞り込み、
  その下の監査行は変わらない。
- 監査：`mcp.oauth.grant`（作成・縮小・失効・再利用による失効）と `mcp.oauth.token`（発行ごと。0116 決定 9 と
  同じく応答を返す前に書く）。トークンの値は決してログにも監査にも書かない。

### 6. 有効化：既定は off。デプロイ、次にテナント

- `AF_MCP_ENABLED=true`、`AF_MCP_OAUTH=1`、`https` の `PUBLIC_BASE_URL` がすべてそろったときだけ提供する。
  そうでなければ `/mcp-oauth` とメタデータのパスはすべて 404 を返し、`/mcp` は素の 401 のままなので、
  クライアントはトークンの入力を求める動きに戻る。
- テナント管理者はテナント単位で無効にできる（ネットワーク制限の隣のテナント設定）。無効にするとテナントの
  グラントを失効させる。既存の PAT には影響しない。
- `AUTH=proxy` では、`/mcp-oauth/authorize` と `/consent` はゲートウェイを通し、決定 1 のそれ以外のパスは
  ゲートウェイを迂回させる必要がある。`/mcp` にもともと必要なことと同じ。オペレーター向けガイドに書く。
  `AUTH=dev` でも動き、固定ユーザーとして認可する。
- ホスト型クライアントには、インターネットから CP に届くことが必要。VPN の内側や狭い `AlbIngressCidr` の
  デプロイでは、構造上ループバックのクライアントだけになる。壊れているように見せず、ガイドにそう書く。

### 7. PAT は残す

CI などのヘッドレスな呼び出しと、OAuth を持たないクライアントのため。Console のトークンのページには、PAT の
隣に OAuth のグラントを「接続中のアプリ」として並べ、クライアント・リダイレクト先ホスト・テナント・スコープ・
最終使用日時と失効ボタンを出す。

## 却下したもの

- **`/dev-idp` から MCP トークンを出す。** 決定 1。
- **リダイレクトを問わない DCR の開放。** 決定 3。登録はもともと認証なしなので、縛れるのはリダイレクトしかない。
- **任意の CIMD URL の取得。** 攻撃者が選んだアドレスへ CP から外向きリクエストを出すことになる。
- **機密クライアント・`private_key_jwt`。** 必要とするクライアントがない。ChatGPT も claude.ai も `none` を
  受け付ける。`private_key_jwt` は必要とするクライアントが現れたら足せる。
- **JWT のアクセストークン。** 失効が期限切れまで効かない。
- **OAuth のトークンを PAT として保存する。** 決定 5。
- **OAuth での `admin:dangerous`。** 決定 4。
- **ChatGPT のワークスペースドメイン制限のための OIDC userinfo。** 接続には不要で、af のどの機能のためでも
  ないのに、メンバーのアドレスを他のベンダーに渡すことになる。

## 影響

- Control Plane：新しい `mcp_oauth*.go` 群（メタデータ、authorize/consent、token、register、CIMD の取得、
  リダイレクト規則）、2 種類目のトークンを解する `authMCP`、`/mcp` の 401 ヘッダー、認証除外パスの一覧、
  テナント設定、Console の「接続中のアプリ」一覧。
- ストア：グラント・トークン・コード・クライアントのテーブル。**sqlite と Postgres の両方**のマイグレーションで、
  前方互換。トークンはすべてハッシュなので封緘列は要らず、`rewrap-keys` には触れない。JWT を出さないので
  署名鍵も要らない。
- ガイド：member（claude.ai・ChatGPT・Claude Code の接続）、admin（テナントのスイッチ）、operate
  （`AF_MCP_OAUTH`、信頼クライアント一覧、`AUTH=proxy` でのイングレスのパス）。
- 0006 決定 2 の「ネイティブ OAuth2.1 対応は二次」は本書で置き換える。PAT はそこに書かれたとおり残る。

## 備考

- 2026-10-20 に読んだ資料：MCP 仕様 2026-07-28「Authorization」（modelcontextprotocol.io）、OpenAI
  「Authentication」（ChatGPT アプリと MCP サーバー向け、developers.openai.com/plugins/build/auth）、Anthropic
  「Authentication for connectors」（claude.com/docs/connectors/building/authentication）。
- 測っていないもの：3 つのホスト型フローのどれも、実際のデプロイでは試していない。インターネットから届き、
  `PUBLIC_BASE_URL` が `https` のデプロイが要る。
- 未確定：ChatGPT と claude.ai が 401 のチャレンジの `scope` をそのまま送るのか、`scopes_supported` のすべてを
  要求するのか。決定 4 はどちらにも対応する（メンバーが狭める）が、同意ページで最初に選択済みにする値は、
  実際の挙動に合わせる。
- 2026-10-21、決定 1〜5 とデプロイフラグの実装（#2433）。決定の変更はなく、本文が開けていた点を次のとおり定めた。
  信頼するクライアントのリストは `AF_MCP_OAUTH_TRUSTED_HOSTS`（ホスト名の完全一致、ループバックだけにするなら `none`）。
  `resource` は `authorize` と `authorization_code` のリクエストで必須、リフレッシュでは指定があれば検査する。
  アクセストークンは自身のスコープを持つので、リフレッシュでグラントより狭められる。使用済みリフレッシュトークンの
  2 回目の提示は、同時の重複であってもグラントを取り消す（1 件が成功し、残りが取り消す）。猶予時間はない。
  CIMD のホストは `token` でも再確認するが、文書は再取得しない。ここで作っていないもの（Consequences のとおり）:
  テナントのスイッチと接続済みアプリ一覧（#2434）、実クライアントでの確認（#2435）。
