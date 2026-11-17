// Forms tab (#195): the form builder for practice admins. Loaded before
// app.js; uses its globals (api, esc, attr, showAlert, session) at call time,
// and WeHelpForms from forms-eval.js for validation and the live preview.
//
// A form version's definition (wehelp.form.v1, see internal/forms) is edited
// in place as a plain object. Inputs name the field they edit with a JSON
// path in data-fb-path; rules and scores are built from pick lists, never
// typed as expressions. Buttons use data-fb with JSON arguments in
// data-fb-args, dispatched through FB_ACTIONS only (no inline handlers; the
// page's CSP forbids inline script). The server validates every save with
// the same rules as the preview (forms.Parse) and is authoritative.

const fb = {
  undo: [], // undo and redo snapshots of def (#260), page memory only
  redo: [],
  snapshot: null,
  save: { state: "saved", message: "", timer: null, inFlight: false, again: false, savedSnapshot: null },
  view: "list", // list or edit
  container: null,
  list: [],
  form: null, // GET /forms/{id}: { form, versions }
  version: null, // GET /form-versions/{id}
  def: null, // the definition being edited
  dirty: false,
  error: null, // { path, message } from the server or the local check
  newForm: false,
  preview: { answers: {}, page: 0, problems: [] },
};

// ---------------------------------------------------------------------------
// Small helpers
// ---------------------------------------------------------------------------

function fbAct(name, ...args) {
  return `data-fb="${attr(name)}" data-fb-args="${attr(JSON.stringify(args))}"`;
}

function fbPathAttr(path, kind) {
  return `data-fb-path="${attr(JSON.stringify(path))}" data-fb-kind="${kind}"`;
}

function getAt(obj, path) {
  let cur = obj;
  for (const k of path) {
    if (cur === null || cur === undefined) return undefined;
    cur = cur[k];
  }
  return cur;
}

// setAt sets the value at path; undefined deletes an object key.
function setAt(obj, path, value) {
  let cur = obj;
  for (let i = 0; i < path.length - 1; i++) {
    if (cur[path[i]] === undefined || cur[path[i]] === null) cur[path[i]] = typeof path[i + 1] === "number" ? [] : {};
    cur = cur[path[i]];
  }
  const last = path[path.length - 1];
  if (value === undefined && !Array.isArray(cur)) delete cur[last];
  else cur[last] = value;
}

// pathString writes a path the way the validator reports it.
function pathString(path) {
  let s = "";
  for (const k of path) s += typeof k === "number" ? `[${k}]` : (s ? "." : "") + k;
  return s;
}

function allIds(def) {
  const ids = new Set();
  for (const s of def.sections || []) {
    ids.add(s.id);
    for (const p of s.pages || []) {
      ids.add(p.id);
      for (const it of p.items || []) ids.add(it.id);
    }
  }
  for (const sc of def.scores || []) ids.add(sc.id);
  return ids;
}

function newId(def, prefix) {
  const ids = allIds(def);
  for (let n = 1; ; n++) if (!ids.has(prefix + n)) return prefix + n;
}

// items lists the answerable items in document order with their position.
function fbItems(def) {
  const out = [];
  let g = 0;
  (def.sections || []).forEach((s, si) => {
    (s.pages || []).forEach((p, pi) => {
      (p.items || []).forEach((it, ii) => out.push({ item: it, si, pi, ii, page: g }));
      g++;
    });
  });
  return out;
}

// eligibleItems are the items a rule at this place may use: earlier pages,
// or earlier on the same page for an item; for a section's next, every page
// up to the end of that section.
function eligibleItems(def, where) {
  return fbItems(def).filter((x) => {
    if (x.item.type === "display") return false;
    if (where.kind === "item") return x.page < where.page || (x.page === where.page && x.ii < where.ii);
    if (where.kind === "page") return x.page < where.page;
    return x.si <= where.si; // next
  }).map((x) => x.item);
}

function itemById(def, id) {
  const x = fbItems(def).find((y) => y.item.id === id);
  return x ? x.item : null;
}

function todayISO() {
  const d = new Date();
  const two = (n) => (n < 10 ? "0" : "") + n;
  return `${d.getFullYear()}-${two(d.getMonth() + 1)}-${two(d.getDate())}`;
}

// ---------------------------------------------------------------------------
// Rules: the pick-list editor reads and writes the JSON tree
// ---------------------------------------------------------------------------

const OPS = {
  eq: "is", ne: "is not", lt: "is less than", le: "is at most", gt: "is more than", ge: "is at least",
  in: "is one of", answered: "is answered", unanswered: "is not answered",
};
const DATE_OPS = { eq: "is", lt: "is before", gt: "is after" };

function opsFor(item) {
  if (!item) return ["answered", "unanswered"];
  switch (item.type) {
    case "choice": return item.multiple ? ["in", "answered", "unanswered"] : ["eq", "ne", "in", "answered", "unanswered"];
    case "boolean": return ["eq", "answered", "unanswered"];
    case "integer": case "decimal": return ["eq", "ne", "lt", "le", "gt", "ge", "answered", "unanswered"];
    case "date": return ["eq", "lt", "gt", "answered", "unanswered"];
    default: return ["eq", "ne", "answered", "unanswered"];
  }
}

function opLabel(item, op) {
  if (item && item.type === "date" && DATE_OPS[op]) return DATE_OPS[op];
  if (item && item.type === "choice" && item.multiple && op === "in") return "includes any of";
  return OPS[op];
}

function defaultValue(item, op) {
  if (!item) return "";
  if (op === "in") return item.options && item.options.length ? [item.options[0].value] : [];
  switch (item.type) {
    case "choice": return item.options && item.options.length ? item.options[0].value : "";
    case "boolean": return true;
    case "integer": case "decimal": return 0;
    case "date": return todayISO();
    default: return "";
  }
}

function literalValue(lit) {
  if (!lit || typeof lit !== "object") return undefined;
  if ("num" in lit) return lit.num;
  if ("str" in lit) return lit.str;
  if ("bool" in lit) return lit.bool;
  return undefined;
}

// condOf reads one simple condition, or returns null.
function condOf(e) {
  if (!e || typeof e !== "object") return null;
  const keys = Object.keys(e);
  if (keys.length !== 1) return null;
  const op = keys[0], a = e[op];
  if (op === "answered" && typeof a === "string") return { item: a, op: "answered" };
  if (op === "not" && a && typeof a.answered === "string" && Object.keys(a).length === 1) return { item: a.answered, op: "unanswered" };
  if (["eq", "ne", "lt", "le", "gt", "ge"].includes(op) && Array.isArray(a) && a.length === 2 &&
      a[0] && typeof a[0].answer === "string" && Object.keys(a[0]).length === 1 && literalValue(a[1]) !== undefined) {
    return { item: a[0].answer, op, value: literalValue(a[1]) };
  }
  if (op === "in" && Array.isArray(a) && a.length >= 2 && a[0] && typeof a[0].answer === "string" &&
      a.slice(1).every((l) => l && typeof l.str === "string" && Object.keys(l).length === 1)) {
    return { item: a[0].answer, op: "in", value: a.slice(1).map((l) => l.str) };
  }
  return null;
}

// ruleToConds turns a rule into { mode, conds } for the editor, or null when
// it is more complex than the editor can show.
function ruleToConds(expr) {
  if (expr === undefined || expr === null) return { mode: "all", conds: [] };
  const one = condOf(expr);
  if (one) return { mode: "all", conds: [one] };
  const key = Object.keys(expr)[0];
  if ((key === "and" || key === "or") && Object.keys(expr).length === 1 && Array.isArray(expr[key])) {
    const conds = expr[key].map(condOf);
    if (conds.every(Boolean)) return { mode: key === "and" ? "all" : "any", conds };
  }
  return null;
}

function condNode(def, c) {
  const item = itemById(def, c.item);
  if (c.op === "answered") return { answered: c.item };
  if (c.op === "unanswered") return { not: { answered: c.item } };
  if (c.op === "in") return { in: [{ answer: c.item }, ...(c.value || []).map((v) => ({ str: String(v) }))] };
  let lit;
  if (item && (item.type === "integer" || item.type === "decimal")) lit = { num: Number(c.value) };
  else if (item && item.type === "boolean") lit = { bool: c.value === true || c.value === "true" };
  else lit = { str: String(c.value ?? "") };
  return { [c.op]: [{ answer: c.item }, lit] };
}

// condsToRule writes the editor's conditions back as a rule (undefined for none).
function condsToRule(def, r) {
  if (!r || r.conds.length === 0) return undefined;
  const nodes = r.conds.map((c) => condNode(def, c));
  if (nodes.length === 1) return nodes[0];
  return { [r.mode === "any" ? "or" : "and"]: nodes };
}

// ---------------------------------------------------------------------------
// Scores: the editor handles "sum of chosen items' scores"
// ---------------------------------------------------------------------------

function scorable(item) {
  return item.type === "choice" && (item.options || []).length > 0 && item.options.every((o) => typeof o.score === "number");
}

// scoreItems returns the item ids a score sums, or null if its expression
// is something else.
function scoreItems(expr) {
  if (!expr || typeof expr !== "object") return null;
  if (typeof expr.scoreOf === "string" && Object.keys(expr).length === 1) return [expr.scoreOf];
  if (Array.isArray(expr.sum) && Object.keys(expr).length === 1 &&
      expr.sum.every((a) => a && typeof a.scoreOf === "string" && Object.keys(a).length === 1)) {
    return expr.sum.map((a) => a.scoreOf);
  }
  return null;
}

function sumExpr(def, ids) {
  const order = fbItems(def).map((x) => x.item.id);
  const sorted = ids.slice().sort((a, b) => order.indexOf(a) - order.indexOf(b));
  return { sum: sorted.map((id) => ({ scoreOf: id })) };
}

// ---------------------------------------------------------------------------
// API (keeps the validator's path on errors)
// ---------------------------------------------------------------------------

async function fbFetch(method, path, body) {
  const opts = { method, credentials: "same-origin", headers: {} };
  if (session && session.csrf_token) opts.headers["x-csrf-token"] = session.csrf_token;
  if (body instanceof FormData) {
    opts.body = body;
  } else if (body !== undefined) {
    opts.headers["content-type"] = "application/json";
    opts.body = JSON.stringify(body);
  }
  const res = await fetch("/api/v1" + path, opts);
  const data = await res.json().catch(() => ({}));
  if (!res.ok) {
    const err = new Error(data.error || res.statusText);
    err.path = data.path;
    err.status = res.status;
    throw err;
  }
  return data;
}

function fbFail(err) {
  if (err.path !== undefined) {
    fb.error = { path: err.path, message: err.message };
    fbRender();
    return;
  }
  showAlert(err.message);
}

// ---------------------------------------------------------------------------
// The list
// ---------------------------------------------------------------------------

async function loadForms(container) {
  fb.container = container;
  if (fb.view === "edit" && fb.version) {
    fbRender();
    return;
  }
  const data = await api("GET", "/forms");
  fb.list = data.forms || [];
  fb.view = "list";
  fbRender();
}

