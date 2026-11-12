---
audience: "af-db の動詞・オプション・接続 URL・終了コードを調べたい人"
source_of_truth: "af-db の使いかたの表示（ワークスペースの workspace-agent に入っているもの）"
---

# af-db

[English](af-db.md) | 日本語

ワークスペースの中で PostgreSQL と MySQL を動かし、接続 URL を渡します。初めて使うときにサーバーを入れて起動し、
作業コピーごとにデータベースを 1 つ用意します。テストでの使いかたと Console からの操作は
[データベースを使うテストを動かす](../../user/workspace/databases.ja.md) にあります。

## 書式

```sh
af-db up       [engine] [--major N] [--durable] [--ephemeral]
af-db url      [engine] [--major N] [--db=NAME] [--tcp] [--format=go-dsn]
af-db env      [engine] [--major N] [--db=NAME] [--tcp]
af-db connect  [engine] [--major N] [--db=NAME]
af-db create   [engine] [--major N] --db=NAME
af-db drop     [engine] [--major N] --db=NAME
af-db reset    [engine] [--major N] [--db=NAME]
af-db down     [engine] [--major N] [--purge]
af-db status   [engine] [--major N] [--json]
```

引数なしで実行したときと、知らない動詞（`--help` を含む）を渡したときは、使いかたを表示して終了コード 2 で終わります。

| 引数 | 値 |
|---|---|
| `engine` | `postgres`（既定）か `mysql` |
| `--major N`（`--major=N`） | PostgreSQL は `16`、`17`（既定）、`18`。MySQL は `8.4`（既定で、これだけ） |

エンジンとメジャー版の組ごとに、別のサーバーと別のデータになります。`af-db up --major 16` と `af-db up` を実行すると、
PostgreSQL のサーバーが 2 つ並んで動きます。

## 動詞

| 動詞 | 動作 | サーバーを起動するか |
|---|---|---|
| `up` | 必要ならエンジンを入れて起動します。`<engine>-<major> running on port <ポート>` を出します | する |
| `url` | 必要ならデータベースを作り、接続 URL を出します | する |
| `env` | `url` と同じですが、`export` の行を出します（後述） | する |
| `connect` | 必要ならデータベースを作り、そこにつないだ `psql` / `mysql` に置き換わります | する |
| `create` | `--db` で名前を付けたデータベースを作ります。すでにあればそのままにして、成功として扱います | しない。止まっていれば終了コード 5 |
| `drop` | `--db` で指定したデータベースを、中身ごと削除します。PostgreSQL では、つないだままの接続を切ります | しない。止まっていれば終了コード 5 |
| `reset` | データベースを削除して作り直し、空にします | しない。止まっていれば終了コード 5 |
| `down` | サーバーを止めます。`--purge` ではデータディレクトリも消します | — |
| `status` | 状態、ポート、ソケット、データベースを出します。`engine` を省くと、af-db が知っているサーバーすべてを出します | — |

## オプション

| オプション | 使える動詞 | 動作 |
|---|---|---|
| `--db=NAME` | `url`、`env`、`connect`、`create`、`drop`、`reset` | 作業コピーのデータベースの代わりに `NAME` を使います。`create` と `drop` では必須です |
| `--tcp` | `url`、`env` | unix ソケットの代わりに、`127.0.0.1` の TCP でつなぐ URL を出します |
| `--format=go-dsn` | `url` | `go-sql-driver/mysql` の形で出します。PostgreSQL では同じ URL になります |
| `--durable` | `up` | コミットのたびにディスクへ書き切ります（PostgreSQL は `fsync`、MySQL はコミットごとのフラッシュ） |
| `--ephemeral` | `up` | データディレクトリを作業ディスクに置きます。速い代わりに、**ワークスペースを停止すると消えます**。作業ディスクがなければ、そう表示して無視し、データはホームに置きます |
| `--purge` | `down` | データディレクトリを消します。**そのエンジンとメジャー版のデータベースすべて**が消えます |
| `--json` | `status` | テキストの代わりに JSON を出します |

- `--durable` と `--ephemeral` はサーバーに残ります。あとでフラグなしの `af-db up` を実行しても、そのままです。
- `--persist` は、そう表示したうえで `--durable` として扱います。

## どのデータベースを使うか

`--db` を付けないときは、作業コピーのデータベースを使います。名前は `af_<ディレクトリ名>_<16 進 6 桁>` で、たとえば
`af_webshop_3f9a1c` です。作業コピーは、次の順に決まります。

1. コマンドを実行したセッションの作業コピー
2. いまのディレクトリの git リポジトリのトップ
3. いまのディレクトリ

同じ作業コピーの 2 つのセッションは、同じデータベースを使います。作業コピーのディレクトリがなくなると、その
データベースは、次に `up`、`url`、`env`、`connect`、`status` がサーバーの動いている状態で実行されたときに削除されます。
`--db` で名前を付けたデータベースはどの作業コピーにも属さず、この方法では削除されません。

`--db` の名前は `^[a-z_][a-z0-9_]{0,62}$` に合わせます。英小文字、数字、`_` だけを使い、数字で始めず、63 文字までです。
それ以外の名前は終了コード 2 で終わります。

## 接続 URL

既定の URL は unix ソケットでつなぎます。ユーザーは PostgreSQL が `postgres`、MySQL が `root` です。パスワードは
サーバーごとに生成され、ワークスペースの中に保存されます。

