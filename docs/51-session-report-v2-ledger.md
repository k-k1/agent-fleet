# 51. セッション報告 v2 — 指示台帳とレベル駆動リコンサイラ

- 状態: **移行中** — Phase 1（判定の一本化）実装済み（2026-07-29）/ Phase 2・3 は設計のまま。
  [docs/30](30-session-report.md) の報告機構の後継設計。
- 決定記録: [ADR 0035](decisions/0035-session-report-v2-ledger.md)
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
      "state": "pending|reported|reopened|cancelled",
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
  デバウンスになる（穴Cの一律解消）
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
1. 会話へ訂正 notice（「先の完了報告は早計でした — セッションは続行中です」）を追記し、
2. 行を `reopened` に戻す（本完了で改めて報告される）。

reopen は行あたり2回まで。上限到達時は「判定が振動している」事実を利用者向け文言で
報告して打ち切る（docs/47 の自動再開上限と同じイディオム）。これで v1 の
「**誤消費＝回復不能**」という非対称が「誤報告＝訂正付きで自己修復」に変わる。

## フック＝ヒント化

`session-status` hook・notify seam・record-exit は**リコンサイラを起こすだけ**にする。
- 実装上は既存の `POST /chat/report` を残し、ハンドラの中身を「wake 送信」に置き換える
  （hook スクリプト・焼き込みイメージの変更を不要にする）。
- フックが全部死んでいても最悪 1 tick 遅れで拾う。agent 再起動中の kick 消失（穴F）は
  台帳が残っている限り自然回復する。

## 自己申告ファストパス（Phase 3・opt-in）

指示プロンプトに「完了したら `af_report_done` を呼ぶ」を注入し（mcp-registry 経由で
全 kind に配布可能）、呼ばれたらヒント起床＋idle 証拠の1つとして数える（2 tick 要求を
1 tick に短縮）。**意味的完了を直接測る唯一の手段**だが、呼び忘れ・早呼びがあるため
単独では backbone にしない — リコンサイラが安全網。申告はタイミング信号のみで、報告
本文は従来どおりサーバ生成（fact-only — prompt injection 面を増やさない）。

## v1 からの移行

| Phase | 内容 | 閉じる穴 | 撤去するもの |
|---|---|---|---|
| 1 ✅ | 判定の一本化: kick の即消費をやめ、arm bit のままリコンサイラが settle 述語（2 tick デバウンス込み）で消費判定 | C, D, F | deferReportWhileBackgroundBusy / waitReportUntilBackgroundDone / reportWaiters（waiter 特例が述語に畳まれる） |
| 2 | 台帳置換: arm 書込み箇所（create_session / /input / /turn / bridge / スケジューラ）を行追加へ。disarm → cancelled。起動時に既存 `armed=true` を1行に変換 | A, B | session-report/*.json（読み替え互換の後に削除）・consumeReportArm |
| 3 | 補償 reopen ＋ 自己申告ファストパス | E の実害（誤報告→訂正に縮退） | — |

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

## トレードオフ / 受容するもの

- **レイテンシ**: ヒントが生きていれば従来同等、死んでいても +1〜2 tick（〜60s）。
  v1 waiter の 90s 待ちより悪化しないことをテストで固定する。
- **Bash run_in_background**（穴E）は busy 証拠に入れない判断を継続する（常駐 dev
  サーバとの区別が原理的に付かない — docs/30 の受容理由のまま）。ただし Phase 3 の
  補償により「誤完了報告→続行検出→訂正」に落ちる。`CLAUDE_CONFIG_DIR/tasks/` を
  busy 証拠に使えるかは別途調査（将来課題）。
- **複雑度の移動**: イベント処理の散在 if 文 → 状態機械＋証拠テーブル。総量は増えな
  い見込み（waiter・保留・世代調停の削除と相殺）が、性質がテーブル駆動テスト向きに
  変わることが本質的な利得。

## テスト戦略

- 述語: シグナル組合せ × kind のテーブル駆動（idle/busy 証拠の全交差の代表列）。
- リコンサイラ: fake clock で tick を進める時間駆動テスト（settle デバウンス・
  reopen 上限・畳み込み・シンク失敗の再試行）。
- 移行: Phase 1 で v1 回帰テスト群を上表の読み替えで全維持。
- E2E: sqmconc シナリオ（AUQ → BG エージェント → チェーンターン → 誤 idle ヒール
  注入）の再現ハーネスを claude 実機で1本。
