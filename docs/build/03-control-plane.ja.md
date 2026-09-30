---
audience: "Control Plane を変える人"
source_of_truth: "コード（本書は地図と設計意図）"
updated: "2026-09"
---

# 03. Control Plane

[English](03-control-plane.md) | 日本語

CP は Workspace の外側で動く唯一の常駐バックエンド（Go 単一バイナリ）。ブラウザは常に CP とだけ話し、
**CP は tmux にも working copy にも直接触れない**。Workspace の中のことは必ず Agent 経由
（[01 §1.3](01-architecture.ja.md)）。内部 git プロバイダの bare リポジトリは CP 自身が持つ。本書は
「CP に何が住んでいて、どう繋がるか」。ワイヤ契約は [05](05-api.ja.md)、セキュリティ設計は [07](07-security.ja.md)。

## 3.1 責務地図

- **Console の配信** — `CONSOLE_DIR` のビルド成果物を `/`（catch-all）で配る。入口のファイル
  （`index.html`・`version.json`・`sw.js`・manifest）は `no-store` なので、デプロイは次の読み込みで
  反映される。`/assets/` 配下のコンテンツハッシュ付きファイルは `public, max-age=31536000, immutable`
  （`registerStatic`）。配備にブランド色があれば、favicon・PWA アイコン・manifest を配信時に塗り替える
  （`brand.go`）。
- **authGate（L1 認証）** — `AUTH=oauth` では CP 自身がエッジ: 全リクエストで署名 cookie を検証し、
  許可リストも毎回確かめ直す（cookie の期限前でも退職者を締め出すため）。**受信した identity ヘッダは
  削除し、検証済みの値を入れ直す**。fail-closed。`dev` と `proxy` にゲートは無い（[07 §7.3](07-security.ja.md)）。
- **identity / tenant 解決** — 検証済み email → identity。`X-AF-Tenant`（ヘッダを付けられないブラウザ
  経路では query の `tenant`）を membership と突き合わせる。未知 identity のプロビジョニング
  （`AF_PROVISION`、`auto` か `invite`）と配備管理者ロール（`SUPER_ADMIN_EMAILS`。起動時に、載っていない
  人を降格もする）もここ（§3.2）。
- **Workspace ライフサイクル** — membership 1 件 = Workspace 1 つ: 払い出し・start・stop・recreate・
  clean-home と、状態の DB 同期。実行基盤は Runtime アダプタ越し（§3.3）。
- **Agent 中継** — REST / SSE / terminal WS / browser REST+WS / preview の 5 経路
  （[05 §5.3](05-api.ja.md)）。**REST 中継は許可リスト**: 中継する経路はどれも CP が登録したルート
  （`routes.go` と隣の `register*Routes` 関数群）で、CP にルートの無い Agent エンドポイントには
  ブラウザから届かない。preview は `AF_PREVIEW_DOMAIN` があれば Workspace ごとのサブドメインでも答える
  （[decisions/0062](../decisions/0062-preview-subdomain.ja.md)）。
- **Console へのプッシュ経路** — `GET /api/events` は CP 自身の SSE。workspace・sessions・stats・
  notifications・engines のポーリングを 1 本の接続に畳み、JSON が変わったストリームだけを送る。
  idle-stop の活性には数えない。
- **Workspace → CP のブリッジ** — `/internal/` 配下のエンドポイントで、Agent は CP が起動時に注入した
  membership ごとのトークンで呼ぶ（§3.4）: memo・スケジュール・テナント MCP レジストリ・ドキュメント・
  エンジントークン・git OAuth の更新・AWS プロファイル。
- **MetadataStore** — SQLite が既定（`AF_DB`）、`AF_DATABASE_URL` か `AF_DB_HOST` があれば Postgres
  （[06](06-data.ja.md)）。
- **監査** — 選ばれた変更系の中継・admin API・MCP write・システムジョブが記録する。書き込み点は
  [05 §5.5](05-api.ja.md)、設計は [07 §7.7](07-security.ja.md)。
- **MCP サーバ** — member / admin ツールを外部クライアントに公開（§3.5）。
- **内蔵 git プロバイダ** — bare リポジトリ + smart HTTP + LFS を CP 自身がホストする（**Agent 非経由**・
  [91](91-internal-git.ja.md)）。
