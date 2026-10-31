import { useCallback, useEffect, useRef, useState } from "react";
import type { ChangeEvent, ReactNode, RefObject } from "react";
import { api, raw, rawJSON } from "../../../core/api/client.ts";
import { Icon } from "../../../ui/Icon.tsx";
import { useConfirm } from "../../../ui/ConfirmProvider.tsx";
import { useToast } from "../../../ui/ToastProvider.tsx";
import { useSettings, setSetting } from "../../../lib/settings.ts";
import { SSM_HOST_COLORS, hostColorBase, termBackground } from "../../../lib/termcolor.ts";
import { useT, t, type MsgKey } from "../../../lib/i18n/index.ts";
import { Field, Meta } from "../parts/mcpForm.tsx";
import { RegionSelect } from "../parts/RegionSelect.tsx";
import { ProfileLoginModal, type LoginProfile } from "../../awslogin/ProfileLoginModal.tsx";
import { useProfileLogout } from "../../awslogin/useProfileLogout.tsx";
import { awsProfileName, validExternalId, validRoleArn, validSessionName } from "../../../lib/awsRoleChain.ts";
import { useAwsLoginStore } from "../../awslogin/store.ts";
import { useSettingsUI } from "../store.ts";
import { AwsConfigImport } from "./AwsConfigImport.tsx";
import { SshConfigImport } from "./SshConfigImport.tsx";
import { AwsConfigExport } from "./AwsConfigExport.tsx";
import { RecentCloudRuns } from "../../cloudruns/cloudRuns.tsx";
import { SIGN_IN_NAME_RE, groupable, samePortal, signInSaveError, toCandidate, type SsmSignIn } from "./ssmSignIns.ts";
import { ShareSignIn } from "./ShareSignIn.tsx";

// SsmTab manages the member's own AWS profiles and SSM hosts (docs/log/p3-ssm-session.md)
// in two tiers so the form isn't cluttered:
//   profile (shared) = the shared auth bundle (SSO portal + account/role/region); many
//                      hosts reuse one. Maps to a ~/.aws named profile.
//   host (per-instance) = per-instance only (alias, instance id, run-as document,
//                      optional region override) + which profile to use.
// NO AWS secrets are stored or entered here — at session start the in-container aws CLI
// runs `aws sso login` (device-code URL surfaced in the terminal) and caches the
// short-lived token in the workspace home.

// postJSON POSTs/PUTs and surfaces failures loudly (a stale CP without the routes
// returns 404 non-JSON, which would otherwise be swallowed). Returns true on success.
export async function postJSON(path: string, method: string, body: unknown, toast: (msg: string) => void): Promise<boolean> {
  let res;
  try {
    res = await rawJSON(path, method, body);
  } catch (e: any) {
    toast(t("ssm.comm_failed", { msg: String(e?.message || e) }));
    return false;
  }
  if (!res.ok) {
    const j = await res.json().catch(() => null);
    // A sign-in refusal (ADR 0125) says what to do about it in the member's words.
    const known = signInSaveError(j?.error?.code);
    toast(known ? t(known) : t("ssm.save_failed_http", { status: res.status, detail: failDetailOf(res, j) }));
    return false;
  }
  return true;
}

// deleteRow DELETEs and, like postJSON, says why it failed. Callers clean up only on "ok":
// a refused delete leaves the row, and its form and marks with it. "in_use" is the server
// refusing a profile that hosts still use (409 ssm_profile_in_use), which means this
// page's host list is stale.
export async function deleteRow(path: string, toast: (msg: string) => void): Promise<"ok" | "in_use" | "failed"> {
  let res;
  try {
    res = await raw(path, { method: "DELETE" });
  } catch (e: any) {
    toast(t("ssm.comm_failed", { msg: String(e?.message || e) }));
    return "failed";
  }
  if (!res.ok) {
    const j = await res.json().catch(() => null);
    if (j?.error?.code === "ssm_profile_in_use" && Array.isArray(j.hosts)) {
      toast(profileInUse(j.hosts, Array.isArray(j.profiles) ? j.profiles : []));
      return "in_use";
    }
    toast(t("ssm.delete_failed_http", { status: res.status, detail: failDetailOf(res, j) }));
    return "failed";
  }
  return "ok";
}

function profileInUse(aliases: string[], dependents: string[] = []): string {
  if (dependents.length > 0) return t("ssm.profile_in_use_source", { n: dependents.length, profiles: dependents.join(", ") });
  return t("ssm.profile_in_use", { n: aliases.length, hosts: aliases.join(", ") });
}

function failDetailOf(res: Response, j: any): string {
  return j?.error?.message ? " — " + j.error.message : res.status === 404 ? t("ssm.save_failed_404") : "";
}

// Meta / Field reuse the shared primitives from mcpForm.tsx (they were identical).
// FieldGroup / Field build the labeled add-form: a bordered group holding fields that
// each carry a label, an optional required * marker, and a hint (where the value comes
// from and its format). Required vs optional is conveyed per-field (the * marker + hint),
// so one group suffices — no separate required / advanced boxes. `title` is optional
// (omitted for the single merged group).
export function FieldGroup({ title, optional, children }: { title?: ReactNode; optional?: ReactNode; children: ReactNode }) {
  return (
    <div className="ssm-fgroup">
      {title && (
        <div className="ssm-fg-title">
          {title}
          {optional && <span className="opt"> {optional}</span>}
        </div>
      )}
      <div className="ssm-fgrid">{children}</div>
    </div>
  );
}

export function SsmTab() {
  const tr = useT();
  const [profiles, setProfiles] = useState<any[] | null>(null);
  const [hosts, setHosts] = useState<any[] | null>(null);
  // The member's shared sign-ins (ADR 0125). A failed or older answer leaves none: the profiles
  // still list, each with the sign-in name the CP gives it.
  const [signIns, setSignIns] = useState<SsmSignIn[]>([]);
  const profileLabelRef = useRef<HTMLInputElement>(null);
  // The profile add-form open state is lifted here so the host form's "add a profile"
  // CTA (「プロファイルを追加」) can expand it (and scroll to it) when none exists yet.
  const [profileOpen, setProfileOpen] = useState(false);

  // Why the last GET of each list failed ("" when it succeeded).
  const [profilesErr, setProfilesErr] = useState("");
  const [hostsErr, setHostsErr] = useState("");

  // A failed GET keeps the last list that loaded (null if none ever did) and says so. Never
  // replace it with []: the sections read a row missing from a new list as deleted and close
  // its open form, so a transient failure would throw away an unsaved draft.
  const reload = useCallback(() => {
    const load = (path: string, set: (d: any[]) => void, setErr: (e: string) => void) =>
      api(path)
        .then((d) => {
          if (!Array.isArray(d)) throw new Error(d?.error?.message || "unexpected response");
          set(d);
          setErr("");
        })
        .catch((e: any) => setErr(String(e?.message || e)));
    load("api/ssm/profiles", setProfiles, setProfilesErr);
    load("api/ssm/hosts", setHosts, setHostsErr);
    api("api/ssm/sign-ins")
      .then((d) => {
        if (Array.isArray(d)) setSignIns(d.filter((x) => typeof x?.id === "string" && typeof x?.name === "string"));
      })
      .catch(() => {});
  }, []);
  useEffect(reload, [reload]);

  const focusProfile = () => {
    setProfileOpen(true);
    requestAnimationFrame(() => profileLabelRef.current?.scrollIntoView({ block: "center" }));
  };

  return (
    <div className="ssm-tab">
      <p className="field-help">
        {tr("ssm.intro_1")}
        <b>{tr("ssm.intro_bold")}</b>
        {tr("ssm.intro_2")}
        <code>aws sso login</code>
        {tr("ssm.intro_3")}
      </p>
      <ProfileSection
        profiles={profiles}
        signIns={signIns}
        loadErr={profilesErr}
        hosts={hosts}
        reload={reload}
        labelRef={profileLabelRef}
        open={profileOpen}
        setOpen={setProfileOpen}
      />
      <HostSection hosts={hosts} loadErr={hostsErr} profiles={profiles} reload={reload} onNeedProfile={focusProfile} />
      <RecentCloudRuns />
    </div>
  );
}

