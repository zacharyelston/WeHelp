// WeHelp staff web UI — vanilla JS SPA, no framework, no build step.
// Embedded in the Go binary via embed.FS; served on the office LAN only.
//
// Sign-in (#38): password + TOTP. The session lives in a server-side cookie
// (Secure, HttpOnly, SameSite=Strict) that this script never sees; changes
// carry the session's CSRF token in a header. Nothing is kept in
// localStorage.

const API = "/api/v1";

// ---------------------------------------------------------------------------
// State
// ---------------------------------------------------------------------------
let session = null; // { status, user_id, role, display_name, practice, csrf_token }
let activeTab = "patients";

// ---------------------------------------------------------------------------
// API helpers
// ---------------------------------------------------------------------------
async function api(method, path, body) {
  const opts = {
    method,
    credentials: "same-origin",
    headers: { "content-type": "application/json" },
  };
  if (session?.csrf_token) opts.headers["x-csrf-token"] = session.csrf_token;
  if (body) opts.body = JSON.stringify(body);
  const res = await fetch(API + path, opts);
  const data = await res.json().catch(() => ({}));
  if (res.status === 401 && session?.status === "active") {
    session = null;
    render();
    throw new Error("Your session ended. Please sign in again.");
  }
  if (!res.ok) throw new Error(data.error || res.statusText);
  return data;
}

// ---------------------------------------------------------------------------
// Auth
// ---------------------------------------------------------------------------
async function login(tenant, email, password, code) {
  session = await api("POST", "/staff/login", { tenant, email, password, code });
  render();
}

async function logout() {
  try {
    await api("POST", "/staff/logout");
  } catch (_) {
    // The cookie is cleared either way.
  }
  session = null;
  render();
}

// ---------------------------------------------------------------------------
// Render
// ---------------------------------------------------------------------------
function render() {
  const app = document.getElementById("app");
  if (!session) {
    app.innerHTML = renderLogin();
    bindLogin();
    return;
  }
  if (session.status === "totp_setup") {
    renderTOTPSetup(app);
    return;
  }
  app.innerHTML = renderDashboard();
  bindDashboard();
  loadTab();
}

function renderLogin() {
  return `
    <div class="card login-card">
      <h1>WeHelp</h1>
      <p class="hint">Staff sign-in. This page works only on the office network.</p>
      <div id="login-error"></div>
      <form id="login-form">
        <div class="form-group">
          <label>Practice</label>
          <input type="text" id="login-tenant" value="Demo Clinic" required>
        </div>
        <div class="form-group">
          <label>Email</label>
          <input type="email" id="login-email" placeholder="you@demo.wehelp" autocomplete="username" required>
        </div>
        <div class="form-group">
          <label>Password</label>
          <input type="password" id="login-password" autocomplete="current-password" required>
        </div>
        <div class="form-group">
          <label>Authenticator code</label>
          <input type="text" id="login-code" inputmode="numeric" autocomplete="one-time-code" pattern="[0-9 ]*" maxlength="7"
            placeholder="6 digits (leave empty the first time)">
        </div>
        <button type="submit" class="btn" style="width:100%">Sign in</button>
      </form>
      <p style="margin-top:1rem;font-size:0.8rem;color:var(--muted)">
        Demo: dr.ada.shaw@demo.wehelp / wehelp-demo-provider
      </p>
    </div>`;
}

function bindLogin() {
  const form = document.getElementById("login-form");
  form.onsubmit = async (e) => {
    e.preventDefault();
    try {
      await login(
        document.getElementById("login-tenant").value,
        document.getElementById("login-email").value,
        document.getElementById("login-password").value,
        document.getElementById("login-code").value,
      );
    } catch (err) {
      document.getElementById("login-error").innerHTML =
        `<div class="alert alert-error">${esc(err.message)}</div>`;
    }
  };
}

