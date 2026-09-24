# 110. lcpp が起動する builtin af MCP 子プロセスへ AF_SESSION_NAME を渡す

- 依頼: `[agent-fleet:spawn from=sjqlfyj]`。親がセッション `svcnyrc` で実測済み: `GET
  /imagegen/status?session=svcnyrc` は `enabled=true, ready=true, provider=image` なのに、
  lcpp が起動する builtin af MCP 子は `args` に `--image-gen` 等を持ちながら
  `AF_SESSION_NAME=<unset>, cwd=/home/dev`（Agent デーモン自身の cwd）で、`generate_image` が
  `tools/list` に一切広告されない。空 model は別件（[109](109-lcpp-empty-model-silent-failure.md)、
  既にマージ済み）。
- 関連: [93](../decisions/0093-lcpp-agent-kind.ja.md)（lcpp kind 本体・「in-process, no child
  process and no daemon」の設計）/ [95](../decisions/0095-muse-agent-kind.ja.md)
  （muse が同種の env scrub 問題を先に踏んだ P2-14、`mcpreg.ForwardEnvNames`）。

## 原因

`internal/mcpx/mcp_stdio.go` の `mcpOwningSession()` は builtin af MCP サーバーが自分の
オーナーセッションを知る唯一の契約で、`AF_SESSION_NAME` → 失敗時のみ cwd→session dir の
フォールバックという順で解決する。`mcpImageGenAdvertise()`（`generate_image` を
`tools/list` に載せるかどうかの判定）を含め、この関数の呼び出し箇所は 11 か所ある
（下記「影響範囲」）。

TERMINAL セッションの CLI（claude 等）は tmux 起動 env（`session_tmux.go` の
`AF_SESSION_NAME=`+m.Name）を素直に継承して自分が起動する子に渡す。codex の MANAGED
セッションはスレッド設定（`mcpreg/thread_codex.go` の `CodexThreadServers`）で、muse は
wire 越しの `session/start.config.mcpServers`（`internal/agents/muse/mcp.go`）で、それぞれ
専用の per-session チャンネルから運ぶ——どちらも「vendor CLI 自身の子プロセス起動」を
経由するので、AF 側のコードが env/config を組み立てる時点でセッション名を知っている。

lcpp には vendor CLI が無い（0093 の設計そのもの：「in-process, no child process and no
daemon」）。`internal/agents/lcpp/driver.go` の `runTurn` は Agent デーモン自身の
goroutine から直接 `internal/mcpc.Manager.Sync` → `mcpc/stdio.go` の `dialStdio` を呼び、
`dialStdio` は `env := os.Environ()`（**呼び出し元プロセス＝Agent デーモン自身の env**）+
`def.Env` で子の環境を組み立てる。デーモンは全セッション共有の 1 プロセスなので、
`os.Environ()` に特定セッションの `AF_SESSION_NAME` が乗っている理由がどこにも無い——
cwd も同様（`dialStdio` は `cmd.Dir` を設定しない＝デーモンの cwd をそのまま継承する）。
生成される MCP 子ごとに新しい per-session プロセス境界を作るホストが 3 つとも持つ
「セッション名を運ぶ専用チャンネル」を、lcpp だけが最初から持っていなかった、という形。

## 修正

`internal/agents/lcpp/mcp.go` に `injectSessionName(defs []mcpreg.ServerDef, name string)
[]mcpreg.ServerDef` を追加し、`driver.go` の `runTurn` が `mcpServersForSession` の結果を
`h.syncMCPServers` に渡す直前で呼ぶ:

```go
mcpDefs, mcpErr := mcpServersForSession(session.KindLcpp)
...
mcpDefs = injectSessionName(mcpDefs, h.name)
h.syncMCPServers(ctx, mcpDefs)
```

