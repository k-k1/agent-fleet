// :shortcode: to emoji. Walks text nodes only; code / pre are left untouched.

// Common GitHub-style emoji shortcodes (:tada: → 🎉). A curated subset covering what
// shows up in dev docs — unknown codes are left as literal text (no regression). Mirrors
// CodeLeaf's EmojiParser intent without pulling in a full ~1800-entry emoji table.
const EMOJI: Record<string, string> = {
  smile: "😄", smiley: "😃", grin: "😁", laughing: "😆", wink: "😉", blush: "😊",
  joy: "😂", sweat_smile: "😅", thinking: "🤔", eyes: "👀", tada: "🎉", rocket: "🚀",
  fire: "🔥", sparkles: "✨", star: "⭐", star2: "🌟", zap: "⚡", boom: "💥",
  bulb: "💡", warning: "⚠️", white_check_mark: "✅", heavy_check_mark: "✔️",
  x: "❌", negative_squared_cross_mark: "❎", question: "❓", exclamation: "❗",
  bangbang: "‼️", "100": "💯", bug: "🐛", memo: "📝", pencil: "✏️", pencil2: "✏️",
  books: "📚", book: "📖", clipboard: "📋", package: "📦", gear: "⚙️", wrench: "🔧",
  hammer: "🔨", lock: "🔒", unlock: "🔓", key: "🔑", mag: "🔍", link: "🔗",
  pushpin: "📌", label: "🏷️", dart: "🎯", trophy: "🏆", rotating_light: "🚨",
  construction: "🚧", no_entry: "⛔", no_entry_sign: "🚫", recycle: "♻️",
  checkered_flag: "🏁", bell: "🔔", email: "📧", "e-mail": "📧",
  speech_balloon: "💬", robot: "🤖", computer: "💻", floppy_disk: "💾",
  hourglass: "⏳", calendar: "📅", clock: "🕐", heart: "❤️", broken_heart: "💔",
  "+1": "👍", thumbsup: "👍", "-1": "👎", thumbsdown: "👎", ok_hand: "👌",
  raised_hands: "🙌", clap: "👏", pray: "🙏", point_right: "👉", point_left: "👈",
  wave: "👋", muscle: "💪", ghost: "👻", thread: "🧵",
  arrow_right: "➡️", arrow_left: "⬅️", arrow_up: "⬆️", arrow_down: "⬇️",
};
const EMOJI_RE = /:([a-z0-9_+-]+):/gi;

// renderEmoji replaces :shortcode: in text nodes (skipping code / pre) with the emoji.
export function renderEmoji(root: HTMLElement) {
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT, {
    acceptNode(n) {
      if (!n.nodeValue || n.nodeValue.indexOf(":") < 0) return NodeFilter.FILTER_REJECT;
      return (n.parentElement?.closest("code,pre"))
        ? NodeFilter.FILTER_REJECT
        : NodeFilter.FILTER_ACCEPT;
    },
  });
  const targets: Text[] = [];
  for (let n = walker.nextNode(); n; n = walker.nextNode()) targets.push(n as Text);
  for (const t of targets) {
    const next = t.nodeValue!.replace(EMOJI_RE, (whole, code: string) => EMOJI[code.toLowerCase()] ?? whole);
    if (next !== t.nodeValue) t.nodeValue = next;
  }
}
