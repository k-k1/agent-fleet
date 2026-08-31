# 0006. MCP — 管理面と作業面を一体で公開（PAT 認証・E を主目的）

[English](0006-mcp-unified.md) | 日本語

- 状態: 確定。実装 = 段1（member/drive + PAT + `/mcp`）+ admin read/write、ともにライブ E2E green（2026-07-01）/ dangerous 段残（鍵ローテ・idle 検出の土台待ち）
- 関連: [roadmap P3-6](../roadmap.md#p3-6-mcp-による-agent-fleet-制御管理面--作業面を一体で) / [history/p3-6-mcp](../log/p3-6-mcp.md) / [dev/01 §1.4 認証は 2 層](../build/01-architecture.ja.md)（旧 architecture §認証スコープ） / [dev/07 セキュリティ](../build/07-security.ja.md)（旧 security）

## 背景

CP は REST + Console を持つが、これは**人間が作ったクライアント（Console）でしか駆動できない**。
運用者・メンバーが自分の Claude（Claude Code / Desktop / claude.ai）から自然言語で Fleet を
操作・観測したい。とくに本プロジェクトの起点である**「1 つの手元 Claude が、自分の Workspace
内で走る複数の claude/opencode/codex セッションを束ねて駆動する」**（フリート運用の MCP 化）が
そもそもの目的。これを REST に人手クライアントを被せる形では達成できない。

「管理面（P3-6 旧構想）」と「作業面（メンバー自身の遠隔セッション駆動）」を **両方** 1 本の
MCP サーバで出したい、という意思決定（2026-06-29）。

## 決定

### 1. 一体化する層は入口だけ（service 層ではない）

`/mcp` の **transport / 認証・RBAC / 監査** だけを一体化する。裏のバックエンドは 2 つのまま:
admin ツールは CP の管理サービス層、member ツールは `manager.resolve` 経由で per-container の
Agent（`/sessions`・`/repos`・`/fs`）へ proxy。**新ロジックを足さない薄いラッパ**の原則を維持。
「サービス層を 1 本に」は管理面の話で、作業面は Agent 表面なので統合しない（混同しない）。

### 2. 認証 = PAT（CP 発行・発行者の role を継承）

- **各ユーザーが Console（CP）で自分の PAT を発行**。別途 service principal を切らない。
- トークンは **identity + membership** を参照。**role は呼び出し毎に store から live 解決**
  （トークンに焼かない）＝降格・membership 削除で既存トークンが即無力化される。
- **role は能力の天井**。admin が発行したトークンは admin ツールに到達でき、member の
  トークンは自分の membership の作業ツールのみ。「admin なら admin の PAT」を満たす。
- **scope は発行時に選択（≤ role）**: `read`（既定）/ `write` / `admin:dangerous`。
  admin でも既定は read。これで「読む Claude は read トークン・壊す操作は別トークン」という
  **read/write 分離が“1 人が複数トークンを持つ”形で自然に成立**する。
- 失効・TTL・ローテを最初から（Console に発行/一覧/失効 UI）。文字列は発行時 1 回表示・保存はハッシュ。
- **テナントはトークンに固定**。クライアント供給の `X-AF-Tenant` は MCP では受けない（cross-tenant 封じ）。
- オンプレの oauth2-proxy（Google forward-auth）は MCP クライアントの OAuth2.1/DCR と噛み合わない
  ので PAT を主にする。OAuth2.1 ネイティブ対応は claude.ai/Desktop を取りに行く段（AWS 以降）で従。

### 3. transport = Streamable HTTP

CP に `/mcp` を 1 ルート追加（新プロセス不要）。旧仕様の HTTP+SSE ではなく **Streamable HTTP**。
SDK は公式 Go SDK を第一候補にしバージョン pin（MCP の transport は一度割れているため）。

### 4. ツールは単一レジストリを principal で capability フィルタ

role + scope で見えるツールを出し分ける。posture は role で非対称:

- **member/drive（E・主目的）**: `list_my_sessions` / `send_to_session` / `get_session_status` /
  `get_session_output`。自分の BYO claude が自分の Workspace を駆動＝**同一信頼ドメイン・自己完結**。
  被害は自分の Workspace に閉じるので read/write 厳格分離の対象外でよい。
- **admin/read**: `get_usage` / `list_*` / `tail_audit`。
- **admin/write**: `start_workspace` / `stop_workspace` / `stop_session` / `set_user_quota` 等（`write` scope）。
- **admin/dangerous**: `rotate_key` / `recreate_workspace` / `stop_all_idle`
  （`admin:dangerous` scope ＋ `confirm` 引数 ＋ `dry_run` 既定 true。fleet 横断ゆえ強権）。

RBAC は **必ずサービス層で再検証**（MCP 層の capability フィルタは UX、authz の権威にしない）。
全呼び出しを `AuditLog(actor_kind=mcp, principal, token_id)`（schema は `mcp` を既に持つ）。

### 5. E（メンバー遠隔駆動）を第一級目標・前倒し

E は既存資産にほぼ乗る: 状態フック（`working|idle|question`、`session_status.go`）が send→完了
判定に使え、tmux `send-keys` で prompt 投入、決定的 sid で対象指定、jsonl transcript で応答取得。
**新規に要るのは Agent の `get_session_output`（jsonl/capture-pane の tail）1 本だけ**。
手元 Claude のループは `send → poll status until idle|question → get_output`（→ question なら回答送信）。
N セッション並行＝フリート駆動。よって E を Phase 1 へ格上げ（旧案の「最後の任意段」から変更）。

## 帰結・正直な限界

- **両方一体・role 出し分け**が、member（自己完結な駆動）と admin（fleet 横断・confirm 必須）の
  posture 非対称によってきれいに同居する。
- **最大の固有リスク = prompt-injection × 変更系の confused-deputy**（admin 側）。監査ログ/ファイルを
  読ませた Claude に rotate/stop_all を持たせると注入で破壊操作に誘導されうる →
  read/write の別トークン分離 + dangerous の人手 confirm + dry-run で殺す。E（member）は自己完結ゆえ対象外。
- **blast radius**: admin トークン＝そのデプロイの管理面の鍵。漏洩＝管理面侵害。短命・ローテ・read 既定・
  scope 分離・監査で抑える。**会社間はデプロイ分離ゆえ波及しない**（本モデルの強み）。
- MCP は CP の口を増やすだけで新たな信頼境界は作らない。逆に **MCP 認証が CP 侵害面そのもの**なので
  入口認証を弱くしない。
- member MCP の read 系は Console があるぶん補完的。E（drive）が主、read 拡張は従。
- 既定 OFF（`AF_MCP_ENABLED`）で同梱、P3-10 runbook に設定（ingress の `/mcp` は Bearer 通し＝
  oauth2-proxy のパス除外が 1 点必要）。phone-home なし。
