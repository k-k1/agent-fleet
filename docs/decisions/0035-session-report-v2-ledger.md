# 0035. セッション報告 v2 — エッジ駆動＋1bit arm を捨て、指示台帳＋レベル駆動リコンサイラへ

- 状態: **採用・実装済み**（2026-07-28 決定 / 2026-07-29 に Phase 1「判定の一本化」・
  Phase 2「台帳置換」・Phase 3「補償 reopen ＋自己申告ファストパス」を実装）。
  設計本文は [51-session-report-v2-ledger.md](../log/51-session-report-v2-ledger.md)。
- 関連: [docs/30](../log/30-session-report.md)（v1 の設計と事故史 — 専用 ADR は無い）/
  [0030](0030-turn-abort-auto-resume.md)（中断分類 — v2 の述語に吸収）/
  [0015](0015-agent-managed-driver.md)（notify seam — v2 でヒント化）

## 背景

v1（docs/30）の報告機構は「Stop フック等のエッジを1回捕まえ、不可逆な1bit（arm）を
消費して報告する」構造。saga5uc（BG起動直後 Stop の早期消費）→ 保留 waiter を追加、
sqmconc（waiter が誤 idle ヒールの窓で早期消費）→ 配送3条件を追加、とパッチを重ねたが、
並行性の縫い目が増えるたびに「機械的 idle ≠ 意味的完了」の新しい窓が生まれる。
残る既知の穴（キュー投入の指示潰れ・消費の世代レース・TUI ポーリング kind の無防備・
consume-then-deliver の配送消失・agent 再起動中の kick 消失）はいずれも
**同一性（1bit）・検出（エッジ推定の一発勝負）・配送（不可逆消費）**のどれかに帰着する。

## 決定

1. **指示の同一性を台帳の行にする。** arm の1bitを廃止し、指示1件=1行
   （id・conv・投入時刻・進捗カーソル・状態機械 pending/interim_reported/reported/
   reopened/cancelled）。
   複数指示の重なりは「潰れる」ではなく「settle 時に1通へ明示的に畳む」。
2. **検出をエッジからレベル（状態収束）にする。** サーバ内の単一リコンサイラが
   tick＋ヒント起床で pending 行を再評価する。settle は「idle 証拠≥1 ∧ busy 証拠=0 を
   2 tick 連続」— 「無マーカー＝idle」の既定を廃し、不明は不明として扱う。
   誤「まだ」は次 tick で自己修正。フック・notify seam・record-exit は起床ヒントに降格し、
   取りこぼしは消失ではなく遅延に縮退する。
3. **配送をシンク側の冪等化にする。** 会話ロック下で行IDにより重複排除し、追記が
   成功した時だけ台帳を進める。検出側から「1回だけ」責務を外す。
4. **誤「完了」は補償で回復可能にする。** reported 行を grace 期間監視し、新指示なしの
   busy 復帰で訂正報告（report ロール — §影響のとおり notice ではない）＋reopen（上限2回）。
   「誤消費＝回復不能」の非対称を崩す。
5. **自己申告はファストパスに留める。** `af_report` MCP ツール（Phase 3）は idle 証拠の
   1つ＋起床ヒントであって backbone ではない。busy 証拠より強くもしない（早呼びは保留）。
   報告本文は従来どおりサーバ生成の事実のみ。
6. **段階移行。** Phase 1=判定一本化（arm bit のまま・waiter/保留特例の撤去）、
   Phase 2=台帳置換、Phase 3=補償＋自己申告。各 Phase 独立にロールバック可能。
   報告本文・interim・自動ターン・disarm 規約という外部契約は不変。

## 捨てた案

- **エッジ＋arm の逐次強化の継続**: sqmconc 対策（3条件）で窓は狭まったが、縫い目が
  増えるたびに同型のパッチが要る。誤消費が構造的に回復不能である限り終わらない。
- **自己申告を backbone にする**: 意味的完了を直接測れる唯一の手段だが、モデルの
  呼び忘れ・早呼びに全体の正しさを賭けることになり、kind 横断の確実性が出ない。
  ファストパスとしてのみ採用。
- **毎 Stop 報告＋オペレーター(LLM)側で重複判断**: 正しさをモデル判断に押し付け、
  報告スパムと自動ターン消費が増える。「指示1件=報告1回」の契約も崩れる。
- **オペレーター会話側の定期ポーリング（get_session_status を自動ターンで回す）**:
  LLM ターンを恒常消費する。判定は安価な機械側でやるべき。
- **プロセスツリー（BackgroundBusy/BackgroundShellBusy）を busy 証拠に入れる**:
  常駐 dev サーバ・監視ループと区別できず永久保留を生む — v1 の受容理由を維持。

## 影響

- 検出ロジックが1箇所（リコンサイラ＋証拠テーブル）に集約され、新 kind の受け入れ
  条件が「表を埋める」に明文化される。TUI 文字列ドリフトは報告消失ではなく遅延になる。
- ヒント喪失時のレイテンシは +1〜2 tick（〜60s）。v1 waiter の 90s 待ちより悪化しない
  ことをテストで固定する。
- `session-report/*.json`・waiter・世代調停コードは Phase 2 完了時点で撤去される。
  （2026-07-29 実装: arm ストアは起動時の移行 `migrateReportArms` が読むだけの残骸になり、
  `consumeReportArm` / `reportArmMu` は消えた。指示の同一性は `instr-ledger/<session>.json` の
  行IDが持つ。）
- 誤「完了」は grace 10 分の監視下で `kind=reopened` の**訂正報告**（notice ではなく
  report ロール — notice はオペレーターの文脈へ再生されない）＋行の開き直しに縮退する。
  訂正の冪等キーは完了報告と名前空間を分け、訂正が指す「いつの報告か」は台帳ではなく
  会話メッセージから引く（`reported_at` は reopen で消えるため）。
- 自己申告は組み込み MCP サーバー `af`（`workspace-agent mcp-stdio --self-report`）が
  CLIを持つ全 kind のセッションへ配られ、指示プロンプトに1行だけ注入される。受け口は既存の
  `POST /chat/report`（`kind=self-report`）で、新しい配送経路も永続化も増やしていない。
- 2026-08-02のChromium Attach View補正後、現行builtinは
  `workspace-agent mcp-stdio --self-report --chromium-attach`で起動し、`af_report`に加えて
  Chromium 7種だけを対話セッションへ広告する。`af_report`の意味・受け口は不変で、他のフリートtoolを
  推測callできない「広告集合がscope境界」という決定も維持する。`--self-report`単独は従来どおり1本限定である。