// --- profiles (common) ----------------------------------------------------------

// The Agent's login states (GET /api/aws-login/profiles). No time is shown: the cache knows
// only the access token's expiry, which the CLI renews until the portal session ends (#1029).
const STATE_KEYS: Record<string, { label: MsgKey; title: MsgKey }> = {
  signed_in: { label: "ssm.state_signed_in", title: "ssm.state_signed_in_title" },
  renew: { label: "ssm.state_renew", title: "ssm.state_renew_title" },
  none: { label: "ssm.state_none", title: "ssm.state_none_title" },
};

const emptyProfile: Record<string, string> = {
  kind: "sso",
  label: "",
  startUrl: "",
  ssoRegion: "",
  accountId: "",
  roleName: "",
  region: "",
  sourceProfileId: "",
  roleArn: "",
  externalId: "",
  sessionName: "",
  durationSeconds: "",
  // "true" when the profile asks for confirmation before use (ADR 0126); pick() stringifies.
  confirmBeforeUse: "",
  // The shared sign-in (ADR 0125): signInMode "" is a sign-in of its own (today's portal
  // fields), "join" one of the member's sign-ins named by signIn, "new" a new one of that name.
  signIn: "",
  signInMode: "",
  // "1" once the Sign-in choice was changed in this form: only then does a save send signIn.
  signInTouched: "",
};

// The assume-role profile type (issue #1109): no portal of its own, it assumes a role from
// the login of another Settings profile. Field names are the CP's (ssmProfileDTO).
const ASSUME_ROLE = "assume_role";
const isChained = (p: any): boolean => p?.kind === ASSUME_ROLE;

// The workspace keys a profile's sign-in by its AWS name (sso-session af-<name>), so an edit
// that changes it leaves the profile signed out under its new name.
export { awsProfileName };

// Profiles saved with a new workspace name or a new portal, which need a fresh login before
// the Agent's badge means anything: its states come from the last 5-minute poll and its token
// cache is keyed by name alone, so it can show a renamed row nothing and a re-pointed row the
// old portal's "Signed in". Module scope so closing and reopening Settings keeps them; cleared
// by a login from the row, which the Agent starts only after re-reading Settings.
const reloginNeeded = new Set<string>();

/** Test hook: forget the marks between cases (they outlive a mount on purpose). */
export function resetReloginMarks(): void {
  reloginNeeded.clear();
}

// pick copies a row's form fields as strings, so a field the CP left out edits as "".
export function pick(row: any, empty: Record<string, string>): Record<string, string> {
  const out: Record<string, string> = {};
  for (const k of Object.keys(empty)) out[k] = row?.[k] == null ? "" : String(row[k]);
  return out;
}

// authMoved: the edit points the profile at another portal (start URL or SSO region).
function authMoved(was: any, now: { startUrl: string; ssoRegion: string }): boolean {
  return now.startUrl !== String(was.startUrl || "") || now.ssoRegion !== String(was.ssoRegion || "");
}

type FieldEvent = ChangeEvent<HTMLInputElement | HTMLSelectElement>;

