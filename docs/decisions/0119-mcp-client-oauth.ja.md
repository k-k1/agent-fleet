# 0119. 登録した MCP サーバーの OAuth 対応：OAuth クライアントは af、メンバーは Console からサインインし、セッションはトークンを持つローカル中継を通してサーバーに届く

[English](0119-mcp-client-oauth.md) | 日本語

- 状態：**提案**（2026-10-22）。ドキュメントのみ。以下はまだ何も実装していない。コードの事実はこの日付の
  ツリーから、プロトコルの要件は MCP 2026-07-28 仕様から読んだ。実際の認可サーバーでは何も測っていない。
- 追跡：#2479
- 関連：[0031](0031-mcp-registry.ja.md)（レジストリ。その「残り」に OAuth の MCP は af から扱わないとあり、
  却下した選択肢に「af が MCP のプロキシになる」がある。決定 6 で見直す）/ [0118](0118-mcp-oauth.ja.md)
  （反対側：自分の `/mcp` のための認可サーバーとしての af）/ [0040](0040-project-mcp.ja.md)（プロジェクトの
  MCP。本書の範囲外）/ `docs/log/80` と `control-plane/oauth_jira.go`（本書が再利用する「CP がコールバック、
  トークンは Workspace」の分担）

## 背景

登録したリモート MCP サーバーが持てるのは固定のヘッダーだけ
（`workspace/agent/internal/secrets/secrets.go` の `secrets.MCPServer.Headers`）。接続テスト
（`workspace/agent/internal/mcpreg/probe.go` の `probeHTTP`）は 401 をただの失敗として報告する。ホスト型の
MCP サーバーは OAuth しか受け付けないものが増えていて、メンバーはそれらをまったく登録できない。

答えの形を決めたツリーの事実が 3 つある：

- **どのエージェントの種類も、MCP の設定を起動時に読む。** af はレジストリを 7 つのネイティブ設定ファイルに
  書き出し（`materialize*.go`、ADR 0031 決定 3）、動いている CLI はそれを読み直さない。測った例外は、
  `thread/start` ごとに読み直す codex の app-server だけ。
- **af はすでに接続（Connections）で OAuth を動かしていて**、分担が決まっている。公開コールバックと CSRF の
  state は CP、トークンはメンバーの Workspace のストア。CP のリフレッシュ橋渡しは、それらのアプリがテナントの
  クライアントシークレットを持つからだけに存在する。
- **Workspace からの外向き通信は、すでに Workspace 自身の egress を通る。** メンバーが登録した MCP サーバーに
  届くのはその経路で、CP からはまったく届かないこともある。

プロトコルがクライアントに求めること（MCP 2026-07-28「Authorization」「Authorization Server Discovery」
「Client Registration」）：

- Protected Resource Metadata を `WWW-Authenticate: resource_metadata` から、なければ
  `/.well-known/oauth-protected-resource/<path>`、次にルートから見つける（RFC 9728）。チャレンジの `scope` は
  そのリクエストについて正とする。
- AS メタデータを決まった順（RFC 8414 のパス挿入、OIDC のパス挿入、OIDC の末尾付加）で探し、URL を組み立てた
  発行者と `issuer` が違う文書は**使わない**。
- 登録は 事前登録 → CIMD（`client_id_metadata_document_supported` のとき）→ DCR（非推奨、`application_type`
  必須）→ 利用者に尋ねる、の順。資格情報は AS の `issuer` ごとに持ち、PRM が別の AS を指したら登録し直す。
- PKCE S256、認可リクエストとトークンリクエストの `resource`（RFC 8707）、仕様の表どおりの `iss` 検証
  （RFC 9207）、リフレッシュトークンの秘匿、403 `insufficient_scope` での step-up。

## 決定

### 1. OAuth クライアントは af。各メンバーは Console からサインインする

リモートの定義に、既存のヘッダーと並べて `auth: oauth` を足す。メンバーは **設定 › MCP サーバー** から、
自分のブラウザで 1 回サインインする。以後、すべてのセッションのすべてのエージェントの種類がそのグラントを
使う。各 CLI 自身の OAuth フローは使わない（「却下したもの」参照）。

範囲：ユーザースコープとテナント配布のリモート定義。組み込み、stdio、プロジェクトの MCP（0040）は範囲外。

### 2. 発見と検証は Agent で、仕様の文言どおりに行う

Agent は、接続テストがすでにしているのと同じく Workspace の egress を通して発見を行い、推測せずに拒否する：

- メタデータの `issuer` が、well-known URL を組み立てた発行者と違う AS。
- `code_challenge_methods_supported` に `S256` がない AS。
- `resource` が、登録したサーバー URL の正規形でない PRM。

`authorization_servers` に複数あるときは、メタデータの検証が通った最初のものを使い、どれを使ったかを
Console に出す。

### 3. クライアント登録は仕様の順で。af 自身の CIMD 文書を持つ

1. メンバーがそのサーバー用に入力した**事前登録**のクライアント ID（AS が求めるならシークレットも）。
   AS の発行者ごとに持つ。
