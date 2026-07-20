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

### Track A — workspace agent 本体（最重量・クリティカルパス）— **実施済（2026-07-20）**

実装は下記計画どおり `internal/agents/agy/` ＋登録一式（`KindAgy`・レジストリ・
`/connections/agy/{start,complete}`＋DELETE・`fs.go` denylist `.gemini`・`agent_rtk.go`）。
Track B の `hostcaps.AgyStatus()` を `Status()`（supported/reason）と `BuildLaunch` に配線済み。
実装時の実測・設計確定事項:

- **オンボーディング画面列とキー操作を実測確定**（v1.1.4、フェイク HOME＋tmux）:
  ログイン方式セレクタ（Enter=OAuth 既定）→ 認可 URL（`accounts.google.com/o/oauth2/auth` regex、
  4000 桁 PTY で非折返し）→ コード貼付（コードと Enter は別 write）→ カラースキーム（Enter）→
  ToS＋Interactions トグル（**Enter=トグルオフ → ↓ → → → Enter=Done**）→ trust（Enter=Yes）→
  メイン画面（`? for shortcuts`）。完了後 `enableTelemetry=false` を settings.json に**追い書きで固定**。
- **workspace trust は `settings.json` の `trustedWorkspaces` へ事前追記でスキップ可**（実測）→
  `BuildLaunch` が起動 dir を毎回事前 trust。auth フローも専用 `login-flow` dir を事前 trust。
- **resume は Track D-3 指摘どおり sid-store 方式**: `--continue` は使わず、
  `cache/last_conversations.json` の cwd エントリを**起動前スナップショットと比較**して
  「この起動が作った会話 UUID」だけを採用（stale エントリの取り違え防止）→ `--conversation <UUID>`。
- **グローバル AGENTS.md は `~/.gemini/AGENTS.md`**（実測: 対話・headless 両モードで読む。
  `~/.gemini/antigravity-cli/AGENTS.md` はどちらも読まれない。**プロジェクト root の AGENTS.md は
  対話 TUI のみ**が読み、`-p` headless は読まない）。rtk ブロックは `~/.gemini/AGENTS.md` に適用。
  → **Track B への回答: M1（Terminal 一択）は entrypoint シード不要**（TUI が root AGENTS.md を読む）。
  headless 補助利用を始める時はグローバル側へのシードを再検討。
- **Status() の email/plan**: token ファイルに identity が無いため、認証完了時にメイン画面ヘッダの
  「email (plan)」行をスクレイプして `~/.config/agent-fleet/agy-account.json` に保存（best-effort）。
- **Caps は M1 では全オフ**（fork なし・transcript なし・label なし）→ **Track C 連絡: registry の
  `chat`/`transcript` フラグは false に倒すこと**（summaries.db は遅延書き込みで M1 ミラー不成立）。
- 検証: 全 342 テスト通過＋live テスト（`AF_AGY_LIVE=1`: 実 TUI での start フロー URL 取得・
  接続済み 409 ガード・/usage スクレイプ）。HandleComplete のコード投入以降は画面列を tmux で
  手動完走済み（実コードが要るため自動 E2E は統合フェーズ）。

（以下は着手時の計画）

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

### Track B — 配備（イメージ・entrypoint）— **実施済（2026-07-20）**

1. `workspace/Dockerfile` — npm 行とは別 RUN で `install.sh --dir /usr/local/bin` 導入（root 設置）。
   実装で判明した事実:
   - **install.sh に版ピンは無い**（常に latest manifest、sha512 検証つき）。`ARG AGY_VERSION` は
     キャッシュバスタ兼「ビルド時に latest だった版」の記録で、`versions.json` に `"agy"` として
     書き出す（設定 UI の「ピン vs 実体」でドリフトが見える）
   - **バックグラウンド自己更新は実在**（install.sh に明記）。バイナリ実測で
     `AGY_CLI_DISABLE_AUTO_UPDATE` 環境変数を発見 → Dockerfile で `=1` に設定して封殺
     （claude の `DISABLE_AUTOUPDATER` と同じ理屈。明示的な `agy update` は可能なまま）
   - RDRAND 非提示ビルドホストでは `agy --version` 自体が SIGABRT するため、
     **--version 検証は rdrand 提示時のみ**実行（非対応ホストでもイメージは焼ける）
   - `env_tool_versions.go` の toolSpecs にも `agy` 行を追加（ピン表示・実体プローブ）
