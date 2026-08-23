# 29. Console キーボード操作体系 — 設計と実装マップ

**Status: ✅ P0〜P5 実装済み（残＝実フリート再ビルド後の実機目視のみ）** — 2026-07 実装

Console 全体をキーボードだけで直感的に操作できるようにする体系。中心にある **xterm がフォーカス中
ほぼ全キーを PTY へ飲む**という制約を、単一の capture-phase ディスパッチャで貫き、Leader＋コマンド
パレット＋少数アクセラレータのハイブリッドで操作系を構成する。意思決定は
[decisions/0017-keyboard-system.md](decisions/0017-keyboard-system.md)。当初の視覚設計は
`console/docs/history/keyboard-system-design.html`（standalone HTML）。

> 本書は実装済み体系のリファレンス（利用者向けの操作一覧＋開発者向けの実装マップ）。各コミットで
> tsc クリーン／vitest 緑／vite build 成功／headless 検証済み。DOM の実機目視は実フリート再ビルド後。

---

## 0. 要旨（何ができるか）

- **Leader = `Ctrl/⌘+K`**。押すと少し遅れて **which-key**（次に押せるキーの一覧）が出る。`p`=ペイン、
  `s`=セッション、`w`=ワークスペース、`,`=設定、`?`=ショートカット一覧、`;`=コマンドパレット。
- **コマンドパレット = `Ctrl/⌘+P`**。コマンドとセッションを曖昧検索して実行/開く。
- **直接アクセラレータ**（`Alt` 修飾）: `Alt+1..8`=ペイン番号へ、`Alt+[ ]`=前後ペイン、
  `Alt+PageUp/PageDown`=前後タブ、`Alt+W`=タブ/ペインを閉じる、`Alt+N`=はじめる、`F6/Shift+F6`=
  領域巡回（レール/メイン/バー）、`Ctrl/⌘+B`=左レール開閉。**一覧は §5.7**。
- **`?`** でショートカット一覧（チートシート）。**モーダル/メニュー/レール**は Tab/矢印で辿れる。
- **【P5】設定 →「キー操作」タブ**で、直接キーと 3 つのアプリ全体キー（Leader/パレット/一覧）を**再割当**でき、
  **端末入力優先**を切り替えられる。

---

## 1. 核心の制約と機構

xterm はフォーカス中 `Ctrl+*`・F1–F10 を軒並み `preventDefault` で握り PTY へ渡す
（`terminal/term.ts` の `attachCustomKeyEventHandler`。素通しの carve-out は Ctrl+C/V とズームのみ）。
アプリのショートカットを端末フォーカス中でも効かせる唯一の抜け道が **window の capture フェーズ**。

- **単一 capture-phase ディスパッチャ**（`features/keys/dispatcher.ts`）が `window` の keydown を
  capture で 1 本購読。xterm ハンドラも React onKeyDown も window の子孫なので、**登録キーだけ**
  `preventDefault + stopPropagation` で先取りし、未登録キーは素通し（シェル無傷）。
- **Escape は capture で握らない**。overlay 閉じは escLayer が bubble で担うので、capture で Escape を
  止めると全モーダルの Esc が壊れる。例外はリーダー保留中のみ。overlay が開いている間は
  `escLayer.hasOpenOverlay()` でディスパッチャ自体を不活性化する。
- **IME 変換中・auto-repeat は非発火**（`chords.ts` の `shouldIgnore`）。

## 2. レイヤリング（重要）

| 層 | 場所 | 原則 |
|----|------|------|
| 純ロジック | `lib/keys/chords.ts`, `lib/keys/registry.ts` | store も DOM も import しない。vitest 対象 |
| ストア結合 | `features/keys/*` | dispatcher / commands / store / bindings / overlays |
| キーキャップ表示 | `ui/Kbd.tsx` | chord 文字列 → 各 OS のキーキャップ（⌘/⌥ or Ctrl/Alt） |

- **キー正規化は `KeyboardEvent.code` 基準**（`chords.ts`）。`.key` は Mac の ⌥/Shift で化けるため不使用
  （⌥+1→"¡"、Shift+k→"K" でも code は Digit1／KeyK）。修飾は `mod`(Ctrl≡⌘)/`alt`/`shift` を固定順で
  出力し、文字列等価で比較できる正規形（例 `mod+k`、`alt+1`、`shift+f6`）にする。Shift は独立修飾＝
  `k` と `shift+k` は別（ペイン移動の hjkl/HJKL）。
