---
audience: "新しい CLI コーディングエージェントを統合する人"
source_of_truth: "既存の `internal/agents/<kind>` パッケージ（一番近いものを写す）"
updated: "2026-09"
---

# 20. エージェント種別を足す

[English](20-add-an-agent.md) | 日本語

**どの面を埋めるかは、その種別がどの driver を持つかで決まります**。その範囲では面は毎回同じで、
**踏む罠も毎回同じ**です。この章はその両方の一覧です。

コードを書く前に、形は [04 §4.3](04-agent.ja.md)、既存種別が実際に何に対応しているかは
[ref/agents.md](../../guide/ref/agents.ja.md) を読んでください。

## 20.1 先に 3 つ決める

**1. どの driver を持つか。** **ターミナル（CLI）** 経路は CLI 自身の画面を tmux の pane で動かします。
その種別の `BuildLaunch` が pane のプログラムを返します。**マネージド** 経路は pane を持たず、
`Driver`（`internal/agents/driver.go`）が構造化 API でターンを回します。形は 3 つあり、
どれも今あります。どの種別がどれかは [ref/agents](../../guide/ref/agents.ja.md) です。

- **ターミナルのみ** — `Driver` を持たない。
- **両方** — 下の面をそれぞれの経路について埋めるので手間が増える。
- **マネージドのみ** — `Caps().ManagedOnly` を立てる。`BuildLaunch` は常に
  `ErrNoTerminalRoute` を返し、`POST /sessions/{name}/driver` は `tui` への切替を断り、
  作成時に driver 未指定なら `managed` になる。pane に載せるプログラムがそもそも無い種別
  （Agent の中で動くものなど）に向く形です。

マネージド経路では**プロセスモデル**（`Capabilities.ProcessModel`）も選びます。ワークスペースで
共有する daemon、セッションごとの子プロセス、Agent 自身の中のコード、のどれかです。それぞれを
使う種別と話すプロトコルは [04 §4.3](04-agent.ja.md) の表です。

**2. 会話 ID をどう持つか。** ここが後から**静かに壊れる**決定なので、意識して選ぶこと。
**捕捉型を選ぶ**（CLI が採番し、イベントごとに再記録する）。押し付けるなら**回収経路を同じ変更で
用意する**。その経路の規則と、どの種別がどちらかは [04 §4.2](04-agent.ja.md)（「会話 id: 捕捉型と押し付け型」）
です。

**3. 何をもって動いたと言うか。** CLI の status 出力でもバナーでもありません。
**実プロンプトに実応答が返ること**。これは何度も間違えたので規則にしてあります
（[08 §8.5](08-integrations.ja.md)）。

## 20.2 埋める面

自分の種別が持つ driver の行を埋めます。パスは、別のツリーを名指ししていない限り
`workspace/agent/` の下です。

