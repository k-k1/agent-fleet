# 39. エージェントメモリ管理 — git 差分管理・時点ロールバック・環境間 import/export

- 状態: **完了（2026-07-27）。P1〜P4 実装済み**。P4 は 5 項目のうち 2 つを実装し、
  1 つを阻害要因つきで将来トラックへ、2 つを不要と判断して落とした（下記「P4 の実際」）。
  設計は確定し、未決事項 4 点も既定値で決着済み（下記「決着した未決事項」）。
  意思決定は [decisions/0022](decisions/0022-agent-memory-management.md)。
- 要望: 各エージェントの「メモリ」を管理したい。①git での差分管理 ②日時・履歴指定でのロールバック
  ③特定プロジェクトだけのロールバック ④他の agent-fleet 環境への import/export。

## 背景 / 「メモリ」の実態（調査結論 2026-07-23・再調査で改訂）

「各エージェントのメモリ」が実際に何であるかを、現物（本環境の実ファイル・実バイナリ）と
公式ドキュメント/上流リポジトリの両面で棚卸しした。

| kind | メモリ機構 | 置き場・形式 | 版管理対象になるか |
|------|-----------|--------------|--------------------|
| claude | auto-memory（既定 ON） | `CLAUDE_CONFIG_DIR=/var/lib/af/claude` の `projects/<slug>/memory/*.md`（`MEMORY.md` 索引 + トピック別 md。slug は **git リポジトリ由来**なので worktree 間で共有）。専用マウント（Docker: `<dataDir>/claude-config`、ECS: EFS AP、native: 同型）。recreate / clean-home を生き残る | **○（v1 ルート #1）** |
| codex | **memories 機能あり**（feature flag `memories`＝stable・**既定 OFF**。`config.toml [features] memories = true` で有効化） | `~/.codex/memories/` の **markdown ワークスペース**（`memory_summary.md`＝セッション開始時に developer instructions へ注入される索引 / `MEMORY.md`＝grep 対象の登記簿 / `rollout_summaries/*.md` / `skills/<name>/SKILL.md` / `extensions/ad_hoc/notes/*.md`）＋パイプライン状態 `memories_1.sqlite`（`stage1_outputs`・`jobs`）。2 段構成: Phase1=rollout 毎の抽出（idle 6h 経過・並列 8）→ Phase2=**内部サブエージェントによるグローバル統合**。統合機構自体が `~/.codex/memories/.git` を差分ベースラインに使う（上流 PR #18982）。スコープは CODEX_HOME 単位のグローバル 1 ワークスペース（プロジェクト区分はファイル**内**のエントリ） | **○（v1 ルート #2・dir 存在検知で自動有効）** |
| opencode | ネイティブ無し（確認済: 上流 schema にメモリ表なし・docs にも無し・feature request #16077 が open のまま。サードパーティ plugin で補う文化） | `~/.config/opencode/AGENTS.md` は毎起動 `cp -f` refresh のフリート管理ファイル | ×（上流が実装したら root 追加） |
| agy | CLI には一級メモリ未確認（Antigravity **IDE** には Knowledge Items があるが `~/.gemini/antigravity/` 配下でレイアウト非公開。CLI 側 `~/.gemini/antigravity-cli/brain/` は会話 artifacts でありメモリではない） | — | ×（watch） |
| copilot | **Copilot Memory はあるが GitHub サーバー側サービス**（repo スコープ事実＋ユーザー選好・28 日失効・管理は GitHub 設定画面）。ローカル `~/.copilot` にはセッションストアのみ | ローカル実体なし | ×（対象外＝versioning する実体がローカルに無い） |
| cursor | **旧 Memories は製品から削除済み**（〜v2.1、公式の代替は Rules＝`.cursor/rules/*.md`＝リポジトリ内で git 管理可能）。現存する memories は cloud **Automations 専用のサーバー側エントリ**。CLI バンドル内の proto（`aiserver.v1.ListAutomationMemories*` / `PotentiallyGenerateMemory` / `KnowledgeBase*`）で全てクラウド RPC であることを実バイナリ確認 | ローカル実体なし（`~/.cursor` は transcript/worker のみ） | ×（対象外＝サーバー側） |
| kiro | **自動メモリなし**（changelog 確認済）。永続は ① steering md（workspace `.kiro/steering/` はリポジトリ内＝既に git 管理圏、**global `~/.kiro/steering/*.md`** はユーザー所有・依頼すればエージェントも書く）② `/knowledge`（experimental・既定 OFF・`~/.local/share/kiro-cli/knowledge_bases/` の BM25/意味検索**索引＝派生状態**。実バイナリで knowledge_store/KnowledgeBase リソース確認） | global steering のみ将来ルート候補 | △（watch。global steering は md ディレクトリなので root 1 行で追加可能。索引は ★9 原則で対象外） |
| （共通） | `AGENTS.md`（codex/opencode。**agy は対象外** — [60](60-user-instructions.md) §60.2 で訂正） | entrypoint が毎起動イメージ内容へ上書きするフリート管理ファイル＝ユーザーメモリではない（`~/.gemini/AGENTS.md` は rtk ブロックのみ 450 B ＝ フリート方針は配られていない） | × |

**結論（全 8 種別調査済・改訂）: 版管理対象になるローカル実体は claude の auto-memory と
codex の memories ワークスペースの 2 つ。** codex は本フリートで機能を有効化していないだけで、
有効化すれば `~/.codex/memories/` に md ファイル群として育つ。よって v1 から**ルートを
2 つ宣言**し、codex 側はディレクトリ存在検知で自動的に対象へ入る（フリートとして memories を
有効化するかどうかは別判断＝未決事項 4）。kiro の global steering（`~/.kiro/steering/*.md`）は
第 3 のルート候補（root 1 行で追加可能・watch）。opencode/agy は上流動向の watch、
copilot/cursor はサーバー側のため対象外。

参考（非採用 CLI の動向）: Gemini CLI は v0.40 で完全ローカル md の 4 層 tiered memory＋
実験的 Auto Memory（transcript 採掘→SKILL.md 化→`/memory inbox` レビュー）へ刷新した直後、
**2026-05 の I/O で廃止が発表され 2026-06-18 に無料/Pro/Ultra 向け提供を終了**（後継は
Antigravity CLI に一本化。Enterprise ライセンス・有償 API キーのみ延命）。よって
「Gemini CLI を種別採用」の線は消えたが、そのメモリ設計は md 正・階層化・レビュー付き
自動抽出の参考事例として有効で、**後継 = agy 系譜へ機能が流れてくる可能性が watch の本命**
になった。Goose も `~/.config/goose/memory` にローカル md メモリを持つ。**「md がメモリの正」
は業界の主流収斂**であり、これらはルート追加だけで本機構に載る。逆に Cursor/Copilot は
サーバー側管理へ寄せており、この系統はローカル版管理の対象にならない。

