# HANDOFF — 次セッションへの引き継ぎ

このファイルは**このホストの稼働状態・実行の作法・落とし穴・現在地**に絞った引き継ぎメモ。
機能仕様の正は **[dev/](dev/README.md)（開発者向け）とコード**、利用者の操作は **[guide/](guide/README.ja.md)**、
時系列の作業ログは [CHANGELOG-handoff.md](CHANGELOG-handoff.md)、前向きの計画は [roadmap](roadmap.md)、
意思決定は [decisions/](decisions/)、使い終わった実装プランは [log/](log/README.md)。
**まず読む順**: この HANDOFF（§1〜§3）→ [dev/01 アーキテクチャ](dev/01-architecture.md) → §4 の現在地。

## 1. いま動いているもの（このホスト）

- **Control Plane**: `:8099` で稼働中（React+Vite Console（`console/dist`）+ REST/WS プロキシ + Docker Runtime）。バイナリ `/tmp/af-cp`。Console の作りは [dev/02](dev/02-console.md)。
- **形態**: **shared（`AUTH=oauth`）でライブ稼働**（2026-06-29 刷新, commit `0d8ce10`）。**CP が Google OAuth を内蔵**（oauth2-proxy + Caddy は廃止）。`authGate` が署名セッション cookie を検証して email を解決（[dev/07 §7.3](dev/07-security.md)）。CP は `127.0.0.1:8099` 束縛＝Funnel 経由のみ。設定は git-ignored の `deploy/local/oauth.env`（`AUTH=oauth`/`CP_ADDR=127.0.0.1:8099`/`GOOGLE_OAUTH_CLIENT_ID|SECRET`/`AF_COOKIE_SECRET`/許可リスト）。
- **Workspace コンテナ**: 運用者は `af-ws-k1-kami-gmail-com`（image `agent-fleet/workspace:dev`）。`~`= bind mount `/tmp/af-data/<user>/home`（永続・`/login` 済み）。許可ユーザー追加は `deploy/local/allowed-emails.txt` に1行（メール or `@domain`）→ ログイン毎にライブ反映、その Google ログインで `af-ws-<email>` が自動払い出し（相互不可視: 別 home/別ネットワーク/別トークン）。dev 形態に戻すには oauth.env の `AUTH` 行を外す。
- **外部アクセス**: このホストの **Tailscale Funnel URL**（`https://<tailnet ホスト>.ts.net/`。ルート配信、旧 `/agent-fleet` プレフィクス廃止。Funnel → CP `:8099` 直結。未認証は `/login` → Google → Console）。**実ホスト名はリポジトリに書かない**（公開リポジトリで生きた入口を晒さないため）— 手元で `tailscale funnel status` か `tailscale status --json | jq -r .Self.DNSName` で引く。入口（ingress）の設計は [dev/09 §9.3](dev/09-deploy.md)。
- **イメージ**: `agent-fleet/workspace:dev`（最新, 約2.8G, 焼き込み内容は [dev/04 §4.9](dev/04-workspace-agent.md)）。**Java は image 外**＝ホスト共有 dir `WS_DATA/shared/jvm`（Temurin 8/21/25）を `/usr/lib/jvm:ro` でマウント。

## 2. ツールチェーン / 実行の作法（このホスト固有）

変更の種類ごとの汎用的な反映ルール（早見表）は [dev/10 §10.2](dev/10-development.md) に移した。ここは**このホスト固有の起動作法**のみ。

- **Go**: user-local。`export PATH="$HOME/.local/go/bin:$HOME/go/bin:$PATH"`（go1.26）。
- **Node**: nvm（`~/.nvm/versions/node/v22.23.1`）。ログインシェルで有効。
- **Docker**: `k1` は `docker` グループだが**非ログインシェルでは未反映**。コマンドは `sg docker -c '...'` で実行する（または `sudo docker`）。
- **CP / Console だけ反映（推奨・軽量）**（`control-plane/**` か `console/src/**` を変えたとき）:
  ```bash
  cd ~/workspace-private/agent-fleet
  sg docker -c "./deploy/local/restart-cp.sh"            # console+CP を build → af-cp をその場再起動→ /healthz 検証
  sg docker -c "SKIP_CONSOLE=1 ./deploy/local/restart-cp.sh"   # CP の Go だけ変えたとき（vite build を省く）
  ```
  `restart-cp.sh` は **Workspace イメージを再ビルドしない**（§3 の OOM リスクを避ける軽量経路）。oauth.env を source し、現 `af-cp`（:8099 を握る pid）を kill→新バイナリを setsid で起動し `/healthz=ok` まで待つ。ログは `/tmp/af-cp.log`。**docker グループのあるシェルで**（CP が Workspace start/stop に docker を叩くため。非ログインシェルは `sg docker -c`）。
- **初回起動 / イメージも込みで一括**:
  ```bash
  cd ~/workspace-private/agent-fleet
  pkill -x af-cp 2>/dev/null
  sg docker -c "cd $PWD && nohup ./deploy/local/run-dev.sh > /tmp/af-cp.log 2>&1 &"
  ```
  `run-dev.sh` は イメージ build + CP build + 起動を一括で行い、`deploy/local/oauth.env`（git 管理外）を自動 source して OAuth env を CP に渡す（渡さないと Console の「OAuth 接続」が未設定になり token 貼付にフォールバック）。手動で CP だけ起動して OAuth を効かせるには先に `set -a; . deploy/local/oauth.env; set +a`。
