# 114-adr-0100-impl-review. ADR 0100「画像生成スタジオ」実装のレビュー

- 対象: [ADR 0100](../decisions/0100-image-generation-studio.ja.md)（決定 1〜12・影響・改訂 1〜7）に対する
  実装ブランチ。巡ごとに節を追記し、既存の節は書き換えない。
- 読み方: 🔴＝直さないと ADR の約束が本番で壊れる（マージ不可）、🟡＝実装時に明記・修正する、
  🔵＝提案。行番号は各巡で取り込んだ HEAD のもの。
  `agent/` は `workspace/agent/`、`mcpx/` は `workspace/agent/internal/mcpx/`、
  `ig/` は `workspace/agent/internal/imagegen/`、`sx/` は `workspace/agent/internal/sessionx/`、
  `con/` は `console/src/` を指す。
- 手順: ① 差分を前回の指摘から独立に読み、ADR の契約と実コードを突き合わせる。② 2 巡目以降は
  前回の指摘を 1 件ずつ **反映済み／不十分／未反映** で判定する。実 CLI は起動しない。
  試験は陰性対照（変異で赤になるか）を自分で確かめる。

---

## 1. 第 1 段（契約凍結＋P0 前提作業）のレビュー（2026-09-23）

- 対象: `temp/susa5m4`（PR #924）の `ad0d58519`（A: 番人）・`e34453e45`（B: 合図の剥がし手）・
  `3ace348f1`（C: 型・ルート・メタ・MCP 宣言）。develop `b5c59d694` に merge して読んだ。
- 検証: `go test ./internal/imagegen/ ./internal/mcpx/ ./internal/sessionx/ .`（workspace/agent）は
  1949 件緑。**自分で変異を 7 本入れ、全部赤になった**: 識別不能時の「同じフォルダに結ばれた
  セッションがある」条件を外す／`run_image_trial` の呼び出し側 `AgentTrial` 照合を外す／
  `settleInitialPrompt` の「pending からだけ」を外す／起動時回収を空にする／合図の `]` 末尾条件を
  外す／絶対パス側の拒否リストを外す／`WriteSessionMetaKeepingLock` の `Studio` 保持を外す。
  PR 本文の変異 13 本の申告と合わせ、A・B・C の陰性対照は揃っている。

### 1.1 新たに見つけた点

#### 🔴1 作成時にスタジオの `session` を結ぶ口が無い。Managed の人格ターンは必ず断られる

決定 2 の③は「Agent は**起動より前に**メタへ `Studio` を書き、**スタジオの `session` を結んでから**
`initial_prompt` を送る」（ADR:115-118）。実装の作成処理は `Meta.Studio` を書くだけ
（`sx/session_handlers.go:998-1019`）。スタジオ側への結びは無く、フックも無い。
凍結したルートで結べるのは `POST /imagegen/studios/{id}/bind`（`ig/studio.go:132-136`）だけ。
ところがセッション名は作成処理の中で採番されるので、ペインが bind できるのは作成の応答を
受け取った後になる。Managed では初回ターンを作成処理の中で同期に `h.Send` する
（`sx/session_handlers.go:1049-1060`）ので、人格ターンで最初に呼ぶ `get_image_studio` は、
ペインが bind するより先に `mcpStudioCall` のスタジオ側照合（`mcpx/mcp_studio.go:109-111`）で
「結びが変わりました」と断られる。TUI は配達が非同期なので多くは間に合うが、保証は無い。
**型は正しいが、本番でこの順序にはならない。** ストアのレーンが後から埋めるにしても、
凍結すべき継ぎ目がいまの契約に無い。

直し方（最小）: `imagegen` にフック（例 `BindStudioOnCreate(studio, session string) error`）を
宣言し、作成処理が `slot.publish` と起動より前に呼ぶ。スタジオが無い・別の生きたセッションに
結ばれているときは作成を 4xx で断る（1 スタジオ 1 セッション）。ストアが入るまでは、フック未設定なら
`studio` 付きの作成を 501 で断る。`ImageStudioBind` の用途は「結び替え・外す」と注記する。
これが入れば、いまは存在確認なしで任意の UUID がメタに書ける点（`:1007`）も同じ所で閉じる。

#### 🟡1 recreate が引き継いだ `Studio` を結び直す主体がいない

逸脱 8 は、recreate で `Studio` を引き継ぎ（`sx/session_handlers.go:1591`）、スタジオ側の追従は
後続のストアのレーンに任せるとしている。追従が無いあいだ、新しいスロットは次の状態になる。

