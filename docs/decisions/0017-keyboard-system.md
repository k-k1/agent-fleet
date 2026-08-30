# 0017. Console キーボード操作体系 — capture-phase 単一ディスパッチャ＋Leader/パレット＋再割当

- 状態: 確定・P0〜P5 実装済み（2026-07-16）——P0（ディスパッチャ＋レジストリ）／P1（領域・ペイン移動）／
  P2（Leader＋which-key＋コマンドパレット）／P3（モーダル focus-trap・メニュー/レール roving）／
  P4（`?` チートシート＋ボタン inline ヒント）／P5（設定での再割当 UI＋端末入力優先トグル）
- 関連: [29-keyboard-system.md](../log/29-keyboard-system.md)（設計本体・実装マップ）/
  [0011-console-rebuild.md](0011-console-rebuild.md)（この体系が載る Console 基盤）/
  [0016-i18n.md](0016-i18n.md)（将来、コマンド文言を lib/i18n へ集約する接続先）

## 背景

Console は 1 ワークスペースを複数ペイン（ターミナル / チャット / ファイル / 差分…）で同時に駆動する。
だが操作の中心である **xterm はフォーカス中ほぼ全キーを PTY へ飲む**（`terminal/term.ts` の
`attachCustomKeyEventHandler` が `Ctrl+*`／F1–F10 を軒並み `preventDefault` で握る。素通しの
carve-out は Ctrl+C/V とズームのみ）。このため「ターミナルにフォーカスしたままアプリの操作をする」
導線が事実上存在しなかった。マウス依存が強く、キーボードだけで完結できない。

## 決定

1. **単一の capture-phase ディスパッチャ**（`features/keys/dispatcher.ts`）が `window` の
   keydown を capture フェーズで 1 本だけ購読する。xterm ハンドラも React の onKeyDown も
   DOM ツリー上は window の子孫なので、**登録済みキーだけ** `preventDefault + stopPropagation`
   で先に握り、未登録キーは素通しする（シェルは無傷）。これが xterm の grab を貫く唯一の機構。

2. **ハイブリッド操作系**: Leader（`Ctrl/⌘+K`）＋ which-key ＋ コマンドパレット（`Ctrl/⌘+P`）＋
   少数の直接アクセラレータ（`Alt` 修飾）。`Ctrl≡⌘`（`e.ctrlKey || e.metaKey` を 1 トークン `mod` に）。
   IME 変換中（`isComposing`／keyCode 229）と auto-repeat は非発火。

3. **キー正規化は `KeyboardEvent.code` 基準**（`lib/keys/chords.ts`）。`.key` は Mac の ⌥/Shift で
   化けるため使わない（⌥+1→"¡"、Shift+k→"K" でも code は Digit1／KeyK）。Shift は独立修飾として保持
   （`k` と `shift+k` は別バインド＝ペイン移動の hjkl/HJKL に使う）。

4. **Escape は capture で握らない**。overlay の閉じは escLayer が bubble フェーズで担う設計なので、
   capture で Escape を stop すると全モーダル/メニューの Esc が壊れる。例外はリーダー保留中のみ。
   overlay が開いている間はディスパッチャ自体を不活性化する（`escLayer.hasOpenOverlay()`）。

5. **レイヤリング**: 純ロジックは `lib/keys/`（`chords.ts`／`registry.ts`、store も DOM も import せず
   vitest 対象）、ストア結合は `features/keys/`。コマンド DATA は 1 か所（`commands.ts` の
   `ALL_COMMANDS`）に集約し、ディスパッチャ・which-key・パレット・チートシート・ボタンヒントが
   すべてそこから読む＝表示と挙動が絶対にドリフトしない。

6. **【P5】再割当できるのは直接アクセラレータと 3 つのアプリ全体キー**（Leader / パレット / チートシート）
   **だけ**。リーダー配下のシーケンス（`p r`、`w t` 等）は木構造ナビゲーションであり、任意再割当は
   グループ木の衝突管理を複雑化し操作不能を招くリスクが高いので固定とする。上書きは
   `Settings.keybindings`（`id → chord`、`""`＝明示無効化）に保存し、`features/keys/bindings.ts` の
   `effectiveCommands()` / `boundChord()` が `ALL_COMMANDS` と予約キーに被せて解決する。純関数
   `applyOverrides()`（`lib/keys/registry.ts`）で被せるので既存 consumer は `Command[]` を受け取る
   だけで変わらない。localStorage＋サーバ（ui-prefs）同期の既存機構に相乗り＝クロスデバイス（`theme`
   等の `DEVICE_LOCAL` には**含めない**——キー配列は環境依存ではなく作業様式の好み）。

7. **【P5】端末入力優先トグル**（`Settings.terminalPriority`、既定 OFF）。ON のとき、端末フォーカス中は
   全アプリショートカットを xterm へ素通しし、**Leader だけ**を生かす（tmux の prefix 方式）。Leader から
   which-key／パレットで全操作に到達できる。Leader 自体も再割当可なので、Leader を無効化すれば「完全に
   純粋な端末」も選べる。1 キーだけ残すのは、端末に閉じ込められない**脱出保証**のため。

## 却下した案

- **エディタ風の 1 グローバルアクセラレータ体系（Ctrl+多数）**: xterm と全面衝突し、端末を使うほど
  ショートカットが死ぬ。Leader 方式なら衝突面は 1 キーに畳める。
- **キャプチャを使わず各コンポーネントで onKeyDown**: xterm の grab を貫けず、端末フォーカス中に無力。
- **リーダーシーケンスまで完全再割当可**: UI とグループ木衝突検知が過大。直接キー＋予約キーで実利の大半を
  カバーでき、シーケンスは意味づけ（p=pane 等）が強いので固定が妥当（決定 6）。
- **端末優先で脱出キーを一切残さない**を既定にする: 端末に入るとキーボードで抜けられず事故る。既定は
  Leader を残し、完全純粋端末は明示オプトイン（Leader 無効化）に留めた（決定 7）。

## 影響

- ディスパッチャ／各 overlay は `effectiveCommands()`／`boundChord()` 経由になり、再割当が即時反映。
- 設定に「キー操作」タブ（`features/settings/KeysTab.tsx`）を新設。再割当行＋キー記録（capture）＋衝突警告＋
  端末優先トグル。設定モーダルは overlay ゆえディスパッチャが不活性で、記録用の capture リスナが安全に
  キーを独占できる（Ctrl+P の印刷等だけ preventDefault で抑止）。
- 文言は将来 [[0016-i18n]] の lib/i18n 経由に置換可能（レジストリ集約済で 1 か所で済む）。
