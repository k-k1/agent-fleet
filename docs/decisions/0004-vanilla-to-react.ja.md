# 0004. Console スタック — React + Vite を採用

[English](0004-vanilla-to-react.md) | 日本語

- 状態: 確定（Phase 3 / Console 全面刷新）
- 関連: [dev/02 Console](../build/02-console.ja.md)（旧 HANDOFF §6.10.1） / [history/console-redesign](../log/console-redesign.md)（当時の診断ブリーフ）

## 背景

確定スタックは当初から React（[requirements §1.6（現 dev/01 §1.1）](../build/01-architecture.ja.md#11-何であるか提供モデル)）だが、Phase 1 MVP は
**最小 vanilla JS**（`app.js` 617+行）で出した。機能追加（SCM / Files / Admin / Connections / テナント picker）で
情報設計が破綻——ナビゲーション無し、暗号アイコンの羅列、サイドバーとオーバーレイの混在、ヘッダ過密。
刷新にあたり「vanilla 維持 / 軽量フレームワーク CDN / React 本格採用」を比較した。当時のブリーフ（旧 18）は
配布の手軽さ（ディスク配信・no-store・即反映）を重んじ **vanilla 維持を推奨**していた。

## 決定

**React + Vite を採用**（`console/src` → `console/dist` を CP が `Cache-Control: no-store` で配信）。
no-build の手軽さより、増えた機能（左アクティビティ・レール + 単一メインの VS Code 風 IA、SCM/ファイル
ビュアー/設定/管理）の作り込みやすさを優先した。配布の懸念は dist をイメージに焼くことで吸収する。

- IA: 2 段バー（TOP = アプリ名/テナント picker/whoami/設定/管理）＋ 左ペイン 3 セクション（Sessions/Repos/Files）
  ＋ メインが選択で切替。オーバーレイは廃止しビュー切替へ。
- フロントだけの調整は `vite build --watch` → ブラウザ・リロードで反映（CP 再起動不要）。

## 帰結

- 旧 vanilla は一時 `console/legacy-phase1/` に退避していたが、移植完了後に削除済み。振る舞いの正は現 Console（HANDOFF §6.10.1）。
- ビルド工程が増えるが、`run-dev.sh` が `NODE_OPTIONS=--max-old-space-size=3072 npm run build`（mermaid の
  heap OOM 回避）で吸収。P3-10 パッケージングは dist 同梱で配布する。