- **egress 統制** — forward proxy への policy 配布・観測イベントの集約・admin と member の API（§3.8）。
- **memo キュー** — membership 単位のメモと一括送信。メモの本文とカテゴリの CRUD は **CP 完結**なので Workspace
  停止中も使える。flush と画像の添付は Agent が要る（§3.6）。
- **定時実行（scheduler）** — スケジュール定義を CP の DB に持ち、goroutine が発火させる（tz は埋め込み
  IANA DB で DST 込みで解決）。作成は `/internal/schedules`（`AF_SCHEDULE_TOKEN`）経由で、オペレーターの
  会話が自然文を spec に訳して使う。Console の `/api/schedules` は一覧・編集・一時停止・再開・即時実行・
  削除はできるが作成はできない（[decisions/0021](../decisions/0021-scheduled-execution.ja.md)）。
- **通知** — Agent の outbox を取得時に CP ストアへ drain し、一覧と既読状態を出す
  （`/api/notifications`、保持 7 日）。
- **テナント MCP レジストリ** — tenant_admin が全メンバーへ配る MCP サーバ定義
  （`/api/admin/mcp-servers`。Agent は `/internal/mcp-servers` を poll する）。メンバー個人の登録は
  Agent 側で合成され、CP は中継するだけ（[decisions/0031](../decisions/0031-mcp-registry.ja.md)）。
- **自前エンジン** — エンジン表とモデルカタログ、Workspace がエンジンに届くためのゲートウェイ、
  エンジンの起動と停止（§3.9）。
- **読み上げ** — CP 自身が VOICEVOX か Polly を呼ぶ（`/api/tts/*`）。AWS では VOICEVOX はオンデマンドの
  ECS サービスで、エンジンと同じコントローラが動かす（[decisions/0070](../decisions/0070-tts-ondemand-engine.ja.md)）。
- **コストと使用量** — 全ターゲットでの占有秒と、請求書がある配備ではメンバー別に按分した AWS の請求額
  （§3.7、[decisions/0048](../decisions/0048-member-cloud-cost.ja.md)）。
- **メンバーをまたぐ機能** — CP が規則を持ち、中身は持ち主の Agent に聞く: セッション共有（共有規則は
  毎リクエスト DB で評価する）・引き継ぎの申し出（[decisions/0057](../decisions/0057-member-handoff.ja.md)）・
  作業項目の受信箱（CP は保存クエリと秘密でないメタデータのキャッシュを持ち、取得は Agent が自分の
  プロバイダトークンで行う。[decisions/0061](../decisions/0061-work-item-inbox.ja.md)）。
- **バックグラウンドジョブ** — reaper・usage サンプラー・クラウドコストのポーラー・git GC・監査
  sweep・ecs-ec2 のプールのジョブ（§3.7）。

いくつかの機能は **Agent 側のもの**で、CP は中継するだけ: 掃除（cleanup）・エージェントメモリ管理
（[decisions/0022](../decisions/0022-agent-memory-management.ja.md)）・セッションのごみ箱
（[decisions/0101](../decisions/0101-session-delete-via-trash.ja.md)）・画像生成のジョブとスタジオ
（[decisions/0081](../decisions/0081-image-generation-pane.ja.md)・
[0100](../decisions/0100-image-generation-studio.ja.md)）。

実装ファイルへの対応は [90-code-map](90-code-map.ja.md)。

## 3.2 リクエストの一生

メンバーの API 呼び出しは同じ前段を通る（認可の原則・エラー形は [05 §5.4](05-api.ja.md) が正）。
以下は完全形で、メンバー向け `/api` の標準ラッパー `withResolved` の手順。CP 完結のルート — memo の
CRUD・スケジュール・保存済みの作業項目クエリ — は `withMembership` を使い、手順 3 で止まって Runtime を
作らない。memo の flush と memo の画像のルートは Agent が要るので `withResolved`。テナントすら要らない
ルート（PAT・テナントの選択）は `withIdentity` で、手順 2 で止まる。preview には専用のラッパー
（`withPreviewResolved`）がある。新しいタブはテナントのヘッダを付けられないため。

1. **authGate**（oauth モードのみ）— cookie 検証と email 注入。除外はそれぞれのルートの隣で宣言する
   （`exemptExact` / `exemptPrefix`）: ログインと OAuth の経路、`/healthz` と `/readyz`、それに自前で
   認証する面 — `/mcp`（Bearer PAT）・`/git/`（git トークンの Basic 認証）・`/internal/`（用途別の
   ブリッジトークン）・`/engine/`（エンジンのセッショントークン）（[07 §7.3](07-security.ja.md)）。
