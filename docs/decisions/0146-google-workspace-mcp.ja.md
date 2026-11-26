# 0146. Google Workspace の MCP サーバー：テナントが配布する OAuth MCP として扱い、サインインは 1 回、確認には Console で人が答える

[English](0146-google-workspace-mcp.md) | 日本語

- Status: **提案**（2026-11-25）。まだ何も作っていない。以下の事実は 2026-11-25 に Google の公開メタデータと
  認証なしの `tools/list` に対して測ったもの。実際の Workspace テナントでサインインはしていない。
- Tracking: #5179
- Follow-ups: #5173, #5175, #5174, #5177, #5176, #5178
- Related: [0119](0119-mcp-client-oauth.ja.md)（この記録の土台となる、登録 MCP サーバーの OAuth と中継。
  その決定 6「中継は MCP を解釈しない」を狭める）/
  [0031](0031-mcp-registry.ja.md)（レジストリとテナント配布）/
  [0032](0032-mcp-2026-07-28.ja.md)（Google のサーバーが話すプロトコル版）/
  [0015](0015-agent-managed-driver.ja.md)（Managed ドライバーの対話チャネル）/
  [0107](0107-google-cloud-profiles.ja.md)（決定 8 が借りるラッパーの形）/
  [0122](0122-egress-gateway.ja.md)（egress の許可リスト）

## 背景

2026-11-25、Google は Workspace 専用のリモート MCP サーバーと Workspace CLI `gwscli` を一般提供した。MCP
サーバーは Gmail、ドライブ、ドキュメント、スプレッドシート、スライド、カレンダー、Chat、People、Tasks、
Classroom、ユニバーサル検索に用意されている。

- **2027-02-01** から、AI エージェントは Workspace の REST API ではなくこれらを使うよう技術的に強制される。
  通常のアプリは REST API を使い続けられる。
- 利用枠の課金は **2027-04-01** に始まり、OAuth クライアントを持つ Cloud プロジェクトに請求される。
- Workspace AI Tools Addendum は、生データの恒久保存、派生データの長期保持、モデル学習、一括取得を禁じている。

Google がクライアントに求めること：

- **agent フラグ付きの OAuth クライアント。** 「Use this client for an AI-powered agent」は通常のクライアント
  ID に付くチェックボックスで、Workspace のサーバーはそれ以外のクライアントで認可された呼び出しを拒否する。
  ホスト型の MCP クライアントには、設定ガイドは **Web アプリケーション** を指定している。
- **form elicitation。**
  - 対象は、メール送信、共有設定の変更、ゴミ箱への移動、一括編集の前（と管理ポリシーが足すもの）。
    `tools/call` が `resultType: "input_required"` と form の `elicitation/create` を返す。
  - クライアントは、不透明な `requestState` と `inputResponses` を付けて同じ呼び出しを再送する。
  - 選択肢は `ALLOW_ONCE` / `DENY_ONCE`、ポリシーが許せば `ALLOW_TOOL_ALWAYS` も出る。
  - `elicitation.form` を宣言しないクライアントには JSON-RPC の `-32021` が返る。
  - 各サーバーは、elicitation を持たないホスト向けに、常に承認が要るツールを省いた `/mcp/v1self` も出している。
- **プロトコル。** ステートレスな Streamable HTTP、POST のみ、MCP 2026-07-28。

af にすでにあるもの（ADR 0119）：

- `auth: oauth` の定義とテナント配布
- テナントが事前登録する confidential クライアント（secret は CP に留まる）
- CP のコールバック `{PUBLIC_BASE_URL}/oauth2/mcp/callback`
- トークンを差し込んでバイトを素通しする Workspace 内の中継

測った不足（2026-11-25）：

1. `https://gmailmcp.googleapis.com/.well-known/oauth-protected-resource/mcp/v1` の PRM は
   `https://accounts.google.com/` を指している。一方、OIDC メタデータは `issuer: https://accounts.google.com`。
   `probe_oauth.go` はこのバイト列の不一致を拒否する。
2. 認証なしの `tools/list` は 200 でツール一覧を全部返す。スコープを示す 401 チャレンジが来ないので、
   `selectScope` は PRM の `scopes_supported` を全部要求する。`https://mail.google.com/`、`gmail.send`、
   `gmail.settings.*` なども入る。
3. Google のメタデータに `offline_access` はない。リフレッシュトークンには `access_type=offline` が要り、
   以前に同意した利用者には `prompt=consent` も要る。なければ grant は 1 時間で切れる。
