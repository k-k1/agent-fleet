# agent-fleet-dist

[English](README.md) | 日本語

[Agent Fleet](https://github.com/k-k1/agent-fleet) の**配布物置き場**です。
ソースコードはここにはありません。成果物は Releases に添付されています。

## Agent Fleet とは

Agent Fleet は、AI コーディングエージェント（Claude Code / Codex CLI /
GitHub Copilot CLI / Antigravity CLI / OpenCode）をフリートとして運用するための
セルフホスト型 Web コンソールです。メンバーごとに隔離されたワークスペース —
cgroup による CPU/メモリクォータ付きの Docker コンテナ（native 版では
bubblewrap サンドボックス上の rootfs）と永続ホーム・git 作業コピー — が
割り当てられ、ブラウザからエージェントセッションを起動・操作・監視できます。
ワークスペースは Go 製のコントロールプレーンが統括します。

主な機能:

- **5 種のエージェント CLI をひとつのコンソールで** — Claude Code / Codex /
  GitHub Copilot / Antigravity / OpenCode のセッションを並べて実行。セッション毎の
  モデル選択に対応し、CLI の版は動作検証済みの組み合わせにピン止め
  （self-update は opt-in）。
- **実 git リポジトリ上の並行セッション** — HTTPS（GitHub / Bitbucket の
  トークンまたは OAuth デバイスフロー）で clone。**Git LFS・submodule
  （ネスト含む）・git worktree に対応**。1 リポジトリに複数セッションを
  worktree 分離で並走させ、各会話はライブミラーで追跡。ターミナルアクセス、
  実行中の入力キュー投入、エージェントセッションと並ぶ素の
  **shell セッション**も。
- **プロジェクト中心のコンソール** — ファイルブラウザ、コミットグラフと diff、
  セッション状態バッジ（実行中 / 入力待ち）、画像添付できるメモキュー、
  通知センター、日英 UI、キーボード主体の操作（コマンドパレット /
  リーダーキー）、返信の読み上げ（VOICEVOX / ずんだもん、AWS Polly・任意）。
- **起動中アプリのライブプレビュー** — ワークスペース内で起動した Web アプリ
  （Vite HMR・WebSocket・Spring Boot 等）を埋め込みブラウザペインで表示。
  ポートは軽量プレビューでも開けます。
- **設計段階からマルチユーザー** — Google OAuth ログイン、テナントとロール
  （member / admin / operator）、ユーザー毎のネットワーク分離、秘密情報の
  at-rest 封筒暗号、ワークスペース毎のメモリクォータ。
- **使用量の可視化** — 各エージェントアカウントの使用量・レート制限
  （と解除時刻）を一目で確認。セッション毎のコンテキスト使用量も可視化し、
  上限が近づくと警告と要約引き継ぎで溢れを防ぎます。
- **アシスタントチャットとフリートのオーケストレーション** — 内蔵アシスタントが
  フリートを操縦: 複数エージェントセッションの起動・指示、異なるエージェント
  間での作業のオーケストレーションと要約付き引き継ぎ、さらに PagerDuty /
  Grafana / CloudWatch 連携と AWS SSM ログインセッションによる
  **SRE アシスタント**として活用できます。
- **運用のしやすさ** — バックアップ / リストアスクリプト、forward-only の
  DB マイグレーションによる更新、air-gap 導入経路、MCP 連携ポイント。

各利用者はエージェント CLI に**自分のアカウント / シート**（例: Claude の
サブスクリプション）でコンソールからログインします。デプロイ自体は AI
プロバイダの認証情報を同梱・共有しません。

## はじめに — どの版を選ぶ？

| 状況 | 版 | 必要なもの |
|---|---|---|
| WSL2 や単一ユーザー Linux で個人利用・Docker なし | **Native**（下記） | unprivileged user namespaces が使える x86_64 Linux/WSL2（素の WSL2 は可）、`curl` か `wget`、ディスク ~1.5 GB |
| 自社の Linux サーバでチーム利用 | **Docker Compose**（下記） | Docker Engine + `docker compose`、ホストに向けた公開ドメイン（自動 TLS。内部 CA フォールバックあり）、ログイン用 Google OAuth 2.0 クライアント |
| AWS でチーム利用 | **ECS（CloudFormation）**（下記） | AWS アカウント、イメージ用 ECR、compose バンドル同梱のテンプレート |
| オフライン / 制限ネットワーク | 上記いずれかの air-gap 経路 | イメージ tar（Compose）または native の `-bundle` tar。ダウンロードの代わりにファイル受け渡し |

全版共通: 各ワークスペースの初回起動時にエージェント CLI のピン版導入で一度だけ
外向きネットワークが必要です（air-gap の代替手順は各同梱 README に記載）。また
各利用者は自分のエージェントアカウント / シートが必要です。

## native 版の導入（Docker 不要・WSL2 / 単一ユーザー Linux）

```bash
curl -fsSL https://raw.githubusercontent.com/k-k1/agent-fleet-dist/main/install.sh | bash
af start
# ブラウザで http://localhost:8099 を開く
```

- インストーラは最新リリースの tar を取得し、`SHA256SUMS` で検証してから
  `~/.local/opt/agent-fleet/<版>/` へ展開し、`~/.local/bin/af` に symlink します。
- 更新も同じコマンドです（データは `~/.local/share/agent-fleet` にあり、触りません）。
- 版を指定する場合: `AF_VERSION=0.1.0 bash install.sh`
- 詳細（ホスト要件・air-gap 導入・常駐化・VOICEVOX / ずんだもんによる読み上げ
  （任意）・制約）は tar 同梱の `README.md` を参照してください。

## Docker Compose 版の導入（チーム・オンプレ）

イメージはレジストリに**公開していません** —
[Releases](https://github.com/k-k1/agent-fleet-dist/releases) からバンドル
（`agent-fleet-<版>.tar.gz`）とイメージ tar（`agent-fleet-images-<版>.tar.gz`）の
両方をダウンロードして:

```bash
V=<版>
sha256sum -c --ignore-missing SHA256SUMS          # 両ダウンロードを検証
tar xzf "agent-fleet-$V.tar.gz" && cd "agent-fleet-$V"
./load-images.sh "../agent-fleet-images-$V.tar.gz" # docker load（CP + workspace）
cp .env.example .env                               # 秘密・ドメイン・Google OAuth を記入
docker compose up -d
```

同梱の `README.md` が完全な runbook です: 前提条件・鍵生成・TLS / ドメイン設定・
バックアップ / リストア・アップグレード・トラブルシュート。

## AWS への導入（ECS / CloudFormation）

compose バンドルには `aws/` 配下に AWS デプロイ一式も同梱しています:
ECS 用 CloudFormation テンプレート（`aws/ecs/cfn/`）、リリースイメージを自分の
ECR へ push するスクリプト（`aws/ecs/release-ecr.sh`）、EC2 一台構成
（`aws/ec2-single/`）。バンドル内の `aws/ecs/README.md` から始めてください。

## アンインストール / データの消去（native 版）

```bash
# 1. systemd ユーザーユニットを設定していた場合は先に停止・削除
systemctl --user disable --now agent-fleet 2>/dev/null
rm -f ~/.config/systemd/user/agent-fleet.service
# （ユニット未設定なら `af start` を Ctrl-C で止めるだけ）

# 2. データ全消去 — DB・各ワークスペースの home（Claude ログイン含む）・展開済み rootfs
af reset --all

# 3. プログラム本体の削除
rm -f ~/.local/bin/af
rm -rf ~/.local/opt/agent-fleet
```

- 手順 2 と 3 は独立です。**データを残して**アンインストールしたい場合
  （再インストール前など）は手順 2 を飛ばしてください — データはプログラムとは
  別置きの `~/.local/share/agent-fleet`（`WS_DATA` で変更した場合はその場所）に
  あります。
- `af` を先に消してしまった場合は手動で削除します。ワークスペース home 内の
  Go モジュールキャッシュは書き込み禁止になっているため、先に書き込み権を
  戻してください:

  ```bash
  chmod -R u+w ~/.local/share/agent-fleet && rm -rf ~/.local/share/agent-fleet
  ```

## Releases の構成

| tag | 添付 | 用途 |
|---|---|---|
| `v<版>` | `agent-fleet-<版>.tar.gz`（compose バンドル）/ `agent-fleet-images-<版>.tar.gz`（air-gap 用イメージ）/ `agent-fleet-native-<版>-linux-amd64.tar.gz`（native）/ `SHA256SUMS` | アプリ本体のリリース |
| `rootfs-<r>` | `agent-fleet-rootfs-<r>-linux-amd64.tar.zst` | native 版が初回起動時に取得する workspace rootfs。**単体では使いません**（native tar 内の `rootfs.json` が版・sha256 を指定） |

`<r>` は内容ハッシュです。アプリの版が上がっても rootfs が不変なら同じ tag を参照し、
再ダウンロードは発生しません。取得物は必ず `SHA256SUMS` / `rootfs.json` の sha256 で
検証してください（install.sh と `af start` は自動で行います）。

## ライセンス / 同梱物について

- 配布イメージ・rootfs は **lean 構成**です: エージェント CLI（Claude Code /
  Codex / GitHub Copilot / Antigravity / OpenCode）は同梱せず、初回起動時に
  各利用者がそれぞれの配布元から検証済みのピン版を取得します（再配布を
  行わないための構成です）。
- 同梱 OSS の帰属は各 tar 内の `NOTICE` を参照してください。
