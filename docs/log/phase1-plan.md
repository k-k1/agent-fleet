# 11. Phase 1 実装プラン（Workspace Agent + Console MVP / ローカル Docker）

[ロードマップ Phase 1](../roadmap.md) の実装計画。
ローカル Docker で「1 ユーザーが Web からターミナル操作 + Claude セッションの一覧/起動/停止」を成立させる。
リポジトリ管理（clone 等）は Phase 2 のため**対象外**。セッションは既存ディレクトリに対して張る。

## 11.1 スコープ（MVP）

含む:
- Workspace イメージ（claude CLI / git / tmux / **Workspace Agent** 同梱）
- Workspace Agent（Go）: セッション一覧 / 起動 / 停止、PTY ターミナル(WS)
- Control Plane（Go）: dev 固定 ID 認証、**Runtime(Docker) アダプタ**で Workspace 起動、Agent への REST/WS 中継、静的 Console 配信
- Console（最小・静的 + xterm.js）: ターミナル表示 + セッション一覧
- ホーム永続: `./data/home/<user>` を Workspace の `~` に bind mount

含まない（後続フェーズ）:
- リポジトリ管理（Phase 2）/ Google OAuth（Phase 2 shared）/ AWS アダプタ（Phase 3）
- マルチユーザーの本格運用、scale-to-zero、監査（Phase 2-4）

## 11.2 リポジトリ構成

```
agent-fleet/
├── workspace/
│   ├── Dockerfile            # node:22 + claude + git + tmux + agent バイナリ
│   └── agent/                # Go: Workspace Agent（module: …/workspace/agent）
├── control-plane/
│   ├── Dockerfile            # multi-stage（golang→distroless 等）
│   └── *.go                  # Go: Control Plane（module: …/control-plane）
├── console/
│   ├── index.html            # xterm.js（CDN）でターミナル + セッション一覧
│   └── app.js / style.css
└── deploy/local/
    ├── docker-compose.yml    # control-plane + ネットワーク + data ボリューム
    └── .env.example          # HOST_DATA_DIR 等
```

## 11.3 実行トポロジ（ローカル）

```
Browser ──HTTP/WS──▶ Control Plane(:8080, コンテナ)
                        │  ├ 静的 Console 配信（/）
                        │  ├ REST /api/*（セッション操作）
                        │  └ WS /ws/terminal（PTY 中継）
                        │ Docker Engine API（/var/run/docker.sock）
                        ▼
                     Workspace コンテナ（af-ws-<user>）
                        ├ Agent(:7700)  HTTP /sessions, WS /ws/pty
                        ├ tmux + claude
                        └ ~ = bind mount ./data/home/<user>
    network: afnet（CP ⇄ Workspace はコンテナ名で到達。外部公開しない）
```

- CP と Workspace は同一 Docker network `afnet`。CP は `http://af-ws-<user>:7700` で Agent に到達。
- **host パスの罠**: CP はコンテナ内から host の Docker に bind mount を依頼するため、`./data` の
  **ホスト絶対パス**を `HOST_DATA_DIR` で渡す（sibling コンテナの bind 元はホスト基準）。

## 11.4 Workspace Agent 契約（HTTP/JSON + WS）

内部のみ公開。MVP の最小エンドポイント。

| Method | Path | 説明 |
|--------|------|------|
| GET | `/healthz` | 生存確認 |
| GET | `/sessions` | tmux の `claude_*` セッション一覧（name, dir, alive）|
| POST | `/sessions` | 起動。body: `{ name, dir, model? }`。決定論的 session-id で claude を tmux 起動 |
| POST | `/sessions/{name}/stop` | `tmux kill-session` |
| GET (WS) | `/ws/pty?session=<name>` | PTY を生成し `tmux attach -t <name>` を実行、双方向中継 |

