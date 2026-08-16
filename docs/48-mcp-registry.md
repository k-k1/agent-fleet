# 48. ユーザー / テナント独自 MCP サーバーの登録（MCP レジストリ）— 設計

- 状態: **✅ P0〜P5 実装済み**（型・実効レジストリ合成・user スコープ CRUD・接続テスト・
  Console「MCP サーバー」タブ・アシスタント配線・
  **全 kind のセッション materialize（claude / codex / opencode / copilot / cursor / kiro / agy）**・
  **テナントスコープの配布（CP テーブル / 管理 API / ブリッジ / 配布キャッシュ / `user_secret`）**・
  **egress allowlist 連携（§9.1）**）。残りは §14 の未決のみ。
  意思決定は [decisions/0031](decisions/0031-mcp-registry.md)。
- 関連: [history/19](history/19-assistant-chat.md)（アシスタント）/ [25](25-ops-monitoring.md)（組み込み ops 連携 = 本設計が一般化する対象）/
  [20](20-container-audit-egress.md)（egress allowlist）/ [46](46-usage-accounting.md)（残 P5 = MCP の使用量計上）/
  [27](27-agent-managed-driver.md) §codex thread MCP / [32](32-agy-agent-kind.md) §MCP / [43](43-kiro-agent-kind.md) §2.6
- 図: [`img/mcp-wiring.drawio`](img/mcp-wiring.drawio) — 「提供する側」（CP `/mcp` / agent `mcp-stdio` の 2 面）と
  「利用する側」（本設計＝レジストリ → materialize / アシスタント配線）の全体配線。
  プロセス構成そのものは [`img/architecture-overview.drawio`](img/architecture-overview.drawio)。

---

## 0. 目的 / 非目的

**目的**: 利用者（member）とテナント管理者が **任意の MCP サーバーを Agent Fleet に登録**し、
アシスタントチャットと対話セッションの両方から使えるようにする。

**非目的**（v1 で扱わない）:

- Agent Fleet が **提供する側**の MCP（CP `/mcp`、agent `mcp-stdio`）の変更。本設計は
  **利用する側**（クライアントとして外部 MCP を掴む）だけを扱う。
- MCP サーバーそのもののホスティング / プロキシ。af は定義を配るだけで、通信は各 CLI が直接行う。
- ツール単位の許可制御（`mcp(server/tool)` 粒度）。v1 はサーバー単位の ON/OFF まで。
- OAuth を要する MCP サーバー（各 CLI に `mcp login` があるが、af は関与しない）。

---

## 1. 現状（コード実測 2026-07-27）

### 1.1 いま af が持っている MCP 関連

| 層 | 実体 | 役割 |
|----|------|------|
| CP | `control-plane/mcp.go` `/mcp` | af が**提供**する運用 MCP（member 4 + admin 6 ツール） |
| Agent | `workspace/agent/mcp_stdio.go` | af が**提供**するローカル stdio MCP（af_read / af_write） |
| Agent | `workspace/agent/mcp_run.go` | **組み込み固定 3 種**の外部 MCP（pagerduty / grafana / cloudwatch）を鍵注入して spawn |
| Agent | `assistants.go` `opsIntegrations` | 上記 3 種のカタログ。`assistant.Integrations` に id を持たせて付与 |
| Agent | `chat_providers.go` `mcpConfigArgs` | claude チャットの `--mcp-config` を組む（af サーバ＋連携） |

### 1.2 穴

- **カタログがコード固定**。`opsIntegrations` に無いサーバーは登録できない（`assistants.go:60`）。
- **対話セッションには MCP 注入経路が一切ない**。`claude/program.go buildProgram` は素の
  `claude --session-id …` を組むだけで、MCP は各 CLI が自前のグローバル設定を読むに任せている。
  利用者が手で `~/.claude.json` や `~/.codex/config.toml` を編集するしかなく、**コンテナ recreate で
  `~/repos` は消えるが home は残る**ため一見残るものの、Console からは何も見えない・管理できない。
- **テナント横断の配布手段が無い**。社内共通の MCP（社内 Wiki、チケット、社内 API）を全員に配るには
  各自が同じ手作業を繰り返すしかない。

---

## 2. 決定サマリ（利用者判断 2026-07-27）

1. **スコープは user + tenant の両方**。実効レジストリ = テナント配布 ∪ 個人登録。
2. **トランスポート**: 個人 = stdio + リモート、**テナント配布 = リモートのみ**。
   リモートは **Streamable HTTP のみ**とする（実装時の絞り込み。理由は §3.1）。
   テナント配布の stdio は「管理者が全員のコンテナで任意コマンドを実行できる」ことと等価なので許可しない。
3. **利用先はアシスタント＋対話セッション（全 kind）**。ただし実装は **claude / codex を先行**させ、
   残り kind（opencode / kiro / cursor / copilot / agy）は後続フェーズ。
4. 設計ドキュメント + ADR を先に置いてから実装する（本ドキュメント）。

---

## 3. データモデル

### 3.1 サーバー定義（agent 側 Go 型 / ワイヤ表現）

実体は `internal/secrets.MCPServer`（user スコープの保存先がそのストアなので、資格情報型と
同じ場所に置く）。`mcpreg.ServerDef` はその型エイリアス。

```go
// workspace/agent/internal/secrets/secrets.go（型）/ internal/mcpreg/def.go（検証・合成）
type MCPServer struct {
    ID        string            `json:"id"`        // 内部 id（uuid 風）
    Name      string            `json:"name"`      // CLI 上のサーバー名。^[a-zA-Z0-9_-]{1,48}$
    Label     string            `json:"label,omitempty"`
    Origin    string            `json:"origin"`    // "user" | "tenant" | "builtin"
    Transport string            `json:"transport"` // "stdio" | "http"

    Command string            `json:"command,omitempty"` // stdio
    Args    []string          `json:"args,omitempty"`
    Env     map[string]string `json:"env,omitempty"`     // 値は秘密扱い

    URL     string            `json:"url,omitempty"`     // http
    Headers map[string]string `json:"headers,omitempty"` // 値は秘密扱い

    Enabled   bool     `json:"enabled"`
    Targets   Targets  `json:"targets"`         // どこへ流すか
    Kinds     []string `json:"kinds,omitempty"` // 空 = 全 kind
    TimeoutMS int      `json:"timeoutMs,omitempty"`

    CreatedAt int64 `json:"createdAt,omitempty"`
    UpdatedAt int64 `json:"updatedAt,omitempty"`
}

type Targets struct {
    Assistant bool `json:"assistant"` // アシスタントチャットで選択可能にする
    Session   bool `json:"session"`   // 対話セッションのネイティブ設定へ materialize
}
```

- `Name` が **各 CLI の設定ファイル上のキー**になる。CLI 側の許容文字が最も狭いところ
  （codex の TOML bare key）に合わせて `[a-zA-Z0-9_-]` に限定する。
- **リモートは Streamable HTTP のみ**。旧来の HTTP+SSE トランスポートは MCP 仕様上 deprecated で、
  接続テスト（§10）を通すだけでも GET ストリームと POST の二経路を持つ別クライアントが要る。
  中途半端に「対応」と書くより落とす方が正しいので、v1 の enum から外した（積み残し §14）。
- `Origin=builtin` は既存 3 種（pagerduty / grafana / cloudwatch）を同じ型に正規化したもの。
  レジストリの中では読み取り専用の行として現れ、`Enabled` だけ利用者が触れる。
  こうすることで `mcpConfigArgs` の分岐が「組み込み or 登録」ではなく **1 本のリスト処理**になる。
  この正規化のおかげで、後から足した **`aws`（Agent Toolkit for AWS — docs/25）** は
  `builtinSpecs` に 1 行と `mcp-run aws` だけで済み、materialize / 名前衝突 / UI は無改修だった。
  `aws` は builtin で唯一 **Targets を assistant + session の両方**に開いている（af はセッション専用、
  ops 3 種はアシスタント専用）。既定の assistant 限定は「レジストリ以前からある連携の挙動を
  勝手に変えない」ための配慮なので、レジストリ以降に生まれた連携には掛からない。

### 3.2 保存先

