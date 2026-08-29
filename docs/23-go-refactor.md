# 23. Go バックエンド内部リファクタ（CP / Workspace Agent — 機能不変・ワイヤ互換）

フラットな単一 `package main` に育った 2 つの Go プログラム（`control-plane/` 約 13.8k 行・54 ファイル、
`workspace/agent/` 約 16k 行・61 ファイル）を、**ワイヤ API 完全互換のまま** `internal/` パッケージに
層化し、重複を畳む。Console リビルド（docs/22, ADR 0011）が新アーキテクチャへのスワップを終えた今、
バックエンドだけが「増築を全部吸い込むフラット構造」のまま残っている — その解消が目的。

> ステータス: **P0〜P2 完了**（2026-07-09、branch `temp/sfiv6ai`）。決定の要約は
> [decisions/0012-go-internal-refactor.md](decisions/0012-go-internal-refactor.md)。
>
> - **P0**: CI（.github/workflows/ci.yml）/ `buildMux()` 抽出 + httptest スモーク（CP・Agent）/
>   エラーコード const 化（errcodes.go ×2 ↔ client.ts）/ agent gofmt 整形。
> - **P1**: gitx.Run 系ラッパー（生 git exec 54 箇所置換）/ fstore（per-sid ストア 7 家系統一）/
>   decodeJSON（25 箇所）/ session.go を 5 ファイルに分割 / **internal/{gitx,fstore,httpx,transcript}
>   の 4 パッケージ切り出し**（writeJSON 110・writeErr 267 箇所書き換え、transcript は共有モデル型
>   Turn/Part/Edit/Question/Task）/ agent.go を CLI 種別ファイルに分割。
> - **P2**: runtime.go 分離（ports / runtime_docker / workspace_handlers / httpapi）/ buildMux を
>   機能別 register 関数 17 個に分散 + **authGate 除外レジストリ**（isAuthExempt のハードコード解消、
>   同一判定テスト付き）/ manager.go を 6 ファイルに分割 + dockerInspectOut シーム /
>   **manager.mu の DB I/O 跨ぎ直列化を解消**（per-membership build ロック、-race テスト付き —
>   本計画で挙動に触れた唯一のコミット c828bff）/ Store を機能別サブインターフェース 17 個に再構成
>   （gitGC を narrow view の実例に）/ グローバル可変 map 3 つを struct 化。
>
> **残タスク①〜③も完了**（2026-07-09 後半）:
> ① **CLI 縦割りパッケージ化完了** — Wave A: internal/{session,status,tmuxx,paths} → B:
> internal/secrets → C: internal/agents（IF層 + Flow/SidStore 共有部）→ D/E/F:
> internal/agents/{opencode,codex,claude}（impl+auth+usage+settings+transcript+ハンドラを
> CLI ごとに集約）。main 残置は registry / routes / hook サブコマンド入口 / ペイン I/O switch /
> session_title / chat プロバイダのみ。Agent は main + 13 internal パッケージ構成。
> ② chat.go → 4 ファイル（モデル/store/プロバイダ/ハンドラ）。
> ③ CP ハンドラ層: memberAuth（withMembership/withResolved/withIdentity/withSuperAdmin +
> superAdminFor/tenantAdminFor）に解決プリアンブルを集約し、機能 struct へ全面変換 —
> memoAPI / ssmConfigAPI / wsSettingsAPI / patAPI / workspaceAPI / agentProxyAPI / previewAPI /
> tenantAPI / adminAPI / egressAPI / gitServerAPI / mcpAPI。store は narrow view
> （合成サブインターフェース）を持つ。config に残るのは edge/auth（Google/Bitbucket OAuth +
> authGate）の 17 メソッドのみ — 「config 解体」完了。
>
> 未着手は **④ P3（契約の型化、任意）** のみ — console/src/types の手書き型との突き合わせを
> 伴うため、着手はユーザー判断。

## 背景と診断

個々のロジックは健全で、実機検証も済んでいる。問題は**境界がコンパイラに強制されていない**ことと、
その帰結（god オブジェクト・並列コピー実装・テスト不能領域）に集約される。

