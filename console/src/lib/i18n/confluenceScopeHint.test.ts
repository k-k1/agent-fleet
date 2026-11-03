// The tenant admin hint under the Jira card's Confluence checkbox has to name every scope the
// control plane requests; a scope missing from it leaves the admin's app short and Atlassian
// refuses every member's authorization. The Go constant is read straight from its source file.
import { describe, it, expect } from "vitest";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { admin as en } from "./locales/en/admin.ts";
import { admin as ja } from "./locales/ja/admin.ts";

function confluenceScopes(): string[] {
  const src = readFileSync(resolve(process.cwd(), "../control-plane/oauth_jira.go"), "utf8");
  const m = src.match(/confluenceScopes\s*=\s*((?:"[^"\n]*"\s*\+?\s*)+)/);
  if (!m) throw new Error("confluenceScopes constant not found in oauth_jira.go");
  const joined = [...m[1].matchAll(/"([^"\n]*)"/g)].map((x) => x[1]).join("");
  return joined.split(/\s+/).filter(Boolean);
}

describe("tenant.git_oauth_scope_group_confluence_hint", () => {
  const scopes = confluenceScopes();
  it("reads all ten scopes from the control plane", () => {
    expect(scopes).toHaveLength(10);
    expect(scopes).toContain("read:page:confluence");
  });
  for (const [name, cat] of [["en", en], ["ja", ja]] as const) {
    it(`${name} lists every scope the control plane requests`, () => {
      const hint = (cat as Record<string, string>)["tenant.git_oauth_scope_group_confluence_hint"];
      for (const s of scopes) expect(hint, s).toContain(s);
    });
  }
});
