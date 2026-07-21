# agent-fleet-dist

[English](README.md) | 日本語

[Agent Fleet](https://github.com/k-k1/agent-fleet) の**配布物置き場**です。
ソースコードはここにはありません。成果物は Releases に添付されています。

## Agent Fleet とは

Agent Fleet は、AI コーディングエージェント（Claude Code / Codex CLI / OpenCode /
GitHub Copilot CLI / Antigravity CLI）をフリートとして運用するためのセルフホスト型
Web コンソールです。メンバーごとに隔離されたワークスペース — cgroup による CPU/
メモリクォータ付きの Docker コンテナ（native 版では bubblewrap サンドボックス上の
rootfs）と永続ホーム・git 作業コピー — が割り当てられ、ブラウザからエージェント
セッションを起動・操作・監視できます。ワークスペースは Go 製のコントロールプレーンが
統括し、デプロイ先はオンプレ Docker Compose / AWS ECS（CloudFormation テンプレート
同梱）/ WSL2・単一ユーザー Linux 向けの Docker 不要 native ランタイムに対応します。

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

- 配布イメージ・rootfs は **lean 構成**です: エージェント CLI（Claude Code / Codex /
  OpenCode / Copilot / Antigravity）は同梱せず、初回起動時に各利用者がそれぞれの
  配布元から検証済みのピン版を取得します（再配布を行わないための構成です）。
- 同梱 OSS の帰属は各 tar 内の `NOTICE` を参照してください。
