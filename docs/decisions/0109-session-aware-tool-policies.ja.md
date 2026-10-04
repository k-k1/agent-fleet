# 0109. セッションを踏まえたツールポリシー: 判定点は Agent に 1 つ、強制点は kind ごと、厳しい層が勝つ

[English](0109-session-aware-tool-policies.md) | 日本語

- 状態: **proposed**（2026-10-04）。まだ何も作っていない。この記録は設計だけである。各 kind の差し込み口は、
  書いた時点のこのリポジトリのコミットから引用した。Claude Code 2.1.288 と codex 0.160.0（`workspace/Dockerfile:320,322`
  で固定している版）のフックの語彙は、入っているバイナリの文字列として読んだもので、**AF の起動フラグの下での振る舞いは
  測っていない**。ここで測った数字は、Agent のバイナリをフックとして起動するコストだけである（§ 性能の予算）。
- 追跡: #1055
- 関連: [0056](0056-tool-permission-choice.ja.md)（各 kind 自身の権限確認と、この記録が上に乗る「既定はスキップ」の選択）/
  [0055](0055-idle-stop-and-carried-interactions.ja.md)（停止をまたいで持ち越す承認）/
  [0103](0103-branch-naming-rules.ja.md)（テナントのデータがすでに Agent に届く経路と、層の先例）/
  [0093](0093-lcpp-agent-kind.ja.md)（lcpp。ツールを AF 自身が動かす唯一の kind）/
  [0095](0095-muse-agent-kind.ja.md)（muse。Workspace では承認が一度も来ない）/
  [build/07 セキュリティ](../build/07-security.ja.md) §7.1–7.2（境界はコンテナである。同じ uid の秘密は受け入れた限界）

## 背景

ツール呼び出しに承認が要るかどうかは、今は kind 自身の権限モードが決めている。ADR 0056 はメンバーが kind ごと・セッションごとに
スキップのフラグを切れるようにし、既定はどの kind でも「スキップ」のままである（`workspace/agent/internal/agents/agents.go:25-68`）。
この選択は kind ごと・ツールごとのスイッチである。Agent Fleet にはセッションの中のツール呼び出しの**並び**を見るものが何も無いので、
次のような規則は書けない。

- 「同じセッションでエージェントが npm パッケージを落とした後の `git push` は承認が要る」
- 「このセッションのツール呼び出しは N 回まで」
- 「このセッションが作ったファイルだけ編集してよい」
- 「コマンドを動かすツールやファイルシステムに触るツールの前には必ず尋ねる」

Issue が求めているのはまさにこれで、Omnigent（Databricks、Apache 2.0、alpha）にならい、デプロイ → ユーザー → セッションと積み、
厳しい層が勝つ。そしてまず設計を、と求めている。

### 今あるもの（コードから測った）

この記録の Go のパスは、別の最上位ディレクトリで始まらない限り `workspace/agent/` からの相対パス（表の中では
`internal/agents/<kind>/` からの相対パス）である。

- **CLI のツール呼び出しを拒む層はどこにも無い。** AF が配るフックはどれも状態を記録するだけで、stdout に何も出さずに終わる
  （`workspace/agent/internal/sessionx/session_status.go:62-208`）。承認要求を受け取る managed ドライバは、人に渡すか、読まずに
  承認するかのどちらかである。codex（`internal/agents/codex/appclient.go:280-311`、`driver.go:128-133` の
  `approvalPolicy:"never"`）と opencode（`internal/agents/opencode/driver.go:1273-1290`。id だけを読んで `always` と返す）。
- **claude ではすでに PreToolUse フックが 3 つ動いている。** AF が管理する `settings.json` を通して、`AskUserQuestion`、
  `ExitPlanMode`、`Write|Edit|MultiEdit|NotebookEdit|Bash` である。最後のものは、権限カードがそのツールを名指せるよう、これから
  動くツールを記録する（`internal/agents/claude/hooks.go:34,46-128`）。マッチャが空の PostToolUse はハートビートとして全ツールの
  後に発火する（`hooks.go:96`）。各フックは `workspace-agent session-status <state>` のプロセスで（`hooks.go:28-30`）、Agent の
  状態ディレクトリの下にファイルを書き、ローカルの Agent にベストエフォートで POST する（`internal/chatx/chat_report.go:200-208`）。
