---
audience: "スキーマとマイグレーションに触れる人"
source_of_truth: "`control-plane/internal/store/migrations/*.sql` と `migrations-pg/*.sql`（本書はその読み解き。SQLite 0076／Postgres 0061 時点）"
updated: "2026-09"
---

# 06. データモデルとマイグレーション

[English](06-data.md) | 日本語

## 6.1 ストア構成

- **メタデータストアは CP が所有する。** ポートは 1 つ（`store.Store`）で、機能ごとの
  サブインターフェース（`TenantStore`・`WorkspaceStore`・`SessionShareStore`・
  `EngineModelStore` …）の和になっている。新しいメソッドはそれが属するサブインターフェースに
  足し、自己完結した部品は必要な最小のサブインターフェースにだけ依存してよい。裏にあるのは
  **2 つの方言で共有する 1 つの SQL 層**で、クエリは `?` で書き、Postgres では `$n` に
  書き換える。
  - **SQLite** が既定。pure Go（`modernc.org/sqlite`）・WAL・外部キー有効・接続 1 本。
    ファイルは `AF_DB`（既定 `<WS_DATA>/control-plane.db`）。
  - **Postgres** は `AF_DATABASE_URL` か `AF_DB_HOST` があるときに選ばれる（後者なら DSN は
    `AF_DB_*` から組み立てる）。CP を複数レプリカで動かすならこちらが要る。ワークスペース
    操作のフェンス（`AcquireWorkspaceOperationFence`、セッション advisory lock）はここにしか
    無い。SQLite は CP 1 台の構成。
- **ユーザーの資格情報は DB に入れない。** ワークスペースの home にある暗号ストアに置く
  （[07 §7.6](07-security.ja.md)）。DB が持つのは wrap 済みの DEK だけ。**テナントが
  預ける秘密はテナント鍵で封印する**：テナント IdP のクライアントシークレット、テナントの
  git OAuth シークレット、MCP のヘッダ、共有の提案や引き継ぎの本文。暗号文＋`key_ref` で
  保存し、平文＋空の `key_ref` に落ちるのはマスターキーの無いデプロイだけ。
- ⚠️ **平文で保存する秘密が 1 つある：`workspace.agent_token`。** CP がそのワークスペースの
  Agent に示す bearer である（[07 §7.5](07-security.ja.md)）。DB のコピーを持つ者は、
  到達できるどの Agent にも CP として認証できる。だから DB のダンプやバックアップは秘密として扱う。
- **RDS ではパスワードはプロセスが持ち続けてよい値ではない。** `AF_DB_PASSWORD` はタスク
  定義の `secrets` で届き、**ECS がこれを解決するのはタスク起動時の 1 回だけ**。RDS の
  マネージドなマスターパスワードは 7 日ごとにローテートする。そこで Postgres が `28P01` を
  返したら、CP は `AF_DB_PASSWORD_SECRET_ARN` のシークレットを読み直し（`AWSCURRENT`、
  次に `AWSPENDING`）、**コネクタの中でリトライ**して呼び出し側に見せない
  （[decisions/0065](../decisions/0065-db-credential-rotation.ja.md)）。env だけだった
  2026-09-01 には、ローテーション 1 回で全面停止が 15 分続いた。ARN 未設定なら注入値が
  すべてで、RDS 以外のデプロイはすべてこちら。
- **このストアを実際に見に行くのは `GET /readyz`**（`Store.Ping`）。`/healthz` は
  プロセスが走っていることしか報告しない（[09 §9.9](09-deploy.ja.md)）。

## 6.2 エンティティ

**人とテナント**（identity ↔ tenant は**多対多**）

