import type { ReactNode } from "react";
import Icon from "./Icon.jsx";

// EmptyState is the shared "nothing here / loading" placeholder for the left-pane
// lists (Sessions / Repos / Files) and the Source Control changes list, so empty and
// loading states read consistently instead of each spot inventing its own muted text
// or bare "…". Renders as an <li> by default so it drops straight into a <ul>; pass
// as="div" for non-list contexts. A `loading` flag swaps the icon for a spinner; an
// optional `action` renders a call-to-action button (e.g. 新規 / クローン).
interface EmptyStateAction {
  label: string;
  icon?: string;
  onClick: () => void;
  disabled?: boolean;
}

interface EmptyStateProps {
  message: ReactNode;
  icon?: string;
  hint?: ReactNode;
  loading?: boolean;
  action?: EmptyStateAction;
  as?: "li" | "div";
}

export default function EmptyState({
  message,
  icon = "info",
  hint,
  loading = false,
  action,
  as = "li",
}: EmptyStateProps) {
  const Tag = as;
  return (
    <Tag className="empty-state">
      <Icon name={loading ? "loading" : icon} spin={loading} className="empty-ic" />
      <div className="empty-text">
        <div className="empty-msg">{message}</div>
        {hint && <div className="empty-hint">{hint}</div>}
      </div>
      {action && (
        <button type="button" className="empty-cta" disabled={action.disabled} onClick={action.onClick}>
          {action.icon && <Icon name={action.icon} />}
          {action.label}
        </button>
      )}
    </Tag>
  );
}