function renderFormList() {
  let html = `<div class="card"><h2>Forms</h2>
    <p class="hint">Intake forms and questionnaires. Build and preview a draft, then publish it; published versions can't be changed.</p>
    <div class="inline-form">
      <button class="btn" ${fbAct("toggleNewForm")}>New form</button>
      <label class="btn btn-outline">Import JSON<input type="file" accept="application/json,.json" data-fb-import hidden></label>
    </div>`;
  if (fb.newForm) {
    html += `<div class="inline-form" style="margin-top:0.5rem">
      <input id="fb-new-name" placeholder="Form name, for example New patient intake" maxlength="200">
      <select id="fb-new-template"><option value="">Start blank</option>${(fb.templates || []).map((t) => `<option value="${attr(t.key)}" title="${attr(t.description)}">Start from: ${esc(t.name)}</option>`).join("")}</select>
      <button class="btn" ${fbAct("createForm")}>Create</button></div>
      <p class="hint">Templates are synthetic starting points: adapt the questions to your practice before you publish.</p>`;
  }
  html += "</div><div class=\"card\">";
  if (fb.list.length === 0) {
    html += '<p class="empty">No forms yet.</p>';
  } else {
    html += "<table><thead><tr><th>Name</th><th>Kind</th><th>Latest</th><th>Published</th><th>Actions</th></tr></thead><tbody>";
    for (const f of fb.list) {
      const latest = f.latest ? `v${f.latest.version} <span class="badge badge-${f.latest.status === "published" ? "active" : "pending"}">${esc(f.latest.status)}</span>` : "";
      const actions = [];
      if (f.latest) actions.push(`<button class="btn btn-sm" ${fbAct("openVersion", f.latest.id)}>${f.kind === "instrument" || f.archived_at ? "View" : "Open"}</button>`);
      if (f.latest) actions.push(`<button class="btn btn-sm btn-outline" ${fbAct("copyForm", f.id, null)}>${f.kind === "instrument" ? "Copy to edit" : "Duplicate"}</button>`);
      if (f.kind === "custom" && !f.archived_at) actions.push(`<button class="btn btn-sm btn-danger" ${fbAct("archiveForm", f.id, f.name)}>Archive</button>`);
      html += `<tr><td>${esc(f.name)}${f.archived_at ? ' <span class="badge badge-revoked">archived</span>' : ""}</td>
        <td>${f.kind === "instrument" ? "Built-in" : "Custom"}</td><td>${latest}</td>
        <td>${f.published ? "v" + f.published : "—"}</td><td>${actions.join(" ")}</td></tr>`;
    }
    html += "</tbody></table>";
  }
  return html + "</div>";
}

async function toggleNewForm() {
  fb.newForm = !fb.newForm;
  if (fb.newForm && !fb.templates) {
    try {
      fb.templates = (await fbFetch("GET", "/form-templates")).templates || [];
    } catch (_) {
      fb.templates = []; // the picker just offers a blank form
    }
  }
  fbRender();
}

async function createForm() {
  const el = document.getElementById("fb-new-name");
  const name = el ? el.value.trim() : "";
  if (!name) {
    showAlert("Give the form a name.");
    return;
  }
  try {
    const tpl = document.getElementById("fb-new-template");
    const body = { name };
    if (tpl && tpl.value) body.template = tpl.value;
    const out = await fbFetch("POST", "/forms", body);
    fb.newForm = false;
    await openVersion(out.version_id);
  } catch (err) {
    showAlert(err.message);
  }
}

async function archiveForm(id, name) {
  if (!confirm(`Archive "${name}"? It stays readable but can't be changed or used for new assignments.`)) return;
  try {
    await fbFetch("POST", `/forms/${id}/archive`);
    fb.view = "list";
    await loadForms(fb.container);
  } catch (err) {
    showAlert(err.message);
  }
}

async function importForm(file) {
  let def;
  try {
    def = JSON.parse(await file.text());
  } catch (_) {
    showAlert("That file isn't JSON.");
    return;
  }
  try {
    const out = await fbFetch("POST", "/forms/import", def);
    await openVersion(out.version_id);
  } catch (err) {
    showAlert(err.path !== undefined ? `Not imported: ${err.message}` : err.message);
  }
}

// ---------------------------------------------------------------------------
// The editor
// ---------------------------------------------------------------------------

async function openVersion(id) {
  try {
    const v = await fbFetch("GET", `/form-versions/${id}`);
    fb.form = await fbFetch("GET", `/forms/${v.form_id}`);
    fb.version = v;
    fb.def = JSON.parse(JSON.stringify(v.definition));
    fb.dirty = false;
    fb.error = null;
    resetHistory();
    fb.preview = { answers: {}, page: 0, problems: [] };
    fb.view = "edit";
    fbRender();
  } catch (err) {
    showAlert(err.message);
  }
}

function fbReadOnly() {
  const v = fb.version;
  return !v || v.status !== "draft" || v.kind === "instrument" || (fb.form && fb.form.form.archived_at);
}

function backToList() {
  if (!fbCanLeave()) return;
  fb.view = "list";
  fb.version = null;
  loadForms(fb.container);
}

function fbRender() {
  const c = fb.container;
  if (!c) return;
  if (fb.view !== "edit" || !fb.def) {
    c.innerHTML = renderFormList();
    return;
  }
  const scroll = window.scrollY;
  c.innerHTML = renderEditor();
  renderPreview();
  markError();
  window.scrollTo(0, scroll);
}

function renderEditor() {
  const v = fb.version, def = fb.def, ro = fbReadOnly();
  const f = fb.form ? fb.form.form : { name: v.form_name };
  const hasDraft = fb.form && fb.form.versions.some((x) => x.status === "draft");
  const badge = `<span class="badge badge-${v.status === "published" ? "active" : "pending"}">${esc(v.status)}</span>`;
  let head = `<div class="card fb-head"><div><h2>${esc(f.name)} · v${v.version} ${badge}${v.kind === "instrument" ? ' <span class="badge badge-scheduled">built-in</span>' : ""}</h2>`;
  if (v.kind === "instrument") head += '<p class="hint">Built-in instruments are read-only; you can preview them, or copy one to edit.</p>';
  else if (def.copyright) head += `<p class="hint fb-copynote">Based on a published instrument. Once edited, it's no longer the validated instrument; keep the attribution: ${esc(def.copyright)}</p>`;
  else if (v.status === "published") head += '<p class="hint">Published versions can\'t be changed. Start a new version to edit.</p>';
  head += `</div><div class="inline-form">
    <button class="btn btn-outline" ${fbAct("backToList")}>Back to forms</button>`;
  if (fb.form && fb.form.versions.length > 1) {
    head += `<select data-fb-version>${fb.form.versions.map((x) => `<option value="${attr(x.id)}" ${x.id === v.id ? "selected" : ""}>v${x.version} ${esc(x.status)}</option>`).join("")}</select>`;
  }
  if (!ro) {
    head += `<button class="btn btn-outline" ${fbAct("undoEdit")} ${fb.undo.length ? "" : "disabled"} title="Undo (Cmd/Ctrl+Z)">Undo</button>
      <button class="btn btn-outline" ${fbAct("redoEdit")} ${fb.redo.length ? "" : "disabled"} title="Redo (Shift+Cmd/Ctrl+Z)">Redo</button>
      <span id="fb-save-status" class="fb-save-status">${esc(saveStatusText())}</span>
      <button id="fb-signin" class="btn" ${fbAct("openSignIn")} ${fb.save.state === "signedout" ? "" : "hidden"}>Sign in to save</button>
      <button class="btn" ${fbAct("saveDraft")}>Save</button>
      <button class="btn" ${fbAct("publishDraft")}>Publish</button>`;
  }
  if (v.status === "published" && v.kind !== "instrument" && !hasDraft && !(fb.form && fb.form.form.archived_at)) {
    head += `<button class="btn" ${fbAct("newVersion")}>New version</button>`;
  }
  head += `<button class="btn btn-outline" ${fbAct("copyForm", v.form_id, v.id)}>${v.kind === "instrument" ? "Copy to edit" : "Duplicate"}</button>`;
  head += `<a class="btn btn-outline" href="/api/v1/form-versions/${attr(v.id)}/export" download>Export JSON</a></div></div>`;

  const err = `<div id="fb-errbox">${errorBox()}</div>`;

  let html = head + err + '<div class="fb-grid"><div class="fb-edit">';
  html += `<div class="card" data-fb-anchor=""><h3>Form</h3>
    <div class="fb-row">${field("Title", ["title"], def.title, "req", ro)}${field("Language", ["locale"], def.locale, "str", ro, "en-US")}</div>
    ${area("Description", ["description"], def.description, ro)}</div>`;
  (def.sections || []).forEach((s, si) => { html += renderSection(s, si, ro); });
  if (!ro) html += `<button class="btn btn-outline" ${fbAct("addSection")}>Add section</button>`;
  html += renderScores(ro) + renderAssets(ro);
  html += '</div><div class="fb-side"><div class="card fb-preview" id="fb-preview"></div></div></div>';
  return html;
}

function field(label, path, value, kind, ro, placeholder) {
  const type = kind === "num" || kind === "int" ? "number" : "text";
  const step = kind === "num" ? ' step="any"' : "";
  return `<label class="fb-field"><span>${esc(label)}</span><input type="${type}"${step} ${fbPathAttr(path, kind)}
    value="${attr(value ?? "")}" ${placeholder ? `placeholder="${attr(placeholder)}"` : ""} ${ro ? "disabled" : ""}></label>`;
}

function area(label, path, value, ro) {
  return `<label class="fb-field fb-wide"><span>${esc(label)}</span><textarea rows="2" ${fbPathAttr(path, "str")} ${ro ? "disabled" : ""}>${esc(value ?? "")}</textarea></label>`;
}

function check(label, path, value, ro) {
  return `<label class="fb-check"><input type="checkbox" ${fbPathAttr(path, "bool")} ${value ? "checked" : ""} ${ro ? "disabled" : ""}> ${esc(label)}</label>`;
}

function choose(label, path, value, options, ro) {
  return `<label class="fb-field"><span>${esc(label)}</span><select ${fbPathAttr(path, "sel")} ${ro ? "disabled" : ""}>
    ${options.map(([v, l]) => `<option value="${attr(v)}" ${String(value ?? "") === v ? "selected" : ""}>${esc(l)}</option>`).join("")}</select></label>`;
}

function moveButtons(kind, path, i, n, ro) {
  if (ro) return "";
  return `<span class="fb-tools">
    <button class="btn btn-sm btn-outline" ${fbAct("moveNode", path, i, -1)} ${i === 0 ? "disabled" : ""} title="Move up">↑</button>
    <button class="btn btn-sm btn-outline" ${fbAct("moveNode", path, i, 1)} ${i === n - 1 ? "disabled" : ""} title="Move down">↓</button>
    <button class="btn btn-sm btn-danger" ${fbAct("removeNode", path, i)} ${n === 1 && (kind === "section" || kind === "page" || kind === "item") ? "disabled" : ""} title="Remove">✕</button></span>`;
}

function pageGlobalIndex(si, pi) {
  let g = 0;
  for (let i = 0; i < si; i++) g += (fb.def.sections[i].pages || []).length;
  return g + pi;
}

function renderSection(s, si, ro) {
  const def = fb.def, path = ["sections", si], n = def.sections.length;
  let html = `<div class="card fb-section" data-fb-anchor="${attr(pathString(path))}">
    <div class="fb-titlebar"><h3>Section ${si + 1}</h3>${moveButtons("section", ["sections"], si, n, ro)}</div>
    <div class="fb-row">${field("Id", [...path, "id"], s.id, "req", ro)}${field("Title", [...path, "title"], s.title, "str", ro)}</div>`;
  (s.pages || []).forEach((p, pi) => { html += renderPage(p, si, pi, ro); });
  if (!ro) html += `<button class="btn btn-sm btn-outline" ${fbAct("addPage", si)}>Add page</button>`;
  html += renderNext(s, si, ro);
  return html + "</div>";
}

function renderPage(p, si, pi, ro) {
  const path = ["sections", si, "pages", pi], n = fb.def.sections[si].pages.length;
  const g = pageGlobalIndex(si, pi);
  let html = `<div class="fb-page" data-fb-anchor="${attr(pathString(path))}">
    <div class="fb-titlebar"><h4>Page ${pi + 1}</h4><span>${ro ? "" : `<button class="btn btn-sm btn-outline" ${fbAct("duplicatePage", si, pi)} title="Duplicate this page">Duplicate</button>`}${moveButtons("page", ["sections", si, "pages"], pi, n, ro)}</span></div>
    <div class="fb-row">${field("Id", [...path, "id"], p.id, "req", ro)}${field("Title", [...path, "title"], p.title, "str", ro)}</div>
    ${renderRule("Show this page only when", [...path, "enableWhen"], p.enableWhen, { kind: "page", page: g }, ro, false)}`;
  html += renderItems(p, si, pi, g, ro); // grids where rows share answers (#259)
  if (!ro) {
    html += `<div class="inline-form fb-add"><select data-fb-newtype="${attr(JSON.stringify([si, pi]))}">
      ${NEW_ITEMS.map(([t, l]) => `<option value="${t}">${esc(l)}</option>`).join("")}</select>
      <button class="btn btn-sm btn-outline" ${fbAct("addFormItem", si, pi)}>Add item</button></div>`;
  }
  return html + "</div>";
}