- 識別は `Origin==mcpreg.OriginBuiltin && ID==mcpreg.BuiltinAF` のペア（`attach.go` の
  `extraEnvVars`・`thread_codex.go` の `CodexThreadServers` と同じ鍵）。**Name では判定しない**
  ——`AFServerName()` はレポジトリの影に応じて改名される（docs/log/48 §8.4）ので、ユーザーが
  偶然 "af" という名前で自前サーバーを登録していても巻き込まない。
- 対象 def の `Env` map は **新しく作ったコピー**に既存エントリをコピーしてから
  `AF_SESSION_NAME` を足す。`mcpServersForSession(session.KindLcpp)` が返す `[]ServerDef` は
  毎ターン呼び直されるとはいえ（`mcpreg.Load` が `secrets.Load()` から毎回組み立て直す）、
  「共有された定義行の `Env` map を in-place で書き換えない」という契約そのものは
  `injectSessionName` の外の事情に依存させず、関数の中だけで閉じた。builtin af 以外の
  def（外部 MCP サーバー・偶然名前が "af" のユーザー定義行を含む）は一切変更せず、
  そのまま通す。
- 名前が空（`h.name == ""`）のときは no-op——`mcpOwningSession` の cwd フォールバックが
  今までどおり働く形を壊さない。

muse の `mcpServerConfig`（`internal/agents/muse/mcp.go`）・codex の `codexAFThreadEntry`
（`mcpreg/thread_codex.go`）と同じ形の「builtin af の Env だけ、コピーの上で 1 個足す」
パターンで、`mcpreg.ForwardEnvNames`（`AGENT_TOKEN`/`AGENT_ADDR`/`AF_CP_BASE_URL`/
`AF_MEMO_TOKEN`）は lcpp では不要——デーモン自身の `os.Environ()` に workspace レベルの
それらは元々乗っており（codex のような default-deny スクラブが無い）、欠けていたのは
セッション固有の `AF_SESSION_NAME` だけだった。

## 影響範囲（AF_SESSION_NAME 依存の session-bound af tools）

初稿はここで「`mcpOwningSession()` を呼ぶ 11 箇所すべてが」と書いた直後に `af_report` を
その一覧へ含めており、`af_report` は実は呼ばない、と自分で注記する自己矛盾があった
（レビュー指摘）。`rg -n "mcpOwningSession\(\)" mcp_stdio.go mcp_imagegen.go` の生の呼出
箇所（関数定義そのものを除く 11 行）を 1 本ずつ、どの `tools/call` ケース／`tools/list`
判定から辿り着くか対応づけた上で、(a) 直接依存・(b) 明示引数検証（呼ばない）・
(c) degrade-only の 3 つに分け直す。推測で名前を足していない——後段の表の右列は
すべて実際にその関数を呼んでいるコードの行番号。

### (a) mcpOwningSession 直接依存——解決できないと呼び出し／広告そのものがブロックされる

| tools/call の名前 | 経路 | 呼出行 | 失敗時の挙動 |
|---|---|---|---|
| `generate_image` | `mcpImageGenAdvertise()`（`tools/list` 判定） | `mcp_stdio.go:1474` | **広告されない**（ツールが `tools/list` に一切出ない——親が最初に報告した症状そのもの） |
| `generate_image` | `mcpGenerateImage()`（`tools/call` 本体） | `mcp_imagegen.go:210` | `mcpToolErr`。advertise を素通りした呼び出し（未広告でも名前を直接呼べば届く）への保険で、通常経路では advertise 側で先に止まる |
| `list_child_sessions` | `case "list_child_sessions"` | `mcp_stdio.go:2433` | `mcpToolErr` |
| `list_peer_sessions` | `case "list_peer_sessions"` | `mcp_stdio.go:2439` | `mcpToolErr` |
| `send_to_peer_session` | `case "send_to_peer_session"` | `mcp_stdio.go:2460` | `mcpToolErr` |
| `propose_session_handoff` | `case "propose_session_handoff"` | `mcp_stdio.go:2508` | `mcpToolErr` |
| `create_session` | `case "create_session"`、`selfReportOnly()` のときだけ | `mcp_stdio.go:2789` | `mcpToolErr`（`parent` を決められない＝ADR 0073 の provenance/idempotency scope/report route/worktree default 全部の起点） |
| `stop_session` / `stop_session_after_turn` / `resume_session` / `rename_child_session` / `get_session_output` | 5 ケースとも共通ゲート `sessionDriveAllowed(a.Name)` 経由（`selfReportOnly()` のときだけ） | ゲート自体の呼出は `mcp_stdio.go:1081`、5 ケースの呼び出し元は `2946`/`2974`/`3049`/`3078`/`3221` | `sessionDriveAllowed` が返すエラーがそのまま `mcpToolErr` になる。**初稿はこの 5 つを丸ごと書き漏らしていた**——af_report の自己矛盾と対になる本体の抜け |
| `af_stop_after_turn` | `case "af_stop_after_turn"`、モデルが `session` 引数を**省略した**ときだけの fallback | `mcp_stdio.go:2561` | `mcpToolErr`。**引数を渡された場合はこの行に到達せず (b) 側で完結する**（下表） |