2. **identity 解決** — email → identity（dev は固定の `DEV_USER`）。membership の無い人は、自動参加
   ドメインが合うテナントに入るか、既定テナントへ自動プロビジョン（`AF_PROVISION=auto`）されるか、
   拒否される（`invite`）。
3. **membership 検証** — `X-AF-Tenant`、次に query のフォールバック。
4. **Workspace runtime 解決** — membership → workspace 行（無ければ払い出し、§3.3）→ DEK の unwrap
   （§3.4）→ factory で Runtime を構築（メモリにキャッシュするが DB が正）。
5. **処理または中継** — CP 完結の面はここで答え、それ以外は 5 経路のどれかで Agent へ
   （[05 §5.3](05-api.ja.md)）。

idle-stop の活性の時計を進めるのは、コードが `touchWorkspace` を呼ぶ箇所で、経路ごとに決まっている —
中継を通る書き込み・ストリーム・接続・preview のアクセス・明示的な起動・attention ビーコン・スケジュールの
起床など。読み取りは原則として**数えない** — 例えば中継される `GET` / `HEAD`、`/api/events`、CP 自身が
答える読み取りやポーリング（`GET /api/workspace`・memo の一覧）。これらは例で、網羅ではない。だから
開きっぱなしの Console が Workspace を温め続けることは無い。

Workspace が running でないときに要求が何に出会うかは経路で違う（[05 §5.3](05-api.ja.md)）。一般の REST
中継（`agentProxyAPI.rest`）は状態を確かめずに接続し、Agent に届かなければ `502`。端末は先に確かめて
`409 workspace_starting` か `409 workspace_stopped` を返し、ログインのフローは `running` になるまで
`409 workspace_starting` で断る。**Workspace の起動は Agent を待たない**:
`POST /api/workspace/start` は起動が確定した時点のライブな状態を返す。ECS ではタスクが収束するまで
`starting` と読め、Console がポーリングを続ける。

**次の一歩で Agent を要する要求** — セッションの作成・fork・再開（`POST /api/sessions`・`…/fork`・
`…/start`）、持ち越した回答（`…/carried-answer`）、SSM ノードの検索 — は、停止中の Workspace を自分で
起こし（`AF_AUTOSTART`、既定 on）、`ensureWorkspaceReady` で Agent を待つ: 既定 55 秒
（`AF_AGENT_READY_WAIT_SEC`）で、起動自体の待ちも数えるよう要求の到着時点から測る。過ぎれば
`409 workspace_starting` を返し、起動は裏で続くので再試行が通る。この待ちは ingress のアイドル
タイムアウト（AWS のロードバランサで 60 秒）より短くなければならず、超えると呼び出し側には 409 でなく
504 が届く。セッションの作成と fork はセッション上限の**前に**待つ。上限は Agent の生きているセッションを
数えるので、Agent が起きていないと数えられない。端末の接続・attention ビーコン・読み取り系は
Workspace を起こさない。

## 3.3 manager と Runtime 抽象

- **manager** が membership ごとの資材を初回に払い出し DB へ永続する: 名前（`af-ws-<slug>-<key>`・
  `af-net-<slug>-<key>`・`<WS_DATA>/<slug>/<key>`。既定テナントは slug 無しの `af-ws-<key>` 形を保つ —
  **既存デプロイとの互換のため**。`manager.workspaceNames`）、Agent ポート（`WS_AGENT_PORT` から採番）、
  `AGENT_TOKEN`。CP 再起動では DB の行が正で、状態は実行基盤から読み取り、作り直さない。
- **`Runtime` / `RuntimeFactory`** が実行基盤を抽象化し、**全呼び出し点** — handler・reaper・admin・MCP —
  が factory 経由で構築する。`AF_RUNTIME` で `docker`（既定。`local` も可）・`native`（`wsl`）・
  `ecs`（`aws`）・`ecs-ec2` のどれかを選び（`runtime.NewFactory`）、それ以外は起動時に失敗する。
  それぞれで何ができるかは [ref/deploy-targets](../../guide/ref/deploy-targets.ja.md)、選び方は
  [09](09-deploy.ja.md)。
