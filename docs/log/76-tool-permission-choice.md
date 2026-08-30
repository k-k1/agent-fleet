# 76. 権限確認のスキップを利用者の選択にする

- 状態: ✅ P0 実装済み（claude / cursor / copilot / kiro / agy）・⏸ P1（codex / opencode）は保留
- 設計判断: [decisions/0056](../decisions/0056-tool-permission-choice.md)
- 関連: [docs/75 アイドル自動停止と対話の持ち越し](75-idle-stop-and-pending-interactions.md)（承認待ちで畳まれたときの受け皿）、
  [dev/07 セキュリティ](../dev/07-security.md)、[guide/operator/03](../operate/04-secure.ja.md)

## 76.1 何を変えるのか

Agent Fleet は起動するエージェントに、**ツール実行の承認を全部スキップするフラグ**を無条件で付けていた。

| kind | 起動経路 | 付けていたフラグ |
| --- | --- | --- |
| claude | TUI | `--dangerously-skip-permissions` |
| cursor | TUI / managed(ACP) | `--force` |
| copilot | TUI / managed(ACP) | `--allow-all` |
| kiro | TUI / managed(ACP) | `--trust-all-tools` |
| agy | TUI | `--dangerously-skip-permissions` |
| codex | TUI / managed | `--dangerously-bypass-approvals-and-sandbox` / `approvalPolicy:"never"` + `dangerFullAccess` |
| opencode | TUI / managed | `--auto` / `permission.asked` の無条件 auto-allow |

理由は 2 つあった。**コンテナ自体がサンドボックス**であること、そして**承認ダイアログで止まった
セッションを Console から答えられない時期があった**こと。後者はもう成り立たない（§76.3）ので、
**「スキップするかどうか」を利用者の選択にする**。**既定は変えない**——今までどおりスキップする。

環境変数（`AGENT_CLAUDE_FLAGS` ほか）は以前からあるが、あれは dev seam で、プロセス全体に効き
Console からは見えない。「利用者の選択」にはならない。

## 76.2 決めたこと（要約）

1. 値は **3 層**で解決する。`session.Meta.SkipPermissions`（そのセッションだけの明示指定・3 値）→
   ui-prefs `agentLaunchDefaults[kind].skipPermissions`（kind 毎の既定）→ `true`（従来どおり）。
2. 解決は **Agent のプロセス内**で行う（`internal/agents/agents.go` の `SkipPermissions` /
   `BypassPermissions`、prefs の読み口は `ui_prefs.go` の `skipPermissionsPref`）。Console だけで
   解決すると、**MCP の `create_session`・定時実行・停止セッションの再起動・fork/recreate** が
   設定を素通りする。
3. **plan 起動は kind を問わずスキップしない**。これは以前からの挙動で、`BypassPermissions` が
   1 か所に畳んだ（各 kind に散っていた `mode == "plan"` の `ReplaceAll` を置き換えた）。
4. 選べるのは **承認待ちを Console から答えられる kind だけ**（`Caps.PermissionChoice` /
   Console 側 `caps.permissionChoice`）。現時点で claude / cursor / copilot / kiro / agy。
5. サーバは対象外 kind の「承認あり」を**黙って無視せず断る**（`permission_choice_unsupported`）。

## 76.3 なぜ 5 kind だけなのか（承認導線の実在）

フラグを外すこと自体はどの kind でもできる。決め手は**止まったセッションに人が答えられるか**で、
答えられないなら利用者から見れば黙って固まったのと同じ。実装済みの導線は次のとおり。

| kind | 承認待ちの見え方 | 答え方 |
| --- | --- | --- |
| claude(TUI) | status hook の `permission` 状態＋対象ツール名（`session_status.go`） | ミラーの許可カード（許可／常に許可／拒否） |
| agy(TUI) | `agy/pending.go` が `permission` ＋選択肢を出す | 保留カード |
| kiro(TUI) | フッタ `requires approval` を `state.go` が question として拾う | 端末／ミラー |
| cursor / copilot / kiro(managed) | ACP `session/request_permission` を `Header:"許可"` の Interaction 化 | `POST /sessions/{name}/respond` |
| **codex** | 無し（managed は `appclient.go` が `item/permissions/requestApproval` を自動応答） | — |
| **opencode** | 無し（managed は `driver.go` が `permission.asked` を auto-allow） | — |

