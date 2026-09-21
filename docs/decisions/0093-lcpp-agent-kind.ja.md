# 0093. 自前 llama.cpp エンジンの上の自前ハーネスをセッション種別（`lcpp`）にする——CLI を持たない最初の kind を、kind 非依存の中核の後ろに段階化する

[English](0093-lcpp-agent-kind.md) | 日本語

- 状態: **採用**（2026-09-21）。段 2 は PR #816・#818・#821・#823・#824・#825・#826・**#829**
  （決定 8 の使用量 exact 化・Console 開放・MCP kind 許可リスト）で develop に入った。
  以下の `file:line` は `951bb402`（当時の develop）で読んだ。
  表ごとの棚卸しは `docs/log/99-lcpp-agent-kind.md` にあり、そちらが作業記録、本 ADR が判断と棄却案を持つ。
  🟢 **2026-09-19・段 0 の前にレビュー済み**（末尾の Review 節。別セッションが全アンカーを tree で読み直した）。
  初稿の前提が 2 つ覆り、本文中で「初稿は…と書いたが誤り」と印を付けて訂正した: `dispatchMCPStdio` は純粋な
  スイッチではない（背景・決定 6）、`ProcessModel` は Console のどこにも届かない（決定 4）。端末経路の門は
  3 箇所でなく 5 箇所だった（決定 2）。未決の問い 3〜5 はそこで閉じ、1〜2 は実機のみ。
  🟢 **2026-09-19 利用者が計画を承認**（決定 9 の段階化、段 0 から）。実装は親セッションが駆動・レビューする
  子セッションで進め、段ごとに PR で着地させる。段 2 がマージされた時点で *採用* へ移す。途中で止まればその
  場所を本 ADR に記す。
  🟢 **段 2 完了（2026-09-20 利用者が決定 9 の門(a)(b)(c)を通過と判定・2026-09-21 段 2 のマージ完了により
  採用へ移行）**。判定の中身と実機 14 本（番号は #1〜#14・#5 は 2 走行のため計 15 走行、さらに #15/#16
  を含めると番号は #1〜#16・走行は計 17）の実測は末尾「決定 9 の門の判定（2026-09-20）」、段 2 が必ず踏む
  既知の負債は「段 2 に持ち越す負債」節、段 2 の実装が決定文から変えたものと負債 9 件の現状は「段 2 の
  実装記録（2026-09-21）」節。決定 8（使用量 exact）と、Console での起動可能化は PR #829 で develop に
  入った——中身は「段 2 の実装記録」節に記録する。
- 依頼は一文: **ベンダーの CLI を駆動する代わりに llama-server の API を直接叩く自前ハーネスは、Agent Fleet の
  セッション種別になれるか、なるなら何がいくらか。**
- 関連: [0015](0015-agent-managed-driver.ja.md)（この kind が子プロセス無しで実装する managed driver の契約）/
  [0026](0026-kiro-agent-kind.ja.md)（kiro——直近の kind、「kind が触る場所」の雛形）/
  [0071](0071-self-hosted-inference-engines.ja.md)（エンジン・起床・gateway）/
  [0072](0072-engine-model-catalog.ja.md)（`context_tokens` が窓の片道 4 ホップ目になる目録）/
  [0079](0079-remote-engine-from-another-deployment.ja.md)（借用エンジン。その `engine_waking` を成功と読んではいけない）/
  [0084](0084-engine-indicator-and-tenant-gate.ja.md)（この kind の可否が乗るテナント別の門）

## 背景

### なぜ出てきたか

自前 llm エンジンの文脈量計算は何度直しても着地しなかった。真因は個々の直しではなく構造で、窓が
**片道 4 ホップ**——`store.EngineModel.ContextTokens` → active set の `c` → sidecar の preset → llama-server の
`--ctx-size`——で伝わり、何も帰ってこない。自分で書いた説明が `workspace/agent/internal/agents/opencode/window.go:27-33`
にある。埋まり具合は `usagex.WindowGuess`（`workspace/agent/internal/usagex/usage.go:75-89`）の推定で、自前
モデルの id を未知の非 Claude モデルと読んで 200,000 と答える。

うまく動いている参考は NousResearch/hermes-agent（Python・MIT）。llama-server を子プロセスとして所有し、OpenAI
互換 API を直接叩く。効いているのは式ではなく `agent/conversation_loop.py:419-437` の 1 本——圧縮の直前に、
これから送るトークン数を手に持ったまま、サーバの窓を伸ばしに行く。1 プロセスがサーバの窓と会話の長さを同時に
知っているから書ける行。**成長ラダーはうちには移植できない**: 彼らの bounce はローカルの再起動で秒、うちは ECS
タスクの入れ替えで分、しかも東京の GPU は OD でも枯れる。うちは窓を**余裕を持って 1 度だけ**決め、伸ばさない。

### llama-server が提供するもの（README で確認済み）

`POST /v1/chat/completions/input_tokens`（送信前の正確な数）、`GET /props`（`default_generation_settings.n_ctx`＝
実際に付いた窓）、`POST /v1/messages` と `/v1/messages/count_tokens`（Anthropic 互換）、`POST /slots/{id}?action=save|restore`、
`POST /v1/chat/completions/control {reasoning_end}`、`--reasoning-budget N` / `--no-reasoning-preserve`（既定は
preserve＝思考が履歴に積む）、`-fit`（既定 on。**未設定の引数しか動かさない**——`-c` を明示すると窓は触らず重みを
CPU に溢れさせる）。

**llama-server 内蔵の MCP クライアント（`--mcp-servers-config`）と `--tools` / `--agent` はここでは使えない。**
動くのは GPU コンテナの中で、うちのツールは Workspace 側でそのセッションの資格情報で動く必要がある。箱が違うので
原理的に委譲不可。`/tools` は "Please do NOT use this endpoint in a downstream application" と明記されている。
**MCP クライアントはうちが書く。**

### リポジトリに既にあるもの（実測）

- kind が実装する Go の契約: read 層 `Agent`（6 メソッド・`workspace/agent/internal/agents/agents.go:141-161`）と
  managed 層 `Driver` / `ThreadHandle`（7 メソッド・`driver.go:132-165`）。`Capabilities.ProcessModel` は
  `shared-daemon` / `per-session-child` / `tui` の 3 値（`driver.go:146`）。
- kind の登録は `session.go:19-29` と `sessionx/agent.go:28-38`。**未登録の kind は黙って `claude` に正規化される**
  （`agent.go:49`）＝拒否されない。managed driver は `sessionx/session_turn.go:31-37` の map。managed 専用の kind は
  今日は無い: create 経路は「managed が無い kind」の拒否（`session_handlers.go:649-668`）しか知らず、逆は無い。
- engine gateway（`control-plane/engine_gateway.go`）に**経路の許可リストは無い**。`serve()`（`:467`）は path を
  見ない。制限しているのは `/v1/` の 2 重適用——mux の `/engine/{key}/v1/{path...}`（`:211`）と `engineUpstreamPrefix`
  がさらに前置する `/v1/`（`:1172`）。よって `/v1/chat/completions`・`/v1/models`・`/v1/messages`（`:1086` に明記）・
  `/v1/messages/count_tokens`・`/v1/chat/completions/input_tokens`・`/v1/chat/completions/control` は通り、
  **`/props`・`/slots`・`/tokenize` は通らない**（llama-server の root にあり `/v1/props` は 404）。streaming には
  `stream_options.include_usage` が注入され（`:597`）、全リクエストが箱を買う demand 信号になる（`:540`）。
- `af` の MCP サーバは `dispatchMCPStdio(line []byte) []byte`（`workspace/agent/internal/mcpx/mcp_stdio.go:243`）。
  **初稿は「JSON-RPC 1 行の純粋なスイッチ」と書いたが誤り**: プロセス全体の許可集合（`parseStdioFlags`・`:131`）の
  ほかに、所有セッション・会話・Chromium・peer・画像・spawn の状態を読み、進捗と list-change の通知をプロセス共通の
  stdout writer へ非同期に書き、`tools/list` はプロセスで一度きりの watcher を起こす（`:79-210`・`:479-544`）。
  in-process で呼べるのは、通知出力と watcher の寿命まで持つリクエスト単位の dispatch 文脈を作ってからに限る。
- Go の MCP **クライアント**は無い。`mcpreg/probe.go` が stdio と Streamable HTTP で `initialize` → `initialized` →
  `tools/list` まで話して止まる（`:332-344`・`:483-498`）。`tools/call` を呼ぶのは e2e テスト 1 本だけ。
- 既存 kind の重さは非テスト src で 2,100〜5,950 行（agy 2,109 … codex 5,948）。

## 決定

### 決定 1——kind は `lcpp`。`native`・`llama`・`llm`・`llamacpp`・`engine`・`rovo` は使えない

`native` はデスクトップの Runtime アダプタ（`AF_RUNTIME=native`・docs/log/34・42）。軸は違うが、同名の kind は
ログと文書の全行を共有してしまう。`llama` は既に Meta に解決するモデル族の鍵（`workspace/agent/model_provider.go:68`）で、
Qwen を動かす kind が `llama` を名乗るのは利用者に嘘になる。`llm` はエンジンの key、`llamacpp` はエンジンの
provider id（`control-plane/engines.go:50`・`:81`）で、opencode のモデル id は `llamacpp/<model>` だから kind と
provider が同綴りになる。`engine` は箱。`operator` は origin（`session.go:45-53`）、`af` は MCP サーバ名の予約語
（`mcpreg/def.go:55`）、`rovo` は docs/log/74 が予約した第 9 種。

`lcpp` は llama.cpp の慣用略で、kind の実体——モデル族でなく llama-server の API（`/props`・`/slots`・reasoning
制御）に依存するハーネス——を言う。`session.KindLcpp`・`.kind-lcpp`・`short: "lc"`・`launchSuffix: "-lc"`
（`""/-cx/-cu/-ag/-cp/-ki/-oc/-sh` と非衝突）。label は `llama.cpp`。ブランドアイコンは無い（ハーネスの主体は
うち）ので shell/ssm と同じ codicon。10 色目は着手前に両テーマの実描画で確定する（docs/log/74 §8.2 が 3 つ目の
青でやったのと同じ）。