| Origin | 保存 | 暗号化 | 編集者 |
|--------|------|--------|--------|
| `user` | `~/.config/agent-fleet/secrets.enc`（`secrets.Data.MCP []secrets.MCPServer`） | 既存 AES-GCM（`AF_SECRET_KEY` = ラップ済み DEK） | 本人 |
| `tenant` | CP DB `mcp_server` テーブル | 秘密フィールドのみ tenant KEK でラップ（`custodian`, `dek.go` と同型） | tenant_admin 以上 |
| `builtin` | コード | — | — |

`user` を既存 `secrets.Data` に相乗りさせるのは、**connections（PagerDuty / Grafana …）と同じ
暗号化ストア・同じライフサイクル**（recreate で消えない home 側）に置くため。新しい鍵管理を増やさない。

### 3.3 CP 側テーブル

```sql
-- control-plane/migrations/0028_mcp_server.sql（+ migrations-pg/0011_mcp_server.sql）
CREATE TABLE IF NOT EXISTS mcp_server(
  id          TEXT PRIMARY KEY,
  tenant_id   TEXT NOT NULL,
  name        TEXT NOT NULL,
  label       TEXT NOT NULL DEFAULT '',
  transport   TEXT NOT NULL DEFAULT 'http',
  url         TEXT NOT NULL DEFAULT '',
  headers_enc TEXT NOT NULL DEFAULT '',
  key_ref     TEXT NOT NULL DEFAULT '',
  targets     TEXT NOT NULL DEFAULT 'assistant,session',
  kinds       TEXT NOT NULL DEFAULT '',
  timeout_ms  INTEGER NOT NULL DEFAULT 0,
  enabled     INTEGER NOT NULL DEFAULT 1,
  user_secret INTEGER NOT NULL DEFAULT 0,
  created_by  TEXT NOT NULL DEFAULT '',
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_mcp_server_name ON mcp_server(tenant_id, name)
```

`command` / `args` / `env` 列を**持たない**のが要点で、テナント stdio を「後から足せる」状態に
しないためにスキーマで落とす（決定 2）。マイグレータは `;` で分割するので、コメントに `;` を書かない。

**P4 実装時の 2 点の変更**（設計時のスケッチからの差分）:

- `headers_enc` は `BLOB` ではなく **`TEXT`**。custodian が返すのは base64 文字列で、かつ
  Postgres に `BLOB` 型が無い（`migrations-pg/` は SQLite 版と DDL を同一に保つ方針）。
- `timeout_ms` を**追加**した。落としてあるのは `command` 系だけで、そこがセキュリティ上の
  不変条件（決定 2）。タイムアウトは無害で、無いとテナント配布だけ user スコープより機能が
  劣ることになる。
- 秘密の封は **`custodian.Wrap/Unwrap`（AES-256-GCM・AAD = keyRef）をヘッダ JSON そのものに
  当てる**。名目は DEK エンベロープだが中身は任意バイト列の AEAD で、§3.2 の「秘密フィールドのみ
  tenant KEK でラップ」がそのままこの形。master 鍵が無い環境では **平文 JSON ＋ `key_ref=''`** に
  縮退する（Agent の暗号化ストアと同じ割り切り）。`key_ref` があるのに custodian が無い場合は
  **エラーで返す**（nil 参照でも「ヘッダ無しで配布」でもなく、原因を名指しする）。

**テーブル列 ≠ per-tenant の隔離境界**という点は明示しておく: `localCustodian` の KEK は
master 由来（`custodian.go` に既述）なので、`key_ref` は暗号学的なテナント分離ではない。
実際に別テナントの行へ届かないようにしているのは **store の全文が `tenant_id` を WHERE に持つ**
ことと、配布面が **トークン→membership→tenant で解決し、リクエストの値を一切使わない**ことの
2 点（`mcp_server_test.go` で固定）。

---

## 4. 実効レジストリの合成

```
effective = builtin(接続済みのものだけ) ∪ tenant(enabled) ∪ user
```

- **名前衝突は tenant 優先**。user 側の作成/改名時に、テナント配布と同名なら 409 で弾く
  （後からテナントが同名を配ってきた場合は tenant を採り、user 行に「衝突」印を出す）。
- 利用者はテナント配布行を **編集できないが、自分の Workspace で無効化できる**
  （`~/.config/agent-fleet/mcp-optout.json` に id を記録）。壊れたサーバーが全員のセッション起動を
  巻き込むのを個人が回避できる逃げ道を残す。
- `builtin` は「接続情報が登録済み（`integrationReady`）」のときだけ現れる。現行挙動の踏襲。

---

## 5. 秘密の扱い

### 5.1 原則

- 定義の秘密（`Env` の値、`Headers` の値）は **暗号化ストア / CP DB の中だけ**が正。
- Console へは**返さない**。GET は `"headers": {"Authorization": "***"}` のようにマスクして返し、
  PUT で値が `***` のままなら既存値を保持する（connections の既存作法と同じ）。
- CLI のネイティブ設定へ materialize する時点で**平文になる**。これは避けられない
  （CLI は自分で読める形でないと使えない）ので、ファイルは `0600`、置き場は home 配下に限定する。

### 5.2 テナント配布の秘密（重要な割り切り）

テナント配布のヘッダに社内トークンを入れると、**そのトークンは全メンバーのコンテナ内で平文で読める**。
コンテナは per-user で隔離されているが、「メンバー本人が読める」ことは防げない。

そこで `user_secret` フラグを用意する:

| `user_secret` | テナント側が持つもの | メンバー側 | 使いどころ |
|---------------|----------------------|-----------|-----------|
| `0`（既定） | URL + ヘッダ（トークン込み） | そのまま使える | 社内ネットワーク内の読み取り専用 MCP など、露出しても実害が小さいもの |
| `1` | URL とヘッダ**名**だけ | 値を各自が Console で入力（`secrets.enc` へ） | 個人トークンが要る MCP。テナントは接続先の定義だけ配る |

`user_secret=1` で値が未入力のサーバーは **materialize しない**（起動して失敗するより、出さない方が良い）。
Console にはその旨のバッジを出す。

**P4 実装（`user_secret` の機構は入れた・既定は `0`）**:

- CP 側は `user_secret=1` のとき **入ってくる値をその場で捨てる**（`stripValues`）。出力時だけ
  隠すのでは、フラグを立てる前に管理者が貼ったトークンが DB に残り続け、誰も読まないのに
  漏洩面だけが残る。
- メンバー側の値は `secrets.Data.MCPSecrets`（`サーバー id → ヘッダ名 → 値`）に入る。**テナントが
  配ったヘッダ名しか埋めない**（`withMemberSecrets`）— どのヘッダを送るかはテナントが決める、を
  保つため。ローカルの古い値は無視される。
- 書き込み口は `PUT /mcp-servers/{id}/secrets` の 1 本だけで、**`user_secret=1` のテナント行以外は
  `ErrReadOnly`**。値込みで配られた行の値をメンバーが差し替えられると、テナントが意図した資格情報で
  接続できなくなる。
- `Masked()` は **空の値を `***` にしない**。未入力は「秘密を隠している」のではなく「誰も入れていない」で、
  そこを潰すと *自分が* 値を入れる番だと Console から判別できなくなる（CP 側の `maskHeaders` も同じ規則）。
- ⚠️ 「`user_secret=1` を既定にするか」の**運用方針は依然として未決**（§14-5）。入れたのは機構だけ。

---

## 6. テナント配布の経路

memo ブリッジ / schedule ブリッジ（`control-plane/memo_bridge.go` / `schedule_bridge.go`）と同型にする。

```
tenant_admin ──(Console 管理タブ)──▶ CP /api/admin/mcp-servers        [tenant_admin 以上・監査ログ]
                                        │
Workspace agent ──(AF_MCP_TOKEN)──▶ CP GET /internal/mcp-servers      [membership 解決・tenant scope]
                                        │
                                    ~/.config/agent-fleet/mcp-tenant.json (0600, キャッシュ)
```

- `AF_MCP_TOKEN` は per-membership。`workspace_lifecycle.go` の `AF_MEMO_TOKEN` /
  `AF_SCHEDULE_TOKEN` と同じ mint 方式（membership id + 切り詰め HMAC タグ・`afm_` プレフィクス）で
  注入する（`control-plane/mcp_server_bridge.go`）。**別建てのトークンにする理由が memo / schedule より
  強い**: この応答は `user_secret=0` のテナント秘密を載せるので、漏洩範囲を MCP 定義の読み取りだけに
  閉じたい。テナントは **トークン→membership→tenant** で解決し、リクエスト側の値は一切使わない。
