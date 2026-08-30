# コンテナ内ブラウザペイン V1 完成イメージ実測レポート

> 実施日: 2026-07-18〜2026-07-19
> 対象設計: [31-container-browser-pane.md](31-container-browser-pane.md)
> 対象ADR: [0018-container-browser-pane.md](../decisions/0018-container-browser-pane.md)
> 最終判定: **不合格（要修正、V1リリースブロッカー）**

## 1. 結論

完成Workspaceコンテナ内で `deploy/local/e2e-smoke.sh` の検証本体を実行し、sandbox、
非root実行、capability、製品binaryのpipe CDP、2 Page同時描画を含む全チェックが成功した。
静的HTML、日本語入力、mouse、Vite HMR/WebSocket、Spring Boot相当redirect/絶対path asset、
frontend `:3000` からAPI `:8080`、明示的なChromium crash後の再接続、hidden停止、Page削除後の
idle回収も、後述の切り分け条件では確認できた。

一方、通常のConsole接続で送る初期`viewport`、連続animation、navigation/reload等を契機に、
Workspace AgentがPageを `reason=screencast backpressure` で `crashed` にする実装バグを再現した。
実Agent WebSocketでは `ready` 後にJPEGが0枚のまま `crashed` になる場合があり、1200×800の
animation Pageと2 Page同時表示は継続計測へ到達しない。ブラウザペインの中心機能と受入条件を
満たせないため、sandbox smoke成功とは分けて全体を不合格とする。

本セッションでは製品修正を行っていない。修正後に、本書で取得不能としたanimation/2 Page/
最大viewportの1〜5分計測と帯域計測を再実行する必要がある。

## 2. 対象と方法

- 実コンテナ: container ID `e87dfc1f9738`、documented image tag `agent-fleet/workspace:dev`
- architecture: `x86_64`
- runtime user: `dev(1000)`
- Chromium package: `150.0.7871.124-1~deb12u1`
- Chromium表示: `Chromium 150.0.7871.124 built on Debian GNU/Linux 12 (bookworm)`
- memory cgroup limit: 10 GiB
- Agent: 完成イメージに焼き込まれた `/usr/local/bin/workspace-agent` と、同じコンテナ内で
  current main sourceから生成した隔離live helperを使用
- browser path: `--remote-debugging-pipe` の製品CDP adapter、Agent REST/WS、実Chromium
- fixture: 静的HTML、CSS animation、実Vite 6.4.3、redirect/絶対asset/API用loopback HTTP server
- cgroup採取: `memory.current`、`memory.events`、`cpu.stat`を1秒間隔で取得

このWorkspaceにはDocker CLI/socketが公開されていないため、コンテナ内からDocker daemonの
`.Image` / `RepoDigests` をinspectできず、**実行イメージdigestは取得不能**だった。tag、container ID、
package revision、焼き込みbinaryの実版は記録したが、digest要件は未充足である。Docker host側で
`docker inspect --format '{{.Image}}' e87dfc1f9738` と対応するimage inspectを実行して追記する必要がある。

資源値はコンテナ全体のcgroup値である。同時に動く通常のAgent/LLM sessionをbaselineに含むため、
Chromium単体RSSではない。別worktreeのbrowser live helper/Chromiumが一時的に存在した区間は破棄し、
それらが0になったことを確認した後の区間だけを採用した。

## 3. Sandbox smoke

`deploy/local/e2e-smoke.sh --inner` を完成コンテナ内で、DockerfileのARG pinを期待値として実行した。
Docker CLIがないためouterの新規 `docker run` は実行せず、現在稼働中の完成Workspaceコンテナに対して
inner本体を実行した。

| 項目 | 結果 | 証拠 |
|---|---|---|
| user | PASS | `dev(1000)` |
| package revision | PASS | `chromium=150.0.7871.124-1~deb12u1`、pin一致 |
| setuid helper | PASS | `/usr/lib/chromium/chrome-sandbox` = `root:root 4755` |
| `NoNewPrivs` | PASS | `/proc/self/status`: `NoNewPrivs: 0` |
| `SYS_ADMIN` effective | PASS | `CapEff=0000000000000000`、bit 21なし |
| `SYS_ADMIN` bounding | PASS | `CapBnd=00000000a82425fb`、bit 21あり |
| 余分なsetuid/setgid executable | PASS | helper以外なし |
| sandbox flag | PASS | 製品processに `--no-sandbox` なし |
| `/dev/shm`回避 | PASS | 製品processに `--disable-dev-shm-usage` あり |
| pipe CDP | PASS | `--remote-debugging-pipe`、raw debugging portなし |
| 日本語描画/font | PASS | PNG signature確認、`Noto Sans CJK JP` |
| 2 Page同時描画 | PASS | image smokeのanimation 2 Page、各Pageでcapture interval 83.333 ms以上 |
| smoke総合 | **PASS** | `== smoke OK ==` |

