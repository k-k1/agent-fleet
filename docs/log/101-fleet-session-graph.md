# 101. フリートのセッション・グラフ（レーン＝セッション × 横軸＝時間）

> 状態: **設計・起票**（2026-09-20）。決定記録は [ADR 0096](../decisions/0096-fleet-session-graph.ja.md)。
> これは [44-operator-interaction-graph.md](44-operator-interaction-graph.md)（ADR 0027）が
> 「別図・別タスク」として送り出し、ADR 0041 決定 9 が必要性を確定させ、ADR 0078 が却下欄で
> 「別物」と線を引いた図の、3 か月後の再開である。0027 は本 ADR が superseded にする。
> 関連: [51-session-report-v2-ledger.md](51-session-report-v2-ledger.md)（`instr-ledger`）/
> [58-cross-session-messaging.md](58-cross-session-messaging.md)（peer）/
> [96-sessions-overview.md](96-sessions-overview.md)（カードの一覧・稼働帯の前例）/
> [94-rail-lineage.md](94-rail-lineage.md)（系譜が消える対価が見えるようになった回）

利用者の要求（原文の絵）:

```
Session A     ○--*-----+-------+-----+--×
Session A-c1      +-----⤴       ↿     |
Session A-c2      +-------------+     ⇂
Session D                   ○--------+----------×
```

## 101.1 いま実際に引ける線の棚卸し（2026-09-20 実測）

コードを読んで確かめた。「ある」は**追加の書き込みなしで、いますぐ過去に遡って描ける**の意。

| 図の要素 | 状態 | 根拠 |
|---|---|---|
| ○ 誕生 | ある | `session.Meta.CreatedAt`（`internal/session/session.go:424`・RFC3339・create 時に確定） |
| × 終了 | ある | `Meta.StoppedAt`（同 `:425`・**exited と初めて観測されたときに遅延で入る**＝停止の瞬間ではなく「気づいた瞬間」） |
| × が赤 | ある | `Session.ExitReason`（oom / killed / crashed）＋ `ExitCode` / `ExitSignal` |
| + spawn | ある | `Meta.Origin` = `session` ＋ `OriginSession`（ADR 0073。サーバが `AF_SESSION_NAME` から解決、ワイヤの値は信用しない） |
| + 引き継ぎ | ある | `Origin` = `user` ＋ `OriginSession`（2026-09-10 の改訂で提案元だけを継ぐ） |
| + fork | **半分** | `Meta.ForkFrom` は**会話 id**（claude=sid / opencode=ses_… / codex=uuid）であってセッション名ではない。レーンに結ぶには id → セッションの逆引き表が要る |
| ↿ ⇂ peer | **無い** | `internal/sessionx/session_peer.go` は永続記録を持たない（レート制限だけがメモリ上・再起動で消える、とコメントに明記） |
| → 指示 | 半分 | `instr-ledger/<session>.json` の `delivered_at`（`internal/chatx/chat_report_ledger.go`） |
| ← 報告 | 半分 | 同 `reported_at` |
| 稼働帯 | **無い** | live state の履歴はどこにも残らない。`Session.TokenSpends` は**数値の列だけで時刻を持たない**（`internal/session/session.go:318`） |
| 7 日より前 | **消える** | `session.StoppedTTL()`（`:534`・既定 7 日・`AF_SESSION_STOPPED_TTL`）で停止セッションの meta ごと prune。系譜も一緒に消える |

### 101.1.1 🔥 0027 の前提はすでに 1 つ崩れている

ADR 0027 決定 2 は「指示は arm ストアにしか無く、報告配送時に消える」ことを根拠に
`operator-graph/<conv>.jsonl` を新設しようとしていた。**その arm は ADR 0035（報告 v2・2026-07-29）が
廃止済み**で、いまは 1 指示 = 1 行の `instr-ledger/<session>.json` がある（`delivered_at` /
`reported_at` / `conv` / `source`）。0027 を書いたまま着工すると、同じものを数える台帳が 2 本になる。

ただし `instr-ledger` を図の台帳として読むのは誤りである（ADR 0096 決定 4）。理由は 3 つで、
どれも「**履歴ではなく作業リスト**」に帰着する。

- `instrClosedKeep = 20` — 閉じた行は新しい 20 件だけ残して捨てる。長く生きたセッションの
  古い指示は**黙って消える**。