- agent 側の取得契機: ①コンテナ起動時 ②5 分間隔のポーリング ③Console からの明示リフレッシュ
  （`workspace/agent/mcp_tenant.go` が契機だけを持ち、取得本体は `internal/mcpreg/tenant.go`）。
- **CP 到達不能ならキャッシュを使う**（fail-open）。Console には最終取得時刻を出し、stale を可視化する。
  ここを fail-closed にすると CP 瞬断でセッションから MCP が消えるので取らない。
- **中身が変わったときだけ materialize する**。無変更でも `fetchedAt` は書き直す（確認済みなのに
  「古い」と見えるのを避けるため）が、`changed=false` なので CLI 設定は触らない。ここを取り違えると
  claude が絶えず書き換える `.claude.json` を 5 分ごとに踏むことになる（§8.2）。
- **受け取った定義は agent 側でも再検証して、通らないものは落とす**（`acceptTenant`）。ADR0031 決定 2 の
  3 段目で、**コマンドを実際に実行する当のマシン上で走る唯一の検査**。`origin` / `enabled` も正規化する
  （「自分は user 行だ」と偽って読み取り専用の扱いを外せないようにする）。CP が復号できなかった行と
  ここで落とした行は Console にトースト（`mcp.tenant_incomplete`）で件数を出す — 黙って消えると
  「管理者が登録していない」に見える。
- ⚠️ **新 REST は `workspace/agent/routes.go` と `control-plane/routes.go` の両方に登録**する
  （CP は明示許可リスト方式。片方漏れると Console から 404）。`/internal/*` はセッション免除。
  P4 で足したのは admin 4 本（`/api/admin/mcp-servers*`）＋ 配布 1 本（`GET /internal/mcp-servers`）＋
  Agent プロキシ 2 本（`POST /api/mcp-servers/tenant-refresh` / `PUT /api/mcp-servers/{id}/secrets`）。
- ⚠️ **検証は CP と agent で二重に持つ**（別 Go モジュールなのでコードを共有できない）。
  **エラーコードは意図して同一**にしてあり、Console は同じ `err.<code>` カタログで解決する。
  ドリフトは agent 側の再検証で「配られたが使われない」として現れる（落ちるのは CI ではなく実運用なので、
  コード追加・改名時は両方に足すこと）。

---

## 7. 消費側 A — アシスタント（チャット）

`assistant.Integrations []string` を**そのまま流用**し、値の意味を「組み込み連携 id」から
「レジストリ上のサーバー id（builtin id を含む）」へ広げる。既存の保存済みアシスタントは
`"pagerduty"` 等を持っているが、builtin の id をその文字列のままにすれば **移行不要**。

`chatConversation.mcpConfigArgs()`（`chat_providers.go:1546`）を
「id → `ServerDef` 解決 → プロバイダ別のシリアライズ」に作り替える。

| プロバイダ | 注入口 | stdio | remote |
|-----------|--------|-------|--------|
| claude | `--mcp-config <ファイル>` + `--strict-mcp-config` | `{"type":"stdio","command","args","env"}` | `{"type":"http","url","headers"}` |
| codex | `-c mcp_servers.<name>.…`（起動単位）＋ 環境変数 | `command` / `args` / `env_vars`（衝突時のみ `env`） | `url` / `env_http_headers` |
| opencode | チャット用 dir の `opencode.json` `mcp` | `{"type":"local","command":[…],"environment":{}}` | `{"type":"remote","url","headers"}` |
| agy | 隔離 HOME の `config/mcp_config.json`（claude 型）＋ `mcp(<name>/*)` 許可ルール | 同左 | 同左 |
| cursor / copilot | チャット未配線（現状どおり） | — | — |

**秘密を argv へ出さない**（P2 実装で最優先した不変条件）。argv は同一 uid から
`/proc/<pid>/cmdline` で読め、CLI 自身のログにも載り得るため、§5.1 が約束する 0600 ファイルより
弱い置き場になる。したがって:

- claude / opencode / agy は**設定ファイル**（0600）に書く。claude はこれまで `--mcp-config` へ
  JSON 文字列を直に渡していたが、会話ごとの `~/.config/agent-fleet/chat-mcp/<convID>.json` へ
  切り替えた（`--mcp-config` はファイルパスも受ける。**claude 2.1.220 で実測確認**。会話削除時に
  一緒に消す）。書き込みに失敗した場合だけ、秘密を持たないサーバーに絞って従来のインライン JSON
  へ縮退する（af ツールを巻き添えで失わせないため）。
- codex には 1 回の exec に効く設定ファイルが無い（`-c` の argv のみ）ので、**値は環境変数で渡し
  argv には名前だけ**置く。stdio は `env_vars`（codex 自身の env から同名で転送）、リモートは
  `env_http_headers`（ヘッダ名 → af が採番した変数名）。

**制約（実測に基づく）**:

- ⚠️ **「codex にはリモートの任意ヘッダが無い」は誤り**だった（旧 CLI の記述）。**codex-cli 0.145.0
  で実測**: `codex mcp list --json` が streamable_http の `http_headers` / `env_http_headers` を
  往復し、ヘッダ記録用のリスナーへ実際に `codex exec` を当てて両方が wire に載ることを確認した。
  よって **codex 非対応というバッジは不要**（`mcpWire.ts` の `codexUnsupported` は当面残すが、
  リモートヘッダの根拠としては失効）。`codex mcp add` に header 系フラグが無いだけで、設定は持つ。
- **codex の MCP 子プロセスの環境は既定 deny**（実測 0.145.0）。既定で渡るのは
  `HOME / PATH / LANG / LC_ALL / PWD / SHELL / SHLVL / TERM / TZ` の core セットのみで、それ以外は
  `env_vars` か `env` で明示する必要がある。HOME / PATH が core にあるおかげで、組み込み連携の
  `mcp-run` ラッパーは暗号化ストアの鍵 `AF_SECRET_KEY` だけ足せばよい。
- `env_vars` は**同名転送**なので、2 つのサーバーが同じ変数名を別の値で要求すると表現できない。
  その場合だけ後勝ちを避けて `env` の直値（argv）へ縮退する（片方に他方の秘密を渡さないため）。
- **opencode のチャット用 dir は grant 別の共有ディレクトリ**だった（`chat-wd/opencode-<grant>`）。
  サーバー集合がアシスタントごとに違うので、dir キーを `<grant>-<サーバー集合のハッシュ>` にした。
  レジストリのサーバーが 0 件なら従来どおりの `opencode-<grant>`（既存 dir をそのまま使う）。
- agy は MCP 設定が**グローバルのみ**（docs/32）。既存の隔離 HOME 方式に相乗りする。組み込み連携は
  レジストリ上「このバイナリ + `mcp-run <id>`」なので、agy の隔離 HOME 用に解決した exe へ
  差し替えてから渡す。

---

## 8. 消費側 B — 対話セッション（kind 別のネイティブ設定）

セッションは CLI をそのまま起動するので、**各 CLI のグローバル設定ファイルへ af が書き出す**
（materialize）方式を採る。起動フラグ方式（claude `--mcp-config` 等）は
`--strict-mcp-config` を伴い、利用者自身のプロジェクト `.mcp.json` を締め出してしまうため採らない。

**実装済み = 全 kind**（P3 で claude / codex、P5 で opencode / copilot / cursor / kiro / agy）。
契機は `mcp_materialize.go`。codex 以外はどれも「JSON 文書の中のサーバー map 1 つ」なので、
共通エンジン `materialize_json.go`（`jsonConfig`）＋ kind ごとの `materialize_<kind>.go`（ファイル・
map のキー・エントリの綴り方）に分けてある。codex だけが TOML の行編集で例外
（`materialize_codex.go`）。**エージェント CLI を持たない kind（shell / ssm）は `Skipped`** —
書く先が無いのは失敗ではない。

### 8.1 実測した設定契約（claude / codex は 2026-07-27、残りは P5 で 2026-07-28 に実測）