smokeはClaude/OpenCode/Codex/Go/ghの実版、`versions.json`、tmux、image内必須fileもすべて成功した。

## 4. 機能シナリオ

### 4.1 成功したシナリオ

| シナリオ | 結果 | 条件・証拠 |
|---|---|---|
| sandboxed pipe CDP 2 Page smoke | PASS | image smoke、800×600、短時間のmild animation |
| 静的HTML表示 | PASS（切り分け条件） | Page `ready` 待ち、初期viewport再送なしで60秒継続、JPEG 121枚 |
| 日本語IME相当入力 | PASS | `Input.insertText`で `input:日本語`、key追加で `input:日本語a` |
| key / mouse | PASS | key down/upとmouse down/upを対象Pageのconsole eventで確認 |
| wheel | PASS（切り分け条件） | 初期viewport再送なしのfresh managerでは対象Pageのwheel eventを確認 |
| Vite/React相当HMR・WebSocket | PASS | 実Vite 6.4.3、`vite:v1`→source更新→`vite:v2`を同一Pageで確認 |
| frontend `:3000` → API `:8080` | PASS | Vite Pageから `127.0.0.2:8080/api` fetch、`api:8080-ok`を確認 |
| Spring Boot相当redirect | PASS | `/spring` 302→`/app/`、最終URLをAgent GETで確認 |
| 絶対path asset | PASS | `/assets/app.css` fetch後の `spring:asset-ok` console event |
| Chromium crash復帰 | PASS | 所有leaderへSIGKILL→`crashed`→新PageでJPEG受信 |
| hidden screencast停止 | PASS | hidden前後のframe数 `3→3`、65秒区間0 fps |
| hidden 60秒後のPage破棄 | PASS | 65秒後のPage GETが404 |
| DELETE後idle回収 | PASS | 130秒後にChromium child 0、一時profile 0 |
| W2→W3→W4 live E2E | PASS | sandbox有効 `/usr/bin/chromium`、REST/relay/Console controller、日本語入力 |

成功欄の「切り分け条件」は、AgentのPageが`ready`になってからviewerをattachし、Consoleが通常送る
冗長な初期viewport messageを省いたものを指す。各機構が単独では動くことの確認であり、通常製品経路の
合格を意味しない。

### 4.2 失敗または未完了のシナリオ

| シナリオ | 結果 | 再現・阻害理由 |
|---|---|---|
| 実Agent WSの初回描画 | **FAIL** | `ready`後にbinary JPEG 0枚、直後にtext `state=crashed` |
| 1200×800 animation | **FAIL** | mild/aggressive双方で `screencast backpressure`、継続表示不能 |
| 2 Page同時animation | **FAIL** | 1 Page目が同理由でcrashし、60秒計測へ未到達 |
| 最大viewport 1600×1200 animation | **FAIL** | 初期JPEG 1枚（13,030 bytes）後、60秒区間0 frameで表示継続せず |
| 通常Console初期viewport | **FAIL / 主トリガー** | attach直後のstop/startで複数frameが重なりPage invalidation |
| wheel（通常初期化後） | **FAIL** | wheelによるpaint後に `crashed`。初期viewportなしではPASS |
| 戻る・進む・再読込 | **FAIL** | navigation中にPageがinvalidatedされ、GET 404 |
| target未listen→起動→再読込 | **FAIL** | 初期 `target-unreachable` は確認、reload後に同バグで `crashed` |
| hidden復帰 | 未評価 | hidden停止・60秒破棄はPASSだが、visible復帰は同バグの影響を受け得る |
| Workspace Stop→Start port/path復元 | 未実施 | 自コンテナをStopすると検証session自体が終了し、Docker/CP管理経路も非公開 |
| goroutine/queue単調増加 | 未評価 | animation/2 Pageが開始直後にcrashし、1〜5分の定常区間が成立しない |

