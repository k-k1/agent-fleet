# 51. セッション報告 v2 — 指示台帳とレベル駆動リコンサイラ

- 状態: **実装済み** — Phase 1（判定の一本化）/ Phase 2（台帳置換）/ Phase 3（補償 reopen ＋
  自己申告ファストパス）すべて実装（2026-07-29）。[docs/30](30-session-report.md) の報告機構の後継設計。
- 決定記録: [ADR 0035](../decisions/0035-session-report-v2-ledger.ja.md)
- 関連: [docs/47](47-turn-abort-auto-resume.md)（中断分類・自動再開）/ [docs/27](27-agent-managed-driver.md)（notify seam）/ [docs/38](38-scheduled-execution.md)（配達検証）

## 背景 — v1 の構造的限界

v1（docs/30）は「**エッジ駆動**（Stop フック等のイベントを1回だけ捕まえる）＋
**不可逆な1bit**（arm を consume したら戻れない）」の組み合わせで、次の事故を
繰り返してきた。

- **2026-07-24 saga5uc**: BG サブエージェント起動直後の早期 Stop が arm を消費 →
  本完了が報告されず。対策＝SubagentBusy 保留と waiter（docs/30 §保留）。
- **2026-07-28 sqmconc**: その waiter が、誤 idle ヒールがマーカーを消した十数秒の
  窓を「完了」と誤認して arm を消費 → 本完了(27分後)の kick は armed=false で棄却。
  対策＝waiter の3条件強化（docs/30 §waiter の誤 idle）。

パッチのたびに窓は狭まるが、**並行性の縫い目（BG エージェント・BG bash・キュー投入・
auto-compaction・TUI 文字列ドリフト）が増えるたびに新しい窓が生まれる**構造は
変わらない。棚卸しで残っている既知の穴:

| # | 穴 | 原因 |
|---|---|---|
| A | キュー投入で複数指示が潰れる | arm が単一bit（re-arm は上書き）。ターン1の Stop が消費すると、キュー済み指示2の完了は報告されない |
| B | waiter が別指示の arm を消費し得る | consume に世代（同一性）検証が無い |
| C | kiro/cursor 等 TUI ポーリング系の誤消費 | poll 1回の footer 誤読で MarkTurnEnd → 即消費。裏取りもデバウンスも無い |
| D | 配送失敗で報告消失 | consume-then-deliver（saveConv 失敗で arm だけ消える） |
| E | Bash run_in_background の中間 Stop | busy シグナルが無く kick が即消費（docs/30 で受容済み） |
| F | agent 再起動中の kick 消失 | kickSessionReport は自プロセスへの best-effort HTTP。再起動の瞬間の Stop は落ちる（次の Stop が無ければ永久） |
| G | managed daemon の異常死が報告されない | serve.go は `status.PersistExit` を書くだけで kick を持たない（docs/30 §制限に明記の非対称） |

痛みは3脚に分解できる: **同一性**（指示の識別子が無い）・**検出**（意味的イベント
「指示の完了」を機械的シグナルの瞬間値から推定し、誤りが一発アウト）・**配送**
（検出時点の不可逆消費に配送成否まで背負わせる）。

## 目標 / 非目標

**目標**
- 「指示1件につき報告1回・報告は完了で」の契約を維持したまま、判定を**冪等・
  再評価可能**にする（誤「まだ」は次 tick で自己修正、誤「完了」は補償で自己修復）。
- kind 追加や TUI 契約ドリフトが「報告の**消失**」ではなく最悪「報告の**遅延**」に
  縮退するようにする。
- 検出ロジック（現在 kick ハンドラ・waiter・ヒール・逆ヒールに散在する idle 判定）を
  1 箇所に集約する。

**非目標**
- 報告本文（事実のみ・全 kind 統一）、interim（question/plan）の UX、自動ターン、
  自動走行モードの変更はしない — 変えるのは「いつ・何を根拠に発火するか」だけ。
- ミリ秒即応の維持は求めない（オペレーターの自動ターン自体が数十秒かかる）。
- v1 ファイルの移行ツールは作らない（§移行の互換規則で併走・自然消滅）。

## 全体像

