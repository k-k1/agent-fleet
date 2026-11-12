---
audience: "af-clean のオプション・出力・終了コードを調べたい人"
source_of_truth: "af-clean --help（ワークスペースの workspace-agent に入っているもの）"
---

# af-clean

[English](af-clean.md) | 日本語

端末から home ディスクを空けます。Console の容量不足の通知にある **再生成できるものを掃除…** と同じ一覧を使います。
ワークスペースの Agent に消せるものを尋ね、その分だけを消します。動いているワークスペースの中でだけ使えます。
Console 側の説明は [マシンと使用状況を見る](../../user/workspace/machine.ja.md#home-ディスクの空きを作る) にあります。

## 書式

```sh
af-clean [--dry-run] [--json]
af-clean --apply [--json]
af-clean --help
```

| オプション | 動作 |
|---|---|
| なし、`--dry-run` | 消すものの一覧を出します。何も削除しません |
| `--apply` | ドライランが挙げたものと同じものだけを削除します |
| `--json` | テキストの代わりに JSON を 1 つ出します |

`--apply` と `--dry-run` は同時に指定できません。

## 対象

| 種類 | 消えるもの | 対象になる条件 |
|---|---|---|
| `worktree` | 作業コピーの `node_modules`、`.next`、`target`、`.gradle`（作業コピー自体は残る） | 30 日間誰も触っておらず、未コミット・未追跡の変更がなく、使っているセッションやプロセスもない |
| `cache` | パッケージマネージャーのキャッシュ（go-build、npm、uv、pip、Maven、どのプロジェクトも固定していない Gradle のバージョン） | 中身があり、ビルドやインストールが使っておらず、Agent が対象として出している |

ここから消すのはこれだけです。ごみ箱、セッションのアーカイブ、使われなくなったファイル、`~/.af-work`、home 全体の内訳は
Console にだけ出ていて、それぞれ確認を挟みます。

削除は**元に戻せず、ごみ箱にも入りません**。消したものを次に必要とするビルドやインストールは、一度だけ遅くなります。

## `--apply` が一覧の外に出ない仕組み

- 直前に一覧したものだけを、1 件ずつ要求して消します。Console が頼まないことは頼みません。
- 作業コピーを掃除する直前に、その作業コピーの一覧をもう一度読みます。フォルダが一覧のときと違えば、その項目は `skipped`（「一覧のあとで変わった」）にして、何も消しません。新しい一覧は `af-clean` をもう一度実行して確認します。
- Agent が拒んだもの（動いているセッション、使用中のキャッシュ）は失敗ではなく `skipped` です。

## 出力

```
worktree  my-app-feature-x                    1.2 GiB
            node_modules                      1.1 GiB
            .next                           100.0 MiB
cache     npm                               800.0 MiB
dry run: 2.0 GiB can be freed; nothing was deleted. Run af-clean --apply to remove exactly this.
```

サイズの後ろの `+` は下限値です（計測が上限に達して止まりました）。`--apply` では各行の末尾に
`removed (<サイズ>)`、`skipped`、`failed` が付き、最後に `freed <サイズ>, <n> skipped, <n> failed` が出ます。

`--json` は `mode`、`items`（`id`、`kind`、`name`、`bytes`、`dirs`、`--apply` では `outcome`、`freed`、`detail`）、
`total_bytes`、`--apply` では `freed_bytes`、`skipped`、`failed` を出します。2 つの取得元の一方が読めなかったときは `partial` が true です。

## 終了コード

| 値 | 意味 |
|---|---|
| 0 | 完了（`skipped` は失敗に数えません） |
| 1 | 項目が失敗した、または Agent に尋ねられない・使える答えがなかった |
| 2 | 使いかたの誤り。使いかたも表示します |

エラーは標準エラー出力に `af-clean: <メッセージ>` で出ます。`cannot reach the Agent on this workspace` は、動いているワークスペースの中で実行していないことを意味します。
