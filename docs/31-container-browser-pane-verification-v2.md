# コンテナ内ブラウザペイン V1 完成イメージ実測レポート v2（backpressure 修正後の再検証）

> 実施日: 2026-07-19
> 対象設計: [31-container-browser-pane.md](31-container-browser-pane.md)
> 対象ADR: [decisions/0018-container-browser-pane.md](decisions/0018-container-browser-pane.md)
> 前回レポート（修正前・不合格）: `feature/browser-pane-v1-container-verify:docs/31-container-browser-pane-verification.md`
> 前提修正: screencast backpressure 修正（`b7ff65d` = `fe388b9` を main へ統合、完成イメージへ反映済み）
> 本再検証で追加した修正: `workspace/agent/browser_manager.go` の `startScreencast` リトライ（§5.2）
> 最終判定: **条件付き合格** — 前回の P0（screencast backpressure crash）は解消・全受入シナリオが継続描画に到達。ただし完成イメージには別系統の初回描画レース（§5）が残存しており、本ブランチの追加修正を焼き込んだ再ビルドを V1 サインオフの条件とする。

## 1. 結論

完成イメージ（container `15592d470c28`、焼き込み `workspace-agent` build 時刻 2026-07-19 01:04:38 =
merge `b7ff65d`(01:01:07) より後、fix シンボル在中）で再検証した。

- **前回 P0（screencast backpressure crash）は解消**。1200×800 aggressive animation、2 Page 同時（5分）、
  最大 1600×1200、wheel、navigation/reload、Vite HMR のいずれも `reason=screencast backpressure` の
  Page crash を起こさず、~12fps 上限で継続描画した。前回「取得不能」だった animation/2 Page/最大 viewport の
  1〜5分計測をすべて採取できた。
- sandbox smoke（`e2e-smoke.sh --inner`）は全項目 PASS（`== smoke OK ==`）、機能シナリオ（crash 復帰・
  hidden 停止・idle 回収・DELETE 404）も回帰なし。
- 一方、**通常 Console 経路（POST 直後に attach）で別系統の初回描画レースを新規に発見した**：viewer attach 時の
  `Page.startScreencast` が、対象ページが about:blank → ナビゲート先へコミットする瞬間に当たると Chromium が
  `-32000 Not attached to an active page` を返し、Agent が Page 全体を `crashed`（WS close `"screencast failed"`）に
  してしまう。速い target への低遅延 attach（localhost・同一ホスト Console）で 0 frame のまま再現する。これは
  前回レポート §4.2 の「実 Agent WS の初回描画 FAIL（ready 後 JPEG 0 枚→crashed）」の実体でもある。
- 本再検証で `startScreencast` にこの transient のみを対象とした短時間リトライを実装し、`predelay=0`（即時 attach）
  25/25 で crash 0・~11.8fps 継続を確認した。ただし**この追加修正は完成イメージにはまだ入っていない**ため、
  完成イメージそのものの通常経路は条件付き（attach 遅延が短いと初回描画が落ち得る）である。

したがって、backpressure 修正は目的を達成しており「不合格の主因」は除去された。最終 V1 サインオフには、
本ブランチの `startScreencast` リトライを焼き込んだイメージ再ビルドと、その再ビルド版での §4 マトリクス再走を条件とする。

## 2. 対象と方法

- 実コンテナ: container ID `15592d470c28`（前回の `e87dfc1f9738` から更新＝完成イメージ再ビルドが反映済み）
- architecture: `x86_64` / runtime user: `dev(1000)` / memory cgroup limit: 10 GiB / `nproc`: 8
- Chromium: `150.0.7871.124-1~deb12u1`（`Chromium 150.0.7871.124 built on Debian GNU/Linux 12 (bookworm)`）
- 焼き込み Agent: `/usr/local/bin/workspace-agent`（sha256 `f64e44e2…`、18,979,487 bytes、build 2026-07-19 01:04:38、
  fix シンボル `restartScreencastForResize`/`offerScreencastFrame` 在中）
