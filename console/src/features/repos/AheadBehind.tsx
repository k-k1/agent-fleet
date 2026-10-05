// AheadBehind renders the ↑N / ↓N parts of a `.repo-chip.ab`, one element each, so the
// chip's flex gap spaces them. A literal space takes the width of whichever font the run
// falls into, and adjacent text merges into one flex item that the gap never reaches.
export function AheadBehind({ ahead, behind }: { ahead?: number; behind?: number }) {
  return (
    <>
      {ahead ? <span><span className="ab-arrow">↑</span>{ahead}</span> : null}
      {behind ? <span><span className="ab-arrow">↓</span>{behind}</span> : null}
    </>
  );
}