| エンジン | 形 | 例 |
|---|---|---|
| PostgreSQL | ソケット（既定） | `postgres://postgres:<パスワード>@/<db>?host=<ソケットのディレクトリ>&port=5432&sslmode=disable` |
| PostgreSQL | `--tcp` | `postgres://postgres:<パスワード>@127.0.0.1:5432/<db>?sslmode=disable` |
| MySQL | ソケット（既定） | `mysql://root:<パスワード>@localhost/<db>?socket=<ソケットのディレクトリ>/mysql.sock` |
| MySQL | `--tcp` | `mysql://root:<パスワード>@127.0.0.1:3306/<db>` |
| MySQL | `--format=go-dsn` | `root:<パスワード>@unix(<ソケットのディレクトリ>/mysql.sock)/<db>` |
| MySQL | `--format=go-dsn --tcp` | `root:<パスワード>@tcp(127.0.0.1:3306)/<db>` |

`env` は `eval` 用に 2 行を出します。

```sh
$ af-db env
export AF_DB_URL_POSTGRES="postgres://postgres:…@/af_webshop_3f9a1c?host=…&port=5432&sslmode=disable"
export DATABASE_URL="postgres://postgres:…@/af_webshop_3f9a1c?host=…&port=5432&sslmode=disable"
```

`mysql` を付けると、1 行目は `AF_DB_URL_MYSQL` になります。

### ポート

サーバーは `127.0.0.1` だけで待ち受けます。af-db はまずエンジンの通常のポート（PostgreSQL は 5432、MySQL は 3306）を、
次にその上の 64 個を試し、どれも使われていれば空いている任意のポートを使います。一度取ったポートは、空いている限り
再起動しても同じです。ポートは決め打ちせず、`af-db status` か URL で確かめてください。

### 新しいセッションに入る変数

サーバーが動いている間に始めたセッションには、クライアント用の変数が入ります。`psql` と `mysql` は引数なしでつながります。

| エンジン | 変数 |
|---|---|
| PostgreSQL | `PGHOST`、`PGPORT`、`PGUSER`、`PGPASSWORD`。作業コピーにデータベースがあれば `PGDATABASE` と `AF_DB_URL_POSTGRES` も入ります |
| MySQL | `MYSQL_UNIX_PORT`、`MYSQL_TCP_PORT`、`MYSQL_PWD`。データベースを指す変数はないので、`mysql <db>` で開きます |

サーバーを起動する前に開いたシェルには、どれも入っていません。`af-db connect` はどのシェルからでも使えます。

## status

```
$ af-db status
mysql-8.4: stopped
postgres-17: running  port=5432  sock=/home/dev/.local/state/af-db/run/postgres-17  pid=4121
  db: af_webshop_3f9a1c  dir=/home/dev/repos/webshop
  db: scratch (shared)
```

`(shared)` は `--db` で名前を付けたデータベースです。まだ何も用意していなければ `af-db: no instances configured` と出ます。

`--json` に engine を付けると、`engine`、`major`、`state`（`running`、`stopped`、`not-configured`）、`pid`、`port`、
`sockdir`、`datadir`、`databases`（名前 → 作業コピー。名前を付けたものは空）、`lastUsedAt` を出し、動いている間は
`version` と `rssBytes` も出します。engine を省くと `instances` を出し、サーバーごとの行に `engine`、`major`、`state`、
`port`、`datadir` が入り、動いている間は `version` と `rssBytes` も入ります。

## ファイルの置き場所

| もの | 場所 |
|---|---|
| データ | `~/.local/state/af-db/`（停止しても作り直しても残ります）。`--ephemeral` では作業ディスクの `af-db/` |
| ソケット | `~/.local/state/af-db/run/<engine>-<major>/` |
| サーバーのバイナリ | `~/.local/share/agent-fleet/<engine>/<major>/` |
| クライアント | `psql`、または `mysql` と `mysqldump`。サーバーを起動したときに `~/.local/bin` に入ります |

初回の起動ではサーバーをダウンロードするので、数分かかることがあります。接続のない状態が 30 分続いたサーバーは、
ワークスペースが止めます。**⚙設定 →「データベース」** の「ワークスペース起動時に開始」をオンにすると、ワークスペースの
起動に合わせて起動します。

## 環境変数

| 変数 | 効果 |
|---|---|
| `AF_DB_MEM_GATE=0` | MySQL を起動する前のメモリの確認（後述）を飛ばします |

## 終了コード

| 値 | 意味 |
|---|---|
| 0 | 完了 |
| 1 | そのほかのエラー。サーバーが SQL を拒んだ場合など（PostgreSQL で、データベースに何かがつないだままの `reset`） |
| 2 | 使いかたの誤り。知らない動詞やオプション、`--db` の指定漏れ、使えない名前 |
| 3 | 導入に失敗した。提供していない `--major` を含みます |
| 4 | サーバーの起動に失敗した |
| 5 | サーバーが動いていない（`create`、`drop`、`reset`）。先に `af-db up` を実行します |
| 6 | メモリが足りない。MySQL にはワークスペースのメモリの上限が 1 GiB 以上必要です |

エラーは標準エラー出力に `af-db: <メッセージ>` で出ます。

## 関連

- [データベースを使うテストを動かす](../../user/workspace/databases.ja.md)
- [af-scratch](af-scratch.ja.md)：`--ephemeral` が使う作業ディスク
