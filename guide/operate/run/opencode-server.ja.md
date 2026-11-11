---
audience: "opencode のターミナルセッションのサーバーと版を管理する人"
source_of_truth: "Workspace Agent の opencode の起動のコード、`workspace/Dockerfile` の `OPENCODE_VERSION`"
---

# opencode の共有サーバーを運用する

[English](opencode-server.md) | 日本語

opencode のターミナル（CLI）セッションは、ワークスペースごとに 1 つの共有サーバーにつながります。
メモリより分離を取りたいときはセッションごとの専用サーバーに切り替えられ、版に問題があったときは以前の版に固定できます。

## 全体像

| あなたがやること | 起きること |
|---|---|
| 何もしない | ターミナルのセッションは、Workspace Agent が動かす 1 つの opencode サーバーを共有する |
| `AF_OPENCODE_TUI_STANDALONE=1` を入れて Workspace Agent を起動する | 以後に起動するターミナルのセッションが、それぞれ専用のサーバーで動く |
| 2.x の以前の版を指定してイメージをビルドし直す | 問題のある版を避けられる |

メンバーから見た違いは [OpenCode を接続する](../../user/agents/opencode.ja.md#知っておくこと) にあります。

## 専用サーバーに切り替える

次を環境に入れて Workspace Agent を起動します。

```
AF_OPENCODE_TUI_STANDALONE=1
```

- 1 セッションあたり、メモリがおよそ 150〜250 MB 増えます（アイドルのときの値）
- この設定で Agent が起動したあとに起動するターミナルのセッションに効きます。動いているセッションは、起動したときのやりかたのままです
- `AF_OPENCODE_SERVE_DISABLE=1`（マネージドの opencode を無効にする）でも、つなぐ共有サーバーがないので専用サーバーになります
- 専用サーバーのペインでは、Console に質問カードが出ません。質問はそのペイン自身のサーバーにあり、Agent はそれを読まないためです。ターミナルで答えます

## 以前の版に固定する

イメージは opencode 2 の版に固定されています（`workspace/Dockerfile` の `OPENCODE_VERSION`）。Agent Fleet がサポートするのは opencode 2 だけです。

1. 2.x の以前の版を指定して、イメージをビルドし直します（`--build-arg OPENCODE_VERSION=<2.x>`）
2. ワークスペースを停止して、起動します

2.x の版は、どれも同じデータベースを読めます。

> [!WARNING]
> **opencode 1 には戻せません。** opencode 1 からは opencode 2 で作ったものが見えず、Agent Fleet も opencode 1 を扱いません。

## opencode 1 から移ったワークスペース

| 状況 | 起きること |
|---|---|
| 最後に opencode 1 で使っていた | opencode 2 の最初の起動で `~/.local/share/opencode/opencode.db` がその場で変換され、opencode 1 のセッションが同じ ID のまま取り込まれます。どれだけ前に使ったワークスペースでも、Agent Fleet は取り込みが終わるのを待ってから再開します |
| 以前に `workspace-agent opencode-major 2` で試していた | 専用のインストール（`~/.local/share/agent-fleet/opencode2`）と専用のデータベース（`~/.local/share/agent-fleet/opencode2-data`）が残っています。どちらも読まれません |

専用のデータベースにしかないセッションは、メインのデータベースに**統合されていません**。いらなくなったら、2 つのディレクトリを消してください。Agent Fleet は自動では消しません。

## 関連

- [OpenCode を接続する](../../user/agents/opencode.ja.md)
- [アップグレードする](upgrade.ja.md)
