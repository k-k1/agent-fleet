# 117. Managed セッションの af MCP 子へ AF_SESSION_NAME を届ける（#978）

- 依頼: #978。セッション側の af MCP サーバは `AF_SESSION_NAME` で自分の持ち主を知る
  （`mcpx.mcpOwningSession`）。Terminal は tmux の起動 env で届くが、Managed は kind ごとに
  経路が要る。issue 本文の表は「copilot / cursor / kiro は共有プロセス」「codex はデーモン
  入れ替え後に失う」としていたが、どちらも実測で覆った。
- 関連: [27](27-agent-managed-driver.md) §9.3.1（codex の thread config）/
  [110](110-lcpp-af-mcp-child-session-name.md)（lcpp）/ #951（lcpp の af_report）/ #960
  （スタジオを Managed kind へ開く）/ #989（opencode のプラグイン経路）。
- 版: codex-cli 0.156.1 / copilot 1.0.88 / cursor-agent 2026.09.23-86fc751 / opencode 1.18.32 /
  kiro-cli（イメージ同梱版）。

## 測り方（課金なし）

実 CLI を本番と同じ ACP の起動引数で立て、親プロセスに目印の env
（`AF_SESSION_NAME=probe-<kind>`、`AF_PROBE_MARK=mark-<kind>`）を付ける。送るのは
`initialize` と `session/new` だけでターンは無い。そのあと、ユーザー全体の設定から起動された
`workspace-agent mcp-stdio` 子プロセスの `/proc/<pid>/environ` を読む。

## 実測結果

### ACP 3 kind は「セッションごとに 1 プロセス」

`threadHandle.spawn` がセッションごとに子を立てている。issue の「共有プロセス」は誤りで、
**プロセス env がそのままセッション単位の経路になる**。

| kind | ユーザー設定の af 子が受け取った env | ACP `session/new.mcpServers` の扱い |
|---|---|---|
| copilot | 親の env をそのまま（`AF_SESSION_NAME`・`AF_PROBE_MARK`・`AGENT_TOKEN`） | 無視（起動されない） |
| kiro | 親の env をそのまま | 起動される。親の env を継承し、`env` の値も載る |
| cursor | **`HOME` `PATH` `SHELL` `TERM` の 4 つだけ** | 起動される（`env` はリテラル）が、**ユーザー設定の af と二重に起動**される |

- cursor の `~/.cursor/mcp.json` は `env` の値にある `${env:NAME}`（と `${NAME}`）を**自身の
  プロセス env から展開する**（独立した HOME で実測）。未設定の変数は `${env:NAME}` の
  **リテラルのまま**子に渡る。ACP で渡したエントリは展開されない。
- 🔴 **別件の不具合**: cursor は MCP 子の env を削るので、af 子に `AGENT_TOKEN` が無く、Agent への
  呼び出しがすべて `401 missing or invalid agent token` になっていた（Terminal・Managed の両方。
  muse が ADR 0095 P2-14 で踏んだものと同じ形）。

### muse

ホストもセッションごとに 1 本（`handle.go`）。MCP 子の env は削られるので、wire の `env` が経路。
これまでの実装は `AF_SESSION_NAME` を Agent 自身の env から転送しようとしており、その env には
値が無いため何も渡っていなかった。

### codex: デーモン入れ替え後も名前は届いている

新設の `TestLiveDriftCodexThreadMCPConfigAppliesOnColdResume`（`driftlive`、実ターン 1 回で
約 15.7k tokens）: thread を名前 A で開始し 1 ターン → `Supervisor.Shutdown()` → 新しいデーモン →
別のサーバ名と名前 B で `thread/resume`。**結果: 新しいサーバが thread の在庫に現れ、プローブは
B を読んだ。**

27 §9.3.1 ⑥ の「resume は `config.mcp_servers` を適用しない」は、**同じ生きたデーモン**への resume
（`TestLiveDriftCodexThreadMCPConfigAppliesOnResume`）の結果だった。codex は読み込み済みの thread を
そのまま返す（codex-rs `thread_processor.rs` の `thread_resume_inner` → `resume_running_thread`）。
読み込まれていない thread は、リクエストの `config` から設定を組み直す（同関数の
`load_for_cwd(request_overrides, …)`）。**測った形と一般化した形がずれていた**のが誤りの原因で、
「デーモンが入れ替わると失う」は測っていなかった。driver は start / resume / fork のいずれでも
`m.Name` を渡しているので、コードの変更は要らない。

### opencode

`contract_mcp_identity_test.go`（`-tags clicontract`）を 1.18.32 で再実行。`POST /session` に
MCP・config の口は無く、同じディレクトリのセッションは af 子を 1 つ共有する（3 セッション・
2 ディレクトリで spawn 2 回）。1.18.15 から変わらず。

一方、opencode のプラグインフック `tool.execute.before` は MCP ツールでも発火し、opencode の
セッション ID と、`client.callTool` にそのまま渡る `args` を受け取る（`session/tools.ts`、
v1.18.32）。AF は `rtk.ts` を同じ仕組みで入れている。これを使う経路は #989 に切り出した。
→ #989 は [119](119-opencode-caller-session-plugin.md) で実装した。

## 実装

- `agents.WithSessionName`: 子の env にある既存の `AF_SESSION_NAME` を置き換える（Agent の env に
  あるなら別セッションの値なので、後ろに足すのではなく差し替える）。copilot / cursor / kiro の ACP
  子と muse ホストに使う。muse ホストと ACP 子に入れたことで、モデル自身のシェルにも Terminal と
  同じく `$AF_SESSION_NAME` が入る。
- muse: `sessionMCPServers(owner)` → builtin af の wire `env` に名前を入れる。Agent の env の値は
  使わない。
- cursor: `mcpreg.cursorStdioEnv` が、builtin の必要とする変数（`extraEnvVars`）を
  `${env:NAME}` の参照として `mcp.json` に書く。秘密はディスクに載らない。定義自身の値が優先。
- `mcpx.dropUnexpandedEnv`: `mcp-stdio` / `mcp-run` の起動時に、値が `${env:<自分の名前>}` の
  変数を未設定に戻す。自己参照だけを消すので、誰かが選んだ値を取り違えることはない。
- `af_report` の `session`、セッション側 `get_session_status` の `name` を省略可能にし、省略時は
  `mcpOwningSession()` で埋める。引き継ぎ説明の「`$AF_SESSION_NAME` を書け」は「名前の無い
  `get_session_status` が返す」へ変えた（#951 の lcpp も同じ修正で解ける）。

## 受け入れ（課金なし）

- 同じフォルダで 2 本を同時に起動（copilot / kiro は本物の設定、cursor は独立 HOME と新形式の
  `mcp.json`）: 各 af 子の `AF_SESSION_NAME` はそれぞれ自分の名前。cursor の af 子にも
  `AGENT_TOKEN` が届いた。この環境には `AGENT_ADDR` が無く、cursor の af 子には
  `${env:AGENT_ADDR}` がリテラルで渡った。
- 新しいバイナリの `mcp-stdio --self-report` に cursor と同じ env（`AGENT_ADDR=${env:AGENT_ADDR}`）
  を与え、`get_session_status` を名前無しで呼ぶと、実 Agent から自分のセッションの状態が返った。
  対照として、イメージ同梱の旧バイナリは同じ env で
  `invalid port ":AGENT_ADDR}"` になる。
- 残り: 配備後に実 Managed セッションで、kind ごとに af ツールが自分に解決されることを確かめる。
  muse は wire の `env` が子に届くことを ADR 0095 の B1-4 で実測済みで、名前はその 1 項目。
