# 113-review. 「画像生成スタジオ」設計へのレビュー

- 対象: [113-imagegen-studio.md](113-imagegen-studio.md)（設計案・4 巡目反映済み・`a03744b84`）
- 日付: 2026-09-23。レビューのみ。113 本文もコードも書き換えていない。実 CLI は起動していない。
- 読み方: 各件は **何が / どのファイル:行 / なぜ問題か / どう直すか**。重大度は
  🔴＝設計を変えないと壊れる／🟡＝実装時に気をつける（事実の小さなずれを含む）／🔵＝提案。
  **行番号はすべてこの worktree の HEAD（`a03744b84`＝develop に #904 を取り込んだ木）で開いて確かめた。**
  「推測」と書いた行だけが憶測。
- パスの略記: `agent/` = `workspace/agent/internal/`、`cp/` = `control-plane/`、`con/` = `console/src/`。

---

## 0. 先に結論

- **§2 の事実主張は、行番号の小さなずれを除けば 2 割が誤り**で、誤りはどれも設計の芯（D2・D4・D6・D9・D14）に
  刺さっている。
- 🔴 **7 件**。うち 3 件は「型は正しいが本番でその形にならない」の典型:
  1. **claude（と agy）には Managed ドライバが無い**。P0＝Managed 専用だと、画面見本の「claude · opus · high」は
     起動できない（🔴1）。
  2. **`sendManagedPrompt` は Managed のプロンプトの通り道ではない**。呼び出し元は 1 か所（carried の回答）だけで、
     Console の `/turn` は `h.Send` を直に呼ぶ（🔴2）。
  3. **ツールの広告を「自分のセッションのメタ」で決める前提は、Managed の 5 kind で成り立たない**。
     `AF_SESSION_NAME` が af 子プロセスに届かず cwd で推測する。D9 の「worktree 無しが既定」と組むと、
     推測が曖昧になってツールが消えるか、**opencode では別セッションとツールを共有する**（🔴3）。
  4. `splitPastedImages` は**末尾**を切る関数で、先頭の状態ブロックは剥がせない（🔴4）。
  5. ジョブの `inputs`/`mask` には**パスの番人が無い**。「`BrowseWritablePath` の内側＝投入と同じ番人」は
     存在しない番人を前提にしている（🔴5）。
  6. 知識の既定の置き場 `~/.config/agent-fleet/…` は **Files ペインが拒否し、ワークスペース方針がエージェントに
     触るなと言う場所**（🔴6）。
  7. Managed の「一級の添付」は **copilot・cursor・kiro・lcpp では黙って捨てられる**（🔴7）。
- 逆に、§9 の「広告集合は接続時のスナップショット・結び直しは resume が要る」は**悲観しすぎ**で、
  今の af サーバは tools/list を要求ごとに組み直し、`list_changed` を 1 分ごとに送る（🟡A）。
  D2 の「メタで広告を切り替える」は**識別さえ届けば**既存の仕組みにそのまま乗る。

---

## 1. §2「今あるもの」の裏取り