- メタに `Studio` があるので、`generate_image` は広告から外され、呼び出し時の再検査でも断られる。
- スタジオのツールは広告されるが、呼ぶと必ず断られる。

つまり画像の手段が何も無いセッションになる。🔴1 のフックを recreate でも呼ぶ
（`studio.session` が旧名のときだけ新名へ条件付きで書き換える）ことを、いまの契約に書くこと。

#### 🟡2 `run_image_trial` の待ちと heartbeat の置き場が決まっていない

ADR の試走ツールは、最長 120 秒待ち、10 秒ごとに heartbeat を送り、パス・seed・警告・所要時間を
返す（ADR:140）。ツールの説明文も既に「Waits up to 2 minutes」と約束している
（`mcpx/mcp_stdio.go:1194`）。ところが中継は `agentDo`（15 秒・heartbeat 無し、
`mcpx/mcp_studio.go:128-130,141`）で press の応答をそのまま返す。
その応答 `ImageStudioPressResult` は版・グループ・ジョブ id しか持たない（`ig/studio.go:196-201`）。

`jobWire` は seed・files・warnings・error・elapsed_ms を持つ（`ig/jobs.go:1069-1101`）ので、
MCP 子が press の後に `GET /imagegen/jobs` を heartbeat 付きでポーリングすれば、契約を変えずに
実装できる。「待つのは MCP 子で、press は即答」とコメントか PR に明記し、MCP のレーンが取りこぼさないようにすること。

#### 🟡3 相対パスを browse root から解決するので、エージェントの cwd 相対が別のファイルを指す

逸脱 1 は、`generate_image` の相対パスを browse root から解くようにした（`ig/inputs.go:157-164`）。
しかしエージェントの cwd はリポジトリ（または worktree）で、`docs/ref.png` と書けばその中を
指したつもりでいる。browse root が home なら `~/docs/ref.png` を開くことになる。同名の別ファイルが
あれば**黙って違う絵を参照する**。無ければ理由の読めない 400 になる。
MCP 子は自分の cwd を知っている（`mcpOwningSession` が `os.Getwd` を読む）。
`mcpGenerateImage`（`mcpx/mcp_imagegen.go:206-`）と、今後の `set_image_draft` の `inputs` で、
相対パスを子の cwd で絶対化してから送ること。Console の InputPicker は browse root 相対のままでよい。
経路が違うので、解決の基準も経路で分けてよい。

#### 🟡4 合図の剥がし手が全セッションで「`[studio ` で始まり `]` で終わる最終行」を消す

`StripStudioSignal`（`ig/studio_signal.go:19-30`）と `stripStudioSignal`（`con/features/mirror/transcript/model.ts`）は、
スタジオに結ばれていないセッションにも効く。利用者が最終行に `[studio notes]` のような行を
書けば、転写・自動タイトル・返信候補・ブランチ名から黙って消える。ADR の合図は
`→ get_image_studio]` で終わる（ADR:212）。末尾条件をその文字列にすれば誤爆はほぼ無くなる。
合図を出す側（Console のレーン）はまだ無いので、**形を狭めるなら今が安い**。定数は Go と TS に
1 つずつあり、一致は試験で固定済み。

#### 🟡5 下書きの型に `full_steps` が無く、localStorage から移すと失われる（凍結型への 1 欄追加）

今のペインの下書きは `fullSteps` を持つ（`con/features/imagegen/draft.ts:61`）。`jobRequest` も
`full_steps` を持つ（`ig/jobs_http.go:55`）。ところが `ImageStudioDraft`（`ig/studio.go:19-43`）にも
`ImageStudioPress`（`:189-192`、mode と session だけ）にも、この欄が無い。スタジオを作った瞬間
（決定 2 ①の「localStorage の下書きを移す」）に、人の「試走をそのまま本番の steps で」が
表せなくなる。決定 2 の「ADR 0081 の利用者は何も失わない」（ADR:132）に反する。
下書きに `full_steps`（人だけ）を足すか、press 本文に載せる。**凍結型の変更なので、この段で入れる。**

#### 🔵1 番人の読める元に scratch base が入らない

