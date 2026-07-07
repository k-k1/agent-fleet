// Memo queue domain types (docs/21). A Memo is a note queued per membership and
// synced across devices, then flushed to a coding session as one concatenated
// message. Notes are grouped by repo then a free-form category (sub-project);
// repo="" is the common/unfiled bucket. Persisted in the Control Plane SQLite.

export type MemoKind = "file" | "text";

export interface Memo {
  id: string;
  repo: string; // "repos/<repo>"-derived name; "" = common bucket
  category: string; // free-form sub-project label; "" = unfiled
  kind: MemoKind;
  body: string; // free text, or a comment when kind=file
  refPath: string; // kind=file: "~/repos/<repo>/..."
  position: number; // order within its group
  createdAt: string; // RFC3339
  sentAt: string; // "" = unsent; RFC3339 stamp = flushed, kept until retention sweep
}

// The user-editable subset sent to POST /api/memos.
export interface MemoInput {
  repo?: string;
  category?: string;
  kind: MemoKind;
  body?: string;
  refPath?: string;
  position?: number;
}

// Partial edit sent to PATCH /api/memos/{id}; omitted fields are left unchanged.
export interface MemoPatch {
  repo?: string;
  category?: string;
  body?: string;
  refPath?: string;
  position?: number;
}