const ITEM_TYPES = [
  ["choice", "Choice"], ["boolean", "Yes / no"], ["integer", "Whole number"], ["decimal", "Number"],
  ["date", "Date"], ["text", "Text"], ["display", "Text or image (no answer)"],
];
const CONTROLS = {
  choice: [["", "Radio buttons"], ["dropdown", "Dropdown"], ["buttons", "Buttons"]],
  boolean: [["", "Default"], ["toggle", "Toggle"], ["buttons", "Buttons"]],
  integer: [["", "Default"], ["keypad", "Keypad"], ["slider", "Slider"]],
  decimal: [["", "Default"], ["keypad", "Keypad"], ["slider", "Slider"]],
  text: [["", "One line"], ["long", "Several lines"]],
};

function renderItem(it, si, pi, ii, g, ro) {
  const path = ["sections", si, "pages", pi, "items", ii], n = fb.def.sections[si].pages[pi].items.length;
  const typeLabel = (ITEM_TYPES.find((x) => x[0] === it.type) || [it.type, it.type])[1];
  let html = `<div class="fb-item" data-fb-anchor="${attr(pathString(path))}">
    <div class="fb-titlebar"><strong>${esc(typeLabel)}</strong><span>${ro ? "" : `<button class="btn btn-sm btn-outline" ${fbAct("duplicateItem", si, pi, ii)} title="Duplicate this question">Duplicate</button>`}${moveButtons("item", ["sections", si, "pages", pi, "items"], ii, n, ro)}</span></div>
    <div class="fb-row">${field("Id", [...path, "id"], it.id, "req", ro)}`;
  if (it.type !== "display") html += check("Required", [...path, "required"], it.required, ro);
  if (CONTROLS[it.type]) html += choose("Shown as", [...path, "control"], it.control, CONTROLS[it.type], ro);
  html += "</div>" + area(it.type === "display" ? "Text" : "Question", [...path, "text"], it.text, ro);
  switch (it.type) {
    case "choice": {
      html += check("Several answers allowed", [...path, "multiple"], it.multiple, ro);
      html += '<table class="fb-options"><thead><tr><th>Value</th><th>Text</th><th>Score</th><th></th></tr></thead><tbody>';
      (it.options || []).forEach((o, oi) => {
        const op = [...path, "options", oi];
        html += `<tr data-fb-anchor="${attr(pathString(op))}"><td><input ${fbPathAttr([...op, "value"], "req")} value="${attr(o.value)}" ${ro ? "disabled" : ""}></td>
          <td><input ${fbPathAttr([...op, "text"], "req")} value="${attr(o.text)}" ${ro ? "disabled" : ""}></td>
          <td><input type="number" step="any" class="fb-num" ${fbPathAttr([...op, "score"], "num")} value="${attr(o.score ?? "")}" ${ro ? "disabled" : ""}></td>
          <td>${moveButtons("option", [...path, "options"], oi, it.options.length, ro)}</td></tr>`;
      });
      html += "</tbody></table>";
      if (!ro) {
        html += `<div class="inline-form"><button class="btn btn-sm btn-outline" ${fbAct("addOption", path)}>Add option</button>
          ${answerListPicker(`data-fb-itemlist="${attr(JSON.stringify(path))}"`)}
          ${copyFromPicker(`data-fb-itemcopy="${attr(JSON.stringify(path))}"`, [it.id])}
          ${(it.options || []).length && it.options.every((o) => typeof o.score === "number") ? `<button class="btn btn-sm btn-outline" ${fbAct("reverseScores", path)}>Reverse scores</button>` : ""}</div>`;
      }
      break;
    }
    case "integer": case "decimal": {
      const k = it.type === "integer" ? "int" : "num";
      html += `<div class="fb-row">${field("Min", [...path, "min"], it.min, k, ro)}${field("Max", [...path, "max"], it.max, k, ro)}
        ${field("Step", [...path, "step"], it.step, k, ro)}${field("Unit", [...path, "unit"], it.unit, "str", ro)}</div>`;
      if (it.control === "slider") html += `<div class="fb-row">${field("Low end label", [...path, "minLabel"], it.minLabel, "str", ro)}${field("High end label", [...path, "maxLabel"], it.maxLabel, "str", ro)}</div>`;
      break;
    }
    case "date":
      html += `<div class="fb-row">${field("Earliest", [...path, "min"], it.min, "str", ro, "YYYY-MM-DD or today")}${field("Latest", [...path, "max"], it.max, "str", ro, "YYYY-MM-DD or today")}</div>`;
      break;
    case "text":
      html += `<div class="fb-row">${field("Longest answer (characters)", [...path, "maxLength"], it.maxLength, "int", ro, "4000")}</div>`;
      break;
    case "display":
      html += renderMedia(it, path, ro);
      break;
  }
  html += renderRule("Show only when", [...path, "enableWhen"], it.enableWhen, { kind: "item", page: g, ii }, ro, false);
  return html + "</div>";
}

function assetURL(name) {
  return `/api/v1/form-versions/${encodeURIComponent(fb.version.id)}/assets/${encodeURIComponent(name)}`;
}

function renderMedia(it, path, ro) {
  const assets = (fb.version && fb.version.assets) || [];
  if (it.media) {
    const ok = assets.some((a) => a.name === it.media.asset);
    return `<div class="fb-media">${ok ? `<img src="${attr(assetURL(it.media.asset))}" alt="${attr(it.media.alt || "")}">` : `<div class="alert alert-error">No image ${esc(it.media.asset)} on this version.</div>`}
      <div class="fb-row">${field("Description for screen readers", [...path, "media", "alt"], it.media.alt, "str", ro)}
      ${ro ? "" : `<button class="btn btn-sm btn-outline" ${fbAct("removeMedia", path)}>Remove image</button>`}</div></div>`;
  }
  if (ro) return "";
  let html = `<div class="inline-form fb-media"><label class="btn btn-sm btn-outline">Upload image (PNG or JPEG)
    <input type="file" accept="image/png,image/jpeg" hidden data-fb-upload="${attr(JSON.stringify(path))}"></label>`;
  if (assets.length) {
    html += `<select data-fb-pickasset="${attr(JSON.stringify(path))}"><option value="">or use an uploaded image…</option>
      ${assets.map((a) => `<option value="${attr(a.name)}">${esc(a.name)}</option>`).join("")}</select>`;
  }
  return html + "</div>";
}

// renderRule is the pick-list editor for one rule. keepOne: a branch rule
// can't be emptied (an entry without a rule is the default).
function renderRule(label, path, expr, where, ro, keepOne) {
  const def = fb.def;
  const r = ruleToConds(expr);
  const anchor = `data-fb-anchor="${attr(pathString(path))}"`;
  if (r === null) {
    return `<div class="fb-rule" ${anchor}><span class="hint">${esc(label)}: a rule the builder can't show (it was written by hand).</span>
      <code class="fb-json">${esc(JSON.stringify(expr))}</code>${ro || keepOne ? "" : ` <button class="btn btn-sm btn-outline" ${fbAct("clearRule", path)}>Remove rule</button>`}</div>`;
  }
  const items = eligibleItems(def, where);
  if (r.conds.length === 0) {
    if (ro) return "";
    if (items.length === 0) return `<div class="fb-rule hint" ${anchor}>${esc(label)}: no earlier questions to use.</div>`;
    return `<div class="fb-rule" ${anchor}><button class="btn btn-sm btn-outline" ${fbAct("addCond", path, items[0].id)}>${esc(label)}…</button></div>`;
  }
  const rp = attr(JSON.stringify(path));
  let html = `<div class="fb-rule" ${anchor}><span class="fb-rule-label">${esc(label)}</span>`;
  if (r.conds.length > 1) {
    html += ` <select data-fb-rule="${rp}" data-fb-cf="mode" ${ro ? "disabled" : ""}>
      <option value="all" ${r.mode === "all" ? "selected" : ""}>all of these hold</option>
      <option value="any" ${r.mode === "any" ? "selected" : ""}>any of these holds</option></select>`;
  }
  r.conds.forEach((c, ci) => {
    const item = itemById(def, c.item);
    const options = items.slice();
    if (item && !options.includes(item)) options.push(item);
    html += `<div class="fb-cond inline-form">
      <select data-fb-rule="${rp}" data-fb-ci="${ci}" data-fb-cf="item" ${ro ? "disabled" : ""}>
        ${item ? "" : `<option value="${attr(c.item)}" selected>${esc(c.item)} (missing)</option>`}
        ${options.map((x) => `<option value="${attr(x.id)}" ${x.id === c.item ? "selected" : ""}>${esc(x.id)}: ${esc((x.text || "").slice(0, 40))}</option>`).join("")}</select>
      <select data-fb-rule="${rp}" data-fb-ci="${ci}" data-fb-cf="op" ${ro ? "disabled" : ""}>
        ${opsFor(item).map((op) => `<option value="${op}" ${op === c.op ? "selected" : ""}>${esc(opLabel(item, op))}</option>`).join("")}</select>
      ${condValue(item, c, rp, ci, ro)}
      ${ro || (keepOne && r.conds.length === 1) ? "" : `<button class="btn btn-sm btn-outline" ${fbAct("removeCond", path, ci)} title="Remove">✕</button>`}</div>`;
  });
  if (!ro && items.length) html += `<button class="btn btn-sm btn-outline" ${fbAct("addCond", path, items[0].id)}>And another condition</button>`;
  return html + "</div>";
}

function condValue(item, c, rp, ci, ro) {
  if (c.op === "answered" || c.op === "unanswered" || !item) return "";
  const a = `data-fb-rule="${rp}" data-fb-ci="${ci}" data-fb-cf="value" ${ro ? "disabled" : ""}`;
  if (item.type === "choice") {
    const vals = Array.isArray(c.value) ? c.value : [c.value];
    return `<select ${a} ${c.op === "in" ? "multiple" : ""}>${(item.options || []).map((o) =>
      `<option value="${attr(o.value)}" ${vals.includes(o.value) ? "selected" : ""}>${esc(o.text || o.value)}</option>`).join("")}</select>`;
  }
  if (item.type === "boolean") {
    return `<select data-fb-bool ${a}><option value="true" ${c.value === true ? "selected" : ""}>yes</option><option value="false" ${c.value === false ? "selected" : ""}>no</option></select>`;
  }
  if (item.type === "integer" || item.type === "decimal") return `<input type="number" step="any" class="fb-num" ${a} value="${attr(c.value)}">`;
  if (item.type === "date") return `<input type="date" ${a} value="${attr(c.value)}">`;
  return `<input ${a} value="${attr(c.value)}">`;
}

function renderNext(s, si, ro) {
  const def = fb.def, path = ["sections", si, "next"];
  const next = s.next || [];
  const later = def.sections.slice(si + 1).map((x) => [x.id, `Section ${def.sections.indexOf(x) + 1} (${x.id})`]);
  const targets = [...later, ["end", "End of the form"]];
  let html = `<div class="fb-next" data-fb-anchor="${attr(pathString(path))}"><h4>After this section</h4>`;
  next.forEach((b, bi) => {
    if (!b.when) return;
    html += `<div class="fb-branch" data-fb-anchor="${attr(pathString([...path, bi]))}">
      ${renderRule("Go to the section below when", [...path, bi, "when"], b.when, { kind: "next", si }, ro, true)}
      <div class="inline-form">${choose("Go to", [...path, bi, "section"], b.section, targets, ro)}
      ${ro ? "" : `<button class="btn btn-sm btn-outline" ${fbAct("removeBranch", si, bi)}>Remove branch</button>`}</div></div>`;
  });
  const dflt = next.find((b) => !b.when);
  const nextLabel = si + 1 < def.sections.length ? `The next section (${def.sections[si + 1].id})` : "End of the form";
  html += `<label class="fb-field"><span>Otherwise go to</span><select data-fb-default="${si}" ${ro ? "disabled" : ""}>
    <option value="">${esc(nextLabel)}</option>
    ${targets.map(([v, l]) => `<option value="${attr(v)}" ${dflt && dflt.section === v ? "selected" : ""}>${esc(l)}</option>`).join("")}</select></label>`;
  if (!ro && eligibleItems(def, { kind: "next", si }).length) html += ` <button class="btn btn-sm btn-outline" ${fbAct("addBranch", si)}>Add a branch</button>`;
  return html + "</div>";
}

