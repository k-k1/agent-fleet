// Lazy loading of anydoc (Firecrawl, MIT, Rust → WASM) (docs/log/82 §82.4).
//
// The layer that converts Word / Excel / PowerPoint to GFM so the Console's MarkdownView can
// show it. It does not reproduce the appearance: page layout, shape positions and cell colours
// are lost and images become alt text. The goal is only "readable, searchable, quotable", which
// is ADR 0063 decision 3.
//
// The WASM is large (2.9 MB gzipped), so never import it statically. Both the module and the
// binary are fetched dynamically so that only someone who opens such a file pays for it (the
// same practice as pdf.js).
import wasmUrl from "@firecrawl/anydoc-wasm/anydoc_wasm_bg.wasm?url";
import type * as AnydocModule from "@firecrawl/anydoc-wasm";

/** The format name anydoc accepts, spelled the same as the extension. */
export type AnydocFormat = AnydocModule.Format;

/** Why the conversion failed; the message shown is chosen from this value (docs/log/82 §82.4). */
export type AnydocFailure = "unsupported" | "needsOcr" | "malformed" | "encrypted" | "resourceLimit" | "missingPart" | "failed";

let loading: Promise<typeof AnydocModule> | null = null;

/** Loads and initialises the WASM. Every later call returns the same Promise. */
export function loadAnydoc(): Promise<typeof AnydocModule> {
  if (!loading) {
    loading = import("@firecrawl/anydoc-wasm").then(async (mod) => {
      await mod.default({ module_or_path: new URL(wasmUrl, document.baseURI).toString() });
      return mod;
    });
  }
  return loading;
}

/** Reads the failure kind from an Error thrown by anydoc; one without `code` counts as failed. */
export function anydocFailure(e: unknown): AnydocFailure {
  const code = (e as { code?: string } | null)?.code;
  switch (code) {
    case "unsupported":
    case "needsOcr":
    case "malformed":
    case "encrypted":
    case "resourceLimit":
    case "missingPart":
      return code;
    default:
      return "failed";
  }
}

/** Converts bytes to GFM. The format is detected from the content, falling back to the
 *  extension only when detection fails. */
export async function toMarkdown(bytes: Uint8Array, format?: AnydocFormat): Promise<string> {
  const anydoc = await loadAnydoc();
  return anydoc.toMarkdownBytes(bytes, format ?? null);
}
