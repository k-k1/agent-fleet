// The launch modal's side of the branch-name resolver (ADR 0103 decision 8): a work-item launch
// asks the Agent for the name and the base, and every typed name gets the advisory check.
import { useCallback, useEffect, useRef, useState } from "react";
import { checkBranchName, fetchBranchName, fetchBranchRule, mergeWarnings } from "./branchRule.ts";
import type { BranchItem, BranchName, BranchWarning } from "./branchRule.ts";

const CHECK_DELAY_MS = 400;

interface Options {
  repo: string;
  /** The work item the launch came from; absent for every other launch, which keeps the
   * server-minted temp/<slug> and only gets the check. */
  item?: BranchItem;
  name: string;
  setName: (v: string) => void;
  setBase: (v: string) => void;
}

export interface LaunchBranchName {
  /** The resolver's answer for the item; null before it arrives, without an item, or when the
   * Agent has no resolver (the Console's branchForItem suggestion then stays in the field). */
  resolved: BranchName | null;
  warnings: BranchWarning[];
  rereading: boolean;
  /** Read Bitbucket's branching model again and re-resolve (the "pending" case). */
  reread: () => void;
  /** Mark a field as the person's own: a later answer no longer overwrites it. */
  touchName: () => void;
  touchBase: () => void;
}

export function useLaunchBranchName({ repo, item, name, setName, setBase }: Options): LaunchBranchName {
  const [resolved, setResolved] = useState<BranchName | null>(null);
  const [rereading, setRereading] = useState(false);
  const nameTouched = useRef(false);
  const baseTouched = useRef(false);
  const seq = useRef(0);
  const itemRef = useRef(item);
  itemRef.current = item;
  const setNameRef = useRef(setName);
  setNameRef.current = setName;
  const setBaseRef = useRef(setBase);
  setBaseRef.current = setBase;

  const resolve = useCallback(async () => {
    const it = itemRef.current;
    if (!it) return;
    const my = ++seq.current;
    const r = await fetchBranchName(repo, { item: it });
    if (my !== seq.current || !r) return;
    setResolved(r);
    // A name the resolver cannot make (name_empty) falls back to the server-minted temp/<slug>
    // at launch (decision 4), which is what an empty field asks for.
    if (!nameTouched.current) setNameRef.current(r.name_empty ? "" : r.name);
    if (!baseTouched.current && r.base_branch) setBaseRef.current(r.base_branch);
  }, [repo]);

  const itemKey = item?.key ?? "";
  useEffect(() => {
    if (!itemKey) return;
    void resolve();
    return () => {
      seq.current++;
    };
  }, [itemKey, resolve]);

  const reread = useCallback(() => {
    setRereading(true);
    // The refresh waits up to the Agent's own 3 s for Bitbucket, then the name is asked again.
    void fetchBranchRule(repo, true)
      .then(() => resolve())
      .finally(() => setRereading(false));
  }, [repo, resolve]);

  const checked = useBranchCheck(repo, name);

  // The resolver's own bad_ref is about the name it proposed; the check covers whatever is in
  // the field now, so it is the one that speaks for the name.
  const warnings = mergeWarnings(
    resolved?.warnings.filter((w) => w.code !== "bad_ref"),
    checked,
  );

  return {
    resolved,
    warnings,
    rereading,
    reread,
    touchName: () => {
      nameTouched.current = true;
    },
    touchBase: () => {
      baseTouched.current = true;
    },
  };
}

/** The advisory check of a typed name (POST …/branch-name/check), shortly after typing stops.
 * Empty for an empty name, an empty repo, and an Agent without the resolver. */
export function useBranchCheck(repo: string, name: string): BranchWarning[] {
  const [checked, setChecked] = useState<BranchWarning[]>([]);
  const typed = name.trim();
  useEffect(() => {
    if (!typed || !repo) {
      setChecked([]);
      return;
    }
    let alive = true;
    const t = setTimeout(() => {
      void checkBranchName(repo, typed).then((ws) => {
        if (alive) setChecked(ws ?? []);
      });
    }, CHECK_DELAY_MS);
    return () => {
      alive = false;
      clearTimeout(t);
    };
  }, [repo, typed]);
  return checked;
}
