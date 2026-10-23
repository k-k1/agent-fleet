// InboxModal is the member inbox (ADR 0111, P1-A first half): received and sent items,
// sending a DM or a request, and the receive controls.
//
// Everything a card shows comes from GET /api/inbox, which the CP has already projected for
// this member: who else was asked, whether each of them read or declined (the sender only),
// and whether this member can still take a request. The buttons map one to one onto the
// state table of decision 2; a refusal answers with the current state, and the card simply
// re-reads and shows it.
//
// Handoffs other members sent (ADR 0057) get a section of their own on the received tab. They
// are not inbox items: they come from the handoff store (GET /api/session-handoff-offers/received)
// and expanding one renders the same HandoffOfferRow the shared view and the handoff dialog use,
// so taking it over, declining and giving back behave exactly as there.
import { useEffect, useMemo, useState } from "react";
import { Modal } from "../../ui/Modal.tsx";
import { Button } from "../../ui/Button.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";
import { api, apiJSON, errText, type ApiError } from "../../core/api/client.ts";
import { useT, type MsgKey } from "../../lib/i18n/index.ts";
import { relTime } from "../../lib/intl.ts";
import { useLaunchSeed, useLaunchTarget, useReposStore } from "../repos/store.ts";
import { HandoffOfferRow } from "../sharing/HandoffOfferRow.tsx";
import { refreshReceivedIfStale, senderLabel, useHandoffStore, type HandoffOffer } from "../sharing/handoffStore.ts";
import {
  assignedItems,
  receivedItems,
  sentItems,
  useInboxStore,
  waitingItems,
  type InboxItem,
  type InboxKind,
  type InboxOp,
  type InboxPerson,
  type InboxSettings,
} from "./store.ts";
import { AttachmentView, buildAttachments, emptyRecipe, RecipeEditor, SessionAttachEditor, type RecipeDraft, type SessionAttach } from "./Attachments.tsx";
import "./inbox.css";

/** The CP's caps (inbox.go), repeated so the form can say so before sending. */
export const INBOX_MAX_RECIPIENTS = 20;
export const INBOX_BODY_MAX_BYTES = 16 << 10;

type Tab = "received" | "sent" | "compose" | "settings";

const TAB_LABEL: Record<Tab, MsgKey> = {
  received: "inbox.tab_received",
  sent: "inbox.tab_sent",
  compose: "inbox.tab_compose",
  settings: "inbox.tab_settings",
};

const STATE_LABEL: Record<Exclude<InboxItem["state"], "">, MsgKey> = {
  open: "inbox.state_open",
  claimed: "inbox.state_claimed",
  done: "inbox.state_done",
  declined: "inbox.state_declined",
  withdrawn: "inbox.state_withdrawn",
};

const DELIVERY_LABEL: Record<"unread" | "read" | "declined", MsgKey> = {
  unread: "inbox.delivery_unread",
  read: "inbox.delivery_read",
  declined: "inbox.delivery_declined",
};

const byteLength = (s: string): number => new TextEncoder().encode(s).length;

function personName(tr: ReturnType<typeof useT>, p: InboxPerson | null | undefined): string {
  if (!p) return "";
  if (p.removed) return tr("inbox.removed_member");
  return p.email || p.userKey;
}

/** Turns a refusal into a sentence that names who, where the CP said who. */
export function inboxErrorText(tr: ReturnType<typeof useT>, err: ApiError | undefined): string {
  if (!err) return "";
  const who = Array.isArray((err as { recipients?: unknown }).recipients)
    ? ((err as { recipients: string[] }).recipients || []).join(", ")
    : "";
  if (err.code === "inbox_recipient_not_accepting" && who) return tr("inbox.refused", { who });
  if (err.code === "inbox_pending_limit" && who) return tr("inbox.pending_full", { who });
  return errText(err);
}