なお上流では codex が `external_agent_memory_import`（開発中フラグ）で **Claude Code の
`projects/<slug>/memory/*.md` をそのまま読み取り自分の MEMORY.md へ統合する**機能を作っており、
「索引 md ＋トピック md」というレイアウトはエージェント間で収斂しつつある。汎用の
「メモリルート宣言」設計はこの潮流にそのまま乗れる。

サイズ実測（本開発環境）: メモリ本体はプロジェクトあたり 32KB〜668KB。一方 `/var/lib/af/claude`
全体は 883MB（大半がセッション transcript の jsonl）。**メモリだけを対象にすれば git 履歴の
コストは無視できる**が、対象選定を誤ると transcript と `.credentials.json` を巻き込む。

もう一つの好条件: claude のメモリは**プロジェクト単位で自己完結**している（`MEMORY.md` 索引も
各 `memory/` ディレクトリ内）。つまり「特定プロジェクトだけロールバック」は、ディレクトリ
1 個のパススコープ操作に落ち、索引の整合性も壊れない。

### 既存機構に無いもの（gap）

- `deploy/compose/backup.sh` は **DATA_DIR 丸ごと tar**（ops 層・全ユーザー一括・時点は取得タイミングのみ）。
- `workspace/agent/cleanup_archive.go`（掃除の gz 安全網 — 専用の設計文書は無い）は削除セッションの tar.gz 安全網であり、継続的な差分履歴ではない。
- 秘匿接続情報にも export 経路は無く、**個人単位の「履歴・巻き戻し・持ち出し」primitive はゼロ**。
- 環境間移設は roadmap 上も「home+DB を丸ごとバックフィル」型のみで、メモリ粒度の移送は存在しない。

## 決定した既定（2026-07-23 提案）

| 論点 | 既定 | 備考 |
|------|------|------|
| 対象データ | v1 = claude `projects/*/memory/**` ＋ codex `~/.codex/memories/**`（存在時のみ・`.git` と sqlite は除外）。**allowlist 方式** | transcript / `.credentials.json` / `settings.json` / `memories_1.sqlite` は構造的に対象外（★1・★9） |
| 履歴エンジン | **git**（bare repo + staging コピー） | 差分・時点解決・bundle 移送・パススコープ復元が全部標準機能で載る |
| repo の置き場 | `/var/lib/af/claude/af-memory.git`（claude 専用マウント内・**codex 分の履歴も同居**） | このマウントが最も強い生存保証を持つため全 kind 共通の置き場にする。ファイルブラウザ非公開領域・recreate / clean-home 生存・ECS でも agent から見える。live ツリーには `.git` を置かない |
| staging の置き場 | `/var/lib/af/claude/af-memory.staging`（**repo と同じマウント**。P1 実装で `$TMPDIR` から変更） | EFS 越しのクロスデバイスコピーを避け、bare repo の index と work-tree の内容を一致させるため。live ツリーに `.git` を置かないという本質的な制約は変わらない |
| 実行主体 | **workspace-agent**（uid=dev、git あり、全 runtime で一様） | CP は REST proxy（dual allowlist）のみ。ECS で CP がファイル直アクセスできない制約を回避 |
| snapshot 契機 | 自動（claude 全セッション idle 遷移 + debounce）＝**既定 ON**＋手動 | 無変更なら commit しない。debounce 既定 5 分。当初併記していた scheduler 連携は、P1 で契機が常駐ポーリングになった時点で二重機構になるため落とした（「P4 の実際」④） |
| ロールバック | **履歴は書き換えない**。restore は「新しい snapshot commit」として積む。適用前に pre-restore snapshot 自動取得 | ロールバックのロールバックが常に可能 |
| スコープ | claude: 全体 / プロジェクト単位（`projects/<slug>/`）。codex: **ワークスペース全体のみ**（プロジェクト区分がファイル内エントリのためディレクトリ粒度が存在しない） | 日時指定は「その時刻以前の直近 snapshot」に解決 |
| export | **git bundle（全履歴）を既定**、tar.gz（最新ツリーのみ）も選択可。**v1 は平文 DL**（暗号化なし） | bundle は 1 ファイル・履歴込み・`git bundle verify` で検証可能 |
| import | bundle → `refs/imports/<ts>` に fetch / tar → staging 展開。適用は**プロジェクト選択式の置き換え（=新 commit）**、または**移設**（`mode=migrate`・履歴ごと入れ替え・2026-08-25 追加） | 3-way merge はしない（v1）。既定の置き換えではローカル履歴は保全される。移設は main を取り込んだ系譜へ付け替える（元の main は `refs/premigrate/<ts>` へ退避＝消さない） |
| UI | 設定モーダル「ワークスペース」グループに「エージェントメモリ」タブ | 破壊操作は P4a の統一確認ダイアログ |
| 監査 | `proxy.go auditActionTarget` に restore / import / import.apply / export をマップ | export は GET だが「持ち出しの唯一の経路」なので読み取りの例外として監査する。target は URL 由来のヒントのみ（本文は読まない） |

## アーキテクチャ

```
live:    /var/lib/af/claude/projects/<slug>/memory/**     ← claude が読み書き（無改変）
         ~/.codex/memories/**（.git と sqlite を除く）    ← codex 統合パイプラインが読み書き（無改変・存在時のみ）
               │ ① allowlist copy（idle 契機・数百KB）
               ▼
staging: /var/lib/af/claude/af-memory.staging/{claude/projects,codex}/...
               │ ② git commit（--git-dir=af-memory.git・専用 identity）
               ▼
repo:    /var/lib/af/claude/af-memory.git（bare・agent 管理・マウントと同寿命）
               │ ③ REST（dual allowlist: workspace/agent/routes.go + control-plane/routes.go）
               ▼
Console: 履歴一覧 / 差分 / ロールバック / export DL / import UL
               │ ④ push mirror（**未実装** — 内部 git に所有者限定 repo の概念が要る）
               ▼
CP 内部 git（bare+http-backend 流用）→ 停止中閲覧・環境間直接同期
```

### ① メモリルートの宣言（拡張点）

agent 側に宣言テーブルを 1 つ持つ:

```go
type memoryRoot struct {
    Kind      string   // "claude" | "codex"
    Label     string   // 表示名
    LiveDir   string   // claude.ConfigDir()+"/projects" / $HOME/.codex/memories
    Include   []string // allowlist glob。これ以外は決して読まない
    Exclude   []string // Include 内の除外（codex: ".git/**", "phase2_workspace_diff.md"）
    Scopes    bool     // ディレクトリ粒度の部分ロールバック可否（claude=true, codex=false）
}
```

v1 のエントリは 2 件:

| Kind | LiveDir | Include | 備考 |
|------|---------|---------|------|
| claude | `<CLAUDE_CONFIG_DIR>/projects` | `*/memory/**` | プロジェクト粒度あり |
| codex | `~/.codex/memories` | `**`（Exclude: `.git/**`・`phase2_workspace_diff.md`） | **dir が存在する時だけ有効**（memories 機能 OFF の環境ではルート自体が現れない）。`memories_1.sqlite` は LiveDir 外なので構造的に対象外 |

codex の統合パイプラインは自前の `~/.codex/memories/.git` を差分ベースラインに使うため、
これは**絶対に触らない・repo にも入れない**（Exclude）。当方の snapshot は staging コピー方式
なので live 側に一切の痕跡を残さず、両者は干渉しない。opencode が上流でメモリを実装したら、
ここへ 1 行足すだけで snapshot / rollback / export の全機能が付いてくる。
repo 内レイアウトは `claude/projects/<slug>/...`・`codex/...` と kind ごとに名前空間を分ける。

### ② snapshot エンジン

- **契機（P1 実装で確定）**: 「claude の全セッションが idle になってから debounce（既定
  5 分）」という意味論はそのままに、実装は**フック相乗りではなく常駐側のポーリング**にした。
  claude の working→idle 遷移は `workspace-agent session-status` という**フックの別プロセス**
  で観測されるため（`session_status.go`）、常駐プロセス側に debounce タイマーを置けない。
  マーカーファイルで渡す設計も可能だが、フックの取りこぼしがそのまま「snapshot が積まれない」
  に直結する。そこで毎 tick（既定 1 分）に
  ①ルート配下の最新 mtime ②前回 snapshot 以降の変更の有無 ③静穏時間が debounce を超えたか
  ④対象 kind のセッションが誰も working でないか、を見て判定する（`memory_trigger.go` の
  `memoryShouldSnapshot` — 純関数なのでテストは実時間を待たない）。走査は
  `projects/*/memory` に glob で限定するので、同じマウントにある 883MB の transcript は
  一切 stat しない。ポーリングなので**「15 分 tick の保険」は本体に統合された**。
  加えて Console からの手動 snapshot。
- **busy 先送りの上限**: 実行中セッションがあるうちは待つが、変更から `MaxDefer`（既定 30 分）
  経つと busy を押し切って積む。状態マーカーは壊れ得る（停止済みセッションに working が
  残る等 — false-idle 対策で分かっているとおり）ので、busy 判定の誤りが「履歴が永久に
  積まれない」という最悪の壊れ方に化けないようにするため。
- **環境変数**: `AF_MEMORY_SNAPSHOT`（off で自動 snapshot を停止）・
  `AF_MEMORY_SNAPSHOT_INTERVAL` / `_DEBOUNCE` / `_MAX_DEFER`。
- **無変更 skip**: staging へ copy 後 `git status --porcelain` が空なら commit しない
  （空コミットで履歴を汚さない）。
- **コミットメッセージ**: 1 行目 `snapshot: 2026-07-23T12:00+09:00 (2 projects changed)`、
  trailer に `AF-Trigger: auto|manual|pre-restore|restore|import` と変更 slug 一覧。
  一覧 API はここから per-snapshot サマリを組み立てる（`git log --stat` 相当）。
- **整合性**: idle 時に copy するので書きかけファイルをほぼ拾わない。仮に torn file を
  拾っても次回 snapshot で自癒する（メモリは追記型 md であり中間状態も可読）。

### ③ REST API（dual allowlist に両側登録）

| メソッド/パス | 内容 |
|---------------|------|
| `GET  /api/agents/memory/roots` | ルート一覧（kind・プロジェクト数・最終 snapshot 時刻）。P4 で **`inactive`**（宣言はあるが今は無効なルートと理由）を追加 |
| `GET  /api/agents/memory/snapshots?limit=&before=` | snapshot 一覧（id/ts/契機/変更パスサマリ） |
| `POST /api/agents/memory/snapshots` | 手動 snapshot（`{trigger:"manual"}`） |
| `GET  /api/agents/memory/diff?from=&to=&path=` | unified diff（path 省略で全体、`projects/<slug>` で絞り込み） |
| `GET  /api/agents/memory/tree?rev=\|at=` | **その時点**のツリー概況（kind 別・プロジェクト別のファイル数/バイト数）。P2 追加 |
| `POST /api/agents/memory/restore` | `{rev \| at, scope: {all \| kinds: [kind...] \| projects: [slug...]}}`。手順は ④ |
| `PUT  /api/agents/memory/settings` | 自動 snapshot の ON/OFF トグル（`{auto:bool}`）。P2 追加 |
| `GET  /api/agents/memory/export?format=bundle\|tar&ack=1` | DL（Content-Disposition。CP proxy は既存 `rest` でストリーム可）。**生成前に secret スキャンを通し、検出時は 409 + findings。`ack=1` でだけ通す**（P3 実装） |
| `POST /api/agents/memory/import` | multipart 受領 → 検証 → preview `{importId, format, ref, head, headTs, snapshots, kinds[], projects[], unavailable[], rejected[], secrets[]}` |
| `POST /api/agents/memory/import/apply` | `{importId, scope: {all \| kinds \| projects}}` → 選択分を live へ適用（=新 commit） |

P1 で `roots` / `snapshots`(GET,POST) / `diff`、P2 で `tree` / `restore` / `settings`、
P3 で `export` / `import` / `import/apply` を実装済み（`workspace/agent/memory_handlers.go`）。
P4 の codex 有効化はこの表に足さず、**既存の `GET/PUT /api/codex/settings` に `memories` を
1 キー足す**形にした（両側登録済みのパスなので、CP 許可リスト漏れの罠を踏まない）。diff は
`?from=&to=&at=&path=` を受け、`from` 省略で「その snapshot が入れた変更」（初回 snapshot は
空ツリーとの差分）を返す。エラーは `memory_bad_request` / `memory_bad_rev` / `memory_bad_path` /
`memory_bad_scope` / `memory_no_snapshots` / `memory_snapshot_failed` / `memory_diff_failed` /
`memory_restore_failed` / `memory_export_failed` / `memory_import_failed` / `memory_bad_import` /
`memory_secret_detected` / `memory_too_large` の安定コード（i18n カタログ両言語に登録済み）。
手動 snapshot の `trigger` は `manual` 固定で、`restore` 等を API から詐称させない。

**P2 で表に足した 2 本の理由**:

- `tree` — restore のスコープ選択は「**その時点**に何が入っていたか」から作る必要がある。
  現在の roots を選択肢にすると、既に消えたプロジェクトを選べず「誤って消したメモリを
  戻す」という本命のユースケースが成立しない。
