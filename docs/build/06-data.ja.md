---
audience: "スキーマとマイグレーションに触れる人"
source_of_truth: "`control-plane/migrations/*.sql`（本書はその読み解き。0001〜0028 時点）"
updated: "2026-07"
---

# 06. データモデルとマイグレーション

[English](06-data.md) | 日本語

## 6.1 ストア構成

- **MetadataStore は CP が所有**。interface（Store）に対し **SQLite（既定・pure-Go `modernc.org/sqlite`・WAL）**
  と **Postgres**（`migrations-pg/`、同一スキーマ）の 2 実装。`AF_DB`（既定 `<WS_DATA>/control-plane.db`）。
- ユーザーの資格情報は DB に入れない: Workspace home の暗号ストア `secrets.enc`（[07 §7.6](07-security.ja.md)）。
  DB が持つのは wrap 済み DEK（`wrapped_dek`）のみ。
- 内部 git のリポジトリ実体は FS（`${DATA_DIR}/git/<slug>/<name>.git`）で、DB は台帳（`git_repo` ほか）。

## 6.2 エンティティ（migrations 0001〜0028 の現在形）

**人とテナント**（identity↔tenant は**多対多**。0002 で `app_user` を置換）

| テーブル | 役割 / 主なカラム |
|----------|------------------|
| `tenant` | 部署単位（既定 1 テナント=全社）。`slug`(unique)・`limits`(JSON: max_workspaces 等)・`isolation`・`key_ref`・ログイン規則 3 種（0039: `allowed_providers`／`auto_join_domains`／`allowed_domains`・すべて CSV。**`allowed_emails` は無い** — 「誰が入れるか」の名簿は `membership` が持つ。[61](../decisions/0043-login-idp.ja.md) §61.9.5）|
| `identity` | 人。`email`(unique)・`user_key`(unique・sanitize 済みキー)・`role`(`super_admin`\|`user`) |
| `membership` | identity×tenant の結節。`role`(`tenant_admin`\|`member`)・`UNIQUE(identity_id, tenant_id)`・`status`。**オフボーディングは論理削除**（`status='inactive'`・workspace / home は残る）で、解決系はすべて `status='active'` を要求する。復活は招待 API だけが行う（自動採番経路が復活させると除名が無効化されるため） |
| `identity_provider` | (provider, subject) → identity の対応（0038 / pg 0021。[61](../decisions/0043-login-idp.ja.md) §61.5）。IdP 側で email が変わっても `user_key`＝home を動かさないための鍵。**行が 1 つでもあれば「一度サインインされた identity」**で、テナント定義 IdP の規則 2' はこれを見て claim と拒否を分ける |
| `tenant_idp` | テナント定義のサインイン方法（0040 / pg 0023。[61](../decisions/0043-login-idp.ja.md) §61.11）。`UNIQUE(tenant_id, name)`・`issuer`／`client_id`／`secret_enc`＋`key_ref`（テナント鍵で封印）・`trust`・`allowed_tids`／`allowed_domains`(CSV・**ドメインは必須**)・`status`(`pending`\|`active`\|`suspended`)・`approved_by`／`approved_at`。**行を書くのは tenant_admin、`active` にできるのは super_admin だけ**（IdP の登録は「誰であるか」を宣言する権限で、identity は email でデプロイ全体に 1 つのため）。CP が見る provider id は `t:<tenant-slug>:<name>` で、env 由来（`entra` 等）と名前空間が分かれている |
| `tenant_git_oauth` | テナントが登録した git プロバイダの OAuth アプリ（0048 / pg 0032。[71](../decisions/0052-tenant-git-oauth.ja.md)）。`UNIQUE(tenant_id, provider)`（`provider` = `github`\|`bitbucket`）・`client_id`・`secret_enc`＋`key_ref`（`tenant_idp` と同じ封筒）。**status 列が無いのが `tenant_idp` との差**——clone 用の OAuth アプリは「誰であるか」を宣言せず、`redirect_uri` は CP 固定・token は本人のワークスペースにしか渡らないので、tenant_admin の保存で即有効（ADR0052 決定 3）。GitHub 行は device flow なので `secret_enc` は常に空。**env は読まない**（`BITBUCKET_OAUTH_KEY/SECRET` は廃止、`GITHUB_OAUTH_CLIENT_ID` はサインイン専用へ） |
| `user_limit` | membership 単位の上限（`max_sessions`・`disk_gb`・`mem_limit`＝Workspace RAM 上限 bytes、0018）。テナント枠内で管理者が設定 |

**実行環境**（Workspace は **membership 単位**＝同一人物でもテナントごとに完全分離）

| テーブル | 役割 / 主なカラム |
|----------|------------------|
| `workspace` | `membership_id`(unique)・`container_name`・`network`・`data_dir`・`agent_port`・`agent_token`・`state`・`settings`(0009: CP 所有の member 設定 JSON。停止中でも編集でき起動時に env へ反映) |
| `session` | Agent 側 tmux の **DB ミラー**（PK=`workspace_id,name`）。`kind`・`dir`・`repo`・`state`・`last_seen` |
| `wrapped_dek` | 封筒暗号（0003）: per-workspace DEK を per-tenant KEK で wrap した暗号文 + `key_ref`/`key_version` |

**アクセスと監査**