| kind | 版 | ファイル | 形 | 確認方法 |
|------|----|---------|----|---------|
| claude | 2.1.220 | `$CLAUDE_CONFIG_DIR/.claude.json` | `mcpServers.<name> = {type:"stdio",command,args,env}` / `{type:"http",url,headers}` | `claude mcp add -s user` を隔離 `CLAUDE_CONFIG_DIR` で実行し生成物を確認 |
| codex | 0.145.0 | `$CODEX_HOME/config.toml` | `[mcp_servers.<name>] command/args` + `[mcp_servers.<name>.env]` / `url` + `[mcp_servers.<name>.http_headers]` | `codex mcp add` を隔離 `CODEX_HOME` で実行し生成物を確認 |
| copilot | 1.0.75 | `$COPILOT_HOME/mcp-config.json` | `mcpServers.<name> = {tools:["*"],type:"local",command,args,env}` / `{tools:["*"],type:"http",url,headers,timeout}` | `copilot mcp add` を隔離 `COPILOT_HOME` で実行し生成物を確認（0600 で生成される） |
| opencode | 1.18.7 | `~/.config/opencode/opencode.jsonc` | `mcp.<name> = {type:"local",command:[…],environment}` / `{type:"remote",url,headers}` | `opencode mcp add --url/--env/--header` を隔離 HOME で実行し生成物を確認 |
| kiro | 2.14.2 | `~/.kiro/settings/mcp.json` | `mcpServers.<name> = {command,args,env,timeout}` / `{url,headers,timeout}`（`type` 判別子なし） | `kiro-cli mcp add` を実行し生成物を確認（**`mcp` は全サブコマンドがログイン必須**）。ヘッダは実ターンで wire 到達まで確認 |
| cursor | 2026.07.23 | `~/.cursor/mcp.json` | `mcpServers.<name> = {command,args,env,cwd}` / `{url,headers}`（`type` 判別子なし） | **`mcp add` が無い**ので逆向き — af が書いた設定を `cursor-agent mcp list` に読ませ、ヘッダは wire 到達まで確認 |
| agy | — | `~/.gemini/config/mcp_config.json` | claude 型 `mcpServers`（`type` 判別子なし） | docs/32（本ホストは RDRAND 非対応で agy 実行不可・**唯一 drift 検知が置けない kind**） |

**P5 で実測して確定した分**（未確認だった kiro / cursor のリモート形を含む）:

- **kiro のリモートはヘッダを持てる**。`mcp add` に header フラグが無いだけで（codex と同じ話）、
  手書きの `headers` は実際に wire へ載る — ヘッダ記録リスナー相手に `kiro-cli chat --no-interactive`
  を 1 ターン走らせ、`Authorization` と独自ヘッダの到達を確認した。ここが無いと
  **テナント配布サーバーだけが全滅する**（配布はリモート専用・決定 2、認証はヘッダ）。
  `timeout` はミリ秒。`disabled` は既定 false なので af は書かない。
- **cursor のリモートは `{url,headers}`**。`type` を付けても付けなくても `mcp list` は ready を返し、
  独自ヘッダは wire に載った。バンドルのパーサも `"command" in o` / `"url"` で分岐しており、
  読むのは `{command,args,env,cwd}` / `{url,headers}` だけ。**`timeout` は存在しない**ので書かない
  （opencode も同様 — 効かない場所へ書くくらいなら落とす）。
- **opencode は `opencode.jsonc` と `opencode.json` の両方を読んでマージする**。af は
  **実在する方を 1 つだけ**編集する（`.jsonc` 優先 = CLI 自身と entrypoint が作る方）。
  「もう一方」へ書くと同じサーバーが二重に載る。
- **copilot の `timeout` はミリ秒**（codex の `startup_timeout_sec` と違い変換しない）。
  `tools` は per-server のツールフィルタで、`mcp add` の既定は `["*"]`。af も同じ既定を明示する —
  省略時の挙動が未文書で、**外すと「登録したのにツールが 1 つも出ない」に化けうる**唯一のキー。
- **cursor / kiro / agy には `type` 判別子が無い**（`command` か `url` かで決まる）。claude・codex・
  copilot・opencode は持つ。同じ `mcpServers` という名前でも中身の綴りは 3 系統に割れている。

**P3 で再実測して確定した分**（claude 2.1.220 / codex-cli 0.145.0、隔離 HOME での `mcp add` 生成物）:

- claude の user スコープは `$CLAUDE_CONFIG_DIR/.claude.json` の `mcpServers` で確定。
  形はアシスタント用の `ClaudeServers()` と**同一**だったので、シリアライザは 1 本のまま両消費側に使う。
- codex の設定**ファイル**でもリモートの任意ヘッダが使える（`[mcp_servers.<name>.http_headers]`）。
  `codex mcp list --json` が `http_headers` / `startup_timeout_sec` をそのまま読み返すことを確認した。
  `codex mcp add` に header フラグが無いだけで、設定は持つ（§7 の訂正と同じ話）。
- materialize はアシスタント配線と違い、**ヘッダ / env の値を設定ファイルへ直に書く**。
  §5.1 が平文化を許す場所は 0600 ファイルであり、codex に「1 回の exec に効く設定ファイル」が
  無いという §7 の制約はここでは効かない（読むのは CLI 本体で、argv は絡まない）。

### 8.2 書き込み規約

- **既存の利用者手書き設定を壊さない**。af が書いた名前の一覧（kind 別）を
  `~/.config/agent-fleet/mcp-managed.json` に残し、レジストリから消えたサーバーは
  **この一覧にあるものだけ**を削除する。台帳が壊れて読めないときは materialize ごと中止する
  （「af は何も所有していない」と解釈すると、書いた行が誰にも消せない孤児になるため）。
- 例外が 1 つある: **これから書く名前**の既存セクション / キーは、手書きでも置き換える。
  TOML は重複テーブルをエラーにするので、`[mcp_servers.x]` を残したまま追記すると
  **config.toml 全体が読めなくなり codex が起動しなくなる**。MCP が 1 本増えない程度では済まない。
- 書き込みは **read → merge → 一時ファイル → `os.Rename`**（原子的）、モードは `0600`。
- **materialize 全体を 1 本の mutex で直列化する**（P4 で追加）。呼び出し元は複数あり
  （レジストリ CRUD の HTTP ハンドラ・各セッション起動・P4 のテナント 5 分ポーリング）、
  1 パスは設定ファイルと**所有台帳の両方**に対する read-modify-write なので、交錯すると片方の
  書き込みが失われる。失うのが台帳の側だと悪い方に転ぶ — **af が自分の所有を忘れ、その名前は
  誰も消せない孤児になる**（台帳がまさに防いでいる失敗）。kind ごとではなく全体で 1 本なのは、
  kind 間で台帳ファイルを共有しているため。
- **変わっていなければ書かない**。`.claude.json` は claude 自身が絶えず書き換える生の状態ファイル
  （オンボーディング・trust ダイアログ）なので、無変更の起動で再シリアライズすると無駄に整形が変わり、
  こちらの rename が claude の書き込みを踏み潰す窓も広がる。
  比較は**デコード後の構造**で行う（`json.MarshalIndent` のバイト比較だと毎回差分になる）。
- **壊れた設定ファイルは触らない**。`.claude.json` が JSON として読めなければエラーで戻る。
  上書きすると trust ダイアログが飛び、`hasCompletedOnboarding` を失って**認証が通っていても
  ログイン画面が出る**（`internal/agents/claude/settings.go` の教訓）。
- codex の TOML は `internal/agents/codex/settings.go` と同じ**行ベース編集**にする。パースして
  再出力するとコメントと project trust セクションが黙って再整形される。af は自分が所有する
  テーブル（とその下位テーブル）だけを行ごと抜き、末尾に新しいテーブルを足す。
- ⚠️ **この規約は「af が*自動で*書くのは user/global だけ」と読む**（[56](56-project-mcp.md) /
  [ADR0040](decisions/0040-project-mcp.md)）。プロジェクトスコープを**利用者の明示操作のときだけ**
  代理編集する別軸のツールを docs/56 で設計しており、そちらは所有台帳もマーカーも持たない
  （監査証跡は git の差分）。`Materialize` / `MaterializeAll` がプロジェクトスコープへ触れないことは
  変わらず、テストで固定する。
- opencode は本ホストで `.jsonc`。`entrypoint.sh:414` の既存作法（**素の JSON として読めなければ触らない**）を
  踏襲する。P5 の実装では **JSON 設定型の全 kind で同じ規約**にした（`materialize_json.go`）: 読めない
  設定は上書きせずエラーで戻り、`mcp materialize <kind>: … is not plain JSON, leaving it alone` を
  ログに残す。コメント入り `opencode.jsonc` はこの一般則に乗る（claude のオンボーディングフラグを
  守る理由とまったく同じ — 読めない設定は整形し直してはいけない）。

