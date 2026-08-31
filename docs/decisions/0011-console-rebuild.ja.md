# 0011. Console リビルド — 並行エントリ方式・zustand 採用・旧側凍結

[English](0011-console-rebuild.md) | 日本語

- 状態: 確定（2026-07-07）
- 関連: [22-console-rebuild.md](../log/22-console-rebuild.md)（設計本体）/ [0004-vanilla-to-react.md](0004-vanilla-to-react.ja.md)（前回刷新）

## 背景

React 化（ADR 0004）後も機能増築（split ペイン・SCM・ビューア群・assistant chat・memo queue・
SSM・admin…）が続き、`console/src` は約 31.5k 行に肥大。単一 `AppContext`（110+ キー・31 消費者・
`bump*()` カウンタ連携）、pane↔xterm↔history の三重 paneId 結合、8.8k 行単一 CSS という
「増築を全部吸い込む構造」が固定化し、リファクタリングでは解けないと判断。機能パリティを保った
リビルド（フレームワークは React + Vite + TS 続投、バックエンド無改修）を行う。

## 決定

1. **移行方式 = 並行エントリ**。同一 Vite プロジェクトの multi-entry で `next.html`（新）を
   `index.html`（現行）と同じ dist に同居させ、実バックエンドで新旧を並走目視。完成後にスワップ
   して旧コードを削除。CP 変更ゼロ、日常運用を壊さない。big-bang（別ディレクトリ一括差し替え）は
   テストゼロの現状ではリスク過大、in-place 段階置換は並走比較ができないため不採用。
2. **状態管理 = zustand**。単一 Context をドメイン別ストア（tenant/workspace/sessions/layout/
   dialogs/settings）に分割、selector 購読で再レンダーを制御。React 外（term 層）との連携が素直で、
   ref ミラー・wired-once フラグを全廃できる。自作ミニストアより保守コストが低い（~1KB の実績依存
   1 つは許容）。
3. **リビルド期間中、旧 Console への新機能追加は凍結**（バグ修正のみ）。新機能は該当領域の移植
   完了後に新側へ実装。二重実装コストをゼロにし最短で完了させる。

## 帰結

- 設計・フェーズ計画・機能パリティチェックリストは docs/22 に集約。P1（ターミナル + レイアウト
  コア）を最初に置き、最難関を先に潰す。
- `term.ts` の xterm 内部・`api.ts` コア・テーマ機構・flat-absolute ペイン戦略は書き直さず移設。
- vitest を導入し純ロジック（layout 演算・transcript パーサ等）に初の自動テストを付ける。
  ビジュアル検証は従来どおりユーザーのブラウザ目視。
- localStorage / ui-prefs キーは据置き、スワップ時に利用者の設定・レイアウトを引き継ぐ。