4. grant はサーバーごとに 1 つ。サービスが 10 あればサインインも 10 回。
5. elicitation を扱うコードがどこにもない。
   - 中継はトランスポートの素通しだけ。
   - codex の Managed ドライバーは知らないサーバーリクエストに答えず放置する。
   - ACP ドライバーは `-32601` を返す。
   - `mcpc` は capability を空で宣言する。
   - どの CLI が自前で `input_required` に答えるかは測っていない。
6. ホストは `gmailmcp.googleapis.com`、`drivemcp.googleapis.com`… で、People は `people.googleapis.com`。
   ドット始まりのサフィックス規則では覆えない。`.googleapis.com` は意図して許可リストから外してある
   （`egress_policy.go`）。

## 決定

### 1. Workspace MCP はテナントが配布する OAuth MCP サーバーとし、クライアントはテナント自身のもの

af は Google の OAuth クライアントを同梱しない。テナント管理者は、自分の Cloud プロジェクトに agent フラグ付きの
Web アプリケーションクライアントを作る。リダイレクトは `{PUBLIC_BASE_URL}/oauth2/mcp/callback` にし、
ADR 0119 のテナント事前登録 confidential クライアントとして入力する。同意画面、Google のアプリ審査、
Addendum、利用枠の請求は、どれもそのプロジェクトに属する。Google がそこに置いているからである。メンバーが
自分のクライアントで自分用にサーバーを登録する道も残す（0119 決定 3.1）。

### 2. OAuth の互換（#5173）

- **issuer の同一視。** パスがちょうど `/` の issuer は、パスが空の同じ issuer と等しいとみなす
  （RFC 3986 §6.2.3、スキームに基づく正規化）。ほかは正規化しない。認可応答の `iss` は、AS メタデータが
  名乗る issuer と比べる。この許容でだけ一致した AS は、PRM の先頭の有効な AS を選ぶとき（0119）に完全一致の
  AS より後に回す。既に使えていた PRM は同じ AS につなぎ続ける。
- **定義側のスコープ。** 定義はスコープの一覧を明示的に持てる。持つときは、チャレンジと `scopes_supported`
  の代わりにそれを使う。Workspace の定義は必ず持つ。
- **issuer ごとの認可パラメータ。** `https://accounts.google.com` 向けの認可要求には `access_type=offline`
  と `prompt=consent` を足す。同意は保存済みの grant にリフレッシュトークンが無いときに限らず、毎回のサインインで
  求める。Google は同意を経た認可にしかリフレッシュトークンを返さず、サインイン（ステップアップや再接続）は保存済みの
  grant を置き換えるので、同意なしのサインインは 1 時間で切れる grant を残すため。これは issuer をキーにした
  表であって、管理者が自由に書ける欄ではない。
- **probe。** 200 の probe でも PRM が見つかれば OAuth の所見を報告する。ツール一覧が公開されているサーバーにも、
  フォームが OAuth を出せるようにするため。

### 3. テナントの MCP 設定に Workspace のプリセット（#5175）

1 つのフォームで、クライアント（id、secret）、提供するサービス、スコープの束を受け取る。そこからサービスごとに
1 つずつ配布定義を書く。

- **読み取り専用：** `*.readonly`、`drive.readonly`、`calendar.events.readonly`…
- **標準：** 読み取り専用に `gmail.compose`、`drive.file`、`documents`、`spreadsheets`、`presentations`、
  `calendar.events`、`chat.messages.create` を足す。

`https://mail.google.com/`、`gmail.send`、`drive`（全権）はどの束にも入れない。プリセットは普通の定義を書く
だけで、下流はプリセットの存在を知らない。

### 4. grant はクライアントと issuer ごとに 1 つ

同じ issuer で同じ事前登録クライアントを使う定義は、grant を共有する。どれか 1 つでサインインするとスコープの
和集合を要求し、その全部がつながる。トークンを grant が名指す URL にだけ送るという中継の検査（0119 P3 の備考）は、
「grant の集合に含まれる URL」に変わる。集合に定義が増えたとき、または 1 つにスコープが増えたときは、黙って
2 回目のサインインを求めるのではなく、「追加の権限が必要」（0119 のステップアップ状態）を出す。

### 5. 決定 6 が出るまでは `/mcp/v1self`

