---
audience: "af-memory のサブコマンド・オプション・出力・終了コードを調べたい人"
source_of_truth: "af-memory --help（ワークスペースの workspace-agent に入っているもの）"
---

# af-memory

[English](af-memory.md) | 日本語

ワークスペースのターミナルから、Agent Fleet のメモリを扱います。変更の一覧、Claude Code と muse のメモリの取り込み、
Claude Code への書き戻し、リント、昇格候補の一覧とリポジトリへの変更案の作成ができます。どれもワークスペースの Agent に頼むので、Console と同じスイッチとシークレットの検査が効きます。
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
af-memory promote-candidates [--project <ID か名前>] [--days N] [--json]
af-memory promote-proposal --project <ID か名前> --name <メモリ> [--file <path.md>] [--section <見出し>] [--days N] [--json | --patch]
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
| `lint` | メモリを点検してレポートを出す | `--apply` を付けたときだけ書く |
| `promote-candidates` | リポジトリの文書へ提案する価値のあるプロジェクトのメモリを一覧する | 書かない |
| `promote-proposal` | 候補 1 件について、文書・節・文面・走査結果・パッチからなる変更案を作る | 書かない |
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
シークレットの検査で引っかかった変更は表示せず、その件数を最後に出します。

## import-sources・import

ワークスペースの Claude Code がプロジェクトごとにためたメモリを、Agent Fleet のメモリへ取り込みます。

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
-home-dev-repos-acme-shop                                      12 files  -> acme-shop
-home-dev-repos-old-tool                                        3 files  no_project
```

`import` は、まずプレビューを状態ごとに表示し、新規（`new`）と更新（`update`）のものを 50 件ずつ取り込みます。

| 状態 | 意味 |
|---|---|
| `new` | 新しく取り込む |
| `update` | Claude のファイルのほうが新しい。Agent Fleet 側のコピーを上書きする |
| `unchanged` | 変わっていない |
| `forgotten` | 忘れたメモリ。取り込まない |
| `secret` | シークレットの疑い。取り込まない（検出したファイル・行と、マスクしたヒントが出ます） |
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

- 状態は `import` と同じで、ほかに `diverged`（Agent Fleet 側に独自のバージョンがあり、取り込みません）があります
- git が追跡しているファイルには `(shared: git tracks this file, teammates may have it)` と付きます
- フロントマターのないメモには `(no frontmatter: …)` と付き、名前はファイル名、説明は最初の行になります
- スイッチとシークレットの検査は `import` と同じです

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
| `--overwrite <名前>` | 競合（`conflict`）のファイルを、Agent Fleet 側のバージョンで置き換えます。何度でも付けられます |

| 状態 | 意味 |
|---|---|
| `new` | Claude にないファイルを作る |
| `update` | 前に書き戻したファイルを更新する |
| `remove` | Agent Fleet で忘れたので、前に書き戻したファイルを消す |
| `conflict` | Agent Fleet が書いていない、または書いたあとに変わったファイル。`--overwrite` で指定しない限り残す |
| `secret` | シークレットの疑い。書かない |
| `unchanged` | 変わっていない |
| `native_only` | Claude にだけある。消さない |

- 書く前に Claude のメモリのスナップショットを取り、`snapshot of claude's memory taken first (<ID>)` と表示します
- `MEMORY.md` は作り直し、何件を載せたかを表示します
- ユーザー全体のメモリと、ほかの種類のエージェントに限ったメモリは書きません。その件数を表示します
- スイッチがオンのときは、オンにする前から動いている Claude セッションが Claude Code 自身のメモリに書いているかもしれない、という注意が出ます

次のときは、書き戻しのあと終了コード 1 で終わります。

