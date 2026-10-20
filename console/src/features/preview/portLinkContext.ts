// The opener a PortLinkMenuHost hands to the Markdown links below it. Kept apart from the host so
// MarkdownView can read it without importing the menu (and, through it, PortOpen and the stores).
import { createContext, useContext } from "react";

/** Opens the menu for `port` (and the link's `path`) at viewport coordinates (x, y). */
export type OpenPortLinkMenu = (port: number, path: string, x: number, y: number) => void;

export const PortLinkMenuContext = createContext<OpenPortLinkMenu | null>(null);

/** The opener of the nearest host, or null where links keep the native menu. */
export function usePortLinkMenu(): OpenPortLinkMenu | null {
  return useContext(PortLinkMenuContext);
}