### (b) 明示引数検証——mcpOwningSession を呼ばない

| tools/call の名前 | 実際の依存 | 呼出行 |
|---|---|---|
| `af_report` | `session.ValidName(a.Session)` のみ。フォールバック無し | `mcp_stdio.go:2539` |
| `af_stop_after_turn`（`session` 引数を渡した場合） | 同じく `session.ValidName(name)` で通過し、`mcpOwningSession()` の行（2561）には到達しない | `mcp_stdio.go:2557`-`2558` |

`mcpSourceSession`（`RunStdio` が起動時に一度読む `os.Getenv("AF_SESSION_NAME")`、
`mcp_stdio.go:180`）を **tool ケースが直接参照している箇所は無い**——`grep -n
"mcpSourceSession" mcp_stdio.go` の結果は代入 1 箇所（180）と `mcpOwningSession()`
自身の第一分岐 1 箇所（3331-3332）だけ。つまり「起動時 `mcpSourceSession`」は (a) の
実装の中の入力であって、それを個別に読む第三のツール集合は存在しない——初稿の書き方
（「呼ぶ箇所」とだけ言って (a) と別扱いするかのように読めた点)を今回訂正する。

### (c) degrade-only——解決できなくても呼び出し自体は成功する

| 経路 | 呼出行 | 失敗時の挙動 |
|---|---|---|
| `outputCursorScope()`（`mcpSessionOutput` 内、`get_session_output` の tail カーソル記憶） | `mcp_stdio.go:3302`（`mcpSessionOutput` からの呼出は `3771`） | `""` を返しカーソル記憶なしに戻るだけ。**`get_session_output` 自体は (a) の `sessionDriveAllowed` ゲートを別途持つ**ので、この経路はゲートを通過した後の二次的な質の劣化——同じツールに (a) と (c) の依存が両方乗っている形 |
| `mcpRequestBrowserAction()`（`request_browser_action`） | `mcp_stdio.go:3529` | `sessionName` を省いて通知するだけ、呼び出し自体は失敗しない（`browser_handoff_ledger.go` のコメントどおり best-effort）。`set_chromium_control_mode` は `mcpOwningSession` を一切呼ばない（grep 上、依存なし） |