### 8.3 反映タイミング

- **agent 起動時**（`main.go`）。コンテナを起こした直後から効かせる。Console を開かずに
  ターミナルから直接 CLI を叩く経路には、この契機しか無い。
- **セッション起動直前**。tui は `startSessionTmux` の `BuildLaunch` 前フック、managed は
  `startManagedSession`（launch 用の `Resume` ラッパー）。「登録 → 新しいセッションを立てる」が最短で通る。
  ⚠️ managed の `Resume` は**走行中スレッドへの再アタッチにも使われる**（turn 送信・ブリッジ・回答）。
  そちらまで materialize すると 1 メッセージごとにレジストリを読むことになるので、**起動 5 箇所だけ**を
  ラッパー経由にしてある。
- **レジストリ変更時**（CRUD）にも全 kind へ書き出す。テナント配布の取得は P4。
- **既に走っているセッションには効かない**（どの CLI も起動時に設定を読む）。Console に
  「新規セッションから有効」を明示する（`mcp.session_restart_note`）。ここを曖昧にすると
  「登録したのに使えない」の問い合わせになる。
- ⚠️ **managed codex は共有 `codex app-server` に相乗りする**（docs/27）ので、daemon が config を
  プロセス起動時に 1 度しか読まないなら materialize が効かない。**実測（0.145.0）では
  `thread/start` ごとに読み直す**ので、`Supervisor.Restart`（workspace 内の codex 全セッションを
  drain する重い操作）は要らない。この前提はドリフトテストで固定してある
  （`codex/mcp_config_drift_test.go` — 崩れたら「登録したのに managed だけ効かない」になる）。

### 8.4 プロジェクトローカルスコープとの住み分け（実測 2026-08-09）

af が書くのは **user / global スコープ 1 箇所だけ**で、リポジトリ側のプロジェクトスコープは
利用者のものとして触らない。この住み分けが成立するには「両者がマージされること」と
「同名衝突の勝者が分かっていること」の 2 つが要る。実 CLI で測った結果:

| kind | af が書く場所（user/global） | プロジェクトスコープ | ゲート | マージ | 同名衝突の勝者 |
| --- | --- | --- | --- | --- | --- |
| claude | `$CLAUDE_CONFIG_DIR/.claude.json` | `.mcp.json` | 承認（⏸ Pending approval） | ✔ | **user**（＋「Conflicting scopes」警告） |
| codex | `$CODEX_HOME/config.toml` | `.codex/config.toml` | **trust**（`[projects."<dir>"] trust_level="trusted"`） | ✔ | **project** |
| opencode | `~/.config/opencode/opencode.jsonc` | `opencode.json`（リポジトリ直下） | 無し | ✔ | **project** |
| cursor | `~/.cursor/mcp.json` | `.cursor/mcp.json` | 承認（`cursor-agent mcp enable`） | ✔ | **project** |
| copilot | `$COPILOT_HOME/mcp-config.json` | `.mcp.json` / `.github/mcp.json` | 無し（実測） | ✔ | **project** |
| kiro | `~/.kiro/settings/mcp.json` | `.kiro/settings/mcp.json` | 未検証（`mcp` 系が全部ログイン必須） | 未検証 | 未検証 |
| agy | `~/.gemini/config/mcp_config.json` | **無し**（global 専用） | — | — | — |

版: claude 2.1.226 / codex-cli 0.147.0 / opencode 1.18.15 / cursor-agent 2026.08.04 /
Copilot CLI 1.0.78。固定しているテストは `mcpreg/materialize_scope_drift_test.go` と
`codex/drift_same_key_test.go`（codex は app-server が要るので別置き）。

読み取れること:

- **マージは全 kind で成立する。** プロジェクト設定を持つリポジトリでも af のサーバは消えない。
- **`.mcp.json` は claude と copilot が共有する。** 1 つのファイルが 2 kind に効く。
- ⚠️ **claude 以外は、リポジトリのプロジェクト設定が af のサーバ名を定義すると af 自身の
  サーバを乗っ取れる。** そうなるとそのリポジトリのセッションで自己申告・引き継ぎ提案・
  Chromium attach が黙って死ぬ（cursor では承認待ちで完全に使用不能、codex/opencode/copilot では
  別のサーバがその名前で起動する）。§9 の `reservedNames` は **AF レジストリ側でしか効かない** ——
  他人のリポジトリのファイルは止められない。

  **対処: af のサーバ名を起動ごとに振り直す**（`mcpreg/af_server_name.go`）。`af` 固定をやめて
  `af_<8桁hex>` を Agent 起動時に mint し、`AFServerName()` が全 materialize と codex の
  thread 単位 config に配る。偶然の衝突は事実上起きなくなり、万一衝突しても**再起動で外れる**。

  - 変わるのは各 CLI の設定ファイルのキー（＝クライアント側で `mcp__<name>__<tool>` の
    プレフィックスになる部分）だけ。**ツール名は変わらない**（`af_report` 等）し、AF が注入する
    指示もツール名で書いてある。**レジストリ上の ID は `af` のまま**で、af を特定する分岐
    （thread config のセッション名刻印・`extraEnvVars`・Console の注記）は全て ID を見ている。
  - 名前は `$AF_CONFIG/mcp-af-name` に永続化する。**rotate は Agent 起動時の 1 回だけ**で、
    他の経路は読むだけ — 2 プロセスが別々に mint すると設定ファイルと食い違うため。ファイルが
    無い状態で読んだ場合は歴史的な `af` に落ちる（勝手に mint しない）。
  - **掃除は台帳（`mcp-managed.json`）に依存しない。** 台帳は本来「af が書いた名前」だけを
    削除の根拠にする保守的な仕組み（§8.2）だが、名前が毎回変わる以上、台帳を失うと前回の
    エントリを自分のものと認識できず、**起動のたびに生きた `af_xxxxxxxx` が 1 つずつ残る**
    （N 回起動で N 個の MCP 子プロセス）。生成形が `af_` + 8 桁 hex と十分狭いので、
    この形に一致する**現在名以外**は台帳に無くても掃除してよい、という規則を足してある
    （`StaleAFServerName`）。同じ形はユーザー登録側でも予約する。
  - アシスタントチャットの MCP 設定（`chat_providers.go` / `chat_mcp.go`）は**据え置き**。
    あちらは会話ごとの隔離 HOME に書くので、プロジェクトスコープが載る経路ではない。
- codex のプロジェクトスコープは **trust が gate**。`codex mcp list` は user レベルしか表示しない
  （openai/codex#13025）ので、これを調査に使うと「プロジェクトスコープは無い」と誤診する。
  ランタイム側（app-server の `mcpServerStatus/list`）で見ること。
- **プロジェクトスコープ側の追加実測は [56](56-project-mcp.md) §2 にある**（2026-08-09）:
  プレースホルダの方言（claude・**copilot**=`${VAR}` / opencode=`{env:VAR}` / cursor=両方 /
  **codex=展開なし**）と、プロジェクトに書いた直後のゲート（claude・cursor は**承認前に起動すらしない**）。
  `.mcp.json` を共有する claude と copilot は同じ方言なので、あのファイルだけは 2 kind で一貫する。
  上表の「マージ / 勝者」に加えて、**同じ値をそのままコピーしても kind が違えば同じ意味にならない**
  ことがこちらで分かる。
- managed codex が thread 単位 config で送る af エントリは、上表の**さらに上**に乗る。
  優先順位は **thread > project > user**（`TestDriftCodexThreadConfigOverridesProjectConfig`）。
  従って **managed codex セッションだけは乗っ取りに強い** — プロジェクト設定が `af` を定義しても
  thread 設定が勝つ。docs/27 §9.3.1 の副次的効果で、狙って作ったものではない。

---

## 9. 安全弁

| 対象 | 仕掛け |
|------|--------|
| テナント配布の任意コマンド実行 | スキーマから `command` 系を落とす（決定 2）。API も transport=stdio を 400 で拒否 |
| リモート先の外部通信 | egress allowlist（docs/20）。登録時に URL の host を照合し、未許可なら Console に警告＋申請導線（§9.1・P5 実装済み） |
| 権限 | テナント CRUD は tenant_admin 以上。RBAC は**サービス層で再検証**（roadmap の原則） |
| 監査 | `audit_log` に `action=mcp.upsert` / `mcp.delete`、`actor_kind=admin`（MCP 経由なら `mcp`） |
| 秘密の露出 | Console へはマスクして返す。ログ・監査 detail に値を書かない |
| 壊れたサーバー | 接続テスト（§10）で登録時に検出。materialize は enabled かつ必要な秘密が揃ったものだけ |

