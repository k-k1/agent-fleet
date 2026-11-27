import { Icon } from "../../ui/Icon.tsx";

// Jira can localize standard names. Match documented aliases exactly so renamed
// types keep their own label and receive the neutral glyph.
function jiraWorkType(type?: string) {
  switch (type?.trim().toLowerCase()) {
    case "task":
    case "タスク":
      return { icon: "check", style: "wi-jira-task" };
    case "bug":
    case "バグ":
      return { icon: "bug", style: "wi-jira-bug" };
    case "story":
    case "ストーリー":
      return { icon: "bookmark", style: "wi-jira-story" };
    case "epic":
    case "エピック":
      return { icon: "zap", style: "wi-jira-epic" };
    case "subtask":
    case "sub-task":
    case "サブタスク":
      return { icon: "layers", style: "wi-jira-subtask" };
    default:
      return { icon: "issues", style: "wi-jira-unknown" };
  }
}

export function JiraWorkTypeIcon({ type }: { type?: string }) {
  const { icon, style } = jiraWorkType(type);
  return (
    <span className={`wi-dot wi-jira-type ${style}`} title={type || undefined}>
      <Icon name={icon} />
    </span>
  );
}