まとめると、`mcpOwningSession()` の全 11 呼出箇所は (a) 直接依存 9 箇所（`generate_image`
向け 2・`list_child_sessions`/`list_peer_sessions`/`send_to_peer_session`/
`propose_session_handoff`/`create_session` 向け各 1 で計 5・`sessionDriveAllowed` の
共有ゲート 1・`af_stop_after_turn` の fallback 1）と (c) degrade-only 2 箇所
（`get_session_output` のカーソル・`request_browser_action` の handoff 通知）に分かれる。
tools/call の名前で数えると (a) は `generate_image`・`list_child_sessions`・
`list_peer_sessions`・`send_to_peer_session`・`propose_session_handoff`・`create_session`・
`af_stop_after_turn`・`sessionDriveAllowed` 配下の `stop_session`/
`stop_session_after_turn`/`resume_session`/`rename_child_session`/`get_session_output`
の計 12 名——lcpp の builtin af 子は今回の修正までこの 12 名すべてで解決不能だった
（`generate_image` は advertise 段階で完全に消え、残りは呼べても即エラー）。(b) の 2 つ
（`af_report`・引数付き `af_stop_after_turn`）は `mcpOwningSession` を呼ばないので今回の
修正の影響を受けない（後述「未解決」）。(c) の 2 つは壊れたままでも気づかれにくい形で
動作は継続していた。

## 検証

`workspace/agent` 内、すべてパイプ無し・`echo $?` で exit code を直接確認:

- `go build ./...` → **exit 0**
- `go vet ./...` → **exit 0**
- `gofmt -l .` → 出力なし、**exit 0**
- `go test ./... -count=1 -p 2` → **exit 0**、`Go test: 3952 passed in 51 packages`
- `go test ./internal/agents/lcpp/... -count=1` → **exit 0**、60 件（新規 3 件含む）
- `go test ./internal/agents/lcpp/... -run 'TestInjectSessionName|TestMCPBuiltinAFChildLearnsOwningSessionName' -count=5 -v` →
  **exit 0**、15 件（新規 3 本だけを単独で 5 回、毎回緑——後述の「既知の赤」から意図的に
  切り分けた形）
- `python3 scripts/docs-check.py` → 初回 **exit 1**（この文書自身が
  `../decisions/0095-meta-muse-code-agent-kind.ja.md` という誤ったパスを書いていた——
  実ファイルは `0095-muse-agent-kind.ja.md`。壊れたリンクを検出する側が実際に落ちることを
  意図せず確認した形の陽性対照）。パスを直して再実行 → **exit 0**、`445 files, 0 error(s),
  0 warning(s)`。

新規試験 3 本（`internal/agents/lcpp/mcp_test.go`）:

- `TestInjectSessionNameCopiesOnlyBuiltinAF` — `injectSessionName` の純粋関数としての単体
  試験。builtin af の `Env` にだけ `AF_SESSION_NAME` が足され、既存エントリは保持され、
  **Name が偶然 "af" と一致するだけの非 builtin 定義行は一切変更されない**（Origin/ID 判定
  であって Name 判定でないことの直接証拠）ことを確認。呼び出し元が渡した元の `Env` map が
  in-place で変異していないこと（同じ map を 2 回目の呼び出しにも使い、1 回目の名前が
  残っていないこと）も確認。
- `TestInjectSessionNameNoopWithoutAName` — 空文字名は no-op（既存の cwd フォールバックを
  壊さない）。
- `TestMCPBuiltinAFChildLearnsOwningSessionName` — プロセス境界の回帰試験。この test
  バイナリ自身を builtin af 定義（`Origin=OriginBuiltin, ID=BuiltinAF`）として実際に
  stdio 子プロセスで spawn し、新設の `whoami` ツール（子が自分の `os.Getenv
  ("AF_SESSION_NAME")` をそのまま返す）を呼んで検証。同じ turn で **外部（非 builtin）の
  fake MCP サーバーも並走**させ、その `whoami` 結果は注入されず親（テストプロセス＝
  daemon 役）の env をそのまま引き継ぐことを確認（生きたプロセス境界での陰性対照）。
  セッション A → B の順で同じ `ServerDef`（同じ Go 値・同じ `Env` map インスタンス）を
  2 回使い回して実行し、A の名前が B に漏れないことを確認（bleed の否定）。駆動している
  test プロセス自身の `AF_SESSION_NAME` は第三の無関係な値に固定した状態で走らせている
  ——どの結果も「たまたま親の env に正しい値が乗っていた」では説明できない。

変異試験（Edit で戻す。git checkout は使っていない）:

1. `driver.go` の `injectSessionName(mcpDefs, h.name)` を `injectSessionName(mcpDefs, "")`
   に変えて実行 → `TestMCPBuiltinAFChildLearnsOwningSessionName` が赤（daemon の sentinel
   値がそのまま返る）。Edit で戻して確認、再度緑。
2. `mcp.go` の識別条件を `d.Origin == mcpreg.OriginBuiltin && d.ID == mcpreg.BuiltinAF` から
   `d.Name == mcpreg.BuiltinAF`（Name だけで判定）に変えて実行 →
   `TestInjectSessionNameCopiesOnlyBuiltinAF` が赤（Name が偶然 "af" のユーザー定義行にも
   注入されてしまう）。ライブ試験（`TestMCPBuiltinAFChildLearnsOwningSessionName`）は
   **この変異では緑のまま**——builtin af 自身の Name もたまたま "af" なので、Name 判定でも
   本来のケースは壊れない。単体試験だけがこの欠陥を捕まえる、という非対称性を確認した上で
   Edit で戻した。
3. `mcp.go` の `Env` コピー処理を「新しい map を作ってコピー」から「`d.Env` が nil なら
   空 map を作ってその場で書き込む」（in-place 変異）に変えて実行 →
   `TestInjectSessionNameCopiesOnlyBuiltinAF` が赤（呼び出し元の元の `Env` map に
   `AF_SESSION_NAME` が書き込まれてしまっている）。Edit で戻して確認。

## 追記（レビュー指摘の訂正）

初稿の「影響範囲」節は「`mcpOwningSession()` を呼ぶ 11 箇所すべてが」と書いた直後の一覧に
`af_report` を含め、かつ同じ一覧の中でそれを「呼ばない」と注記する自己矛盾を含んでいた。
`sessionDriveAllowed` 経由で依存している 5 ツール（`stop_session` /
`stop_session_after_turn` / `resume_session` / `rename_child_session` /
`get_session_output`）も丸ごと欠落していた。「影響範囲」節を (a) 直接依存・(b) 明示引数
検証（呼ばない）・(c) degrade-only の 3 分類に全面的に書き直し、全 11 呼出行を
`tools/call`/`tools/list` の対応するケースへ 1 対 1 で対応づけた（推測で名前を足していない
——各行は実際にその関数を呼んでいるコードの行番号）。`get_session_output` と
`generate_image` はそれぞれ (a) を 2 経路持つ二重依存であることも今回明確化した。

このドキュメント自身の修正のみで、`workspace/agent` 配下のコード（`mcp.go`/`driver.go`/
`mcp_test.go`）は変更していない。念のためすべてパイプ無し・`echo $?` で直接確認:

- `python3 scripts/docs-check.py` → **exit 0**、`445 files, 0 error(s), 0 warning(s)`
- `go test ./internal/agents/lcpp/... -count=1` → **exit 0**、`Go test: 60 passed in 1
  packages`
- `go test ./... -count=1 -p 2` → **exit 0**、`Go test: 3952 passed in 51 packages`

既知の赤（本件と無関係と確認済み、`internal/agents/lcpp` の既存の仕組み）:

- `go test ./internal/agents/lcpp/... -count=5`（パッケージ全体）は新規試験の有無に
  関わらず赤くなる——新規 3 本を一時的にリネームして `Test` プレフィックスを外し
  （`sed` で `TestX` → `xTestX`、走らせた後 Edit で戻した）走らせても、同じ失敗集合
  （`TestDriverSendPersistsTurnAndCompletes` / `TestForkWholeConversation` /
  `TestMCPToolReachesRealServer` 等、MCP と無関係な既存試験まで含む）が同じ形
  （`records = []`）で再現した。原因は [109](109-lcpp-empty-model-silent-failure.md) が
  既に特定済みのグローバル `handles map[string]*threadHandle`（セッション名だけで
  キーイングしており、同一プロセス内の `-count=N` 繰り返しで前の回の（片付け済みディレクトリを
  指す）ハンドルを再利用してしまう）。新規試験 3 本を**単独で** `-run` 指定して
  `-count=5` すると 15/15 緑（上記「検証」参照）——本件が持ち込んだ赤ではない。

