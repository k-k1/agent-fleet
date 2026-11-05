// Attachment helpers shared by the terminal mirror (MirrorView) and the assistant
// chat (ChatView) composers. All agents ride the same flow: none of the headless CLI
// agents has native file input in this path, so we upload the file, then reference
// its saved absolute path in the prompt and let the agent open it — claude via its
// Read tool, codex via view_image / shell (a plain path mention suffices), opencode
// via its own tools (image vision is model-dependent; a non-vision model declines
// honestly). Attachments are not just images anymore: drag&drop / the ＋ picker
// accept any file; images additionally get thumbnails in the bubble.

// Machine-facing instructions appended to a prompt when file(s) are attached. English
// (not Japanese) because they're CLI-agent instructions — hidden from the chat bubble
// by splitPastedImages, and English tokenizes cheaper. claude gets its Read-tool
// wording; other agents a tool-neutral one (codex has no Read tool).
export const FILE_PROMPT = "Open the following file(s) with the Read tool:";
export const FILE_PROMPT_GENERIC = "Look at the following file(s):";
// Prior wordings (image-only era), still stripped from older turns so their bubbles
// stay clean.
export const IMG_PROMPT = "Open the following image(s) with the Read tool:";
export const IMG_PROMPT_GENERIC = "Look at the following image file(s):";
// i18n-exempt: legacy agent-facing prompt, only stripped from older turns, never shown (docs/log/28 §4)
export const IMG_PROMPT_LEGACY = "次の画像を Read ツールで開いて確認してください:";
const ATTACH_PROMPTS = [FILE_PROMPT, FILE_PROMPT_GENERIC, IMG_PROMPT, IMG_PROMPT_GENERIC, IMG_PROMPT_LEGACY];
// Matches our pasted-attachment paths (…/pasted/<key>/paste-<n>[-name].<ext>),
// capturing the basename. Images keep the bare paste-<n>.<ext> form; other files
// carry a sanitized copy of their original name for the agent's benefit.
export const PASTE_PATH_RE = /\S*\/pasted\/[^\s/]+\/(paste-\d+[\w.-]*)/g;
// Codex app-server serializes a localImage input back into the rollout as a textual
// marker alongside the image item. It is transport metadata, not user-authored text.
// A captioned send round-trips as separate input_text items concatenated with no
// separator — opening tag, then (after the input_image item) a bare "</image>" — so
// both must be stripped or the leftover closing tag keeps the reconciled text from
// ever matching the plain caption of the optimistic echo (pendingEcho.ts), and the
// "pending" badge (mirror.pending) never clears.
const CODEX_IMAGE_TAG_RE = /<image\b[^>]*\bpath="[^"]+"[^>]*>|<\/image>/gi;

// isImageName reports whether a pasted basename is a thumbnail-able image.
export function isImageName(name: string): boolean {
  return /\.(png|jpe?g|gif|webp)$/i.test(name);
}

// splitPastedImages pulls the pasted-attachment basenames out of a user turn's text and
// returns the text with the appended instruction removed (so the bubble shows the user's
// words + thumbnails / file chips, not the machine-facing paths). images are the
// thumbnail-able ones; files everything else.
export function splitPastedImages(text: string): { text: string; images: string[]; files: string[] } {
  const images: string[] = [];
  const files: string[] = [];
  PASTE_PATH_RE.lastIndex = 0;
  let m: RegExpExecArray | null;
  while ((m = PASTE_PATH_RE.exec(text))) (isImageName(m[1]) ? images : files).push(m[1]);
  if (!images.length && !files.length) return { text, images, files };
  // Trim the instruction (any known wording) plus the trailing paths; fall back to
  // stripping the paths alone. When several instruction wordings are present, cut at the one that
  // appears FIRST in the text: following the order of ATTACH_PROMPTS instead would cut at a later
  // wording, leaving the earlier one in the body or trimming more of the body than intended.
  // Codex's <image …> transport markers are stripped whether or not an instruction was found,
  // since they can appear before it.
  const found = ATTACH_PROMPTS.map((p) => text.indexOf(p)).filter((i) => i >= 0);
  const idx = found.length ? Math.min(...found) : -1;
  // Remove the tags before the paths: if PASTE_PATH_RE eats the path="…" inside a tag first, the
  // rest of the <image …> tag is left behind in the body.
  const cleaned = (idx >= 0 ? text.slice(0, idx) : text.replace(CODEX_IMAGE_TAG_RE, "").replace(PASTE_PATH_RE, ""))
    .replace(CODEX_IMAGE_TAG_RE, "")
    .trim();
  return { text: cleaned, images, files };
}

// The tail buildImagePrompt appends for claude: the instruction, then attachment paths only
// (claude's transcript wraps each in backticks, see image_paste.go).
const CLAUDE_ATTACH_TAIL_RE = new RegExp(
  "^" + FILE_PROMPT.replace(/[.*+?^${}()|[\]\\]/g, "\\$&") + "(?:\\s+`?\\S*/pasted/[^\\s/]+/paste-\\d+[\\w.-]*`?)+\\s*$",
);

