---
audience: "全員（4 人の読者が共有する唯一のセクション）"
source_of_truth: "能力の事実についてはこのセクション（軸はコードと突き合わせている）"
---

# リファレンス — Agent Fleet にできること

[English](README.md) | 日本語

ほかのセクションは「何に対応しているか」を繰り返し書かず、ここへリンクします。
どの事実もここに 1 か所だけあり、4 つの方向から引けます。

| 表 | 知りたいこと |
|---|---|
| [features.md](features.ja.md) | そもそも何ができて、誰ができる？ |
| [agents.md](agents.ja.md) | このエージェントでできる？ |
| [repos.md](repos.ja.md) | このリポジトリ種別に対してできる？ |
| [deploy-targets.md](deploy-targets.ja.md) | この配備形態にある？ |
| [roles.md](roles.ja.md) | このロールに許されている？ |
| [settings.md](settings.ja.md) | どこで設定する？ 変数名は？ |
| [limits.md](limits.ja.md) | 既定はいくつ？ 上限は？ |
| [cli/](cli/README.ja.md) | この `af-` コマンドは何をする？ オプションは？ |
| [mcp/](mcp/README.ja.md) | この MCP ツールは何をする？ セッション・アシスタント・外部のどこで使える？ |
| [aws-profiles.md](aws-profiles.ja.md) | AWS プロファイルの取り込み・共有サインイン・ロールの細かい規則は？ |
| [aws-ssm.md](aws-ssm.ja.md) | SSM ホストのリージョンの決まりかた、AWS からの検索、~/.ssh/config の取り込みの規則は？ |
| [bedrock-route.md](bedrock-route.ja.md) | AWS のプロファイルで claude や opencode を Amazon Bedrock 上で動かすには？ 呼び出しが拒否された理由は？ |
| [glossary.md](glossary.ja.md) | この言葉は画面とコードで何を指す？ |
| [browser-pane.md](browser-pane.ja.md) | 動いている Web アプリを人にどう見せる？ |
| [console-ui.md](console-ui.ja.md) | 画面のこの印やメニューの項目は何？ |
| [更新履歴](../../deploy/release/notes/SUMMARY.ja.md) | いつ入った？ どのバージョンで直った？ |

## 表の読み方

**ほかのページの本文とこの表が食い違ったら、表が正です。**

- **推測の数字は書きません。** 誰も計測していない上限には、それらしい既定値を
  置かず「画面を見てください」と書いています（[limits.md](limits.ja.md)）。
- **「—」は「できない」とは限りません。** [features.md](features.ja.md) の詳細欄の「—」は
  「手順を書いたページがまだない」であって、機能がないという意味ではありません。
