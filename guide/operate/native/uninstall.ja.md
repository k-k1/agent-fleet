---
audience: "ネイティブパッケージの Agent Fleet のデータを消す人、または取り除く人"
source_of_truth: "`deploy/native/af` の `reset`（食い違ったらこのページのバグ）"
---

# リセット・アンインストールする

[English](uninstall.md) | 日本語

ネイティブパッケージのデータを消してやり直すか、Agent Fleet をホストから取り除きます。
プログラムとデータは別の場所にあるので、どちらか一方だけを消すこともできます。

## 全体像

| あなたがやること | 起きること |
|---|---|
| `af reset` を実行する | 固定のユーザーの home と claude-config（Claude のログインを含む）が消える。DB と rootfs は残る |
| `af reset --all` を実行する | `WS_DATA` がまるごと消える。DB・すべての home・claude-config・展開した rootfs |
| unit・データ・プログラムを順に消す | Agent Fleet がホストから取り除かれる |

## データを消す

Control Plane を止めてから実行します。動いていると `af reset` は断ります。

```bash
./af reset          # dev user's data only (DB and rootfs are kept)
./af reset --all    # all of WS_DATA (full wipe incl. the extracted rootfs)
```

- 消す前に確かめます。`--yes` を付けると聞きません
- インストーラで入れたときは、`./af` の代わりに `af` で実行できます

## アンインストールする

```bash
# 1. If you set up the systemd user unit, stop and remove it first
systemctl --user disable --now agent-fleet 2>/dev/null
systemctl --user disable --now agent-fleet-update.timer 2>/dev/null   # auto-update timer
rm -f ~/.config/systemd/user/agent-fleet.service \
      ~/.config/systemd/user/agent-fleet-update.service \
      ~/.config/systemd/user/agent-fleet-update.timer
# otherwise just stop `af start` with Ctrl-C

# 2. Wipe all data (skip to keep it — data is separate from the program)
./af reset --all

# 3. Remove the program
#    installed via install.sh:
rm -f ~/.local/bin/af && rm -rf ~/.local/opt/agent-fleet
#    extracted from a tar directly: just delete the extracted directory
```

| 手順 | 補足 |
|---|---|
| 1. unit を止めて消す | systemd を使っていなければ、`af start` を Ctrl-C で止めるだけです |
| 2. データを消す | データを残すなら飛ばします |
| 3. プログラムを消す | tar を直接展開したなら、そのディレクトリを消すだけです |

### `af` がもうないとき

データを手で消します。ワークスペースの home の中の Go のモジュールのキャッシュは書き込みが禁止されているので、先に書き込みの権限を戻します。

```bash
chmod -R u+w ~/.local/share/agent-fleet && rm -rf ~/.local/share/agent-fleet
```

`WS_DATA` を変えていたなら、そのパスにします。

## 関連

- [ネイティブパッケージを入れる](install.ja.md)
- [systemd でサービスとして動かす](service.ja.md)