| 113 の主張（行） | 判定 | 根拠 |
|---|---|---|
| 下書きは `localStorage` `af.imagegen-draft.<tenant>`（`draft.ts:106`）・`storage` で同期（`ImagegenView.tsx:92-98`）(113:63-64) | 正しい | `con/features/imagegen/draft.ts:106`。`storage` は別窓（ポップアウト）だけに届き、同窓はマウント時の再読（`ImagegenView.tsx:89-91`） |
| ジョブ一覧は未完了＋完了 500 件・メモリ上（`jobs.go:41-57`）(113:66) | 正しい | `agent/imagegen/jobs.go:45,47,51`・`:17-18`「Memory is the store」 |
| サイドカー（`props.go:46-120`）(113:67) | 正しい | `ImageProps` は `props.go:46-99`、`sidecarPathFor` `:104-106`、0600 で書く `:111-117` |
| 「画像生成で開く」（`viewer/ImageProps.tsx:123`）(113:69) | 🟡 行ずれ | 実際は `con/features/viewer/ImageProps.tsx:167-175`（`openImagegen(...)` は `:172`）。`:123` は読み込み中の文言 |
| 結果カードは seed の 2 ボタン（`ResultCards.tsx:84-134`）(113:70) | 正しい | `:115-122`「同じ seed」・`:123-125`「新しい seed」。なお `onAgain`（`ImagegenView.tsx:370-375`）は**カードの prompt を戻さず**今の下書きで試走する——D7 の「この設定に戻す」が埋める穴はここ |
| 層 B は `askAssistant()` 1 発（`prompthelp.ts:41-87`）(113:71-73) | 🟡 行ずれ | `prompthelp.ts:41-87` は `buildPromptHelpMessage`。`parseProposal` は `:103-121`、`askAssistant` の呼び出しは `parts/PromptHelpModal.tsx:9,39`（D11 で消す物の一覧に `PromptHelpModal.tsx` を明記すること） |
| `knobs`（`comfy.go:329-358`）・`families.ts:42-177`・11 ファミリー (113:74-75) | 正しい | `agent/imagegen/comfy.go:329-338,344-355,357`、`con/features/imagegen/families.ts:42-177` は 11 件 |
| 要求語彙（`jobs_http.go:28-59`）(113:78-80) | 🟡 列挙漏れ | 構造体は正しいが列挙に `op`（`:30`）・`provider`・`aspectRatio`・`background` が無い。**`op` は既に語彙にある**ので D3 の「`op` をエージェント側に」は語彙追加ではない（「1 語も足さない」は守られる） |
| 警告で返る（`comfy.go:375-399`）(113:80) | 正しい | `comfyIgnoredParamWarnings` `:375-393`（cfg と scheduler だけ） |
| ペインはワークスペースに 1 枚 (113:82) | 正しい | `con/features/layout/types.ts:88` `{kind:"imagegen"}` に欄無し、`layout/ops.ts:138` |
| `/turn` の `attachments` は絶対パスの一級の添付 (113:86-88) | 🔴 **一部誤り** | 🔴7。検証も無い（`agent/sessionx/session_turn.go:154-158` は空チェックだけ） |
| プロンプトは `sendManagedPrompt`（`session_carried.go:333`）1 関数を通る (113:88-89) | 🔴 **誤り** | 🔴2 |
| Managed ドライバは 9 kind 全部にある (113:90-91) | 🔴 **誤り** | 🔴1。`session_turn.go:33-41` は 7 kind |
| `session.Meta.Model/Effort/Mode`（`session.go:386-391`）(113:92) | 正しい | `agent/session/session.go:386,390,391` |
| メタに `Origin`/`OriginSession`（`session.go:192-193`）(113:102) | 🟡 別の構造体 | `:192-193` はワイヤの `Session`。`Meta` では `:507,510` |
| af サーバは `AF_SESSION_NAME`→`mcpSourceSession`・`mcpOwningSession` `:3330` (113:93-94) | 正しい（ただし届く kind が限られる） | `agent/mcpx/mcp_stdio.go:180,3330`。届かない kind は 🔴3 |
| `generate_image` は `--image-gen` のときだけ広告（`ui_prefs.go:286`・`mcp_stdio.go:166-170`）(113:95-96) | 正しい | 解析は `:151-152`、`--self-report` 必須の判定が `:170`。構成の書き直しは `ui_prefs.go:295` |
| `MaterializeAll`（`mcp_materialize.go:46`）(113:97) | 正しい | 契機は起動（`main.go:145`）・登録変更・ui-prefs・セッション起動直前の `Materialize(kind)`。**af の定義は kind ごとに 1 つ・全セッション共通**（`mcpreg/builtin.go:103-106`） |
| 本文の添付指示は `splitPastedImages` で剥がす＝前置も同じ場所で剥がせる（`TranscriptTurn.tsx:433`）(113:98-100) | 🔴 **後半が誤り** | 🔴4 |
| `MirrorView` は `session` を受ける部品（`MirrorView.tsx:126-146`）(113:101) | 正しい（同居の罠あり） | 🟡F |
| アイドル停止・生成ジョブはセッション停止でも走る (113:104-106) | 正しい（ただし段 2 は別） | 🟡G |
| 枠: 子は 6 まで・停止で戻らない・削除は利用者だけ (113:107) | 🟡 不正確 | 🟡H |
| `chat_providers.go:2210, 2085`・`chat.go:43` (113:111-114) | 正しい | `chatToolLimits` `:2210-2215`、`chatWorkdir` `:2085`、`ChatMessage.Content string` は `chat.go:45`、既定 `claude-sonnet-5`（`chat.go:390`） |
| `generate_image` を呼べない（所有セッションが要る・`mcp_stdio.go:1471-1478`）(113:115) | 🟡 理由が違う | `:1471-1478` は `mcpImageGenAdvertise` の**広告しない**分岐。チャットの af サーバは `--self-report` 無しで起動する（`agent/chatx/chat_providers.go:960`）ので `mcpImageGenEnabled` が常に false（`mcp_stdio.go:170`）＝**そもそも広告されない**。結論（チャットは生成できない）は同じ |
| `set_chat_plan`（`mcp_stdio.go:2743-2790`）(113:116) | 正しい（範囲が広い） | `:2743-2780`。`agentGET`/`agentDo` で `/chat/conversations/{id}/plan` を叩く（`:2758-2775`） |
| 冷えた 1 枚目 5.3 分（`engine.go:216-245`）(113:122) | 正しい | `agent/imagegen/engine.go:215`、上限 `:220,236` |
| 試走の規則・`imagegenTrialMax`（`jobs.go:48-51`）(113:159-160) | 正しい（共有の枠） | `jobs.go:51,261-272,330-334,924-931`。枠が**ワークスペース共通**なのは 🟡D |
| `mcp_imagegen.go:205-228`・33 分・10 秒 (113:627) | 正しい（1 行ずれ） | `agent/mcpx/mcp_imagegen.go:204,227-228,308`。**codex は `tool_timeout_sec` で 600 秒に切られる**（`:202-203`）＝🟡D |
| muse は af サーバが 401 (113:176,600) | 🟡 古い | 資格情報の転送は `0490eb56f` で直っている（`agent/agents/muse/mcp.go:82-101`）。残る穴は `AF_SESSION_NAME`（🔴3） |
| 広告集合は接続時のスナップショット・結び直しは resume (113:597-599) | 🟡 悲観しすぎ | 🟡A |
| MCP ツール名は文字列リテラル（AST 走査）(113:622-623) | 正しい | `agent/mcpx/mcp_stdio_schema_test.go:98-142`（`*ast.BasicLit` は `:118,128`） |
| 撮影スタブ `server.mjs` (113:620-621) | 正しい | 今の imagegen は `console/scripts/shots/server.mjs:167,184,258`。メソッドを見ない（`:368-372`）ので `POST`/`PUT` も同じ応答になる |
| 「計画」の原文キャリーフォワードが「強く間違える」(113:639-640) | 🟡 引用の言い換え | log 33 `:304-306` は「原文方式では**自信を持って間違える**」で、実測の事故ではなく**想定したリスク**。実測の失敗は自動圧縮で計画が忘れられる方（§6.1 `:241-255`） |

---

## 2. 指摘（重大度順）

### 🔴1 claude と agy には Managed ドライバが無い——P0「Managed 専用」では主役が起動できない

- **何が**: `managedDrivers`（`agent/sessionx/session_turn.go:33-41`）は opencode・codex・copilot・cursor・kiro・
  lcpp・muse の **7 kind**。claude と agy は無く、Managed での作成は `driver_unsupported` で断られる
  （`agent/sessionx/session_handlers.go:722-731`、コメント「claude is out of scope (ADR 0015)」）。
  `agent/agents/claude/claude.go:29-33` も「It launches as a TUI only」。`mcp_stdio.go:2806-2807` も
  「both have managedDriver:false」。
