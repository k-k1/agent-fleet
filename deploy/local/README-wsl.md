# WSL で個人利用（認証なし・単一ユーザーで即起動）

Windows の **WSL2** 上で、agent-fleet を**1人の検証用**にすぐ立ち上げるための runbook。
ログイン画面もテナント選択もなし（`AUTH=dev` の固定ユーザー `dev`）、ワークスペースは
WSL 内の Docker で起動します。本番向けの Compose + Caddy(自動TLS) 構成（`deploy/compose/`）は
公開ドメイン/80・443 が要るので個人検証には使いません。

起動は `deploy/local/wsl-quickstart.sh` 一本。以下はその前提づくりです。

---

## 0. 全体像（何が「省かれて」いるか）

- **認証**: `AUTH=dev`（既定）。OAuth ゲートも oauth2-proxy もかからず、全リクエストが
  固定ユーザー `dev` に解決されます。ログイン不要。
- **テナント**: 内部的に「default」テナントが1個だけ自動生成され、`dev` が自動所属します。
  UI 上でテナントを意識することはありません（複数所属時のみ選択が要る仕組みなので、
  1人なら常に自動選択）。
- **暗号化**: `AF_MASTER_KEY` 未設定 → at-rest 暗号化オフ（シークレットは平文 JSON）。個人検証前提。
- **ランタイム**: `AF_RUNTIME=local` → WSL 内 Docker でワークスペースコンテナを起動。

コードを削る必要はありません。これらは `run-dev.sh` / `wsl-quickstart.sh` の既定挙動です。

## 1. 前提ソフト（WSL2 ディストロ内に入れる）

推奨は **WSL2 ディストロ内に native な Docker Engine を直接入れる**構成です（Docker Desktop 連携でも
動きますが、この構成は `network_mode` を使わない代わりにホスト Docker と同一名前空間・同一パスを
前提にするため、native の方が素直）。

- **Docker Engine（native dockerd）**
  ```bash
  curl -fsSL https://get.docker.com | sh          # docker-ce を導入
  sudo usermod -aG docker "$USER"                 # 以後 sudo 無しで docker
  sudo service docker start                        # WSL では systemd 無しでも service で起動可
  # 新しいシェルを開き直して: docker info が通ればOK
  ```
  systemd を有効化しておくと `dockerd` が自動起動して楽です（`/etc/wsl.conf` に `[boot]\nsystemd=true`）。
- **cgroup v2**（メモリ上限 `--memory` とリソース表示が依存）
  ```bash
  stat -fc %T /sys/fs/cgroup     # => cgroup2fs なら OK
  ```
  近年の WSL2 は既定で v2。古い場合は WSL を更新（`wsl --update`）してください。
- **Go**（Control Plane をホストビルド） … https://go.dev/dl/ から入れて PATH に通す。
- **Node**（Console の Vite ビルド） … nvm 推奨。
  ```bash
  curl -fsSL https://raw.githubusercontent.com/nvm-sh/nvm/v0.40.1/install.sh | bash
  . ~/.nvm/nvm.sh && nvm install --lts
  ```

## 2. 起動

```bash
git clone <this-repo> && cd agent-fleet
deploy/local/wsl-quickstart.sh
```

スクリプトがやること:
1. preflight（docker 疎通・cgroup v2・go・npm）を確認。
2. 共有 JDK を `~/.local/share/agent-fleet/shared/jvm` に一度だけ展開し、bind-mount で提供（`WS_JDK=0` で省略可、§4）。
3. ワークスペースイメージを **rtk 同梱**でビルド（`--build-arg BAKE_RTK=1`）。
4. Console(Vite) と Control Plane(Go) をビルド。
5. `AUTH=dev` / `AF_RUNTIME=local` で CP を起動。

初回はイメージビルド（Chromium/各種CLI焼き込み）と JDK 展開で時間がかかります。

### GitHub 連携（デバイスフロー）

GitHub の clone/push を OAuth デバイスフローで通したい場合は、`GITHUB_OAUTH_CLIENT_ID` を
渡します（client_id は**非秘密**。デバイスフローに必要なのはこれだけ）。

1. GitHub で OAuth App を作成（Settings → Developer settings → OAuth Apps）。
   **「Enable Device Flow」を ON** にする。
2. ひな型をコピーして client_id を記入（このファイルは git-ignore 済み）:
   ```bash
   cp deploy/local/oauth.env.example deploy/local/oauth.env
   # deploy/local/oauth.env を編集し GITHUB_OAUTH_CLIENT_ID=<your-client-id> を設定
   ```
3. `wsl-quickstart.sh` を再実行。起動時に `deploy/local/oauth.env` を自動で読み込み、
   `GITHUB_OAUTH_CLIENT_ID` を CP 経由でワークスペースへ注入します（起動ログに
   `loaded .../oauth.env（… client_id: 設定あり）` と出ます）。
