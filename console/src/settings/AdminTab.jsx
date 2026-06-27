import { useCallback, useEffect, useState } from "react";
import { api, apiJSON, rawJSON } from "../api.js";

// AdminTab (super_admin only): list tenants, create them, edit per-tenant limits,
// manage members (add / set session limit / force-stop their workspace).
export default function AdminTab() {
  const [tenants, setTenants] = useState(null);
  const [sel, setSel] = useState(null); // slug
  const [forbidden, setForbidden] = useState(false);

  const loadTenants = useCallback(async () => {
    try {
      const d = await api("api/admin/tenants");
      if (d && d.error) {
        setForbidden(true);
        return;
      }
      setTenants(d.tenants || []);
    } catch {
      setForbidden(true);
    }
  }, []);
  useEffect(() => {
    loadTenants();
  }, [loadTenants]);

  if (forbidden) return <p className="muted pad">権限がありません（super_admin のみ）。</p>;
  if (tenants === null) return <p className="muted pad">読み込み中…</p>;

  return (
    <div className="admin">
      <div className="admin-left">
        <div className="sub-head">テナント</div>
        <ul className="list">
          {tenants.map((t) => (
            <li
              key={t.slug}
              className={"admin-tenant" + (sel === t.slug ? " active" : "")}
              onClick={() => setSel(t.slug)}
              title={t.slug}
            >
              {t.name} · {t.users}u · {t.running}▶ · w:{t.max_workspaces || "∞"} s:{t.max_sessions || "∞"}
            </li>
          ))}
        </ul>
        <NewTenant onCreated={loadTenants} />
      </div>
      <div className="admin-detail">
        {sel ? (
          <TenantDetail
            slug={sel}
            tenant={tenants.find((t) => t.slug === sel)}
            onChanged={loadTenants}
          />
        ) : (
          <p className="muted pad">テナントを選択</p>
        )}
      </div>
    </div>
  );
}

function NewTenant({ onCreated }) {
  const [slug, setSlug] = useState("");
  const [name, setName] = useState("");
  const submit = async (e) => {
    e.preventDefault();
    if (!slug.trim()) return;
    const r = await rawJSON("api/admin/tenants", "POST", { slug: slug.trim(), name: name.trim() });
    if (r.ok) {
      setSlug("");
      setName("");
      onCreated();
    } else {
      const er = await r.json().catch(() => ({}));
      alert("作成に失敗: " + (er.error?.message || r.status));
    }
  };
  return (
    <form className="form" onSubmit={submit}>
      <div className="sub-head">新規テナント</div>
      <input value={slug} onChange={(e) => setSlug(e.target.value)} placeholder="slug" />
      <input value={name} onChange={(e) => setName(e.target.value)} placeholder="name" />
      <button type="submit">作成</button>
    </form>
  );
}

function TenantDetail({ slug, tenant, onChanged }) {
  const [maxWs, setMaxWs] = useState(tenant?.max_workspaces || 0);
  const [maxSs, setMaxSs] = useState(tenant?.max_sessions || 0);
  const [members, setMembers] = useState(null);

  useEffect(() => {
    setMaxWs(tenant?.max_workspaces || 0);
    setMaxSs(tenant?.max_sessions || 0);
  }, [slug, tenant]);

  const loadMembers = useCallback(async () => {
    try {
      const d = await api(`api/admin/tenants/${encodeURIComponent(slug)}/members`);
      setMembers(d.members || []);
    } catch {
      setMembers([]);
    }
  }, [slug]);
  useEffect(() => {
    setMembers(null);
    loadMembers();
  }, [loadMembers]);

  const saveLimits = async () => {
    await apiJSON(`api/admin/tenants/${encodeURIComponent(slug)}/limits`, "PUT", {
      max_workspaces: +maxWs || 0,
      max_sessions: +maxSs || 0,
    });
    onChanged();
  };

  return (
    <div className="tenant-detail">
      <div className="sub-head">上限（0 = 無制限）</div>
      <div className="form-row">
        <label>
          最大 Workspace
          <input type="number" min="0" value={maxWs} onChange={(e) => setMaxWs(e.target.value)} />
        </label>
        <label>
          最大 Session
          <input type="number" min="0" value={maxSs} onChange={(e) => setMaxSs(e.target.value)} />
        </label>
        <button onClick={saveLimits}>保存</button>
      </div>

      <div className="sub-head">メンバー</div>
      {members === null ? (
        <p className="muted">…</p>
      ) : members.length === 0 ? (
        <p className="muted">メンバーなし</p>
      ) : (
        <div className="members">
          {members.map((m) => (
            <MemberRow key={m.user_key} m={m} slug={slug} onChanged={() => { loadMembers(); onChanged(); }} />
          ))}
        </div>
      )}
      <AddMember slug={slug} onAdded={loadMembers} />
    </div>
  );
}

function MemberRow({ m, slug, onChanged }) {
  const stop = async () => {
    if (!confirm(`${m.user_key} の ${slug} の Workspace を停止しますか？`)) return;
    await apiJSON("api/admin/stop-workspace", "POST", { tenant_slug: slug, user_key: m.user_key });
    onChanged();
  };
  const setLimit = async () => {
    const v = prompt(`${m.user_key} の最大セッション数 (0 = 無制限)`, m.max_sessions ?? 0);
    if (v == null) return;
    await apiJSON("api/admin/user-limits", "PUT", {
      user_key: m.user_key,
      tenant_slug: slug,
      max_sessions: +v || 0,
    });
    onChanged();
  };
  return (
    <div className="member">
      <span className="m-who mono" title={m.user_key + (m.email ? ` <${m.email}>` : "")}>
        {m.user_key}
        {m.email ? ` <${m.email}>` : ""}
        {m.super_admin ? " ★" : ""}
      </span>
      <span className="muted">
        {m.role}
        {m.max_sessions != null ? ` · s≤${m.max_sessions}` : ""}
      </span>
      <span className={"m-state " + (m.state === "running" ? "on" : "off")}>{m.state}</span>
      <span className="chg-acts">
        {m.state === "running" && (
          <button className="icon danger" title="Workspace 強制停止" onClick={stop}>
            ⏹
          </button>
        )}
        <button className="icon" title="セッション上限を設定" onClick={setLimit}>
          ≤
        </button>
      </span>
    </div>
  );
}

function AddMember({ slug, onAdded }) {
  const [email, setEmail] = useState("");
  const [key, setKey] = useState("");
  const [role, setRole] = useState("member");
  const submit = async (e) => {
    e.preventDefault();
    const r = await rawJSON("api/admin/memberships", "POST", {
      email: email.trim(),
      user_key: key.trim(),
      tenant_slug: slug,
      role,
    });
    if (r.ok) {
      setEmail("");
      setKey("");
      onAdded();
    } else {
      const er = await r.json().catch(() => ({}));
      alert("追加に失敗: " + (er.error?.message || r.status));
    }
  };
  return (
    <form className="form add-member" onSubmit={submit}>
      <div className="sub-head">メンバー追加</div>
      <div className="form-row">
        <input value={email} onChange={(e) => setEmail(e.target.value)} placeholder="email" />
        <input value={key} onChange={(e) => setKey(e.target.value)} placeholder="or user_key" />
        <select value={role} onChange={(e) => setRole(e.target.value)}>
          <option value="member">member</option>
          <option value="tenant_admin">tenant_admin</option>
        </select>
        <button type="submit">追加</button>
      </div>
    </form>
  );
}
