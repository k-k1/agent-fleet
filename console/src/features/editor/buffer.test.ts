import { describe, expect, it } from "vitest";
import { EditorState, Transaction } from "@codemirror/state";
import { EditorView } from "@codemirror/view";
import {
  bufferValidationExtensions,
  filterBufferTransaction,
} from "./CodeEditor.tsx";
import {
  MAX_EDIT_BYTES,
  REVISION_RE,
  revisionOf,
  validateEditorBuffer,
} from "./buffer.ts";

describe("editor buffer invariant", () => {
  it("hashes raw UTF-8 with the API revision format", () => {
    expect(revisionOf("abc\n")).toBe(
      "sha256:edeaaff3f1774ad2888673770c6d64097e391bc362d7d6fb34982ddf0efd18cb",
    );
    expect(revisionOf("😀\n")).toMatch(REVISION_RE);
  });

  it("accepts LF and paired surrogates but rejects CR, NUL and lone surrogates", () => {
    expect(validateEditorBuffer("a\n😀")).toBeNull();
    expect(validateEditorBuffer("a\r\n")?.code).toBe("unsupported_newline");
    expect(validateEditorBuffer("a\u0000")?.code).toBe("binary_not_supported");
    expect(validateEditorBuffer("\ud800")?.code).toBe("invalid_unicode");
    expect(validateEditorBuffer("\udc00")?.code).toBe("invalid_unicode");
  });

  it("enforces the decoded UTF-8 2 MiB bound", () => {
    expect(validateEditorBuffer("a".repeat(MAX_EDIT_BYTES))).toBeNull();
    expect(validateEditorBuffer("a".repeat(MAX_EDIT_BYTES + 1))?.code).toBe("too_large");
  });

  it.each(["input.type", "input.paste", "undo", "redo"])(
    "rejects an invalid %s document transaction without applying it",
    (event) => {
      const state = EditorState.create({ doc: "ok\n" });
      const transaction = state.update({
        changes: { from: state.doc.length, insert: "\u0000" },
        annotations: Transaction.userEvent.of(event),
      });
      const errors: string[] = [];
      expect(filterBufferTransaction(transaction, (error) => errors.push(error.code))).toEqual([]);
      expect(errors).toEqual(["binary_not_supported"]);
      expect(state.doc.toString()).toBe("ok\n");
    },
  );

  it("rejects CR/CRLF through CodeMirror's raw clipboard and transaction APIs", async () => {
    const errors: string[] = [];
    const state = EditorState.create({
      doc: "ok\n",
      extensions: bufferValidationExtensions((error) => errors.push(error.code)),
    });
    const rawClipboard = "x\r\ny\r";

    // This is the normalization boundary that made a transaction filter alone
    // insufficient: a direct CodeMirror transaction has already lost every CR.
    const normalized = state.update({
      changes: { from: state.doc.length, insert: rawClipboard },
      annotations: Transaction.userEvent.of("input.paste"),
    });
    expect(normalized.newDoc.toString()).toBe("ok\nx\ny\n");

    // Exercise the same public facets and conversion APIs used by CodeMirror's
    // paste handler: raw clipboard filters run before state.toText().
    let filtered = rawClipboard;
    for (const filter of state.facet(EditorView.clipboardInputFilter)) {
      filtered = filter(filtered, state);
    }
    const rejected = state.update(state.replaceSelection(state.toText(filtered)), {
      annotations: Transaction.userEvent.of("input.paste"),
    });
    expect(rejected.newDoc.toString()).toBe("ok\n");
    await Promise.resolve();
    expect(errors).toEqual(["unsupported_newline"]);
  });
});
