# 119. Managed opencode の呼び出し元セッションをプラグインで届ける（#989）

- 依頼: #989（#978 の案 b を切り出したもの）。Managed opencode は af MCP 子に「どのセッションか」を
  伝える経路が無く、セッションに結び付く af ツールは cwd と生死の推定に落ちる。同じ worktree で
  2 本が動いていると曖昧として断る。
- 関連: [117](117-managed-af-session-name-delivery.md)（他 kind の経路と、opencode に設定の口が
  無いことの再測定）/ [27](27-agent-managed-driver.md) §9.3.1。
- 版: opencode 1.18.32。

## 測り方（課金なし）

偽の OpenAI 互換エンドポイント（決まった tool_call を返すだけ）を custom provider として登録し、
本物の `opencode serve` に Python の stdio MCP（受け取った `tools/call` の引数と cwd を記録する）を
2 つ繋いだ。1 つは af と同じ形の名前（`af_<8 桁 16 進>`）、もう 1 つは無関係な名前にした。
同じディレクトリに 2 セッションを作り、1 ターンずつ回した。

## 実測結果

1. **`tool.execute.before` は MCP ツールでも発火**し、`input.sessionID` は opencode のセッション ID。
   `output.args` に足した鍵は、MCP 子の `tools/call` の `arguments` にそのまま届いた。
   同じ子を共有する 2 セッションは、それぞれ自分の ID を届けた。
2. **スキーマ検査で弾かれない。** af のツールは `additionalProperties: false` だが、検査はモデル
   出力に対して先に済んでおり、フックはその後に動く。モデル自身が同名の鍵を書いた場合も弾かれず、
   プラグインがあれば上書きされ、**無ければ書いた値がそのまま届く**。
3. 🔥 **足した鍵は opencode のツール記録（`state.input`）に残り、次のターンでモデルに返る**
   （assistant の `tool_calls.arguments`）。`tool.execute.after` で消しても記録は書き込み済みで
   手遅れだった。AF のミラーはツール入力から決まった鍵（command / path など）しか表示しないので、
   画面には出ない。モデルが鍵を覚えて自分で書いても、2 のとおり上書きされる。
4. MCP 子の cwd はセッションのディレクトリ（既存の cwd 推定が前提にしていたことの確認）。

## 実装

- `workspace/opencode-plugin/agent-fleet-caller.js`: ツール名が `^af_[0-9a-f]{8}_` のときだけ
  `args._af_caller_sid = input.sessionID` を**上書き**する。他社の MCP には触らない。旧名の素の
  `af` は `af_` だけでは他のツールと区別できないので対象外（cwd 推定のまま）。entrypoint が起動
  ごとに `*.js` をコピーするので、配布と更新は `agent-fleet-status.js` と同じ経路に乗る。
- `mcpx.mcpStdioCall`: 引数から `_af_caller_sid` を取り除いてから処理する（メモ系は引数を CP へ
  そのまま中継するため、先に除く必要がある）。値は 1 回の呼び出しの間だけ持つ（処理は直列）。
- `mcpx.mcpOwningSession` の順:
  1. `AF_SESSION_NAME`（プロセス単位の事実。呼び出しごとの申告では上書きしない）
  2. `mcpCallerSession`: 作業フォルダ（`Dir` か `CWD()`）が一致し、アーカイブされていない
     opencode セッションのうち、AF の sid ストア（`opencode.SlotSessionID`、Managed ドライバが
     書く）の値が申告と一致するものが**ちょうど 1 つ**あり、しかも Agent が生きていると答えたとき
     だけ採る
  3. 今までの cwd と生死の推定
- 🔥 **tools/list には申告が乗らない。** `generate_image` は一覧を作る時点で持ち主を解決し、
  解決できなければ一覧に出さない（`mcpImageGenAdvertise`）。呼び出し側だけ直しても、同じ worktree
  に 2 本いると道具そのものが見えず、受け入れ条件を満たせない。そこで、フォルダの生きている
  セッションが**全部 Managed opencode**（＝どの呼び出しも申告を運べる）のときに限り、スタジオでない
  1 本を代表にして一覧に出し、誰のものかは呼び出しの申告で決める（`mcpStampedFolderSessions`）。
  スタジオの道具が「持ち主が見分けられないなら出して、呼び出しで理由付きで断る」（ADR 0100）のと
  同じ形。共有された子の一覧はもともと全セッションで 1 つなので、1 本ごとに変えることはできない。
  他 kind が混ざる・生死が読めない・全員スタジオ、のときは今までどおり出さない。
- 信頼: 申告はモデルでも書ける（プラグインは利用者が消せる）ので、セッション名としては受け取らず、
  AF の対応表を引く鍵としてだけ使う。一致しなければ黙って 3 に落ちる。つまりプラグインが無い・
  壊れている状態は、今までと同じ結果になる。

## 検証