### 共通: 安全網が存在しない

- CI なし（`.github/` 不在）・linter 設定なし。`CONTRIBUTING.md` の gofmt / vet / test 規約は手動運用。
- API 契約は **三重の手動同期**: Console の手書き TS 型（`console/src/types/*`）↔ Go の構造体
  （または `map[string]any` 直返し）↔ エラーコード文字列（`core/api/client.ts` の `ERR_TEXT` と
  Go 側リテラル）。コンパイル時・CI 時のガードはゼロで、唯一の防波堤は目視。
- HTTP ハンドラ層に `httptest` が 1 本もない（テストされているのは純粋パーサ/ヘルパのみ）。
  ルート登録が両プログラムとも `main()` にインライン展開されており、そもそもテストから mux を
  組み立てられない。

### control-plane

- **god オブジェクト×2**: `config` に **133 ハンドラメソッドが 22 ファイル**に分散。`manager`
  （`manager.go`）は identity/RBAC 解決・ワークスペースライフサイクル・runtime キャッシュ・
  DEK 暗号・docker exec を全部抱え、しかも `manager.mu` を **DB I/O を跨いで保持**
  （`buildResolvedLocked`）— 全ワークスペース解決が 1 本のミューテックスで直列化される。
- `main()` が 463 行: フラグ・初期化順序制約（コメントでのみ文書化）・約 180 のルート登録が同居。
- `runtime.go` に dockerRuntime アダプタ／バックエンド非依存の HTTP ハンドラ／`writeJSON` が同居。
- 認証 3 モデル（セッション・admin RBAC・内部トークン）のうちミドルウェア化されているのは 1 つ
  だけ。`isAuthExempt`（oauth_google.go）の除外パスリストは `main()` のルート登録と手動同期。
- `resolvedFor` / `requireSuperAdmin` プリアンブルが約 130 ハンドラに手書きコピー。
- プロセスグローバルの可変状態（`bbStates` / `cpuPrev` / `diskCache`）— 単体テスト不能で、
  将来のマルチインスタンス CP（AWS アダプタ）とも相性が悪い。

### workspace-agent

- **CLI 3 種（claude / codex / opencode）の並列コピー実装**: transcript パーサ 3 本（計約 1,930 行、
  同じ `chatTurn` モデルを出力）・auth 3 系統・usage 2 系統が、インターフェースなしの
  parallel-but-duplicated コード。`agent.go` の `Agent` インターフェースは呼び出し側だけ統一し、
  実装側は統一されていない。
- **per-sid ファイルストアの 5 点セット**（dir/path/write/read/remove）が `session_status.go` 内に
  6 回コピペ + `agent.go` の `sidStore`。
- 生の `exec.Command("git", ...)` が約 60 箇所（ラッパーなし）。`-C` / stderr / エラー整形が
  呼び出し毎に再実装。
- `session.go`（1,208 行）にワイヤ変換・メタ永続化・tmux 実行・HTTP ハンドラ・CLI コマンド構築の
  5 責務が同居。`session_title.go`（638 行）は LLM 呼び出し + git + transcript に跨る横断機能の塊。
- 状態が **11 個の独立したグローバルロック島**（`titleGenMu` / `procMu` / `convLocks` / …）に分散。

## 据え置く資産（壊さない）

- **Store 抽象**（`control-plane/store.go`）: sqlite/postgres は別実装ではなく、`?` プレースホルダの
  rebind（`store_sql.go`）+ 方言別マイグレーション embed だけで差分吸収する**単一 `sqlStore`**
  （`store_postgres.go` は 53 行）。この構成は維持。
- **Runtime ポート**（`runtime.go` の `Runtime`/`RuntimeFactory`）: docker/ECS の分岐はファクトリ
  1 箇所に完全集約され、具象型がハンドラに漏れている箇所はゼロ。ECS 側の fake テストも維持。
- **Agent インターフェース**（`workspace/agent/agent.go`）: かつて散在した ~50 箇所の kind 分岐を
  registry + caps 方式に畳んだ既存の成功パターン。P1 はこれを「実装側」へ延長する。
