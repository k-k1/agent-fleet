# P3-6 実装プラン — MCP（管理面 + 作業面を一体・E が主目的）

> ✅ **段1 完了（実装・ライブ検証済）** — 設計確定は [decisions/0006](../decisions/0006-mcp-unified.ja.md)、フェーズ要約は [roadmap P3-6](../roadmap.md#p3-6-mcp-による-agent-fleet-制御管理面--作業面を一体で)。現状は [HANDOFF](../HANDOFF.md) を正とする。
>
> **実装（段1）**: CP（migration 0006 / PAT store+API / `/mcp` Streamable HTTP / member-drive 4 ツール）・Agent（`/sessions/{name}/input|status|output`）・Console（設定→MCP タブで PAT 発行/失効）。run-dev.sh が `AF_MCP_ENABLED` を渡す。
> **ライブ E2E green（2026-06-29, 運用者デプロイ）**: 運用者が write PAT 発行 → `/mcp` に Bearer 接続 → `list_my_sessions` で自分のセッション一覧 → `send_to_session`→`get_session_status`(working→idle)→`get_session_output` で **遠隔 claude を駆動し応答取得**（"PONG"）。**2 セッション並行駆動も確認**（ALPHA / BETA を別個に取得＝フリート駆動）。revoke→401。isolated dev E2E（別ポート+temp DB）で scope フィルタ（write=4・read=3、send は read で unauthorized）/ AF_MCP_ENABLED ゲート（無効→404）も green。
> **⚠️ 実装中に潰した点**: `send-keys -t =claude_x` は **target-pane では `=` 接頭辞をリテラル解釈**して `can't find pane` で失敗 → `list-panes` で**アクティブ pane id（%N）を解決して send-keys** する方式に修正（`sessionPaneID`）。`has-session`/`list-panes` は target-session ゆえ `=` でよい。

CP に `/mcp`（Streamable HTTP）を 1 本生やし、PAT で identity+membership+role を解決して
**member（自分の遠隔セッション駆動＝E）と admin（fleet 運用）を role で出し分ける**。
**段 1 で E を動かす**のが本プランの主眼。新ロジックを足さない薄いラッパに保つ。

## P3-6.0 確定スコープ（[0006](../decisions/0006-mcp-unified.ja.md)）

| # | 内容 |
|---|------|
| 認証 | PAT を Console 発行。発行者の identity+membership を参照、**role は呼び出し毎に live 解決**、role が天井 |
| scope | 発行時選択 `read`(既定)/`write`/`admin:dangerous`（≤role）。read/write 別トークンで injection 分離 |
| transport | Streamable HTTP（公式 Go SDK・pin）。CP プロセス内 `/mcp` 1 ルート |
| レジストリ | 単一・principal で capability フィルタ。RBAC はサービス層で再検証 |
| 監査 | 全呼び出し `AuditLog(actor_kind=mcp, principal, token_id)` |
| E（主目的） | member/drive: `list_my_sessions`/`send_to_session`/`get_session_status`/`get_session_output` |
| 配布 | 既定 OFF `AF_MCP_ENABLED`。ingress は `/mcp` を Bearer 通し（oauth2-proxy パス除外）|

## P3-6.1 段階（リスク昇順・E を前倒し）

| 段 | 内容 | 主眼 |
|---|------|------|
| **1** | PAT 基盤 + `/mcp` + member/drive(E) + Agent `get_session_output` | **E が動く最短経路** |
| 2 | member read 拡張（repo/fs/diff）+ admin read（usage/list/audit）| 可視化 |
| 3 | admin write（start/stop/quota, `write`）| 運用操作 |
| 4 | admin dangerous（rotate/recreate/stop_all, `admin:dangerous`+confirm+dry-run）| 強権 |

---

## 段 1 — 基盤 + E（詳細）

### A. PAT（CP）

新 migration `0006_pat.sql`:

```sql
CREATE TABLE pat (
  id          TEXT PRIMARY KEY,            -- token_id（監査・失効キー）
  identity_id TEXT NOT NULL REFERENCES identity(id),
  membership_id TEXT REFERENCES membership(id), -- member スコープのトークンはここで固定（admin は NULL 可）
  token_hash  TEXT NOT NULL,               -- SHA-256(secret)。平文は保存しない
  scope       TEXT NOT NULL DEFAULT 'read',-- read|write|admin:dangerous
  name        TEXT NOT NULL DEFAULT '',    -- 利用者が付けるラベル
  created_at  INTEGER NOT NULL,
  expires_at  INTEGER,                     -- NULL=無期限（既定は TTL 推奨）
  revoked_at  INTEGER,
  last_used_at INTEGER
);
CREATE INDEX idx_pat_identity ON pat(identity_id);
```

- **生成**: `af_pat_<base64url(32B 乱数)>`。返却は発行時 1 回のみ、保存は `token_hash`。
- store IF（`control-plane/store.go` / `store_sqlite.go`）: `CreatePAT` / `ListPATsByIdentity` / `GetPATByHash` / `RevokePAT` / `TouchPAT(last_used)`。
- **role は焼かない**。`GetPATByHash` → identity_id/membership_id → 既存 `manager.resolve` 系で role を live 解決。降格・membership 削除で即失効。
- **scope は role 以下に発行時クランプ**（member が `admin:dangerous` を取れない）。
- CP API（Console 発行 UI 用、本人のみ・通常の AuthGateway 経路）:
  - `GET /api/pat` 一覧（hash/secret は返さない）
  - `POST /api/pat {name, scope, ttl}` → `{token, ...}`（token は 1 回だけ）
  - `DELETE /api/pat/{id}` 失効
- Console: 設定モーダルに「API / MCP トークン」セグメント（発行・一覧・失効・1 回表示コピー）。CP のみ変更。

### B. `/mcp` エンドポイント（CP）

- `control-plane/mcp.go`（新規）。Streamable HTTP。SDK は公式 Go SDK（`go.mod` に追加・pin）。
- **認証**: `Authorization: Bearer af_pat_…` を SHA-256 → `GetPATByHash`。無効/失効/期限切れ=401。
  解決した principal（identity, membership(任意), role, scope, token_id）を MCP の各ツール呼び出しに渡す。
  **`/mcp` は AuthGateway(`X-Forwarded-Email`)を通さない PAT 専用経路**（ingress で oauth2-proxy をパス除外）。
- **テナント固定**: principal.membership_id を権威に。クライアント供給 `X-AF-Tenant`/`tenant` は無視。
- **capability フィルタ**: role+scope でツール集合を出し分け（下記レジストリ）。各ツールは
  **サービス層で RBAC 再検証**（フィルタは UX）。
- **監査**: `audit.Record(actor_kind="mcp", actor=identity, token_id, action, target, detail)`。
  段 1 は member/drive 中心ゆえ最小実装でよいが actor_kind は最初から `mcp`。
- CP 起動で `AF_MCP_ENABLED`（既定 false）が true のときだけ `/mcp` を mux 登録。

### C. ツールレジストリ（段 1 = member/drive のみ）

`mcp.go` の registry に登録。principal.role=member（or admin も自分の作業として使える）で可視:

| tool | 入力 | 動作（CP→Agent proxy） |
|------|------|------------------------|
| `list_my_sessions` | — | `manager.resolve` → Agent `GET /sessions`。自分の membership のセッションのみ |
| `get_session_status` | `name` | Agent `GET /sessions/{name}/status`（status ファイル `working|idle|question`）|
| `send_to_session` | `name, prompt` | Agent `POST /sessions/{name}/input`（**新規**, 下記 D）。即時 return |
| `get_session_output` | `name, since?` | Agent `GET /sessions/{name}/output?since=` （**新規**, 下記 D）|

- 全ツールは principal.membership にピン留め（CP の `rtFor` 相当を PAT 由来の membership で構築）。
- 手元 Claude のループ（クライアント側で自然に回る）:
  `send_to_session → poll get_session_status until idle|question → get_session_output → (question なら send_to_session で回答)`。
- N セッション並行 = フリート駆動（= E 本体）。

### D. Agent 追加（**段 1 で唯一の新規 Agent コード**）

`workspace/agent/session.go`（or 新 `session_io.go`）に 2 本。CP は `proxyAgentREST` で委譲。

- **`POST /sessions/{name}/input {prompt}`** — 対象 tmux セッションへ prompt 投入。
  - 実装: `tmux send-keys -t =<tmuxName> -l <prompt>` → `tmux send-keys -t =<tmuxName> Enter`。
    `=` で exact target（§6.10.2 の前方一致 gotcha 回避）。複数行/ペースト安全のため
    bracketed-paste（`send-keys` の `-l` でリテラル送出 → `Enter`）。alive 確認後のみ。
  - claude TUI 前提。kind=shell へは段 1 では出さない（後段で検討）。
- **`GET /sessions/{name}/output?since=<cursor>`** — 直近の assistant 応答を返す。
  - 一次案: claude の **jsonl transcript を tail**（`jsonlPaths(sid)` 既存）し、`since`（行 offset/timestamp）
    以降の `type:assistant` の text を連結。`{output, cursor, status}` を返す。
  - status は `session_status.go` の `working|idle|question` を同梱（クライアントのループ簡素化）。
  - opencode/codex は transcript 形式が異なるので段 1 は claude 限定（kind で 400）。後段で対応。
  - 既存の denylist/traversal 防御の作法（`fs.go`/`git_view.go`）に準拠、サイズ上限。

> 設計の肝: E に要る lifecycle 信号（`working|idle|question`）は **`session_status.go` で既に配線済み**。
> tmux `send-keys` も既存。よって段 1 の新規は Agent の上記 2 本だけで E が成立する。

### E. ingress / 配布

- `AF_MCP_ENABLED=true` のとき CP が `/mcp` を出す。
- oauth2-proxy / Caddy で **`/agent-fleet/mcp` を Google 認証から除外**（Bearer 通し）。設定例を runbook（P3-10）へ。
- README/HANDOFF に MCP クライアント設定例（`.mcp.json`）。

## 段 1 完了判定（実機 E2E）

1. Console で member ロールのユーザーが `read`+`write`（drive 用）PAT を発行。
2. 手元 Claude（Claude Code）の `.mcp.json` に Fleet MCP（Streamable HTTP + Bearer）を登録。
3. `list_my_sessions` で自分の claude セッションが見える。**他 membership のセッションは見えない**。
4. `send_to_session(s, "1+1 は？")` → `get_session_status` が working→idle → `get_session_output` が応答を返す。
5. 2 セッションへ並行 send → それぞれ独立に応答取得（= フリート駆動）。
6. 失効: `DELETE /api/pat/{id}` 後に同トークンで 401。降格（membership 削除）後に role が落ちることを確認。
7. `AF_MCP_ENABLED=false` で `/mcp` が 404。

## 後続段（要約）

- **段 2**: member read 拡張（`repo_status`/`git_diff`/`git_log`/`list_files`/`read_file`＝既存 Agent endpoint の薄いラッパ、denylist 流用）+ admin read（`get_usage`/`list_workspaces`/`list_sessions`/`tail_audit`、CP 管理サービス層）。
- **段 3**: admin write（`start_workspace`/`stop_workspace`/`stop_session`/`set_user_quota`、`write` scope、サービス層 RBAC）。
- **段 4**: admin dangerous（`rotate_key`/`recreate_workspace`/`stop_all_idle`、`admin:dangerous`+`confirm`+`dry_run` 既定 true、read/write 別トークン分離を運用既定に）。AuditLog を厚く。

## 注意・落とし穴（先回り）

- **send-keys の exact target**: `=<tmuxName>` 必須（§6.10.2 前方一致で兄弟セッション誤爆）。
- **prompt の制御文字**: `-l` リテラル送出。改行を含む prompt は Enter で確定するので、本文中の改行と確定 Enter を取り違えない（bracketed-paste 検討）。
- **claude 限定（段 1）**: output は claude jsonl 前提。opencode/codex は transcript 差異ゆえ後段。
- **role live 解決**: PAT に role を焼かない。`GetPATByHash → identity/membership → resolve`。テストで降格即失効を確認。
- **テナント越境**: membership は PAT 由来のみ。クライアント供給ヘッダを絶対に信用しない。
- **injection（admin 段で顕在化）**: 段 1 は member 自己完結ゆえ低リスク。段 3/4 で read/write 別トークン + confirm を必須化。
