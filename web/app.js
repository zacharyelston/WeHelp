// WeHelp web client — vanilla JS SPA, no framework, no build step.
// Embedded in the Go binary via embed.FS; served at / by the chi router.

const API = "/api/v1";
const STORAGE_KEY = "wehelp_session";

// ---------------------------------------------------------------------------
// State
// ---------------------------------------------------------------------------
let session = JSON.parse(localStorage.getItem(STORAGE_KEY) || "null");
let activeTab = "messages";

// ---------------------------------------------------------------------------
// API helpers
// ---------------------------------------------------------------------------
async function api(method, path, body) {
  const opts = {
    method,
    headers: { "content-type": "application/json" },
  };
  if (session?.access_token) {
    opts.headers["authorization"] = "Bearer " + session.access_token;
  }
  if (body) opts.body = JSON.stringify(body);
  const res = await fetch(API + path, opts);
  const data = await res.json().catch(() => ({}));
  if (res.status === 401) {
    logout();
    throw new Error("session expired");
  }
  if (!res.ok) throw new Error(data.error || res.statusText);
  return data;
}

// ---------------------------------------------------------------------------
// Auth
// ---------------------------------------------------------------------------
async function login(tenant, email, password) {
  const data = await api("POST", "/auth/login", { tenant, email, password });
  session = data;
  localStorage.setItem(STORAGE_KEY, JSON.stringify(session));
  await fetchIdentity();
  render();
}

async function fetchIdentity() {
  const me = await api("GET", "/me");
  session = { ...session, ...me };
  localStorage.setItem(STORAGE_KEY, JSON.stringify(session));
}

function logout() {
  session = null;
  localStorage.removeItem(STORAGE_KEY);
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
  app.innerHTML = renderDashboard();
  bindDashboard();
  loadTab();
}

function renderLogin() {
  return `
    <div class="card login-card">
      <h1>WeHelp</h1>
      <div id="login-error"></div>
      <form id="login-form">
        <div class="form-group">
          <label>Tenant</label>
          <input type="text" id="login-tenant" value="Demo Clinic" required>
        </div>
        <div class="form-group">
          <label>Email</label>
          <input type="email" id="login-email" placeholder="you@demo.wehelp" required>
        </div>
        <div class="form-group">
          <label>Password</label>
          <input type="password" id="login-password" required>
        </div>
        <button type="submit" class="btn" style="width:100%">Log in</button>
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
    const tenant = document.getElementById("login-tenant").value;
    const email = document.getElementById("login-email").value;
    const password = document.getElementById("login-password").value;
    try {
      await login(tenant, email, password);
    } catch (err) {
      document.getElementById("login-error").innerHTML =
        `<div class="alert alert-error">${err.message}</div>`;
    }
  };
}

function renderDashboard() {
  const role = session.role || "";
  const userId = session.user_id || "";
  return `
    <div class="header">
      <h1>WeHelp</h1>
      <div>
        <span class="user">${role} · ${userId.slice(0, 8)}</span>
        <button class="btn btn-sm btn-outline" onclick="logout()" style="margin-left:0.5rem">Log out</button>
      </div>
    </div>
    <div id="alert-area"></div>
    <div class="tabs">
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
  area.innerHTML = `<div class="alert alert-${type}">${msg}</div>`;
  setTimeout(() => { if (area) area.innerHTML = ""; }, 4000);
}

async function loadTab() {
  const content = document.getElementById("tab-content");
  content.innerHTML = '<div class="loading">Loading…</div>';
  try {
    if (activeTab === "messages") await loadMessages(content);
    else if (activeTab === "appointments") await loadAppointments(content);
    else if (activeTab === "links") await loadLinks(content);
  } catch (err) {
    content.innerHTML = `<div class="alert alert-error">${err.message}</div>`;
  }
}

// ---------------------------------------------------------------------------
// Messages tab
// ---------------------------------------------------------------------------
async function loadMessages(container) {
  const data = await api("GET", "/messages?limit=50");
  const msgs = data.messages || [];
  const links = await api("GET", "/links");
  const contacts = links.filter((l) => l.status === "active").map((l) => {
    const other = l.provider_id === session.user_id ? l.patient_id : l.provider_id;
    return { id: other, label: other.slice(0, 8) };
  });

  let html = '<div class="card"><h2>Send Message</h2>';
  if (contacts.length === 0) {
    html += '<p class="empty">No active contacts to message.</p>';
  } else {
    html += `<form id="msg-form">
      <div class="form-row">
        <div class="form-group">
          <label>To</label>
          <select id="msg-recipient">${contacts.map((c) => `<option value="${c.id}">${c.label}</option>`).join("")}</select>
        </div>
        <div class="form-group">
          <label>Kind</label>
          <select id="msg-kind">
            <option value="message">Message</option>
            <option value="memo">Memo</option>
            <option value="checkin">Check-in</option>
            <option value="reminder">Reminder</option>
          </select>
        </div>
      </div>
      <div class="form-group">
        <label>Body</label>
        <textarea id="msg-body" required></textarea>
      </div>
      <button type="submit" class="btn">Send</button>
    </form>`;
  }
  html += "</div>";

  html += '<div class="card"><h2>Inbox</h2>';
  if (msgs.length === 0) {
    html += '<p class="empty">No messages.</p>';
  } else {
    html += '<div class="message-list">';
    for (const m of msgs) {
      const unread = !m.read_at;
      const time = new Date(m.created_at).toLocaleString();
      const from = m.sender_id === session.user_id ? "You" : m.sender_id.slice(0, 8);
      html += `<div class="message-item ${unread ? "unread" : ""}">
        <div class="meta"><span class="kind">${m.kind}</span> · ${from} · ${time}</div>
        <div class="body">${esc(m.body)}</div>
        ${unread && m.recipient_id === session.user_id ? `<button class="btn btn-sm" onclick="markRead('${m.id}')">Mark read</button>` : ""}
      </div>`;
    }
    html += "</div>";
  }
  html += "</div>";

  container.innerHTML = html;
  const form = document.getElementById("msg-form");
  if (form) {
    form.onsubmit = async (e) => {
      e.preventDefault();
      try {
        await api("POST", "/messages", {
          recipient_id: document.getElementById("msg-recipient").value,
          kind: document.getElementById("msg-kind").value,
          body: document.getElementById("msg-body").value,
        });
        showAlert("Message sent", "success");
        loadTab();
      } catch (err) {
        showAlert(err.message);
      }
    };
  }
}

async function markRead(id) {
  try {
    await api("POST", `/messages/${id}/read`);
    loadTab();
  } catch (err) {
    showAlert(err.message);
  }
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
    html += "<table><thead><tr><th>Provider</th><th>Patient</th><th>Status</th><th>Actions</th></tr></thead><tbody>";
    for (const l of links) {
      const provider = l.provider_id === session.user_id ? "You" : l.provider_id.slice(0, 8);
      const patient = l.patient_id === session.user_id ? "You" : l.patient_id.slice(0, 8);
      html += `<tr>
        <td>${provider}</td><td>${patient}</td>
        <td><span class="badge badge-${l.status}">${l.status}</span></td>
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
render();