// First sign-in: set up the authenticator app.
async function renderTOTPSetup(app) {
  app.innerHTML = `<div class="card login-card"><h1>Set up two-step sign-in</h1><div class="loading">Loading…</div></div>`;
  let setup;
  try {
    setup = await api("POST", "/staff/totp/setup");
  } catch (err) {
    session = null;
    app.innerHTML = `<div class="card login-card"><div class="alert alert-error">${esc(err.message)}</div>
      <button class="btn" onclick="render()">Back to sign in</button></div>`;
    return;
  }
  app.innerHTML = `
    <div class="card login-card pairing">
      <h1>Set up two-step sign-in</h1>
      <p>Scan this with an authenticator app (for example Google Authenticator, 1Password or Microsoft Authenticator).</p>
      <img class="qr" alt="Authenticator QR code" src="data:image/png;base64,${setup.qr_png}">
      <p class="hint">Or enter this key: <code>${esc(setup.secret.replace(/(.{4})/g, "$1 ").trim())}</code></p>
      <div id="setup-error"></div>
      <form id="setup-form">
        <div class="form-group">
          <label>Code from the app</label>
          <input type="text" id="setup-code" inputmode="numeric" autocomplete="one-time-code" maxlength="7" required>
        </div>
        <button type="submit" class="btn" style="width:100%">Turn on two-step sign-in</button>
      </form>
    </div>`;
  document.getElementById("setup-form").onsubmit = async (e) => {
    e.preventDefault();
    try {
      session = await api("POST", "/staff/totp/confirm", { code: document.getElementById("setup-code").value });
      render();
    } catch (err) {
      document.getElementById("setup-error").innerHTML = `<div class="alert alert-error">${esc(err.message)}</div>`;
    }
  };
}

function renderDashboard() {
  const role = session.role || "";
  const who = session.display_name || (session.user_id || "").slice(0, 8);
  return `
    <div class="header">
      <h1>WeHelp</h1>
      <div>
        <span class="user">${esc(who)} · ${esc(role)}</span>
        <button class="btn btn-sm btn-outline" onclick="showMyDevices()" style="margin-left:0.5rem">My devices</button>
        <button class="btn btn-sm btn-outline" onclick="logout()" style="margin-left:0.5rem">Log out</button>
      </div>
    </div>
    <div id="alert-area"></div>
    <div class="tabs">
      <div class="tab ${activeTab === "patients" ? "active" : ""}" data-tab="patients">Patients</div>
      <div class="tab ${activeTab === "messages" ? "active" : ""}" data-tab="messages">Messages</div>
      <div class="tab ${activeTab === "appointments" ? "active" : ""}" data-tab="appointments">Appointments</div>
      <div class="tab ${activeTab === "links" ? "active" : ""}" data-tab="links">Links</div>
    </div>
    <div id="tab-content"></div>`;
}

function bindDashboard() {
  document.querySelectorAll(".tab").forEach((t) => {
    t.onclick = () => {
      activeTab = t.dataset.tab;
      document.querySelectorAll(".tab").forEach((x) => x.classList.remove("active"));
      t.classList.add("active");
      loadTab();
    };
  });
}

function showAlert(msg, type = "error") {
  const area = document.getElementById("alert-area");
  if (!area) return;
  area.innerHTML = `<div class="alert alert-${type}">${esc(msg)}</div>`;
  setTimeout(() => { if (area) area.innerHTML = ""; }, 4000);
}

async function loadTab() {
  const content = document.getElementById("tab-content");
  content.innerHTML = '<div class="loading">Loading…</div>';
  try {
    if (activeTab === "patients") await loadPatients(content);
    else if (activeTab === "messages") await loadMessages(content);
    else if (activeTab === "appointments") await loadAppointments(content);
    else if (activeTab === "links") await loadLinks(content);
  } catch (err) {
    content.innerHTML = `<div class="alert alert-error">${esc(err.message)}</div>`;
  }
}

// ---------------------------------------------------------------------------
// Messages tab (#67): conversations on the left, the open thread on the
// right, both directions, with names.
let openThread = null; // { id, name }

