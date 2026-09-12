# 96. 稼働中セッションを一望するペイン — 検討・実装・実測

> 状態: **P0 / P1① 完了**（2026-09-12）。決定は [ADR 0078](../decisions/0078-sessions-overview-pane.ja.md)。
> P0 は Console だけの変更だったが、**P1①（カードの最後の一言・§96.9）は Agent → CP → Console の
> 3 段**に入っている。本書は「何を調べて何を選び、実機（headless Chromium＋README 用スタブ）で
> 何を測ったか」の記録である。
> 関連: [52-working-sets.md](52-working-sets.md)（作業グループの絞り込み規則）/
> [94-rail-lineage.md](94-rail-lineage.md) §94.10（wire に無いフィールドは CP の中継で落ちる——P1 の「最後の一言」が踏む）/
> [44-operator-interaction-graph.md](44-operator-interaction-graph.md)（別タスクに送られた相関図。本ビューとは別物）

## 96.1 要求

利用者の言葉のまま: 「稼働中のセッションを一望できるビュー。ビュー内にパネルが並ぶ。パネルには
セッションの状況が表示される。左ペインの右クリックメニューと同じ操作が可能。ビューの横幅によって
横に並ぶ数が変わる」。

検討で確認を求めた 4 点と答え: ①置き場所＝**ペイン種別**、②停止セッション＝**既定は稼働中のみ、
トグルで足す**、③カードを開いたら一覧は**残す（隣に開く）**、④列＝**CSS Grid の auto-fill**。

> 🔴 ③は**スマホでは誤り**だった（2026-09-12・§96.7）。電話には「隣」が無く、
> 常に `split=true` で開くと半分の高さのペインが 2 枚できる。

## 96.2 調べて分かったこと（実装前）

- **メニューは既に部品**。`features/sessions/SessionMenu.tsx` の冒頭に「セッションが出る場所すべてで
  同じ項目を出すために行から切り出した」とあり、呼び手が持つのは開閉状態と配置だけ。タブ付き
  グリッドのタブ右クリック（`Pane.tsx`）が 2 つ目の呼び手で、カードは 3 つ目。項目は 17 種。
- **状態導出も共有済み**。`lib/sessionview.ts` の `stateInfo()` は左ペインの行とペインヘッダの共通部品。
  クラス値は `on / off / off dead / off warn / off question / working / bg / question / question limited /
  on|off handoff` の 10 通り。
- **ペイン種別の追加は 8 か所**（union・`migrate.ts` の検証・`sameTarget`・`Pane.tsx` の switch・
  `paneTitle`・`LayoutMap` の略号・`canPopout`・i18n）。共有セッション種別（`5e47b714`）が同じ手順の前例。
- **DTO に無いもの**: 「最後の発話」「最終更新時刻」。`Session` には `updatedAt` が無い。
- **「フリート俯瞰図」は先約がある**が別物。ADR 0027 が「全フリート俯瞰を同じ図で兼ねる」を却下して
  別タスクに送り、ADR 0041 決定 9 が「相関図が必要になる」と確定した。それは会話×セッション×
  メッセージの**相関図**であり、本ビューはカードの**一覧**。ADR 0078 の却下案に明記した。
- **ADR 0077 は別レーンが取っていた**（PR #566・EC2 Fleet）。本 ADR は 0078。
  [[parallel-lanes-migration-version-collision]] と同じ罠が ADR 番号にもある——`gh pr list` で
  開いている PR の `docs/decisions/` を見てから番号を取る。

## 96.3 実装で踏んだこと

1. **レイアウト図はペインが 1 つだと消える**（`LayoutMap.tsx:47` `paneCount(layout) <= 1 → null`）。
   最初は図の見出し行にだけボタンを置いたが、単一ペインの画面（＝一覧を開きたい場面）で入口が
   消えるのをスクリーンショットで見て、操作バー（右に分割／下に分割／全て閉じる の並び）に足した。
2. **`features/sessions/open.ts` を keys の command 表から import すると node の試験が落ちる**
   （`ReferenceError: self is not defined`——`terminal/service.ts` 経由で `@xterm/addon-fit` が
   module 評価時に `self` を触る）。opener は `features/overview/open.ts` に分け、`layout/store` だけを
   import する。
