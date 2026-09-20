// P1 runs S-LOGIC (console/src/lib/fleetgraph.ts) in its own worktree, in parallel with
// this lane — so this repo may not have that file yet, and FleetGraphView.tsx draws from
// ./fixture.ts and ./geometry.ts in the meantime (docs/log/101 §101.6, decision explicitly
// permits this for P1). Once lib/fleetgraph.ts lands (P2 integration), the view must import
// laneY/xOf/BuildFleetGraph from there instead — this is the trip-wire: it fails the
// instant that file exists on disk, so "merged S-LOGIC but forgot to swap the import"
// cannot pass CI silently forever.
//
// Checks existence only, not the module's exports/shape — importing a module that may not
// exist would break typecheck/build for every OTHER worktree still running P1, which is
// the opposite of what a trip-wire is for.
import { describe, expect, it } from "vitest";
import { existsSync } from "node:fs";
import { fileURLToPath } from "node:url";

describe("fleetgraph stub removal", () => {
  it("fails once lib/fleetgraph.ts exists, to force the P2 swap away from the fixture/geometry stand-ins", () => {
    const libPath = fileURLToPath(new URL("../../lib/fleetgraph.ts", import.meta.url));
    expect(
      existsSync(libPath),
      "lib/fleetgraph.ts now exists: swap FleetGraphView.tsx's imports from ./fixture.ts " +
        "and ./geometry.ts to lib/fleetgraph.ts's BuildFleetGraph/laneY/xOf, then delete " +
        "fixture.ts, geometry.ts and this test.",
    ).toBe(false);
  });
});