async function loadMessages(container) {
  const data = await api("GET", "/conversations");
  const convs = data.conversations || [];
  let html = '<div class="threads"><div class="card conv-list"><h2>Conversations</h2>';
  if (convs.length === 0) {
    html += '<p class="empty">No linked patients yet.</p>';
  }
  for (const c of convs) {
    const when = c.last_at ? new Date(c.last_at).toLocaleString() : "";
    const preview = c.last_body ? (c.last_from_me ? "You: " : "") + c.last_body : "No messages yet";
    html += `<div class="conv ${openThread && openThread.id === c.user_id ? "active" : ""}" onclick="openConversation('${esc(c.user_id)}', '${esc(c.name).replace(/'/g, "&#39;")}')">
      <div class="conv-top"><strong>${esc(c.name || "Unnamed")}</strong>
        ${c.unread_count > 0 ? `<span class="badge badge-pending">${c.unread_count}</span>` : ""}</div>
      <div class="conv-preview">${esc(preview)}</div><div class="hint">${esc(when)}</div></div>`;
  }
  html += '</div><div class="card thread" id="thread"><p class="empty">Choose a conversation.</p></div></div>';
  container.innerHTML = html;
  if (openThread) showThread();
}

function openConversation(id, name) {
  openThread = { id, name };
  document.querySelectorAll(".conv").forEach((el) => el.classList.remove("active"));
  showThread();
}

async function showThread() {
  const el = document.getElementById("thread");
  if (!el || !openThread) return;
  let data;
  try {
    data = await api("GET", `/messages?with=${openThread.id}&limit=100`);
  } catch (err) {
    el.innerHTML = `<div class="alert alert-error">${esc(err.message)}</div>`;
    return;
  }
  const msgs = (data.messages || []).slice().reverse(); // oldest first, like a chat
  let html = `<h2>${esc(openThread.name)}</h2><div class="bubbles" id="bubbles">`;
  for (const m of msgs) {
    const mine = m.sender_id === session.user_id;
    html += `<div class="bubble ${mine ? "mine" : "theirs"}">
      ${m.kind !== "message" ? `<div class="kind">${esc(m.kind)}</div>` : ""}
      <div>${esc(m.body)}</div><div class="time">${new Date(m.created_at).toLocaleString()}</div></div>`;
  }
  if (msgs.length === 0) html += '<p class="empty">No messages yet. Say hello.</p>';
  html += `</div><form id="reply-form" class="reply">
      <select id="reply-kind"><option value="message">Message</option><option value="memo">Memo</option>
        <option value="checkin">Check-in</option><option value="reminder">Reminder</option></select>
      <textarea id="reply-body" placeholder="Write a reply" required></textarea>
      <button type="submit" class="btn">Send</button></form>`;
  el.innerHTML = html;
  const bubbles = document.getElementById("bubbles");
  bubbles.scrollTop = bubbles.scrollHeight;
  // Opening the thread reads their messages.
  for (const m of data.messages || []) {
    if (!m.read_at && m.recipient_id === session.user_id) {
      api("POST", `/messages/${m.id}/read`).catch(() => {});
    }
  }
  document.getElementById("reply-form").onsubmit = async (e) => {
    e.preventDefault();
    try {
      await api("POST", "/messages", {
        recipient_id: openThread.id,
        kind: document.getElementById("reply-kind").value,
        body: document.getElementById("reply-body").value,
      });
      showThread();
    } catch (err) {
      showAlert(err.message);
    }
  };
}