## 5. 要修正: screencast backpressure crash

### 5.1 再現条件

1. Chromium 150をsandbox有効、pipe CDPで起動する。
2. 1200×800 Pageを作成し、Agent browser WebSocketへviewerをattachする。
3. Consoleと同様に接続直後のviewportを送る、または連続animation/navigation/scrollでpaintを発生させる。
4. WebSocketは `ready` を受信するが、JPEGを受信しないか、ごく少数受信した後に `state=crashed` となる。

一時診断ログを追加したfresh BrowserManagerで、次を取得した。診断ログ変更は成果物から除去した。

```text
TEMP browser verification: invalidate page=<id> state=crashed reason=screencast backpressure
```

### 5.2 真因

- Pageごとの `frameEvents` は容量1である。
- `Page.screencastFrame` eventのnon-blocking sendが満杯になると、Agentは
  `p.stopScreencast()` と `invalidatePage(..., "crashed", "screencast backpressure")` を実行する。
- `frameLoop` は1 eventを取り出した後、12fps用の83.333 ms timerが切れるまでACKを遅延する。
- 実装は「ChromiumはACKまで次frameを生成しない」と仮定しているが、実測したChromium 150は
  ACK待ち中にも複数のscreencast frameを送る場合がある。
- Console接続直後のviewport処理は既存screencastをstop/startする。旧castと新castのframeが近接し、
  容量1 queueをさらに飽和させやすい。
- animation、navigation、reload、wheel等の連続paintでも同じ飽和が起きる。

したがって、これは初回Chromium processの状態依存でも、CP WebSocket relayの欠落でもない。
Agent内のACK pacingと容量1 queueの組合せ、および複数frameを異常扱いする方針が原因である。

### 5.3 影響と優先度

**深刻度: Critical / 優先度: P0（V1リリースブロッカー）**。

- 通常Consoleが必ず送る初期viewportだけで発火し得る。
- animationという受入条件を常時満たせない。
- 1 Pageの失敗でviewerが閉じ、Page IDも404になる。
- 2 Page定常性、帯域、メモリ、最大viewportの合否判定そのものを阻害する。
- smokeは800×600、短時間、直接latest-frame slotを読む条件のため成功し、本番WS失敗を検出できない。

修正では、CDP readerを止めずに全screencast frameへ適切にACKしつつ、decode/send対象だけをlatest-onlyで
boundedに置換する必要がある。stop/start世代を識別して古いcast frameを安全に捨てること、同寸法viewportの
冗長なrestartを避けること、実Chromium 150で通常WS経路の長時間testを追加することも必要である。
具体的な修正は別セッションで行う。

## 6. 資源計測

### 6.1 取得値

byte値のMiB換算は1 MiB = 1,048,576 bytes。`cpu.stat`はコンテナ全体の `usage_usec` 差分で、
1 CPU coreに対する平均使用率を括弧内に示す。

| 状態 | 時間 | memory start / peak / end | 区間peak delta / end delta | cpu差分 | frame / fps | JPEG payload | loopback転送 |
|---|---:|---|---|---:|---|---|---:|
| Chromium未起動 baseline | 60秒 | 2675.6 / 2774.4 / 2676.2 MiB | +98.8 / +0.6 MiB | 23.421 s (39.0%) | なし | なし | 128 B/s |
| 1 Page静的 1200×800 | 60秒 | 2829.4 / 2851.0 / 2843.3 MiB | +21.6 / +13.8 MiB | 27.271 s (45.5%) | 120 window、2.000 fps | 1,032,466 bytes、17,202 B/s | 17,403 B/s |
| hidden（静的Page） | 65秒 | 2839.7 / 2839.7 / 2796.8 MiB | +0 / -42.9 MiB | 24.310 s (37.4%) | hidden前後 `3→3`、0 fps | hidden前まで25,631 bytes | 168 B/s |
| Page削除後→idle超 | 130秒 | 2858.3 / 2999.1 / 2811.1 MiB | +140.8 / -47.2 MiB | 68.118 s (52.4%) | なし | なし | 418 B/s |
| 最大1600×1200 animation | 60秒（無効値） | 2837.8 / 2837.8 / 2796.8 MiB | +0 / -40.9 MiB | 21.922 s (36.5%) | 初期1枚後、区間0 fps | 13,030 bytes total | 123 B/s |

