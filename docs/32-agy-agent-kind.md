# 32. `kind=agy`（Antigravity CLI）実装計画 — 並行トラック構成

- 状態: **採用決定・計画**（2026-07-20）。設計と PoC 経緯は [decisions/0008](decisions/0008-antigravity-cli-agent-kind.md)。
- ゴール: `agy` を claude/codex/opencode と並ぶ第4のエージェント種別として組み込む。
  **M1 は Starter Quota で「実験枠」として成立させ、M2 で GCP プロジェクト経路を足して常用化**する。
- 前提（0008 の再 PoC で実証済み）: RDRAND 有効ホストで起動・OAuth 認証・`-p`・resume 動作確認済。
  実行方式は **Terminal (CLI)/tmux 一択**（v1.1.4 に構造化出力なし → Managed 不可）。

## 先に固定する契約（これで各トラックが独立に走れる）

**認証 API は claude 型**（認可 URL 表示 → コード貼付。codex の device-code 型ではない）:

| Method/Path | 意味 |
|---|---|
| `POST /api/connections/agy/start` | フロー開始 → `{flow_id, url}`（body: `{method?: "oauth"\|"gcp-project", project_id?}` — M1 は `oauth` のみ実装、フィールドだけ先に切る） |
| `POST /api/connections/agy/complete` | `{flow_id, code}` でコード投入 → オンボーディング完走 |
| `GET /api/connections`（既存） | 応答に `agy` フィールド追加（ProviderConn 形） |
| `DELETE /api/connections/agy` | ログアウト（TUI `/logout` スクレイプ or `~/.gemini/antigravity-cli/antigravity-oauth-token` 削除） |

**launch 契約**: `agy` を作業 dir で起動。resume は **cwd→最終会話マップ（`cache/last_conversations.json`）による `--continue`** を第一とし、CP 側で会話 UUID を保持できたら `--conversation <ID>`。会話一覧は `~/.gemini/antigravity-cli/conversation_summaries.db`（SQLite・平文）。

**ログイン状態判定**: `agy models` の成否（未認証時 "Please sign in" エラー）＋ token ファイル存在。

## トラック分割（Console の worktree セッションで並行実施を想定)

### Track A — workspace agent 本体（最重量・クリティカルパス）

現行コードはレジストリ構造に refactor 済みで、0008 記載の行番号は古い。実際の触り先:

1. `workspace/agent/internal/agents/agy/` パッケージ新設（**codex パッケージ `codex.go` がテンプレ**、auth は **claude の `auth.go` がテンプレ**）:
   - `agy.go` — `agentImpl`（`Kind/Caps/ForkSource/Transcript/BuildLaunch/WireLive/ClearResume`）
   - `program.go` — `buildProgram`: `agy` ＋ resume フラグ（上記 launch 契約）。`--model` は M1 では既定のまま
   - `auth.go` — `agents.Flow`（`StartFlow`/`WaitFor`）で TUI をスクレイプ。**画面列は 0008 再 PoC で実測済**:
     ログイン方式セレクタ（1=Google OAuth / 2=GCP project）→ 認可 URL（`accounts.google.com` regex）→
     コード貼付 → カラースキーム → ToS＋**Interactions データ収集トグル（既定オン→オフに倒す）** → workspace trust（Yes）
2. 登録: `internal/session/session.go:18-22` に `KindAgy`、`agent.go:21-34` レジストリ、
   `routes.go:182-190` に `agy.Handle*`、`connections.go:33-45` に `"agy": agy.Status()`
3. `fs.go:80-89` denylist に **`.gemini`** 追加（OAuth token が平文で home 配下）
4. AGENTS.md: `agy` はプロジェクト root の `AGENTS.md` を読むため **codex/opencode 型のシード不要**の見込み。
   rtk ブロックは `internal/agents/{codex,opencode}/rtk.go` に倣い `agy/rtk.go` ＋ `reconcileAgentRTK` に追加

### Track B — 配備（イメージ・entrypoint）

1. `workspace/Dockerfile` — npm 行（`:193-199`）とは**別 RUN** で `install.sh` 導入。
   **決定: `--dir /usr/local/bin` の root 設置**（他 CLI と同列・イメージ更新で追随・自己更新は封じる。
   `agy update` の自動起動があるか確認し、あれば無効化）。`&& agy --version` 検証、`versions.json`（`:200-206`）に追記
