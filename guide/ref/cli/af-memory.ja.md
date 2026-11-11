---
audience: "af-memory のサブコマンド・オプション・出力・終了コードを調べたい人"
source_of_truth: "af-memory --help（ワークスペースの workspace-agent に入っているもの）"
---

# af-memory

[English](af-memory.md) | 日本語

ワークスペースの端末から、Agent Fleet のメモリを扱います。変更の一覧、Claude Code と muse のメモリの取り込み、
Claude Code への書き戻し、リントができます。どれもワークスペースの Agent に頼むので、Console と同じスイッチと秘密情報の検査が効きます。
Console での使いかたは [メモリを取り込む・書き出す](../../user/memory/import-export.ja.md) にあります。

## 書式

```sh
af-memory changes [--limit N]
af-memory import-sources
af-memory import --project <slug> [--dry-run]
af-memory muse-import-sources
af-memory muse-import --dir <名前> [--scope project|personal_project] [--dry-run]
af-memory muse-import --scope personal --into-user [--dry-run]
af-memory export-sources
af-memory export --project <ID か名前> [--dry-run] [--overwrite <名前>]...
af-memory lint [--user | --project <ID か名前>] [--json] [--apply]
af-memory --help
```

## サブコマンド

| サブコマンド | すること | 書き込むか |
|---|---|---|
| `changes` | Agent Fleet のメモリの変更を、新しい順に一覧する | 書かない |
| `import-sources` | 取り込める Claude のプロジェクトを一覧する | 書かない |
| `import` | Claude のプロジェクトのメモリを取り込む | `--dry-run` なしなら書く |
| `muse-import-sources` | 取り込める muse のフォルダを一覧する | 書かない |
| `muse-import` | muse のメモを取り込む | `--dry-run` なしなら書く |
| `export-sources` | Claude Code へ書き戻せるプロジェクトを一覧する | 書かない |
| `export` | プロジェクトのメモリを Claude Code 自身のメモリへ書き戻す | `--dry-run` なしなら書く |
| `lint` | メモリを点検してレポートを出す | `--apply` なしなら書かない |
| `--help`（`-h`・`help`） | 使いかたを表示する | 書かない |

## 終了コード

| コード | 意味 |
|---|---|
| 0 | 終わった |
| 1 | 失敗した。取り込みで見送った項目がある、書き戻しで書けなかったファイルがある、なども含みます |
| 2 | 使いかたの誤り（知らないサブコマンド、足りない・余分なオプション）。使いかたも表示します |

エラーは標準エラーに `af-memory: <メッセージ>` の形で出ます。Agent が返したエラーには、最後に `(<コード>)` が付きます。
`cannot reach the Agent on this workspace` は、ワークスペースの Agent に届かなかったという意味です。

## changes

```sh
af-memory changes [--limit N]
```

公開された変更を、新しい順に `N` 件まで表示します。既定は 30 件です。

```
2026-11-10T09:12:03Z  update  acme-shop/test-commands  by claude (s4k2m9x)
2026-11-09T17:40:51Z  create  user/review-style  by member (console)
(1 change(s) withheld: they did not pass the secret scan)
```

1 行に、日時・操作・`<プロジェクト>/<名前>`（ユーザー全体のメモリは `user/<名前>`）・書き手の種類とセッションが並びます。
秘密情報の検査に掛かった変更は表示せず、その件数を最後に出します。

## import-sources・import

ワークスペースの Claude Code がプロジェクトごとに溜めたメモリを、Agent Fleet のメモリへ取り込みます。

```sh
af-memory import-sources
af-memory import --project <slug> --dry-run
af-memory import --project <slug>
```

| オプション | 意味 |
|---|---|
| `--project <slug>` | 取り込む Claude のプロジェクト。必須です。`import-sources` の 1 列目の名前を渡します |
| `--dry-run` | 何が起きるかを表示するだけで、何も書きません |

`import-sources` は、Claude のプロジェクトごとにファイル数と取り込み先を表示します。取り込めないものは、取り込み先の代わりに理由が出ます。

