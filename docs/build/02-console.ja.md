---
audience: "Console（ブラウザ側）を変える人"
source_of_truth: "コード（本書は地図と設計意図）"
updated: "2026-09"
---

# 02. Console（React + Vite + zustand）

[English](02-console.md) | 日本語

## 2.1 スタックと設計原則

React・Vite・TypeScript・zustand の SPA。版は `console/package.json` にあるもの。ビルド成果物は
CP が配信する（§2.7）。Console と CP の会話は次のとおり。

- `api/` 配下の REST
- よく変わる状態を運ぶ SSE のプッシュストリーム `api/events` 1 本（§2.3）
- アシスタントチャットのターンと、モデルが答えるまでリクエストを保持するルートの SSE 応答
  （`core/api/client.ts` の `fetchHeld`）
- WebSocket 3 本: `ws/terminal`（PTY）・`ws/browser`（ブラウザペイン）・`ws/browser-attachments`
  （アタッチした Chromium の表示）

ルート表とワイヤの規則は [05](05-api.ja.md) の持ち物。今の構造は、2026-07 に機能パリティを保った
全面リビルドで God-context 構造を廃したときのもので、経緯は
[decisions/0011](../decisions/0011-console-rebuild.ja.md)。設計原則:

- **ドメイン別ストア + selector 購読**。単一 Context・`bump*()` カウンタ・ref ミラーは持たない（§2.3）。
- **レイアウト演算は純関数**（`console/src/layout/`）。副作用（永続・履歴・xterm）はストアとサービスが
  持つ（§2.4）。
- **feature 単位の凝集**: エンドポイント呼び出し・状態・UI・CSS を `features/<x>/` に同居させる。CSS は
  co-located のプレーン CSS（CSS Modules は使わず、クラス接頭辞の規約で衝突を避ける）。
- **StrictMode 耐性**: アプリは `React.StrictMode` の下で描画する。シェルが起動時に始めるアプリ全体の
  結線（`app/App.tsx` の `wire*()` / `start*()` 呼び出し）は後始末を返し、エフェクトから呼ばれるので、
  二重マウントの後も購読は 1 本だけ残る。wired-once フラグは無い。
- **全 URL は `document.baseURI` 相対**（`core/api/client.ts` の `rel()`、`vite.config.js` の
  `base: "./"`）。絶対パスは禁止: Console は path-strip プロキシの配下で動くことがあり、
  `index.html` が `<base>` を立てて相対 URL をマウント先の下に解決させている。

## 2.2 どこに何があるか（`console/src/`）

| ディレクトリ | 責務 |
|---|---|
| `app/` | `main.tsx`（入口）・シェル（`App.tsx`）・画面最上部のバーとワークスペースバー・作業グループの切替・viewport・スマホのジェスチャ。boot 順はシェルの持ち物: 結線とポーラーが始まり、テナント → UI 設定 → そのテナントのレイアウトの順に進む |
| `core/api/` | `client.ts`: `fetch` をラップする唯一の場所（§2.3）|
| `core/push/` | プッシュチャネル: `events.ts` が受信、`wire.ts` がストリームごとのフレームをストアへ適用 |
| `core/store/` | 基盤ストア: テナント（選択・所属・whoami）・ワークスペース（状態機械 + start/stop）・左レール・統計フィード |
| `core/auth/` | 「ログインセッションが切れた」「このテナントは別のサインイン方式が要る」のラッチ。React の外に置き、React 外のコードからも立てられるようにしている |
| `layout/` | ペインレイアウトの純関数エンジン（`types` / `ops` / `migrate`）・layout ストア・履歴・ポップアウトタブ（§2.4）|
| `terminal/` | xterm の知識を集めた `term.ts` と、そこへの唯一の入口 `service.ts` |
| `agents/` | `registry.ts`: セッション種別ごとに記述子 1 つ（§2.4）|
| `ui/` | プリミティブ。例: Button・Modal・Section・Icon・FileIcon・トーストと確認のプロバイダ・モデルピッカー |
| `features/*` | 機能ごとに 1 ディレクトリ（下記）|
| `lib/` | 純ロジックと小さな hook。例: コミットグラフのレーン・ファイルアイコンとメタデータ・端末の色味・UI 設定の同期（`settings.ts`）・作業グループ。i18n は `lib/i18n/`（§2.8）|
| `styles/` | テーマ変数の唯一の置き場 `tokens.css` と、リセットの `base.css` |
| `types/` | 横断のドメイン型。例: セッション・チャット・メモ |
| `test/` | dom テストのセットアップと、ソース全体に掛ける静的検査（例: 生の制御文字が無い・兄弟要素の key が重複しない）|