- **codex がフックを受け取るのは Terminal の経路だけである。** UserPromptSubmit、Stop、PreCompact、PostCompact について
  `-c hooks.<Event>=…` として渡す（`internal/agents/codex/program.go:43-50,78-85`）。共有の `codex app-server` は `-c` 無しで
  起動する（`serve.go:285`）。
- **Console には承認の面が 2 つある。** Terminal（CLI）の経路は `PermissionCard` を出し、CLI 自身のメニューにキーを送って答える
  （`console/src/features/mirror/parts/pendingCards.tsx:77-119`、`MirrorPendingCards.tsx:103-114`）。Managed の経路は要求を
  種別 `approval` の `Interaction` にし、`ApprovalRequest`（要約、ツール、コマンド、解析済みの段）を載せ
  （`internal/agents/driver.go:103-171`）、`ApprovalCard` を出し（`pendingCards.tsx:122-185`）、`POST /sessions/{name}/respond`
  で答える。このルートは Terminal セッションには 501 `respond_unsupported` を返す（`internal/sessionx/session_turn.go:294-298`）。
  状態 `permission` は `permission-request` の通知を出し、それが Console の通知とチャットブリッジに届く（`session_status.go:300-308`）。
- **本物のゲートは lcpp だけである。** ツールを動かすのが AF 自身のハーネスなので `approve()` は本当に止められ、承認役が未設定なら
  変更系の呼び出しをすべて断る（`internal/harness/approval.go:1-20`）。
- **ツール呼び出しを数えるものは無い。** 転写は求められたときに読むだけで、追従はしない（`internal/agents/claude/transcript.go`、
  `codex/transcript.go`、`opencode/transcript.go`）。Console は作業中 1.2 秒ごとにポーリングする
  （`console/src/features/mirror/pollCadence.ts:21-23`）。使用量の集計はトークンだけを畳む（`workspace/agent/usage_fold.go`）。
  managed ドライバはツールのイベントを出さない（`driver.go:214-219` の `agents.Event`）。
- **テナントのデータはすでに pull で Agent に届く。** ブランチ規則は 5 分ごとに `GET /internal/branch-rules` から取り、
  fail-open でキャッシュする（`internal/branchrule/tenant.go:7-34`）。egress にはデプロイ全体の許可リストとモードがあり、
  `GET /internal/egress/policy` から配られ、enforce はプロキシでだけ止める（build/07 §7.8）。ADR 0056 の選択をデプロイや
  テナントの単位で固定するものは無い。コントロールプレーンはそれに一度も触れない。
- **エージェントと Agent は同じ uid で動く。** `AGENT_TOKEN` と `AF_SECRET_KEY` はどのエージェントのシェルにもあり、エージェントは
  Agent の状態ディレクトリにも claude の `settings.json` にも書ける（build/07 §7.2）。

### 各 kind が差し込み口として差し出すもの

**block** は、ツールが動く*前に* kind が AF の制御下にある何かに尋ね、それに否と言え、その時点で引数が見えることを言う。
**observe** は、呼び出しが始まった後か終わった後にしか AF が見られないことを言う。**none** は、構造化されたものを AF が何も見ない
ことを言う。

