// Staff web tests (#195) for the Forms tab and the builder. They load the
// real scripts (staff.js, forms-eval.js, forms.js, app.js) into one Node vm
// context with a minimal fake DOM, the way index.html loads them, then call
// the page's own functions. This harness is for tests only; nothing in the
// shipped page uses vm or eval.
//
//   node --test web/*.test.js     (or: make test-js; CI runs it in the go job)
'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const fixture = (rel) => JSON.parse(fs.readFileSync(path.join(__dirname, '..', 'testdata', 'forms', rel), 'utf8'));

function fakeElement() {
  return {
    innerHTML: '',
    value: '',
    dataset: {},
    classList: { add() {}, remove() {} },
    querySelectorAll() { return []; },
    querySelector() { return null; },
    addEventListener() {},
  };
}

function page() {
  const elements = {};
  const document = {
    addEventListener() {},
    getElementById(id) { return elements[id] || (elements[id] = fakeElement()); },
    querySelectorAll() { return []; },
    querySelector() { return null; },
  };
  const ctx = {
    document,
    location: { hash: '', pathname: '/' },
    history: { replaceState() {} },
    fetch: () => Promise.reject(new Error('offline in tests')),
    FormData: class FormData {},
    CSS: { escape: (s) => String(s).replace(/["\\]/g, '\\$&') },
    setTimeout,
    console,
    scrollY: 0,
    scrollTo() {},
    addEventListener() {},
  };
  ctx.window = ctx;
  ctx.self = ctx;
  vm.createContext(ctx);
  for (const f of ['staff.js', 'forms-eval.js', 'forms.js', 'app.js']) {
    vm.runInContext(fs.readFileSync(path.join(__dirname, f), 'utf8'), ctx, { filename: f });
  }
  return { ctx, elements, run: (src) => vm.runInContext(src, ctx) };
}

function dashboard(role, features) {
  const p = page();
  p.ctx.__role = role;
  p.ctx.__features = features;
  p.run('session = { status: "active", role: __role, display_name: "T", csrf_token: "x" };' +
    'practiceInfo = { type: { words: {} , clinical: true }, features: __features };');
  return p.run('renderDashboard()');
}

test('the Forms tab shows for an admin with forms on, and not for the front desk or with forms off', () => {
  const on = { forms: true, messages: true, store: true, website: true };
  assert.match(dashboard('admin', on), /data-tab="forms"/);
  assert.doesNotMatch(dashboard('office', on), /data-tab="forms"/);
  assert.doesNotMatch(dashboard('provider', on), /data-tab="forms"/);
  assert.doesNotMatch(dashboard('admin', { ...on, forms: false }), /data-tab="forms"/);
});

test('rules the pick-list editor shows round-trip to the same JSON tree', () => {
  const p = page();
  p.ctx.__def = fixture('valid/operators.json');
  const results = p.run(`(() => {
    const out = [];
    for (const it of __def.sections[0].pages[1].items) {
      const r = ruleToConds(it.enableWhen);
      out.push({ id: it.id, simple: r !== null, same: r !== null && JSON.stringify(condsToRule(__def, r)) === JSON.stringify(it.enableWhen) });
    }
    return out;
  })()`);
  const simple = JSON.parse(JSON.stringify(results)).filter((r) => r.simple).map((r) => r.id);
  // Every single-operator rule on an answer is editable; score and nested-not rules aren't.
  assert.deepEqual(simple.sort(), ['and_b', 'answered_t', 'eq_n', 'eq_t', 'ge_n', 'gt_n', 'in_c', 'in_m', 'le_i', 'lt_d', 'lt_i', 'ne_c', 'not_n', 'or_c'].sort());
  for (const r of results) if (r.simple) assert.ok(r.same, `${r.id} changed on the way through the editor`);
  assert.equal(p.run('JSON.stringify(ruleToConds({ not: { answered: "t" } }))'), '{"mode":"all","conds":[{"item":"t","op":"unanswered"}]}');
});

test('the editor renders a definition, with anchors at validator paths', () => {
  const p = page();
  p.ctx.__def = fixture('valid/branching.json');
  const html = p.run(`
    fb.view = "edit";
    fb.version = { id: "v1", form_id: "f1", form_name: "Intake", kind: "custom", status: "draft", version: 1, assets: [] };
    fb.form = { form: { name: "Intake" }, versions: [{ id: "v1", version: 1, status: "draft" }] };
    fb.def = __def;
    renderEditor();`);
  assert.match(html, /data-fb-anchor="sections\[0\]\.pages\[1\]\.items\[1\]"/);
  assert.match(html, /includes any of/); // pain_level: in on a multiple choice
  assert.match(html, /can't show/); // s_intake's next uses not(eq), shown as JSON
  assert.match(html, /data-fb="publishDraft"/);
  assert.doesNotMatch(html, /\son[a-z]+\s*=\s*["']/i);

  const ro = p.run('fb.version.status = "published"; fb.form.versions[0].status = "published"; renderEditor();');
  assert.doesNotMatch(ro, /data-fb="saveDraft"|data-fb="addSection"/);
  assert.match(ro, /data-fb="newVersion"/);
  const inst = p.run('fb.version.status = "draft"; fb.version.kind = "instrument"; renderEditor();');
  assert.match(inst, /Built-in instruments are read-only/);
  assert.doesNotMatch(inst, /data-fb="publishDraft"/);
});

test('the preview walks the branches as a patient would', () => {
  const p = page();
  p.ctx.__def = fixture('valid/branching.json');
  const walk = (answers) => {
    p.ctx.__answers = answers;
    return JSON.parse(p.run('JSON.stringify(previewPages(WeHelpForms.compile(__def), __def, __answers).pages.map((x) => x.id))'));
  };
  assert.deepEqual(walk({ consent: false }), ['p_intro']);
  assert.deepEqual(walk({ consent: true, symptoms: ['fever'] }), ['p_intro', 'p_symptoms', 'p_fever', 'p_notes']);
  assert.deepEqual(walk({ consent: true, symptoms: ['cough'] }), ['p_intro', 'p_symptoms', 'p_general', 'p_notes']);

  const html = p.run(`
    fb.view = "edit";
    fb.version = { id: "v1", form_id: "f1", kind: "custom", status: "draft", version: 1, assets: [] };
    fb.def = __def;
    fb.preview = { answers: { consent: true, symptoms: ["fever"], temp: 38.5 }, page: 2, problems: [] };
    renderPreview();
    document.getElementById("fb-preview").innerHTML;`);
  assert.match(html, /Highest temperature/);
  assert.match(html, /Page 3 of 4/);
  assert.match(html, /101\.3/); // the live score
});

test('the score editor reads and writes a sum of chosen items', () => {
  const p = page();
  p.ctx.__def = fixture('valid/missing.json');
  assert.equal(p.run('JSON.stringify(scoreItems(__def.scores[0].expr))'), '["q1","q2","q3","q4"]');
  assert.equal(p.run('JSON.stringify(sumExpr(__def, ["q3", "q1"]))'), '{"sum":[{"scoreOf":"q1"},{"scoreOf":"q3"}]}');
  assert.equal(p.run('scoreItems(__def.scores[5].expr)'), null); // a division: shown as JSON
});

test('the Due forms tab shows for every staff role while forms are on (#196)', () => {
  const on = { forms: true, messages: true, store: true, website: true };
  for (const role of ['admin', 'office', 'provider']) {
    assert.match(dashboard(role, on), /data-tab="dueforms"/, role);
    assert.doesNotMatch(dashboard(role, { ...on, forms: false }), /data-tab="dueforms"/, role);
  }
});

test('the patient Forms panel and the Due forms view render from the API (#196)', async () => {
  const p = page();
  p.run('session = { status: "active", role: "office", csrf_token: "x" }; practiceInfo = { type: { words: {} }, features: { forms: true } };');
  p.run(`api = async (method, path) => {
    if (path.startsWith('/assignments')) return { assignments: [{ id: 'a1', form_name: 'Intake <b>', schedule: { kind: 'weeks', every: 2, start_date: '2026-11-20', count: 4 },
      channel: 'app', assigned_by_name: 'Dr. Ada', created_at: '2026-11-16T10:00:00Z', occurrences: [
        { id: 'o1', state: 'open', due_at: '2026-11-20T14:00:00Z', opens_at: '2026-11-20T14:00:00Z', closes_at: '2026-11-23T14:00:00Z' }] }] };
    if (path.startsWith('/responses?')) return { responses: [{ id: 'r1', form_name: 'Intake', version: 1, submitted_at: '2026-11-16T15:00:00Z', new: true, scores: { total: { value: '12', band: 'mild' } }, flags: [{ id: 'self_harm', text: 'Item 9 above Not at all' }] }] };
    if (path === '/responses/r1') return { id: 'r1', title: 'Intake', version: 1, channel: 'app', submitted_at: '2026-11-16T15:00:00Z', flags: [{ id: 'self_harm', text: 'Item 9 <b>above</b> Not at all' }],
      scores: [{ id: 'total', text: 'Total', value: '12', band: 'mild', band_text: 'Mild' }], items: [{ item_id: 'age', text: 'Your age', display: '40 <years>' }] };
    if (path === '/forms') return { forms: [{ id: 'f1', name: 'Intake', published: 1 }, { id: 'f2', name: 'Old', published: 1, archived_at: 'x' }] };
    if (path === '/form-occurrences/due') return { due_today: [{ patient_name: 'Jordan', form_name: 'Intake', due_at: '2026-11-16T14:00:00Z', closes_at: '2026-11-19T14:00:00Z', providers: [{ name: 'Dr. Ada' }] }], overdue: [], missed_this_week: [],
      flagged_this_week: [{ patient_name: 'Jordan', form_name: 'PHQ-9', submitted_at: '2026-11-16T15:00:00Z', flags: [{ id: 'self_harm', text: 'Item 9 above Not at all' }], providers: [] }] };
    throw new Error('unexpected ' + path);
  };`);
  await p.run('showPatientForms("p1", "Jordan")');
  const panel = p.elements.pairing.innerHTML;
  assert.match(panel, /Every 2 weeks from 2026-11-20, 4 times/);
  assert.match(panel, /Intake &lt;b&gt;/); // escaped
  assert.match(panel, /<option value="f1">Intake<\/option>/);
  assert.doesNotMatch(panel, /value="f2"/); // archived forms can't be assigned
  assert.match(panel, /data-fb="endAssignment"/);
  assert.match(panel, /Intake v1 <span class="badge badge-pending">new<\/span>/);
  assert.match(panel, /total: 12 \(mild\)/);
  assert.match(panel, /<tr class="fb-flagged">.*flag: self_harm/s); // red in the list
  p.elements['fb-response'] = { innerHTML: '', scrollIntoView() {} };
  await p.run('showFormResponse("r1", "p1", "Jordan")');
  const detail = p.elements['fb-response'].innerHTML;
  assert.match(detail, /Your age<\/td><td>40 &lt;years&gt;/);
  assert.match(detail, /Mild/);
  assert.match(detail, /alert-error fb-flag"><strong>Flagged<\/strong><ul><li>Item 9 &lt;b&gt;above&lt;\/b&gt; Not at all/);
  const el = { innerHTML: '' };
  p.ctx.__el = el;
  await p.run('loadDueForms(__el)');
  assert.match(el.innerHTML, /Due today <span class="badge badge-pending">1<\/span>/);
  assert.match(el.innerHTML, /Nothing overdue/);
  assert.match(el.innerHTML, /Dr\. Ada/);
  assert.match(el.innerHTML, /fb-flagged-card.*Flagged this week.*<td class="fb-flag">Item 9 above Not at all/s);
});


// openEditor puts a draft in the builder with fake timers and fetch (#260).
function openEditor(defFile) {
  const p = page();
  const timers = [];
  const calls = [];
  p.ctx.setTimeout = (fn, ms) => { timers.push({ fn, ms }); return timers.length; };
  p.ctx.clearTimeout = (id) => { if (id && timers[id - 1]) timers[id - 1].fn = null; };
  p.ctx.__respond = () => ({ status: 200, body: { id: 'v1', version: 1, digest: 'x' } });
  p.ctx.fetch = async (url, opts) => {
    calls.push({ url, method: opts.method, body: opts.body });
    const r = p.ctx.__respond(url, opts);
    return { ok: r.status < 300, status: r.status, statusText: '', json: async () => r.body };
  };
  p.ctx.__def = fixture(defFile);
  p.run(`session = { status: "active", role: "admin", csrf_token: "x" };
    fb.container = document.getElementById("tab-content");
    fb.view = "edit";
    fb.version = { id: "v1", form_id: "f1", form_name: "F", kind: "custom", status: "draft", version: 1, assets: [] };
    fb.form = { form: { name: "F" }, versions: [{ id: "v1", version: 1, status: "draft" }] };
    fb.def = __def;
    resetHistory();`);
  // fire runs the timers that are due, as time passing would.
  const fire = async () => {
    const due = timers.filter((x) => x.fn);
    for (const x of due) {
      const fn = x.fn;
      x.fn = null;
      await fn();
    }
    return due.map((x) => x.ms);
  };
  const def = () => JSON.parse(p.run('JSON.stringify(fb.def)'));
  return { p, timers, calls, fire, def };
}

test('undo and redo across add, remove and move (#260)', () => {
  const { p, def } = openEditor('valid/branching.json');
  const start = def();
  p.run('addSection()');
  assert.equal(def().sections.length, start.sections.length + 1);
  p.run('moveNode(["sections"], 0, 1)');
  assert.equal(def().sections[1].id, start.sections[0].id);
  p.run('removeNode(["sections", 1, "pages", 0, "items"], 0)');
  p.run('undoEdit()');
  p.run('undoEdit()');
  assert.equal(def().sections[0].id, start.sections[0].id, 'undo the move');
  p.run('undoEdit()');
  assert.deepEqual(def(), start, 'undo the add');
  assert.equal(p.run('fb.dirty'), false, 'back to the saved state');
  p.run('redoEdit()');
  p.run('redoEdit()');
  assert.equal(def().sections[1].id, start.sections[0].id, 'redo the add and the move');
  p.run('addPage(0)');
  assert.equal(p.run('fb.redo.length'), 0, 'a new edit clears redo');
  // The undo and redo buttons are drawn with their state.
  const html = p.run('renderEditor()');
  assert.match(html, /data-fb="undoEdit" data-fb-args="\[\]"\s+title="Undo/);
  assert.match(html, /data-fb="redoEdit" data-fb-args="\[\]" disabled/);
});

test('at least 50 undo steps (#260)', () => {
  const { p, def } = openEditor('valid/minimal.json');
  const start = def();
  for (let i = 0; i < 60; i++) p.run(`fb.def.title = "T${i}"; changed();`);
  for (let i = 0; i < 60; i++) p.run('undoEdit()');
  assert.deepEqual(def(), start);
  assert.equal(p.run('fb.redo.length'), 60);
});

test('autosave waits for a pause, saves once, then shows Saved (#260)', async () => {
  const { p, calls, fire } = openEditor('valid/minimal.json');
  p.run('fb.def.title = "A"; changed();');
  p.run('fb.def.title = "AB"; changed();');
  assert.equal(calls.length, 0, 'nothing saved while editing');
  assert.equal(p.run('saveStatusText()'), 'Unsaved changes');
  const delays = await fire();
  assert.deepEqual(delays, [2000], 'one pending autosave, two seconds after the last edit');
  assert.equal(calls.length, 1);
  assert.equal(calls[0].method, 'PUT');
  assert.match(calls[0].url, /\/api\/v1\/form-versions\/v1$/);
  assert.equal(JSON.parse(calls[0].body).title, 'AB');
  assert.equal(p.run('saveStatusText()'), 'Saved');
  assert.equal(p.run('fb.dirty'), false);
  assert.equal(p.run('fbCanLeave()'), true, 'nothing to warn about');
});

test('a refused autosave keeps the draft and shows the error at its path (#260)', async () => {
  const { p, fire, def } = openEditor('valid/minimal.json');
  p.ctx.__respond = () => ({ status: 422, body: { error: 'sections[0].pages[0].items[0].options: duplicate value "1"', code: 'invalid_definition', path: 'sections[0].pages[0].items[0].options', kind: 'duplicate' } });
  p.run('fb.def.title = "Edited"; changed();');
  await fire();
  assert.equal(def().title, 'Edited', 'the local copy is kept');
  assert.match(p.run('saveStatusText()'), /^Not saved: sections\[0\]\.pages\[0\]\.items\[0\]\.options: duplicate value/);
  assert.equal(p.run('fb.error.path'), 'sections[0].pages[0].items[0].options');
  assert.equal(p.run('fb.dirty'), true);
  // Leaving now asks.
  p.ctx.confirm = () => false;
  assert.equal(p.run('fbCanLeave()'), false);
  p.ctx.confirm = () => true;
  assert.equal(p.run('fbCanLeave()'), true);
});

test('nothing goes to localStorage or IndexedDB (#260)', () => {
  const src = fs.readFileSync(path.join(__dirname, 'forms.js'), 'utf8');
  assert.doesNotMatch(src, /(localStorage|sessionStorage|indexedDB)\s*[.[]/);
});

test('duplicate a page: fresh ids, rules inside the copy re-pointed, others untouched (#258)', () => {
  const { p, def } = openEditor('valid/branching.json');
  const before = def();
  p.run('duplicatePage(0, 1)'); // p_symptoms: symptoms, and pain_level shown when symptoms includes pain
  const after = def();
  const pages = after.sections[0].pages;
  assert.equal(pages.length, before.sections[0].pages.length + 1);
  const copy = pages[2];
  assert.equal(copy.id, 'p_symptoms_copy');
  assert.deepEqual(copy.items.map((i) => i.id), ['symptoms_copy', 'pain_level_copy']);
  assert.deepEqual(copy.items[1].enableWhen, { in: [{ answer: 'symptoms_copy' }, { str: 'pain' }] }, 'the copy points at the copied question');
  assert.deepEqual(copy.enableWhen, before.sections[0].pages[1].enableWhen, 'a rule on an earlier page is unchanged');
  assert.deepEqual(pages[1], before.sections[0].pages[1], 'the original page is unchanged');
  assert.deepEqual(after.sections[0].next, before.sections[0].next, 'rules elsewhere are unchanged');
  const ids = new Set();
  for (const s of after.sections) for (const pg of s.pages) { ids.add(pg.id); for (const it of pg.items) ids.add(it.id); }
  assert.equal(p.run('localError()'), null, 'the result validates');
  // A second copy gets another fresh id.
  p.run('duplicatePage(0, 1)');
  assert.equal(def().sections[0].pages[2].id, 'p_symptoms_copy2');
  p.run('undoEdit(); undoEdit()');
  assert.deepEqual(def(), before, 'undo removes the copies');
});

test('duplicate a question keeps its rule and gets a fresh id (#258)', () => {
  const { p, def } = openEditor('valid/branching.json');
  p.run('duplicateItem(0, 0, 2)'); // smoker, shown when age >= 18
  const items = def().sections[0].pages[0].items;
  assert.equal(items[3].id, 'smoker_copy');
  assert.deepEqual(items[3].enableWhen, items[2].enableWhen);
  assert.equal(items[3].text, items[2].text);
  assert.equal(p.run('localError()'), null);
});

test('New form offers the templates and sends the chosen one; Duplicate and Copy to edit call copy (#258)', async () => {
  const { p, calls } = openEditor('valid/minimal.json');
  p.ctx.__respond = (url, opts) => {
    if (url.endsWith('/form-templates')) return { status: 200, body: { templates: [{ key: 'intake', name: 'New patient intake', description: 'd' }] } };
    if (url.endsWith('/copy')) return { status: 201, body: { form_id: 'f2', version_id: 'v2', version: 1, name: 'PHQ-9 (copy)' } };
    if (opts.method === 'POST' && url.endsWith('/forms')) return { status: 201, body: { form_id: 'f3', version_id: 'v3', version: 1 } };
    return { status: 404, body: { error: 'no' } };
  };
  p.run('fb.view = "list"; fb.list = [{ id: "f9", name: "PHQ-9", kind: "instrument", latest: { id: "v9", version: 1, status: "published" }, published: 1 }, { id: "f8", name: "Intake", kind: "custom", latest: { id: "v8", version: 2, status: "draft" } }]; fb.newForm = false;');
  await p.run('toggleNewForm()');
  const list = p.run('renderFormList()');
  assert.match(list, /<option value="intake" title="d">Start from: New patient intake<\/option>/);
  assert.match(list, /data-fb="copyForm" data-fb-args="\[&quot;f9&quot;,null\]">Copy to edit/);
  assert.match(list, /data-fb="copyForm" data-fb-args="\[&quot;f8&quot;,null\]">Duplicate/);
  p.elements['fb-new-name'] = { value: 'My intake' };
  p.elements['fb-new-template'] = { value: 'intake' };
  p.ctx.openVersion = async () => {};
  await p.run('createForm()');
  const create = calls.find((c) => c.method === 'POST' && c.url.endsWith('/forms'));
  assert.deepEqual(JSON.parse(create.body), { name: 'My intake', template: 'intake' });
  await p.run('fb.view = "list"; copyForm("f9", null)');
  const copy = calls.find((c) => c.url.endsWith('/forms/f9/copy'));
  assert.ok(copy && copy.method === 'POST' && copy.body === '{}', 'Copy to edit posts to the copy route');
});

test('an edited copy of an instrument says it is no longer the validated instrument (#258)', () => {
  const { p } = openEditor('valid/instrument-phq9.json');
  const html = p.run('renderEditor()');
  assert.match(html, /fb-copynote">Based on a published instrument\. Once edited, it's no longer the validated instrument; keep the attribution: Developed by Drs\. Robert L\. Spitzer/);
});

test('the starter templates validate and evaluate in JavaScript (#258)', () => {
  for (const key of ['intake', 'followup', 'symptoms']) {
    const p = page();
    p.ctx.__def = fixture(`valid/template-${key}.json`);
    const res = JSON.parse(p.run('JSON.stringify(WeHelpForms.evaluate(WeHelpForms.compile(__def), {}))'));
    assert.equal(res.error, null, key);
    assert.ok(res.path.length >= 1, key);
  }
});

test('answer lists: add with scores, reverse scores, copy options from another question (#259)', () => {
  const { p, def } = openEditor('valid/minimal.json');
  p.elements['__sel'] = null;
  p.ctx.document.querySelector = () => ({ value: 'choice:freq4' });
  p.run('addFormItem(0, 0)');
  const added = def().sections[0].pages[0].items[1];
  assert.deepEqual(added.options.map((o) => [o.value, o.text, o.score]), [['never', 'Never', 0], ['sometimes', 'Sometimes', 1], ['often', 'Often', 2], ['always', 'Always', 3]]);
  p.run('reverseScores(["sections", 0, "pages", 0, "items", 1])');
  assert.deepEqual(def().sections[0].pages[0].items[1].options.map((o) => o.score), [3, 2, 1, 0]);
  p.run('copyOptionsFrom(["sections", 0, "pages", 0, "items", 0], "' + added.id + '")');
  assert.deepEqual(def().sections[0].pages[0].items[0].options, def().sections[0].pages[0].items[1].options);
  p.run('applyAnswerList(["sections", 0, "pages", 0, "items", 0], "yesno")');
  assert.deepEqual(def().sections[0].pages[0].items[0].options, [{ value: 'yes', text: 'Yes' }, { value: 'no', text: 'No' }, { value: 'not_sure', text: 'Not sure' }]);
  // q1's score sums its option scores: an unscored list is flagged, a scored one fixes it.
  assert.equal(p.run('localError().path'), 'scores[0].expr.scoreOf');
  p.run('applyAnswerList(["sections", 0, "pages", 0, "items", 0], "zero10")');
  assert.equal(def().sections[0].pages[0].items[0].options.length, 11);
  assert.equal(p.run('localError()'), null);
});

test('the grid: shared answers, rows, paste, sum score, and it reopens as a grid (#259)', () => {
  const { p, def } = openEditor('valid/minimal.json');
  p.run('addGrid(0, 0, "freq4")'); // rows 1..3 after q1
  let items = def().sections[0].pages[0].items;
  assert.equal(items.length, 4);
  let html = p.run('renderItems(fb.def.sections[0].pages[0], 0, 0, 0, false)');
  assert.match(html, /Grid: 3 rows share these answers/);
  // A shared answer changes every row.
  p.run('gridSetOption(0, 0, 1, 4, 0, "text", "Not at all")');
  items = def().sections[0].pages[0].items;
  assert.ok(items.slice(1).every((it) => it.options[0].text === 'Not at all'));
  p.run('gridAddOption(0, 0, 1, 4)');
  assert.ok(def().sections[0].pages[0].items.slice(1).every((it) => it.options.length === 5));
  p.run('gridRemoveOption(0, 0, 1, 4, 4)');
  // Paste rows, one per line.
  p.elements['fb-gridpaste-0-0-1'] = { value: 'Headaches\n\n  Tiredness  \nSleep' };
  p.run('gridPaste(0, 0, 1, 4)');
  items = def().sections[0].pages[0].items;
  assert.deepEqual(items.slice(4).map((it) => it.text), ['Headaches', 'Tiredness', 'Sleep']);
  assert.equal(new Set(items.map((it) => it.id)).size, items.length, 'fresh ids');
  assert.ok(items.slice(4).every((it) => JSON.stringify(it.options) === JSON.stringify(items[1].options)));
  // Score = sum of these rows.
  p.run('gridSum(0, 0, 1, 7)');
  const score = def().scores.find((sc) => sc.id.startsWith('grid_total'));
  assert.deepEqual(score.expr, { sum: items.slice(1, 7).map((it) => ({ scoreOf: it.id })) });
  assert.equal(p.run('localError()'), null);
  p.ctx.__answers = Object.fromEntries(items.slice(1, 7).map((it) => [it.id, 'often']));
  const res = JSON.parse(p.run('JSON.stringify(WeHelpForms.evaluate(WeHelpForms.compile(fb.def), Object.assign({ q1: "1" }, __answers)))'));
  assert.equal(res.scores[score.id].value, '12', 'six rows at 2');
  // Saved as plain choice questions; reopens as a grid, until a row's answers differ.
  html = p.run('renderItems(fb.def.sections[0].pages[0], 0, 0, 0, false)');
  assert.match(html, /Grid: 6 rows share these answers/);
  p.run('fb.def.sections[0].pages[0].items[3].options[0].text = "Different"; changed();');
  html = p.run('renderItems(fb.def.sections[0].pages[0], 0, 0, 0, false)');
  assert.doesNotMatch(html, /Grid: 6 rows/);
  assert.match(html, /Grid: 3 rows share these answers/, 'the rows after it still group');
  p.run('undoEdit()');
  assert.match(p.run('renderItems(fb.def.sections[0].pages[0], 0, 0, 0, false)'), /Grid: 6 rows/);
});

test('a built-in opens as a read-only grid (#259)', () => {
  const { p } = openEditor('valid/instrument-phq9.json');
  p.run('fb.version.kind = "instrument"');
  const html = p.run('renderItems(fb.def.sections[0].pages[0], 0, 0, 0, fbReadOnly())');
  assert.match(html, /Grid: 9 rows share these answers/);
  assert.doesNotMatch(html, /data-fb="gridAddRow"/);
});

test('the staff web scripts never define the same global twice (#259)', () => {
  // forms.js once had an addItem that app.js (loaded later) replaced.
  const seen = {};
  for (const f of ['staff.js', 'forms-eval.js', 'forms.js', 'app.js']) {
    const src = fs.readFileSync(path.join(__dirname, f), 'utf8');
    for (const m of src.matchAll(/^(?:async\s+)?function\s+([A-Za-z0-9_$]+)|^(?:const|let|var)\s+([A-Za-z0-9_$]+)/gm)) {
      const name = m[1] || m[2];
      assert.ok(!seen[name], `${name} is defined in both ${seen[name]} and ${f}`);
      seen[name] = f;
    }
  }
});