### 9.1 egress allowlist 連携（P5 実装済み）

リモート MCP サーバーは**外向きの宛先そのもの**なので、egress 統制（docs/20）と噛み合わない
限り「登録できたのに繋がらない」が残る。しかも失敗の出方が悪い:

- **enforce**: proxy が弾く。CLI 側からは「MCP サーバーが壊れている」ようにしか見えない。
- **log-only**: **今日は動く**。運用が enforce へ切り替えた日に、誰も何も変えていないのに壊れる。

どちらも CLI セッションの中では原因が分からないので、**まだ直せる場所＝登録画面**で言う。

**CP に member 面を 2 本足した**（`control-plane/egress_member.go`）。allowlist を持っているのは
CP だけ（proxy は `/internal/egress/policy` をポーリングして受け取る側）なので、判定も申請も
CP でしか成立しない:

| ルート | 認可 | 役割 |
|--------|------|------|
| `GET /api/egress/check?host=…` | 任意のメンバー（`withMembership`） | 宛先ごとに `allowed` / `proposed` ＋ 配備の `mode` |
| `POST /api/egress/propose` | 任意のメンバー（`withMembership`） | 許可の**申請**（`egress_allowlist` に `state=proposed` の行を作る） |

**なぜ member に開けてよいか**: 書き込みは `proposed` しか作れない。有効化は従来どおり
super_admin の `POST /api/admin/egress/allowlist/{id}/state`。M4 のエージェント用ツール
（`propose_allowlist_change`）と同じ「頼めるが通せない」分割で、**申請導線を渡しても
配備の egress は広がらない**。

判定を出すときの原則が 3 つある。どれも「狼少年にしない」ためのもの:

1. **`configured=false` なら何も言わない。** 実際にワークスペースを縛っているのは
   `AF_EGRESS_PROXY_ADDR`（これが設定されたときだけ CP が `http(s)_proxy` をコンテナへ注入する）
   であって、`AF_EGRESS_TOKEN` ではない。docs/20 M2 は**コンテナ配線が既定 OFF** なので、
   ここを取り違えるとほぼ全配備で「存在しない制限」を警告し続けることになる。
2. **答えが無い宛先は「無し」。** 未応答・失敗・未知のホストを「遮断」と推測しない。CP の
   一時不調が全部ポリシー違反に見えてしまう。
3. **申請済みが最優先。** `proposed` が既にあるなら、取れる行動は「待つ」であって「もう一度頼む」
   ではない。申請済み判定は**同じ `newEgressPolicy` で照合**するので、`.example.com` の申請が
   `mcp.example.com` を正しく覆う。

申請側の作法:

- 項目は**ホストか `.suffix`** のみ（スキーム / ポート / パスは 400）。policy はそれらを剥がさない
  ので、受け付けると「保存できたのに永遠に一致しない」という一番たちの悪い壊れ方になる。
- **TLD 丸ごと（`.com`）は拒否**。承認する管理者が列の中からこれを見分ける前提にしない。
- **同じ項目の重複は行を増やさない**。`active` なら「もう許可されている」、`proposed` なら既存を
  返す。`retired`（＝一度却下）からの再申請だけは新しい行を作る — 理由を添えて頼み直せるのが
  却下の意味なので。
- 行は**配備全体（`tenant_id=""`）**。承認の効果が実際に配備全体（`EffectiveAllowlist` はスコープを
  見ない）なので、申請者のテナントを行に載せるとありもしないスコープを約束してしまう。テナントは
  監査行（`egress.propose` / `actor_kind=user`）側に持たせる。

Console 側は判定ロジックを `egressCheck.ts`（純粋関数・`egressCheck.test.ts` で固定）へ、
取得と描画を `EgressNote.tsx` へ分けた。**メンバーの MCP タブとテナント配布の管理画面の両方**に
出す — 配布定義が弾かれる場合はテナント全員分が同時に壊れるので、むしろ管理画面の方が効く。

---

## 10. 接続テスト

`POST /mcp-servers/{id}/test`（agent）で、実際に `initialize` → `tools/list` を投げる:

- stdio: プロセスを spawn して stdio に JSON-RPC。10 秒でタイムアウトし kill。
- remote: `POST <url>`（`Accept: application/json, text/event-stream`、`MCP-Protocol-Version: 2025-06-18`）。
- 返すのは **成否・サーバー名/版・ツール数・ツール名の先頭数件**。ツールの説明文までは返さない（画面が荒れる）。

登録直後にここが green にならないと、利用者は「セッションを立てて初めて壊れているとわかる」ことになる。
このフェーズを P0 に入れるのはそのため。

接続テストは **MCP 2026-07-28（ステートレス版）と 2025-* の両方**を喋る。詳細は
[49](49-mcp-2026-07-28.md) §3.2（`server/discover` 先行 → 旧版 fallback）。

---

## 11. Console UI

### 11.1 「MCP サーバー」タブ（P1 実装済み）

設定モーダルの**接続グループ**に新設（`console/src/features/settings/McpTab.tsx`、section key = `mcp`）。

- **一覧は実効レジストリそのまま**（組み込み ∪ テナント ∪ 個人）。アシスタントとセッションが
  実際に見る集合と 1 対 1 にするため、由来で分けたセクションにはしない。行の由来バッジが
  「ここで何ができるか」を表す＝ user は全 CRUD、tenant は無効化のみ、builtin は無効化のみ
  （接続情報は 運用・監視 タブへ誘導）。
- 行: 名前（mono）/ 表示名 / 由来バッジ / トランスポート / コマンドか URL / 利用先 / 対象エージェント /
  秘密の**キー名だけ**（値は出さない）。`enabled && !ready` の行には「値が未入力」の警告を出す。
- 追加・編集フォーム: stdio（command / args / env）とリモート（URL / ヘッダ）をトランスポートで切り替え。
  秘密は `type="password"` の KV 行で、保存済みの値は `***` として現れ、そのまま送れば維持される（§5.1）。
- 「接続テスト」は**保存前のフォームからも**押せる（§10 の id 解決が保存済み定義とマージするため）。
  結果はサーバー名 / 版 / ツール数 / プロトコル版と、失敗時は stderr / ボディ末尾をそのまま表示する。
- 注記: `targets.session` を選んだフォームには「**新規セッションから有効**」を出す（§8.3）。
  リモート＋ `Authorization` 以外のヘッダには「codex では使えません」を出す（§7）。
- **egress 警告**（§9.1・P5）: リモート行と URL 入力中のフォームに、宛先が許可リストに無いことと
  「許可を申請」を出す（`EgressNote.tsx`）。判定は `egressCheck.ts` の純粋関数で、**未配線・未応答・
  未知ホストは黙る**。同じ部品を管理モーダルの配布フォームにも置く（§11.3）。
- **ワークスペース稼働ゲート必須**。停止中は「登録ゼロ」に見えてしまうため、起動 CTA を出して一覧は描かない。
  取得は CP 502（agent 起動途中）をリトライし、既存スナップショットを空へ落とさない。
- 型・フォーム↔定義の変換・マスク往復は `mcpWire.ts` に分離し、`mcpWire.test.ts` で固定している
  （コンポーネントを起動せずにワイヤ契約だけを回帰できる）。

### 11.2 アシスタントの「MCP サーバー」欄（P2 実装済み）

アシスタント作成/編集ダイアログ（`AssistantModal.tsx`）に追加した。実効レジストリのうち
`targets.assistant` の行だけを 1 リストで出し、チェックで `integrations` に入れる。
P1 以前は**そもそも UI が無く**（組み込み 3 種は SRE アシスタントにコード固定）、利用者が
自分のアシスタントに MCP を付ける手段が無かったので、これは「固定 3 種の置き換え」ではなく新設。

- 実際には接続されない行も**隠さずに出して理由を添える**（無効化中 / 設定が未完了 /
  このエージェントは対象外）。選択自体は残せるので、後から鍵を入れたりスコープを直せば効く。
- 由来（builtin / tenant / user）でセクションを割らないのは §11.1 と同じ理由。

### 11.3 テナント配布の UI（P4 実装済み）