3. **トグルの状態は React state に置けない**。タブ表示ではタブ切替でビューが unmount される
   （[[tabbed-pane-shared-container]] の「1 セル 1 コンポーネント」の裏返し）。`showStopped` を
   `PaneContent` に持たせ、`setPaneTarget` で書く。スタブ実機で `localStorage` の `af.layout2.*` に
   `{"kind":"sessions","showStopped":true}` が永続することを確認した（§96.5）。
4. **`@container paneview` はビューの根に自分で宣言しないと効かない**。コンテナを宣言しているのは
   ミラーの shell と端末の根であって Pane ではない。`.ovw` に `container: paneview / inline-size`。
5. **カードは `<button>` にできない**（中に ⋯ と序数バッジのボタンが入る＝入れ子禁止）。
   `<article role="button" tabIndex=0>` にして Enter／Space／Menu キー／Shift+F10 を自前で受ける。
   Menu キーの判定は `features/project/contextMenuKey.ts` を借りる。
6. **待ち時刻の台帳は借りない**。`waiting.ts` は「パレットの並び以外に使うな」と明記されている。
   `sortSessionsByAttention(list, () => 0)` で段だけ借り、段内は createdAt 降順（＝API の順）に固定。
   純関数試験で「同じ入力なら順序が変わらない」を陽性に取っている。

## 96.4 試験

- 純関数 `features/overview/overview.test.ts`（node）: 稼働のみ／停止込み、作業グループ（直接所属と
  repo 継承）、質問中が先頭、段内の安定、稼働数。
- DOM `features/overview/SessionCard.dom.test.tsx`（jsdom）: クリック・Enter・中クリックのすべてが
  `openSessionFromList(s, **true**, running)` を呼ぶ（隣に開く契約）／フォルダ消失＋転写無しは開かない／
  右クリック・⋯・Shift+F10 の 3 経路で `.ui-menu` に停止・改名・ID コピーが出る。
- 既存: `layout/*.test.ts`・`features/panes/*.test.ts`・`features/keys/commands.test.ts` 緑（後者は
  §96.3-2 の修正前は赤）。`typecheck`（TS7）は陽性対照つきで緑、`i18n:lint` 緑。

## 96.5 見た目の実測（headless Chromium × README 用スタブ）

`console/scripts/shots/server.mjs`（実バンドル＋fixture の API）を動的ポートで立て、Chromium は
`--remote-debugging-port=0` と `user-data-dir/DevToolsActivePort` で拾う（固定 9223 は共有コンテナで
他セッションに刺さる）。`--blink-settings=primaryHoverType=2,…` で hover を有効化。

| 場面 | 幅 | 実測 |
|---|---|---|
| 一覧だけ（1500×900） | ペイン約 1140px | カード 5・**4 列**・見出し「セッション一覧 稼働中 5」 |
| 側の列に置く（colRatios 0.72/0.28） | ペイン約 320px | カード 5・**1 列**・トグルはアイコンのみ（`@container`） |
| 右クリック | — | `.ui-menu` に 11 項目（copilot・alive）。行のメニューと同一の文言 |
| 「停止中も表示」 | — | カード 6・`.stopped` 1・`af.layout2.*` に `showStopped:true` が永続 |
| カードをクリック | — | `.pane` 2・一覧は残る・新ペインは `activeCellId` になり、カードと左ペインの行に序数 **2** |
| en / light | — | 質問カードの暖色枠・「5 running」・「Show stopped」が読める |

画像は `/tmp/ovw-shots/` に出したが成果物としては保存しない（README の 6 枚とは別・fixture は架空）。

## 96.6 P1 に送ったもの

- **「最後の一言」**: Agent が DTO に 1 行要約を足す。CP の `sessionWire` に無いフィールドは黙って
  落ちる（§94.10）ので、足すのは 4 か所。
- 文字列での絞り込み（左ペインの検索欄と同じ照合）。
- 停止カードの再開ボタン直置き（今は右クリック→再開）。
- 系譜でカードを束ねる（P2）。相関図は本ビューの外。

## 96.7 🔴 訂正（2026-09-12）——スマホには「隣」が無い

利用者の指摘: 「スマホでは、クリック、タップは別ペインではなく、普通に同じペインで開く。
＋Ctrl で別ペインで開く。戻るボタンで戻る」。ADR 0078 決定 3 を改訂した（本文は残す）。