```
投入（create_session / send_to_session / スケジューラ / ブリッジ）
  └─ 台帳に「指示行」を追加（上書きしない）
検出（リコンサイラ: 単一 goroutine・15〜30s tick ＋ ヒント起床）
  └─ pending 行を持つセッションだけ settled/progressed 述語で再評価
配送（シンク: 会話ファイルへ冪等追記）
  └─ 行IDで重複排除 → 既存の自動ターン（変更なし）
フック・notify seam・record-exit
  └─ 「イベント」から「ヒント（起床信号）」へ降格
```

## データモデル — 指示台帳

arm の1bit（`session-report/<name>.json`）を廃止し、指示1件=台帳1行にする。

```
~/.config/agent-fleet/instr-ledger/<session>.json
{
  "rows": [
    {
      "id": "i-x7k2m9",            // 行ID（配送冪等キー）
      "conv": "<conversation-uuid>",
      "source": "operator|scheduler|bridge",
      "delivered_at": "RFC3339",     // 投入（配達検証後）時刻
      "cursor": { ... },             // 投入時点の進捗カーソル（kind 別・§述語）
      "state": "pending|interim_reported|reported|reopened|cancelled",
      "interim": {"question_at": "...", "plan_at": "..."},  // 中間報告の既報記録
      "reported_at": "RFC3339",
      "reopen_count": 0
    }
  ]
}
```

- 書き手は全員サーバプロセス内（投入ハンドラ・リコンサイラ）— セッション単位の
  in-process mutex で直列化する。hook プロセスは台帳を読まない（ヒントを送るだけ）。
- **状態機械**: `pending →(完了/異常検出)→ reported →(補償)→ reopened →…→ reported`
  （reopen は上限2回・§補償）。`cancelled` は stop_session の disarm 相当
  （Console halt は行を残す — 再開後の完了で報告する v1 規約どおり）。
- **複数 pending 行の畳み込み**: settle 時に pending 全行を**1通の報告**で報告し、
  全行を同じ報告 ref で reported にする（本文に「指示N件ぶん」と各 delivered_at を
  添える）。穴Aを「潰れる」から「明示的に束ねる」に変える。スパムもしない。

## settled / progressed 述語

判定は「**idle 証拠が1つ以上 ∧ busy 証拠がゼロ**、を**2 tick 連続**」で settle とする。
シグナルは証拠として列挙し、kind ごとに使えるものを表で固定する（新 kind 追加時は
この表を埋めるのが受け入れ条件）。

**busy 証拠**（1つでもあれば settle しない）
- `SubagentBusy`（BG サブエージェント/Workflow jsonl 鮮度・90s）
- `TranscriptBusy`（メイン transcript 鮮度・90s）
- `tmuxx.IsBusy`（ペインの中断アフォーダンス — settle 候補時のみ実行して tmux 負荷を抑える）
- transcript 末尾の未消化 `queued_command` attachment（キュー済み指示が残っている）
- pending question / plan / permission（interim 状態はそもそも完了ではない）

**idle 証拠**（最低1つ必要 — 「不明」を idle と既定しない）
- 明示 idle マーカー（Stop フック / MarkTurnEnd が書いたファイル。**無ファイルは
  「不明」であって idle ではない** — v1 waiter の敗因の恒久化）
- managed driver の turn 終端（MarkTurnEnd 済み）
- opencode: SQLite の終端レコード / codex: rollout 終端（既存の kind 別 LiveState 源）
- TUI ルート（kiro 等）: `AtIdlePrompt` — ただし単独では 2 tick 連続要求が実質の
  デバウンスになる（**穴Cは「一律解消」ではなく「窓の縮小」**: footer の1回誤読は
  2 tick で落とせるが、TUI 文字列契約が丸ごとドリフトして「常時 idle に見える」状態は
  何 tick 連続でも通る。恒久策は kind 別 live state の**再導出**を busy 証拠に足すこと
  — §移行 Phase 2 の TODO）
- 自己申告（§ファストパス — Phase 3）

**progressed(行)** — 完了報告の前提: 行の `cursor` より後にセッションが実際に働いた
証拠があること。投入直後の誤 settle（何もしていないのに「完了」）を防ぐ。

| kind | cursor / progressed の実装 |
|---|---|
| claude | 投入時の jsonl サイズ（バイト）→ それ以降に assistant レコードが存在 |
| codex/opencode（managed） | MarkTurnStart/End のターン連番 → 番号が進んだ |
| codex(TUI)/opencode(TUI) | rollout / SQLite のレコード数 |
| cursor/copilot/kiro/agy | 初版は progressed を免除（settled のみ。transcript 相当が
  無い/薄い kind — 弱い保証であることを表に明記し、将来ソースが増えたら埋める） |