| テーブル | 役割 / 主なカラム |
|---|---|
| `tenant` | 部署単位（既定は 1 テナント＝全社）。一意な `slug` と JSON の `limits`（`max_workspaces`・`max_sessions`・`max_workspace_mem` …）。ログイン規則は CSV の列：`allowed_providers`・`auto_join_domains`・`allowed_domains`・`hidden_providers`。最後のものは**表示専用で、門には決して使わない**。`allowed_cidrs` は接続元ネットワークの制限（[decisions/0047](../decisions/0047-tenant-network-restriction.ja.md)）。毎リクエスト、ログイン規則のキャッシュ越しに読まれるので、`limits` ではなく列にしてある。**`allowed_emails` は意図して無い**：誰が入れるかの名簿は `membership`（[decisions/0043](../decisions/0043-login-idp.ja.md)）。`isolation` と `key_ref` に従って動くものは無い。鍵の管理者はテナント id を鍵参照に使う（[07 §7.6](07-security.ja.md)） |
| `identity` | 人。`email` は空でなければ一意。`user_key` は一意で、コンテナと home の名前になる sanitize 済みキー。`role` はデプロイ全体のロール（`super_admin` \| `user`） |
| `membership` | identity × tenant の結節。`UNIQUE(identity_id, tenant_id)`、`role`（`tenant_admin` \| `member`）と `status`。**オフボーディングは論理削除**（`status='inactive'`）で、workspace と home は残り、解決系はすべて `status='active'` を要求する。**サインインや自動作成の経路は決して復活させない**（`EnsureMembership` は再有効化しない）。自動経路が復活させると除名が黙って取り消されるから。明示の判断である招待は復活させる。行の削除は別の、後の手順（§6.3） |
| `identity_provider` | (provider, subject) → identity。**IdP 側で email が変わっても home を動かさないための鍵**で、だから `user_key` は今のアドレスから導かれるとは限らない。行が 1 つでもあれば「一度サインインされた identity」で、テナント IdP の規則の 1 つがこれを見る。`realm`（issuer か `https://github.com`）と `realm_claim`／`realm_subject`（Entra の `oid` のような安定した claim の名前と値）で、2 つのボタンから来た同じ IdP アカウントを 1 つの identity に解決する。claim の値は必ず署名済みトークンから取り、テナントの行からは取らない |
| `tenant_idp` | テナント定義のサインイン方法。`kind` は `oidc`（`issuer`・`trust`・`allowed_tids`）か `github`（`allowed_orgs`）。封印した `secret_enc`、**必須**の `allowed_domains`、`link_claim`、`status`（`pending` \| `active` \| `suspended`）を持つ。**行を書くのはテナント管理者、`active` にできるのはデプロイ管理者だけ。** IdP の登録は「誰であるか」を宣言する権限で、identity は email を鍵にデプロイ全体で 1 つだから。active の行で承認された内容を変える（issuer・client id・trust・kind・`link_claim` の変更、ドメイン・テナント id・org を広げる）と `pending` に戻る（`repend`）。CP から見える provider は `t:<tenant-slug>:<name>` で、env で設定したものとは衝突しない |
| `tenant_git_oauth` | テナント自身の git プロバイダ用 OAuth アプリ（`github` \| `bitbucket`）。(tenant, provider) ごとに 1 行で、同じ封筒で封印する。**`tenant_idp` と違って status 列が無い。** clone 用の OAuth アプリは誰かが誰であるかを宣言せず、コールバックは CP 固定で、トークンは本人のワークスペースにしか届かない。だからテナント管理者の保存で即有効（[decisions/0052](../decisions/0052-tenant-git-oauth.ja.md)）。GitHub の行のシークレットは意図して空（device flow には要らない）。**これらのために env は一切読まない**：`GITHUB_OAUTH_CLIENT_ID` は今はサインイン用アプリだけを指す |
| `user_limit` | membership 単位の上限。テナントの枠内で管理者が設定する：`max_sessions`・`disk_gb`・`mem_limit`（bytes）・`cpu_limit`（Fargate CPU 単位）・`slot_class`（デプロイが宣言するクラス id。効くのは `ecs-ec2` だけ）。0 や空はテナントかデプロイの既定 |

**ワークスペースとセッション**（ワークスペースは **membership 単位**＝同一人物でも
テナントごとに完全分離）

