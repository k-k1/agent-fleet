// English カタログ / ドメイン: gallery
// キー接頭辞: gallery
//
// ⚠️ 追記は**自分のドメインのファイルだけ**に行う（ADR 0067 決定 4）。分割前は 4,700 行の
// 1 ファイルで、フロントの並列セッションが全員ここへ追記＝毎回確実に衝突していた。
// ja が正本。Record<keyof typeof ja...> で ja に無いキー / 足りないキーを tsc が落とす。
import type { gallery as jaGallery } from "../ja/gallery.ts";

export const gallery: Record<keyof typeof jaGallery, string> = {
  "gallery.root": "Home",
  "gallery.up": "Up",
  "gallery.breadcrumb": "Current folder",
  "gallery.summary_folders": "{n} folders",
  "gallery.folder_images": "{n} images",
  "gallery.title_session": "Generated images — {name}",
  "gallery.empty": "No images in this folder.",
  "gallery.failed": "Could not load the folder",
  "gallery.loading": "Loading…",
  "gallery.refresh": "Refresh",
  "gallery.sort": "Sort",
  "gallery.sort_new": "Newest first",
  "gallery.sort_name": "By name",
  "gallery.summary": "{count} images · {size}",
  "gallery.shown": "{shown} of {count}",
  "gallery.more": "Show more",
  "gallery.zoom": "Enlarge {name}",
  "gallery.open_in_pane": "Open {name} in a pane",
  "gallery.open_folder": "Open the folder",
  "gallery.prev": "Previous image",
  "gallery.next": "Next image",
  "gallery.position": "{index} / {total}",
};
