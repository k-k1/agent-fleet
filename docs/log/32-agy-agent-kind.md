# 32. `kind=agy`（Antigravity CLI）実装計画 — 並行トラック構成

- 状態: **M1 実装済**（採用決定・計画 2026-07-20 → 同日 Track A/B/C 実施・統合＋M1 E2E 実機完走 — §統合と M1 E2E 結果）。設計と PoC 経緯は [decisions/0008](../decisions/0008-antigravity-cli-agent-kind.ja.md)。
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
   - **【更新 2026-07-25】真のピンを公式不変 object へ更新**: 1.1.7 では GitHub
     Releases が追随しないため、公式installer manifestの `version-build-id` 付き GCS
     objectを直接取得する。`AGY_VERSION` + `AGY_RELEASE_BUILD` +
     `AGY_SHA256_X64/ARM64` を固定し、焼き込み・lean boot-install とも同じ byte を検証する。
     この行の「ピン不可」制約は解消（self-update の latest 追従は manifest 経路のまま）
   - **バックグラウンド自己更新は実在**（install.sh に明記）。バイナリ実測で
     `AGY_CLI_DISABLE_AUTO_UPDATE` 環境変数を発見 → Dockerfile で **`=true`** に設定して封殺
     （claude の `DISABLE_AUTOUPDATER` と同じ理屈。明示的な `agy update` は可能なまま）
   - 🔴 **ここは長らく `=1` で、まったく効いていなかった**（2026-08-23 に実測で発覚・
     docs/70 §70.14.9）。受け付ける値は **`true` だけ**で、公式ドキュメントもそう書いている。
     同じコマンドを値だけ変えて走らせた実測:

     ```
     AGY_CLI_DISABLE_AUTO_UPDATE=1     → auto_updater.go:305] Spawned background update process
     AGY_CLI_DISABLE_AUTO_UPDATE=true  → auto_updater.go:218] Auto-update disabled via environment variable
     ```

     ⚠️ **教訓: バイナリから env 名を見つけただけでは「封殺した」ことにならない。**
     名前は文字列として転がっているが、値の判定はコードの中にあって strings には出ない。
     **効いたことをログで一度確かめるまでは、設定したという事実しか無い。**
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
   - ✅ **「amd64 のみ要件化」は 2026-08-22 に実測で裏が取れた**（[70](70-slot-instance-classes.md) §70.13）。
     Graviton 3 世代（Graviton4 / Graviton3 / Neoverse-N1）の Debian 12 コンテナで
     `agy --version` `agy --help` が RC=0。**決め手は m6g** で、`/proc/cpuinfo` に `rng`
     （ARMv8.5-RNG＝RNDR。x86 の rdrand に相当）が**無いのに動いた**——arm64 の BoringCrypto は
     乱数を命令ではなく **`getrandom(2)`** から取る。したがって arm64 でガードを課さないのは正しい。
     ⚠️ ただし確かめたのは起動 2 種で、**OAuth 認証と実セッションは arm64 では未通し**。

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

> ⚠️ **インシデント（2026-07-20）**: この方式の初期実行では tmux ソケットを分離しておらず、
> テスト agent の shutdown が共有デフォルトソケットへ `tmux kill-server` を実行、無関係の
> 並行セッション（開発者の claude CLI 複数）を計 4 回全滅させた。恒久対応（shutdown の
> owned-only kill-session 化・`AF_TMUX_SOCKET` によるソケット分離・tripwire テスト）と
> 第 2 インスタンス起動の安全手順は [dev/04 §4.11](../build/04-agent.ja.md) を正とする。
> 以後この形の in-container E2E は必ず `AF_TMUX_SOCKET`＋`AF_SESSIONS_DIR`＋別ポートの
> 3 点セットで隔離すること。

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

### 実機フォローアップ修正（2026-07-20）: agy 子プロセスのゾンビ化（reap 漏れ）

実機で workspace-agent（PID 7、PID 1 ではない）の子として `[agy] <defunct>` が
蓄積するのを観測（/usage スクレイプ・auth フローの回数分。メモリ消費はゼロだが PID リーク）。

- **真因**: PTY ログインフロー共有プラミング `agents.Flow.Close()`（`internal/agents/flow.go`）が
  `Process.Kill()` のみで `Cmd.Wait()` を呼んでいなかった。workspace-agent は PID 1 でないため
  init による回収がなく、kill された子は親が Wait するまで永久にゾンビのまま残る。
  agy 固有のバグではなく **Flow を使う全 kind 共通**（claude auth / codex device-auth も同根）だが、
  agy は /usage スクレイプ（キャッシュ 5 分）で高頻度に通るため唯一実機で顕在化した。