| テーブル | 役割 |
|---|---|
| `workspace` | `membership_id`（一意）・`container_name`・`network`・`data_dir`・`agent_port`・`agent_token`・`state`・`settings`。`settings` は CP が所有する JSON で、停止中でも編集でき、起動時に環境へ反映される。`data_dir` は使うたびに今の `WS_DATA` へ付け替えるので、データディレクトリを動かしてもワークスペースに空の home が渡らない。`preview_slug` は起動ごとに引き直し、停止で消す。空でないときだけ一意（プレビューのリクエストが持つのは Host だけだから。[decisions/0062](../decisions/0062-preview-subdomain.ja.md)） |
| `session` | **Agent のセッション一覧のミラー**で、真実ではない。PK (`workspace_id`, `name`) と `kind`・`dir`・`repo`・`label`・`state`・`last_seen`。`carried` は畳んだときにまだ答えを待っていた質問・プラン・許可、`studio` はそのセッションが結び付いた画像スタジオ。どちらも、停止中のワークスペースの一覧がこの表だけから作られるので要る |
| `wrapped_dek` | 封筒暗号：ワークスペースごとの DEK をテナントごとの KEK で wrap したもの＋`key_ref`・`key_version` |
| `workspace_activity` | ワークスペースごとに 1 行：`last_seen_at` と `connected_until`。**どの CP レプリカも書く**ので、あるレプリカのアイドル停止が別のレプリカにある接続を見られる |
| `workspace_stop_intent` | アイドル停止がワークスペースを止める前に取る不可分の宣言。別のレプリカに来た新しい活動と競合しないようにする |

**アクセスと監査**

| テーブル | 役割 |
|---|---|
| `pat` | MCP 用の Personal Access Token。**保存するのは SHA-256 のハッシュだけ。** identity に属し、任意で 1 つの membership に紐付く。`scope` は `read` \| `write` \| `admin:dangerous` で、発行時に発行者のロールで上限を掛ける。**ロール自体は発行時に凍結せず、呼び出し時に live 解決する** |
| `audit_log` | `actor_kind`（`user` \| `admin` \| `mcp` \| `system` \| `claude`。最後は claude セッション自身の編集とコマンドで、transcript の掃引が記録する）・`actor_id`・`action`・`target`・`detail`・`tenant_id`（空＝デプロイ全体）。`http_status` は中継した変更操作の上流の応答（0＝未記録）。**membership の列を持たない**ので、オフボーディングが自分の記録を消すことはできない。書き込み点は [05 §5.5](05-api.ja.md) |

**占有とコスト**（請求書から来た値でない限り、どれも金額として描かない）

| テーブル | 役割 |
|---|---|
| `usage_daily` | showback：**ワークスペース占有秒**の日次バケツ。持ち込み資格情報のモデルでは、運用者のコストはトークンでなく占有。サンプラーが加算する近似で、設計上それで十分 |
| `usage_hourly` | 同じ占有の**時間**バケツ＋セッション本数（[decisions/0066](../decisions/0066-uptime-heatmap.ja.md)）。⚠️ `membership_id = ''` はメンバーではなく**その時間のサンプラーのハートビート**で、「観測できて停止」と「記録なし」を分けるのがこの行。`measured_secs` は一段下で同じ区別をする：Agent を読めなかった稼働中のワークスペースのセッション数は 0 ではなく不明。保持 92 日 |
| `cloud_cost_daily` | コスト配分タグで帰属させた AWS の請求額。(day, membership, service) ごと（[decisions/0048](../decisions/0048-member-cloud-cost.ja.md)）。金額は整数の **micro 単位**で、浮動小数にしない。`estimated` は Cost Explorer がまだ変えうる日の印。`membership_id = ''` は共有バケツで、**人に按分しない** |
| `cloud_cost_role_daily` | 同じ共有バケツを `af-role` タグでもう一度切ったもの。同じ明細なので合計は共有分の合計と等しい。`role = ''` は欠損ではなく正直な残差（NAT・ALB・RDS・税） |

**共有と引き継ぎ**（transcript の本文は所有者のワークスペースから出ない）