// ---------------------------------------------------------------------------
// Appointments tab
// ---------------------------------------------------------------------------
async function loadAppointments(container) {
  const data = await api("GET", "/appointments");
  const appts = data.appointments || [];
  const isProvider = session.role === "provider";

  let html = "";
  if (isProvider) {
    const links = await api("GET", "/links");
    const patients = links.filter((l) => l.status === "active" && l.provider_id === session.user_id)
      .map((l) => ({ id: l.patient_id, label: l.patient_id.slice(0, 8) }));

    html += '<div class="card"><h2>Schedule Appointment</h2>';
    if (patients.length === 0) {
      html += '<p class="empty">No active patients to schedule.</p>';
    } else {
      const now = new Date();
      const defaultStart = new Date(now.getTime() + 24 * 3600 * 1000).toISOString().slice(0, 16);
      html += `<form id="appt-form">
        <div class="form-row">
          <div class="form-group">
            <label>Patient</label>
            <select id="appt-patient">${patients.map((p) => `<option value="${p.id}">${p.label}</option>`).join("")}</select>
          </div>
        </div>
        <div class="form-row">
          <div class="form-group">
            <label>Starts At</label>
            <input type="datetime-local" id="appt-start" value="${defaultStart}" required>
          </div>
          <div class="form-group">
            <label>Ends At</label>
            <input type="datetime-local" id="appt-end" value="${defaultStart}" required>
          </div>
        </div>
        <button type="submit" class="btn">Schedule</button>
      </form>`;
    }
    html += "</div>";
  }

  html += '<div class="card"><h2>Appointments</h2>';
  if (appts.length === 0) {
    html += '<p class="empty">No appointments.</p>';
  } else {
    html += "<table><thead><tr><th>Start</th><th>End</th><th>Status</th><th>Patient</th>";
    if (isProvider) html += "<th>Actions</th>";
    html += "</tr></thead><tbody>";
    for (const a of appts) {
      const start = new Date(a.starts_at).toLocaleString();
      const end = new Date(a.ends_at).toLocaleString();
      const patient = a.patient_id === session.user_id ? "You" : a.patient_id.slice(0, 8);
      html += `<tr>
        <td>${start}</td><td>${end}</td>
        <td><span class="badge badge-${a.status}">${a.status}</span></td>
        <td>${patient}</td>`;
      if (isProvider && a.status === "scheduled") {
        html += `<td>
          <button class="btn btn-sm" onclick="cancelAppt('${a.id}')">Cancel</button>
          <button class="btn btn-sm btn-outline" onclick="completeAppt('${a.id}')">Complete</button>
        </td>`;
      } else if (isProvider) {
        html += "<td></td>";
      }
      html += "</tr>";
    }
    html += "</tbody></table>";
  }
  html += "</div>";

  container.innerHTML = html;
  const form = document.getElementById("appt-form");
  if (form) {
    form.onsubmit = async (e) => {
      e.preventDefault();
      try {
        const startVal = document.getElementById("appt-start").value;
        const endVal = document.getElementById("appt-end").value;
        await api("POST", "/appointments", {
          patient_id: document.getElementById("appt-patient").value,
          starts_at: new Date(startVal).toISOString(),
          ends_at: new Date(endVal).toISOString(),
        });
        showAlert("Appointment scheduled", "success");
        loadTab();
      } catch (err) {
        showAlert(err.message);
      }
    };
  }
}

async function cancelAppt(id) {
  try {
    await api("PATCH", `/appointments/${id}`, { status: "cancelled" });
    showAlert("Appointment cancelled", "success");
    loadTab();
  } catch (err) {
    showAlert(err.message);
  }
}

async function completeAppt(id) {
  try {
    await api("PATCH", `/appointments/${id}`, { status: "completed" });
    showAlert("Appointment completed", "success");
    loadTab();
  } catch (err) {
    showAlert(err.message);
  }
}