Files ペインの絶対パスは scratch base（`/tmp/claude-<uid>` 等）も読める（`agent/fs.go:164-168`）。
一方、番人は browse root と生成物 root の 2 つだけ（`ig/inputs.go:166-177`）。これは ADR の決定 4
どおりだが、エージェントが scratchpad に置いた絵を `generate_image` の参照に渡すと、今後は断られる。
ツールの説明文かガイドに「参照は browse root か生成画像の下に置く」と 1 行書いておくと、
エージェントが迷わない。

#### 🔵2 agy に渡す参照の置き場が変わった（実機で 1 回）

agy は参照パスをプロンプトの文字列で受け取り、自分で開く（`ig/agy.go:449-458`、cwd は `~/wd`）。
渡すパスは利用者のパスから `~/.cache/agent-fleet/generated/console/inputs/<set>/` に変わった。
隠しディレクトリの下を agy のツールが読めるかは測っていない。受け入れの実機の回で 1 度確かめる。

#### 確認して問題が無かった点

- 固定コピーは provider の引数 `Request` にだけ載り、原本は `JobSpec` の記録専用欄から各 `jobRec` へ
  写される（`ig/jobs.go:235-265,363-366`）。サイドカーと `jobWire` は原本から書く（`:529,1179`）。
  番人を通っていない spec は `Enqueue` が断る（`:291-293`）。
- 掃除は 4 経路にある。投入失敗（`ig/jobs_http.go:83-86`）、`finish`、待機中の個別取消、グループ取消
  （`ig/jobs.go:623-649,791-793,889-891`・判定は `:631`）、起動時（`agent/main.go:187`）。
  `/imagegen/generate` の同期経路は `defer` で消す（`ig/http.go:555-564`）。
- 番人は root の FD の下で `RESOLVE_BENEATH|RESOLVE_NO_SYMLINKS` を使う。FIFO を `O_NONBLOCK` で
  開いた後に種別で断り、上限を `LimitReader` で二重に掛ける（`ig/inputs_open_linux.go:21-64`）。
  provider 側の読み口は `readRequestFile` 1 か所で、AST 走査で固定されている。
- `generate_image` は広告から除外され（`mcpx/mcp_stdio.go:1570-1575`）、呼び出し時にも再検査される
  （`mcpx/mcp_imagegen.go:214-219`）。スタジオのツールは両側を照合する（`mcpx/mcp_studio.go:100-115`）。
  名前は文字列リテラル。
- `InitialPromptState` について:
  - Managed は `slot.publish` で pending を書き、同期に確定する（`sx/session_handlers.go:1022-1060`）。
  - TUI は全ての戻り口で状態を返す（`sx/session_io.go:853-932`）。
  - 起動時に pending を回収する（`sx/locks.go:283-311`）。
  - 一覧のスナップショットによる上書きからは保護している（`sx/locks.go:169-172`）。
- `Studio` の欄が通る箇所: 作成・fork（引き継がない）・recreate・`wireSession`・CP の `sessionWire`・
  DB ミラー（`control-plane/workspace_handlers.go:700-710,737,757`、移行 sqlite 0071／pg 0056）。

### 1.2 総評

新規の指摘は 🔴 1、🟡 5、🔵 2 件。番人（A）と剥がし手（B）は、陰性対照まで含めて良い出来。
C の型とルートもおおむね ADR どおり。ただし 🔴1 の「作成時の結び」は凍結すべき継ぎ目が欠けていて、
Managed の人格ターンが本番で必ず断られる。🟡5 も凍結型への追加。
**この 2 件を直すまでマージは不可。** 🟡1〜4 は同じ PR で直すのが安いが、🟡2・🟡3 は後続レーンへの
明記でも可。

---

## 2. 第 1 段・第 2 巡のレビュー（2026-09-23）

- 対象: `temp/susa5m4`（PR #924）の `7cb17b6c6`（第 1 巡への対応）。§1 は変更していない。
- 検証: `go test`（imagegen 468・mcpx・sessionx）緑。自分で変異を 4 本入れた。結果は §2.1 の 🟡6。

### 2.1 新たに見つけた点

**新規 🔴 は 0 件。** 作成時の結び（`sx/session_studio_bind.go:14-29`）は `slot.publish` と起動より前に
呼ばれる（`sx/session_handlers.go:1040-1049`）。結びから起動までの間に、ほかの早期 return は無い。
起動失敗の 2 経路では、条件付きで外している（`:1056,1077`）。

#### 🟡6 巻き戻しの 3 経路に陰性対照が無い

次の 3 本の変異は、どれも **緑のまま**だった。

