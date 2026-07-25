import { describe, expect, it } from "vitest";
import { EditorState, Transaction } from "@codemirror/state";
import { filterBufferTransaction, validateEditorInsertion } from "./CodeEditor.tsx";
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

  it("rejects raw CR before CodeMirror can normalize it to LF", () => {
    const state = EditorState.create({ doc: "ok\n" });
    expect(validateEditorInsertion(state, state.doc.length, state.doc.length, "\r\n")?.code)
      .toBe("unsupported_newline");
  });
});