### 決定 2——`lcpp` は最初の managed 専用 kind。Terminal(CLI) 経路は無い

tmux のペインに入れるものが無い。`BuildLaunch` はエラーを返し、`POST /sessions` はこの kind で `driver` を
`managed` 既定にし、`POST /sessions/{name}/driver` は `tui` 行きを 400 で拒み、Console の descriptor に「端末経路
無し」のフラグを 1 つ足す。**初稿は読み手を 3 箇所と書いたが、Review で 5 箇所と分かった**（`LaunchModal.tsx:776-803`・
`StartModal.tsx:303-316`・クイック起動 `RepoRowConnected.tsx:178-184`・切替メニューと動作 `SessionMenu.tsx:179-190` /
`useSessionActions.tsx:262-287`）＋サーバ側の managed→TUI 遷移（`session_driver.go:62-105`）。引き継ぎモーダルは
generic の create 経路を使うので経路ラベルは要らず、この kind で managed を選ぶだけでよい。ペインの
無いセッションは新しくない——managed セッションは既にそう。新しいのは「二度とペインに戻れない」ことだけ。

REPL（`workspace-agent lcpp-tui`）を書けば Terminal 経路は作れる。ミラーが全部を映す kind に 2 つ目の UI を
書くことになるので、作らない。

### 決定 3——転写はハーネスが書く。1 レコードから 2 つの表現を導く

他の kind は CLI が書く店を読む。`lcpp` は自分の店を書く: `AgentDataDir()/lcpp/sessions/<sid>.jsonl`、
append-only、user ターン／assistant ターン（text・reasoning・tool_calls）／ツール結果／system ノート（圧縮・
モデル変更）／usage で 1 レコード。1 レコードから**次ターンで送る OpenAI `messages` 配列**（正史）と**ミラー用の
`transcript.Turn`**（正規化先はこれ一択——他の形にすると共有・マーク・変更ファイルが同時に落ちる）を導く。
読み手と書き手が同じコードなので、ミラー parity は構造的に完全。

追記単調は書き手が守る: 圧縮は system ノートと要約を**足す**のであって過去行を消さない。送信用 `messages` は
最後の圧縮ノート以降から組む。過去ターンの reasoning は送信用から落とし（llama-server の `reasoning-preserve`
既定はそのままだと積む）、ファイルには残す。fork と fork-at は anchor でこのファイルを切って新 sid に複製する
（claude と同じ）。経路が 1 つなので「この経路では fork できない」場合は無い。

### 決定 4——managed driver は in-process。`ProcessModel` の 4 つ目の値、Supervisor は無い

`Resume(m)` が返すのはハンドル（goroutine とチャネル）で、子プロセスではない。opencode Supervisor の骨格
（ensure / adopt / generation / drain）はどれも当てはまらない。残るのはターン goroutine の寿命、Agent 再起動時に
握っていたターンの settle（`TurnUnknown` → `Snapshot` が JSONL の末尾を読む: assistant レコードで閉じていれば
completed、tool_call で開いていれば aborted）、`shutdown.go` での ctx cancel。`Capabilities.ProcessModel` に
`"in-process"` を足し、`tuiMemoryCost` は空。**初稿は 4 つ目の値が Console の読み手を壊し得ると見ていたが、Review で
読み手は無いと分かった**: `ProcessModel` は Agent API に乗らず、managed driver が埋めるだけ（`driver.go:142-157`）。
値を足しても何も壊れず、同時に UI も駆動しない——決定 2 の端末経路の門は descriptor のフラグであって、この enum ではない。

ハンドルの 7 メソッド: **Send** はループ 1 周／**Steer** は次のツール境界に user メッセージを積む（`Queued` に
映す）／**Interrupt** は ctx cancel、思考中なら先に `POST /v1/chat/completions/control {reasoning_end}` で自然に
閉じさせてもよい／**UpdateSettings** は model・effort（`reasoning_budget`）・mode（plan＝書き込みツールを外す）を
次ターンから反映＝`DynamicModel`・`DynamicEffort`・`DynamicMode` は全部 true（llama-server router は
`--models-max` で複数モデルを同居させる）／**Respond** は `ask_user` ツールの戻り値／**Events**・**Snapshot** は素直。

状態は検出せず発生させる: ハーネスが `status.Persist(sid, working|idle|question)` を自分で書き、claude が使う
generic の `DriveState` 経路に乗る。語彙に無い状態が 1 つ——**箱の起床**（最初のターンで分単位）。v1 は `working`
＋「エンジン待ち（n 秒）」の last-say 行で出し、`waking` 状態は Console の語彙変更を伴うので後回し。クライアントは
最初のトークンが来るまで streamed の 200 を成功と読まない（ADR 0079 の潰れた `engine_waking`）。

### 決定 5——ツールはうちのもの。だから承認が本物になる

既存 kind のツールループ・組み込みツール（read / write / edit / bash / glob / grep / ls）・権限モデル・出力上限・
cwd 拘束・質問と計画のツール・system prompt の組立・圧縮は**全部 CLI の中**にあり、どの kind 契約にも出てこない。
`lcpp` では全部うち。これで買えるのは他のどの kind にも無いもの——**実行者がうちなので、承認は本当にツールを止める**。
`Permissions: true` と `Caps.PermissionChoice: true` を「実測済み」として宣言できる（docs/log/76 の条件）。
指示ファイル層（fleet notes・利用者指示・プロジェクトの `AGENTS.md` / `CLAUDE.md`）はハーネスが読んで system
prompt に載せる。順序は既存の fleet → user → project → rtk。書き先のファイルが無いので `instrSupportedKinds` には
入れない。rtk は bash ツールの exec の前段に挟むだけ——フックもプラグインもファイルも無い。スキルは foreign
（SKILL.md を注入で読ませる・docs/log/50 §8）だけ。

tool-call の書式は llama-server の chat template に依存し、モデル族で違う（Qwen・GLM・Llama で JSON の壊れ方が
違う）。**kind は 1〜2 族だけを対応**とし、guide に名指しする。「目録にあるもの何でも」にはしない。

### 決定 6——`af` ツールは in-process、外部 MCP は自前クライアント、「known だが materialize しない kind」

`af` の 72 ツールは MCP を通さない: ハーネスは `dispatchMCPStdio` を直接呼ぶ。**初稿は「プロセス全体のフラグを
per-call のオプション構造体にすれば足りる」と書いたが誤り**（Review）: dispatcher はプロセス単位のセッション／会話／
Chromium／peer／画像／spawn の状態、非同期通知のための stdout writer、一度きりの watcher にも依存する
（`mcp_stdio.go:79-210`・`:479-544`）。in-process 呼び出しはリクエスト単位の dispatch 文脈がそれらも持ってから
成り立ち、それまでは——そのリファクタを見送るなら以後も——`af` ツールは他の CLI と同じく loopback の HTTP で回す。
どちらでもツール名と説明文はそのままなので、セッションごとの説明文コストは変わらない。

外部サーバ（テナント配布・プロジェクト・利用者登録）には本物のクライアント: stdio と Streamable HTTP、
2026-07-28 の stateless 世代と 2025-06-18 の `initialize` 世代の両方（うちのサーバが両方受けるのと対称）、
`tools/call`・`notifications/tools/list_changed`・サーバごとの寿命（stdio 子はセッションと共に死ぬ）・kind 別の
タイムアウト。種は `mcpreg/probe.go`。

登録簿はメモリで読む。`lcpp` は `mcpreg.knownKinds` に入る（利用者がこの kind 向けにサーバを有効化できる）が
`MaterializedKinds` には入らない（書くファイルが無い）——この状態の kind は初。プロジェクトスコープの綴りは
持たず、他 kind の `.mcp.json` を `mcpproj` で読むだけで、コピー先にもならない。

### 決定 7——窓: CP に read-only の迂回 1 本、毎送信前の正確な数、1 度だけの圧縮

- **窓を知る。** `GET /props` が正解で、今日の gateway は通さない。CP に `GET /engine/{key}/props` を足す:
  read-only・同じ session token・`/v1/` 前置無し・100 行前後。これが**4 ホップに無かった帰り道**で、kind 固有では
  ない: P0 でも P1 でも要り、`syncEngineProviders`（`workspace/agent/engines.go:287`）が箱の起きている間に実際の
  `n_ctx` で `limit.context` を上書きできる＝opencode 経路も直る。
- **埋まりを知る。** これから送る `messages` をそのまま `POST /v1/chat/completions/input_tokens` へ。chat template
  込みの数が返る。gateway の model 検査は通り、demand なので寝ている箱は起きる。
- **決める。** `(input_tokens + 予約出力) > 窓 × 閾値` で要約ターンを 1 回。窓は `/props` が届けばそれ、届かなければ
  目録の `context_tokens`。入力側が正確なら、窓の誤差は圧縮が少し早い／遅いに収まり、「25k を 13% と表示しながら
  毎ターン圧縮」は再現しない。
- **成長ラダーは無い**（背景）。

### 決定 8——使用量は exact。この kind は `WindowGuess` を呼ばない

llama-server の `usage` は正確で、stream にも乗る。`lcpp` は `usageMeasuredForKind` の **exact** 側に入る——
自前経路で自分の数字により exact になる最初——、`LiveInfo.Context` を `WindowSource="recorded"` で書く。
エンジンの GPU 時間の使用量は既に Agent に届いている（`control-plane/engine_usage.go:365` →
`workspace/agent/engines.go:788`）ので、目録の `llamacpp` provider 行を「GPU 時間で課金・トークン単価 0」として
同じトークンを 2 度数えない。

### 決定 9——段階化: CP の迂回 → kind 非依存の中核として P1 → 実測合格時だけ P2

