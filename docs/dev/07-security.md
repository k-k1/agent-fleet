# 07. セキュリティ — 脅威モデル・認証・暗号・監査

> 正: コード（本書は境界と設計意図）/ 主な更新トリガ: 認証モード・暗号・隔離・監査・egress に触る変更 / 最終確認: 2026-07

## 7.1 脅威モデルと信頼境界

各 Workspace では CLI エージェントが**任意コードを実行**する（`--dangerously-skip-permissions` 運用を含む）。
「ユーザーのセッションが untrusted コードを動かす」前提で境界を設計する。守るのは
**他ユーザーのデータ / CP・ホスト基盤 / シークレット / 情報持ち出し**。

```
信頼度 低 ┌──────────────────────────────┐
          │ Workspace コンテナ内部        │ ← 任意コード実行を許容する領域
          └──────────────┬───────────────┘
                         │ 主要な隔離境界
信頼度 高 ┌──────────────▼───────────────┐
          │ Control Plane / ホスト基盤    │ ← Workspace から侵害されてはならない
          └──────────────────────────────┘
```

**前提の限界（正直に明記）**: 1 デプロイ内では CP が `docker.sock`（ホスト root 相当）を持ち平文 DEK を
注入するため、**CP/ホストが侵害されるとそのデプロイ内の分離は一括で破れる**。会社間は別デプロイゆえ
波及しない——これが提供モデルの強み（[decisions/0001](../decisions/0001-self-host-vs-saas.md)）。
緩和候補: rootless Docker / socket-proxy / CP 最小権限（別ワーク・未着手）。

## 7.2 隔離コントロール（local / aws 両対応）

| 対象 | local（Docker・既定） | aws（ECS）🚧 |
|------|----------------------|--------------|
| ユーザー間ファイル | per-membership home を bind mount（`<WS_DATA>/<user>/home`）。他ユーザーの home はマウントされない | EFS アクセスポイントで root dir と uid/gid を固定 |
| プロセス / メモリ | 1 membership = 1 コンテナ。`--memory`（`WS_MEMORY` 既定 1g）| タスク分離（Fargate はホスト共有なし）|
| ネットワーク | 専用 network `af-net-<user>` で相互到達を遮断。Agent はホスト `127.0.0.1` publish 経由で CP のみ到達。egress は NAT 維持（統制は §7.8）| SG / サブネット。IMDS 遮断・Task Role 最小化 |
| コンテナ → ホスト | 非特権コンテナ・capability 最小化 | Fargate ならホスト共有なし |
| 機微状態の退避 | claude の平文状態は 2nd mount `<dataDir>/claude-config:/var/lib/af/claude` + `CLAUDE_CONFIG_DIR` 注入で**ファイルブラウザの範囲外**へ。暗号化済み `secrets.enc` は home 据置 + denylist | 同左（イメージ・Agent は共通）|

限界: 同一 uid のコンテナ内 shell から本人の BYO トークンを完全不可視にはできない（原理的に不可）。
ブラウザ不可視 + at-rest 暗号 + env 注入で実用十分とする設計判断。

## 7.3 L1 Console 認証（AUTH 3 モード）

`AUTH` env で分岐。いずれも解決した email を sanitize（小文字化・非英数→`-`・40 字上限）して
`identity.user_key` にする。

| モード | 仕組み | 用途 |
|--------|--------|------|
| `oauth`（実運用の既定）| **CP ネイティブ Google OAuth**。`/oauth2/{login,callback,logout}` + `/login` を CP が所有。ログイン成功で署名 cookie（HMAC-SHA256・`AF_COOKIE_SECRET`・HttpOnly/Secure/Lax・TTL `AF_SESSION_TTL` 既定 168h）発行 | セルフホスト本命。HTTPS 前提（エッジは Caddy/Funnel）|
| `proxy` | 外部ゲートウェイ（oauth2-proxy / ALB OIDC）の `X-Forwarded-Email`（`AUTH_EMAIL_HEADER` で変更可）を信頼。**ヘッダ欠落は 401**（フォールバック無し）。CP は loopback 束縛前提 | ALB OIDC（aws）/ 既存ゲート流用 |
| `dev` | 固定 `DEV_USER`（既定 `dev`）| ローカル開発のみ。`AUTH=oauth` は素の HTTP では Secure cookie が保存されず使えない |

