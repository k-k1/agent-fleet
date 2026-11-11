---
audience: "配備の防御に責任を持つ人"
source_of_truth: "`deploy/` 配下のスクリプト（記述がスクリプトと食い違ったら、このページのバグ）"
---

# 04. セキュリティ運用

[English](04-secure.md) | 日本語

この章の内容は、やりたいことごとのページに移りました。

| やりたいこと | ページ |
|---|---|
| 隔離の境界、残るリスクへの備え、脆弱性の報告 | [脅威モデルと残るリスクを知る](secure/threat-model.ja.md) |
| `AF_MASTER_KEY` と `AF_COOKIE_SECRET` の要件、鍵のない配備、鍵の前に保存された値、用途ごとの cookie の鍵 | [配備の鍵を扱う](secure/deployment-keys.ja.md) |
| ワークスペースからの外向きの通信を観測し、許可リストで絞る | [外向きの通信を制御する](secure/egress.ja.md) |
| `/mcp` の OAuth、信頼するクライアントのホスト、メンバーの OAuth の MCP サーバー、`/mcp` のログ | [MCP と外部接続のサインインを運用する](secure/mcp.ja.md) |
| `AUTH=dev` と開発用 OIDC IdP | [開発用サインインを使う](secure/dev-signin.ja.md) |
| プレビュー用ドメイン、サインインの許可リスト、監査ログ、ホストのクラウドの身元、ブラウザの引き渡し | [そのほかの運用上の制御](secure/controls.ja.md) |
| 人を外す、全員のセッションを切る、super_admin を引き継ぐ | [アクセスを取り消す・super_admin を引き継ぐ](secure/offboarding.ja.md) |
