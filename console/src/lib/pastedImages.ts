// Pasted-image helpers shared by the terminal mirror (MirrorView) and the assistant
// chat (ChatView) composers. Both ride the same flow: the headless CLI agent (claude)
// has no native image input, so we upload the image, then reference its saved absolute
// path in the prompt and let claude's Read tool open it.

// Machine-facing instruction appended to a prompt when image(s) are attached. English
// (not Japanese) because it's a CLI-agent instruction — hidden from the chat bubble by
// splitPastedImages, and English tokenizes cheaper.
export const IMG_PROMPT = "Open the following image(s) with the Read tool:";
// Prior (Japanese) wording, still stripped from older turns so their bubbles stay clean.
export const IMG_PROMPT_LEGACY = "次の画像を Read ツールで開いて確認してください:";
// Matches our pasted-image paths (…/pasted/<key>/paste-<n>.<ext>), capturing the basename.
export const PASTE_PATH_RE = /\S*\/pasted\/[^\s/]+\/(paste-\d+\.(?:png|jpe?g|gif|webp))/g;

// splitPastedImages pulls the pasted-image basenames out of a user turn's text and returns
// the text with the appended image instruction removed (so the bubble shows the user's
// words + thumbnails, not the machine-facing paths).
export function splitPastedImages(text: string): { text: string; images: string[] } {
  const images: string[] = [];
  PASTE_PATH_RE.lastIndex = 0;
  let m: RegExpExecArray | null;
  while ((m = PASTE_PATH_RE.exec(text))) images.push(m[1]);
  if (!images.length) return { text, images };
  // Trim the instruction (current or legacy wording) plus the trailing paths; fall back
  // to stripping the paths alone.
  let idx = text.indexOf(IMG_PROMPT);
  if (idx < 0) idx = text.indexOf(IMG_PROMPT_LEGACY);
  const cleaned = (idx >= 0 ? text.slice(0, idx) : text.replace(PASTE_PATH_RE, "")).trim();
  return { text: cleaned, images };
}

// buildImagePrompt composes the sent prompt: the user's words + the instruction + the
// space-joined absolute paths (kept on ONE line so send-keys can't submit early).
export function buildImagePrompt(text: string, paths: string[]): string {
  if (!paths.length) return text;
  const instr = IMG_PROMPT + " " + paths.join(" ");
  return text ? text + " " + instr : instr;
}
