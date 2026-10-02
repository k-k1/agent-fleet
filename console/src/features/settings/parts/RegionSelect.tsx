import { useEffect, useRef, useState } from "react";
import { useT, type MsgKey } from "../../../lib/i18n/index.ts";
import { AWS_REGIONS, isListedAwsRegion } from "../../../lib/awsRegions.ts";

// Never a region code: codes are lowercase letters, digits and dashes.
const OTHER = "*other*";

// RegionSelect picks an AWS region from AWS_REGIONS, with an "Other" entry that reveals a
// text input for codes the list lacks (GovCloud, China, regions newer than the list). A
// stored value outside the list opens in Other with the value shown, so editing a row never
// replaces or drops it. emptyLabel names the "" choice (unset / profile default / "select").
export function RegionSelect({
  value,
  onChange,
  emptyLabel,
  className = "cinput",
}: {
  value: string;
  onChange: (v: string) => void;
  emptyLabel: string;
  className?: string;
}) {
  const tr = useT();
  const unlisted = (v: string) => v.trim() !== "" && !isListedAwsRegion(v.trim());
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
  const emit = (v: string) => {
    emitted.current = v;
    onChange(v);
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
          emit(e.target.value);
        }}
      >
        <option value="">{emptyLabel}</option>
        {AWS_REGIONS.map((code) => (
          <option key={code} value={code}>
            {code} — {tr(`ssm.region.${code}` as MsgKey)}
          </option>
        ))}
        <option value={OTHER}>{tr("ssm.region_other")}</option>
      </select>
      {other && (
        <input
          className={className}
          placeholder={tr("ssm.region_other_placeholder")}
          value={value}
          onChange={(e) => emit(e.target.value)}
          autoFocus={!unlisted(value)}
        />
      )}
    </div>
  );
}
