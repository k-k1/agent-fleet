import { useEffect, useRef, useState } from "react";
import { useT, type MsgKey } from "../../../lib/i18n/index.ts";
import { AWS_REGIONS } from "../../../lib/awsRegions.ts";

// Never a region or zone: those are lowercase letters, digits and dashes.
const OTHER = "*other*";

// The i18n families a picker draws its labels from: `<prefix>_other` and
// `<prefix>_other_placeholder`, plus `<prefix>.<code>` per option when the options are named.
type MsgPrefix = "ssm.region" | "gcp.region" | "gcp.zone";

// RegionSelect picks a code from `options` (AWS_REGIONS unless given), with an "Other" entry
// that reveals a text input for codes the list lacks (GovCloud, China, regions newer than the
// list). A stored value outside the list opens in Other with the value shown, so editing a row
// never replaces or drops it. emptyLabel names the "" choice (unset / profile default /
// "select"). onChange also says where the value came from — the dropdown ("list") or the
// Other input ("other") — for a caller whose rule depends on it. `named` options read
// "<code> — <name>"; unnamed ones (zones) show the code alone.
export function RegionSelect({
  value,
  onChange,
  emptyLabel,
  className = "cinput",
  options = AWS_REGIONS,
  msgPrefix = "ssm.region",
  named = true,
}: {
  value: string;
  onChange: (v: string, from: "list" | "other") => void;
  emptyLabel: string;
  className?: string;
  options?: readonly string[];
  msgPrefix?: MsgPrefix;
  named?: boolean;
}) {
  const tr = useT();
  const unlisted = (v: string) => v.trim() !== "" && !options.includes(v.trim());
  const [other, setOther] = useState(() => unlisted(value));
  // A value the parent sets on its own (a reset, or a profile pick that brings its region)
  // decides the mode again; one this component just emitted must not, or typing a listed
  // code into Other would yank the input away mid-word.
  const emitted = useRef(value);
  useEffect(() => {
    if (value === emitted.current) return;
    emitted.current = value;
    setOther(unlisted(value));
  }, [value]);
  // When the list itself changes (the zone picker following its region), a kept value that
  // left the list moves to Other rather than leaving the dropdown pointing at no option.
  // Never the other way: a value just typed into Other stays there.
  const shown = useRef(options);
  useEffect(() => {
    if (options === shown.current) return;
    shown.current = options;
    if (unlisted(value)) setOther(true);
  }, [options]);
  const emit = (v: string, from: "list" | "other") => {
    emitted.current = v;
    onChange(v, from);
  };

  return (
    <div className="region-select">
      <select
        className={className}
        value={other ? OTHER : value.trim()}
        onChange={(e) => {
          if (e.target.value === OTHER) {
            setOther(true);
            return;
          }
          setOther(false);
          emit(e.target.value, "list");
        }}
      >
        <option value="">{emptyLabel}</option>
        {options.map((code) => (
          <option key={code} value={code}>
            {named ? `${code} — ${tr(`${msgPrefix}.${code}` as MsgKey)}` : code}
          </option>
        ))}
        <option value={OTHER}>{tr(`${msgPrefix}_other`)}</option>
      </select>
      {other && (
        <input
          className={className}
          placeholder={tr(`${msgPrefix}_other_placeholder`)}
          value={value}
          onChange={(e) => emit(e.target.value, "other")}
          autoFocus={!unlisted(value)}
        />
      )}
    </div>
  );
}