| テーブル | 役割 |
|---|---|
| `session_share` | ACL の行：所有者の membership → 共有先の membership。`scope_type` は `session` \| `repo` \| `worktree`、`permission` は `ro` \| `rw` |
| `shared_session_catalog` | 共有先に見せてよい形の、所有者ワークスペースのセッション一覧。同期のたびに丸ごと差し替え、共有先のプロジェクト／worktree ツリーに要るもの（`working_copy_id`・`parent_working_copy_id`・`branch`・live の `activity`）を持つ |
| `session_share_proposal` | **共有先 → 所有者**：`rw` の共有先が提案し、所有者が承認する操作。本文は封印する。`status` は `pending` \| `processing` \| `approved` \| `rejected` \| `expired`。カタログ行から cascade する |
| `session_share_owner_lease` | 1 人の所有者について、承認された操作と共有の変更をレプリカをまたいで直列化する |
| `session_handoff_offer` | **所有者 → 共有先**：すでにそのセッションを共有している相手へ差し出す「この続きを」（[decisions/0057](../decisions/0057-member-handoff.ja.md)）。提案とは向きが逆で、だから別の表。持つのは封印した文章と、Agent が読んだ git の座標（`repo_remote`・`branch`・`head_sha`）だけ。**部分ユニーク索引で、未処理はセッションごとに 1 件だけ。** 数えてから入れる形だと、同時に来た 2 件が両方通る |

**自前エンジン**（[decisions/0071](../decisions/0071-self-hosted-inference-engines.ja.md)・
[0072](../decisions/0072-engine-model-catalog.ja.md)・
[0079](../decisions/0079-remote-engine-from-another-deployment.ja.md)。engine key は
エンジン表のキー（`llm`・`image`））

| テーブル | 役割 |
|---|---|
| `engine_models` | モデルカタログ。PK (`role`, `id`) で**デプロイ全体で 1 つ**。`files` はエンジンのバケットにあるオブジェクトを指し、実体はそちらにあってここには無い。トグル（`enabled`・`selected`・`is_default`）、モデルごとの引数と生成の既定値、表示用のメタデータ、コンテキスト窓を決める KV キャッシュの形状、取り込み時点のスナップショットとしてのライセンス（`license`・`license_name`・`commercial_use` をすべて残す）。ライセンスの受諾は、誰が・いつ・どのテナントで・受諾時のライセンス文を記録する。書き手が 2 つあり、設定には compare-and-swap が無いので、設定の JSON ではなく表にしてある。**`last_used_at` は意図して無い**：誰も判断に使わない数字のために、リクエスト経路に書き込みが増えるから |
| `engine_ingest_jobs` | タスクとして走るモデルのダウンロード。**ジョブが存在する唯一の記録**で、状態は信じずに ECS から突き合わせる。`spec` はジョブが作るカタログ行で、開始時に書く。ダウンロード中に CP が入れ替わっても行を作れるように |
| `engine_hourly` | エンジンの時間ごとの占有。オンデマンド制御器自身の tick が書く。状態は 3 つ：行があればその時間は観測済み、無ければ不明。tick の間隔が変わるので `observed_secs` は導出せず保存する。`draining_secs` は停止したがまだ課金中 |
| `engine_membership_hourly` / `engine_usage_undelivered` | エンジンが誰の仕事をしていたか。ワークスペースを持たない借り手にエンジンを**貸す**デプロイのためのもの。前者は membership × 時間ごとのリクエスト数、後者はゲートウェイがワークスペースへ届けられなかった使用量の行を残す。**どちらも 2 つ目の使用量台帳ではなく**、届かなかった行を後から届け直すことはしない。保持 92 日 |

**機能別**