- **なぜ問題か**: 113 は §2.2（113:90）で「9 kind 全部」と書き、§4 の画面見本は「claude · opus · high」、
  D9 は「P0 は Managed 専用・TUI は P1」。**この組み合わせでは P0 のスタジオで claude も agy も選べない**。
  「高度な推論」（2 巡目の要件）を利用者が claude で満たすつもりなら、P0 は要件を満たさない。
  §7「TUI を P0 に入れる」の却下理由（113:561-562）も「claude が TUI しか無い」ことを勘定に入れていない。
- **どう直すか**: 次のどれかを選ぶ（§4 の問い 1）。
  - (a) P0 を Managed 7 kind で出し、claude は P1（TUI）まで待つ。画面見本を codex 等に直す。
  - (b) **TUI を P0 に入れる**（D9 の「見える 1 行＋pull」を P0 に繰り上げる）。
  - (c) claude の Managed ドライバを作る（ADR 0015 の範囲外判断を覆す＝別 ADR。重い）。
  - どれでも、起動ダイアログの kind 一覧は `managedDrivers` から引くこと（手書きの一覧は古くなる）。

### 🔴2 `sendManagedPrompt` は通り道ではない——前置の置き場所が無い

- **何が**: `sendManagedPrompt`（`agent/sessionx/session_carried.go:333-343`）の呼び出し元は
  `session_carried.go:280`（carried の回答）**だけ**。Console の `/turn` op:start は `handleManagedTurn` の中で
  `h.Send(in)` を直に呼ぶ（`session_turn.go:158-162`）。Managed に届くプロンプトの実際の口は少なくとも 4 つ:
  1. `/turn` start/steer（`session_turn.go:160,162`）——Console の発言・メモの流し込み（`cp/memo.go:622-628`）
  2. `injectManagedPrompt`（`agent/sessionx/bridge_inbound.go:53,86`）——チャットのブリッジ・中断からの自動再開
     （`abort_resume.go:101,227`）・認証の自動再開（`auth_resume.go:164`）
  3. `/input` の Managed 分岐（`agent/sessionx/session_io.go:675`）——peer 宛て（`mcp_stdio.go:2479-2481`）・
     レート制限の再開予約と予約実行（`cp/scheduler_reuse.go:157-190`）・operator の `send_to_session`
  4. 作成時の `initial_prompt`（`session_handlers.go:1028`）——D10 の人格・spawn・handoff
- **なぜ問題か**: §5.1 の「`sendManagedPrompt` の手前に `injectStudioState`」（113:502）は、書いたとおりに
  実装すると**利用者の発言には 1 度も前置されない**。テストは carried の回答で緑になり、本番の輪（発言→
  前置→編集）は無音で壊れる。さらに:
  - **ドライバ内の待ち行列**: codex・opencode・copilot・lcpp は busy 中の `Send` を自前のキューに積み、
    後で `pump()` が流す（例 `agents/codex/driver.go:655-690`・`agents/opencode/driver.go:393-420`）。前置を
    `Send` の時点で組むと、**実際にターンが走る時点の「since last turn」より古い**。
  - **steer**: codex の steer は走行中のターンに合流する（`agents/codex/driver.go:612-653`）。steer に前置するか
    決まっていない。
- **どう直すか**: 前置は「口 4 つの手前」ではなく **1 つの関数に寄せてから**入れる（例: `managedSend(m, in, op)`
  を作り 4 口がそれを通る。前例は封筒の前置 `peerEnvelope` `session_peer.go:115`・`SpawnEnvelope`
  `session_spawn.go:319`・`withSelfReportHint` `session_io.go:348` が**呼び出し側でばらばら**にやっている形で、
  これを真似ない）。どの口に前置するかを表で決める（推奨: 1 と 3 の人・peer 由来は前置、2 の自動再開は前置、
  4 の人格は前置しない＝初回はまだ状態が空）。キュー積みの遅延は「前置はターン開始時に組む」をドライバの
  `pump` 側に入れないと直らない——少なくとも §9 に「キュー中の発言の前置は古い」と書く。

### 🔴3 Managed の 5 kind では af サーバが自分のセッションを知らない——D2 の広告と D9 の worktree 無し既定が衝突する

- **何が**: `mcpOwningSession()`（`agent/mcpx/mcp_stdio.go:3309-3359`）は `AF_SESSION_NAME` が無いと
  **cwd でセッションを推測**し、同じ cwd の生きたセッションが 1 本に絞れないとエラーを返す。
  `AF_SESSION_NAME` が af 子に届くのは kind・実行方式によって違う:

  | 経路 | 届くか | 根拠 |
  |---|---|---|
  | Terminal（全 kind） | 届く | tmux の起動環境 `agent/sessionx/session_tmux.go:42` |
  | codex Managed | 新規スレッドは届く・**差し替わったデーモンへ resume したスレッドは落ちる** | スレッド構成 `agents/codex/driver.go:84-120`、`mcp_stdio.go:3313-3318` |
  | lcpp | 届く | `agents/lcpp/mcp.go:73-91` の `injectSessionName` |
  | opencode Managed | **届かない**。しかも af 子は**ディレクトリごとに 1 本を複数セッションが共有** | `mcp_stdio.go:3320-3323` |
  | copilot / cursor / kiro Managed | **届かない**（子の環境は Agent 自身の環境＋α） | `agents/copilot/driver.go:366-373`、`cursor/driver.go:360`、`kiro/driver.go:458` |
  | muse | **届かない**（`ForwardEnvNames` は Agent の環境から値を写すが、Agent は持っていない） | `agents/muse/mcp.go:88-101` |