// ---------------------------------------------------------------------------
// Links tab
// ---------------------------------------------------------------------------
async function loadLinks(container) {
  const links = await api("GET", "/links");
  const isProvider = session.role === "provider";

  let html = "";
  if (isProvider) {
    html += `<div class="card"><h2>Invite Patient</h2>
      <form id="link-form">
        <div class="form-group">
          <label>Patient Email</label>
          <input type="email" id="link-email" placeholder="patient@demo.wehelp" required>
        </div>
        <button type="submit" class="btn">Invite</button>
      </form></div>`;
  }

  html += '<div class="card"><h2>Links</h2>';
  if (links.length === 0) {
    html += '<p class="empty">No links.</p>';
  } else {
    html += "<table><thead><tr><th>Provider</th><th>Patient</th><th>Status</th><th>Check-in reminders</th><th>Actions</th></tr></thead><tbody>";
    for (const l of links) {
      const provider = l.provider_id === session.user_id ? "You" : l.provider_id.slice(0, 8);
      const patient = l.patient_id === session.user_id ? "You" : l.patient_id.slice(0, 8);
      html += `<tr>
        <td>${provider}</td><td>${patient}</td>
        <td><span class="badge badge-${l.status}">${l.status}</span></td>
        <td>${checkinCell(l)}</td>
        <td>`;
      if (l.status === "pending" && session.role === "patient" && l.patient_id === session.user_id) {
        html += `<button class="btn btn-sm" onclick="acceptLink('${l.provider_id}')">Accept</button>`;
      }
      if (l.status !== "revoked") {
        html += ` <button class="btn btn-sm btn-danger" onclick="revokeLink('${l.provider_id === session.user_id ? l.patient_id : l.provider_id}')">Revoke</button>`;
      }
      html += "</td></tr>";
    }
    html += "</tbody></table>";
  }
  html += "</div>";

  container.innerHTML = html;
  const form = document.getElementById("link-form");
  if (form) {
    form.onsubmit = async (e) => {
      e.preventDefault();
      try {
        await api("POST", "/links", { patient_email: document.getElementById("link-email").value });
        showAlert("Invite sent", "success");
        loadTab();
      } catch (err) {
        showAlert(err.message);
      }
    };
  }
}

// Check-in reminder cadence (#19): the provider picks how often the patient
// is reminded to check in; the Back Office sends them as reminder messages.
const CHECKIN_CHOICES = [["", "Off"], [1, "Daily"], [3, "Every 3 days"], [7, "Weekly"], [14, "Every 2 weeks"], [30, "Monthly"]];

function checkinCell(l) {
  const every = l.checkin_every_days ?? "";
  if (l.status !== "active" || l.provider_id !== session.user_id) {
    return every === "" ? "Off" : `Every ${every} days`;
  }
  const choices = CHECKIN_CHOICES.some(([v]) => v === every) ? CHECKIN_CHOICES : [...CHECKIN_CHOICES, [every, `Every ${every} days`]];
  const opts = choices.map(([v, label]) => `<option value="${v}" ${v === every ? "selected" : ""}>${label}</option>`).join("");
  return `<select onchange="setCheckin('${l.patient_id}', this.value)">${opts}</select>`;
}

async function setCheckin(patientId, value) {
  try {
    await api("PUT", `/links/${patientId}/checkin`, { every_days: value === "" ? null : Number(value) });
    showAlert(value === "" ? "Check-in reminders off" : "Check-in reminders set", "success");
    loadTab();
  } catch (err) {
    showAlert(err.message);
  }
}

async function acceptLink(providerId) {
  try {
    await api("POST", `/links/${providerId}/accept`);
    showAlert("Link accepted", "success");
    loadTab();
  } catch (err) {
    showAlert(err.message);
  }
}

async function revokeLink(otherId) {
  try {
    await api("POST", `/links/${otherId}/revoke`);
    showAlert("Link revoked", "success");
    loadTab();
  } catch (err) {
    showAlert(err.message);
  }
}

// ---------------------------------------------------------------------------
// Patients tab and QR pairing (#37)
// ---------------------------------------------------------------------------
// Patients have no password. Staff add them here and press Pair phone: the
// QR code holds the office's pairing identity and a single-use invite that
// expires in 10 minutes. Works on the office network only (the server refuses
// it through the Relay).
let pairingTimer = null;