| kind（実行方式） | 差し込み口 | 区分 | 引数が見えるか | 根拠 |
|---|---|---|---|---|
| claude（Terminal） | PreToolUse のコマンドフック。2.1.288 の判定の語彙は `allow` / `deny` / `ask` / `defer` | **block** | 見える（`tool_name`、`tool_input`） | AF はすでに PreToolUse を差し込んでいる（`hooks.go:34,69-80`）。語彙はバイナリのエラー文字列 "Valid types are: allow, deny, ask, defer" から |
| codex（Terminal） | `-c hooks.PreToolUse=…` による PreToolUse フック。0.160.0 ではフックは **deny** しかできず、理由が要る。`ask` と `allow` は未対応として断られる。PermissionRequest フックは承認を拒める | **block**（deny のみ） | 見える（`tool_name`、`tool_input`） | AF はほかのイベントをすでにこの方法で差し込んでいる（`program.go:43-50`）。バイナリに "PreToolUse hook returned unsupported permissionDecision:ask" と "…permissionDecision:deny without a non-empty permissionDecisionReason" がある |
| codex（Managed） | app-server の承認要求。今は自動承認。この経路にフックは設定していない | 今は **observe**。block にはスレッド構成でのフックか、`never` 以外の承認ポリシーが要る | 承認のパラメータがコマンドを持つ | `appclient.go:280-311`、`driver.go:128-133`。docs/log/76 はポリシーの変更を P1 として保留している |
| opencode（Managed） | `permission.asked` → `POST /permission/{id}/reply`。プラグインのフック `tool.execute.before`（AF は引数を書き換えるものを 1 つ配っている） | 今は **observe**。block には、全呼び出しが返答に届くよう opencode の権限設定を ask にするか、throw するプラグインが要る。どちらも測っていない | プラグイン: 見える。`permission.asked`: AF は id しか読まない | `opencode/driver.go:1273-1290`、`workspace/opencode-plugin/rtk.ts:24-38` |
| opencode（Terminal） | `--auto`。確認はペインから読む | **observe**（プラグインの差し込み口は上と同じ） | ペインの文字 | `opencode/program.go:31-36`、`screen.go:3-16` |
| cursor、kiro、copilot（Managed、ACP） | `session/request_permission`。`Respond` が allow か reject の選択肢で答える | **block。ただしスキップのフラグが切れているときだけ**。既定の `--force` / `--trust-all-tools` / `--allow-all` では要求は来ず、区分は **observe**（`session/update` の `tool_call`）に落ちる | 見える（`rawInput`。copilot は title と command だけを読む） | cursor `driver.go:375-376,913-927,1143-1189`、kiro `driver.go:470-471,1068-1090,1296-1345`、copilot `driver.go:374-377,905-919,986-1037` |
| copilot（Terminal） | ユーザースコープの `preToolUse` フックのファイル。rtk のためにすでに使っている。その出力は `permissionDecision` を持てる | **block**（測っていない） | 見える | `copilot/rtk.go:8-44` |
| kiro（Terminal） | バイナリに PreToolUse のフックの発火点がある。配線していない | 今は **observe**。block は測っていない | — | `kiro/state.go:14-17` |
| cursor（Terminal） | CLI に `hooks.json` の `beforeShellExecution` がある。配線しておらず、シェルしか覆わない | 今は **observe** | — | ADR 0023、`cursor/modal.go:3-24` |
| agy（Terminal のみ） | 無い。保留中の権限は会話の DB から読む | **observe**（`transcript_full.jsonl`） | ベストエフォート | `agy/pending.go:3-16,222-230`、`agy/agy.go:96` |
| muse（Managed のみ） | MSP の `approval/requested` に拒否の選択肢があるが、恒久的な `--disable-sandbox` の下では全呼び出しが承認層より前に `allow:policy` で決まる | **observe**（`item/*` の通知）。プロトコルとしては止められるが、Workspace では一度も尋ねない | もっとも豊か（`ToolName`、段ごとの argv） | `muse/muse.go:32-52`、`muse/handle.go:803-841,1507-1518` |
| lcpp | AF 自身のハーネス | **block**（本物、fail-closed） | 見える | `harness/approval.go:1-20` |
| shell、ssm | エージェントが居ない | **none** | — | — |

ここから 2 つのことが言える。lcpp を除けば、今止められる差し込み口はどれも**CLI が送ることを選んだフックかプロトコルの
メッセージ**であり、エージェントの uid で動く。そして ACP の kind では、差し込み口は CLI 自身のスキップのフラグが切れているときにしか
存在せず、ADR 0056 はそのフラグを既定で入れたままにしている。

## 決定

### 1. 判定点は Agent に 1 つ、強制点は kind ごと

**判定点**は Agent のプロセスの部品である。生きている各セッションの有効なポリシーと、そのツール呼び出しの台帳（決定 4）を持ち、
1 つの問いに答える。*このセッションの履歴に照らして、この正規化したツール呼び出しを動かしてよいか。allow か、deny か、メンバーに
尋ねるか。*

**強制点**は薄く、kind ごとにあり、ポリシーを持たない。それぞれが kind の差し込み口を判定点への呼び出しに変え、答えを kind の語彙に
戻す。

- **claude Terminal:** マッチャが空（全ツール）の PreToolUse フックが `workspace-agent policy-check` を動かす。このサブコマンドは
  フックの JSON を読み、ローカルの Agent に尋ね、`permissionDecision` を出す。`allow` は決して出さない。許された呼び出しは判定を
  返さない（`defer` か何も出さないか、CLI 自身のモードに任せるのはどちらかを P0 で測る）。だからポリシーは制限を足すことしかできず、
  CLI の権限モードが尋ねたであろうものを広げることは無い。
- **codex Terminal:** 同じサブコマンドを `-c hooks.PreToolUse=…` として差し込む。codex は `deny` しか受け付けないので、`ask` の判定は
  フックの中で保持する（決定 6）。