- `reopened` / `cancelled` — 状態機械が行を書き換える。補償 reopen が走ると `reported_at` が
  動く＝**図の過去が動く**。
- peer は構造的に載らない。ADR 0041 決定 4（arm を一切いじらない）は AF 版の回避不能な必須要件で、
  peer 送信が台帳に行を作ることは将来も無い。

### 101.1.2 injection ストアも使えない

`internal/sessionx/session_injections.go` は「誰が投げた文面か」を覚えているが、
**時刻を持たない**（`{Text, Source}` のみ・テキストで dedupe・上限 100 件）。目的が
「転写の user ターンにバッジを貼る」ことなので、時系列の台帳にはならない。

## 101.2 🔥 観測の間隔は「誰かが見た間隔」である（設計の肝）

稼働帯を live state の遷移から作ると決めた以上（ADR 0096 決定 3）、**誰がどれくらいの頻度で
状態を観測しているか**が図の分解能そのものになる。調べた結果:

| 観測者 | 間隔 | 条件 |
|---|---|---|
| Console（セッション一覧） | 4 秒 | **タブが開いているあいだだけ** |
| CP の idle-stop reaper | 既定 **1 分** | `AF_IDLE_SWEEP_INTERVAL`（`control-plane/main.go:295`・`intervalOff` で `0` は**配備ごと停止**）。`sweep` → `sweepWorkspace` が `sessionWire` を取る＝Agent の一覧ハンドラを叩く |
| 報告リコンサイラ | 15〜30 秒 | ⚠️ **全セッションではない**。`sweep()` は `instrSweepSessions()` が返す「未報告の行を持つセッション」だけを回る（`internal/chatx/chat_report_reconcile.go:756`） |

つまり **Agent 側に「全セッションを定期的に見る」常設の掃引は存在しない**。誰も Console を開かず、
reaper を切った配備では、観測は**ゼロ**になる。

→ したがって「観測が無かった区間」を idle として塗ってはいけない。報告 v2 が敗因として書き残した
**「無ファイルは『不明』であって idle ではない」**（docs/log/51 §settled 述語）を、そのまま図の
描画規則にする。塗らずに斜線にする。

→ Agent の再起動を挟んだ区間も同じ扱いにできるよう、起動直後に全セッションの現在状態を 1 回
書く（`resync` 行）。

## 101.3 台帳の形と量の見積もり

```
~/.config/agent-fleet/fleet-graph/lineage.jsonl                 # 追記・永続
~/.config/agent-fleet/fleet-graph/activity-<YYYY-MM-DD>.jsonl   # 日次回転・既定 30 日
```

系譜行（`lineage.jsonl`）:

```jsonc
{"ts":"2026-09-20T10:04:11+09:00","ev":"birth","name":"sage35s","kind":"claude","repo":"agent-fleet",
 "origin":"session","originSession":"sxmzm4b","conv":"<own sid>","forkFrom":"<source conv id>",
 "display":"ADR 0096 起票"}
{"ts":"2026-09-20T14:31:00+09:00","ev":"convid","name":"sage35s","conv":"<drifted sid>"}
{"ts":"2026-09-20T18:22:03+09:00","ev":"death","name":"sage35s","reason":"oom","code":137}
{"ts":"2026-09-21T09:00:00+09:00","ev":"archived","name":"sage35s"}
```

活動行（`activity-*.jsonl`）:

```jsonc
{"ts":"…","ev":"state","name":"sage35s","from":"idle","to":"working"}
{"ts":"…","ev":"instruct","from":"conv:<id>","to":"sage35s","source":"operator","excerpt":"…"}
{"ts":"…","ev":"report","from":"sage35s","to":"conv:<id>","kind":"answer-ready"}
{"ts":"…","ev":"peer","from":"sage35s","to":"sxmzm4b","intent":"request"}
{"ts":"…","ev":"resync","name":"sage35s","to":"idle"}
```

`from` / `to` のうち**レーンになるのはセッション名だけ**で、`conv:<id>`（チャット会話）・`user`・
`schedule`・`bridge:discord` / `bridge:slack` はレーンを持たない（図の上下へ抜ける矢印・ADR 0096
決定 8-2）。

量（1 行 ≒ 80〜200 バイト）:

- 状態遷移は 1 ターンあたり 2〜4 行。10 セッション × 100 ターン/日 ≒ **2,000〜4,000 行/日 ≒
  0.2〜0.8 MB/日**。30 日で 25 MB 未満。
- 系譜は 1 セッション 2 行（birth / death）。1 日 20 セッション起こしても **年 1 MB 級**。
- ⚠️ **書き込み頻度は費用である**（ADR 0087：EFS のメタデータ IO で本番全体が遅くなった回）。
  1 行ごとに `open`→`write`→`close` するとメタデータ IO が遷移の回数だけ発生する。
  **実装時に測ること**——プロセス内で fd を保持する／1 秒バッファして束ねる、のどちらが要るかは
  実測で決める（この文書は「要るかもしれない」ところまでしか言えない）。

## 101.4 レイアウト（純関数の輪郭）

`console/src/lib/fleetgraph.ts` に、SCM の `lib/gitgraph.ts` と同じ形で置く（純関数 → モデル →
インライン SVG）。

- **レーンの並び** = 家系（ADR 0078 決定 6 の規則を借りる。親の直下に子・兄弟は古い順・根は新しい順）。
  段の組み立ては `origin_session` の親リンクを辿るだけで、0078 の実装が既に持っている。
- **x** = `(t - windowStart) / (windowEnd - windowStart) * width`。線形（ADR 0096 決定 8）。
  右端が「いま」。ライブのレーンは右端で脈打つ。
- **y** = レーン index × 行高。レーンの本数だけが高さを決める（時間は高さに効かない＝これが
  シーケンス図との一番の違い）。
- **矢印**は 2 レーン間の縦の線 ＋ 向きの印。同時刻に複数本あるときは x を数 px ずらす
  （コミットグラフのエッジと同じ問題・同じ解）。
- **稼働帯**は `[t0, t1)` の矩形。kind 色 ＋ 状態（working / question / limited）で濃淡。
  **観測の無い区間は斜線**（101.2）。
- 分岐（`+`）は親レーンの y から子レーンの y への曲線を、子の `birth` の x に引く。

## 101.5 2026-09-20 に利用者と決めたこと（起票時の未決 5 件のうち 4 件）

1. ✅ **`ForkFrom` の逆引き** → 系譜台帳の `birth` 行に**そのセッション自身の会話 id を焼く**
   （ADR 0096 決定 13）。🔥 焼くのは **AF が割り当てた id であって観測値ではない**——fork 直後の
   claude は自分の jsonl が実体化するまで元セッションの転写を読むので
   （`internal/sessionx/session_transcript.go`）、観測して焼くと**子の行に親の id が入り、エッジが
   自分自身を指す**。会話 id のドリフト（claude が自分を再起動すると argv から `--session-id` が
   落ち、新しいランダム id で書き始める。実測 2.1.239・`internal/agents/claude/sid.go`）は
   `convid` 行で追う。
2. ✅ **遡及は有界でよい**（利用者の言葉で「遡りたいがある程度制限があってもよい」）。左へパンすると
   3 段で減衰する: 活動 30 日 → 系譜と生没だけの骨格 → 導入以前は `Meta` から書き起こした 7 日ぶん。
   **どこで何が減ったかは図に明示する**（境界線を引く。無言で薄くしない）。
3. ⏳ **書き込みのバッファリングの要否**（101.3 の ⚠️・ADR 0087）。実装時に測って決める。
4. ✅ **セッションでない発信元にはレーンを作らない**（ADR 0096 決定 8-2）。会話・人・定時実行・
   ブリッジとの往復は**図の上下へ抜ける矢印**。理由: この図のレーンは「生まれて死ぬもの」の帯で、
   x 軸はその生存期間を意味する。生没を持たないものをレーンにすると**同じ横線が 2 つの意味を持つ**。
5. ✅ **停止中・アーカイブ済みは点線で描く**（ADR 0096 決定 12・利用者の指示）。規則は
   **点線＝まだ居る（再開できる）／終端＝もう居ない**。停止中は既定で描く——0078 の一覧は
   「既定は稼働中のみ」だが、あれは*いま*の断面で、この図は*経過*である（過去から停止中を隠すと
   図が空になる）。アーカイブのトグルは `PaneContent` に持つ（React state はタブ切替で消える）。
