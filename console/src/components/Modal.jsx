import Icon from "./Icon.jsx";

// Modal is the shared dialog shell: a backdrop, a centered panel, and a header with
// a title + close button. Every modal in the app used to repeat this markup. The
// backdrop closes on click unless `lockClose` (e.g. a clone/checkout in flight).
// Pass `as="form"` + `onSubmit` for form dialogs; `className` adds a size variant
// (session-modal / branch-modal / settings-modal). Children are the body/footer.
export default function Modal({ title, onClose, className = "", as = "div", onSubmit, lockClose = false, children }) {
  const Panel = as;
  const panelProps = { className: ("modal " + className).trim(), onClick: (e) => e.stopPropagation() };
  if (as === "form") panelProps.onSubmit = onSubmit;
  return (
    <div className="modal-backdrop" onClick={lockClose ? undefined : onClose}>
      <Panel {...panelProps}>
        <header className="modal-head">
          <h3 className="modal-title">{title}</h3>
          <button type="button" className="icon" title="閉じる" onClick={onClose}>
            <Icon name="close" />
          </button>
        </header>
        {children}
      </Panel>
    </div>
  );
}