**管理モーダルの「MCP 配布」モード**（`AdminTab.tsx` の `McpAdminView`、mode key = `mcp`）。
テナントを選んで配布中の一覧・追加・編集・削除。`tenantAdminFor` でハンドラ内ゲートなので、
super_admin は全テナント、tenant_admin は自分のテナントだけが見える。

- フォームは**リモート専用の別シェイプ**（`mcpWire.ts` の `TenantForm` / `bodyOfTenant`）にした。
  member 用 `Form` からフィールドを隠す作りにすると「stdio を作れてしまう状態」が理屈上残る。
  `transport` はフォームから取らず `"http"` 固定で、`bodyOfTenant` は `command` / `args` / `env` を
  **そもそも出力しない**（`mcpWire.test.ts` で固定）。
- 「認証の値は各メンバーが入力する」トグル（`user_secret`）を入れると値の入力欄が**消える**。
  無効化して無視するのではなく消すのは、そこに入れた値がどこにも保存されないため。
- 削除は「各メンバーのワークスペースからは次回の取得時に消えます」と明示する（即時ではない）。
- **egress 警告はここにも出す**（§9.1）。配布定義が proxy に弾かれる場合、壊れるのは
  テナント全員のセッションなので、メンバータブより先に気付ける場所がここになる。

**メンバー側タブ（`McpTab.tsx`）の追随**:

- テナント配布の**最終取得時刻＋明示リフレッシュ**（fail-open が「黙って古い」にならないための唯一の手掛かり）。
  取得したことが一度も無い環境（ブリッジ未設定）では行そのものを出さない — 説明の無い「未取得」は障害に見える。
- `user_secret` 行には「値を入力」ボタンと、ヘッダ**名だけ固定**の値入力フォーム（`SecretsForm`）。
- `enabled && !ready` の警告を 2 通りに割った: **自分が値を入れれば直る**（`mcp.needs_member_secrets`）か、
  それ以外（従来の `mcp.not_ready`）か。前者は行動可能なので同じ文言にしてはいけない。

**i18n 必須**: `ja` / `en` 両方に文言を追加する。裸和文の AST lint が CI で落とすので、文字列は必ず `tr()` 経由。
サーバー側の拒否理由も **1 理由 = 1 コード**（`mcpreg.ValidationError.Code` / CP の `codeMCP*`）にして
`err.<code>` カタログで解決する（Go 側の message は言語非依存の developer fallback）。
色トークンの追加は不要（新しい agent kind ではない）。

---

## 12. フェーズ計画

| P | 内容 | 主な触り所 |
|---|------|-----------|
| **P0** ✅ | 型・実効レジストリ合成・user CRUD REST・接続テスト。builtin 3 種を `ServerDef` へ正規化 | `internal/mcpreg/`（新設）、`secrets.go`、`assistants.go`、`routes.go`（agent + CP 両方） |
| **P1** ✅ | Console 「MCP サーバー」タブ（実効レジストリ一覧＋ user CRUD＋接続テスト）＋ i18n（ja/en）＋検証コード化 | `McpTab.tsx`、`mcpWire.ts`、`SettingsDialog.tsx`、`settings.css`、i18n、`mcpreg/def.go` |
| **P2** ✅ | アシスタント配線（claude / codex / opencode / agy）。`mcpConfigArgs` の一般化＋アシスタント編集 UI | `mcpreg/attach.go`（新設）、`chat_mcp.go`（新設）、`chat_providers.go`、`AssistantModal.tsx` |
| **P3** ✅ | セッション materialize — **claude / codex 先行**（所有台帳・非破壊書き込み・起動/CRUD 契機・drift CI） | `mcpreg/materialize*.go`（新設）、`mcp_materialize.go`（新設）、`session_tmux.go`、`paths.go`、`.github/workflows/mcp-config-contract.yml`（新設） |
| **P4** ✅ | テナントスコープ: CP テーブル・管理 API・ブリッジ・配布キャッシュ・`AdminTab` UI・`user_secret` | `control-plane/mcp_server.go` / `mcp_server_bridge.go` / `migrations/0028` + `migrations-pg/0011`（新設）、`store.go`・`store_sqlite.go`・`routes.go`・`workspace_lifecycle.go`、`mcpreg/tenant.go`・`mcp_tenant.go`（新設）、`AdminTab.tsx`・`McpTab.tsx`・`mcpWire.ts` |
| **P5** ✅ | 残り kind の materialize（opencode / copilot / cursor / kiro / agy）＋ egress allowlist 連携（§9.1） | `mcpreg/materialize_json.go`・`materialize_{opencode,copilot,cursor,kiro,agy}.go`（新設）、`paths.go`、`materialize_drift_test.go`、`control-plane/egress_member.go`（新設）・`egress.go`・`main.go`・`routes.go`、`console/.../egressCheck.ts`・`EgressNote.tsx`（新設）・`McpTab.tsx`・`AdminTab.tsx` |

P0〜P3 で「個人が登録して claude / codex で使う」が閉じる。P4 で組織配布、P5 で全 kind と
egress 連携。**v1 の計画分はこれで全て入った**（未決は §14）。

---

## 13. 検証計画

- **unit**: 実効レジストリの合成（優先順・衝突・opt-out）、各 kind のシリアライズ（黄金ファイル比較）、
  マスク往復（`***` で既存値保持）、テナント stdio 拒否、name のバリデーション。
  Console 側は `mcpWire.test.ts`（name 規則・マスク往復・トランスポート別に片側だけ送る）。
  **UI は headless Chromium ＋素の CDP で実描画を確認**する
  （`console/scripts/shots/server.mjs` の `/api/mcp-servers` スタブが由来 3 種を返す）。
- **materialize の非破壊性**: 利用者手書きのキーを持つ既存設定に対して書き→消しを往復させ、
  **手書き分が残り af 分だけ消える**ことを検証する。opencode のコメント入り設定は skip されること。
- **drift 検知**: 各 CLI の設定契約は版で壊れる（`false-idle-reverse-heal` の教訓）。
  §8.1 の各形式について、`<cli> mcp add` を隔離 HOME で実行して生成物を比較する drift テストを置く
  （codex の既存 `drift_test.go` と同じ作法）。**契約が変わったら赤くする**のが目的。
- **実機**: 実 MCP サーバー 1 本（stdio 1 / remote 1）を登録し、アシスタントと claude/codex セッションの
  双方でツールが見えることを確認する。

**P2 で済ませた分**（`internal/mcpreg/attach_test.go`）:

- 各プロバイダのキー名を固定（claude の `type`、opencode の `command` 配列と `environment`、
  agy が `type` を持たないこと、overlay と定義値の優先順）。
- **秘密が argv に出ないこと**を独立した assertion にした（codex の args を全結合して秘密文字列を
  検索する）。ここは仕様というより本実装の不変条件なので、壊れたら赤くなる形にしておく。
- `env_vars` 同名衝突 → `env` 直値への縮退。同名**同値**なら縮退しないことも合わせて。
- 実 CLI での確認（本コンテナの焼き込み版）:
  **claude 2.1.220** = `--mcp-config <ファイル>` に `type:"stdio"` / `type:"http"` を渡して
  両サーバーが登録され、リモートのヘッダが wire に載ることをヘッダ記録リスナーで確認。
  **codex-cli 0.145.0** = `env_vars` / `env` / `env_http_headers` / `http_headers` の挙動を
  実 exec で確認（子プロセス env の core セット、ヘッダ到達）。
- **未検証**: アシスタント編集ダイアログの MCP 欄はブラウザで実操作していない
  （tsc / vitest / i18n lint / 本番ビルドは通っている）。opencode / agy チャットでの実ターンも未実施。

**P3 で済ませた分**（`internal/mcpreg/materialize_test.go` / `materialize_drift_test.go` /
`internal/agents/codex/mcp_config_drift_test.go`）:

- **非破壊性**: 利用者手書きの `.claude.json`（オンボーディング・trust・自前 `mcp add` 分）と
  `config.toml`（コメント・`[projects."…"]`・自前 `[mcp_servers.mine]`）に対し、書き→消しを往復させ、
  **codex は元ファイルとバイト同一に戻る**ことまで固定した。claude は「手書き分だけが残る」を確認。
- **冪等性**: 同じ定義で 2 回目を走らせると `changed=false`（ファイルを触らない）。
- 壊れた `.claude.json` を上書きしないこと。同名テーブルを 1 つに畳むこと（TOML 重複回避）。
  ヘッダ名に `.` を含むときクオートすること（さもないと入れ子テーブルになり別ヘッダになる）。