- **コマンド DATA は 1 か所**（`features/keys/commands.ts` の `ALL_COMMANDS`／`GROUPS`）。ディスパッチャ・
  which-key・パレット・チートシート・ボタンヒントが全部そこから読む＝表示と挙動がドリフトしない。
  `registry.ts` は型（`Command`/`Group`/`KeyContext`）と純粋な照合関数（`matchDirect`／`resolveLeader`／
  `isLeaderPrefix`／`leaderChildren`／`paletteCommands`／`applyOverrides`）を `commands` を引数で受けて提供。

## 3. コマンドの構造

```ts
interface Command {
  id: string;              // 安定 id（再割当の上書きキー）
  title: string;           // which-key / パレット / 一覧の表示名
  keys?: string[];         // 直接アクセラレータ（例 ["alt+1"]）
  seq?: string;            // Leader 後のシーケンス（例 "p r" = Leader→p→r）
  when?: (ctx) => boolean; // このゲートが true のときだけ照合/表示
  run: (ctx) => void;
}
```

`when` はペインの有無等で照合を絞る（例: `Alt+N` はペイン N が存在するときだけ握り、無ければ端末へ流す）。

## 4. フェーズ別の実装（P0〜P5）

- **P0 基盤**: ディスパッチャ＋レジストリ＋`Kbd`＋focus-ring トークン。`wireKeys()` を App boot で 1 回配線
  （cleanup 返却＝StrictMode 安全）。
- **P1 領域/ペイン移動**: `Alt+1..8`・`Alt+[ ]`・`F6/Shift+F6`。ペイン移動は `layout/nav.ts`（純関数、
  `badges.ts` の序数/col-row）＋`.pane.active`＋`data-pane-id`＋可視フォーカス。
- **P2 Leader＋パレット**: `Ctrl/⌘+K`→which-key（遅延表示でチラつき防止）、`Ctrl/⌘+P`→曖昧検索パレット
  （コマンド＋セッション）。
- **P3 フォーカス誘導**: モーダルの focus-trap（`lib/focusTrap.ts`・role=dialog/aria-modal）、コンテキスト
  メニューとレール tree の矢印 roving（`lib/useMenuRoving.ts`／`features/project/useRailRoving.ts`・
  role=menu/tree）。
- **P4 発見性**: `?` チートシート（`CheatSheet.tsx`・レジストリから生成・検索可）＋主要ボタン title への
  inline ヒント（`keyHint.ts`）。
- **P5 カスタマイズ**: 下記 §5。

## 5. 【P5】再割当と端末入力優先

### 5.1 再割当できる範囲（意思決定）

**直接アクセラレータ**（`keys` を持つコマンド）と **3 つのアプリ全体キー**（Leader＝`app.leader`／
パレット＝`app.palette`／チートシート＝`app.cheatsheet`）**だけ**を再割当可能にする。リーダー配下の
シーケンス（`p r` 等）は木構造ナビゲーションであり、任意再割当はグループ木の衝突管理を過大にし操作不能を
招くので**固定**（ADR-0017 決定 6）。

### 5.2 解決層（`features/keys/bindings.ts`）

- 上書きは `Settings.keybindings`（`id → chord`、`""`＝明示的に無効化、未登録＝既定）に保存。
- `effectiveCommands()` … `applyOverrides(ALL_COMMANDS, overrides())` で `keys` に上書きを被せた
  `Command[]` を返す（純関数 `applyOverrides` は `lib/keys/registry.ts`／vitest 対象）。既存 consumer は
  `Command[]` を受けるだけで無改修。
- `boundChord(id)` … 予約キー（`app.*`）の実効 chord を返す（`""`＝無効化＝不一致）。
- ディスパッチャは毎 keydown で `boundChord`／`effectiveCommands` を**ライブ読み**＝再割当が即時反映。
  React の overlay は `useEffectiveCommands()`（設定 store を購読）で再レンダー。
- 永続化は既存の ui-prefs（localStorage＋サーバ）に相乗り＝**クロスデバイス同期**。`theme` 等の
  `DEVICE_LOCAL` には**含めない**（キー配列は環境依存でなく作業様式の好み）。

