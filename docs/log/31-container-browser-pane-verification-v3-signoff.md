# コンテナ内ブラウザペイン V1 サインオフ実測レポート v3（`b892d37` 焼き込みイメージ再走）

> 実施日: 2026-07-19
> 対象設計: [31-container-browser-pane.md](31-container-browser-pane.md)
> 対象ADR: [decisions/0018-container-browser-pane.md](../decisions/0018-container-browser-pane.md)
> 前回（修正前・不合格）: `feature/browser-pane-v1-container-verify:docs/31-container-browser-pane-verification.md`
> 前回（backpressure 修正後・条件付き合格）: [31-container-browser-pane-verification-v2.md](31-container-browser-pane-verification-v2.md)（`verify/browser-pane-v1-recheck:88986ce`）
> 対象イメージ: `b892d37`（＝`b7ff65d` backpressure 修正 ＋ `88986ce` 初回 attach レース修正を統合）を焼き込んだ完成イメージ
> 最終判定: **合格 — V1 サインオフ完了**

## 1. 結論

v2 の唯一の残ブロッカー（§5 の初回 attach `Page.startScreencast` レース）を修正した `b892d37` を
**焼き込み直した完成イメージ**（container `91cd25172070`、`workspace-agent` build 2026-07-19 09:15:53）で
V1 マトリクスと即時 attach ストレスを再走した。結果、**新規バグはなく、v2 で条件付きだった項目がすべて
PASS に転じた**。V1 をサインオフ可能と判定する。

- **焼き込みバイナリが対象修正を含むことを高い確度で確認**した。baked `workspace-agent` の
  `main.(*browserPage).startScreencast` は、リトライ用エラー文字列 `Not attached to an active page` を
  持ち、本ワークツリー（HEAD `b892d37`）から `go build` した同関数と**ニーモニック列（293 命令）と
  CALL 数（28）が一致**、build 時刻 09:15:53 も commit 時刻 07:44:12 より後。これらの静的一致に加え、
  即時 attach 30/30 の実挙動（§4.1）と合わせて修正の焼き込みを確認した。なお**ニーモニック比較は
  オペランド・即値・分岐先・定数を照合しないため、image digest の代替となる完全な同一性証明ではない**（§2.2）。
- **前回 P0（初回 attach レース）は解消**。Console と同じ「POST 直後・ready 待ちなしの WS attach」
  （`predelay=0`）を **30 回連続で 0 crash**、全回で JPEG フレーム到達（avg ~10.8fps）。v2 の
  完成イメージ（修正前）は `predelay=0` で確実に crash・0 frame だった項目が、本イメージでは消滅した。
  `predelay` 掃引 0/30/60/100/200/400ms も**全点 OK**（v2 は 0ms のみ crash）。
- **前回 P0（screencast backpressure crash）も継続して解消**。1200×800 aggressive animation、
  navigation、wheel、Vite 相当 HMR、最大 1600×1200、2 Page 同時（5 分）のいずれも
  `reason=screencast backpressure` の crash を起こさず ~12fps 上限で継続描画。
- sandbox smoke（`e2e-smoke.sh --inner`）は全項目 PASS（`== smoke OK ==`）。機能シナリオ
  （Chromium crash 復帰・hidden 停止・idle 回収・DELETE 404）も回帰なし。
- 全区間で `memory.events` の `oom/oom_kill/high/max` 増分は 0。cgroup memory・Agent プロセスの
  OS Threads（全シナリオ・5 分連続で 18 固定）とも単調悪化なし。ただし OS Threads は Go ランタイムの
  M であり goroutine 数の代理にはならない（多数の goroutine が少数 thread 上で動く）。goroutine 数・
  Page 単位 queue 深さは本書では未計測で、実装上の固定上限（latest-only buffer・ACK pace）と既存 race
  テストの確認にとどまる（§4／§7-2）。

## 2. 対象と方法

- 実コンテナ: container ID `91cd25172070`（v2 の `15592d470c28` から更新＝完成イメージ再ビルドが反映済み）
- architecture: `x86_64` / runtime user: `dev(1000)` / memory cgroup limit: 10 GiB / `nproc`: 8
- Chromium: `150.0.7871.124-1~deb12u1`（`Chromium 150.0.7871.124 built on Debian GNU/Linux 12 (bookworm)`）
- 焼き込み Agent: `/usr/local/bin/workspace-agent`（sha256 `f0c86d702a590940e28e8122…`、18,979,581 bytes、
  build 2026-07-19 09:15:53、`startScreencast` リトライ在中＝§2.2）