- 台帳が壊れたら materialize を続行しないこと。`targets.session` / `enabled` / `kinds` の絞り込み。
- **drift（実 CLI）**: `<cli> mcp add` に生成させた設定と af の materialize 結果を**構造比較**する
  （期待値を手で書き写さない）。claude は stdio / remote の 2 形、codex は stdio を `mcp add` と比較し、
  リモートは `codex mcp list --json` に af の出力を読み返させて `http_headers` /
  `startup_timeout_sec` を確認。加えて **app-server の config リロード契約**（§8.3）。
  CI は `.github/workflows/mcp-config-contract.yml`（pinned × latest のマトリクス、認証・課金なし）。
- **未検証**: Console からの実操作、実 MCP サーバーを登録しての claude / codex セッション実起動。
  CI ワークフロー自体の実行（GitHub Actions の支払い停止中）。

**P5 で済ませた分**（`internal/mcpreg/materialize_json_test.go` / `materialize_drift_test.go`）:

- **非破壊性・冪等性・0600・読めない設定は触らない**を、P5 の 5 kind へ横断で当てた（表駆動）。
  共通エンジンを通るので中身は同じ検証だが、**書く先のファイルと map のキーが kind ごとに違う**のが
  このフェーズの実体で、取り違えると「登録したのに何も起きない」（別ファイルへ書いた）か
  「利用者の設定を壊した」になる。加えて **書くものが無い kind は設定ファイルを作らない**こと
  （使っていない CLI のホームに af の痕跡を増やさない）。
- **エントリの形**は kind ごとに個別テスト: opencode の `command` 配列、copilot の `tools:["*"]` と
  ミリ秒 `timeout`、kiro の `type` 無し＋ヘッダ、cursor の `timeout` を**書かない**こと。
- **opencode のファイル選択**: `.jsonc` / `.json` のどちらが在るかで編集先が決まること（両方在れば
  `.jsonc`、無ければ `.jsonc` を作る）。opencode は両方読んでマージするので、ここを外すと二重登録になる。
- **drift（実 CLI・build tag `drift`）**: opencode / copilot は `mcp add` の生成物と構造比較。
  **kiro は `mcp` 全サブコマンドがログイン必須**なので、CLI 側にだけ実 HOME の資格を渡し、書き込みは
  CWD 配下の workspace スコープへ逃がす（開発者のグローバル設定を触らない）。未ログインなら skip。
  **cursor は `mcp add` を持たない**ので参照は逆向き — af の書いた `~/.cursor/mcp.json` を
  `cursor-agent mcp list` に読ませ、両サーバーが名前で出ることを見る。
- **未検証**: agy（このホストでは起動不能）。実 MCP サーバーを登録しての 5 kind のセッション実起動。

**P5 egress 連携で済ませた分**（`control-plane/egress_member_test.go` /
`console/.../egressCheck.test.ts`）:

- **判定は proxy と同じ policy**（製品既定 ∪ `active` 行）であること。`.suffix` の申請が
  サブドメインを覆うこと、`retired` 行が許可にも申請中にも見えないこと、enforce の切替が
  **文言だけを変えて判定を変えない**こと。
- **`configured` は proxy 配線に従う**（`AF_EGRESS_PROXY_ADDR` 未設定なら `false`）。ここが
  トークン依存になっていると、egress を配線していない配備で全リモート MCP に警告が出る。
- **member の書き込みは有効化しない**: `propose` の後で `EffectiveAllowlist` に出ないこと、
  行が `proposed` かつ `tenant_id=""` で、テナントは監査行（`actor_kind=user`）側に載ること。
- **重複を積まない**: 同一項目の再申請は既存を返す（`active` なら「もう許可済み」）、`retired`
  からの再申請だけが新しい行になること。
- **形の悪い項目は 1 つも保存しない**: URL / ポート付き / パス付き / 空白入り / `..` は 400、
  TLD 丸ごと（`.com`・`*.com`）は別コードで 400、`*.example.com` は `.example.com` へ正規化。
- **Console 側は「黙る条件」を固定**（`egressCheck.test.ts`）: 未配線・未応答・未知ホスト・許可済みは
  すべて「何も言わない」。申請中は遮断表示より優先。URL→host の抽出（大小・ポート・IPv6・
  スキーム無しの入力途中）も込み。
- **実描画**（headless Chromium ＋素の CDP、`shots/server.mjs` に `/api/egress/check` スタブを追加）:
  log-only での警告文＋「許可を申請」→ 理由が前埋めされた申請フォーム → 申請済み表示、の 3 状態を
  MCP タブ上で目視確認。
- **未検証**: 実 proxy を立てての end-to-end（enforce で実際に弾かれる／承認後に通る）、
  管理モーダル側の実描画、CP 実配備での 2 ルートの認可挙動。

**P4 で済ませた分**（`control-plane/mcp_server_test.go` / `internal/mcpreg/tenant_test.go` /
`console/.../mcpWire.test.ts`）:

- **決定 2 の 3 段**を別々に固定した: CP の API が `transport=stdio` を `mcp_tenant_stdio` で 400、
  Console の `bodyOfTenant` が `command` / `args` / `env` を出力しない、agent の `acceptTenant` が
  stdio と「http なのにコマンドを持つ」定義を落とす。3 つ目が**コマンドを実行する当のマシン上の検査**。
- **テナント隔離**: `mcp_server` の get / list / update / delete を別テナント id で叩き、見えない・
  改名できない・消せないことを確認（id は Console とメンバーのキャッシュに載るので秘密ではなく、
  隔離しているのは `tenant_id` の WHERE）。配布面も membership の tenant だけを返すことを確認。
- **秘密の往復**: `***` で保存済みが維持される / 省略が削除になる / **保存先の無い `***` は捨てる**
  （生の `"***"` を資格情報として送らないため）/ `user_secret` は値を**その場で捨てる**（隠すだけでは
  DB に残る）/ 空の値は `***` にしない。封は AAD が keyRef なので別テナント鍵では開かないこと、
  master 鍵の無い環境では平文へ縮退すること、**封済みなのに custodian が無い**場合はエラーになること
  （nil 参照でも「ヘッダ無しで配布」でもない）。
- **復号できない行は配布しない**（件数だけ返す）。ヘッダ無しで配ると、メンバーが鍵設定ではなく
  MCP サーバー自体を疑うことになる。
- **fail-open と冪等**: 到達不能な CP でキャッシュが残る / 同内容の再取得は `changed=false` で
  CLI 設定を触らないが `fetchedAt` は前進する / ブリッジ未設定ではキャッシュファイルすら作らない。
- **トークン**: membership ごとに決定的（毎回の注入が冪等）、改竄・別 master 鍵・**schedule トークンの
  流用**を拒否。
- **未検証**: 管理モーダルとメンバータブのブラウザ実操作、実 MCP サーバーを配布しての実セッション、
  Postgres バックエンドでのマイグレーション適用（DDL は SQLite 版と同一・SQLite で実行済み）。

---

## 14. 未決 / 積み残し

1. **OAuth を要する MCP**。claude / codex / opencode はいずれも `mcp login` を持つが、af からは駆動しない。
   v1 は「利用者が自分でターミナルから `login` する」。将来 Console から叩けるようにするか。
2. **ツール単位の許可**。agy は `mcp(<server>/<tool>)`、copilot は `--allow-tool`/`--deny-tool` を持つ。
   サーバー単位より細かい制御を入れるか。
3. **使用量計上**（docs/46 残 P5）。MCP ツール呼び出しのトークンを台帳のどのバケットに入れるか。
4. **オペレーター MCP からの登録**。`mcp_stdio.go` / CP `mcp.go` に `list_mcp_servers` 等を出すか（v1 は出さない）。
5. **テナント配布の秘密がコンテナ内で平文になる**件（§5.2）。**機構は P4 で入った**（`user_secret`）が、
   `1` を既定にするか、運用ガイドで「露出前提のトークンだけ配る」とするかは依然として未決。
6. ~~kiro / cursor のリモート設定形が未確認~~ → **P5 で実機確定**（§8.1）。残る穴は **agy だけ**で、
   このホストでは agy が起動できない（RDRAND 非対応）ため drift 検知の層が置けない。RDRAND のある
   ホストで一度当てるまでは、agy の設定形は「docs/32 とチャット経路の実績」に依存したままになる。