- **段 0——決定 7 の迂回**（半日）。どの案でも要る。やらない理由が無い。
- **段 1——P1**: `internal/harness` のような kind 非依存パッケージに LLM クライアント・ツールループとツール・
  MCP クライアント・文脈管理を置き、chatx の `ChatProvider`（`chat_providers.go:44, 70, 110, 149`）から使う。
  「自前エンジンでコーディング作業が本当に回るか」「どの族なら tool-call が壊れないか」を実機で測るのはここ。
- **段 2——P2**: kind の配線・driver・転写の書き手・使用量・テスト・guide。同じ中核を包む。段 1 が合格したときだけ:
  (a) 選んだ族で 20 ターン級の実作業が完走、(b) 起床と再 prefill の固定費を利用者が受け入れる、(c) opencode 経路より
  事故が減る見込みがある。

動機が窓だけなら**段 0 で止める**。自前エンジンの利用がときどきの単発質問に留まるなら P0（`Send` だけの
プロバイダ・1〜2 日）で止める。

### 決定 10——ピン・導入・ログイン・ドリフト監視は無い。契約テストはエンジン側へ移る

Dockerfile の ARG も `versions.json` の行も `env_tool_versions` の行も cli-drift・release watcher も、両
`routes.go` のログインルートも無い。接続カードは「chat エンジンが目録にある・warm か・既定モデル」だけ。
`fs.go` の denylist も不要（エンジン token はメモリに留まり、`opencode.EngineEnv` と同じ func-var の seam で
取る）。残る唯一のドリフト軸は llama-server 自身の API と tool-call の出力で、これはエンジンイメージの
llama.cpp 版で動く: live テストと同じ opt-in の実エンジン契約テストが、TUI 文字列契約テストの代わりになる。

## 棄却した案

- **llama-server の MCP クライアント / `--tools` / `--agent` を使う。** 箱が違う（背景）。節約ではなく不可能。
- **hermes-agent の成長ラダーを移植する。** ここでは bounce が分単位で、GPU は戻らないことがある。
- **REPL で Terminal 経路を作る。** ミラーが映す kind に 2 つ目の UI（決定 2）。
- **`native` / `llama` / `llm` / `llamacpp` / `engine` と名付ける。** 決定 1。
- **窓のために kind を作る。** 段 0 だけで帰り道は得られ、kind はそこに何も足さない。kind の理由は「opencode を
  経由しない自前エンジンのセッション」が欲しいかどうかだけ。
- **いきなり P2。** 費用の過半は agent の中核で、選んだ族で動かすまで判断できない。P2 固有の増分（12〜14
  セッション日）を知る前に使うことになる。
- **`lcpp` 向けにファイルを materialize して登録簿を読む。** 読む者がいないファイルはドリフトの源。

## 帰結

- 6 つの「初」と、それぞれの設計費用（上記）: managed 専用 kind／転写の書き手／in-process の `ProcessModel`／
  うちが実行するツール／known だが materialize しない MCP kind／自前経路で exact な使用量。
- 利用者が感じ、guide に書く固定費: 箱の起床（最初のターンで分単位）と、箱が入れ替わった後の毎ターン再 prefill
  （30B・30k で数十秒。`/slots` の退避は箱を跨げず、そもそも gateway を通らない）。どちらも opencode 経路より
  悪くはならず、kind でなくエンジンの費用。
- 見積りは最大の 2 項目で下振れより上振れしやすい: Review は E（ツール群）が 3,000 行、F（MCP クライアント）が
  1,200 行を超える見込みとした（上記のリクエスト単位 dispatch のリファクタは F に入り、最小の完成 kind が既に
  2,109 行）。幅は据え置き、向きだけ記録する。
- 見積り（明細と項目別の表は docs/log/99 §8）: **22〜27 セッション日・テスト込み約 9,000〜13,000 行**、1 レーン
  ≈ 5 週・3 レーン ≈ 2〜3 週。うち P1 と共通の中核（LLM クライアント・ループとツール・MCP クライアント・文脈）が
  12〜15 日、**P2 固有の増分は 12〜14 日**。P0 は 1〜2 日、P1 は 10〜13 日。
- kind がもう要らなくなるもの: ピン・導入・ログイン・文字列契約テスト。新しく依存するもの: エンジンイメージの
  llama.cpp 版。

## 段階

| 段 | 内容 | 次への門 |
|---|---|---|
| 0 | CP に `GET /engine/{key}/props`。`syncEngineProviders` が起床中に `limit.context` を上書き | 無し——出す |
| 1 | P1: `internal/harness` の中核＋chatx プロバイダ。族の選定。実機計測 | 決定 9 の (a)(b)(c) |
| 2 | P2: kind の配線・driver・転写の書き手・契約テスト・使用量・Console 開放・guide・本 ADR を *採用* に。
  **完了**——PR #816（kind の器・端末経路無しの門）・#818（転写ストア）・#821（保存先の再検討・ハンドル
  使い回し）・#823（qwen3-coder 決着）・#824（Console 配線）・#825（managed driver）・#826（実エンジン
  契約テスト）・#829（決定 8 の使用量 exact 化・Console 開放・MCP kind 許可リスト。詳細は「段 2 の実装
  記録」節） | — |

## 未決の問い（段 1 の前に答える）

1. どの 1〜2 族か。その族で llama-server の `tool_calls` が 20 ターンの作業を通して壊れない JSON のままか。
2. エンジンイメージに焼かれている llama.cpp 版に `/v1/chat/completions/input_tokens` と `/control` が実在するか
   （README で確認済みだが古いビルドには無いことがあり、決定 7 が変わる）。
3. `dispatchMCPStdio` のグローバルを 72 本のツール本体に触らず per-call にできるか。できなければ `af` ツールも
   他と同じく loopback の HTTP で回す。→ Review で回答済み。
4. Console のどこが `Capabilities.ProcessModel` を読むか（4 つ目の値が壊す先）。→ Review で回答済み。
5. 「端末経路無し」のフラグをどこが読む必要があるか（起動モーダル・driver 切替・引き継ぎモーダル）。→ Review で回答済み。

## 参照した出典（2026-09-19・`951bb402`）

`workspace/agent/internal/agents/{agents.go,driver.go}` · `workspace/agent/internal/sessionx/{agent.go,session_turn.go,session_driver.go,session_handlers.go}` ·
`workspace/agent/internal/mcpx/mcp_stdio.go` · `workspace/agent/internal/mcpreg/{def.go,materialize.go,probe.go}` ·
`workspace/agent/internal/chatx/chat_providers.go` · `workspace/agent/internal/agents/opencode/window.go` ·
`workspace/agent/internal/usagex/usage.go` · `workspace/agent/{engines.go,agent_models.go,agent_instructions.go,agent_rtk.go,usage_fold.go,model_provider.go}` ·
`control-plane/engine_gateway.go` · `control-plane/internal/mcpsrv/{mcp.go,mcp_server.go}` · `console/src/agents/registry.ts` ·
`console/src/lib/agentModels.ts` · `docs/log/{32,36,40,43,74}-*-agent-kind.md` · `docs/log/34-native-runtime.md`

## Review (2026-09-19, before Phase 0)

### 確認した

- gateway の `serve` に path allow-list は無いが、登録 route は `/engine/{key}/v1/{path...}` だけで、local の llama.cpp
  向け target は engine URL＋`/v1/`＋捕捉 path になる（`control-plane/engine_gateway.go:211,467-583,1102-1117,1169-1177`）。
  borrowed engine も llama-server root ではなく far gateway の `base_url` に差し替える
  （`control-plane/engine_gateway.go:1094-1117`）。したがって `/props`・`/slots` の抜け道は無く、決定 7 と段 0 は残る。
- 未登録 kind は引き続き Claude に正規化される（`workspace/agent/internal/sessionx/agent.go:25-53`）。exact 集合は Claude・Codex・
  OpenCode だけ（`workspace/agent/usage_fold.go:201-212`）。`ProcessModel` の記載値も `shared-daemon`・`per-session-child`・`tui`
  の 3 つだけ（`workspace/agent/internal/agents/driver.go:142-157`）。
- `internal/agents/<kind>` の非テスト Go src を再計測すると agy 2,109、cursor 2,753、copilot 2,892、kiro 2,896、
  opencode 5,171、claude 5,853、codex 5,948 行で、docs/log/99 §8 の物差しは再現した。
- 非テストの `"kiro"` 全数棚卸しでは、handoff・spawn・共有ビュー・fork-at に表から漏れた kind 分岐は無かった。
  これらは capability／registry／generic 経路である（`workspace/agent/internal/sessionx/session_handlers.go:482-529,1002-1103`、
  `workspace/agent/internal/sessionx/session_spawn.go:294-363`、`control-plane/session_share.go:645-670`）。定時実行は表に既載
  （`control-plane/scheduler_wake.go:278-285`）。本提案以外に `lcpp`／`lc`／`-lc`／`KindLcpp`／`kind-lcpp` は無く、
  `git fetch origin` 後の `origin/develop` に ADR 0093 と docs/log 99 は無い。

### 壊れた

- `dispatchMCPStdio` は純粋な「1 行入力→1 行出力」ではない。`parseStdioFlags` の権限 global 以外にも、所有 session・conversation・
  Chromium・peer・image・fleet-spawn の process state を読み、`tools/list` は process-wide の once-only watcher を起動し、progress と
  list-change notification は process-wide stdout writer へ非同期に書く
  （`workspace/agent/internal/mcpx/mcp_stdio.go:79-123,125-210,243-297,385-416,479-544,2252-2518,3236-3277`）。決定 6 の
  in-process 直呼びは、notification 出力と watcher の寿命も request-scoped dispatch context にした場合にだけ成立する。
  `parseStdioFlags` を option struct に替えるだけでは足りず、loopback が fallback として残る。
- `Capabilities.ProcessModel` は wire に載らず Console も読んでいない。現状は managed driver が値を埋めるだけである
  （`workspace/agent/internal/agents/driver.go:142-157`、`workspace/agent/internal/agents/{codex,opencode,kiro,cursor,copilot}/driver.go`）。
  `in-process` を足しても今日の Console は壊れない。UI の制御に使うなら wire field と consumer が追加作業になる。
  `tuiMemoryCost` は別の descriptor data である（`console/src/agents/registry.ts:123-127`）。
