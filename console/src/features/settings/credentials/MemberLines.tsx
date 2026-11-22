import { useCallback, useEffect, useState } from "react";
import { errDetail, type ApiError } from "../../../core/api/client.ts";
import { getUnusableConnections, removeUnusableConnections } from "./credentialsApi.ts";
import { useToast } from "../../../ui/ToastProvider.tsx";
import { useConfirm } from "../../../ui/ConfirmProvider.tsx";
import { Button } from "../../../ui/Button.tsx";
import { useT } from "../../../lib/i18n/index.ts";
import { setSettings, waitPrefsSaved } from "../../../lib/settings.ts";

// The two lines a member's credential screens carry under ADR 0138. Neither names a layer or a
// hidden item (D10a): the Agent answers with a count and booleans only, and so do these.

/** GET /connections/unusable: how many stored credentials the policy forbids on every route, and
 *  the token that stands for exactly those (their bindings at this moment). */
interface UnusableAnswer {
  count: number;
  token?: string;
}

/** POST /connections/unusable/remove: what happened to each counted credential, and the line
 *  recomputed afterwards. */
interface UnusableRemoveAnswer extends UnusableAnswer {
  removed: number;
  skipped: number;
  failed: number;
}

const isUnusable = (v: unknown): v is UnusableAnswer =>
  !!v && typeof v === "object" && typeof (v as UnusableAnswer).count === "number";

/**
 * UnusableLine is D14a's line: shown only while stored credentials exist that the policy forbids
 * on every route, without listing them, with one action that removes them after a confirmation.
 * The removal sends the token it was shown with, so only the credentials counted then — and still
 * present, unchanged and forbidden when the Agent re-checks — are removed.
 */
export function UnusableLine({ running }: { running: boolean }) {
  const tr = useT();
  const toast = useToast();
  const confirm = useConfirm();
  const [offer, setOffer] = useState<UnusableAnswer | null>(null);
  const [busy, setBusy] = useState(false);

  const load = useCallback(() => {
    getUnusableConnections()
      .then((r) => setOffer(isUnusable(r) ? r : null))
      .catch(() => setOffer(null));
  }, []);
  useEffect(() => {
    if (!running) {
      setOffer(null);
      return;
    }
    load();
  }, [running, load]);

  if (!running || !offer || offer.count <= 0 || !offer.token) return null;

  const token = offer.token;
  const remove = async () => {
    const ok = await confirm({
      title: tr("cred.unusable_confirm_title"),
      body: tr("cred.unusable_confirm_body", { count: offer.count }),
      confirmLabel: tr("cred.unusable_remove"),
      danger: true,
    });
    if (!ok) return;
    setBusy(true);
    try {
      let r: (UnusableRemoveAnswer & { error?: ApiError }) | null;
      try {
        r = (await removeUnusableConnections(token)) as typeof r;
      } catch (e) {
        // A network failure: whether anything was removed is unknown, so ask the Agent again.
        r = { error: { code: "network", message: e instanceof Error ? e.message : String(e) } } as typeof r;
      }
      if (!r || r.error || typeof r.removed !== "number") {
        toast(tr("cred.unusable_remove_failed", { msg: errDetail(r?.error) }));
        load();
        return;
      }
      const done = r;
      toast(
        done.skipped > 0 || done.failed > 0
          ? tr("cred.unusable_removed_partly", { removed: done.removed, left: done.skipped + done.failed })
          : tr("cred.unusable_removed", { removed: done.removed }),
      );
      setOffer(isUnusable(done) ? { count: done.count, token: done.token } : null);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="agents-ws-hint cred-line" data-testid="cred-unusable-line">
      <p className="muted ds-note">{tr("cred.unusable_line")}</p>
      <Button icon="trash" disabled={busy} onClick={() => void remove()}>
        {tr("cred.unusable_remove")}
      </Button>
    </div>
  );
}

/**
 * OwnSettingsLine is shown only while the member's own switches hide something the policy allows
 * (hidden, from the page's fresh GET /agents/kinds): "Some agents or credentials are hidden by your
 * own settings", with Reset my settings. The reset asks first, clears exactly the member layer —
 * every harness's on/off (agentEnabled) and llama.cpp's own switch — and nothing the policy
 * decides. The Agent applies the member layer from the saved ui-prefs, so the page refreshes what
 * the Agent shows (onReset) only once the save has landed, and says so when it has not.
 */
export function OwnSettingsLine({
  hidden,
  onReset,
  line = "cred.own_settings_line",
}: {
  hidden: boolean;
  onReset: () => void | Promise<void>;
  /** The sentence before the button: the launch dialog says every agent is switched off. */
  line?: "cred.own_settings_line" | "launch.all_off_line";
}) {
  const tr = useT();
  const toast = useToast();
  const confirm = useConfirm();
  const [busy, setBusy] = useState(false);
  if (!hidden) return null;

  const reset = async () => {
    const ok = await confirm({
      title: tr("cred.own_settings_confirm_title"),
      body: tr("cred.own_settings_confirm_body"),
      confirmLabel: tr("cred.own_settings_reset"),
    });
    if (!ok) return;
    setBusy(true);
    try {
      setSettings({ agentEnabled: {}, lcppEnabled: true });
      if (!(await waitPrefsSaved())) {
        toast(tr("cred.own_settings_reset_failed"));
        return;
      }
      await onReset();
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="agents-ws-hint cred-line" data-testid="cred-own-settings-line">
      <p className="muted ds-note">{tr(line)}</p>
      <Button icon="refresh" disabled={busy} onClick={() => void reset()}>
        {tr("cred.own_settings_reset")}
      </Button>
    </div>
  );
}