- **同種の個別漏れ（横断監査で発見・同時修正）**: codex `startDaemonLocked` / opencode `start` の
  **デーモン起動タイムアウト経路**が `Kill()` のみで Wait なし（waiter goroutine `waitDaemon` は
  起動成功時にしか始動しない）。起動失敗 1 回につきゾンビ 1 体。
  それ以外の spawn 箇所（`.Run()`/`.Output()`/`.CombinedOutput()` 系、terminal.go、fs_search.go、
  chat_providers.go、browser_cdp.go）は全経路で reap されており問題なし。
- **修正**: `Flow.Close()` に `Cmd.Wait()` を追加（SIGKILL 後の Wait は pty.Start が *os.File を
  直結する構成のためブロックしない）。codex/opencode の起動タイムアウト経路にも `cmd.Wait()` を追加。
- **テスト**: `internal/agents/flow_test.go` — Close 後に `Cmd.ProcessState` 非 nil ＋
  `/proc/<pid>/stat` が Z でないことを、kill 経路と自然終了（先にゾンビ化）経路の両方で検証。
  修正前のコードでは両テストとも fail することを確認済み。
- **実機検証**: 修正版で live スクレイプ（`AF_AGY_LIVE=1 -run TestScrapeUsageLive`）を計 3 回、
  各 kind の短命コマンド（claude `auth status` / codex `login status` / opencode・agy `--version`）を
  各 3 回誘発し、`ps aux` でゾンビ増加ゼロを確認。観測済みの既存 4 体は稼働中の旧バイナリの
  workspace-agent が親で、agent の再起動（新バイナリ配備）時に消える。

### 残課題（統合スコープ外メモ）

- セッション作成拒否（BuildLaunch 経由）のエラーが `tmux_failed` コードで届く（文言は正しい）。
  Console はセレクタ非露出で通常到達しないが、コードは後で整えたい。
- `ClearResume` は registry に定義済みだが呼び出し元が未配線（全 kind 共通の既存事項）。
- CP 込み・ブラウザ Console 込みの L2 E2E（`e2e/`）は docker のあるホストでの実行が別途必要。
- **既知の問題（2026-07-20 判明・対応後回し）**: agent の `shutdown.go` は
  SIGTERM 時の終了処理を「自インスタンス管理下のセッションのみ」ではなく
  **`tmux kill-server`（デフォルトソケット全体）** で行う。コンテナ唯一の
  agent という本番前提では正しいが、同一 tmux サーバを共有する第2インスタンス
  （開発・E2E・将来の多重起動）があると他インスタンスのセッションを全滅させる。
  実害を実機で確認（E2E 用テスト agent への SIGTERM が共有 tmux を kill し、
  並行セッションを 4 回巻き込み停止）。対応候補: 管理下セッション限定の
  kill-session 化、または agent の tmux 操作の専用ソケット（`tmux -L`）化。
  それまで **E2E でテスト agent を立てる際は tmux ソケット分離（または
  SIGKILL 停止で graceful shutdown を回避）が必須**。

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

## アシスタントチャット対応（2026-07-20、headlessChat）

`agy -p` を第4のチャットバックエンドとして配線した（`chat_providers.go` の
`agyChat`。AssistantModal のエージェント候補・設定「アシスタントのエージェント」
の選択肢・auto 選択順に agy/Antigravity が加わる）。実機（v1.1.4）検証で確定した
headless チャット契約:

- **プロンプトは `-p` の値（argv）**、stdout は応答テキストそのもの（構造化出力
  なし）。system-prompt フラグが無いので persona/knowledge は codex/opencode 同様
  `headlessPrompt` の前置きで渡す。
- **resume**: `-p` プロセスは終了時に cwd→会話 UUID マップ
  （`cache/last_conversations.json`）を書く（TUI と違い graceful exit 不要 —
  Track D-3 の観測どおり）。初回ターン後にマップから UUID を採用し、以後
  `--conversation <UUID>`。**実行は会話ごとの分離 HOME + 専用 cwd**
  （`~/.config/agent-fleet/chat-wd/agy-<convID>/{home,wd}`、下記 MCP 節）—
  共有だと並行する初回ターン同士が UUID を取り違えるため。会話削除時に
  dir ごと削除。
- **既定モデルは `Gemini 3.5 Flash (Medium)`**（`defaultAgyChatModel`）: チャットは
  レイテンシ重視で、agy を選ぶ価値は Gemini（Claude 系は claude バックエンドと
  重複かつ Thinking 固定）、無料枠のクォータ消費も最小。固定名の陳腐化対策として
  送信時にライブカタログ（`agy.Models()`）に無い名前は落として agy 既定へ退避
  （`agyChatModel`）。