- `unbindStudioAfterFailedLaunch` の `BindStudioSession(studio, "", name)` を呼ばない（`sx/session_studio_bind.go:38`）。
- recreate の起動失敗時の `undoStudio` を空にする（`sx/session_handlers.go:1610-1614`）。
- recreate で、ストアが無いときの `m.Studio = ""` を外す（`sx/session_studio_bind.go:51-53`）。

PR の「起動失敗時は条件付きで外す」は実装されているが、試験では固定されていない。
起動失敗は `startSessionTmux`／`StartManagedSession` を差し替えれば作れる。
フックの呼び出しを記録し、次の 3 つを確かめる試験を 1 本ずつ足すこと。

- 作成の起動が失敗したら `(studio, "", name)` が呼ばれる。
- recreate の起動が失敗したら `(studio, old, new)` が呼ばれる。
- フックが無いとき、新しいメタに `Studio` が残らない。

#### 🟡7 置き換えられた停止中セッションのメタに `Studio` が残る

`BindStudioSession` は、停止中または削除済みのセッションを置き換えてよいとしている（`ig/studio.go:23-26`）。
しかし置き換えられた側の `Meta.Studio` を消す手順が契約に無い。そのセッションを resume すると、次のようになる。

- スタジオのツールは広告されるが、呼ぶと必ず断られる。
- `generate_image` も断られる。
- 決定 10 の杖アイコンとペインの振り分けで、別のセッションが結ばれたスタジオへ寄せられる。

§1 の 🟡1 と同じ形。直し方は 2 通りあり、どちらでも後続のストアのレーンで閉じられる。

- `BindStudioSession` を実装するストアが、置き換えた旧セッションの `Meta.Studio` を
  `sessionLockMu` の下で消す。
- フックが置き換えた名前を返し、sessionx が消す。

フックのコメントに、どちらを採るかを 1 行書いておくこと。

### 2.2 §1 の指摘を 1 件ずつ照合

| 前回 | 判定 | 根拠と残る点 |
|---|---|---|
| 🔴1 | **反映済み** | `imagegen.BindStudioSession`（`ig/studio.go:16-35`）を起動と publish の前に呼ぶ。404／409／501 で断る。起動失敗時は条件付きで外す。巻き戻しの試験は 🟡6 |
| 🟡1 | **反映済み** | recreate で旧から新へ条件付きで移し、移せなければ `Studio` 無しで起こす（`sx/session_studio_bind.go:47-59`）。起動失敗で戻す。試験は 🟡6 |
| 🟡2 | **反映済み** | heartbeat を付けた。待ちは MCP 子が `GET /imagegen/jobs` を最長 120 秒回す、と明記した（`mcpx/mcp_studio.go:128-137`）。ポーリングの実装はストアのレーン |
| 🟡3 | **反映済み** | MCP 子が cwd から絶対化する（`mcpx/mcp_studio.go:158-199`、`mcp_imagegen.go:223`）。変異で赤になる |
| 🟡4 | **反映済み** | 末尾条件を `→ get_image_studio]` にした（Go・TS）。定数の一致と変異を試験で固定 |
| 🟡5 | **反映済み** | `full_steps` を下書き（Go `ig/studio.go:64-67`・TS `wire.ts`）に人だけの欄として足した。`ImageStudioAgentFields` と `clear` の enum には入っていない |
| 🔵1 | **反映済み** | `inputs` の説明に置き場を 1 行足した（`mcpx/mcp_stdio.go:1145,1238`） |
| 🔵2 | **持ち越し** | 実 CLI の起動は禁止。PR 本文で P0 の実機受け入れに回した |

前回 🔴1・🟡1〜5 は **反映済み 6／不十分 0／未反映 0**。

### 2.3 総評

**新規 🔴 0・マージ可。** 🟡6（巻き戻しの試験の欠け）は契約を変えない。できれば同じ PR で足す。
🟡7 はストアのレーンで閉じられ、フックの型も変えずに済む（コメント 1 行で方針を固定）。

---

## 3. 第 1 段・第 3 巡のレビュー（2026-09-23）

- 対象: `temp/susa5m4` の `b0e2e6580`（🟡6）と `dd9ce6c81`（🟡7、フックのシグネチャ変更）。§1・§2 は変更していない。
- 検証:
  - sessionx・imagegen・mcpx の `go test` と、sessionx・imagegen の `go vet` は緑。
  - §2 の変異 3 本は、実装者の申告どおり赤になる試験が入った。
  - 自分で `clearReplacedStudio` の呼び出しを外す変異を入れ、`TestCreateClearsTheStudioOfTheSessionItReplaced` が赤になることを確かめた。