export function InboxModal({ onClose }: { onClose: () => void }) {
  const tr = useT();
  const items = useInboxStore((s) => s.items);
  const loadError = useInboxStore((s) => s.loadError);
  const mailbox = useInboxStore((s) => s.mailbox);
  const [tab, setTab] = useState<Tab>("received");
  // Looking at the inbox is what moves the read cursor, so the badge clears while it is
  // open and comes back for what arrives after it closes.
  useEffect(() => {
    void useInboxStore.getState().markSeen();
  }, [mailbox?.lastSeq]);
  useEffect(() => {
    void useInboxStore.getState().refresh();
    refreshReceivedIfStale();
  }, []);
  const received = receivedItems(items);
  const sent = sentItems(items);
  return (
    <Modal title={tr("inbox.title")} onClose={onClose} className="inbox-modal">
      <div className="ui-modal-body">
        <div className="ui-seg choice-seg inbox-tabs" role="tablist">
          {(["received", "sent", "compose", "settings"] as const).map((t) => (
            <button
              key={t}
              type="button"
              role="tab"
              aria-selected={tab === t}
              className={"seg-btn" + (tab === t ? " active" : "")}
              onClick={() => setTab(t)}
            >
              {tr(TAB_LABEL[t])}
            </button>
          ))}
        </div>
        {loadError && <p className="ui-field-hint inbox-warn">{tr("inbox.load_failed")}</p>}
        {tab === "received" && <ReceivedList items={received} onClose={onClose} />}
        {tab === "sent" &&
          (sent.length === 0 ? (
            <p className="ui-field-hint">{tr("inbox.empty_sent")}</p>
          ) : (
            <ul className="inbox-list">
              {sent.map((i) => (
                <ItemCard key={i.id} item={i} />
              ))}
            </ul>
          ))}
        {tab === "compose" && <Compose onSent={() => setTab("sent")} />}
        {tab === "settings" && <SettingsPane />}
      </div>
    </Modal>
  );
}

function ReceivedList({ items, onClose }: { items: InboxItem[]; onClose: () => void }) {
  const tr = useT();
  // The route lists only offers still pending, including recovery-only rows a confirmed take
  // still holds; the filter keeps a decided one out between reads.
  const offers = useHandoffStore((s) => s.received).filter((o) => o.status === "pending");
  const waiting = waitingItems(items);
  const mine = assignedItems(items);
  const rest = items.filter((i) => !waiting.includes(i) && !mine.includes(i));
  if (items.length === 0 && offers.length === 0) return <p className="ui-field-hint">{tr("inbox.empty_received")}</p>;
  const group = (key: MsgKey, list: InboxItem[]) =>
    list.length > 0 && (
      <section className="inbox-group">
        <h4>{tr(key)}</h4>
        <ul className="inbox-list">
          {list.map((i) => (
            <ItemCard key={i.id} item={i} />
          ))}
        </ul>
      </section>
    );
  return (
    <>
      {offers.length > 0 && (
        <section className="inbox-group inbox-handoffs">
          <h4>{tr("inbox.handoffs")}</h4>
          <ul className="inbox-list">
            {offers.map((o) => (
              <HandoffItem key={o.id} offer={o} onClose={onClose} />
            ))}
          </ul>
        </section>
      )}
      {group("inbox.waiting", waiting)}
      {group("inbox.assigned", mine)}
      {group("inbox.others", rest)}
    </>
  );
}

/** A handoff summarised like an inbox card. Taking it over opens the launch dialog, so the
 *  inbox closes rather than stay over it, as the handoff dialog does. */
function HandoffItem({ offer, onClose }: { offer: HandoffOffer; onClose: () => void }) {
  const tr = useT();
  const [open, setOpen] = useState(false);
  return (
    <li className="inbox-card" data-state="handoff">
      <button type="button" className="inbox-card-head" aria-expanded={open} onClick={() => setOpen((v) => !v)}>
        <span className="inbox-kind inbox-kind-handoff">{tr("inbox.kind_handoff")}</span>
        <span className="inbox-who">{tr("inbox.from", { who: senderLabel(offer) })}</span>
        {offer.createdAt && <span className="inbox-when muted">{relTime(offer.createdAt)}</span>}
        {!open && <span className="inbox-preview">{offer.title}</span>}
      </button>
      {open && (
        <div className="inbox-card-body">
          <ul className="handoff-inbox">
            <HandoffOfferRow offer={offer} onDone={onClose} />
          </ul>
        </div>
      )}
    </li>
  );
}