2. `workspace/entrypoint.sh` — シード要否の確認のみ（上記 A-4。不要なら触らない）
3. **RDRAND ガード**: entrypoint か agent 起動時に `grep -q rdrand /proc/cpuinfo` を確認し、
   非対応ホストでは agy kind を capability として無効化（Console のセレクタに出さない）。
   配備ドキュメントに RDRAND 要件を明記（0008）

### Track C — control-plane + Console

1. `control-plane/routes.go:383-406` — claude ブロック（`:397-399`）と同型に `/api/connections/agy/...` を追加（プロキシのみ）
2. Console:
   - `console/src/types/session.ts` — `SessionKind` union・`SESSION_KINDS`・`ConnectionsStatus.agy`
   - `console/src/agents/registry.ts` — descriptor 追加（`managedDriver` なし＝Terminal (CLI) 固定）
   - `console/src/features/settings/AgentsTab.tsx` — **`AgyCard`（`ClaudeCard :326` のクローン**、URL＋コード貼付型）。
     方式セレクタ（OAuth / GCP project）は M1 では OAuth のみ活性
   - `StartModal.tsx` / `SessionModals.tsx` — セレクタに露出
3. **実験枠の明示（採用条件）**: AgyCard に `/usage` スクレイプ由来の**残量%表示**（週次・2グループ制）と
   「実験枠（クォータ小・IDE/Jules と共有）」ラベル。agent 側に `GET /connections/agy/usage`（TUI `/usage` スクレイプ）を追加

### Track D — 調査（実装と並行、本セッション向き）

1. **GCP プロジェクト経路**（M2 の本丸）: TUI セレクタ選択肢 2 の画面列を実測 → auth.go の `method` 分岐仕様化。
   `gcloud` 連携要否、必要 API・課金、`quotaProject` の挙動、料金モデル、`/usage` 表示の変化。
   **要ユーザー準備: 課金有効な GCP プロジェクト**（下記「ユーザーに依頼する事項」）
2. クォータ実測の継続: 実タスク 1 回の消費%（Starter）→ 常用に必要な経路の見極めの裏付け
3. resume 耐久性: コンテナ再作成後の `--continue`（home 永続なので通る見込み）、長会話・複数 workdir
4. Managed 化 watch: `agy` の構造化出力（`--output-format` 相当）の提供状況を定期確認

## 依存関係と順序

```
契約固定（本ドキュメント） ──┬─ Track A（agent 本体）──┐
                              ├─ Track B（イメージ）    ├─ 統合 E2E（M1: Starter 実機）─ M2: GCP 経路（D1 → A/C に反映）
                              └─ Track C（CP+Console）──┘
Track D は常時並行（D1 はユーザーの GCP 準備待ち）
```

- A/B/C は上記 API・launch 契約のみを合意点として**相互依存なし**。worktree セッション 3 本で並行可。
- 統合 E2E は 3 トラック合流後: Console から agy セッション作成 → 認証 → 会話 → resume → logout、
  denylist・残量表示・RDRAND ガードの確認。
- M2（GCP 経路）は D1 の実測が済み次第、`auth.go` の method 分岐と AgyCard のセレクタ活性化のみ（構造は M1 で先置き）。

## マイルストーン / 完了条件

- **M1（Starter・実験枠）**: Console から作成・OAuth 認証（データ収集オフ既定）・会話・resume・logout・
  残量%表示・RDRAND 非対応ホストでの非露出、が実機で通る
- **M2（GCP 経路・常用化）**: method=gcp-project で認証完走、quotaProject 課金で週次枠の制約が外れることを実測、
  Connections UI で経路が見える
- **M3（常用判定）**: 実利用 1〜2 週間のクォータ/安定性データで 0008 に常用判定を追記

## ユーザーに依頼する事項（並行作業のブロッカー解消）

1. **GCP プロジェクトの用意**（D1/M2 用）: 課金有効化済みプロジェクト ID を Connections 設定時に使える形で。
   どの API 有効化が要るかは D1 冒頭で実測して提示する
2. Starter 実験枠の**クォータ消費許可**: D2/E2E で実タスク数回分（週次プールの数%〜十数%）を消費する