- **なぜ問題か**: D2 は「`mcpOwningSession()` のメタに `Studio` があるか」で 4 本のツールを広告する。D9 は
  「cwd はリポジトリ・**worktree は既定で作らない**」。すると:
  - 同じ作業コピーで別のセッションが動いている（普通の状態）と推測が曖昧になり、**スタジオのツールが
    黙って広告から消える**（`generate_image` が今そうなっている）。エージェントは下書きを書けない。
  - **opencode では af 子が同じディレクトリの全セッションで共有**される。推測が 1 本に絞れた場合でも、
    それは「生きている方」であって呼んだ本人とは限らない。最悪、**別のセッションの会話から
    `set_image_draft` が走り、スタジオの下書きが書き換わる**／スタジオでないセッションにツールが見える。
  - D9 の「同じ作業コピーで別セッションが動いているときは起動ダイアログが今どおり警告する」（113:315-316）は
    **存在しない**。`con/features/repos/LaunchModal.tsx` にも en/ja の文言にも無い（近いのは同じブランチの
    二重チェックアウト `launch.branch_in_use` `LaunchModal.tsx:743-748` と、サーバ側の「生きたセッションがある
    作業コピーは切替・削除できない」`con/i18n/ja/errors.ts:25,29`）。
- **どう直すか**（§4 の問い 2）:
  - 最低限: スタジオのセッションは **worktree（または専用ディレクトリ）を既定にする**——cwd の推測を
    一意にする唯一の手段。「資料を見ながら」は worktree でも成り立つ。
  - 本筋: `AF_SESSION_NAME` を届ける。copilot/cursor/kiro は**セッションごとの子**なので `cmd.Env` に 1 行
    足せば届く見込み（推測: ACP の CLI が MCP 子へ環境を引き継ぐかは未測定。codex のように洗うなら
    構成側で渡す必要がある）。muse は `session/start.config.mcpServers` の `env` に値を入れれば届く
    （`agents/muse/handle.go:232-238` がセッション単位の構成）。**opencode Managed は共有デーモンなので
    構造的に無理**——スタジオの kind から外すか、Terminal に限る。
  - スタジオのツールは曖昧なとき「広告しない」でなく**呼ばれたら理由付きで断る**方が利用者に見える
    （広告から消えると、エージェントは「そんなツールは無い」と言うだけ）。

### 🔴4 `splitPastedImages` は先頭の状態ブロックを剥がせない——生の本文を読む口が他に 5 つある

- **何が**: `splitPastedImages`（`con/lib/pastedImages.ts:45-66`）は、`/pasted/…/paste-N` のパスが本文に
  **無ければ何もせずに返す**（`:51`）。あれば、既知の添付指示文が**最初に現れる位置から後ろ**を切る（`:58-62`）。
  作りが「本文＋指示文＋パス」の**接尾辞**（`buildImagePrompt` `:77-81`）専用で、先頭のブロックは残る。
  しかも Managed は添付を本文に織り込まない（`MirrorView.tsx:1187`）ので、**Managed のターンは
  そもそもこの関数を素通りする**。
- **生の本文を読む口**（前置が見える／効いてしまう所）:
  - 自動タイトル: `writeConversationWindow` は各ターンの**先頭 400 字**を使う（`agent/sessionx/session_title.go:278-284`）。
    状態ブロックが先頭にあると、**タイトルが状態ブロックから付く**。ブランチ名提案（`:306` 以降）も同形。
  - コピー（`con/features/mirror/transcript/TranscriptTurn.tsx:164-168`）・入力履歴と Ctrl+R（`MirrorView.tsx:1421-1426`）・
    その時点から分岐（`MirrorView.tsx:1554`→`ForkAtModal.tsx:52,70`＝**新しいセッションの下書きに状態ブロックが入る**）。
  - 返信候補（`session_suggest_reply.go:205`）は末尾を残すので害は小さい。翻訳は user ターンを訳さない（`TranscriptTurn.tsx:131`）。
- **どう直すか**: 前置専用の剥がし手を**転写モデルの層**（`con/features/mirror/transcript/model.ts`、peer／spawn
  封筒を解析している場所 `:52-80`）に置き、上の全消費者がそこを通るようにする。サーバ側（タイトル・ブランチ名・
  返信候補）にも同じ剥がし手が要る（Go 側に 1 つ、見出しを定数で共有）。§9 の「`splitPastedImages` と同じ位置・
  同じ試験の形」は取り下げる。
  - 🔵 見出しを `<` で始めない（`isNoise` は `<system-reminder>` 等で始まる user ターンを**丸ごと隠す**
    `model.ts:22-41`）。`[image studio state]` はこれに当たらないので今の案で良い。

### 🔴5 ジョブの `inputs`/`mask` にはパスの番人が無い——D3 の「投入と同じ番人」は存在しない

- **何が**: `spec()`（`agent/imagegen/jobs_http.go:103-162`）は `inputs`/`mask` をそのまま写し（`:157`）、
  Comfy は `os.ReadFile(path)` でそのまま読んでエンジンへアップロードする（`agent/imagegen/comfy.go:1186-1187`、
  呼び出しは `:1035,1054`）。検査は枚数と op×mask の組み合わせだけ（`comfy.go:1136-1152`）。
  `BrowseWritablePath` が掛かるのは `out_dir` と props だけ（`props.go:133-136,153,599-617`）。
  Console の相対パスが通るのは Agent の cwd が `/home/dev` だから（`InputPicker.tsx:51`・`workspace/Dockerfile:721`）。
- **なぜ問題か**: 113 D3（113:191-192）と §9（113:643-644）は「`inputs` に置けるのは `BrowseWritablePath` の内側
  だけ＝投入と同じ番人」「投入時にも同じ検証が走る（二重は意図）」と書くが、**投入側には無い**。
  エージェントに `inputs` を書かせると、`../` や絶対パス（`~/.config/agent-fleet` の暗号化ストア・資格情報を含む）
  を**画像としてエンジン（別配備から借りたエンジンを含む）へ送れる**。プロンプト注入（リポジトリの資料を読む＝
  D9 の芯）と組むと現実的な経路になる。人のボタンでも同じ穴があるが、人は自分でパスを選ぶ。
