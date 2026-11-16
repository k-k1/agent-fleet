// The Model access pages' rule helpers (ADR 0138 M2 P8). Pinned here:
//   1. The vocabulary equals the route catalogue the CP embeds, so an editor never offers a value
//      the CP refuses or misses one it accepts.
//   2. A save body is the loaded rule with only the edited fields replaced: a field the page does
//      not edit survives, and "every class" (absent) never becomes "no class" ([]) or the reverse.
import { existsSync, readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import {
  ROUTE_CATALOG,
  ROUTE_CLASSES,
  ROUTE_DESTINATIONS,
  draftOf,
  parseTenantPolicy,
  ruleEqual,
  ruleFromDraft,
  routesAllowedAbove,
} from "./routePolicyRule.ts";

const CATALOG = new URL("../../../../../control-plane/internal/agentroutes/catalog.json", import.meta.url);
const haveCatalog = existsSync(CATALOG);
const catalog: {
  routes: { id: string; class: string; destination: string; perConnection?: boolean }[];
  destinations: { id: string }[];
} = haveCatalog ? JSON.parse(readFileSync(CATALOG, "utf8")) : { routes: [], destinations: [] };

describe.skipIf(!haveCatalog)("vocabulary against the CP's route catalogue", () => {
  it("lists every catalogued route with its class, destination and per-connection mark", () => {
    expect(ROUTE_CATALOG.map((r) => ({ ...r, perConnection: !!r.perConnection }))).toEqual(
      catalog.routes
        .map((r) => ({ id: r.id, class: r.class, destination: r.destination, perConnection: !!r.perConnection }))
        .sort((a, b) => a.id.localeCompare(b.id)),
    );
  });

  it("offers exactly the catalogue's classes and its destinations except unclassified", () => {
    expect([...ROUTE_CLASSES].sort()).toEqual([...new Set(catalog.routes.map((r) => r.class))].sort());
    expect([...ROUTE_DESTINATIONS].sort()).toEqual(
      catalog.destinations
        .map((d) => d.id)
        .filter((d) => d !== "unclassified")
        .sort(),
    );
  });
});

describe("ruleFromDraft", () => {
  it("sends the loaded rule back unchanged when nothing was edited", () => {
    const loaded = { classes: ["member-key"], deny: ["openai-api"], endpoints: ["*.corp.example"], future: { x: 1 } };
    expect(ruleFromDraft(loaded, draftOf(loaded))).toEqual(loaded);
  });

  it("keeps absent apart from empty", () => {
    const all = ruleFromDraft({}, draftOf({}));
    expect("classes" in all).toBe(false);
    expect("endpoints" in all).toBe(false);
    const none = ruleFromDraft({}, { ...draftOf({}), classesAll: false, endpointsAll: false });
    expect(none.classes).toEqual([]);
    expect(none.endpoints).toEqual([]);
  });

  it("replaces only the edited field and drops an emptied exception list", () => {
    const loaded = { destinations: ["aws-bedrock"], except: ["copilot-login"], endpoints: ["llm.corp.example"] };
    const d = { ...draftOf(loaded), except: [] as string[], classesAll: false, classes: ["provided"] };
    expect(ruleFromDraft(loaded, d)).toEqual({
      classes: ["provided"],
      destinations: ["aws-bedrock"],
      endpoints: ["llm.corp.example"],
    });
  });

  it("splits endpoint text on lines, spaces and commas", () => {
    const d = { ...draftOf({}), endpointsAll: false, endpoints: " a.corp.example\nb.corp.example, c.corp.example \n\n" };
    expect(ruleFromDraft({}, d).endpoints).toEqual(["a.corp.example", "b.corp.example", "c.corp.example"]);
  });
});

describe("ruleEqual", () => {
  it("ignores order, not absence", () => {
    expect(ruleEqual({ classes: ["a", "b"] }, { classes: ["b", "a"] })).toBe(true);
    expect(ruleEqual({}, { classes: [] })).toBe(false);
    expect(ruleEqual({ except: [] }, {})).toBe(true);
  });
});

describe("routesAllowedAbove", () => {
  it("keeps a route only the tenant's own rule forbids", () => {
    const routes = [
      { id: "a", class: "c", destination: "d", allowed: true },
      { id: "b", class: "c", destination: "d", allowed: false, forbiddenBy: ["tenant"] },
      { id: "c", class: "c", destination: "d", allowed: false, forbiddenBy: ["ceiling", "tenant"] },
    ];
    expect(routesAllowedAbove(routes).map((r) => r.id)).toEqual(["a", "b"]);
  });
});

describe("parseTenantPolicy", () => {
  it("refuses a partial answer", () => {
    expect(parseTenantPolicy({ ceiling: { raw: "", rule: {}, source: "deployment" } })).toBeNull();
    expect(parseTenantPolicy({ error: { code: "forbidden" } })).toBeNull();
  });
});

describe("parseHidden", () => {
  it("accepts only all three well-formed lists", async () => {
    const { parseHidden } = await import("./routePolicyRule.ts");
    expect(parseHidden({ kinds: [], chat: [], imagegen: [] })).not.toBeNull();
    expect(parseHidden(undefined)).toBeNull();
    expect(parseHidden({ kinds: [], chat: [] })).toBeNull();
    expect(parseHidden({ kinds: [{ id: "agy", routes: ["agy-login"] }], chat: [], imagegen: [] })).toBeNull();
  });
});
