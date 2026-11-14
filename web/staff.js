// Provider accounts (#100). Loaded before app.js; uses its globals (api,
// session, render, esc, showAlert) at call time.
//
// An admin adds a provider and gets a single-use set-up link that works for
// 24 hours on the office network. The provider opens it, chooses their own
// password and enrolls TOTP. Admins never see or set the password. The
// link's token is in the URL fragment, so it never reaches a server log.

// startStaffSetup shows the set-up page if the page was opened from a
// set-up link, and returns true if it did.
function startStaffSetup() {
  const m = location.hash.match(/^#setup=([A-Za-z0-9_-]+)$/);
  if (!m) return false;
  const token = m[1];
  history.replaceState(null, "", location.pathname); // drop the token from the address bar and history
  const app = document.getElementById("app");
  app.innerHTML = `
    <div class="card login-card">
      <h1>Set up your WeHelp account</h1>
      <p class="hint">Choose a password (12 characters or more). Next you'll set up two-step sign-in with an authenticator app.</p>
      <div id="staff-setup-error"></div>
      <form id="staff-setup-form">
        <div class="form-group">
          <label>Password</label>
          <input type="password" id="staff-setup-password" autocomplete="new-password" minlength="12" required>
        </div>
        <div class="form-group">
          <label>Password again</label>
          <input type="password" id="staff-setup-password2" autocomplete="new-password" minlength="12" required>
        </div>
        <button type="submit" class="btn" style="width:100%">Continue</button>
      </form>
    </div>`;
  document.getElementById("staff-setup-form").onsubmit = async (e) => {
    e.preventDefault();
    const pw = document.getElementById("staff-setup-password").value;
    const errEl = document.getElementById("staff-setup-error");
    if (pw !== document.getElementById("staff-setup-password2").value) {
      errEl.innerHTML = '<div class="alert alert-error">The passwords don\'t match.</div>';
      return;
    }
    try {
      session = await api("POST", "/staff/setup", { token, password: pw });
      render(); // status totp_setup: the authenticator step
    } catch (err) {
      errEl.innerHTML = `<div class="alert alert-error">${esc(err.message)}</div>`;
    }
  };
  return true;
}

const PROVIDER_STATUS = {
  invited: ["pending", "Invited"],
  invite_expired: ["revoked", "Link expired"],
  totp_pending: ["pending", "Setting up"],
  active: ["active", "Active"],
  disabled: ["revoked", "Disabled"],
};

// showProviders is the admin's Providers screen, in place of a tab.
async function showProviders(invite) {
  document.querySelectorAll(".tab").forEach((x) => x.classList.remove("active"));
  const content = document.getElementById("tab-content");
  let data;
  try {
    data = await api("GET", "/staff/providers");
  } catch (err) {
    content.innerHTML = `<div class="alert alert-error">${esc(err.message)}</div>`;
    return;
  }
  let html = `<div class="card"><h2>Add provider</h2>
    <form id="provider-form" class="inline-form">
      <input id="provider-name" placeholder="Name, for example Dr. Sam Lee" required>
      <input id="provider-email" type="email" placeholder="email (their sign-in name)" required>
      <button type="submit" class="btn">Add and get set-up link</button>
    </form>
    <p class="hint">The provider chooses their own password and sets up an authenticator app from the link. You never see their password.</p></div>`;
  if (invite) {
    html += `<div class="card"><h2>Set-up link for ${esc(invite.provider.display_name)}</h2>
      <p>Give this link to them in person or open it on their computer in the office. It works once, on the office network, until ${esc(new Date(invite.expires_at).toLocaleString())}. It is shown only now.</p>
      <div class="inline-form"><input id="setup-link" readonly value="${esc(invite.setup_url)}" style="flex:1;min-width:20rem">
      <button class="btn" onclick="copySetupLink()">Copy</button></div></div>`;
  }
  html += '<div class="card"><h2>Providers</h2>';
  if (data.providers.length === 0) {
    html += '<p class="empty">No providers yet.</p>';
  } else {
    html += "<table><thead><tr><th>Name</th><th>Email</th><th>Status</th><th>Actions</th></tr></thead><tbody>";
    for (const p of data.providers) {
      const [badge, label] = PROVIDER_STATUS[p.status] || ["pending", p.status];
      let note = "";
      if (p.status === "invited" && p.invite_expires_at) note = `<div class="hint">Link works until ${esc(new Date(p.invite_expires_at).toLocaleString())}</div>`;
      const actions = [];
      if (p.status === "invited" || p.status === "invite_expired") {
        actions.push(`<button class="btn btn-sm" onclick="providerAction('${esc(p.id)}', 'invite')">New set-up link</button>`);
      }
      if (p.status === "disabled") {
        actions.push(`<button class="btn btn-sm" onclick="providerAction('${esc(p.id)}', 'enable')">Enable</button>`);
      } else {
        actions.push(`<button class="btn btn-sm btn-danger" onclick="providerAction('${esc(p.id)}', 'disable')">Disable</button>`);
      }
      html += `<tr><td>${esc(p.display_name)}</td><td>${esc(p.email)}</td>
        <td><span class="badge badge-${badge}">${esc(label)}</span>${note}</td><td>${actions.join(" ")}</td></tr>`;
    }
    html += "</tbody></table>";
  }
  content.innerHTML = html + "</div>";
  document.getElementById("provider-form").onsubmit = async (e) => {
    e.preventDefault();
    try {
      const inv = await api("POST", "/staff/providers", {
        display_name: document.getElementById("provider-name").value,
        email: document.getElementById("provider-email").value,
      });
      showProviders(inv);
    } catch (err) {
      showAlert(err.message);
    }
  };
}

async function providerAction(id, action) {
  if (action === "disable" && !confirm("Disable this provider? They are signed out at once and can't sign in until enabled again. Nothing is deleted.")) return;
  try {
    const res = await api("POST", `/staff/providers/${id}/${action}`);
    if (action === "invite") showProviders(res);
    else {
      showAlert(action === "disable" ? "Provider disabled" : "Provider enabled", "success");
      showProviders();
    }
  } catch (err) {
    showAlert(err.message);
  }
}

async function copySetupLink() {
  const el = document.getElementById("setup-link");
  try {
    await navigator.clipboard.writeText(el.value);
    showAlert("Link copied", "success");
  } catch (_) {
    el.select();
  }
}