- **ACP の kind（Managed）:** ドライバの `onServerRequest` が、`Interaction` を立てる前に判定点に尋ねる。許された要求はドライバが
  答え、ask は既存の承認の `Interaction` になり、deny は reject の選択肢を送る。これは CLI のスキップのフラグが切れているときにしか
  働かないので、効いている block のポリシーを持つセッションはフラグを切って起動し、判定点はポリシーが許すものすべてについて代わりに
  答える（決定 7）。
- **lcpp:** `approve()` が自分のゲートの前に判定点に尋ねる。
- **observe しかできない kind**（agy、muse、opencode、codex Managed、そしてフックを測るまでの kiro と cursor の Terminal）は、
  すでに後から読んでいるものから台帳を埋める。それが何を意味してよいかは決定 7 が言う。

**判定点は CP ではなく Agent に置く。** フックはコンテナの中で動き、ミリ秒で答えなければならない（§ 性能の予算）。CP への往復は、
すべてのツール呼び出しの中にネットワークを置くことになる。docs/log/20 は、当時 Agent→CP が届かなかったためにフックから CP への
push を退けた。今 Agent の pull を運んでいる Workspace 専用の listener は定期的な取得のためのもので、呼び出しごとのホットパスの
ためのものではない。CP はポリシーの出どころであって、呼び出しごとの裁き手ではない。

どの強制点も、尋ねる前に呼び出しを 1 つの形に正規化する。`{session, kind, tool, category, argv の段, パス, mcp サーバ}`。
**category** は閉じた集合で、`shell`、`edit`、`read`、`web`、`mcp`、`agent`、`other` である。各 kind は自分のツール名をこれに
写す（claude の `Bash` → `shell`、`Write|Edit|MultiEdit|NotebookEdit` → `edit`。ACP の `kind` の値も同じように写す）。ポリシーは
category、argv、パスに対して書き、1 つの CLI のツール名に対しては決して書かない。どの写しも知らないツール名は `other` であり、
fail closed のポリシーは `other` を自分が名指すあらゆる category に合うものとして扱う。解析できないコマンドと同じ規則である
（決定 2）。

### 2. ポリシーのモデル: 小さな組み込みの集合、宣言的、利用者のコードは無し

ポリシーは**パラメータを持つ組み込みの型**であり、データとして保存する（エディタでは YAML、ワイヤでは JSON）。式の言語も、
スクリプトも、利用者が渡すバイナリも無い。マルチテナントの配備では判定点はメンバーごとの Agent の中で動き、あるメンバーや
テナントのコードが別の人の判定の経路で動くことはあってはならない。

最初の 4 つの型。Issue の例 1 つにつき 1 つ。

| 型 | パラメータ | 判定 |
|---|---|---|
| `tool_call_cap` | `max`（セッションごと）。任意で `categories` | 数えた呼び出しが `max` に達した後は `deny`（または `ask`。未決事項 6） |
| `approval_gate` | `after`: マッチャ。`then`: マッチャ。`verdict`（`ask` / `deny`） | `after` に合う呼び出しがセッションで一度許された後は、`then` に合う呼び出しすべてが `verdict` になる |
| `path_scope` | `allow`: 作業コピーからの相対のパスの glob、または `created_by_session: true`、あるいはその両方。`categories`（既定は `edit`） | 範囲の外の編集は `deny` か `ask` になる |
| `ask_on_categories` | `categories`（例 `shell`、`edit`） | 合う呼び出しはすべて `ask` になる |

**マッチャ**は閉じた文法である。category、ツール名、`["git","push"]` や `["npm",["i","install","ci","add"]]` のような argv の
前置部、パスの glob。組み込みの例「ダウンロードの後の push」は、`after` = {npm、pnpm、yarn、pip、uv の add/install、curl、wget}、
`then` = `git push` の `approval_gate` である。

**シェルのコマンドは解析し、解析できないコマンドは合ったものとする。** `shell` の呼び出しのコマンド行は、Agent の中のシェル
パーサで段と argv に分ける（カードのためにすでに `ApprovalRequest.Stages` がしているように）。パーサが素の argv に落とせない
コマンド（`eval`、プログラム名を生む `$(…)`、`sh` にパイプする base64）は、fail closed のポリシーの**あらゆる `shell` のマッチャに
合うものとして数える**。これで難読化はすり抜けではなく承認になる。それでもサンドボックスではない。決定 9。