- versions.json: `claude 2.1.212 / opencode 1.18.3 / codex 0.144.5 / go 1.26.4 / gh 2.96.0 / chromium 150.0.7871.124-1~deb12u1`

計測手法（前回の「隔離 live helper」を製品経路寄りに置き換え）:

1. **稼働中の焼き込み Agent（`127.0.0.1:7700`、`AGENT_TOKEN` 認証）の REST/WS をそのまま駆動**して、
   Console と同じ「POST `/browser/pages` → WS `/ws/browser` attach → `viewport` → `visibility=true`」で JPEG を受ける
   Go ハーネス（`bverify`）で機能・レースを確認。fixture（静的・aggressive animation・tall scroll・navigation・
   実 Vite 6.4.3 HMR）はハーネス内 loopback HTTP server が提供。
2. **追加修正の検証は、同一ソースからビルドした Agent を別ポート（`:7799`）で起動**して駆動。焼き込みバイナリと
   同一の製品コード（`workspaceBrowserManager` グローバル・実ハンドラ・実 Chromium）を通すため、再ビルド後イメージの
   忠実な先取りになる。焼き込みバイナリ（`:7700`）には追加修正が無いため、通常経路の crash 再現はこちらで採取した。
3. cgroup 採取: `memory.current` / `memory.events` / `cpu.stat(usage_usec)` を 1 秒間隔。goroutine の代理として
   Agent プロセスの `/proc/<pid>/status: Threads` を採取。値はコンテナ全体（他 fleet セッション・LLM を含む）で
   Chromium 単体ではない。

`Page.startScreencast` の実エラー特定には、同一ソースの Agent に一時診断ログを入れて `-32000 Not attached to an
active page` を直接観測した（診断ログは成果物へ残していない）。

### 2.1 image digest — 取得不能（環境制約・前回同）

このコンテナには Docker CLI / `docker.sock` が公開されておらず（`command -v docker`=none、`/var/run/docker.sock`=absent）、
コンテナ内から `docker inspect` で `.Image`/`RepoDigests` を取得できない。**実行イメージ digest は今回も取得不能**。
代替として次を固定記録した：container ID `15592d470c28`（前回から更新）、焼き込み `workspace-agent` の sha256
`f64e44e2…`・build 時刻（merge 後）、`chromium` の Debian revision、`versions.json` の全ピン。digest 固定は
Docker host 側で `docker inspect --format '{{.Image}}' 15592d470c28` を実行して追記する必要がある。

## 3. Sandbox smoke（回帰なし・PASS）

`e2e-smoke.sh --inner` を完成コンテナ内で、`versions.json` のピンを期待値として実行。全項目 PASS（`== smoke OK ==`）。

| 項目 | 結果 | 証拠 |
|---|---|---|
| 各 CLI/toolchain 版ピン | PASS | claude 2.1.212 / opencode 1.18.3 / codex 0.144.5 / go 1.26.4 / gh 2.96.0 / rtk 0.43.0 |
| Chromium 版（Debian rev 含む） | PASS | `150.0.7871.124-1~deb12u1` 一致 |
| runtime user | PASS | `dev(1000)` |
| setuid sandbox | PASS | `/usr/lib/chromium/chrome-sandbox` = `0:0:4755` |
| `NoNewPrivs` | PASS | `NoNewPrivs=0`（setuid sandbox 利用可） |
| `SYS_ADMIN` eff/bnd | PASS | effective なし・bounding にあり |
| 余分な setuid/setgid | PASS | Chromium helper のみ |
| 日本語描画/font | PASS | headless screenshot・`Noto Sans CJK JP` |
| pipe CDP・2 Page 同時（image smoke） | PASS | sandboxed pipe CDP・2 Page・capture interval ≥ 83.333ms |
| smoke 総合 | **PASS** | `== smoke OK ==` |

