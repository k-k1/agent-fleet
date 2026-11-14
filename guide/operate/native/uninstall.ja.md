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
| `af reset` を実行する | 固定ユーザーの home と claude-config（Claude のログインを含む）が消える。DB と rootfs は残る |
| `af reset --all` を実行する | `WS_DATA` がまるごと消える。DB・すべての home・claude-config・展開した rootfs |
| unit・データ・プログラムを順に消す | Agent Fleet がホストから取り除かれる |

## データを消す

Control Plane を止めてから実行します。動いている間は、`af reset` が実行を拒否します。

```bash
./af reset          # dev ユーザーのデータだけ（DB と rootfs は残る）
./af reset --all    # WS_DATA を全部（展開した rootfs も消える）
```

- 消す前に確認を求めます。`--yes` を付けると確認を省きます
- `./af` は、tar を展開したディレクトリで実行するときの形です。インストーラーで入れたときは `af` は `~/.local/bin/af` にあるので、どこからでも `af reset` で実行します

## アンインストールする

```bash
# 1. systemd のユーザー unit を設定したなら、先に止めて消す
systemctl --user disable --now agent-fleet 2>/dev/null
systemctl --user disable --now agent-fleet-update.timer 2>/dev/null   # 自動更新のタイマー
rm -f ~/.config/systemd/user/agent-fleet.service \
      ~/.config/systemd/user/agent-fleet-update.service \
      ~/.config/systemd/user/agent-fleet-update.timer
# 設定していなければ、`af start` を Ctrl-C で止めるだけ

# 2. データを全部消す（残すなら飛ばす。データはプログラムとは別の場所にある）
#    install.sh で入れたとき:
af reset --all
#    tar を直接展開したとき（展開したディレクトリで実行）:
./af reset --all

# 3. プログラムを消す
#    install.sh で入れたとき:
rm -f ~/.local/bin/af && rm -rf ~/.local/opt/agent-fleet
#    tar を直接展開したとき: 展開したディレクトリを消すだけ
```

| 手順 | 補足 |
|---|---|
| 1. unit を止めて消す | systemd を使っていなければ、`af start` を Ctrl-C で止めるだけです |
| 2. データを消す | インストーラーで入れたなら `af reset --all`、tar を展開したなら、そのディレクトリで `./af reset --all` です。プログラムより先に消します。プログラムを先に消すと、データが残ります（下の「`af` がもうないとき」）。データを残すなら飛ばします |
| 3. プログラムを消す | tar を直接展開したなら、そのディレクトリを消すだけです |

### `af` がもうないとき

データを手で消します。ワークスペースの home の中の Go のモジュールキャッシュは書き込み禁止になっているので、先に書き込み権限を戻します。

```bash
chmod -R u+w ~/.local/share/agent-fleet && rm -rf ~/.local/share/agent-fleet
```

`WS_DATA` を変えていたなら、そのパスにします。

## 関連

- [ネイティブパッケージを入れる](install.ja.md)
- [systemd でサービスとして動かす](service.ja.md)
