---
audience: "新しい CLI コーディングエージェントを統合する人"
source_of_truth: "既存の `internal/agents/<kind>` パッケージ（一番近いものを写す）"
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
| kind 定数と登録 | `internal/session/session.go` の `Kind*`、`internal/agentreg/agentreg.go` の `registry` と `internal/kinds/kinds.go` の同じ kind、マネージドなら `internal/sessionx/session_turn.go` の `managedDrivers` | **レジストリに無い kind は黙って claude になる**。`NormalizeKind` と `AgentOf` がそこへ落とす |
| capability | 自パッケージの `Caps()`、自 `Driver` の `Capabilities()` | **利用者に見える capability は、通しで駆動するまで立てない**。`Capabilities()` は driver の実装を宣言する（§20.4）。`Caps()` は `GET /agents/kinds` がそのまま配る値でもある（§20.7）。`Tri` のフィールドを未設定のままにすると `unverified` と読まれ、`no` にはならない |
| ターミナル起動 | 自パッケージの `BuildLaunch`（`agents.LaunchPlan` を返す）| 環境変数は `LaunchPlan.Env` に入れ、**コマンドに前置しない**（§20.3）。マネージド専用の種別は `ErrNoTerminalRoute` を返す |
| マネージドの実行系 | 自 `Driver`。`Resume` が `ThreadHandle` を返す | 形はプロセスモデルに従う（[04 §4.3](04-agent.ja.md)）|
| 呼び出し側がどちらの driver を選ぶか | 手で足すものは無い。Agent は driver 未指定を `tui` にするので、**両対応でマネージドで始めたい種別は `Caps().ManagedDefault` を立てる**。コンテナ内の `create_session` はレジストリから、CP の `create_session` と `injectDriver` は kind カタログのスナップショット（`agentkinds.ManagedDefaultKinds`）から、Console は `GET /api/agents/kinds` から導出する（`console/src/agents/registry.ts` の `managedDriver` / `terminalDriver` はそのゲッターで、読み込み前は `console/src/agents/kindCatalog.ts` の `FALLBACK_KIND_FACTS` が答える） | マネージド専用は Agent が既定を決め、Console は `managedOnly` から `terminalDriver: false` を導出する |
| live 状態 | フック / プラグイン / CLI のストアのポーリング / runtime イベント。状態ストアを超える kind 自身の判定は `capabilities.go` の `agents.LiveStater` | 状態ストアへ正規化する。working / idle / question と、`plan`・`permission`（[04 §4.4](04-agent.ja.md)）。**Stop フックの無い kind は `PolledTurnEnd` を立てる**。立てないと完了報告が届かない |
| transcript | `Agent.Transcript` の裏のリーダー | **CLI が読めて安定した native store を持つなら、それを読む。会話を自前ストアへ複製しない**。パーサは統合しない。そういうストアが無い種別は自分で持つ。[04 §4.3](04-agent.ja.md) の 2 つの例外がそれ |
| サインイン | Agent の `/connections/<kind>/…` ハンドラ、**および** CP で 1 本ずつ名指しで中継するルート（`control-plane/relays_connections.go` の行。ログインフローは `routes.go` の `restLogin`）| CP へのコールバックが要る種別は無い（[08 §8.6](08-integrations.ja.md)）。サインインする相手が無い種別にはフローも無い |
| 資格の置き場と fs denylist | 自パッケージと `fsDeny`（`fs.go`）| CLI が資格情報や状態を書く場所は**ファイルブラウザから隠す** |
| MCP | `internal/mcpreg`: CLI が設定ファイルを読むなら `writerFor` の writer と `MaterializedKinds` への追加、別経路（ワイヤ上・プロセス内）で渡るなら `ServedKinds`。`Caps().MCPClient`。`knownKinds` は `ServedKinds` から、CP の `mcpKnownKinds` は kind カタログのスナップショット（§20.7）から導出される | CLI ごとに設定の形もプレースホルダ方言も違う。設定ファイル型で CLI を CI で動かせる種別は `mcp-config-contract.yml` にも足す |
| エージェントへの指示 | `agent_instructions.go`: `instrSupportedKinds` と種別ごとの適用、または Agent の `agents.InstructionsUnsupported`（理由コードを返す 1 行のメソッド。`cursor/instructions.go`） | Console の配布先一覧は `instrSupportedKinds` とレジストリの `InstructionsUnsupported` の答えから作られる。ユーザー単位の置き場が無い CLI は、**黙って捨てず理由付きで載せる**。システムプロンプトを自分で組む種別は、各層をそこで読む。lcpp は `harness.SystemPrompt` でターンごとにそうしている。こうした種別も `instrSupportedKinds` に載せ、配信方式 `prompt`・パス無し・`Applied` は true とする（行のスイッチがハーネスの読む値そのものになる）。プレビューの case も足す |
| Console の descriptor | `console/src/types/session.ts` の `SESSION_KINDS` と、`console/src/agents/registry.ts` の descriptor 1 個 | 操作要素は descriptor の `caps` で決まる。driver に関する事実（`managedDriver`、`terminalDriver`）はカタログから読み、読み込み前は `console/src/agents/kindCatalog.ts` の `FALLBACK_KIND_FACTS` が答える。ただし kind 名で分岐する画面がまだあるので grep する（下記）|
| 版ピン | `workspace/Dockerfile` の ARG、そこで書き出す `versions.json`、`deploy/local/cli-drift-check.sh` の行 | [10 §10.2.1](10-development.ja.md)。ベンダーの CLI を動かさない種別にはピンが無い |
| contract ワークフロー | `.github/workflows/` の下に専用ファイル（ワークスペースイメージが CLI やホストをピンする種別）| **エージェント毎に 1 ファイル**。リリース監視に登録する。lcpp はイメージで何もピンせず、代わりに手動の検査を持つ（§20.5）|