## 4. 機能シナリオと長時間計測

計測は §2 の追加修正入り Agent（`:7799`）で、**通常 Console 経路（`predelay=0` の即時 attach、`vdelay=0`）**で採取。
定常描画の帯域・資源値は焼き込みイメージ（backpressure 修正済み）と同一の streaming 状態に到達したもので、
追加修正は attach 起動時のみに効くため定常値には影響しない。

| シナリオ | viewport | 時間 | crash | fps/Page | JPEG payload | mem Δpeak(コンテナ全体) | CPU(1core) | Agent Threads | OOM |
|---|---|---:|---|---:|---:|---:|---:|---|---:|
| 静的 | 1200×800 | 65s | なし | 0（初回1枚描画 `frames_ever=1`） | — | +43.4 MiB | 26.0% | 15→15→15 | 0 |
| aggressive animation | 1200×800 | 65s | なし | 11.89 | 909,495 B/s（avg 76.5 KB/f） | +79.2 MiB | 164.4% | 15→15→15 | 0 |
| wheel（tall scroll） | 1200×800 | 65s | なし | 2.40（0.5s 毎 scroll の repaint） | 48,907 B/s | +78.8 MiB | 62.2% | 15→15→15 | 0 |
| navigation/reload | 1200×800 | 65s | なし | 10.05（`ready↔loading` 循環） | 101,329 B/s | +82.1 MiB | 89.4% | 15→15→15 | 0 |
| Vite HMR（実 Vite 6.4.3） | 1200×800 | 65s | なし | 0.26（HMR 17 回＝17 frame） | 2,170 B/s | +111.5 MiB | 50.1% | 15→15→15 | 0 |
| **最大 animation** | **1600×1200** | 65s | なし | **11.89** | 1,006,892 B/s（avg 84.7 KB/f） | +144.7 MiB | 255.1% | 15→15→15 | 0 |
| **2 Page 同時 animation** | 1200×800×2 | **305s** | なし | **11.82** | 1,811,420 B/s（両 Page 合計） | +267.2 MiB | 280.6% | **15→15→15** | 0 |

- **前回 FAIL/取得不能だった animation・2 Page・最大 viewport がすべて継続描画に到達**。1600×1200 は ~12fps・1.0 MB/s、
  2 Page 5 分は各 11.82fps・両 Page `ready` 維持。
- **Agent Threads は全シナリオで start=peak=end=15**（2 Page 5 分でも増加なし）。前回「判定不能」だった
  「2 Page で goroutine/queue/memory が単調増加しない」を、goroutine の代理（Threads）と cgroup memory で確認。
  2 Page の `memory.current` は peak 1028.9 MiB → end 950.4 MiB と peak 後に低下し、単調増加ではない。
- 全シナリオで `memory.events` の `oom/oom_kill/high/max` 増分は 0。
- 静的ページは repaint が無いため定常 fps=0 が正しい（初回 1 枚は届いており空描画ではない。§5.3）。
- Vite HMR は実 Vite dev server の HMR WebSocket 更新 17 回がそのまま frame として届いた（crash なし）。

### 4.1 その他の機能シナリオ（回帰なし・PASS）

| シナリオ | 結果 | 証拠 |
|---|---|---|
| Chromium crash 復帰 | PASS | animation Page 描画中に全 Chromium を SIGKILL → Page `ready→crashed`・GET 404、新規 Page 作成で Chromium 再起動・frame 再開 |
| hidden で screencast 停止 | PASS | visible 3s=35 frame → `visibility=false` 後 5s=0 frame |
| idle 回収（Chromium＋profile） | PASS | DELETE→204・GET→404、idle(120s) 後に `chromium` プロセス 10→0・`/tmp/agent-fleet-chromium-*` 1→0 |
| 日本語 IME 入力（go integration） | PASS | `Input.insertText` 往復で `日本語`（`TestBrowserChromiumIntegration`） |

