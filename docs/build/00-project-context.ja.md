---
audience: "コードを変える人のうち、設計が乗っている前提を知りたい人"
source_of_truth: "決定記録と guide/ref。このページはその索引"
updated: "2026-09"
---

# 00. プロジェクトの前提 — 現状と、決着している仮定

[English](00-project-context.md) | 日本語

この棚の残りが乗っている前提です。議論になった行にはそれぞれ決定記録がありますが、
決定記録を 1 本ずつ読んで組み立て直さなくても読めるように、ここに 1 枚だけ置いています。

## 現状

Agent Fleet は 0.x のリリースとして出荷していて、リリースノートと一緒に
[配布リポジトリ](https://github.com/k-k1/agent-fleet-dist/releases)に公開しています。
領域ごとに何ができていて何ができていないかは
[01 §1.7](01-architecture.ja.md)にあります。

フェーズの計画（Phase 0〜3 と `P3-n` の節目）は 2026-09-29 に凍結し、以後は保守していません。
古い決定記録やジャーナルに残るフェーズの呼び名は経緯として読んでください。Phase 3 の
条件でただ 1 つ残っているもの——プロジェクトの外の誰かが配布バンドルとその手順書だけで
導入し、E2E を記録して通すこと（[decisions/0001](../decisions/0001-self-host-vs-saas.ja.md)）——は
[#1175](https://github.com/k-k1/agent-fleet/issues/1175) です。ほかの未完の作業はすべて
[GitHub の Issue](https://github.com/k-k1/agent-fleet/issues) にあります。

## 決着している仮定（v1）

| 論点 | 決定 | 理由・補足 |
|------|------|-----------|
| 提供モデル | パッケージ製品・会社ごとに自社ホスト | 1 社 1 配備。SaaS は ToS の理由で断念（[decisions/0001](../decisions/0001-self-host-vs-saas.ja.md)） |
| エージェント認証 | エージェント CLI のアカウントは各メンバーが自分のものを持ち込み、Console から接続する | 会社ごとに自社ホストする理由がこれ（[decisions/0001](../decisions/0001-self-host-vs-saas.ja.md)）。例外は `lcpp` で、配備のエンジンか、メンバーが指した llama.cpp サーバの上で動き、ログインが無い。kind ごとのログインの仕方は [ref/agents](../../guide/ref/agents.ja.md#ログインの仕方) |
| 利用者の隔離 | メンバーシップ（テナントの中の 1 人）ごとに 1 ワークスペース | `native` 以外のすべての形態でコンテナ。`native` は設計上 1 人用（[ref/deploy-targets](../../guide/ref/deploy-targets.ja.md)）。タスクごとの環境でなくメンバーごとに長寿命のワークスペースを 1 つ持たせる理由は [decisions/0104](../decisions/0104-long-lived-member-workspace.ja.md) |
| 想定規模 | 1 配備あたり数十〜100 人程度・同時 20 人程度を想定した大きさ | どちらも想定であって実測した上限ではない。1 台のホストか 1 つの ECS クラスタで足りるつもりで作っている。配備全体の上限はコードに無く、テナントにはワークスペース数とセッション数の上限を設定できる（[ref/limits](../../guide/ref/limits.ja.md)） |
| デプロイ層 | 1 つの中核。ランタイムアダプタは `AF_RUNTIME` で選ぶ | `docker`（既定）・`native`・`ecs`・`ecs-ec2` をポートとアダプタの裏に置く（[01 §1.6](01-architecture.ja.md)）。形態ごとの違いは [ref/deploy-targets](../../guide/ref/deploy-targets.ja.md) |
| 永続化 | ワークスペースが持ち続けるデータ——作業コピー・CLI のログイン・手元の会話履歴——は、停止と再開をまたいで残る | 全部がホームにあるわけではない。Claude の状態は専用のディレクトリにあり（下の `CLAUDE_CONFIG_DIR`）、`ecs-ec2` ではホームが EBS で、選んだ一部の資格情報・身元のファイルだけが EFS に残る。形態ごとのホームの置き場は [ref/deploy-targets](../../guide/ref/deploy-targets.ja.md) の「ホームの置き場」列。手元に保存されない会話もあり、たとえば Managed の cursor の会話は Cursor のサーバに残る（[ref/agents](../../guide/ref/agents.ja.md)） |
| git 認証 | Console（接続）経由の HTTPS トークン／OAuth | SSH 鍵から格下げ（[decisions/0003](../decisions/0003-ssh-to-connections.ja.md)）。メンバーのトークンはそのワークスペースの暗号化ストアにあり、CP は通すが持たない。CP が持つのはテナントの OAuth アプリの秘密（[08 §8.1](08-integrations.ja.md#81-連携一覧)） |
| 技術スタック | Console=React+Vite / バックエンド=Go | Console の React + Vite は [decisions/0004](../decisions/0004-vanilla-to-react.ja.md)。Control Plane と Workspace Agent は Go（2 つのモジュール）で、デーモン・WebSocket 中継・コンテナ制御に向く。この選択を論じた決定記録は無い |

## 何から作られたか

個人でエージェントを回していた仕組みが先にあり、製品はそれを一般化したものです。
どの部分がどこから来たかを知っていると、コードのいくつかの形が説明できます。

- **`oauth2-proxy`** — Google のドメイン限定認証ゲートで、allowlist は `emails.txt`。
  **CP 自身のログイン（`AUTH=oauth`）に置き換え済み**で、今は Google・GitHub・任意の
  OIDC プロバイダを受けます。oauth2-proxy のようなゲートを信頼する `AUTH=proxy` も残っています。
  ファイルの形式は `AF_OAUTH_ALLOWED_EMAILS_FILE` として残っています（1 行に 1 つのメールか
  `@domain`、書き換えに再起動は要らない）。設計は [07 §7.3](07-security.ja.md)。
- **`tmux-claude.sh`** — その個人の仕組みにあったスクリプトで、このリポジトリに入ったことは
  ありません。detached な tmux の中で複数の Claude CLI を冪等に起動・再開・世代管理していました。
  [04](04-agent.ja.md) のセッションモデルはこの子孫で、長寿命の形が残った理由は
  [decisions/0104](../decisions/0104-long-lived-member-workspace.ja.md) です。
- **`CLAUDE_CONFIG_DIR` によるプロファイル分離** — ディレクトリごとに別の `~/.claude`。
  今はどのランタイムもワークスペースごとに 1 つ、閲覧できるホームの外に置きます。
- **`~/.claude/settings.json`** に `remoteControlAtStartup` と
  `skipDangerousModePermissionPrompt` を仕込んでいたこと。`workspace/entrypoint.sh` は今も
  新しいワークスペースの `$CLAUDE_CONFIG_DIR` に既定の `settings.json` を置き（起動時の
  Remote Control は off）、その後は Console の Claude 設定がこのファイルの持ち主になります。

## スクリーンショット

リポジトリの README に載せている画像は、実際の Console のバンドルをデモ用データに対して、
ロケールごとに 1 回ずつ `console/scripts/shots/capture.mjs` で撮ったものです。
手順は[こちら](../../console/scripts/shots/README.md)。