この表は kind 名が現れる場所の完全な一覧ではありません。手で持っている一覧がまだあります。
たとえば `internal/sessionx/session_io.go` の bracketed paste を使う種別、`usage_fold.go` の
`usageMeasuredForKind`。
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
- ⚠️ **ピッカーの表示名はモデル id ではない**。ピッカーや MCP の `create_session` など他の呼び出し側が、
  起動経路の受け付けない表示名や略称を送り得るなら、その種別を `HandleCreateSession` の解決
  （`resolveLiveModel`）に足す。これが **clone / worktree の副作用より前に拒否**します。
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

なので、ワークスペースイメージが CLI やホストをピンする種別には**専用の contract ワークフロー**が
要ります。何を検査できるかは種別次第です。多くはテスト用の資格情報で実 CLI を駆動します。
`muse-contract.yml` は資格情報を要さず、ホストのプロトコルスキーマと版表示の形を検査するだけで、
ターンは回しません。

lcpp は例外です。ベンダーの CLI を動かさず、話し相手の llama.cpp サーバーはワークスペースイメージ
ではなくエンジン（配備のもの、または利用者自身のサーバー。[08 §8.6](08-integrations.ja.md)）と
一緒に来るので、どのワークフローもリリース監視も対象にしていません。そのサーバーの API との契約は、
実エンジンに対して手で走らせるオプトインの live テスト（`internal/harness/live_contract_test.go`、
ビルドタグ `contract_manual`）です。

2 時間ごとのリリース監視 `cli-release-watch.yml` に登録し、公開版が変わったら dispatch されるようにします。
`cli-drift.yml` はピンの遅れを報告するだけで、何も dispatch しません。登録は 4 か所です。

- `deploy/local/cli-release-edges.sh` の `KINDS` に種別を足す。
- `deploy/local/cli-drift-check.sh` に行を足す。
- ワークフローの状態・エッジ・dispatch の各ステップに行を足す。
- contract 自身に、成功時に `deploy/local/cli-release-state.sh set tested <kind> <version>` を
  走らせるステップを置く。**これが無いと、監視はそのリリースを実行のたびに新しいと見て、2 時間ごとに dispatch します。** 無人で dispatch される contract は、失敗も `cli-contract-report.sh failure <kind> <version>` で報告してください（ほかの contract を参照）。赤い版の再 dispatch を止めるのがこれです。
  記録する版は、監視がその種別の最新として読む版と同じ形にします。muse の版はビルド ID を含む
  （`1.4.0-R4302.1`）ので、`1.4.0` に削るとエッジが閉じません。