- 単体（`mcp_stdio_caller_test.go`）: 同じフォルダの 2 本がそれぞれ自分に解決される／偽造・停止中・
  別フォルダ・opencode 以外の申告は採らない／一致しない申告は cwd 推定の答えを保つ／subdir／
  `AF_SESSION_NAME` が勝つ／鍵が Agent へ渡らず次の呼び出しに残らない／同じフォルダの 2 本に
  `generate_image` が一覧に出て、生成はそれぞれ申告した側に付く（申告なしは断る）／他 kind 混在・
  生死不明・全員スタジオでは一覧に出ない。変異 11 本のうち 10 本はどれかのテストが落とした。
  残る 1 本（一覧の代表選びで生死の読み取り失敗を無視する）は、`mcpAliveSessions` が失敗時に空を
  返すので挙動が変わらない等価変異。
- 契約（`contract_caller_plugin_test.go`、`-tags clicontract`、tier A）: 上の 1・2・4 を出荷する
  プラグインそのもので固定した。陰性対照として、全ツールに付けるプラグインと何もしないプラグインの
  どちらでも赤になることを確かめた。
- 残り: 配備後に実 Managed opencode 2 本を同じ worktree で動かし、`propose_session_handoff` /
  `generate_image` がそれぞれ自分に解決されること（#989 の受け入れ条件）。

## レビュー（codex の子セッション）で直したこと

- 🔴 **一覧の監視 goroutine が呼び出しの申告を読んでいた。** `mcpWatchToolList` は 1 分ごとに
  `mcpStdioToolList` を別 goroutine で組み直し、その中の `mcpImageGenAdvertise` /
  `mcpStudioAdvertise` が `mcpOwningSession` → `mcpCallerSID` を読む。`generate_image` の呼び出しは
  最長 33 分ループを占有するので、その間の監視は**データ競合**になり、しかも共有された子の一覧を
  「たまたま呼んでいた 1 本」の持ち主で組み直す。持ち主の解決を呼び出し用（申告を使う）と一覧用
  （`mcpListOwningSession`、申告を見ない）に分けた。`-race` で、一覧側を申告に戻す変異が
  `mcpCallerSession` の競合として検出されることを確かめた（CI は `-race` を使わないので、CI での
  見張りは意味を見るテストの方）。
- 🔴 **プラグインが印を付けない状況で、モデルの書いた値を信じていた。** プラグインが消された・
  af が旧名の `af`（プラグインは乱数付きの名前しか見ない）のとき、モデルが同じフォルダの生きている
  別セッションの `ses_` を書けばそのセッションとして扱われた。申告は「プラグインが入っている かつ
  af の名前が乱数付き」のときだけ使う（`mcpCallerStampTrusted`）。モデルは同じ uid でシェルを持つ
  ので、`AF_SESSION_NAME` を偽って `mcp-stdio` を自分で起動することもでき、これは壁ではない。
  予約引数を「普通のツール呼び出しで開く扉」にしない、というところまで。
- 🟡 **一覧に出したのに毎回断られる形があった。** 一覧側も同じ信頼条件と、生きている全員に sid
  ストアの対応があることを求める。
- 変異 6 本（一覧が申告を読む・常に信頼・対応の確認なし・名前の確認なし・プラグインの確認なし・
  呼び出し側が信頼条件を見ない）はすべてどれかのテストが落とした。

## 再レビューで直したこと

- 🔴 **信頼条件がプラグインのファイルの「存在」しか見ていなかった。** 空・別の JS に置き換えられて
  いても申告を信じた。イメージ同梱の原本（`/usr/local/share/agent-fleet/opencode-plugin/`）と
  バイト一致するときだけ信じる（`opencode.CallerPluginCurrent`）。この開発環境のように、イメージが
  まだプラグインを持たない版なら信じない＝cwd 推定のまま。
- 🟡 **`XDG_CONFIG_HOME`**: opencode はこれを見るが、af の書き込みはすべて HOME 基準
  （`paths.OpencodeConfigDir`）。別の場所を指しているときは、確かめたファイルが opencode の読む
  ものではないので信じない。
- 🟡 **生きている 2 本が同じ `ses_` に対応していると、一覧に出して毎回断る**形が残っていた。
  一覧側は対応が互いに異なることも求める。
- 🔴（既存の穴）**subdir で起動したセッションが、作業フォルダの推定とスタジオの一覧判定から
  漏れていた。** どちらも `Meta.Dir` だけを比べており、subdir で走る MCP 子の cwd と一致しない。
  今回の変更で入ったものではないが、`mcpRunsIn`（`Dir` か `CWD()`）にそろえた。
- 指摘のうち「Agent 再起動で af 名が回ると、引き継がれた旧 `opencode serve` の子の申告を全部拒否
  する」は再現しない。信頼条件は「旧名の素の `af` でない」ことで、現在の名前との一致は見ていない。
  旧名も `af_<8 桁>` ならプラグインは印を付ける。