function ProfileSection({
  profiles,
  signIns,
  loadErr,
  hosts,
  reload,
  labelRef,
  open,
  setOpen,
}: {
  profiles: any[] | null;
  signIns: SsmSignIn[];
  loadErr: string;
  hosts: any[] | null;
  reload: () => void;
  labelRef: RefObject<HTMLInputElement | null>;
  open: boolean;
  setOpen: (v: boolean) => void;
}) {
  const tr = useT();
  const askConfirm = useConfirm();
  const toast = useToast();
  const [f, setF] = useState<Record<string, string>>(emptyProfile);
  const [busy, setBusy] = useState(false);
  // The row being edited; its form replaces the row's details. Never open together with
  // the add form, which shares f.
  const [editing, setEditing] = useState<any | null>(null);
  // ids: the profiles a login clears the relogin mark of (every member of a sign-in).
  // next: further members of a sign-in to log in through when the Agent refuses this one as not
  // exported; startNow: the member already pressed Log in.
  const [loginFor, setLoginFor] = useState<
    (LoginProfile & { id: string; ids?: string[]; signIn?: string; next?: (LoginProfile & { id: string })[]; startNow?: boolean }) | null
  >(null);
  // The sign-in whose name and portal are being edited on its header.
  const [editingSignIn, setEditingSignIn] = useState<SsmSignIn | null>(null);
  const [sf, setSf] = useState({ name: "", startUrl: "", ssoRegion: "" });
  const logout = useProfileLogout();
  const loggingOut = useAwsLoginStore((st) => st.loggingOut);
  // Bumped when reloginNeeded changes, which React cannot see.
  const [, setMarks] = useState(0);
  // The ~/.aws/config import (issue #3010); openSettings("ssm:import") opens it from elsewhere.
  const requested = useSettingsUI((s) => (s.settingsSection === "ssm" ? s.settingsView : null));
  const [importOpen, setImportOpen] = useState(false);
  const [sshImportOpen, setSshImportOpen] = useState(false);
  const [exportOpen, setExportOpen] = useState(false);
  // "Share one sign-in": the ungrouped profiles ticked for it, and the dialog that moves them.
  const [picked, setPicked] = useState<Set<string>>(new Set());
  // The group is fixed when the dialog opens: its rows leave the ticked set as they move, and the
  // dialog has to keep listing them (with what happened to each) until it is closed.
  const [shareGroup, setShareGroup] = useState<ReturnType<typeof toCandidate>[] | null>(null);
  useEffect(() => {
    if (requested !== "import") return;
    setImportOpen(true);
    useSettingsUI.getState().clearSettingsView();
  }, [requested]);
  // Each row's login state, from the Agent (absent while the workspace is stopped). Asked on
  // open and after the login modal closes; nothing polls.
  const [states, setStates] = useState<Record<string, string>>({});
  // The same for each shared sign-in, by its name (one login for every member).
  const [signInStates, setSignInStates] = useState<Record<string, string>>({});
  const loadStates = useCallback(() => {
    api("api/aws-login/profiles")
      .then((d) => {
        // No prototype: a profile named "__proto__" would otherwise set it instead of a key.
        const m: Record<string, string> = Object.create(null);
        for (const p of Array.isArray(d?.profiles) ? d.profiles : []) m[String(p.name)] = String(p.state);
        const g: Record<string, string> = Object.create(null);
        for (const si of Array.isArray(d?.signIns) ? d.signIns : []) g[String(si.name)] = String(si.state);
        setStates(m);
        setSignInStates(g);
      })
      .catch(() => {
        setStates({});
        setSignInStates({});
      });
  }, []);
  useEffect(loadStates, [loadStates]);
  const set = (k: string) => (e: FieldEvent) => setF((p) => ({ ...p, [k]: e.target.value }));
  const setVal = (k: string) => (v: string) => setF((p) => ({ ...p, [k]: v }));
  const chainedForm = f.kind === ASSUME_ROLE;
  const duration = f.durationSeconds.trim();
  const durationOk = duration === "" || (/^\d+$/.test(duration) && Number(duration) >= 900 && Number(duration) <= 3600);
  const joining = f.signInMode === "join";
  const newSignIn = f.signInMode === "new";
  const joined = joining ? signIns.find((x) => x.name === f.signIn) : undefined;
  const portalOk = /^https:\/\//.test(f.startUrl.trim()) && !!f.ssoRegion.trim();
  const valid = chainedForm
    ? f.label.trim() && f.sourceProfileId && validRoleArn(f.roleArn.trim()) && validExternalId(f.externalId.trim()) && validSessionName(f.sessionName.trim()) && durationOk
    : f.label.trim() && (joining ? !!joined : portalOk && (!newSignIn || SIGN_IN_NAME_RE.test(f.signIn.trim())));
  // The profiles an assume-role profile can start from: SSO profiles, never another chained one.
  const sources = (profiles || []).filter((p) => !isChained(p) && p.id !== editing?.id);
  // Why a row cannot log in from here (null when it can; "" when a CP too old to send the
  // name leaves nothing to say). The Agent refuses the same rows; saying so up front beats
  // a failed press. The button is also off while a save or delete is out: a login begun
  // before the PUT lands signs in to the old portal, and its completion would clear the new
  // relogin mark.
  const loginOff = (p: any): string | null => {
    if (isChained(p)) {
      // The login is the source profile's; the button logs in to that one.
      const src = (profiles || []).find((x) => x.id === p.sourceProfileId);
      return src && !isChained(src) ? loginOff(src) : tr("ssm.login_off_no_source");
    }
    return !p.name
      ? ""
      : p.nameCollides
        ? tr("ssm.login_off_collides", { name: p.name })
        : !p.accountId || !p.roleName
          ? tr("ssm.login_off_incomplete")
          : null;
  };
  // The profile whose login a row's button starts: the row itself, or a chain's source.
  const loginTarget = (p: any): any => (isChained(p) ? (profiles || []).find((x) => x.id === p.sourceProfileId) || p : p);

  const close = () => {
    setOpen(false);
    setEditing(null);
    setF(emptyProfile);
  };
  // A row that leaves the list (deleted here or elsewhere) takes its form with it; otherwise
  // the section would keep hiding Add for a form it no longer renders.
  useEffect(() => {
    if (editing && profiles && !profiles.some((p) => p.id === editing.id)) close();
  }, [profiles, editing]);
  const startAdd = () => {
    setEditing(null);
    setEditingSignIn(null);
    setF(emptyProfile);
    setOpen(true);
  };
  const startEdit = (p: any) => {
    setOpen(false);
    setEditingSignIn(null);
    setF({ ...pick(p, emptyProfile), signInMode: p.signIn ? "join" : "" });
    setEditing(p);
  };
  // The sign-in the profile is in after the save ("" for its own). An edit that did not touch the
  // Sign-in choice keeps the sign-in the Control Plane holds: the form's copy may be stale (another
  // tab moved the profile), so it is neither sent nor assumed.
  const signInTouched = !editing || f.signInTouched === "1";
  const nextSignIn = chainedForm ? "" : !signInTouched ? String(editing?.signIn || "") : joining || newSignIn ? f.signIn.trim() : "";
  const live = (st: string | undefined) => st === "signed_in" || st === "renew";
  // Whether the saved profile needs a new login before its badge means anything. Joining a sign-in
  // that is signed in uses its login; a new sign-in named after a profile of its own that is
  // signed in now, on the same portal, takes that login over (ADR 0125 decision D). Leaving a
  // sign-in, any other new sign-in, and a rename or portal change of a profile of its own do not.
  const needsLogin = (body: Record<string, unknown>): boolean => {
    if (!editing || chainedForm || isChained(editing)) return false;
    const was = String(editing.signIn || "");
    if (nextSignIn !== was) {
      if (!nextSignIn) return true;
      if (joining) return !live(signInStates[nextSignIn]);
      const named = (profiles || []).find((x) => !isChained(x) && !x.signIn && x.name === nextSignIn);
      return !(named && live(states[named.name]) && !authMoved(named, body as any));
    }
    if (nextSignIn) return false;
    return awsProfileName(String(editing.label || "")) !== awsProfileName(String(body.label)) || authMoved(editing, body as any);
  };
  // An edit keeps the profile's id, so the hosts that use it follow; delete and re-add
  // would leave them on an id that no longer exists (ssm_host.profile_id has no FK).
  const save = async () => {
    if (!valid) return;
    setBusy(true);
    try {
      const body: Record<string, unknown> = chainedForm
        ? {
            kind: ASSUME_ROLE,
            label: f.label.trim(),
            sourceProfileId: f.sourceProfileId,
            roleArn: f.roleArn.trim(),
            externalId: f.externalId.trim(),
            sessionName: f.sessionName.trim(),
            region: f.region.trim(),
            ...(duration ? { durationSeconds: Number(duration) } : {}),
            confirmBeforeUse: f.confirmBeforeUse === "true",
          }
        : {
            label: f.label.trim(),
            // Joining takes the sign-in's portal: the CP refuses any other (sign_in_mismatch).
            startUrl: joining ? "" : f.startUrl.trim(),
            ssoRegion: joining ? "" : f.ssoRegion.trim(),
            accountId: f.accountId.trim(),
            roleName: f.roleName.trim(),
            region: f.region.trim(),
            confirmBeforeUse: f.confirmBeforeUse === "true",
            // A name joins that sign-in or creates it; "" leaves the one the profile was in. Left
            // out when the choice was not touched, so the CP keeps what it holds.
            ...(!signInTouched ? {} : joining || newSignIn ? { signIn: f.signIn.trim() } : editing?.signIn ? { signIn: "" } : {}),
          };
      const ok = editing
        ? await postJSON(`api/ssm/profiles/${encodeURIComponent(editing.id)}`, "PUT", body, toast)
        : await postJSON("api/ssm/profiles", "POST", body, toast);
      if (!ok) return;
      if (editing && needsLogin(body)) {
        reloginNeeded.add(editing.id);
        setMarks((n) => n + 1);
      } else if (editing && !chainedForm && nextSignIn !== String(editing.signIn || "") && reloginNeeded.delete(editing.id)) {
        // Moved into a login that works now: the mark was about the login it left.
        setMarks((n) => n + 1);
      }
      close();
      reload();
    } finally {
      setBusy(false);
    }
  };
  const remove = async (id: string) => {
    // The server refuses to delete a profile that hosts use, so say so up front instead of
    // asking to confirm a delete that cannot happen. This list may be stale (the hosts may
    // have moved on in another tab), so refetch it: otherwise the refusal repeats forever and
    // a profile nothing uses any more can never be deleted from this page.
    const using = (hosts || []).filter((h) => h.profileId === id).map((h) => String(h.alias));
    if (using.length > 0) {
      toast(profileInUse(using));
      reload();
      return;
    }
    const dependents = (profiles || []).filter((x) => isChained(x) && x.sourceProfileId === id).map((x) => String(x.label));
    if (dependents.length > 0) {
      toast(profileInUse([], dependents));
      return;
    }
    const ok = await askConfirm({
      title: tr("ssm.profile_del_title"),
      body: tr("ssm.profile_del_body"),
      confirmLabel: tr("common.delete_confirm"),
      danger: true,
    });
    if (!ok) return;
    // busy holds every row and the form still while the DELETE is out, so no later edit can be
    // started and then lost. The open form closes through the list effect, once the row is gone.
    setBusy(true);
    try {
      const res = await deleteRow(`api/ssm/profiles/${encodeURIComponent(id)}`, toast);
      // Refused for a host this page has not loaded yet: fetch it, so the list shows why.
      if (res === "in_use") reload();
      if (res !== "ok") return;
      reloginNeeded.delete(id);
      reload();
    } finally {
      setBusy(false);
    }
  };

  // What an edit changes outside this page: the sign-in is cached per profile name, so a
  // rename starts signed out, and a new portal under the same name would reuse a token
  // issued by the old one.
  const was = editing ? awsProfileName(String(editing.label || "")) : "";
  const now = awsProfileName(f.label);
  // The sign-in this edit leaves the profile in ("" for its own): moving into, out of or between
  // sign-ins changes which login the profile uses, and a member's own name and portal no longer
  // decide it.
  const signInMoved = !!editing && !chainedForm && !isChained(editing) && String(editing.signIn || "") !== nextSignIn;
  const portalChanged =
    !!editing && !chainedForm && !isChained(editing) && !nextSignIn && authMoved(editing, { startUrl: f.startUrl.trim(), ssoRegion: f.ssoRegion.trim() });
  const kindSelect = (
    <Field label={tr("ssm.f_kind")} hint={tr("ssm.f_kind_hint")}>
      <select className="cinput" value={f.kind || "sso"} onChange={set("kind")}>
        <option value="sso">{tr("ssm.kind_sso")}</option>
        <option value={ASSUME_ROLE}>{tr("ssm.kind_assume_role")}</option>
      </select>
    </Field>
  );
  const chainedFields = (
    <FieldGroup>
      {kindSelect}
      <Field label={tr("ssm.f_label")} req hint={tr("ssm.f_label_hint")}>
        <input ref={labelRef} className="cinput" placeholder="deploy-role" value={f.label} onChange={set("label")} autoFocus />
      </Field>
      <Field label={tr("ssm.f_source")} req wide hint={tr("ssm.f_source_hint")}>
        <select className="cinput" value={f.sourceProfileId} onChange={set("sourceProfileId")}>
          <option value="">{tr("ssm.select_profile")}</option>
          {sources.map((p) => (
            <option key={p.id} value={p.id}>
              {p.label}
            </option>
          ))}
        </select>
      </Field>
      <Field label={tr("ssm.f_role_arn")} req wide hint={tr("ssm.f_role_arn_hint")}>
        <input className="cinput" placeholder="arn:aws:iam::123456789012:role/deploy" value={f.roleArn} onChange={set("roleArn")} />
      </Field>
      <Field label={tr("ssm.f_external_id")} hint={tr("ssm.f_optional")}>
        <input className="cinput" value={f.externalId} onChange={set("externalId")} />
      </Field>
      <Field label={tr("ssm.f_session_name")} hint={tr("ssm.f_optional")}>
        <input className="cinput" placeholder="agent-fleet" value={f.sessionName} onChange={set("sessionName")} />
      </Field>
      <Field label={tr("ssm.f_duration")} hint={tr("ssm.f_duration_hint")}>
        <input className="cinput" inputMode="numeric" placeholder="3600" value={f.durationSeconds} onChange={set("durationSeconds")} />
      </Field>
      <Field label={tr("ssm.meta_default_region")} hint={tr("ssm.f_default_region_hint")}>
        <RegionSelect value={f.region} onChange={setVal("region")} emptyLabel={tr("ssm.region_unset")} />
      </Field>
    </FieldGroup>
  );
  const form = (
    <div className={"ssm-frm" + (editing ? " ssm-frm-edit" : "")}>
      <fieldset className="ssm-fieldset" disabled={busy}>
        {chainedForm ? chainedFields : (
        <FieldGroup>
          {kindSelect}
          <Field label={tr("ssm.f_label")} req hint={tr("ssm.f_label_hint")}>
            <input
              ref={labelRef}
              className="cinput"
              placeholder="my-profile"
              value={f.label}
              onChange={set("label")}
              autoFocus
            />
          </Field>
          <Field label={tr("ssm.f_sign_in")} wide hint={tr("ssm.f_sign_in_hint")}>
            <select
              className="cinput ssm-sign-in-select"
              value={newSignIn ? "new" : joining ? "join:" + f.signIn : "own"}
              onChange={(e) => {
                const v = e.target.value;
                setF((p) =>
                  v === "new"
                    ? { ...p, signInMode: "new", signIn: "", signInTouched: "1" }
                    : v.startsWith("join:")
                      ? { ...p, signInMode: "join", signIn: v.slice(5), signInTouched: "1" }
                      : { ...p, signInMode: "", signIn: "", signInTouched: "1" },
                );
              }}
            >
              <option value="own">{tr("ssm.sign_in_own")}</option>
              {signIns.map((si) => (
                <option key={si.id} value={"join:" + si.name}>
                  {si.name}
                </option>
              ))}
              {/* A profile in a sign-in this page has not loaded yet still shows its own. */}
              {joining && !joined && <option value={"join:" + f.signIn}>{f.signIn}</option>}
              <option value="new">{tr("ssm.sign_in_new")}</option>
            </select>
          </Field>
          {newSignIn && (
            <Field label={tr("ssm.f_sign_in_name")} req hint={tr("ssm.f_sign_in_name_hint")}>
              <input className="cinput ssm-sign-in-name" placeholder="my-company" value={f.signIn} onChange={set("signIn")} />
            </Field>
          )}
          {joining ? (
            joined && (
              <div className="ssm-meta ssm-sign-in-portal">
                <Meta k={tr("ssm.meta_sso_region")} v={joined.ssoRegion} />
                <Meta k="start URL" v={joined.startUrl} wide />
              </div>
            )
          ) : (
            <>
          <Field label={tr("ssm.meta_sso_region")} req hint={tr("ssm.f_sso_region_hint")}>
            <RegionSelect value={f.ssoRegion} onChange={setVal("ssoRegion")} emptyLabel={tr("ssm.region_select")} />
          </Field>
          <Field
            label="start URL"
            req
            wide
            hint={
              <>
                {tr("ssm.f_starturl_hint_1")}
                <code>https://…awsapps.com/start</code>
                {tr("ssm.f_starturl_hint_2")}
              </>
            }
          >
            <input
              className="cinput"
              placeholder="https://my-company.awsapps.com/start"
              value={f.startUrl}
              onChange={set("startUrl")}
            />
          </Field>
            </>
          )}
          <Field label={tr("ssm.f_account_id")} hint={tr("ssm.f_optional_login_pick")}>
            <input className="cinput" placeholder="123456789012" value={f.accountId} onChange={set("accountId")} />
          </Field>
          <Field label={tr("ssm.f_role_name")} hint={tr("ssm.f_optional_login_pick")}>
            <input className="cinput" placeholder="AdministratorAccess" value={f.roleName} onChange={set("roleName")} />
          </Field>
          <Field label={tr("ssm.meta_default_region")} hint={tr("ssm.f_default_region_hint")}>
            <RegionSelect value={f.region} onChange={setVal("region")} emptyLabel={tr("ssm.region_unset")} />
          </Field>
        </FieldGroup>
        )}
        <label className="ssm-confirm-use">
          <input
            type="checkbox"
            checked={f.confirmBeforeUse === "true"}
            onChange={(e) => setF((p) => ({ ...p, confirmBeforeUse: e.target.checked ? "true" : "" }))}
          />
          <span>
            <b>{tr("ssm.f_confirm_use")}</b>
            <span className="field-help">{tr("ssm.f_confirm_use_hint")}</span>
          </span>
        </label>
      </fieldset>
      {editing && (
        <div className="field-help ssm-edit-notes">
          {signInMoved ? (
            <p className="ssm-edit-warn ssm-edit-signin-warn">
              {nextSignIn ? tr("ssm.edit_signin_join_warn", { signIn: nextSignIn }) : tr("ssm.edit_signin_leave_warn", { from: String(editing.signIn) })}
            </p>
          ) : !nextSignIn && was !== now ? (
            <p className="ssm-edit-warn">{tr("ssm.edit_rename_warn", { from: was, to: now })}</p>
          ) : (
            portalChanged && <p className="ssm-edit-warn">{tr("ssm.edit_portal_warn")}</p>
          )}
          <p>
            {tr("ssm.edit_profile_note_1")}
            <code>~/.aws/config</code>
            {tr("ssm.edit_profile_note_2")}
          </p>
        </div>
      )}
      <div className="ssm-frm-foot">
        <button className="primary" disabled={busy || !valid} onClick={save}>
          {editing ? tr("common.save") : tr("ssm.add_profile")}
        </button>
        <button className="ghost" disabled={busy} onClick={close}>
          {tr("common.cancel")}
        </button>
        <span className="req-note">
          <b>*</b> {tr("ssm.req_note")}
        </span>
      </div>
    </div>
  );

  // Profiles of a shared sign-in are listed under its header (ADR 0125 decision 6); the rest,
  // chains included, as rows of their own. A sign-in the list of sign-ins does not name (an
  // older or failed answer) still groups its members, with nothing to edit.
  const ungrouped = (profiles || []).filter((p) => isChained(p) || !p.signIn);
  // Ungrouped SSO profiles can be ticked to share one sign-in. Ticks of rows that have since
  // left the list (grouped elsewhere, deleted) are dropped when read.
  const groupables = ungrouped.filter(groupable);
  const shareable = groupables.length >= 2;
  const chosen = groupables.filter((p) => picked.has(p.id)).map(toCandidate);
  const chosenPortalOk = samePortal(chosen);
  const togglePick = (id: string, on: boolean) =>
    setPicked((s) => {
      const n = new Set(s);
      if (on) n.add(id);
      else n.delete(id);
      return n;
    });
  const groups: { si: SsmSignIn; members: any[] }[] = signIns.map((si) => ({
    si,
    members: (profiles || []).filter((p) => !isChained(p) && p.signIn === si.name),
  }));
  for (const p of profiles || []) {
    if (isChained(p) || !p.signIn || groups.some((g) => g.si.name === p.signIn)) continue;
    groups.push({
      si: { id: "", name: String(p.signIn), startUrl: String(p.startUrl || ""), ssoRegion: String(p.ssoRegion || ""), createdAt: "", profiles: [] },
      members: (profiles || []).filter((x) => !isChained(x) && x.signIn === p.signIn),
    });
  }

  // The header's own form: the sign-in's name and portal (PUT /api/ssm/sign-ins/{id}).
  const startEditSignIn = (si: SsmSignIn) => {
    setOpen(false);
    setEditing(null);
    setSf({ name: si.name, startUrl: si.startUrl, ssoRegion: si.ssoRegion });
    setEditingSignIn(si);
  };
  const sfValid = SIGN_IN_NAME_RE.test(sf.name.trim()) && /^https:\/\//.test(sf.startUrl.trim()) && !!sf.ssoRegion.trim();
  const saveSignIn = async () => {
    const si = editingSignIn;
    if (!si || !sfValid) return;
    const next = { name: sf.name.trim(), startUrl: sf.startUrl.trim(), ssoRegion: sf.ssoRegion.trim() };
    const renamed = next.name !== si.name;
    const moved = next.startUrl !== si.startUrl || next.ssoRegion !== si.ssoRegion;
    const members = (profiles || []).filter((p) => !isChained(p) && p.signIn === si.name);
    // Both sign every member out (a rename moves the login to a new key, a new portal makes the
    // old token unusable), so the member says yes to that first.
    if (renamed || moved) {
      const ok = await askConfirm({
        title: tr("ssm.sign_in_save_title", { signIn: si.name }),
        body: (
          <>
            {renamed && <p>{tr("ssm.sign_in_rename_warn", { from: si.name, to: next.name })}</p>}
            {moved && <p>{tr("ssm.sign_in_portal_warn")}</p>}
            <p>{tr("ssm.sign_in_members_out", { profiles: members.map((m) => String(m.label)).join(", ") || "-" })}</p>
          </>
        ),
        confirmLabel: tr("common.save"),
      });
      if (!ok) return;
    }
    setBusy(true);
    try {
      const ok = await postJSON(`api/ssm/sign-ins/${encodeURIComponent(si.id)}`, "PUT", next, toast);
      if (!ok) return;
      if (renamed || moved) {
        for (const m of members) reloginNeeded.add(m.id);
        setMarks((n) => n + 1);
      }
      setEditingSignIn(null);
      reload();
    } finally {
      setBusy(false);
    }
  };
  const signInForm = (
    <div className="ssm-frm ssm-frm-edit ssm-signin-form">
      <fieldset className="ssm-fieldset" disabled={busy}>
        <FieldGroup>
          <Field label={tr("ssm.f_sign_in_name")} req hint={tr("ssm.f_sign_in_name_hint")}>
            <input className="cinput ssm-signin-name" value={sf.name} onChange={(e) => setSf((x) => ({ ...x, name: e.target.value }))} autoFocus />
          </Field>
          <Field label={tr("ssm.meta_sso_region")} req hint={tr("ssm.f_sso_region_hint")}>
            <RegionSelect value={sf.ssoRegion} onChange={(v) => setSf((x) => ({ ...x, ssoRegion: v }))} emptyLabel={tr("ssm.region_select")} />
          </Field>
          <Field label="start URL" req wide>
            <input
              className="cinput ssm-signin-url"
              placeholder="https://my-company.awsapps.com/start"
              value={sf.startUrl}
              onChange={(e) => setSf((x) => ({ ...x, startUrl: e.target.value }))}
            />
          </Field>
        </FieldGroup>
      </fieldset>
      <div className="ssm-frm-foot">
        <button className="primary ssm-signin-save" disabled={busy || !sfValid} onClick={saveSignIn}>
          {tr("common.save")}
        </button>
        <button className="ghost" disabled={busy} onClick={() => setEditingSignIn(null)}>
          {tr("common.cancel")}
        </button>
      </div>
    </div>
  );

  // A sign-in's header: its name, portal and one login state, Log in and Log out, and the edit
  // of its name and portal. The login runs as a member the workspace can log in with; the Agent
  // logs in the sign-in's one sso-session, for every member.
  const renderSignIn = (si: SsmSignIn, members: any[]) => {
    // Every member the workspace could log in through. The first is tried; one the member's own
    // ~/.aws holds back is refused by the Agent (not_exported), and the next is tried instead.
    const candidates = members.filter((m) => m.name && !m.nameCollides && m.accountId && m.roleName);
    const loginMember = candidates[0];
    const asLogin = (m: any) => ({ id: String(m.id), name: String(m.name), label: si.name, accountId: String(m.accountId), roleName: String(m.roleName) });
    const relogin = members.some((m) => reloginNeeded.has(m.id));
    const st = signInStates[si.name];
    return (
      <li key={"sign-in:" + si.name} className="ssm-item ssm-signin">
        <div className="ssm-item-head ssm-signin-head">
          <span className="ssm-alias">{si.name}</span>
          <span className="ssm-signin-tag">{tr("ssm.sign_in_tag", { n: members.length })}</span>
          {relogin ? (
            <span className="ssm-login-state relogin" title={tr("ssm.state_relogin_title")}>
              {tr("ssm.state_relogin")}
            </span>
          ) : (
            STATE_KEYS[st] && (
              <span className={"ssm-login-state " + st} title={tr(STATE_KEYS[st].title)}>
                {tr(STATE_KEYS[st].label)}
              </span>
            )
          )}
          <button
            className="ghost ssm-login ssm-signin-login"
            title={loginMember ? tr("ssm.sign_in_login_title", { signIn: si.name }) : tr("ssm.login_off_incomplete")}
            disabled={busy || !loginMember}
            onClick={() =>
              loginMember &&
              setLoginFor({ ...asLogin(loginMember), ids: members.map((m) => String(m.id)), signIn: si.name, next: candidates.slice(1).map(asLogin) })
            }
          >
            {tr("ssm.login")}
          </button>
          {loginMember && !relogin && (st === "signed_in" || st === "renew") && (
            <button
              className="ghost ssm-logout ssm-signin-logout"
              title={tr("awslogin.logout_title")}
              disabled={busy || loggingOut[loginMember.name] === true}
              onClick={() => void logout(loginMember, candidates.slice(1).map((m) => String(m.name))).then((done) => done && loadStates())}
            >
              {tr("awslogin.logout")}
            </button>
          )}
          <button
            className="ghost ssm-edit ssm-signin-edit"
            title={tr("ssm.sign_in_edit_title")}
            disabled={busy || !si.id || editingSignIn?.id === si.id}
            onClick={() => startEditSignIn(si)}
          >
            {tr("ssm.edit")}
          </button>
        </div>
        {editingSignIn?.id === si.id ? (
          signInForm
        ) : (
          <div className="ssm-meta">
            <Meta k={tr("ssm.meta_sso_region")} v={si.ssoRegion} />
            <Meta k="start URL" v={si.startUrl} wide />
          </div>
        )}
        {members.length > 0 ? (
          <ul className="ssm-list ssm-signin-members">{members.map((m) => renderRow(m, true))}</ul>
        ) : (
          <p className="muted">{tr("ssm.sign_in_empty")}</p>
        )}
      </li>
    );
  };

  // One profile's row. A member of a shared sign-in (grouped) has no login of its own: its
  // header logs in, out and shows the state once for every member, and holds the portal.
  const renderRow = (p: any, grouped: boolean) => (
    <li key={p.id} className={"ssm-item" + (grouped ? " ssm-member" : "")}>
      <div className="ssm-item-head">
        {!grouped && shareable && groupable(p) && (
          <input
            type="checkbox"
            className="ssm-share-pick"
            aria-label={tr("ssm.share_pick", { label: String(p.label) })}
            checked={picked.has(p.id)}
            disabled={busy}
            onChange={(e) => togglePick(p.id, e.target.checked)}
          />
        )}
        <span className="ssm-alias">{p.label}</span>
        {grouped ? null : reloginNeeded.has(p.id) ? (
          <span className="ssm-login-state relogin" title={tr("ssm.state_relogin_title")}>
            {tr("ssm.state_relogin")}
          </span>
        ) : (
          p.name &&
          STATE_KEYS[states[p.name]] && (
          <span
            className={"ssm-login-state " + states[p.name]}
            title={tr(STATE_KEYS[states[p.name]].title)}
          >
            {tr(STATE_KEYS[states[p.name]].label)}
          </span>
          )
        )}
        {!grouped && (
          <button
            className="ghost ssm-login"
            title={loginOff(p) || tr("ssm.login_title")}
            disabled={busy || loginOff(p) !== null}
            onClick={() => {
              const t0 = loginTarget(p);
              setLoginFor({ id: t0.id, name: t0.name, label: t0.label, accountId: t0.accountId, roleName: t0.roleName });
            }}
          >
            {tr("ssm.login")}
          </button>
        )}
        {/* A chain has no login of its own: its button ends the source's (after a confirmation that
            names the source and the chains following it), so it needs a source to end. */}
        {!grouped && p.name && !reloginNeeded.has(loginTarget(p).id) && !(isChained(p) && isChained(loginTarget(p))) && (states[p.name] === "signed_in" || states[p.name] === "renew") && (
          <button
            className="ghost ssm-logout"
            title={tr("awslogin.logout_title")}
            disabled={busy || loggingOut[p.name] === true}
            onClick={() => void logout(p).then((done) => done && loadStates())}
          >
            {tr("awslogin.logout")}
          </button>
        )}
        <button
          className="ghost ssm-edit"
          title={tr("ssm.edit_profile_title")}
          disabled={busy || editing?.id === p.id}
          onClick={() => startEdit(p)}
        >
          {tr("ssm.edit")}
        </button>
        <button className="ghost danger ssm-del" title={tr("common.delete")} disabled={busy} onClick={() => remove(p.id)}>
          {tr("common.delete")}
        </button>
      </div>
      {editing?.id === p.id ? (
        form
      ) : (
        isChained(p) ? (
          <div className="ssm-meta">
            <Meta k={tr("ssm.meta_account")} v={p.accountId} />
            <Meta k={tr("ssm.meta_source")} v={(profiles || []).find((x) => x.id === p.sourceProfileId)?.label || tr("ssm.source_missing")} />
            <Meta k={tr("ssm.meta_default_region")} v={p.region} />
            <Meta k={tr("ssm.f_role_arn")} v={p.roleArn} wide />
            {p.externalId && <Meta k={tr("ssm.f_external_id")} v={p.externalId} />}
            {p.sessionName && <Meta k={tr("ssm.f_session_name")} v={p.sessionName} />}
            {p.durationSeconds ? <Meta k={tr("ssm.f_duration")} v={String(p.durationSeconds)} /> : null}
            {p.confirmBeforeUse === true && <Meta k={tr("ssm.meta_confirm_use")} v={tr("ssm.meta_confirm_use_on")} />}
          </div>
        ) : (
        <div className="ssm-meta">
          <Meta k={tr("ssm.meta_account")} v={p.accountId} />
          <Meta k={tr("ssm.meta_role")} v={p.roleName} />
          <Meta k={tr("ssm.meta_default_region")} v={p.region} />
          {/* A member's portal is the sign-in's, shown once on its header. */}
          {!grouped && <Meta k={tr("ssm.meta_sso_region")} v={p.ssoRegion} />}
          {!grouped && <Meta k="start URL" v={p.startUrl} wide />}
          {p.confirmBeforeUse === true && <Meta k={tr("ssm.meta_confirm_use")} v={tr("ssm.meta_confirm_use_on")} />}
        </div>
        )
      )}
    </li>
  );

  return (
    <section className="ds-group">
      <div className="ds-title">{tr("ssm.profile_cat")}</div>
      <div className="field-help">
        {tr("ssm.profile_help_1")}
        <code>~/.aws</code>
        {tr("ssm.profile_help_2")}
      </div>
      {loadErr && <p className="ssm-load-err">{tr(profiles === null ? "ssm.load_failed" : "ssm.refresh_failed", { msg: loadErr })}</p>}
      {profiles === null ? (
        !loadErr && <p className="muted pad">{tr("common.loading")}</p>
      ) : profiles.length === 0 ? (
        <p className="muted">{tr("ssm.profile_empty")}</p>
      ) : (
        <ul className="ssm-list">
          {ungrouped.map((p) => renderRow(p, false))}
          {groups.map(({ si, members }) => renderSignIn(si, members))}
        </ul>
      )}
      {chosen.length > 0 && (
        <div className="flow ssm-share-bar">
          <span className="muted">{tr("ssm.share_bar", { n: chosen.length })}</span>
          <button
            className="ghost ssm-share-open"
            disabled={busy || chosen.length < 2 || !chosenPortalOk}
            title={chosen.length < 2 ? tr("ssm.share_need_two") : !chosenPortalOk ? tr("ssm.share_mixed") : undefined}
            onClick={() => setShareGroup(chosen)}
          >
            {tr("ssm.share_open")}
          </button>
          {chosen.length >= 2 && !chosenPortalOk && <span className="field-help">{tr("ssm.share_mixed")}</span>}
        </div>
      )}
      {open
        ? form
        : !editing && (
            <div className="flow">
              <button className="ghost ssm-add-toggle" onClick={startAdd}>
                <Icon name="add" /> {tr("ssm.add_profile")}
              </button>
              <button className="ghost ssm-import-toggle" onClick={() => setImportOpen(true)}>
                <Icon name="cloud-upload" /> {tr("ssm.imp_open")}
              </button>
              <button className="ghost ssm-import-ssh-toggle" onClick={() => setSshImportOpen(true)}>
                <Icon name="cloud-upload" /> {tr("ssm.ssh_open")}
              </button>
              <button className="ghost ssm-export-toggle" onClick={() => setExportOpen(true)}>
                <Icon name="download" /> {tr("ssm.exp_open")}
              </button>
            </div>
          )}
      {shareGroup && (
        <ShareSignIn
          members={shareGroup}
          others={(profiles || []).filter((p) => groupable(p) && !shareGroup.some((g) => g.id === p.id)).map(toCandidate)}
          signIns={signIns}
          live={(n) => live(states[n])}
          signInLive={(n) => live(signInStates[n])}
          onChanged={(joined, keeps) => {
            // A group that took over no working login starts signed out: say so on its header.
            if (!keeps) for (const id of joined) reloginNeeded.add(id);
            else for (const id of joined) reloginNeeded.delete(id);
            setPicked((s) => new Set([...s].filter((id) => !joined.includes(id))));
            setMarks((n) => n + 1);
            reload();
            loadStates();
          }}
          onClose={() => setShareGroup(null)}
        />
      )}
      {importOpen && <AwsConfigImport onClose={() => setImportOpen(false)} onImported={reload} />}
      {sshImportOpen && <SshConfigImport onClose={() => setSshImportOpen(false)} onImported={reload} />}
      {exportOpen && <AwsConfigExport onClose={() => setExportOpen(false)} />}
      {loginFor && (
        <ProfileLoginModal
          key={loginFor.name}
          profile={loginFor}
          startNow={loginFor.startNow}
          onRefused={(code) => {
            if (code !== "not_exported" || !loginFor.signIn || !loginFor.next?.length) return;
            // The next member starts at once only while the Agent still lists it in the sign-in the
            // member pressed Log in for; one moved to another sign-in meanwhile would log that in.
            const cur = loginFor;
            void api("api/aws-login/profiles")
              .then((d) => {
                const inSignIn = (n: string) =>
                  (Array.isArray(d?.profiles) ? d.profiles : []).some((x: any) => x?.name === n && !x?.source && x?.signIn === cur.signIn);
                const left = (cur.next || []).filter((x) => inSignIn(x.name));
                const [nx, ...rest] = left;
                if (nx) setLoginFor((now) => (now === cur ? { ...nx, ids: cur.ids, signIn: cur.signIn, next: rest, startNow: true } : now));
              })
              .catch(() => {});
          }}
          onClose={() => {
            setLoginFor(null);
            loadStates();
          }}
          onLoggedIn={() => {
            reloginNeeded.delete(loginFor.id);
            for (const id of loginFor.ids || []) reloginNeeded.delete(id);
            setMarks((n) => n + 1);
          }}
        />
      )}
    </section>
  );
}

