---
audience: "ネイティブパッケージで動く Agent Fleet を新しい版に上げる人"
source_of_truth: "`deploy/native/af` の `update` と配布リポジトリの `install.sh` の更新タイマー（食い違ったらこのページのバグ）"
---

# ネイティブパッケージを更新する

[English](update.md) | 日本語

ネイティブパッケージを新しい版に上げます。新しい版を用意するところまでは自動で、切り替えるのは都合のよいときに自分で再起動したときです。

## 全体像

| あなたがやること | 起きること |
|---|---|
| 何もしない（インストーラがタイマーを有効にできたとき） | 毎日のタイマーが `af update` を実行し、新しい版を取得・検証して、今の版の隣に用意する。タイマーがなければ、自分で `af update` を実行する |
| 都合のよいときに再起動する | 新しい版に切り替わる。動いていたセッションは中断される |
| 版を固定したいときは `AF_VERSION` を書く | 自動の更新がその版より先に進まない |

`WS_DATA` の下のデータには触れません。新しいワークスペースのイメージ（rootfs）は、次の起動のときに必要なら取得します。

## 新しい版を用意する

`af update` は最新のリリース（`AF_VERSION` があればその版）を取得し、sha256 で検証し、今の版の隣に置いてから `~/.local/bin/af` を付け替えます。
**動いている Control Plane は再起動しません。** 新しい版は再起動したときから使われるので、動いているエージェントのセッションが勝手に切れることはありません。

```bash
af update --check   # report whether a newer release exists (no download)
af update           # download + verify + stage the latest (or AF_VERSION-pinned)
```

- `--yes` を付けると、取得してよいかを聞きません
- `af update` は、インストーラで入れた配置（`~/.local/bin/af` のリンク）でだけ使えます。tar を手で展開したものでは断られるので、下の [tar で入れ替える](#tar-で入れ替える) にします

### 毎日のタイマー

インストーラは、`af update` を毎日実行する **ユーザーのタイマー**（`agent-fleet-update.timer`）を有効にします。用意するだけで、切り替えはしません。

- 有効にするのは、`systemctl` があり、ユーザーの systemd に接続できるときだけです。有効にできたときは、インストーラが `auto-update: daily 'af update' timer enabled` と表示します
- 表示されなかったとき（WSL で systemd が無効など）は、タイマーはありません。上の `af update` を自分で実行して更新します。WSL で systemd を有効にする方法は [systemd でサービスとして動かす](service.ja.md#うまくいかないとき) にあります
- インストールのときに断るなら `AF_NO_AUTOUPDATE=1` を付けます
- あとから止めるときや、次に動く時刻を見るときは次のとおりです

```bash
systemctl --user disable --now agent-fleet-update.timer   # stop auto-staging
systemctl --user list-timers agent-fleet-update.timer     # when it next runs
```

## 用意した版に切り替える

切り替えは、メインのサービスの再起動です。都合のよいときに、どちらかで行います。

| 場所 | 操作 |
|---|---|
| Console | 新しい版が用意されると、**設定 → ツールチェーン** の「**Agent Fleet の更新**」に「**再起動して適用**」が出ます。再起動する前に、動いているセッションの数を示して確かめます |
| シェル | `systemctl --user restart agent-fleet`。フォアグラウンドで動かしているなら Ctrl-C して `af start` |

![「Agent Fleet の更新」。実行中のバージョンは v0.43.0、適用待ちに v0.44.0 と「再起動で適用」のバッジがあり、「更新を適用」の行に「再起動して適用」のボタンがある](../../assets/native-update-ja-light.webp)

> [!WARNING]
> 再起動すると、Control Plane とその下のワークスペースが止まり、動いているエージェントのセッションは中断されます（systemd では unit の cgroup がまるごと止まります）。
> そのため、切り替えは自動にせず、自分で行う操作にしています。

## 版を固定する

`agent-fleet` と `agent-fleet-update` の **両方の** unit に `Environment=AF_VERSION=<v>` を書きます。
`af update` はその版を目標にし、その版になったら何もしません。unit の書き方は [systemd でサービスとして動かす](service.ja.md) にあります。

## tar で入れ替える

tar を手で展開して使っているときは、新しい版を展開して、その中の `af` で起動します。

```bash
tar xzf agent-fleet-native-<v'>-linux-<arch>.tar.gz   # extract the new version
cd agent-fleet-native-<v'>-linux-<arch> && ./af start
```

- データ（`WS_DATA`）はパッケージの外にあるので、そのまま使われます
- DB の移行は起動のときに自動で行われ、前にしか進みません。**ダウングレードはできません**。更新の前に `WS_DATA` を tar でバックアップしておくことをおすすめします
- rootfs の版が同じリリースどうしなら、rootfs を取り直しません
- 古いパッケージのディレクトリは、いらなくなったら消します

## 手で持ち込む

外に出られないホストでは、ファイルを手で持ち込みます。

| 方法 | 内容 |
|---|---|
| rootfs を同梱した版を使う | `--bundle-rootfs` で作った `-bundle` の tar は rootfs を含み、何も取得せずに起動します |
| rootfs の tar を渡す | ふつうの版でも、別のホストで取った rootfs の tar を `./af start --rootfs <tar.zst>` で渡せます。同じ sha256 の検証を行います |

- ネットワークが要るのは、初回のエージェントの CLI の自動インストールだけです
- CLI まで完全にオフラインで使うなら、リポジトリの Dockerfile から `BAKE_AGENT_CLIS=1` で自分でビルドした Docker の形態を使います。CLI を焼き込んだビルドは、ライセンスの都合で配布できません

## 関連

- [ネイティブパッケージを入れる](install.ja.md)
- [systemd でサービスとして動かす](service.ja.md)
- [ツールチェーンを選ぶ](../../user/workspace/toolchains.ja.md#agent-fleet-を更新する)
