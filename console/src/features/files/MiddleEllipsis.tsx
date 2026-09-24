import { splitMiddle } from "./middleEllipsis.ts";

/** A name that truncates in the middle ("SMS-A2P…v2.0.0.pdf"); see splitMiddle. */
export function MiddleEllipsis({ text }: { text: string }) {
  const [head, tail] = splitMiddle(text);
  return (
    <>
      {head ? <span className="mid-head">{head}</span> : null}
      <span className="mid-tail">{tail}</span>
    </>
  );
}