- `settings` — 決着 #1 の「全体 OFF は UI トグル（P2）で提供」の受け口。設定は
  `<CLAUDE_CONFIG_DIR>/af-memory.json` に永続し（repo と同じマウント＝同寿命）、
  ポーリングループが毎 tick 読み直すので再起動を要さない。`AF_MEMORY_SNAPSHOT=off`
  による運用側の強制はトグルより強く、UI からは戻せない（`autoLocked` で提示する）。

`scope` に `kinds` を足したのは、`Scopes=false` のルート（codex）を「まるごと」指す
表現が `all` と `projects` だけでは作れないため。`all` は個別指定を飲み込み、同じ
prefix を二重に適用しない。

`at`（日時指定）は agent 側で `git rev-list -1 --before=<at>` により snapshot に解決する。
CP 側は `control-plane/routes.go` に同パスを `rest` で登録（**登録漏れ=FE 404** の既知罠。
パターンルート `/api/agents/...` の扱いは既存の models ルートに倣う）。

### ④ ロールバックの安全設計

1. `restore` 受領 → まず **pre-restore snapshot** を自動取得（現在の live を必ず保全）。
2. 対象 rev の staging checkout（scope 指定時は `git checkout <rev> -- claude/projects/<slug>/`）。
3. live への適用は **allowlist 限定の rsync --delete 相当**（P2 実装では外部コマンドに
   頼らず Go で書いた）。要は非対称の組み方が肝で、「望ましい状態」は staging 側の列挙、
   「今の状態」は `memoryCollect`（= allowlist とシンボリックリンク不追従が効いた経路）から
   取る。削除候補が allowlist を通ったファイルに限られるので、transcript や資格情報を
   消す経路が**構造的に**存在しない（★1 の裏返し）。書き込み先も 1 セグメントずつ検査し、
   経路にシンボリックリンクがあれば拒否する（リンク越しに live の外を上書きしない）。
   削除で空になったディレクトリだけ畳み、非メモリが残る枝では必ず止まる。
4. 適用結果を **restore commit** として積む（`AF-Trigger: restore`、戻し元 rev を
   `AF-Restore-Rev`、適用範囲を `AF-Restore-Scope` として trailer に記録）。ここは live を
   読み直して commit するので、「実際に何が起きたか」が履歴の側で確定する（③ の結果を
   信用しない）。pre-restore commit にも `AF-Restore-Rev` を刻むので、履歴だけを見て
   「何に戻そうとした操作か」が follow できる。無変更なら commit しない（既に同じ内容
   だった場合）が、その場合でも API は「戻す前の状態を指す rev」として直近 snapshot を返す。
5. 実行ゲート: 当該 kind のセッションが実行中（working）の場合は警告を返し、Console 側で
   確認ダイアログを一段強くする（既定は続行可。実行中セッションが後からメモリを
   書けば restore 後の新 commit として現れるだけで、履歴上は追跡可能）。
6. **codex 固有**: restore の粒度はワークスペース全体（`Scopes=false`）。restore 後、codex の
   統合パイプラインは自前の `.git` ベースラインとの diff として変更を検知し、次回統合で
   再消化する——外部編集を差分として扱う diff 駆動設計なので、restore は仕様上グレースフルに
   受け止められる。`memories_1.sqlite` の watermark とズレても再統合で自癒する（★9）。

### ⑤ import / export（環境間移送）

- **export = git bundle**: `git bundle create <file> --all`。1 ファイルに全履歴・全 refs が
  入り、受け側で `git bundle verify` により完全性検証できる。「最新だけ軽く持ち出したい」
  用に tar.gz（HEAD ツリーのみ）も併設。
- **import**:
  - bundle → `git bundle verify` を必須に通してから
    `git fetch <file> +refs/heads/*:refs/imports/<ts>/*`。ローカル履歴とは独立の
    系譜として保持（graft しない）。preview はこの ref から生成。
  - tar.gz → 検証しつつ **work dir**（staging ではない）へ展開し、専用 index で
    `write-tree` → `commit-tree` して**同じ `refs/imports/<ts>/main` へ載せる**。
    P3 実装で staging 展開から変えた理由: staging は bare repo の index と対になって
    いて snapshot/restore が使い続けるため、外部入力の展開先に流用すると
    「取り込みに失敗したら live の版管理が止まる」という結合が生まれる。ref に
    揃えれば preview も apply も bundle と 1 本の経路で書ける。
  - **apply はプロジェクト選択式**: 含まれる slug 一覧を見せ、選んだものだけを
    「置き換え = 新 commit」で live に適用。3-way merge は .md の意味的衝突を機械で
    解決できないためやらない。取り込まなかった側もローカル履歴に残るので後悔がない。
    実装は **restore と同じ経路**（`memoryApplyRev`）で契機と trailer だけが違う
    （`AF-Trigger: import` / `AF-Import-Id` / `AF-Import-Ref`）。したがって取り込みでも
    pre-restore snapshot が積まれ、**取り込み自体を巻き戻せる**。
  - **apply の第 2 のかたち = 移設（`mode=migrate`・2026-08-25 追加・ADR 0022 決定 5-b）**:
    選択置き換えは**最新ツリーしか使わない**ので、bundle が運んできた全 snapshot は
    `refs/imports` に埋もれたまま 10 本を超えると刈られていた。「前の環境の履歴ごと
    引っ越したい」に応えるのが移設で、live へ書く手順は 1 バイトも変えず（＝★1 の
    裏返しの防御はそのまま）、**main をその系譜へ付け替える**。これだけで履歴一覧・差分・
    巻き戻しが相手の履歴に対してそのまま効く（`memoryResolveRev` は repo 内の任意 commit
    を受けるので復元側は無改修）。設計上の要点は 4 つ:
    ① 付け替えは **live を書き終えた後**に置く（①〜③ のどこで失敗しても履歴は動かない
    ＝「履歴だけ入れ替わって中身は古い」状態を作らない）。② 元の main は
    `refs/premigrate/<ts>` へ退避＝履歴は消さない（gc の対象にもならない）。③ 範囲は
    **全体固定**（一部だけ入れ替えると履歴と live が食い違う）。④ 付け替え後の live は
    取り込んだ head と一致するのが普通なので、★8 の無変更 skip をそのまま通すと
    **移設した事実がどこにも残らない**——ここだけ空 commit を許し、`AF-Import-Mode: migrate`
    と `AF-Premigrate-Rev` を刻む。REST は経路を増やさず apply に `mode` を 1 キー足す
    （新 REST は CP 許可リストの片側漏れという既知の罠を踏むため）。UI は bundle の
    ときだけ選択肢を出す（tar は 1 世代しか無く、選べば履歴を捨てるだけになる）。
