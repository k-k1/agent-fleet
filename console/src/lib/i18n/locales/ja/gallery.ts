// 日本語 カタログ / ドメイン: gallery
// キー接頭辞: gallery
//
// ⚠️ 追記は**自分のドメインのファイルだけ**に行う（ADR 0067 決定 4）。分割前は 4,700 行の
// 1 ファイルで、フロントの並列セッションが全員ここへ追記＝毎回確実に衝突していた。
// ja が正本。新しいキーはここに足し、同じキーを ../en/ の同名ファイルにも足す。
export const gallery = {
  "gallery.title_session": "生成した画像 — {name}",
  "gallery.empty": "このフォルダに画像はありません。",
  "gallery.failed": "フォルダを読み込めませんでした",
  "gallery.refresh": "最新に更新",
  "gallery.sort": "並び",
  "gallery.sort_new": "新しい順",
  "gallery.sort_name": "名前順",
  "gallery.summary": "{count} 枚 · {size}",
  "gallery.shown": "{shown} / {count} 枚",
  "gallery.more": "さらに表示",
  "gallery.zoom": "{name} を拡大",
  "gallery.open_in_pane": "{name} をペインで開く",
  "gallery.open_folder": "フォルダを開く",
  "gallery.prev": "前の画像",
  "gallery.next": "次の画像",
  "gallery.position": "{index} / {total}",
};
