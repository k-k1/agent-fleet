import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";

const fake = vi.hoisted(() => ({
  controller: {
    attachmentId: "ba_test",
    snapshot: {
      state: "ready",
      attachmentState: "attached",
      url: "https://example.invalid/edit?token=must-not-render",
      title: "Episode editor",
      width: 900,
      height: 600,
      canBack: true,
      canForward: false,
      errorCode: "",
      errorMessage: "",
      console: [],
      expiresAt: "2099-01-01T00:00:00Z",
      controlMode: "user-control",
      handoff: {
        message: "Please confirm",
        completionLabel: "Publish",
        allowCancel: true,
        controlMode: "user-control",
        result: "pending",
      },
    },
    subscribe: vi.fn(() => () => {}),
    mount: vi.fn(),
    unmount: vi.fn(),
    setVisible: vi.fn(),
    setViewport: vi.fn(),
    sendInput: vi.fn(),
    history: vi.fn(),
    reload: vi.fn(),
    reconnect: vi.fn(),
    finish: vi.fn(),
    detach: vi.fn(),
  },
}));

vi.mock("./attachmentService.ts", () => ({ ensureBrowserAttachment: () => fake.controller }));
vi.mock("../../layout/store.ts", () => ({
  useLayoutStore: (selector: (state: { closePane: () => void }) => unknown) => selector({ closePane: () => {} }),
}));
vi.mock("../../lib/i18n/index.ts", () => ({ useT: () => (key: string) => key }));
vi.mock("../../ui/toast.ts", () => ({ toast: vi.fn() }));
vi.mock("../../core/api/client.ts", () => ({ errText: (error: unknown) => String(error) }));

import { BrowserAttachPane } from "./BrowserAttachPane.tsx";

describe("BrowserAttachPane markup", () => {
  it("shows control/handoff chrome and origin without persisting or rendering URL secrets", () => {
    const html = renderToStaticMarkup(<BrowserAttachPane paneId="p3" attachmentId="ba_test" />);
    expect(html).toContain("browser-attach-mode-user-control");
    expect(html).toContain("Please confirm");
    expect(html).toContain("Publish");
    expect(html).toContain("https://example.invalid");
    expect(html).not.toContain("token=must-not-render");
    expect(html).not.toContain("127.0.0.1:");
    expect(html).not.toContain("browser-port");
    expect(html).not.toContain("browser-path");
  });
});