function renderScores(ro) {
  const def = fb.def;
  const scorableItems = fbItems(def).map((x) => x.item).filter(scorable);
  let html = '<div class="card" data-fb-anchor="scores"><h3>Scores</h3>';
  if (!(def.scores || []).length) html += '<p class="hint">No scores. A score adds up the scores of chosen options.</p>';
  (def.scores || []).forEach((sc, i) => {
    const path = ["scores", i];
    const ids = scoreItems(sc.expr);
    html += `<div class="fb-item" data-fb-anchor="${attr(pathString(path))}">
      <div class="fb-titlebar"><strong>Score ${i + 1}</strong>${moveButtons("score", ["scores"], i, def.scores.length, ro)}</div>
      <div class="fb-row">${field("Id", [...path, "id"], sc.id, "req", ro)}${field("Name", [...path, "text"], sc.text, "str", ro)}
      ${check("Show to the patient", [...path, "showScore"], sc.showScore, ro)}</div>`;
    if (ids === null) {
      html += `<p class="hint">Calculated by a formula the builder can't show (it was written by hand):</p><code class="fb-json">${esc(JSON.stringify(sc.expr))}</code>`;
    } else {
      html += '<div class="fb-scoreitems"><span>Adds up the scores of:</span>';
      if (!scorableItems.length) html += '<span class="hint"> no choice item has a score on every option yet.</span>';
      for (const it of scorableItems) {
        html += `<label class="fb-check"><input type="checkbox" data-fb-scoreitem="${attr(JSON.stringify([i, it.id]))}" ${ids.includes(it.id) ? "checked" : ""} ${ro ? "disabled" : ""}> ${esc(it.id)}</label>`;
      }
      html += "</div>";
    }
    const m = sc.missing || {};
    html += `<div class="fb-row"><label class="fb-field"><span>Unanswered items</span><select data-fb-missing="${i}" ${ro ? "disabled" : ""}>
      <option value="null" ${!m.policy || m.policy === "null" ? "selected" : ""}>make the score blank</option>
      <option value="zero" ${m.policy === "zero" ? "selected" : ""}>count as 0</option>
      <option value="prorate" ${m.policy === "prorate" ? "selected" : ""}>scale up from the answered ones</option></select></label>`;
    if (m.policy === "zero" || m.policy === "prorate") html += field("At least this many answered", [...path, "missing", "minAnswered"], m.minAnswered, "int", ro);
    if (m.policy === "prorate") html += field("Round to decimal places", [...path, "missing", "places"], m.places, "int", ro);
    html += '</div><table class="fb-options"><thead><tr><th>From</th><th>Band id</th><th>Label</th><th></th></tr></thead><tbody>';
    (sc.bands || []).forEach((b, bi) => {
      const bp = [...path, "bands", bi];
      html += `<tr data-fb-anchor="${attr(pathString(bp))}"><td><input type="number" step="any" class="fb-num" ${fbPathAttr([...bp, "min"], "num")} value="${attr(b.min ?? "")}" ${ro ? "disabled" : ""}></td>
        <td><input ${fbPathAttr([...bp, "id"], "req")} value="${attr(b.id)}" ${ro ? "disabled" : ""}></td>
        <td><input ${fbPathAttr([...bp, "text"], "str")} value="${attr(b.text ?? "")}" ${ro ? "disabled" : ""}></td>
        <td>${ro ? "" : `<button class="btn btn-sm btn-outline" ${fbAct("removeNode", [...path, "bands"], bi)}>✕</button>`}</td></tr>`;
    });
    html += "</tbody></table>";
    if (!ro) html += `<button class="btn btn-sm btn-outline" ${fbAct("addBand", i)}>Add band</button>`;
    html += "</div>";
  });
  if (!ro) html += `<button class="btn btn-sm btn-outline" ${fbAct("addScore")}>Add score</button>`;
  return html + "</div>";
}

function renderAssets(ro) {
  const assets = (fb.version && fb.version.assets) || [];
  if (!assets.length) return "";
  let html = '<div class="card"><h3>Images on this version</h3><table><tbody>';
  for (const a of assets) {
    html += `<tr><td><img class="fb-thumb" src="${attr(assetURL(a.name))}" alt=""></td><td>${esc(a.name)}</td><td>${Math.ceil(a.size / 1024)} KB</td>
      <td>${ro ? "" : `<button class="btn btn-sm btn-danger" ${fbAct("deleteAsset", a.name)}>Delete</button>`}</td></tr>`;
  }
  return html + "</tbody></table></div>";
}

// markError outlines the part of the form the error's path names.
function markError() {
  if (!fb.error || !fb.container) return;
  const p = fb.error.path || "";
  let best = null, bestLen = -1;
  fb.container.querySelectorAll("[data-fb-anchor]").forEach((el) => {
    const a = el.dataset.fbAnchor;
    if ((p === a || p.startsWith(a + ".") || p.startsWith(a + "[") || a === "") && a.length > bestLen) {
      best = el;
      bestLen = a.length;
    }
  });
  if (best) best.classList.add("fb-error");
}

// ---------------------------------------------------------------------------
// Editing actions
// ---------------------------------------------------------------------------

// changed records an edit to fb.def: an undo step, a fresh check, an
// autosave, and a redraw.
function changed() {
  const now = JSON.stringify(fb.def);
  if (now !== fb.snapshot) {
    if (fb.snapshot !== null) fb.undo.push(fb.snapshot);
    if (fb.undo.length > FB_HISTORY) fb.undo.shift();
    fb.redo = [];
    fb.snapshot = now;
  }
  afterEdit();
}

// ---------------------------------------------------------------------------
// Undo, redo and autosave (#260). History and drafts live in page memory
// only: never localStorage or IndexedDB, since a definition can hold
// practice content.
// ---------------------------------------------------------------------------

const FB_HISTORY = 100; // undo steps kept
const FB_AUTOSAVE_MS = 2000; // after the last edit

function resetHistory() {
  if (fb.save && fb.save.timer) clearTimeout(fb.save.timer);
  fb.undo = [];
  fb.redo = [];
  fb.snapshot = fb.def ? JSON.stringify(fb.def) : null;
  fb.save = { state: "saved", message: "", timer: null, inFlight: false, again: false, savedSnapshot: fb.snapshot };
}

function afterEdit() {
  fb.dirty = fb.snapshot !== fb.save.savedSnapshot;
  fb.error = localError();
  scheduleAutosave();
  fbRender();
}

function undoEdit() {
  if (!fb.undo.length || fbReadOnly()) return;
  fb.redo.push(fb.snapshot);
  fb.snapshot = fb.undo.pop();
  fb.def = JSON.parse(fb.snapshot);
  afterEdit();
}

function redoEdit() {
  if (!fb.redo.length || fbReadOnly()) return;
  fb.undo.push(fb.snapshot);
  fb.snapshot = fb.redo.pop();
  fb.def = JSON.parse(fb.snapshot);
  afterEdit();
}

function scheduleAutosave() {
  if (fb.save.timer) clearTimeout(fb.save.timer);
  fb.save.timer = null;
  if (fbReadOnly() || !fb.dirty) {
    if (!fb.save.inFlight && fb.save.state !== "failed" && fb.save.state !== "signedout") fb.save.state = "saved";
    return;
  }
  // Signed out: keep editing in page memory, but don't try to save until
  // the user signs in again (each try would only be refused).
  if (fb.save.state === "signedout") return;
  fb.save.state = "pending";
  fb.save.timer = setTimeout(autosave, FB_AUTOSAVE_MS);
}

async function autosave() {
  fb.save.timer = null;
  if (fb.save.inFlight) {
    fb.save.again = true; // save again when this one finishes
    return;
  }
  await persist();
  if (fb.save.again) {
    fb.save.again = false;
    if (fb.dirty) scheduleAutosave();
  }
}

// persist saves the current snapshot. It updates only the status line and
// the error box, never the whole editor, so typing elsewhere isn't lost.
async function persist() {
  const snap = fb.snapshot;
  const vid = fb.version && fb.version.id;
  if (!vid) return false;
  fb.save.inFlight = true;
  fb.save.state = "saving";
  showSaveStatus();
  try {
    await fbFetch("PUT", `/form-versions/${vid}`, JSON.parse(snap));
    if (!fb.version || fb.version.id !== vid) return true;
    fb.save.savedSnapshot = snap;
    fb.dirty = fb.snapshot !== snap;
    fb.save.state = fb.dirty ? "pending" : "saved";
    fb.save.message = "";
    if (!fb.dirty) fb.error = localError();
    return true;
  } catch (err) {
    if (err.status === 401) {
      // The staff session ended (30 minutes idle, or signed out elsewhere).
      // The draft stays in page memory; signing in again saves it.
      fb.save.state = "signedout";
      fb.save.message = "";
      return false;
    }
    fb.save.state = "failed";
    fb.save.message = err.message || String(err);
    if (err.path !== undefined) fb.error = { path: err.path, message: err.message };
    return false;
  } finally {
    fb.save.inFlight = false;
    showSaveStatus();
  }
}

function saveStatusText() {
  const s = fb.save || {};
  switch (s.state) {
    case "saving": return "Saving…";
    case "pending": return "Unsaved changes";
    case "failed": return "Not saved: " + s.message;
    case "signedout": return "Not saved: you were signed out. Your changes are kept on this page; sign in to save them.";
    default: return "Saved";
  }
}

// Signing in again from the editor (the staff session ends after 30 minutes
// idle). A dialog over the editor, so the unsaved draft stays on the page;
// after sign-in the draft saves at once.
function openSignIn() {
  if (typeof document === "undefined") return;
  let box = document.getElementById("fb-signin-dialog");
  if (!box) {
    box = document.createElement("div");
    box.id = "fb-signin-dialog";
    document.body.appendChild(box);
  }
  const practice = (session && session.practice) || "";
  box.innerHTML = `<div class="modal-backdrop"><div class="card login-card fb-signin">
      <h2>Sign in to save</h2>
      <p class="hint">You were signed out after 30 minutes without activity. Your form is still here.</p>
      <div id="fb-signin-error"></div>
      <div class="form-group"><label>Practice</label><input type="text" id="fb-signin-tenant" value="${attr(practice)}" required></div>
      <div class="form-group"><label>Email</label><input type="email" id="fb-signin-email" autocomplete="username" required></div>
      <div class="form-group"><label>Password</label><input type="password" id="fb-signin-password" autocomplete="current-password" required></div>
      <div class="form-group"><label>Authenticator code</label><input type="text" id="fb-signin-code" inputmode="numeric" autocomplete="one-time-code" maxlength="7"></div>
      <button class="btn" ${fbAct("submitSignIn")}>Sign in and save</button>
      <button class="btn btn-outline" ${fbAct("closeSignIn")}>Cancel</button>
    </div></div>`;
  box.hidden = false;
}

function closeSignIn() {
  if (typeof document === "undefined") return;
  const box = document.getElementById("fb-signin-dialog");
  if (box) { box.innerHTML = ""; box.hidden = true; }
}

async function submitSignIn() {
  const val = (id) => document.getElementById(id).value;
  const errBox = document.getElementById("fb-signin-error");
  try {
    const res = await fetch("/api/v1/staff/login", {
      method: "POST",
      credentials: "same-origin",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ tenant: val("fb-signin-tenant"), email: val("fb-signin-email"),
        password: val("fb-signin-password"), code: val("fb-signin-code") }),
    });
    const data = await res.json().catch(() => ({}));
    if (!res.ok || data.status !== "active") {
      throw new Error(res.ok ? "Finish setting up two-step sign-in in a new tab, then try again." : (data.error || res.statusText));
    }
    session = data; // the new CSRF token comes with it
    closeSignIn();
    fb.save.state = fb.dirty ? "pending" : "saved";
    await saveDraft();
  } catch (err) {
    if (errBox) errBox.innerHTML = `<div class="alert alert-error">${esc(err.message || String(err))}</div>`;
  }
}

function errorBox() {
  return fb.error ? `<div class="alert alert-error fb-errmsg">${esc(fb.error.message)}</div>` : "";
}