- **MCP / ツール（2026-07-20 追補・実機検証済み）**: agy の MCP 設定は
  **グローバルのみ**（`~/.gemini/config/mcp_config.json`、claude 型 `mcpServers`
  スキーマ。stdio は command/args/env、リモートは serverUrl の SSE。claude
  `--mcp-config` / codex `-c` 相当の起動単位フラグは無い）。そこでチャットは
  **会話ごとの分離 HOME**（`chatAgyHome`）で走らせ、実 `~/.gemini` とは OAuth
  トークンの symlink だけを共有する（ローテーションは codex 同様
  `reconcileChatCreds` で書き戻し）。分離 HOME に書く内容:
  - `settings.json` / `config/config.json` — wd の trust・telemetry off・
    `permissions.allow`（有効ファイルがビルドで揺れた経緯があるため両方に書く）。
  - `config/mcp_config.json` — グラントに応じた af サーバ（af_write は
    `--write --conv <convID>` を args に同梱 — これが per-conversation HOME に
    した理由）＋接続済み ops 連携（pagerduty 等）。**MCP 子プロセスは agy の
    env を継承するので、各サーバの env で `HOME` を実 home に戻す**
    （workspace-agent が実セッション状態を見るために必須・実機確認）。
  - 許可ルールの書式は **`mcp(<server>/<tool|*>)`**（例 `mcp(af/*)`。binary
    strings から特定 — `mcp(af)` や素のツール名では自動拒否のまま）。read 系
    （read_file 等）は素名で許可 → knowledge dir も読める。コマンド実行・
    書き込み系は許可しない＝`-p` の自動拒否のままがチャット契約。
    `--dangerously-skip-permissions` は使わない。
  usage イベントは無いままなのでコンテキストゲージは空。one-shot（タイトル案）
  も共有の分離 HOME（`agy-oneshot`）で走らせ、実 HOME の会話履歴やユーザー自身の
  グローバル MCP 設定を汚染しない。
- **auto 選択順の既定は claude → codex → opencode → agy の最後尾**（Starter
  無料枠が週次極小のため。agy-only ワークスペースでのみ自動選択される）。
  順位は 設定 > エージェント「アシスタントのエージェント優先順位」の並べ替え UI
  でユーザーが変更可能（ui-prefs `assistantAgentOrder`。旧・単一ピン
  `assistantAgent` は先頭昇格で読み替え — `assistantAgentOrderPref`）。one-shot
  （タイトル案）も agy 経路あり（`AF_TITLE_MODEL_AGY` で上書き）— ephemeral
  モードが無いので使い捨て会話が agy 側に残る点は許容。
- 検証: unit（args/モデル退避/resolveChatModel/allow ルール/mcp_config 生成/
  分離 HOME 内容）＋ live（`AF_AGY_LIVE=1 go test . -run TestAgyChatSendLive`:
  分離 HOME 経由で af MCP ツール `list_my_sessions` の実呼び出し成功→UUID 捕捉→
  resume で文脈維持、を実機確認）。

## 会話中インタラクティブプロンプトの調査（2026-07-20、AskUserQuestion 相当の網羅確認）

claude の AskUserQuestion / 許可プロンプトに相当する、**会話進行中に agy TUI が
出しうる対話プロンプト**を実機で誘発して洗い出した（オンボーディング系は Track A
対応済みのため対象外）。v1.1.4・実 TUI（tmux）＋ Console 統合経路（sandbox agent
直結・headless Chromium）で確認。

### 種類の洗い出し（実機で誘発・観察）

| # | プロンプト | UI | transcript_full.jsonl への記録 |
|---|---|---|---|
| 1 | **コマンド実行許可**（"Requesting permission for: …"） | 4 択（Yes / この会話で常に許可 / settings.json に永続許可 / No）＋ esc cancel・tab Amend・ctrl+g 編集 | **保留中は無記録**。承認後に `RUN_COMMAND`（DONE）。**拒否は step 自体が残らない**（TUI にのみ "User declined the tool call"） |
| 2 | **ファイル作成許可**（"Allow creation of this file?"） | 2 択＋インライン diff・f full diff・tab Amend | 同上。承認後に `CODE_ACTION`（DONE） |
| 3 | **ファイル編集許可**（"Accept this file edit?"） | 2 択＋diff。**shift+tab で auto-approve edits トグル**の案内あり | 同上。承認後に `CODE_ACTION`（DONE） |
| 4 | **ASK_QUESTION（AskUserQuestion 相当）** | "Question N/M:" ＋番号付き選択肢＋ **Write-in…（自由記述）**＋ esc Skip | **保留中は無記録**。回答後に `ASK_QUESTION`（DONE）— ただし content は **回答のみ**（"A1: Apples"）で**質問文・選択肢は JSONL に残らない** |
| 5 | plan モード特有の承認 | **無し** — plan は brain の artifact（.md）として保存され「/artifact で確認して手動で shift+tab」方式。claude の ExitPlanMode 型の承認ダイアログは存在しない | plan 作成は `CODE_ACTION` |