export function ItemCard({ item }: { item: InboxItem }) {
  const tr = useT();
  const toast = useToast();
  const [open, setOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const run = async (op: InboxOp) => {
    if (busy) return;
    setBusy(true);
    const r = await useInboxStore.getState().act(item.id, op);
    setBusy(false);
    if (!r.ok) toast(inboxErrorText(tr, r.error));
  };
  const expand = () => {
    setOpen((v) => !v);
    if (!open && item.role === "recipient" && item.delivery === "unread") void useInboxStore.getState().markRead(item.id);
  };
  const block = async () => {
    const d = await apiJSON(`api/inbox/blocks/${encodeURIComponent(item.sender.userKey)}`, "PUT", {}).catch(() => null);
    if (!d || d.error) toast(errText(d?.error));
    else toast(tr("inbox.blocked_done", { who: personName(tr, item.sender) }), { kind: "success" });
    void useInboxStore.getState().refresh();
  };
  const isRequest = item.kind === "request";
  const counterpart =
    item.role === "sender"
      ? tr("inbox.to", { who: item.recipients.map((r) => personName(tr, r)).join(", ") })
      : tr("inbox.from", { who: personName(tr, item.sender) });
  const firstLine = item.body.split("\n", 1)[0];
  return (
    <li className={"inbox-card" + (item.unseen ? " is-unseen" : "")} data-state={item.state || "dm"}>
      <button type="button" className="inbox-card-head" aria-expanded={open} onClick={expand}>
        <span className={"inbox-kind inbox-kind-" + item.kind}>{tr(isRequest ? "inbox.kind_request" : "inbox.kind_dm")}</span>
        {isRequest && item.state && <span className="inbox-state">{tr(STATE_LABEL[item.state])}</span>}
        {item.longOpen && <span className="inbox-long-open">{tr("inbox.long_open")}</span>}
        {item.role === "recipient" && item.delivery === "unread" && <span className="inbox-unread">{tr("inbox.unread")}</span>}
        <span className="inbox-who">{counterpart}</span>
        <span className="inbox-when muted">{relTime(item.createdAt)}</span>
        {!open && <span className="inbox-preview">{firstLine}</span>}
      </button>
      {open && (
        <div className="inbox-card-body">
          <pre className="inbox-body">{item.body}</pre>
          {isRequest && item.assignee && (
            <p className="ui-field-hint">
              {item.assignedToMe ? tr("inbox.claimed_by_you") : tr("inbox.claimed_by", { who: personName(tr, item.assignee) })}
            </p>
          )}
          {isRequest && item.state === "withdrawn" && item.assignedToMe && (
            <p className="ui-field-hint inbox-warn">{tr("inbox.withdrawn_after_claim")}</p>
          )}
          {item.role === "sender" && (
            <ul className="inbox-recipients">
              {item.recipients.map((r, i) => (
                <li key={r.userKey || "removed-" + i}>
                  {personName(tr, r)}{" "}
                  <span className="muted">
                    {r.delivery ? tr(DELIVERY_LABEL[r.delivery]) : ""}
                    {isRequest && item.state === "open" && !r.eligible && r.delivery !== "declined" ? " · " + tr("inbox.not_eligible") : ""}
                  </span>
                </li>
              ))}
            </ul>
          )}
          {item.session && (
            <p className="ui-field-hint">
              {item.session.name
                ? tr("inbox.session_mine", { name: item.session.name, when: relTime(item.session.launchedAt) })
                : tr("inbox.session_other", { who: personName(tr, item.session.by), when: relTime(item.session.launchedAt) })}
              {item.session.branch ? " · " + tr("inbox.session_branch", { branch: item.session.branch }) : ""}
            </p>
          )}
          {item.pr?.url && !item.assignedToMe && (
            <p className="ui-field-hint">
              {tr("inbox.pr")}:{" "}
              <a href={item.pr.url} target="_blank" rel="noreferrer noopener">
                {item.pr.url}
              </a>
            </p>
          )}
          {(item.attachments ?? []).map((att, i) => (
            <AttachmentView key={i} item={item} att={att} />
          ))}
          {isRequest && item.assignedToMe && (item.state === "claimed" || item.state === "done") && <PRLink item={item} />}
          {isRequest && item.assignedToMe && item.state === "claimed" && !item.session && <LaunchRow item={item} />}
          <div className="inbox-actions">
            {item.canClaim && (
              <>
                <Button variant="ghost" disabled={busy} onClick={() => void run("decline")}>
                  {tr("inbox.decline")}
                </Button>
                <Button variant="primary" disabled={busy} onClick={() => void run("claim")}>
                  {tr("inbox.claim")}
                </Button>
              </>
            )}
            {item.assignedToMe && item.state === "claimed" && (
              <>
                <Button variant="ghost" disabled={busy} onClick={() => void run("release")}>
                  {tr("inbox.release")}
                </Button>
                <Button disabled={busy} onClick={() => void run("done")}>
                  {tr("inbox.done")}
                </Button>
              </>
            )}
            {item.role === "sender" && isRequest && (item.state === "open" || item.state === "claimed") && (
              <Button variant="danger" disabled={busy} onClick={() => void run("withdraw")}>
                {tr("inbox.withdraw")}
              </Button>
            )}
            {item.role === "recipient" && !item.sender.removed && (
              <Button variant="ghost" small onClick={() => void block()}>
                <Icon name="circle-slash" /> {tr("inbox.block_sender")}
              </Button>
            )}
          </div>
        </div>
      )}
    </li>
  );
}

/** The assignee's launch: pick a working copy, then the ordinary launch dialog opens with
 *  the request as its prompt. The CP checks the claim again before the session exists. */
function LaunchRow({ item }: { item: InboxItem }) {
  const tr = useT();
  const toast = useToast();
  const repos = useReposStore((s) => s.repos);
  const [repoName, setRepoName] = useState(() => repos[0]?.name || "");
  const launch = () => {
    const repo = repos.find((r) => r.name === repoName);
    if (!repo?.path) {
      toast(tr("inbox.launch_no_repo"));
      return;
    }
    useLaunchSeed.getState().set(item.body, "");
    useLaunchSeed.getState().setInboxItem(item.id);
    useLaunchTarget.getState().open({ ...repo });
  };
  return (
    <div className="inbox-launch">
      <label className="ui-field">
        <span className="ui-field-label">{tr("inbox.launch_repo")}</span>
        <select value={repoName} onChange={(e) => setRepoName(e.target.value)}>
          {repos.map((r) => (
            <option key={r.name} value={r.name}>
              {r.name}
            </option>
          ))}
        </select>
      </label>
      {repos.length === 0 && <p className="ui-field-hint inbox-warn">{tr("inbox.launch_no_repo")}</p>}
      <p className="ui-field-hint">{tr("inbox.launch_hint")}</p>
      <Button variant="primary" disabled={!repoName} onClick={launch}>
        <Icon name="run" /> {tr("inbox.launch")}
      </Button>
    </div>
  );
}

function PRLink({ item }: { item: InboxItem }) {
  const tr = useT();
  const toast = useToast();
  const [url, setUrl] = useState(item.pr?.url || "");
  const [branch, setBranch] = useState(item.pr?.branch || item.session?.branch || "");
  const save = async () => {
    const r = await useInboxStore.getState().setPR(item.id, url, branch);
    toast(r.ok ? tr("inbox.pr_saved") : inboxErrorText(tr, r.error), r.ok ? { kind: "success" } : undefined);
  };
  return (
    <div className="inbox-pr">
      <label className="ui-field">
        <span className="ui-field-label">{tr("inbox.pr_url")}</span>
        <input type="url" value={url} onChange={(e) => setUrl(e.target.value)} placeholder="https://" />
      </label>
      <label className="ui-field">
        <span className="ui-field-label">{tr("inbox.pr_branch")}</span>
        <input value={branch} onChange={(e) => setBranch(e.target.value)} />
      </label>
      <Button small onClick={() => void save()}>
        {tr("inbox.pr_save")}
      </Button>
    </div>
  );
}

/** MemberPicker searches the tenant through the share recipient search (at most 20 rows)
 *  and adds the chosen member's key. */
function MemberPicker({ chosen, onChange, max }: { chosen: InboxPerson[]; onChange: (p: InboxPerson[]) => void; max?: number }) {
  const tr = useT();
  const [q, setQ] = useState("");
  const [hits, setHits] = useState<InboxPerson[]>([]);
  useEffect(() => {
    let live = true;
    const t = window.setTimeout(async () => {
      const d = await api(`api/session-share-recipients?q=${encodeURIComponent(q.trim())}`).catch(() => null);
      if (!live) return;
      const rows = d && !d.error && Array.isArray(d.members) ? (d.members as { userKey: string; email: string }[]) : [];
      setHits(rows.map((m) => ({ userKey: m.userKey, email: m.email, removed: false })));
    }, 200);
    return () => {
      live = false;
      window.clearTimeout(t);
    };
  }, [q]);
  const keys = useMemo(() => new Set(chosen.map((c) => c.userKey)), [chosen]);
  const full = max !== undefined && chosen.length >= max;
  return (
    <div className="inbox-picker">
      <ul className="inbox-chips">
        {chosen.map((c) => (
          <li key={c.userKey} className="inbox-chip">
            {c.email || c.userKey}
            <button type="button" aria-label={tr("inbox.remove")} onClick={() => onChange(chosen.filter((x) => x.userKey !== c.userKey))}>
              <Icon name="close" />
            </button>
          </li>
        ))}
      </ul>
      <input value={q} onChange={(e) => setQ(e.target.value)} placeholder={tr("inbox.compose_search")} aria-label={tr("inbox.compose_search")} />
      {full && <p className="ui-field-hint">{tr("inbox.compose_max_recipients", { max: String(max) })}</p>}
      <ul className="inbox-hits">
        {hits
          .filter((h) => !keys.has(h.userKey))
          .map((h) => (
            <li key={h.userKey}>
              <button type="button" disabled={full} onClick={() => onChange([...chosen, h])}>
                {h.email || h.userKey}
              </button>
            </li>
          ))}
      </ul>
    </div>
  );
}

function Compose({ onSent }: { onSent: () => void }) {
  const tr = useT();
  const toast = useToast();
  const [kind, setKind] = useState<InboxKind>("request");
  const [to, setTo] = useState<InboxPerson[]>([]);
  const [body, setBody] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const [recipe, setRecipe] = useState<RecipeDraft | null>(null);
  const [sessions, setSessions] = useState<SessionAttach[]>([]);
  const bytes = byteLength(body);
  const send = async () => {
    if (to.length === 0) {
      setErr(tr("inbox.compose_no_recipient"));
      return;
    }
    setBusy(true);
    const r = await useInboxStore.getState().send(
      kind,
      to.map((p) => p.userKey),
      body,
      buildAttachments(recipe, sessions, to),
    );
    setBusy(false);
    if (!r.ok) {
      setErr(inboxErrorText(tr, r.error));
      return;
    }
    toast(tr("inbox.compose_sent"), { kind: "success" });
    setBody("");
    setTo([]);
    setErr("");
    setRecipe(null);
    setSessions([]);
    onSent();
  };
  return (
    <div className="inbox-compose">
      <div className="ui-field">
        <span className="ui-field-label">{tr("inbox.compose_kind")}</span>
        <div className="ui-seg choice-seg">
          {(["request", "dm"] as const).map((k) => (
            <button key={k} type="button" className={"seg-btn" + (kind === k ? " active" : "")} onClick={() => setKind(k)}>
              {tr(k === "dm" ? "inbox.kind_dm" : "inbox.kind_request")}
            </button>
          ))}
        </div>
      </div>
      <div className="ui-field">
        <span className="ui-field-label">{tr("inbox.compose_to")}</span>
        <MemberPicker chosen={to} onChange={setTo} max={INBOX_MAX_RECIPIENTS} />
      </div>
      <label className="ui-field">
        <span className="ui-field-label">{tr("inbox.compose_body")}</span>
        <textarea rows={6} value={body} onChange={(e) => setBody(e.target.value)} />
        <span className={"ui-field-hint" + (bytes > INBOX_BODY_MAX_BYTES ? " inbox-warn" : "")}>
          {tr("inbox.compose_bytes", { n: String(bytes), max: String(INBOX_BODY_MAX_BYTES) })}
        </span>
      </label>
      <label className="inbox-check">
        <input type="checkbox" checked={!!recipe} onChange={(e) => setRecipe(e.target.checked ? emptyRecipe() : null)} />
        <span>{tr("inbox.attach_recipe")}</span>
      </label>
      {recipe && <RecipeEditor value={recipe} onChange={setRecipe} />}
      <SessionAttachEditor value={sessions} recipients={to} onChange={setSessions} />
      <p className="ui-field-hint">{tr("inbox.compose_agents_note")}</p>
      {err && (
        <p className="ui-field-hint inbox-warn" role="alert">
          {err}
        </p>
      )}
      <div className="inbox-actions">
        <Button variant="primary" disabled={busy || !body.trim() || bytes > INBOX_BODY_MAX_BYTES} onClick={() => void send()}>
          <Icon name="send" /> {tr("inbox.compose_send")}
        </Button>
      </div>
    </div>
  );
}

function SettingsPane() {
  const tr = useT();
  const toast = useToast();
  const [s, setS] = useState<InboxSettings | null>(null);
  const load = async () => {
    const d = await api("api/inbox/settings").catch(() => null);
    if (d && !d.error) setS(d as InboxSettings);
  };
  useEffect(() => {
    void load();
  }, []);
  if (!s) return null;
  const save = async () => {
    const d = await apiJSON("api/inbox/settings", "PUT", {
      acceptFrom: s.acceptFrom,
      dnd: s.dnd,
      allowed: s.allowed.map((p) => p.userKey),
    }).catch(() => null);
    if (!d || d.error) {
      toast(errText(d?.error));
      return;
    }
    setS(d as InboxSettings);
    toast(tr("inbox.settings_saved"), { kind: "success" });
    void useInboxStore.getState().refresh();
  };
  const unblock = async (p: InboxPerson) => {
    const d = await apiJSON(`api/inbox/blocks/${encodeURIComponent(p.userKey)}`, "DELETE").catch(() => null);
    if (d && !d.error) setS(d as InboxSettings);
  };
  return (
    <div className="inbox-settings">
      <div className="ui-field">
        <span className="ui-field-label">{tr("inbox.settings_accept")}</span>
        <div className="ui-seg choice-seg">
          {(["tenant", "chosen"] as const).map((v) => (
            <button
              key={v}
              type="button"
              className={"seg-btn" + (s.acceptFrom === v ? " active" : "")}
              onClick={() => setS({ ...s, acceptFrom: v })}
            >
              {tr(v === "tenant" ? "inbox.accept_tenant" : "inbox.accept_chosen")}
            </button>
          ))}
        </div>
      </div>
      {s.acceptFrom === "chosen" && (
        <div className="ui-field">
          <span className="ui-field-label">{tr("inbox.settings_chosen")}</span>
          <MemberPicker chosen={s.allowed} onChange={(allowed) => setS({ ...s, allowed })} />
        </div>
      )}
      <label className="inbox-check">
        <input type="checkbox" checked={s.dnd} onChange={(e) => setS({ ...s, dnd: e.target.checked })} />
        <span>{tr("inbox.settings_dnd")}</span>
      </label>
      <p className="ui-field-hint">{tr("inbox.settings_handoff_note")}</p>
      <div className="inbox-actions">
        <Button variant="primary" onClick={() => void save()}>
          {tr("inbox.settings_save")}
        </Button>
      </div>
      <div className="ui-field">
        <span className="ui-field-label">{tr("inbox.settings_blocked")}</span>
        {s.blocked.length === 0 ? (
          <p className="ui-field-hint">{tr("inbox.settings_none_blocked")}</p>
        ) : (
          <ul className="inbox-recipients">
            {s.blocked.map((p) => (
              <li key={p.userKey}>
                {p.email || p.userKey}{" "}
                <Button small variant="ghost" onClick={() => void unblock(p)}>
                  {tr("inbox.unblock")}
                </Button>
              </li>
            ))}
          </ul>
        )}
      </div>
      <p className="ui-field-hint">{tr(s.sealed ? "inbox.sealed" : "inbox.unsealed")}</p>
    </div>
  );
}