この 5 kind は **plan 起動で既に bypass 無しの運転を通している**——つまり今回の「オフ」は、
plan がやっていることから plan フラグを引いたものにすぎない。

## 76.4 codex / opencode を保留にした理由（P1 でやること）

- **codex**: `--dangerously-bypass-hook-trust` は**外してはいけない**。AF が注入した status hook が
  発火しなくなり、working/idle 検出が丸ごと死ぬ。外すのは approvals/sandbox 側だけで、
  `approval_policy` / `sandbox_policy` を承認ありの値に置き換え、managed 側は自動応答をやめて
  Interaction 化する必要がある。
- **opencode**: `--auto` を外すと composer の status 行が `Build · <model>` になり `auto` トークンが
  消える。`opencodeStatusAgentRe`（`session_io.go`）が外れてモードチップが消え、launch-seed の
  readiness 待ちが最大 30 秒遅延する（1.17.13 / 1.17.18 / 1.18.3 で実測 — `opencode_contract_test.go`）。
  正規表現の ` auto ` を任意化したうえで、managed の auto-allow を Interaction 化する。

## 76.5 docs/75 との関係（承認待ちで畳まれたらどうなるか）

ADR 0055 で、人待ち（question / plan / **permission** / blocked / auth / spend_limit）は
「コンテナを起こし続ける理由」ではなくなり、`interaction_idle_timeout`（既定 1h）で tier1 halt される。
畳んでも失われないことは**持ち越し**が担保する。

持ち越しの守備範囲は docs/75 P5 で**全 kind・両ルート**へ広がった。保留の在処は kind ごとに
違う（会話 DB / `events.jsonl` / ペインのフッタ / ACP handle / ネイティブストア）ので、
畳む側は `agents.ModalReporter`（`PendingModal`）1 つに訊く — 一覧は docs/75 §75.7.2。
claude だけは従来どおり hooks が書く `pending-*` が正。

★**承認待ちが持ち越すのは「何を訊かれていたか」だけ**である（ADR 0055 決定 13）。ACP の
Interaction は Console に選択カードを描かせるため `Kind:"question"` を名乗るが、**可否の
宛先（JSON-RPC の id・TUI のモーダル）はプロセスと一緒に死んでいる**。畳んだ後に Yes/No を
選ばせると「許可したのに実行されない」を作るので、持ち越しは `permission`＝事実だけへ落とし、
再開後の配達は「必要ならもう一度実行を試みて、作業を続けてください」になる。つまり本機能で
承認をオンにしたセッションは、畳まれても**何を訊かれていたかは残るが、承認そのものはやり直し**
になる。費用（畳む）と安全（届かない承認を偽装しない）のどちらを採るかで、後者を採った。

⚠️ **取れない経路**: コンテナごと SIGKILL（ECS の stop timeout 超過・ホスト OOM・EC2 の強制停止）
されると、ACP handle とペインにしかない承認待ちは失われる。`halt`（tier1）と `gracefulShutdown`
を通る正常停止（tier2 の停止はこちら）では取れる。

## 76.6 「毎回たずねる」で claude は何モードになるか（実測）

claude 2.1.241 を実 tmux で 1 本ずつ起動して採取（2026-08-24）。`--permission-mode` を渡さない
＝ AF の「毎回たずねる」起動は **manual** で始まる。auto は既定ではない（選択肢としては在る）。

| `--permission-mode` | 状態行 | チップ |
| --- | --- | --- |
| （無指定＝既定）/ `manual` | `⏸ manual mode on · ← for agents` | Manual |
| `auto` | `⏵⏵ auto mode on (shift+tab to cycle) · …` | Auto |
| `acceptEdits` | `⏵⏵ accept edits on (shift+tab to cycle) · …` | Accept Edits |
| `bypassPermissions` | `⏵⏵ bypass permissions on (shift+tab to cycle) · …` | Bypass |
| `dontAsk` | `⏵⏵ don't ask on (shift+tab to cycle) · …` | Don't ask |
| `plan` | `⏸ plan mode on (shift+tab to cycle) · …` | Plan |

