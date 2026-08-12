import { useLayoutStore } from "../../layout/store.ts";

export function openSharedSession(id: string, split = false): void {
  const target = { content: { kind: "sharedSession" as const, sharedSessionId: id }, session: null };
  const store = useLayoutStore.getState();
  if (split) store.openTargetInNew(target);
  else store.openTarget(target);
}