「**セッションが作った**」は台帳から分かる。判定点が許した時点で対象が存在しなかった edit の category の呼び出しは、そのパスを
セッションが作ったものとして記録する。`shell` のコマンドが作ったファイルは見えない。だから `created_by_session` を持つ
`path_scope` は構造化された編集だけを制限し、そのエディタはそう言う。

利用者が書くポリシー（新しいマッチャ、新しい組み合わせ）は後で来る。同じ文法のままで、やはりコードは動かさない（§ 段階）。

### 3. 積み方: デプロイ → テナント → ユーザー → セッション、厳しい値が勝つ

ポリシーは 4 つの層で設定する。**どの層も、上の層が設定したものを緩められない。**

| 層 | 誰が設定するか | どこにあるか | どう Agent に届くか |
|---|---|---|---|
| デプロイ | super_admin | CP のデータベース | ブランチ規則と同じ pull（`GET /internal/tool-policies`、5 分ごと、最後の写しをキャッシュ） |
| テナント | テナント管理者 | CP のデータベース | 同じ応答の中で、メンバーのテナントについて解決したもの |
| ユーザー | メンバーが Settings で | CP（Workspace をまたいでメンバーについて回るように） | 同じ応答 |
| セッション | セッションを起動する者（Console、`create_session`、スケジュール） | `session.Meta`。`SkipPermissions` と同じく再起動とフォークで写す | 起動時 |

テナントの層が要るかどうかは未決事項 1 である。Issue は 3 層を挙げているが、AF にはデプロイとユーザーの間にテナントがある。

**厳しい方が勝つ。型ごとに:** 上限は最小を取る。`approval_gate` と `ask_on_categories` は和を取る。`path_scope` は積を取る。1 つの
呼び出しについての判定の順は `deny` > `ask` > 判定なしである。セッションの層のポリシーはゲートを足し上限を下げられるが、上の層が
設定したゲートを外したり上限を上げたりはできない。別のセッションが起動した子セッションは親のセッションの層を受け継ぎ、それに
足すことしかできない。だからエージェントは、断られたことをさせるために制限の無い子を起動することはできない。

**ポリシーは起動時に固定し、生きている間は締めるだけ。** 有効な集合はセッションの開始時に計算する。後の pull がポリシーを足したり
締めたりしたら、動いているセッションの次の呼び出しから効く。緩める pull はその後に起動したセッションに効く。だから CP の一瞬の
不通や編集で、動いているセッションがターンの途中で開け放たれることは無い。

### 4. 状態: 判定点が持ち、判定そのものが書く、セッションごとの台帳

判定点はセッションごとに**台帳**を持つ。category ごとの回数、立っているゲートの旗、セッションが作ったパス、正規化した直近 N 件の
呼び出し（カードと監査のため）。台帳は**判定の中で同期して**更新する。判定点が許した呼び出しは答えを返す前に記録するので、次の
呼び出しはそれを見る。転写は出どころではない。転写は求められたときに数秒遅れで読むもので、それを待つポリシーでは、後に続く
`npm install` が読まれるより先に、ゲートのかかった `git push` が動いてしまう。

- 数えるのは結果ではなく**試み**である。呼び出しは許した時点で記録し、その後成功したかどうかは問わない。ゲートと上限にとっては
  これが厳しい方の読み方である。claude ですでに全ツールで発火している PostToolUse は記録を補ってよい（終了ステータス）が、旗を
  下ろすことは決して無い。
- 台帳はセッションごとに Agent の状態ディレクトリの下に永続化するので、Agent が再起動しても上限は戻らず、セッションとともに消す。
  フォークは写し、新しいセッションは空で始める。
- observe しかできない kind では、台帳はそのドライバがすでに読んでいるイベント（ACP の `tool_call`、`events.jsonl`、`item/*`、
  転写）から遅れて埋め、判定したものではなく観測したものとして印を付ける。

### 5. 承認は既存のカードに出す

`ask` の判定は、Console にすでにある 2 つの承認の面のどちらかになる。3 つ目は作らない。

- **kind が自前で尋ねられるところ**（claude Terminal の `ask`。スキップのフラグの下でもそれが確認を出すと P0 で測れた場合。
  未決事項 9）では、CLI が自分のメニューを出し、既存の `PermissionCard` がキーでそれに答える。カードには尋ねたポリシーを名指す
  1 行が加わる。