- 権限系 1–3 は**フリート既定では発生しない**: BuildLaunch が
  `--dangerously-skip-permissions` で起動する（M1 設計。plan 起動時のみ外れるが、
  Console は agy の startMode を出していない）。
- **4 の ASK_QUESTION は skip-permissions 下でも発火する**（実機確認）— つまり
  フリートの agy セッションが実際に踏みうる保留プロンプトは実質これ。

### ミラー／セッション統合経路での見え方（実機 E2E）

- **保留中の可視性: 現状ゼロ**。transcript が保留中無記録のため、ミラーは
  ユーザーターンのみ＋「Awaiting reflection」のまま。状態チップも agy は live
  state を持たず、/messages の driveState（claude 形ヒューリスティック）は
  質問保留中を **working と誤報告**することすらある。ユーザーはターミナル
  ビューに切り替えない限り、セッションが選択待ちでブロックしていることに
  気づけない。
- **応答経路は既存プラミングで機能する**: ウィジェット表示中に
  `POST /input {seq:[{t:"1"},{k:"Enter"}]}`（claude の TUI 質問モーダルと同じ
  経路）で選択が確定し、回答後は `ASK_QUESTION` tool パート＋続きの応答が
  ミラーに正しく出ることを確認。**タイミング注意**: モデル思考中（ウィジェット
  表示前）に送った入力はコンポーザ側にキューされ、質問には届かない — 検知なしの
  盲目送信は誤爆する（実測）。
- 回答済みターンの表示は正常（ASK_QUESTION tool パートとして表面化）。

### ギャップと対応方針（→ 下記「保留プロンプト検知の実装」で解消）

1. ~~検知は pane スクレイプ一択~~ → **撤回**。実装前の再調査で上位互換の
   チャネルを発見した（下記）。
2. ASK_QUESTION の**質問文・選択肢が JSONL に残らない**ため、履歴表示の充実は
   検知時スナップショットに依存する（回答後は "A1: …" しか残らない）— 現状は
   保留中カードのみ構造化表示、履歴は従来どおり。
3. 拒否されたツール呼び出しが transcript に**痕跡を残さない**点は、ミラー履歴の
   忠実性の既知の穴として記録しておく（TUI にのみ表示）。
4. 権限系はフリート既定（skip-permissions）で抑止されているため実質
   ASK_QUESTION が本命。将来 agy の startMode=plan を Console に出す場合は
   権限プロンプトが復活する — state=permission の検知は下記実装済み、カードの
   選択肢駆動は未配線（残課題）。

## 保留プロンプト検知の実装（2026-07-20 後続、`temp/szpaeta-agy-ui-fixes`）

### 検知チャネルの再調査（pane スクレイプの前に代替を確認）

保留中の実機で各チャネルを確認した結果:

| チャネル | 結果 |
|---|---|
| OSC / pane・window タイトル | **無し**（タイトル不変、OSC シーケンス出力なし） |
| stderr | **無し**（空） |
| lock / 状態ファイル | 無し |
| CLI ログ（`log/cli-*.log`、glog） | `Surfacing ask_question at step N` 行が**ライブで出る**が、イベントのみで質問本文・選択肢なし（補助止まり） |
| **会話 DB（`conversations/<uuid>.db` の steps 最終行）** | **採用** — 保留中は `status=9`（実測: 2=実行中・3=完了・9=ユーザー入力待ち）。ask_question はツール引数 JSON `{"questions":[{question, options, is_multi_select}]}` が step_payload に**平文文字列として**埋まっており（protobuf の length-delimited 文字列）、スキーマ逆解析なしで `{"questions":` の位置から 1 値デコードするだけで取れる。権限保留も同じ status=9（該当ツール step、CommandLine 等の引数 JSON つき） |

→ **pane スクレイプは不要**（折返し・文言変更に脆い方式を回避できた）。

### 実装（agent 側のみ・Console は既存カードがそのまま点灯）

- `internal/agents/agy/pending.go` — `Probe(m)`: sids の会話 UUID → 会話 DB を
  read-only で開き最終 step を見る。status=9 かつ questions JSON あり →
  `("question", []transcript.Question)`（multiSelect・選択肢つき）、JSON 無し →
  `("permission", nil)`。TUI が付け足す "Write-in..." 行は JSON に含まれない
  ため、選択肢 index はウィジェットの行番号と 1:1（menu モードの Down×i+Enter
  がそのまま正しい行に届く）。
- 配線: `Transcript()` が Pending を載せ（generic /messages が alive 時のみ
  `pendingQuestions` として返す）、`WireLive` が State=question/permission を
  返し（セッション一覧バッジ）、`driveState` は agy 分岐で Probe を最優先 —
  これが **working 誤報告の修正**（真因: /input が楽観的に "working" を永続し、
  質問ウィジェットが idle フッタを隠すため claude 形ヒューリスティックの
  自己修復が発火せず貼り付いていた）。
