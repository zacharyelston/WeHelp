// WeHelp Registry console (#61). WeHelp support runs practices, providers,
// install tokens and Relays here. No patient data exists in the Registry.
// The page's CSP allows no inline script, so actions use data-action
// attributes and one delegated click handler.
"use strict";

let me = null; // { status, email, name, role, csrf_token }
let tab = "practices";
let selected = null; // practice ID shown in detail
let healthTimer = null;

const $ = (id) => document.getElementById(id);
const isAdmin = () => me && me.role === "registry-admin";

// esc escapes for HTML text and for quoted attribute values alike (#159):
// both quote characters too, so a value can't end its attribute.
function esc(s) {
  return String(s ?? "").replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;").replace(/'/g, "&#39;");
}

function when(t) {
  return t ? new Date(t).toLocaleString() : "—";
}

async function api(method, path, body) {
  const headers = {};
  if (body !== undefined) headers["Content-Type"] = "application/json";
  if (me && me.csrf_token && method !== "GET") headers["X-CSRF-Token"] = me.csrf_token;
  const res = await fetch("/v1/console" + path, {
    method, headers, credentials: "same-origin",
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (res.status === 401 && path !== "/login" && !path.startsWith("/totp")) {
    me = null;
    render();
    throw new Error("Signed out");
  }
  if (res.status === 204) return null;
  const data = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(data.error || `HTTP ${res.status}`);
  return data;
}

function alertBox(msg, kind = "error") {
  const el = $("alert");
  if (!el) return;
  el.className = `alert alert-${kind}`;
  el.textContent = msg;
  el.classList.remove("hidden");
  setTimeout(() => el.classList.add("hidden"), 5000);
}

// ---------------------------------------------------------------------------
// Sign-in
// ---------------------------------------------------------------------------

function render() {
  clearInterval(healthTimer);
  if (!me) return renderLogin();
  if (me.status === "totp_setup") return renderTOTPSetup();
  renderShell();
}

function renderLogin() {
  $("app").innerHTML = `
    <div class="card login-card">
      <h1>WeHelp Registry</h1>
      <div id="alert" class="hidden"></div>
      <form id="login-form">
        <div class="form-group"><label>Email</label><input id="email" type="email" autocomplete="username" required></div>
        <div class="form-group"><label>Password</label><input id="password" type="password" autocomplete="current-password" required></div>
        <div class="form-group"><label>Authenticator code</label><input id="code" inputmode="numeric" autocomplete="one-time-code" placeholder="Leave empty the first time"></div>
        <button class="btn" type="submit">Sign in</button>
      </form>
    </div>`;
  $("login-form").onsubmit = async (e) => {
    e.preventDefault();
    try {
      me = await api("POST", "/login", { email: $("email").value, password: $("password").value, code: $("code").value });
      render();
    } catch (err) {
      alertBox(err.message);
    }
  };
}

async function renderTOTPSetup() {
  $("app").innerHTML = `
    <div class="card login-card">
      <h1>Set up two-step sign-in</h1>
      <div id="alert" class="hidden"></div>
      <p>Scan this with an authenticator app, then enter the 6-digit code it shows.</p>
      <img id="qr" class="qr" alt="Authenticator QR code">
      <p class="hint">Or enter this key: <span id="secret" class="token"></span></p>
      <form id="totp-form">
        <div class="form-group"><label>Code</label><input id="code" inputmode="numeric" autocomplete="one-time-code" required></div>
        <button class="btn" type="submit">Turn on</button>
      </form>
    </div>`;
  try {
    const s = await api("POST", "/totp/setup");
    $("qr").src = "data:image/png;base64," + s.qr_png;
    $("secret").textContent = s.secret;
  } catch (err) {
    alertBox(err.message);
  }
  $("totp-form").onsubmit = async (e) => {
    e.preventDefault();
    try {
      me = await api("POST", "/totp/confirm", { code: $("code").value });
      render();
    } catch (err) {
      alertBox(err.message);
    }
  };
}

// ---------------------------------------------------------------------------
// Shell and tabs
// ---------------------------------------------------------------------------

function renderShell() {
  const tabs = [["practices", "Practices"], ["relays", "Relays"], ["health", "Health"], ["audit", "Audit log"]];
  $("app").innerHTML = `
    <div class="header">
      <h1>WeHelp Registry</h1>
      <div class="user">${esc(me.name)} · ${esc(me.role)}
        <button class="btn btn-sm btn-outline" data-action="logout">Sign out</button></div>
    </div>
    <div class="tabs">${tabs.map(([k, l]) => `<div class="tab ${tab === k ? "active" : ""}" data-action="tab" data-tab="${k}">${l}</div>`).join("")}</div>
    <div id="alert" class="hidden"></div>
    <div id="content"><p class="loading">Loading…</p></div>`;
  loadTab();
}

async function loadTab() {
  clearInterval(healthTimer);
  const c = $("content");
  try {
    if (tab === "practices") await loadPractices(c);
    else if (tab === "relays") await loadRelays(c);
    else if (tab === "health") {
      await loadHealth(c);
      healthTimer = setInterval(() => loadHealth(c).catch(() => {}), 30000);
    } else if (tab === "audit") await loadAudit(c);
  } catch (err) {
    alertBox(err.message);
  }
}

// ---------------------------------------------------------------------------
// Practices
// ---------------------------------------------------------------------------

async function loadPractices(c) {
  const [{ practices }, { relays }] = await Promise.all([api("GET", "/practices"), api("GET", "/relays")]);
  let html = `<div class="card"><h2>Practices</h2>`;
  if (practices.length === 0) {
    html += `<p class="empty">No practices yet.</p>`;
  } else {
    html += `<table><thead><tr><th>Practice</th><th>Status</th><th>Subscription</th><th>Relay</th><th>Office</th><th>Contact</th></tr></thead><tbody>`;
    for (const p of practices) {
      html += `<tr class="row-link" data-action="open" data-id="${p.id}">
        <td>${esc(p.name)}</td>
        <td><span class="badge badge-${esc(p.status)}">${esc(p.status)}</span></td>
        <td><span class="badge badge-${p.subscription === "cancelled" ? "cancelled_sub" : esc(p.subscription)}">${esc(p.subscription.replace("_", " "))}</span></td>
        <td>${esc(p.relay)}</td>
        <td>${p.office_id ? "Installed " + esc(new Date(p.installed_at).toLocaleDateString()) : "Not installed"}</td>
        <td>${esc(p.contact_name)}${p.contact_email ? "<br>" + esc(p.contact_email) : ""}</td>
      </tr>`;
    }
    html += `</tbody></table>`;
  }
  html += `</div><div id="detail"></div>
    <div class="card"><h2>New practice</h2>
      <form id="new-practice">
        <div class="form-row">
          <div class="form-group"><label>Name</label><input id="np-name" required></div>
          <div class="form-group"><label>Relay</label><select id="np-relay">${relays.map((r) => `<option>${esc(r.name)}</option>`).join("")}</select></div>
        </div>
        <div class="form-row">
          <div class="form-group"><label>Admin contact</label><input id="np-cname"></div>
          <div class="form-group"><label>Contact email</label><input id="np-cemail" type="email"></div>
          <div class="form-group"><label>Contact phone</label><input id="np-cphone"></div>
        </div>
        <button class="btn" type="submit">Create</button>
      </form></div>`;
  c.innerHTML = html;
  $("new-practice").onsubmit = async (e) => {
    e.preventDefault();
    try {
      const p = await api("POST", "/practices", {
        name: $("np-name").value, relay: $("np-relay").value,
        contact_name: $("np-cname").value, contact_email: $("np-cemail").value, contact_phone: $("np-cphone").value,
      });
      selected = p.id;
      alertBox("Practice created", "success");
      await loadPractices(c);
    } catch (err) {
      alertBox(err.message);
    }
  };
  if (selected) await showPractice(selected, relays);
}

async function showPractice(id, relays) {
  const p = await api("GET", `/practices/${id}`);
  relays = relays || (await api("GET", "/relays")).relays;
  const subs = ["trial", "active", "past_due", "cancelled"];
  let html = `<div class="card"><h2>${esc(p.name)}</h2>
    <form id="edit-practice">
      <div class="form-row">
        <div class="form-group"><label>Name</label><input id="ep-name" value="${esc(p.name)}" required></div>
        <div class="form-group"><label>Subscription</label><select id="ep-sub">${subs.map((s) => `<option value="${s}" ${s === p.subscription ? "selected" : ""}>${s.replace("_", " ")}</option>`).join("")}</select></div>
      </div>
      <div class="form-row">
        <div class="form-group"><label>Admin contact</label><input id="ep-cname" value="${esc(p.contact_name)}"></div>
        <div class="form-group"><label>Contact email</label><input id="ep-cemail" type="email" value="${esc(p.contact_email)}"></div>
        <div class="form-group"><label>Contact phone</label><input id="ep-cphone" value="${esc(p.contact_phone)}"></div>
      </div>
      <div class="form-row">
        <div class="form-group"><label>Video visits at once</label><input id="ep-visits" type="number" min="1" max="200"
          value="${p.turn_max_visits == null ? "" : esc(String(p.turn_max_visits))}" placeholder="Automatic (${esc(String(p.max_visits))})"></div>
      </div>
      <p class="hint">Live video visits this practice can have on its Relay at once. Leave it empty for automatic: one more than its providers, at least 4. Now ${esc(String(p.max_visits))}.</p>
      <button class="btn" type="submit">Save</button>
    </form>
    <div class="actions">
      <button class="btn btn-outline" data-action="token" data-id="${p.id}" ${p.status !== "active" ? "disabled" : ""}>Issue install token</button>`;
  if (isAdmin()) {
    html += p.status === "active"
      ? `<button class="btn btn-danger" data-action="suspend" data-id="${p.id}">Suspend</button>`
      : `<button class="btn" data-action="reactivate" data-id="${p.id}">Reactivate</button>`;
  }
  html += `</div><div id="token-out"></div>`;
  if (isAdmin() && !p.office_id) {
    html += `<div class="form-row actions"><div class="form-group"><label>Relay</label>
      <select id="ep-relay">${relays.map((r) => `<option value="${r.id}" ${r.id === p.relay_id ? "selected" : ""}>${esc(r.name)} (${r.practices}/${r.capacity})</option>`).join("")}</select></div>
      <div class="form-group"><label>&nbsp;</label><button class="btn btn-outline" data-action="relay" data-id="${p.id}">Move to Relay</button></div></div>`;
  } else if (p.office_id) {
    html += `<p class="hint">Relay: ${esc(p.relay)}. Office ${esc(p.office_id)} installed ${esc(when(p.installed_at))}.</p>`;
  }
  html += `<h2>Providers and admins</h2>`;
  const provs = p.providers || [];
  if (provs.length === 0) html += `<p class="empty">None yet.</p>`;
  else {
    html += `<table><thead><tr><th>Name</th><th>Email</th><th>Role</th><th>Credentials</th><th>Licence</th><th></th></tr></thead><tbody>`;
    for (const v of provs) {
      html += `<tr><td>${esc(v.name)}</td><td>${esc(v.email)}</td><td>${esc(v.role)}</td><td>${esc(v.credentials)}</td>
        <td>${esc(v.licence_number)}</td><td><button class="btn btn-sm btn-danger" data-action="remove-provider" data-id="${v.id}">Remove</button></td></tr>`;
    }
    html += `</tbody></table>`;
  }
  html += `<form id="new-provider" class="actions">
      <div class="form-row">
        <div class="form-group"><label>Name</label><input id="pv-name" required></div>
        <div class="form-group"><label>Email</label><input id="pv-email" type="email" required></div>
        <div class="form-group"><label>Role</label><select id="pv-role"><option value="provider">Provider</option><option value="admin">Admin</option></select></div>
        <div class="form-group"><label>Credentials</label><input id="pv-cred" placeholder="MD, NP…"></div>
        <div class="form-group"><label>Licence number</label><input id="pv-lic"></div>
      </div>
      <button class="btn" type="submit">Add</button>
    </form></div>`;
  $("detail").innerHTML = html;
  $("detail").scrollIntoView({ behavior: "smooth", block: "start" });

  $("edit-practice").onsubmit = async (e) => {
    e.preventDefault();
    await act(() => api("PATCH", `/practices/${p.id}`, {
      name: $("ep-name").value, subscription: $("ep-sub").value,
      contact_name: $("ep-cname").value, contact_email: $("ep-cemail").value, contact_phone: $("ep-cphone").value,
      turn_max_visits: $("ep-visits").value === "" ? 0 : Number($("ep-visits").value),
    }), "Saved");
  };
  $("new-provider").onsubmit = async (e) => {
    e.preventDefault();
    await act(() => api("POST", `/practices/${p.id}/providers`, {
      name: $("pv-name").value, email: $("pv-email").value, role: $("pv-role").value,
      credentials: $("pv-cred").value, licence_number: $("pv-lic").value,
    }), "Provider added");
  };
}

// act runs a change, reports it, and reloads the current tab.
async function act(fn, ok) {
  try {
    const out = await fn();
    if (ok) alertBox(ok, "success");
    await loadTab();
    return out;
  } catch (err) {
    alertBox(err.message);
  }
}

// ---------------------------------------------------------------------------
// Relays, health, audit
// ---------------------------------------------------------------------------

function relayBadge(r) {
  return r.up ? `<span class="badge badge-up">up</span>` : `<span class="badge badge-down">${r.reported_at ? "not reporting" : "never reported"}</span>`;
}

async function loadRelays(c) {
  const { relays } = await api("GET", "/relays");
  let html = `<div class="card"><h2>Relays</h2><table><thead><tr><th>Relay</th><th>Offices dial</th><th>Phones dial</th>
    <th>Practices / capacity</th><th>Status</th><th>Last report</th></tr></thead><tbody>`;
  for (const r of relays) {
    const cap = isAdmin()
      ? `${r.practices} / <input class="capacity" type="number" min="1" id="cap-${r.id}" value="${r.capacity}">
         <button class="btn btn-sm btn-outline" data-action="capacity" data-id="${r.id}">Set</button>`
      : `${r.practices} / ${r.capacity}`;
    html += `<tr><td>${esc(r.name)}</td><td>${esc(r.ssh_address)}</td><td>${esc(r.connect_address)}</td>
      <td>${cap}</td><td>${relayBadge(r)}</td><td>${esc(when(r.reported_at))}</td></tr>`;
  }
  html += `</tbody></table><p class="hint">Relays are added on the Registry host with <code>wehelp-registry relay add</code>.</p></div>`;
  c.innerHTML = html;
}

async function loadHealth(c) {
  const h = await api("GET", "/health");
  let html = `<div class="card"><h2>Relays</h2><table><thead><tr><th>Relay</th><th>Status</th><th>Office list</th><th>Offices connected</th><th>Last report</th></tr></thead><tbody>`;
  for (const r of h.relays) {
    const lag = r.list_version != null && r.list_version < h.latest_list_version ? ` (latest is v${h.latest_list_version})` : "";
    html += `<tr><td>${esc(r.name)}</td><td>${relayBadge(r)}</td><td>${r.list_version != null ? "v" + r.list_version + esc(lag) : "—"}</td>
      <td>${r.connected_offices}</td><td>${esc(when(r.reported_at))}</td></tr>`;
  }
  html += `</tbody></table></div><div class="card"><h2>Offices</h2>`;
  if (h.offices.length === 0) html += `<p class="empty">No offices installed.</p>`;
  else {
    html += `<table><thead><tr><th>Practice</th><th>Relay</th><th>Practice status</th><th>Connected</th></tr></thead><tbody>`;
    for (const o of h.offices) {
      html += `<tr><td>${esc(o.practice_name)}</td><td>${esc(o.relay)}</td>
        <td><span class="badge badge-${esc(o.status)}">${esc(o.status)}</span></td>
        <td>${o.connected ? '<span class="badge badge-up">connected</span>' : '<span class="badge badge-down">not connected</span>'}</td></tr>`;
    }
    html += `</tbody></table>`;
  }
  html += `<p class="hint">From each Relay's signed report, sent every minute. Refreshes every 30 seconds.</p></div>`;
  c.innerHTML = html;
}

async function loadAudit(c) {
  const { events } = await api("GET", "/audit?limit=200");
  let html = `<div class="card"><h2>Audit log</h2><table><thead><tr><th>#</th><th>When</th><th>Who</th><th>Action</th><th>What</th></tr></thead><tbody>`;
  for (const e of events) {
    html += `<tr><td>${e.seq}</td><td>${esc(when(e.created_at))}</td><td>${esc(e.actor)}</td><td>${esc(e.action)}</td>
      <td>${esc(e.resource_type)} ${esc(e.resource_id.slice(0, 8))}</td></tr>`;
  }
  html += `</tbody></table><p class="hint">Hash-chained; check it with <code>wehelp-registry audit verify</code>.</p></div>`;
  c.innerHTML = html;
}

// ---------------------------------------------------------------------------
// Actions
// ---------------------------------------------------------------------------

document.addEventListener("click", async (e) => {
  const el = e.target.closest("[data-action]");
  if (!el) return;
  const id = el.dataset.id;
  switch (el.dataset.action) {
    case "tab":
      tab = el.dataset.tab;
      selected = null;
      renderShell();
      break;
    case "logout":
      await fetch("/v1/console/logout", { method: "POST", credentials: "same-origin" });
      me = null;
      render();
      break;
    case "open":
      selected = id;
      try { await showPractice(id); } catch (err) { alertBox(err.message); }
      break;
    case "token":
      try {
        const t = await api("POST", `/practices/${id}/install-token`);
        $("token-out").innerHTML = `<p class="hint">Install token (shown once, valid until ${esc(when(t.expires_at))}). Run <code>wehelpd init --install-token …</code> on the office machine.</p>
          <div class="token">${esc(t.install_token)}</div>`;
      } catch (err) { alertBox(err.message); }
      break;
    case "suspend":
      if (confirm("Suspend this practice? Its office is disconnected from its Relay within a minute.")) {
        await act(() => api("POST", `/practices/${id}/suspend`), "Suspended");
      }
      break;
    case "reactivate":
      await act(() => api("POST", `/practices/${id}/reactivate`), "Reactivated");
      break;
    case "relay":
      await act(() => api("POST", `/practices/${id}/relay`, { relay_id: $("ep-relay").value }), "Relay changed");
      break;
    case "remove-provider":
      if (confirm("Remove this provider from the practice?")) await act(() => api("DELETE", `/providers/${id}`), "Removed");
      break;
    case "capacity":
      await act(() => api("PUT", `/relays/${id}/capacity`, { capacity: Number($("cap-" + id).value) }), "Capacity set");
      break;
  }
});

(async () => {
  try { me = await api("GET", "/me"); } catch { me = null; }
  render();
})();