// --- hosts (per-instance) -------------------------------------------------------

// HostColorPicker chooses the terminal background hue for a host's sessions. The
// choice is a per-user setting (synced), applied to a session's terminal when it is
// created (new sessions to this host pick it up). Swatches show the vivid hue; the
// terminal itself renders a subtle dark tint of it.
function HostColorPicker({ hostId }: { hostId: string }) {
  const tr = useT();
  const settings = useSettings();
  const cur = settings.ssmHostColors?.[hostId] || "auto";
  const setColor = (id: string) =>
    setSetting("ssmHostColors", { ...(settings.ssmHostColors || {}), [hostId]: id });
  return (
    <div className="ssm-host-color">
      <span className="ssm-meta-k">{tr("ssm.term_color")}</span>
      <div className="ssm-swatches">
        {SSM_HOST_COLORS.map((c) => {
          const base = c.base || hostColorBase("auto", hostId); // vivid identity color
          return (
            <button
              key={c.id}
              type="button"
              className={"ssm-swatch" + (cur === c.id ? " active" : "")}
              title={c.id === "auto" ? tr("ssm.color_auto_title") : tr(c.labelKey)}
              style={{ background: base }}
              onClick={() => setColor(c.id)}
            >
              {c.id === "auto" ? "A" : ""}
            </button>
          );
        })}
        <span className="ssm-swatch-preview" title={tr("ssm.term_preview_title")} style={{ background: termBackground("ssm", hostColorBase(cur, hostId)) }}>
          {tr("ssm.term_label")}
        </span>
      </div>
    </div>
  );
}