- **slug 互換性**: リポジトリは全環境で `~/repos/<repo>` 規約なので、同名リポジトリなら
  slug（パス由来）が環境間で一致する。worktree suffix 付き slug はメモリを持たない
  （claude のメモリディレクトリは git リポジトリ由来で worktree 間共有）ため衝突しない。
- **codex の import 単位**: グローバル 1 ワークスペースなので kind 単位の置き換え
  （claude のようなプロジェクト選択はできない）。UI は kind ごとにチェックボックスを分ける。
- **検証**（★3）: サイズ上限（既定 64MB・`AF_MEMORY_IMPORT_MAX`）/ tar path traversal 防御
  （`cleanup_archive.go` の guard と同型）/ allowlist glob に合致しないエントリの拒否 /
  通常ファイル以外（symlink・hardlink・device）の拒否 / import・export とも監査ログ。
  拒否したエントリは黙って捨てず preview の `rejected` に出す。判定に使うのは
  `memoryRootDecls()`（**この環境で有効なルートではない**）で、codex を有効化していない
  環境でも codex 分を取り込むことはでき、live へ書く段（`memoryApplyScopeToLive`）で
  初めて弾かれる — preview の `unavailable` がそれを事前に伝える。
- **secret スキャン**（★4・決着 #2 で v1 必須へ格上げ・`memory_secrets.go`）: export 生成前に
  gitleaks 級の高シグネチャ正規表現で走査し、検出時は **409 でブロック**、`ack=1` を
  付け直したときだけ通す（UI 任せにせず API 単体で止まる）。走査範囲は運ぶ範囲に合わせる —
  tar は HEAD ツリーのみ、**bundle は到達可能な全 blob**（「一度書いて消した鍵」は HEAD に
  無くても bundle には入っている）。現ツリーに同じ blob が無い検出は `history` 印を付ける。
  **返すのは規則名・パス・行番号・先頭数文字のマスク済みヒントだけで、生値は API 応答にも
  ログにも出さない** — ここで生値を返すと、防御のつもりの機構が秘密を新しい場所
  （監査ログ・ブラウザ履歴）へ配る経路に化ける。import 側も同じ走査をかけるが、本人の
  データを持ち込む操作なのでブロックはせず preview に件数を出すだけにする。
- **repo 肥大**（★8）: snapshot commit と import の後に `git gc --auto`（閾値判断は git に
  任せる）。取り込み系譜は新しい 10 件を残して `update-ref -d` で刈る — 適用済みの内容は
  main 側の import commit として残るので、ref を消しても失われない。
- **将来トラック（P4 で見送り）**: CP 内部 git プロバイダ（ADR 0010 の bare+http-backend）へ
  agent が push mirror する経路を足すと、(a) WS 停止中の履歴閲覧・export、(b) scoped token に
  よる環境間の**直接** pull 同期、が同じ repo 形式のまま手に入る……はずだったが、内部 git の
  認可がテナント単位で per-user ACL を持たないため、そのまま載せると個人メモリが同僚から
  読める（「P4 の実際」③）。前提条件を満たすまで着手しない。v1 のデータ形式（git repo）
  を変えずに積み増しできることが、git を選ぶ決め手の一つ。

### Console UI（P2・P3）

設定モーダル「ワークスペース」グループに「エージェントメモリ」タブを追加
（`SettingsDialog.tsx` の GROUPS へ 1 エントリ + `MemoryTab.tsx`）。

- 上段: ルート/プロジェクト一覧（slug→リポ名整形表示・最終更新・サイズ）と手動 snapshot ボタン。
- 中段: snapshot 履歴（時刻・契機・変更プロジェクト）。行選択で右に diff ビュー
  （SCM コミットペインの diff 描画部品を流用）。
- 操作: 「この時点に戻す」（全体 / プロジェクト選択）→ 統一確認ダイアログ（P4a）。
  日付ピッカーからの「この日時時点へ」も同じ restore に解決。
- 下段: export（bundle / tar 選択 DL）・import（ファイル選択 → プロジェクト選択 preview → 適用）。
  **これは P3**。

P2 実装の実際（`console/src/features/settings/MemoryTab.tsx`）:

- 上段の手動 snapshot に加えて**自動取得トグル**を置いた（決着 #1）。運用側が
  `AF_MEMORY_SNAPSHOT` で止めている環境では disabled ではなく理由を添えて提示する。
- 中段の diff は SCM のコミットペインと同じ `<Diff>` を流用（見え方を 2 つに増やさない）。
- 戻し操作は P4a の統一確認ダイアログではなく、既存の `useConfirm`（danger）を使う。
  文面に「戻す直前の状態も自動でスナップショットに残る＝この操作自体を取り消せる」ことを
  明記し、実行中セッションがあるときだけ警告を 1 行足す（④-5）。
- ワークスペース停止中は起動導線の EmptyState に落とし、起動直後の 502 は
  `useRetryLoad` + `isTransientErr` で吸収する（running ゲート必須の既知パターン）。
- restore の POST は rev を**クエリにも**載せる。CP の監査台帳は URL からしか target を
  採らない（本文は読まない = docs/20 §A.6）ため、監査行に戻し元が残るようにするための
  ヒントで、実処理に使うのは本文側。import/apply の `importId` も同じ理由でクエリに載せる。

P3 実装の実際（同ファイル `TransferSection`）:

- 書き出しは素のリンク遷移ではなく **fetch → Blob → 一時 URL** で保存する。409（secret 検出）
  を JSON として受け取り、**何が引っかかったかを見せてから** ack して叩き直す必要があるため。
  自動 ack はしない（それをやると防御が実質無効になる）。値そのものは表示しない。
- 取り込みは「ファイル選択（受領）」と「範囲を選んで適用」を別操作にした。受領だけでは live に
  触れないので、中身（形式・スナップショット数・最終時刻・プロジェクト一覧・拒否件数・
  秘密情報の件数）を見てから決められる。
- 適用の選択肢は preview が返す `kinds` / `projects` から作り、`unavailable`（この環境に
  受け皿が無い kind）は選ばせない。

## P4 の実際（2026-07-27）

P4 として並べていた 5 項目は確度が大きく違った。**実装したのは 2 つ**で、1 つは
設計上の阻害要因が見つかったため実装せず前提条件つきで将来トラックへ送り、2 つは
不要と判断して落とした。

### ①-P4 codex memories のフリート有効化配線（実装）

決着 #4 が P4 へ送った「フリートとして memories を有効にするか」の受け口。有効化の実体は
`~/.codex/config.toml` の `features.memories = true`（`codex features enable memories` と等価）で、
書き込みは**既存の `GET/PUT /codex/settings`** を拡張して賄う（新規 REST を足さないので、
CP 許可リストの片側漏れ = FE 404 という既知の罠を踏まない）。`rate_limit_model_nudge` が
使っていた TOML 外科編集を汎用化し（`tomlBool` / `tomlSetBool` / `tomlHasSection`）、
利用者のコメントと `[projects.*]` 信頼設定をバイト単位で保つ。

