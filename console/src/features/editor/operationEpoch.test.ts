import { describe, expect, it } from "vitest";
import {
  beginSave,
  createFileEditorModel,
  discardToBase,
  editBuffer,
  saveSucceeded,
} from "./model.ts";
import { revisionOf } from "./buffer.ts";
import { OperationEpoch } from "./operationEpoch.ts";

describe("save operation epoch", () => {
  it("does not apply an in-flight 200 response after discard", () => {
    const base = createFileEditorModel("p1", "repos/a.txt", "base\n", revisionOf("base\n"));
    const [saving, snapshot] = beginSave(editBuffer(base, "mine\n"));
    const operations = new OperationEpoch();
    const saveEpoch = operations.capture();

    operations.invalidate();
    const discarded = discardToBase(saving);
    const afterResponse = operations.isCurrent(saveEpoch)
      ? saveSucceeded(discarded, snapshot, snapshot.bufferRevision)
      : discarded;

    expect(afterResponse).toBe(discarded);
    expect(afterResponse.content).toBe("base\n");
    expect(afterResponse.baseDiskRevision).toBe(revisionOf("base\n"));
    expect(afterResponse.dirty).toBe(false);
    expect(afterResponse.phase).toBe("clean");
  });
});