- **何が起きるか。** `layout/ops.ts` の `openInNew` は `mobile` のとき列を上下 2 段に割る
  （`cells.length < 2` なら 2 段目を作る）。`split` を常に true にしていたので、電話では
  タップのたびに**半分の高さのペインが 2 枚**になる。カードのタップは「読みに行く」操作で、
  半分の高さで読みたい人はいない。
- **改めた規則**: 素のクリックは「隣に余地があれば隣・無ければこのペイン」（境目は
  `lib/device.ts` の `MOBILE_QUERY` ＝ `max-width: 760px`。`openInNew` 自身が使っている
  breakpoint と同じものを見る）。Ctrl／⌘／中クリックは幅によらず別ペイン。タブ表示は
  `openInTab` が `split` を潰すので分岐しない。戻るは既存の履歴機構（commit ごとの
  `pushState`）がそのまま担う。
- 🔥 **なぜ実測で気づかなかったか。** §96.5 の「ペイン約 320px」は**窓 1500px の中の細い列**で
  あって電話ではない。一覧の折り畳みは `@container`（ペインの幅）、開き方の分岐は `@media`
  相当の `matchMedia`（**窓の幅**）——**同じ「320px」でも見ている量が違う**。コンテナクエリで
  作った画面を「狭い側の列」だけで測ると、窓幅に依存する分岐は 1 つも動かない。
  **狭さの検証は、細い列と小さい窓の 2 通りで測る。**

### 実測（headless Chromium × README 用スタブ・実バンドル・2026-09-12）

`Emulation.setDeviceMetricsOverride` で**ビューポートごと**変え（390×844 / 1400×900）、
カードは `Input.dispatchMouseEvent`（Ctrl は `modifiers: 2`）で突く。11 項目すべて OK。

| 場面 | 操作 | 実測 |
|---|---|---|
| 電話 390px・分割 | 起動 | `.pane` 1・`.ovw` 1・カード 5・`matchMedia(max-width:760px)` true |
| 〃 | 素のタップ | **`.pane` 1 のまま**・`.ovw` 0（セッションがそのペインを取った） |
| 〃 | 戻る | `.ovw` 1・カード 5（トグルごと復元） |
| 〃 | Ctrl＋タップ | `.pane` 2 |
| 卓上 1400px・分割 | 素のクリック | `.pane` 2・`.ovw` 1（**隣に開く契約は不変**） |
| 電話 390px・タブ | 素のタップ | `.pane` 1・`.pane-tab` **2**・`.ovw` 0／戻ると `.ovw` 1 |

- **陽性対照**: `split` を常に true に戻して再ビルドすると、落ちるのは電話の 2 行だけ
  （`.pane` 2・`.ovw` 残る）。他の 9 項目は緑のまま＝「タップが当たっていないから 1 のまま」
  ではないことも同時に示している。
- 🔥 **ハーネスの罠**: `loadStoredLayout` は **sessionStorage をタブ固有の layout として
  localStorage より優先する**（`migrate.ts`）。同じタブを使い回して場面を切り替えると、
  2 つ目の場面が 1 つ目の後始末（＝直前に開いたペイン 2 枚）で起動し、「卓上は 1 ペインで
  始まる」という前提だけが静かに崩れる。**場面ごとに `sessionStorage.clear()`**
  （`capture.mjs` は場面ごとに新しいターゲットを開くので踏まない）。

## 96.8 第 2 版（2026-09-12）——プロジェクト見出し・家系の並び・カードの形

利用者の要求（原文）: 「プロジェクト毎にグルーピングしたい。親子関係のあるセッションは
親→子の順番に並べたい（停止中も）」「ブランチの右に、親との差分バッジを表示したい」
「進行中などのバッジは、パネルの右上に表示したい」「開始時間に加えて、最後に入力待ちや
AUQ になってからの時間を表示したい」。決定は ADR 0078 決定 9〜11（決定 6 は改訂）。

確認した 4 点と答え: ①グループ単位＝**リポジトリ（remote）**、②並び＝**家系単位で段を決める**
（停止は既存のトグルで足りる）、③待ち時刻＝**まず台帳・不足なら P1 で DTO**、
④カード＝**見出し行の右端にチップ**。

### 96.8.1 実装で踏んだこと