## 5. 新規発見: 初回 attach 時の `startScreencast` レース（完成イメージに残存）

### 5.1 再現

1. Chromium 150 を sandbox 有効・pipe CDP で起動、1200×800 の Page を作成。
2. Console と同じく **POST が返った直後に** WS を attach（`controller.ts:200-211,224-231` は `await createPage()` 解決後に
   同期で `connect()`→`socket.onopen` で `viewport`＋`visibility` を送る。ready 待ちをしない）。
3. WS は `ready` を受けるが JPEG は 0 枚、直後に `state=crashed`（WS close code 1001 `reason="screencast failed"`）。

`predelay`（POST 解決から WS dial までの遅延）を掃引すると挙動が二値に割れる：

| predelay | 結果（anim 1200×800） |
|---:|---|
| 0 ms | **crash・0 frame** |
| 30 ms | OK・11.83 fps |
| 60–400 ms | OK・~11.5–11.8 fps |

warm Chromium での「POST 解決 → `state=ready`」は約 21ms。速い target ではコミット窓が ~20–30ms しかなく、
localhost/同一ホストの低遅延 attach がその窓に嵌まる。逆に target が遅い場合（fixture 応答を 300/800/1500ms 遅延、
`predelay=100ms`）は about:blank が active のまま `startScreencast` が通り、後段のコミットも cast が生き残るため
crash しない（11.4/10.5/9.5 fps）。**速い target × 低遅延 attach の同時成立時のみ**発火する狭いレースである。

### 5.2 真因と修正

- Page target は `about:blank` で作られ、`Page.navigate` でナビゲート先へスワップする。attach 時の
  `Page.startScreencast` がこのコミット遷移の瞬間に当たると Chromium が `-32000 Not attached to an active page` を返す。
- ハンドラ（`browser_handlers.go`）は attach 時に無条件で `startScreencast` を呼び、失敗を即 `crashed`＋WS close に
  変換する。ライブフレームは数十 ms で来るため、この 1 回の失敗で Page 全体を落とすのは過剰である。
- これは backpressure 修正で新規混入したものではなく、修正前から潜在していた（前回 §4.2「初回描画 FAIL」の実体）。
  backpressure crash が先に起きなくなった分、こちらが前面化した。

**修正（本ブランチ `workspace/agent/browser_manager.go`）**: `startScreencast` を、`Not attached to an active page`
の transient に限り最大 12 回・各 40ms（最大 ~480ms）リトライするよう変更。それ以外のエラーと Page 消失は即返す。
リトライは attach 起動時のみで定常描画には無影響。回帰テスト `TestBrowserScreencastRetriesFrameNotActive`（fake CDP で
3 回失敗→成功、非 transient は即返し）を追加。実 Chromium で `predelay=0` の即時 attach 25/25 crash 0・~11.8fps を確認。

### 5.3 検討したが実在しなかった件（記録）

「静的ページは初回 frame が破棄されて空描画になる」と一度疑い、setVisible での再 arm を試作したが、追加修正なしでも
`frames_ever=1` が `vdelay=0`（Console と同じ即時送出）6/6 で安定して届き、**空描画は再現しなかった**（初回 frame は
`visible=true` 確定後に到達する）。同修正は不要かつ backpressure 修正が避けた screencast churn を戻すため撤回した。
本ブランチには含まれない。

## 6. 資源計測

- byte→MiB は 1 MiB=1,048,576 B。`cpu.stat` はコンテナ全体差分（1 core 比 %）。値は他 fleet セッション/LLM を含む
  コンテナ全体で Chromium 単体 RSS ではない。無ページ baseline: `memory.current` ≈ 704–761 MiB、`memory.events` 全 0。
- 定常帯域は §4 表の JPEG payload（application 層）。CP relay は byte 保存で中継する設計だが、**Agent→CP / CP→Console の
  segment 別 wire byte は本書でも未計測**（隔離 Agent WS 直結のため。§8）。
