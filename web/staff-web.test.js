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
    if (path.startsWith('/responses?')) return { responses: [{ id: 'r1', form_name: 'Intake', version: 1, submitted_at: '2026-11-16T15:00:00Z', new: true, scores: { total: { value: '12', band: 'mild' } } }] };
    if (path === '/responses/r1') return { id: 'r1', title: 'Intake', version: 1, channel: 'app', submitted_at: '2026-11-16T15:00:00Z', flags: [],
      scores: [{ id: 'total', text: 'Total', value: '12', band: 'mild', band_text: 'Mild' }], items: [{ item_id: 'age', text: 'Your age', display: '40 <years>' }] };
    if (path === '/forms') return { forms: [{ id: 'f1', name: 'Intake', published: 1 }, { id: 'f2', name: 'Old', published: 1, archived_at: 'x' }] };
    if (path === '/form-occurrences/due') return { due_today: [{ patient_name: 'Jordan', form_name: 'Intake', due_at: '2026-11-16T14:00:00Z', closes_at: '2026-11-19T14:00:00Z', providers: [{ name: 'Dr. Ada' }] }], overdue: [], missed_this_week: [] };
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
  p.elements['fb-response'] = { innerHTML: '', scrollIntoView() {} };
  await p.run('showFormResponse("r1", "p1", "Jordan")');
  const detail = p.elements['fb-response'].innerHTML;
  assert.match(detail, /Your age<\/td><td>40 &lt;years&gt;/);
  assert.match(detail, /Mild/);
  const el = { innerHTML: '' };
  p.ctx.__el = el;
  await p.run('loadDueForms(__el)');
  assert.match(el.innerHTML, /Due today <span class="badge badge-pending">1<\/span>/);
  assert.match(el.innerHTML, /Nothing overdue/);
  assert.match(el.innerHTML, /Dr\. Ada/);
});
