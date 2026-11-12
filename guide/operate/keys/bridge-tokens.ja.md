---
audience: "漏れたかもしれないワークスペースのトークンを無効にしたい配備管理者"
source_of_truth: "`control-plane/bridge_epoch.go`、`control-plane/bridge_epoch_cmd.go`、`POST /api/admin/rotate-bridge-tokens`"
---

# ブリッジトークンを入れ替える

[English](bridge-tokens.md) | 日本語

ワークスペースが Control Plane を呼ぶときのトークンを、1 人分だけ、または全員分まとめて無効にします。
入れ替えは配備のどの形態でも同じです。一斉の入れ替えは、主に AWS の配備（`ecs` / `ecs-ec2`）で、古いタスク定義やログに残ったトークンを消すためのものです。

## 全体像

| あなたがやること | 起きること |
|---|---|
| 1 人分のトークンを入れ替える | そのメンバーの epoch が進み、古いトークンが通らなくなる。動いているワークスペースに「**要再起動**」が出る |
| `af-cp advance-bridge-epochs --yes` を実行する | すべてのメンバーの epoch が進み、動いているすべてのワークスペースのトークンが一斉に無効になる |
| `AF_TOKEN_SIGN_VERSION` を上げる | トークンの署名鍵が変わり、保存済みの資格情報に触れずにすべてのトークンが無効になる |
| ワークスペースを再起動する | 新しいトークンを受け取る |

## トークンとその渡しかた

ワークスペースが Control Plane を呼ぶときの資格情報は、次の 3 つです。

- メンバーごとのブリッジトークン
- 内部 git トークン
- 開発用 IdP のクライアントシークレット

ワークスペースへは、ECS では SSM SecureString、docker では権限 0600 の env ファイル、Kubernetes では Secret で渡します。タスク定義の `environment` には入れません。

ブリッジトークンにはメンバーシップの **epoch** が入っていて、今の epoch のトークンだけが通ります。入れ替えとは、この epoch を進めることです。デプロイが提供する Bedrock ルートをオンにしている（`AF_PROVIDED_ROUTES=1`）と、ルート発行用のトークン `AF_ROUTE_ISSUE_TOKEN` も 0.44.0 から同じ経路で渡し、同じ epoch に結び付くので、入れ替えで一緒に無効になります。

## 1 人分を入れ替える

テナント管理者が `POST /api/admin/rotate-bridge-tokens` を呼びます。本文は `{"tenant_slug": "…", "user_key": "…"}` です。Console にはこの操作のボタンはありません。

- そのメンバーの epoch が進み、監査ログに `membership.rotate_bridge_tokens` が残ります
- 動いているワークスペースは、再起動するまで古いトークンを持ちます。ワークスペースのバーに「**要再起動**」が出ます
- メンバーを外したとき、同じメンバーシップを有効に戻したときも、epoch は進みます

## 全員分を一度に入れ替える

トークンをタスク定義の `environment` から外したリリースへ上げても、それより前のトークンは古いタスク定義のリビジョン、CloudTrail、ログに残っているかもしれません。アップグレードだけでは何も無効になりません。今のトークンは epoch 0 で、どのワークスペースもすでに持っているものだからです。

1. すべてのワークスペースがそのリリースで動いていることを確かめます
2. Control Plane のデータベースに対して `af-cp advance-bridge-epochs` を実行します。対象のメンバーシップの数を出すだけで、何も変えません
3. 静かな時間帯に `af-cp advance-bridge-epochs --yes` を実行します。ブリッジトークンと内部 git トークンの epoch が、1 回だけ進みます
4. 動いているワークスペースを再起動します。「**要再起動**」が出ているものが対象です

ECS では `af-cp` を Control Plane のタスク定義を使う単発のタスクとして動かします。貼り付けるコマンドは [ECS の手順書](../../../deploy/aws/ecs/README.md#running-af-cp-commands-against-the-control-planes-database) にあります。

## 署名鍵を入れ替える

`AF_TOKEN_SIGN_VERSION` は、トークンの鍵を導くもとを選びます。

| 値 | 導くもと |
|---|---|
| 未設定、または `0` | 配備の `AF_MASTER_KEY` |
| `1` 以上 | `AF_MASTER_KEY` から導いた専用のもと。トークンの鍵が、保存済みの資格情報を封じる鍵から分かれます |

- 値を変えると、保存済みの資格情報に触れずに、すべてのトークンが無効になります。変えるたびに、すべてのワークスペースを再起動してください
- 0 以上の整数でない値を設定すると、Control Plane は起動しません

## 関連

- [保存時の鍵を AWS KMS にする](kms.ja.md)
- [ホームごとに鍵を分ける](home-keys.ja.md)
- [アップグレードする](../run/upgrade.ja.md)