- **`Start` は Agent の応答でなく、起動の確定で返る**（`Runtime` interface の契約）。ローカルのアダプタは
  `/healthz` を礼儀程度に待ち、ECS はサービスを確定させて非同期に収束させる。`State` は `running`・
  `starting`・`stopped`・`none` を返し、`starting` の Workspace は再 Start も idle-stop もしてはならない。
  準備が間に合わないのはエラーではない。`docker` の Start は停止済みの残骸を消し、home と Claude の
  設定をマウントし、トークンと鍵を env に入れて現行イメージを起動する。
- **Stop は二段の graceful stop**: SIGTERM → 猶予（`AF_STOP_GRACE_SEC`、30 秒）→ SIGKILL。Agent には
  意図的に*短い*猶予（`AGENT_STOP_GRACE_SEC`）を渡し、pane を中断して tmux を先に終わらせる。
- **ライフサイクル操作は Workspace ごとに直列化する** — start・stop・recreate・clean-home はローカルの
  ロックと DB のリースを取るので、同時のリクエストも別の CP レプリカも「確かめてから起動」の間に
  割り込めない。
- **接続追跡** — long-lived 接続の数・セッション別アタッチ・最後に記録された活性（§3.2）・最後の端末キー入力を
  メモリに記録し、他のレプリカ向けに更新式の presence リースを DB へ出す。reaper はこれを読む（§3.7）。
  端末はキー入力がある間だけ presence に数え（`AF_PRESENCE_IDLE_TIMEOUT`、30 分）、ブラウザペインは
  見えている間だけ数える。`POST /api/workspace/attention` は、入力せずに読んでいる人のための Console の
  ビーコン。

## 3.4 起動時の配線: 鍵とトークン

暗号設計そのもの（封筒暗号・鍵導出・crypto-shred の限界）は [07 §7.6](07-security.ja.md)。ここは配線だけ:

- **boot 時**: `AF_MASTER_KEY` をハッシュして master 鍵とし、鍵カストディアン（`localCustodian`）を
  構成する。無ければ暗号は一切無い（開発専用）。
- **Workspace 解決時**: 包まれた DEK をカストディアンで unwrap し（初回はレガシー DEK を導出して包んで
  保存する — 既存ストアを再暗号化しないための互換点）、平文 DEK を `AF_SECRET_KEY` として注入する。
  **Agent は暗号方式に無関心で、鍵の出自を知らない。**
- **ブリッジトークン**も同時に注入する（`workspaceExtraEnv`）: 用途ごとに membership 単位のトークンが 1 つ —
  `AF_INTERNAL_GIT_TOKEN`・`AF_MEMO_TOKEN`・`AF_SCHEDULE_TOKEN`・`AF_MCP_TOKEN`・`AF_DOCS_TOKEN`・
  `AF_ENGINE_ISSUE_TOKEN`・`AF_GIT_OAUTH_TOKEN`・`AF_AWS_PROFILES_TOKEN`、それに `AF_CP_BASE_URL`。
  どれも決定的（master 鍵から導いた鍵、無ければ `WS_DATA` に置いた乱数の鍵による membership id の
  HMAC）なので、起動のたびに注入し直しても何も変わらない。**それぞれ自分のエンドポイントしか開けない**:
  memo のトークンが漏れても、テナントの MCP の秘密は読めない。`PUBLIC_BASE_URL` が無ければどれも
  注入しない。コンテナが CP に届く宛先がそれだから。

## 3.5 MCP サーバ

設計と決定は [decisions/0006](../decisions/0006-mcp-unified.ja.md)。`/mcp` は `AF_MCP_ENABLED=true` の
ときだけ登録する。

- **トランスポート**は Streamable HTTP の最小形: POST の JSON-RPC 2.0（単発 + batch）に JSON で応答し、
  SSE は無い。プロトコルの両世代に応える: ステートレスな 2026-07-28 版（`server/discover`、版は各要求の
  `_meta`）と、旧来の `initialize` ハンドシェイク。**エッジは `/mcp` を Bearer のまま素通しする必要がある。**
- **認証は PAT**（Console で発行・DB はハッシュのみ・[06](06-data.ja.md)）。スコープ（`read` か `write`）は
  発行時に固定され、発行者自身の上限で頭打ち。**role は呼び出しごとに live で解決**し、tenant はトークンで
  固定でクライアントからは受け取らない。
- **member ツール**（`internal/mcpsrv/mcp.go` の `memberTools()`）— 自分のセッションの観測と操縦（一覧・
  状態・出力・送信・作成・停止・再開）、掃除とそのアーカイブ、使用量、リポジトリとモデル、memo キュー。
  目的は「手元の Claude が自分の遠隔セッションを駆動する」こと。
