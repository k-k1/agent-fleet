---
audience: "ネイティブパッケージの Agent Fleet を、ターミナルを閉じても動き続けるようにしたい人"
source_of_truth: "`deploy/native/af` と配布リポジトリの `install.sh`（食い違ったらこのページのバグ）"
---

# systemd でサービスとして動かす

[English](service.md) | 日本語

`af start` をフォアグラウンドで動かし続ける代わりに、systemd の**ユーザー**サービスにします。
落ちたら再起動し、シェルを閉じても動き続けます。WSL2 では systemd が既定で有効です。

## 全体像

| あなたがやること | 起きること |
|---|---|
| unit ファイルを書く | `af start` がユーザーサービスになる |
| `systemctl --user enable --now` で有効にする | いますぐ起動し、ログインのたびに起動する |
| 必要なら linger を有効にする | WSL のセッションを閉じても動き続ける |

## 1. unit を書く

`~/.config/systemd/user/agent-fleet.service` を次の内容で作ります。

```ini
[Unit]
Description=Agent Fleet (native)

[Service]
ExecStart=%h/.local/bin/af start
Restart=on-failure
# Lets the Console's "restart to apply" button (auto-update, below) find this
# unit; %N expands to the unit name (agent-fleet).
Environment=AF_SYSTEMD_UNIT=%N

[Install]
WantedBy=default.target
```

- `%h` はホームディレクトリです。`ExecStart` のパスは、インストーラーで入れたとき（`~/.local/bin/af` のリンク）のものです
- tar を手で展開したときは、展開した場所の `af` を指定します。例: `ExecStart=%h/agent-fleet-native-<v>-linux-<arch>/af start`。本当のパスは `readlink -f "$(command -v af)"` でわかります
- `AF_SYSTEMD_UNIT` は、Console の「**再起動して適用**」がこの unit を見つけるためのものです（[ネイティブパッケージを更新する](update.ja.md#用意したバージョンに切り替える)）
- 読み上げ・画像・エンジンを借りるための環境変数は、`[Service]` に `Environment=` の行で足します（例: `Environment=AF_VOICEVOX_URL=...`）

## 2. 有効にする

フォアグラウンドの `af start` がまだ動いていたら、先に止めます。動いていると、サービスがポート 8099 に bind できずに失敗します。

```bash
systemctl --user daemon-reload && systemctl --user enable --now agent-fleet
systemctl --user status agent-fleet          # Active: active (running) と出れば OK
```

## 3. 閉じても動き続けるようにする

WSL のセッションを閉じたあとも動かしておくなら、linger を有効にします。

```bash
loginctl enable-linger "$USER"
```

## ログを見る

```bash
journalctl --user -u agent-fleet -f
```

## うまくいかないとき

| 症状 | 見るところ |
|---|---|
| `systemctl --user` が `Failed to connect to bus` と出る | ユーザーの systemd が動いていません。WSL2 では `/etc/wsl.conf` の `[boot]` に `systemd=true` を書き、`wsl.exe --shutdown` してから開き直します |
| サービスが起動してすぐ失敗する | フォアグラウンドの `af start` が残っていて、ポート 8099 が使われていないか |
| `ExecStart` のコマンドが見つからない | tar を手で展開したなら、`readlink -f "$(command -v af)"` のパスに書き換えます |

## 関連

- [ネイティブパッケージを入れる](install.ja.md)
- [ネイティブパッケージを更新する](update.ja.md)
- [リセット・アンインストールする](uninstall.ja.md)
