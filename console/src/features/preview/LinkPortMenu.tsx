// The menu itself, loaded when the first one opens (see PortLinkMenuHost).
import { useWorkspaceStore } from "../../core/store/workspace.ts";
import { openPort, PortMenu, usePortState } from "./PortOpen.tsx";
import type { PortTarget } from "./portTargets.ts";

export default function LinkPortMenu({ menu, onClose }: { menu: { port: number; path: string; x: number; y: number }; onClose: () => void }) {
  const running = useWorkspaceStore((s) => s.state === "running");
  const { info, targets } = usePortState(menu.port, running);
  const go = (t: PortTarget, inNew = false) => openPort(t, menu.port, menu.path, inNew);
  return <PortMenu port={menu.port} info={info} targets={targets} go={go} at={menu} onClose={onClose} />;
}