4. Console からワークスペースで GitHub 連携を開始すると、デバイスコードと認証 URL が
   案内されます。`gh auth login` は不要です（透過認証ラッパーがトークンを注入）。

この WSL プリセットは単一ユーザー固定のため **`AUTH` は常に `dev`** です（`oauth.env` に
`AUTH=oauth` を書いても採用しません＝ログイン認証は変えません）。`oauth.env` から読むのは
git プロバイダ用の `GITHUB_OAUTH_CLIENT_ID` / `BITBUCKET_OAUTH_KEY,SECRET` / `PUBLIC_BASE_URL`
だけです。token 貼り付け（PAT）でも連携でき、その場合は client_id 不要です。

## 3. ブラウザで開く

CP は `http://localhost:8099` で待ち受けます。WSL2 の localhostForwarding により、
**Windows 側のブラウザからそのまま `http://localhost:8099`** を開けます。
そこから Console でリポジトリを clone し、Claude セッションを起動してください。

## 4. rtk と JDK の扱い

- **rtk**（Bash のトークン節約フック）: ホスト側 vendoring はやめ、**イメージのビルド時に
  コンテナ内へダウンロード**（`BAKE_RTK=1` / `RTK_VERSION` は `workspace/Dockerfile` の既定と一致）。
  静的バイナリ1個なのでイメージへの影響は誤差。entrypoint が `rtk` の有無を見てフックを自動 seed します。
- **JDK**: 既定は**共有 bind-mount**（`WS_JVM_DIR`）でイメージを太らせません。加えて、どの環境でも
  コンテナ内から後入れできます:
  ```bash
  workspace-agent install-jdk 21     # 最新 GA Temurin を ~/.local/share/agent-fleet/jvm に導入
  ```
  Console の**ツール設定（toolchains）で Java 版を選ぶ**と、次回コンテナ起動時に未導入分を
  entrypoint が自動でこの場所へ入れ、`JAVA_HOME` を各セッションへ通します。`WS_JDK=0` で起動すると
  共有 provision を省き、この on-demand 導入だけに寄せられます。

## 5. 停止・後片付け・再ビルド

- 停止: フォアグラウンドの CP を `Ctrl-C`。起動済みワークスペースコンテナは残るので
  必要なら `docker ps` / `docker stop <name>`。
- データ: `~/.local/share/agent-fleet`（DB・各ユーザー home・共有JDK）に永続。消せば初期化。
- 再ビルド: CLI 版などを上げたら `wsl-quickstart.sh` を再実行（イメージ再ビルド）。

## 6. Docker を入れられない場合（実験的: ネイティブ実行）

どうしても Docker を導入できない WSL2 では、コンテナを使わずワークスペースを
**ホストプロセス**として動かせます（`AF_RUNTIME=native`・単一ユーザー専用・実験的）:

```bash
# tmux / git / claude 等の CLI をホストに入れておく（イメージ焼き込みが無いため）
AF_RUNTIME=native deploy/local/run-dev.sh
```

コンテナ隔離・メモリ上限・entrypoint 初期化（claude 自動インストール等）が無い、という
割り切りの構成です。詳細と制約は [docs/34-native-runtime.md](../../docs/34-native-runtime.md)。
Docker が入るなら §1 の構成（`wsl-quickstart.sh`）を推奨します。

## 7. トラブルシュート

| 症状 | 確認 |
|------|------|
| `docker daemon に接続できない` | `sudo service docker start` / `usermod -aG docker` 後に再ログイン |
| cgroup が v2 でない警告 | `wsl --update`（Windows 側）で WSL カーネル更新 |
| Console ビルドが OOM | `NODE_OPTIONS=--max-old-space-size=3072`（スクリプトは設定済み）。メモリ逼迫時は他ビルドを止める |
| `go`/`npm` が無い | §1 で導入し PATH を通す（nvm はスクリプトが自動 source） |
| Java が見つからない | `ls -d /usr/lib/jvm/temurin-*-jdk* ~/.local/share/agent-fleet/jvm/temurin-*-jdk*`、無ければ `workspace-agent install-jdk <major>` |
| エージェント選択に `agy` が出ない | ホスト CPU が RDRAND 非提示（`grep -w rdrand /proc/cpuinfo` が空）。agy は FIPS ビルドで RDRAND 必須のため意図的に非露出（[0008](../../docs/decisions/0008-antigravity-cli-agent-kind.md)） |

デプロイ形態と env 索引は [docs/dev/09-deploy.md](../../docs/dev/09-deploy.md)、本番 Compose 手順は
[deploy/compose/README.md](../compose/README.md) を参照。
