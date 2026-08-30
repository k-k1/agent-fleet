# 21. チャット履歴からの会話 fork（ワンクリック分岐）

> 🗄 **実装記録**。チャットビュー（`MirrorView`）から、いまの会話を引き継いだ新セッションを 1 クリックで分岐する。
>
> ⚠️ **§21.1 の「任意メッセージからの厳密分岐は非サポートにつき却下」は 2026-08 に覆った。**
> codex / opencode は公式に分岐点パラメータを持ち、claude も切り詰め resume が実在する。
> 後継設計は [55-fork-at-message.md](../log/55-fork-at-message.md)、判断は
> [decisions/0039](../decisions/0039-fork-at-message.md)。本書の残りは当時の実装記録として有効
> （ここで作った `ForkFrom` と `handleForkSession` が後継の土台になる）。

## 21.1 ゴールと採用範囲

チャットを読みながら「ここまでの文脈ごと別方向を試したい」を摩擦なく。**採用＝会話まるごと分岐（案 A）**。
Claude Code 公式サポートの `--fork-session` だけで完結させ、jsonl 改変には踏み込まない（堅牢・バージョン変化に強い）。

**却下した案**: 任意メッセージからの厳密分岐は Claude Code 非サポート。Console まで届く per-message 識別子は
`idx`（jsonl 行番号）のみで `uuid`/`parentUuid` は Agent が捨てており、しかも `idx` は compaction で不安定＝
恒久アンカーにならない。jsonl 切り詰めで種を作るのは非サポート（スキーマ内部仕様・更新で壊れる）ため採らない。

## 21.2 効く事実（検証済み）

コンテナ内 `claude` で実挙動を確認: **`claude --resume <A> --fork-session --session-id <B>`** は
`<B>.jsonl` に fork 履歴を書き（元 `<A>.jsonl` は無傷）、**`--session-id` が fork 先 sid を固定する**。
これにより Agent の決定的マッピング `sid = UUIDv5(dir|name)` を保ったまま fork できる（新規 state 不要）。

## 21.3 実装（コア無改修・港越し）

- **Agent** (`session.go`):
  - `sessionMeta.ForkFrom`（SOURCE の sid）を追加。**初回起動時のみ**効く。
  - `buildSessionProgram(sid, model, label, forkFrom)`: 自分の jsonl が既にあれば通常 resume（＝fork 後の再起動は
    再 fork しない）／無ければ forkFrom があれば `claude --resume <forkFrom> --fork-session --session-id <sid>`。
  - `POST /sessions/{name}/fork` = `handleForkSession`: source meta 検証（claude 限定・dir 存在・`jsonlResumable`）→
    `deriveForkName`（`<name>-fork`／`-fork2`…、40 字以内・`[A-Za-z0-9_-]`）→ ForkFrom 付き meta で `startSessionTmux`。
- **CP** (`runtime.go`/`main.go`): `POST /api/sessions/{name}/fork` = `handleSessionFork`（auto-start ＋ セッション
  クォータ→proxy）。クォータ判定は `sessionQuotaExceeded` に抽出し create と共用。
- **Console** (`MirrorView.jsx`): チャット見出しに **⑂ 分岐** ボタン（claude セッションのみ）。押下で
  `POST api/sessions/{name}/fork` → 成功時は新セッションを split ペインで開き `bumpSessions()`。元は残る。

## 21.4 検証

- claude CLI 実挙動（fork 先 sid 固定・元無傷）: コンテナ内で確認。
- `buildSessionProgram` の fork 分岐が上記コマンドを厳密生成 / `deriveForkName` の命名・truncation: 単体テスト
  （`session_fork_test.go`）。
- Agent ハンドラのルーティング＋バリデーション: ホスト上の隔離 Agent で 404(no_session) / 400(not_claude) /
  400(not_resumable) を確認。
- CP/Agent `go build`+`vet`+`test`、Console `vite build` 通過。
- 実会話込みの happy-path E2E は運用者の claude 認証が要るため未実施（上記の合成で担保。反映後に実ブラウザ確認）。

## 21.5 触れたファイル

- `workspace/agent/session.go`（ForkFrom / buildSessionProgram / handleForkSession / deriveForkName）、
  `workspace/agent/main.go`（route）、`workspace/agent/session_fork_test.go`（新規）。
- `control-plane/runtime.go`（handleSessionFork / sessionQuotaExceeded 抽出）、`control-plane/main.go`（route）。
- `console/src/views/MirrorView.jsx`（⑂ 分岐 ボタン + doFork）、`console/src/styles.css`（`.fork-btn`）。

## 21.6 残り / 後続

- 反映には **Workspace イメージ再ビルド＋コンテナ recreate**（Agent 変更）＋ CP/Console 再起動が要る。
- 将来「特定発言から」を本当に要るなら、fork 後の新ブランチで `/rewind` へ誘導する UI（案 B）を上乗せ可能。
  厳密自動化は rewind が対話前提ゆえ非推奨。