async function loadPatients(container) {
  stopPairing();
  const data = await api("GET", "/patients");
  let html = `<div class="card"><h2>Add patient</h2>
    <form id="patient-form" class="inline-form">
      <input id="patient-name" placeholder="Full name" required>
      <input id="patient-email" type="email" placeholder="email@example.com" required>
      <button type="submit" class="btn">Add</button>
    </form>
    <p class="hint">Patients don't get a password. Pair their phone here, in the office.</p></div>`;
  html += '<div id="pairing"></div><div class="card"><h2>Patients</h2>';
  if (data.patients.length === 0) {
    html += '<p class="empty">No patients yet.</p>';
  } else {
    html += "<table><thead><tr><th>Name</th><th>Email</th><th>Devices</th><th></th></tr></thead><tbody>";
    for (const p of data.patients) {
      html += `<tr><td>${esc(p.display_name || "—")}</td><td>${esc(p.email)}</td>
        <td><button class="btn btn-sm btn-outline" onclick="showDevices('${esc(p.id)}', '${esc(p.display_name || p.email)}')">${p.devices} active</button></td>
        <td><button class="btn btn-sm" onclick="startPairing('${esc(p.id)}')">Pair phone</button></td></tr>`;
    }
    html += "</tbody></table>";
  }
  html += "</div>";
  container.innerHTML = html;
  document.getElementById("patient-form").onsubmit = async (e) => {
    e.preventDefault();
    try {
      await api("POST", "/patients", {
        display_name: document.getElementById("patient-name").value,
        email: document.getElementById("patient-email").value,
      });
      showAlert("Patient added", "success");
      loadTab();
    } catch (err) {
      showAlert(err.message);
    }
  };
}

function stopPairing() {
  if (pairingTimer) clearInterval(pairingTimer);
  pairingTimer = null;
  const el = document.getElementById("pairing");
  if (el) el.innerHTML = "";
}

async function startPairing(patientId) {
  stopPairing();
  let inv;
  try {
    inv = await api("POST", "/pairing/invites", { user_id: patientId });
  } catch (err) {
    showAlert(err.message);
    return;
  }
  const el = document.getElementById("pairing");
  el.innerHTML = `<div class="card pairing">
    <h2>${inv.role && inv.role !== "patient" ? "Pair your phone or iPad" : `Pair ${esc(inv.patient_name || "patient")}'s phone`}</h2>
    <div id="pairing-body">
      <img class="qr" alt="Pairing QR code" src="data:image/png;base64,${inv.qr_png}">
      <p>Ask the patient to open the WeHelp app, tap <strong>Pair with your practice</strong>, and scan this code.</p>
      <p class="hint">Single use. Expires <span id="pairing-expiry"></span>.</p>
    </div>
    <button class="btn btn-sm btn-outline" onclick="stopPairing()">Close</button>
  </div>`;
  el.scrollIntoView({ behavior: "smooth" });
  const expires = new Date(inv.expires_at);
  const tick = async () => {
    const left = Math.max(0, Math.round((expires - Date.now()) / 1000));
    const exp = document.getElementById("pairing-expiry");
    if (exp) exp.textContent = `in ${Math.floor(left / 60)}:${String(left % 60).padStart(2, "0")}`;
    try {
      renderPairingStatus(inv.id, await api("GET", `/pairing/invites/${inv.id}`));
    } catch (err) {
      showAlert(err.message);
      stopPairing();
    }
  };
  await tick();
  pairingTimer = setInterval(tick, 2000);
}

function renderPairingStatus(inviteId, st) {
  const body = document.getElementById("pairing-body");
  if (!body || st.status === "issued") return;
  clearInterval(pairingTimer);
  pairingTimer = null;
  body.innerHTML = st.status === "paired"
    ? `<p><strong>Paired.</strong> ${esc(st.device_name || "The phone")} is signed in.</p>`
    : "<p>The code expired. Start again.</p>";
  if (st.status === "paired") setTimeout(loadTab, 1500); // refresh the phone counts
}

// A patient's paired devices (#10). Revoke a lost or replaced phone; it is
// locked out at once. Office network only.
async function showDevices(patientId, name) {
  stopPairing();
  let data;
  try {
    data = await api("GET", `/patients/${patientId}/devices`);
  } catch (err) {
    showAlert(err.message);
    return;
  }
  const el = document.getElementById("pairing");
  let html = `<div class="card"><h2>${esc(name)}'s devices</h2>`;
  if (data.devices.length === 0) {
    html += '<p class="empty">No devices yet. Use Pair phone.</p>';
  } else {
    html += "<table><thead><tr><th>Device</th><th>Paired</th><th>Last seen</th><th>Status</th><th></th></tr></thead><tbody>";
    for (const d of data.devices) {
      const when = (t) => (t ? new Date(t).toLocaleString() : "—");
      html += `<tr><td>${esc(d.name || "Unnamed device")}</td><td>${when(d.created_at)}</td>
        <td>${when(d.last_seen_at)}</td>
        <td><span class="badge badge-${d.status === "active" ? "active" : "revoked"}">${esc(d.status)}</span></td>
        <td>${d.status === "active"
          ? `<button class="btn btn-sm btn-danger" onclick="revokeDevice('${esc(patientId)}', '${esc(d.id)}', '${esc(name)}')">Revoke</button>`
          : ""}</td></tr>`;
    }
    html += "</tbody></table>";
  }
  html += `<button class="btn btn-sm btn-outline" onclick="stopPairing()">Close</button></div>`;
  el.innerHTML = html;
  el.scrollIntoView({ behavior: "smooth" });
}