- `memory.current` は 2 Page 5 分で peak 1028.9 → end 950.4 MiB と peak 後低下（単調増加なし）。全区間 OOM 0。

## 7. 合否基準（前回表と同形式で再評価）

| 基準 | 前回 | 今回 | 根拠 |
|---|---|---|---|
| sandbox smoke 全条件必須 | PASS | **PASS** | `== smoke OK ==` 全項目 |
| 各 Page capture 12fps 以下かつ表示継続 | FAIL | **PASS** | anim/最大 viewport/2 Page/nav/wheel/HMR すべて継続、~12fps 上限内 |
| 2 Page で goroutine/queue/memory 単調増加なし | 判定不能 | **PASS** | 5 分・Threads 15 固定・memory peak 後低下・OOM 0 |
| `memory.events` に新規 OOM なし | PASS | **PASS** | 全区間 0 |
| hidden 後に screencast 停止 | PASS | **PASS** | 35→0 frame |
| Page 削除・idle 後に Chromium/profile なし | PASS | **PASS** | chromium 10→0・profile 1→0・GET 404 |
| 帯域・メモリ実測値を記録 | 一部 | **PASS** | 全シナリオ採取（§4/§6） |
| **通常 Console 経路（即時 attach）で初回描画継続** | （FAIL を backpressure に帰属） | **要修正（完成イメージ）／修正後 PASS** | 完成イメージは §5 レース残存。本ブランチ修正入り Agent で即時 attach 25/25 crash 0 |
| Chromium crash 復帰 | PASS | **PASS** | §4.1 |
| image digest 記録 | FAIL(環境) | **未充足（環境制約）** | Docker daemon 非公開（§2.1）。代替固定値は記録 |
| Stop→Start port/path 復元 | 未実施 | **一部（環境制約）** | 実 Stop→Start は不可。Agent の restart で無状態＝再作成可を確認・Console 復元経路をコード確認（§8） |

**最終判定: 条件付き合格**。前回不合格の主因（screencast backpressure crash）は解消し、受入シナリオはすべて継続描画に
到達した。残る唯一の描画系ブロッカーは §5 の初回 attach レースで、これは本ブランチで修正済み・検証済みだが完成イメージには
未反映。**本修正を焼き込んだ再ビルド版で §4 マトリクスを再走し、初回 attach レースの消失を確認すれば V1 は合格**となる。

## 8. 残課題・環境制約

1. **image digest 固定（未充足・環境制約）**: Docker CLI/socket 非公開のため digest 取得不能。Docker host 側で
   `docker inspect` 実行が必要。container ID/binary sha256/版ピンは記録済み。
2. **可観測化（未実装・課題）**: Agent に per-Page の capture/decode/send queue 深さ・drop 数・ACK 数・goroutine 数を
   公開する metric（expvar/pprof/prometheus/`/debug`）は無い。本書は代理として Agent プロセス Threads と cgroup を採取した。
   運用診断のため Page 単位カウンタの公開を推奨。
3. **segment 別帯域（未計測）**: Agent→CP / CP→Console の実 wire byte は未計測（隔離 Agent WS 直結のため）。
   CP/Console 双方に byte counter を置いての再計測が必要。
4. **実 Workspace Stop→Start（不可・環境制約）**: コンテナ内から自コンテナを停止するとセッションが終了し、Docker/CP 管理
   経路も非公開。代替として Agent プロセスの restart で「無状態（旧 Page は 404）＋同一 port/path からの再作成 201」を確認し、
   Console 側の layout からの復元（`registry.ensure`→`start`→`createPage(target)`、`wireBrowserReconcile`）をコードで確認した。
5. **§5 の初回 attach レース修正の焼き込み**: 本ブランチ `browser_manager.go` の `startScreencast` リトライを完成イメージへ
   再ビルド反映し、§4 マトリクスと 25/25 の即時 attach ストレスを再走すること（V1 サインオフの条件）。