| テーブル | 役割 |
|---|---|
| `ssm_profile` / `ssm_host` | SSM ログインの 2 層：profile は共通の SSO 束（1 つの `~/.aws` named profile に対応）、host は個々のインスタンス。**AWS の秘密は保存しない**：短命の資格情報はコンテナの中で取得され、CP には届かない |
| `egress_daily` / `egress_allowlist` | egress 統制（[07 §7.8](07-security.ja.md)）：(day, host, allowed) ごとの日次集計と、全体またはテナント単位の版管理 allowlist（`active` \| `proposed` \| `retired`） |
| `deployment_setting` | デプロイ全体の KV。egress モード、ブランディング、エンジンごとの設定、封印した Hugging Face と Civitai のトークン、claude 監査のカーソルを持つ。ここの値には compare-and-swap が無いので、書き手が 2 つあるものは表にする |
| `git_repo` / `lfs_object` / `lfs_lock` | 内部 git プロバイダの台帳（[91](91-internal-git.ja.md)）。bare リポジトリは `<WS_DATA>/git/<tenant-slug>/<name>.git` のファイルで、LFS の実体はその横に content-addressed で置く。表はリポジトリ一覧、O(1) のクォータ集計、ロックのためにある。**アクセストークンは保存しない**：membership ごとの HMAC を都度導出する |
| `memo` / `memo_category` | メモキュー（[03 §3.6](03-control-plane.ja.md)）。添付は JSON の参照で、画像そのものはコンテナの中に残る。`memo_category` はカテゴリの並び順を持ち、空のカテゴリも残す。送信済みのメモは 7 日で掃除する |
| `notification` / `notification_usage_state` | 通知センター（[03](03-control-plane.ja.md)）：membership ごとの行（`event_id` で一意、保持 7 日）と、使用量しきい値通知の窓の状態 |
| `schedule` / `schedule_run` | 定時実行（[decisions/0021](../decisions/0021-scheduled-execution.ja.md)）。前者は定義と発火台帳（`next_run`・`last_run`）、reuse モードの回転、`report`、`stop_after_run`。後者は上限付きの実行履歴。**CP の DB に置くのは、ワークスペースが停止している間も時計を見られるのが CP だけだから** |
| `mcp_server` | テナント配布の MCP サーバ：remote の定義だけで、**stdio 用の command・引数・環境の列を意図して持たない**（[decisions/0031](../decisions/0031-mcp-registry.ja.md)）。ヘッダは封印する。`user_secret=1` はヘッダ名だけを配り、値は各メンバーが入れる |
| `work_item_query` / `work_item_cache` / `work_item_session` | ワークアイテムの受信箱（[decisions/0061](../decisions/0061-work-item-inbox.ja.md)）：保存したクエリ、**秘密でない**チケットのメタデータのキャッシュ（本文・コメント・トークンは持たない。ワークスペースが停止中でもレールを描くため）、どのチケットからどのセッションを始めたかの台帳。台帳はキャッシュへの外部キーを意図して持たない。キャッシュは揮発するクエリ結果だから |

`schema_migrations` は適用済みの版を記録する（§6.5）。

## 6.3 関係の要点

```
identity ──< membership >── tenant ──< git_repo, tenant_idp, tenant_git_oauth,
   │            │                      mcp_server, egress_allowlist
   │            │ 1:1
   │         workspace ──< session
   │            │      ──< shared_session_catalog ──< session_share_proposal
   │            │                                 ──< session_handoff_offer
   │            └─ 1:1  wrapped_dek, workspace_activity, workspace_stop_intent
   ├─< identity_provider
   └─< pat (optionally pinned to one membership)

membership ──< user_limit (1:1), ssm_profile ──< ssm_host, memo, memo_category,
               notification, schedule ──< schedule_run,
               work_item_query ──< work_item_cache,
               work_item_session (no link to a query or the cache),
               session_share (as owner or as recipient)
```

- identity のキーは email を sanitize したもの：小文字化し、英数字以外の連続を `-` に置き換え、
  40 字で切る。コンテナ名（`af-ws-<slug>-<key>`。既定テナントは `af-ws-<key>` のまま）と
  home のパスに使う。
- `workspace.state` は起動と停止が書く。API が報告するのはランタイム自身の答えなので、
  古くなった列は信じられない。
- **`session` はミラーで、真実は Agent。** `ReplaceSessions` がワークスペースの行を今の一覧で
  差し替える。表示・管理者の俯瞰・クォータ判定にはミラーを使い、実際の操作は必ず Agent へ送る。
- **削除は表を明示的に列挙する。** `DeleteWorkspace`・`DeleteMembership`
  （`membershipCascade`）・`DeleteTenant` は、宣言している表が一部しかない `ON DELETE CASCADE`
  に頼らず、依存する表を順に挙げる。例外が 1 つある。`DeleteWorkspace` は
  `shared_session_catalog` の行を消し、`session_share_proposal` と `session_handoff_offer`
  の削除はそこからの外部キーの cascade に任せる（両表とも両方言で宣言している）。
  `membershipCascade` はこの 2 表も明示的に消す。membership の削除は「除名 → ワークスペースの破棄 →
  行の削除」の最後の手順で、取り消せない。**意図して残すのは履歴**：`audit_log`・
  `usage_daily`・`usage_hourly`・`cloud_cost_daily`・エンジンの帰属の表。消すと過去の合計が
  後から変わる。

## 6.4 DB に無いもの

本章が持つのは CP の DB だけ。ほかの永続状態は別の章が持つ：

