---
audience: "コードを変える人のうち、設計が乗っている前提を知りたい人"
source_of_truth: "決定記録。このページはその索引"
updated: "2026-09"
---

# 00. プロジェクトの前提 — 現状と、決着している仮定

[English](00-project-context.md) | 日本語

この棚の残りが乗っている前提です。議論になった行にはそれぞれ決定記録がありますが、
12 本の ADR から組み立て直さなくても読めるように、ここに 1 枚だけ置いています。

## 現状

**Phase 2 完了・Phase 3 進行中。** オンプレ 1 台の上で、複数の利用者が互いに見えない
まま並行して作業できます（利用者ごとの Workspace / AuthGateway / ネットワーク隔離 /
保存時暗号化）。Phase 3 の製品化はパッケージングと配布の節目（P3-10）まで来ていて、
Console の作り直し（React + Vite）、AWS ECS アダプタ（P3-7）、compose / ECS /
Docker 無しの native という配布形態が出荷済み、0.x のリリースを
[配布リポジトリ](https://github.com/k-k1/agent-fleet-dist)に公開しています。
先の計画は [roadmap.md](../roadmap.md)。

## 決着している仮定（v1）

| 論点 | 決定 | 理由・補足 |
|------|------|-----------|
| エージェント認証 | 利用者が自分のアカウント／席を Console から接続する（Claude: OAuth コード貼り付け、Codex: ChatGPT のデバイスコードか API キー、Copilot は GitHub 接続に相乗り、Cursor / Kiro: ブラウザサインイン、OpenCode: プロバイダの API キーか opencode アカウント） | Console が各人の認証状態を出して再ログインを促す。端末で手動 `/login` する逃げ道も残っている |
| 利用者の隔離 | 1 利用者 1 コンテナ | 移植性が高く隔離が強い。AWS にもよく載る |
| 想定規模 | 同時 20 人程度 | 1 クラスタ＋オーケストレーション層で足りる |
| 永続化 | `local`=バインドマウント / `aws`=EBS/EFS | ホーム・クローン・資格情報・履歴をディスクに残す |
| git 認証 | Console（接続）経由の HTTPS トークン／OAuth | SSH 鍵から格下げ。CP は秘密を持たない（[decisions/0003](../decisions/0003-ssh-to-connections.ja.md)） |
| 技術スタック | Console=React+Vite / バックエンド=Go | デーモン・WS 中継・コンテナ制御に Go が向く（[decisions/0004](../decisions/0004-vanilla-to-react.ja.md)） |
| 提供モデル | パッケージ製品・会社ごとに自社ホスト | 1 社 1 配備。SaaS は ToS の理由で断念（[decisions/0001](../decisions/0001-self-host-vs-saas.ja.md)） |
| デプロイ層 | 1 つの中核の上で local / aws を差し替える | ポートとアダプタで分離（local = Docker・local-first） |

## 何から作られたか

個人でエージェントを回していた仕組みが先にあり、製品はそれを一般化したものです。
どの部分がどこから来たかを知っていると、コードのいくつかの形が説明できます。

- **`oauth2-proxy`** — Google のドメイン限定認証ゲート（`emails.txt` の allowlist）。
  **CP 自身の Google OAuth（`AUTH=oauth`）に置き換え済み**で、allowlist は
  `deploy/local/allowed-emails.txt`（メールか `@domain`）になりました。設計は
  [07 §7.3](07-security.ja.md)。
- **`scripts/tmux-claude.sh`** — detached な tmux の中で複数の Claude CLI を冪等に
  起動・再開・世代管理していたもの。[04](04-agent.ja.md) のセッションモデルはこの子孫です。
- **`CLAUDE_CONFIG_DIR` によるプロファイル分離** — ディレクトリごとに別の `~/.claude`。
- **`~/.claude/settings.json`** に `remoteControlAtStartup` と
  `skipDangerousModePermissionPrompt` を仕込んでいたこと。

## スクリーンショット

リポジトリの README に載せている画像は、実際の Console のバンドルをデモ用データに対して
撮ったもので、両言語ぶんあります。作り直しは
`node console/scripts/shots/capture.mjs --locale en`（既定のロケールは `ja`）。
手順は[こちら](../../console/scripts/shots/README.md)。