- **どう直すか**: 番人を**投入の `spec()` に入れる**（`BrowsePath` の内側・拒否リスト `workspace/agent/fs.go:126` 付き）ことを
  P0 の前提作業にする。D3 の `op`/`inputs` 解放（§8 残り 3）はこれが入るまで開けない。113 の文言は
  「番人を足す」に直す。

### 🔴6 知識の既定の置き場 `~/.config/agent-fleet/imagegen/knowledge/` は、人もエージェントも触れない

- **何が**:
  - Files ペインは `.config/agent-fleet` を**拒否リスト**に持つ（`workspace/agent/fs.go:126`「encrypted secrets store +
    connection state」、`agent/paths/paths.go:2-3`）。D14 の「人は Files ペインで直せ」（113:399）が既定の置き場で成り立たない。
  - ワークスペース方針（`/etc/claude-code/CLAUDE.md`「Do not」＝全セッションに入る）は `~/.config/agent-fleet` を
    「read or touch the agents' internal state」として**エージェントに禁じている**。D14 の「要約・設定・プロンプトの
    書き直しはエージェントの Edit」（113:406-408）と、人格が「Read で読める（パスを返す）」（113:414）と言うのは、
    方針と正面から衝突する（従順なエージェントほど拒む）。
  - Agent のコードがそこへ書くこと自体は正しい（`paths.go:19-45` が user-authored content の置き場として
    `knowledge/` を挙げ、`~/.config/agent-fleet/knowledge/af` の前例 `workspace/agent/assistants.go:39`）。問題は**人とエージェントに
    ファイルとして見せる**設計の方。
- **どう直すか**（§4 の問い 3・113 §8 残り 1 の前提が変わる）: 既定の置き場を拒否リストの外に移す
  （例 `~/imagegen-knowledge/` や `~/.local/share/agent-fleet-imagegen/`＝recreate を生き、Files ペインに出る）。
  または「ファイル」をやめ、読み書きを MCP（`get_image_knowledge`/`set_image_knowledge_section`）と専用 UI に
  寄せる（エージェントがパスを知る必要が無くなる）。前者が D14 の「kind を選ばない」を保つ。
  - 🟡 ecs-ec2 では `~/.config/agent-fleet` は共有 EFS 上（`paths.go:24-28`）。毎ターンの前置で読むなら
    キャッシュが要る（EFS のメタデータ I/O は既知の性能問題）。

### 🔴7 Managed の添付は copilot・cursor・kiro・lcpp では黙って捨てられる

- **何が**: ドライバごとに扱いが違う。opencode は全ファイル一級（`agents/opencode/driver.go:947-969`）、codex は
  画像だけ `localImage`・他は本文に織り込み（`agents/codex/driver.go:1074-1100`）、muse は画像を base64・他は本文
  （`agents/muse/handle.go:771-788`）。**copilot・cursor・kiro・lcpp は `in.Attachments` を読まない**（添付だけの
  ターンは空プロンプトで断られる `copilot/driver.go:561`・`cursor/driver.go:563`・`kiro/driver.go:742`・`lcpp/driver.go:390`）。
  型のコメントも codex と opencode しか言っていない（`agents/driver.go:21-24`）。
- **なぜ問題か**: D6「この絵をエージェントに見せる」と D9「D6 の添付も Managed だけ一級」は、4 kind で**押しても
  何も届かず、エラーも出ない**。エージェントは見ていない絵について答える。
- **どう直すか**: 「見せる」の可否を kind の能力（新しい `Caps` 欄か、`imagePaste` と同じ扱い＝ミラーは
  `agent.caps.imagePaste` `MirrorView.tsx:157` で判定しており、113 の言う `canAttach` という判定は無い）で決め、
  非対応は理由付きで無効。能力表は `guideTable.test.ts` の突き合わせに載せる（lcpp で能力表が古くなった前例）。

---

### 🟡A §9「接続時のスナップショット・結び直しは resume」は事実と違う（良い方に）

- tools/list は**要求ごとに** `mcpStdioToolList()` を組み直す（`mcp_stdio.go:286-298`）。`generate_image` の可否は
  そのたびに `GET /imagegen/status?session=<self>` を引き（`mcp_imagegen.go:115-117`）、Agent 側は既に
  `session.ReadMeta(name)` を読んでいる（`agent/imagegen/http.go:213-217`）。**`Studio` を 1 欄足すだけで D2 が乗る**。
- `listChanged: true` を宣言し（`mcp_stdio.go:274,377`）、最初の tools/list の後は 1 分ごとに指紋を取り直して
  `notifications/tools/list_changed` を送る（`:480-545`）。lcpp は通知を受けて取り直す（`agent/mcpc/server.go:146-156`）。
  claude の接続時スナップショットは ADR 0072 の 2026-09-15 訂正（`docs/decisions/0072-engine-model-catalog.ja.md:2078-2085`）の話で、
  その訂正が watcher を生んだ。
- **直す**: §9 の「結び直しは resume を伴う」「トグルは結び直しまで効かない」（113:597-599,628-629）を
  「通知を尊重するクライアントなら 1 分以内。尊重しない kind は resume」に。kind ごとの尊重の有無は**未測定**
  （claude 以外）——P0 の受け入れで測る項目に入れる。
- 🟡 注意: status の 3 秒タイムアウトで失敗すると `generate_image` が一覧から落ち（`mcp_stdio.go:1480-1482`）、指紋が
  変わって通知が飛ぶ。スタジオのツールを同じ呼び出しで決めると**Agent が遅いときにツールが点滅する**。
  スタジオの判定はメタの読みだけで済むので、status と切り離して落ちない側に倒す。

### 🟡B D4 の固定費 400〜700 トークンは「1 ターン分」で、会話全体では累積する

