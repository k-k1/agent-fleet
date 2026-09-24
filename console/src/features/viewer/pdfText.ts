// What goes on the clipboard when text is copied out of the PDF pane.

// Kangxi Radicals block. PDF producers (chromium's print-to-PDF among them) map ideographs to
// these look-alikes in ToUnicode, so the copy reads right and then matches nothing it is pasted
// into or searched for. pdf.js's normalizeUnicode leaves them alone.
const KANGXI_RADICALS = /[\u2f00-\u2fd5]/g;

/** Clipboard text for a PDF selection: pdf.js's normalisation (ligatures, presentation forms),
 *  radicals folded to their ideographs, NULs dropped.
 *
 *  Deliberately not a blanket NFKC: that would also turn full-width punctuation and digits into
 *  ASCII and half-width kana into full-width, rewriting Japanese text the reader meant to copy. */
export function clipboardText(selected: string, normalizeUnicode: (s: string) => string): string {
  return normalizeUnicode(selected)
    .replace(KANGXI_RADICALS, (c) => c.normalize("NFKC"))
    .replaceAll("\0", "");
}