中継が elicitation に答えられない間、プリセットは `/mcp/v1self` の URL を書く。常に承認が要るツールは、
`-32021` で失敗する代わりに、最初から現れなくなる。決定 6 が出たら、プリセットは URL を `/mcp/v1` に書き換える。
これは URL の変更なので 0119 ではサインインが終わるが、ここでは決定 4 の集合の変更として grant を引き継ぐ。

### 6. elicitation には中継が答え、「はい」と言えるのは人だけ（#5174）

`elicitation: relay` の印が付いた定義（プリセットが付ける）では、中継が JSON-RPC を解釈する。

- 各リクエストの `_meta["io.modelcontextprotocol/clientCapabilities"]` に `elicitation.form` を足す。
- `tools/call` の結果が `input_required` なら、中継は CLI のリクエストを開いたまま保留する。そして Agent に
  承認を登録する。中身は、サーバー、ツール、引数（サーバーが実行するとおりのもの）、フォームの選択肢。
  Console はそれを、Managed でも Terminal（CLI）でも、そのセッションのペインに承認カードとして出し、通知もする。
- メンバーの選択は、変えていない `requestState` と一緒に `inputResponses` として送り返す。最終的な結果を CLI
  に渡す。
- 拒否、カードを閉じた場合、時間切れは `decline` として送る。サーバーは「Action canceled」を返し、エージェント
  はそれを見る。
- `ALLOW_TOOL_ALWAYS` は、サーバーが出したときにだけ出す。
- 中継が自分で答えることはない。CLI が何を宣言していても、CLI も答えない。CLI が見る結果は常に `complete` である。
  だから bypass モードのエージェントが自分の操作を自分で確認することはできない。それが Google の確認の目的である。
- 各回答は監査行 `mcp.elicitation`（サーバー、ツール、選択、誰が）になる。引数と内容は載せない。

これで、0119 が却下した「メッセージを解釈する完全な MCP プロキシ」の範囲を、印の付いた定義だけ狭める。固定
ヘッダーの定義と、ほかの OAuth 定義は素通しのまま。時間切れの長さは CLI のツール呼び出しタイムアウトに収まる
必要がある。これが #5174 の最初の計測である。

### 7. ガイドと方針（#5177）

- **管理者ガイド：** クライアントの作り方、`*mcp.googleapis.com` のサービスの有効化、egress に足す正確なホスト、
  2027-04-01 から誰が払うか。
- **メンバーガイド：** 1 回のサインインと、承認カードの意味。
- **告知：** 2027-02-01 から、エージェントとして Workspace の REST API を呼ぶものは動かなくなる。REST で作られた
  コミュニティの MCP サーバーや、`af-gcloud-exec` 越しの REST 呼び出しも含む。
- **Addendum と保存データ：** セッションの記録とエージェントのメモリーには、メールやファイルの中身が残り得る。
  それらに af が保存期間の設定を持つべきかは、#5177 が持つ問いである。

### 8. Workspace CLI は後で（#5176）

`gwscli` のサインインはループバックへのリダイレクトで、Workspace の中ではメンバーのブラウザーから届かない。
確認は stdin を待ち、トークンのフラグはプロセス一覧に見える。MCP が使われ、CLI を必要とする人が出たときに、
オンデマンドのインストールと `af-gcloud-exec` の形のラッパーを用意する。

## 却下したもの

- **各 CLI 自身の elicitation。** 対応は測っておらず、種類ごとに違う。bypass モードでは問われるのはエージェント
  であり、エージェントが自分で出せる確認は、Google が求める人による確認ではない。
- **全配備で共有する af 所有の Google クライアント。** 全顧客のデータについて、審査も利用枠も Addendum の当事者も
  1 つになる。そして af にはそれを持つ Cloud プロジェクトがない。
- **ずっと `/mcp/v1self` だけ。** 見直した下書きを送るのは中心的な用途で、v1self はそれを省く。
- **0119 のとおり `scopes_supported` を要求する。** スレッドを 1 つ読むために、全メンバーにメールボックスの全権を
  求めることになる。
- **af から Google Cloud の Agent Gateway や Model Armor を前に置く。** それは顧客の Cloud の統制である。ホストは
  af の egress ゲートウェイがすでに覆い、中継が af のリクエスト経路である。

## 影響

- **Agent（`mcpreg`）：** issuer の同一視、定義側のスコープ、issuer ごとのパラメータ表、probe の報告、grant の集合、
  承認付きで解釈する中継の経路。