### 3.1 新たに見つけた点

**新規 🔴 0。** 契約の変更は `BindStudioSession(studio, session, previous) (replaced string, err error)`
（`ig/studio.go:22-32`）。imagegen はメタの錠（`sessionLockMu`）を取れない。このため、置き換えた名前を返して
sessionx が錠の下で、まだ同じスタジオを指すときだけ消す（`sx/session_studio_bind.go:67-81`）。
依存の向きに合った形で、§2 🟡7 の 2 案のうち素直な方。呼び出し側は 4 か所すべて新しい型に
揃っている（コンパイラが保証する）。ストアはまだ無いので、実装側に影響は無い。

#### 🔵3 作成の起動失敗で、置き換えた停止中セッションの結びは戻らない

作成は起動より前に旧セッションの `Studio` を消す。このため起動に失敗すると、スタジオは結び無し
（`studio.session=""`）になり、旧セッションにも戻らない。利用者は新しいエージェントを付けようと
していたので実害は小さく、ペインから結び直せる。記録だけ残す。

### 3.2 §2 の指摘を 1 件ずつ照合

| 前回 | 判定 | 根拠 |
|---|---|---|
| 🟡6 | **反映済み** | 継ぎ目 `launchTmuxFn`（`sx/session_studio_bind.go:12-14`）で起動失敗を作り、フック呼び出し `(studio,"",new)`・`(studio,old,new)` と、フック無しの recreate で `Studio` が残らないことを試験した。§2 の変異 3 本はそれぞれ赤 |
| 🟡7 | **反映済み** | `replaced` を返し、sessionx が錠の下で、まだ同じスタジオを指すときだけ消す。別のスタジオを指すメタは残す。消す呼び出しを外す変異は赤 |

### 3.3 総評

**新規 🔴 0・マージ可。** シグネチャの変更は凍結の意図を損なわない。

---

## 4. P0-B（CP＋ガイド）第 1 巡（2026-09-23）

- 対象: `temp/sz3wl63`（PR #925）の `c315ebedd`（test(cp)）と `42abadb15`（docs(guide)）。
  develop（#924 入り）に merge して読んだ。
- 検証: 新しく入った CP の試験 2 本は緑。自分で、停止中の行に `Studio: r0.Studio` を載せない変異
  （`control-plane/workspace_handlers.go:757`）を入れ、`TestSessionsPayloadKeepsStudioThroughTheMirror` が
  赤になることを確かめた。中継の試験（`control-plane/imagegen_relay_test.go`）は、次をバイト単位で固定している。
  - If-Match と ETag
  - 409
  - クエリ
  - CP が知らない鍵

  ルートは 13 行で、中継と両方の golden に載っている（#924 の本文の「14」は数え違い）。

### 4.1 新たに見つけた点

**新規 🔴 は 0 件。**

#### 🟡B1 muse は「ターミナル（CLI）だけ」でなく、P0 では付けられない

ガイドと af-usage の 3 か所が「copilot・cursor・kiro・muse も、いまはターミナル（CLI）だけ」と書く。

- `guide/member/04-files.ja.md:308`
- `guide/member/04-files.md:322`
- `workspace/agent/knowledge/af-usage.md:120`

しかし muse は `terminalDriver: false` で、マネージドにしか実行方式が無い（`console/src/agents/registry.ts:592-614`）。
さらに muse は MCP 子の環境を洗うので、af サーバに届かない。したがって P0 では、スタジオに付ける手段が無い。

ADR 決定 8 も muse を「Terminal 限定」の組に入れつつ、同じ決定の中で「false を持つのは lcpp と muse」と
書いている。つまり ADR 本文の中で食い違っている。ガイドは「muse はいまは付けられません」に直すこと。
Console のレーン（P0-C）の kind 一覧でも muse を外すことを、そちらのレビューで確かめる（レーン横断）。

#### 🔵B1 画面の語はレーン C の確定待ち

次の語は、レーン C の画面がまだ無いので ADR の語で書かれている（一覧は PR 本文）。

- 会話／設定／結果のタブ
- エージェントを替える
- この時点に戻す
- エージェントの試走を許す
- メモ

P0-C のレビューで、この一覧と画面の文言（i18n）を突き合わせる。