function showSaveStatus() {
  if (typeof document === "undefined") return;
  const el = document.getElementById("fb-save-status");
  if (el) {
    el.textContent = saveStatusText();
    el.className = "fb-save-status" + (fb.save.state === "failed" || fb.save.state === "signedout" ? " fb-flag" : "");
  }
  const signIn = document.getElementById("fb-signin");
  if (signIn) signIn.hidden = fb.save.state !== "signedout";
  const box = document.getElementById("fb-errbox");
  if (box) box.innerHTML = errorBox();
  if (fb.container && fb.container.querySelectorAll) {
    fb.container.querySelectorAll(".fb-error").forEach((x) => x.classList.remove("fb-error"));
    markError();
  }
}

// fbCanLeave asks before leaving an editor with unsaved changes.
function fbCanLeave() {
  if (fb.view !== "edit" || !fb.version || (!fb.dirty && !fb.save.inFlight)) return true;
  if (!confirm("Your latest changes aren't saved yet. Leave anyway?")) return false;
  if (fb.save.timer) clearTimeout(fb.save.timer);
  fb.save.timer = null;
  return true;
}

// localError checks the draft the way the server will, before saving.
function localError() {
  try {
    WeHelpForms.compile(fb.def);
    return null;
  } catch (e) {
    return e && e.path !== undefined ? { path: e.path, message: e.message } : null;
  }
}

function moveNode(path, i, dir) {
  const list = getAt(fb.def, path);
  const j = i + dir;
  if (!Array.isArray(list) || j < 0 || j >= list.length) return;
  [list[i], list[j]] = [list[j], list[i]];
  changed();
}

function removeNode(path, i) {
  const list = getAt(fb.def, path);
  if (!Array.isArray(list)) return;
  list.splice(i, 1);
  if (list.length === 0 && path[path.length - 1] === "bands") setAt(fb.def, path, undefined);
  changed();
}

function newItem(type) {
  const id = newId(fb.def, type === "display" ? "info" : "q");
  switch (type) {
    case "choice": return { id, type, text: "Question", options: [{ value: "yes", text: "Yes" }, { value: "no", text: "No" }] };
    case "integer": return { id, type, text: "Question", min: 0, max: 10 };
    case "display": return { id, type, text: "Text" };
    default: return { id, type, text: "Question" };
  }
}

function addFormItem(si, pi) {
  const sel = document.querySelector(`[data-fb-newtype="${CSS.escape(JSON.stringify([si, pi]))}"]`);
  const type = sel ? sel.value : "choice";
  if (type.startsWith("grid:")) return addGrid(si, pi, type.slice(5)); // #259
  if (type.startsWith("choice:")) {
    const it = newItem("choice");
    it.options = listOptions(type.slice(7));
    fb.def.sections[si].pages[pi].items.push(it);
    return changed();
  }
  fb.def.sections[si].pages[pi].items.push(newItem(type));
  changed();
}

function addPage(si) {
  const id = newId(fb.def, "p");
  fb.def.sections[si].pages.push({ id, items: [] });
  fb.def.sections[si].pages[fb.def.sections[si].pages.length - 1].items.push(newItem("display"));
  changed();
}

function addSection() {
  const sid = newId(fb.def, "s");
  fb.def.sections.push({ id: sid, pages: [] });
  const s = fb.def.sections[fb.def.sections.length - 1];
  s.pages.push({ id: newId(fb.def, "p"), items: [] });
  s.pages[0].items.push(newItem("display"));
  changed();
}

function addOption(path) {
  const it = getAt(fb.def, path);
  const opts = it.options || (it.options = []);
  let n = opts.length + 1;
  while (opts.some((o) => o.value === String(n))) n++;
  opts.push({ value: String(n), text: "Option " + n });
  changed();
}

function removeMedia(path) {
  setAt(fb.def, [...path, "media"], undefined);
  if (!getAt(fb.def, [...path, "text"])) setAt(fb.def, [...path, "text"], "Text");
  changed();
}

function writeRule(path, r) {
  setAt(fb.def, path, condsToRule(fb.def, r));
  changed();
}

function addCond(path, itemId) {
  const r = ruleToConds(getAt(fb.def, path)) || { mode: "all", conds: [] };
  const item = itemById(fb.def, itemId);
  const op = opsFor(item)[0];
  r.conds.push({ item: itemId, op, value: defaultValue(item, op) });
  writeRule(path, r);
}

function removeCond(path, ci) {
  const r = ruleToConds(getAt(fb.def, path));
  if (!r) return;
  r.conds.splice(ci, 1);
  writeRule(path, r);
}

function clearRule(path) {
  setAt(fb.def, path, undefined);
  changed();
}

function editCond(path, ci, fieldName, value) {
  const r = ruleToConds(getAt(fb.def, path));
  if (!r) return;
  if (fieldName === "mode") {
    r.mode = value;
    writeRule(path, r);
    return;
  }
  const c = r.conds[ci];
  if (!c) return;
  if (fieldName === "item") {
    c.item = value;
    const item = itemById(fb.def, value);
    c.op = opsFor(item).includes(c.op) ? c.op : opsFor(item)[0];
    c.value = defaultValue(item, c.op);
  } else if (fieldName === "op") {
    const item = itemById(fb.def, c.item);
    const wasList = c.op === "in";
    c.op = value;
    if (wasList !== (value === "in") || c.value === undefined) c.value = defaultValue(item, value);
  } else {
    c.value = value;
  }
  writeRule(path, r);
}

function addBranch(si) {
  const s = fb.def.sections[si];
  const items = eligibleItems(fb.def, { kind: "next", si });
  if (!items.length) return;
  const item = items[items.length - 1];
  const op = opsFor(item)[0];
  const target = si + 1 < fb.def.sections.length ? fb.def.sections[si + 1].id : "end";
  const next = s.next || (s.next = []);
  const at = next.findIndex((b) => !b.when);
  const branch = { when: condsToRule(fb.def, { mode: "all", conds: [{ item: item.id, op, value: defaultValue(item, op) }] }), section: target };
  if (at < 0) next.push(branch);
  else next.splice(at, 0, branch);
  changed();
}

function removeBranch(si, bi) {
  const s = fb.def.sections[si];
  s.next.splice(bi, 1);
  if (!s.next.length) delete s.next;
  changed();
}

function setDefaultNext(si, value) {
  const s = fb.def.sections[si];
  const next = (s.next || []).filter((b) => b.when);
  if (value) next.push({ section: value });
  if (next.length) s.next = next;
  else delete s.next;
  changed();
}

function addScore() {
  const def = fb.def;
  const ids = fbItems(def).map((x) => x.item).filter(scorable).map((it) => it.id);
  (def.scores || (def.scores = [])).push({ id: newId(def, "score"), text: "Total", expr: sumExpr(def, ids) });
  changed();
}

function addBand(i) {
  const sc = fb.def.scores[i];
  const bands = sc.bands || (sc.bands = []);
  const last = bands.length ? Number(bands[bands.length - 1].min) : -1;
  bands.push({ min: last + 1, id: "band" + (bands.length + 1), text: "" });
  changed();
}

function setScoreItem(i, id, on) {
  const sc = fb.def.scores[i];
  const ids = scoreItems(sc.expr) || [];
  const next = on ? Array.from(new Set([...ids, id])) : ids.filter((x) => x !== id);
  sc.expr = sumExpr(fb.def, next);
  changed();
}

function setMissing(i, policy) {
  const sc = fb.def.scores[i];
  const n = (scoreItems(sc.expr) || []).length;
  if (policy === "null") delete sc.missing;
  else if (policy === "zero") sc.missing = { policy: "zero" };
  else sc.missing = { policy: "prorate", minAnswered: Math.max(1, n - 1), places: 0 };
  changed();
}

// ---------------------------------------------------------------------------
// Saving, publishing, versions, images
// ---------------------------------------------------------------------------

async function saveDraft() {
  if (fb.save.timer) clearTimeout(fb.save.timer);
  fb.save.timer = null;
  const ok = await persist();
  if (ok && !fb.dirty) showAlert("Draft saved", "success");
  return ok;
}

async function publishDraft() {
  if (!confirm("Publish this version? Published versions can't be changed; to edit later you start a new version.")) return;
  if (fb.dirty && !(await saveDraft())) return;
  try {
    await fbFetch("POST", `/form-versions/${fb.version.id}/publish`);
    await openVersion(fb.version.id);
    showAlert("Published", "success");
  } catch (err) {
    fbFail(err);
  }
}

async function newVersion() {
  try {
    const out = await fbFetch("POST", `/forms/${fb.version.form_id}/draft`);
    await openVersion(out.id);
  } catch (err) {
    showAlert(err.message);
  }
}

async function uploadImage(path, file) {
  const body = new FormData();
  body.append("file", file);
  try {
    const a = await fbFetch("POST", `/form-versions/${fb.version.id}/assets`, body);
    fb.version.assets = [...(fb.version.assets || []).filter((x) => x.name !== a.name), a];
    const prev = getAt(fb.def, [...path, "media"]);
    setAt(fb.def, [...path, "media"], { type: "image", asset: a.name, alt: (prev && prev.alt) || "" });
    changed();
  } catch (err) {
    showAlert(err.message);
  }
}

async function deleteAsset(name) {
  if (!confirm(`Delete the image ${name} from this draft?`)) return;
  try {
    await fbFetch("DELETE", `/form-versions/${fb.version.id}/assets/${encodeURIComponent(name)}`);
    fb.version.assets = (fb.version.assets || []).filter((a) => a.name !== name);
    fbRender();
  } catch (err) {
    showAlert(err.message);
  }
}

// ---------------------------------------------------------------------------
// Preview: walks the draft as a patient would, with forms-eval.js
// ---------------------------------------------------------------------------

// previewPages lists the pages a patient sees for these answers, in order.
function previewPages(form, def, answers) {
  const res = WeHelpForms.evaluate(form, answers);
  const pages = [];
  for (const s of def.sections) {
    if (!res.visible[s.id]) continue;
    for (const p of s.pages) if (res.visible[p.id]) pages.push(p);
  }
  return { res, pages };
}

function renderPreview() {
  const el = document.getElementById("fb-preview");
  if (!el) return;
  let form;
  try {
    form = WeHelpForms.compile(fb.def);
  } catch (e) {
    el.innerHTML = `<h3>Preview</h3><p class="hint">Fix the problem marked in red to preview the form.</p>`;
    return;
  }
  const pv = fb.preview;
  const { res, pages } = previewPages(form, fb.def, pv.answers);
  if (pv.page > pages.length) pv.page = pages.length;
  let html = `<div class="fb-titlebar"><h3>Preview</h3><button class="btn btn-sm btn-outline" ${fbAct("previewRestart")}>Start again</button></div>`;
  if (pv.page >= pages.length) {
    html += `<p><strong>End of the form.</strong> ${pages.length} page${pages.length === 1 ? "" : "s"} shown.</p>`;
    if (pages.length) html += `<button class="btn btn-sm btn-outline" ${fbAct("previewMove", -1)}>Back</button>`;
  } else {
    const page = pages[pv.page];
    html += `<p class="hint">Page ${pv.page + 1} of ${pages.length}${page.title ? " · " + esc(page.title) : ""}</p>`;
    for (const it of page.items) {
      if (!res.visible[it.id]) continue;
      const problem = pv.problems.find((x) => x.item === it.id);
      html += `<div class="fb-q ${problem ? "fb-error" : ""}">${previewItem(it, pv.answers[it.id])}${problem ? `<div class="hint fb-problem">${esc(problem.message)}</div>` : ""}</div>`;
    }
    html += `<div class="inline-form">${pv.page > 0 ? `<button class="btn btn-sm btn-outline" ${fbAct("previewMove", -1)}>Back</button>` : ""}
      <button class="btn btn-sm" ${fbAct("previewMove", 1)}>Next</button></div>`;
  }
  const ids = Object.keys(res.scores);
  if (ids.length) {
    html += '<h4>Scores</h4><table class="fb-scores"><tbody>';
    for (const id of ids) {
      const s = res.scores[id];
      const sd = fb.def.scores.find((x) => x.id === id) || {};
      html += `<tr><td>${esc(sd.text || id)}</td><td>${s.value === null ? `<span class="hint">blank (${esc(s.reason)})</span>` : esc(s.value)}</td>
        <td>${s.band ? esc(((sd.bands || []).find((b) => b.id === s.band) || {}).text || s.band) : ""}</td></tr>`;
    }
    html += "</tbody></table>";
  }
  if (res.error) html += '<div class="alert alert-error">A score is too large to compute exactly.</div>';
  el.innerHTML = html;
}