- **CP：** 定義の項目（スコープ、`elicitation`）、プリセットの API、`mcp.elicitation` の監査。
- **Console：** プリセットのフォーム、中継の承認につながる承認カード、grant の集合の行の状態。
- **`mcpc`**（lcpp、muse）は中継を通るので、自前の elicitation のコードは要らない。
- **実機確認：** #5178。

## 備考

- **2026-11-25 に読んだ資料：**
  - Google Cloud Blog「Deploying agents safely in your enterprise」
  - developers.google.com/workspace/guides/configure-mcp-servers、…/mcp-elicitation、…/workspace-cli
  - support.google.com/cloud/answer/15549257（agent 用クライアント）
- **2026-11-25 に測ったこと：**
  - Gmail とドライブの PRM はパス挿入の well-known URL にある。ルートの形は 404。
  - `accounts.google.com` の `oauth-authorization-server` 文書は 404。OIDC 文書は 200 で、`S256`、
    `authorization_response_iss_parameter_supported: true` を持ち、`registration_endpoint` と CIMD はない。
  - `Mcp-Method` 付きの認証なし `tools/list` は 200。`Mcp-Method` がないと `-32020`「Header mismatch」。
- **2026-11-26 に測ったこと（#5174）：** ループバックの Streamable HTTP サーバーに、各 CLI を非対話で向けた。
  - claude 2.1.332 と opencode 2.4.1 は 2026-07-28 で話し、`elicitation: {form, url}` を宣言する。どちらも
    `input_required` に自分で、すぐに `action: "cancel"` を返した。人には尋ねなかった。codex 0.172.0 は
    2025-06-18 だけで話し、呼び出しは失敗した（「Unexpected response type」）。
  - HTTP でのツール呼び出しの上限：claude は `notifications/progress` が届かないと 60 秒で打ち切る（進捗で
    延びる。進捗付きなら 660 秒を通した）。codex は進捗の有無にかかわらず 300 秒。opencode は進捗なしでも 330 秒を
    通した。そのため中継は保留中に進捗を流し、10 分で辞退する。
- **保留中の呼び出しへの回答（#5174 part 2）：** Console は `GET /api/mcp-elicitations` で一覧を読み、
  `POST /api/mcp-elicitations/answer` で答える（呼び出しの id は本文に入れる）。回答にはメンバー由来の印
  （ADR 0113）が要るので、セッションが自分の `AGENT_TOKEN` で自分の呼び出しを承認することはできない。署名する中継の
  パスは固定文字列でなければならないため、id をパスに入れていない。監査行の対象はサーバー id とツール、詳細は選択と
  セッション。保留中の呼び出しは、CLI のソケットを持つプロセスの環境変数 `AF_SESSION_NAME` が名指すセッションに
  帰属させる。この名前はカードをどこに出すかを決めるだけで、どのセッションでも設定でき、回答の可否は決めない。
  共有の codex・opencode デーモンからの呼び出しは帰属しない。
- **カード（#5174 part 3）：** 保留した呼び出しは outbox の通知 `mcp-elicitation` を出す。ペイロードは呼び出しの
  id だけ（outbox はどのエージェントも書ける）。宛先は帰属したセッション、無ければワークスペース。Console は、
  同じブラウザーウィンドウでそのセッションのペインが画面にある間はコンポーザーの上にカードを、無ければ消えない
  トーストを出す。帰属しない呼び出しは常にトーストで、トーストを閉じると辞退になる。カードはサーバーのラベル
  （どのセッションでも変えられる）の横に接続先のオリジンを出す。一覧は引数を元の文字列のまま送り、カードはそれを
  解析せずに整形して表示する。同じ名前のメンバーが二度ある場合や長い数値も、サーバーが読むとおりに見える。引数が
  UTF-8 でない呼び出しは一覧で運べないので保留せず、エラーで終える。方向制御文字などの見えない文字は、引数では
  エスケープで、それ以外では印で示す。ラベルが同じに見える選択肢は値も添え、それでも同じに見えれば番号を振る
  （#5282）。残り時間は Agent からミリ秒で受け取り、ブラウザーが読み取りを送った時点から数えるので、ブラウザーの時計が
  ずれていてもカードは消えない。
- **確かめていないこと：** Google が RFC 8707 の `resource` を受け付けるか無視するか。設定ガイドにある
  「Developer Preview Program が必要」という一文（GA より前の記述かもしれない）。