- Console 変更なし: 既存の PendingQuestions カードが kind≠claude を menu
  モード（Down×i+Enter・esc キャンセル）で駆動する設計だったため、agy は
  そのまま乗った（型コメントのみ更新）。multiSelect 質問と複数質問ページングは
  カード側の既存ガードで端末誘導ヒントに落ちる（agy でのページング keys は
  未実測のため据え置き）。

### 実機 E2E（sandbox agent 直結・headless Chromium）

起動モーダル→最初のプロンプトで質問誘発→**ミラーに Question チップ＋質問
カード（選択肢ボタン・コンポーザロック）が表示**（state=question、
pendingQuestions に構造化質問）→カードの選択肢クリックで TUI が回答を受理→
応答ターンがミラーに出て idle へ復帰、まで完走。ユニットテストは probe の
question/permission/実行中/DB 無しの 4 経路＋既存全緑（agent 362）。

## ツール種別のミラー表面化・網羅確認（2026-07-20、`temp/szpaeta-agy-ui-fixes`）

ASK_QUESTION 以外のツール呼び出しがチャットミラーの Working steps に正しく
出るかを実機で網羅確認した。

- **型の全列挙**: バイナリの proto 列挙 `CORTEX_STEP_TYPE_*`（約 120 種）を
  strings で抽出。大半は IDE / google3 内部用（BLAZE_* / MOMA / CIDER /
  BROWSER_*（CLI では browser tools are disabled）/ MCP_TOOL（未設定）等）で、
  CLI の通常会話で到達するのは限られる。
- **実機誘発で確認した型**（1 会話で一括誘発 → /messages の parts を検証）:
  LIST_DIRECTORY・VIEW_FILE・GREP_SEARCH・RUN_COMMAND・CODE_ACTION（作成/編集
  とも）・SEARCH_WEB・READ_URL_CONTENT（＋既知の ASK_QUESTION・GENERIC・
  ERROR_MESSAGE）。ファイル名検索（find 相当）とリネームは agy 側が
  RUN_COMMAND に落とすため FIND / MOVE 型は CLI では出ない。いずれも
  「tool パート（tool=型名・output=本文）」として表面化し、Working steps の
  集計行（例: 8 tools LIST_DIRECTORY · VIEW_FILE · … · RUN_COMMAND×3 ·
  CODE_ACTION×2）と個別出力の展開表示を実機スクリーンショットで確認。
  パーサは「MODEL の非 PLANNER_RESPONSE 型はすべて tool パート」の汎用
  マッピングなので、**未知の型が来ても自動で表面化する**（型名がそのまま
  ラベルになる）。
- **バックグラウンドコマンドの生涯**: `sleep 15 &` 相当は RUN_COMMAND step が
  status=RUNNING で**ライブ追記**され（完了しても同 step の行は書き換わらず
  重複行も出ない）、完了通知は SYSTEM_MESSAGE（非表示で正 — モデル向け）→
  モデルの PLANNER_RESPONSE が結果を再掲する、という流れ。ミラーは
  「実行開始の tool パート＋完了報告のテキスト」になり破綻しない。
- **status=9 プローブとの衝突なし**: 長時間コマンド実行中の DB 最終 step は
  status=2（実行中）で、Probe は question/permission を返さないことを実機確認
  （保留誤検知なし）。
- **磨き込み**: RUN_COMMAND 出力に agy が付ける 4 タブのテンプレインデント
  （`\t\t\t\tOutput:` …）をミラー表示で除去（stripCommandIndent — 出力自身の
  インデントは 4 タブ超過分として保持）。
- 副次確認: 停止セッションの履歴ビュー（transcript cap）・タイトル自動提案
  （「ツール実行テスト」が提案された）も agy で動作。
- **既知の残**: 実行中の working/idle は hook が無いため不正確（ツール実行中に
  入力待ち表示になり得る。誤検知側ではないので実害は小）。

## 権限保留の安全性再確認とカード駆動の実装（2026-07-20、`temp/szpaeta-agy-ui-fixes`）

「権限保留は state 表面化のみ・フリート既定では発生しないため優先度低」の
判断を実機で再検証した結果、**前提が崩れるケースと危険な挙動を確認**したため
カード駆動まで実装した。

### 確認結果

1. **skip-permissions 起動の確実性**: コード上は `AGENT_AGY_FLAGS` 既定
   `--dangerously-skip-permissions`（フリート実機の env 未設定を確認）で、
   Console 経由の実セッションの pane 起動コマンドにフラグが付くことを実機確認。
   **ただし抜け道あり**: sessions create の `mode` は kind 非依存で受理される
   ため、API 直叩き（af_write MCP の create_session 等）の `mode=plan` で
   skip なし agy が起動できる（buildProgram が plan 時に skip を外す設計。
   Console UI に plan が出ないだけで到達可能）。「発生しない」は成立しない。
