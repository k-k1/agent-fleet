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
