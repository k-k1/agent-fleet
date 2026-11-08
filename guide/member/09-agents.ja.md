---
audience: "エージェントを選んで接続する人"
---

# 09. エージェント — 接続の仕方と選び方

[English](09-agents.md) | 日本語

この章の内容は、次のページに移りました。

| やりたいこと | ページ |
|---|---|
| 使うエージェントを選ぶ、接続する、動作設定や使わないモデルを決める | [エージェントを選ぶ](../user/agents/choose.ja.md) |
| Claude Code を接続する、リモートコントロールを使う | [Claude を接続する](../user/agents/claude.ja.md) |
| Codex を接続する | [Codex を接続する](../user/agents/codex.ja.md) |
| OpenCode を接続する、課金のしかたや API キーを決める | [OpenCode を接続する](../user/agents/opencode.ja.md) |
| GitHub Copilot を使う | [GitHub Copilot を接続する](../user/agents/copilot.ja.md) |
| Cursor を接続する | [Cursor を接続する](../user/agents/cursor.ja.md) |
| Kiro を導入して接続する | [Kiro を接続する](../user/agents/kiro.ja.md) |
| Antigravity（agy）を接続する | [Antigravity を接続する](../user/agents/agy.ja.md) |
| Muse Code を導入して接続する | [Muse Code を接続する](../user/agents/muse.ja.md) |
| 配備の llama.cpp エンジンや自分の llama-server を使う | [lcpp を使う](../user/agents/lcpp.ja.md) |
| モデルの価格、使用量チップ、コンテキストの残り、RTK、エージェント使用量を見る | [料金と使用量を確かめる](../user/agents/usage.ja.md) |
| 自分の働き方を一度だけ書いておく（エージェントへの指示） | [作業のしかたを一度だけ書いておく](../user/agents/instructions.ja.md) |
| 自分の Claude Code のスキル・フック・サブエージェントを持ち込む | [手元の Claude Code の設定を持ち込む](../user/agents/bring-claude-setup.ja.md) |

種類ごとにできることは [エージェントリファレンス](../ref/agents.ja.md) にあります。

## アシスタントチャットの既定モデル

各アシスタントで個別にモデルを指定でき、claude の
既定は管理者が配備全体で変えることもできます。会話用途のため高速・低コストの
ティアを既定にしています: claude → Sonnet 5 · codex → 一覧にある最新の Luna（執筆時点で `gpt-6-luna`） · opencode →
一覧にあれば `opencode-go/glm-5.2`、無ければ `opencode/nemotron-3-ultra-free` · agy → Gemini 3.5 Flash · cursor → Cursor 自身の既定
（Auto）。muse は高速ティアではなく、推奨のままにすると、contributor 用ではない Muse Code の最新モデルを選びます。
Meta に会話を使われるかどうかは、モデルとは別に、自分の Contributor モードの設定に従います（[エージェントリファレンス](../ref/agents.ja.md)のアシスタントチャットの注）。そもそもどの種別が
アシスタントチャットになれるかは、同じ表の「アシスタントチャットとして利用」の行にあります。たとえば
cursor のアシスタントは**読み取り専用**で動きます。