## 未解決 / 範囲外

- **モデルが自分のセッション名を明示引数として知る必要が残る継ぎ目は `af_report` だけ
  ——`propose_session_handoff` と `send_to_peer_session` は今回の修正で実用経路が回復
  している。** レビュー指摘を受けて schema/dispatch を実コードで再確認した:
  - `propose_session_handoff`（`mcpStdioSelfReportTools`、`mcp_stdio.go:646`）の
    `inputSchema` は `prompt`/`title` の 2 つだけが `required`（`mcp_stdio.go:659-660` の
    2 フィールド、`required` 宣言は `662`）。オーナー（＝提案元セッション自身）は
    `case "propose_session_handoff"` の中で `name, err := mcpOwningSession()`
    （`mcp_stdio.go:2508`）がサーバー側だけで解決し、モデルは一切渡さない。今回の修正で
    この呼出が解決するようになったので、このツールはフルに回復する。
  - `send_to_peer_session`（`mcp_stdio.go:730`）の `name` 引数は schema の説明文自体が
    「Destination session name (the name from list_peer_sessions)」（`mcp_stdio.go:752`）
    ——**宛先**であって自分の名前ではない。送信元（`from`/`peer_from`）は
    `case "send_to_peer_session"` 内の `self, err := mcpOwningSession()`
    （`mcp_stdio.go:2460`）がサーバー側だけで解決する（`mcp_stdio.go:2479`）。宛先候補は
    同じく `mcpOwningSession()` で自分を除外する `list_peer_sessions`
    （`mcp_stdio.go:2439`）が返す一覧から得るので、モデルは他セッションの名前を知って
    いれば足り、自分の名前を知る必要はない。こちらもフルに回復する。
  - `af_report`（`mcp_stdio.go:666`）だけが違う: schema の説明文自体が「The server writes
    the body, so pass only your own session name」（`mcp_stdio.go:672`）と明記しており、
    `case "af_report"` は `session.ValidName(a.Session)`（`mcp_stdio.go:2539`）を検証する
    だけで `mcpOwningSession()` を一切呼ばない——サーバー側に解決する経路そのものが無い。
    人間向けの案内文（`handoffReportBackNote()`, `mcp_stdio.go:595`）は「プロンプトに
    `$AF_SESSION_NAME` を書け」と教えるが、これはシェル変数の展開を前提にした文言で、
    `internal/harness/tools_bash.go` の bash ツール（`exec.CommandContext(runCtx, "bash",
    "-c", cmd)`、`cmd.Env` 未設定＝これも Agent デーモン自身の env をそのまま継承）は
    **同じ理由で** `AF_SESSION_NAME` を持たない——`echo $AF_SESSION_NAME` は空を返す。
    `h.name` はシステムプロンプトにもツール引数にも一切埋め込まれていない
    （`internal/agents/lcpp/*.go` を `h.name` で grep した限り、ログ出力以外の使用箇所
    なし）。今回の修正が解決したのは「MCP 子プロセス自身がオーナーを名乗れるか」であって、
    `af_report` に限っては「モデルが自分の名前を知る手段」も別に要る——これは今回の依頼
    （af MCP 子への env 伝播）の範囲外と判断し、別件として記録するに留める。
  → Issue #951 に起票（2026-09-24）
- `create_session` の `parent` 決定・`list_child_sessions`/`list_peer_sessions` は
  実際に子/兄弟セッションを立てないと end-to-end では検証していない（単体+プロセス境界の
  試験で `mcpOwningSession` が正しい名前を返すことまでは確認済み）。
- 実機 LAN/GPU・MCP 設定/秘密/借用行/購入モード/目録は一切触っていない。