### 5.3 端末入力優先（`Settings.terminalPriority`・既定 OFF）

ON のとき、端末フォーカス中は全アプリショートカットを xterm へ素通しし、**Leader だけ**を生かす
（tmux prefix 方式）。Leader から which-key／パレット（`;`）で全操作に到達できる。Leader 自体も
再割当可なので、Leader を無効化すれば「完全に純粋な端末」も選べる。1 キーだけ残すのは端末に閉じ込め
られないための**脱出保証**（ADR-0017 決定 7）。実行中のリーダーシーケンスは常に完了する（脱出中に
中断されない）。

### 5.4 設定 UI（`features/settings/KeysTab.tsx`・「キー操作」タブ）

- 端末入力優先トグル＋再割当行（グループ別）。各行: 現在の chord（`Kbd`）／「変更」（キー記録）／
  「解除」（`""`）／「既定」（上書き削除）。`bindingConflicts()` で同一 chord の重複を各行に警告。
- **キー記録**: 設定モーダルは overlay ゆえディスパッチャが不活性。記録用の capture-phase リスナが
  安全にキーを独占し、`Ctrl+P`（印刷）等だけ `preventDefault` で抑止。Esc で取消。

### 5.5 i18n（日英）とパレットの日英マッチ

コマンド/グループ名・各 overlay の文言は [0016-i18n](decisions/0016-i18n.md) の `lib/i18n` へ集約済み。
`Command.title` は i18n メッセージキーで、表示は `cmdLabel()`（現ロケール）で解決する（`features/keys/labels.ts`）。
**コマンドパレットの絞り込みは `cmdSearch()`＝全ロケール文言（ja＋en…）に対して曖昧マッチ**するので、
UI 言語に関係なく日本語でも英語でも打ってヒットする（例: 英語 UI でも "分割" で「右に分割/Split right」に一致）。
`lib/i18n` に `tLocales(key)`（全ロケール訳の配列）を追加。生成コマンド（ペイン N）は `keys.cmd.paneFocus|n=N`
の `|k=v` 記法で `{n}` を運ぶ。

### 5.6 overlay のフォーカス復帰

コマンドパレット／チートシートは、**開いた時点のフォーカス元（opener）を記憶**し、**キャンセル
（Esc / ブラウザ戻る / 背景クリック）時に opener へフォーカスを戻す**。入力中（コンポーザ等）に `Ctrl+P` →
`Esc` でフォーカスが行方不明になる問題の対策。ただし**コマンド実行時は戻さない**（コマンドが意図的に
フォーカスを移す＝ペインへ移動等のため）。タッチ端末ではソフトキーボード誤起動を避けて復帰しない
（`coarsePointer()` ガード。既存 `useFocusTrap` と同方針）。

## 5.7 日常操作の Alt 直キー（2026-08 追加）

`Alt` は「端末にもブラウザにも取られていない」唯一のまとまった名前空間なので、**毎日何度も押す操作**は
リーダー経由（3 打鍵）だけでなく Alt 直キーを持つ。現在の割り当て:

| キー | コマンド | 備考 |
|------|----------|------|
| `Alt+1..8` | ペイン N へ | 存在するときだけ握る |
| `Alt+[` / `Alt+]` | 前 / 次のペイン | JIS は `.key` 優先で解決（§1） |
| `Alt+PageUp` / `Alt+PageDown` | 前 / 次の**タブ** | タブモードで 2 枚以上あるときだけ握る。ペイン軸（`Alt+[ ]`）とは別軸 |
| `Alt+W` | タブ / ペインを閉じる | ブラウザの「タブを閉じる」慣習に合わせる |
| `Alt+Shift+W` | 全ペインを閉じる | |
| `Alt+N` | ＋はじめる（新規セッション） | |
| `Alt+A` | メモを追加 | `Alt+M` は Markdown 表示切替が使用中なので A（Add） |
| `Alt+G` | 作業グループを巡回 | 切替先をトーストで通知 |
| `Alt+Q` | 読み上げ ON/OFF | Quiet |
| `Alt+/` | レールの絞り込みへ | 畳んだレールは開いてから focus（ワークスペース起動中のみ） |
| `Alt+,` | 設定を開く | VS Code の `Ctrl+,` 相当。`Ctrl` は端末の制御コードと衝突するので Alt 側 |
| `Alt+M` / `Alt+R` / `Alt+Z` | Markdown 表示切替 / 朗読 / 折り返し | ビュー系（既存） |
| `Alt+=` / `Alt+-` / `Alt+0` | 文字を大きく / 小さく / 既定へ | **アクティブなペインの面**の設定を 1px（下記） |

