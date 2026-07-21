# agent-fleet-dist

[Agent Fleet](https://github.com/k-k1/agent-fleet)（private）の**配布物置き場**です。
ソースコードはここにはありません。成果物は Releases に付いています。

## native 版（Docker 不要・WSL2 / Linux 単一ユーザー向け）の導入

```bash
curl -fsSL https://raw.githubusercontent.com/k-k1/agent-fleet-dist/main/install.sh | bash
af start
# ブラウザで http://localhost:8099
```

- インストーラは最新リリースの tar を取得し、`SHA256SUMS` で検証してから
  `~/.local/opt/agent-fleet/<版>/` へ展開、`~/.local/bin/af` に symlink します。
- 更新も同じコマンドです（データは `~/.local/share/agent-fleet` にあり、触りません）。
- 版を指定する場合: `AF_VERSION=0.1.0 bash install.sh`
- 詳細（ホスト要件・air-gap・常駐化・制約）は tar 同梱の `README.md` を参照。

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
  OpenCode ほか）は同梱せず、初回起動時に各利用者がそれぞれの配布元から検証済みの
  ピン版を取得します（再配布を行わないための構成です）。
- 同梱 OSS の帰属は各 tar 内の `NOTICE` を参照してください。
