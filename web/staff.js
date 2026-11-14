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

// A set-up link pasted into a tab already on this page only changes the
// fragment, with no reload.
window.addEventListener("hashchange", startStaffSetup);

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
  let html = `<div class="card"><h2>Add staff</h2>
    <form id="provider-form" class="inline-form">
      <input id="provider-name" placeholder="Name, for example Dr. Sam Lee" required>
      <input id="provider-email" type="email" placeholder="email (their sign-in name)" required>
      <select id="provider-role"><option value="provider">${Word("provider")}</option><option value="office">Front desk (whole schedule, no setup)</option></select>
      <button type="submit" class="btn">Add and get set-up link</button>
    </form>
    <p class="hint">They choose their own password and set up an authenticator app from the link. You never see their password.</p></div>`;
  if (invite) {
    html += `<div class="card"><h2>Set-up link for ${esc(invite.provider.display_name)}</h2>
      <p>Give this link to them in person or open it on their computer in the office. It works once, on the office network, until ${esc(new Date(invite.expires_at).toLocaleString())}. It is shown only now.</p>
      <div class="inline-form"><input id="setup-link" readonly value="${esc(invite.setup_url)}" style="flex:1;min-width:20rem">
      <button class="btn" onclick="copySetupLink()">Copy</button></div></div>`;
  }
  html += '<div class="card"><h2>Staff</h2>';
  if (data.providers.length === 0) {
    html += '<p class="empty">No providers yet.</p>';
  } else {
    html += "<table><thead><tr><th>Name</th><th>Role</th><th>Email</th><th>Status</th><th>Actions</th></tr></thead><tbody>";
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
      html += `<tr><td>${esc(p.display_name)}</td><td>${p.role === "office" ? "Front desk" : Word("provider")}</td><td>${esc(p.email)}</td>
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
        role: document.getElementById("provider-role").value,
      });
      showProviders(inv);
    } catch (err) {
      showAlert(err.message);
    }
  };
}

async function providerAction(id, action) {
  if (action === "disable" && !confirm("Disable this account? They are signed out at once and can't sign in until enabled again. Nothing is deleted.")) return;
  try {
    const res = await api("POST", `/staff/providers/${id}/${action}`);
    if (action === "invite") showProviders(res);
    else {
      showAlert(action === "disable" ? "Account disabled" : "Account enabled", "success");
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

// ---------------------------------------------------------------------------
// My profile (#102): your name as shown, credentials, bio and photo. With
// "Show on the practice website" on, the next website publish uses them for
// your bio. Only you can edit your profile.
// ---------------------------------------------------------------------------
async function showMyProfile() {
  document.querySelectorAll(".tab").forEach((x) => x.classList.remove("active"));
  const content = document.getElementById("tab-content");
  let p;
  try {
    p = await api("GET", "/staff/profile");
  } catch (err) {
    content.innerHTML = `<div class="alert alert-error">${esc(err.message)}</div>`;
    return;
  }
  const photo = p.has_photo
    ? `<img src="${API}/staff/profile/photo?t=${Date.now()}" alt="Your photo" style="width:160px;height:160px;object-fit:cover;border-radius:8px;display:block;margin-bottom:0.5rem">`
    : '<p class="empty">No photo yet.</p>';
  content.innerHTML = `
    <div class="card"><h2>My profile</h2>
      <p class="hint">This is how patients see you on the practice website. Your changes appear there the next time the site is published from the Website tab.</p>
      <form id="profile-form">
        <div class="form-group"><label>Name as shown</label><input id="pf-name" maxlength="120" required value="${attr(p.display_name)}"></div>
        <div class="form-group"><label>Credentials</label><input id="pf-cred" maxlength="120" placeholder="for example MD, Family Medicine" value="${attr(p.credentials)}"></div>
        <div class="form-group"><label>Bio</label><textarea id="pf-bio" rows="8" maxlength="3000">${esc(p.bio)}</textarea>
          <div class="hint">Separate paragraphs with a blank line. At most 3000 characters.</div></div>
        <div class="form-group"><label><input type="checkbox" id="pf-show" ${p.show_on_website ? "checked" : ""} style="width:auto"> Show me on the practice website</label></div>
        <button type="submit" class="btn">Save profile</button>
      </form>
    </div>
    <div class="card"><h2>Photo</h2>
      ${photo}
      <form id="photo-form" class="inline-form">
        <input id="pf-photo" type="file" accept="image/png,image/jpeg,image/webp" required>
        <button type="submit" class="btn">Upload photo</button>
        ${p.has_photo ? '<button type="button" class="btn btn-outline" onclick="removeProfilePhoto()">Remove photo</button>' : ""}
      </form>
      <p class="hint">A square head-and-shoulders photo works best. PNG, JPEG or WebP, at most 2 MB.</p>
    </div>
    <div id="message-settings"></div>`;
  if (session.role === "provider" && feature("messages")) renderMessageSettings();
  document.getElementById("profile-form").onsubmit = async (e) => {
    e.preventDefault();
    try {
      const saved = await api("PUT", "/staff/profile", {
        display_name: document.getElementById("pf-name").value,
        credentials: document.getElementById("pf-cred").value,
        bio: document.getElementById("pf-bio").value,
        show_on_website: document.getElementById("pf-show").checked,
      });
      session.display_name = saved.display_name;
      showAlert("Profile saved", "success");
    } catch (err) {
      showAlert(err.message);
    }
  };
  document.getElementById("photo-form").onsubmit = async (e) => {
    e.preventDefault();
    const file = document.getElementById("pf-photo").files[0];
    if (!file) return;
    if (file.size > 2 * 1024 * 1024) {
      showAlert("The photo must be at most 2 MB.");
      return;
    }
    try {
      const res = await fetch(API + "/staff/profile/photo", {
        method: "PUT",
        credentials: "same-origin",
        headers: { "content-type": file.type || "application/octet-stream", "x-csrf-token": session.csrf_token },
        body: file,
      });
      const data = await res.json().catch(() => ({}));
      if (!res.ok) throw new Error(data.error || res.statusText);
      showAlert("Photo uploaded", "success");
      showMyProfile();
    } catch (err) {
      showAlert(err.message);
    }
  };
}

async function removeProfilePhoto() {
  if (!confirm("Remove your photo?")) return;
  try {
    await api("DELETE", "/staff/profile/photo");
    showMyProfile();
  } catch (err) {
    showAlert(err.message);
  }
}

// renderMessageSettings is My profile's Messages card (#109): how this
// provider takes messages from patients.
async function renderMessageSettings() {
  const el = document.getElementById("message-settings");
  let m;
  try {
    m = await api("GET", "/staff/profile/messages");
  } catch (err) {
    el.innerHTML = `<div class="alert alert-error">${esc(err.message)}</div>`;
    return;
  }
  const hm = (n) => `${String(Math.floor(n / 60)).padStart(2, "0")}:${String(n % 60).padStart(2, "0")}`;
  const days = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"];
  const mode = (v, label, hint) => `<label style="display:flex;gap:0.5rem;align-items:flex-start;margin:0.4rem 0">
    <input type="radio" name="msg-mode" value="${v}" ${m.mode === v ? "checked" : ""} style="width:auto;margin-top:0.3rem">
    <span><b>${label}</b><br><span class="hint">${hint}</span></span></label>`;
  el.innerHTML = `<div class="card"><h2>Messages</h2>
    <p class="hint">How you take messages from ${esc(word("patients"))} in the app. You can always write to them.</p>
    <form id="msg-form">
      ${mode("on", "On", "Messages arrive any time, with a notification.")}
      ${mode("hours", "Office hours", `Messages arrive any time. Outside your hours the ${esc(word("patient"))} gets an automatic reply and you get no notification.`)}
      ${mode("off", "Off", `${esc(Word("patients"))} can't message you; they're pointed to the practice inbox, which the front desk answers.`)}
      <div class="form-group" style="margin-top:0.75rem"><label>Office hours</label>
        <div>${days.map((d, i) => `<label style="display:inline-flex;gap:0.25rem;margin-right:0.6rem"><input type="checkbox" data-day="${i}" ${m.days.includes(i) ? "checked" : ""} style="width:auto">${d}</label>`).join("")}</div>
        <div class="inline-form" style="margin-top:0.5rem"><input type="time" id="msg-start" value="${hm(m.start_minute)}"> to <input type="time" id="msg-end" value="${hm(m.end_minute)}"></div>
      </div>
      <div class="form-group"><label>Automatic reply (optional)</label>
        <textarea id="msg-reply" rows="3" maxlength="500" placeholder="Left empty, it says when you reply.">${esc(m.auto_reply)}</textarea></div>
      <button type="submit" class="btn">Save message settings</button>
    </form></div>`;
  document.getElementById("msg-form").onsubmit = async (e) => {
    e.preventDefault();
    const mins = (v) => { const [h, mm] = v.split(":").map(Number); return h * 60 + mm; };
    try {
      await api("PUT", "/staff/profile/messages", {
        mode: document.querySelector('input[name="msg-mode"]:checked').value,
        days: [...document.querySelectorAll("[data-day]")].filter((x) => x.checked).map((x) => Number(x.dataset.day)),
        start_minute: mins(document.getElementById("msg-start").value),
        end_minute: mins(document.getElementById("msg-end").value),
        auto_reply: document.getElementById("msg-reply").value,
      });
      showAlert("Message settings saved", "success");
    } catch (err) {
      showAlert(err.message);
    }
  };
}

// profileBiosCard lists, on the Website tab, the bios that come from staff
// profiles. Each person edits their own under My profile.
function profileBiosCard(profiles) {
  let html = `<div class="card"><h2>Provider bios from profiles</h2>
    <p class="hint">Staff who turn on "Show me on the practice website" under <b>My profile</b> appear here with their own bio and photo. A bio below with the same name is replaced by the profile.</p>`;
  if (!profiles || profiles.length === 0) {
    html += '<p class="empty">No one has turned this on yet.</p>';
  } else {
    html += "<ul>" + profiles.map((p) => `<li>${esc(p.name)}${p.credentials ? ", " + esc(p.credentials) : ""}${p.has_photo ? "" : ' <span class="hint">(no photo)</span>'}</li>`).join("") + "</ul>";
  }
  return html + "</div>";
}

// themePicker is the Website tab's colour scheme choice (#104). The hidden
// input carries the choice into Save like any other field.
function themePicker(data) {
  const themes = data.themes || [];
  const current = data.content.theme || (themes[0] && themes[0].id) || "";
  const swatches = themes.map((t) => `
    <button type="button" class="theme-swatch${t.id === current ? " selected" : ""}" data-theme-id="${attr(t.id)}" onclick="pickTheme(this)"
      title="${attr(t.label)}" style="display:inline-flex;align-items:center;gap:0.5rem;padding:0.4rem 0.75rem;margin:0 0.4rem 0.4rem 0;border-radius:999px;cursor:pointer;
      border:2px solid ${t.id === current ? "var(--primary)" : "#ddd"};background:#fff">
      <span style="width:1.2rem;height:1.2rem;border-radius:50%;background:${attr(t.swatch)};display:inline-block"></span>${esc(t.label)}
    </button>`).join("");
  return `<div class="card"><h2>Look</h2>
    <p class="hint">The website's colours: header, banner, buttons and accents. Visitors in dark mode get a dark version. Save and publish to apply.</p>
    <input type="hidden" data-field="theme" id="site-theme" value="${attr(current)}">
    <div>${swatches}</div></div>`;
}

function pickTheme(btn) {
  document.getElementById("site-theme").value = btn.dataset.themeId;
  document.querySelectorAll(".theme-swatch").forEach((b) => {
    b.classList.toggle("selected", b === btn);
    b.style.borderColor = b === btn ? "var(--primary)" : "#ddd";
  });
}

// ---------------------------------------------------------------------------
// Settings (#108): the practice type, admins only. It sets the words the
// staff web, website and app use, and whether the practice is clinical.
// The feature switches of #109 will join this screen.
// ---------------------------------------------------------------------------
async function showSettings() {
  document.querySelectorAll(".tab").forEach((x) => x.classList.remove("active"));
  const content = document.getElementById("tab-content");
  let p;
  try {
    p = await api("GET", "/practice");
  } catch (err) {
    content.innerHTML = `<div class="alert alert-error">${esc(err.message)}</div>`;
    return;
  }
  const rows = p.types.map((t) => `
    <label class="type-option" style="display:flex;gap:0.75rem;align-items:flex-start;padding:0.75rem;border:1px solid #ddd;border-radius:8px;margin-bottom:0.5rem;cursor:pointer">
      <input type="radio" name="practice-type" value="${attr(t.id)}" ${t.id === p.type.id ? "checked" : ""} style="width:auto;margin-top:0.3rem">
      <span><b>${esc(t.label)}</b><br>
        <span class="hint">Says “${esc(t.words.patient)}” and “${esc(t.words.provider)}”.
        ${t.clinical ? "Clinical: check-ins and health monitors available." : "Not clinical: check-ins and health monitors hidden."}</span></span>
    </label>`).join("");
  content.innerHTML = `
    <div class="card"><h2>Practice type</h2>
      <p class="hint">Sets the words the staff web, the website and the app use, and which features fit the practice. Security is the same for every type. The website changes at its next publish.</p>
      <form id="practice-form">${rows}
        <button type="submit" class="btn">Save</button>
      </form>
    </div>
    <div class="card"><h2>Features</h2>
      <p class="hint">Turn off what this practice doesn't use. It disappears from the staff web and the app, and the server refuses it, so older app versions can't use it either. Nothing is deleted. Check-in reminders need a clinical practice type.</p>
      <form id="features-form">
        ${(p.switches || []).map((f) => `<label style="display:flex;gap:0.5rem;align-items:center;margin:0.4rem 0">
          <input type="checkbox" data-feature="${attr(f.id)}" ${p.features[f.id] ? "checked" : ""} style="width:auto"
            ${f.id === "checkins" && !p.type.clinical ? "disabled" : ""}> ${esc(f.label)}</label>`).join("")}
        <button type="submit" class="btn" style="margin-top:0.5rem">Save features</button>
      </form>
    </div>`;
  document.getElementById("features-form").onsubmit = async (e) => {
    e.preventDefault();
    const features = {};
    document.querySelectorAll("[data-feature]").forEach((el) => {
      if (!el.disabled) features[el.dataset.feature] = el.checked;
    });
    try {
      practiceInfo = await api("PUT", "/practice/features", { features });
      render();
      await new Promise((r) => setTimeout(r, 300));
      showSettings();
      showAlert("Features saved", "success");
    } catch (err) {
      showAlert(err.message);
    }
  };
  document.getElementById("practice-form").onsubmit = async (e) => {
    e.preventDefault();
    const chosen = document.querySelector('input[name="practice-type"]:checked');
    if (!chosen) return;
    try {
      practiceInfo = await api("PUT", "/practice", { type: chosen.value });
      render();
      showAlert(`Saved: ${practiceInfo.type.label}`, "success");
    } catch (err) {
      showAlert(err.message);
    }
  };
}

// ---------------------------------------------------------------------------
// Practice inbox (#110): patients message "the practice"; the front desk and
// admins answer here. Providers do not see it. Separate from provider threads.
// ---------------------------------------------------------------------------
let inboxOpen = null; // { id, name }

async function loadInbox(container) {
  const data = await api("GET", "/practice-inbox");
  const convs = data.conversations || [];
  let html = `<div class="threads"><div class="card conv-list"><h2>Practice inbox</h2>
    <p class="hint">Messages ${esc(word("patients"))} send to the practice. Answers come from the practice, signed with your name.</p>`;
  if (convs.length === 0) html += `<p class="empty">No messages to the practice yet.</p>`;
  for (const c of convs) {
    const preview = (c.last_from_patient ? "" : "Practice: ") + c.last_body;
    html += `<div class="conv ${inboxOpen && inboxOpen.id === c.patient_id ? "active" : ""}" data-inbox="${attr(c.patient_id)}" data-name="${attr(c.patient_name)}">
      <div class="conv-top"><strong>${esc(c.patient_name || "Unnamed")}</strong>
        ${c.unread > 0 ? `<span class="badge badge-pending">${c.unread}</span>` : ""}</div>
      <div class="conv-preview">${esc(preview)}</div><div class="hint">${esc(new Date(c.last_at).toLocaleString())}</div></div>`;
  }
  html += `</div><div class="card thread" id="inbox-thread"><p class="empty">Choose a conversation.</p></div></div>`;
  container.innerHTML = html;
  container.querySelectorAll("[data-inbox]").forEach((el) => {
    el.onclick = () => { inboxOpen = { id: el.dataset.inbox, name: el.dataset.name }; loadInbox(container); };
  });
  if (inboxOpen) showInboxThread();
}

async function showInboxThread() {
  const el = document.getElementById("inbox-thread");
  if (!el || !inboxOpen) return;
  let data;
  try {
    data = await api("GET", `/practice-inbox/${inboxOpen.id}`);
    await api("POST", "/practice-inbox/read", { patient_id: inboxOpen.id });
  } catch (err) {
    el.innerHTML = `<div class="alert alert-error">${esc(err.message)}</div>`;
    return;
  }
  let html = `<h2>${esc(inboxOpen.name)}</h2><div class="bubbles" id="inbox-bubbles">`;
  for (const m of data.messages) {
    html += `<div class="bubble ${m.from_patient ? "theirs" : "mine"}">
      ${m.from_patient ? "" : `<div class="kind">${esc(m.author_name)} for the practice</div>`}
      <div>${esc(m.body)}</div><div class="time">${esc(new Date(m.created_at).toLocaleString())}</div></div>`;
  }
  html += `</div><form class="reply" id="inbox-reply"><textarea id="inbox-body" rows="2" maxlength="4000" placeholder="Reply as the practice" required></textarea>
    <button type="submit" class="btn">Send</button></form>`;
  el.innerHTML = html;
  const b = document.getElementById("inbox-bubbles");
  b.scrollTop = b.scrollHeight;
  document.getElementById("inbox-reply").onsubmit = async (e) => {
    e.preventDefault();
    try {
      await api("POST", "/practice-inbox", { patient_id: inboxOpen.id, body: document.getElementById("inbox-body").value });
      loadInbox(document.getElementById("tab-content"));
    } catch (err) {
      showAlert(err.message);
    }
  };
}

// ---------------------------------------------------------------------------
// Care teams (#111): a patient-approved group of providers sharing one thread
// with the patient. Providers propose; the patient approves in the app. Every
// read of the thread is recorded, and the patient can see who read it.
// ---------------------------------------------------------------------------
let teamOpen = null; // patient id

const TEAM_STATUS = { active: ["active", "On the team"], pending: ["pending", "Waiting for approval"], none: ["scheduled", "Not on it yet"] };

async function loadCareTeams(container) {
  const data = await api("GET", "/care-teams");
  let html = `<div class="threads"><div class="card conv-list"><h2>Care teams</h2>
    <p class="hint">A ${esc(word("patient"))} chooses which ${esc(word("providers"))} share one thread with them. Each person you propose joins once the ${esc(word("patient"))} approves in the app.</p>`;
  if (data.teams.length === 0) html += `<p class="empty">No linked ${esc(word("patients"))} yet.</p>`;
  for (const t of data.teams) {
    const [badge, label] = TEAM_STATUS[t.my_status] || ["pending", t.my_status];
    html += `<div class="conv ${teamOpen === t.patient_id ? "active" : ""}" data-team="${attr(t.patient_id)}">
      <div class="conv-top"><strong>${esc(t.patient_name)}</strong><span class="badge badge-${badge}">${esc(label)}</span></div>
      <div class="hint">${t.active_members} on the team${t.last_message_at ? " · last message " + esc(new Date(t.last_message_at).toLocaleString()) : ""}</div></div>`;
  }
  html += `</div><div class="card thread" id="team-pane"><p class="empty">Choose a ${esc(word("patient"))}.</p></div></div>`;
  container.innerHTML = html;
  container.querySelectorAll("[data-team]").forEach((el) => {
    el.onclick = () => { teamOpen = el.dataset.team; loadCareTeams(container); };
  });
  if (teamOpen) showTeam(data.teams.find((t) => t.patient_id === teamOpen));
}

async function showTeam(summary) {
  const el = document.getElementById("team-pane");
  if (!el || !summary) return;
  let team, msgs = null;
  try {
    team = await api("GET", `/care-teams/${summary.patient_id}`);
    if (summary.my_status === "active") msgs = (await api("GET", `/care-teams/${summary.patient_id}/messages`)).messages;
  } catch (err) {
    el.innerHTML = `<div class="alert alert-error">${esc(err.message)}</div>`;
    return;
  }
  const live = team.members.filter((m) => m.status !== "removed");
  let html = `<h2>${esc(team.patient_name)}</h2><table><thead><tr><th>Member</th><th>Status</th><th>Added by</th></tr></thead><tbody>`;
  for (const m of live) {
    html += `<tr><td>${esc(m.name)}</td><td><span class="badge badge-${m.status === "active" ? "active" : "pending"}">${m.status === "active" ? "On the team" : "Waiting for approval"}</span></td><td>${esc(m.invited_by)}</td></tr>`;
  }
  if (live.length === 0) html += `<tr><td colspan="3" class="empty">No one yet.</td></tr>`;
  html += "</tbody></table>";
  if (summary.my_status === "none") {
    html += `<p style="margin-top:0.75rem"><button class="btn" onclick="proposeTeam(\x27${attr(summary.patient_id)}\x27, \x27${attr(session.user_id)}\x27)">Ask to join this care team</button></p>`;
  } else if (summary.my_status === "pending") {
    html += `<p class="hint" style="margin-top:0.75rem">Waiting for ${esc(team.patient_name)} to approve you in the WeHelp app.</p>`;
  }
  if (summary.my_status === "active" && (team.eligible || []).length) {
    html += `<form class="inline-form" id="team-propose" style="margin-top:0.75rem"><select id="team-candidate">${team.eligible.map((c) => `<option value="${attr(c.member_id)}">${esc(c.name)}</option>`).join("")}</select>
      <button type="submit" class="btn btn-outline">Propose a colleague</button></form>
      <p class="hint">They join once ${esc(team.patient_name)} approves in the app.</p>`;
  }
  if (msgs) {
    html += `<h3 style="margin-top:1rem">Shared thread</h3><p class="hint">Everyone on the team and ${esc(team.patient_name)} see this thread. Each time you open it is recorded.</p><div class="bubbles" id="team-bubbles">`;
    for (const m of msgs) {
      const mine = m.author_id === session.user_id;
      html += `<div class="bubble ${mine ? "mine" : "theirs"}"><div class="kind">${esc(m.author_name)}</div><div>${esc(m.body)}</div><div class="time">${esc(new Date(m.created_at).toLocaleString())}</div></div>`;
    }
    if (msgs.length === 0) html += `<p class="empty">No messages yet.</p>`;
    html += `</div><form class="reply" id="team-reply"><textarea id="team-body" rows="2" maxlength="4000" placeholder="Write to the care team" required></textarea><button type="submit" class="btn">Send</button></form>`;
  }
  el.innerHTML = html;
  const propose = document.getElementById("team-propose");
  if (propose) propose.onsubmit = (e) => { e.preventDefault(); proposeTeam(summary.patient_id, document.getElementById("team-candidate").value); };
  const reply = document.getElementById("team-reply");
  if (reply) {
    const b = document.getElementById("team-bubbles");
    b.scrollTop = b.scrollHeight;
    reply.onsubmit = async (e) => {
      e.preventDefault();
      try {
        await api("POST", `/care-teams/${summary.patient_id}/messages`, { body: document.getElementById("team-body").value });
        loadCareTeams(document.getElementById("tab-content"));
      } catch (err) {
        showAlert(err.message);
      }
    };
  }
}

async function proposeTeam(patientId, memberId) {
  try {
    await api("POST", `/care-teams/${patientId}/members`, { member_id: memberId });
    showAlert("Proposed. They join once the patient approves in the app.", "success");
    loadCareTeams(document.getElementById("tab-content"));
  } catch (err) {
    showAlert(err.message);
  }
}
