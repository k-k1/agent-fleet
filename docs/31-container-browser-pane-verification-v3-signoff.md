# コンテナ内ブラウザペイン V1 サインオフ実測レポート v3（`5690b15` 焼き込みイメージ再走）

> 実施日: 2026-07-19
> 対象設計: [31-container-browser-pane.md](31-container-browser-pane.md)
> 対象ADR: [decisions/0018-container-browser-pane.md](decisions/0018-container-browser-pane.md)
> 前回（修正前・不合格）: `feature/browser-pane-v1-container-verify:docs/31-container-browser-pane-verification.md`
> 前回（backpressure 修正後・条件付き合格）: [31-container-browser-pane-verification-v2.md](31-container-browser-pane-verification-v2.md)（`verify/browser-pane-v1-recheck:37b46a8`）
> 対象イメージ: `5690b15`（＝`382dfe7` backpressure 修正 ＋ `37b46a8` 初回 attach レース修正を統合）を焼き込んだ完成イメージ
> 最終判定: **合格 — V1 サインオフ完了**

## 1. 結論

v2 の唯一の残ブロッカー（§5 の初回 attach `Page.startScreencast` レース）を修正した `5690b15` を
**焼き込み直した完成イメージ**（container `91cd25172070`、`workspace-agent` build 2026-07-19 09:15:53）で
V1 マトリクスと即時 attach ストレスを再走した。結果、**新規バグはなく、v2 で条件付きだった項目がすべて
PASS に転じた**。V1 をサインオフ可能と判定する。

- **焼き込みバイナリが本当に修正を含むことを静的に確証**した。baked `workspace-agent` の
  `main.(*browserPage).startScreencast` の命令列は、本ワークツリー（HEAD `5690b15`）から
  `go build` した同関数と**ニーモニック列が 293 命令すべて一致（差分 0）**・CALL 数も 28 で一致。
  build 時刻 09:15:53 は commit 時刻 07:44:12 より後（§2.2）。
- **前回 P0（初回 attach レース）は解消**。Console と同じ「POST 直後・ready 待ちなしの WS attach」
  （`predelay=0`）を **30 回連続で 0 crash**、全回で JPEG フレーム到達（avg ~10.8fps）。v2 の
  完成イメージ（修正前）は `predelay=0` で確実に crash・0 frame だった項目が、本イメージでは消滅した。
  `predelay` 掃引 0/30/60/100/200/400ms も**全点 OK**（v2 は 0ms のみ crash）。
- **前回 P0（screencast backpressure crash）も継続して解消**。1200×800 aggressive animation、
  navigation、wheel、Vite 相当 HMR、最大 1600×1200、2 Page 同時（5 分）のいずれも
  `reason=screencast backpressure` の crash を起こさず ~12fps 上限で継続描画。
- sandbox smoke（`e2e-smoke.sh --inner`）は全項目 PASS（`== smoke OK ==`）。機能シナリオ
  （Chromium crash 復帰・hidden 停止・idle 回収・DELETE 404）も回帰なし。
- 全区間で `memory.events` の `oom/oom_kill/high/max` 増分は 0。Agent プロセス Threads は
  全シナリオ・5 分連続でも 18 固定（goroutine 単調増加なし）。

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
3. cgroup 採取: `memory.current` / `memory.events` / `cpu.stat(usage_usec)` を 1 秒間隔。goroutine の代理として
   Agent プロセス（pid 7）の `/proc/<pid>/status: Threads` を採取。値はコンテナ全体（他 fleet セッション・
   LLM を含む）で Chromium 単体ではない。

### 2.1 image digest — 取得不能（環境制約・前回同）

このコンテナには Docker CLI / `docker.sock` が非公開（`command -v docker`=none、`/var/run/docker.sock`・
`/run/docker.sock`=absent）で、コンテナ内から `docker inspect` の `.Image`/`RepoDigests` を取得できない。
**実行イメージ digest は今回も取得不能**。代替として次を固定記録した：container ID `91cd25172070`（更新済み）、
焼き込み `workspace-agent` の sha256 `f0c86d702a590940e28e8122…`・build 時刻（commit 後）、`chromium` の
Debian revision、`versions.json` の全ピン。digest 固定は Docker host 側で
`docker inspect --format '{{.Image}}' 91cd25172070` を実行して追記する必要がある。

### 2.2 焼き込みバイナリ＝`5690b15` の静的確証（今回追加）

digest が取れない代替として、焼き込みバイナリが本当に修正版であることを命令列レベルで確証した。

