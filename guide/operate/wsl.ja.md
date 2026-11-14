---
audience: "Windows の WSL2 で、Agent Fleet を 1 人で試す人"
source_of_truth: "`deploy/local/run-dev.sh`（食い違ったらこのページのバグ）"
---

# WSL で個人用に動かす

[English](wsl.md) | 日本語

Windows の WSL2 で、Agent Fleet を 1 人で試せるように、コマンド 1 つで立ち上げます。
サインインもテナントの選択もなく、ワークスペースは WSL の中の Docker で動きます。

## 全体像

| あなたがやること | 起きること |
|---|---|
| WSL2 のディストリビューションに Docker Engine・Go・Node を入れる | 起動スクリプトでビルドできるようになる |
| `deploy/local/run-dev.sh wsl` を実行する | ワークスペースのイメージ・Console・Control Plane をビルドし、Control Plane をホストで起動する |
| Windows のブラウザで `http://localhost:8099` を開く | サインインなしで Console が開く |

本番向けの Docker Compose と Caddy の構成は、公開ドメインと 80・443 番ポートが要るので、個人で試すのには使いません。

### 省いているもの

| 項目 | この構成での扱い |
|---|---|
| サインイン | `AUTH=dev` の固定ユーザー `dev` です。サインインの画面も oauth2-proxy もなく、すべてのリクエストが `dev` になります。スクリプトが `AF_ALLOW_INSECURE_DEV_AUTH=1` も渡します |
| テナント | `default` のテナントが内部で作られ、`dev` が自動で入ります。所属が 1 つなので、テナントを選ぶ画面は出ません |
| 暗号化 | `AF_MASTER_KEY` を設定しないので、保存時の暗号化はオフです。シークレットは平文の JSON で保存されます。個人で試すぶんには許容範囲です |
| ランタイム | `AF_RUNTIME=local` です。ワークスペースのコンテナは WSL の中の Docker で起動し、本番の形態と同じくコンテナの隔離とワークスペースごとの cgroup の上限があります |

コードを変える必要はありません。どれも `run-dev.sh wsl` の既定の振る舞いです。

## 1. 必要なものを入れる

WSL2 のディストリビューションの中に入れます。Docker は、**WSL2 の中に直接入れた Docker Engine** をおすすめします。
Docker Desktop の連携でも動きますが、この構成はホストの Docker と同じ名前空間とパスを前提にしているので、直接入れるほうが合います。

| 入れるもの | 補足 |
|---|---|
| Docker Engine（dockerd） | 下のコマンドで入れます |
| cgroup v2 | `--memory` の上限とリソースの表示がこれに依存します。最近の WSL2 は既定で v2 です |
| Go | Control Plane をホストでビルドします。https://go.dev/dl/ から入れて PATH に足します |
| Node | Console を Vite でビルドします。nvm をおすすめします |

```bash
curl -fsSL https://get.docker.com | sh          # docker-ce を入れる
sudo usermod -aG docker "$USER"                 # 以後は sudo なしで docker を使える
sudo service docker start                        # WSL では systemd がなくても `service` が使える
# 新しいシェルを開く。`docker info` が通れば準備完了
```

- systemd を有効にすると（`/etc/wsl.conf` の `[boot]` に `systemd=true`）、`dockerd` が自動で起動し、手で起動する手間が省けます

```bash
stat -fc %T /sys/fs/cgroup     # cgroup2fs と出れば OK
```

- `cgroup2fs` と出なければ、Windows の側で `wsl --update` を実行して WSL を新しくします

```bash
curl -fsSL https://raw.githubusercontent.com/nvm-sh/nvm/v0.40.1/install.sh | bash
. ~/.nvm/nvm.sh && nvm install --lts
```

## 2. 起動する

```bash
git clone <this-repo> && cd agent-fleet
deploy/local/run-dev.sh wsl        # 前の wsl-quickstart.sh はこれを呼ぶだけ（今も使える）
```

スクリプトがすることは次のとおりです。

1. 事前チェックをします（docker に接続できるか、cgroup v2、go、npm）
2. 共有の JDK を `~/.local/share/agent-fleet/shared/jvm` に 1 回だけ展開し、bind mount で渡します（`WS_JDK=0` で省けます）
3. ワークスペースのイメージをビルドします
4. Console（Vite）と Control Plane（Go）をビルドします
5. `AUTH=dev`・`AF_RUNTIME=local` で Control Plane を起動します

- 初回は、イメージのビルドと JDK の展開に時間がかかります
- イメージはエージェントの CLI を含まない軽量版です。CLI は、ワークスペースの初回の起動のときに決められたバージョンが入ります。`BAKE_AGENT_CLIS=1` を付けると、CLI をイメージに組み込んでビルドし、初回の起動を速くできます
- 古い `deploy/local/wsl-quickstart.sh` は、同じものを呼ぶだけの互換のためのスクリプトです

`run-dev.sh` は、サブコマンドで動作を選びます。

