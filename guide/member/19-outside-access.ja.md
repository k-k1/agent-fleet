---
audience: "ワークスペースを外部のエージェントやアプリから使う人"
---

# 19. ワークスペースを外から使う — MCP と OAuth

[English](19-outside-access.md) | 日本語

この章の内容は、次のページに移りました。

| やりたいこと | ページ |
|---|---|
| 手元の Claude Code / Claude Desktop から、トークンでワークスペースのセッションを動かす | [外部の Claude からワークスペースを動かす](../user/outside/external-claude.ja.md) |
| claude.ai・ChatGPT・Claude Code・Codex を、トークンなしのサインインでつなぐ | [リモートの MCP クライアントをサインインでつなぐ](../user/outside/oauth-client.ja.md) |
| 手元の Claude Code で始めた作業を、ワークスペースのセッションで続ける | [手元の Claude Code の作業をワークスペースに移す](../user/outside/move-work-in.ja.md) |

外部のエージェントから Agent Fleet のメモリを使う方法は、下に残っています。

## 外部のエージェントからメモリを使う

### メモリ専用のコネクタ（claude.ai 向け）

claude.ai は、そこで追加したコネクタをアカウントのすべての Claude Code セッションに渡します。このワークスペースで動いている
セッションも含みます。そのため claude.ai のコネクタを `/mcp` につなぐと、それらのセッションにあなたのメンバー権限そのものが
渡ってしまいます（[このデプロイの /mcp をセッションにつながない](../user/outside/external-claude.ja.md#このデプロイの-mcp-をセッションにつながない)）。**`/mcp/memory`** につなぐコネクタなら、そうはなりません。

- `/mcp/memory` 向けに作ったトークンや OAuth 接続が一覧・実行できるのは、メモリの 8 つのツール（`memory_projects`・
  `memory_index`・`memory_search`・`memory_read`・`memory_save`・`memory_patch`・`memory_feedback`・`memory_forget`）だけです。`/mcp` はそれを拒否し、
  `/mcp/memory` も `/mcp` 向けのトークンや接続を拒否します。切り替えるには接続し直します。
- ワークスペースのセッションは、組み込みの af ツールで同じメモリをすでに使えます。このコネクタがセッションに渡っても、
  新しくできることは増えません。
- 確認ページには、アプリが求めているのはメモリだけだと表示されます。あなたのメモリ設定、テナント管理者のブロック、
  `read` / `write` のスコープ、監査ログは `/mcp` とまったく同じように効きます
  （[18 エージェントメモリ](18-agent-memory.ja.md#外部のエージェントから-agent-fleet-のメモリを使う)）。

すでに `/mcp` でつないでいる claude.ai のコネクタは、そのまま動き続けます。移すには claude.ai でそのコネクタを削除し、
`/mcp/memory` のアドレスで追加し直してください。`/mcp` は、Claude Desktop やコマンドラインのクライアントのように自分の
パソコンだけで動き、セッションのツールが要るクライアントに使ってください。

### 外部のエージェントから Agent Fleet のメモリを使う

同じ MCP トークンで、手元の Claude Code や Claude Desktop に Agent Fleet のメモリを使わせられます。claude.ai には
[メモリ専用のコネクタ](#メモリ専用のコネクタclaudeai-向け)を使います。
[18 エージェントメモリ](18-agent-memory.ja.md#外部のエージェントから-agent-fleet-のメモリを使う)を見てください。