2. **発生時の旧挙動**（skip なし agent を立てて実機確認）:
   - ミラーは state=permission のチップのみでカード無し。**コンポーザが
     ロックされず**、送信すると本文＋Enter が許可メニューに落ち、Enter が
     ハイライト行（1. Yes）を確定 = **無言の承認事故**になり得る。
   - ターミナルビューへの切替で応答は可能（完全な詰みではない）。
   - **halt が承認を踏む footgun を実証**: GracefulStop の `/exit`＋Enter は
     メニュー表示中だと Enter が「1. Yes」を確定 — 保留中 halt でファイル作成が
     実際に承認された（ptest.txt が生成された）。
3. **脱出可否**: halt/archive 自体は常に通る（graceful 失敗時は kill-session
   fallback）。詰みはしないが、上記のとおり halt が副作用を持っていた。

### 実装（agent 側のみ）

- **権限カードの駆動**: Probe が保留ツール名（payload 内の平文 `run_command` /
  `write_to_file` / `replace_file_content`）から TUI メニューを**行数・順序
  まで一致**させた Question を合成（コマンド=4 行、ファイル作成/編集=2 行、
  引数 JSON から CommandLine / TargetFile を質問文に付記）。既存の menu モード
  カード（Down×i+Enter）がそのまま駆動し、カード表示中はコンポーザも
  ロックされる（誤爆封じ）。**未検証ツールはカードを出さない**（メニュー形が
  不明なまま鍵駆動すると誤選択するため state のみ・ターミナル誘導）。
- **halt の Escape 先行**: GracefulStop は保留プロンプト検知時に Escape で
  メニューを棄却（question=Skip / permission=cancel — どちらも選択なし）して
  から `/exit`。実機で「保留中 halt → 承認されず 1.1 秒で graceful 終了」を確認。
- Probe 冒頭で captureConversation を実行（セッション一覧ポーリング前に
  ミラーだけが走った場合でも会話 UUID を確定できる）。

### 実機 E2E

skip なし agent（`AGENT_AGY_FLAGS=" "` の dev seam）＋ Console で、ファイル
作成の許可待ち → ミラーに「Allow creation of this file? <path>」カード
（Yes/No）表示・state=permission → **No, deny creation クリックで TUI が
"User declined the tool call"**・ファイル未作成・idle 復帰。別の保留を
作って halt → 承認なしで graceful 終了（htest.txt 未作成）。ユニットは
コマンド 4 行 / 作成・編集 2 行 / 未知ツール無カードを追加し全緑（agent 365）。

## 初回プロンプトがブート画面に食われる問題の修正（2026-07-20 後続）

**症状**: 起動導線（作業を始める）で agy を選び最初のプロンプトを入れて起動
すると、プロンプトが TUI に届かず入力内容が消える。

**真因**: `paneMode`（agent `session_io.go`）に agy 分岐が無く、常に "" を
返していた。`paneMode` は launch-seed 配送の readiness ゲートを兼ねる —
ミラーは `mode` 非空を待って seed を送り（`MirrorView` の readiness gate）、
サーバ側 `deliverInitialPrompt`（MCP create の `initial_prompt`）も同じ信号を
待つ。agy はどちらも検知不能で、ミラーは 15 秒の盲目 fallback（`seedForce`）、
サーバは固定 2.5 秒 beat に落ちていた。agy の「Signing in...」ブート画面は
**タイプされたテキストを完全に食う**（実機確認: ブート中に send-keys した
文字列は composer 出現後に残らない）ため、composer 描画前に落ちた送信は
無音で消える。15 秒 fallback 前にミラーを離れる（ターミナルビューへ切替 =
MirrorView unmount でタイマー破棄）と配送自体が起きない。

**直し方**: `paneMode` に agy 分岐を追加。composer フッタ（v1.1.4 実測:
左 "? for shortcuts"（idle）/"esc to cancel"（生成中）、右 "<model>"、plan
モードは "plan · <model>"）を paneTail(3) から検出し "Default"/"Plan" を
返す。これで readiness ゲート（ミラー/サーバ両方）と `/messages` の mode が
agy でも機能する。ミラーのモードチップは `planCycleKey` 必須のため agy には
出ず、UI 副作用なし。

**検証**: ①ドリフトテスト `agy_pane_drift_test.go`（build tag `drift`・実
サインイン必須・§4.11 のソケット隔離）で Default/Plan 両モードの実 TUI
フッタ検出を確認。②第 2 インスタンス（:7710・3 点隔離）で `POST /sessions
{kind:agy, initial_prompt}` → readiness 待ち後に自動投入・agy 応答・
`/messages` に user/assistant ターンと mode:"Default" を確認。agent 全緑
（384）。