const emptyHost: Record<string, string> = { alias: "", profileId: "", instanceId: "", documentName: "", region: "" };

function HostSection({
  hosts,
  loadErr,
  profiles,
  reload,
  onNeedProfile,
}: {
  hosts: any[] | null;
  loadErr: string;
  profiles: any[] | null;
  reload: () => void;
  onNeedProfile: () => void;
}) {
  const tr = useT();
  const askConfirm = useConfirm();
  const toast = useToast();
  const [f, setF] = useState<Record<string, string>>(emptyHost);
  const [busy, setBusy] = useState(false);
  const [open, setOpen] = useState(false);
  // The host being edited (its id); its form replaces the row's details.
  const [editing, setEditing] = useState<string | null>(null);
  const set = (k: string) => (e: FieldEvent) => setF((p) => ({ ...p, [k]: e.target.value }));
  const setVal = (k: string) => (v: string) => setF((p) => ({ ...p, [k]: v }));
  const profileOf = (id: string) => (profiles || []).find((p) => p.id === id);
  const noProfiles = profiles !== null && profiles.length === 0;
  // The profile must be one the current list holds: a dead id (deleted while the form was
  // open, or the list not loaded yet) would only come back from the CP as bad_profile.
  const valid = f.alias.trim() && f.instanceId.trim() && !!profileOf(f.profileId);

  const close = () => {
    setOpen(false);
    setEditing(null);
    setF(emptyHost);
  };
  useEffect(() => {
    if (editing && hosts && !hosts.some((h) => h.id === editing)) close();
  }, [hosts, editing]);
  // Drop a picked profile that the refreshed list no longer has, so the select and what Save
  // sends agree.
  useEffect(() => {
    if (profiles !== null && f.profileId && !profiles.some((p) => p.id === f.profileId)) {
      setF((p) => ({ ...p, profileId: "" }));
    }
  }, [profiles, f.profileId]);
  const startAdd = () => {
    setEditing(null);
    setF(emptyHost);
    setOpen(true);
  };
  const startEdit = (h: any) => {
    setOpen(false);
    setF(pick(h, emptyHost));
    setEditing(h.id);
  };
  const save = async () => {
    if (!valid) return;
    setBusy(true);
    try {
      const body = {
        alias: f.alias.trim(),
        profileId: f.profileId,
        instanceId: f.instanceId.trim(),
        documentName: f.documentName.trim(),
        region: f.region.trim(),
      };
      const ok = editing
        ? await postJSON(`api/ssm/hosts/${encodeURIComponent(editing)}`, "PUT", body, toast)
        : await postJSON("api/ssm/hosts", "POST", body, toast);
      if (!ok) return;
      close();
      reload();
    } finally {
      setBusy(false);
    }
  };
  const remove = async (id: string) => {
    const ok = await askConfirm({
      title: tr("ssm.host_del_title"),
      body: tr("ssm.host_del_body"),
      confirmLabel: tr("common.delete_confirm"),
      danger: true,
    });
    if (!ok) return;
    setBusy(true);
    try {
      if ((await deleteRow(`api/ssm/hosts/${encodeURIComponent(id)}`, toast)) !== "ok") return;
      reload();
    } finally {
      setBusy(false);
    }
  };

  const form = (
    <div className={"ssm-frm" + (editing ? " ssm-frm-edit" : "")}>
      <fieldset className="ssm-fieldset" disabled={busy}>
        <FieldGroup>
          <Field label={tr("ssm.f_use_profile")} req wide hint={tr("ssm.f_use_profile_hint")}>
            <select className="cinput" value={f.profileId} onChange={set("profileId")} autoFocus>
              <option value="">{tr("ssm.select_profile")}</option>
              {(profiles || []).map((p) => (
                <option key={p.id} value={p.id}>
                  {p.label}
                </option>
              ))}
            </select>
          </Field>
          <Field label={tr("ssm.f_alias")} req hint={tr("ssm.f_alias_hint")}>
            <input className="cinput" placeholder="admin@web-01" value={f.alias} onChange={set("alias")} />
          </Field>
          <Field label={tr("ssm.f_instance_id")} req hint={<>{tr("ssm.f_instance_hint_1")}<code>aws ec2 describe-instances</code>{tr("ssm.f_instance_hint_2")}</>}>
            <input className="cinput" placeholder="i-0123456789abcdef0" value={f.instanceId} onChange={set("instanceId")} />
          </Field>
          <Field label={tr("ssm.f_document")} hint={tr("ssm.f_document_hint")}>
            <input className="cinput" placeholder="SSM-SessionManagerRunShell" value={f.documentName} onChange={set("documentName")} />
          </Field>
          <Field label={tr("ssm.meta_region")} hint={tr("ssm.f_region_hint")}>
            <RegionSelect
              value={f.region}
              onChange={setVal("region")}
              emptyLabel={
                profileOf(f.profileId)?.region
                  ? tr("ssm.region_profile_default", { region: profileOf(f.profileId).region })
                  : tr("ssm.f_region_placeholder")
              }
            />
          </Field>
        </FieldGroup>
      </fieldset>
      {editing && <p className="field-help ssm-edit-notes">{tr("ssm.edit_host_note")}</p>}
      <div className="ssm-frm-foot">
        <button className="primary" disabled={busy || !valid} onClick={save}>
          {editing ? tr("common.save") : tr("ssm.add_host")}
        </button>
        <button className="ghost" disabled={busy} onClick={close}>
          {tr("common.cancel")}
        </button>
        <span className="req-note">
          <b>*</b> {tr("ssm.req_note")}
        </span>
      </div>
    </div>
  );

  return (
    <section className="ds-group">
      <div className="ds-title">{tr("ssm.host_cat")}</div>
      <div className="field-help">
        {tr("ssm.host_help_1")}
        <code>aws ssm start-session --target &lt;instance&gt; --document-name &lt;document&gt;</code>
        {tr("ssm.host_help_2")}
      </div>
      {loadErr && <p className="ssm-load-err">{tr(hosts === null ? "ssm.load_failed" : "ssm.refresh_failed", { msg: loadErr })}</p>}
      {hosts === null ? (
        !loadErr && <p className="muted pad">{tr("common.loading")}</p>
      ) : hosts.length === 0 ? (
        <p className="muted">{tr("ssm.host_empty")}</p>
      ) : (
        <ul className="ssm-list">
          {hosts.map((h) => {
            const prof = profileOf(h.profileId);
            return (
              <li key={h.id} className="ssm-item">
                <div className="ssm-item-head">
                  <span className="ssm-alias">{h.alias}</span>
                  <button
                    className="ghost ssm-edit push"
                    title={tr("ssm.edit_host_title")}
                    disabled={busy || editing === h.id}
                    onClick={() => startEdit(h)}
                  >
                    {tr("ssm.edit")}
                  </button>
                  <button className="ghost danger ssm-del" title={tr("common.delete")} disabled={busy} onClick={() => remove(h.id)}>
                    {tr("common.delete")}
                  </button>
                </div>
                {editing === h.id ? (
                  form
                ) : (
                  <>
                    <div className="ssm-meta">
                      <Meta k={tr("ssm.meta_instance")} v={h.instanceId} />
                      <Meta k={tr("ssm.meta_document")} v={h.documentName} />
                      <Meta k={tr("ssm.meta_region")} v={h.region} />
                      <Meta
                        k={tr("ssm.meta_profile")}
                        v={prof ? prof.label : profiles === null ? "" : <span className="ssm-missing">{tr("ssm.profile_missing")}</span>}
                        mono={false}
                      />
                    </div>
                    <HostColorPicker hostId={h.id} />
                  </>
                )}
              </li>
            );
          })}
        </ul>
      )}
      {noProfiles && !editing ? (
        <div className="ssm-dep">
          <span className="i">
            <Icon name="info" />
          </span>
          <span className="t">{tr("ssm.need_profile")}</span>
          <button className="primary" onClick={onNeedProfile}>
            {tr("ssm.add_profile")}
          </button>
        </div>
      ) : open ? (
        form
      ) : (
        !editing && (
          <button className="ghost ssm-add-toggle" onClick={startAdd}>
            <Icon name="add" /> {tr("ssm.add_host")}
          </button>
        )
      )}
    </section>
  );
}