**`features/`** は機能ごとに 1 ディレクトリで、コンポーネント・たいてい `store.ts`・多くは `api.ts`・
CSS を持つ。一覧は `ls console/src/features`。製品が画面ごとに何を提供するかは
[guide/ref/features.ja.md](../../guide/ref/features.ja.md) で、どちらもここには繰り返さない。入口を
探す手がかりとして、ディレクトリはいくつかの種類に分かれる。例:

- **ペインが表示するもの**: `scm`・`viewer`・`editor`・`mirror`（セッションのチャットミラー）・
  `browser`・`overview`（セッション一覧、[ADR 0078](../decisions/0078-sessions-overview-pane.ja.md)）・
  `fleetgraph`（フリートのセッショングラフ、[ADR 0096](../decisions/0096-fleet-session-graph.ja.md)）・
  `gallery`（[ADR 0080](../decisions/0080-image-gallery-pane.ja.md)）・`imagegen`（画像生成スタジオ、
  [ADR 0081](../decisions/0081-image-generation-pane.ja.md) と
  [ADR 0100](../decisions/0100-image-generation-studio.ja.md)）。
- **左ペインが表示するもの**: `project`（作業コピーのツリー）・`chat`（アシスタント）・`memo`・
  `workitems`・`schedules`・`sharing`。
- **バー・ダイアログ・横断の仕組み**: `panes`（ペインホスト）・`sessions`・`repos`・`settings`・
  `notifications`・`keys`（キーボード操作体系とコマンドパレット）・`auth`・`engines`（エンジン表示）・
  `usage`・`cost`。

これらに繰り返し出てくる規約が 2 つある。

- **`open.ts` はビューと分ける。** ペインを持つ機能のいくつか — 例えば `overview`・`fleetgraph`・
  `gallery`・`imagegen`・`scm` — は `open*()` を専用の `open.ts` から出す。新しく作るものはこれに
  倣う。キーボードのコマンド表が開く関数を import するので、ビューを import すると描画と CSS が
  メニューを持つ全バンドルに引き込まれる。古い種類は別の場所から開く（`features/viewer/openFile.ts`・
  `features/browser/attachmentAction.ts`）。
- **`api.ts` は `core/api/client.ts` の上に作り、形は `wire.ts` に置いてよい。** `client.ts` は
  import された時点で `localStorage` を読み `window.fetch` を差し替えるので、node のテスト
  プロジェクトでは読み込めない。型だけが要る純モジュールは `wire.ts` を import する。

## 2.3 状態管理とサーバ同期

- ストアはドメイン別に分かれ、**selector 購読が「プッシュのフレームごとに全画面が再レンダーされる」
  ことを構造的に防ぐ**。React の外のコードからは `getState()` / `setState()` で触る。
- ストア同士は購読でつながる。例: シェルはワークスペースの stopped ↔ running の**遷移エッジ**で
  repos・sessions・files・chat を読み直し（`app/App.tsx` の `wireWorkspaceRefresh`）、その間の
  未確定状態は無視する。