- **ワークスペース自身の状態**は home にある：セッションのメタデータ（ごみ箱を含む）、
  transcript、使用量の記録（[04 §4.2](04-agent.ja.md)・[04 §4.7](04-agent.ja.md)）。
  **ユーザーの秘密**は同じ home の暗号ストアにある（[07 §7.6](07-security.ja.md)）。
- **home が物理的にどこにあるか**はデプロイ先で決まる（[09](09-deploy.ja.md)）。
- **内部 git のリポジトリ**は `<WS_DATA>/git/` の下のファイル（[91](91-internal-git.ja.md)）。
- **エンジンのモデルファイル**はエンジンのバケットのオブジェクトで、`engine_models.files` が
  名指す。

## 6.5 マイグレーション作法

- SQL は同梱（`//go:embed`）し、起動時に**冪等に**適用する。ファイルごとに 1 トランザクションで、
  版を `schema_migrations` に記録する。**変更は必ず両方のディレクトリに置く。** 番号は揃わない：
  `migrations-pg/0001` は SQLite の初期系列を畳んだ統合スキーマで、以後は別々に採番している。
  **対にするのは番号の後ろの名前**（例：`migrations/0071_session_studio.sql` ↔
  `migrations-pg/0056_session_studio.sql`）。
- ⚠️ **片方に足して片方を忘れても、誰も気づかない。** 実際に 2 度起きた。`memo_category` は
  Postgres へ写されず、カテゴリの呼び出しがすべて 500 を返した。**Console は配列でない応答を
  空リストに畳むので、症状は「カテゴリが出ない」だった**。障害として報告しようのない形である。
  `workspace.settings` も写されず、読み込みがエラーを飲むので保存だけが失敗した。今は 2 つの
  テストが守る：
  - `TestMigrationSeriesDeclareTheSameSchema` は書かれた SQL を突き合わせる。DB が要らず、
    CI で走る。
  - `TestSchemaDialectParity` は着地したスキーマを突き合わせ、`AF_TEST_DATABASE_URL` を
    与えたときだけ走る。**マイグレーションを足したら実 Postgres で 1 度は回すこと**
    （[立て方](10-development.ja.md)）。
- ⚠️ **同じ版番号のファイルが 2 つあると CP は起動しない。** 意図した動作で、そうしないと
  2 つ目が適用済み扱いで永遠に飛ばされる。並行するブランチでは日常的にぶつかる。**どこかの
  デプロイが適用済みのマイグレーションの番号を付け替えること自体が破壊的変更**で、そのデプロイは
  同じ DDL に新しい番号で出会う。2 つのブランチがぶつかったら、まだどこにもデプロイされていない
  方を動かす。両方デプロイ済みなら、名前を変えずに修復用のマイグレーションを足す（SQLite
  `0059`／Postgres `0044` がそうした）。
- ⚠️ **マイグレータは `;` で素朴に分割する**ので、**文の終わり以外にセミコロンを書かない**：
  コメントにも文字列リテラルにも。コメントの中に 1 つあると文が半分に切れ、CP は
  「incomplete input」で起動しなくなる。
- ⚠️ **SQLite では、`workspace` の新しい列を `repairWorkspaceColumns` にも挙げる。** 0002 の
  `workspace_new` への入れ替えは後の ALTER より後に走り、それらが足した列を全部捨てる。修復は
  入れ替えの後で列を足し直す。
- **SQL は両方言で通る形に保つ。** 時刻は RFC3339 の `TEXT`、真偽値は 0/1 の `INTEGER`
  （どちらの方言でも `BOOLEAN` は使わない）、片方の方言しか受け付けないキーワードを列名に
  しない（だから `precision` ではなく `model_precision`）。
- 破壊的変更は新テーブル＋データ移行で行う。使わなくなった列は残してよい
  （`ssm_host.account_id`）。SQLite の ALTER の制約を尊重するため。
- **新しいメンバー設定は列を増やさず、`workspace.settings` の JSON のフィールドにする。**
- **membership・workspace・tenant に紐付く新しい表**は、履歴でない限り §6.3 の対応する削除に
  加える。
- マイグレーションを足したら **§6.2 を更新する**。更新責務の表は [README](README.ja.md)。
