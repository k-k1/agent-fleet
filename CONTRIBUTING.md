# Contributing to Agent Fleet

Thanks for your interest. This is a small project with a single maintainer, so a
few conventions keep things smooth.

## License of contributions

By submitting a contribution you agree it is licensed under the project's
**Apache License, Version 2.0** (see `LICENSE`). Apache-2.0 includes an explicit
patent grant; no separate CLA is required.

## Ground rules

- **Never commit secrets.** OAuth secrets, `AF_MASTER_KEY`, cookie secrets, and
  allowlists live in git-ignored files (`deploy/compose/.env`,
  `deploy/local/oauth.env`, `allowed-emails.txt`). Double-check `git diff` before
  committing. Compiled binaries are git-ignored too.
- **Keep the core deploy-agnostic.** Don't bake Docker/compose assumptions into
  the Control Plane. Deployment specifics belong behind the ports (Runtime,
  KeyCustodian, MetadataStore, AuthGateway) — see `docs/dev/09-deploy.md`.
- **Match the surrounding code.** Go: `gofmt` + `go vet` clean, `go test ./...`
  passing. Console: `npm run build` clean.
- **Run `gofmt` before every commit that touches Go — this is a hard gate.**
  `ci.yml` runs `gofmt -l .` per Go module and fails the build on *any* unformatted
  file, so `go build` / `go vet` / `go test` passing is not enough (editors and
  auto-format often skip `_test.go` files, which is exactly what has slipped through).
  Before committing, run in each touched module and make sure it prints nothing:
  ```bash
  (cd control-plane && gofmt -l .)      # lists unformatted files; empty = OK
  (cd workspace/agent && gofmt -l .)
  ```
  Fix with `gofmt -w <file>` (or `gofmt -w .`). Same for `go vet ./...`.

## Building & running

Local dev (host process): `deploy/local/run-dev.sh`.
On-prem container deploy: `deploy/compose/` (see its `README.md`).

Build paths on the dev host are non-standard: Go is at `$HOME/.local/go/bin`,
Node via nvm. The compose image build is self-contained (multi-stage) and needs
neither on the host — only Docker.

## Commits & PRs

**develop がトランク（default branch）**。日常の開発は develop への直 push /
随時マージで運用し（単独メンテナ・レビュー gate なし）、**「完了」の定義は
develop マージ済**。自分からブランチを切らない — worktree セッションは Console が
専用ブランチで払い出す。GitHub リモートは `git@github.com:k-k1/agent-fleet.git`。

**main は常時 CI 緑の安定ブランチ**で、develop→main の PR（リリーストレイン、
週 1〜2 回 or フェーズ完了時）でのみ更新する。hosted CI（ci.yml / e2e.yml /
contract 系）はこの PR と main push・毎晩の cron（develop）に集約し、develop への
push では回さない — per-commit の検証は各セッションのローカル実行
（gofmt/vet/test/build、下記）が担う。課金の背景と2層 CI 運用の全体像は
docs/35-packaging.md を参照。緊急修正は main から分岐 → PR → main、develop へ
back-merge する。リリースタグ・リリースビルド・公開配布はすべて main から切る。

小さく焦点の合ったコミットを心がけ、下記の形式に従う。

### コミットメッセージの形式

Conventional Commits に**日本語の subject** を組み合わせる。**subject も body も日本語で
書く**（このリポジトリは日本語で統一している。英語で書き始めたら書き直す）。

```
<type>(<scope>): <日本語の要約>     ← 1 行目。句点なし・要約/命令形・50 字目安

<本文>                              ← 空行を 1 つ挟む。何を・なぜ変えたか。挙動変更は
                                       真因→直し方→検証まで。折返し ~72 字。

Co-Authored-By: <実行モデル名> <noreply@<提供元>>   ← 末尾トレーラ（空行で分離）
```

- **type**: `feat` / `fix` / `docs` / `style`（整形のみ・挙動不変）/ `refactor` / `perf` /
  `test` / `build`（焼き込み CLI・依存の版上げ等）/ `chore` / `ci`。
- **scope**: 変更の主対象。実例 = `console` `chat` `cp`（= control-plane）`agent` `mirror`
  `tts` `workspace` `deploy`、ドキュメントは `docs(NN)`（章番号）。付けられるなら付ける。
- **body**: バグ修正・挙動変更は「真因 → 直し方 → 検証（どう確かめたか）」まで書き残す
  — このプロジェクトは実フリートで検証する前提で、コミット履歴が唯一の設計記録になる。
- **migration**: スキーマ変更（`control-plane/migrations/`）は前方互換を確認し body に明記
  （埋め込み migrator が CP 起動時に自動適用・ダウングレード非対応）。

### 帰属（Co-Authored-By トレーラ）

このプロジェクトは Claude Code / Codex / opencode を併用する。エージェントが書いた
コミットは末尾に空行を挟み、**実際に生成したモデル名**で共同著者を記す（CLI 名ではなく
モデルで帰属する）。

| 実行環境 | Co-Authored-By 例 |
|----------|-------------------|
| Claude Code | `Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>`（版により `Claude Fable 5` 等） |
| Codex | `Co-Authored-By: GPT-5.6 <noreply@openai.com>` |
| opencode | 実行モデルで帰属（Claude 系 → `@anthropic.com` / GPT 系 → `@openai.com`） |

- メール部は提供元ドメインの `noreply@`（Anthropic = anthropic.com / OpenAI = openai.com /
  その他はモデル提供元のドメイン）。モデル名は実行版に合わせる（固定しない）。
- **session URL 行は付けない**（旧 `Claude-Session:` トレーラは廃止）。ただし Claude Code は
  Remote Control 接続時に `Claude-Session:` 行を CLI が自動付与する — これは無害なので許容
  （抑止は agent-fleet 側でなく Claude Code の設定で行う）。
- **`Co-Authored-By:` は必ず付ける**（エージェントが関与したコミットは 1 行以上を必須と
  する）。複数のモデル／人が関与したら複数行並べる。純粋に人間だけのコミットのときだけ省略可。
