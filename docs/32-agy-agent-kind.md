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

## Track D 実測結果（2026-07-20、Starter / Gemini 3.5 Flash Medium）

### D-2 クォータ消費率（週次 Gemini プールに対する%、`/usage` 前後差分で実測）

| 呼び出し | 内容 | 消費 |
|---|---|---|
| 極小 `-p`（挨拶レベル） | 前日 PoC | **1.01%** |
| **許可不足で即失敗**した `-p`（6〜9 秒、出力なし）×3 | permissions 未設定 | **1.3〜2.5%/回**（計 5.04%） |
| 実タスク `-p`（repo 3 ファイル読解→12 項目要約、19 秒） | T1 | **6.01%** |
| resume 上の小プロンプト ×4 | R1〜R4 | **0.5〜1.5%/回** |

- 実タスク基準で**週次プール ≈ 16 タスク分**。多ターンセッション（実タスク＋10 ターン）換算では
  **週 6 セッション前後で枯渇**。Starter 常用不可の定量的裏付け（0008 の判定どおり）。
- **失敗呼び出しでも 1〜2.5% 消費する**。統合実装は権限設定を正しく出荷しないとクォータを空費する
  （下記 D-5）。Claude/GPT プールは全テストを通じ 100% のまま（別プール実証）。

### D-3 resume 耐久性 — ✅ 合格

`--continue` 連鎖 ×3 ＋ `--conversation <UUID>` ×1 を**全て別プロセス**（毎回 CLI 新起動＝再起動相当）、
うち 1 回は**別 cwd** から実行。読んだファイル・初回指示（12 項目・3 トピック）・累計メッセージ数
（5 を正答）まで完全に想起し、破綻なし。resume 時の再読み込みは 0.5〜1.5%/回と安価
（コンテキストキャッシュがプロセスを跨いで効いている）。状態は全て `~/.gemini/` 配下＝home 永続
なので、コンテナ再作成後も同等に再開できる見込み（プロセス再起動と同型）。

**統合実装への注意点（スロット対応で必須）**:

1. `--conversation <ID>` を別 cwd で実行すると**その cwd の最終会話マッピング
   （`cache/last_conversations.json`）が上書きされる**。`--continue` 依存はスロット間の
   取り違えリスクがある → **codex の sid-store 同様、CP/agent 側で会話 UUID を明示保持**して
   `--conversation` で resume するのが安全。
2. UUID の即時ソースは `cache/last_conversations.json`（cwd→ID）または `conversations/` の最新
   `.db`。`conversation_summaries.db` への反映は**遅延書き込み**で当てにできない。
3. `--continue` を付けない `-p` は**毎回新規会話を作る**（失敗呼び出しも会話を残す）。

### D-5（新規判明）headless の権限モデル

- `-p`（print モード）は**ツール許可プロンプトを自動拒否**する。回避は
  (a) `~/.gemini/config/config.json` の `permissions.allow`（`antigravity-cli/settings.json` では
  ないことをログ `cli_setting_manager.go` で確認。`read_file` は通ったが `command` 等ツール名の
  全列挙が必要）、(b) `--dangerously-skip-permissions`。
- Agent-Fleet の実行方式は Terminal (CLI)＝TUI（ユーザーが対話承認）なので常用経路には影響しないが、
  ヘッドレス補助利用や E2E では allow リスト整備が前提。

## Track D: 個人 AI サブスク経路の比較（AI Plus / Pro / Ultra、2026-07-20 Web 調査）

GCP プロジェクト経路（未着手）の代替として、個人 Google アカウントの有償サブスクで
常用が成立するかを調査した。プラン体系は 2026 年に大きく動いている
（I/O 2026 で Ultra 2 段化・compute ベース制へ移行、2026-06 に Plus 値下げ）。

| | Starter（無料） | AI Plus | AI Pro | AI Ultra | AI Ultra 上位 |
|---|---|---|---|---|---|
| 月額（US） | $0 | **$4.99**（2026-06 改定、旧 $7.99） | **$19.99** | **$100**（I/O 2026 新設・開発者向け） | **$200**（旧 $249.99） |
| Antigravity 枠 | 週次極小（実測: 実タスク ≈16 回/週） | 「より多くのアクセス」あり・倍率非公表（Gemini アプリは無料比 2 倍。**Antigravity 常用には細すぎる見込み**） | higher limits（対 Starter 倍率は**非公表**。ヘビー利用は Claude 系で週数プロンプトの枯渇報告あり） | Gemini アプリ・**Antigravity とも Pro 比 5 倍** | **Pro 比 20 倍** |
| リフレッシュ | 週次のみ（実測） | 5 時間毎に回復 → 週次上限で停止（compute ベース、全有償プラン共通） | 同左 | 同左 | 同左 |
| クレジット追い足し | 不可 | 参考値 200/月（第三者集計） | **可**（pay-as-you-go。参考値 1,000/月） | 可（参考値 12,500/月） | 可（同左） |
| 学習利用 | 既定オン・オプトアウト可 | **全消費者プランで同一**: 有償でも既定は学習対象、Gemini Apps Activity ＋ Antigravity 側設定（agy はオンボーディングの Interactions トグル）でオプトアウト。**プランを上げても既定は変わらない** | 同左 | 同左 | 同左 |
| アカウント | 個人のみ | **個人 Google アカウント限定（Workspace アカウントは加入不可）** | 同左 | 同左 | 同左 |

