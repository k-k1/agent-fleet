# 101. フリートのセッション・グラフ（レーン＝セッション × 横軸＝時間）

> 状態: **P0（契約凍結）実装**（2026-09-20・§101.6）。以降 P1（3 並列）→ P2（統合）。
> 決定記録は [ADR 0096](../decisions/0096-fleet-session-graph.ja.md)。
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
| 報告リコンサイラ | 15〜30 秒 | ⚠️ **全セッションではない**。`sweep()` が回るのは `instrSweepSessions()` が返す 2 集合——**未報告の行を持つセッション**と**猶予窓の中にある報告済みセッション**（補償 reopen 用）——だけ（`chat_report_ledger.go` / `chat_report_reconcile.go:756`） |

つまり **Agent 側に「全セッションを定期的に見る」常設の掃引は存在しない**。誰も Console を開かず、
reaper を切った配備では、観測は**ゼロ**になる。

→ したがって「観測が無かった区間」を idle として塗ってはいけない。報告 v2 が敗因として書き残した
**「無ファイルは『不明』であって idle ではない」**（docs/log/51 §settled 述語）を、そのまま図の
描画規則にする。塗らずに斜線にする。

→ 🔥 **観測者が 2 人いるので、重複行の防止は書き手の責務**（ADR 0096 決定 3）。Console（4 秒）と
reaper（1 分）は同じ一覧ハンドラを同時に叩きうる。書き手はセッションごとの直前状態をプロセス内に
持ち、変化したときだけ・セッション単位の mutex で直列化して書く。`StateEvent.from` はその表から
埋め、**表に無ければ省く**（`from` 無し＝「直前は不明」。idle ではない）。

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
{"ts":"2026-09-20T14:31:00.412+09:00","ev":"convid","name":"sage35s","conv":"<drifted sid>"}
{"ts":"2026-09-20T18:22:03.900+09:00","ev":"death","name":"sage35s","reason":"oom","code":137}
{"ts":"2026-09-20T19:05:41.088+09:00","ev":"revive","name":"sage35s"}
{"ts":"2026-09-21T09:00:00.000+09:00","ev":"archived","name":"sage35s","archived":true}
```

`revive` が要るのは、**再開すると `Meta.StoppedAt` がクリアされる**から（`session_handlers.go` の
一覧・`session_tmux.go`・`session_driver.go`）。1 レーンの生涯は ○──×──○──× と続きうるので、
死を 1 組しか持てないモデルでは 2 回目の × がどの区間の終わりか決まらない。

活動行（`activity-*.jsonl`）:

```jsonc
{"ts":"…","ev":"state","name":"sage35s","from":"idle","to":"working"}
{"ts":"…","ev":"instruct","from":"conv:<id>","to":"sage35s","source":"operator","excerpt":"…"}
{"ts":"…","ev":"report","from":"sage35s","to":"conv:<id>","kind":"answer-ready"}
{"ts":"…","ev":"peer","from":"sage35s","to":"sxmzm4b","intent":"request"}
{"ts":"…","ev":"resync","name":"sage35s","to":"idle"}
```

`from` / `to` のうち**レーンになるのはセッション名だけ**で、`conv:<id>`（チャット会話）・`user`・
`schedule`・`bridge:discord` / `bridge:slack`・`agent`（自動再開＝誰の指示でもないターン）は
レーンを持たない（図の上下へ抜ける矢印・ADR 0096 決定 8-2）。

**時刻の綴りは 2 つある**（ADR 0096 決定 2）。台帳の行は **RFC3339（ミリ秒）**、DTO
（`FleetGraphPage`）は **unix ミリ秒の数値**で、変換は必ず S-BE が行う。日次ファイルの日付は
**UTC**（窓は millis で来るので、ローカル TZ で切ると境界の 1 日を無言で読み落とす）。

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
   （ADR 0096 決定 13）。~~🔥 焼くのは **AF が割り当てた id であって観測値ではない**~~
   会話 id のドリフト（claude が自分を再起動すると argv から `--session-id` が落ち、新しい
   ランダム id で書き始める。実測 2.1.239・`internal/agents/claude/sid.go`）は `convid` 行で追う。

   🔴 **訂正（P0 レビュー 1 巡目・§101.6）**: 取り消し線の規則は**誤り**で、claude にしか
   当てはまらなかった。`ForkFrom` の実値を作る `Forker.ForkSource` は**どの kind も観測ストアから
   解く**——claude=`LiveSID()`（`claude.go:42`）、codex=hook が記録した slot 別 id
   （`codex.go:78`）、opencode=会話ストアの現行セッション（`opencode.go:47`）。
   規則どおりに実装すると **codex と opencode の fork エッジは永久に一致しない**。正しくは
   「**その kind の `ForkSource` と同じ経路で解決した id を焼く**」。
   **なぜ間違えたか**: claude の sid が決定的（セッション名由来）であることを一般則だと思い込み、
   `ForkSource` の実装を 3 kind ぶん読まずに claude の転写先読みの罠だけを見ていた。
   **一般化**: kind をまたぐ値の規則は、**kind ごとの実装を全部読んでから**書く。
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

## 101.6 P0 = 契約凍結（2026-09-20 実装）

凍結したのは 3 点で、すべて `console/src/types/fleetgraph.ts` に入っている（型のみ・import 専用・
実行コードなし）。ADR 0027 の `types/opgraph.ts` は同じコミットで退役させた（どこからも import
されていなかった。ADR 0041 決定 9 の「型の正」の指し先も 0041 に 🔴 追記して引き直した）。

1. **REST DTO** — `FleetGraphPage`（`GET /api/fleet-graph?since=&until=`）。`now` は **Agent の時計**
   を載せる（ブラウザの時計で「いま」を決めない）。`coverage` は遡及の 3 段減衰がどこで切れるかを
   運ぶ——**「矢印が無い」と「何も起きていない」は図の上で区別がつかない**ので、境界は描くために
   データとして持つ。
2. **台帳の行形式** — `LineageEvent`（`birth` / `convid` / `death` / `archived`）と
   `ActivityEvent`（`state` / `resync` / `instruct` / `report` / `peer`）。
3. **描画モデルと組み立て関数の型** — `GraphModel` / `GraphLane` / `GraphSegment` / `GraphArrow` /
   `CoverageMark` と `BuildFleetGraph`。

### 型に埋めた「消えると事故になる」区別

- `LedgerState` を**台帳専用のリテラル union**として置いた。`SessionState` を借りると
  `""`＝idle の綴りが混入し、`limited` / `blocked` / `auth` / `spend_limit` / `failed` / `aborted`
  （`agents/notify.go`）が落ちる。🔥 そして **TS では `SessionState | string` は `string` に潰れて
  何も縛らない**——最初の版はこれで、S-BE が `""` を書き S-LOGIC が「未知＝斜線」に倒すと
  **働いていた区間が「不明」で塗られる**。正規化（`""`→`idle`・語彙外→`idle`）は書き手の責務と明記。
- `SegmentKindByState`（状態 → 帯の写像）も契約に入れた。**どちらのレーンが持つか未定のまま**だと
  2 通りの色分けが生まれる。
- `SegmentKind` の `unknown`。**既定値にしない**（`idle` に倒さない）ことがこの図の正直さの全部で、
  型に無ければ実装は必ず `idle` を書く。
- `LaneRun[]`（`GraphLane.runs`）と `ev:"revive"`。🔥 **再開すると `Meta.StoppedAt` はクリアされる**
  ので 1 レーンの生涯は複数の走行。死を 1 組しか持てない最初の版では、2 回目の × がどの区間の
  終わりか決まらず、**死んで再開した区間が「停止中の点線」に化けた**。
- `FleetGraphPage.lineage` は**窓で切らない**と明記。窓の中のイベントだけにすると、3 日前に生まれて
  今も動くレーンに `kind` も `origin` も表示名も無い（ライブの `Session` に `origin` は無い）。
- `ActorId` は「レーンになるもの（セッション名）」と「ならないもの（`conv:` / `user` /
  `schedule` / `bridge:` / `agent`）」を 1 つの型に混ぜてある。`GraphArrow.fromRow` / `toRow` が
  `null` を取れるのが**図の外へ抜ける矢印**（決定 8-2）で、型で表さないと外部発信元が無言で捨てられる。
  `agent` は自動再開（`auto-resume`）——**誰の指示でもないターン**なので、`user` に倒さない。
- `GraphSegment.laneId`（レーン id）と `GraphLane.row` / `GraphArrow.fromRow`（行 index）は
  **綴りで区別する**。最初の版は両方 `lane` で、S-VIEW が index と読めば全セグメントが行 0 に落ちた。
- `GraphModel` は**素データだけ**（関数メンバを持たない）。手本の `lib/gitgraph.ts` が素データ＋
  別 export の補助関数なのと同じ理由で、関数を埋めると **fixture が JSON にならず**、
  モデルのスナップショット比較もできない。`xOf` / `laneY` は `GraphScale` を取る別 export。
- `BirthEvent.conv` の解決規則（🔥 **その kind の `ForkSource` と同じ経路**。claude=`LiveSID()`・
  codex=hook 記録の slot 別 id・opencode=会話ストアの現行セッション）。ここを「AF が割り当てた id」に
  すると **codex / opencode の fork エッジは永久に一致しない**。

### P1 の担当境界（衝突をマージ 1 点に閉じる）

| レーン | 触ってよいもの |
|---|---|
| S-BE | `workspace/agent/internal/…`（台帳 2 本・**書き込み 8 箇所**〔create／stop・exit／**resume**（`StoppedAt` をクリアする 3 経路）／**archive・restore**／状態観測／peer／指示投入／報告配送〕・`GET /api/fleet-graph`）、`workspace/agent/routes.go`、`control-plane/routes.go` の許可リスト |
| S-LOGIC | `console/src/lib/fleetgraph.ts` ＋ `fleetgraph.test.ts` のみ |
| S-VIEW | `console/src/features/fleetgraph/*`、**共有グルー（`layout/types.ts` の union・`migrate.ts`・`Pane.tsx`・`paneTitle.ts`・`features/keys/commands.ts`・i18n の ja/en）は S-VIEW 専有**、🔥 **`console/src/core/api/client.ts`（API 呼び出しを全部持つ 1 ファイル）も S-VIEW 専有**——3 レーンが同じファイルを触りうる最後の 1 箇所がここだった |
| （誰も） | `console/src/types/fleetgraph.ts` は**凍結**。P1 のレーンは編集しない。直す必要が出たら**統合役へ差し戻す**（契約を片側だけ書き換えると、他の 2 レーンは気づかないまま食い違う） |

### P0 レビュー（子セッション・opus・2026-09-20）で直したもの

重大 3・中 7・軽微 4。**重大 3 件はすべて「契約の穴」**で、3 レーンが相談せずに実装したときに
突き合わせで初めて壊れる種類のものだった（上の「型に埋めた区別」の 🔥 が該当箇所）。加えて
**事実誤認が 1 件**——決定 13 の「焼くのは AF が割り当てた id」は claude 以外に当てはまらず、
`Forker.ForkSource` の実装（codex は hook 記録の観測値、opencode は会話ストアの現行セッション）を
読めば分かることだった。裏取りは一次情報で再確認済み。

ほかに直したもの: 台帳＝RFC3339 / DTO＝millis の明記と日次ファイル日付の UTC 固定、
`ArchivedEvent` の例に必須フィールドが無かった件、`source` 語彙に `auto-resume` が欠けていた件、
重複 state 行の防止（観測者が 2 人いる）、担当境界表の `client.ts` と契約の改訂権、
§101.2 の掃引集合の記述（正確には 2 集合）、docs/log/44 の状態行の矛盾、
`types/fleetgraph.ts` のヘッダから履歴の記述を落としたこと（`AGENTS.md`）。

P0 では共有グルーに**一切触っていない**（`types/fleetgraph.ts` の追加と `types/opgraph.ts` の削除だけ）。
`npm run typecheck` は緑——ただし**この型はまだ誰も import していない**ので、陽性対照として
わざと `GraphModel["nope"]` を書いて `TS2339` が出ることを確かめてから戻した（型ファイルを足しただけの
コミットで「緑」を報告するときは、tsc がそのファイルを見ていることを確かめる。メモ
`scm-commit-graph-edges` の「テストの存在≠実行」と同じ罠）。

## 101.7 P0 レビュー 2 巡目（同じ子セッション・opus）で出た穴

1 巡目の修正で**新しく 2 つ開いた**。どちらも「型は正しいが、それを書く者が居ない／語彙が足りない」形。

- 🔥 **`revive` と `archived` の書き手が居なかった。** 型に `runs[]` と `presence:"archived"` を
  足したのに、帰結と §101.6 の S-BE 欄は**「書き込み 6 箇所」のまま**だった（create・stop/exit・
  状態観測・peer・指示・報告）。resume と archive/restore が無いので、S-BE は 6 箇所を実装して
  「完了」とし、`ReviveEvent` は 1 行も書かれず `runs` は常に 1 本——**直したはずの「死んで再開した
  区間が点線に化ける」がそのまま再発する**。→ **8 箇所**に直し、resume＝`StoppedAt` をクリアする
  3 経路（一覧ハンドラ・`session_tmux.go`・`session_driver.go`）と archive/restore＝
  `HandleArchiveSession` / `HandleRestoreSession` を名指しした。
  **一般化: 型を足したら「それを書く行がどの関数に増えるか」まで同じ変更で書く。**
- 🔥 **`compacting` が語彙から漏れ、「語彙外→idle」がそれを消すところだった。** `compacting` は
  codex が立てる実在のワイヤ状態（`codex.go:219`）で、Console は working として描く
  （`sessionview.ts:160`）。閉じた union にした副作用で、**自動圧縮中＝明確に稼働中の区間が帯から
  消える**ことになっていた。→ 語彙に足し、さらに**「語彙外→`idle`」という倒し方自体をやめた**
  （`unknown` にして生の綴りを `raw` に残す）。**一般化: 未知を既定値に倒すときは、倒す先が
  「安全側」かを確かめる。ここでは idle が危険側だった**（無観測を idle にしないという
  この図の主旨とも矛盾していた）。

ほかに直したもの: 正規化がライブ `Session.state` 経路（S-LOGIC）に割り当てられていなかった件
（→ `GraphNormalizeState` を契約に置き、同一 fixture を Go と vitest の両方に置く）、窓外の親を
持つ子の `parent`/`depth` が未定義で**兄弟が窓の取り方次第で離れる**件（→ `rootId` を並びの鍵に）、
削除で系譜だけ消えて活動行が残ると**セッション間の peer が「図の外から来た矢印」に化ける**件
（→ `erased` レーンとして描く規則を契約に）、`gone` なのに run が開いたままの終端規則（→ `cut`）、
`LaneRun` が `exitSignal` を落としていた件、時刻精度の根拠の一文（`Meta`/`instr-ledger` は**秒**精度
なので「揃える」は不正確・「同じ形式で精度だけ上げる」が正しい）。