- 前置は user メッセージとして**そのまま転写に残り**（全ドライバで確認: `cursor/driver.go:637`・`kiro/driver.go:815`・
  `lcpp/driver.go:466`、codex/opencode は自前の履歴）、以降のターンで**毎回再送される文脈**になる。n ターン目の
  入力には前置 n 個分が載る（プロンプトキャッシュが効く kind では課金は軽いが、窓は食う）。
- **lcpp は致命的に近い**: 圧縮境界から全履歴を毎ターン再生し（`agents/lcpp/store.go:467-472`）、窓は 8000 以上
  （lcpp の実測メモ）。600 トークンの前置は **10 ターン強で窓の大半**を占める。D10 は lcpp を「CLI ログインの
  無い会員の答え」に挙げている。
- 見積りそのもの: 下書き JSON 1 KB 弱＋錠＋「since last turn」（結果 1 件 ≒ 30〜50 トークン、40 枚の量産後は
  件数上限が無い）。**D14 の「要約」節（モデル・ファミリー各 1 KB＝日本語なら各 300〜400 トークン）が載ると
  700 を超える**。
- 113 の中で矛盾: D14（113:412）は「前置には要約節だけ・モデルが替わったときだけ」、§9（113:630-632）は「末尾 20 件・
  2 KB が毎ターンの固定費」。どちらかに揃える。
- **直す**: 「since last turn」の件数に上限（例 5 件＋「他 N 件」）。lcpp では前置を最小形（D9 の TUI 用 1 行）に落とす。
  古い前置を剥がす手段は無い（転写は CLI の物）ので、**費用は累積すると本文に書く**。

### 🟡C D4 の帳簿 `LastSentModel` がスタジオに付いていると、結び替えた新しいエージェントは facts を受け取らない

- §5.1（113:473）は `LastSentModel` を `Studio` に持つ。D1 の結び替え（sonnet→opus・claude→codex）で新しい
  セッションが結ばれると、帳簿は「送った」と言うので前置は `unchanged — get_image_studio for details` になる。
  圧縮で facts が要約から落ちた場合も同じ。
- **直す**: 帳簿を**(スタジオ, セッション) の組**に持ち、結び替え・`compacting` の検出でリセットする。

### 🟡D 試走の枠 3 はワークスペース共通・codex は 10 分で切れる

- `var jobs = newJobQueue()`（`jobs.go:186`）1 本で、`pendingTrialsLocked`（`:345-353`）は送り手を問わず数える。
  **全スタジオのエージェント試走・人の試走ボタン・他のペインの試走が同じ 3 枠**を取り合う。エージェントは
  他人の試走で 429 `trial_pending` を受け、今の文言「look at those before asking for another」（`jobs.go:303-306`）は
  エージェントに**自分のでない試走を見ろ**と言う。試走は**新しい物が先頭**に入る（`:330-334`）ので、連打すると
  人の試走を追い越す。
- `run_image_trial` の待ちは 33 分だが、**codex は `tool_timeout_sec` で 600 秒**（`mcp_imagegen.go:202-203`）。冷えた
  qwen-image-edit の 5.3 分は入るが、エンジンの上限 16 分（`engine.go:220`）までは入らない。
- **直す**: 429 の文言をスタジオ文脈で書き直す（「他の試走が 3 枚待っています」）。エージェントの試走は
  スタジオごとに 1 枚までに絞る案（🔵）。codex の上限は §9 に書く。

### 🟡E D7 の `draft_log` 全文 500 件は fstore では持てない形

- `fstore` には read-modify-write も錠も無い（`agent/fstore/fstore.go:30-35` は素の `os.WriteFile`・原子的な rename でもない）。
  113 §5.1（113:464-465）の「`fstore` の read-modify-write を使う」は存在しない API を指している（メモの題名は
  「使うな」の方）。チャットは `LockConv`（`agent/chatx/chat_store.go:24-30`）でメモリ上の錠を取っている——これが前例。
- 1 ファイルに `draft_log` 500 件（113 の見積りで ≒500 KB）を持つと、`set_image_draft` と人の 500 ms デバウンス PUT の
  **たびに 500 KB を書き直す**。非原子的な書き込み中に Agent が落ちると**スタジオごと読めなくなる**（下書き・錠・版も）。
  ecs-ec2 では EFS 上。
- `If-Match`/`ETag` は Agent のどのハンドラにも無い（CP が JSON GET に弱い ETag と `If-None-Match` を付けるだけ
  `cp/etag.go:12-29`）。113 §5.2-5 の `If-Match` は新規実装。CP の ETag は**本文全体のハッシュ**なので、2 秒ポーリングの
  304 は本文が小さいほど効く。
- **直す**: `draft_log` は**別ファイルの追記専用 JSONL**（`studios/<id>.log.jsonl`）にし、スタジオ本体は小さく保つ。
  本体は tmp＋rename で原子的に書く。`If-Match` は `UpdatedAt` を ETag 相当として Agent 側で比べる（113 の案どおり）。

### 🟡F `MirrorView` の埋め込みは、同じセッションのミラーペインと同居すると状態が衝突する

- 部品としては埋め込める（`Pane.tsx:691-701` が唯一のマウント点・ペイン文脈への依存は `paneId` を TTS に渡すだけ
  `MirrorView.tsx:353`・`.mirrorview` が自前の `container: paneview` を持つ `mirror/parts/shell.css:17`）。
- ただし D8「左レールから開くと imagegen ペイン」でも、ミラーペインで同じセッションを開く経路は残る。同居すると:
  入力の下書き `af.mirror-draft.<session>`（`MirrorView.tsx:283-284`・インスタンス間同期なし `lib/draft.ts:59-78`）、
  添付の下書き（`:301`）、送信エコーの `echoStore`（モジュール変数・`parts/sendEcho.ts:11`）、起動シード（取り出し 1 回
  `lib/launchSeed.ts:14`）が**後勝ちで互いを潰す**。handoff の 3 秒ポーリングが 2 倍（`HandoffProposal.tsx:38-53`）。
  Ctrl+F はペイン側の `PaneFind`（`Pane.tsx:661`）なので埋め込みには無い。
