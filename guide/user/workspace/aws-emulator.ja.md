---
audience: "本物の AWS アカウントに触らずに、AWS のコードを試したい人"
source_of_truth: "MiniStack のドキュメントと、af-aws-exec --help"
---

# AWS のコードを手元のエミュレーターで試す

[English](aws-emulator.md) | 日本語

オープンソースの AWS API エミュレーター [MiniStack](https://github.com/ministackorg/ministack) をワークスペースの中で動かすと、
本物のアカウントに触らずに、コード・テンプレート・スクリプトを試せます。

## 全体像

| あなたがやること | エージェントがやること |
|---|---|
| MiniStack を入れて起動する | 専用のプロファイルを付けて、エミュレーターにコマンドを送る |
| 専用のプロファイルを `~/.aws/config` に書く | 試行錯誤をエミュレーターの中で済ませる |
| 仕上げに本物の AWS で確かめる | `af-aws-exec` で本物のアカウントに対して実行する |

MiniStack は PyPI から入れれば Docker が要らず、数秒で起動します。イメージには入っていないので、自分で入れてください。

## MiniStack を入れて起動する

```sh
python3 -m venv ~/.local/ministack && ~/.local/ministack/bin/pip install ministack
GATEWAY_PORT=14566 ~/.local/ministack/bin/ministack
```

- ポートはほかのセッションと共有です。空いている番号を選んでください。既定の 4566 は使われていることがあります
- 使い終わったら、サーバーを止めてください

## 専用のプロファイルを作る

まだどこにも使われていない名前のプロファイルを、`~/.aws/config` の管理ブロックの**外**に足します。

```ini
[profile ministack]
region = us-east-1
endpoint_url = http://127.0.0.1:14566
aws_access_key_id = 000000000001
aws_secret_access_key = test
```

名前は、次のどれにもないものを選んでください。

| 確かめる場所 | 同じ名前があると |
|---|---|
| `aws configure list-profiles` / `~/.aws/credentials` | `~/.aws/credentials` の鍵が優先され、本物の資格情報がエミュレーターに送られます |
| `af-aws-exec --list`（設定画面のプロファイル） | 設定画面の同じ名前のプロファイルが、書き出されなくなります |

## 使う

コマンドには、毎回プロファイルを付けます。

```sh
aws --profile ministack s3 mb s3://demo-bucket
aws --profile ministack s3 ls
```

MiniStack は `af-aws-exec` を通しては使いません。`af-aws-exec` は、エンドポイントの設定をわざと外します。

### プロファイルのエンドポイントを読まないツール

プロファイルの `endpoint_url` を読むのは、AWS CLI v2 と今の SDK だけです。次のツールは読みません。

- AWS SDK for Java 1.x、JavaScript v2、Go v1
- 自分でエンドポイントを決めるツール

これらは、ダミーの鍵のまま本物の AWS に向かいます。Terraform の `endpoints` ブロックや SDK のエンドポイント指定で、
クライアント自身にエンドポイントを渡してください。

シェルに `AWS_ENDPOINT_URL*` や `AWS_IGNORE_CONFIGURED_ENDPOINT_URLS=true` が `export` されていると、プロファイルの
エンドポイントより優先されます。これも確かめてください。

## 本物の AWS に向かわないようにする

> [!WARNING]
> **ダミーの鍵をシェルに `export` しないでください**（`AWS_ACCESS_KEY_ID=test` など）。すべてのコマンドがその鍵を持つので、
> `--endpoint-url` を付け忘れたコマンドが本物の AWS にリクエストを送ります（ダミーの鍵なので拒否されます）。

プロファイルを付け忘れたときにどうなるかは、ワークスペースによります。

| ワークスペース | プロファイルを付け忘れたコマンド |
|---|---|
| ふつうの構成のワークスペース（AWS の身元を渡さない） | 「Unable to locate credentials」で止まります |
| 管理者がワークスペース自身のロールを戻しているワークスペース、マシン自身の資格情報を持つネイティブのランタイム | その身元で、本物の AWS に対して動きます |

後ろの 2 つでは、プロファイルを決して省かないでください。詳しくは
[af-aws-exec](../../ref/cli/af-aws-exec.ja.md#プロファイルの指定が要る理由) にあります。

## 何人かで 1 つのサーバーを使う

12 桁のアクセスキーが、そのままアカウント ID になります。リソースはアカウントごとに分かれるので、互いのリソースを
見てはいけないセッションには、別々のキーを使わせてください。

リージョンで分かれるかどうかはサービスによります（たとえば S3 Bucket はどのリージョンからも見えます）。
2 つのセッションを分けるのに、リージョンを頼らないでください。

## できないこと

- **RDS・ElastiCache・ECS は動きません。** これらは本物の Docker コンテナを起動しますが、ワークスペースには Docker がありません
- それでも RDS は `available` と答え、`localhost:5432` のようなエンドポイントを返します。そこにあなたのデータベースはなく、
  ワークスペースの別の PostgreSQL が待ち受けていることがあります。データベースが要るときは `af-db` を使ってください
  （[データベースを使うテストを動かす](databases.ja.md)）
- **エミュレーターです。** ここで動いたテンプレートや IAM ポリシーが、AWS では失敗することがあります。
  試行錯誤に使い、頼る前に本物の AWS で `af-aws-exec` を使って確かめてください

## 状態を残す

状態はメモリの上にあり、サーバーを止めると消えます。

| 環境変数 | 残るもの |
|---|---|
| `PERSIST_STATE=1` | サービスの状態（S3 のオブジェクトの中身は残りません） |
| `S3_PERSIST=1` | S3 のオブジェクトの中身 |

どちらも、既定の置き場は `/tmp` の下です。`/tmp` はワークスペースを起動し直すと消えるので、`STATE_DIR` と `S3_DATA_DIR` を
ホームの下のディレクトリにしてください。

## 関連

- [AWS SSO を使う](../integrations/aws-sso.ja.md)：本物のアカウントで実行する
- [af-aws-exec](../../ref/cli/af-aws-exec.ja.md)
- [データベースを使うテストを動かす](databases.ja.md)
