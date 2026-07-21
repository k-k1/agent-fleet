# Agent Fleet — native パッケージ（Docker 不要・単一ユーザー）

素の WSL2（または任意の Linux・単一ユーザー運用）で、**ホストに何も追加インストール
せず** Agent Fleet を動かすパッケージです。ワークスペースは同梱の rootfs
（workspace イメージと同一のユーザーランド）を bubblewrap の無特権実行で動かすため、
tmux / git / node / 各エージェント CLI をホストへ入れる必要はありません。

## クイックスタート

配布 repo（[k-k1/agent-fleet-dist](https://github.com/k-k1/agent-fleet-dist)）からの
ワンライナー導入（`~/.local/opt/agent-fleet/<v>/` へ展開・`~/.local/bin/af` に symlink）:

```bash
curl -fsSL https://raw.githubusercontent.com/k-k1/agent-fleet-dist/main/install.sh | bash
af start            # 初回のみ rootfs（200MB台）を取得・検証して展開
# ブラウザで http://localhost:8099
```

tar を直接もらった場合:

```bash
tar xzf agent-fleet-native-<v>-linux-amd64.tar.gz
cd agent-fleet-native-<v>-linux-amd64
./af start          # 初回のみ rootfs（200MB台）を取得・検証して展開
# ブラウザで http://localhost:8099
```

- 初回の `af start` が rootfs.json のピン（URL + sha256）に従い rootfs をダウンロード
  します。**2 回目以降はオフラインで起動**します（展開済み rootfs を再利用）。
- エージェント CLI（claude / codex / opencode / agy / copilot / rtk）は焼き込まれて
  いません。ワークスペースの初回起動時に entrypoint が versions.json のピン版
  （動作検証済みの版）を仮想 HOME へ自動導入します（この時だけネットワークが必要）。
- 停止は Ctrl-C（フォアグラウンド実行）。常駐化は下記の systemd user unit を参照。

## ホスト要件

| 要件 | 備考 |
|---|---|
| Linux カーネル（unprivileged user namespaces） | WSL2 標準カーネルは AppArmor 無効のためそのまま動く見込み。下記注記参照 |
| bash / coreutils / tar | 標準ユーザーランドのみ。zstd は同梱 `bin/zstd` を使うため**ホスト不要** |
| curl または wget | rootfs の初回ダウンロードにのみ使用（air-gap は下記） |

**Ubuntu 23.10+ の実機（非 WSL）**では unprivileged userns が AppArmor で制限されて
いる場合があります。その場合は一度だけ:

```bash
sudo sysctl -w kernel.apparmor_restrict_unprivileged_userns=0
# 恒久化:
echo 'kernel.apparmor_restrict_unprivileged_userns=0' | sudo tee /etc/sysctl.d/60-agent-fleet.conf
```

userns 自体が無効なカーネルでは native 版は動きません（Docker 構成
`agent-fleet-<v>.tar.gz` を使ってください）。

## データの場所

| 内容 | 場所 |
|---|---|
| 全データ（DB / ワークスペース home / claude-config / 展開済み rootfs） | `~/.local/share/agent-fleet`（`WS_DATA` で変更可） |
| パッケージ本体 | この展開ディレクトリ（データは持たない — 消しても安全） |

レイアウトは Docker 構成と互換です（同じ `WS_DATA` を docker ⇄ native で行き来
できます）。Windows の Explorer からは
`\\wsl.localhost\<ディストロ>\home\<user>\.local\share\agent-fleet` で参照できます。

## 更新

```bash
tar xzf agent-fleet-native-<v'>-linux-amd64.tar.gz   # 新版を展開
cd agent-fleet-native-<v'>-linux-amd64 && ./af start
```

- データ（`WS_DATA`）はパッケージの外にあるため触れません。DB migration は起動時
  自動・前方のみ（**ダウングレード非対応**）。更新前に `WS_DATA` の tar バックアップを
  推奨します。
- rootfs の版 `<r>` が変わらないリリースでは再ダウンロードは発生しません。
- 旧パッケージディレクトリは不要になったら削除してください。

## air-gap / ファイル渡し

- self-contained 版（`--bundle-rootfs` で生成された `-bundle` tar）は rootfs を同梱
  しており、ダウンロードなしで起動できます。
- 通常版でも、別ホストで取得した rootfs tar を `./af start --rootfs <tar.zst>` で
  渡せます（sha256 検証は同様に行われます）。
- CLI の初回自動導入だけはネットワークが必要です。完全オフラインで CLI まで必要な
  場合はリポジトリの Dockerfile で自社ビルド（`BAKE_AGENT_CLIS=1`）した Docker 構成を
  使ってください（ライセンス上、CLI 焼き込み版の再配布はできません — docs/35 §35.4.1）。

## systemd user unit（常駐化・WSL2 は systemd 既定有効）

`~/.config/systemd/user/agent-fleet.service`:

```ini
[Unit]
Description=Agent Fleet (native)

[Service]
ExecStart=%h/agent-fleet/af start
Restart=on-failure

[Install]
WantedBy=default.target
```

```bash
systemctl --user daemon-reload && systemctl --user enable --now agent-fleet
```

## 制約（Docker 構成との差分）

- **単一ユーザー限定**（AUTH=dev 固定）。コンテナ隔離は無く、全ワークスペースが
  同一 OS ユーザーの bubblewrap サンドボックス内で動きます。
- **メモリ上限なし**（cgroup を使わないため `WS_MEMORY` は無効）。重いビルドは
  ホストごと巻き込みます。
- **ブラウザペインの chromium は初回利用時ダウンロード**（約 200MB・ピン版）。
  bubblewrap 配下では SUID sandbox が使えないため、環境によっては chromium の
  namespace sandbox が動かないことがあります。その場合は `AF_CHROMIUM_NO_SANDBOX=1`
  を設定して起動してください（ペイン用途・接続先 localhost 限定の割り切り。
  信頼できないサイトの閲覧には使わないでください）。

## リセット

```bash
./af reset          # dev ユーザーのデータのみ（DB・rootfs は温存）
./af reset --all    # WS_DATA 全体（展開済み rootfs 含む完全初期化）
```