- **プッシュが先、ポーリングは保険。** `core/push/events.ts` がタブごとに `api/events` を 1 本持ち、
  フレームを各ハンドラへ渡す。ストリームの種類はそこの `PushStream` 型で、ワイヤ形式は
  [05 §5.1](05-api.ja.md#51-公開面（console-↔-cp）)。同じデータのポーラーは動き続け、`pushHealthy()` が
  真の間だけ番を飛ばすので、ストリームが切れても、ルートを持たない CP でも何も失わない。
  ワークスペースとセッションのポーラーは、自分の取得中に同じストリームのフレームが届いていたら
  自分の結果を捨てる（`pushStamp`）。ただし楽観的な `…` 状態を収めるワークスペースの読み直しは
  必ず反映される。ほかの読み直し（例えば課題・エンジン）にはこの守りが無い。
  (再)接続のたびに whoami とセッション一覧を読み直す。フレームは何かが変わったときにしか
  送られないからである。プッシュストリームに載らないデータはそれぞれの周期でポーリングする
  （例: repos は 60 秒ごと）。
- **ワークスペースの状態**は CP の値（`running`・`starting`・`stopped`・`none`）か、読み取りに失敗した
  ときのクライアント側の `unknown`。末尾の `…` は楽観的な処理中の印で、ボタンもポーラーも busy と
  みなして手を出さない。
- `features/files/sessionRefresh.ts` はセッション一覧を見張り、セッションの**稼働 → 非稼働エッジ**
  （working/compacting または backgroundBusy が外れた＝ターンの終わり）で files の**範囲つき**更新
  `refreshUnder("repos/<作業コピー>")` を撃つ。ツリーはその範囲で画面に出ているディレクトリだけを
  読み直し、「変更」ビューは一覧を消さずに差し替える。これが無いと、エージェントが作った／消した
  ファイルは誰かが更新を押すまで見えない。セッション一覧はどのみち届くので追加の通信は無く、発火は
  作業コピー単位に合流させ、最短の間隔を空ける。**読み直しの失敗（5xx・切断）は握り潰して今の行を
  残すこと**: 失敗を空の一覧として書き戻すと、ターンの終わりごとにツリーが空になる。
- 引き金はイベントが主で、間隔は保険。間合いは `features/files/refreshPolicy.ts` にまとめてある。
  ターン終了のエッジで届かない場面は 2 つあり、それぞれに引き金を持つ: **ターンの途中**は稼働中
  セッションの作業コピーを一定間隔で読み直す（走っているものが無ければタイマーは止まり、タブが
  裏にあるときやワークスペース停止中は撃たない）。**タブ／ウィンドウへの復帰**では画面に出ている
  ぶんを間隔を制限して再検証する（`features/editor/probe.ts` と同じゲート）。後者は、状態を持たない
  セッション（shell・SSM）や Agent Fleet の外での変更を拾う唯一の道でもある。自動の読み直しで
  **増えた行**は数秒だけ強調される（`.fs-new`）。
- **ネットワークへの扉は `core/api/client.ts` だけ。** これが `window.fetch` を差し替えるので、素の
  `fetch` を含む全リクエストが `X-AF-Tenant` ヘッダを運ぶ。WebSocket・新しいタブ・ダウンロードは
  ヘッダを運べないので `?tenant=` を付ける（[05 §5.4](05-api.ja.md#54-横断規約)）。呼び出し側が
  頼ってよい規則:
  - `api()` は **HTTP エラーで reject しない**。`{error: {code, …}}` で resolve する。reject するのは
    ネットワーク障害だけ。`r.error` を確かめないと失敗が成功として通る。
  - `api()` は `304` に前回返したオブジェクトで答えるので、**結果は不変として扱う**。
  - `401` でページは移動しない。ラッチ（`core/auth/authExpired.ts`）が立ち、再ログインのダイアログ
    （`features/auth/AuthExpiredModal.tsx`）が開く。走っている端末はそのまま動く。端末のソケットは
    ラッパを通らないので、切断時に API を 1 回叩いて、原因がログインかどうかを確かめる。
  - エラーコードの利用者向けの文言は `errText()` で、カタログの `err.<code>` キーから引く（§2.8）。
- `lib/attention.ts` はタブが見えている間、実際の操作を最大で 1 分に 1 回 CP へ知らせる。読んでいる
  だけの人がアイドル扱いされてワークスペースを止められないようにするため。

## 2.4 ペイン・レイアウト・端末サービス

- **レイアウトはジオメトリとランタイムを分ける**（`layout/types.ts`、レイアウトの版 3）。**Cell** は
  ジオメトリ: React の key・アクティブ化・番号バッジ・ドロップ先。**View** はランタイムの同一性:
  タブ・xterm とその WebSocket・ブラウザのコントローラ・未保存のエディタ。`View.content` は
  ビューが描くもの（端末・ファイル・scm・セッション一覧・スタジオ…）の判別 union。**`session` は
  content ではなく View に持つ**: ビューが表示するものを切り替えても PTY ソケットとスクロールバックは
  隠れたまま生きていて、端末に戻すと同じセッションが出る。
- **レイアウトのプロファイルは 2 つ**で、端末ローカルの設定で選ぶ: `split` は最大 4 カラム × 1〜2
  セル、各セルにビュー 1 つ。`tabs` は最大 3 カラムで、各セルがタブを持つ（全体で 24 ビュー）。
  プロファイルごとに保存レイアウトを持つので、切り替えてもどちらの配置も失われない。切り替えの
  後、読み込んだレイアウトに View id が無いランタイムは、端末サービスとブラウザのレジストリが
  破棄する。
- **id の契約はハードな不変条件。** 入れ替え・ドロップ分割・タブの移動は View の id も Cell の id も
  保つ。振り直しや複製は禁止。端末を表示するビューでは、新しい View id は新しい xterm のインスタンス
  （とその描画器）を作り、
  **動かしたばかりの端末が白紙で現れる**。`layout/ops.ts` の純関数とそのテストがこれを強制する。
- `layout/ops.ts` は `Layout in → Layout out`。何もしない操作は入力を参照のまま返すので、呼び手は
  `next === cur` で commit を省ける。レイアウトの操作は layout ストアの守られた経路
  `commit()` と `commitAction()` を通る。これらはレイアウトを `history.state` に記録し — 移動なら
  push、アクティブ化・タブの選択・仕切りのドラッグなら置き換え。URL は変えない — ストアが
  hydrate 済みなら永続化を予約する。保存レイアウトやプロファイルの読み込み・ポップアウトの種まき・
  履歴からの復元はレイアウトを直接置く。
- **永続化は利用者・テナント単位、タブ単位。** キーは `layout/migrate.ts` の `LKEY_NEW` が作る。
  タブ自身のレイアウトは `sessionStorage` にあるので、2 つのタブは別々のレイアウトを持てる。
  `localStorage` は最後に書かれたものを持ち、新しいタブの種にする。読み戻すのは信用できない JSON
  なので、`migrate.ts` が content の種類ごとに検証し、**知らない種類は空の端末として読み込む**。
- **content の種類を足す**ときに触るのは、例えば `layout/types.ts` の union、`layout/migrate.ts` の
  検証、`layout/ops.ts` の `sameTarget`（2 度目に開いたとき既存のビューへフォーカスするかを決める）、
  `features/panes/Pane.tsx` の描画の分岐、`features/panes/paneTitle.ts` の題名、それを開く関数。検証を落とすとビューはリロードで消える。
- **代わりに出るタブは最近使った順（MRU）。** タブ列の並びはセルの `views` 配列の順で、新しい
  タブは末尾に足され、ドラッグで並べ替えられる。`lastUsedAt` は追い出す対象と、**表示中のタブが
  抜けたあと何を出すか**を決める。閉じる・移す・切り離すのどれでも、残りのうち最後に見ていたタブを
  選ぶ — ミラーからファイルを開いて閉じれば、ミラーに戻る。同じミリ秒の 2 度の touch が同点に
  ならないよう、スタンプはページセッション内で厳密に単調増加させる。
- **ポップアウト**: ビューは自分専用のブラウザタブへ移せる（`?pane=<nonce>`、`layout/popout.ts`）。
  ポップアウトしたタブは共有の `localStorage` の種を書かない。
- **端末サービスが xterm への唯一の入口**（`terminal/service.ts`）。layout ストアの購読 1 本で、
  レイアウトから抜けたビューの端末を破棄する。`term.ts` は苦労して得たドメイン知識の塊で、慎重に
  変えること: データチャネルのハートビートによるゾンビソケット検出（text フレームは帯域外の制御、
  binary フレームは PTY 出力）、WebGL 描画とコンテキスト喪失からの復旧、フォーカス中の Keyboard
  Lock、選択でコピーするクリップボード統合、ソフトキーボードへの追従。ペインはすべて 1 つのホスト
  （`features/panes/PaneHost.tsx`）の下の平らな絶対配置の子。端末のコンテナには `.xterm` がちょうど
  1 つだけ入っていなければならない（`terminal/paneContainer.dom.test.tsx`）。画面外の端末は WebGL
  コンテキストを返す。ブラウザはタブあたりの生きたコンテキスト数に上限を持つからである。
- ブラウザのレジストリ（`features/browser/controller.ts`、`service.ts` で結線）もビュー id で
  引かれ、ページ・ソケット・キャンバスを持つ。**永続化するのは `{kind, port, path}` だけ**で、
  ページ id は保存しない。隠れたページは 60 秒後に破棄され、再表示・リロード・ワークスペースの
  再起動のときはポートとパスから作り直す。ブラウザペインの使い方は
  [guide/ref/browser-pane.ja.md](../../guide/ref/browser-pane.ja.md)。
- **`agents/registry.ts` はセッション種別ごとに記述子を 1 つ持つ** — エージェントに `shell` と `ssm`
  を加えたもの（`types/session.ts` の `SESSION_KINDS`）。記述子は表示名・利用可否の述語・能力の集合を
  持ち、UI は種別名ではなく能力で分岐する。種別を足すのは記述子から始まる。ほかに種別名が出てくる
  場所には、色（§2.6）と [guide/ref/agents.ja.md](../../guide/ref/agents.ja.md) がある。後者の能力の
  行は `agents/guideTable.test.ts` が記述子と突き合わせる。
- **表示名は 3 つの幅**を持ち、内部の識別子は不変の小文字: 狭いバッジ用の 2 文字の `short`、
  ペインのヘッダとセッション行用の短い `label`、起動カードと設定カード用の正式な `displayName`。
  表示のコードは `lib/sessionkind.ts` のヘルパ（`kindShort`・`kindLabel`・`kindDisplayName`）経由で
  書き、生の label を読んだり名前を直書きしたりしない。

## 2.5 情報設計（IA）

画面そのものはメンバー向けに `guide/member/` が説明している。この節は、変更がはまるべき形である。

- **2 段のバー。** 画面最上部のバーには、例えばアプリ名・テナントピッカー（所属が 1 つなら
  隠れる）・通知センター・エンジン表示・外観のポップオーバー・アカウントメニュー（ガイド・設定・
  テナント設定・管理・サインアウト）がある。その下のワークスペースバーには、状態と起動／停止・
  リソースと使用量のチップ・ポートプレビュー・分割の操作がある。
- **左ペイン**: レイアウトマップ・作業グループの切替、その下に `app/App.tsx` が描く順で常駐の
  セクション（アシスタント・課題・メモキュー・プロジェクトツリーほか）。中心はプロジェクトツリーで、
  project-first の IA: 作業コピーをプロジェクトごとに束ね、その下にセッションとファイルを入れ子に
  する。リポジトリ外のセッション・共有されたセッション・全体のファイルブラウザはその下にある。
- **メイン**: ペインホスト。
- **履歴ナビゲーション**はレイアウトを `history.state` に push し、**URL は変えない**（path-strip
  プロキシのせいで URL のパスは使えない）。戻る／進むでレイアウトとスマホのドロワーが戻る。
  「戻るでモーダルを閉じる」は共有のモーダル層（`ui/Modal` と `lib/backClose.ts`）の持ち物で、
  ドリルダウンはその上に積む。Console が読む URL は入口で、例えば `?session=`（通知のリンク）・
  `?pane=`（ポップアウト）・`?tenant=`（サインインの後）・`?share=`（Web Share Target）・
  `open/browser-attachment/{id}`。
- **スマホの横スワイプは稼働中のセッションを順に回す**（ドロワーが閉じているとき）。選び方の規則は
  `features/sessions/rotate.ts`、ジェスチャは `app/swipeGestures.ts`。順序は返ってきたセッション
  一覧を作業グループで絞ったもので、左ペインに見えているものと一致する。左端から始まるスワイプは
  ドロワーに譲る。自前の横操作を持つ面の上ではスワイプを見送る（`app/swipeGuard.ts`: ブラウザ
  ペイン・入力欄・横スクロール域・`[data-no-swipe]`）。
  - **横スクロール域の判定に `overflow-x` の計算値は使えない**: CSS はもう片方の軸が visible で
    なければ `visible` を `auto` に計算するので、縦だけのスクローラも `auto` と読める。転写の中の
    折り返せない文字列 1 つがミラー全体を横にはみ出させ、そのセッションだけスワイプが効かなく
    なっていた。対処は両側から: 転写は折り返し（`overflow-wrap: anywhere`）、縦に読む面は
    `[data-swipe-y]` を宣言して、そこでの横のはみ出しは定義上事故とする。**判定自体は緩めない
    こと**: コードや diff のビューは本当に両方向へ動く。
- **設定のダイアログは 3 つ**: 個人の設定・テナント設定（テナント管理者のもの）・管理（配備の
  管理者のもの）。どのタブがあってどこにあるかは [guide/ref/settings.ja.md](../../guide/ref/settings.ja.md)。
  変更するときの規則:
  - 個人の設定はグループ分けしたレール（`features/settings/SettingsDialog.tsx` の `GROUPS`）。スマホ
    ではレール → 内容とドリルする。セクションのキーはディープリンクの id（`openSettings(section)`）
    なので、レールを組み替えても保つ。どのセクションがどのグループにあるかは
    `settingsRail.dom.test.tsx` が固定している。
  - 配備にその能力が無いタブは出さない — 例えば AWS の請求が無い配備のクラウド費用、発行されない
    配備のプレビュー用サブドメイン。
  - 管理機能は別のダイアログで、個人の設定には混ぜない。
  - **テナント設定と管理のダイアログは器を共有し、テナント 1 つ分の面は 1 つのコンポーネント**
    （`features/settings/tenant/tenantScope.tsx`）。管理のレールは 2 段で、テナントを開くとレールごと
    そのテナントへ入れ替わる。同じテナントを別の入口から見ているだけなので、IA を二重にしない。
  - ダイアログが何を出し何を隠すかは案内にすぎない。**権限を決めるのはサーバ。**

## 2.6 表示の仕組み

- **テーマ**: 変数の唯一の置き場は `styles/tokens.css`（`:root` がダーク、`[data-theme=light]` が
  上書き）。`lib/settings.ts` の `applyTheme()` が `data-theme` と領域の変数を書き込む。面の色は
  テーマごとに色味を変え、ライトテーマで暗いバーが読めなくならないようにしている。highlight.js は
  `--hl-*` 変数でテーマに追従する。**既知の限界: 端末にはライトテーマが無い** — ライトモードでも
  暗いまま。
- **エージェント種別の色**は `tokens.css` の `--kind-*` から来る（両テーマ分）。CSS は
  `var(--kind-*)` を使い、淡い色は `color-mix(…)` で作る。**CSS ファイルは種別の色の値を繰り返さ
  ない**。`tokens.css` の外にある唯一の写しは `lib/termcolor.ts` で、ダークの値を端末の背景に混ぜて
  いるので、一緒に変える必要がある。新しい種別の色は、既存の色と意味色に対して両テーマで確かめる
  （`console/scripts/kindcolor/`）。
- **アイコンは役割で分ける**: クロームは `currentColor` に従う単色の codicon、ファイル種別は拡張子で
  引くカラー SVG（`lib/fileicons.ts`・`ui/FileIcon.tsx`）。
- **UI 設定は利用者ごとにサーバへ保存する**（`GET/PUT /api/env/ui-prefs`、`lib/settings.ts`）。
  `localStorage` を即時のキャッシュにし、debounce した `PUT` で永続化する。起動時に
  `hydrateUIPrefs()` がサーバの写しを優先してマージする。ただしまだ保存していないローカルの変更は
  残り、次の保存で送られる。読み取りの失敗を「サーバが空」と取り違えることはしない。いくつかの
  キーは**端末ローカル**でブラウザの外へ出ない — 例えばテーマ・面の色・レイアウトのプロファイル・
  読み上げのスイッチ（`DEVICE_LOCAL`）。保存に失敗すると Console がそう表示する（`PrefsSyncBanner`）。
- **スマホは監視と軽い操作のため。** スマホの境目は 760 px（`lib/device.ts` の `MOBILE_QUERY`、CSS の
  メディアクエリでも同じ値）。スマホだけの振る舞いはその分岐の中に留め、デスクトップの DOM と CSS
  には触れない。

## 2.7 ビルド・配信・ハードな制約

- `npm run build` は `vite build`、`npm run dev` は `vite build --watch` で、ブラウザをリロードすれば
  変更が入る — CP の再起動は要らない（[10](10-development.ja.md)）。Mermaid と Marp はヒープを食う
  ので、スクリプトは Node のヒープを全体ではなく**コマンド単位**で上げる。sourcemap は切ってある
  （生成でヒープを溢れさせた前科がある）。
- 重い描画系は**遅延 import のチャンク**でメインバンドルの外にある — 例えば Mermaid・Marp・pdf.js・
  オフィス文書の変換器（WASM）・CodeMirror の言語パック。
- **知っておくべき Marp の罠**: 数式を切っていても MathJax（~43 MB）と KaTeX を*静的に* require する
  ので、手当てをしない本番ビルドは minify でハングする。`vite.config.js` のエイリアスが
  `marp-math-stub.js` に差し替えている。**これは固定の制約で、エイリアスを外すとビルドが死ぬ。**
- **配信。** CP は `CONSOLE_DIR` が指すディレクトリ（開発ではビルド成果物の `console/dist`）を
  `control-plane/routes.go` の `registerStatic` から配る。どのパスがどうキャッシュされるかは
  [05 §5.4](05-api.ja.md#54-横断規約) の持ち物。変更に求められること:
  - **`assets/` の下のファイルは、中身が変わるたびに名前も変わらなければならない。** Vite の
    コンテンツハッシュがそうする。ディレクトリごと複製するものはパスに版を入れる。pdf.js の文字
    マップがそれ（`assets/pdfjs/<版>/`、`afPdfjsAssets` プラグイン）。
  - 配備がタブに届くのは、次にシェルを読み込んだとき。開いたままのタブは、ビルドが書き出し
    `lib/useUpdateCheck.tsx` がポーリングする `version.json` で気づき、リロードを勧める。
  - 配備がブランディングされていると、CP は `index.html` とマニフェストをリクエストごとに書き換える
    （`control-plane/brand.go`）。
  - `public/sw.js` は Web Share Target のためだけにある。アプリのシェルをキャッシュせず、それ以外は
    何も横取りしない。そのままにしておくこと。さもないと上の規則が成り立たなくなる。

## 2.8 文言と i18n

- カタログは `lib/i18n/locales/{ja,en}/<ドメイン>.ts`。日本語が親: キーは日本語カタログの `keyof`
  で、英語の各ファイルは対応する日本語ファイルに対して型付けされるので、キーの欠けも余りも型検査で
  落ちる。既定のロケールは `ja`。詳細と理由は [decisions/0016](../decisions/0016-i18n.ja.md)。
- React のコードは `useT()`、React の外のコードは `t()` を使う。エラーコードは `errors.ts` の
  `err.<code>` キーに対応する。
- 日本語はカタログと利用者に見える文字列に置き、コメントには書かない（`AGENTS.md`）。
  `npm run i18n:lint` は JSX のテキストや文字列リテラルに生の日本語があると落ちる。
  `scripts/i18n-lint-pending.json` に載ったファイルは警告だけの積み残しで、きれいになったファイルは
  そこから外れる。`// i18n-exempt` は訳さない文言の印。

## 2.9 テストと検査

`console/` から実行する（リポジトリの直下からだと誤った結果になる理由は `AGENTS.md`）。

- **vitest のプロジェクトは 2 つ**（`console/vite.config.js`）。既定の `node` は `*.test.ts(x)` を
  走らせる: 純ロジック — レイアウト操作・パーサ・ストア — と、静的マークアップに描いたコンポーネント。
  `dom` は `*.dom.test.tsx` を jsdom で `src/test/domSetup.ts` とともに走らせる。コンポーネントを
  マウントするテスト用。分けているのは、jsdom の立ち上げにテストファイルあたり約 1.3 秒かかるから
  で、全部を jsdom で走らせると 10.8 秒 → 51.3 秒と測られている。共有ホストのメモリのため、
  ワーカーは 2 に制限している。
- **型検査**: `npm run typecheck`（`scripts/typecheck.mjs`）。
- **lint**: `npm run lint` は規則 1 つ `react/rules-of-hooks` だけの oxlint（`.oxlintrc.json`）。
  早期 return の下に置いたフックが Console 全体を真っ黒にしたことがある。
- **CI**（`.github/workflows/ci.yml` の `console` ジョブ）は型検査・lint・i18n lint・vitest・実物の
  headless Chromium での検査 2 つ（`pdf:check`・`doc:check`）・ビルドを走らせる。
- `package.json` のほかのスクリプト（スクリーンショット・ミラーやビューアのスクロール検査・コントラスト
  検査など）も実物の headless ブラウザを動かすが、手で走らせるもので CI には入っていない。
- **エンドツーエンド**: `console-e2e/`（Playwright）。実物のブラウザから CP を通って実物のコンテナまで
  （[10 §10.4](10-development.ja.md#104-テスト)）。