- **admin ツール**（`adminTools()`）— read（Workspace・使用量・セッション・監査ログ・egress の統計と
  許可リスト）と write（Workspace やセッションの停止・メンバーの上限設定・許可リスト変更の提案）。
  `super_admin` かそのテナントの `tenant_admin` に見え、write は監査ログに `actor_kind=mcp` で残る。
- **dangerous ツール**（鍵ローテ・recreate・idle な Workspace の一括停止）は予定しない。求める声が無く、
  エージェントにそれをさせてよいかは、作る前にそれ自体の決定が要る（[decisions/0006](../decisions/0006-mcp-unified.ja.md)）。

## 3.6 memo キュー

溜めて一括でセッションへ送るメモ（テーブルは [06](06-data.ja.md)）。

- **メモの本文とカテゴリの CRUD は CP 完結**: membership 解決だけで済み、**Workspace を起動しない** —
  停止中でも別端末から追加・整理できる。グルーピングは repo × category の 2 段。
- **画像の添付はコンテナの中に置く**: メモは参照を持つだけで、`POST /api/memos/paste-image`・
  `GET /api/memos/images/{file}`・`POST /api/memos/images/gc` は Agent へ中継する。
- **flush**（`POST /api/memos/flush`）は id のリストを受け（「レポ全体」「カテゴリ」「これら」を 1 つの
  表現で扱う）、category 見出しで 1 メッセージに連結し、対象セッションの input へ**1 回だけ**送り、
  送信済みと打刻する。送信には Agent が要るので、flush は runtime を解決する。
- **コンテナ内のオペレーター**は同じ handler に `/internal/memos` から `AF_MEMO_TOKEN` で届く。
- **保持**: 送信済みは送信時に消さず 7 日残し、一覧取得時に lazy に掃除する。

## 3.7 バックグラウンドジョブ

いずれも CP 内の goroutine。間隔は env で、`0` は無効化。

- **reaper（idle-stop）** — `AF_IDLE_SWEEP_INTERVAL`（1 分）。**既定で有効**: セッションは 1 時間で
  （`AF_SESSION_IDLE_TIMEOUT`）、人の判断待ち — 質問・プラン承認・許可 — のセッションは
  `AF_INTERACTION_IDLE_TIMEOUT` で（未設定ならセッションの値）、Workspace は 2 時間で
  （`AF_WS_IDLE_TIMEOUT`）idle になる。これらは配備の既定値で、テナントの limits がそれぞれを `0` も
  含めて上書きする。4 段:
  - **tier 1** は、アタッチされていない idle なセッションを halt する — `shell` と `ssm` 以外の全 kind
    （この 2 つは halt が実行中のジョブを殺すため対象外）。再開できる。
  - **tier 2** は、presence（§3.3）が無く、Workspace を引き留めるセッションも、走っているリポジトリの
    取り込みや画像ジョブも無く、記録された活性（§3.2）からタイムアウトを過ぎた Workspace を止める。`starting` の
    Workspace には触らない。reaper は見たものを公開するので、管理画面の「なぜ止まらないか」は reaper
    自身の答えになる。
  - **tier 3**（ecs-ec2 のみ）は、`AF_ECS_EC2_HIBERNATE_AFTER_SEC` より長く止まっている Workspace の
    home を休眠させる: EBS ボリュームをスナップショットして削除し、次の起動で戻す。既定 off。
  - **tier 4**（ecs-ec2 のみ）は、Workspace が何をしていても `AF_ECS_EC2_BACKUP_EVERY_SEC` ごとに home を
    別のアベイラビリティゾーンへ写す。既定 off。
- **usage サンプラー** — `AF_USAGE_SAMPLE_INTERVAL`（5 分）ごとに、running な Workspace の占有秒を日次と
  時間単位のバケツへ加算する。稼働ヒートマップの元にもなる。モデルの資格情報は利用者持ちなので、
  **運用者のコストはトークンでなく占有時間**で、それをこれが測る。同じ巡回が `starting` の上限も執行する
  （`start_deadline.go`）: 起動から `AF_WORKSPACE_START_DEADLINE`（30 分）経っても `starting` のままの
  Workspace を、明示の停止と同じライフサイクルの柵の下で停止する。そこに届くのは収束し得ない起動だけ
  （ECS が配置を拒むタスクなど）。`0` で無効。サンプラーを止めてもこれは止まる。