async function revokeDevice(patientId, deviceId, name) {
  if (!confirm("Revoke this device? It will be signed out immediately and must be paired again in the office.")) return;
  try {
    await api("DELETE", `/patients/${patientId}/devices/${deviceId}`);
    showAlert("Device revoked", "success");
    await loadTab();
    showDevices(patientId, name);
  } catch (err) {
    showAlert(err.message);
  }
}

// Your own phones and iPads (#69). Pair one here, in the office, to use the
// WeHelp app as a provider: it opens on your schedule and messages, through
// the Relay, with Face ID. Revoke a lost one at once.
async function showMyDevices() {
  if (activeTab !== "patients") {
    activeTab = "patients";
    render();
    await new Promise((r) => setTimeout(r, 300));
  }
  stopPairing();
  let data;
  try {
    data = await api("GET", "/devices");
  } catch (err) {
    showAlert(err.message);
    return;
  }
  const el = document.getElementById("pairing");
  let html = `<div class="card"><h2>My devices</h2>
    <p class="hint">Pair a phone or iPad to use the WeHelp app as ${esc(session.role)}. It works in the office and remotely, with Face ID; no password on the device.</p>
    <button class="btn" onclick="startPairing('${esc(session.user_id)}')">Pair my phone or iPad</button>`;
  if (data.devices.length > 0) {
    html += "<table><thead><tr><th>Device</th><th>Paired</th><th>Last seen</th><th>Status</th><th></th></tr></thead><tbody>";
    const when = (t) => (t ? new Date(t).toLocaleString() : "—");
    for (const d of data.devices) {
      html += `<tr><td>${esc(d.name || "Unnamed device")}</td><td>${when(d.created_at)}</td><td>${when(d.last_seen_at)}</td>
        <td><span class="badge badge-${d.status === "active" ? "active" : "revoked"}">${esc(d.status)}</span></td>
        <td>${d.status === "active" ? `<button class="btn btn-sm btn-danger" onclick="revokeMyDevice('${esc(d.id)}')">Revoke</button>` : ""}</td></tr>`;
    }
    html += "</tbody></table>";
  }
  html += `<button class="btn btn-sm btn-outline" onclick="stopPairing()">Close</button></div>`;
  el.innerHTML = html;
  el.scrollIntoView({ behavior: "smooth" });
}

async function revokeMyDevice(deviceId) {
  if (!confirm("Revoke this device? It is signed out at once and must be paired again in the office.")) return;
  try {
    await api("DELETE", `/devices/${deviceId}`);
    showAlert("Device revoked", "success");
    showMyDevices();
  } catch (err) {
    showAlert(err.message);
  }
}

// ---------------------------------------------------------------------------
// Utils
// ---------------------------------------------------------------------------
function esc(s) {
  const d = document.createElement("div");
  d.textContent = s;
  return d.innerHTML;
}

// ---------------------------------------------------------------------------
// Init
// ---------------------------------------------------------------------------
// Resume a session from the cookie, if there is one.
api("GET", "/staff/me")
  .then((me) => { session = me; })
  .catch(() => { session = null; })
  .finally(render);

