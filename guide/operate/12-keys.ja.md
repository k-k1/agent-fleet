---
audience: "Control Plane の鍵を KMS に持たせたい（ホームごとの専用鍵の有無は問わない）AWS 上の配備管理者"
source_of_truth: "パラメータは `deploy/aws/ecs/README.md` と `deploy/aws/ecs/cfn/PARAMETERS.md`。コマンドは `af-cp rewrap-keys --help` と `af-cp home-dek-status --help`"
---

# 12. 保存時の鍵: AWS KMS とホームごとの鍵

[English](12-keys.md) | 日本語

この章の内容は、作業ごとのページに移りました。

| ページ | できること |
|---|---|
| [保存時の鍵を AWS KMS にする](keys/kms.ja.md) | Control Plane が封じる秘密を KMS で守り、切り替える前の値を封じ直す。`AF_MASTER_KEY` のパラメータを自前の鍵で暗号化する |
| [ホームごとに鍵を分ける](keys/home-keys.ja.md) | メンバーの資格情報ストアをホームごとの鍵で封じ、KMS 鍵の無効化で何が読めなくなるかを知る |
| [ブリッジトークンを入れ替える](keys/bridge-tokens.ja.md) | ワークスペースのトークンを、1 人分または全員分まとめて無効にする |