```
-home-dev-repos-acme-shop        12 files  -> acme-shop
-home-dev-repos-old-tool          3 files  no_project
```

`import` は、まずプレビューを状態ごとに表示し、新規（`new`）と更新（`update`）のものを 50 件ずつ取り込みます。

| 状態 | 意味 |
|---|---|
| `new` | 新しく取り込む |
| `update` | Claude のファイルのほうが新しい。Agent Fleet 側の写しを上書きする |
| `unchanged` | 変わっていない |
| `forgotten` | 忘れたメモリ。取り込まない |
| `secret` | 秘密情報の疑い。取り込まない（検出したファイル・行と、伏せたヒントが出ます） |
| `invalid` | 取り込めない（理由が出ます） |

```
claude project -> acme-shop
new        2
  test-commands
  deploy-notes
unchanged  10
progress: 2/2
done: 2 imported, 0 skipped
```

- **取り込むには、Agent Fleet のメモリのスイッチがオンである必要があります。** `--dry-run` はオフでも使えます
- プレビューのあとにファイルが変わった項目などは見送り（`skipped`）になり、理由を表示して終了コード 1 で終わります

## muse-import-sources・muse-import

Muse Code のメモを取り込みます。

```sh
af-memory muse-import-sources
af-memory muse-import --dir <名前> [--scope project|personal_project] [--dry-run]
af-memory muse-import --scope personal --into-user [--dry-run]
```

| オプション | 意味 |
|---|---|
| `--dir <名前>` | 取り込むフォルダ。`project` と `personal_project` では必須で、`personal` では指定しません |
| `--scope <保存先>` | `project`（既定）、`personal_project`、`personal` のどれか |
| `--into-user` | `personal` の取り込み先が、すべてのプロジェクトが読むユーザー全体のメモリだと承知したしるし。`personal` では必須で、ほかの保存先には付けられません |
| `--dry-run` | 何が起きるかを表示するだけで、何も書きません |

| 保存先 | 読むところ | `--dir` に渡すもの | 取り込み先 |
|---|---|---|---|
| `project` | 作業コピーの `.agents/memory` | 作業コピーのディレクトリ名 | その作業コピーのプロジェクト |
| `personal_project` | `~/.local/share/muse/memory/projects` の作業コピー別のフォルダ | `muse-import-sources` が表示する名前 | 対応する作業コピーのプロジェクト |
| `personal` | `~/.local/share/muse/memory/personal` | なし | ユーザー全体のメモリ |

- 状態は `import` と同じで、ほかに `diverged`（Agent Fleet 側に独自の版がある。取り込まない）があります
- git が追跡しているファイルには `(shared: git tracks this file, teammates may have it)` と付きます
- フロントマターのないメモには `(no frontmatter: …)` と付き、名前はファイル名、説明は最初の行になります
- スイッチと秘密情報の検査は `import` と同じです

## export-sources・export

プロジェクトの Agent Fleet のメモリを、Claude Code 自身のメモリへ一度だけ書き戻します。Agent Fleet のメモリのスイッチは要りません。

```sh
af-memory export-sources
af-memory export --project <ID か名前> --dry-run
af-memory export --project <ID か名前> [--overwrite <名前>]...
```

| オプション | 意味 |
|---|---|
| `--project <ID か名前>` | 書き戻すプロジェクト。必須です。`export-sources` の ID か名前を渡します。同じ名前が 2 つあるときは ID を渡します |
| `--dry-run` | 何が起きるかを表示するだけで、何も書きません |
| `--overwrite <名前>` | 競合（`conflict`）のファイルを、Agent Fleet の版で置き換えます。何度でも付けられます |

| 状態 | 意味 |
|---|---|
| `new` | Claude にないファイルを作る |
| `update` | 前に書き戻したファイルを更新する |
| `remove` | Agent Fleet で忘れたので、前に書き戻したファイルを消す |
| `conflict` | Agent Fleet が書いていない、または書いたあとに変わったファイル。`--overwrite` で名指ししない限り残す |
| `secret` | 秘密情報の疑い。書かない |
| `unchanged` | 変わっていない |
| `native_only` | Claude にだけある。消さない |