| 面 | 置き場 | 注意 |
|---|---|---|
| kind 定数と登録 | `internal/session/session.go` の `Kind*`、`internal/sessionx/agent.go` の `agentRegistry`、マネージドなら `internal/sessionx/session_turn.go` の `managedDrivers` | **`agentRegistry` に無い kind は黙って claude になる**。`NormalizeKind` と `AgentOf` がそこへ落とす |
| capability | 自パッケージの `Caps()`、自 `Driver` の `Capabilities()` | **利用者に見える capability は、通しで駆動するまで立てない**。`Capabilities()` は driver の実装を宣言する（§20.4）|
| ターミナル起動 | 自パッケージの `BuildLaunch`（`agents.LaunchPlan` を返す）| 環境変数は `LaunchPlan.Env` に入れ、**コマンドに前置しない**（§20.3）。マネージド専用の種別は `ErrNoTerminalRoute` を返す |
| マネージドの実行系 | 自 `Driver`。`Resume` が `ThreadHandle` を返す | 形はプロセスモデルに従う（[04 §4.3](04-agent.ja.md)）|
| 呼び出し側がどちらの driver を選ぶか | Console: `console/src/agents/registry.ts` の `managedDriver` と `terminalDriver`。コンテナ内 MCP の `create_session`（`mcpStdioCall`）。CP: `control-plane/internal/mcpsrv/mcp.go` の `create_session` と `control-plane/scheduler_wake.go` の `injectDriver` | Agent は driver 未指定を `tui` にするので、**両対応でマネージドで始めたい種別は全部の呼び出し側に足す**。マネージド専用は Agent が既定を決めるが、Console には `terminalDriver: false` が要る |
| live 状態 | フック / プラグイン / CLI のストアのポーリング / runtime イベント | 状態ストアへ正規化する。working / idle / question と、`plan`・`permission`（[04 §4.4](04-agent.ja.md)）|
| transcript | `Agent.Transcript` の裏のリーダー | **CLI が読めて安定した native store を持つなら、それを読む。会話を自前ストアへ複製しない**。パーサは統合しない。そういうストアが無い種別は自分で持つ。[04 §4.3](04-agent.ja.md) の 2 つの例外がそれ |
| サインイン | Agent の `/connections/<kind>/…` ハンドラ、**および** `control-plane/routes.go` で 1 本ずつ名指しで中継するルート（ログインフローは `restLogin`）| CP へのコールバックが要る種別は無い（[08 §8.6](08-integrations.ja.md)）。サインインする相手が無い種別にはフローも無い |
| 資格の置き場と fs denylist | 自パッケージと `fsDeny`（`fs.go`）| CLI が資格情報や状態を書く場所は**ファイルブラウザから隠す** |
| MCP | `internal/mcpreg`: CLI が設定ファイルを読むなら `writerFor` の writer と `MaterializedKinds` への追加、別経路（ワイヤ上・プロセス内）で渡るなら `ServedKinds`。`knownKinds` と、`control-plane/internal/mcpsrv/mcp_server.go` の `mcpKnownKinds` | CLI ごとに設定の形もプレースホルダ方言も違う。設定ファイル型で CLI を CI で動かせる種別は `mcp-config-contract.yml` にも足す |
| エージェントへの指示 | `agent_instructions.go`: `instrSupportedKinds` と種別ごとの適用、または理由コード付きで `instrUnsupported` | Console の配布先一覧はこの 2 つから作られる。ユーザー単位の置き場が無い CLI は、**黙って捨てず理由付きで載せる**。システムプロンプトを自分で組む種別は、各層をそこで読む。lcpp は `harness.SystemPrompt` でターンごとにそうしており、どちらの一覧にも無いので、Console に lcpp の行は出ない |
| Console の descriptor | `console/src/types/session.ts` の `SESSION_KINDS` と、`console/src/agents/registry.ts` の descriptor 1 個 | 操作要素は descriptor の `caps` で決まる。ただし kind 名で分岐する画面がまだあるので grep する（下記）|
| 版ピン | `workspace/Dockerfile` の ARG、そこで書き出す `versions.json`、`deploy/local/cli-drift-check.sh` の行 | [10 §10.2.1](10-development.ja.md)。ベンダーの CLI を動かさない種別にはピンが無い |
| contract ワークフロー | `.github/workflows/` の下に専用ファイル（版のある外部製品に依存する種別）| **エージェント毎に 1 ファイル**。リリース監視に登録する。そういう製品を持たない種別は別の方法でドリフトを見る（§20.5）|

この表は kind 名が現れる場所の完全な一覧ではありません。手で持っている一覧がまだあります。
たとえば `internal/sessionx/session_io.go` の bracketed paste を使う種別、`usage_fold.go` の
`usageMeasuredForKind`、`console/src/lib/agentModels.ts` の `isDynamic`（live のモデルカタログを持つ種別）。
`workspace/agent`・`control-plane`・`console/src` を、自分と同じ driver を持つ既存種別の名前で grep し、
当たりを 1 つずつ判断してください。

## 20.3 実際に踏んだ罠

どれも実際にデバッグ時間を払ったものです。最初の 3 つは [04 §4.3](04-agent.ja.md) の起動の契約で、
根拠はそちらにあります。

