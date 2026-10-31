// G7 / ADR 0126: a request shows as a toast unless its session's mirror is on screen. A mirror
// a phone keeps mounted in a hidden column (display:none) is not on screen.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, useEffect, useRef } from "react";
import { createRoot, type Root } from "react-dom/client";

vi.mock("../../core/api/client.ts", () => ({
  getTenant: () => "",
  api: vi.fn(async (path: string) =>
    path === "api/aws-confirm"
      ? {
          requests: [
            {
              id: "r1",
              session: "s1",
              targets: [{ profile: "prod-admin", label: "", accountId: "123456789012", roleName: "Admin" }],
              firstAt: "2026-10-31T00:00:00Z",
              lastAt: "2026-10-31T00:00:00Z",
              expiresInSec: 900,
            },
          ],
        }
      : {},
  ),
}));

const { AwsConfirmHost } = await import("./AwsConfirmHost.tsx");
const { useAwsConfirmStore } = await import("./store.ts");
const { useDisplayed, displayed } = await import("./useDisplayed.ts");
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");
const { t } = await import("../../lib/i18n/index.ts");

// A mirror stand-in that registers itself exactly as MirrorView does.
function Mirror({ session }: { session: string }) {
  const ref = useRef<HTMLDivElement>(null);
  const onScreen = useDisplayed(ref);
  const mirrorShown = useAwsConfirmStore((s) => s.mirrorShown);
  useEffect(() => (onScreen ? mirrorShown(session) : undefined), [session, onScreen, mirrorShown]);
  return <div ref={ref}>mirror</div>;
}

let root: Root | null = null;
let host: HTMLDivElement;

beforeEach(() => {
  useAwsConfirmStore.setState({ requests: [], answering: {}, mirrors: {} });
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root?.unmount());
  host.remove();
});

const toastShown = () => document.body.textContent!.includes(t("awsconfirm.title_session", { session: "s1" }));

async function render(hidden: boolean) {
  await act(async () =>
    root!.render(
      <ToastProvider>
        <div style={hidden ? { display: "none" } : {}}>
          <Mirror session="s1" />
        </div>
        <AwsConfirmHost />
      </ToastProvider>,
    ),
  );
  await act(async () => new Promise((r) => setTimeout(r, 0)));
}

describe("where a confirmation request shows", () => {
  it("is a toast when the session's mirror is mounted but hidden", async () => {
    await render(true);
    expect(useAwsConfirmStore.getState().mirrors).toEqual({});
    expect(toastShown()).toBe(true);
  });
  it("is not a toast when the session's mirror is on screen", async () => {
    await render(false);
    expect(useAwsConfirmStore.getState().mirrors).toEqual({ s1: 1 });
    expect(toastShown()).toBe(false);
  });
  it("displayed() sees display:none on an ancestor", () => {
    const outer = document.createElement("div");
    const inner = document.createElement("span");
    outer.appendChild(inner);
    document.body.appendChild(outer);
    expect(displayed(inner)).toBe(true);
    outer.style.display = "none";
    expect(displayed(inner)).toBe(false);
    outer.remove();
  });
});