// splitCommandArgs is splitPastedImages for a slash command's args. Only claude logs command
// args, so an attachment is exactly a trailing FILE_PROMPT followed by nothing but pasted
// paths; anything else (a quoted instruction, a pasted path the member typed) is their own
// words and stays in the args.
export function splitCommandArgs(args: string): { text: string; images: string[]; files: string[] } {
  const at = args.lastIndexOf(FILE_PROMPT);
  if (at < 0 || !CLAUDE_ATTACH_TAIL_RE.test(args.slice(at))) return { text: args, images: [], files: [] };
  const { images, files } = splitPastedImages(args.slice(at));
  return { text: args.slice(0, at).trim(), images, files };
}

// The characters claude removes from typed and pasted input before submitting it ("Removed N
// invisible characters"): every C0/C1 control but tab and line feed (so CR goes, which also covers
// the CRLF the Agent already turns into LF), soft hyphen, zero-width and bidi controls, variation
// selectors, BOM, tag characters and the blank fillers. Read from claude 2.1.318's own hidden-character
// test; it keeps some of them in context (a joiner inside an emoji), which this drops anyway, so two
// prompts differing only there compare equal. Tabs, spaces and line breaks are left alone: they can
// change what a prompt means. Without this an echo of a paste copied from a chat app or a document
// never matches claude's copy and stays "Pending".
const CLAUDE_INVISIBLE_RE =
  /[\u0000-\u0008\u000b-\u001f\u007f-\u009f\u00ad\u034f\u061c\u115f\u1160\u17b4\u17b5\u180b-\u180f\u200b-\u200f\u2028-\u202e\u2060-\u206f\u3164\ufe00-\ufe0f\ufeff\uffa0\ufff0-\ufffb\u{1107f}\u{13430}-\u{1343f}\u{16fe4}\u{1bca0}-\u{1bca3}\u{1d173}-\u{1d17a}\u{e0000}-\u{e0fff}]/gu;

// samePastedPrompt compares the user-authored portion of two prompt renderings.
// Codex's rollout adds <image …> transport markers that the optimistic Console
// echo does not contain; ignoring those markers lets the real turn replace the echo.
export function samePastedPrompt(a: string, b: string): boolean {
  return splitPastedImages(a).text.trim() === splitPastedImages(b).text.trim();
}

// samePromptIgnoringInvisible is samePastedPrompt that also ignores CLAUDE_INVISIBLE_RE, for a
// prompt claude logged. Other CLIs keep those characters, so for them it would call two different
// prompts the same.
export function samePromptIgnoringInvisible(a: string, b: string): boolean {
  const key = (s: string) => splitPastedImages(s).text.replace(CLAUDE_INVISIBLE_RE, "").trim();
  return key(a) === key(b);
}

// buildImagePrompt composes the sent prompt: the user's words + the kind's instruction +
// the space-joined absolute paths (kept on ONE line so send-keys can't submit early).
export function buildImagePrompt(text: string, paths: string[], kind = "claude"): string {
  if (!paths.length) return text;
  const instr = (kind === "claude" ? FILE_PROMPT : FILE_PROMPT_GENERIC) + " " + paths.join(" ");
  return text ? text + " " + instr : instr;
}

// quoteImagePaths is the twin of claude.QuoteImagePaths (workspace/agent/internal/agents/claude/
// image_paste.go): the spelling the Agent types into claude, so a text read back from claude's
// queue can be compared with the prompt the Console sent. Only the attachment line at the end is
// rewritten — a tail of absolute paths alone after the last Read-tool wording, with at least one
// image in our upload directories — and only those image paths are wrapped in backticks. Keep
// the two in step: a rule the Go side drops or adds here makes a queued prompt look different.
const CLAUDE_READ_PROMPTS = [FILE_PROMPT, IMG_PROMPT];
const AGENT_ATTACHMENT_RE = /\/agent-fleet\/(?:pasted\/[^/]+|memo-images)\/[^/]+$/;
const quotableImage = (tok: string) => /\.(?:png|jpe?g|gif|webp)$/i.test(tok) && AGENT_ATTACHMENT_RE.test(tok);
export function quoteImagePaths(body: string): string {
  let at = -1;
  for (const p of CLAUDE_READ_PROMPTS) {
    const j = body.lastIndexOf(p);
    if (j >= 0 && j + p.length > at) at = j + p.length;
  }
  if (at < 0) return body;
  const tail = body.slice(at);
  // Same split as Go's strings.Fields (Unicode White_Space, NBSP included) for the test, while
  // the rewrite below keeps Go's ASCII token boundaries: a path after an NBSP is quoted with it.
  const fields = tail.split(/\p{White_Space}+/u).filter(Boolean);
  if (!fields.every((f) => f.startsWith("/")) || !fields.some(quotableImage)) return body;
  return body.slice(0, at) + tail.replace(/[^ \t\r\n]+/g, (tok) => (quotableImage(tok) ? "`" + tok + "`" : tok));
}