- §3 の実装チェック表には report reconciler が無い。2 tick settle は polling TUI のためにある
  （`workspace/agent/internal/chatx/chat_report_reconcile.go:45-54`）。`lcpp` は §4.8 のとおり通常の turn-end marker を発生させる必要がある。
  これは検証・テスト点であって新しい kind-name 分岐ではない。指定された handoff・spawn・定時実行・共有ビュー・fork-at には
  ほかの漏れを認めなかった。
- 見積り E は 3,000 src 行を上振れしやすい。cwd に閉じた filesystem editor、shell cancel、出力／binary 制限、approval policy、
  plan／question state、parallel tool loop を合わせ、最小の既存 kind 全体でも非テスト 2,109 行ある。F も 1,200 行より上に偏る。
  2 transport・2 MCP 世代・reconnect／lifetime／notification と、上記 request-scoped 化まで含み、probe の延長だけではないためである
  （`workspace/agent/internal/mcpreg/probe.go:332-344,483-498`）。数字は直さず、E・F とも下振れより上振れ余地が大きいと記録する。

### 答えた

- 問い 1・2 は要実機のまま残る。model family の tool-call integrity と engine image に焼かれた endpoint は tree だけでは確定しない。
- 問い 3: flag global だけの変更では不可。上記の広い request-scoped 境界が要り、できなければ loopback
  （`workspace/agent/internal/mcpx/mcp_stdio.go:125-210,479-544`）。
- 問い 4: 今日の Console には読者がいない。`ProcessModel` は Agent API を越えない
  （`workspace/agent/internal/agents/driver.go:142-157`）。
- 問い 5: 新しい terminal-route capability は 2 つの起動 form
  （`console/src/features/repos/LaunchModal.tsx:776-803`、`console/src/features/repos/StartModal.tsx:303-316`）、quick launch
  （`console/src/features/repos/RepoRowConnected.tsx:178-184`）、driver-switch menu と action
  （`console/src/features/sessions/SessionMenu.tsx:179-190`、`console/src/features/sessions/useSessionActions.tsx:262-287`）、server 側の
  managed→TUI 遷移（`workspace/agent/internal/sessionx/session_driver.go:62-105`）で読む必要がある。handoff は同じ generic create 経路を
  使うので、別の route label ではなく target kind から managed を選ばせる
  （`console/src/features/sessions/HandoffModal.tsx:75-84,119-130`）。

## 段 0・段 1 の実装記録（2026-09-20）

段 0 と段 1 は develop に入った（#761・#767＝段 0 と追補、#762・#764・#766・#770＝段 1 の D/F/E/G）。
**決定文は変えていない。**以下は実装と実測が決定文に対して何を足し、何を覆したかの記録である。

### 覆った前提 2 つ

- 🔴 **決定 6 の「`af` ツールは他の CLI と同じく loopback の HTTP で回す」は、後半が事実と違った。**
  `mcpreg/builtin.go:45-50` の `BuiltinAF` は `runArgs: ["mcp-stdio", "--self-report", "--chromium-attach"]`
  の **stdio** ServerDef で、Agent 側に HTTP の MCP 入口は無い。**他の CLI も stdio で繋いでいる。**
  「他の CLI と同じく」という意図は正しく、その手段の記述だけが誤りだった。#764 は既存の stdio
  ServerDef を汎用クライアントで繋いでおり、`af` 専用のコードは `internal/mcpc` に 1 行も無い。
  `dispatchMCPStdio` の in-process 直呼びを見送る判断（Review）はそのまま有効。
- 🔴 **決定 7 の「`/props` が実際に付いた窓を返す」は、router モードの配備では成立しない。**
  実測（#770 の実機）: `GET /engine/llm/props` は 200 を返すが `role: "router"`・`model_path: "none"`・
  `default_generation_settings.n_ctx = 0`。llama-server を `--models-max` で複数モデル同居させると、
  `/props` はルータ自身を describe する。実値は `{engine}/v1/models` の `data[].meta.n_ctx` にある。
  #767 は、段 0 の門（demand を記録しない・起こさない）を保ったまま、`n_ctx == 0` のときだけ
  `props()` が自分で `/v1/models` を読んで 1 キーを足す形で塞いだ。**`/v1/models` を gateway の
  通常経路で読むのは不可**——`serve()` が `demand.record`（`engine_gateway.go:540`）と
  `ensureReady`（`:1018`）を踏み、窓を尋ねるために GPU を買ってしまう。

### 決定文に無かった条件 1 つ

- 🔴 **借用行（ADR 0079）では、貸し手側の配備にも同じものが要る。** 借用行の `/props` は far が
  トークン応答の `base_url` で宣言した経路＝far 自身の `/engine/{key}/props` を叩く。貸し手側の CP が
  古いと Go 標準の `404 page not found` が返る（2026-09-19 に実際に発生し、sandbox の再配備で解消した）。
  #767 は借用行を**ローカルで augment しない**——far の `/v1/models` を読むことは far の `serve()` を
  叩くこと＝**貸し手側で GPU を買うこと**になるため。したがって借用側で実値が読めるのは、
  貸し手側にも同じ変更が配備されてからになる。

### 実測

| 項目 | 実測 | 備考 |
|---|---|---|
| 未決の問い 2: `/v1/chat/completions/input_tokens` | **実在**。`{"input_tokens":61,...}` が chat 応答の `prompt_tokens` と一致 | 🔴 フィールド名は README の `tokens`/`n_tokens` ではなく **`input_tokens`** |
| 未決の問い 2: `/v1/chat/completions/control` | **実在**。`model` と in-flight な completion の id を要求 | `Interrupt` は段 2 なので public API には起こしていない |
| 未決の問い 1: 族 | **Qwen3 系**（`qwen3.8-27b-uncensored-q4_k_m`）で 16 往復・2 プロジェクト完走。`tool_calls` は全ターン有効な JSON・名前化けなし・4 件並列でも引数の取り違えなし | ⚠️ **実質 1 族・少数本**。他族は共有 GPU 箱（`role: router` / `max_instances: 1`）の退去コストを避けて未検証 |
| 窓 | 実値 262144。目録の宣言値 262144 と**完全一致・ずれ無し** | 下記 |
| 起床（真の cold start） | **4〜5 分**（箱の購入＋モデル同期）。`engine_waking` が跨いで正しくリトライ | 段 1-D で測れた 34.8 秒は温まりかけの箱だった。決定文の「分単位」が正しい |
| 圧縮 | `input_tokens` が 93→663 と増え、閾値で実際に発火。full は 29→30 エントリ＝**過去行は消えていない** | 窓を 900 に強制して実施（実の 262144 を埋めるのは共有 GPU を焼くだけで、試験対象は閾値の算術と送信形） |

🔴 **動機について正直に記録する。** 窓の実値と宣言値は一致していた。本 ADR の動機だった「窓が片道
4 ホップで伝わり、ずれる」は、**少なくともこの 1 件では実害として現れていない**。段 0 が無意味に
なるわけではない（確かめる手段が無かったこと自体が問題だった）が、動機の強さは実測に合わせて
弱く見積もるべきである。

### 実機だけが見つけたもの

偽クライアントのテストでは出ず、実エンジンに通して初めて出た欠陥が 3 件あった。この kind の
費用の一部は「うちが実行者になる」ことで**この種の問題をうちが抱える**点にある、という決定 5 の
帰結の実例として記録する。

- 圧縮直後の送信が Qwen の chat template に 2 箇所で弾かれた（未回答の user ターンまで要約に畳んで
  送信が system で終わる＝`No user query found in messages`／要約を 2 つ目の system として途中に
  入れる＝`System message must be at the beginning`）。
- chatx の P0 プロバイダが履歴の最後の 1 件を無条件に落としていた。`prov.Send` の呼び出し元 6 つの
  うち前提が成り立つのは 2 つだけで、圧縮と報告の自動ターンでは要約対象が消えていた。**履歴を
  自分で組み立てる最初のプロバイダが `lcpp`** なので、この問題はここで初めて現れる。

### 段 1 の範囲として残したもの

- **`chat_providers_lcpp.go`（P0 プロバイダ）は E/F/G に繋いでいない。** P0 プロバイダをツール
  ループに繋ぐのはアシスタントチャットの挙動を変える製品判断で、段 1 の範囲を超える。
- ⚠️ **要約の「質」はモデル依存。** 量子化 27B の要約は薄いことがあり、一度は次ターンの質問を
  反響しただけだった。機構（追記単調・単一の先頭 system・末尾の user ターン・実トークン数）は
  すべて保たれており、ハーネスの欠陥ではない。**段 2 で族を選ぶときの判断材料**になる。
- 承認の門の既定は **fail-closed**（`Runtime.Approve == nil` は `Mutates` を拒否）。無人実行は
  `AutoApprove` を明示的に渡す。決定 5 の「承認は本当にツールを止める」を、忘れたときの壊れ方が
  「静かに素通り」ではなく「うるさく止まる」側になるように実装した。

## 決定 9 の門の判定（2026-09-20）

段 1 の実機測定が積み上がったところで、利用者が決定 9 の (a)(b)(c) を判定した。実機の全 14 本の一覧は
`docs/log/99-lcpp-agent-kind.md` §14（旧 §12.5 の 6 本を延長）。番号の数え方は #1〜#14（#5 は並列指示の
有無で 2 走行）で、既存 §12.5 の「実機 6 本」の数え方をそのまま延長したもの——番号行は 14、#5 の 2 走行を
数えると走行数は計 15。

- **(a) 20 ターン級の実作業が完走するか → 合格。** 4 チェックポイント／**3 族**（Qwen3・GPT-OSS・Gemma）で
  PASS した（#1・#2・#6・#7・#8・#12・#13）。ADR が求めた「1〜2 族」を超えた。⚠️ ただし**同じ仕事のターン数が
  族で 2 倍以上違う**（#2 の 29 ターン 対 #7 の 95 ターン）——差は**正しさではなく費用**である。