- 変異 5 本（内容の比較なし・XDG の確認なし・重複の確認なし・推定が root だけ・スタジオが root
  だけ）はすべてどれかのテストが落とした。

## 3 巡目のレビューで直したこと

- 🔴 **信頼条件が見ていたのは「Agent の現在の af 名」で、この子が実際に登録されている名前では
  なかった。** プロジェクトの `opencode.json` などが同じコマンドを素の `af` で登録していると、
  プラグインは印を付けない（乱数付きの名前しか見ない）のに、子は信頼条件を満たしてモデルの書いた
  値を信じた。opencode は子に自分の登録名を教えないので、af の opencode エントリだけに
  `environment.AF_MCP_SERVER_KEY=<登録名>` を書き（`mcpreg.OpencodeServers`）、子はこれが
  `af_<8 桁>` の形のときだけ申告を使う。起動引数は変えない（全 kind の af 定義と、それを比べる
  テストに波及するため）。副作用として、Agent 再起動で名前が回っても、引き継がれた旧 serve の子は
  自分が起動された時の名前で判定される（2 巡目で「再現しない」とした件は、この形でも成り立つ）。
- 変異 2 本（鍵を書かない・鍵を見ない）はどちらもテストが落とした。

## 4 巡目のレビュー: 直さないと決めたこと

- 指摘: プロジェクトの `opencode.json` が素の `af` で同じコマンドを登録し、`environment` に
  `AF_MCP_SERVER_KEY=af_<8 桁>` を自分で書くと、プラグインは印を付けないのに子は申告を信じる。
- 経路は実在するが塞がない。MCP エントリの書き手は子の環境を丸ごと決められ、その書き手は
  `AF_SESSION_NAME=<他人>` を書くだけで、申告と関係なく以前からなりすませる
  （`mcpOwningSession` は `AF_SESSION_NAME` を最初に無条件で信じる）。環境で届ける身元は、それを
  書く設定より強くならない。この検査が閉じるのは「誰も嘘をつくつもりが無いのにずれる」形
  （旧名・残骸・プラグインの欠落や改変・XDG）まで。
- 「親の環境から継承する」形は経路が無い。`AF_MCP_SERVER_KEY` を持つのは opencode が起動した af
  子だけで、af 子は opencode を起動せず、モデルのシェルは opencode が起動する。

## 範囲外

- opencode の task ツールで作られる子セッションは `ses_` が別なので一致せず、cwd 推定に落ちる
  （悪化はしない）。親をたどるなら opencode のストアの `parent_id` が使える。
- Terminal の opencode は `AF_SESSION_NAME` が先に決まるので、プラグインの申告は除かれるだけ。

## 2026-10-14: opencode 2 (#2104, part of #2101)

Measured on `@opencode/cli` 2.0.28 and `opencode-ai` 1.19.0, each in an isolated `HOME`, against a
stub OpenAI-compatible endpoint and a stub MCP server (no real model).

- The three plugins are now one module per file: a plain default-exported
  `{ id, setup(ctx), server() }`. 2.0.28 loads it from `~/.config/opencode/plugin/` (and from
  `plugins/`) without `@opencode/plugin`, `.ts` included, and ignores `server`; 1.19.0 calls
  `server`. The directory stays `plugin/`, so `CallerPluginCurrent` and the entrypoint seed are
  unchanged and no stale file can collide on an id. `/api/plugin` lists the ids.