- **クラウドコストのポーラー** — 請求書のある runtime（AWS のターゲット）では、`AF_CLOUD_COST_INTERVAL`
  （6 時間）ごとに Cost Explorer を直近 `AF_CLOUD_COST_WINDOW_DAYS`（7 日）分読み、コスト配分タグで
  メンバー別に按分する。`docker` と `native` では何もせず、コストの画面も無い。
- **git GC** — `AF_GIT_GC_INTERVAL`（24 時間）ごとに内蔵 git の bare で `git gc --auto` を走らせ、
  `AF_LFS_GC_GRACE`（14 日）より古い LFS の孤児を prune する（進行中の push と競合しない）。**共有ホストの
  RAM を守るため逐次実行**（[91](91-internal-git.ja.md)）。
- **scheduler** — `AF_SCHEDULER_INTERVAL`（1 分）ごとに期限の来たスケジュールを発火させ、スケジュールごとの
  ゆらぎ（`AF_SCHEDULE_JITTER`、2 分）で散らす。発火は停止中の Workspace を CLI の自己更新無しで起こし、
  `AF_SCHEDULE_WAKE_TIMEOUT`（起動予算の 300 秒）まで待ち、`AF_SCHEDULE_SETTLE` の間 keep-alive を保つ。
- **監査 sweep** — `AF_CLAUDE_AUDIT_INTERVAL`、opt-in で既定 off。コンテナ内で claude がすることは CP の
  proxy を通らないので見えない。Agent → CP 方向は意図的に塞いであるので **CP が pull する**: running な
  claude セッションの transcript を読み、書き込み・編集・コマンドを監査する（`actor_kind=claude`）。
  セッションごとの cursor で進み、**初めて見たセッションは baseline を取るだけ** — 過去を遡って監査しない。
- **ecs-ec2 のプール** — ドリフト sweeper（`AF_ECS_EC2_SWEEP_SEC`、5 分）がスロット・ボリューム・所有者
  タグを AWS から導き直し、落ちた CP がやりかけたことを終わらせる。golden スナップショットの自動焼き
  （`AF_ECS_EC2_GOLDEN_AUTOBAKE`、on）は、Workspace イメージが変わると、新しい home の元になる
  スナップショットを焼き直す。idle-stop の有無に関わらず動く。
- **エンジン** — ロールごとのコントローラ・エンジン表の再読込・借用カタログのポーリング（§3.9）。
- **metrics は常駐ジョブでなく on-demand。** CP が Workspace と同じホストにいれば `/proc` とコンテナの
  cgroup を直接読み、そうでなければ（ECS）Agent に聞く。ホスト全体の統計は配備管理者に限る — あるテナントが
  他のテナントの負荷を推し量れないように。

## 3.8 egress 統制の CP 側

設計と段階運用 — log-only → allowlist → enforce — は [07 §7.8](07-security.ja.md)。CP に住んでいるもの:

- **egress-proxy サブコマンド** — `control-plane egress-proxy` で同じバイナリが forward proxy として動く
  （FQDN 判定・TLS 非復号・`AF_EGRESS_LISTEN`、`:3128`）。許可リストに無いホストを遮断するのは
  enforce のときだけで、loopback・link-local（クラウドのメタデータのアドレスを含む）・unspecified の宛先は
  どのモードでも拒否する。
- **policy 配布** — `GET /internal/egress/policy` が実効の許可リストとモードを proxy へ返す。
- **ingest** — `POST /internal/egress`（`AF_EGRESS_TOKEN`）で観測イベントを受けて日次に集計する。
  would-block は日 × ホストで重複を除き、監査ログにも記録する。
- **admin API** — `/api/admin/egress*`、`super_admin` のみ: 統計・許可リスト（active / proposed /
  retired）・log-only / enforce の切替。
- **member の面** — `GET /api/egress/check` は Workspace がそのホストに届くかを答え、
  `POST /api/egress/propose` は*提案*のエントリを出す。承認は配備管理者の仕事のまま。

コンテナ側の配線は `AF_EGRESS_PROXY_ADDR` があるときだけで、全 Workspace に proxy の env を注入する。
**既定は off で、何も変わらない**。

## 3.9 自前エンジン

