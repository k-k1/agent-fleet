import { Icon } from "../../ui/Icon.tsx";

export function WorkItemStateIcon({ name }: { name: string }) {
  if (name === "issue-in-progress") {
    return <Icon name="circle-large-outline" className="wi-issue-progress" />;
  }
  return <Icon name={name} />;
}