- **それ以外のところ**では、判定点が **AF が保持する承認**を立てる。種別 `approval` の `Interaction` で、既存の `ApprovalRequest`
  （要約、ツール、コマンド、段）に、新しい任意の `policy` 欄（ポリシーの id と 1 行の理由）を足したものである。既存の
  `ApprovalCard` として描かれ、既存の `permission-request` の通知を出すので、チャットブリッジと Console の通知はそのまま運ぶ。
  このやり取りを持つのは CLI ではなく Agent なので、`POST /sessions/{name}/respond` は Terminal セッションについてもこれを
  受け付ける。Terminal セッションのほかのやり取りについては 501 のままとする。
- **スコープ**は `once` / `turn` / `thread` を流用する。ポリシーが保持する承認にメンバーが「常に許可」（セッションの間そのゲートを
  外すことになる thread スコープ）で答えてよいかは未決事項 5 である。
- **答えるのは人だけである。** 承認に答える af MCP のツールは無く、親セッションは子の代わりに承認できない
  （[guide/member/02-sessions.ja.md](../../guide/member/02-sessions.ja.md) が CLI の承認についてすでにそう言っている）。承認は
  ADR 0055 と同じく停止をまたいで持ち越せる。

### 6. フックの中で承認を保持する、そしてタイムアウト

codex では、またスキップのフラグの下で `ask` が確認を出さない場合の claude では、AF が保持する承認が開いている間フックの
プロセス自身が待ち、その後で `deny` か判定なしを出す。これで承認は CLI のフックのタイムアウトに縛られる。claude 2.1.288 の
内部のスキーマの文字列はフックのタイムアウトの上限を 600 秒としているが、測ってはいない。codex のものは分かっていない。そのタイムアウトか、ポリシー自身の
（それより短い）`approval_timeout` が答えの無いまま切れたら、判定は **deny** で、モデルが読む理由（「承認がタイムアウトした。
利用者に尋ねよ」）を付け、カードは引っ込める。無人のセッション（スケジュール、子）ではむしろ止まって待つべきかは未決事項 4 である。

### 7. 止められない kind

ポリシーは `enforcement: block | observe` を持つ。**block**（組み込みのどの型でも既定）には、強制点が止められる kind が要る。

- **起動時に** Agent は有効な集合を計算する。block のポリシーが当たり、その kind がこの実行方式では止められないなら、起動を理由付きで
  断る（`policy_requires_blocking_kind`）。ADR 0056 が `permission_choice_unsupported` を断るのと同じ方法である。Agent が解決する
  ので、ADR 0056 の決定 3 と同じく、すべての起動の経路（Console、`create_session`、スケジュール、再起動、フォーク）に効く。
- block のポリシーを持つ **ACP の kind** は、スキップのフラグを切って起動する。そのうえで判定点がポリシーの許す要求すべてに答える
  ので、メンバーにはスキップのフラグがあったときと同じものが見え、ポリシーが尋ねる呼び出しだけがカードに届く。
- **observe** のポリシーはどの kind でも動く。台帳は遅れて埋まる。違反は通知と監査の記録を出し、ターンを中断してもよい
  （未決事項 7）が、違反したその呼び出しを防ぐことはできない。

「observe のみ」のバッジを付けて起動する代わりに起動を断るのが製品として正しいかは、未決事項 3 である。

### 8. 失敗時の倒れ方、ポリシーごと

各ポリシーは `on_error: closed | open` を持つ。組み込みの既定は、`tool_call_cap`、`approval_gate`、`path_scope` は **closed**、
`ask_on_categories` は **open** である。`ask_on_categories` は便宜のためのもので、それを closed に倒すと障害が拒否の壁に変わる。

- **フックが Agent に届かない**（Agent が落ちている、再起動中）: フックは、Agent が起動時と変更のたびに書くセッションごとの
  スナップショットのファイルを読む。スナップショットは closed のポリシーが当たるかどうかを言う。当たるなら、フックは理由付きで
  拒む。当たらないなら判定なしを返す。
- **CP に届かない:** 最後に pull した写しが効いたままである（ブランチ規則と同じ）。一度も写しを取っていない配備は、組み込みの
  既定（空）で始まる（未決事項 8）。
- **コマンドを解析できない:** 決定 2（合ったものとする）。
- **判定点が内部で panic するかタイムアウトする:** 届かないものとして扱う。

### 9. セキュリティ: エージェントの uid でのガードレールであり、サンドボックスではない