- **コストへの手当**: 有効化すると抽出（Phase1）と統合（Phase2）がバックグラウンドで走り
  トークンを消費する。そこでトグルは「スイッチ 1 個」にせず、**有効化と同時に保守的な
  `[memories]` を seed** する（`min_rollout_idle_hours = 12` / `max_rollouts_per_startup = 4`、
  および抽出・統合モデルの安価な pin）。振れ幅は「走る量が減る方向」にしか取らない。
  既に `[memories]` がある設定には一切触らない（利用者の調整値が正）。**無効化では
  seed を消さない** — トグルの往復で調整値が飛ぶと「元に戻したつもり」が設定の消失になる。
- **モデル pin は焼き込まない**: スラッグはカタログ側で入れ替わるため、有効化時点の
  カタログ（`codex debug models`）から安価なものを引き、引けなければ**何も書かない**。
  当たらない pin を残すより自己無効化する方が安全。
- **「有効だが未生成」を区別する**: 有効化しても `~/.codex/memories` は次に codex が
  走るまで生えない。ルート宣言は `RequireDir` なのでその間ルートは現れないが、黙って
  落とすと「設定が効いていない」と読めてしまう。そこで `roots` API に **`inactive`** を
  足し、`codex_memories_disabled`（未有効）/ `codex_memories_pending`（有効・未生成）/
  `absent` を理由として返す。Console はこの行に有効化トグルとコスト注意書きを出す。
- **ドリフト検知**: 本番の codex 起動は `--strict-config` を付けない＝**未知のキーは黙って
  無視される**ので、上流が改名しても seed は無言で効かなくなる。`drift_test.go` に 2 本
  追加した: `memories` が `codex features list` に stage 付きで在ること、seed する
  `[memories]` キー全部が `app-server --strict-config` を通ること（存在しないキーを
  与えて拒否されることを毎回確かめる負の対照つき）。期待値は手書きせず
  `memoriesTuning()` から組み立てる。codex 0.145.0 で 4 キーとも受理を実測。

### ②-P4 operator MCP（実装・持ち出し系は意図して非公開）

`mcp_stdio.go` に 3 本足した。読み取り 2 本は af_read の会話にも出し、破壊的な 1 本は
`--write` の会話にだけ広告する（広告ツール集合がゲート＝既存の流儀）。

| ツール | 種別 | 中身 |
|--------|------|------|
| `list_memory_snapshots` | 読み取り | 履歴一覧（時刻・契機・変更プロジェクト） |
| `get_memory_snapshot` | 読み取り | `tree`（その時点の中身）＋ `diff`（その snapshot の変更）を 1 回で返す |
| `restore_memory_snapshot` | 書き込み | 指定時点へ戻す。範囲は `all` / `projects` / `kinds` |

- **範囲の省略を「全体」と解釈しない**。モデルがフィールドを落としただけでメモリ全体が
  巻き戻る事故を引数の段で潰す（利用者の承認は「この範囲を」に対して得られているので、
  暗黙の拡大は裏切りになる）。rev / at の省略も同様に拒否する。
- **export / import は公開しない**。P3 で持ち出しに secret スキャン＋本人の明示 ack を
  課したのに、MCP 経由で「モデルが ack してファイルを吐ける」経路を作ると、その防御を
  迂回する二つ目の出口になる。加えて MCP の応答はテキストで、bundle はバイナリかつ
  MB 級＝そもそも器が合わない。持ち出し／取り込みは Console の本人操作に限る。
  この不在は回帰テストで固定してある（ツール名に export/import を含むものが
  広告集合に現れたら失敗する）。

### ③-P4 CP internal git への push mirror（**実装せず・将来トラック**）

「WS 停止中の閲覧/export」と「環境間の直接同期」を狙った項目だが、**現行の内部 git
プロバイダにそのまま載せてはいけない**ことが分かったので実装しなかった。

- **阻害要因（本質）**: `control-plane/git_http.go` の `authorizeGitRepo` は認可が
  **テナント境界まで**で、`canPush` が write を role で絞る一方、**read は当該テナントの
  アクティブメンバー全員に開いている**。repo 台帳も `ListGitReposByTenant` とテナント単位で、
  per-user ACL の概念が無い。つまり個人メモリを内部 git repo として mirror すると、
  **テナント同僚の誰もが clone できる**。★4（メモリは個人情報・export は本人操作のみ）と
  正面から衝突し、P3 の secret スキャンによる持ち出しゲートも mirror 経路が丸ごと迂回する。
- **副次の重さ**: 停止中閲覧を成立させるには CP 側に snapshots / diff / tree / export の
  読み取り API 一式（＋export の secret スキャン）を持つ必要があり、ADR 0022 が
  「v1 で CP に台帳・認証・GC を持つのは可動部過多」として退けた構図がそのまま戻る。
- **着手の前提条件**: (a) 内部 git に所有者限定 repo の概念を入れる、または (b) 台帳に
  載せない per-user のミラー領域（`<dataRoot>/memory-mirror/<membership>.git`）と
  専用 API を新設する。いずれも docs/39 の範囲を超えるので**別 ADR 相当**。
  v1 のデータ形式（git repo）は変えていないので、前提が整えば形式無変更で接続できる
  ——「git を選ぶ決め手」は生きたまま残る。

### ④-P4 scheduler 連携（**削除**）

不要と判断した。P1 で契機がフック相乗りではなく**常駐ポーリング（既定 1 分 tick）**に
なった時点で、定時実行から snapshot を叩く機構は二重になる（②に「ポーリングなので
『15 分 tick の保険』は本体に統合された」と書いたのと同じ理由）。頻度を変えたいなら
`AF_MEMORY_SNAPSHOT_INTERVAL` / `_DEBOUNCE` があり、全体 OFF は UI トグルがある。

### ⑤-P4 opencode / agy の上流 watch（**コード作業なし**）

ルート宣言テーブルは P1 で「1 行足せば snapshot / rollback / export が全部付く」形に
なっており、上流がメモリを実装するまで触る余地が無い。kiro の global steering
（`~/.kiro/steering/*.md`）も同じく root 1 行で載る候補のまま。

## 先行 OSS の調査（2026-07-23 追記）

「agent-memory」という名前の OSS は 2 つあり、性質が違う。本設計との関係を整理する。