| テーブル | 役割 |
|----------|------|
| `pat` | MCP 用 Personal Access Token（0006）。`token_hash`=SHA-256（平文非保存）・`scope`(read\|write\|admin)。**role は発行時に凍結せず呼び出し時に live 解決** |
| `audit_log` | 監査（0007）。`actor_kind`(user\|admin\|mcp\|system)・`action`・`target`・`tenant_id`(''=デプロイ全体)・`http_status`（0027: 上流応答の保存、0=未記録）。書き込み点は [05 §5.5](05-api.ja.md) |
| `usage_daily` | showback（0008）。**workspace 占有秒**の日次バケツ（BYO モデルでは Claude 使用量でなく占有が運用者コスト）。サンプラーが加算＝近似で十分の設計 |

**機能別**

| テーブル | 役割 |
|----------|------|
| `ssm_profile` / `ssm_host` | SSM ログイン（0010→0011 で 2 層化）: profile=共通 SSO 束（1 つの `~/.aws` named profile に対応）/ host=個別インスタンス。**AWS の秘密は保存しない**（短命credはコンテナ内 `aws sso login` が取得・CP に到達しない） |
| `egress_daily` / `egress_allowlist` / `deployment_setting` | egress 統制（0012/0013・[07 §7.8](07-security.ja.md)）: 日次観測集計 / 版管理 allowlist（state=active\|proposed\|retired）/ デプロイ全体 KV（egress mode 等） |
| `git_repo` / `lfs_object` / `lfs_lock` | 内部 git プロバイダの台帳（0014〜0016・[91](91-internal-git.ja.md)）。LFS 実体は FS content-addressed、テーブルは O(1) クォータ集計と locks 用。**git アクセストークンは非保存**（per-membership HMAC を都度導出） |
| `memo` / `memo_category` | メモキュー（0017・[03](03-control-plane.ja.md)）。membership×repo×category、`sent_at`='' が未送、送信済みは retention 後に sweep。`attachments`（0021）は画像添付の JSON 参照（実体はコンテナ内、DB 非保存）。`memo_category`（0020）はカテゴリの並び順と空カテゴリの存在を持つ |
| `notification` / `notification_usage_state` | 通知センター（0019・[03](03-control-plane.ja.md)）: membership 毎の通知行（`event_id` unique・`seen_at`）と、使用量しきい値通知の窓状態 |
| `schedule` / `schedule_run` | 定時実行（0022〜0026・[docs/38](../decisions/0021-scheduled-execution.ja.md)）: cron/interval/once の定義と発火台帳（`next_run`/`last_run`）。reuse モードの回転台帳（0024）・run の対象セッションと manual/scheduled 区別（0025/0026）。CP DB に置くのは Workspace 停止中も時計を見られるのが CP だけだから |
| `mcp_server` | テナント配布 MCP サーバ（0028・[docs/48](../decisions/0031-mcp-registry.ja.md)）: remote（http/sse）定義のみで **stdio 用の command/args/env カラムを意図的に持たない**（ADR0031）。`headers_enc` はテナント鍵で封筒暗号、`user_secret`=1 はヘッダ名だけ配布 |

## 6.3 関係の要点

```
identity ──< membership >── tenant
                │ 1:1                └─< git_repo / egress_allowlist(tenant scope) / mcp_server
             workspace ──< session
                │ 1:1
             wrapped_dek
membership ──< pat / user_limit / ssm_profile ──< ssm_host / memo / memo_category
           / usage_daily / notification / schedule ──< schedule_run
```

- `identity.user_key` は email の sanitize（小文字化・非英数→`-`・40 字上限）。コンテナ名
  `af-ws-<slug>-<key>`（既定テナントは旧 `af-ws-<key>` を維持）や home パスに使われる。
- `workspace.state` は Start/Stop 処理が DB 同期する（Runtime の実状態とずれた場合は inspect 採用で回復）。
- `session` は Agent 側 tmux が正で DB はミラー（ReplaceSessions で洗い替え）。表示・admin 俯瞰・
  クォータ判定に使い、実操作は必ず Agent へ。

## 6.4 マイグレーション作法

- `//go:embed` で SQL を同梱し、起動時に**冪等適用**（適用済み番号を記録）。**両ディレクトリに
  置く**——番号は揃わない（`migrations-pg/0001` が SQLite の初期系列を畳んだ統合スキーマなので、
  以降は番号が別々に進む）。対応関係はファイル冒頭のコメントで示す（例:
  「Postgres mirror of migrations/0028_mcp_server.sql」）。
- ⚠️ **片方に足して片方を忘れても、誰も気づかない。** 実際に `memo_category`（`migrations/0020`）が
  Postgres 側へ写されないまま残り、ECS/RDS のデプロイではカテゴリの API が全部 500 を返していた
  （Console は配列でない応答を空リストに畳むので、症状は「エラー」ではなく**「カテゴリが出ない」**
  ——障害として報告しようがない形だった）。2026-08-22 に `migrations-pg/0030` で写し、
  **`TestSchemaDialectParity`（`store_schema_parity_test.go`）が両系列の着地スキーマを実測で
  突き合わせる**ようにした。`AF_TEST_DATABASE_URL` を与えたときだけ走るので、**マイグレーションを
  足したら実 Postgres で 1 度は回すこと**（[postgres の立て方](10-development.ja.md)）。
- ⚠️ **マイグレータは `;` で素朴に分割する**ため、**SQL コメントにセミコロン（と引用符）を書かない**
  （0011 以降の各ファイル冒頭に同趣旨の注記あり）。
- 破壊的変更は新テーブル + データ移行（0002 の `app_user`→`identity`+`membership` が先例）。
  使わなくなったカラムは残置可（0011 の `ssm_host.account_id` 等）——SQLite の ALTER 制約を尊重。
- 新しい member 設定は**カラムを増やさず** `workspace.settings` の JSON フィールドで（0009 の設計意図）。
- migration を足したら**本書 §6.2 の表を更新**する（更新責務は [README](README.ja.md)）。