- versions.json: `claude 2.1.212 / opencode 1.18.3 / codex 0.144.5 / go 1.26.4 / gh 2.96.0 / chromium 150.0.7871.124-1~deb12u1`
- browser 設定（既定）: `AF_BROWSER_PAGE_LIMIT=2`、`FrameInterval=1/12s`（=12fps 上限）、
  `JPEGQuality=70`、`AF_BROWSER_IDLE_SEC=120`、`AF_BROWSER_DETACHED_GRACE_SEC=60`

計測手法（v2 と同一の「製品経路寄り」）:

1. **稼働中の焼き込み Agent（`127.0.0.1:7700`、`AGENT_TOKEN` 認証）の REST/WS をそのまま駆動**した。
   これは本セッションのコンテナで常駐している**シップ済みプロセス（pid 7 の `/usr/local/bin/workspace-agent`）
   そのもの**であり、追加ビルドや別ポート起動を挟まない最も忠実な対象。v2 で必要だった `:7799` の
   同一ソース別ビルドは、**修正が焼き込み済みのため今回は不要**になった。
2. Console と同じ手順で駆動する Go ハーネス `bverify`（成果物には未コミット・使い捨て）を用いた：
   `POST /browser/pages` → `WS /ws/browser?id=…` を **POST 解決直後に dial**（`predelay=0`）→
   `viewport`（作成時と同寸＝restart 抑止）＋`visibility=true` を送出 → JPEG バイナリフレームを計数。
   fixture（静的・aggressive animation・tall scroll・navigation・HMR）はハーネス内 loopback HTTP が提供。
   HMR は fixture の WebSocket push で DOM を更新し「dev サーバの HMR 更新 → 単発 repaint → 単発 frame」を再現。
3. cgroup 採取: `memory.current` / `memory.events` / `cpu.stat(usage_usec)` を 1 秒間隔。goroutine 数は
   計測不能のため、別指標として Agent プロセス（pid 7）の `/proc/<pid>/status: Threads`（OS Threads）と
   cgroup memory を採取した。値はコンテナ全体（他 fleet セッション・LLM を含む）で Chromium 単体ではない。

### 2.1 image digest — 取得不能（環境制約・前回同）

このコンテナには Docker CLI / `docker.sock` が非公開（`command -v docker`=none、`/var/run/docker.sock`・
`/run/docker.sock`=absent）で、コンテナ内から `docker inspect` の `.Image`/`RepoDigests` を取得できない。
**実行イメージ digest は今回も取得不能**。代替として次を固定記録した：container ID `91cd25172070`（更新済み）、
焼き込み `workspace-agent` の sha256 `f0c86d702a590940e28e8122…`・build 時刻（commit 後）、`chromium` の
Debian revision、`versions.json` の全ピン。digest 固定は Docker host 側で
`docker inspect --format '{{.Image}}' 91cd25172070` を実行して追記する必要がある。

### 2.2 焼き込みバイナリが対象修正を含むことの確認（今回追加・digest 代替ではない）

image digest が取れないため、焼き込みバイナリが対象修正（`startScreencast` リトライ）を含むことを
複数の状況証拠で確認した。下表の一致に、即時 attach 30/30 の実挙動（§4.1）を合わせて判断している。

| 確認 | 結果 |
|---|---|
| build 時刻 vs commit 時刻 | build `09:15:53` > commit `b892d37`=`07:44:12`（＝マージ後ビルド） |
| `startScreencast` リトライ用エラー文字列 | baked に `Not attached to an active page` 在中 |
| `startScreencast` ニーモニック構造（baked vs `b892d37` から `go build`） | 293 命令すべて一致 |
| `startScreencast` の CALL 命令数 | baked 28 / from-src 28（一致） |

`go tool objdump -s 'main\.\(\*browserPage\)\.startScreencast$'` の出力からアドレスを除いたニーモニック列が
一致した（リトライ本体＝transient `Not attached to an active page` に限り最大 12 回×40ms）。