#### 🔵B2 af-usage の「人だけ」の欄からラベルが落ちている

`workspace/agent/knowledge/af-usage.md:116` は「モデル・seed・枚数・出力先・マスク画像」とある。
ガイド（`04-files.ja.md` の「人だけ」）と ADR 決定 4 の表では、これに `label` も入る。

#### 確認して問題が無かった点

- ガイドの記述は ADR と一致する。
  - 結び無しで残るスタジオ（決定 2）
  - 人格の送信と再送（決定 2）
  - worktree 既定 ON、OFF はターミナル（CLI）と lcpp（決定 8）
  - opencode のマネージドは不可（決定 8）
  - 人だけの欄、錠、縁取り（決定 4・6）
  - 試走は画面の下書きそのまま 1 枚、N 枚は人（決定 3）
  - `generate_image` はスタジオのセッションで使えない、CLI 組み込みの画像ツールは範囲外（決定 3）
  - 編集履歴、巻き戻し時の錠の扱い、版は押した単位（決定 9）
  - 絵の履歴と「この設定に戻す」「参照にする」（決定 9。並べる・見せるは P1 なので書いていない）
  - 知識の置き場・4 節・消すのは人・Recreate でも残る・home でない配備では「メモ」（決定 12）
- 「ワークスペースにつき 1 枚」の削除は決定 10（ペインはスタジオ id を持つ）に合う。層 B の節の撤去は決定 7 に合う。
- 用語集の「系統」は既存の対応（`glossary.ja.md:50`、画面では「系統」）どおり。入力セットの説明は決定 4 どおり。
- 見出しのアンカーは docs-check が Console の slug 規則で検査している。

### 4.2 総評

**新規 🔴 0・マージ可。** 🟡B1 は文言 3 か所の修正で、契約は変えない。できれば同じ PR で直す。

---

## 5. P0-A（Workspace Agent）第 1 巡（2026-09-23）

- 対象: `temp/sch4mby`（PR #926）の次の 4 コミット。develop（#924 入り）に merge して読んだ。
  - `9b78e8e9f`（決定 7）
  - `8447a3876`（スタジオ本体）
  - `a2b38a0ef`（`run_image_trial`）
  - `2a288b959`（bind の名前検査）
- 範囲: ストア・press・編集ログ・欄の検証・bind・MCP の試走待ちは自分で読んだ。
  history・knowledge・`view=agent` は同じセッションの下請けに読ませ、下の 🔴A2・🟡A5〜A7 はその中から自分で裏を取った。
- 検証:
  - `go test ./internal/imagegen/` は緑（496 件）。
  - 一時的な試験ファイル（削除済み・作業木はクリーン）で `applyDraftPatch` の挙動を直接確かめた（🔴A1）。

### 5.1 新たに見つけた点

#### 🔴A1 `set_image_draft` の `params` が丸ごと置き換わる。エージェントが決定 4 の外の摘みも書ける

`applyDraftPatch` は `params` を 1 つの欄として `cur[k] = raw` で置き換える
（`ig/studio_validate.go:213-217`）。実測した結果:

- 人の下書きが `{steps:30,cfg:7,sampler:"euler"}` のとき、エージェントが `{"params":{"cfg":5}}` を送ると、
  結果は `{"cfg":5}` になる。**steps と sampler が黙って消え、ファミリーの既定値に戻る。**
- ADR の約束は「部分更新。書いた欄だけ変え」（決定 3）で、例は「下書きを更新: cfg 7→5」（決定 9）。
  その一番よくある編集で、人が触っていない摘みまで動く。ツールの説明文も「Only the fields you send change」と言っている。
- さらに検証は `EngineParams` 全体を受ける（`:121-128`）。このため、エージェントが `{"params":{"clip_skip":2,"weight":3}}` を
  書けて、そのまま保存された。決定 4 がエージェントに許すのは `params{steps,cfg,sampler,scheduler}` だけ。

直し方:

- `params` は 1 段深く merge する（下位の鍵ごとに null で消す）。
- エージェントの書き込みでは、下位の鍵を 4 つに限る（それ以外は `human_only` か `invalid`）。
- `draftChanges` は `params.cfg` の単位で報告する（カードの「cfg 7→5」がこの形を前提にしている）。
- 陰性対照を付ける: steps が残ること、clip_skip が落ちること。

#### 🔴A2 8 KB への削りが終わらず、スタジオの錠を握ったまま回り続ける