function previewItem(it, value) {
  const q = `<div class="fb-qtext">${esc(it.text || "")}${it.required ? ' <span class="fb-req">*</span>' : ""}</div>`;
  const a = (extra) => `data-fbp="${attr(it.id)}" ${extra || ""}`;
  switch (it.type) {
    case "display": {
      const img = it.media && it.media.type === "image" ? `<img class="fb-pimg" src="${attr(assetURL(it.media.asset))}" alt="${attr(it.media.alt || "")}">` : "";
      return q + img;
    }
    case "choice":
      if (it.multiple) {
        const vals = Array.isArray(value) ? value : [];
        return q + it.options.map((o) => `<label class="fb-check"><input type="checkbox" ${a(`value="${attr(o.value)}"`)} ${vals.includes(o.value) ? "checked" : ""}> ${esc(o.text)}</label>`).join("");
      }
      if (it.control === "dropdown") {
        return q + `<select ${a()}><option value=""></option>${it.options.map((o) => `<option value="${attr(o.value)}" ${o.value === value ? "selected" : ""}>${esc(o.text)}</option>`).join("")}</select>`;
      }
      return q + it.options.map((o) => `<label class="fb-check"><input type="radio" name="fbp-${attr(it.id)}" ${a(`value="${attr(o.value)}"`)} ${o.value === value ? "checked" : ""}> ${esc(o.text)}</label>`).join("");
    case "boolean":
      return q + [["true", "Yes"], ["false", "No"]].map(([v, l]) =>
        `<label class="fb-check"><input type="radio" name="fbp-${attr(it.id)}" ${a(`value="${v}" data-fbp-type="boolean"`)} ${String(value) === v ? "checked" : ""}> ${l}</label>`).join("");
    case "integer": case "decimal":
      return q + `<input type="number" step="any" ${a(`data-fbp-type="number"`)} value="${attr(value ?? "")}"
        ${it.min !== undefined ? `min="${attr(it.min)}"` : ""} ${it.max !== undefined ? `max="${attr(it.max)}"` : ""}>${it.unit ? " " + esc(it.unit) : ""}`;
    case "date":
      return q + `<input type="date" ${a()} value="${attr(value ?? "")}">`;
    default:
      return q + (it.control === "long" ? `<textarea rows="3" ${a()}>${esc(value ?? "")}</textarea>` : `<input ${a()} value="${attr(value ?? "")}">`);
  }
}

function previewRestart() {
  fb.preview = { answers: {}, page: 0, problems: [] };
  renderPreview();
}

function previewMove(dir) {
  const pv = fb.preview;
  let form;
  try {
    form = WeHelpForms.compile(fb.def);
  } catch (_) {
    return;
  }
  pv.problems = [];
  if (dir > 0) {
    const { pages } = previewPages(form, fb.def, pv.answers);
    const page = pages[pv.page];
    if (page) {
      const onPage = new Set(page.items.map((it) => it.id));
      const check = WeHelpForms.checkAnswers(form, pv.answers, todayISO());
      pv.problems = check.errors.filter((e) => onPage.has(e.item));
      if (pv.problems.length) {
        renderPreview();
        return;
      }
    }
  }
  pv.page = Math.max(0, pv.page + dir);
  renderPreview();
}

function previewAnswer(el) {
  const id = el.dataset.fbp;
  const answers = fb.preview.answers;
  let v;
  if (el.type === "checkbox") {
    v = Array.from(document.querySelectorAll(`[data-fbp="${CSS.escape(id)}"]`)).filter((x) => x.checked).map((x) => x.value);
    if (!v.length) v = undefined;
  } else if (el.dataset.fbpType === "boolean") {
    v = el.value === "true";
  } else if (el.dataset.fbpType === "number") {
    v = el.value === "" ? undefined : Number(el.value);
  } else {
    v = el.value === "" ? undefined : el.value;
  }
  if (v === undefined) delete answers[id];
  else answers[id] = v;
  fb.preview.problems = fb.preview.problems.filter((p) => p.item !== id);
  renderPreview();
}

// ---------------------------------------------------------------------------
// Assignments (#196): on a patient, their forms and "Assign form"; the Due
// forms tab for the practice. Staff on the office LAN.
// ---------------------------------------------------------------------------

const OCC_BADGE = { upcoming: "scheduled", open: "pending", completed: "active", missed: "revoked", cancelled: "revoked" };

function fmtWhen(ts) {
  return ts ? new Date(ts).toLocaleString([], { dateStyle: "medium", timeStyle: "short" }) : "";
}

function scheduleText(s) {
  if (s.kind === "once") return `Once, due ${s.start_date}`;
  const unit = s.kind === "days" ? "day" : "week";
  let t = s.every === 1 ? `Every ${unit}` : `Every ${s.every} ${unit}s`;
  t += ` from ${s.start_date}`;
  if (s.count) t += `, ${s.count} times`;
  if (s.until) t += `, until ${s.until}`;
  return t;
}

async function showPatientForms(patientId, name) {
  const el = document.getElementById("pairing");
  if (!el) return;
  el.innerHTML = '<div class="card"><div class="loading">Loading…</div></div>';
  let data, library, responses;
  try {
    [data, library, responses] = await Promise.all([api("GET", `/assignments?patient_id=${encodeURIComponent(patientId)}`), api("GET", "/forms"),
      api("GET", `/responses?patient_id=${encodeURIComponent(patientId)}`)]);
  } catch (err) {
    el.innerHTML = `<div class="card"><div class="alert alert-error">${esc(err.message)}</div></div>`;
    return;
  }
  const assignable = (library.forms || []).filter((f) => f.published && !f.archived_at);
  const today = todayISO();
  let html = `<div class="card"><div class="fb-titlebar"><h2>Forms for ${esc(name)}</h2>
    <button class="btn btn-sm btn-outline" ${fbAct("closePatientForms")}>Close</button></div>`;
  if (!assignable.length) {
    html += '<p class="hint">No published forms yet. An admin builds and publishes them in the Forms tab.</p>';
  } else {
    html += `<h3>Assign a form</h3><div class="fb-row" id="assign-form">
      <label class="fb-field"><span>Form</span><select id="as-form">${assignable.map((f) => `<option value="${attr(f.id)}">${esc(f.name)}</option>`).join("")}</select></label>
      <label class="fb-field"><span>How often</span><select id="as-kind"><option value="once">Once</option><option value="days">Every N days</option><option value="weeks">Every N weeks</option></select></label>
      <label class="fb-field"><span>Every</span><input id="as-every" type="number" min="1" max="365" value="1" class="fb-num"></label>
      <label class="fb-field"><span>First due</span><input id="as-start" type="date" value="${attr(today)}"></label>
      <label class="fb-field"><span>Times (repeating)</span><input id="as-count" type="number" min="1" max="366" value="4" class="fb-num"></label>
      <label class="fb-field"><span>Opens, days before due</span><input id="as-open" type="number" min="0" max="365" value="0" class="fb-num"></label>
      <label class="fb-field"><span>Closes, days after due</span><input id="as-close" type="number" min="0" max="365" value="3" class="fb-num"></label>
      <label class="fb-field"><span>Where</span><select id="as-channel"><option value="app">In the app</option><option value="kiosk">On the office kiosk</option></select></label>
      <button class="btn" ${fbAct("assignForm", patientId, name)}>Assign</button></div>
      <p class="hint">Due, opening and closing times are 09:00 practice time. The ${esc(word("patient"))} gets a notification when it opens and a day before it closes.</p>`;
  }
  const sent = responses.responses || [];
  html += `<h3>Answers</h3>`;
  if (!sent.length) {
    html += '<p class="empty">No forms sent back yet.</p>';
  } else {
    html += "<table><thead><tr><th>Form</th><th>Sent</th><th>Scores</th><th></th></tr></thead><tbody>";
    for (const r of sent) {
      const scores = Object.entries(r.scores || {}).map(([id, v]) => `${esc(id)}: ${v.value === null ? "—" : esc(v.value)}${v.band ? " (" + esc(v.band) + ")" : ""}`).join(", ");
      const flagged = (r.flags || []).length ? ` <span class="badge badge-revoked fb-flag">flag: ${esc(r.flags.map((x) => x.id).join(", "))}</span>` : "";
      html += `<tr${flagged ? ' class="fb-flagged"' : ""}><td>${esc(r.form_name)} v${r.version}${r.new ? ' <span class="badge badge-pending">new</span>' : ""}${flagged}</td>
        <td>${esc(fmtWhen(r.submitted_at))}</td><td>${scores}</td>
        <td><button class="btn btn-sm" ${fbAct("showFormResponse", r.id, patientId, name)}>Open</button></td></tr>`;
    }
    html += "</tbody></table>";
  }
  html += '<div id="fb-response"></div><h3>Assigned</h3>';
  const list = data.assignments || [];
  if (!list.length) {
    html += `<p class="empty">No forms assigned yet.</p>`;
  }
  for (const a of list) {
    html += `<div class="fb-item"><div class="fb-titlebar"><div><strong>${esc(a.form_name)}</strong> · ${esc(scheduleText(a.schedule))}
      · ${a.channel === "kiosk" ? "kiosk" : "app"}${a.ended_at ? ' <span class="badge badge-revoked">ended</span>' : ""}
      <div class="hint">Assigned by ${esc(a.assigned_by_name)} on ${esc(fmtWhen(a.created_at))}</div></div>
      ${a.ended_at ? "" : `<button class="btn btn-sm btn-danger" ${fbAct("endAssignment", a.id, patientId, name)}>End</button>`}</div>`;
    if (a.occurrences.length) {
      html += "<table><thead><tr><th>Due</th><th>Window</th><th>Status</th></tr></thead><tbody>";
      for (const o of a.occurrences) {
        html += `<tr><td>${esc(fmtWhen(o.due_at))}</td><td>${esc(fmtWhen(o.opens_at))} – ${esc(fmtWhen(o.closes_at))}</td>
          <td><span class="badge badge-${OCC_BADGE[o.state] || "pending"}">${esc(o.state)}</span></td></tr>`;
      }
      html += "</tbody></table>";
    } else {
      html += '<p class="hint">No occurrences yet: the first is created two weeks before it opens.</p>';
    }
    html += "</div>";
  }
  el.innerHTML = html + "</div>";
}

function closePatientForms() {
  const el = document.getElementById("pairing");
  if (el) el.innerHTML = "";
}

async function assignForm(patientId, name) {
  const v = (id) => document.getElementById(id).value;
  const kind = v("as-kind");
  const schedule = { kind, start_date: v("as-start") };
  if (kind !== "once") {
    schedule.every = Number(v("as-every"));
    schedule.count = Number(v("as-count"));
  }
  try {
    await api("POST", "/assignments", {
      patient_id: patientId, form_id: v("as-form"), schedule,
      open_before_days: Number(v("as-open")), close_after_days: Number(v("as-close")), channel: v("as-channel"),
    });
    showAlert("Form assigned", "success");
    showPatientForms(patientId, name);
  } catch (err) {
    showAlert(err.message);
  }
}

async function endAssignment(id, patientId, name) {
  if (!confirm("End this assignment? Forms not yet open are cancelled; an open one stays open.")) return;
  try {
    await api("POST", `/assignments/${id}/end`);
    showPatientForms(patientId, name);
  } catch (err) {
    showAlert(err.message);
  }
}

