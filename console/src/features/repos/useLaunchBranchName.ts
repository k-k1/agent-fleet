// The launch modal's side of the branch-name resolver (ADR 0103 decision 8): a work-item launch
// asks the Agent for the name and the base, and every typed name gets the advisory check.
import { useCallback, useEffect, useRef, useState } from "react";
import { baseSource, checkBranchName, fetchBranchName, fetchBranchRule, mergeWarnings } from "./branchRule.ts";
import type { BranchItem, BranchName, BranchWarning } from "./branchRule.ts";

const CHECK_DELAY_MS = 400;

/** When a provisional answer is asked again, in ms after that first answer (ADR 0103 decision
 * 4). The Agent makes the English slug for a non-ASCII title in the background and never waits
 * for it; measured on a deployed Agent it arrived 20–80 s later, so one re-ask at 8 s almost
 * always got the deterministic name again. A few asks with back-off, then the provisional name
 * stays: the modal does not poll for as long as it is open. Mutable so the tests can shorten it. */
export const launchBranchTiming = { reaskAtMs: [8000, 20000, 45000] };

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
  /** Resolve again, e.g. after Initialize Git Flow wrote a declaration. */
  resolveAgain: () => void;
  /** The name in the field is the resolver's provisional one and may still change to the
   * English slug: false once the person edited it, a final answer arrived, or the re-asks ran
   * out. */
  provisional: boolean;
  /** Mark a field as the person's own: a later answer no longer overwrites it. */
  touchName: () => void;
  touchBase: () => void;
  /** Where the rules took the base from; "" once the person typed their own base, which the
   * launch then uses as given (decision 5). */
  baseSource: string;
}

export function useLaunchBranchName({ repo, item, name, setName, setBase }: Options): LaunchBranchName {
  const [resolved, setResolved] = useState<BranchName | null>(null);
  const [rereading, setRereading] = useState(false);
  const nameTouched = useRef(false);
  const baseTouched = useRef(false);
  const [baseEdited, setBaseEdited] = useState(false);
  const [nameEdited, setNameEdited] = useState(false);
  // A provisional answer is being asked again, so the name may still change.
  const [reasking, setReasking] = useState(false);
  const seq = useRef(0);
  // The pending re-ask's timer, cleared on close, on another item and on an edit of the name,
  // so nothing fires into a closed modal.
  const reaskTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
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
    const apply = (r: BranchName) => {
      setResolved(r);
      // A name the resolver cannot make (name_empty) falls back to the server-minted temp/<slug>
      // at launch (decision 4), which is what an empty field asks for.
      if (!nameTouched.current) setNameRef.current(r.name_empty ? "" : r.name);
      if (!baseTouched.current && r.base_branch) setBaseRef.current(r.base_branch);
    };
    clearTimeout(reaskTimer.current);
    const r = await fetchBranchName(repo, { item: it });
    if (my !== seq.current || !r) return;
    apply(r);
    // A provisional name carries the deterministic slug while the English one is being made.
    // Asked again on a short schedule, stopping at the first final answer, when the person
    // edits the name (a late answer must not replace theirs), or when the modal closes or
    // moves to another item (seq moved on).
    if (!r.provisional || nameTouched.current) return;
    setReasking(true);
    try {
      let waited = 0;
      for (const at of launchBranchTiming.reaskAtMs) {
        const ms = Math.max(0, at - waited);
        waited = at;
        await new Promise((done) => (reaskTimer.current = setTimeout(done, ms)));
        if (my !== seq.current || nameTouched.current) return;
        const again = await fetchBranchName(repo, { item: it });
        if (my !== seq.current || !again) return;
        apply(again);
        if (!again.provisional) return;
      }
    } finally {
      // A superseded ask leaves the flag to the one that replaced it.
      if (my === seq.current) setReasking(false);
    }
  }, [repo]);

  const itemKey = item?.key ?? "";
  useEffect(() => {
    if (!itemKey) return;
    void resolve();
    return () => {
      seq.current++;
      clearTimeout(reaskTimer.current);
    };
  }, [itemKey, resolve]);

  const reread = useCallback(() => {
    setRereading(true);
    // The refresh waits up to the Agent's own 3 s for Bitbucket, then the name is asked again —
    // unless the target changed or the modal closed meanwhile (seq moved on): resolving then
    // would answer for the old repository and overwrite the new one's name and base.
    const gen = seq.current;
    void fetchBranchRule(repo, true)
      .then(() => {
        if (seq.current === gen) return resolve();
      })
      .finally(() => setRereading(false));
  }, [repo, resolve]);

  const checked = useBranchCheck(repo, name);

  // The resolver's own bad_ref is about the name it proposed; the check covers whatever is in
  // the field now, so it is the one that speaks for the name. Likewise base_missing is about the
  // base the rules picked, which a typed base replaces.
  const warnings = mergeWarnings(
    resolved?.warnings.filter((w) => w.code !== "bad_ref" && !(baseEdited && w.code === "base_missing")),
    checked,
  );

  return {
    resolved,
    warnings,
    rereading,
    reread,
    resolveAgain: () => void resolve(),
    provisional: reasking && !!resolved?.provisional && !nameEdited,
    touchName: () => {
      nameTouched.current = true;
      clearTimeout(reaskTimer.current);
      setNameEdited(true);
    },
    touchBase: () => {
      baseTouched.current = true;
      setBaseEdited(true);
    },
    baseSource: baseEdited ? "" : baseSource(resolved?.sources),
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