2. `workspace/entrypoint.sh` — **変更なし**（A-4 のとおり agy はプロジェクト root の AGENTS.md を
   読む前提。グローバル AGENTS.md シードの要否確定は Track A の auth/実機確認に委ねる）
3. **RDRAND ガード**: `workspace/agent/internal/hostcaps/` 新設。`/proc/cpuinfo` の flags 行から
   rdrand を語単位で検知（amd64 のみ要件化・キャッシュ付き）し、
   `hostcaps.AgyStatus() (supported bool, reason string)` を提供。
   reason 語彙は `"not_installed"`（agy バイナリ無し = 旧イメージ）/ `"no_rdrand"`。
   配備ドキュメントに要件明記済（`deploy/compose/README.md` Prerequisites・`deploy/local/README-wsl.md`）

   **← Track A / C への契約（capability の配線）**:
   - Track A: `agy.Status()`（`GET /connections` の `agy` フィールド）に
     `hostcaps.AgyStatus()` 由来の `"supported": bool` と `"reason": string`（unsupported 時のみ）を
     含めること。セッション作成（`agy.Handle*` / create 経路）も同判定で拒否すること
   - Track C: `ConnectionsStatus.agy.supported === false` のとき `agy` をセレクタに出さない
     （registry の `available: (c) => c.conns?.agy?.supported !== false` 相当。
     conns 未取得時は隠さない側に倒す）

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

## 統合と M1 E2E 結果（2026-07-20、`temp/szpaeta-agy-integration`）

### 統合

- Track A/B/C を origin 経由でマージ — **コンフリクトなし**（事前調整どおり `usage*.go` は
  A/C 同一内容、`hostcaps` は A/B 同一内容で自動解決。`routes.go` は双方の追加が共存）。
- ビルド・既存テスト全緑: agent（Go, 17 pkg）/ CP（Go）/ Console（tsc・vitest・i18n lint・vite build）。

### M1 E2E（実機・本 Workspace コンテナ内で agent を直接駆動）

CP はコンテナ内に docker が無く起動不可のため、**マージ済みビルドの agent を別ポートで起動し、
Console→CP が叩くのと同一の agent API を直接駆動**した（CP プロキシ・Console UI はそれぞれ
既存ユニットテストでカバー）。sandbox HOME＋`~/.gemini` symlink 共有で実アカウント
（AI Pro）を使用。

| M1 完了条件 | 結果 |
|---|---|
| セッション作成（`POST /sessions` kind=agy） | ✅ tmux pane で agy TUI 起動、事前 trust 有効（プロンプト非表示） |
| 会話 | ✅ marker ファイル読解を正答 |
| resume | ✅ 停止→`POST /sessions/{name}/start` → `--conversation <UUID>` で履歴・文脈とも完全復元（ファイル再読なしで正答） |
| logout（`DELETE /connections/agy`） | ✅ token 削除・connections `connected:false`・usage `authed:false`・`agy models` が sign-in 要求 |
| OAuth 認証フロー | ✅ **complete まで実機完走**（logout 状態から start→ユーザーがブラウザ承認→コード貼付→complete）。connected:true・token 0600・`agy models` 正常・Interactions 収集オフ（`enableTelemetry:false`）を確認。済みアカウントでは 409 gate |
| `/usage` 残量表示 | ✅ 4 バー（下記）。スクレイプはクォータ消費なし |
| RDRAND 非対応ホストでの非露出 | ✅ user+mount namespace で rdrand 無し cpuinfo を bind-mount した実 agent で `supported:false/no_rdrand`、セッション作成拒否、auth start 409 を確認 |

### E2E で発見した統合バグと修正（agy resume の UUID 取得）

**v1.1.4 の TUI は cwd→会話マップ（`cache/last_conversations.json`）を graceful exit 時に
しか書かない**。D-3 の「初回プロンプトで書く」観測は `-p` 実行がプロンプト毎にプロセス終了して
いたための見え方で、TUI 常駐では会話 DB（`conversations/<uuid>.db`）だけが先に生まれ、
マップは `/exit` まで更新されない。このため当初実装（alive 中の poll でマップを読む
`captureConversation` ＋ 停止は `tmux kill-session` 即死）では **UUID を一度も取得できず
resume が新規会話になる**。修正:

1. `WireLive` の capture を alive/dead 両側で実行（ユーザー自身の `/exit` 後の poll で採用）。
2. `agents.GracefulStopper`（optional interface）新設 — halt は kill 前に agy へ
   `C-u → /exit → Enter` を送り、猶予 4s 内の自死で flush の機会を与え、その場で UUID を採用。
   実機で下書き入り入力欄からの halt が 0.6s で graceful 完了（下書きは送信されない）。

### E2E で発見した統合バグと修正②（認可 URL の OSC-8 二重化）

auth start がスクレイプする認可 URL が **2 連結＋`]8;;` 残骸**で返っていた。agy の TUI は
URL を OSC-8 ハイパーリンクで描画するため、ANSI 除去後のバッファには「リンク先 URI＋可視
テキスト」として**同一 URL が区切りなしで 2 回**並び、`urlRe` の `\S+` が両方を飲み込む。
二重化した `state` はスペースを含まないため live テストの既存アサート（prefix・空白なし）を
素通りしていた。修正: `sanitizeAuthURL`（最初の 1 本に切り出し＋OSC 残骸除去）＋
live テストに scheme 出現回数 =1 のアサートを追加。修正後の実機フローでクリーンな URL を確認。

### `/usage` 4 バー対応（D-4 反映）

AI Pro でグループ毎に Weekly + Five Hour の 2 バーになったため（D-4）、パーサを limit
サブセクション分割に拡張。ワイヤ形状は **既存フラットフィールド＝週次**（Starter 互換）＋
`fiveHour: {remainingPct, resetsAt}`（存在時のみ）。AgyCard は週次バーの下に 5h サブバーを
インデント表示（計 4 バー）。実機 live スクレイプで 4 バーのパースを確認済み。
Pro ではヘッダのプラン表記が消える（メールのみ）ため `plan` は空になり得る — Card は劣化表示で受ける。

### 残課題（統合スコープ外メモ）

- セッション作成拒否（BuildLaunch 経由）のエラーが `tmux_failed` コードで届く（文言は正しい）。
  Console はセレクタ非露出で通常到達しないが、コードは後で整えたい。
- `ClearResume` は registry に定義済みだが呼び出し元が未配線（全 kind 共通の既存事項）。
- CP 込み・ブラウザ Console 込みの L2 E2E（`e2e/`）は docker のあるホストでの実行が別途必要。

## 統合後 UI 修正（2026-07-20、`temp/szpaeta-agy-ui-fixes`）

統合ブランチ合流後に見つかった Console 側の不具合・未決事項の後始末。いずれも
ビルド済みコンテナ内で稼働中の main ビルド agent(:7700) に Console を直結し、
headless Chromium で実機確認済み。

1. **起動系モーダルに認証済み agy が出ない**（真因: `StartHost`/`RepoRow` が
   「コーディングエージェント」の選別に `caps.chat` を流用しており、chat 無しの
   agy が常に落ちる）→ `caps.runsInDir` 判定に修正。ホーム起動・作業を始める・
   repo 行メニュー全てで選択可を実機確認（ホーム起動から実セッション作成まで完走）。
2. **表示順**を `claude, codex, agy, opencode` に統一（`SESSION_KINDS`・
   `repoLaunchKinds`・設定カード・WS バーのチップ順）。
3. **残量%表示を AgyCard から WS バーへ移設**: Claude/Codex と同列の使用量チップ
   （used% 表示・4 バーのドロップダウン・実験枠注記・`g a` キー）。採用条件の
   残量可視化はチップが引き継ぐ。
4. **AgyCard の構造を他カードと統一**: 設定セクション＋RTK トグル（`agy_rtk`、
   agent 側は実装済みだった）を追加。
5. **色・アイコン確定**: Google Blue（dark `#4285f4` / light `#1a73e8`）＋
   codicon `magnet`（反重力=磁気浮上のメタファ。ssm の藍・shell の青緑と区別可）。