**authGate の要点**（`oauth` モード）:
- 全リクエストを検査し、**受信した `X-Forwarded-Email` を必ず削除**してから検証済み email を自ら注入
  （エッジがヘッダ素通しでも成りすまし不可）。以降の identity/membership 解決は `proxy` と共通経路。
- 除外パス（`routes.go` の `exemptExact`/`exemptPrefix` 宣言が正）: ログイン導線と死活の
  `/oauth2/*`・`/login`・`/healthz`・`/brand/*`、自前認証を持つ `/mcp`・`/mcp/*`（Bearer PAT）・
  `/git/*`（Basic git token）・`/internal/*`（デプロイ内部の Bearer トークン: egress ingest /
  schedule bridge / mcp-servers poll）、旧パス互換リダイレクトの `/agent-fleet[/…]`。
- 許可リスト 3 系統の併用可・**すべて空なら全拒否（fail-closed）**:
  `AF_OAUTH_ALLOWED_EMAILS`（CSV）/ `AF_OAUTH_ALLOWED_DOMAINS`（CSV）/
  `AF_OAUTH_ALLOWED_EMAILS_FILE`（1 行 = メール or `@domain`・ログイン毎に再読込＝**追加は再起動不要**）。

認可は [05 §5.4](05-api-contracts.md): 自分のリソースのみ + membership 検証、admin は role gate
（super_admin=デプロイ全体 / tenant_admin=自テナント）。role の階層は `identity.role` と
`membership.role` の 2 段（[06 §6.2](06-data-model.md)）。

## 7.4 L2 エージェント認証との分離

L2（Claude/codex/opencode を誰として動かすか）はユーザー本人の OAuth で、CP は関与しない
（可視化と接続 UI のみ）。フローと保存先は [08](08-integrations.md)。Workspace を跨いだ認証情報の
共有は設計上禁止（home 分離がそのまま境界）。

## 7.5 CP ↔ Agent 認証

- per-container の `AGENT_TOKEN` を CP が起動時に `-e` 注入し DB に永続（[06](06-data-model.md)）。
  CP 再起動時は既存コンテナから inspect で採用（再作成しない）。
- CP の全中継（REST/SSE/WS/preview）と Bitbucket callback が `Authorization: Bearer` を付与。
  Agent の `requireToken` が `/healthz` 以外を**定数時間比較**で検証（未設定時は dev 用に開放）。
- ネットワーク分離（§7.2）との多層防御。

## 7.6 シークレット管理と封筒暗号

**原則: 秘密はユーザー領域に閉じ、CP は平文を保持・解釈しない。ログに秘密を出さない。**

| シークレット | 保管 | 露出範囲 |
|-------------|------|----------|
| git 資格情報（GitHub PAT/Device、Bitbucket OAuth/token）・opencode env キー | Workspace home の **`secrets.enc`**（AES-256-GCM・0600）| 当該ユーザーのみ。統一 cred helper（`workspace-agent cred`）が都度復号して出力＝**平文ファイルを作らない** |
| Claude `.credentials.json`（claude 自身が書く）| `CLAUDE_CONFIG_DIR`（home 外・browse 範囲外, §7.2）| 当該ユーザーのみ |
| システム秘密（Google OAuth client secret・`AF_MASTER_KEY`・`AF_COOKIE_SECRET`）| `oauth.env` / compose `.env`（git 管理外）。aws=Secrets Manager/SSM 🚧 | CP のみ。`AF_MASTER_KEY` はデータ領域の外で保管（バックアップに含めない＝失えば crypto-shred）|
| PAT | DB に SHA-256 ハッシュのみ（[06](06-data-model.md)）| 平文は発行時 1 回だけ表示 |