この層は事故と、プロンプトインジェクションで仕込まれた並び（「これを落として、それから push して」）を捕まえる。打ち破ろうと
するエージェントは止められない。強制するものすべてと同じ uid でエージェントが動くからである（build/07 §7.2）。具体的には次の
とおり。

- **エージェントが自分のポリシーを書き換える。** ポリシーは Workspace の中からは書けない。Agent はポリシーを変えるルートを出さず、
  出どころは CP である。それでもエージェントは台帳のファイル、pull したキャッシュ、claude の `settings.json`（`disableAllHooks` を
  含む）に書ける。だから claude の強制のフックは、エージェントが書けない場所、つまりイメージの `/etc/claude-code/` の下の root
  所有の managed settings に置く。2.1.288 で managed のフックがユーザースコープの `disableAllHooks` を越えて残るかは測っていない
  （P0）。台帳とキャッシュの整合性検査は事故に対するものでしかない。意図したエージェントに対しては守られず、ガイドはそう言う。
- **プロンプトインジェクション。** モデルが読む拒否の理由はポリシーを名指し、「利用者に尋ねよ」と言う。規則がどう照合されるかは
  決して説明しないので、仕込まれた指示は正確な難読化の仕方を知ることができない。カードは書かれたままのコマンドと解析した段を
  見せるので、メンバーはエージェントによる説明ではなく、実際に動くものを承認する。
- **止められない kind を通したすり抜け。** 決定 7 が起動を断る。`create_session` で起動した子を含め、すべての経路で断る。統制下の
  セッションの中で起動した入れ子の CLI（Bash からの `claude -p`）は `AF_SESSION_NAME` と managed のフックを受け継ぐので、親の
  台帳に照らして判定される。shell セッションは人の端末であり、統制しない。
- **シェルを通したすり抜け。** `shell` の呼び出しは `edit` の呼び出しにできることは何でもでき、それ以上もできる。`path_scope` と
  `created_by_session` は構造化された編集しか見ない。シェルについても成り立たせたいポリシーは `shell` でも尋ねなければならない。
  エディタは封じ込めをほのめかすのではなく、その引き換えを示す。
- **判定点は裁定者ではない。** モデルを動かさず、ネットワークを呼ばない。ポリシーが呼び出しの安全性を LLM に尋ねることはできない。

### 性能の予算

判定はツール呼び出しのたびに通る。測定: `workspace-agent --version` の起動は 17–33 ms（中央値 22 ms、20 回、1 つの Workspace、
2026-10-04）。claude はすでに PostToolUse のハートビートでツールごとに 1 回、PreToolUse のマッチャごとにさらに 1 回これを
払っている。予算は次のとおり。

- **判定点:** p95 で判定 1 回 ≤ 2 ms。メモリの中で、allow の経路では台帳の追記を除いてディスクに触れない。
- **フックの端から端まで**（プロセスの起動、ローカルの HTTP、答え）: ツール呼び出し 1 回につき足される分が p95 で ≤ 50 ms。
- **ツール呼び出し 1 回につきプロセス 1 つ。** 強制のフックが既存の `permtool` の記録を吸収するので、1 回の呼び出しあたりの
  claude の PreToolUse のプロセス数は増えない。

P0 は本物のセッションで両方を測る。守れない予算は黙って引き上げるのではなく、P0 を止める。

### 対象外

- セッションごとのネットワークの egress。egress は build/07 §7.8 が持ち、ここのポリシーはそこに欠けている柵の代わりにはならない。
- モデルやトークンの予算。使用量の上限はすでに別にある。
- ツールの引数の書き換え。それは rtk がしており、ポリシーは決めるだけである。

## 退けた案

- **規則をモデルへのプロンプトにする。** Issue が置き換えたいのはこれである。プロンプトの中の規則はお願いであり、仕込まれた指示が
  それに勝つ。
- **呼び出しごとに CP で判定する。** すべてのツール呼び出しで、フックの予算の中に Agent→CP の往復が要り、CP の障害が統制下の
  セッションすべてを止めることになる（決定 8 はそれを最後の写しで済ませる）。
- **状態を転写から導く。** 転写は求められたときに数秒遅れで読むもので（決定 4）、ゲートのかかった呼び出しが先に動いてしまう。
- **P0 での汎用のポリシー言語（OPA/Rego、CEL）や利用者のスクリプト。** Rego は Agent ごとのランタイムと、メンバーが覚えなければ
  ならない言語を要する。スクリプトはあるテナントのコードを別のテナントの判定の経路で動かすことになる。CEL はサンドボックス化されて
  おり、利用者が書くポリシーの候補として残す（未決事項 10）。