セッション制御は [07 §7.4（現 dev/04 §4.2 セッションモデル）](../dev/04-workspace-agent.md#42-セッションモデル) のロジック（jsonl 有無で `--resume`/`--session-id`）。

WS フレーム（JSON テキスト）:
- 上り: `{"type":"input","data":"…"}` / `{"type":"resize","cols":N,"rows":N}`
- 下り: `{"type":"output","data":"…"}`

## 11.5 Control Plane エンドポイント（MVP）

[06](../dev/05-api-contracts.md) のサブセット。認証は dev 固定 ID（`X-Dev-User` か固定値）。

| Method | Path | 説明 |
|--------|------|------|
| GET | `/` ほか | 静的 Console 配信 |
| GET | `/api/workspace` | Workspace 状態（running/stopped）|
| POST | `/api/workspace/start` | Docker で Workspace 起動（bind mount + afnet 接続）|
| POST | `/api/workspace/stop` | 停止 |
| GET | `/api/sessions` | Agent へ proxy |
| POST | `/api/sessions` | Agent へ proxy |
| POST | `/api/sessions/{name}/stop` | Agent へ proxy |
| GET (WS) | `/ws/terminal?session=…` | Agent の `/ws/pty` へ透過 proxy |

Runtime アダプタ（[09 §9.3（現 dev/09 §9.2 ポート&アダプタ）](../dev/09-deploy.md#92-ポートアダプタ--何をどのノブで差し替えるか)）の `local` 実装をここで初めて具現化する。

## 11.6 技術選定（MVP）

- 言語: Go（agent / control-plane）。依存は最小:
  - WebSocket: `github.com/gorilla/websocket`
  - PTY: `github.com/creack/pty`
  - Docker 制御: MVP は `docker` CLI を exec（SDK 導入は後続）or Docker Engine SDK。まずは CLI exec で軽く。
- Console: 素の HTML + `xterm.js`（CDN）。Next.js 化は Phase 2。
- ビルド: Docker multi-stage（host に Go 不要でも動くが、検証高速化のため Go も用意）。

## 11.7 マイルストーン

| # | 内容 | 完了条件 |
|---|------|----------|
| M1 | リポ構成 + プラン | 本書 + ディレクトリ雛形 |
| M2 | Agent: sessions + PTY | コンテナ内で `curl /sessions`、WS で tmux に繋がる |
| M3 | Workspace イメージ | agent 同梱でビルド・起動、ホーム bind mount 永続 |
| M4 | Control Plane | Docker で Workspace 起動、REST/WS proxy、Console 配信 |
| M5 | E2E（compose）| ブラウザでターミナル操作 + セッション一覧/起動/停止が通る |

## 11.8 実行方法（完成時の想定）

```bash
cd deploy/local
cp .env.example .env          # HOST_DATA_DIR を実パスに
docker compose up --build -d
# ブラウザで http://localhost:8080 → ターミナル + セッション一覧
```

## 11.9 既知の留意点

- Phase 1 は dev 形態（認証バイパス・単一ユーザー）。隔離/認証強化は Phase 2 以降。
- `claude /login` は [10](../log/phase0-poc.md) の方式 A（対話コード貼り戻し）をターミナルから実施。
- docker.sock を CP に渡す = ホスト root 相当（[09 §9.8（現 dev/07 セキュリティ）](../dev/07-security.md)）。dev 前提で許容。

## 11.10 実装結果と実運用の知見（Phase 1 完了）

**状態: 完了・実機検証済み（2026-06-26）。** M1〜M5 を実装し、実際の公開経路で E2E 動作を確認した。

### 検証した経路（実機）
```
ブラウザ → Tailscale Funnel(HTTPS) → oauth2-proxy(Google) → Caddy(strip /agent-fleet)
        → Control Plane → Docker で Workspace 起動 → Agent → PTY → tmux → 実 claude
```
Workspace 起動/停止、セッション作成/一覧/停止、ターミナル往復、ホーム永続、tmux 分離、
各自アカウントの `/login`（方式A）、日本語表示までを確認。

### 計画からの差分（確定）
- **claude は実行時 install**（イメージに焼かない）。entrypoint が起動時に最新を `~/.local` へ install/update
  → 再ビルドなしで常に最新。`INSTALL_CLAUDE` build-arg は廃止、`CLAUDE_INSTALL` env で制御。
- **M5 は host-CP 版**（`deploy/local/run-dev.sh`）で実現。compose（CP もコンテナ化 + afnet）は後続（[09](../dev/09-deploy.md)）。
- CP のポートは **8099**（8080 はこの環境で使用中）。
- まだ**単一 Workspace**（`af-ws-dev`）。per-user 化は Phase 2。

### 実運用で潰した点（再発防止メモ）
- **base-path**: Console は API/WS/アセットを相対パス化し、`/agent-fleet` strip 配下でも動くように（`<base>` 補正つき）。
- **UTF-8 locale**: コンテナ既定の POSIX だと tmux が UTF-8 を無効化し CJK が化ける → `ENV LANG=C.UTF-8`。
- **bypass 警告の誤 exit**: `--dangerously-skip-permissions` の初回警告で `No, exit` を選ぶと claude 終了
  → entrypoint で `~/.claude/settings.json` に `skipDangerousModePermissionPrompt: true` を seed。
- **Console キャッシュ**: 静的配信に `Cache-Control: no-store`（dev 中の反映漏れを防止）。
- **端末描画**: 絵文字の半クリップは unicode11 アドオン、見栄えは WebGL + JetBrains Mono。
- **/login URL の受け渡し**: Ink がハード改行するため、バッファから折返し連結で URL を復元し
  「⧉ sign-in URL」ボタンでオンデマンド Copy（自動バナーは誤検出が多く廃止）。→ [02 §2.6（現 dev/08 §8.5 Claude 認証）](../dev/08-integrations.md#85-claude-認証オンボーディングl2-の本丸)。

### 次フェーズの入口
per-user Workspace 化（CP が user→container を払い出し）、リポジトリ管理、SSH 鍵、settings.json 編集 UI、
Claude 認証状態表示。→ [05 ロードマップ](../roadmap.md)。