2. AS が対応を広告していれば **CIMD**。af はデプロイに 1 つ、`{PUBLIC_BASE_URL}/oauth/mcp-client.json` に文書を
   置く：`client_id` はその URL、`client_name` はデプロイのブランドから、`redirect_uris` は
   `[{PUBLIC_BASE_URL}/oauth2/mcp/callback]`、`token_endpoint_auth_method: none`、
   `grant_types: [authorization_code, refresh_token]`。AS から取得できる `https` の `PUBLIC_BASE_URL` が要る。
   取得できるかどうかは、AS が試すまで分からない。
3. AS に `registration_endpoint` があれば **DCR**：`application_type: "web"`、同じリダイレクト、`none`。
   返ってきた資格情報は Workspace ごと・AS の発行者ごとに保存して使い回す。
4. それ以外は、Console がメンバーにクライアント ID を尋ねる。

CIMD での認可が AS で失敗し、その AS が DCR も提供していれば、Console は DCR でやり直すことを提案する。
どの経路を使ったかは Console が常に表示する。

### 4. 流れ：Agent が準備し、コールバックは CP が持ち、交換は Agent が行う

1. Console がサインインを求める。リクエストは（Agent への素通しのプロキシではなく）**CP のハンドラー**に
   届き、CP が `state` を作って、メンバー・membership・Workspace・10 分の期限と一緒に記録し、Agent を呼ぶ。
2. Agent は PKCE の verifier を作り、スコープを選び（チャレンジの `scope`、なければ PRM の
   `scopes_supported`。AS が載せていれば `offline_access` も）、`response_type=code`・`client_id`・
   `redirect_uri`・`code_challenge`（S256）・`state`・`resource`・`scope` を持つ認可 URL を返す。
   Console がそれを開く。
3. AS は `{PUBLIC_BASE_URL}/oauth2/mcp/callback`（すでに認証除外の `/oauth2/` 配下）にリダイレクトする。
   CP は `state` を 1 回だけ消費し、ブラウザの Console セッションが開始したメンバーと同じであることを求め、
   `code`・`iss`・`state` をその Workspace の Agent に渡す。
4. Agent は仕様の表どおりに `iss` を検証し、verifier と `resource` を付けてコードを交換し、トークンを保存する。
   CP はブラウザを Console に戻し、結果だけを伝える。

verifier、コードの交換、トークンは CP に一切触れない。トークンエンドポイントにはサーバー自身と同じく
Workspace の egress から届くので、CP から届かなくても困らない。これは接続（Connections）の分担から
リフレッシュの橋渡しを除いたもの。橋渡しが要るのはテナントのクライアントシークレットがあるときだけ
（それがある場合は決定 9）。

### 5. トークンは Workspace のストアに。リフレッシュは 1 本化し、ローテーションでも失わない

- アクセストークン、リフレッシュトークン、DCR の資格情報は `secrets.enc` に、サーバー ID と AS の発行者を
  キーに保存する。再作成（Recreate）とホームの初期化でも、接続と同じく残る。
- リフレッシュは Agent で行い、すべてのセッションを通じてグラントごとに同時に 1 本まで。新しいリフレッシュ
  トークンを保存してから新しいアクセストークンを使うので、途中で落ちてもローテーションを失わない。
- リフレッシュで `invalid_grant` が返ったら、そのグラントを「もう一度サインインが必要」にする。繰り返し
  試すことはしない。
- 切断では、AS が失効（RFC 7009）を提供していれば失効させ、どちらにしてもローカルから削除する。

### 6. セッションはローカル中継を通してサーバーに届く

Agent は `127.0.0.1` でだけ待ち受け、OAuth のサーバーごとにパスを 1 つ出す。そのサーバーについては、
すべての種類の設定に、本物の URL とトークンの代わりに中継の URL と**中継キー**のヘッダーを書く。中継は：

- 中継キー（Workspace ごとのランダム値。`0600` の設定ファイルにだけ書く）とループバックの `Host`
  （DNS リバインディング対策）が揃ったリクエストだけを受け付ける。
- CLI が送ってきた `Authorization` は捨て、新しいアクセストークンを付ける。
- 登録された URL にそのまま転送する。POST・GET（SSE）・DELETE に対応し、ストリームの応答は届いた分から
  流し、MCP のヘッダー（`MCP-Protocol-Version`、`Mcp-Method`、`Mcp-Name`、旧世代の `Mcp-Session-Id`）は
  そのまま通す。上流のリダイレクトには従わない。
- 上流が 401 を返したら、1 回だけリフレッシュしてやり直す（サーバーは何も処理していない）。
- **上流の `WWW-Authenticate` を CLI には決して渡さない。** だからどの CLI もサーバーに対して自前の OAuth
  フローを始めない。リフレッシュできないグラントは、もう一度サインインする場所として Console を名指しする
  エラーになる。
- 403 `insufficient_scope` はそのまま通して記録する。Console は、チャレンジが挙げたスコープでもう一度
  サインインすることを提案できる（step-up にはメンバーのブラウザが要る）。

固定ヘッダーのサーバーは中継を使わず、何も変わらない。アシスタントチャットは、起動ごとの設定で中継の URL を
使う。