- `ctx.tool.hook("execute.before")` fires for the model's `shell` call and, separately, for each
  MCP call made inside the Code Mode `execute` tool (tool name `af_<hex>_<tool>`, with
  `sessionID`). Mutating `event.input` in place reaches the tool: the rtk rewrite took effect.
  `POST /api/session/{id}/shell` (the user's own shell) does not fire it.
- **The stamped argument does not survive on 2.0.28 when the tool's `inputSchema` is closed**
  (`additionalProperties: false`, as af's are); with an open schema it arrives. Every 2.0.28
  `tools/call` carries `params._meta["ai.opencode/sessionID"]` (1.19.0 sends none), so
  `mcpStdioCall` now takes that as a second source; the stamp still wins, and `_meta` needs only the
  rotated server key, not the plugin, since the model cannot write it.
- Status events: `session.execution.started` → working; `succeeded`, `failed` and `interrupted`
  → idle (each observed; `session.idle` was not emitted). Subagent sessions carry `parentID` on
  `session.created` and are left out so their end does not flip the badge.
- Not covered: a Terminal (CLI) opencode 2 shares one background service, so
  `AF_SESSION_SID` in the plugin's environment is the service starter's (#2105).

## 2026-10-14: opencode 2 TUI launch, service, install (#2105, part of #2101)

Measured on `@opencode/cli` 2.0.28 in an isolated `HOME`, a loopback stub OpenAI-compatible
provider with an explicit stub model, service port moved off 49374.

**Shared service.** A bare `opencode` (and `opencode models`, `run`, `session list`) starts
`opencode serve --service`, one per user, and that process keeps the environment of whoever
started it (`AF_SESSION_SID` of the first session was in its `/proc/<pid>/environ`). Per-session
env therefore never reaches a plugin or a provider on a second session. `opencode service set
disabled true` (file `~/.config/opencode/service.json`; keys: disabled, remote, hostname, port,
password, cors, env) makes every command run a private `serve --stdio --port 0` instead: no
service process afterwards. Decision: **`--standalone` per TUI** rather than `--server` to af's
serve.

| layout (idle, RSS) | per TUI | server | 3 sessions |
|---|---|---|---|
| `--standalone` | ~160 MB | ~245 MB each | ~1.2 GB |
| shared service or `--server` to one serve | ~160 MB | ~300 MB once | ~0.8 GB |

`--server` works (`OPENCODE_SERVER_PASSWORD` in the TUI's environment carries the password), but
the server's environment, not the TUI's, is what plugins see, so the status badge, slot-id capture
and the session-scoped engine token would break, and Terminal sessions would have to count as
daemon dependents and survive a serve restart. The price of `--standalone` is ~245 MB per Terminal
session; v1's TUI embeds its server in-process, so this is the v2 equivalent.

**Model, agent, fork.** `--model/--agent/--fork` are not TUI flags. `OPENCODE_CONFIG_CONTENT`
(`model`, `default_agent`) picks them for a *new* session; a session that exists keeps its own
whatever the config says. `opencode api --standalone POST /api/session/{id}/model|agent|fork`
(~1 s each) works against the same database, so a resume sets them before attaching and a fork is
made first and the copy resumed. Forking an empty session is refused by v2.

**Config.** `OPENCODE_CONFIG` (a file path) is ignored by v2; `OPENCODE_CONFIG_CONTENT` and the
project `opencode.json` are read. The chat read-only policy (`permission: {edit, bash: deny}`, v1
shape) held in a project `opencode.json` on v2 (the shell tool is removed from the model's list) but
not when delivered through `OPENCODE_CONFIG`, so chat and one-shot runs now also pass the same JSON
inline on v2. Instructions: the global `~/.config/opencode/AGENTS.md` is read (fleet policy and
memory guide arrive); `CLAUDE.md` (project, global, `~/.claude`) and the `instructions[]` array are
not. The member's own instructions are therefore composed into that AGENTS.md as the `user-notes`
block on v2 and kept in `instructions[]` on v1.

**Install.** `@opencode/cli` installs `opencode` and `opencode2` bins (the same executable), and
`opencode-ai` and it both claim `opencode`, so a lean-variant boot that crosses the major
uninstalls the other package first. The auto-updater is `update: "disable"` in the config, seeded
by the entrypoint when the installed major is 2.

Nothing was run against a real provider. The pin stays 1.19.0.

Review round 1 (#2118): a top-level `permission` deny is only a rule, and a later project or agent
rule (`agents.build.permissions` allow) brings shell/edit/write back (a shell call created a file).
`experimental.policies` entries (`tool.use`, resource `<action>:*`, deny) are evaluated after every
rule and held (`Blocked by configuration policy`) with the same agent allow in place; a bare tool
name as the resource matched nothing. Chat and one-shot runs on v2 now carry `shell:*`, `edit:*`,
`write:*` and `subagent:*` denies inline. Code Mode's `execute` did not reach the built-in shell
even without a policy (control run), so it has no entry of its own.

## 2026-10-14 — Terminal sessions on one shared serve (#2120)

On opencode 2 a Terminal session now attaches to af's own supervised `opencode serve`
(`opencode --server`) instead of a private `--standalone` server. The v1 launch and the
standalone launch are untouched; `AF_OPENCODE_TUI_STANDALONE=1` puts the standalone one back
without a release, and a launch that cannot get the serve (not connected, disabled) falls back
to it by itself. Code: `workspace/agent/internal/agents/opencode/shared.go`, `attach.go`,
`paneenv.go`; plugins `agent-fleet-shared.js`, `agent-fleet-tui/`, and the metadata path of
`agent-fleet-status.js`.

**How sessions stay apart** (all measured on 2.0.28, isolated `HOME`, loopback stub provider):

- Identity is session metadata. The Agent creates (or forks) the opencode session itself with
  `{"af.sid", "af.name"}`, records slot → session id at once, and the plugins resolve
  `sessionID → ctx.session.get(...).metadata["af.sid"]`. `PATCH /api/session/{id}` **replaces**
  the whole metadata object (not a merge), so every tag is read-merge-write. A fork copies
  metadata (and permissions), so the copy is re-tagged.
- The shell environment is `PUT /api/session/{id}/environment` (`{"variables": {…}}`, 204; 404 for
  an unknown id). Without it the shell prints the **server's** environment, including the server
  password and the provider keys. With it, it prints exactly the PUT variables.
  `workspace-agent opencode-attach` is the pane program: it PUTs its own environment (toolchain
  exports included, server credentials removed) and only then `exec`s the TUI.
- The password rides the pane environment, never the command line.
- The engine token per session is a server plugin `session.hook("http.request")`: the event
  carries `request` (a `Request`), `sessionID`, `model`; setting `authorization` on it reaches the
  provider (the stub saw `Bearer tok-<slot>` on every request). The token is minted by the Agent
  (`workspace-agent opencode-engine-token <name>`), never stored in metadata.
- Provider keys cannot leave the server environment: `POST /api/credential` stores the key in
  `opencode.db` in plaintext (found with `strings` in the WAL), which af's no-plaintext-credential
  rule excludes.

**TUI plugin.** `~/.config/opencode/plugins/<dir>/` with a `package.json` whose `./tui` export
is the entry loads on 2.0.28; its context has `client` (the generated client: `session.get`,
`session.update({sessionID, metadata})`, `session.environment({sessionID, variables})`,
`server.info()` with the server `pid`), `ui.router.current()` and `data.listen`. **Every TUI on
the server receives every `session.created`**, so the first version, which claimed a new session
on the event, tagged a `/new` made in one pane with another pane's sid (found by the end-to-end
run). A pane now claims a session only while its own route shows it.

**Restart gap and the shell gate (revised after review).** The environment is in memory, so a new
serve generation has none, and neither does a subagent's session (it inherits metadata and
permissions, not the environment), a fork (copies the tag only) or a `/new` before its tag. On every
new generation the Agent PUTs the live shared panes' environments (read from the pane's process
tree, `/proc/<pid>/environ`, nothing on disk; retried) as a warm-up. The guarantee is per session:
the server plugin makes the shell tool ask the Agent (`workspace-agent opencode-put-env <slot>
<session>`) once per session and server generation — the plugin's memory is exactly one generation —
and REFUSES the command when the Agent cannot confirm (a throw in `execute.before` blocks the tool;
measured). The Agent PUTs only onto a session whose af.sid/af.name are the slot's. A new untagged
session is waited for (5 s); an old untagged one, a Managed one (`af.managed`) and a new one nobody
claims have no pane to take an environment from and run as before. Measured with four panes: the
environments are back 170–350 ms after the new daemon answers, the TUIs reconnect. **Residual:**
commands that do not go through the shell tool, and a plugin that fails to load. The password is kept
across generations. A pane remembers how it was launched (`opencode-mode`): an automatic fall back to
`--standalone` is not read, drained or interrupted as a shared pane. `opencode-attach` checks the
session is the pane's own and refuses connection-changing flags.

**Memory** (idle, bare TUIs, no plugins, RSS/PSS MB): 3 × `--standalone` 1230 / 842; one serve + 3 ×
`--server` 732 / 543. Per extra pane about +160 / +112 (TUI) against +410 / +280 (standalone).

**Left alone.** Provider, MCP and config settings stay per server. The stale slot directory:
the status plugin of a standalone/opencode 1 pane still writes
`~/.config/agent-fleet/opencode-sid`, which the Agent has not read since ADR 0087 moved the store
to `~/.local/state` (#2122); the shared-server plugins write where the Agent reads.

Review round 2 (#2123): the workspace switch (`AF_OPENCODE_TUI_STANDALONE`) decides the next launch
only. A pane keeps the mode it was launched with (`opencode-mode`), and a daemon that still has a
shared pane keeps its stable password and shared plugin mode after the switch is flipped; measured
with a restart after flipping it. Two panes showing the same untagged session used to both tag it
(PATCH has no compare-and-set): taking a session is now `workspace-agent opencode-claim`, an
exclusive file create keyed by the session id taken before anything is read or written (one winner,
the loser changes nothing, a claim that fails before the tag is released).

Review round 3 (#2123): the Go fork/resume re-tag was an unconditional PATCH, outside the claim
exclusivity: a fork carries the source's tag from the moment it exists, so the source's pane could
claim it first and then have its tag overwritten. The Go launch now uses the same exclusive claim
(`claimSessionFor`); a copy somebody else got first is left to them and a new one is forked, and the
tag inherited from the source is accepted only for the claimant that won the file. The TUI plugin
re-sends environments through `opencode-put-env`, which checks the tag.

Review round 4 (#2123): a resume now takes its claim BEFORE changing anything on the session (a
refused foreign resume made no mutation; it also gives back the record it took). The claim record
is written whole and linked into place (never created-but-empty), and a record is replaced when it is
older than 30 s and the session's tag never followed it (an abandoned claim); a completed claim
stands however old. A fork's copy that cannot be claimed because of an API error is deleted, and a
claimed one is recorded as the slot's session before the model switch, so a retry resumes it.

Review round 5 (#2123): deleting a fork copy that failed to be claimed took no account of a pane
that claimed it in between (the source's pane sees the copy under the inherited tag). The cleanup now
takes the session's exclusive claim record again first: if another pane holds it the copy is theirs
and stays, and while the cleanup holds it nobody can claim the copy before the DELETE is done.

## 2026-10-14 — opt-in to opencode 2 beside the pinned 1.x (#2133)

One workspace can run opencode 2 while the image keeps `opencode-ai@1.19.0` and everyone else stays
on it. `workspace-agent install-opencode2 [version]` installs `@opencode/cli` into the private npm
prefix `~/.local/share/agent-fleet/opencode2` (default `2.0.28`, the version #2102–#2123 were
measured on; nothing goes on PATH, so the pinned `opencode` is not shadowed). `workspace-agent
opencode-major 2|1` flips the switch (a file in the state directory; `AF_OPENCODE_MAJOR=1|2` in the
Agent's environment wins). Choosing 2 without the binary changes nothing.

Measured on 2.0.28, linux x64, isolated HOME:
- `npm install` leaves 806 MB (every libc/baseline platform package is an optional dependency and
  the postinstall copies the matching binary to `bin/opencode.exe`); the installer prunes the
  `@opencode/cli-*` packages after the version check and 202 MB remain. The pruned binary runs.
- `opencode2 models` against a copy of a 1.19.0 home changed `opencode.db`, its WAL and its SHM
  (in-place migration, v1 tables never written again) and added `service.json`. `OPENCODE_DB` is
  the only data override the binary has (`strings`: no data/state/cache variable). With it set the
  1.x database, WAL and SHM stay byte-identical across a Managed turn and a Terminal turn on 2.x
  (`TestContractOptInRunsV2BesideV1WithoutTouchingV1Data`). The directory of `OPENCODE_DB` must
  exist (ENOENT on start otherwise); `~/.local/share/opencode/log/opencode.log` is appended by both
  majors, and the model cache and `state/locks` are shared and format-compatible.
- `XDG_DATA_HOME` would isolate everything, but it would also reach the shell tool of every v2
  session, so it is not used. `OPENCODE_DB` is likewise kept out of the shell environment af PUTs
  for a Terminal session: 1.19.0 also honours it, and a 1.x `opencode` run from a session would
  write 1.x tables into the 2.x database. A managed session's shell still inherits the serve's
  environment; that remainder is accepted.
- Config and plugins under `~/.config/opencode` stay shared (already dual-major, #2118); the only
  v2 write there is `service.json` (`disabled: true`, seeded by `opencode-major 2` and at boot),
  which 1.19.0 does not read (a 1.x session list after the 2.x run is unchanged).

Consequences: the 2.x database starts empty, so a session created on 1.x is not resumable on 2.x
(the shared Terminal route starts a new conversation, the standalone route errors) and the other
way round. The supervised serve records how it was started (`launchKey`); `ensure` replaces a
daemon started under the other setting (drain, stop, re-resume, as for a key change) and an adopted
one by the pid in `opencode-serve.json` (written only while the opt-in is on). Panes launched on
1.x are never counted as attached to the serve (their stale `opencode-mode` record is cleared).
Open Terminal panes keep the opencode they started with; a 2.x pane loses its server when the
serve is replaced and must be relaunched.

## 2026-10-16 — the cut-over: image pin to opencode 2.0.29 (#2105, #2134)

`OPENCODE_VERSION` moves from `1.19.0` to `2.0.29`; `@opencode/cli` is installed for a 2.x pin and
`opencode-ai` for a 1.x one in the Dockerfile, the entrypoint, `release-gate.yml`,
`setup-agent-cli`, `cli-drift-check.sh` and `cli-pin-bump.sh` (`deploy/local/opencode-pkg-test.sh`
runs all five rules on the same versions). The pin-bump script keeps refusing a major crossing and
now bumps inside 2.x.

**2.0.29 rather than 2.0.28.** Measured with both binaries in isolated homes: `/openapi.json`
differs only by one added optional `policies` array in a config schema, `--help` for the root,
`serve`, `run` and `service` is identical, and the six opencode 2 contract tests (serve API, form
round trip, state reads, shared serve, Terminal form respond, plugin modules) pass on 2.0.29.

**The opt-in (#2133) after the cut-over.** The `opencode` on PATH is v2 and owns
`~/.local/share/opencode/opencode.db`, so a second private v2 with `OPENCODE_DB` would split a
member's sessions in two. `opencode.PinnedV2()` (the PATH opencode, then the image pin, is 2.x)
makes `UseV2` and `CurrentLaunch` ignore the switch file, the `AF_OPENCODE_MAJOR` variable and the
private install; `install-opencode2` and `opencode-major 2` print why and change nothing, `opencode-major 1`
still clears a leftover switch file, and the status names the private install and
`opencode2-data` as leftovers that are **not merged** into the main database (sessions that exist
only there are not visible). Nothing is deleted. With a 1.x PATH opencode (a rollback) the opt-in is
live again unchanged, so the code stays; `V2Version` follows the pin.

**Data on upgrade, measured** (real `opencode-ai@1.19.0` and `@opencode/cli@2.0.29`, isolated HOME,
loopback stub provider with an explicit model; two sessions created through the 1.19.0 serve, the
home copied, 2.0.29 started on the copy):
- The v1 session ids survive the import (`ses_…` identical), titles, directory, model and the
  message history come across (`GET /api/session/{id}/message` returns the assistant text and the
  user text).
- Resume through af's v2 calls works on an imported session: `POST /api/session/{id}/prompt` queues
  and runs a turn, and the provider request carried the whole earlier history (system, the v1 user
  message, the v1 assistant reply, the new message); `POST …/model` and `…/agent` answer 204;
  `PATCH` metadata (`af.sid`/`af.name`, the Terminal claim tag) answers 204.
- A real 2.0.29 TUI started as `opencode --server … --session <v1 id> --auto` (the Terminal route)
  opens the imported conversation with its history, and shows `Plan auto · m` after the agent switch.
- Rollback direction: a copy of the migrated home started under 1.19.0 lists the two imported
  sessions and their v1 messages, but not the session created under 2.0.29 and not the turn added
  to an imported session under 2.0.29. Nothing is deleted; the data is invisible to the other major.

**Entrypoint trap, measured.** With `opencode-ai@1.19.0` installed into a prefix,
`npm install -g @opencode/cli@2.0.29` fails with `EEXIST` on the `opencode` bin and the prefix keeps
answering `1.19.0`; uninstalling the other package first works in both directions. The boot-install
path already did that; the self-update path did not, so a home that had self-updated on the old pin
would have kept a v1 shadow ahead of the baked v2. It now drops the other package first.

**Contract tests in CI.** The `contract` job runs `go test -tags contract -run TestContract` against
the pinned and the newest version, which are both 2.x now. Eight tests of the 1.x contract failed
on 2.0.29 (SQLite tables, `/global` and `/session` paths, the v1 plugin file, `OPENCODE_CONFIG`,
`PATCH /global/config`, the v1 `/doc` spec) and the pane-mode test launched a bare `opencode`, which
on 2.x starts the per-user service on its fixed port (the test now passes `--standalone`, as
`buildProgram` does; Tab does not cycle the agent at the 2.0.29 start screen, so only the first
reading is asserted there). The 1.x-only tests skip on a 2.x binary (`skipOnOpencode2`, naming the
test that pins the 2.x counterpart) and were re-run green on 1.19.0, so a rollback keeps them.
`setup-agent-cli` resolved `latest` to `opencode-ai` unconditionally; it now follows the pin's major.

**Rolling back.** Rebuild the image with `--build-arg OPENCODE_VERSION=1.19.0` (or edit the ARG),
Stop and Start the workspaces. 1.19.0 runs on the migrated database and sees every session from
before the upgrade; sessions created or extended under 2.x are invisible to it. Copy
`~/.local/share/opencode/opencode.db*` before the first v2 start to restore exactly. The v1 code
paths (launch line, store reads, plugins, instruction delivery) are kept and covered by the existing
tests for both majors.

### 2026-10-16 (later) — review round 1 on the cut-over

- **First-start migration race.** On 2.0.29 a 253 MB 1.x database (50 conversations x 1000
  messages) keeps importing for 25-70 s after `/api/info` answers; meanwhile `GET /api/session/{id}`
  is 404 for a conversation that exists (503 right at the start) and a create succeeds, so the
  Managed `Resume` started a new conversation and overwrote the slot's recorded session id.
  `GET /api/experimental/migration/v1` answers `{"status":"running","progress":{…}}` and then
  `{"status":"completed"}`. `EnsureMigrated` (migrate.go) now runs before the supervised serve,
  `BuildLaunch`, the models listing and both chat `run` paths open the database: it takes one
  lock, starts a throwaway `serve` on a free port, waits for the status and records a marker
  (binary version + database). `TestContractFirstStartWaitsForV1Migration` (needs
  `AF_TEST_V1_SEED_DB`) is red with the gate bypassed (404/503) and green with it. What the gate
  cannot cover: an `opencode` a member runs by hand in a shell before af's first start.
- **Self-update swap.** The other opencode package is now moved aside and put back when the
  install fails (`oc_install_swap`, `deploy/local/opencode-swap-test.sh`), instead of being
  uninstalled first.
- **`opencode run` on 2.x has no `--dir`** (`Unrecognized flag`, exit 1): every assistant chat and
  one-shot would have failed. 2.x gets `run --format json --standalone` with the directory from
  `cmd.Dir`; `session delete` gets `--standalone` too. `TestContractV2ChatPostureAndMCP` runs the
  production config path on a real 2.x with a stub provider: the read-only posture holds (shell and
  write calls do not run, while they do without the config), the af server of the inline
  config runs ahead of a same-named project entry and carries `--conv`. Not reproduced: the
  agent-rule bypass of the `permission` deny measured earlier; the hard-deny policies stay as a
  second layer.

### 2026-10-16 (round 2)

- The migration gate no longer keeps a marker. It reads the database: opencode 2 records the import
  in `kv.migration.v1-v2` (`{"phase":"completed"}`, measured), and a file without it that still
  has rows in the 1.x `session` table needs the import; a missing file, no `session` table or no
  rows needs nothing. A pre-upgrade copy restored over an imported database (the documented
  rollback) is imported again; the real-seed contract test now does exactly that. Only status
  `completed` ends the wait: `required`/`running` wait, `error` and any unknown or empty status
  fail with the body.
- Boot-install (lean) and self-update both install through `oc_install_swap`; a copy left aside by
  an interrupted start is put back before anything else, never deleted.
- `parseOpencodeRunEvents` reads the flattened 2.x error event
  (`error.type`/`error.message`); the chat contract test demands a clean exit and the stub's reply
  from every run.

### 2026-10-16 (round 3)

`oc_swap_restore` restores from what is in the aside directory, not from the current "other"
package: each aside package under its own name, and the bins independently of the package, with the
directory removed only after every move succeeded. This covers a stop between the package move and
the bin move, and an image re-pinned to the other major while a copy was aside.

## 2026-10-23 — the opt-in's install version follows the image pin (#2493)

`opencode.V2Version` was a literal (`2.0.29`) that called itself the image pin and stayed there
while the pin moved to 2.1.x, so `install-opencode2` after a 1.x rollback installed a release the
contracts had not run on. It cannot be read from the `opencode` entry of `versions.json`: the opt-in
only works while that is 1.x, which is exactly when it names no 2.x release. The Dockerfile now has
`ARG OPENCODE_V2_VERSION`, recorded in `versions.json` as `opencode_v2` and read by
`opencode.V2Version()`. `cli-pin-bump.sh` moves it with `OPENCODE_VERSION` whenever the pin is 2.x,
and a rollback (`OPENCODE_VERSION=1.19.0`, nothing else) leaves it at the last tested 2.x.
`V2FallbackVersion` remains for images built before the key existed; nothing bumps it and a test
only requires it to be 2.x. `TestDockerfileV2ArgFollowsOpencodePin` fails when a 2.x pin and the V2
ARG differ.

## 2026-10-24 — fork-at on the shared-serve Terminal, and the ToDo list, re-measured on 2.1.4 (#2493)

**Fork-at.** `ResolveForkAt` refused every non-managed opencode session, although a Terminal pane
attached to af's serve (`sharedSlot`) already forks through `POST /api/session/{id}/fork` with
`before` in `buildSharedLaunch` (#2123). It now accepts a Terminal session when the pane is on the
shared serve AND the fork's own launch will use it (`sharedTUI()`); every other route — standalone
pane, no mode record, the next launch switched to `--standalone`, opencode 1.x, codex — still
returns `ErrForkAtRoute`. A launch that falls back to `--standalone` with a fork point is still
refused by `BuildLaunch`, never widened to a whole-conversation copy. The Console learns the
per-session answer from a new wire field `forkAtTerminal` (`TerminalForkAtter`, computed in
`wireSession`; the kind's static `forkAtManagedOnly` is unchanged). Verified with unit tests (the
accepted route, four refused routes, the serve-unavailable launch) and, on the wire, the goldens
and the CP relay test; not verified end to end in a live Terminal pane.

**ToDo, measured on opencode 2.1.4** (`opencode --version` → `v2.1.4`; isolated `HOME`, own
`opencode serve` on a free loopback port, stopped afterwards):

- `GET /openapi.json` (124 paths): no todo route, event or schema. The only mention of "todo" is
  `Config.experimental.todo` ("Enable the experimental todowrite tool. Defaults to false.").
- `TestContractV2Todo` (contract tag, stub OpenAI-compatible provider that calls `todowrite`):
  with the default config the call fails — `state.status: "error"`, "No tool named \"todowrite\" is
  currently available"; with `experimental.todo: true` it runs — `state.status: "completed"`,
  `state.input.todos: [{content, status, priority}]` and the same list in `state.metadata.todos`.
- The opencode 2 TUI itself derives its list from the newest completed `todowrite` tool part of the
  messages (no todo table), so that is what `v2Tasks` now reads into `Tasks`. Both captures are in
  `testdata/v2/todo-{off,on}.messages.json`.

So the 2.0.28 statement "no ToDo on opencode 2" holds only for a default config: the list exists
once a member enables `experimental.todo`, and af does not set it.