### 文字サイズ（`Alt+=` / `Alt+-` / `Alt+0`）

文字サイズはグローバル設定が面ごとに 4 本あり（`termSize` / `viewerSize` / `chatSize` /
`readerSize`、いずれも 9〜28px）、設定 › 表示のステッパーと朗読ビューの ＋/− ボタンが動かして
いるのと同じ値。キーボードもそれを踏襲し、**アクティブなペインが属する面**の設定を上下する
（新しい永続状態を増やさないので、設定画面の表示・クロスデバイス同期・既定へのリセットがその
まま効く）。対応は `lib/viewFont.ts` の `fontSettingFor()`（純関数・vitest 対象）:

| ペイン | 動かす設定 |
|--------|-----------|
| 端末（`terminal` chat=false） | `termSize`（xterm は refit 済み） |
| ミラー / チャット / 共有ビュー（`terminal` chat=true・`chat`・`sharedSession`） | `chatSize` |
| 朗読ビュー（`read`） | `readerSize` |
| ファイル / diff / SCM / commit / doc | `viewerSize` |
| 画像・ブラウザペイン | **なし＝キーを握らず端末へ流す** |

キーの形の注意: **US 配列の `+` は Shift+= なので `alt+=` と `alt+shift+=` の両方**を登録して
ある（テンキーの `+`/`-` も）。JIS では `=` の位置が `^` キーだが、ディスパッチャの「句読点は
`e.key` を先に、無ければ `e.code`」の候補順で `alt+=` に落ちるので同じく効く。`Ctrl` 側を使え
ないのは、端末が `Ctrl +/-/0` を**意図的にブラウザズームへ通している**ため（`terminal/term.ts`
の `NO_GRAB`）——`Alt` を選ぶ理由がここにもある。

**避けたキー（ブラウザ / OS が先に食う）**: `Alt+D`（アドレスバー）、`Alt+E`・`Alt+F`（Chrome メニュー。
Firefox はメニューニーモニックで E/F/V/S/B/T/H）、`Alt+←/→`・`Alt+Home`（履歴移動）。この禁止リストは
`features/keys/commands.test.ts` の不変条件テストで固定してあり、うっかり割り当てると CI が落ちる。

**割り当てない方針のもの**: ワークスペース起動/停止（誤爆でフリートが止まる＝リーダー 3 打鍵のまま）、
分割（素直な空きが `\` 系しか無く JIS で `.code` が化ける）。

**既知のトレードオフ**: 入力欄にフォーカスがあっても直キーは発火する（既存の `Alt+M/R/Z` と同じ）。macOS で
`⌥+英字` を特殊文字入力に使う場合はぶつかるので、その環境では設定 →「キー操作」で解除/再割当する。
端末では設定「端末入力を優先」ON で全部素通しにできる（§5.3）。

## 6. 実装ファイル早見

| 関心事 | ファイル |
|--------|----------|
| キー正規化（純） | `console/src/lib/keys/chords.ts` |
| 型＋照合＋override（純） | `console/src/lib/keys/registry.ts` |
| コマンド DATA | `console/src/features/keys/commands.ts` |
| ディスパッチャ | `console/src/features/keys/dispatcher.ts` |
| 再割当解決層 | `console/src/features/keys/bindings.ts` |
| overlay 状態 store | `console/src/features/keys/store.ts` |
| which-key / パレット / チートシート | `console/src/features/keys/{WhichKey,CommandPalette,CheatSheet}.tsx` |
| ボタン inline ヒント | `console/src/features/keys/keyHint.ts` |
| 文言解決（i18n 表示＋全ロケール検索） | `console/src/features/keys/labels.ts` |
| 文言カタログ（`keys.*`） | `console/src/lib/i18n/locales/{ja,en}.ts` |
| 設定タブ | `console/src/features/settings/KeysTab.tsx` |
| 永続化（keybindings/terminalPriority） | `console/src/lib/settings.ts` |