- **(b) 起床と再 prefill の固定費を受け入れるか → 受け入れる。想定より良かった。** 🔴 **モデルの入れ替えに
  箱の入れ替えは要らない**ことが実測で分かった（#7〜#13 のいずれでも `engine_waking` は 0 件）。高いのは
  最初の購入だけで、真の cold start は 3.5〜5 分に収まる。§12.5 の実測では #5' の cold start が**302.33 秒**
  で `context deadline exceeded` になった例が 1 件ある。起床待ちの上限は**900 秒**（`docs/log/99` §4.2 の
  `errEngineWaking`・wake timeout の定数であって、決定 4 の記述ではない）。⚠️ この上限を実際に超える hold が
  起きたという話は引き継ぎ時の申告であって、`$HOME/lcpp-live/` の生ログでは裏が取れていない（断定しない）。
- **(c) opencode 経路より事故が減る見込みがあるか → 判定は合格だが、実機だけが見つけた欠陥は 8 件。**
  （2026-09-21 追加・前セッション `sfbkvkc` からの一次情報で内訳が判明。前回版はここを「残り 4 件は
  特定できない」と書いていたが誤りではなく単に未確認だっただけで、以下がその内訳）。🔴 **この「8」は
  本 ADR 単体に根拠がある数ではなく、由来が 3 層に分かれる。** 平板な箇条書きにすると層が失われるので、
  層ごとに書く。

  **A 群（本 ADR が「実機だけが見つけたもの」として既に明記している 3 件・段 1 前半）**:
  (1) 圧縮直後の送信が Qwen の chat template に弾かれた（`No user query found in messages`）／
  (2) 同（`System message must be at the beginning`）／
  (3) chatx の P0 プロバイダが履歴の最後の 1 件を無条件に落としていた（`prov.Send` の呼び出し元 6 つの
  うち前提が成り立つのは 2 つだけ）。

  **B 群（前々セッション `srx5cky` の引き継ぎが「5 件」と数えたときに A に足していた 2 件。PR は実在
  するが、本 ADR の「実機だけが見つけたもの」節の一覧には入っていない）**:
  (4) 並列 edit の read-modify-write レース（PR #774・コミット `2d8eedcf`）／
  (5) `/props` が router 自身を describe して窓を返さない（`role: "router"`／`model_path: "none"`／
  `n_ctx = 0`。PR #767 で塞いだ）。🔴 **(5) は本 ADR 既存の「覆った前提 2 つ」節に書かれており、
  「実機だけが見つけた 3 件」の側には入っていない**——別の節に既に記録済みという位置関係。

  **C 群（前セッション `sfbkvkc` が実測した 3 件）**:
  (6) ツールループに繰り返し検知が無く、§14 #3 で `todo_write` が 72 ターン連続（turn 50〜121）しても
  誰も止めなかった（→ PR #794）／
  (7) `Run` に文脈管理が 1 つも無く、§14 #9 で実窓超過（`request (32772 tokens) exceeds the available
  context size (32768 tokens)`）が起きた（`loop.go` に窓も圧縮も 0 件だった。→ PR #811 の動機）／
  (8) PR #811 初版の圧縮スラッシング（同モデル・同窓の A/B で 59 ターンから 353 ターン超に膨らみ、
  task-1 だけで圧縮 86 回。§14 #10）。この (8) が下記「実機だけが見つけたもの（続き）」に記録した #811 の件。

  **8 件に入らないもの（次に数え直す人が同じ問いを繰り返さないための記録）**: `manuallive` の再試行と
  `EngineError.Retryable()` の不一致（`113a894e`）は**テストの足場の修正**であって製品の欠陥として
  数えていない。承認ゲートの既定が fail-open だった件（`aeb924f0`）も数えていない。🔴 **繰り返し検知が
  実機で 0 回発火した件（負債 2）は 8 件に入っていない**——これは「欠陥の発見」ではなく「(6) の手当てが
  実際の失敗を捕まえられていない」という**手当ての不十分さ**として別に数えたもの。**9 件目として数える
  立場もあり得るが、前セッションはそう数えていない**（前セッションから両論併記の明示の許可を得ている）。

  以上は決定 5 の帰結（実行者がうちになる＝この種の問題をうちが抱える）の実例。**8 件のうち最後（(8)・
  #811）は「偽クライアントのテストは全部緑・実機でも当初の症状は直っている・それでも実用上 6 倍遅い」
  という、テストでは原理的に見えない種類**だった。下記の既存節に続けて記録する。

### 実機だけが見つけたもの（続き・2026-09-20 追加）

上記「段 0・段 1 の実装記録」節の 3 件（圧縮直後の chat template 拒否 2 パターン・P0 プロバイダの履歴欠落）に
続けて、段 1 後半（PR #811 前後）で実機だけが見つけたものをもう 1 件記録する。

- **PR #811 の初版（#10）は 353 ターンを超えて未完に終わった**（task-1 だけで圧縮 86 回）。偽クライアントの
  ユニットテストは全部緑のまま、実エンジンに通して初めて症状が出た。#811 の最終版（#11〜#13）で直近往復を
  生で残す形に直したところ、窓 3500 では `ErrCompactionThrashing` で設計どおり明示停止し（#11・139 ターン）、
  窓を 8000 に上げると圧縮が正しく 2 回発火して想起も正確になり（#12）、窓 24000 では圧縮自体が要らなくなった
  （#13・圧縮 0 回）。**実用上の結論は「直った」ではなく「窓 3500 は #811 のハーネスにとって実質的に狭すぎる」**
  ——修正の効果を測るには窓を広げて再測定する必要があり、この種の性能特性はテストでは原理的に見えない。

## 段 2 に持ち越す負債（2026-09-20・全 9 件・全部実測で確定済み）

段 1 で実測により確定したが、段 1 の範囲では直さなかった負債。1 と 2 は段 2 の作業が必ず踏む。

1. **`continuationPrompt` の合成 user ターンが `Result.Messages` に残る**（`workspace/agent/internal/harness/loop.go:77`
   の定数・`:320`・`:329-335`）。圧縮の境界がツール結果で終わるとき、harness が `"Continue with the task."` という
   user ターンを作り、記録に残る。🔴 **段 2 の転写の書き手は、これを利用者の発言として描いてはいけない。**
   定数 1 つで識別できる。
2. **繰り返し検知（PR #794・`workspace/agent/internal/harness/repeat.go`）は coder の実際の失敗を捕まえられて
   いない。**実機で 0 回発火。同一ツール名が 6 連続していたのに**引数が毎回変わっていた**ため `name+args` 一致が
   不発。`repeat.go` 自身が「既知の限界」と書いているものが現実に起きた。
3. 🟢 **決着済み（2026-09-21・§14 #15/#16）。** `qwen3-coder-30b-a3b` は #811 最終版のハーネスでは 1 度も
   測っていない、という指摘（初出時）は解消した。過去 3 回の測定はそれぞれ別のハーネス版に当たっていた
   （走行順とハーネス版の確定は `docs/log/99` §14 の 2026-09-20 追加訂正参照）: **§14 #3＝段 1 版で 72 ターン
   連続ループ**／**§14 #9＝段 1 版で実窓超過**（原因の「`Run` に窓が渡らない」欠陥は 52bbda2d と 570d5042
   で直っている）／**§14 #14＝#811 初版で非収束**（この版自体が §14 #10 と同じ圧縮スラッシング欠陥を持つ
   ため、coder の性質の証拠にならない＝交絡）。**#811 最終版（570d5042 以降・現 develop）で新規に測った
   §14 #15（窓 3500）・#16（窓 8000）**: 窓 3500 は `ErrCompactionThrashing` で設計どおり明示停止（task-0 は
   40 ターンで完走）、窓 8000 は 164 ターンで PASS——gemma-4 の §14 #11→#12 と**同じ形**であり、「窓 3500 は
   #811 のハーネスにとって狭すぎる」という §14 の結論を 2 族目で裏づけた。過去 3 回の不調はいずれも当時の
   ハーネス版側の問題であり、coder 自体の欠格事由ではなかった。⚠️ **ただし合格は「正しさ」の話であり、
   164 ターンという費用は同一窓の gemma-4（63 ターン・§14 #12）の 2.6 倍・窓は異なるが qwen3.8（29〜30 ターン・
   §14 #1/#2）の 5.5 倍**——決定 9 門 (a) の「族で 2 倍以上違う＝費用の差」という軸に、coder は最も極端な点
   として乗る。詳細（`compacted_at_turn=0` の由来の切り分けを含む）は `docs/log/99` §14 の 2026-09-21 追記
   参照。
4. **Llama 3.1 はこのエンジン版では出口なし。**`common/chat.cpp` に Llama 専用パーサが無く差分オートパーサに
   落ちる。role 全体の `--jinja` が常に勝つため（`tools/server/server-models.cpp:548-551` の overlay・予約キーに
   `LLAMA_ARG_JINJA` が無い）、モデル別引数で legacy 経路へ逃がす道も塞がっている（§12.4 に既述の事実。負債表
   からはそこを参照するだけでよい）。
5. **`control-plane/engine_gguf.go` が非均一な `layer_types` の KV を算出できない。**GPT-OSS・gemma-4・LFM2.5
   がその形で、**有効化が 409 で拒否される**（"cannot be sized (no attention geometry)"）。前セッションは層ごとに
   計算した実数を `vram_mib` として申告して通した（GPT-OSS 14500 / gemma-4 9800）。**これは直す価値のある別件**
   （段 2 の本体ではない）。🔴 **一次資料**（出典: 前セッション `sfbkvkc` の実機操作。ほかに記録は無い）:
   実物は sandbox の `PUT /api/admin/engines/llm/models/{id}` が返すエラーコード `engine_vram_confirm`
   （本文 "wants a KV cache that cannot be sized (no attention geometry) on top of 11548 MiB of
   weights"）。回避手段は、上記の層ごとの実数を `vram_mib` として同じ PUT の body に載せて申告すること。
6. **`chat_template` は `/props` にも `/v1/models` にも出ない**（実測）。GGUF に焼かれたテンプレを配備側から
   確認する手段が無い。docs/log/99 §12.5 に既述なので、負債表では「未解決のまま段 2 へ」と再掲する。
7. **エンジンイメージは PR #803 で固定「できる」ようになっただけで、誰もまだ pin していない。**sandbox は
   動くタグのまま。tool call の構文解析は完全にエンジン側にあるので、版が変われば挙動が変わる。実測版は
   `b10830-465e49b9c` で、`/props` の `build_info` から**箱を起こさずに**読める（§12.3）。
8. **`InputTokens` がツールループの毎反復で呼ばれる。**生成を伴わないので安価だが往復は倍。閾値に近いときだけ
   実測する余地が doc に残っている。
9. **走行順で §14 #14 以降が PR #811 のツールループ内圧縮を持つ。** それ以前の 10 走行（#1〜#9 と #5'）は
   タスク境界でしか圧縮していない。さらに §14 #14 と #10 は #811 の**初版**、#11〜#13 が**最終**版——番号順
   （#9→#14）と走行順（#14 は #10〜#13 より前）は一致しない（`docs/log/99` §14 の 2026-09-20 追加訂正・
   同じ事実）。負債としては「以後の比較で `compacted_at_turn` を版をまたいで素朴に突き合わせない」という
   注意として書く。

## 段 2 の実装記録（2026-09-21）

段 2 は develop に入った（#816・#818・#821・#823・#824・#825・#826・#829）。#829 が決定 8 の使用量
exact 化・Console 開放・MCP kind 許可リストを持ち込んだ。**決定文は変えていない。**以下は実装と実測が
決定文に対して何を足し、何を変えたかの記録である——段 0・段 1 の実装記録と同じ体裁。

### 決定文に対して実装が変えたもの 4 件

- 🔴 **決定 4 は `DynamicModel`・`DynamicEffort`・`DynamicMode` を「全部 true」と書いたが、`DynamicEffort`
  は false。** `UpdateSettings` が次の `runTurn` に実際に効くのは model（`newHarnessClient` へ渡す文字列）と
  mode（`rt.Plan`）の 2 つだけで、どちらも `driver_test.go` が運動させている。`reasoning_budget` は
  llama-server の**起動フラグ**としてしか確認できておらず（ADR 0093 背景節）、per-request の欄が実機で
  見つかっていない——確認していない上限を名乗るのは docs/log/76 が禁じる「未検証の cap」そのものなので、
  false のままにした（`workspace/agent/internal/agents/lcpp/driver.go:44-59`）。
- 🔴 **決定 5 は `Permissions: true` と `PermissionChoice: true` を「実測済みとして宣言できる」と書いたが、
  `Permissions` は false。** grep で確認した限り**どの kind もこのフィールドを立てていない**——ワイヤの
  語彙自体が無い。承認は kiro の ACP `session/request_permission` と同じやり方で、専用の「承認」
  Interaction 種別ではなく既存の `Questions` を再利用して実装した（`driver.go:57-59` の `approve()`）。
  `PermissionChoice` は true のまま（`agent.go:36-49`）。
- 🔴 **決定 4 の Steer「次のツール境界に積む」は「次の最上位ターンとしてキュー」に簡略化された。**
  `harness.Run` は 1 回のブロッキング呼び出しで、ツールループの途中にメッセージを差し込むフックが無い
  ——kiro / copilot / cursor が同じ理由で既にしている簡略化と同じで、「いま実行中の `Run` 呼び出しが
  戻り次第」を「次のツール境界」の近似として採用した（`driver.go:44-48`・`threadHandle.Steer` →
  `accept`）。
- 🔴 **決定 3 の保存先は `AgentDataDir` ではなく `AgentStateDir`。**（#821。`store.go:86-97`）。sid で引く
  ものは state 側、という `paths.AgentStateDir` 自身の doc コメントの線引きに、この store は最初から
  当てはまっていた——ADR 0087 決定 4 の実装節もほぼ同じ言葉でこの規則を繰り返している。ズレの原因は
  「両方の doc コメントを突き合わせるチェックが無かった」だけで、悪意や設計判断ではない。**副産物として
  denylist の穴が閉じた**: `AgentStateDir` は Console のファイルブラウザの denylist（`fs.go` の
  `fsDeny`）に既に入っているが `AgentDataDir` は入っておらず、移す前は生の会話内容（ツール結果・
  reasoning を含む）がファイルブラウザ経由で見えていた。**書き込み方式も同じ PR で直した**: 毎レコード
  `MkdirAll`→`OpenFile`→`Write`→`Close` だった実装をハンドル使い回しに変え、`strace -c` の実測で
  1 レコードあたり約 4.09 → 約 1.10 syscalls（定常状態は write 1 回のみ）に減った（`docs/log/99` §15.2）。
  **EFS には寄せない**という判断（claude の転写方式との対比）の根拠は、`AgentStateDir`・`AgentDataDir`
  ともに同じ home（EBS・単一 AZ）ボリュームに載っており、claude の EFS 選択（`CLAUDE_CONFIG_DIR`）は
  ADR 0045 決定 3-6・ADR 0087 が明言する「資格情報を残すため」の意図的な設計であって `lcpp` には
  当てはまらない、という一次資料に基づく判断（`docs/log/99` §15.1.1）。

### 決定 8 の実装で見つかったもの（PR #829）

- 🔴 **ワイヤの片肺欠落。** `session.ContextUsage`（Go 側）に `Window`/`WindowSource` の 2 欄が無く、
  TS 側の `SessionContextUsage` は**既にこの 2 欄を宣言していたのに Go 側が埋めていなかった**
  （`workspace/agent/internal/session/session.go`）。実機の欠陥ではなく、**決定 8 を実際に実装しようと
  して初めて見つかった型定義の非対称性**——「実機だけが見つけたもの」ではなく「実装が見つけたもの」
  として記録する。Go 側に `Window int`・`WindowSource string` を追加し `wire.golden` を更新した。
- **決定 8 の実装そのものは決定文どおり。** `usage_fold.go` の `usageMeasuredForKind` が `KindLcpp` を
  `exact` に加え、店の `Record` に `Window` を足して `AppendUsage(u, window)` に拡張——`window` は
  driver が `runTurn` 内で既に呼んでいた `harness.EngineWindow` の値をそのまま渡すだけで、新規のエンジン
  呼び出しは無い。`WireLive` は `Store.LastUsage()`（ディスク読みのみ）から `li.Context` を組み、
  `window>0` のときだけ `WindowSource="recorded"` を名乗る——未解決なら空のままで `WindowGuess` を騙らない。
- 🔴 **二重計上を塞ぐ側は実装済みだが、生トークン数の重複は残る。** `usage_price.go` の `usagePriceOf` に
  `kind == KindLcpp` の早期リターンを追加し、常に `price=0`・`src="gpu-billed"` を返す（catalog も
  builtin テーブルも見ないので、自前モデル名が実在モデル名と衝突しても価格が付かない）。これで**費用の
  二重計上は無い**。ただし **生トークン数**は `feature=session` 行と `feature=engine.llm` 行の両方に現れ、
  `by=kind` 集計では重複する。🔴 **これは `lcpp` が作った問題ではない**——`handleEngineUsage`
  （`workspace/agent/engines.go:1098-1116`）の `Kind` は `engineSessionKind(req.Session)` でセッション側
  から解決されるため、**opencode が自前エンジンを使うときにも同じことが既に起きている**（駆動役が実
  コードで確認済み）。決定 8 の文言「同じトークンを 2 度数えない」は**単価 0 という機構としては実装した
  が、その機構が目指した状態（生トークンも重複しない）とは一致していない**——このずれを実装記録として
  正直に書く。生トークンの重複は別レイヤ（`handleEngineUsage` の `Kind` 解決）の既存条件であり、段 2 の
  本体の範囲外として残す。

### 段 2 に持ち越した負債 9 件の現状（2026-09-21）

番号は「段 2 に持ち越す負債」節と同じ。

1. **解消（#818）。** `harness.IsContinuationPrompt` が公開され、`lcpp` の転写ストアが `Transcript()`
   の生成時にこれを判別してミラー描画から除外している（`store.go:231,270`）。
2. **未解消（変化なし）。** #823 の qwen3-coder 最終計測でも実機で 0 回発火。原因は未確定のまま。
3. 🟢 **決着済み（2026-09-21・§14 #15/#16、既に本 ADR 内に記録済み）。** 変更なし。
4. **未解消（変化なし、エンジン側の制約）。** `llama-3.1-8b-instruct-q4_k_m` はこのエンジンビルドで
   引き続き動かない。#826 の契約テストは版の変化を見張るだけで、このモデルの直しは範囲外。
5. **未解消・別件（変化なし）。** `control-plane/engine_gguf.go` は段 2 の 7 PR のどれからも触られて
   いない。develop には非均一な GGUF 幾何の扱いを広げる別トラックの改修（例: KV 見積りのハイブリッド
   モデル 4 倍過大修正）が段 2 と並行して入っているが、これが GPT-OSS・gemma-4・LFM2.5 の 409 拒否
   そのものを解消したかどうかは本調査の範囲では確認できなかった——**分からなかった**、とだけ言える。
6. **未解消（変化なし）。** #826 の契約テストが `chat_template` は `/props` にも `/v1/models` にも
   出ないことを対象外として記録している（`live_contract_test.go:45`）。
7. **見張りだけ付いた（変化なし）。** #826 の契約テストが `GET /props` の `build_info` を**箱を起こさず**
   読めることを確認し、これが変わればテストが落ちる形にした（`live_contract_test.go:372-393`）。pin
   自体は #803 で可能になっているだけで、まだ誰も実施していない。
8. **未解消（変化なし）。** `maybeCompact`（`loop.go:263-281`）は `runToolCalls` の毎反復から呼ばれ、
   `InputTokens` を毎回叩く形のまま。
9. 🟢 **解明済み（既に本 ADR 内に記録済み）。** 変更なし。

### 段 2 で新しく生まれた残件

- **ミラー読み取りの増分パースは未実装。** `Store` の `Records()`/`Transcript()`/`Full()` は今も毎回
  全文を読み直す（codex の `rolloutcache.go` が持つ増分パースに相当するものが無い）。#821 が明示的に
  スコープ外とした残作業（`docs/log/99` §15.3）。
- **last-say の「起床待ち」判定は経過時間のみ。** `threadHandle.lastSay`（`driver.go:709-732`）は
  ターン開始からの経過秒数だけを見て「エンジン起動待ち（n 秒）」を出す——エンジン側の実際の起床
  シグナル（`engine_waking` そのもの）は見ていない。決定 4 が v1 として認めた簡略化のまま（#825）。
- 🔴 **通し経路が未検証。** 「配備済みの Agent から `lcpp` セッションを実際に起こして 1 往復させる」確認が
  段 2 を通じて一度もできていない。ハーネス単体は実機 16 本、driver は偽クライアント、契約テスト（#826）
  は実エンジンで通っているが、**create → driver → harness → エンジンの通し経路だけが未検証**——理由は
  構造的で、ワークツリーのソースを直しても、このセッションの `af` ツールに答えているのは**配備済みの
  Agent バイナリ**なので、再ビルド・再配備まで効かない。残る経路は (i) 配備資格情報を使う dev-deploy
  （組織ポリシーで触れない）と (ii) コンテナ内で Agent を二重に走らせる（ワークスペース方針が名指しで
  禁止）で、**どちらも実行していない**——credential が環境にあることを把握したうえで使わなかった、意図的
  な停止線である。**利用者の判断により、この残件を記録したうえで採用へ移す**——次の配備の機会に通し確認
  すること。
- **生トークン数が `feature=session` 行と `feature=engine.llm` 行の両方に現れ重複する。** 上の「決定 8 の
  実装で見つかったもの」節を参照——`$` は両方 0 なので費用の二重計上は無く、`lcpp` が作った問題でもない
  （opencode が自前エンジンを使う場合に既に起きている既存条件）。

### 段 2 の最後の 1 歩（PR #829・マージ済み）

**決定 8（使用量 exact）と、Console での起動可能化は PR #829 で develop に入った。**
`usage_fold.go` の exact 集合に `KindLcpp` を追加し、`console/src/agents/registry.ts` の
`lcpp.available` を `() => true` に変え `repoLaunchKinds` へ `lcpp` を足し、`mcpx/mcp_stdio.go` の kind
直書き許可リストに `lcpp` を追加した（無いと `create_session` / `list_models` が `lcpp` に使えない）
——付随して `GET /agents/lcpp/models` と `agentModels.ts` の `isDynamic` も足した（無いと起動時にモデル
を選べず driver が "no model configured" で即失敗する）。中身の詳細は「決定 8 の実装で見つかったもの」
節。

### 段 2 の MCP 配線（決定 6 の外部 MCP 部分の実装記録）

決定 6 の「`lcpp` は `mcpreg.knownKinds` に入るが `MaterializedKinds` には入らない」はここまで**文言
だけ**で、コードは何も足していなかった（`knownKinds` に `session.KindLcpp` は無く、`internal/mcpc` を
import する非テストのファイルは 0 件・`driver.go:481` は `harness.NewRegistry(harness.BuiltinTools()...)`
のみ——組み込みツールしか登録していなかった）。この節はその配線を実装した記録。「`af` は他の CLI と
同じく loopback の HTTP で回す」という段 0/1 の訂正（335 行め「覆った前提」節）はそのまま踏襲: `af` 専用
コードは今回も足していない——`mcpreg.BuiltinAF` の stdio `ServerDef` を、他サーバと全く同じ経路
（`mcpc.Manager`）で繋いだだけ。

**足したもの。** `internal/agents/lcpp/mcp.go`（新規）と `driver.go` の `threadHandle`:

- `threadHandle` に `mcpCtx`/`mcpCancel`/`mcpMgr *mcpc.Manager` を追加（`Resume` で構築、セッションの
  一生と同じ寿命——ターンごとの `ctx` とは別物）。`mcpMgr` 自体の構築は I/O をしない（`mcpc.NewManager`
  は ctx-watcher の goroutine を 1 本立てるだけ）ので、毎 `Resume` のコストは実質ゼロ。
- `runTurn`（`driver.go:481` 付近）: レジストリを組む前に `mcpreg.ForSession(session.KindLcpp)` を読み、
  `h.syncMCPServers(ctx, defs)` で `mcpMgr.Sync` に渡し、`h.mcpTools()` が返す `[]harness.Tool` を
  `harness.BuiltinTools()` に足し込んでから `harness.NewRegistry` に渡す。**毎ターン**呼ぶ
  （`mcpreg.ForSession` も含めて）——UpdateSettings が model/mode を「次ターンから反映」するのと同じ
  規約で、有効化/無効化したサーバがそのまま次ターンに効く。
- `mcpToolMutates(def) bool`（`mcp.go`）が決定 1 の「未知は止める側」を 1 か所に閉じ込める——今は常に
  `true`。`mcpc` は MCP の `readOnlyHint` 等の注釈を一切読んでいない（`internal/mcpc` を grep して確認）
  ので、機械的に「読み取り専用」と判定する手段が無く、ADR 0093 決定 5 の fail-closed（`Runtime.Approve
  == nil` は `Mutates` を拒否）に合わせて安全側に倒した。将来 `readOnlyHint` を読む／allowlist を持つ
  ようになったら、この関数の中身だけを差し替える。
- `runMCPTool`（`mcp.go`）は `tools_bash.go` の `runBash` が自分の per-call タイムアウトに対して既に
  やっている 3 分岐（我々自身のタイムアウト／呼び出し元 ctx の cancel／普通の失敗）をそのまま踏襲。
  kind 別のタイムアウト表は探しても無かった（grep 済み）ので、`mcpToolCallTimeout = 60秒` を `lcpp` 自身
  の既定値として `mcp.go` の 1 か所に定義した。
- 接続失敗（`Sync` が返す `map[string]error`）はターンを落とさない: 毎回 `log.Printf` で残し、直前の
  ターンと**シグネチャが変わった時だけ** `Store.AppendMCPSyncErrorNote`（`store.go` の新しい
  `Note = "mcp_error"`）で 1 行記録する——壊れたサーバがそのまま何時間も放置されても、ストアが 1 ターン
  1 行で際限なく伸びないようにする de-dup。ミラーへの描画は付けていない
  （`NoteModelChange` と同じ理由——Part/Turn の形をまだ設計していない、この節の範囲外）。
- `DropHandle` → `closeIdleResources`（旧 `closeStoreOnceIdle` を改名）が、ターンが止まるのを待ってから
  `store.Close()` と `mcpMgr.Close()` を両方呼ぶ（stdio の子を確実に殺す）。`mcpCancel()` も呼ぶので、
  ここを飛ばしても `mcpc.NewManager` 自身の ctx-watcher が保険として閉じる。

**`ServedKinds` に足した理由。** `mcpreg.knownKinds` と `mcpreg.ServedKinds`（`materialize.go`）の両方に
`session.KindLcpp` を足した——`MaterializedKinds` には**足していない**（書くファイルが無いのは muse と
同じ）。`ServedKinds` は `sessionx/session_selfreport.go` の `selfReportToolAvailable` が直接読んでいる
軸で、ここに乗ったことで `lcpp` セッションにも自己申告のヒント文（`af_report` を呼べという 1 行）が
今回から付くようになった——狙った副作用であり、決定 2 の「出すのは有効化したサーバ＋`af`」の `af` 側が
実際に効く条件そのもの。

🔴 **CP と Console に同じ表が 2 つあり、両方直す必要があった。** `control-plane/internal/mcpsrv/mcp_server.go`
の `mcpKnownKinds`（`mcpreg.knownKinds` の写し）と `console/src/features/settings/mcp/mcpWire.ts` の
`MCP_KINDS` は独立した手書きの写しで、`control-plane/internal/mcpsrv/mcp_server_test.go` の
`TestMcpKnownKindsMirrorsTheOtherTwoCopies` がドリフトを検出する——このテストは**あらかじめ** `"lcpp"`
を期待リストに含んでいた（段 0/1 の時点で決定 6 を見越して書かれていた）ので、`workspace/agent` 側だけ
直して `go test ./workspace/agent/...` が緑でも、`control-plane` 側は赤いまま黙って見落とすところだった
（実際に一度落として確認した）。両方に `"lcpp": true` を足して緑にした。

🔴 **訂正（`sikdmnv` の指摘、2026-09-21）: 「`af` を有効にする＝毎ターン実プロセスを spawn する」は誤り
だった。** 初稿はここで `mcpMgr.Sync` が「渡された `ServerDef` をその場で dial（exec）する」と書いたが、
`Sync`（`internal/mcpc/manager.go:55-102`）は `m.servers` を走査し **`!reflect.DeepEqual(d, s.def)` の
ときだけ** 閉じて繋ぎ直す——def が前回と同じなら map 比較だけで終わり、`Connect`（= spawn）は呼ばれない。
`mcpreg.ForSession` はターンごとに変わる値（トークン・時刻）を作らず、`BuiltinAF` の `runArgs` も固定
なので、**`af` の spawn はセッションにつき 1 回（最初のターン）だけ**で、以後の毎ターンの `Sync` 呼び出し
はハンドシェイクではなく map 比較のコストしか払わない。`mcpc/manager_test.go` に無かった陰性対照
（`TestManager_SyncReconnectsOnDefChange` の逆——def が同じなら同じ `*Server` のまま）を
`TestManager_SyncIsANoOpWhenDefsAreUnchanged` として追加し、これで固定した（PR #869 へのレビュー
コメントを受けた追補コミット）。「本番のコストとして次の段に持ち越す」対象も、ターンごとの
ハンドシェイクではなく**セッション開始時 1 回ぶんの spawn+ハンドシェイク**の実測レイテンシに変わる。
`AF_AGENT_INSTALLED_BIN=/bin/false` によるテスト側の無害化（テストバイナリ自身の再帰 re-exec を防ぐ
ため）はこの訂正と無関係に必要——そちらは変えていない。

🔴🔴 **本物の製品欠陥（`sikdmnv` の指摘、CI で実測、2026-09-21）: 接続できないサーバはターンを毎回遅くする
——上の訂正は「成功する場合」の話で、「失敗する場合」は別の欠陥として残っていた。** PR #869 の CI 初回
実行（run 35614081914・`workspace-agent` ジョブ）で `lcpp` の**既存試験 10 本が全滅**した:
`/usr/local/bin/workspace-agent` が無い CI では `paths.ConfigExePath()` が **volatile な `ExePath()`
（＝テストバイナリ自身）にフォールバック**し、`af` の `Command` がテストバイナリ自身を指す——`dialStdio`
がそれを `-test.run` フィルタ無しで exec し、**テストの中からパッケージ全体の `go test` を再帰的に
起動する**格好になった。ログの `mcpc: af: context deadline exceeded` は、そのまま返ってこない子を
10 秒のハンドシェイク既定値まで待った跡。ローカルは `/usr/local/bin/workspace-agent` が実在し速く
失敗する（＝速く"成功"に見える）ため緑だった、という**環境差**。

指摘の核心は環境差の指摘だけでは終わらない: `runTurn` は毎ターン `syncMCPServers` を同期で呼び、
接続に失敗したサーバは `mcpMgr.servers` に入らないので**次のターンでも同じ `toConnect` に入り、また
満額のハンドシェイク待ちを払う**。「spawn はセッション 1 回」は接続が**成功する**場合の性質であって、
**失敗し続けるサーバがある配備では、`lcpp` の毎ターンが推論の前に接続タイムアウトぶん止まる**——
これは実装時から存在した本物の欠陥で、CI がたまたま最初に踏んだだけだった。

対応（PR #869 への追補コミット、3 点）:

1. **`mcpSyncBudget`（既定 3 秒）**: `syncMCPServers` が `mcpMgr.Sync` に渡す `ctx` を
   `context.WithTimeout(ctx, mcpSyncBudget)` で包み、定義側の `TimeoutMS` がどれだけ長くても
   **セッション側の上限として**上書きする。🔴 ただし stdio で「接続は受けるが応答も stdin close にも
   応じない」種類のハングには**完全な上限にならない**: `mcpc.Connect` はハンドシェイク失敗後
   `Close()` を呼ぶが、そちらは `ctx` を受け取らず `stdio.go` の非公開 `stdioKillGrace`（既定 3 秒）
   で `Kill()` に昇格するまで独自に待つ——この尾は budget を縮めても縮まらない。それでも無制限より
   大幅に良く、かつ backoff（次点）で最悪でも 30 秒に 1 回しか払わない。
2. **`mcpFailures`（backoff、既定 30 秒）**: 直前の `Sync` で失敗したサーバ名は、`next`（失敗時刻＋
   30 秒）を過ぎるまで**その名前を `mcpMgr.Sync` に渡す `defs` から除外する**——接続を試みること自体を
   スキップする。ストアへの記録（`NoteMCPError`）の重複排除も、この「現在壊れている」状態
   （`h.mcpFailures`）を基準に取り直した——以前の実装は「今回 `Sync` に渡した defs の errs」だけを
   シグネチャにしていたため、backoff で `defs` から除外されるたびにシグネチャが変わって見え、
   復旧→再故障を繰り返しているかのように誤って重複記録するところだった。
3. **既存試験の隔離を `TestMain` に一本化**: `AF_AGENT_INSTALLED_BIN=/bin/false` を個々の試験の
   `t.Setenv` ではなく `internal/agents/lcpp` パッケージの `TestMain` で 1 回だけ設定——加えて
   `mcpServersForSession`（`mcpreg.ForSession` を包む func-var の継ぎ目、`newHarnessClient` と同じ
   作法）を `TestMain` で「サーバ 0 件」を返すスタブに差し替えた。🔴 **`AF_AGENT_INSTALLED_BIN` だけの
   無害化では不十分**——`af` は無害化しても依然として**確実に失敗する**ため、`mcp_test.go` の外の
   `driver_test.go` 全試験に `NoteMCPError` レコードが 1 本ずつ混入し、レコード件数を厳密に数えている
   既存試験が壊れる（実際に手元で再現・確認済み）。`mcpTestHome`（この節の新規試験用ヘルパー）だけが
   `mcpServersForSession` を実装に戻す——それ以外の全試験は「サーバ 0 件」という、この PR 以前と
   バイト単位で同じ挙動を見る。

陽性対照（Edit で当てて赤確認・戻して緑確認、追加分のみ列挙）:
- `mcpFailures` の backoff 判定を無効化（`false &&` を足す）→ 新設の
  `TestMCPUnreachableServerDoesNotSlowDownLaterTurns` が赤（2 ターン目が 1 ターン目と同じだけ遅い）。
- `syncMCPServers` の `context.WithTimeout` を実質無制限に変える → 同じ試験が赤
  （`waitState` 自身の 3 秒待ちでタイムアウトして落ちる——スイート全体は固まらない）。
- `mcpc/manager_test.go` の `TestManager_SyncReconnectsOnDefChange` の条件に `true ||` を足す →
  `TestManager_SyncIsANoOpWhenDefsAreUnchanged` が赤（既に前段の訂正で確認済み、再掲）。

`mcpSyncBudget`/`mcpSyncBackoff` は `stdioKillGrace`（`mcpc/stdio.go`）と同じ理由で `const` ではなく
`var`——試験が実時間を溶かさず縮められる。

**ツール定義の費用（実測、この配備）。** 実 `workspace-agent mcp-stdio --self-report --chromium-attach`
に `mcpc.Connect` して `ToolDefs()` を JSON にしたもの（`--peer-messaging`/`--image-gen`/`--fleet-spawn`
はどれも off——利用者ごとの設定に依るため、この数字は**最小構成**）:

| 内訳 | 本数 | JSON バイト数 |
|---|---:|---:|
| `harness.BuiltinTools()`（既存の組み込み） | 9 | 3,635 |
| `af`（`--self-report --chromium-attach` のみ） | 15 | 13,175 |
| 合計（サーバ未登録でも `af` は常に乗る） | 24 | 16,810 |

`af` 1 個だけで送信ペイロードが素の組み込み分の **約 3.6 倍**になる。利用者が `--peer-messaging`/
`--image-gen`/`--fleet-spawn` を有効にした配備では `af` だけで 70 本超になる（別セッションの実測、この
ADR の範囲外）。利用者登録の外部サーバはこの上にさらに乗る——今回の試験で使った偽サーバ 1 個（ツール
2 本）で確認した以上の実測は取っていない。

**試験と陽性対照。** `internal/agents/lcpp/mcp_test.go`（新規）。フェイクサーバは `internal/mcpc` の
`helper_process_test.go` と同じ自己 re-exec トランポリン（`-test.run=TestHelperProcess`）を、この
パッケージ用に別実装したもの（`mcpc` のそれは package-private で再利用できない）。

- 🔴 **`mcpreg.ForSession(session.KindLcpp)` は無条件で `af` を含む**（上述）——最初の対策
  （`AF_AGENT_INSTALLED_BIN=/bin/false` を各試験の `mcpTestHome` から）は CI の再帰 spawn は塞いだが
  それだけでは足りず、後で `TestMain` ＋ `mcpServersForSession` スタブに置き換えた。理由と最終形は
  「本物の製品欠陥」節（下）にまとめて書いた——ここでは重複させない。
- 陽性対照（`git checkout` ではなく Edit で当てて戻した。全て確認済み）:
  - `driver.go` の `h.mcpTools()...` をレジストリ構築から外す → `TestMCPToolReachesRealServer`/
    `TestMCPToolRequiresApprovalByDefault` が赤。
  - `mcpToolMutates` を `false` 固定にする → `TestMCPToolRequiresApprovalByDefault` が赤（承認待ちに
    ならない）。
  - `syncMCPServers` の de-dup 判定を常に `true`（毎回書く）にする → `TestMCPSyncErrorDoesNotFailTurnAndIsNotedOnce`
    が赤（2 ターンで note が 2 件）。
  - `closeIdleResources` から `mcpMgr.Close()`（と `mcpCancel()`）を外す → `TestDropHandleKillsMCPStdioChild`
    が赤（子プロセスが `DropHandle` 後も生きている——実際に 1 回、子プロセスが残った状態で赤を確認して
    から kill した）。
  - backoff とタイムアウト上限それぞれの陽性対照は「本物の製品欠陥」節に書いた。
- 通し（`go test ./internal/agents/lcpp/... -count=1`）は 54 件全緑（`TestHelperProcess` 込み）・
  0.5 秒前後——実時間を払う試験は `TestMCPUnreachableServerDoesNotSlowDownLaterTurns` のみで、
  `mcpSyncBudget`/`mcpSyncBackoff` を試験用に縮めているので実質数百ミリ秒。`control-plane`/`console`
  は既存スイートに新規失敗なし（後述の CP ドリフト試験 1 件を除く）。CI（run 35617795561、
  35614081914 が赤だった `workspace-agent` ジョブの次の実行）で緑を確認済み——7 チェック全通過。
- 実機の通し経路（配備済み Agent から実際に `lcpp` セッションを起こして LAN の llama-server まで）は
  この節の範囲外——利用者の LAN の llama-server に自分から繋ぐなという指示のとおり、偽サーバのみで
  試験した。実機測定は利用者側の担当。

**範囲外にしたもの（決定どおり）。** プロジェクトスコープ（`mcpproj` 経由の `.mcp.json`）は今回触って
いない——`lcpp` は他 kind の `.mcp.json` を読むだけの kind でも、コピー先の kind でもない（決定 6 の
既存の文言のまま）。
