---
audience: "Docker を使わずに、1 人用の Linux（WSL2 を含む）で Agent Fleet を動かす人"
source_of_truth: "`deploy/native/af` と配布リポジトリの `install.sh`（食い違ったらこのページのバグ）"
---

# ネイティブパッケージを入れる

[English](install.md) | 日本語

Docker のない 1 人用の Linux（WSL2 を含む）に Agent Fleet を入れ、ブラウザから使い始めます。
ホストに何もインストールせずに動きます。tmux・git・node・エージェントの CLI をホストに入れる必要はありません。

## 全体像

| あなたがやること | 起きること |
|---|---|
| インストーラを実行するか、tar を展開する | `af` コマンドが使えるようになる |
| `af start` を実行する | 初回だけ rootfs（約 200 MB）を取得・検証・展開し、Control Plane と Console がホストのプロセスとして動き出す |
| ブラウザで `http://localhost:8099` を開く | サインインなしで Console が開く。ワークスペースは bubblewrap のサンドボックスで動く |
| ワークスペースを初めて起動する | エージェントの CLI を、決められた版で仮想の HOME に入れる |

複数の人で使うときや、コンテナの隔離と CPU・メモリの上限が要るときは、Docker を使う形態（[Docker Compose で構築する](../compose/install.ja.md) など）を選びます。

## 始める前に

