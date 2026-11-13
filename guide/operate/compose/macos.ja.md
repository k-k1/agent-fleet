---
audience: "Apple Silicon の Mac で Agent Fleet を 1 人で動かす人"
source_of_truth: "`deploy/compose/docker-compose.macos.yml` と `deploy/macos/acceptance.sh` の確認項目（食い違ったらこのページのバグ）"
---

# macOS（Apple Silicon）で動かす

[English](macos.md) | 日本語

Apple Silicon の Mac で、Agent Fleet を 1 人で使えるように動かします。
Docker のエンジン（colima・OrbStack・Docker Desktop）で動かすか、Linux の VM の中でネイティブパッケージを動かすかを選べます。

## 全体像

| あなたがやること | 起きること |
|---|---|
| `.env` に Mac 用の値を書く | Control Plane が 1 人用（`AUTH=dev`）で起動する |
| 重ね合わせのファイル `docker-compose.macos.yml` を足して起動する | Control Plane がブリッジネットワークで動き、`host.docker.internal` でワークスペースに届く |
| Mac のブラウザで `http://127.0.0.1:8099` を開く | Console が開き、ワークスペースを起動できる |

対象と対象外は次のとおりです。

| 対象 | 対象外 |
|---|---|
| Apple Silicon の Mac で、1 人で使う | Intel の Mac（動かしたことがありません） |
| colima・OrbStack・Docker Desktop | チームや公開の配備（この構成には Caddy も IdP もありません） |
| Lima・OrbStack のマシンの中のネイティブパッケージ | Apple の `container` ツール（Docker のデーモンではないので、Control Plane が動かせません） |

## Docker のエンジンで動かす

イメージは `linux/arm64` でも公開されていて、Mac は合うほうを取ります。Linux と違うのは、コンテナから Docker を動かすときの配線です。

colima・OrbStack・Docker Desktop のそれぞれで、ワークスペースの起動、`linux/arm64`、共有ディレクトリの home の所有者と権限、headless Chromium、停止→起動で home が残ることを確かめてあります。

- colima は `colima start --vm-type vz --mount-type virtiofs` で起動したものです
- OrbStack と Docker Desktop は、Mac にログインした画面から 1 回起動しておく必要があります。そのあとは、どの端末からでも下のコマンドが動きます

### 1. `.env` を書く

[Docker Compose で構築する](install.ja.md#1-設定を書いてソースから起動する) の秘密に加えて、次を書きます。

```bash
AUTH=dev                                  # one fixed user, no IdP
AF_ALLOW_INSECURE_DEV_AUTH=1              # required: the CP refuses AUTH=dev on Docker without it
AF_PROVISION=auto                         # the template's `invite` leaves that user in no tenant
PUBLIC_BASE_URL=http://127.0.0.1:8099
AF_TRUSTED_PROXY_HOPS=0                   # no Caddy in front; the template's 1 assumes one
VERSION=<version>                         # the template's `dev` is not a published CP tag
WS_IMAGE=ghcr.io/k-k1/agent-fleet/workspace:<version>
# (B) a directory the engine shares with its VM — under your home works for all three
DATA_DIR=/Users/<you>/agent-fleet/data
# (C) the socket's group as the engine reports it, not a host `docker` group
#     (measured: 991 on colima, 0 on OrbStack and Docker Desktop):
#   docker run --rm -v /var/run/docker.sock:/var/run/docker.sock alpine stat -c %g /var/run/docker.sock
DOCKER_GID=<that number>
```

| 値 | 理由 |
|---|---|
| `DATA_DIR` | エンジンが VM と共有するディレクトリに置きます。ホームの下なら 3 つのエンジンのどれでも共有されます |
| `DOCKER_GID` | ホストの `docker` グループではなく、エンジンが見せるソケットのグループです。colima では 991、OrbStack と Docker Desktop では 0 でした。エンジンごとに測り直します |

### 2. 重ね合わせを足して起動する

```bash
docker compose -f docker-compose.yml -f docker-compose.macos.yml up -d
```

Mac のブラウザで `http://127.0.0.1:8099` を開きます。

### 重ね合わせが変えること

[コンテナから Docker を動かす 3 つの制約](../diagnose/checklist.ja.md#コンテナから-docker-を動かす-3-つの制約) のうち、Mac で変わるところです。

| 制約 | Mac での扱い |
|---|---|
| A. ホストのネットワーク | Mac ではデーモンが Linux の VM の中で動くので、「ホスト」は VM です。重ね合わせは Control Plane をブリッジネットワークに置き、`host.docker.internal`（`WS_AGENT_HOST`）でワークスペースに届きます。ワークスペースの中の `127.0.0.1` はワークスペース自身を指すので、ワークスペースには同じ名前で [ワークスペース専用のリスナー](workspace-listener.ja.md) を渡します。2 つのポートは Mac のループバックにだけ公開されます |
| B. `DATA_DIR` の絶対パス | home は、エンジンが VM と共有するディレクトリに置かれます。colima と virtiofs では、ファイルは uid 1000 と権限（`~/.ssh` は 700、新しいファイルは 600）を保ち、書き込みもコンテナ自身のディスクより遅くありませんでした。ほかのエンジンのファイル共有は振る舞いが違うことがあります |
| C. ユーザーと docker グループ | 上の `DOCKER_GID` |

まだ確かめていない別の方法もあります。Docker Desktop 4.34 以降はホストネットワーキングを有効にでき（Settings → Resources → Network）、OrbStack は `network_mode: host` に対応しています。これを使えば、`docker-compose.yml` をそのまま使えるかもしれません。

## Linux の VM でネイティブパッケージを動かす

macOS には bubblewrap も Linux のユーザー名前空間もなく、ワークスペースの rootfs は Linux です。そのため macOS 用のパッケージはなく、`install.sh` は Mac では実行を断ります。

1. Linux の VM を用意します（Lima、OrbStack のマシン（`orb create ubuntu`）など）
2. VM の中で `linux-arm64` のパッケージを入れ、`af start` します（[ネイティブパッケージを入れる](../native/install.ja.md)）
3. Mac のブラウザから、VM のポートフォワードで `http://localhost:8099` を開きます

- Lima の Ubuntu LTS のテンプレートと、OrbStack のマシンで、インストール・起動・ワークスペースの起動・停止→起動を確かめてあります。OrbStack のマシンのポートは、Mac の `localhost` にも届きます
- Ubuntu の VM は、ほかの Ubuntu 23.10 以降と同じく、特権のないユーザー名前空間を制限しています。先に [AppArmor の設定](../native/install.ja.md#ubuntu-2310-以降で-apparmor-が止めるとき) をしておきます

## うまくいかないとき

| 症状 | 見るところ |
|---|---|
| Control Plane が `AUTH=dev` を拒んで起動しない | `AF_ALLOW_INSECURE_DEV_AUTH=1` |
| サインインしても、どのテナントにも入れない | `AF_PROVISION=auto` |
| docker のソケットで permission denied | `DOCKER_GID` を、使っているエンジンで測り直したか |
| エンジンを切り替えたら、前のデータが見える・ポートが衝突する | エンジンごとに `DATA_DIR` を分け、前のエンジンを止めてから `docker context use <名前>` で切り替えます |

## 関連

- [Docker Compose で構築する](install.ja.md)
- [ワークスペースから CP に届かないとき](workspace-listener.ja.md)
- [ネイティブパッケージを入れる](../native/install.ja.md)