| 確認 | 結果 |
|---|---|
| build 時刻 vs commit 時刻 | build `09:15:53` > commit `5690b15`=`07:44:12`（＝マージ後ビルド） |
| `startScreencast` リトライ文字列 | baked に `Not attached to an active page` 在中 |
| `startScreencast` ニーモニック列（baked vs `5690b15` から `go build`） | **293 命令すべて一致・差分 0** |
| `startScreencast` の CALL 命令数 | baked 28 / from-src 28（一致） |

`go tool objdump -s 'main\.\(\*browserPage\)\.startScreencast$'` の出力からアドレスを除いたニーモニック列が
完全一致したことで、焼き込みバイナリの当該関数は `5690b15` のソースと同一と確定した（リトライ本体
= transient `Not attached to an active page` に限り最大 12 回×40ms）。

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
- **Agent Threads は全シナリオで start=peak=end=18**（2 Page 5 分でも増加なし）。「2 Page で
  goroutine/queue/memory が単調増加しない」を goroutine 代理（Threads）と cgroup memory で確認。
  2 Page の `memory.current` は peak 1081.3 MiB → end 1057.8 MiB と peak 後に低下し、単調増加ではない。
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
| 2 Page で goroutine/queue/memory 単調増加なし | 判定不能 | PASS | **PASS** | 5 分・Threads 18 固定・memory peak 後低下・OOM 0 |
| `memory.events` に新規 OOM なし | PASS | PASS | **PASS** | 全区間 0 |
| hidden 後に screencast 停止 | PASS | PASS | **PASS** | 35→増分 0 |
| Page 削除・idle 後に Chromium/profile なし | PASS | PASS | **PASS** | chromium 8→0・profile 1→0（t+135s） |
| 帯域・メモリ実測値を記録 | 一部 | PASS | **PASS** | 全シナリオ採取（§4/§5） |
| **通常 Console 経路（即時 attach）で初回描画継続** | FAIL | 要修正（完成イメージ）／修正後 PASS | **PASS** | 焼き込みイメージで即時 attach 30/30 crash 0・掃引全点 OK（§4.1） |
| Chromium crash 復帰 | PASS | PASS | **PASS** | §4.2 |
| image digest 記録 | FAIL(環境) | 未充足(環境) | **未充足（環境制約）** | Docker daemon 非公開（§2.1）。代替固定値＋命令列一致で焼き込み確証（§2.2） |
| Stop→Start port/path 復元 | 未実施 | 一部(環境) | **一部（環境制約）** | 実 Stop→Start は不可。idle 回収で無状態＝再作成可を確認・Console 復元経路はコード確認 |

**最終判定: 合格（V1 サインオフ完了）**。v2 で唯一残っていた描画系ブロッカー（初回 attach `startScreencast`
レース）は、`5690b15` を焼き込んだ完成イメージで**即時 attach 30/30 crash 0・predelay 掃引全点 OK**として
消失を実測した。backpressure crash も継続解消。sandbox smoke・機能シナリオ・長時間計測いずれも回帰なし。
**新規バグは発見されなかった。** 焼き込みバイナリが `5690b15` の修正を含むことは命令列一致で確証済み（§2.2）。
以上より **V1 をサインオフする**。

## 7. 残課題・環境制約（サインオフをブロックしない）

以下は v2 と同じ**環境制約に起因する未充足**であり、ブラウザペイン V1 の受入判定をブロックしない。運用向けの継続課題。

1. **image digest 固定（未充足・環境制約）**: Docker CLI/socket 非公開のため digest 取得不能。Docker host 側で
   `docker inspect` 実行が必要。container ID/binary sha256/版ピン＋命令列一致は記録済み（§2.1/§2.2）。
2. **可観測化（未実装・課題）**: Agent に per-Page の capture/decode/send queue 深さ・drop 数・ACK 数・goroutine 数を
   公開する metric（expvar/pprof/prometheus/`/debug`）は無い（routes に該当エンドポイントなし、GET レスポンスにも
   カウンタ無しを確認）。本書は代理として Agent プロセス Threads と cgroup を採取した。運用診断のため Page 単位
   カウンタの公開を推奨。
3. **segment 別帯域（未計測）**: Agent→CP / CP→Console の実 wire byte は未計測（隔離 Agent WS 直結のため）。
   CP/Console 双方に byte counter を置いての再計測が必要。
4. **実 Workspace Stop→Start（不可・環境制約）**: コンテナ内から自コンテナを停止するとセッションが終了し、Docker/CP 管理
   経路も非公開。代替として idle 回収（無状態化・§4.2）＋Console 側 layout 復元経路（`registry.ensure`→`start`→
   `createPage(target)`、`wireBrowserReconcile`）のコード確認で担保。