- **直す**: スタジオに結ばれたセッションをミラーペインで開こうとしたら imagegen ペインへ寄せる（`sameTarget` 側で
  同一視）か、上の鍵にペイン id を足す。

### 🟡G 段 2（ワークスペース停止）は生成中でも止める——113 は段 1 しか書いていない

- 段 1（セッション停止）について 113（113:104-106,610-613）は正しい。だが段 2 の `busy` は `RepoJobs` を見るだけで
  imagegen のジョブを見ない（`cp/reaper.go:570`・`cp/session_activity.go:72-102`）。在席は**端末に触れたか**
  （`presenceGrace` 30 分 `reaper.go:80`）で、imagegen ペインの 2 秒 GET は数えない（非 GET だけが時計を 1 回戻す
  `cp/proxy.go:155-162`）。
- 40 枚を眺めている間にワークスペースが止まると、**メモリ上のジョブキュー（未完了）は消える**（`jobs.go:17-18`）。
  ADR 0081 からある穴だが、スタジオは「眺める時間」を長くする方向の設計なので当たりやすくなる。
- **直す**: 別件として起票（imagegen の未完了ジョブを段 2 の busy に数える）。113 §9 に 1 行。

### 🟡H §5.3 の「枠」は spawn の子の枠で、人が起動したスタジオのセッションには当たらない

- 子の枠は既定 **3**・設定の上限が 6（`agent/session/session.go:75,86`）で、数えるのは `origin=session` かつ
  `OriginSession==親` の非アーカイブ（`agent/sessionx/session_spawn.go:49-60`）。**アーカイブで戻る**（停止では戻らない・
  停止から `StoppedTTL` 7 日で自動アーカイブ `session.go:564-571`）。
- Console の「エージェントを付ける」で人が起動するスタジオのセッションは子ではない。113 の「使い終わったらアーカイブ
  しないと枠が戻らない」は、別の何か（ワークスペースのセッション数上限など）を指すならその根拠を書く。無いなら
  §5.3・§9 の「枠」の段は削ってよい（アーカイブまで行う方針自体は一覧の整理として良い）。

### 🟡I メタに `Studio` を足す先は 5 か所で、fork／recreate では明示のリテラルが落とす

- `Meta` 構造体（`session.go:377-`）に足すだけでは足りない: 作成・fork・recreate の `Meta{...}` リテラル
  （`agent/sessionx/session_handlers.go:992-998,1225-1236,1566-1573`）が欄を列挙して写す。fork したセッションが
  スタジオを引き継ぐべきかは**決めること**（推奨: 引き継がない＝1 スタジオ 1 セッション。D1 の結びが 2 本になる）。
- Console が杖のアイコン（D8）を出すにはワイヤの `Session`（`session.go:171-`）と `wireSession`
  （`agent/sessionx/session.go:78-99`）、**CP の `sessionWire`**（`cp/workspace_handlers.go:545-706`・明示の型付き構造体で、
  無い欄は黙って落ちる＝`:548-551,621-624` のコメント、今も `origin` を運ばず `originSession` だけ）と、停止中の DB ミラー
  （`workspace_handlers.go:738-740`）。**[cp-session-wire-relay-drops-fields] がそのまま当たる**。113 §9（113:614-615）は
  D5 について「当たらない」と書いたが、D8 には当たる。
- CP の新しい口は `cp/routes.go:494-501` に並べるだけでなく、**`cp/testdata/routes.golden`（:267-273）**と Agent の
  `workspace/agent/routes.go:135-144` にも要る。

### 🟡J D13: #904 は取り込み済み。待つべきは「仮説の決着」ではなく「改訂の実装」

- ADR 0094 の改訂（`docs/decisions/0094-instruction-edit-image-models.ja.md:923-990`・`b3c8c73ec`）は**条件付き・未実装**。
  条件付きなのは**縮める先の寸法**（表の最近傍か、`TextEncodeQwenImageEditPlus` の丸めの不動点か——3 仮説
  `:937-951`・仮置き `:964`）で、**「中央クロップをやめ、全体を縮める」はどの仮説でも変わらない**。
  格子説を支持する実測（log 112 §13）は `temp/s477q73`（`cb64d9fa2`）にあり、develop にはまだ無い。
- キャンバスが知るべきは「入力全体が出力全体に写る」ことだけで、縮める先の寸法には依存しない（マスクは入力の座標で
  塗り、Agent が同じ比で縮める）。したがって D13 の「確定前にキャンバスを着工しない」は**強すぎる**。正しい条件は
  「**クロップを外す実装が出荷されてから**」（出荷中の配線はまだ切っている＝今キャンバスを出すと帯の問題に戻る）。
- 113:364 の「`sovai4k` が実測中」は古い。log 111 §9 の三択（`111-inpaint-mask-canvas-p2.md:120-133`）は、改訂の実装が
  出れば 3（帯を見せない）に自然に落ちる。
- 付記: `PREFERRED_KONTEXT_RESOLUTIONS` はコードのどこにも無く（docs だけ）、このファミリーの `sizes` は空
  （`comfy.go:605-609`）。

### 🟡K D14 をリポジトリ内フォルダに切り替えたときの汚れ方

- 変更ファイルは transcript の編集ツール呼び出しから作る（`agent/sessionx/session_files.go:3-7`）ので、
  **`add_image_knowledge`（Agent が書く）はセッションの変更一覧に載らない**。一方 `GET /fs/changes`（`git status`・
  `workspace/agent/fs_git.go:29`）には未追跡／変更として**常に**出る。`.agent-fleet/` を無視する `.gitignore` は無い。
