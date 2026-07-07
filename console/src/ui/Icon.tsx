// Icon — codicon wrapper (name → span.codicon). `spin` for loading spinners.
interface IconProps {
  name: string;
  spin?: boolean;
  className?: string;
  title?: string;
}

export function Icon({ name, spin, className, title }: IconProps) {
  return (
    <span
      className={
        `codicon codicon-${name}` + (spin ? " codicon-spin" : "") + (className ? " " + className : "")
      }
      title={title}
      aria-hidden={title ? undefined : "true"}
    />
  );
}
