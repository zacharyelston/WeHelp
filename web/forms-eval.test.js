// Runs the shared wehelp.form.v1 vectors against web/forms-eval.js.
//
//   node --test web/forms-eval.test.js     (or: make test-js)
//
// Needs Node 18 or later; no packages. It isn't embedded in the binary
// (web/web.go embeds files by name).
'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const F = require('./forms-eval.js');

const root = path.join(__dirname, '..', 'testdata', 'forms');
const read = (rel) => JSON.parse(fs.readFileSync(path.join(root, rel), 'utf8'));
const plain = (o) => JSON.parse(JSON.stringify(o));

test('valid definitions compile', () => {
  for (const f of fs.readdirSync(path.join(root, 'valid'))) {
    assert.doesNotThrow(() => F.compile(read(path.join('valid', f))), f);
  }
});

test('shared invalid definitions are refused with the same path and kind', () => {
  const { cases } = read('invalid/cases.json');
  const shared = cases.filter((c) => c.shared);
  assert.ok(shared.length > 20);
  for (const c of shared) {
    let err = null;
    try {
      F.compile(read(path.join('invalid', c.file)));
    } catch (e) {
      err = e;
    }
    assert.ok(err instanceof F.FormError, `${c.file}: not refused (${err})`);
    assert.deepEqual({ path: err.path, kind: err.kind }, { path: c.path, kind: c.kind }, c.file);
  }
});

for (const file of fs.readdirSync(path.join(root, 'vectors'))) {
  const vf = read(path.join('vectors', file));
  const form = F.compile(read(vf.definition));
  for (const c of vf.cases) {
    test(`${file}: ${c.name}`, () => {
      const answers = c.answers || {};
      const res = F.evaluate(form, answers);
      if (c.error) {
        assert.equal(res.error && res.error.kind, c.error);
      } else {
        assert.equal(res.error, null);
      }
      for (const [id, want] of Object.entries(c.visible || {})) {
        assert.equal(res.visible[id], want, `visible[${id}]`);
        assert.equal(F.visible(form, answers, id), want, `visible(${id})`);
      }
      for (const [id, want] of Object.entries(c.next || {})) {
        assert.equal(F.nextSection(form, answers, id), want, `next[${id}]`);
      }
      if (c.path) assert.deepEqual(res.path, c.path);
      if (c.scores) {
        if (Object.keys(c.scores).length === 0) assert.deepEqual(Object.keys(res.scores), []);
        for (const [id, want] of Object.entries(c.scores)) {
          assert.deepEqual(plain(res.scores[id]), want, `score ${id}`);
        }
      }
      if (c.check) {
        const ch = F.checkAnswers(form, answers, c.today || '2026-11-16');
        assert.deepEqual(plain(ch.answers), c.check.answers);
        assert.deepEqual(ch.errors.map((e) => `${e.item}:${e.code}`), c.check.errors.map((e) => `${e.item}:${e.code}`));
      }
    });
  }
}

test('half-even rounding and output formatting', () => {
  const cases = [
    ['0.5', 0, '0'], ['1.5', 0, '2'], ['2.5', 0, '2'], ['-0.5', 0, '0'], ['-1.5', 0, '-2'],
    ['-2.5', 0, '-2'], ['1.005', 2, '1'], ['1.015', 2, '1.02'], ['-1.015', 2, '-1.02'], ['123', 0, '123'],
  ];
  for (const [inp, places, want] of cases) assert.equal(F._formatDecimal(inp, places), want, `${inp} @ ${places}`);
});

test('dates', () => {
  for (const [s, want] of [['2024-02-29', true], ['2023-02-29', false], ['1900-02-29', false], ['2000-02-29', true], ['2026-04-31', false], ['0000-01-01', false]]) {
    assert.equal(F._validDate(s), want, s);
  }
});

test('ids that collide with Object.prototype are plain ids', () => {
  const def = {
    format: 'wehelp.form.v1', title: 't',
    sections: [{ id: 'constructor', pages: [{ id: 'toString', items: [{ id: 'hasOwnProperty', type: 'boolean', text: 'b' }] }] }],
  };
  const res = F.evaluate(def, {});
  assert.equal(res.visible.hasOwnProperty, true);
  assert.deepEqual(plain(res.answers), {});
  assert.equal(F.visible(def, {}, 'valueOf'), false);
});

test('the evaluator never uses eval or Function', () => {
  const src = fs.readFileSync(path.join(__dirname, 'forms-eval.js'), 'utf8');
  assert.doesNotMatch(src, /\beval\s*\(|\bnew\s+Function\b|\bFunction\s*\(/);
});