### 他の投入経路の監査と追加修正（同日後続）

paneMode 修正後に、agy へテキストが届く全経路を洗い直した。

| 経路 | 結果 |
|---|---|
| Console 起動導線（mirror launch-seed） | ✅ paneMode 修正で解消 |
| MCP `create_session` + `initial_prompt`（`deliverInitialPrompt`） | ✅ 同上・実機確認済み |
| MCP `send_to_session` → `/input {prompt}`（作成直後） | ❌→修正: readiness 待ちが無く、ブート画面に食われていた |
| 質問/許可保留中の `/input {prompt}` | ❌→修正: `questionPending` がフック status（claude 専用）しか見ず素通り — 本文＋Enter がハイライト行を確定（許可メニューでは無言承認事故） |
| MCP ツール定義（workspace `mcp_stdio.go` / CP `mcp.go`） | ❌→修正: `list_models` が agy を拒否・`create_session` の kind 説明に agy 非記載 |
| 複数行指示（literal send-keys） | ✅ 実機確認: composer に複数行のまま入り早期送信なし（claude と同じ paste 合体挙動） |
| `/turn` tui start/steer | ✅ `submitPromptTUI` 共有のため上記修正が効く |

追加修正（agent `session_io.go`・`mcp_stdio.go`、CP `mcp.go`）:

- `questionPending` に agy 分岐: フック status の代わりに `agy.Probe`（会話 DB）で
  判定し、question / permission **両方**で自由文を `question_pending` 409 に。
  回答は従来どおり `{seq}`/`{keys}`（カード駆動）で通る。
- `submitPromptTUI` に agy の readiness 待ち: paneMode 非空まで最大 ~15 秒
  ポーリングしてから投入（フッタは idle/working とも常在なので、空 = ブート中
  のみ。保留プロンプトは直前の reject 済みで、ウィジェットで停滞しない）。
- MCP 両面で agy を第一級 kind に: `list_models` の許可と説明、`create_session`
  の kind 説明（接続済みのときのみ起動可の旨）。

実機 E2E（第 2 インスタンス・3 点隔離）: 作成直後（Signing in... 中）の
`/input {prompt}` が readiness 待ち（~2.8 秒）後に配送され応答まで到達／
ask_question 保留中の自由文が 409・`{seq:[{t:"1"},{k:Enter}]}` での回答は成功。
agent 384・CP 147 全緑。

### アシスタント経由 MCP ツール全体の agy 再確認（同日さらに後続）

kind 追加の影響が投入経路以外にも及ぶため、workspace `mcp_stdio.go` と CP
`mcp.go` の全ツールを agy 観点で洗った。

| ツール | 結果 |
|---|---|
| `list_my_sessions` / `get_session_status` / `get_session_output` | ✅ kind 非依存で agy も正しく列挙・状態・端末出力を返す（実機確認。ただし agy はライブ working/idle を持たず、状態は保留プロンプト検知のみ — 仕様どおり） |
| `stop_session` / `resume_session` / archive / delete 系 | ✅ halt/start・cleanup とも kind 非依存 |
| `get_agent_usage` | ❌→修正: claude/codex のみで、実在する agy の使用量（`/connections/agy/usage`・Starter 枠ゲージ）を欠いていた。agy キーを追加（{account, plan, groups} 形。未ログイン時は authed=false を自己申告し 500 を返さないので merge 安全）。説明も更新 |
| `get_session_usage` | △ 説明修正: agy は transcript-capable なので一覧に**含まれるが**、agy の transcript にトークン情報が無いため context 空・cumulative 全 0 になる。「消費ゼロ」と誤読されないよう注記を追加（残枠は get_agent_usage を見る旨） |
| `list_models` / `create_session` | ✅ 前段で修正済み |
| メモ系（add/update/delete/flush/list_memos） | ✅ セッション kind 非依存 |

追加修正（agent `mcp_stdio.go`＋test、CP `mcp.go`）:

- `get_agent_usage` に agy を merge（両面）。ユニット（`mcp_stdio_test.go`）に
  agy の {account, groups} 形が混ざることを追加検証。
- `get_session_usage` と `list_my_sessions`（CP）の説明に agy を反映
  （agy はトークン 0・列挙対象である旨）。

実機（第 2 インスタンス）: `/connections/agy/usage` が account/groups を返し、
agy セッション作成 → sessions 一覧に kind=agy で出現 → `sessions/usage` が
cumulative 全 0（トークン情報なし）で返ることを確認。agent 384・CP 147 全緑。

### ミラーの ContextBar（context 充填率）MVP — `/context` スクレイプ（2026-07-20）