// showFormResponse shows a submitted response: each answer by its
// question, the scores with their bands, and any flags (#197). Opening it
// clears the new badge.
async function showFormResponse(id, patientId, name) {
  const el = document.getElementById("fb-response");
  if (!el) return;
  let r;
  try {
    r = await api("GET", `/responses/${encodeURIComponent(id)}`);
  } catch (err) {
    showAlert(err.message);
    return;
  }
  let html = `<div class="fb-item"><div class="fb-titlebar"><strong>${esc(r.title || r.form_name)} · v${r.version}</strong>
    <span class="hint">Sent ${esc(fmtWhen(r.submitted_at))} · ${r.channel === "kiosk" ? "kiosk" : "app"}</span></div>`;
  // Flags (#199) are staff-side: shown here in red, never to the patient.
  if ((r.flags || []).length) html += `<div class="alert alert-error fb-flag"><strong>Flagged</strong><ul>${r.flags.map((x) => `<li>${esc(x.text || x.id)}</li>`).join("")}</ul></div>`;
  if ((r.scores || []).length) {
    html += '<table class="fb-scores"><tbody>';
    for (const sc of r.scores) {
      html += `<tr><td>${esc(sc.text || sc.id)}</td><td>${sc.value === null ? `<span class="hint">blank (${esc(sc.reason || "")})</span>` : esc(sc.value)}</td>
        <td>${esc(sc.band_text || sc.band || "")}</td></tr>`;
    }
    html += "</tbody></table>";
  }
  html += "<table><thead><tr><th>Question</th><th>Answer</th></tr></thead><tbody>";
  for (const it of r.items || []) html += `<tr><td>${esc(it.text || it.item_id)}</td><td>${esc(it.display)}</td></tr>`;
  html += "</tbody></table></div>";
  el.innerHTML = html;
  el.scrollIntoView({ block: "nearest" });
}

// loadDueForms is the Due forms tab: due today, overdue and missed this week.
async function loadDueForms(container) {
  const data = await api("GET", "/form-occurrences/due");
  const section = (title, list, empty) => {
    let html = `<div class="card"><h2>${esc(title)} <span class="badge badge-pending">${list.length}</span></h2>`;
    if (!list.length) return html + `<p class="empty">${esc(empty)}</p></div>`;
    html += `<table><thead><tr><th>${esc(Word("patient"))}</th><th>Form</th><th>Due</th><th>Closes</th><th>${esc(Word("providers"))}</th></tr></thead><tbody>`;
    for (const d of list) {
      html += `<tr><td>${esc(d.patient_name)}</td><td>${esc(d.form_name)}</td><td>${esc(fmtWhen(d.due_at))}</td>
        <td>${esc(fmtWhen(d.closes_at))}</td><td>${esc((d.providers || []).map((p) => p.name).join(", ") || "—")}</td></tr>`;
    }
    return html + "</tbody></table></div>";
  };
  const flagged = data.flagged_this_week || [];
  let flaggedHTML = `<div class="card${flagged.length ? " fb-flagged-card" : ""}"><h2>Flagged this week <span class="badge badge-${flagged.length ? "revoked" : "active"}">${flagged.length}</span></h2>`;
  if (!flagged.length) {
    flaggedHTML += '<p class="empty">No flagged answers this week.</p></div>';
  } else {
    flaggedHTML += `<table><thead><tr><th>${esc(Word("patient"))}</th><th>Form</th><th>Sent</th><th>Flag</th><th>${esc(Word("providers"))}</th></tr></thead><tbody>`;
    for (const d of flagged) {
      flaggedHTML += `<tr class="fb-flagged"><td>${esc(d.patient_name)}</td><td>${esc(d.form_name)}</td><td>${esc(fmtWhen(d.submitted_at))}</td>
        <td class="fb-flag">${esc((d.flags || []).map((x) => x.text || x.id).join("; "))}</td><td>${esc((d.providers || []).map((p) => p.name).join(", ") || "—")}</td></tr>`;
    }
    flaggedHTML += "</tbody></table></div>";
  }
  container.innerHTML = flaggedHTML + section("Due today", data.due_today || [], "Nothing due today.") +
    section("Overdue", data.overdue || [], "Nothing overdue.") +
    section("Missed this week", data.missed_this_week || [], "Nothing missed this week.");
}

// ---------------------------------------------------------------------------
// Starting points (#258): copy a form or a built-in, duplicate a page or a
// question.
// ---------------------------------------------------------------------------

// copyForm makes a new custom form from a form (a version of it, or its
// latest published one) and opens its draft. A built-in becomes an
// editable copy that keeps its attribution.
async function copyForm(formId, versionId) {
  if (fb.view === "edit" && !fbCanLeave()) return;
  try {
    const out = await fbFetch("POST", `/forms/${formId}/copy`, versionId ? { version_id: versionId } : {});
    showAlert(`Created "${out.name}"`, "success");
    await openVersion(out.version_id);
  } catch (err) {
    showAlert(err.message);
  }
}

// freshId returns an unused id based on base (taken holds ids already
// handed out in this copy).
function freshId(def, base, taken) {
  const ids = allIds(def);
  const root = String(base).replace(/_copy\d*$/, "").slice(0, 50) + "_copy";
  let id = root;
  for (let n = 2; ids.has(id) || taken.has(id); n++) id = root + n;
  taken.add(id);
  return id;
}

// repointRefs rewrites item references inside a rule tree by map.
function repointRefs(node, map) {
  if (Array.isArray(node)) return node.map((x) => repointRefs(x, map));
  if (!node || typeof node !== "object") return node;
  const out = {};
  for (const [k, v] of Object.entries(node)) {
    if ((k === "answer" || k === "scoreOf" || k === "answered") && typeof v === "string") out[k] = map[v] || v;
    else if (k === "count" && Array.isArray(v)) out[k] = v.map((x) => (typeof x === "string" && map[x]) || x);
    else out[k] = repointRefs(v, map);
  }
  return out;
}

// duplicatePage copies a page right after itself with fresh ids. Rules
// inside the copy that use questions of the copied page point at the
// copies; rules anywhere else are unchanged.
function duplicatePage(si, pi) {
  const pages = fb.def.sections[si].pages;
  const copy = JSON.parse(JSON.stringify(pages[pi]));
  const taken = new Set();
  const map = {};
  copy.id = freshId(fb.def, copy.id, taken);
  for (const it of copy.items) map[it.id] = freshId(fb.def, it.id, taken);
  for (const it of copy.items) {
    it.id = map[it.id];
    if (it.enableWhen) it.enableWhen = repointRefs(it.enableWhen, map);
  }
  if (copy.enableWhen) copy.enableWhen = repointRefs(copy.enableWhen, map);
  pages.splice(pi + 1, 0, copy);
  changed();
}

// duplicateItem copies a question right after itself with a fresh id; its
// rule keeps pointing at the same earlier questions.
function duplicateItem(si, pi, ii) {
  const items = fb.def.sections[si].pages[pi].items;
  const copy = JSON.parse(JSON.stringify(items[ii]));
  copy.id = freshId(fb.def, copy.id, new Set());
  items.splice(ii + 1, 0, copy);
  changed();
}

// ---------------------------------------------------------------------------
// Answer lists and the grid editor (#259). Builder only: a grid is a run of
// ordinary single-choice questions with identical options, so the format,
// scoring, the server and the app don't change.
// ---------------------------------------------------------------------------

const ANSWER_LISTS = {
  freq4: { label: "Never / Sometimes / Often / Always", options: [["never", "Never", 0], ["sometimes", "Sometimes", 1], ["often", "Often", 2], ["always", "Always", 3]] },
  yesno: { label: "Yes / No / Not sure", options: [["yes", "Yes"], ["no", "No"], ["not_sure", "Not sure"]] },
  severity: { label: "None / Mild / Moderate / Severe", options: [["none", "None", 0], ["mild", "Mild", 1], ["moderate", "Moderate", 2], ["severe", "Severe", 3]] },
  zero10: { label: "0 to 10", options: Array.from({ length: 11 }, (_, i) => [String(i), String(i), i]) },
};

function listOptions(key) {
  const l = ANSWER_LISTS[key];
  return l.options.map(([value, text, score]) => (score === undefined ? { value, text } : { value, text, score }));
}

// NEW_ITEMS is the "Add item" menu: the item types, choice questions with an
// answer list, and a grid.
const NEW_ITEMS = [
  ...ITEM_TYPES,
  ...Object.entries(ANSWER_LISTS).map(([k, l]) => [`choice:${k}`, `Choice: ${l.label}`]),
  ["grid:freq4", "Grid (rows share one answer list)"],
];

function sameOptions(a, b) {
  return JSON.stringify(a.options || []) === JSON.stringify(b.options || []);
}

function gridable(it) {
  return it && it.type === "choice" && !it.multiple && !it.enableWhen && (it.options || []).length > 0;
}

// gridRuns finds the grids on a page: runs of 2 or more consecutive
// single-choice questions with identical options, required and control,
// and no rule. Each is {start, end} (end exclusive).
function gridRuns(items) {
  const runs = [];
  let i = 0;
  while (i < items.length) {
    let j = i + 1;
    if (gridable(items[i])) {
      while (j < items.length && gridable(items[j]) && sameOptions(items[i], items[j]) &&
        !!items[j].required === !!items[i].required && (items[j].control || "") === (items[i].control || "")) j++;
    }
    if (j - i >= 2) runs.push({ start: i, end: j });
    i = Math.max(j, i + 1);
  }
  return runs;
}

function rowsOf(si, pi, start, end) {
  return fb.def.sections[si].pages[pi].items.slice(start, end);
}

// Item-level answer list actions.

function applyAnswerList(path, key) {
  if (!ANSWER_LISTS[key]) return;
  getAt(fb.def, path).options = listOptions(key);
  changed();
}

function copyOptionsFrom(path, fromId) {
  const from = itemById(fb.def, fromId);
  if (!from || !from.options) return;
  getAt(fb.def, path).options = JSON.parse(JSON.stringify(from.options));
  changed();
}

// reverseOptions flips the scores: the first option gets the last score.
function reverseOptions(options) {
  const scores = options.map((o) => o.score);
  options.forEach((o, i) => {
    const s = scores[scores.length - 1 - i];
    if (s === undefined) delete o.score;
    else o.score = s;
  });
}

function reverseScores(path) {
  reverseOptions(getAt(fb.def, path).options || []);
  changed();
}

// Grid actions: each takes the page and the run's [start, end).

function gridEach(si, pi, start, end, fn) {
  rowsOf(si, pi, start, end).forEach(fn);
  changed();
}

function gridApplyList(si, pi, start, end, key) {
  if (!ANSWER_LISTS[key]) return;
  gridEach(si, pi, start, end, (it) => { it.options = listOptions(key); });
}

function gridCopyFrom(si, pi, start, end, fromId) {
  const from = itemById(fb.def, fromId);
  if (!from || !from.options) return;
  gridEach(si, pi, start, end, (it) => { it.options = JSON.parse(JSON.stringify(from.options)); });
}

function gridReverse(si, pi, start, end) {
  gridEach(si, pi, start, end, (it) => reverseOptions(it.options));
}

function gridSetOption(si, pi, start, end, oi, field, value) {
  gridEach(si, pi, start, end, (it) => {
    const o = it.options[oi];
    if (!o) return;
    if (value === undefined) delete o[field];
    else o[field] = value;
  });
}

function gridAddOption(si, pi, start, end) {
  const first = rowsOf(si, pi, start, end)[0];
  let n = first.options.length + 1;
  while (first.options.some((o) => o.value === String(n))) n++;
  gridEach(si, pi, start, end, (it) => { it.options.push({ value: String(n), text: "Option " + n }); });
}

function gridRemoveOption(si, pi, start, end, oi) {
  gridEach(si, pi, start, end, (it) => { it.options.splice(oi, 1); });
}

function gridShared(si, pi, start, end, field, value) {
  gridEach(si, pi, start, end, (it) => {
    if (value === undefined || value === "" || value === false) delete it[field];
    else it[field] = value;
  });
}

function gridRowCode(si, pi, ii, code) {
  const it = fb.def.sections[si].pages[pi].items[ii];
  if (code) it.code = { system: "http://loinc.org", code };
  else delete it.code;
  changed();
}

function newRow(template, text) {
  const row = JSON.parse(JSON.stringify(template));
  row.id = freshId(fb.def, "row", new Set());
  row.text = text;
  delete row.code;
  return row;
}

function gridAddRow(si, pi, start, end) {
  const items = fb.def.sections[si].pages[pi].items;
  items.splice(end, 0, newRow(items[end - 1], "Row " + (end - start + 1)));
  changed();
}

// gridPaste adds one row per non-empty line of the paste box.
function gridPaste(si, pi, start, end) {
  const box = document.getElementById(`fb-gridpaste-${si}-${pi}-${start}`);
  const lines = (box ? box.value : "").split(/\r?\n/).map((l) => l.trim()).filter(Boolean);
  if (!lines.length) return;
  const items = fb.def.sections[si].pages[pi].items;
  const rows = [];
  for (const line of lines) {
    const row = newRow(items[end - 1], line);
    items.splice(end + rows.length, 0, row); // reserve the id before the next
    rows.push(row);
  }
  changed();
}