（クレジット参考値は第三者集計 [digitalapplied](https://www.digitalapplied.com/blog/google-ai-plans-free-plus-pro-ultra-2026)、
5×/20× と 5h/週次制は [Google 公式ブログ](https://blog.google/products-and-platforms/products/google-one/google-ai-subscriptions/)、
Plus 値下げは [9to5Google](https://9to5google.com/2026/06/08/google-ai-plus-price-drop/)、
Antigravity の枠・クレジットは [AI Pro benefits](https://support.google.com/googleone/answer/14534406)）

### 判定（0008 初版の「個人 Pro=検証どまり」の再評価）

- **個人利用デプロイの常用: AI Pro（$19.99）が本命**。常用成立の鍵は倍率よりも
  (a) **5 時間リフレッシュ**（Starter の週次一発とは回復モデルが別物）と
  (b) **クレジット追い足しで枯渇が「待ち」でなく「金」で解決できる**こと。
  Plus は Antigravity 枠が細く常用不足、Ultra は重使用者向けの増量版（モデル・機能の質差ではなく量差）。
  ヘビーな Claude 系利用は Pro でも枯渇報告が多く、その場合は Ultra $100 かクレジット前提。
- **会社デプロイの常用: 3 プランとも不適格のまま**（0008 初版判定を維持）。理由は量ではなく構造:
  ①個人アカウント限定で会社がシートを所有できない、②学習除外が**各ユーザーのオプトアウト
  設定頼み**（Workspace/AI Ultra for Business のような契約上の不収集がない）、③会社経費で
  個人サブスクを負担する BYO グレー（claude の個人 Pro/Max を避けたのと同型）。
  → **会社常用は引き続き GCP プロジェクト経路が唯一の妥当解**。コスト比較（定額 vs 消費課金）は
  GCP 経路実測（D-1）でトークン規模を掴んでから。
- **未確定事項**: Pro の対 Starter 倍率（非公表）、CLI の `/usage` 表示が有償プランでどう変わるか
  （5h リフレッシュ行・クレジット残高の露出）は**実測が必要** → D-4（要ユーザー承認・課金）。

## Track D-4 実測結果: AI Pro 実測（2026-07-20、アップグレード後）

ユーザー承認のもと AI Pro（$19.99/月）へアップグレードし再実測した。**結論: 個人利用
デプロイの常用は AI Pro で数字上成立する。**

### `/usage` 表示の変化

- 各モデルグループに **Weekly Limit に加えて Five Hour Limit のバーが出現**（Starter は週次のみ）。
  説明文言も「weekly limit **and a 5-hour limit**」に変化。5h 枠はバースト抑制、週次枠がティア連動。
- ヘッダの「(Antigravity Starter Quota)」表記は消えた（メールのみ）。**クレジット残高の表示は
  CLI には無い**（枯渇時か Web アカウント側でのみ露出の見込み — 未確認）。
- アップグレードで両プールとも 100% にリセットされた。

### 消費率の再実測（同一タスクで Starter と直接比較）

| 計測（`-p`、skip-permissions） | Starter 週次 | **Pro 週次** | **Pro 5h** |
|---|---|---|---|
| 実タスク T1（Gemini Flash Medium、repo 3 ファイル→12 項目） | 6.01% | **0.22%** | 1.33% |
| 実タスク（**Claude Sonnet 4.6 (Thinking)**、repo 2 ファイル→8 項目） | 未計測 | **1.23%** | 3.69% |
| resume 小プロンプト（`--continue`） | 0.5〜1.5% | **0.03%** | 0.19% |

- **Pro の週次 Gemini プール ≈ Starter の 27 倍**（同一タスク 6.01%→0.22%）。
  週次換算: **Gemini ≈455 実タスク/週（5h 窓あたり ≈75）、Claude 系 ≈81 実タスク/週（5h 窓 ≈27）**。
- 5h プールは週次プールの約 1/6（Gemini）〜1/3（Claude）。1 週間には 5h 窓が 33 個あるので
  **持続利用の拘束は週次側**、5h 枠は瞬間的な連打だけを制限する。
- Gemini/Claude プールの独立を再確認（Claude タスク中 Gemini 側は不変）。
- 副産物: **クロスモデル resume が成立**（Claude モデルで開始した会話を既定 Gemini Flash の
  `--continue` で完全に引き継ぎ）。モデル切替を挟むスロット運用に制約なし。
- 旧報道の「Pro は Claude 数プロンプトで週枯渇」（2026-03 期）は**現行制度では再現しない**
  （I/O 2026 のクォータ改定後とみられる）。

### 常用判定（個人利用デプロイ）

1 日 20 実タスク＋resume 多数の重めの個人利用でも週次 Gemini 消費 ≈5%/日、Claude 系を
1 日 10 タスク使っても ≈12%/日 ≒ 週 86% で収まる。**AI Pro で常用成立**。枯渇時は
クレジット追い足しで回復可能（Pro の特典）。Console 側は D 実測どおり `/usage` スクレイプで
残量%（週次・5h の 4 バー）を出せばよい。学習除外はオプトアウト設定の維持が引き続き前提。

## ユーザーに依頼する事項（並行作業のブロッカー解消）

1. **GCP プロジェクトの用意**（D1/M2 用）: 課金有効化済みプロジェクト ID を Connections 設定時に使える形で。
   どの API 有効化が要るかは D1 冒頭で実測して提示する
2. Starter 実験枠の**クォータ消費許可**: D2/E2E で実タスク数回分（週次プールの数%〜十数%）を消費する — ✅ 許可済み・実測完了
3. **AI Pro 契約の承認**（D-4 用・任意）: $19.99/月（初年 50% オフのキャンペーン報道あり）。
   契約後に `/usage` 表示の変化・実効クォータ・クレジット露出を再実測し、個人利用デプロイの
   常用可否を確定させる。
   — **状態（2026-07-20）: ✅ 承認・アップグレード・実測完了**（上記 D-4 実測結果）