最後の段（`ig/studio_agent.go:386-398`）の動き:

- 段ごとの削りの後でも 8 KB を超えていれば、長い方のプロンプトを切る。
- `Prompt` が一度 `"…"` になると、次の周も `len("…") >= len(NegativePrompt)` かつ非空なので、同じ `"…"` に戻すだけになる。
- `default` の枝（プロンプトが空のとき）には来ない。

超過の元が、どの段でも切れない所にあると、無限ループになる。例:

- 長い `label`／`out_dir`／`title`（保存時に長さの上限が無い、`ig/studio_validate.go:56-57`）
- 長い `error` を持つ since の failed 5 件（ComfyUI のエラー文は切られていない）

この処理は `lockStudio(id)` の下で動く（`:118-119`）。このため、そのスタジオへの PUT・press・bind はすべて
止まり、CPU を 1 コア食い続ける。下請けが一時試験（`Prompt:"p"`＋9,000 字の `Label`）で、2 秒以内に
返らないことを確かめた。私も読んで同じ結論になった。

直し方:

- ループが必ず進むようにする（`"…"` か空になった欄は次の対象へ回し、最後は固定の短い応答で抜ける）。
- `title`・`label`・`out_dir`・since の `error` にも上限を掛ける（since を組む時点で約 300 字）。
- 試験を 2 本足す: 長い label の場合と、長いエラーの failed 5 件＋非空のプロンプトの場合。

#### 🟡A1 エージェントの試走が `provider` の空を断らない

決定 3 の試走は「provider・model を含む＝画面が選んだ行そのもの。Agent の既定 provider や warm モデルへ
落とさない」。実装が空を断るのは `model` だけ（`ig/studio_press.go:97-101`）。`provider` が空なら、
`Enqueue` の `fleetProviderFor(ctx, "")` が既定の provider を選ぶ。複数の fleet 行が同じモデル id を
持つとき、画面と違う行で走る（元のレビュー 🔴2 と同じ懸念）。`no_provider` で断ること。
Console（P0-C）は、スタジオを作るときも provider を選び直したときも、解決済みの行 id を必ず書くこと（レーン横断）。

#### 🟡A2 opencode のマネージドを結ぶことを、サーバ側が断らない

決定 8 は opencode のマネージドをスタジオから外す（af 子を複数セッションが共有し、別のセッションから
書き込みが走り得るため）。ところが作成時の結び（`sx/session_studio_bind.go`）も、既存セッションの bind
（`ig/studio_http.go:279-301`）も、kind と実行方式を見ていない。Console が出さないだけでは、API や画面のバグで
すり抜ける。bind の口で `kind=opencode && driver=managed` を 409 で断ること。
AF_SESSION_NAME が届くまでの copilot／cursor／kiro のマネージドも、同じ口で断るのが素直。

#### 🟡A3 `history.jsonl` を作り直す処理が、実際には走らない

`appendHistory` は、ファイルが無ければ 1 行のファイルを新しく作る（`ig/history.go:33-44`、`appendLines` の
`O_CREATE`）。作り直しはファイルが無いときの `readHistory` にしかない（`:55-68`）。このため、利用者が
消した後や、アップグレード直後に最初の 1 枚ができた時点で、それ以前の絵は一覧から永久に落ちる。
決定 9 は「無ければ走査して再生成」。`appendHistory` も、ファイルが無ければ同じ錠の下で先に作り直すこと。

#### 🟡A4 作り直しで行番号が変わり、since と cursor が狂う

作り直したファイルは `created_at` 順に並び、`out_dir` の絵を含まない（`ig/history.go:64-66`）。
一方、帳簿 `seen.History`（`ig/studio_store.go:61-68`）と cursor は行番号。作り直しの後はこうなる。

- 新しい絵が since に出ない、または古い絵が「新しい結果」として出る。
- 範囲外の `before` は「cursor 無し」扱いになり、先頭の頁をもう一度返す。

帳簿と cursor にファイルの世代（先頭行の id か inode）を持たせ、変わったら初回扱いにすること。
範囲外の `before` は空の頁を返すこと。

#### 🟡A5 8 KB の最後の手段が、since を黙って捨てる

`default` の枝（`ig/studio_agent.go:393-395`）は `Since.Items` を nil にする。`More` を増やさず、
その後で帳簿は進む。このため、人の編集・巻き戻し・結果の知らせが永久に失われる（items は `null` で出る）。
🔴A2 の直しと合わせて、items を落とすなら件数を `more` に足し、`[]` で出すこと。