- **CP↔Agent の 2 バイナリ分離**: 一見重複に見える `preview.go` ×2 や SSM 系は実際は
  **プロトコルの両端**（CP が認証を付けて転送・Agent がコンテナ内へ中継）であり、信頼境界として
  設計・文書化済み（architecture.md）。統合しない。

## ハード制約（全フェーズ共通）

1. **ワイヤ API 完全互換**。Console は書き上がったばかりで型は手書き同期 — パス・メソッド・
   JSON 形・エラーコード文字列を 1 バイトも変えない。
2. **`//go:embed` の配置固定**: CP のマイグレーション（`store_sqlite.go` / `store_postgres.go`）、
   Agent の `knowledge/af-usage.md`（+ `workspace/.dockerignore` の `!` 復帰ルール）。パッケージを
   動かすときは embed 対象ディレクトリを**一緒に**動かす。
3. **Docker ビルド**: 両 Dockerfile はモジュールディレクトリ丸ごと COPY → `go build .`。
   **モジュール内の `internal/` 分割はビルド無変更**で通る。モジュール横断の共有パッケージは
   go.work + Dockerfile コンテキスト変更が必要（→ 見送り、下記）。
4. **ポート&アダプタ原則**（CONTRIBUTING.md）: デプロイ固有物は Runtime / KeyCustodian /
   MetadataStore / AuthGateway の背後に。既存ポートを跨いで壊す再編は不可。
5. 検証ホストはメモリ制約あり — ビルド/テストは直列で。

## 方針

**採用**: 各モジュール内で `internal/` パッケージに層化し、重複はモジュール内ヘルパに畳む。
安全網（CI + httptest スモーク）を先に敷き、以後は機械的な移動 wave を小さく刻む。

**不採用**:

- **2 バイナリの統合** — 信頼境界として設計された分離。得るものがない。
- **共有 Go モジュール（go.work）** — モジュール横断の真の重複は `writeJSON` と ID 生成程度
  （しかも `writeErr` の有無など流儀が既に分岐）。Dockerfile 2 本の変更コストに見合わない。
  型付き API 契約を共有したくなった時（P3）に再検討。
- **web フレームワーク導入・書き直し** — stdlib `net/http` + Go 1.22 ルーティングで足りている。
  docs/22 が Console で「リファクタでは足りない」と診断したのとは逆に、Go 側は**ロジック健全・
  構造だけの問題**なので、リビルドではなくリファクタが正解。

## フェーズ計画

各フェーズは独立にマージ可能。1 wave = コンパイル + テスト green + ワイヤ互換の小さな単位で刻み、
**ファイル移動 wave とロジック変更 wave を混ぜない**。

### P0 — 安全網（最初に、これなしで大移動はしない）

1. **CI**（GitHub Actions）: 両モジュールの `gofmt -l` / `go vet` / `go test` / `go build`、
   Console の typecheck + build。既存規約の機械化のみ — 初日から green で開始する
   （golangci-lint は既存コードへの指摘で red から始まるため見送り、後続で opt-in）。
2. **ルート登録の関数抽出 + httptest スモーク**: 両 `main()` から `buildMux()` を機械的に抽出し
   （挙動不変）、主要ルートグループに「期待ステータス + 既知 JSON キー」レベルの薄いテストを敷く。
   以後のハンドラ移動のリグレッション検出器。
3. **エラーコード文字列の定数化**: `ERR_TEXT`（`console/src/core/api/client.ts`）と対になる Go 側
   リテラル（`quota_sessions` / `worktree_dirty` 等）を両プログラムで const に集約。三重同期の
   一角を grep 可能にする。

### P1 — Agent: 重複畳み込み + `internal/` 分割（効果/リスク比が最良・先行）

先に機械的な畳み込み（挙動不変・diff が読みやすい）:

1. `runGit(dir, args...)` ラッパー導入 → 約 60 箇所を置換。
2. ジェネリック `fileStore[T]` を 1 つ作り、`session_status.go` の 6 コピペ + `sidStore` を置換。
3. `decodeJSON(r, &v)`（400 応答込み）で約 24 箇所の decode 手書きを置換。ポーリング用の小さな
   `poll(interval, timeout, fn)` も同様。