**異常系**は述語と同列に扱う（v1 の kind=exit / turn-failed / turn-aborted を吸収）:
- `ExitInfo`（record-exit / managed wait）に oom/crashed/killed → exit 報告。ファイルを
  レベルで読むので、kick を持たない managed daemon の異常死（穴G）も追加配線なしで拾える
- transcript 終端分類 `AbortedTurn`（docs/47）→ aborted/failed 報告（自動再開の
  カウンタ・文言は v1 のまま）

## リコンサイラ

- **単一 goroutine**・tick 15〜30s（可変）＋ ヒント channel で即時起床。pending/
  reopened 行を持つセッションだけ評価するので定常コストはほぼゼロ。
- 判定→台帳更新→シンク追記まで**同一 goroutine で直列**。v1 で必要だった
  reportArmMu / reportWaiters / consumeReportArm の世代レースの議論が丸ごと消える。
- **interim**: pending-question / pending-plan の存在をレベルで見て、行ごとに1回だけ
  interim 報告（`interim.question_at` 等に刻む）。非消費の意味論は v1 と同一。
- **配送（シンク）**: 会話ロック下で「この行IDの報告が既にあるか」を見てから追記。
  追記に失敗したら台帳は動かさない（次 tick で再試行）— 穴Dの解消。
  成功後に自動ターンを1回（複数行の畳み込みでも1回）。

### 補償 — 誤「完了」の自己修復

reported 行は grace 期間（10分）監視を続ける。**新しい指示行が無いまま**セッションが
busy 証拠を出し始めたら、
1. 会話へ訂正（「先の完了報告は早計でした — セッションは続行中です」）を追記し、
2. 行を `reopened` に戻す（本完了で改めて報告される）。

順序は落とせない: **訂正を配ってから**開き直す。逆順だと、訂正の配送に失敗したときに
「黙って開き直しただけ」になり、利用者から見て v1 の消失と区別が付かない。

reopen は行あたり2回まで。上限到達時は「判定が振動している」事実を利用者向け文言で
報告して打ち切る（docs/47 の自動再開上限と同じイディオム）。これで v1 の
「**誤消費＝回復不能**」という非対称が「誤報告＝訂正付きで自己修復」に変わる。

## フック＝ヒント化

`session-status` hook・notify seam・record-exit は**リコンサイラを起こすだけ**にする。
- 実装上は既存の `POST /chat/report` を残し、ハンドラの中身を「wake 送信」に置き換える
  （hook スクリプト・焼き込みイメージの変更を不要にする）。
- フックが全部死んでいても最悪 1 tick 遅れで拾う。agent 再起動中の kick 消失（穴F）は
  台帳が残っている限り自然回復する。

## 自己申告ファストパス（Phase 3）

指示プロンプトに「完了したら `af_report` を呼ぶ」を注入し（mcp-registry の組み込み
サーバー `af` としてCLIを持つ全 kind のセッションへ配る）、呼ばれたらヒント起床＋idle 証拠の1つと
して数える（2 tick 要求を 1 tick に短縮）。**意味的完了を直接測る唯一の手段**だが、呼び忘れ・早呼びがあるため
単独では backbone にしない — リコンサイラが安全網。申告はタイミング信号のみで、報告
本文は従来どおりサーバ生成（fact-only — prompt injection 面を増やさない）。builtin `af`は後にChromium Attach Viewの
対話セッション直接経路も担うが、`af_report`の入力・判定・配送契約は変えない（[docs/53 §53.8](53-chromium-attach-view.md)）。

## v1 からの移行