- ⚠️ **環境変数は `tmux new-session -e` でプロセスに届く**。`LaunchPlan.Env` に入れれば
  `startSessionTmux` が渡します。**秘密をコマンドに前置しない**——前置は `/proc/*/cmdline` と
  tmux の `pane_start_command` に残り、ワークスペース内の何からでも読めます。
- ⚠️ **子プロセスを必ず reap する。**agent は PID 1 ではありません。wait の無い `Start()` は
  PID を**永久に**リークし、漏れるのは決まって失敗経路——「起動タイムアウトで kill して return」。
  2 つの runtime が実際にこれを出荷しました。
- ⚠️ **入れ子スキーマのフックはフラットに書くと、パースは通って二度と鳴らない。**
  エラーもログも出ず、resume が黙って新規会話になります。
- ⚠️ **tmux の target は前方一致**。セッションの target には `session.ExactTarget`（`=<name>`）を使う。
  いつか違うセッションを kill します。`capture-pane` はこの形を受け付けず、pane の target が要ります
  （`internal/tmuxx`）。
- ⚠️ **ピッカーの表示名はモデル id ではない**。live カタログを持つ種別なら `HandleCreateSession` の
  解決（`resolveLiveModel`）に足す。これが **clone / worktree の副作用より前に拒否**します。
  起動後に落ちる無効モデルはゴミを残します。拒否できるのはカタログが読めるときだけで、読めないときは
  指定値のまま起動を続けます。どの種別が解決されるかと規則の残りは [04 §4.2](04-agent.ja.md)
  （「その他のセッション操作」）です。
- ⚠️ **無料プランは別の製品**。copilot の Free プランは Auto しか使えず、Auto は `--effort` を拒否します
  （`internal/agents/copilot/program.go`）。cursor の Free プランは名前指定のモデルで起動できません
  （`internal/agents/cursor/models.go`）。無条件に付けたフラグは、**最も切り分けが難しい利用者の
  ところで**起動に失敗します。
- ⚠️ **trust / onboarding のプロンプトは認証ではない**。サインイン済みでもウィザードが出ることが
  あり、未認証と見分けがつきません（[08 §8.5](08-integrations.ja.md)）。
- ⚠️ **モーダルの回答はキー列で。ラベルを打鍵しない。**そして**配送層まで通して検証する**——
  プローブで正しいキー列が Agent に届いていないことがあります（[92](92-driving-a-tui.ja.md)）。

## 20.4 駆動していない capability を立てない

`Caps()` も Console の descriptor の `caps` もドキュメントではありません。Console はこれで操作要素を
出し分け、サーバーはこれで断ります。たとえば `PermissionChoice` の無い種別の作成には
`permission_choice_unsupported` が返ります。このリポジトリが学んだ規則は
**「利用者に見える capability は、実物の実行系で通しで駆動したものだけを立てる」**。実物とは
ベンダーの CLI やホスト、プロセス内で動く種別なら実エンジンです。具体例: 権限確認のスキップを
選べるようにするには、**承認待ちが Console から実際に答えられる**ことが要ります。フラグを外すだけ
ならどの kind でもできますが、**利用者に見えず答えられないダイアログ**で止まったセッションは、
その人から見れば**黙って固まったのと同じ**です。

`Driver` の `Capabilities()` は別の主張で、driver が何を実装しているかの宣言です。テスト以外で
読むのは `GET /sessions/{name}/settings` の 1 か所だけで、`DynamicModel`・`DynamicEffort`・
`DynamicMode` を渡します（`internal/sessionx/session_turn.go`）。違いは muse に出ています。
承認の経路は作られてテスト済みなので driver は `Permissions` を立てますが、ワークスペースの muse
セッションは承認を一度も出さないと実測されたので、`Caps()` の `PermissionChoice` は false です
（`muse/driver.go`・`muse/muse.go`）。Console に届くフィールドは利用者向けの水準で扱います。

逆向きも同じです。**能力が本当に無いなら、操作要素をそもそも出さない。**
押しても何も起きないボタンは、ボタンが無いより悪い。