1. 🔥 **`session.remoteUrl` は clone URL ではない。** Console の型には「clone URL（repo を持つ
   agent セッション）」と書いてあったが、Agent の実体は
   `agents/claude/claude.go: li.RemoteURL = RemoteSessionURL(sid)` ＝ **claude.ai の Remote
   Control URL**（RC を橋渡ししているときだけ入る）。「リポジトリでグループ化する」を
   セッション側の 1 フィールドで済ませる案はここで消えた。コメントを実態に直した。
2. 🔥 **作業コピー側にも「どのリポジトリか」は無かった。** `gitx.Repo.Remote` は
   「origin の**ホスト**・path も token も載せない」と明記されており、`github.com` しか
   分からない。そこで **`RemotePath`（`owner/name`）を足した**——決定 9。
   `GET /api/repos` は CP を素通しするので中継の追加はゼロで、Console にそのまま届く。
   資格情報は落とす（`SSHToHTTPS` が scp 形式の `git@` を、残る `user:pass@` はホストと一緒に）。
3. 🔥 **一覧が `repos` を自分で読んでいなかった**（実バンドルの実測で発覚。単体試験は
   store を直接積むので永久に気づかない）。レールが読んでくれる前提だったが、
   **ポップアウトしたタブにレールは無く**、起動直後はまだ空。その間グループは
   フォルダ名に落ちて `webshop` / `webshop@checkout-validation` / `webshop-review` が
   3 つの見出しに割れる。`useRetryLoad` でこのビュー自身が読む（停止中は store を
   空にしない——それはレールの仕事）。
4. 🔥 **メニュー項目のクリックがカードのクリックになっていた**（利用者報告:
   「"停止する" を選ぶと、モーダルが出る裏で別タブが表示される」）。React のイベントは
   **ポータルを越えて“コンポーネント木”を伝播する**ので、`SessionMenu` がカードの子である
   限り、項目の click はカードの `onClick` に届く。レールの行が踏まなかったのは、行では
   メニューがクリック対象 `button` の**兄弟**だから。カードは全体がクリック対象なので
   `ovw-menu-host` で明示的に境界を張る。
5. 狭いカードでは**穏やかな状態の語を畳む**（`@container ovwcard (max-width: 320px)`）。
   チップを見出し行に移すとタイトルの幅が減り、4 列だと「親: 移行の下…」まで縮んだ。
   人を待っている状態（`.question`）だけは語を残す——探しに来るのはそのカードだから。
   コンテナは**カード自身**に宣言する（ペインではなく、グリッドが配った幅で決まる）。

### 96.8.2 実測（headless Chromium × README 用スタブの写し・実バンドル・15 項目 OK）

スタブの fixtures に「同じリポジトリの 2 つ目の clone（`webshop-review`）」「親→子（子は質問中）
→停止した子」「`integration` 付きの worktree」を足して駆動した（`remotePath` は**本体の
fixtures にも足した**——スタブが古い形を返すとハーネスは黙って嘘をつく）。

| 見るもの | 実測 |
|---|---|
| 見出し | `payments-api` / `platform-infra` / `webshop` の 3 つ・名前順 |
| リポジトリ同一性 | `webshop` の見出しに **base・worktree・2 つ目の clone** が入る（tooltip は `github.com/acme/webshop`） |
| 家系 | 親のカードの**次が子**・質問を抱えた家系が見出しの先頭 |
| 停止中も表示 | 停止した子が**同じ親の下・兄の次**に入る |
| 状態チップ | 全カードで `.ovw-head` の中・空のバッジ行は 0 枚 |
| 親との差分 | worktree のカードに「分岐 3↕2・FF不可」（レールの行と同一文言） |
| 待ち経過 | 台帳に 12 分前を積んだカードが「待ち 12m」・台帳が知らないカードは**空** |
| 「停止する」 | ペインは 1 のまま（増えない）＋確認ダイアログが出る＝利用者報告の再発防止 |

- **陽性対照**: 境界（`ovw-menu-host` の `stopPropagation`）を外すと、DOM 試験
  「menu の項目を選んでもセッションを開かない」だけが落ちる。

## 96.9 P1①「カードに最後の一言」——Agent → CP → Console の 3 段（2026-09-12）

