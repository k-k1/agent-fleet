# 48. ユーザー / テナント独自 MCP サーバーの登録（MCP レジストリ）— 設計

- 状態: **◐ P0 実装済み**（型・実効レジストリ合成・user スコープ CRUD・接続テスト）。
  P1 以降は未着手。意思決定は [decisions/0031](decisions/0031-mcp-registry.md)。
- 関連: docs/19（アシスタント）/ [25](25-ops-monitoring.md)（組み込み ops 連携 = 本設計が一般化する対象）/
  [20](20-container-audit-egress.md)（egress allowlist）/ [46](46-usage-accounting.md)（残 P5 = MCP の使用量計上）/
  [27](27-agent-managed-driver.md) §codex thread MCP / [32](32-agy-agent-kind.md) §MCP / [43](43-kiro-agent-kind.md) §2.6

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
-- control-plane/migrations/0028_mcp_server.sql
CREATE TABLE IF NOT EXISTS mcp_server(
  id          TEXT PRIMARY KEY,
  tenant_id   TEXT NOT NULL,
  name        TEXT NOT NULL,
  label       TEXT NOT NULL DEFAULT '',
  transport   TEXT NOT NULL DEFAULT 'http',
  url         TEXT NOT NULL DEFAULT '',
  headers_enc BLOB,
  key_ref     TEXT NOT NULL DEFAULT '',
  targets     TEXT NOT NULL DEFAULT 'assistant,session',
  kinds       TEXT NOT NULL DEFAULT '',
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
  `AF_SCHEDULE_TOKEN` と同じ mint 方式（membership id + 切り詰め HMAC タグ）で注入する。
- agent 側の取得契機: ①コンテナ起動時 ②5 分間隔のポーリング ③Console からの明示リフレッシュ。
- **CP 到達不能ならキャッシュを使う**（fail-open）。Console には最終取得時刻を出し、stale を可視化する。
  ここを fail-closed にすると CP 瞬断でセッションから MCP が消えるので取らない。
- ⚠️ **新 REST は `workspace/agent/routes.go` と `control-plane/routes.go` の両方に登録**する
  （CP は明示許可リスト方式。片方漏れると Console から 404）。`/internal/*` はセッション免除。

---

## 7. 消費側 A — アシスタント（チャット）

`assistant.Integrations []string` を**そのまま流用**し、値の意味を「組み込み連携 id」から
「レジストリ上のサーバー id（builtin id を含む）」へ広げる。既存の保存済みアシスタントは
`"pagerduty"` 等を持っているが、builtin の id をその文字列のままにすれば **移行不要**。

`chatConversation.mcpConfigArgs()`（`chat_providers.go:1546`）を
「id → `ServerDef` 解決 → プロバイダ別のシリアライズ」に作り替える。

| プロバイダ | 注入口 | stdio | remote |
|-----------|--------|-------|--------|
| claude | `--mcp-config <json>` + `--strict-mcp-config` | `{"type":"stdio","command","args","env"}` | `{"type":"http","url","headers"}` |
| codex | `-c mcp_servers.<name>.…`（起動単位） | `command` / `args` / `env` | `url` / `bearer_token_env_var` |
| opencode | チャット用 dir の `opencode.json` `mcp` | `{"type":"local","command":[…],"environment":{}}` | `{"type":"remote","url","headers"}` |
| agy | 隔離 HOME の `config/mcp_config.json`（claude 型）＋ `mcp(<name>/*)` 許可ルール | 同左 | 同左 |
| cursor / copilot | チャット未配線（現状どおり） | — | — |

**制約（実測に基づく）**:

- **codex にはリモートの任意ヘッダが無い**。`--bearer-token-env-var` 相当（`bearer_token_env_var`）だけ。
  `Authorization: Bearer …` 以外のヘッダを持つサーバーは codex では使えないので、Console で
  「このサーバーは codex 非対応」と明示する。Bearer の場合は af が env 名を採番して注入する。
- **opencode のチャット用 dir は現在 grant 別の共有ディレクトリ**（`chat-wd/opencode-<grant>`,
  `chat_providers.go:632`）。サーバー集合がアシスタントごとに違うので、dir キーを
  `<grant>-<サーバー集合のハッシュ>` に変える必要がある。
- agy は MCP 設定が**グローバルのみ**（docs/32）。既存の隔離 HOME 方式に相乗りする。

---

## 8. 消費側 B — 対話セッション（kind 別のネイティブ設定）

セッションは CLI をそのまま起動するので、**各 CLI のグローバル設定ファイルへ af が書き出す**
（materialize）方式を採る。起動フラグ方式（claude `--mcp-config` 等）は
`--strict-mcp-config` を伴い、利用者自身のプロジェクト `.mcp.json` を締め出してしまうため採らない。

### 8.1 実測した設定契約（2026-07-27・本コンテナの焼き込み版）

| kind | 版 | ファイル | 形 | 確認方法 |
|------|----|---------|----|---------|
| claude | 2.1.220 | `$CLAUDE_CONFIG_DIR/.claude.json` | `mcpServers.<name> = {type:"stdio",command,args,env}` / `{type:"http",url,headers}` | `claude mcp add -s user` を隔離 `CLAUDE_CONFIG_DIR` で実行し生成物を確認 |
| codex | 0.145.0 | `$CODEX_HOME/config.toml` | `[mcp_servers.<name>] command/args` + `[mcp_servers.<name>.env]` / `url` + `bearer_token_env_var` | `codex mcp add` を隔離 `CODEX_HOME` で実行し生成物を確認 |
| copilot | 1.0.75 | `$COPILOT_HOME/mcp-config.json` | `mcpServers.<name> = {type:"local",command,args,tools:["*"]}` / `{type:"http",url,headers,tools:["*"]}` | `copilot mcp add` を隔離 `COPILOT_HOME` で実行し生成物を確認 |
| opencode | 1.18.5 | `~/.config/opencode/opencode.jsonc` | `mcp.<name> = {type:"local",command:[…],environment,enabled}` / `{type:"remote",url,headers,enabled}` | 既存 af コード（`chat_providers.go:645`）＋ `opencode mcp add --url/--env/--header` のオプション |
| kiro | 2.14.2 | `~/.kiro/settings/mcp.json` | `mcpServers.<name>`（command/args/env/timeout/disabled、`--url` で HTTP） | `kiro-cli mcp add --help`（**実生成物は未確認 — `mcp add` がログイン必須**） |
| cursor | 2026.07.23 | `~/.cursor/mcp.json` | `mcpServers.<name>`（`command` の有無で stdio 判定。バンドル内に `.cursor/mcp.json` と `"command" in o` の分岐を確認） | CLI バンドルの静的確認（**リモート形は未確認**） |
| agy | — | `~/.gemini/config/mcp_config.json` | claude 型 `mcpServers` | docs/32（本ホストは RDRAND 非対応で agy 実行不可） |

未確認の 2 件（kiro のリモート形、cursor のリモート形）は **実装フェーズで実機確認してから配線する**。
推測で書かない。

### 8.2 書き込み規約

- **既存の利用者手書き設定を壊さない**。af が書いた行だけを識別できるように、各サーバー名を
  そのまま鍵にしつつ、`~/.config/agent-fleet/mcp-managed.json` に「af が書いた名前の一覧（kind 別）」を
  残す。レジストリから消えたサーバーは、この一覧にあるものだけを削除する。
- 書き込みは **read → merge → 一時ファイル → `os.Rename`**（原子的）。既存キーは保存する。
  codex の TOML は `internal/agents/codex/settings.go` の既存編集器（コメント・trust セクション保存）に合わせる。
- opencode は本ホストで `.jsonc`。`entrypoint.sh:414` の既存作法（**素の JSON として読めなければ触らない**）を
  踏襲する。コメント入りは skip し、Console に「opencode の設定にコメントがあるため反映できません」と出す。
- ファイルモードは `0600`。

### 8.3 反映タイミング

- **セッション起動直前**に materialize する（`startSessionTmux` の `BuildLaunch` 前フック）。
  こうすると「登録 → 新しいセッションを立てる」が最短で通る。
- レジストリ変更時（CRUD / テナント配布の取得）にも全 kind へ書き出す。
- **既に走っているセッションには効かない**（どの CLI も起動時に設定を読む）。Console に
  「新規セッションから有効」を明示する。ここを曖昧にすると「登録したのに使えない」の問い合わせになる。

---

## 9. 安全弁

| 対象 | 仕掛け |
|------|--------|
| テナント配布の任意コマンド実行 | スキーマから `command` 系を落とす（決定 2）。API も transport=stdio を 400 で拒否 |
| リモート先の外部通信 | egress allowlist（docs/20）。登録時に URL の host を照合し、未許可なら Console に警告＋申請導線（`egress_allowlist` の `proposed` 行を作る既存経路を再利用） |
| 権限 | テナント CRUD は tenant_admin 以上。RBAC は**サービス層で再検証**（roadmap の原則） |
| 監査 | `audit_log` に `action=mcp.upsert` / `mcp.delete`、`actor_kind=admin`（MCP 経由なら `mcp`） |
| 秘密の露出 | Console へはマスクして返す。ログ・監査 detail に値を書かない |
| 壊れたサーバー | 接続テスト（§10）で登録時に検出。materialize は enabled かつ必要な秘密が揃ったものだけ |

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

- 設定モーダルの**接続グループに「MCP サーバー」タブを新設**（`console/src/features/settings/McpTab.tsx`）。
  - 一覧: 名前 / 由来バッジ（テナント・個人・組み込み）/ トランスポート / 利用先チップ（アシスタント・セッション）
    / 有効トグル / 最終テスト結果。
  - 追加・編集フォーム: stdio（command / args / env）とリモート（URL / ヘッダ）を切り替え。
    テナント行は読み取り専用＋「無効化」のみ。
  - 「接続テスト」ボタン（§10）。
- 管理モーダル（`AdminTab.tsx`）に**テナント MCP セクション**を追加（tenant_admin 以上に表示）。
- アシスタント編集ダイアログの「連携」欄が、固定 3 種からレジストリ由来の一覧に変わる。
- **i18n 必須**: `ja` / `en` 両方に文言を追加する。裸和文の AST lint が CI で落とすので、文字列は必ず `tr()` 経由。
- 色トークンの追加は不要（新しい agent kind ではない）。

---

## 12. フェーズ計画

| P | 内容 | 主な触り所 |
|---|------|-----------|
| **P0** ✅ | 型・実効レジストリ合成・user CRUD REST・接続テスト。builtin 3 種を `ServerDef` へ正規化 | `internal/mcpreg/`（新設）、`secrets.go`、`assistants.go`、`routes.go`（agent + CP 両方） |
| **P1** | Console 「MCP サーバー」タブ（user スコープ）＋ i18n | `McpTab.tsx`、`SettingsDialog.tsx`、i18n |
| **P2** | アシスタント配線（claude / codex / opencode / agy）。`mcpConfigArgs` の一般化 | `chat_providers.go` |
| **P3** | セッション materialize — **claude / codex 先行** | `internal/mcpreg/materialize_*.go`、`startSessionTmux` |
| **P4** | テナントスコープ: CP テーブル・管理 API・ブリッジ・配布キャッシュ・`AdminTab` UI・`user_secret` | `control-plane/mcp_server.go`（新設）、`workspace_lifecycle.go`、`AdminTab.tsx` |
| **P5** | 残り kind の materialize（opencode / kiro / cursor / copilot / agy）＋ egress allowlist 連携 | 同上 + `egress.go` |

P0〜P3 で「個人が登録して claude / codex で使う」が閉じる。P4 で組織配布、P5 で全 kind。

---

## 13. 検証計画

- **unit**: 実効レジストリの合成（優先順・衝突・opt-out）、各 kind のシリアライズ（黄金ファイル比較）、
  マスク往復（`***` で既存値保持）、テナント stdio 拒否、name のバリデーション。
- **materialize の非破壊性**: 利用者手書きのキーを持つ既存設定に対して書き→消しを往復させ、
  **手書き分が残り af 分だけ消える**ことを検証する。opencode のコメント入り設定は skip されること。
- **drift 検知**: 各 CLI の設定契約は版で壊れる（`false-idle-reverse-heal` の教訓）。
  §8.1 の各形式について、`<cli> mcp add` を隔離 HOME で実行して生成物を比較する drift テストを置く
  （codex の既存 `drift_test.go` と同じ作法）。**契約が変わったら赤くする**のが目的。
- **実機**: 実 MCP サーバー 1 本（stdio 1 / remote 1）を登録し、アシスタントと claude/codex セッションの
  双方でツールが見えることを確認する。

---

## 14. 未決 / 積み残し

1. **OAuth を要する MCP**。claude / codex / opencode はいずれも `mcp login` を持つが、af からは駆動しない。
   v1 は「利用者が自分でターミナルから `login` する」。将来 Console から叩けるようにするか。
2. **ツール単位の許可**。agy は `mcp(<server>/<tool>)`、copilot は `--allow-tool`/`--deny-tool` を持つ。
   サーバー単位より細かい制御を入れるか。
3. **使用量計上**（docs/46 残 P5）。MCP ツール呼び出しのトークンを台帳のどのバケットに入れるか。
4. **オペレーター MCP からの登録**。`mcp_stdio.go` / CP `mcp.go` に `list_mcp_servers` 等を出すか（v1 は出さない）。
5. **テナント配布の秘密がコンテナ内で平文になる**件（§5.2）。`user_secret=1` を既定にするか、
   運用ガイドで「露出前提のトークンだけ配る」とするか。
6. kiro / cursor の**リモート設定形が未確認**（§8.1）。P5 着手時に実機で確定させる。