- worktree 無しの既定（D9）だと、その作業コピーで動く**他のセッションの `git status` と commit に知識ファイルが混ざる**。
  ブランチ切替の拒否（`con/i18n/ja/errors.ts:25`）にも巻き込む。
- §7 の却下理由（113:557-560）の「`session-changed-files` の追跡が下書きの往復で埋まる」も、Agent が書く限り事実とは
  違う（埋まるのは `/fs/changes` の方）。結論（生きた下書きをリポジトリに置かない）は変わらない。
- **直す**: リポジトリの書き先は「共有したいときに明示で書き出す」（P1 の `*.imagedraft.json` と同じ扱い）に寄せ、
  毎回の追記はワークスペース側（🔴6 で移した既定の置き場）へ。

### 🟡L D9 の起動ダイアログの部分集合

- `LaunchModal`（`con/features/repos/LaunchModal.tsx:159`・881 行）は一枚岩で、再利用できる部品は `ModelPicker`/
  `EffortPicker`（`ui/ModelPicker.tsx`）・`SubdirPicker`・`BranchList`・`useAttachDraft` まで。`StartModal.tsx` も
  埋め込まずに部品だけ使っている（`:627` のコメント）——同じ形で作るのが筋。
- 部分集合に要る欄（113 の列挙＋🔴1/🔴3 の帰結）: kind（**`managedDrivers` から引く・opencode Managed を除く**）・
  モデル・effort・startMode・リポジトリ・subdir・**worktree（既定 ON）**・skipPermissions。driver は Managed 固定、
  prompt 欄は無し（初回指示は人格）。

---

## 3. 確認済み（指摘なし）

- **D1**（スタジオを別 id に）: 理由（結び替え・resume 不能・枠のための削除で下書きを失わない）は今の木で成り立つ。
  `~/.config/agent-fleet/imagegen/studios/` に Agent が書くこと自体は `paths.go:19-45` の区分どおり（🔴6 は人と
  エージェントに見せる方の話）。
- **D2 の契約を MCP にする判断**と、`generate_image` を広告しないことで「生成は人」を構造で守る判断: 🟡A のとおり
  広告は要求ごとに決まり、呼び出し側も「最後に広告した名前」で断る（`mcp_stdio.go:553,2291`）ので、**広告から外せば
  呼べない**は本当に構造的に成り立つ（識別が届く限り＝🔴3）。`set_chat_plan` の形（`agentGET/agentDo`）の前例もある。
- **D3 の `model` を人だけに**・自動の錠を作らない: 異論なし。
- **D5**（ファミリー表を Agent へ）: `comfyFamilyRow`（`comfy_workflows.go:460-553`）と `comfyFamilyRows`（`:559`）は
  足す欄を受けられる。`GET /imagegen/status` は CP の中継を素通りする（`cp/routes.go:495`）ので欄は落ちない。
- **D8**（`sameTarget` をスタジオ id に・null 同士は同じ的）: `layout/ops.ts:105,138` の分岐 1 つで済む。セッション一覧から
  開く分岐は `con/features/sessions/open.ts:35-41,64-81`。
- **D10**: Managed のセッションに会話ごとの system prompt の口が無いのは事実（ADR 0042 の層は kind ごとに 1 本
  `agent/userinstr/userinstr.go:1-15`、`--append-system-prompt` はアシスタントチャットだけ `chat_providers.go:360,465`）。
  初回指示に置く判断は正しい。🔵 lcpp は毎ターン system prompt を組み直す（`agents/lcpp/driver.go:504`→
  `harness/systemprompt.go:42-59`）ので、lcpp に限れば人格も前置もここへ置け、🟡B の累積を避けられる。
- **D11**: `askAssistant` の他の利用者は無関係（撤去するのは `prompthelp.ts` と `PromptHelpModal.tsx`）。
- **D12**（生成の経路を変えない）: CP の中継 7 行（`cp/routes.go:495-501`）は正しい。ただし 🔴5 の番人は経路への
  **追加**になる——「1 バイトも変えない」の例外として明記する。
- **§7 の却下理由**: アシスタントチャット（§2.3 の事実はすべて正しい）・fenced block・`generate_image` を持たせる・
  スタジオ＝セッション名・`model` を書かせる・版を編集ごとに切る・ジョブ一覧の拡大・専用一覧・毎ターン自動添付・
  MirrorView を書き直す——いずれも今の木で成り立つ。例外は「TUI を P0 に入れない」（🔴1 で前提が崩れる）と
  「リポジトリの下書き」の理由の一部（🟡K）。
- **§2.4**: 変えない、で正しい。撮影スタブの追加（§9）も正しい。

---

## 4. 利用者に問うべき判断

1. **claude でスタジオを使うか（🔴1）**——(a) P0 は Managed 7 kind（codex・opencode 以外推奨）で出し claude は P1、
   (b) TUI を P0 に繰り上げて claude を初日から使う、(c) claude の Managed ドライバを作る（ADR 0015 の見直し）。
   「高度な推論」を claude（opus）で想定しているなら (b) が最短。
2. **スタジオのセッションの cwd（🔴3）**——worktree を既定 ON にしてよいか（cwd の推測を一意にする唯一の手段。
   「資料を見ながら」は worktree でも成り立つが、未コミットの資料は見えない）。あわせて **opencode Managed を
   スタジオの kind から外してよいか**。
3. **知識の既定の置き場（🔴6・113 §8 残り 1 の前提変更）**——`~/.config/agent-fleet` は人も（Files ペイン）エージェントも
   （方針）触れない。拒否リスト外のホーム配下（例 `~/imagegen-knowledge/`）に移すか、ファイルをやめて MCP＋専用 UI に
   するか。
4. **D3 の `inputs` 解放（113 §8 残り 3）**は、投入側に番人を足す（🔴5）まで保留でよいか。
5. **D13（113 §8 残り 4）**の条件を「#904 の仮説の決着」から「クロップを外す実装の出荷」に変えてよいか（🟡J）。