- 書く前に Claude のメモリのスナップショットを取り、`snapshot of claude's memory taken first (<ID>)` と表示します
- `MEMORY.md` は作り直し、何件を載せたかを表示します
- ユーザー全体のメモリと、ほかの種類のエージェントに限ったメモリは書きません。その件数を表示します
- スイッチがオンのときは、オンにする前から動いている Claude セッションが自前のメモリに書いているかもしれない、という注意が出ます

次のときは、書き戻しのあと終了コード 1 で終わります。

| 表示 | すること |
|---|---|
| `newer edit(s) were kept under hidden names` | 書き戻し中に Claude 側で変わった編集が、別の名前で残っています。表示された名前のファイルを手で戻します |
| `file(s) could not be written` | 書けなかったファイルがあります |
| `MEMORY.md could not be updated` | 書き戻し中に `MEMORY.md` が変わったか、書けませんでした。プレビューからやり直します |

## lint

Agent Fleet のメモリを点検して、レポートを表示します。何を見つけるかは「古くなったメモリを見つける」のページで説明しています。

```sh
af-memory lint [--user | --project <ID か名前>] [--json] [--apply]
```

| オプション | 意味 |
|---|---|
| （範囲の指定なし） | ユーザー全体のメモリと、メモリのあるすべてのプロジェクトを点検します |
| `--user` | ユーザー全体のメモリだけを点検します |
| `--project <ID か名前>` | 1 つのプロジェクトだけを点検します。`--user` と一緒には使えません |
| `--json` | レポートを JSON で出します |
| `--apply` | 見つかったことを、メモリの理由として書き込みます（[`--apply`](#--apply)） |

```
== acme-shop: 24 memories, 2 finding(s)  [refs/remotes/origin/main @ 3f9c2a1e5b07]
   missing-path   deploy-notes:4  scripts/deploy.sh
   oversize       architecture  5210 bytes
== user (every project reads it): 6 memories, 0 finding(s)
```

| 指摘 | 意味 |
|---|---|
| `broken-link` | どのメモリにも向かない `[[リンク]]` |
| `missing-path` | 既定ブランチにないパス |
| `missing-symbol` | 既定ブランチのどこにもないシンボル |
| `missing-adr` | 記録のない ADR 番号 |
| `retired-term` | 廃止した用語 |
| `oversize` | 4,096 バイトを超える本文 |

- `--apply` なしでは何も書きません。読み取りもメモリの使用回数に数えません
- リポジトリは作業コピーではなく、`origin` の既定ブランチを ref で読みます。fetch はしないので、古いクローンでは古い答えになります
- 廃止した用語の提案が出ることがあります。一覧に入れるかどうかは Console で決めます
- 終了コードは、コマンドが動いたかどうかです。指摘があっても 0 で終わります

### --apply

見つかったことを、メモリに書き込みます。

- リンク・パス・シンボル・ADR・廃止した用語の指摘は、そのメモリの理由になります。メモリはどこで読まれても「要確認」として出ます。サイズの指摘は理由になりません
- 既定ブランチで git の名前変更として動いたパスは、本文のそのパスだけを書き換えます。操作 `lint-rename` の、別の変更になります
- 変更はメモリ 1 件ごとに、作成者 `lint` の 1 コミットになります。どれもメモリの「変更」の表示から元に戻せます
- 1 回で書くのは最大 100 件です。もう一度実行すると続きをします
- 範囲ごとに `applied (…): N marked, N renamed, N moved since the check, N deferred` と表示し、そのあとのレポートは書き込んだあとの状態です
- Agent Fleet のメモリのスイッチがオンである必要があります
- 自動の実行や、ほかの `--apply` が動いている間は断られます。終わってから、もう一度実行してください
