# 0009 — チャット transcript の末尾ウィンドウ読み込みと逆方向ページング

[English](0009-transcript-paging.md) | 日本語

- 状態: 採用（段階導入。P1 実装着手、P2/P3 は後続）
- 日付: 2026-07-03
- 関連: `workspace/agent/session_transcript.go`（`handleSessionMessages` / `collectTurns`）、`workspace/agent/session_io.go`（`transcriptRead`）、`console/src/views/MirrorView.tsx`

## 背景

チャット（MirrorView）の初回表示は `GET …/messages?since=0` で、サーバは
`transcriptRead`（`os.ReadFile` で全体を読み split）→ `collectTurns`（`since` から
末尾まで全行 `parseTurn`＝JSON Unmarshal）→ 全ターンを一括返却している。以後の
ライブ更新は `since=cursor` で末尾増分だけ取るため軽い。**重いのは初回の一回**で、
「全 I/O ＋ 全行パース ＋ 全ターン転送」になっている。

実測（ホストの Claude Code transcript）: 最大 **19.3 MB / 2,090 行**、最長の単一行
**≈391 KB**（画像・巨大 tool_result）、**5 MB 超が 14 本**。肥大化した会話で初回が遅い。

コスト内訳（支配順）:
1. **JSON パース**（全行 `Unmarshal`。数百 KB の行が混じる）— 支配的。
2. **転送ペイロード**（全ターンの構造化 JSON）。
3. **ファイル I/O**（全体 `ReadFile`。OS キャッシュが効けば二次的）。
4. クライアント描画（全ターンの Markdown/ハイライト）。

支配項の 1・2 は **ファイルを逆読みしなくても、返す範囲を末尾に絞るだけで消える**。

副次: `collectTurns` の 1 MiB 予算は `since` から**古い方へ**積んで打ち切るため、巨大
transcript では「最新ターンが欠ける」向きになっている（潜在バグ）。

## 決定

「末尾から遡って読む」を、**既存の `cursor＝先頭からの絶対行番号`（append-only ＝ 安定）
1 本の座標系**に統合する。3 アクセスを同じ座標で扱う:

- **① 初回（tail）**: `?tail=1&limit=M` → 末尾ウィンドウのターン ＋ `firstLine` ＋ `cursor=len` ＋ `hasMore`
- **② 過去へ（before）**: `?before=<firstLine>&limit=M` → firstLine より前のターン ＋ 新しい `firstLine` ＋ `hasMore`（クライアントは prepend）
- **③ ライブ（since）**: `?since=<cursor>` → 末尾の新着（**現状のまま・無改造**）

初回で `cursor=len` を返しさえすれば ③ は不変。② は別パラメータで ③ の `since` 空間を
汚さない。**破壊的変更ゼロ**で足せ、`reset` 機構・fork プレビューとも同じ枠に収まる。

### 段階

| 段階 | 内容 | 効果 | 工数/リスク |
| --- | --- | --- | --- |
| **P1** | サーバ: 末尾ウィンドウのみパース（`collectTurns` を `[lo,hi)` に）。予算を**末尾優先**へ修正。`before`/`tail`/`firstLine`/`hasMore` を後方互換で追加。`collectTasks`・`collectAnswers` を軽量化（プレフィルタ/窓内）。 | 支配項のパース/転送を大幅削減。 | 小 / 低（既存 API 内で完結・後方互換） |
| **P2** | クライアント: 初回 `tail`、`before` ページング、prepend＋スクロール位置維持、「さらに読む」。 | 過去は必要時のみロード。初回は末尾固定で軽い。 | 中 / 中（位置維持の検証） |
| **P3** | EOF からの seek 逆読み（長行跨ぎのチャンク拡張つき）。 | 残る I/O も削減。20 MB 常態化時の保険。 | 中 / 高（逆読み端処理・テスト） |
| 代替 | パース結果キャッシュ（`sid＋mtime`、増分のみ再パース）。P1 と直交。 | 再オープンが速い。 | 小 / 低 |

**推奨: P1＋P2 を実装、P3 は実測後に判断。** 遅さの支配項はパースと転送で、逆読み
せずとも末尾ウィンドウで消える。I/O（P3）は二次的。

## 難所と対処

- **ウィンドウ端でターンが切れる**: ② で遡れば繋がる。`groupTurns` を prepend でべき等に。
- **回答解決の範囲依存**: `collectAnswers` を窓内に絞ると窓跨ぎの回答が空になりうる。質問と
  回答は近接するため実用上は許容。緩和は窓幅拡大 or 行 ID 遅延解決。
- **スクロール位置維持（UX の肝）**: prepend 前後の `scrollHeight` 差だけ `scrollTop` を
  足して視点を固定。既存の stick-to-bottom と独立。
- **compaction / ファイル置換**: 絶対行番号が無効化 → 既存 `reset` で末尾から引き直す。
- **全履歴の要約（ToDo/トークン推移/コンテキスト）**: 末尾窓だけにすると痩せる。重い
  `parseTurn` は窓に絞りつつ、**軽い全行スキャン（Task 行・usage 行の抽出）は残す**
  非対称設計を採る（`collectTasks` は文字列プレフィルタで軽量化）。

## 帰結

- 初回表示が肥大化に対してスケールする（返す範囲＝末尾窓に比例）。
- `collectTurns` の予算方向が**末尾優先**になり、最新ターン欠けの潜在バグも解消。
- ライブ増分・reset・fork プレビューは不変。旧クライアント（`tail` 非送出）は従来どおり
  全履歴を受け取る（P1 サーバは単独デプロイ可・後方互換）。
- 非目標: jsonl 形式・保存方式は変更しない。全文検索・仮想スクロールは射程外（将来）。