[ref/agents.md](../../guide/ref/agents.ja.md) をコードと突き合わせる検査は 2 つあり、それぞれ
名指しした行だけを見ます。

- `scripts/docs-check.py`（その `ref` 検査。`docs.yml` がすべての PR で走らせる）は、すべての
  `Kind*` 定数に列があることと、`CAPS_ROWS` の行が `Caps()` と完全一致することを求めます。
  `Caps` のフィールドは `CAPS_ROWS` に載るか、`CAPS_UNMAPPED` で除外理由を持つかのどちらかです。
- `console/src/agents/guideTable.test.ts` は `ROW_TO_CAP` の行を descriptor の `caps` と突き合わせ、
  見ない行は `UNMAPPED_ROWS` に理由付きで挙げます。

どちらも `Driver` の `Capabilities()` は読まず、対応付けの無い行も見ません。そこのセルは
自分で正しくしてください。

## 20.5 検証と、ワークフローをエージェント毎に分ける理由

ローカルのテストでは足りません。CI はピン版をビルドし、self-update するワークスペースは最新版を
走らせ、headless のスモークは TUI を描かないからです。その理由と、だからワークフローを
エージェント毎に 1 ファイルにするのが規則であることは [10 §10.4](10-development.ja.md)
（「上流 CLI の破壊検知」）にあります。

なので、版のある外部製品に依存する種別には**専用の contract ワークフロー**が要ります。何を検査
できるかは種別次第です。多くはテスト用の資格情報で実 CLI を駆動します。`muse-contract.yml` は
資格情報を要さず、ホストのプロトコルスキーマと版表示の形を検査するだけで、ターンは回しません。
ベンダーの製品を持たない種別は、実際に依存している軸を別の方法で見ます。lcpp の軸は llama.cpp
サーバーの API で、実エンジンに対するオプトインの live テスト（`internal/harness/live_contract_test.go`、
ビルドタグ `manuallive`）が押さえています。これを走らせるワークフローはありません。

毎日のリリース監視 `cli-release-watch.yml` に登録し、公開版が変わったら dispatch されるようにします。
`cli-drift.yml` はピンの遅れを報告するだけで、何も dispatch しません。登録は 4 か所です。

- `deploy/local/cli-release-edges.sh` の `KINDS` に種別を足す。
- `deploy/local/cli-drift-check.sh` に行を足す。
- ワークフローの状態・エッジ・dispatch の各ステップに行を足す。
- contract 自身に、成功時に `deploy/local/cli-release-state.sh set tested <kind> <version>` を
  走らせるステップを置く。**これが無いと、監視はそのリリースを毎日新しいと見て、毎日 dispatch します。**
  `muse-contract.yml` にはこのステップが無く、今まさにそうなっています。

無人で dispatch するのは、資格情報を無人で供給できる場合だけです。**対話的な refresh で回転する
資格情報のものは「seen」として記録し、手で dispatch**します。資格情報が未設定のうちに来たリリースも
同じ扱いです。「seen」は「tested」を進めません——「新しい版に気づいた」と「テストが通った」を
混同すると、そのまま回帰が出荷されます。

## 20.6 完了の条件

動いたら完了、ではありません。次が済んで完了です。

1. `Caps()` が実物の実行系で**実際に駆動した内容**と、`Driver` の `Capabilities()` が driver の
   実装と一致している（§20.4）。
2. [ref/agents.md](../../guide/ref/agents.ja.md) の列が埋まり、§20.4 の 2 つの検査が通っている。
3. [member/06-agents](../../guide/member/06-agents.ja.md) に、**Console の言葉で**
   （`console/src/lib/i18n/locales/`）接続の仕方が書いてある。
4. 版のある外部製品に依存する種別なら、contract ワークフローが在り、リリース監視に登録され、
   実リリースに対して通っている。そうでなければ、代わりの検査（§20.5）が通っている。
5. 蒸し返され得る論点（なぜこの driver か、なぜこの id 戦略か）を決着させたなら、
   [decisions/](../decisions/) に記録が在る。