**封筒暗号 + custodian 抽象**（[decisions/0005](../decisions/0005-envelope-custodian.md)）:
- per-workspace **DEK** を per-tenant **KEK** で wrap し `wrapped_dek` に保存。CP が Workspace 起動時に
  unwrap して `AF_SECRET_KEY` としてコンテナへ注入（Agent は暗号方式に無関心）。
- custodian は interface（`KeyCustodian{Wrap,Unwrap}`）。現実装は **localCustodian**
  （KEK = `HMAC(master, "af-kek:"+tenantID)`・AES-GCM・AAD=keyRef）。
- ⚠️ **honest な限界**: localCustodian は KEK が master 由来のため、実効強度は単一 `AF_MASTER_KEY` と
  同等。テナント鍵 disable による**真の per-tenant crypto-shred は Vault/KMS custodian 採用時**に達成
  （📋 seam のみ・[decisions/0005](../decisions/0005-envelope-custodian.md)）。

## 7.7 監査

- 器は `audit_log`（[06](06-data-model.md)）。`actor_kind` = user / admin / mcp / system（将来 claude）。
- **記録範囲は「変更・破壊操作」のみ**（読み取りは既定オフ、**ターミナル生ストリームは保存しない**——
  秘密混入リスク。[docs/20 E.1](../20-container-audit-egress.md)）。
- 書き込み点: CP proxy 層（fs/git/repo/session の変更系・[05 §5.5](05-api-contracts.md)）、admin API、
  MCP write ツール（`actor_kind=mcp`・PAT id 記録・role は呼び出し時に live 再解決）。
- 読み取り面: `GET /api/admin/audit`（tenant scope・RBAC）+ Console admin タブ。
- 第 2 段（claude PreToolUse hook で `actor_kind=claude`）は 📋（[docs/20 A-第2段](../20-container-audit-egress.md)）。

## 7.8 egress 統制 🚧（log-only 運用まで実装・enforce は後続）

設計と決定は [docs/20](../20-container-audit-egress.md)。実装済みの器:

- **forward proxy 方式**（CP バイナリのサブコマンド、`AF_EGRESS_LISTEN` 既定 `:3128`）。
  FQDN（CONNECT/SNI）で allow/deny を判定し、**TLS は復号しない**。
- イベントは `/internal/egress`（`AF_EGRESS_TOKEN`）で CP に集約 → `egress_daily` に日次集計。
  policy 配布は `/internal/egress/policy`。
- **allowlist は版管理**（`egress_allowlist`: active/proposed/retired）+ デプロイ全体モード
  （`deployment_setting`）。admin API/UI（一覧・承認・mode 切替）実装済み。
  AI は**提案のみ・人間が承認**（自動適用しない）。
- 段階運用が設計の核: **log-only で実測 → allowlist を固める → enforce へ切替**。
  enforce 化とコンテナ側配線（`--internal` 網 + proxy env 注入）の常時有効化は後続
  🚧、aws 側（Network Firewall / DNS Firewall）は P3-7 と同時 📋。

## 7.9 リスクと残課題

1. **`--dangerously-skip-permissions` の既定運用** — コンテナ境界が唯一の砦。§7.2 を厳格に。
2. **CP/ホスト侵害 = デプロイ内一括崩壊**（§7.1 の前提の限界）。会社間非波及が緩和。
3. **長期保持する L2 認証情報の失効・ローテーション** — 封筒暗号で枠組みは入ったが、真の失効は
   Vault/KMS 待ち（§7.6）。
4. **サプライチェーン** — Workspace イメージ同梱ツールの出所管理・定期更新（[04](04-workspace-agent.md)）。
5. **egress enforce 未了** — log-only の観測は入ったが遮断はまだ（§7.8）。
6. 対外的な脅威モデル・脆弱性報告窓口は [SECURITY.md](../../SECURITY.md)（英語・対外向け）。