ADR 0031 は「af が MCP のプロキシになり全通信を中継する」案を、af が認証・ストリーミング・OAuth を背負うから
という理由で却下した。この中継はもっと狭い：OAuth のサーバーだけ、MCP を解釈しないトランスポート層の素通し、
Workspace の中だけ、外向きの通信は接続テストと同じ egress を通る。代わりの案（決定 1 で却下したもの）は、
何日も続くセッションの 1 時間目で死ぬトークンになる。副作用は望ましいもの：セッションはグラントを使えるが
トークンそのものは読めない。今の固定ヘッダーは 7 つの設定ファイルに平文で置かれている。

### 7. 設定への書き出し

OAuth の定義は、メンバーがその定義の有効なグラントを持っている間だけ各種類の設定に書き出す。値の入っていない
`user_secret` ヘッダーと同じ決まり（ADR 0031 決定 4）。ドリフトテストには種類ごとに
`http://127.0.0.1:<port>/…` の URL と独自ヘッダーのケースを 1 つ足す。中継は、すべての種類が平文 http の
ループバック URL を受け付けることに依存しているから。

### 8. Console

- サーバーのフォームで認証を **なし / ヘッダー / OAuth** から選ぶ。
- 接続テストは、OAuth のメタデータ付きの 401 を受けたら、見つかったもの（AS の発行者、使える登録経路、S256、
  `iss` 対応、スコープ）を報告し、**サインイン**を出す。
- OAuth の行には「未サインイン / 接続済み（期限、スコープ）/ もう一度サインインが必要 / 権限が足りない」を
  出し、**切断**ボタンを置く。
- egress の注記は、サーバーのホストだけでなく、Agent が呼ぶ AS のホスト（PRM、メタデータ、トークン、登録）も
  確かめる。

### 9. テナント配布

配布する定義には `auth: oauth` を書ける。資格情報は持たず、各メンバーが自分でサインインする。メンバーが
サインインするまで、そのメンバーには書き出さない。テナントが事前登録する**機密**クライアントは後の段階：
そのシークレットは CP に置くことになり、CP がコードの交換とリフレッシュを行う必要がある（既存の
`/internal/git-oauth/*/refresh` の橋渡しの形）。その場合に限り、決定 4 の「CP はトークンを見ない」を覆す。
必要とするテナントが現れたら作る。

### 10. 監査

CP は、サインインの開始・完了・失敗・切断について `mcp.client_oauth` の行を書き、サーバーと AS の発行者を
記す。トークン、コード、verifier はログにも監査にも書かない。

## 却下したもの

- **各 CLI 自身の OAuth。** コールバックはコンテナの中のループバックで、メンバーのブラウザから届かない。7 つの
  種類がトークンを 7 か所に持ち、Console から見えない。テナント配布だと、メンバー 1 人に 7 回サインインさせる
  ことになる。
- **アクセストークンを固定ヘッダーにして、リフレッシュのたびに書き出し直す。** 設定は起動時に読まれるので、
  長いセッションの途中でトークンが死ぬ。マネージドのセッションは再起動しない。しかも、すべての種類の設定に
  ベアラートークンが平文で残る。
- **トークンとリフレッシュを CP に置く。** CP からサーバーの AS に届く経路がないことがあり、すべてのメンバーの
  グラントが 1 か所に集まる。Workspace のストアなら、それぞれを持ち主のところに置いておける。
- **中継を CP に置く。** MCP の全バイトが CP を通り、しかもメンバーの登録を照らし合わせる egress は CP のでは
  なく Workspace のもの。
- **メッセージを解釈する完全な MCP プロキシ。** ここで MCP を理解する必要はなく、解釈すると中継がプロトコルの
  改訂（0032）に縛られる。

## 影響

- Agent：発見と登録（`mcpreg`）、フローとトークンのストア（`secrets`）、中継の待ち受け、OAuth の行の書き出し、
  接続テストの報告。
- CP：サインインのハンドラーと `state` のテーブル、`/oauth2/mcp/callback`、CIMD 文書、監査、テナントの
  `auth: oauth` 欄（CP のテーブルと Agent の `acceptTenant` の再検証。ADR 0031 は両方での検証を求めている）。
- Console：フォーム、接続テストの報告、行の状態、サインインと切断。
- ガイド：member（OAuth の MCP サーバーの登録）、admin（その配布）、operate（CIMD には AS から届く `https` の
  `PUBLIC_BASE_URL` が要る）。
- 段階は #2479 で追う：接続テストで「OAuth が必要」を報告 → サインインと保存 → 中継と書き出し → テナント配布 →
  必要ならテナントの機密クライアント → 実機確認（最初は別のデプロイの `/mcp`、ADR 0118）。

## 備考

- 2026-10-22 に読んだ資料：MCP 仕様 2026-07-28「Authorization」「Authorization Server Discovery」
  「Client Registration」（modelcontextprotocol.io）。
- 測っていないもの：どのホスト型 MCP サーバーの AS が CIMD に対応しているか。すべてのエージェントの種類が、
  ヘッダー付きの平文 http のループバック URL を受け付けるか（決定 7 で足すテストがそれを示す）。
