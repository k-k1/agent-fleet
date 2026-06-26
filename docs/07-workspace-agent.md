# 07. Workspace Agent 仕様（ドラフト）

各 Workspace コンテナ内に常駐する薄い仲介プロセス。Control Plane からの指示を受け、
コンテナ内で git・tmux・claude・設定を操作し、ターミナル PTY を中継する。
`tmux-claude.sh` のセッション制御ロジックをサービス向けに再実装した中核でもある。

## 7.1 位置づけ

```
Control Plane ──内部(VPC)── Workspace Agent ──┬── PTY(tmux attach)
   (Go)                        (Go, 同梱)      ├── git (clone/checkout/status…)
                                               ├── claude セッション制御
                                               ├── ~/.claude/settings.json 読み書き
                                               └── ~/.ssh 鍵生成/公開鍵取得
```

- 外部公開しない。Control Plane からのみ内部到達（[03 §3.3](03-aws-deployment.md#33-ネットワーク--認証ゲート)）。
- 非特権ユーザーで起動。コンテナ内のユーザー領域のみ操作。

## 7.2 インターフェース方式

- **制御系（要求/応答）**: HTTP/JSON もしくは gRPC（VPC 内）。冪等・短命。
- **ストリーム系**: WebSocket（PTY 入出力）。Control Plane が Console の `/ws/terminal` を透過プロキシ。
- ポートは固定（例 `:8800` 制御 / `:8801` PTY）。コンテナ外へは出さない。

## 7.3 制御 API（Agent 内部）

Control Plane が呼ぶ内部エンドポイント。Console 公開 API（[06](06-api-spec.md)）の下請け。

| 機能 | 概要 | 主な実体 |
|------|------|----------|
| `git.clone` | `git clone <url> <path>` | `~/.ssh` の鍵を使用 |
| `git.status` | 構造化 status | `git status --porcelain=v2 --branch` を解析 |
| `git.branches` | ブランチ一覧 | `git for-each-ref` |
| `git.checkout` | checkout / ブランチ作成 | `git checkout [-b] <ref>` |
| `git.fetch` | fetch/prune | `git fetch [--prune]` |
| `session.list` | 稼働セッション一覧 | `tmux ls` + jsonl 突合 |
| `session.create` | セッション起動 | §7.4 |
| `session.stop` | 停止 | `tmux kill-session` |
| `session.resume` | 再開 | 決定論的 session-id で `claude --resume` |
| `claudeAuth.status` | `/login` 状態 | `~/.claude/.credentials.json` 判定 |
| `settings.get/put` | 設定読み書き | `~/.claude/settings.json` |
| `sshkey.get` | 公開鍵取得 | `~/.ssh/id_ed25519.pub` |
| `sshkey.ensure` | 鍵が無ければ生成 | `ssh-keygen -t ed25519` |
| `health` | 生存確認 | — |

## 7.4 セッション制御（tmux-claude.sh の継承）

既存スクリプトの要点をそのまま設計に取り込む。

- **決定論的セッション ID**: `uuidgen --sha1 --namespace @url --name "<salt>|<dir>|<name>"` 相当。
  同じ Working copy + スロット名なら毎回同じ ID。claude.ai 側に新エントリを増やさず resume する。
- **起動規則**: 履歴 jsonl が在れば `--resume`、無ければ `--session-id` で新規。
- **作業ディレクトリ**: Working copy の `clone_path`。
- **設定プロファイル**: 既定は Workspace 共通の `~/.claude`。ディレクトリ単位サンドボックスを使う場合は
  `CLAUDE_CONFIG_DIR` を Working copy ごとに割り当て（[02 §2.7](02-architecture.md#27-サンドボックス設計c2)）。
- **共通フラグ**: `--dangerously-skip-permissions` 等は Workspace ポリシーとして管理（[04](04-security.md)）。
- **世代操作**: `renew`（全スロット新 ID）/ `restart`（同 ID 再起動）/ `reset`（履歴のみ削除）に相当する
  操作を Agent の API として提供。フリート全体版は管理者 API（[06 §6.9](06-api-spec.md#69-管理者)）。

```
session.create(repository_id, name, model?, config_profile?):
  dir   = repo.clone_path
  sid   = deterministic_id(salt, dir, name)
  tmux  = "claude_" + slug(repo + name)
  if jsonl_exists(sid): launch `claude --resume <sid> …`
  else:                 launch `claude --session-id <sid> …`
  inside `tmux new-session -d -s <tmux>`
  return { sid, tmux, state: running }
```

## 7.5 Control Plane との認証

Agent は内部公開でも認証必須（多層防御）。候補:

- **mTLS** — Control Plane と Agent が相互証明書検証。VPC + SG で到達制限の上に暗号認証。
- **署名付きトークン** — 起動時に Workspace ごとの短命トークンを注入し、各要求で検証。

→ [01 未決 #6](01-requirements.md#17-未決事項今後詰める)。MVP は SG 制限 + 署名トークン、本番で mTLS を推奨。

## 7.6 PTY ストリーム

- Agent は `forkpty` で擬似端末を生成し、`tmux attach -t <session>`（または shell）を実行。
- 上り `input`/`resize`、下り `output` を WS で中継（[06 §6.10](06-api-spec.md#610-websocket)）。
- 切断時も tmux セッションは存続。再接続で同一画面に復帰。
- 複数タブから同一 tmux への同時アタッチを許容（共有ビュー）。

## 7.7 ライフサイクルと永続

- Agent はコンテナ起動時に常駐開始。`~`（EFS）をマウント済み前提。
- 起動時処理: `~/.ssh` 鍵の存在確認（無ければ生成）、known_hosts 整備、tmux サーバ起動。
- コンテナ停止: tmux セッションはプロセスとして消えるが、会話履歴 jsonl は EFS に残り resume 可能。
- イメージ更新で Agent/claude を入れ替えてもユーザーデータ（EFS）は不変。