#### 🟡A6 `run_image_trial` の結果に seed が無い

決定 3 は、間に合ったら「パス・seed・警告・所要時間」を返す。`studioTrialJobWire`（`mcpx/mcp_studio.go`）は
`seed` を読まず、答えにも入れていない。`jobWire` は `seed` を持つ（`ig/jobs.go:1081`）。

#### 🔵A1 スタジオの錠を握ったまま、遅い処理を呼んでいる

次の 2 つは、`lockStudio` の下で provider の接続や目録を引き得る。冷えた配備では、人の PUT がその間待たされる。

- `view=agent` の `studioModelFactsFor(r.Context(), …)`（`ig/studio_agent.go:146`）
- press の `jobs.Enqueue`（`fleetProviderFor`／`resolveModelFamily`、`ig/studio_press.go:143`）

版の番号と行の順序に錠が要るのは press の①と④だけ。モデルの事実は錠の外で組めるので、外へ出すこと。

#### 🔵A2 知識ファイルの書き込みが symlink を実ファイルに置き換える

`knowledge.go:178-191` は tmp→rename で書く。このため、利用者が symlink にしていたファイル（例えば
リポジトリへ向けたもの）が実ファイルになり、mode が 0600 になる。人の手の編集と重なった書き込みの
窓は設計上の限界なので、コメントに書く。

#### 🔵A3 エージェントの付け替えは 2 手になる（Console への申し送り）

bind は、生きているセッションに結ばれたスタジオを 409 で断る（`ig/studio_store.go:203-206`）。
このため「エージェントを替える」は、いまのセッションを外す（`session:""`）→ 新しいセッションを結ぶ、の 2 手になる。
P0-C はこの順で呼ぶこと。

#### 確認して問題が無かった点

- **press の順序:** 決定 9 の 4 段のとおり。
  - ① press 行（書けなければ投入しない）
  - ② 番人
  - ③ `JobSpec.Studio/Version`
  - ④ `press_result`（即時に 1 回再試行し、`recorded:false` は 2 回とも失敗したときだけ）

  （`ig/studio_press.go:113-167`）。
- **試走の断り:** 断る 5 種ではログに何も書かない。エージェントの試走は `count=1`・`full_steps=false` に固定する。429 の文言はスタジオの言葉になっている。
- **起動時の補完:** サイドカーから recovered／lost を作る（`:190-234`）。
- **編集ログ:** 失敗時に最後の改行まで切り戻す。読む側は壊れた行を捨て、版ごとに最初の `press_result` を採る（`ig/studio_log.go`）。
- **PUT:**
  - If-Match は 412。
  - エージェントの書き込みは結びを照合する。
  - 錠は検証の後に掛ける（決定の順序どおり）。
  - エージェントが人の欄を送ると `human_only` で落とす。
- **巻き戻し:** 錠の欄も戻し、錠そのものは残す。
- **bind:** 生きている相手からは取らない。停止中の相手からは取り、`replaced` のメタを消す。削除と外すでは、メタも条件付きで外す。
- **起動時の突き合わせ:** スタジオを正とし、4 通りを扱う（`ig/studio_store.go:229-280`）。
- **決定 7 の 5 鍵:** 並べられている。cfg を読まないファミリーには出していない。
- **knowledge:**
  - 鍵は `PathEscape` で 1 段になる。先頭 `.`・NUL・200 字超は断る。
  - 要約の 1 KB は文字の境界で切る。
  - 記録の追記は他の節と人の見出しを byte 単位で保つ。
- **帳簿 `seen`:** スタジオの錠の下で保存し、`updated_at` を動かさない。
- **`run_image_trial`:** 自分のジョブだけを最長 120 秒、heartbeat 付きで待つ。

### 5.2 総評

新規の指摘は 🔴 2・🟡 6・🔵 3 件で、**マージ不可**。

- 🔴A1: 部分更新の約束が、一番よくある編集（cfg を 1 つ変える）で破れる。決定 4 の欄の限定も破れる。
- 🔴A2: 条件が揃うと、スタジオが恒久的に止まる。

どちらも直し方は局所的。🟡A1・A2 は契約（断る条件）を足す。🟡A3・A4 は履歴の約束（決定 9）に関わる。この 4 件は同じ PR で直すのが望ましい。