次にパッケージ分割（embed は `knowledge/` ごと移動）:

| パッケージ | 中身（現状の出所） |
|---|---|
| `internal/httpx` | writeJSON / writeErr / decodeJSON / requireToken / logRequests |
| `internal/store` | `fileStore[T]` と `~/.config/agent-fleet` パス規約（現在 ~15 ファイルに散在） |
| `internal/gitx` | `runGit` + 作業コピー操作（git.go / git_view.go / git_identity.go / fs_git.go） |
| `internal/agents` | `Agent` インターフェース + registry + caps（agent.go） |
| `internal/agents/{claude,codex,opencode}` | launch / auth / usage / transcript を **CLI 縦割り**に集約（現 `*_auth.go` / `*_usage.go` / `*_transcript.go` / `*_settings.go`） |
| `internal/transcript` | 共有 `chatTurn` モデルとウィンドウ処理（session_transcript.go の共通部） |
| `internal/session` | メタ永続化・tmux・ライフサイクル（session.go の 5 責務を分離） |
| `internal/chat` `internal/title` `internal/remote` | assistant chat / タイトル+ブランチ提案 / GitHub+Bitbucket REST |

transcript パーサ 3 本は無理に 1 本化しない（入力形式が本当に違う）— **同じパッケージ境界と同じ
出力型に揃える**ことで並列性を構造として固定する。opencode の usage 欠落や codex chat スタブの
ような穴も、インターフェースの未実装として可視化される。グローバルロック島は移動時に各所有
struct へ取り込む。

### P2 — CP: god オブジェクト分解 + `internal/` 分割

1. **プリアンブルのラッパー化**: `member(h func(w, r, resolved))` / `admin(...)` アダプタで
   約 130 箇所の `resolvedFor` / `requireSuperAdmin` 手書きを畳む。
2. **ルート登録の機能別分散**: `main()` の約 180 行を機能ごとの `registerRoutes(mux)` へ。
   `isAuthExempt` のパスも各機能の登録側に寄せて手動同期を解消。`main()` は配線数十行に。
3. **`config`（133 メソッド）の解体**: 機能別ハンドラ struct（tenants / memo / ssm / gitserver /
   admin / …）へ分割し、各々が `*manager` ではなく必要最小のインターフェースを持つ。
4. **`manager` の分割**: identity/RBAC 解決（resolver）・ワークスペースライフサイクル・DEK
   （custodian 側へ）・docker exec（`execer` シームでテスト可能に）。あわせて **`manager.mu` の
   ロックスコープから DB I/O を追い出す** — 挙動が変わり得る唯一の箇所なので**単独 PR**で。
5. **`runtime.go` の分離**: `internal/runtime`（port + docker + ecs）とハンドラ（api 側）に分割。
6. **Store インターフェース（85 メソッド）の機能別サブインターフェース化**: 実装は単一 `sqlStore`
   のまま、利用側は `MemoStore` 等の狭いビューに依存。`migrations/` はパッケージと同伴移動。
7. グローバル（`bbStates` / `cpuPrev` / `diskCache`）の struct 取り込み — マルチインスタンス CP
   （P3-7 AWS アダプタ）への布石。

### P3 —（任意・後日）契約の型化

Go 構造体 → TS 型生成（tygo 等）や `map[string]any` レスポンスの DTO 化。効果は大きいが
`console/src/types` の手書き型との突き合わせ・置換を伴うため、P1/P2 完了後に別タスクとして判断。

## 進め方の原則

- 挙動に触るのは `manager.mu` のスコープ修正 1 点のみ。それ以外は純粋な再配置 + 畳み込み。
- 各 wave のゲート: `gofmt -l` 空 / `go vet` / `go test ./...`（両モジュール）+ P0 スモーク。
- コミットは都度 push。実機（ライブフリート）での目視確認は wave 単位でなくフェーズ単位で可
  （ワイヤ互換ゆえ、コンパイル + スモークが通れば運用リスクは低い）。
