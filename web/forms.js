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
      <button class="btn" ${fbAct("createForm")}>Create</button></div>`;
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
      if (f.kind === "custom" && !f.archived_at) actions.push(`<button class="btn btn-sm btn-danger" ${fbAct("archiveForm", f.id, f.name)}>Archive</button>`);
      html += `<tr><td>${esc(f.name)}${f.archived_at ? ' <span class="badge badge-revoked">archived</span>' : ""}</td>
        <td>${f.kind === "instrument" ? "Built-in" : "Custom"}</td><td>${latest}</td>
        <td>${f.published ? "v" + f.published : "—"}</td><td>${actions.join(" ")}</td></tr>`;
    }
    html += "</tbody></table>";
  }
  return html + "</div>";
}

function toggleNewForm() {
  fb.newForm = !fb.newForm;
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
    const out = await fbFetch("POST", "/forms", { name });
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
  if (fb.dirty && !confirm("Leave without saving your changes?")) return;
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
  if (v.kind === "instrument") head += '<p class="hint">Built-in instruments are read-only; you can preview them.</p>';
  else if (v.status === "published") head += '<p class="hint">Published versions can\'t be changed. Start a new version to edit.</p>';
  head += `</div><div class="inline-form">
    <button class="btn btn-outline" ${fbAct("backToList")}>Back to forms</button>`;
  if (fb.form && fb.form.versions.length > 1) {
    head += `<select data-fb-version>${fb.form.versions.map((x) => `<option value="${attr(x.id)}" ${x.id === v.id ? "selected" : ""}>v${x.version} ${esc(x.status)}</option>`).join("")}</select>`;
  }
  if (!ro) {
    head += `<button class="btn" ${fbAct("saveDraft")}>${fb.dirty ? "Save" : "Saved"}</button>
      <button class="btn" ${fbAct("publishDraft")}>Publish</button>`;
  }
  if (v.status === "published" && v.kind !== "instrument" && !hasDraft && !(fb.form && fb.form.form.archived_at)) {
    head += `<button class="btn" ${fbAct("newVersion")}>New version</button>`;
  }
  head += `<a class="btn btn-outline" href="/api/v1/form-versions/${attr(v.id)}/export" download>Export JSON</a></div></div>`;

  let err = "";
  if (fb.error) err = `<div class="alert alert-error fb-errmsg">${esc(fb.error.message)}</div>`;

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
    <div class="fb-titlebar"><h4>Page ${pi + 1}</h4>${moveButtons("page", ["sections", si, "pages"], pi, n, ro)}</div>
    <div class="fb-row">${field("Id", [...path, "id"], p.id, "req", ro)}${field("Title", [...path, "title"], p.title, "str", ro)}</div>
    ${renderRule("Show this page only when", [...path, "enableWhen"], p.enableWhen, { kind: "page", page: g }, ro, false)}`;
  (p.items || []).forEach((it, ii) => { html += renderItem(it, si, pi, ii, g, ro); });
  if (!ro) {
    html += `<div class="inline-form fb-add"><select data-fb-newtype="${attr(JSON.stringify([si, pi]))}">
      ${ITEM_TYPES.map(([t, l]) => `<option value="${t}">${esc(l)}</option>`).join("")}</select>
      <button class="btn btn-sm btn-outline" ${fbAct("addItem", si, pi)}>Add item</button></div>`;
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
    <div class="fb-titlebar"><strong>${esc(typeLabel)}</strong>${moveButtons("item", ["sections", si, "pages", pi, "items"], ii, n, ro)}</div>
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
      if (!ro) html += `<button class="btn btn-sm btn-outline" ${fbAct("addOption", path)}>Add option</button>`;
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

function changed() {
  fb.dirty = true;
  fb.error = localError();
  fbRender();
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

function addItem(si, pi) {
  const sel = document.querySelector(`[data-fb-newtype="${CSS.escape(JSON.stringify([si, pi]))}"]`);
  const type = sel ? sel.value : "choice";
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
  try {
    await fbFetch("PUT", `/form-versions/${fb.version.id}`, fb.def);
    fb.dirty = false;
    fb.error = null;
    fbRender();
    showAlert("Draft saved", "success");
    return true;
  } catch (err) {
    fbFail(err);
    return false;
  }
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
      html += `<tr><td>${esc(r.form_name)} v${r.version}${r.new ? ' <span class="badge badge-pending">new</span>' : ""}</td>
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
  if ((r.flags || []).length) html += `<div class="alert alert-error">Flagged: ${esc(r.flags.map((x) => x.text || x.id || x).join(", "))}</div>`;
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
  container.innerHTML = section("Due today", data.due_today || [], "Nothing due today.") +
    section("Overdue", data.overdue || [], "Nothing overdue.") +
    section("Missed this week", data.missed_this_week || [], "Nothing missed this week.");
}

// ---------------------------------------------------------------------------
// Events: one allowlist, no window lookups
// ---------------------------------------------------------------------------

const FB_ACTIONS = {
  addBand, addBranch, addCond, addItem, addOption, addPage, addScore, addSection, archiveForm, backToList,
  clearRule, createForm, deleteAsset, moveNode, newVersion, openVersion, previewMove, previewRestart,
  publishDraft, removeBranch, removeCond, removeMedia, removeNode, saveDraft, toggleNewForm,
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
        if (fb.dirty && !confirm("Leave without saving your changes?")) {
          el.value = fb.version.id;
          return;
        }
        openVersion(el.value);
      } else if (el.dataset.fbp) {
        previewAnswer(el);
      }
    } catch (err) {
      showAlert(err.message || String(err));
    }
  });
}