- **CP 停止**: `pkill -x af-cp`（`pkill -f /tmp/af-cp` は自分のシェルも巻き込むので使わない）。
- **Stop→Start の要点**: `start()` は `docker rm -f`→新 image で `docker run`＝確実に新 image。**`docker run` は既に running だと no-op**＝`start` 単独では新 image を反映できない（必ず Stop→Start）。ホーム（`/login`・接続・repos）は永続。走行中コンテナは各**利用者の Stop→Start で随時反映**（強制入れ替えはしない）。

## 3. ⚠️ 最重要の落とし穴（メモリ / フリート）

このホストは `tmux-claude.sh` のライブ claude フリート（現 12〜、`MEM_HIGH=1G/MEM_MAX=2G` の cgroup 上限つき）を抱え、
**ベースラインで RAM がほぼ埋まる**。重い Docker ビルド / コンテナ / `go build` を重ねると **OOM でフリート（と現セッション）が落ちる**
（実際に2回発生）。詳細はメモリ `host-oom-fleet-risk`。

- 重い作業の前に `free -h` で **available 数 GiB** を確保。足りなければフリートを縮小（この会話のセッションは残す → 他を `tmux kill-session`）、後で `/tmux-claude`（冪等 resume）で復帰。
- コンテナは `--memory` を付ける（CP は `WS_MEMORY` 既定 `1g`）。
- 「検証」も負荷源（dry-run が tmux サーバを起こす等）。
- ⚠️ 検証の後始末で `tmux kill-server` や広域の `rm ~/.config/agent-fleet/sessions/*.json` は**運用者の生きたセッションを巻き込む**。対象セッションのみ操作する。

## 4. 現在地とドキュメント地図

**Phase 1 MVP 完了（2026-06-26, `dd2330e`）以降、Phase 2 完了・Phase 3 進行中。** オンプレ 1 台で複数ユーザーが
相互不可視に並行利用でき（per-user Workspace / AuthGateway / ネットワーク分離 / at-rest 暗号化）、Phase 3 の
プロダクト化は P3-1〜P3-7 + Console 全面刷新（React+Vite）まで実装済み（P3-7/P3-10 の実装プランは history/ 入り）。
**P3-10（パッケージング）は dist 配布の publish 運用中**（[docs/35](log/35-packaging.md)・リリースノートは
`deploy/release/notes/`）。残 = P3-8（専用分離）・P3-9 の成熟項目（観測 / egress 統制）・P3-10 の完了ゲート
（第 2 デプロイ E2E）（[roadmap](roadmap.md)）。フェーズごとの実装記録は [log/](log/README.md)、確定事項の背景は decisions/。

- **仕様を知りたい** → [dev/](dev/README.md): アーキテクチャ(01) / Console(02) / Control Plane(03) / Agent(04) /
  API 契約(05) / データモデル(06) / セキュリティ(07) / 外部連携(08) / デプロイ(09) / 開発作法(10) / コードマップ(90)。
- **操作を知りたい** → [guide/](guide/README.ja.md): member / admin / operator / lite の分冊。
- **恒久的に有効な検証知見**: `/login` は localhost 非依存（`redirect_uri=platform.claude.com/oauth/code/callback`）で
  ヘッドレス/リモートに無条件成立、認証と onboarding は別物、`/login` URL 折返し復元 →
  詳細は [dev/08 §8.5](dev/08-integrations.md) と [history/phase1-plan §11.10](log/phase1-plan.md#1110-実装結果と実運用の知見phase-1-完了)。
- **進行中の設計**: egress 統制（[docs/20](log/20-container-audit-egress.md)・enforce 未了）。Go 内部リファクタ
  （[docs/23](log/23-go-refactor.md)）は develop マージ済・残 = ④契約の型化のみ。i18n（[docs/28](log/28-i18n.md)）は
  Console 側 P0〜P5 ＋ P6（エージェント出力言語）まで完了・残 = 実フリート再ビルド後の実機目視。
  **新しく prompt を足すときの判断基準は
  [§6.6](log/28-i18n.md#66-p6-エージェント出力言語完了) の判定表と地雷。**

## 5. 動作確認の最短手順

```bash
# CP が落ちていたら §2 の手順で起動
curl -s http://127.0.0.1:8099/api/workspace            # {"state":"running"|"stopped"}
# ブラウザ: Funnel URL（`tailscale funnel status` で確認）  (ハードリロード。未認証は /login → Google)
#   Start
#   設定→接続: [Claude 接続]→URL承認→コード貼付 / [GitHub 接続]→PAT or Device Flow / [Bitbucket]→email+token or OAuth
#   Repos: clone URL→Clone（private は上の git 接続が前提）
#   New session（shell 既定 / claude は接続済なら追加ログイン不要）
# 旧来の手動経路: 端末で claude → /login（⧉ sign-in URL でURL取得）も併用可
```

利用者視点の詳しい手順は [use/01-first-day.ja.md](use/01-first-day.ja.md)。

## 6. コミット規約

**正は [CONTRIBUTING.md](../CONTRIBUTING.md#commits--prs)**（形式・日本語・帰属トレーラ）。
このホスト固有の要点だけ:

- **develop がトランク**（日常開発は develop へ直 push / 随時マージ・「完了」= develop マージ済。
  `main` は develop→main の PR トレインのみで更新——[CONTRIBUTING](../CONTRIBUTING.md#commits--prs) 参照）。
  GitHub リモート = `git@github.com:k-k1/agent-fleet.git`。
- コミットは `<type>(<scope>): 日本語要約` ＋末尾に**実行モデル名**の共同著者を付ける
  （Claude Code は `Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>` 等、版に合わせる。
  Codex/opencode は実行モデルで帰属）。旧 `Claude-Session:` 行は廃止。