| Phase | 内容 | 閉じる穴 | 撤去するもの |
|---|---|---|---|
| 1 ✅ | 判定の一本化: kick の即消費をやめ、arm bit のままリコンサイラが settle 述語（2 tick デバウンス込み）で消費判定 | C, D, F | deferReportWhileBackgroundBusy / waitReportUntilBackgroundDone / reportWaiters（waiter 特例が述語に畳まれる） |
| 2 ✅ | 台帳置換: arm 書込み箇所（create_session / /input / /turn / bridge / スケジューラ）を行追加へ。disarm → cancelled。起動時に既存 `armed=true` を1行に変換 | A, B | session-report/*.json（起動時の移行が読んで削除）・consumeReportArm・reportArmMu |
| 3 ✅ | 補償 reopen ＋ 自己申告ファストパス | E の実害（誤報告→訂正に縮退） | — |

- 各 Phase は独立にリリース／ロールバック可能。Phase 1 の時点で v1 の外部契約
  （報告本文・interim・自動ターン・disarm 規約）は不変。

### Phase 1 実装メモ（2026-07-29 / `chat_report_reconcile.go`）

設計との差分・実装で決めたことだけを記す（残りは上記のとおり）。

- **idle 証拠は「明示 idle マーカー」1本＋2つの限定**にした。マーカーの実在だけでは
  足りない: `status.TurnEnd`（その書込みが**ターンの終端**かの1bit）と「指示（arm）
  以降の書込みか」を要求する。SessionStart の idle リセットと managed の runtime 喪失
  （`TurnUnknown`）も同じ `"idle"` を書くので、状態文字列だけでは「終わった」と
  「分からない」が同型になり、レベル判定が誤完了を作る。この1bit が §progressed の
  最小実装も兼ねる（kind 別カーソルは Phase 2 の台帳で）。
- **transcript 鮮度は常設ゲートにしない**。素の 90s TTL を busy 証拠に置くと、正常な
  完了報告が毎回 90s 遅れる（v1 は waiter 経路でしか使っていなかった）。Stop が書いた
  終端マーカーという positive な証拠がある以上、「マーカーより後にも転写が伸びているか」
  の相対比較が正しい。鮮度は安全弁として併用し、上限は v1 と同じ 90s に収める。
- **`tmuxx.IsBusy` は claude TUI のみ**（v1 waiter と同じ適用範囲）。実装が claude の
  スピナー契約を読むので、他 kind のペインで誤検知すると報告が永久に出ない。
- **異常系の qualifier**（turn-failed / turn-aborted）はレベルから読めない（どちらも
  status には idle が書かれる）ので、唯一ヒントが運ぶ情報にした。ヒントを失うと素の
  完了報告に縮退する — 消失ではなく情報の欠落。
- **異常終了（exit）は ExitInfo をレベルで読む**ため、設計どおり穴 G（managed daemon の
  異常死が報告されない）も Phase 1 の時点で閉じた。
- 配送は deliver-then-consume。会話が消えていれば arm を畳み（配送先が無い）、追記に
  失敗したときだけ再試行する。プロセスが「追記成功→消費」の間で落ちると報告が重複し
  得るが、Phase 1 の 1bit ではそこまで — 重複は Phase 2 の行ID冪等で消える。
- v1 回帰テストは意味論を引き継ぐ: DeferredWhileSubagentBusy → busy 証拠、
  WaiterIgnoresFalseIdle → 「無マーカー=不明」、HaltDisarmsOnlyWhenFlagged →
  cancelled、DeliveredAfterHealWipedMarker → ヒント喪失時の tick 回収。

### Phase 2 実装メモ（2026-07-29 / `chat_report_ledger.go`）

設計との差分・実装で決めたことだけを記す。

- **台帳はセッション1ファイル**（`instr-ledger/<session>.json` に `rows` 配列）。行を
  ファイル1枚ずつにしなかったのは、判定が常に「そのセッションの未報告行**全体**」を
  必要とするから（最古カーソル・畳み込み・cancel は行の集合演算）。read-modify-write は
  セッション単位の in-process mutex で直列化する。
- **どの行が「この静穏」で完了したかは証拠の時刻で切る**。settle は「セッションが静穏か」
  というセッション単位の判定だが、報告義務は指示単位なので、idle マーカー（または
  ExitInfo）**より後に投入された指示行はその静穏では完了になり得ない** — 行は pending の
  まま残り、次のターンの終端で改めて報告される。これがキュー投入（穴A）の解消の実体で、
  「1bit の上書き」という現象そのものが定義から消える。逆に、証拠に覆われる行が複数
  あれば1通に畳む（`instrFoldNote` が「指示N件ぶん」と各 delivered_at を添える。
  1件のときは本文を1文字も変えない）。
- **カーソルは秒精度の RFC3339 のまま**（Phase 1 の arm 時刻と同じ意味）。比較相手の
  status マーカーが秒精度なので、カーソルだけを nano にすると「投入と同じ秒に終わった
  速いターン」がマーカー < カーソルに見えて永久に settle しない。kind 別の濃いカーソル
  （jsonl サイズ・ターン連番）は将来課題のまま。
- **配送の冪等キーは「行ID＋reopen 世代」**。行IDだけにすると、補償で開き直した行
  （Phase 3）が「配送済み」と誤判定されて本完了の報告を握り潰す。会話メッセージ側に
  `instr` として持たせ、シンクは会話ロック下でこれを照合してから追記する。
  「追記は成功したが台帳を進める前に落ちた」窓は再送で二重投稿にならず、行だけが進む。
- **interim（質問 / プラン）は既報を刻むだけで抑止しない**。設計本文は「行ごとに1回」と
  書いたが、1つの指示の中で質問が2回起きるのは普通で、2問目を握り潰すとオペレーターが
  答えられなくなる（外部契約「interim 非消費」の実質を壊す）。状態は
  `pending → interim_reported` へ進むが行は open のままで、完了報告の義務は残る。
- **reopen（reported → reopened）は台帳側だけ実装**。遷移を引く検出（reported 行の
  grace 監視で busy が復活したか）と訂正 notice は Phase 3。ここを Phase 2 で入れておく
  のは、状態機械の出口が無いと Phase 3 がスキーマ変更から始まるため。上限は行あたり2回。
- **移行は起動時に1回**（`migrateReportArms`）。`armed=true` の v1 ファイルを1行へ変換し、
  変換元を消す（再起動のたびに行が増えないため）。代償は「Phase 1 バイナリへ戻すと
  移行済みの未完了指示は報告されない」こと — 二重報告より軽い方に倒した。
- **穴C の残り（TUI 由来 kind の live state 再導出）は TODO**。kiro / cursor は Stop 相当の
  フックを持たず、idle 証拠が TUI 文字列契約に依存する。2 tick デバウンスで「1回の誤読」は
  落ちるが「契約ドリフトで常時 idle に見える」状態は落ちない。恒久策は各 kind の live
  state（`AtIdlePrompt` 等）を**リコンサイラ側で再導出して busy 証拠にも足す**こと
  （現状 busy 証拠は claude 由来のシグナルに偏っている）。Phase 3 か kind 追加時に着手する。
- v1 回帰テストは Phase 1 の読み替えを維持したまま、arm ではなく行の状態を見るように
  だけ書き換えた（`sessionReportPending` / `awaitReported`）。追加は台帳の状態機械・
  キュー投入で後行指示が残ること・配送の冪等性・移行の4本。

### Phase 3 実装メモ（2026-07-29 / `chat_report_reconcile.go` + `session_selfreport.go`）

設計との差分・実装で決めたことだけを記す。

- **補償の作業集合は sweep の第2ラインにした**。リコンサイラの tick は「open 行を持つ
  セッション」に加えて「grace 内の reported 行を持つセッション」も回る（両者は同じ
  readdir 1回で分ける）。補償はデバウンスを持たない**単発観測**でよい — busy は
  positive な証拠で、誤って開き直したときの代償は「報告がもう1通増える」だけだから。
- **復帰の判定は settle 述語の裏返しではなく別述語**（`evalReportResumed`）。busy 証拠の
  列は同じだが、「**報告より後の**証拠か」という条件が1つ増える。マーカー系はその条件を
  書込み時刻で切る。鮮度ベース（サブエージェント・転写・ペイン）は、報告が出た時点では
  必ず false だった（busy 証拠がゼロでなければ報告は出ない）ので、いま true なら新しい
  書込みがあったということになり、追加の条件は要らない。
- **訂正は notice ではなく report ロールで配る**。設計本文は「訂正 notice」と書いたが、
  notice は provider コンテキストへ再生されない（ADR 0033）ので、既に「完了しました」と
  利用者へ伝えたオペレーター自身に訂正が届かない。report ロールなら既存の配送・冪等・
  自動ターンにそのまま乗り、`kind=reopened` として本文だけが違う。
- **訂正の冪等キーは「行ID＋reopen 世代＋`~reopen`」**。完了報告と同じ鍵にすると訂正が
  「配送済み」に化け、世代を1つ進めた鍵にすると開き直した行の**本完了**を握り潰す。
  名前空間を分けるのが唯一の正解だった。
- **訂正が名指しする「いつの報告か」は会話メッセージから取る**。台帳の `reported_at` は
  reopen した瞬間に消えるので、そこから読むと訂正の再試行や2回目の補償で参照先が無い。
  会話側の報告カード（行IDを `instr` に持つ）は訂正の対象そのものなので消えようがない。
- **自己申告の受け口は既存の kick（`POST /chat/report`）**。`kind=self-report` を
  リコンサイラの `hint` seam へ流し、`idleEvidence()` にマーカーと同格の証拠として
  足すだけ — 新しい配送経路も新しい永続化も作らない。証拠が自己申告だけのときは、
  行を覆う「証拠の時刻」も申告時刻を使う（申告より後に投入された指示は巻き込まない）。
- **自己申告は busy 証拠より強くしない**。早呼びは settle しないまま保留され、busy が
  晴れた最初の tick で配送される。保留中も申告は捨てない（`resetSettle` は落とさない）—
  捨てると「モデルが最後の1トークンを吐いた直後に呼んだときだけ効く」ものになる。
  保持はプロセス内メモリのみ: agent が落ちて失うのは「2 tick が 1 tick になる」高速化
  だけで、報告の有無はディスク上の台帳が持っている。
- **ツールの配り方は mcpreg の組み込み `af`**（`Targets{Session: true}`・接続情報を持た
  ないので常に ready）。Phase 3時点の`workspace-agent mcp-stdio --self-report`は`af_report` 1本だけを
  広告した。2026-08-02にdocs/53の直接handoff契約を満たすため、現行builtinは
  `--self-report --chromium-attach`で起動し、`af_report`＋Chromium Attach View 7種だけを広告する。
  `--self-report`単独の1本限定は後方互換として残り、他のフリートread/writeは広告・推測callとも拒否する。
  専用の別配線にしなかったのは ADR0031 決定6（レジストリは1つのリスト）の
  ため — 自前サーバーだけ materialize の外に置くと、利用者から見えず名前衝突の調停からも
  外れる（`af` は元から予約名）。CLI を持たない kind（shell / ssm）には配られないので、
  プロンプトへの注入もその kind では行わない。
- **注入文は日英併記**。docs/30 の自動再開文言と同じ理由で、英語で作業しているセッションへ
  日本語だけを流し込むと以後の出力言語が引きずられる（セッションごとの言語を読む術が無い）。
  「出力言語を変えるな」も明示している。
- **自動再開カウンタ（docs/47）はセッション単位で1回**に直した。配送は会話ごとに1通なので、
  カウンタを配送側（`recordSessionReport`）で進めると、2つのオペレーター会話から指示されて
  いるセッションが中断1回で上限（2回）に届く。数えるべきは「中断報告を配った」という
  セッションのイベント1つなので、会話ループの外で1回だけ動かす。
- 追加テストは補償4本（busy 復帰で reopen・新指示があれば reopen しない・上限2回で打ち切り・
  訂正の冪等性）＋純関数2本（grace/新指示の候補選び・復帰証拠の表）＋ファストパス5本
  （hint seam の配線・1 tick 短縮・申告なしでも 2 tick で拾う・早呼びの保留・マーカー無しの
  idle 証拠）＋カウンタ1本。

## トレードオフ / 受容するもの

- **レイテンシ**: 実測どおりに言うと、**ヒントが生きていても従来同等ではない** —
  v1 は Stop kick でその場で配送していたので、settle デバウンス（2 tick 連続＋tick 間隔
  ぶんの経過）が入るぶん**全ての完了報告が 15〜30s 遅れる**。通知センター側の
  「応答あり」通知は従来どおり即時なので、利用者からは「通知は出たのにオペレーターの
  報告カードは十数秒後」という時差として見える（オペレーターの自動ターン自体が数十秒
  かかるので、後続処理の体感差は小さい）。ヒントが死んだ場合も遅延は同じ 1〜2 tick で、
  **上限は v1 と同じ 90s**（transcript 鮮度の TTL）から悪化しないことをテストで固定する。
- **Bash run_in_background**（穴E）は busy 証拠に入れない判断を継続する（常駐 dev
  サーバとの区別が原理的に付かない — docs/30 の受容理由のまま）。ただし Phase 3 の
  補償により「誤完了報告→続行検出→訂正」に落ちる。`CLAUDE_CONFIG_DIR/tasks/` を
  busy 証拠に使えるかは別途調査（将来課題）。
- **補償は「新指示の有無」しか見ない**（Phase 3 実装）。報告のあとに**利用者が Console で
  直接**打った入力でセッションが動き出した場合、それは指示行を作らないので誤報告として
  訂正されてしまう。誤検知の代償が「訂正1通＋完了報告がもう1通」で、見逃し（誤完了が
  残る）より軽いこと・grace が 10 分に限られることから、この非対称は受容する。
- **複雑度の移動**: イベント処理の散在 if 文 → 状態機械＋証拠テーブル。総量は増えな
  い見込み（waiter・保留・世代調停の削除と相殺）が、性質がテーブル駆動テスト向きに
  変わることが本質的な利得。

## 追補（2026-07-30）— 実測で出た 3 つの穴

オペレーター会話 `aduznyc` の 1 日で、同じ指示に対して「完了 → 訂正 →（同内容の）完了」が
6 セット飛び、自動応答の上限（10 回）まで消費した。原因は補償の誤発火 2 種と、settle の
早発 1 種。いずれも**述語の入力が実態とズレていた**もので、述語の構造自体は変えていない。

### (1) 転写の鮮度が mtime だった

`TranscriptBusy` はファイルの mtime を 90 秒窓で見ていた。claude はターンと無関係な
記帳行（`system/away_summary`・`last-prompt`・`custom-title`・`agent-name`・`mode`・
`permission-mode`・`file-history-*`）を後から追記するので、静止したセッションが
「実行中」に化け、報告済みの行が補償に「働き出した」と誤読された。

- sp2qemx: 09:56:56 の完了以降 10:37 まで user/assistant 行ゼロなのに 10:06:19 reopen
- s2bl5pv: 09:55:11 `away_summary` → 8 秒後に reopen
- sannme2: 10:02:58 `away_summary` → 6 秒後に reopen

対処: 鮮度を**最後の user/assistant 行の timestamp**で測る（`claude.TranscriptTouched`）。
除外リストではなく許可リスト — 記帳行の種類は版ごとに増減するので、知らない type は
既定で無視される側へ倒す（docs/47 の `AbortedTurn` と同じ作法）。`isSidechain` は含める
（旧版はサブエージェントのターンをメイン転写へ inline で書いており、実行中の証拠）。

### (2) 完了の遅着を「再開」と読んでいた

`evalReportResumed` の証拠が鮮度だけのとき、**報告した完了そのものが数秒後に書かれる**
ケースを再開と読む。sannme2 は 09:59:34 報告 → 09:59:50 に本物の回答＋Stop → 10:00:08
「まだ作業中です」という嘘の訂正 → 10:00:34 同内容を再報告。

対処: 報告より後に「ターンが終わった」証拠（終端 idle マーカー / 転写末尾の中断）が
あるときは、鮮度を見ずに再開なしと判定する。本当に再開していれば最新のマーカーは
working / question 側になるので、そちらの列で従来どおり拾える。

### (3) 自己申告の早呼び

sannme2 は 09:57:29 に `af_report` を呼んでから最終回答を 2 分 22 秒書き続けた。その間
転写は 142 秒沈黙（鮮度 TTL 90s 超）、ペインのスピナーもエージェント表示で読めず、busy
証拠が全部消えて早すぎる完了報告が出た。「申告は busy より弱い」だけでは、busy 証拠が
鮮度しか無い局面を守れない。

対処: 申告**だけ**を根拠に完了とするときは、申告から `selfReportSettleDelay`（3 分）
以上静かなままであることを要求する。正常系（申告 → 直後に Stop）はマーカーで即 settle
するのでこの遅延を踏まない。窓が効くのは「マーカーが最後まで来ない」＝申告が唯一の
手掛かりのときだけ。

### 併せて入れたもの

- **中断のレベル読み**（docs/47 §4-2）: マーカー不在でも中断を報告できるようにした。
  idle 証拠に `abort` が加わる。
- 中断が末尾にあるときは転写の鮮度を busy 証拠から下ろす（その新しさは中断レコード
  自身のもので、進行中の証拠ではない）。

## テスト戦略

- 述語: シグナル組合せ × kind のテーブル駆動（idle/busy 証拠の全交差の代表列）。
- リコンサイラ: fake clock で tick を進める時間駆動テスト（settle デバウンス・
  reopen 上限・畳み込み・シンク失敗の再試行）。
- 移行: Phase 1 で v1 回帰テスト群を上表の読み替えで全維持。
- E2E: sqmconc シナリオ（AUQ → BG エージェント → チェーンターン → 誤 idle ヒール
  注入）の再現ハーネスを claude 実機で1本。
