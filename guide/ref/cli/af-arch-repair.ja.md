---
audience: "CPU のアーキテクチャが変わったあと af-arch-repair が何を戻し、何を残すかを調べたい人"
source_of_truth: "workspace/af-arch-repair.sh（/usr/local/bin/af-arch-repair として入るもの）"
---

# af-arch-repair

[English](af-arch-repair.md) | 日本語

ワークスペースが別の CPU アーキテクチャ（`amd64` ↔ `arm64`）のマシンに移ったあと、ホームに自分で入れたものを
同じ版で入れ直します。ワークスペースが、その起動の中で自動で実行します。手で実行するのは、やり直すときと、
まだ壊れているものを確かめるときだけです。直せなかったものは、Console の通知で知らせます。

Agent Fleet 自身が入れるツール（エージェントの CLI、選んだ JDK と Node.js、Chromium）は、af-arch-repair ではなく
ワークスペースの起動が入れ直します。

## 書式

```sh
af-arch-repair <was> <now>
```

`<was>` と `<now>` は、前といまのアーキテクチャで、`amd64` か `arm64` です。`<was>` はメッセージに出るだけです。
`<now>` がないと `usage: af-arch-repair <was> <now>` を出して、終了コード 2 で終わります。

```sh
af-arch-repair amd64 arm64
```

## 入れ直すもの

| もの | 対象 | 方法 |
|---|---|---|
| `pip install --user` のパッケージ | 別のアーキテクチャ用にビルドされたネイティブ拡張を含むもの。純 Python のものには触りません | `pip install --user --force-reinstall --no-deps <名前>==<版>`。`--no-deps` で依存はそのまま残すので、新しい版に変わるものはありません |
| `uv tool` のツール | 環境の中に、別のアーキテクチャ用のネイティブ拡張があるもの | `uv tool install --force --reinstall <ツール>==<版>` |
| `~/.local` に `npm install -g` したパッケージ | アーキテクチャが変わった起動の最初に、ワークスペースが `~/.local/lib/node_modules` を消す前に控えたもの | `npm install -g --prefix ~/.local <名前>@<版>...` |

npm の一覧があるのは、アーキテクチャが変わった起動の間だけです。手で実行したときに npm のパッケージを入れ直すのは、
前の試みが失敗して一覧が残っているときだけです。

## 知らせるだけのもの

次のものには触らず、名前を出して知らせます。

| もの | 触らない理由 | 直しかた |
|---|---|---|
| `~/repos` の下のビルド生成物。別のアーキテクチャ用のバイナリを含む `node_modules`、`target`、`.venv` | 未コミットの作業がある場所だから | `npm ci`、`cargo clean && cargo build`、`.venv` の作り直し |
| `~/.local/bin` に自分で置いた、別のアーキテクチャ用のプログラム | 出どころが分からないから | ダウンロードかビルドをやり直します |

af-arch-repair は、ディレクトリがあるかどうかではなく、バイナリそのものを調べます。純 JavaScript の `node_modules` は
知らせず、直したものは知らせなくなります。

## 出力

どの行も `[arch-repair]` で始まります。

```
[arch-repair] pip: 2 件を同じ版で入れ直します: greenlet==3.0.3 PyYAML==6.0.1
...
[arch-repair] pip: 完了
[arch-repair] ⚠️ ~/repos のビルド生成物が amd64 用のままです（~/repos は触っていません）:
[arch-repair] ⚠️  shop/node_modules
[arch-repair] ⚠️  直す: node_modules → npm ci ／ target → cargo clean && cargo build ／ .venv → 作り直し
[arch-repair] 完了
```

入れ直しに失敗すると、手で実行するコマンドを書いた `WARN:` の行を出します。

ワークスペースの起動の中では、この出力は起動のログに出るので、見ることはできません。見えるのは
「**自動で戻せなかったものがあります**」という通知で、残っている `~/repos` のディレクトリと `~/.local/bin` のプログラムが
並びます。通知の元は af-arch-repair が書くファイル `~/.local/share/agent-fleet/arch-residue` で、いま残っているものが入り、
なくなれば消えます。同じ内容の通知は、2 度は出ません。

## やり直し

| 失敗したもの | やり直すタイミング |
|---|---|
| npm のパッケージ | 次の起動。入れ直しが成功するまで一覧が残ります |
| pip のパッケージ、uv のツール | af-arch-repair がもう一度実行されたときだけ。手で実行するか、npm もやり直す起動のときです。`WARN:` の行のコマンドか、`af-arch-repair <was> <now>` を実行してください |

## 環境変数

| 変数 | 効果 |
|---|---|
| `AF_REPAIR_PY=0` | pip の入れ直しを飛ばします。同じ起動で Python のメジャー版も変わったときに、ワークスペースが設定します。新しい Python には同じ版のパッケージがないことがあるためです |

## 終了コード

| 値 | 意味 |
|---|---|
| 0 | 実行した。入れ直しに失敗したときも含みます（出力に出ます） |
| 2 | `<now>` がない |

## 関連

- [マシンと使用状況を見る](../../user/workspace/machine.ja.md)：ワークスペースのアーキテクチャ
- [ツールチェーンを選ぶ](../../user/workspace/toolchains.ja.md)