| プロジェクト | 実態 | 本設計との関係 |
|--------------|------|----------------|
| [jayzeng/agentmemory](https://github.com/jayzeng/agentmemory)（CLI 名 `agent-memory`・11★・MIT） | [tobi/qmd](https://github.com/tobi/qmd)（Shopify Tobi 氏のローカル markdown 検索エンジン・28k★・BM25+ベクトル+リランクを全ローカル GGUF で実行）の上に載る**検索・注入系**。`~/.agent-memory/` に MEMORY.md/daily/topics/scratchpad の素 md を置き、SKILL.md でエージェントに書かせ、セッション開始時に優先度付き 16K 注入＋プロンプト連動の qmd 検索結果を差し込む。**版管理・import/export は皆無**（「git-friendly なので各自どうぞ」） | **別軸**。当設計＝メモリの**ライフサイクル**（履歴・巻き戻し・移送）、こちら＝メモリの**活用**（検索・想起）。競合せず、md をメモリの正とする点は当設計の妥当性を裏付ける |
| [xChuCx/agent-memory](https://github.com/xChuCx/agent-memory)（同名別物・Go・Apache-2.0・qmd 不使用） | 「git-native な per-repo メモリ」。**staged write→`review --diff`→apply の人間レビューゲート**、**書き込み時の secret/PII スキャン**、SQLite FTS5、git commit にピン留めした共有ストア | むしろ**こちらが当設計の直接の先行事例**（md の正＋git が同期、ベクトル DB なし）。取り込む価値のある発想が 2 つ（下記） |

取り込む点 / 確認できた点:

1. **export/snapshot 時の secret スキャン**（xChuCx 由来）: ★4 の防御を強化。export 生成時に
   軽量な secret パターン検査（gitleaks 級の正規表現）をかけ、検出時は警告付き確認にする。
   v1 の要件に昇格させる価値あり（実装コストは小さい）。→ **P3 で実装済み**
   （`memory_secrets.go`。警告ではなく既定ブロック＋明示 ack にした。snapshot 側には
   かけない — 自動 snapshot をブロックすると履歴が欠け、それは本件の主訴そのものを
   壊すため。持ち出しの瞬間に止めるのが正しい位置）。
2. **「検索索引は派生状態」原則の裏付け**: qmd の索引（`~/.cache/qmd/index.sqlite`）も codex の
   `memories_1.sqlite` も md から再構築可能な派生状態であり、版管理対象は md だけで良い——
   ★9 の一般化。将来メモリ検索を足す場合も md が正のままで済む。
3. **将来トラック（本設計とは別機能）**: フリート横断のメモリ検索・セッション注入をやるなら
   qmd が有力バックエンド（MCP サーバ内蔵・BM25 のみなら軽量）。ただしベクトル系は
   ローカル GGUF 模型 ~2GB＋CPU 消費があり、メモリ制約ホストでは BM25 限定から。
   これは「メモリ活用」の独立機能であり docs/39 のスコープ外とする。

採らない点: グローバル単一ストア（jayzeng 型。当環境はプロジェクト/kind 分離が正）、
per-repo コミット型ストア（xChuCx 型。リポジトリに個人メモリを混ぜない方針と衝突）。

## 検討して捨てた選択肢

| 案 | 捨てた理由 |
|----|-----------|
| CP ホスト bare repo を v1 の正にする（内部 git 流用） | WS 停止中も操作できる利点はあるが、ECS では CP がユーザーデータへ直接ファイルアクセスできず、結局 agent push 型が必須。v1 から CP 側に台帳・認証・GC を持つのは可動部過多。P4 の mirror として積む予定だったが、内部 git の認可がテナント単位で per-user ACL が無いことが分かり見送り（「P4 の実際」③） |
| live ツリーへ直接 `git init` | claude 自身がメモリ列挙で `.git` を見る・エージェントの誤操作/破損リスク・allowlist 外ファイル混入。staging コピーはメモリが小さいので実質無料 |
| `/var/lib/af/claude` 全体の版管理 | transcript 883MB と `.credentials.json` を巻き込む。論外 |
| restic / 定期 tar 世代管理 | 差分 UX・時点解決・プロジェクト単位の選択復元・環境間移送のすべてで git に劣る。git はこの4要求の共通基盤 |
| import の 3-way merge | .md の意味的衝突は機械で解決不能。「選択置き換え + 双方の履歴保全」で実用十分 |
| `~/.local/share/agent-fleet` に repo を置く | home は clean-home の対象（homeKeep 追加が要る）。claude-config マウントは仕様として「何をしても消えない」保証が既にあり、メモリと同寿命で自然 |

## 重要な落とし穴（設計で必ず閉じる）

- **★1 巻き込み事故**: 対象は roots の allowlist（claude: `*/memory/**`、codex:
  `memories/**` − `.git`）で構造的に限定。deny 方式にしない。transcript・credentials・
  sqlite が repo に入る経路をコード上作らない（テストで担保）。
  **P1 実装で追加した防御**: allowlist の内側にシンボリックリンクを置かれると、リンク先
  （`.credentials.json` 等）を allowlist 内のファイルとして読めてしまう。よって収集時に
  **シンボリックリンクは種別を問わず不採用**にする（ファイルもディレクトリも辿らない）。
  回帰テストは本番同様の live ツリー（transcript jsonl・`.credentials.json`・`settings.json`・
  `af-usage.json`・`memories_1.sqlite`・codex の自前 `.git`・メモリ内から資格情報へ抜ける
  シンボリックリンク）を置いた隔離 HOME で snapshot し、HEAD ツリーが許可 md と完全一致
  すること＋資格情報の中身がどの blob からも grep できないことを見る。
- **★2 restore と live 書き込みの競合**: pre-restore snapshot の自動取得＋実行中セッション
  警告。restore 自体も commit として積むので、どの順で何が起きたか常に履歴から復元可能。
- **★3 import は外部入力**: サイズ上限・traversal 防御・allowlist 外拒否・監査。bundle は
  `git bundle verify` 通過を必須にする。**P3 実装で確認した効き方**: 取り込んだ ref に何が
  入っていようと、live へ書く段は restore と同じ `memoryApplyScopeToLive` を通るので、
  allowlist の外へは 1 バイトも書かれない（★1 の裏返しが import にもそのまま効く）。
  回帰テストは traversal・allowlist 外・`.git`・symlink を混ぜた敵対 tar を取り込ませ、
  ツリーに残るのが許可 md 1 件だけであることと、live 側に痕跡が無いことを見る。
- **★4 メモリは個人情報**: export は本人操作のみ・監査ログ必須（GET だが読み取りの例外として
  `auditActionTarget` に載せる）。UI に「共有前に内容確認・書き出したファイルは暗号化されて
  いない」の注意書き。運用ポリシー上メモリに秘密を書かない規約はあるが、防御はそれに依存
  しない。**P3 でこれを secret スキャン（既定ブロック＋明示 ack）まで実装した**（⑤ 参照）。
- **★5 git 実行環境の独立**: repo 専用に `user.name=af-memory` / `user.email` を固定し、
  ユーザーの `~/.gitconfig`（signing 設定等）を継がない（`GIT_CONFIG_GLOBAL=/dev/null`）。
- **★6 slug の表示**: `-home-dev-repos-agent-fleet` をそのまま見せず、リポ名へ整形
  （markdown-ref-linkify で使った slug→表示名対応を流用）。
- **★7 ECS 実測**: agent 側完結なので動く設計だが、EFS 上の staging copy レイテンシは実測
  項目（対象が小さいため実害なしの見込み）。
- **★8 repo 肥大**: 追記型 md ＋テキスト差分なので伸びは遅い。P3 実装では snapshot commit と
  import の直後に毎回 `git gc --auto` を呼ぶ（実際に走るかの閾値判断は git 側に任せる方が、
  自前で「N 回ごと」を数えるより壊れにくい）。失敗は握り潰す — snapshot は成立している
  のに失敗として返すと「積めたのに失敗」に見えてしまう。取り込み系譜の刈り込みも同様。
- **★9 codex の派生状態との整合**: `memories_1.sqlite`（stage1 抽出・jobs の watermark）と
  `~/.codex/memories/.git`（統合ベースライン）は**派生状態なので snapshot に含めない**。
  md だけを restore すると一時的に不整合になるが、codex の統合は diff 駆動なので次回
  パイプラインが外部変更として再消化する。逆に sqlite まで巻き戻すと thread 由来の
  watermark が実 rollout と食い違い、抽出済みデータを失う方向に壊れる——含めないのが正。
  codex 統合と snapshot/restore の同時実行だけは避ける（codex セッション実行中警告が兼ねる）。
- **★10 受け側の live ルートはまだ無いことがある**（2026-08-25 実機で発覚・修正済）:
  claude の live ルート `<CLAUDE_CONFIG_DIR>/projects` は claude が一度起動して初めて
  出来る。ところが import の本命は「新しい環境を立てて、真っ先に前の環境のメモリを
  持ち込む」であり、そこではルート自体が存在しない。`memoryPrepareDest` は rel の各段を
  1 段ずつ `os.Mkdir`（経路上の symlink 検査のため）していたので、ルートが無いと
  **プレビューまでは通り、適用だけが ENOENT で落ちる**。ルートだけは `os.MkdirAll` で
  用意する（作るのは自分の config 配下なので ★1 の防御は緩まない）。
  併せて**診断可能性**の穴も同時に塞いだ: Agent は原因を message に載せていたのに、
  Console の `errText` は `err.<code>` の訳があると message を捨てるため、画面には
  「取り込みに失敗しました」しか出ず、Agent 側にもログが残っていなかった。
  → 汎用コードは `errDetail`（定型文＋message 併記）で出し、500 は Agent 側でも
  `log.Printf` する。**原因がコードで一意に定まらない `*_failed` 系で message を畳むと、
  利用者の環境でしか起きない失敗は永久に追えない。**

## フェーズ

| フェーズ | 内容 | 出口条件 |
|----------|------|----------|
| P1 ✅ | snapshot エンジン（roots 宣言 = claude+codex・staging・bare repo・自動/手動契機）＋ REST（roots/snapshots/diff）＋ dual allowlist 登録 | 自動 snapshot が実データで積まれ、一覧/差分が API で取れる（codex は dir 存在環境で） |
| P2 ✅ | restore（全体/プロジェクト・pre-restore・scope 限定の適用）＋ `tree` / `settings` REST ＋ Console タブ（履歴/差分/復元/自動取得トグル） | UI から日時指定・プロジェクト単位の巻き戻しが往復できる |
| P3 ✅ | export/import（bundle/tar・preview・選択適用・検証・監査）＋ **★4 secret スキャン**（決着 #2 で v1 必須へ格上げ）＋ ★8 `git gc --auto` | 2 環境間で bundle 持ち回りの移送が実際に通る（隔離 HOME を 2 つ演じる往復テストで担保。**実機往復は 2026-08-25 に実施し ★10 を検出→修正**） |
| P4 ✅ | 拡張: codex memories のフリート有効化配線（`[features] memories` の Console トグル＋`[memories]` チューニング seed）／operator MCP ツール（読み取り＋restore）。CP mirror は阻害要因つきで将来トラックへ、scheduler 連携は不要と判断して削除（下記「P4 の実際」） | 有効化トグルが実バイナリで効くこと（drift テスト）・MCP から履歴閲覧と復元が通ること |

## 決着した未決事項（2026-07-27 利用者承認・実装はこの前提で進める）

設計時に持ち越した 4 点は、いずれも推奨既定値のまま採用で決着した。

| # | 論点 | 決定 | 根拠 / 実装への含意 |
|---|------|------|--------------------|
| 1 | 自動 snapshot の既定 | **ON**（claude 全セッション idle 遷移 + **debounce 5 分**） | 履歴が無いことが本件の主訴なので、既定 OFF では機能が存在しないのと同じ。無変更 skip があるので履歴は汚れず、対象が数百 KB なのでコストも無視できる。debounce・保険 tick は環境変数で上書き可能にする（`AF_MEMORY_SNAPSHOT_*`）。全体 OFF は UI トグル（P2）で提供 |
| 2 | export の暗号化 | **v1 は平文 DL**（HTTPS・本人操作のみ・監査ログ前提） | 経路は TLS で保護され、DL 主体は本人に限定される。パスフレーズ暗号化（age 等）は「持ち出したファイルの保管中の保護」であり別レイヤの要件——**将来トラック**として残し v1 の要件にはしない。代わりに ★4 の secret スキャン＋UI 注意書きを v1 で必ず入れる |
| 3 | UI の置き場 | **設定モーダル「ワークスペース」群の新タブ**（`SettingsDialog.tsx` GROUPS ＋ `MemoryTab.tsx`） | データ管理は設定群と同質で、日常的に開く画面ではない。左レール常設が要るほどの操作頻度ではないと判断。必要になれば P2 の実装をそのまま差し替え可能（タブの中身はコンポーネント 1 個） |
| 4 | codex memories のフリート有効化 | **P4 で配線を実装。有効化そのものは利用者の選択に委ねる（既定は OFF のまま）** | 統合パイプラインが extract/consolidation モデルをバックグラウンドで呼びトークンを消費するため、有効化はコストを伴う独立の判断。**P1 は「有効化されていれば自動で対象化」まで**を実装し、**P4 でその ON/OFF を Console から選べるようにした**（コスト注意書き＋保守的な `[memories]` seed 付き。「P4 の実際」①） |

将来トラック（v1 のスコープ外・要求が出たら着手）: export のパスフレーズ暗号化、
CP internal git への push mirror（**前提条件つき** — 内部 git に所有者限定の概念が要る。
「P4 の実際」③）、メモリ検索・セッション注入（qmd 等・本設計とは別機能）。