**限界（重要）**: このニーモニック比較は命令の並びと種類・CALL 数を照合するもので、**オペランド・即値
（例: 12 や 40ms のリテラル）・分岐先・定数を比較しない**ため、バイナリの厳密な同一性証明にはならない。
したがって本節は「対象修正が焼き込まれていることの高確度の確認」であって、**取得できなかった image digest の
代替となる完全な同一性証明ではない**。確定的な固定は §7-1 のとおり Docker host 側の `docker inspect` が必要。

## 3. Sandbox smoke（回帰なし・PASS）

`e2e-smoke.sh --inner` を完成コンテナ内で実行（期待版は `workspace/Dockerfile` の ARG から parse＝outer と同じ源）。
全項目 PASS（`== smoke OK ==`）。

| 項目 | 結果 | 証拠 |
|---|---|---|
| 各 CLI/toolchain 版ピン | PASS | claude 2.1.212 / opencode 1.18.3 / codex 0.144.5 / go 1.26.4 / gh 2.96.0 / rtk 0.43.0 |
| versions.json 版ピン | PASS | Dockerfile ARG と全 6 キー一致 |
| Chromium 版（Debian rev 含む） | PASS | `150.0.7871.124-1~deb12u1` 一致 |
| runtime user | PASS | `dev(1000)` |
| setuid sandbox | PASS | `/usr/lib/chromium/chrome-sandbox` = `0:0:4755` |
| `NoNewPrivs` | PASS | `NoNewPrivs=0`（setuid sandbox 利用可） |
| `SYS_ADMIN` eff/bnd | PASS | effective なし・bounding にあり |
| 余分な setuid/setgid | PASS | Chromium helper のみ |
| `DISABLE_AUTOUPDATER` | PASS | コンテナ env 実測 `=1` |
| 日本語描画/font | PASS | headless screenshot PNG・`Noto Sans CJK JP` |
| pipe CDP・2 Page 同時（image smoke） | PASS | sandboxed pipe CDP・2 Page・capture interval ≥ 83.333ms |
| smoke 総合 | **PASS** | `== smoke OK ==` |

## 4. 機能シナリオと長時間計測（焼き込み `:7700` を直接駆動）

すべて **Console 経路（`predelay=0` の即時 attach）** で採取。crash は全シナリオ **なし**。

| シナリオ | viewport | 時間 | crash | fps/Page | JPEG payload | mem Δpeak(コンテナ全体) | CPU(1core) | Agent Threads | OOM |
|---|---|---:|---|---:|---:|---:|---:|---|---:|
| 静的 | 1200×800 | 65s | なし | 0.02（初回1枚描画） | 166 B/s（10.6 KB/f） | +21.2 MiB | 45.1% | 18→18→18 | 0 |
| aggressive animation | 1200×800 | 65s | なし | 11.80 | 1,471,615 B/s（avg 121.8 KB/f） | +96.2 MiB | 259.0% | 18→18→18 | 0 |
| wheel（tall scroll） | 1200×800 | 65s | なし | 3.00（0.5s 毎 scroll） | 118,008 B/s | +67.3 MiB | 63.7% | 18→18→18 | 0 |
| navigation（A↔B 3s 毎） | 1200×800 | 65s | なし | 0.34 | 3,202 B/s | +29.1 MiB | 42.4% | 18→18→18 | 0 |
| HMR（WS push 更新） | 1200×800 | 65s | なし | 0.28（18 更新＝18 frame） | 3,351 B/s | +26.9 MiB | 33.4% | 18→18→18 | 0 |
| **最大 animation** | **1600×1200** | 65s | なし | **11.77** | 2,273,441 B/s（avg 188.6 KB/f） | +113.0 MiB | 237.8% | 18→18→18 | 0 |
| **2 Page 同時 animation** | 1200×800×2 | **305s** | なし | **11.77** | 2,932,821 B/s（両 Page 合計） | +128.7 MiB | 277.6% | **18→18→18** | 0 |

- **全シナリオ継続描画・crash 0**。aggressive animation は 1200×800 / 1600×1200 とも ~11.8fps で
  12fps 上限内。2 Page 5 分は各 11.77fps・両 Page `ready` 維持。
