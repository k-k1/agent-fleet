// DeviceCodeView — the device-authorization step of an AWS IAM Identity Center login: the
// code to compare, a "Sign in" button the member opens by hand, and the warning. Shared by
// the SSM session login and af-aws-exec's Console login (ADR 0102). The URL is opened only on
// the member's press, never automatically: the member must compare the code first
// (device-code phishing guard).
import { Button } from "../../ui/Button.tsx";
import { useT } from "../../lib/i18n/index.ts";

export function DeviceCodeView({ url, code, hint }: { url: string; code: string; hint: string }) {
  const tr = useT();
  return (
    <>
      <p className="ui-field-hint">{hint}</p>
      {code && (
        <div className="ssm-code-row">
          <span className="ui-field-label">{tr("sx.ssm_code_label")}</span>
          <span className="ssm-code">{code}</span>
        </div>
      )}
      <div>
        <Button variant="primary" icon="link-external" disabled={!url} onClick={() => url && window.open(url, "_blank", "noopener")}>
          {tr("sx.ssm_sign_in")}
        </Button>
      </div>
      <p className="ui-field-hint">{tr("sx.ssm_warn")}</p>
    </>
  );
}
