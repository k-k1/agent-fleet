// Agent memory > Memories: one master-detail over AF-owned memory (list with scope, search and the
// All / Pinned / Forgotten chips; body, metadata, actions and history in the detail pane). It takes
// its data through the same props as the other views, so it can be mounted elsewhere unchanged.

import { MemoriesMasterDetail } from "../memoryMasterDetail.tsx";

export function MemoriesView({
  reload,
  onChanged,
  onOpenChanges,
}: {
  reload: number;
  onChanged: () => void;
  onOpenChanges?: () => void;
}) {
  return <MemoriesMasterDetail reload={reload} onChanged={onChanged} onOpenChanges={onOpenChanges} />;
}