6. **ミラービュー調査 → 実装で解決**（同日後続作業）: まず実機調査では
   `GET /sessions/{name}/messages` が `unsupported_kind`（agent が transcript()
   を持たない）で、会話 DB（`conversations/<uuid>.db`）の steps payload は
   **protobuf バイナリ**のため逆解析は見送りと判断した。その後の再調査で、agy が
   会話ごとに **`brain/<uuid>/.system_generated/logs/transcript_full.jsonl`**
   （素の JSONL・USER_INPUT / PLANNER_RESPONSE / ツール step）を**ターン進行中も
   ライブ追記**していることを protobuf 内の参照から発見 — これを transcript ソース
   に採用してチャットミラーを実装した（下記「チャットミラー実装」）。

## チャットミラー・モデル選択の実装（2026-07-20 後続、`temp/szpaeta-agy-ui-fixes`）

M1 で「不成立」とした chat/transcript を、brain transcript の発見により実装した。

- **transcript ソース**: `brain/<conversation-uuid>/.system_generated/logs/
  transcript_full.jsonl`（未 truncate 版を優先。`transcript.jsonl` はチェック
  ポイント後に古い step が落ちるモデル向けビュー）。1 行 1 step で、
  `USER_INPUT`（`<USER_REQUEST>` ラッパを剥がして user ターン）／
  `MODEL/PLANNER_RESPONSE`（assistant テキスト）／`MODEL/<ツール名>`
  （RUN_COMMAND・VIEW_FILE 等 → tool パート）／`SYSTEM/ERROR_MESSAGE`
  （tool パートとして表面化）を generic /messages の transcript.Turn に正規化
  （`internal/agents/agy/transcript.go`、`Caps{CanTranscript:true}`）。
- **会話 UUID のライブ特定**: brain/<uuid>/ は**初回プロンプト投入の瞬間に生成**
  される（実機確認）。BuildLaunch が brain ディレクトリ一覧をスナップショット
  （`agy-brain-prelaunch` store）し、ポーリング時に「スナップショットに無い dir が
  ちょうど 1 つ」なら生存中でも採用 — これでミラーが会話開始直後から点灯し、
  resume も graceful exit 不要になった。2 つ以上（同時起動レース）は取り違え
  回避で見送り、従来の graceful-exit cwd マップが後で確定させる。
- **入力**: 既存の generic /input（tmux paste）がそのまま効く。チャット
  コンポーザからの送信・最初のプロンプトの自動送信とも実機で往復確認。
- **モデル選択**: `GET /agents/agy/models`（`agy models` の行分割・1 分キャッシュ・
  stale-if-error）を追加。表示名がそのまま `--model` の引数になる（実機確認:
  TUI ステータスバーに選択モデルが出る）。Console は registry `caps.model`＋
  `agentModels.isDynamic` に agy を追加し、起動モーダルと設定カードの既定モデル
  （effort 行はモデル名に織込みのため非表示）を配線。
- Console registry は `chat/transcript/model: true` に反転。実機 E2E（Console→
  sandbox agent 直結）で「起動モーダルでモデル選択→agy 起動→最初のプロンプト
  自動送信→ミラーに user/assistant ターン（Working steps 付き）→コンポーザ追送
  →応答ミラー」まで完走。既知の残: コンテキストゲージ・plan/fork は据え置き
  （transcript にトークン情報が無い・agy に fork 相当なし）。

## ユーザーに依頼する事項（並行作業のブロッカー解消）

1. **GCP プロジェクトの用意**（D1/M2 用）: 課金有効化済みプロジェクト ID を Connections 設定時に使える形で。
   どの API 有効化が要るかは D1 冒頭で実測して提示する
2. Starter 実験枠の**クォータ消費許可**: D2/E2E で実タスク数回分（週次プールの数%〜十数%）を消費する — ✅ 許可済み・実測完了
3. **AI Pro 契約の承認**（D-4 用・任意）: $19.99/月（初年 50% オフのキャンペーン報道あり）。
   契約後に `/usage` 表示の変化・実効クォータ・クレジット露出を再実測し、個人利用デプロイの
   常用可否を確定させる。
   — **状態（2026-07-20）: ✅ 承認・アップグレード・実測完了**（上記 D-4 実測結果）