| 要るもの | 補足 |
|---|---|
| Linux のカーネル（特権のないユーザー名前空間） | WSL2 の標準のカーネルは AppArmor が無効なので、そのまま動くはずです。Ubuntu 23.10 以降は下の設定が要ることがあります |
| bash・coreutils・tar | 標準のものだけです。zstd は `bin/zstd` として同梱しているので、ホストには要りません |
| curl か wget | 初回の rootfs の取得にだけ使います（[手で持ち込む](update.ja.md#手で持ち込む) も参照） |
| （`agy` を使うなら）RDRAND を出す CPU | `agy`（Antigravity CLI）の FIPS のビルドは、RDRAND がないと止まります。カーネルが命令を取り下げているときは Agent が OpenSSL の検出から外して動かし、乱数はカーネルから取ります。`AF_AGY_RDRAND_MASK=0` にすると、それを断り、Console が `agy` を隠します |

ユーザー名前空間がまったく無効なカーネルでは、ネイティブパッケージは動きません。Docker の形態（`agent-fleet-<v>.tar.gz`）を使います。
macOS には、ネイティブパッケージはありません。Apple Silicon の Mac では Linux の VM の中で動かします（[macOS（Apple Silicon）で動かす](../compose/macos.ja.md#linux-の-vm-でネイティブパッケージを動かす)）。

### Ubuntu 23.10 以降で AppArmor が止めるとき

WSL ではないふつうの Ubuntu 23.10 以降では、AppArmor が特権のないユーザー名前空間を制限していることがあります。そのときは 1 回だけ実行します。

```bash
sudo sysctl -w kernel.apparmor_restrict_unprivileged_userns=0
# make it permanent:
echo 'kernel.apparmor_restrict_unprivileged_userns=0' | sudo tee /etc/sysctl.d/60-agent-fleet.conf
```

## 1. インストールする

配布リポジトリ（[k-k1/agent-fleet-dist](https://github.com/k-k1/agent-fleet-dist)）のインストーラを使うと、`~/.local/opt/agent-fleet/<v>/` に展開し、`~/.local/bin/af` にリンクを張ります。

```bash
curl -fsSL https://raw.githubusercontent.com/k-k1/agent-fleet-dist/main/install.sh | bash
af start            # first run only: fetch, verify, and extract the rootfs (~200MB)
# open http://localhost:8099 in a browser
```

| 環境変数 | 意味 |
|---|---|
| `AF_VERSION` | 入れる版。既定は最新のリリースです |
| `AF_PREFIX` | インストール先の接頭辞。既定は `~/.local` です |
| `AF_NO_AUTOUPDATE` | `1` にすると、毎日の自動更新のタイマーを有効にしません（[ネイティブパッケージを更新する](update.ja.md)） |

tar を直接受け取ったときは、展開してその中の `af` を使います。`<arch>` は `amd64` か `arm64` で、`uname -m` の `x86_64`・`aarch64` に合わせます。

```bash
tar xzf agent-fleet-native-<v>-linux-<arch>.tar.gz
cd agent-fleet-native-<v>-linux-<arch>
./af start          # first run only: fetch, verify, and extract the rootfs (~200MB)
# open http://localhost:8099 in a browser
```

## 2. 起動して開く

- 初回の `af start` は、`rootfs.json` に書かれた rootfs を URL と sha256 で取得します。**2 回目からはオフラインで起動できます**（展開した rootfs を使い回します）
- `af start` はフォアグラウンドで動きます。止めるときは Ctrl-C です。サービスとして動かすときは [systemd でサービスとして動かす](service.ja.md) にします
- ブラウザで `http://localhost:8099` を開きます。WSL2 なら、Windows の側のブラウザからそのまま開けます
- 動いているかは `af status` で確かめられます。Control Plane とワークスペースが動いているかを表示します

サインインの画面はありません。`AUTH=dev` の固定のユーザー 1 人で動き、そのユーザーは配備管理者（super_admin）です。
テナントの設定の画面にも、`af start` の直後から入れます。

### エージェントの CLI

エージェントの CLI はパッケージに入っていません。

| CLI | 入るとき |
|---|---|
| claude・codex・opencode・cursor・copilot・agy・rtk | ワークスペースの初回の起動で、`versions.json` で決まった版を仮想の HOME に入れます。ネットワークが要るのはこのときだけです |
| kiro（約 855 MB） | 初めて Kiro のセッションを起動したとき |
| Muse Code | 頼むまで入りません。接続のカードの「**Muse Code を導入**」で、決められた版をホームに入れます |

## データの置き場所

| 中身 | 場所 |
|---|---|
| すべてのデータ（DB・ワークスペースの home・claude-config・展開した rootfs） | `~/.local/share/agent-fleet`。`WS_DATA` で変えられます |
| パッケージそのもの | 展開したディレクトリ。データは持たないので、消してもかまいません |

- 配置は Docker の形態と同じなので、同じ `WS_DATA` を docker と native の間で移せます
- Windows のエクスプローラーからは `\\wsl.localhost\<distro>\home\<user>\.local\share\agent-fleet` で見えます

## リポジトリを OAuth でつなぐ

非公開のリポジトリをクローン・プッシュするには、Console の **設定 → Git ホスティング** から GitHub か Bitbucket をつなぎます。
トークンを貼り付けるだけなら、何も設定しなくても動きます。

ワンクリックの「OAuth で接続」も使いたいときは、OAuth アプリを Console のテナントの設定で登録します（[連携に使う OAuth アプリを選ぶ](../../admin/access/oauth-apps.ja.md)）。環境変数はありません。

| プロバイダ | 入れるもの | 用意のしかた |
|---|---|---|
| GitHub（デバイスフロー） | `client_id` だけ | GitHub → Settings → Developer settings → OAuth Apps で、**Enable Device Flow をオン**にした OAuth App を作ります。`client_id` は秘密ではありません。デバイスフローはコールバックの URL が要らないので、`localhost` でもそのまま動きます |
| Bitbucket（認可コード） | Key と Secret | **Callback URL** が `<PUBLIC_BASE_URL>/api/oauth/bitbucket/callback` と完全に一致する OAuth consumer を作ります。`PUBLIC_BASE_URL` は Console を開く URL（`http://localhost:8099` など）にします。貼り付ける URL は画面に出ます |

1 人で使うネイティブの配備では、コールバックの要らない GitHub のデバイスフローがいちばん簡単です。

## Docker の形態との違い

| 違い | 内容 |
|---|---|
| 1 人だけ | `AUTH=dev` に固定です。コンテナの隔離はなく、すべてのワークスペースが同じ OS のユーザーの下で、bubblewrap のサンドボックスで動きます |
| メモリの上限がない | cgroup を使わないので、`WS_MEMORY` は効きません。重いビルドでホストごと落ちることがあります |
| ブラウザペインの Chromium は初回に取得する | 決められた版を約 200 MB 取得します。下の「Chromium のサンドボックスが動かないとき」 |

### Chromium のサンドボックスが動かないとき

bubblewrap の下では SUID のサンドボックスを使えず、環境によっては Chromium の名前空間のサンドボックスも動きません。そのときは次のように起動します。

```bash
WS_ENV=AF_CHROMIUM_NO_SANDBOX=1 af start
```

- この変数は `WS_ENV`（カンマ区切りの `KEY=VAL` の並び）でワークスペースに渡します。`af start` にそのまま付けると、Control Plane にしか届きません
- ローカルホストにしかつながないブラウザペインの用途なら許せるトレードオフです。信頼できないサイトを見るのには使わないでください

## 任意で足せるもの

| やりたいこと | ページ |
|---|---|
| 返信を読み上げる（VOICEVOX） | [読み上げエンジンを動かす](../engines/read-aloud.ja.md) |
| 自分のネットワークの ComfyUI で画像を生成する | [自前の ComfyUI で画像を生成する](../engines/comfyui.ja.md) |
| AWS の Agent Fleet の推論エンジンを借りる | [別のデプロイのエンジンを借りる](../engines/borrow.ja.md) |
| LAN の llama.cpp でチャットする | [LAN の llama.cpp でチャットする](../engines/llama-cpp.ja.md) |

どれも `af start` に環境変数を付けるか、systemd の unit の `Environment=` に書きます。`af` はシェルの環境を Control Plane にそのまま渡します。

## うまくいかないとき

| 症状 | 見るところ |
|---|---|
| `af start` がユーザー名前空間のエラーで止まる | Ubuntu 23.10 以降なら [AppArmor の設定](#ubuntu-2310-以降で-apparmor-が止めるとき)。カーネルで無効なら Docker の形態を使います |
| ブラウザペインの Chromium が起動しない | [Chromium のサンドボックスが動かないとき](#chromium-のサンドボックスが動かないとき) |
| ポート 8099 が使えない | 別の `af start` か、systemd のサービスがすでに動いています |

## 関連

- [ネイティブパッケージを更新する](update.ja.md)
- [systemd でサービスとして動かす](service.ja.md)
- [リセット・アンインストールする](uninstall.ja.md)
- [WSL で個人用に動かす](../wsl.ja.md)