⚠️ **manual だけ `(shift+tab to cycle)` を出さない。** `paneMode` の claude 分岐は
「4 つの名前 ＋ `shift+tab to cycle` の合言葉」で判定していたので、**承認ありのセッションは
モード不明（空文字）**になっていた。空文字は「コンポーザ未描画」の意味も兼ねており
（`session_io.go` の launch-seed readiness ゲート）、**初回プロンプトの配達が 30 秒待たされてから
best-effort に落ちる**。`internal/tmuxx` の `modeFooterRe` は 2.1.212 で同じ罠を踏んで直して
いたが、`paneMode` 側には反映されていなかった。→ `claudeModeLabel` にモード名を並べ、最後の砦を
**フッタ帯そのもの**（`tmuxx.ClaudeModeFooter`）にした。名前が増えても「描画済み・Default」に
倒れるので、配達だけは止まらない。

✅ `--allow-dangerously-skip-permissions` を残す判断も実測で確認した。manual 起動から
shift+tab を 4 回送ると **manual → accept edits → plan → bypass permissions → auto** と巡り、
利用者は再起動せずに自分で bypass へ入れる。

## 76.7 無人運転との相性

承認ありのセッションは、定時実行・フリートオペレーター・MCP の drive ツールでは**完了しない**。
`session_io.go` は保留中の許可があるとき送信を `permission_pending` で断るので黙ってハングは
しないが、ターンは進まない。設定 UI にはその旨の注記を出す（`agents.skip_permissions_off_note`）。

## 76.8 触っていないもの（意図的）

- **信頼／オンボーディング系**: claude の `hasTrustDialogAccepted` / `hasCompletedOnboarding`、
  cursor `--trust`、kiro `chat.disableTrustAllConfirmation`、copilot `--no-remote`。これらは権限確認
  ではなく、外すと起動時ダイアログで固まる（あるいは会話がフリート外へ出る）。
- **アシスタントチャットと headless 経路**（`chat_providers.go` / `mcp_stdio.go`）。人が答えられない
  一発実行なので従来どおり。AF 自身の自動化であって、セッションの権限方針とは別軸。
- **稼働中セッションの動的変更**。この選択は起動時にだけ効く（TUI は再起動が要る）。managed は
  再 spawn 時に解決し直す。

## 76.9 実装（P0）

- `internal/agents/agents.go` — `SkipPermissionsPref`（ui-prefs フック）/ `SkipPermissions` /
  `BypassPermissions` / `Caps.PermissionChoice`。表テストは `permissions_test.go`。
- `internal/session/session.go` — `Meta.SkipPermissions *bool`（3 値。nil = 既定に従う）。
- 各 kind の `program.go` / ACP の `driver.go` — `bypass bool` 1 つで分岐（plan は畳み込み済み）。
- `ui_prefs.go` — `skipPermissionsPref`（`PermissionChoice` を持たない kind の設定は無視）。
- `session_handlers.go` — create の `skip_permissions`、fork / recreate の継承、非対応 kind の拒否。
- Console — `caps.permissionChoice`、設定 > エージェント > 各カードのトグル、起動ダイアログ
  （LaunchModal / StartModal）の「権限確認」欄。**起動ダイアログで触らなければ値を送らない**ので、
  あとから既定を変えれば新しいセッションに効く。
- `session_io.go` — `claudeModeLabel`（§76.6 の実測表）と、`internal/tmuxx` の
  `ClaudeModeFooter`（未知のモード名でも「描画済み」と読む最後の砦）。
- Console — **モードの表示名**（`nonPlanModeLabel`）。claude の非 plan ラベル `Bypass` は
  「権限確認をスキップして起動したときの状態名」なので、承認ありのときは `Manual` に読み替える。
  これをやらないと、同じ起動ダイアログの中で「権限確認: 毎回たずねる」と「開始モード: Bypass」が
  並ぶ。ミラーのモードチップ自体は端末の状態行（`paneMode`）由来なので**元から正しい**（承認あり
  なら `Default` と出る）が、plan を抜けたときの楽観ラベルだけが種別の既定ラベルを使っていたので、
  **端末が直近に名乗った非 plan 名**を覚えて使うようにした。

## 76.10 残り

- P1: codex / opencode（§76.4）。
- 一覧やミラーで「このセッションは承認あり」を常時見せるか。claude はモードチップが
  `Bypass` / `Default` を出す（`paneMode`）が、他 kind と managed には出ない。
- 承認待ちの**可否そのもの**を持ち越すこと（§76.5）。現状は事実だけで、承認はやり直しになる。
  やるなら「再開後に同じツール呼び出しを再現して、生きたモーダルで訊き直す」形になるが、
  それは docs/75 決定 4（復元するのはモーダルではなく意図）を承認だけ例外にする話なので、
  実際に「やり直しが煩わしい」という声が出てから考える。