// gridSum adds a score that is the sum of the grid rows' option scores.
function gridSum(si, pi, start, end) {
  const rows = rowsOf(si, pi, start, end);
  if (!rows.every(scorable)) return;
  const scores = fb.def.scores || (fb.def.scores = []);
  scores.push({ id: newId(fb.def, "grid_total"), text: "Total", expr: { sum: rows.map((it) => ({ scoreOf: it.id })) } });
  changed();
}

function addGrid(si, pi, key) {
  const items = fb.def.sections[si].pages[pi].items;
  const taken = new Set();
  for (let i = 1; i <= 3; i++) {
    items.push({ id: freshId(fb.def, "row", taken), type: "choice", text: "Row " + i, control: "buttons", options: listOptions(key) });
  }
  changed();
}

function answerListPicker(attrs) {
  return `<select ${attrs}><option value="">Use an answer list…</option>
    ${Object.entries(ANSWER_LISTS).map(([k, l]) => `<option value="${attr(k)}">${esc(l.label)}</option>`).join("")}</select>`;
}

function copyFromPicker(attrs, exclude) {
  const others = fbItems(fb.def).map((x) => x.item).filter((it) => it.type === "choice" && !exclude.includes(it.id));
  if (!others.length) return "";
  return `<select ${attrs}><option value="">Copy options from…</option>
    ${others.map((it) => `<option value="${attr(it.id)}">${esc(it.id)}: ${esc((it.text || "").slice(0, 40))}</option>`).join("")}</select>`;
}

function renderGrid(si, pi, run, ro) {
  const rows = rowsOf(si, pi, run.start, run.end);
  const first = rows[0];
  const key = [si, pi, run.start, run.end];
  const k = attr(JSON.stringify(key));
  const pathOf = (ii) => ["sections", si, "pages", pi, "items", ii];
  let html = `<div class="fb-item fb-grid-block" data-fb-anchor="${attr(pathString(pathOf(run.start)))}">
    <div class="fb-titlebar"><strong>Grid: ${rows.length} rows share these answers</strong></div>
    <div class="fb-row">
      <label class="fb-check"><input type="checkbox" data-fb-gridshared="${k}" data-fb-field="required" ${first.required ? "checked" : ""} ${ro ? "disabled" : ""}> Every row required</label>
      <label class="fb-field"><span>Shown as</span><select data-fb-gridshared="${k}" data-fb-field="control" ${ro ? "disabled" : ""}>
        ${CONTROLS.choice.map(([v, l]) => `<option value="${attr(v)}" ${(first.control || "") === v ? "selected" : ""}>${esc(l)}</option>`).join("")}</select></label>
    </div>
    <table class="fb-options"><thead><tr><th>Value</th><th>Answer</th><th>Score</th><th></th></tr></thead><tbody>`;
  first.options.forEach((o, oi) => {
    const a = (field) => `data-fb-gridopt="${attr(JSON.stringify([...key, oi, field]))}" ${ro ? "disabled" : ""}`;
    html += `<tr><td><input ${a("value")} value="${attr(o.value)}"></td><td><input ${a("text")} value="${attr(o.text)}"></td>
      <td><input type="number" step="any" class="fb-num" ${a("score")} value="${attr(o.score ?? "")}"></td>
      <td>${ro || first.options.length < 2 ? "" : `<button class="btn btn-sm btn-outline" ${fbAct("gridRemoveOption", ...key, oi)} title="Remove this answer from every row">✕</button>`}</td></tr>`;
  });
  html += "</tbody></table>";
  if (!ro) {
    html += `<div class="inline-form">
      <button class="btn btn-sm btn-outline" ${fbAct("gridAddOption", ...key)}>Add answer</button>
      ${answerListPicker(`data-fb-gridlist="${k}"`)}
      ${copyFromPicker(`data-fb-gridcopy="${k}"`, rows.map((r) => r.id))}
      ${first.options.every((o) => typeof o.score === "number") ? `<button class="btn btn-sm btn-outline" ${fbAct("gridReverse", ...key)}>Reverse scores</button>
      <button class="btn btn-sm btn-outline" ${fbAct("gridSum", ...key)}>Score = sum of these rows</button>` : ""}</div>`;
  }
  html += '<table class="fb-options fb-grid-rows"><thead><tr><th>Row (question)</th><th>Id</th><th>Code (LOINC, optional)</th><th></th></tr></thead><tbody>';
  rows.forEach((it, r) => {
    const ii = run.start + r;
    html += `<tr data-fb-anchor="${attr(pathString(pathOf(ii)))}"><td><input ${fbPathAttr([...pathOf(ii), "text"], "req")} value="${attr(it.text)}" ${ro ? "disabled" : ""}></td>
      <td><input ${fbPathAttr([...pathOf(ii), "id"], "req")} value="${attr(it.id)}" ${ro ? "disabled" : ""} class="fb-gridid"></td>
      <td><input data-fb-gridcode="${attr(JSON.stringify([si, pi, ii]))}" value="${attr(it.code ? it.code.code : "")}" ${ro ? "disabled" : ""} class="fb-num"></td>
      <td>${moveButtons("item", ["sections", si, "pages", pi, "items"], ii, fb.def.sections[si].pages[pi].items.length, ro)}</td></tr>`;
  });
  html += "</tbody></table>";
  if (!ro) {
    html += `<div class="inline-form"><button class="btn btn-sm btn-outline" ${fbAct("gridAddRow", ...key)}>Add row</button></div>
      <div class="inline-form"><textarea id="fb-gridpaste-${si}-${pi}-${run.start}" rows="2" placeholder="Paste rows, one per line"></textarea>
      <button class="btn btn-sm btn-outline" ${fbAct("gridPaste", ...key)}>Add pasted rows</button></div>`;
  }
  return html + "</div>";
}

// renderItems draws a page's items, with grids where the rows share answers.
function renderItems(p, si, pi, g, ro) {
  const items = p.items || [];
  const runs = gridRuns(items);
  let html = "";
  for (let ii = 0; ii < items.length;) {
    const run = runs.find((r) => r.start === ii);
    if (run) {
      html += renderGrid(si, pi, run, ro);
      ii = run.end;
    } else {
      html += renderItem(items[ii], si, pi, ii, g, ro);
      ii++;
    }
  }
  return html;
}

// ---------------------------------------------------------------------------
// Events: one allowlist, no window lookups
// ---------------------------------------------------------------------------

const FB_ACTIONS = {
  addBand, addBranch, addCond, addFormItem, addOption, addPage, addScore, addSection, archiveForm, backToList,
  clearRule, createForm, deleteAsset, moveNode, newVersion, openVersion, previewMove, previewRestart,
  publishDraft, removeBranch, removeCond, removeMedia, removeNode, saveDraft, toggleNewForm,
  // Undo and redo (#260)
  redoEdit, undoEdit,
  // Signing in again after the session ends, without losing the draft
  closeSignIn, openSignIn, submitSignIn,
  // Starting points (#258)
  copyForm, duplicateItem, duplicatePage,
  // Answer lists and grids (#259)
  gridAddOption, gridAddRow, gridPaste, gridRemoveOption, gridReverse, gridSum, reverseScores,
  // Assignments (#196)
  assignForm, closePatientForms, endAssignment, showPatientForms,
  // Responses (#197)
  showFormResponse,
};

function fieldValue(el) {
  const kind = el.dataset.fbKind;
  if (kind === "bool") return el.checked ? true : undefined;
  const raw = el.value;
  if (kind === "req") return raw;
  if (raw === "") return undefined;
  if (kind === "num" || kind === "int") {
    const n = Number(raw);
    return Number.isFinite(n) ? n : raw;
  }
  return raw;
}

if (typeof document !== "undefined") {
  // Undo and redo keys, outside text fields (those keep their own undo).
  document.addEventListener("keydown", (e) => {
    if (fb.view !== "edit" || !(e.metaKey || e.ctrlKey)) return;
    const k = (e.key || "").toLowerCase();
    if (k !== "z" && k !== "y") return;
    const t = e.target;
    if (t && (t.tagName === "INPUT" || t.tagName === "TEXTAREA" || t.isContentEditable)) return;
    e.preventDefault();
    if (k === "y" || e.shiftKey) redoEdit();
    else undoEdit();
  });
  // Closing or reloading the tab with unsaved changes asks first.
  window.addEventListener("beforeunload", (e) => {
    if (fb.view === "edit" && fb.version && (fb.dirty || (fb.save && fb.save.inFlight))) {
      e.preventDefault();
      e.returnValue = "";
    }
  });

  document.addEventListener("click", (e) => {
    const el = e.target.closest("[data-fb]");
    if (!el || !Object.prototype.hasOwnProperty.call(FB_ACTIONS, el.dataset.fb)) return;
    let args = [];
    try {
      args = JSON.parse(el.dataset.fbArgs || "[]");
    } catch (_) {
      return;
    }
    e.preventDefault();
    FB_ACTIONS[el.dataset.fb](...args);
  });

  document.addEventListener("change", (e) => {
    const el = e.target;
    if (!el || !el.dataset) return;
    try {
      if (el.dataset.fbPath) {
        setAt(fb.def, JSON.parse(el.dataset.fbPath), fieldValue(el));
        changed();
      } else if (el.dataset.fbRule) {
        const value = el.multiple ? Array.from(el.selectedOptions).map((o) => o.value)
          : el.dataset.fbCf === "value" && el.type === "number" ? Number(el.value)
          : el.dataset.fbBool !== undefined ? el.value === "true"
          : el.value;
        editCond(JSON.parse(el.dataset.fbRule), Number(el.dataset.fbCi || 0), el.dataset.fbCf, value);
      } else if (el.dataset.fbDefault !== undefined) {
        setDefaultNext(Number(el.dataset.fbDefault), el.value);
      } else if (el.dataset.fbScoreitem) {
        const [i, id] = JSON.parse(el.dataset.fbScoreitem);
        setScoreItem(i, id, el.checked);
      } else if (el.dataset.fbMissing !== undefined) {
        setMissing(Number(el.dataset.fbMissing), el.value);
      } else if (el.dataset.fbUpload && el.files && el.files[0]) {
        uploadImage(JSON.parse(el.dataset.fbUpload), el.files[0]);
      } else if (el.dataset.fbPickasset && el.value) {
        const path = JSON.parse(el.dataset.fbPickasset);
        setAt(fb.def, [...path, "media"], { type: "image", asset: el.value, alt: "" });
        changed();
      } else if (el.dataset.fbImport !== undefined && el.files && el.files[0]) {
        importForm(el.files[0]);
      } else if (el.dataset.fbVersion !== undefined) {
        if (!fbCanLeave()) {
          el.value = fb.version.id;
          return;
        }
        openVersion(el.value);
      } else if (el.dataset.fbGridopt) { // #259: a shared grid answer
        const [si, pi, start, end, oi, field] = JSON.parse(el.dataset.fbGridopt);
        const v = field === "score" ? (el.value === "" ? undefined : Number(el.value)) : el.value;
        gridSetOption(si, pi, start, end, oi, field, v);
      } else if (el.dataset.fbGridshared) {
        const [si, pi, start, end] = JSON.parse(el.dataset.fbGridshared);
        gridShared(si, pi, start, end, el.dataset.fbField, el.type === "checkbox" ? el.checked : el.value);
      } else if (el.dataset.fbGridlist && el.value) {
        gridApplyList(...JSON.parse(el.dataset.fbGridlist), el.value);
      } else if (el.dataset.fbGridcopy && el.value) {
        gridCopyFrom(...JSON.parse(el.dataset.fbGridcopy), el.value);
      } else if (el.dataset.fbGridcode) {
        gridRowCode(...JSON.parse(el.dataset.fbGridcode), el.value.trim());
      } else if (el.dataset.fbItemlist && el.value) {
        applyAnswerList(JSON.parse(el.dataset.fbItemlist), el.value);
      } else if (el.dataset.fbItemcopy && el.value) {
        copyOptionsFrom(JSON.parse(el.dataset.fbItemcopy), el.value);
      } else if (el.dataset.fbp) {
        previewAnswer(el);
      }
    } catch (err) {
      showAlert(err.message || String(err));
    }
  });
}