| 表示 | すること |
|---|---|
| `<N> newer edit(s) were kept under hidden names` | 書き戻し中に Claude 側で変わった編集が、別の名前で残っています。表示された名前のファイルを手で戻します |
| `<N> file(s) could not be written` | 書けなかったファイルがあります |
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
| `--apply` | 見つかったことを、メモリの理由として書き込みます（[`--apply`](#見つけたことを書き込む)） |

```
== acme-shop: 24 memories, 2 finding(s)  [refs/remotes/origin/main @ 3f9c2a1e5b07]
   missing-path   deploy-notes:4  scripts/deploy.sh
   oversize       architecture  5210 bytes
== user (every project reads it): 6 memories, 0 finding(s)
```

| 指摘 | 意味 |
|---|---|
| `broken-link` | どのメモリにも向かない `[[名前]]` のリンク |
| `missing-path` | 既定ブランチにないパス |
| `missing-symbol` | 既定ブランチのどこにもないシンボル |
| `missing-adr` | 記録のない ADR 番号 |
| `retired-term` | 廃止した用語 |
| `oversize` | 4,096 バイトを超える本文 |

- `--apply` なしでは何も書きません。読み取りもメモリの使用回数に数えません
- リポジトリは作業コピーではなく、`origin` の既定ブランチを ref で読みます。fetch はしないので、古いクローンでは古い答えになります。部分クローン（`git clone --filter`）では、既定ブランチのファイルの中身がすべてそろっていない限り、シンボルの検査は飛ばします（既定ブランチをチェックアウトすると、そろいます）
- 廃止した用語の提案が出ることがあります。一覧に入れるかどうかは Console で決めます
- 終了コードは、コマンドが動いたかどうかです。指摘があっても 0 で終わります

### 見つけたことを書き込む

見つかったことを、メモリに書き込みます。

- リンク・パス・シンボル・ADR・廃止した用語の指摘は、そのメモリの理由になります。メモリはどこで読まれても「要確認」として出ます。サイズの指摘は理由になりません
- 既定ブランチで git のリネームによって移動したパスは、本文のそのパスだけを書き換えます。これは操作 `lint-rename` の、独立した変更になります
- 変更はメモリ 1 件ごとに、作成者 `lint` の 1 コミットになります。どれもメモリの「変更」の表示から元に戻せます
- 1 回で書くのは最大 100 件です。もう一度実行すると、続きから書き込みます
- 範囲ごとに `applied (…): N marked, N renamed, N moved since the check, N deferred` と表示し、そのあとのレポートは書き込んだあとの状態です
- Agent Fleet のメモリのスイッチがオンである必要があります
- 定期実行や、ほかの `--apply` が動いている間は断られます。終わってから、もう一度実行してください

## promote-candidates

十分に確かめられ、リポジトリ自身の文書へ提案する価値があるプロジェクトのメモリを一覧します。一覧するだけで、何も書きません。1 件を変更案にするのは `promote-proposal` です。

```sh
af-memory promote-candidates [--project <ID か名前>] [--days N] [--json]
```

| オプション | 意味 |
|---|---|
| （プロジェクトの指定なし） | メモリのあるすべてのプロジェクトを一覧します |
| `--project <ID か名前>` | 1 つのプロジェクトだけを一覧します |
| `--days N` | 今のリビジョンが変わらずに経っているべき日数です。1〜90 日、既定は 7 日です |
| `--json` | 一覧を JSON で出します |

次のすべてを満たすメモリが候補になります。

- 種類（kind）が `lesson`・`procedure`・`preference` のどれかで、タイプ（type）が `user` ではありません（あなたではなく、リポジトリについてのメモリです）
- 異なる 2 つ以上のセッションが、今のリビジョンを `memory_feedback` で確かめています。前のリビジョンへの確認は数えません
- 保存されたアンカーがあり、どれも既定ブランチで見つかります。ツリーのファイルかディレクトリ、記録のある `ADR <番号>`、メモリの `[[名前]]`、ファイルの中に見つかるコードの形のシンボル（修飾なし。`pkg.Func` は調べられません）のどれかです。アンカー全体がそのどれか 1 つである必要があります。コマンドなど調べようのないアンカーは、実在する名前を含んでいても保留になります
- 既定ブランチで lint がそのメモリについて何も指摘していません
- 未解決の理由がありません
- ストアの履歴で、ファイルが N 日以上変わっていません（lint が理由を足したり消したりするのも変更です）。また、最後に公開した変更と同じ内容です

```
== acme-shop: 1 candidate(s), 1 held (confirmed by 2 sessions, unchanged for 7 days)  [origin/main @ 3f9c2a1e5b07]
   candidate  release-steps r3  procedure, 2 sessions, since 2026-11-01T00:00:00Z
              anchors: scripts/release.sh
   held       deploy-notes r1  lesson: no-anchors; confirmations (1 of 2 sessions on revision 1)
```

その種類のメモリで条件を満たさないものは `held` として、足りないものを添えて表示します。ストアの履歴（履歴による `changed-recently` と `unpublished`）は、ほかの条件をすべて満たすメモリについてだけ読むので、ほかに足りないものがあるメモリは、それを直したあとで理由が増えることがあります。

| 足りないもの | 意味 |
|---|---|
| `about-member` | タイプが `user` です |
| `open-reasons` | 「要確認」です |
| `disputed` | あるセッションが、今のリビジョンを stale か wrong と報告しました。理由がメモリに付いていなくても、新しいリビジョンになるまで保留します |
| `no-anchors` | 保存されたアンカーがありません |
| `unverified` | 確かめられないものがあったので、どのメモリも候補になりません。既定ブランチを読めなかったか、その上の検査が失敗した、`origin/HEAD` が設定されていない（既定ブランチが推測にすぎません。`git remote set-head origin --auto` で設定できます）、部分クローンである、廃止した用語の一覧を読めない、のどれかです。どれだったかは `skipped` の行に出ます |
| `anchor-not-found` | 保存されたアンカーが既定ブランチで見つかりません（そのアンカーを添えます） |
| `lint` | lint がそのメモリについて指摘しています（指摘の名前を添えます） |
| `moved` | 一覧を作っている間に変わりました。もう一度実行してください |
| `unpublished` | ファイルが、最後に公開した変更と違います（Agent Fleet の外で編集したときなど）。または、リビジョンがその前の変更から正しく続いていません |
| `confirmations` | 今のリビジョンを確かめたセッションが足りません |
| `changed-recently` | 今のリビジョンができてから N 日経っていません |

- アーカイブ済み・置き換え済みのメモリと、種類 `state`・`log` のメモリは表示しません
- リポジトリは `lint` と同じく、`origin` の既定ブランチを ref で読みます。作業コピーは使わず、fetch もしません
- 読み取りはメモリの使用回数に数えません。終了コードは、コマンドが動いたかどうかです

## promote-proposal

`promote-candidates` の候補 1 件について、リポジトリへの変更案を作ります。Agent Fleet はコミットも
push もプルリクエストの作成もしません。パッチを当ててプルリクエストを出すのはあなたです（セッションに頼んでも構いません）。

```sh
af-memory promote-proposal --project <ID か名前> --name <メモリ> [--file <path.md>] [--section <見出し>] [--days N] [--json | --patch]
```

| オプション | 意味 |
|---|---|
| `--project <ID か名前>` | メモリのプロジェクト |
| `--name <メモリ>` | 対象のメモリ。`promote-candidates` と同じ判定（同じ `--days`）で、今候補でなければ失敗し、足りないものを示します |
| `--file <path.md>` | 変更する Markdown 文書のリポジトリ内パス。既定: メモリのアンカーのうち既定ブランチにある最初の Markdown ファイル、無ければそこにある `AGENTS.md`・`CLAUDE.md`・`README.md` の最初のもの、どれも無ければ新しい `AGENTS.md` |
| `--section <見出し>` | 文面を入れる見出し（`#` は付けない）。既にある見出しならその節の末尾に、無ければ文書の末尾に新しい `##` の節を足します。既定: メモリの説明の最初の文 |
| `--days N` | 候補の判定に使う期間。1〜90 日、既定 7 |
| `--json` | 変更案を JSON で表示します |
| `--patch` | `git apply` 用にパッチだけを表示します |

```
== proposal: AGENTS.md (ready to commit)
   repository: github.com/acme/shop  [origin/main @ 3f9c2a1e5b07]
   section:    "Release" (new section)
   from:       release-steps r3 (project)
   secret scan: clean
   pre-commit:  passed (core.hooksPath .githooks)

diff --git a/AGENTS.md b/AGENTS.md
...
```

- 文面はメモリの本文そのままです。翻訳も書き直しもしません。メモリが文書と違う言語で書かれているとき、
  `[[name]]` で他のメモリにリンクしているとき、文書が新規のときは警告が出ます
- 走査は 2 つです。Agent Fleet の秘密情報の走査が文面・見出し・ファイル名を調べ、何か見つかれば文面とパッチは返しません。
  続いてリポジトリ自身の pre-commit フック（作業コピーでコミットしたとき git が実行するもの。作業コピーの
  `core.hooksPath`、設定が無ければリポジトリの `hooks/`）を、既定ブランチの使い捨てのクローンでパッチに対して
  実行します。環境には Agent の資格情報も `GIT_*` 変数も渡さず、失敗したときだけ出力を表示します。git が実行する
  フックが無いリポジトリでは通すものがありません。両方が通り、フックが変更を書き換えなかったときに ready です
  （変更を整形し直すフックなら ready にならず、表示するのはフックに渡したパッチのままで、フックが残したものではありません）
- パッチは表示した既定ブランチのリビジョン（`[origin/main @ …]`）に当たります。メモリのストアにも作業コピーにも
  何も書かず、fetch もしません。変更案は一度に 1 つだけ作り、フックは 3 分で止めます
- `from:` は文面を取ったメモリとリビジョンです（`--json` では文面のダイジェストも返します）。マージ後にメモリを
  文書で置き換えられるのは、メモリがまだそのリビジョンとその文面のままのときだけです。プルリクエストを待つ間に
  変わった文面が隠れることはありません
- 節の見出しは、書いた後に同じ見出しとして読み戻せるものに限ります。末尾の `#` は ATX 見出しの閉じ記号になるので
  `Release #` は受け付けません。対象になるのは最上位の見出しだけです（リストや引用の中の見出しは対象外）。文面は
  その節の中で独立したブロックになる必要があり、前や文面の中の閉じていないコードフェンス、節と同じかそれより上の
  レベルの見出しを含む文面、文書中の文字の無い見出しがあると提案は断られます
