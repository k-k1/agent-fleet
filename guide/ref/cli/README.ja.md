---
audience: "ワークスペースに入っている af- コマンドを調べたい人"
source_of_truth: "各コマンドの --help"
---

# コマンド

[English](README.md) | 日本語

ワークスペースには、Agent Fleet 独自のコマンドが入っています。どれもターミナルからもエージェントからも使え、
`--help` で使いかたを表示します。

| コマンド | できること |
|---|---|
| [af-aws-exec](af-aws-exec.ja.md) | AWS プロファイルの権限で、コマンドを 1 つ実行する |
| [af-clean](af-clean.ja.md) | Console の容量不足の掃除と同じ、再生成できるビルド成果物とキャッシュの一覧と削除を、端末から行う |
| [af-gcloud-exec](af-gcloud-exec.ja.md) | Google Cloud のプロファイルのトークンで、コマンドを 1 つ実行する |
| [af-memory](af-memory.ja.md) | Agent Fleet のメモリの変更の一覧・取り込み・書き戻し・リントを、端末から行う |
| [af-watch-hook](af-watch-hook.ja.md) | プライベートリポジトリに Webhook を足し、見張りに合図が届くようにする |

ほかのコマンド（`af-db`、`af-scratch`、`af-arch-repair`）のページは、順に追加します。

## サインイン済みの CLI

次のコマンドは、セッションでも端末でも、どのエージェントでも、設定でつないだ接続を使います。端末でログインする
必要はなく、期限が近いトークンは使うときに更新されます。

| コマンド | 使う接続 | 詳しく |
|---|---|---|
| `git` | **Git ホスティング**（GitHub、Bitbucket） | [Git のサービスを接続する](../../user/code/connect-git.ja.md) |
| `gh` | **Git ホスティング**の GitHub | [GitHub の CLI を使う](../../user/integrations/github-cli.ja.md) |
| `bkt` | **Git ホスティング**の Bitbucket Cloud（bitbucket.org） | [Bitbucket の CLI を使う](../../user/integrations/bitbucket-cli.ja.md) |
| `jira` | **課題管理**の Jira | [Jira を使う](../../user/integrations/jira.ja.md) |
| `confluence` | **課題管理**の Jira（同じ Atlassian 接続） | [Confluence を使う](../../user/integrations/confluence.ja.md) |
| `af-aws-exec --profile <名前> -- <コマンド>` | **AWS プロファイル/SSM**。プロファイルにログインしたあと | [af-aws-exec](af-aws-exec.ja.md) |
| `af-gcloud-exec --profile <名前> -- <コマンド>` | **Google Cloud**。プロファイルにログインしたあと | [af-gcloud-exec](af-gcloud-exec.ja.md) |

エージェントには、Bitbucket では MCP サーバーではなく `bkt` を、Jira では既定で `jira` を使うよう伝えてあります。
CLI は呼び出しのたびに保存済みの接続を読むので、MCP のコネクタのように、ログインが切れてセッションを再起動することがありません。
Jira で Atlassian の MCP ツールを優先させたいときは、[Jira を使う](../../user/integrations/jira.ja.md#atlassian-の-mcp-ツールを使わせたいとき)を見てください。