| サブコマンド | 使いどころ |
|---|---|
| `wsl` | この構成 |
| `local` | 開発の既定（Docker） |
| `native` | Docker を入れられないとき（下の [Docker を入れられないとき](#docker-を入れられないとき)） |
| `reset` | データを消す（下の [止める・消す・ビルドし直す](#止める消すビルドし直す)） |

## 3. ブラウザで開く

Control Plane は `http://localhost:8099` で待ち受けます。WSL2 の localhostForwarding で、**Windows の側のブラウザから** そのまま開けます。
開いたら、Console でリポジトリをクローンし、Claude のセッションを始めます。

## GitHub とつなぐ

GitHub のクローンとプッシュを OAuth のデバイスフローで使うには、GitHub の OAuth App の `client_id` を Console に登録します。環境変数は使いません。

1. GitHub（Settings → Developer settings → OAuth Apps）で OAuth App を作り、**Enable Device Flow をオン**にします
2. Console のテナント設定で、その `client_id` を登録します（[連携に使う OAuth アプリを選ぶ](../admin/access/oauth-apps.ja.md)）。`client_id` はシークレットではありません
3. ワークスペースから GitHub の連携を始めます。デバイスコードと確認の URL が案内されます。`gh auth login` は要りません

- この構成は 1 人に固定で、`AUTH` は常に `dev` です。`oauth.env` に `AUTH=oauth` と書いても使われません
- 固定ユーザーは配備管理者（super_admin）なので、テナント設定の画面に入れます
- トークン（PAT）を貼り付けても動きます。そのときは何も登録しなくてかまいません

## rtk と JDK

| 項目 | 扱い |
|---|---|
| rtk（トークンを節約する Bash のフック） | ワークスペースの起動のときに、決められたバージョンを入れます（`BAKE_AGENT_CLIS=1` ならイメージに組み込んでビルドします）。フックはエントリポイントが自動で用意します |
| JDK | 既定は、イメージを軽く保つ **共有の bind mount**（`WS_JVM_DIR`）です |

どの環境でも、コンテナの中から JDK を入れられます。

```bash
workspace-agent install-jdk 21     # 最新の GA の Temurin を ~/.local/share/agent-fleet/jvm に入れる
```

- Console の **設定 → ツールチェーン** で Java のバージョンを選ぶと、足りないバージョンを次のコンテナの起動で入れ、各セッションに `JAVA_HOME` を渡します（[ツールチェーンを選ぶ](../user/workspace/toolchains.ja.md)）
- `WS_JDK=0` で起動すると、共有の JDK を用意せず、この入れ方だけに頼ります
- エージェントの CLI と rtk を最新版に追従させる設定も同じタブにあります（[エージェントの CLI を最新にする](../user/workspace/toolchains.ja.md#エージェントの-cli-を最新にする)）

## 止める・消す・ビルドし直す

| やりたいこと | 操作 |
|---|---|
| 止める | フォアグラウンドの Control Plane を `Ctrl-C` します。起動したワークスペースのコンテナは残るので、必要なら `docker ps`・`docker stop <name>` で止めます |
| データの場所 | `~/.local/share/agent-fleet`（DB・ユーザーごとの home・共有の JDK） |
| 消す | Control Plane を止めてから `deploy/local/run-dev.sh reset`。既定では `dev` のワークスペース（home と claude-config）だけを消し、DB と共有の JDK は残します。`--all` で DB と共有の JDK まで消します（JDK は次の実行で用意し直します） |
| ビルドし直す | CLI のバージョンを上げたときなどは、`run-dev.sh wsl` をもう一度実行します。イメージがビルドし直されます |

`reset` は、消す前にどちらのランタイムの残り（コンテナ・Agent のプロセス・専用の tmux）も片付けるので、どの形で動かしていても安全です。

## Docker を入れられないとき

WSL2 にどうしても Docker を入れられないときは、ワークスペースをコンテナなしでホストのプロセスとして動かせます（`AF_RUNTIME=native`、1 人だけ、試験的）。

```bash
# tmux / git / claude などの CLI はホストに入れておく（何も同梱されない）
deploy/local/run-dev.sh native
```

- コンテナの隔離・メモリの上限・エントリポイントの初期化（claude の自動インストールなど）はありません。承知のうえでのトレードオフです
- ワークスペースの HOME は `~/.local/share/agent-fleet/dev/home` に分かれていて、自分の `~` には触れません。Windows のエクスプローラーから `\\wsl.localhost\<distro>\...` で見えます
- Docker を使えるなら、[1. 必要なものを入れる](#1-必要なものを入れる) の構成を選びます
- ビルドツールを入れずに Docker なしで使うなら、[ネイティブパッケージ](native/install.ja.md) もあります

## うまくいかないとき

| 症状 | 見るところ |
|---|---|
| `cannot reach the docker daemon` | `sudo service docker start`・`usermod -aG docker` のあと、ログインし直します |
| cgroup が v2 でないという警告 | Windows の側で `wsl --update` を実行して、WSL のカーネルを新しくします |
| Console のビルドがメモリ不足で落ちる | `NODE_OPTIONS=--max-old-space-size=3072`（スクリプトが設定済みです）。メモリが厳しいときは、ほかのビルドを止めます |
| `go`・`npm` がない | [1. 必要なものを入れる](#1-必要なものを入れる) のとおりに入れ、PATH を直します（スクリプトは nvm を自動で読み込みます） |
| Java が見つからない | `ls -d /usr/lib/jvm/temurin-*-jdk* ~/.local/share/agent-fleet/jvm/temurin-*-jdk*`。空なら `workspace-agent install-jdk <major>` |
| エージェントの選択肢に `agy` がない | ホストの CPU が使える RDRAND を出しておらず（`grep -w rdrand /proc/cpuinfo` が空）、回避もうまくいきませんでした。agy は RDRAND が要る FIPS のビルドで、カーネルが命令を取り下げているときは Agent が OpenSSL の CPU 検出から外して動かします。それでも出ないのは、外した状態の `agy --version` も失敗したか、`AF_AGY_RDRAND_MASK=0` でこの回避を無効にしているからです |

## 関連

- [ネイティブパッケージを入れる](native/install.ja.md)
- [Docker Compose で構築する](compose/install.ja.md)
- [デプロイ先を選ぶ](choose.ja.md)
