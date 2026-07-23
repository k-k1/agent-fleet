# agent-fleet-dist

[English](README.md) | 日本語

[Agent Fleet](https://github.com/k-k1/agent-fleet) の**配布物置き場**です。
ソースコードはここにはありません。成果物は Releases に添付されています。

## Agent Fleet とは

Agent Fleet は、AI コーディングエージェント（Claude Code / Codex CLI /
GitHub Copilot CLI / Antigravity CLI / Cursor CLI / OpenCode）をフリートとして運用するための
セルフホスト型 Web コンソールです。メンバーごとに隔離されたワークスペース —
cgroup による CPU/メモリクォータ付きの Docker コンテナ（native 版では
bubblewrap サンドボックス上の rootfs）と永続ホーム・git 作業コピー — が
割り当てられ、ブラウザからエージェントセッションを起動・操作・監視できます。
ワークスペースは Go 製のコントロールプレーンが統括します。

主な機能:

- **6 種のエージェント CLI をひとつのコンソールで** — Claude Code / Codex /
  GitHub Copilot / Antigravity / Cursor / OpenCode のセッションを並べて実行。
  セッション毎のモデル選択に対応し、CLI の版は動作検証済みの組み合わせにピン止め
  （self-update は opt-in）。
- **実 git リポジトリ上の並行セッション** — HTTPS（GitHub / Bitbucket の
  トークンまたは OAuth デバイスフロー）で clone。**Git LFS・submodule
  （ネスト含む）・git worktree に対応**。1 リポジトリに複数セッションを
  worktree 分離で並走させ、各会話はライブミラーで追跡。ターミナルアクセス、
  実行中の入力キュー投入、エージェントセッションと並ぶ素の
  **shell セッション**も。**Subversion にも対応** — URL＋基本認証で
  チェックアウト（サブパス / 複数 path のチェックアウト、自己署名証明書の
  サーバ単位信頼（opt-in）、作業コピーロックの自動復旧）。
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
- **定時実行** — アシスタントに自然言語で定期的なエージェント実行を仕込めます
  （「毎平日9時に昨日の変更をレビュー」など）。コントロールプレーンが
  壁時計（cron / 間隔 / 単発、タイムゾーン・DST 対応）で発火し、**停止中の
  ワークスペースを起こして**プロンプトを実行し、結果を報告します——誰も見て
  いない時間帯の作業も自動で回ります。長寿命セッションを再利用して実行間で
  文脈を積み上げることも、毎回新規で始めることも可能。スケジュール一覧と
  実行毎の履歴（各実行が動かしたセッション付き）は左レールから確認できます。
- **チャットブリッジ（Discord / Slack）** — Console から Bot を接続（トークン
  貼付のガイド付きウィザード）すると、セッション毎のスレッドに進捗が届きます:
  応答あり・質問・プラン承認・許可リクエスト・異常終了・完了報告。スレッドへの
  返信でそのセッションを操縦、質問やプラン承認はボタンで回答、@メンションで
  フリート・オペレーターにフリート全体の操作を頼むことも — チャット発の
  破壊的操作は承認 / 却下ゲートで一旦停止します。opt-in の全文モードは
  エージェントの応答本文そのものを投稿（秘密情報は自動伏字化）。
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
- 更新も同じコマンドです（データは `~/.local/share/agent-fleet` にあり、触りません）。`af update` でその場更新も可能です。
- **自動更新:** インストーラは日次の systemd user timer を有効化し、`af update` で最新版を *stage*（sha256 検証）します。走行中サービスは再起動しません — 適用は都合の良いときに `systemctl --user restart agent-fleet` か Console の「再起動して適用」で行い、実行中セッションを不意に切りません。無効化は `AF_NO_AUTOUPDATE=1 bash install.sh`。
- 版を指定する場合: `AF_VERSION=0.1.0 bash install.sh`（unit の `Environment=` に置けば自動更新もその版で停止）
- 詳細（ホスト要件・air-gap 導入・常駐化・VOICEVOX / ずんだもんによる読み上げ
  （任意）・制約）は tar 同梱の `README.md` を参照してください。

### private リポジトリのクローン（git プロバイダ OAuth）

各利用者は自分の GitHub / Bitbucket を Console（**⚙設定 → 「Git」**）から接続します。
アクセストークン（PAT）を貼り付ける方式はデプロイ側の設定なしで動きます。加えて
ワンクリックの **「OAuth で接続」** ボタンを有効にするには、クライアント資格情報を
**`af start` の前に**環境変数で渡します（`af` がそれを control plane へ引き渡します）:

| プロバイダ | 変数 | 補足 |
|---|---|---|
| **GitHub**（device flow） | `GITHUB_OAUTH_CLIENT_ID` | OAuth App を作り **「Enable Device Flow」を ON**。client_id は**秘密ではありません**。コールバック URL 不要なので `localhost` でもそのまま動きます。 |
| **Bitbucket**（auth code） | `BITBUCKET_OAUTH_KEY`, `BITBUCKET_OAUTH_SECRET`, `PUBLIC_BASE_URL` | consumer の Callback URL を `<PUBLIC_BASE_URL>/api/oauth/bitbucket/callback` と完全一致で登録します。 |

```bash
GITHUB_OAUTH_CLIENT_ID=<your-client-id> af start
```

systemd 常駐時は `[Service]` に `Environment=` 行として追加します。これらは OAuth
ボタンを点けるだけで、**未設定でもトークン貼付は使えます**（OAuth は利便性のため）。

フォアグラウンドの `af start` の代わりにサービス常駐させる場合（WSL2 は systemd が
既定で有効）— `~/.config/systemd/user/agent-fleet.service` を作成:

```ini
[Unit]
Description=Agent Fleet (native)

[Service]
ExecStart=%h/.local/bin/af start
Restart=on-failure

[Install]
WantedBy=default.target
```

```bash
systemctl --user daemon-reload && systemctl --user enable --now agent-fleet
systemctl --user status agent-fleet          # 期待: Active: active (running)
loginctl enable-linger "$USER"               # WSL セッションを閉じても常駐させる
```

`%h` はホームディレクトリに展開されます。`ExecStart` のパスは上のワンライナー導入
（`~/.local/bin/af` への symlink）に一致します。フォアグラウンドの `af start` が
残っていると port 8099 の bind に失敗するので先に止めてください。

## Docker Compose 版の導入（チーム・オンプレ）

イメージはレジストリに**公開していません**。そのためバンドル
（`agent-fleet-<版>.tar.gz`）とイメージ tar（`agent-fleet-images-<版>.tar.gz`）を
[Releases](https://github.com/k-k1/agent-fleet-dist/releases) に添付しています。
ヘルパーが両方を取得・検証し、バンドルを展開してイメージを `docker load` します:

```bash
curl -fsSL https://raw.githubusercontent.com/k-k1/agent-fleet-dist/main/install-compose.sh | bash
cd agent-fleet-<版>
cp .env.example .env     # 秘密・ドメイン・Google OAuth を記入（下記も参照）
docker compose up -d
```

native 版と違い**完全なワンライナーにはなりません**: `docker compose up` の前に
`.env`（秘密・`PUBLIC_DOMAIN`・Google OAuth・任意で下記の git プロバイダ OAuth 変数）を
編集する必要があります。版を固定するなら `AF_VERSION=<版>` を前置し、`AF_SKIP_IMAGES=1`
で大きいイメージ tar の DL を省けます（別途ファイル受け渡しする場合）。手動が良ければ
2 つの tar を DL 後、`sha256sum -c --ignore-missing SHA256SUMS`・`tar xzf`・`./load-images.sh`。

同梱の `README.md` が完全な runbook です: 前提条件・鍵生成・TLS / ドメイン設定・
git プロバイダ OAuth（`.env` の `GITHUB_OAUTH_CLIENT_ID` / `BITBUCKET_OAUTH_KEY` /
`BITBUCKET_OAUTH_SECRET`）・バックアップ / リストア・アップグレード・トラブルシュート。

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
- 一方、自分でビルドする **全焼き込み Docker イメージ**（既定
  `BAKE_AGENT_CLIS=1`）は、これらエージェント CLI の本体を同梱します。
  **自組織内での利用は問題ありませんが、そのイメージを再配布しないでください**
  （公開レジストリへの push、`docker save` したイメージ tar の第三者への配布 等）
  — プロプライエタリ CLI の再配布に当たります。第三者へイメージを渡す必要がある
  場合は lean 構成（`BAKE_AGENT_CLIS=0`）でビルドしてください（本配布物の
  イメージ・rootfs はこの lean 構成です）。
- 同梱 OSS の帰属は各 tar 内の `NOTICE` を参照してください。
