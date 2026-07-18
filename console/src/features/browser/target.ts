export interface BrowserTarget {
  port: number;
  path: string;
}

/** Validate the persisted/user-entered half of a browser Page identity. */
export function browserTarget(port: unknown, path: unknown): BrowserTarget | null {
  if (typeof port !== "number" || !Number.isInteger(port) || port < 1 || port > 65535 || port === 7700) return null;
  if (typeof path !== "string" || !path.startsWith("/") || path.startsWith("//") || path.startsWith("/\\") || /[\u0000-\u001f\u007f]/.test(path)) {
    return null;
  }
  try {
    const u = new URL(path, `http://127.0.0.1:${port}/`);
    if (u.hostname !== "127.0.0.1" || Number(u.port || "80") !== port) return null;
  } catch {
    return null;
  }
  return { port, path };
}

export function targetFromURL(raw: string): BrowserTarget | null {
  try {
    const u = new URL(raw);
    if ((u.protocol !== "http:" && u.protocol !== "https:") || (u.hostname !== "127.0.0.1" && u.hostname !== "localhost")) {
      return null;
    }
    const port = Number(u.port || (u.protocol === "https:" ? 443 : 80));
    return browserTarget(port, u.pathname + u.search + u.hash);
  } catch {
    return null;
  }
}