- **Agent の OS Threads は全シナリオで start=peak=end=18**（2 Page 5 分でも増加なし）。cgroup memory も
  2 Page 5 分で peak 1081.3 MiB → end 1057.8 MiB と peak 後に低下し単調増加ではない。**OS Threads と
  cgroup memory の単調悪化なし**を確認した。ただし OS Threads は Go の M（thread）であって goroutine 数
  そのものではないため、**「goroutine が単調増加しない」ことの直接証明にはならない**。goroutine 数・
  Page 単位 queue 深さは未計測で、実装上の固定上限（`offerScreencastFrame` の latest-only 非ブロッキング
  buffer・FrameInterval 毎 1 ACK pace）と既存の `-race` テスト群で担保している（可観測化は §7-2 の課題）。
- 全シナリオで `memory.events` の `oom/oom_kill/high/max` 増分は 0。
- 静的ページは repaint が無いため定常 fps≈0 が正しい（初回 1 枚は届いており空描画ではない）。
- animation の payload が v2（~0.9–1.0 MB/s）より大きいのは、本 fixture の canvas が 1 frame あたり
  400 矩形を塗る**より重い描画**で JPEG が大きい（122–189 KB/f）ため。fps 上限・crash 挙動・資源単調性の
  評価には影響しない。

### 4.1 即時 attach ストレス（V1 サインオフの核心）と predelay 掃引

| 試験 | 条件 | 結果 |
|---|---|---|
| **即時 attach ストレス** | `predelay=0`・warm target・anim 1200×800・**30 連続** | **crash 0 / 30・全回フレーム到達・avg 10.84fps** |
| predelay 掃引 | anim 1200×800、各 2s | 0ms=OK 11.5fps / 30ms=11.5 / 60ms=11.0 / 100ms=11.5 / 200ms=11.5 / 400ms=11.5、**全点 crash なし** |

v2 の完成イメージ（修正前）は `predelay=0` で crash・0 frame、掃引でも 0ms のみ crash だった。本イメージでは
**0ms を含む全条件で crash が消滅**。§2.2 の命令列一致と合わせ、`startScreencast` リトライが焼き込みで効いていることを実挙動で確認。

### 4.2 その他の機能シナリオ（回帰なし・PASS）

| シナリオ | 結果 | 証拠 |
|---|---|---|
| Chromium crash 復帰 | PASS | anim 描画中（28 frame 到達）に全 Chromium を SIGKILL → Page `crashed`・GET 404、新規 Page 作成で Chromium 再起動・frame 再開（28 frame） |
| hidden で screencast 停止 | PASS | visible 3s=35 frame → `visibility=false` 後 5s の増分 0 |
| idle 回収（Chromium＋profile） | PASS | 最終 Page DELETE→204・GET→404、idle(120s) 経過後 `chromium` プロセス 8→0・`/tmp/agent-fleet-chromium-*` 1→0（t+135s で 0 を実測） |
| DELETE 後 404 | PASS | GET 200(loading) → DELETE → GET 404 |

## 5. 資源計測

- byte→MiB は 1 MiB=1,048,576 B。`cpu.stat` はコンテナ全体差分（1 core 比 %）。値は他 fleet セッション/LLM を含む
  コンテナ全体で Chromium 単体 RSS ではない。無ページ baseline: `memory.current` ≈ 704–795 MiB、`memory.events` 全 0。
- 定常帯域は §4 表の JPEG payload（application 層）。CP relay は byte 保存で中継する設計だが、**Agent→CP / CP→Console の
  segment 別 wire byte は本書でも未計測**（隔離 Agent WS 直結のため。§7）。
- `memory.current` は 2 Page 5 分で peak 1081.3 → end 1057.8 MiB と peak 後低下（単調増加なし）。全区間 OOM 0。
- セッション全区間を通して `memory.events` は `low/high/max/oom/oom_kill=0` を維持（cgroup 逼迫なし）。

## 6. 合否基準（v1/v2 と同形式で再評価）