エンジンとは何か、どのターゲットに何があるかは [01 §1.3](01-architecture.ja.md) と
[ref/deploy-targets](../../guide/ref/deploy-targets.ja.md)。配備のエンジン表にあるエンジンは CP が持ち、
**Workspace がそれと直接話すことは無い**（[decisions/0071](../decisions/0071-self-hosted-inference-engines.ja.md)）。
例外はメンバー自身の llama.cpp サーバ（lcpp の接続として設定したもの）で、Agent がその URL に自分で
つなぎ、CP は関わらない。設定されていれば配備のエンジンより優先される（[08](08-integrations.ja.md)）。

- **エンジン表** — ロール（`llm`・`image`・`comfy`）ごとに 1 行で、プロバイダ（`llamacpp`・`comfy`・
  `openai-compat`）とライフサイクルを持つ: この配備自身の ECS サービス、`external`（ここでは誰も起動しない
  URL）、`remote`（別の配備のもの）。AWS では `AF_ENGINES_SSM_PARAM`、インラインなら `AF_ENGINES_JSON`
  から読み、`docker` と `native` では `AF_LLM_URL` と `AF_COMFY_URL` が `external` の行を足す
  （[decisions/0076](../decisions/0076-external-image-engine-on-lan.ja.md)）。SSM の形は 10 秒ごとに読み直すが、
  その場で効くのはオファーの並びとキャパシティプロバイダだけ。
- **モデルカタログ** — モデルは表の項目でなく DB の行（[decisions/0072](../decisions/0072-engine-model-catalog.ja.md)）。
  CP は Hugging Face か Civitai の出所を解決し、S3 へ書く取り込みタスクを起動する。CP 自身は S3 を
  読むだけ。エンジンが読み込むアクティブセットを公開する。
- **ゲートウェイ** — `/engine/{key}/v1/*`（と `GET /engine/{key}/props`）。エンジン表があるときだけ
  登録する。Workspace は発行用トークン（`AF_ENGINE_ISSUE_TOKEN`）を持ち、`POST /internal/engine/token` で
  有効 30 日のトークンに替える。これは membership とエンジンのキー 1 つに縛られ、呼び出し側が
  セッションを指定したときはその 1 セッションにも縛られる。指定しない経路（opencode の共有 Managed
  デーモン・画像生成・起動時の問い合わせ）ではワークスペース全体に効く。モデルが読めるのはこの
  2 つ目のトークンで、opencode は `AF_ENGINE_TOKEN` として受け取る。membership がまだ有効か、
  テナントがそのエンジンを使えるかは、呼び出しのたびに確かめ直す。
- **コールドスタート** — ストリーミングの要求にはすぐ 200 を返してハートビートで保つ。非ストリーミングの
  要求は `AF_ENGINE_PLAIN_HOLD`（45 秒、借りたエンジンは 75 秒）保持してから、`Retry-After` 付きの
  `503 engine_waking` で答え、エンジンはそのまま起き続ける。45 秒はロードバランサの 60 秒のアイドル
  タイムアウトに応答を収めるため。
- **起動と停止** — ロールごとのコントローラが ECS サービスを 0 と 1 の間で動かし（`off`・`on`・
  `ondemand`）、Admin → 推論エンジンでロールごとに設定したアイドル時間で止める（既定値は
  `AF_ENGINE_<KEY>_*_SEC`）。`ecs-ec2` で行がオファーを宣言していれば、CP は GPU インスタンスを自分で
  買う: オファーごとに `CreateFleet(type=instant)` を 1 回、宣言の順に。自分の箱はタグで探し直す
  （[decisions/0077](../decisions/0077-engine-boxes-bought-by-cp.ja.md)）。Spot のオファーは配備管理者が受け
  入れてから使う（[decisions/0075](../decisions/0075-engine-purchase-offers.ja.md)）。インスタンスクラスは
  [decisions/0074](../decisions/0074-engine-instance-classes.ja.md)。
- **借用** — `AF_REMOTE_ENGINE_URL` と `AF_REMOTE_ENGINE_TOKEN`（貸し手が `POST /api/admin/engines/issue-token`
  で発行するトークン）があれば、CP は貸し手のカタログを 2 分ごとに取り込み、セッショントークンを貸し手から
  買う（[decisions/0079](../decisions/0079-remote-engine-from-another-deployment.ja.md)）。
- **API** — 管理者向けは `/api/admin/engines…`、メンバー向けは `GET /api/engines/status` と `/api/events`
  の `engines` ストリーム、Agent と借り手の配備向けは `GET /internal/engine/catalog`。