§96.6 で P1 に送った 3 案のうち、利用者が選んだのは **①「エージェントの最後の発話の冒頭 N 文字」**
（②直近のツール実行・③待ち中の質問文は見送り）。決定は [ADR 0078 決定 12](../decisions/0078-sessions-overview-pane.ja.md)。
この節は「何を測ってそう言えるのか」である。

### 96.9.1 なぜ 3 段なのか（前提の再確認）

`Session` DTO に「最後の発話」も「最終更新時刻」も無いのは §96.2 で確認済みで、今回も変わって
いない。したがって出所を作るところから要る。当てた場所:

| 段 | ファイル | 何を |
|---|---|---|
| Agent | `internal/agents/claude/lastsay.go`（新規） | `LastSay(sid)`・`spokenText`・`lastSayLine`・`tailLines` |
| 〃 | `internal/agents/claude/transcript.go` | `windowLines` を切り出し（`tailThenWhole` と共用・挙動不変） |
| 〃 | `internal/agents/agents.go` / `agents/claude/claude.go` | `LiveInfo.LastSay` と `WireLive` での充填 |
| 〃 | `internal/session/session.go` / `internal/sessionx/session.go` | `Session.LastSay` と `wireSession` の写し |
| 〃 | `testdata/wire.golden` | 再生成（`-update-wire-golden`） |
| CP | `workspace_handlers.go` の `sessionWire` | `LastSay`（**中継に無いフィールドは黙って落ちる**） |
| 〃 | `contract_session_test.go` | `sessionWireBinding` と `tsKeys` の両方 |
| 〃 | `session_wire_test.go` / `testdata/wire.golden` | 往復の期待値と golden |
| Console | `types/session.ts` / `overview/SessionCard.tsx` / `overview.css` / i18n ja・en | 型・1 行・`.ovw-say`・tooltip |

### 96.9.2 🔥 性能が設計の中心——`lastLineWhere` は**広げる**（実測）

「末尾ウィンドウだけ読む」という前提で `lastLineWhere` を使うつもりだったが、その下の
`tailThenWhole` は **2 つの窓を先に両方読んでから返す**（`for _, w := range windows` の中で
`io.ReadAll` が 2 回）。つまり 512 KiB を超える転写では、呼ぶたびに**毎回ファイル全体も読む**。
コメントの「窓に無いときだけ広げる」は遅延ではない。

- **実測**: 陽性対照の 1 つとして `lastSpokenInTail` を `lastLineWhere` に差し替えると、
  「窓の外の発話は見つからない」を主張する試験が**落ちる**＝全体まで読んで見つけている。
- したがって `LastSay` は**自分の 1 窓だけを読む**（`windowLines` を `transcriptTailWindow` で
  1 回）。切り出しは共用のためで、`tailThenWhole` の挙動は変えていない（読めなかったときに
  `out` を返して打ち切る分岐も含めて同じ）。
- **`tailThenWhole` 自体は直していない。** 触るのは abort 判定と鮮度判定（どちらもレコンサイラの
  tick で回る）で、この作業の範囲外である。**既知の costs として残す**——[[codex-rollout-fullparse-per-poll]]
  と同じ形の負担が、512 KiB 超の転写を持つセッションぶんだけ既に存在する。
- 2 段目の安全装置が **mtime のメモ**（`ctxCache` と同じ作り）。変化の無い転写は stat 1 回。
- 3 段目が「窓に発話が無ければ**直前の行を保つ**」。ここを `""` にすると、大きなツール結果で窓が
  埋まった瞬間にカードが黙る。

### 96.9.3 試験と陽性対照（全部 exit 1 を取った）

Go は `-count=1` 必須（[[go-test-cache-defeats-positive-control]]）。変異の戻しは編集で行った
（[[mutation-revert-not-git-checkout]]）。