静的Pageのframe間隔はp50 500.04 ms、p95 500.68 msで、12fps上限以下だった。静的Pageの
安定後memoryはbaseline end比 +167.1 MiBだった。idle区間ではpeakから188.1 MiB低下し、
130秒後にChromium child 0、一時profile 0を確認した。一方、コンテナ全体の `memory.current` は
baseline end比 +134.9 MiBであり、他のAgent/LLM processとpage cacheを含むため完全復帰の帰属はできない。

全採用区間で `memory.events` の `high/max/oom/oom_kill` 増分はすべて0だった。実行前後の
global値もすべて0だった。

### 6.2 取得できなかった値

| 項目 | 状態 | 理由 |
|---|---|---|
| 1 Page animation 1〜5分 | 取得不能 | 15秒以内にbackpressure crash、frame 0 |
| 2 Page同時animation 1〜5分 | 取得不能 | 1 Page目がcrashし定常状態不成立 |
| 2 Page goroutine/queue/memory trend | 取得不能 | 同上。Agentにgoroutine/queue公開metricもない |
| 最大viewportの継続fps | 取得不能 | 初期1 frame後に停止、表示継続条件を満たさない |
| Agent→CP wire bytes | 未取得 | 資源採取は隔離したAgent WS直結。CP区間のbyte counterなし |
| CP→Console wire bytes | 未取得 | 同上 |
| Workspace Stop→Start前後 | 未取得 | 管理経路非公開かつ自コンテナ停止がsessionを終了させる |

静的PageのJPEG application payloadは17,202 B/sだった。CP relayはbinary messageをbyte-preservingで
中継するためapplication payload量は両segmentで同じになる設計だが、本書では各segmentの実wire byteを
実測していない。backpressure修正後にCP/Console双方へbyte counterを置いて再計測する。

## 7. 合否基準

| 基準 | 判定 | 根拠 |
|---|---|---|
| sandbox smoke全条件必須 | **PASS** | smoke全チェック成功 |
| 各Page capture 12fps以下かつ表示継続 | **FAIL** | 静的2fpsはPASS、animation/最大viewportはcrashまたは停止 |
| 2 Pageでgoroutine/queue/memoryが単調増加しない | **判定不能→全体FAIL** | 2 Page定常状態へ到達不能 |
| `memory.events`に新規OOMなし | **PASS** | 全区間で増分0 |
| hidden後にscreencast停止 | **PASS** | 65秒でframe `3→3` |
| Page削除・idle後にChromium/profileなし | **PASS** | 130秒後に双方0 |
| 帯域・メモリ実測値を記録 | **一部PASS** | baseline/静的/hidden/idleは記録、animation/2 Pageはbugで取得不能 |
| image digest記録 | **FAIL（環境制約）** | Docker daemon非公開で取得不能 |
| Stop→Start port/path復元 | **未実施** | 実行環境の管理経路制約 |

**最終判定は不合格**。sandboxとcleanupの成立は確認できたが、通常WS描画、animation、2 Pageという
主要受入条件が成立しない。帯域・メモリの数値gateを設計書へ反映する段階ではなく、P0 bug修正と
完成イメージ再build後に同一matrixを再実行する。

## 8. 再検証時の必須項目

1. Docker host側で対象image digestを取得し、全試験で同じdigestを固定する。
2. 通常Consoleと同じ「POST直後attach→viewport→visibility=true」でJPEGが継続することを確認する。
3. Chromium 150でscreencast frameが複数in-flightになるfixtureを回帰testへ追加する。
4. 静的、aggressive animation、Vite HMR、navigation、wheelを各1200×800で最低1分採取する。
5. 2 Page同時animationを最低5分、1600×1200を最低1分採取する。
6. Pageごとのcapture/decode/send queue深さ、drop、ACK、goroutine数を観測可能にする。
7. Agent→CP、CP→Consoleをsegment別に計測する。
8. 実Workspace Stop→Startでlayoutのport/pathからPageが再生成されることを確認する。
9. `memory.events`、Chromium process、一時profileの回収を再確認する。