その contract が通ったあとピンを自動で上げさせる
（[10 §10.2.2](10-development.ja.md#1022-エージェント-cli-の版上げの自動化cli-pin-bumpyml)）には、
`deploy/local/cli-pin-bump.sh` の `KINDS` に種別を足し（チェックサムをピンするなら、それを取得・
照合する `resolve_<kind>` も）、`cli-pin-bump.yml` の `workflow_run` の一覧に contract の
`name:` を足します。

人が要るときに更新 issue が開くように（10 §10.2.2）、`deploy/local/cli-update-issues.sh` の
`ALL_KINDS`、`deploy/local/cli-contract-report.sh` の `prune` の種別一覧（watcher が無人で
dispatch するなら `RED_KINDS` も）に種別を足し、`cli-drift.yml` の streak のループに contract を
足します。contract のファイル名が `<kind>-contract.yml` でない、または `cli_version` 入力を
取らないなら、`cli-update-issues.sh` の `workflow_of` と `dispatch_cmd` も広げます。これが無いと
その種別はずっと `waiting` のままで、issue は開きません。

無人で dispatch するのは、資格情報を無人で供給できる場合だけです。**対話的な refresh で回転する
資格情報のものは手で dispatch**します。資格情報が未設定のうちに来たリリースも同じ扱いです。
`deploy/local/cli-update-issues.sh` の `dispatch` か `credential` の状態に種別を足し、更新 issue が
人に実行を頼むようにします（[10 §10.2.2](10-development.ja.md#1022-エージェント-cli-の版上げの自動化cli-pin-bumpyml)）。
リリースに気づいただけで「tested」は進みません——「新しい版に気づいた」と「テストが通った」を
混同すると、そのまま回帰が出荷されます。

## 20.6 完了の条件

動いたら完了、ではありません。次が済んで完了です。

1. `Caps()` が実物の実行系で**実際に駆動した内容**と、`Driver` の `Capabilities()` が driver の
   実装と一致している（§20.4）。
2. [ref/agents.md](../../guide/ref/agents.ja.md) の列が埋まり、§20.4 の 2 つの検査が通っている。
3. [member/09-agents](../../guide/member/09-agents.ja.md) に、**Console の言葉で**
   （`console/src/lib/i18n/locales/`）接続の仕方が書いてある。
4. ワークスペースイメージがその CLI やホストをピンするなら、contract ワークフローが在り、
   リリース監視に登録され、実リリースに対して通っている。そうでなければ、実際に依存している相手に
   対する検査が通っている。ワークフローで走らせられないなら手で走らせる（§20.5）。
5. 蒸し返され得る論点（なぜこの driver か、なぜこの id 戦略か）を決着させたなら、
   [decisions/](../decisions/) に記録が在る。

## 20.7 チェックリスト

触る箇所を 1 行ずつ並べたもので、レビューでそのまま消し込めます。kind の一覧がカタログへ移るにつれて
短くなります（[#1077](https://github.com/k-k1/agent-fleet/issues/1077)）。各行の詳細は §20.2 にあります。

**kind カタログ。** Agent は `GET /agents/kinds`（`workspace/agent/agent_kinds.go`）を配ります。
レジストリのキーごとに 1 件で、`Caps()`、実効の既定 driver、MCP サーバと指示を受け取るかを
持ちます。capability の答えは `yes` / `no` / `unverified` の三値で、利用側は `unverified` を
「未対応」ではなく「不明」として扱います。CP は、問い合わせる Workspace が無いままテナント定義を
検証するので、そのスナップショット `control-plane/internal/agentkinds/catalog.json` を埋め込みます。
手で編集せず、次で作り直します。

```
cd workspace/agent && go test -run TestKindCatalogSnapshot -update-kind-catalog .
```

**新しい種別の配備は Agent が先。** スナップショットの作り直し忘れは `TestKindCatalogSnapshot` が
CI で止めますが、Control Plane が Workspace イメージより古い配備の途中までは見えません。CP は
スナップショットに無い種別の作成を `bad_kind` で断る（`agentkinds.CheckCreateKind`）ので、
スナップショットが古い CP は、新しい Agent なら受け付ける種別でも、更新されるまで断ります。
Agent のイメージを先に、または同時に配備し、その間は断られることを見込んでください。

**必須 — どの種別でも:**

- [ ] `internal/session/session.go` の `Kind*` 定数、`internal/agentreg` の `registry` と
      `internal/kinds` の一覧への登録。キーは `Kind()` と一致し（`TestRegisteredKindsSpellTheirOwnKind`）、
      2 つの一覧は揃う（`TestRegistryMatchesTheKindsList`）。`internal/kinds` の行は通知ブリッジが書く
      製品名（無ければ `""`）を持ち、`kinds_test.go` の `TestLabels` が固定する
- [ ] `Caps()` の全フィールドを決める。`MCPClient` も含む（`unverified` だと
      `TestMCPClientKindsMatchRegistry` が落ちる）。マネージドで始めたい種別は `ManagedDefault` も
      （CP とコンテナ内の呼び出し側が追従する。集合は `TestManagedDefaultKindFollowsTheRegistry` が固定）
- [ ] カタログのスナップショットを作り直す（`TestKindCatalogSnapshot`）。カタログの形が変わったら
      `testdata/wire.golden` も。続けて、ツールの説明に種別を列挙する両側の MCP ツールの golden も
      （Agent 側はスナップショットから列挙する）。`workspace/agent` で
      `go test ./internal/mcpx -run 'TestToolListGolden|TestToolCallGolden' -update-tool-golden`、
      `control-plane` で同じものを `./internal/mcpsrv` に（パッケージをフラグより前に書く。逆だと
      `go test` はモジュールのルートをビルドしてフラグを拒む）。ここでは Agent のテストを `-count=1` で
      走らせる。CP の `catalog.json` を読むが、テストのキャッシュはその変化を見ないので、キャッシュされた
      `mcpx` は golden が古いまま通る
- [ ] [ref/agents.md](../../guide/ref/agents.ja.md) と `ref/agents.ja.md` の列。`scripts/docs-check.py` が
      `CAPS_ROWS` の行（MCP を含む）を `Caps()` と、日本語の表の印を英語の表と突き合わせる
- [ ] 指示: `agents.Instructions` と `instrSupportedKinds`、または `agents.InstructionsUnsupported`、
      CLI を動かさない種別なら `noInstructionTargets`（`TestInstructionsClassifyEveryRegisteredKind`。
      `agent_instructions_kinds_test.go` の `legacyInstr` に行を足すのは新しい形を固定するときだけ）
- [ ] MCP: `MaterializedKinds`（と writer）か `ServedKinds` を `Caps().MCPClient` に合わせる。
      CP はスナップショット経由で追従する
- [ ] モデル一覧: `internal/agents/<kind>/capabilities.go` の `ListModels`（`agents.ModelLister`）。
      `GET /agents/{kind}/models` がこれを返す（`TestEveryAgentKindListsModels`）
- [ ] Console のフォールバック（`GET /api/agents/kinds` が答えるまで使う）:
      `console/src/agents/kindCatalog.ts` の `FALLBACK_KIND_FACTS`（`MCP_KINDS` はここから作る）。
      `kindCatalog.test.ts` がそれと `SESSION_KINDS` をカタログのスナップショットと突き合わせる
- [ ] 種別ごとに実測した一覧。どれも、漏れた種別を名指しするテストが守る:
      `usageMeasuredForKind`（`usage_fold.go`。`kind_lists_test.go` の `usageMeasuredByKind`）、
      移動先（`moveTargetKinds` / `moveUnmeasuredTargets`。`session_move_kinds_test.go` の
      `moveClassification`）、チャットプロバイダか `noChatProvider` への登録（`kind_lists_test.go`。
      プロバイダがあれば `chatx` の `oneShotKinds` と `providerResumeHandles` がそれぞれのテストに従う）。
      `providerResumeKinds` は `providerResumeHandles` から作る（この表は各 kind のメッセージカーソル欄も持つ。
      `TestEveryMessageCursorFieldIsListed`）。`TestMoveTargetListsAreUnchanged` は
      移動先の 2 つの一覧をリテラルで固定するので、どちらかに足した種別はこれも直す
- [ ] `wantRuntime`（`internal/sessionx/managed_runtime_test.go`）にどの種別も 1 行。マネージドの
      ランタイムを持たない種別は `nil`（`TestManagedRuntimeDispatchPinned`）
- [ ] スキル: どのエージェント種別も `capabilities.go` に `agents.SkillsLayouter`。固有の仕組みが
      確認できていなければ `SkillsForeignOnly`（`TestSkillsLayoutCoversEveryAgentKind`）

**種別固有 — その機能を持つときだけ:**

- [ ] ターミナル: `BuildLaunch` と貼り付けの判断。リテラルのキーに続く Enter を入力欄が食うなら
      `agents.PromptPaste`、そうでなければ `prompt_paste_test.go` の `literalKinds` に入れる（実測する。
      どちらにも無い種別は `TestPromptPasteClassifiesEveryRegisteredKind` が落とす）。加えて
      `capabilities.go` に `agents.PaneModeReader` と `agents.ModalProbe`
      （`TestPaneModeReaderCoversEveryTerminalKind`、`TestPromptBlockerCoversEveryTerminalKind`）
- [ ] マネージド: `Driver`、`managedDrivers`、Agent に `agents.ManagedRuntime`
      （無いと `TestManagedRuntimeFollowsCaps` が落ちる。`wantRuntime` の行はそのパッケージの関数を
      名指しする）、および `capabilities.go` に `agents.ManagedLifecycler`。Agent は起動時にその種別を
      再接続し、停止時に中断・終了させる。順序は `internal/kinds` の順（`lifecycle_caps_test.go` の
      `handWiredLifecycle` に行を足す）
- [ ] サインインのハンドラと CP の中継。資格情報は `fsDeny` で隠す
- [ ] `GET /connections` の項目: `capabilities.go` に `ConnectionStatus`（`agents.ConnectionStatuser`）、
      `TestHandleConnectionsGetKeySet` のキー一覧にその種別
- [ ] CLI を固定する種別: バージョン固定、契約ワークフロー、リリース監視への登録（§20.5）

**表示だけ — Console:**

- [ ] `console/src/types/session.ts` の `SESSION_KINDS`（表示順）と `console/src/agents/registry.ts` の記述子
      （`caps`）。記述子が無い種別も汎用アイコンで表示はされるが、Console からは起動できない
- [ ] 色・アイコン・文言。i18n の文字列を両言語で。色は `console/src/styles/tokens.css` の両テーマの
      ブロックに置く `--kind-<slug>` と、同じファイルの対応表の `.kind-<slug>` 行（`kindCssMap.test.ts`）
- [ ] `available()`: 接続の要らない種別は `availability.test.ts` の期待する一覧を変え、
      `caps.launchableFromRepo` を持つ種別は `derivedKindLists.test.ts` の `repoLaunchKinds` の
      リテラルを変える
- [ ] 記述子の一覧向けの事実: `caps.liveModels` / `caps.concreteModel`（モデル選択）、
      `caps.skillsCatalog`（設定 > スキル）、`caps.memberDefault`（CP の `memberDefaultAgents` と
      一致させる。`derivedKindLists.test.ts`）、`caps.headlessChat` なら `assistantRank`
      （アシスタントの既定の優先順位）

## 20.8 新しい種別が触るファイルを測る

ADR 0124 の「新しい種別が触るファイル数」は、既存の種別の名前を grep するのではなく、種別を 1 つ
足して測ります（[0124](../decisions/0124-second-refactor.ja.md)、2026-10-29 の追記）。
`scripts/stub-kind-count.sh` は、スタブの種別 `zz`（`scripts/stub-kind/zz.patch`。登録されて
`bash -l` を起動するだけのターミナル専用の種別で、MCP・指示の宛先・サインイン・固定する CLI を
持たない）を使い捨てのクローンに当て、`ref/agents` に列を足し、カタログのスナップショットと両モジュールの
MCP ツールの golden を作り直し、`internal/agents/zz` の外で変わったファイルを分類して並べます。
数値は、テストと Markdown を除いた合計です。

```
scripts/stub-kind-count.sh            # HEAD で数える
scripts/stub-kind-count.sh --verify   # 両 Go モジュール、Console、docs-check も走らせる
```

暫定値（2026-11-01、`instrUnsupported` を `agents.InstructionsUnsupported` へ移した後、#2933）:
`--verify` 無しの実行で、テスト・Markdown 以外 16 ファイル、含めて 21 ファイル（それまでは 17 / 22）。
`--verify` 未実行のため確定値ではありません。

数値として使えるのは、`--verify` が通った実行だけです。パッチが当たらなくなったとき、または
`--verify` で新しいガードが落ちたときはパッチを作り直します。`--keep` で実行し、残したクローンで
`--verify` の各段が通るまで直してから、スクリプトが自分で作る生成物と `ref/agents` の表を除いた差分を
書き戻します。

```
git -C <残したクローン> add -A
git -C <残したクローン> diff --cached -- . ':!control-plane/internal/agentkinds/catalog.json' \
  ':!control-plane/internal/mcpsrv/testdata' ':!workspace/agent/internal/mcpx/testdata' \
  ':!guide/ref/agents.md' ':!guide/ref/agents.ja.md' > scripts/stub-kind/zz.patch
```