| 壊した箇所 | 壊し方 | 落ちた試験 | exit |
|---|---|---|---|
| `spokenText` の 3 つの除外 | `\|\| ev.IsMeta \|\| ev.IsSidechain \|\| ev.IsAPIError` を削る | sidechain / API エラー / meta の 3 件 | 1 |
| 窓に無いときの保持 | `say := prev.text` → `say := ""` | 「窓に発話が無いとき既知の行を保つ」 | 1 |
| 1 窓しか読まない規則 | `lastLineWhere` に差し替え | 「窓の外へは広げない」 | 1 |
| mtime のメモ | `if cached && …` を `if false && …` に | 「変化の無い転写は読み直さない」 | 1 |
| CP の中継 | `sessionWire.LastSay` を 1 行削る | `TestContractFamilies/sessionWire`（両方向を名指し）・`TestAgentSessionsRelayKeepsFields`（`relayed lastSay = <nil>`）・`TestWireShapeGolden` の **3 本が別々の理由で** | 1 |
| カードの行 | `{s.lastSay && (…)}` の条件を外す | DOM「発話が無いカードには行を描かない」 | 1 |

全量: `(cd workspace/agent && go test ./... -count=1 -p 2)` exit 0 /
`(cd control-plane && go test ./... -count=1 -p 2)` exit 0 /
`cd console && npm test` → **257 files・2524 passed**（`src/features/viewer/` の既知の赤は
この worktree では出ていない）・`typecheck` / `i18n:lint` / `css:dup` すべて exit 0。
**exit code はパイプを通さずに取った。**

- ⚠️ CP の golden の失敗文は **`-update-routes-golden` を名乗る**が、正しいのは
  `-update-wire-golden` である（`assertGoldenLines` が routes 用の文面を共用しているため。
  `wiremap_golden_test.go:66` が同じ穴を既に書いている）。1 往復ぶん無駄にした。

### 96.9.4 実測（headless Chromium × README 用スタブの写し・実バンドル）

スタブは `/tmp` に写して `DIST` を絶対パスに差し替え、`/api/sessions` に claude の 4 行
（長い一言・**120 文字ちょうど＋…**・まだ何も言っていない・停止中）を足して駆動した。
**本体の fixtures にも `lastSay` を足してある**——スタブが古い形を返すとハーネスは黙って嘘をつく
（§96.8.2 と同じ理由。README の 6 枚に一覧の場面は無いので画像は変わらない）。

狭さは**細い列と小さい窓の 2 通り**で測った（§96.7 の区別）。判定は目視ではなく
`getBoundingClientRect()` と `getComputedStyle`。

| 場面 | ペイン幅 | 列 | 一言のあるカード | 高さ（無→有） | 1 行か | 省略記号 |
|---|---|---|---|---|---|---|
| 広い 1 ペイン（1500×900） | 1146px | 4 | 4 / 10 | 97 → **118px** | 15px（＝1 行） | 4 枚とも有効 |
| 細い側の列（0.72/0.28・窓 1500px） | 318px | 1 | 4 / 10 | 97 → 118px | 同 | 同 |
| 電話（390×844・`mobile:true`） | 376px | 1 | 4 / 10 | 97 → 118px | 同 | 同 |

- **`matchMedia("(max-width: 760px)")` は電話の場面でだけ true**＝2 通りが本当に別のものを
  測っていることの確認（§96.7 の再発防止）。
- **伸びるのは 1 枚ではなく段**。グリッドの段は最も高いカードに揃うので、4 列のうち 1 枚が
  喋ると**その段の 4 枚が 118px** になる。広い画面の実測でも、一言を持たないカードが
  同じ段の高さに揃っていた。
- **まだ何も言っていないカードは 97px のまま**＝行を予約していない（`squiet3`）。
- **停止したカードにも出る**（`sstop04`）。
- 文字数は 42 / 48 / 52 / **121**（120 文字＋`…`）で、**どの幅でも 1 行に収まらず省略される**
  ——すなわち 120 という上限は「カードが読む量」ではなく「運ぶ量」を決めている（決定 12）。
- 画像は `/tmp/ovwsay/` に出したが成果物としては保存しない（fixture は架空）。

### 96.9.5 「CP を通って届く」と言える根拠

実 CP を起こす代わりに、**CP の中継そのものを回す試験**で取った——
`TestAgentSessionsRelayKeepsFields` は Agent 形の JSON を `agentSessions` に decode させ、
再 emit した JSON を key ごとに突き合わせる。§94.10 で `originSession` の欠落を**唯一**
捕まえたのがこの試験であり、上の陽性対照で `lastSay` についても同じ失敗（`relayed lastSay =
<nil>`）を出すことを確認してある。スタブ CP（§96.9.4）は Console の描画を測る道具であって
中継の証拠にはならない——**その 2 つを混ぜないこと**。