- **代わりに各 CLI 自身のもっとも厳しいモードを入れる。** それは ADR 0056 のスイッチである。並びも上限も表せず、kind ごとに違い、
  いくつかの kind では中を確かめられない。
- **スキップのフラグを入れたまま ACP の要求に答える。** フラグが入っていると要求はそもそも送られない（cursor について測定済み、
  `workspace/agent/internal/agents/cursor/driver.go:20-24`）。

## 結果

- 統制下のセッションのすべてのツール呼び出しの前に、新しいホットパスができる。そこのバグはそうしたセッションすべての作業を止める。
  だから予算と倒れ方をポリシーごとに持ち、測る。
- block のポリシーを持つセッションは、ACP の CLI をスキップのフラグを切って動かす。判定点はポリシーが許すものすべての承認役に
  なるので、各 kind 自身の「尋ねる」集合を写さなければならない（P1 で kind ごとに測る）。
- claude の強制のフックはイメージの managed settings に移るので、Workspace のすべての claude、メンバー自身がセッション無しで
  端末から動かす `claude` にも入る。`AF_SESSION_NAME` が無ければ、すぐに判定なしを返す。
- `respond` は Terminal セッションの AF が保持する承認を受け付ける。Console の承認カードが Terminal セッションで初めて出る。
- `guide/ref/agents.md` に kind ごとの「ツールポリシー: block / observe」の行が加わり、権限の選択の行と同じく
  `scripts/docs-check.py` が kind の能力フラグと照らす。

## 利用者への未決事項

この記録が決めない、製品としての決定である。

1. **層。** デプロイとユーザーの間にテナントの層が要るか（決定 3）。それとも Issue どおりのデプロイ → ユーザー → セッションか。
2. **セッションの層を誰が設定してよいか。** 起動ダイアログのメンバーだけか、`create_session` を通した親セッションやスケジュールも
   か。決定 3 は彼らに制限を足すことだけを許す。
3. **止められない kind。** 起動を断るか（書いたとおりの決定 7）、「observe のみ」のバッジと警告を付けて起動するか。
4. **答えの無い承認。** タイムアウトの後に拒むか（書いたとおりの決定 6）、スケジュールや子のような無人のセッションでは待たせたままに
   するか（どれだけの間か）。
5. **ポリシーが保持する承認への「常に許可」。** セッションの間そのゲートを外す `thread` スコープを許すか、`once` だけか。
6. **上限に達した。** それ以降の呼び出しをすべて拒むか、引き上げを尋ねるか、セッションを止めるか。
7. **observe のみの違反。** 通知だけか、ターンの中断もか。
8. **既定。** ポリシーを何も有効にせずに出すか（書いたとおり）、ダウンロードの後の push のゲートのようなデプロイの既定を 1 つ
   入れて出すか。
9. **P0 を確定する前に測るもの**（決定ではなく測定。忘れないよう挙げる）: `--dangerously-skip-permissions` の下で claude の
   PreToolUse の `ask` が確認を出すか。`defer` が CLI のモードに任せるか。managed settings のフックが `disableAllHooks` を越えて
   残るか。codex のフックのタイムアウト。
10. **利用者が書くポリシー。** 同じ閉じた文法を広げるか、後で CEL にするか。

## 段階

| 段階 | 何を | 完了の条件 |
|---|---|---|
| P0 | 判定点と台帳。`tool_call_cap` と `approval_gate`。**claude Terminal のみ**。デプロイとユーザーの層。既存のカードでの AF の保持または自前の ask。倒れ方。スナップショット | 本物の claude セッションで `npm install x` の後の `git push` がゲートを名指すカードを出し、deny がモデルに届き、上限が N+1 回目の呼び出しを拒む。未決事項 9 の測定をこの記録に書く。予算が守られる |
| P1 | `path_scope`、`ask_on_categories`。セッションの層。lcpp。スキップのフラグを切った cursor、kiro、copilot の Managed。決定 7 の起動拒否。`guide/ref/agents.md` の行 | 挙げた各 kind で同じシナリオが通る。block のポリシーが agy の起動を断る |
| P2 | codex Terminal（deny、保持する ask）と Managed。opencode。copilot、kiro、cursor の Terminal のフック。agy と muse の observe のみの供給。要るならテナントの層 | 表の各 kind の行が推測ではなく測定になる |
| P3 | 利用者が書く宣言的なポリシー（未決事項 10） | — |
