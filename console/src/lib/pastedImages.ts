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
export const IMG_PROMPT_LEGACY = "次の画像を Read ツールで開いて確認してください:";
const ATTACH_PROMPTS = [FILE_PROMPT, FILE_PROMPT_GENERIC, IMG_PROMPT, IMG_PROMPT_GENERIC, IMG_PROMPT_LEGACY];
// Matches our pasted-attachment paths (…/pasted/<key>/paste-<n>[-name].<ext>),
// capturing the basename. Images keep the bare paste-<n>.<ext> form; other files
// carry a sanitized copy of their original name for the agent's benefit.
export const PASTE_PATH_RE = /\S*\/pasted\/[^\s/]+\/(paste-\d+[\w.-]*)/g;

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
  // stripping the paths alone.
  const idx = ATTACH_PROMPTS.map((p) => text.indexOf(p)).filter((i) => i >= 0)[0] ?? -1;
  const cleaned = (idx >= 0 ? text.slice(0, idx) : text.replace(PASTE_PATH_RE, "")).trim();
  return { text: cleaned, images, files };
}

// buildImagePrompt composes the sent prompt: the user's words + the kind's instruction +
// the space-joined absolute paths (kept on ONE line so send-keys can't submit early).
export function buildImagePrompt(text: string, paths: string[], kind = "claude"): string {
  if (!paths.length) return text;
  const instr = (kind === "claude" ? FILE_PROMPT : FILE_PROMPT_GENERIC) + " " + paths.join(" ");
  return text ? text + " " + instr : instr;
}
