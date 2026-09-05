// Memo queue domain types (docs/log/21). A Memo is a note queued per membership and
// synced across devices, then flushed to a coding session as one concatenated
// message. Notes are grouped by repo then a free-form category (sub-project);
// repo="" is the common/unfiled bucket. Persisted in the Control Plane SQLite.

export type MemoKind = "file" | "text";

// An image attached to a memo (docs/log/21 image attachments). path is the absolute in-container path
// returned by memoPasteImage (under ~/.cache/agent-fleet/memo-images); name is the
// basename, used both for display and to preview via GET api/memos/images/{name}.
export interface MemoAttachment {
  path: string;
  name: string;
}

export interface Memo {
  id: string;
  repo: string; // "repos/<repo>"-derived name; "" = common bucket
  category: string; // free-form sub-project label; "" = unfiled
  kind: MemoKind;
  body: string; // free text, or a comment when kind=file
  refPath: string; // kind=file: "~/repos/<repo>/..."
  attachments?: MemoAttachment[]; // image attachments (any kind); omitted = none
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
  attachments?: MemoAttachment[];
  position?: number;
}

// Partial edit sent to PATCH /api/memos/{id}; omitted fields are left unchanged.
export interface MemoPatch {
  repo?: string;
  category?: string;
  body?: string;
  refPath?: string;
  attachments?: MemoAttachment[];
  position?: number;
}

// A first-class category (docs/log/21 UI overhaul): created ahead of any memo, reordered by drag,
// kept while empty. name is unique within a (repo) bucket and stays the grouping key that
// Memo.category references — so a rename cascades onto the memos.
export interface MemoCategory {
  id: string;
  repo: string; // "" = common bucket, mirrors Memo.repo
  name: string;
  position: number; // order within the repo bucket
  createdAt: string; // RFC3339
}

// Body sent to POST /api/memo-categories.
export interface MemoCategoryInput {
  repo?: string;
  name: string;
}

// Partial edit sent to PATCH /api/memo-categories/{id}; omitted fields are unchanged.
// A name change cascades onto the memos (and merges into a same-name category).
export interface MemoCategoryPatch {
  name?: string;
  position?: number;
}