| 基準 | v1 | v2 | v3（本書） | 根拠 |
|---|---|---|---|---|
| sandbox smoke 全条件必須 | PASS | PASS | **PASS** | `== smoke OK ==` 全項目 |
| 各 Page capture 12fps 以下かつ表示継続 | FAIL | PASS | **PASS** | anim/最大 viewport/2 Page/nav/wheel/HMR すべて継続、~12fps 上限内 |
| 2 Page で OS Threads・cgroup memory の単調悪化なし | 判定不能 | PASS | **PASS** | 5 分・OS Threads 18 固定・memory peak 後低下 |
| 2 Page で goroutine 数・queue 深さの単調増加なし | 判定不能 | （代理計測） | **未計測** | 直接計測手段なし（可観測化未実装・§7-2）。実装上の固定上限（latest-only buffer・ACK pace）と既存 `-race` テストを確認 |
| per-Page 可観測化（queue/drop/ACK/goroutine 公開） | 未実装 | 未実装 | **未実装（運用課題・非ブロッキング）** | expvar/pprof/metrics エンドポイントなし・GET 応答に counter なし（§7-2） |
| `memory.events` に新規 OOM なし | PASS | PASS | **PASS** | 全区間 0 |
| hidden 後に screencast 停止 | PASS | PASS | **PASS** | 35→増分 0 |
| Page 削除・idle 後に Chromium/profile なし | PASS | PASS | **PASS** | chromium 8→0・profile 1→0（t+135s） |
| 帯域・メモリ実測値を記録 | 一部 | PASS | **PASS** | 全シナリオ採取（§4/§5） |
| **通常 Console 経路（即時 attach）で初回描画継続** | FAIL | 要修正（完成イメージ）／修正後 PASS | **PASS** | 焼き込みイメージで即時 attach 30/30 crash 0・掃引全点 OK（§4.1） |
| Chromium crash 復帰 | PASS | PASS | **PASS** | §4.2 |
| image digest 記録 | FAIL(環境) | 未充足(環境) | **未充足（環境制約）** | Docker daemon 非公開（§2.1）。代替として固定値記録＋対象修正の焼き込みを高確度で確認（§2.2、完全な同一性証明ではない） |
| Stop→Start port/path 復元 | 未実施 | 一部(環境) | **一部（環境制約）** | 実 Stop→Start は不可。idle 回収で無状態＝再作成可を確認・Console 復元経路はコード確認 |

**最終判定: 合格（V1 サインオフ完了）**。v2 で唯一残っていた描画系ブロッカー（初回 attach `startScreencast`
レース）は、`b892d37` を焼き込んだ完成イメージで**即時 attach 30/30 crash 0・predelay 掃引全点 OK**として
消失を実測した。backpressure crash も継続解消。sandbox smoke・機能シナリオ・長時間計測いずれも回帰なし。
**新規バグは発見されなかった。** 焼き込みバイナリが対象修正を含むことは、静的一致（§2.2）と即時 attach 30/30 の
実挙動（§4.1）から高確度で確認した（image digest による完全な同一性固定は環境制約で未充足・§7-1）。
以上より **V1 をサインオフする**。

## 7. 残課題・環境制約（サインオフをブロックしない）

以下は v2 と同じ**環境制約に起因する未充足**であり、ブラウザペイン V1 の受入判定をブロックしない。運用向けの継続課題。

1. **image digest 固定（未充足・環境制約）**: Docker CLI/socket 非公開のため digest 取得不能。Docker host 側で
   `docker inspect` 実行が必要。代替として container ID/binary sha256/版ピンを記録し、対象修正の焼き込みは
   §2.2 で高確度に確認したが、これは完全な同一性証明ではない（digest の確定は上記 `docker inspect` を要する）。
2. **可観測化（未実装・課題）**: Agent に per-Page の capture/decode/send queue 深さ・drop 数・ACK 数・goroutine 数を
   公開する metric（expvar/pprof/prometheus/`/debug`）は無い（routes に該当エンドポイントなし、GET レスポンスにも
   カウンタ無しを確認）。goroutine 数は計測不能のため、本書は別指標として Agent プロセスの OS Threads と
   cgroup memory を採取したにとどまる。運用診断のため Page 単位カウンタの公開を推奨。
3. **segment 別帯域（未計測）**: Agent→CP / CP→Console の実 wire byte は未計測（隔離 Agent WS 直結のため）。
   CP/Console 双方に byte counter を置いての再計測が必要。
4. **実 Workspace Stop→Start（不可・環境制約）**: コンテナ内から自コンテナを停止するとセッションが終了し、Docker/CP 管理
   経路も非公開。代替として idle 回収（無状態化・§4.2）＋Console 側 layout 復元経路（`registry.ensure`→`start`→
   `createPage(target)`、`wireBrowserReconcile`）のコード確認で担保。
