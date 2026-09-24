# 109. lcpp セッションが空 model で作成され、初回ターンが黙って失敗する

- 依頼: `[agent-fleet:spawn from=sjqlfyj]`。親が `svcnyrc` で再現を確認済み: `GET
  /sessions/svcnyrc/settings` が `model:"", dynamicModel:true` を返し、transcript は
  user メッセージ 1 件のみ（assistant/usage/error 無し）。generate_image は別件（親が別途調査）。
- 関連: [0093](../decisions/0093-lcpp-agent-kind.ja.md)（lcpp kind 本体・decision 7「エンジン目録が
  そのままモデル一覧」・`role: "router"` の実測）/[107](107-lcpp-member-endpoint.md)（`lcppMemberReachable`
  の 2026-09-21 追記——単機 llama-server は request の `model` 欄を読まない、という実測の出典）。

## 前提の訂正（レビュー指摘）

初稿は「llama-server は常に model file の指定を要求する」という書き方をしたが、これは誤り。
`docs/log/107` の 2026-09-21 追記（`workspace/agent/engines.go` の `lcppMemberReachable` の
コメント）が実機で確認した事実はその逆で、**単機 llama-server は request 自身の `model` 欄を
一切読まない**（任意の文字列、あるいは無指定でも 200 を返し、実際にロードされている 1 個のモデルが
答える）。`model` 欄が実際に効くのは `--models-max` で複数モデルを同居させた **router 配備**
だけ（0093 decision 7 の実測、`GET /engine/llm/props` が `role: "router"` を返すケース）。

したがって「model 必須」は llama-server というワイヤプロトコルの制約ではなく、**この Agent 自身の
設計判断**である。理由は 3 つ:

1. **router 配備では実際に配送先を決める** — 単機なら無視されるが、router では `model` が
   どの下位モデルが答えるかを決める。配備がどちらのモードかは Agent 側から見分けられない
   （`docs/log/107` の同コメント）。
2. **カタログ契約の単位そのもの** — ADR 0093 decision 7「エンジン目録がそのままモデル一覧」:
   lcpp には claude のような固定ティア別名が無く、`GET /agents/lcpp/models` が返す一覧の
   どれか 1 件を指すことが、この kind の「モデルを選ぶ」という行為の全て。
3. **表示・追跡の唯一の記録** — `engines.go` の `lcppMemberReachable` が指摘する通り、メンバーが
   同じ URL の裏で LAN の箱を差し替えても単機 llama-server は黙って応答を続ける。セッション
   Meta.Model が「この会話がどのモデルのつもりで話しているか」を記録する唯一の場所であり、
   これが空だと Console の context bar / model バッジ（`agent.go` の `WireLive`）も何も言えない。

## 原因

lcpp は codex/opencode と違い「CLI 自身の既定モデル」という概念が無い（1 の router 配送先問題）。
ところが:

- Console 側（`console/src/lib/agentModels.ts` の `fetchModels`）は他の動的 kind と同じ
  `defaultOnly()`（空文字列＝Default の選択肢）を lcpp にも先頭に足していた。
- `console/src/lib/repoLast.ts` の `resolveModel` は動的 kind の最終フォールバックを
  `""`（CLI の既定に任せる）にしていた——lcpp にはその「CLI の既定」が存在しない。
- `console/src/features/repos/useStartWork.ts` は `model` が空文字なら単に POST の `model`
  欄を送らない（`if (hasModel && model) body.model = model;`）。
- サーバ側 `workspace/agent/internal/sessionx/session_handlers.go` の
  `HandleCreateSession` は lcpp について model の有無を検証しておらず、空のまま worktree
  作成・`mcpx.StartManagedSession` まで進んでいた。
- `workspace/agent/internal/agents/lcpp/driver.go` の `runTurn` は `st.AppendUser(in.Prompt)`
  で user メッセージを保存した**後**に `model == ""` を検出して `TurnFailed` にしていた
  （`Store` に何も残さず、ログにだけ書いていた）。