agy の転写（`transcript_full.jsonl`）にも他の永続状態にも token 数が一切無い
（実機 grep 0 件。会話 DB の `gen_metadata` は非公開スキーマ protobuf で token
らしき値も確認できず）ため、ミラーの ContextBar はターン usage 経由では永遠に
出ない。代わりに TUI の **`/context` パネル（"Visualize current context usage"
— `26.0k/1.0M tokens` の合計とカテゴリ別内訳を描く）** を唯一の取得元として、
セッションレベルの context 充填率を縮退表示する MVP を実装した。

- **取得**（agent `internal/agents/agy/context.go`）: `/usage` と同じ
  `agents.Flow` 配管で `agy --conversation <uuid>` を scratch dir
  （`/tmp/af-agy-ctx`・事前 trust）に復帰 → `/context` 投入 → 合計行
  `· <used>/<window> tokens` をパース（履歴リプレイに同語が混ざり得るため
  最終 "Context Usage" 以降のみ）。数値は agy 自身のクライアント側推定
  （パネル自ら "Estimated usage" 表示）であり API 報告値ではない。
- **キャッシュ**: 会話ごとに転写の size+mtime を指紋にし、変化時のみ 60 秒を
  下限にバックグラウンド更新（`ContextFill` は常に非ブロッキング、初回 nil）。
  スクレイプはプロセス全体で同時 1 本（メモリ制約ホスト）。
- **ワイヤ**: `agents.ContextReporter`（optional interface）を generic
  `/messages` ハンドラだけが呼び、`context: {tokens, window, at}` を応答に追加。
  `/sessions/usage`（MCP get_session_usage）からは呼ばない — 一覧クエリで
  スクレイプが走らないため。Console（MirrorView）はターン usage が無い時の
  フォールバックとして ContextBar に内訳なし（全量 fresh 扱い）で描く。
- **ライブ会話への並行復帰の安全性（go/no-go 実測）**: 稼働中セッション A の
  会話へ第二プロセス B が `--conversation` 復帰 → `/context` 実数取得 → B kill
  後も A は応答継続・`transcript_full.jsonl` 無傷（USER_INPUT/PLANNER_RESPONSE
  各 2 で混入なし）。
- **実機 E2E**（第 2 インスタンス :7711・3 点隔離）: `POST /sessions
  {kind:agy, initial_prompt}` → 応答後 ~20 秒で `/messages` に
  `context:{tokens:17100, window:1000000}` が出現、以後キャッシュ応答。
- **検証**: agent 全緑（419）・live スクレイプテスト（`AF_AGY_LIVE=1 -run
  TestScrapeContextLive`）成功・Console typecheck / mirror テスト 33 全緑。
- **制約（MVP スコープ外）**: cache read/create/fresh の内訳とターン毎 token
  消費（スパークライン）は取得元が無く出せない。更新は転写変化 + 60 秒下限の
  スクレイプ粒度で、claude/codex のようなターン即時反映ではない。

### enableTelemetry の起動毎再固定（2026-07-20）

`enableTelemetry:false` の固定は auth 完了時の一回だけだったため、後から
キーが on に倒れる/消えると次のログインまで戻らなかった（実機: 手動の
/config 実験で on に戻っていたのを確認）。利用者合意の上で、**agy を起動する
全経路**（`BuildLaunch`・`/usage` スクレイプ・`/context` スクレイプ）の直前に
`enforceTelemetryOff()` を呼び足し、常時オフへ再固定するようにした（no-op
when already false なので追加コストなし）。なお TUI `/config` の
"Enable Telemetry"（利用状況テレメトリ）はオンボーディングの Interactions
トグル（学習利用オプトアウト・アカウント側）とは別レイヤー。あわせて
`showTips:false`・`showFeedbackSurvey:false` も設定した — どちらも TUI に
予告なく描画される要素で、ミラーの launch-seed 自動投入や PTY スクレイプ
（画面テキストのパターンマッチ依存）とは共存させない方が安全なため。

## ユーザーに依頼する事項（並行作業のブロッカー解消）

1. **GCP プロジェクトの用意**（D1/M2 用）: 課金有効化済みプロジェクト ID を Connections 設定時に使える形で。
   どの API 有効化が要るかは D1 冒頭で実測して提示する
2. Starter 実験枠の**クォータ消費許可**: D2/E2E で実タスク数回分（週次プールの数%〜十数%）を消費する — ✅ 許可済み・実測完了
3. **AI Pro 契約の承認**（D-4 用・任意）: $19.99/月（初年 50% オフのキャンペーン報道あり）。
   契約後に `/usage` 表示の変化・実効クォータ・クレジット露出を再実測し、個人利用デプロイの
   常用可否を確定させる。
   — **状態（2026-07-20）: ✅ 承認・アップグレード・実測完了**（上記 D-4 実測結果）