結果、空 model の lcpp セッションが作成でき、最初の発言が保存されたまま以降何も起きず、
Console からは「送ったのに何も返ってこない」ようにしか見えなかった。

## 修正

### 1. Console: lcpp には「空の Default」を選択肢に出さず、カタログ確定後に具体モデルへ自動選択

- `agentModels.ts`: `requiresConcreteModel(kind)`（今のところ lcpp のみ）を追加。
  `defaultOnly(kind)` は該当 kind に対して空配列を返す（Default 選択肢そのものを作らない）。
- `useAutoConcreteModel(kind, model, onChange)`: 対象 kind でカタログが 1 件以上そろい、かつ
  現在の選択が空なら先頭のモデルを自動選択する新規フック。`LaunchModal.tsx` /
  `StartModal.tsx`（home ステージ）/ `AgentCardParts.tsx` の `LaunchDefaults`（設定の既定値行）
  の 3 箇所すべてに配線。
- `resolveQuickLaunchModel(kind, resolved)`: クイック起動（`RepoRowConnected.tsx` の ▼ / 右クリック
  メニュー、マウントされたピッカーが無い経路）向けの非同期版。`resolveModel` が空を返したときだけ
  カタログを 1 回 await して先頭を採用する。
- `ModelPicker.tsx`: 「カタログに無い今の選択を救済して選択肢に足す」処理が空文字列に対しても
  発火して `["", ""]` という空欄行を一瞬表示していたのを、空モデルは救済しない（`!model` で
  ガード）よう修正——Default 行が無い kind でも空欄が見えないようにするための副次修正。
- 起動ボタンは lcpp について model 未確定（カタログ読み込み中 or 空）の間は disabled のまま。

### 2. サーバ: `HandleCreateSession` で lcpp の空 model を副作用前に 400 で拒否 + 明示 model の検証

- `session_handlers.go`: `NormalizeKind(req.Kind) == session.KindLcpp && strings.TrimSpace(req.Model)
  == ""` を、`lcpp_disabled` チェックの直後・worktree 作成より前で `400 bad_model` として拒否。
- 明示 model が付いている場合は、codex/opencode/copilot と同じ `resolveLiveModel` に通す
  （曖昧一致・打ち間違いの候補提示）。ただし比較対象のカタログはコンパイル依存を避けるため
  新設の関数変数 `sessionx.LcppLiveModels`（`internal/harness` の `EngineToken`/`EngineWindow` と
  同じ継ぎ目の形）経由——`engines.go` の `init()` で `agent_models.go` の `lcppModels` を代入する。
  カタログが引けない（nil）場合は `resolveLiveModel` 自身の「空カタログなら通す」規約に従い、
  明示 id をそのまま受理する（router/メンバー接続が一時的に応答しない場合に、届かないことを
  「そのモデルは存在しない」と誤読しないため）。

### 3. Driver: 検証順は変えず、失敗を transcript に残す

「AppendUser を先に確定させる」という既存の順序（承認拒否や engine 到達不可でも同じ形）は
変えない——先に確定させないと、一時的に engine が起きていないだけのケースでユーザーの入力
そのものが消える。代わりに `store.go` に `NoteTurnError`（`KindSystemNote` の新しい `Note`）を
追加し、`Transcript()` が `Part{Kind:"error"}` を持つ assistant ターンとしてレンダリングするよう
にした——codex/opencode の `errors.go` が使っている `Kind:"error"` と同じ Console 側の表示経路。
`driver.go` の 3 箇所の事前条件失敗（model 未設定・`harness.EngineToken` 未配線・engine 到達不可）
はすべて新設の `failTurn(st, msg)` を通り、`TurnFailed` にする前に必ずこのノートを残す。

## 検証

初稿はここを `npm test 2>&1 | tail -150` / `go test ... 2>&1 | tail -30`（バックグラウンド化された
結果をそう表示していた）で確認したと書いていたが、これは `tail` の exit code を拾ってしまう
（AGENTS.md「パイプ無し exit code」）——しかも Console と Go の重い試験を同時に走らせていた。
レビュー指摘を受け、パイプ無し・逐次（Console 完了後に Go を開始）でやり直した実際のコマンドと
exit code は次のとおり（すべて `echo $?` で直接確認、`| tail` は使っていない）:

- `cd console && npm test`（vitest run。`vite.config.js:132` の `maxWorkers: 2` が worker 上限を
  既に絞っているので追加フラグ無し）→ **exit 0**、`Test Files 314 passed | 1 skipped (315)` /
  `Tests 3320 passed | 1 skipped (3321)`（新規 20 件超含む：`agentModels.dom.test.tsx`・
  `LaunchModal.dom.test.tsx`・`StartModal.dom.test.tsx`・`LcppCard.dom.test.tsx`）。
- （Console 完了後に）`cd workspace/agent && go build ./...` → **exit 0**。
- `go vet ./...` → **exit 0**。
- `go test ./... -count=1 -p 2` → **exit 0**、`Go test: 3949 passed in 51 packages`
  （新規 9 件含む：`session_lcpp_model_test.go` 6 件・`driver_test.go` 3 件）。
- `out=$(gofmt -l .); echo "[$out]"` → `[]`（出力を読む。exit code だけでは未整形を見落とす）。
- 既存の `session_lcpp_managed_test.go` / `session_lcpp_toggle_test.go` /
  `session_managed_only_test.go` は、lcpp を model 無しで POST していたため今回の 400 ガードで
  赤くなった——`model: "test-model"` を足して修正（意図した回帰）。
- 変異試験: `requiresConcreteModel`・`canLaunch`（LaunchModal）・`homeModelPending`
  （StartModal）・`useAutoConcreteModel` の呼び出し（AgentCardParts）を一時的に無効化 or
  真偽反転し、対応する新規試験が赤に落ちることを確認して Edit で戻した。

既知の赤（本件と無関係と確認済み、単独 `-count=5` の生の結果）:

- `cd internal/agents/lcpp && go test -run 'TestDriverSendPersistsTurnAndCompletes' -count=5 -v .`
  → **exit 1**、`1 passed, 4 failed`（毎回 `driver_test.go:149: unexpected records: []`）。この
  試験は本件で一切触っていない既存試験——原因はグローバル `handles` map
  （`internal/agents/lcpp/driver.go` の `handles map[string]*threadHandle`）がセッション名
  だけをキーにしており、`-count=N` が同一プロセス内で同じ名前のテストを繰り返すと、2 回目以降の
  `Resume` が 1 回目の（`t.TempDir()` が既に片付けたディレクトリを指す）ハンドルを再利用して
  しまうこと。
- 同じ原因で、本件の新規試験 3 本（`TestDriverSendWithNoModelRecordsVisibleError` /
  `TestDriverSendWithNoEngineSeamRecordsVisibleError` /
  `TestDriverSendWithUnreachableEngineRecordsVisibleError`）も単独 `-count=5` では
  **exit 1**、`3 passed, 12 failed`（同じ `records = [], want [user, turn-error note]`）になる
  ことを確認した——新しい壊れ方ではなく、上と同じ既存の仕組みに同じ形で乗っているだけ。
  `-count=1`（通常の CI・上の全体実行）では両方とも緑。

## 未解決 / 範囲外

- RepoRowConnected（クイック起動 ▼ / 右クリック）は `resolveQuickLaunchModel` を呼ぶだけの
  1 行の配線——`RepoRailContext` 一式を組み立てる専用のマウント試験は費用対効果が低いと判断し、
  `resolveQuickLaunchModel` 自体の単体試験（`agentModels.dom.test.tsx`）とコードレビューに留めた。
  空 model が万一この経路をすり抜けても、サーバ側の 400 ガードが最終防衛線になる。
- 上記「既知の赤」の `handles` map テスト分離問題自体の修正は本件の範囲外（別件として起票が必要）。
  → Issue #952 に起票（2026-09-24）
- generate_image まわりは親セッションが別途調査。
