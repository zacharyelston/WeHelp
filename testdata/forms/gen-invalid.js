// Generates testdata/forms/invalid/*.json, cases.json and valid/depth-32.json. One problem per file.
// Run from the repo root: node testdata/forms/gen-invalid.js testdata/forms/invalid
const fs = require('fs');
const path = require('path');
const out = process.argv[2];

function base() {
  return {
    format: 'wehelp.form.v1',
    title: 'Invalid case',
    sections: [
      {
        id: 's1',
        pages: [
          {
            id: 'p1',
            items: [
              { id: 'q1', type: 'choice', text: 'One', options: [{ value: '0', text: 'No', score: 0 }, { value: '1', text: 'Yes', score: 1 }] },
              { id: 'n1', type: 'integer', text: 'A number', min: 0, max: 10 },
            ],
          },
          {
            id: 'p2',
            items: [
              { id: 'm1', type: 'choice', multiple: true, text: 'Several', options: [{ value: 'a', text: 'A' }, { value: 'b', text: 'B' }] },
              { id: 'd1', type: 'date', text: 'A date' },
              { id: 'info', type: 'display', text: 'Note' },
            ],
          },
        ],
      },
      { id: 's2', pages: [{ id: 'p3', items: [{ id: 't1', type: 'text', text: 'Words' }] }] },
    ],
    scores: [{ id: 'total', expr: { scoreOf: 'q1' } }],
  };
}

const item = (d, s, p, i) => d.sections[s].pages[p].items[i];
const cases = [];
// shared: the JavaScript validator must refuse it with the same path and kind.
function add(name, mutate, p, kind, shared, raw) {
  let text;
  if (raw !== undefined) text = raw;
  else {
    const d = base();
    mutate(d);
    text = JSON.stringify(d, null, 1) + '\n';
  }
  fs.writeFileSync(path.join(out, name + '.json'), text);
  cases.push({ file: name + '.json', path: p, kind, shared });
}

add('not-json', null, '', 'json', false, '{"format": "wehelp.form.v1",\n');
add('not-object', null, '', 'schema', false, '[]\n');
add('wrong-format', (d) => { d.format = 'wehelp.form.v2'; }, 'format', 'schema', false);
add('missing-title', (d) => { delete d.title; }, 'title', 'schema', false);
add('unknown-top-field', (d) => { d.version = 2; }, 'version', 'schema', false);
add('unknown-item-field', (d) => { item(d, 0, 0, 1).colour = 'red'; }, 'sections[0].pages[0].items[1].colour', 'schema', false);
add('field-for-other-type', (d) => { item(d, 0, 0, 1).options = []; }, 'sections[0].pages[0].items[1].options', 'schema', false);
add('unknown-item-type', (d) => { item(d, 0, 0, 1).type = 'slider'; }, 'sections[0].pages[0].items[1].type', 'schema', false);
add('no-sections', (d) => { d.sections = []; }, 'sections', 'schema', false);
add('empty-page', (d) => { d.sections[1].pages[0].items = []; }, 'sections[1].pages[0].items', 'schema', false);
add('bad-control', (d) => { item(d, 0, 0, 1).control = 'radio'; }, 'sections[0].pages[0].items[1].control', 'schema', false);
add('slider-without-bounds', (d) => { const it = item(d, 0, 0, 1); delete it.max; it.control = 'slider'; }, 'sections[0].pages[0].items[1].control', 'schema', false);
add('min-above-max', (d) => { item(d, 0, 0, 1).min = 11; }, 'sections[0].pages[0].items[1].min', 'range', false);
add('fraction-on-integer', (d) => { item(d, 0, 0, 1).max = 9.5; }, 'sections[0].pages[0].items[1].max', 'range', false);
add('number-exponent', null, 'sections[0].pages[0].items[1].max', 'range', false,
  JSON.stringify(base(), null, 1).replace('"max": 10', '"max": 1e1') + '\n');
add('number-too-many-digits', (d) => { item(d, 0, 0, 1).max = 1234567890123; }, 'sections[0].pages[0].items[1].max', 'range', false);
add('bad-date-bound', (d) => { item(d, 0, 1, 1).max = '2026-13-01'; }, 'sections[0].pages[1].items[1].max', 'range', false);
add('required-display', (d) => { item(d, 0, 1, 2).required = true; }, 'sections[0].pages[1].items[2].required', 'schema', false);
add('maxlength-too-big', (d) => { item(d, 1, 0, 0).maxLength = 10001; }, 'sections[1].pages[0].items[0].maxLength', 'range', false);
add('media-url', (d) => { item(d, 0, 1, 2).media = { type: 'image', asset: 'https://example.com/a.png' }; }, 'sections[0].pages[1].items[2].media.asset', 'schema', false);
add('bad-id', (d) => { item(d, 0, 0, 1).id = '1n'; }, 'sections[0].pages[0].items[1].id', 'schema', false);
add('reserved-id', (d) => { d.sections[1].id = 'end'; }, 'sections[1].id', 'schema', false);
add('default-next-not-last', (d) => { d.sections[0].next = [{ section: 's2' }, { when: { answered: 'q1' }, section: 'end' }]; }, 'sections[0].next[0]', 'schema', false);
add('prorate-without-minimum', (d) => { d.scores[0].missing = { policy: 'prorate', places: 0 }; }, 'scores[0].missing.minAnswered', 'schema', false);
add('bands-not-increasing', (d) => { d.scores[0].bands = [{ min: 1, id: 'hi' }, { min: 0, id: 'lo' }]; }, 'scores[0].bands[1].min', 'range', false);

// Shared with the JavaScript validator: ids, references, ordering, types, operators and caps.
add('duplicate-option', (d) => {
  d.sections[0].pages[1].items.push({ id: 'q3', type: 'choice', text: 'Dup', options: [{ value: '1', text: 'One' }, { value: '1', text: 'Also one' }] });
}, 'sections[0].pages[1].items[3].options', 'duplicate', true);
add('duplicate-id', (d) => { item(d, 0, 1, 1).id = 'p1'; }, 'sections[0].pages[1].items[1].id', 'duplicate', true);
add('duplicate-score-id', (d) => { d.scores.push({ id: 'q1', expr: { num: 1 } }); }, 'scores[1].id', 'duplicate', true);
add('dangling-answer', (d) => { item(d, 0, 1, 0).enableWhen = { eq: [{ answer: 'nope' }, { num: 1 }] }; }, 'sections[0].pages[1].items[0].enableWhen.eq[0].answer', 'reference', true);
add('dangling-score', (d) => { d.scores.push({ id: 'twice', expr: { mul: [{ score: 'nope' }, { num: 2 }] } }); }, 'scores[1].expr.mul[0].score', 'reference', true);
add('dangling-next', (d) => { d.sections[0].next = [{ section: 's9' }]; }, 'sections[0].next[0].section', 'reference', true);
add('answer-of-page', (d) => { item(d, 0, 1, 0).enableWhen = { answered: 'p1' }; }, 'sections[0].pages[1].items[0].enableWhen.answered', 'reference', true);
add('forward-item-rule', (d) => { item(d, 0, 0, 0).enableWhen = { answered: 'n1' }; }, 'sections[0].pages[0].items[0].enableWhen.answered', 'order', true);
add('page-rule-on-own-page', (d) => { d.sections[0].pages[1].enableWhen = { answered: 'm1' }; }, 'sections[0].pages[1].enableWhen.answered', 'order', true);
add('next-rule-on-later-section', (d) => { d.sections[0].next = [{ when: { answered: 't1' }, section: 's2' }]; }, 'sections[0].next[0].when.answered', 'order', true);
add('next-backward', (d) => { d.sections[1].next = [{ section: 's1' }]; }, 'sections[1].next[0].section', 'order', true);
add('next-self', (d) => { d.sections[0].next = [{ section: 's1' }]; }, 'sections[0].next[0].section', 'order', true);
add('score-cycle', (d) => {
  d.scores = [{ id: 'a', expr: { add: [{ score: 'b' }, { num: 1 }] } }, { id: 'b', expr: { add: [{ score: 'a' }, { num: 1 }] } }];
}, 'scores[0].expr.add[0].score', 'order', true);
add('score-self', (d) => { d.scores[0].expr = { add: [{ score: 'total' }, { num: 1 }] }; }, 'scores[0].expr.add[0].score', 'order', true);
add('rule-on-later-score', (d) => {
  d.scores.push({ id: 'words', expr: { count: ['t1'] } });
  item(d, 0, 1, 0).enableWhen = { gt: [{ score: 'words' }, { num: 0 }] };
}, 'sections[0].pages[1].items[0].enableWhen.gt[0].score', 'order', true);
add('unknown-operator', (d) => { item(d, 0, 1, 0).enableWhen = { xor: [{ answered: 'q1' }, { answered: 'n1' }] }; }, 'sections[0].pages[1].items[0].enableWhen', 'operator', true);
add('two-operators', (d) => { item(d, 0, 1, 0).enableWhen = { answered: 'q1', not: { answered: 'n1' } }; }, 'sections[0].pages[1].items[0].enableWhen', 'operator', true);
add('wrong-arity', (d) => { item(d, 0, 1, 0).enableWhen = { eq: [{ answer: 'n1' }] }; }, 'sections[0].pages[1].items[0].enableWhen.eq', 'operator', true);
add('arithmetic-in-rule', (d) => { item(d, 0, 1, 0).enableWhen = { gt: [{ add: [{ answer: 'n1' }, { num: 1 }] }, { num: 3 }] }; }, 'sections[0].pages[1].items[0].enableWhen.gt[0].add', 'operator', true);
add('compare-number-with-string', (d) => { item(d, 0, 1, 0).enableWhen = { eq: [{ answer: 'n1' }, { str: '3' }] }; }, 'sections[0].pages[1].items[0].enableWhen.eq[1]', 'type', true);
add('rule-not-boolean', (d) => { item(d, 0, 1, 0).enableWhen = { answer: 'n1' }; }, 'sections[0].pages[1].items[0].enableWhen', 'type', true);
add('order-strings', (d) => { item(d, 0, 1, 0).enableWhen = { lt: [{ answer: 'q1' }, { str: '1' }] }; }, 'sections[0].pages[1].items[0].enableWhen.lt', 'type', true);
add('unknown-option-in-rule', (d) => { item(d, 0, 1, 0).enableWhen = { eq: [{ answer: 'q1' }, { str: 'maybe' }] }; }, 'sections[0].pages[1].items[0].enableWhen.eq[1]', 'reference', true);
add('unknown-option-in-in', (d) => { item(d, 1, 0, 0).enableWhen = { in: [{ answer: 'm1' }, { str: 'a' }, { str: 'c' }] }; }, 'sections[1].pages[0].items[0].enableWhen.in[2]', 'reference', true);
add('in-with-expression', (d) => { item(d, 1, 0, 0).enableWhen = { in: [{ answer: 'n1' }, { answer: 'n1' }] }; }, 'sections[1].pages[0].items[0].enableWhen.in[1]', 'type', true);
add('multiple-choice-eq', (d) => { item(d, 1, 0, 0).enableWhen = { eq: [{ answer: 'm1' }, { str: 'a' }] }; }, 'sections[1].pages[0].items[0].enableWhen.eq[0]', 'type', true);
add('bad-date-literal', (d) => { item(d, 1, 0, 0).enableWhen = { lt: [{ answer: 'd1' }, { str: '2026-02-30' }] }; }, 'sections[1].pages[0].items[0].enableWhen.lt[1]', 'type', true);
add('display-in-rule', (d) => { item(d, 1, 0, 0).enableWhen = { answered: 'info' }; }, 'sections[1].pages[0].items[0].enableWhen.answered', 'type', true);
add('score-not-number', (d) => { d.scores[0].expr = { answered: 'q1' }; }, 'scores[0].expr', 'type', true);
add('scoreof-unscored', (d) => { d.scores[0].expr = { scoreOf: 'm1' }; }, 'scores[0].expr.scoreOf', 'type', true);
add('scoreof-not-choice', (d) => { d.scores[0].expr = { scoreOf: 'n1' }; }, 'scores[0].expr.scoreOf', 'type', true);
add('round-places', (d) => { d.scores[0].expr = { round: [{ answer: 'n1' }, { num: 7 }] }; }, 'scores[0].expr.round[1]', 'range', true);
add('round-places-expression', (d) => { d.scores[0].expr = { round: [{ answer: 'n1' }, { answer: 'n1' }] }; }, 'scores[0].expr.round[1]', 'range', true);
add('literal-too-many-digits', (d) => { d.scores[0].expr = { add: [{ answer: 'n1' }, { num: 1234567890123 }] }; }, 'scores[0].expr.add[1].num', 'range', true);
add('literal-too-many-decimals', (d) => { d.scores[0].expr = { add: [{ answer: 'n1' }, { num: 0.1234567 }] }; }, 'scores[0].expr.add[1].num', 'range', true);
add('flag-dangling', (d) => { d.flags = [{ id: 'f1', text: 'Flag', when: { answered: 'nope' } }]; }, 'flags[0].when.answered', 'reference', true);
add('flag-not-boolean', (d) => { d.flags = [{ id: 'f1', text: 'Flag', when: { scoreOf: 'q1' } }]; }, 'flags[0].when', 'type', true);
add('flag-duplicate-id', (d) => { d.flags = [{ id: 'q1', text: 'Flag', when: { answered: 'q1' } }]; }, 'flags[0].id', 'duplicate', true);
add('count-display', (d) => { d.scores[0].expr = { count: ['q1', 'info'] }; }, 'scores[0].expr.count[1]', 'type', true);

// Caps.
{
  const d = base();
  let e = { answered: 'q1' };
  let p = '';
  for (let i = 0; i < 32; i++) { e = { not: e }; }
  // 33 nodes deep: 32 nots and the answered leaf.
  for (let i = 0; i < 32; i++) p += '.not';
  item(d, 0, 1, 0).enableWhen = e;
  add('too-deep', () => {}, 'sections[0].pages[1].items[0].enableWhen' + p, 'depth', true, JSON.stringify(d) + '\n');
}
{
  // 31 nots and a leaf: exactly 32 deep, valid. Tested from the Go and JS tests directly.
  const d = base();
  let e = { answered: 'q1' };
  for (let i = 0; i < 31; i++) { e = { not: e }; }
  item(d, 0, 1, 0).enableWhen = e;
  fs.writeFileSync(path.join(out, '..', 'valid', 'depth-32.json'), JSON.stringify(d) + '\n');
}
{
  const d = base();
  const args = [];
  for (let i = 0; i < 4095; i++) args.push({ bool: true });
  // total: 1 score node (scoreOf) + 1 or + 4095 = 4097
  item(d, 0, 1, 0).enableWhen = { or: args };
  add('too-many-nodes', () => {}, 'sections[0].pages[1].items[0].enableWhen.or[4094]', 'nodes', true, JSON.stringify(d) + '\n');
}
{
  const d = base();
  const items = [];
  for (let i = 0; i < 495; i++) items.push({ id: 'x' + i, type: 'display', text: '.' });
  // 6 base items + 495 = 501
  d.sections[1].pages[0].items.push(...items);
  add('too-many-items', () => {}, 'sections[1].pages[0].items[495]', 'items', true, JSON.stringify(d) + '\n');
}

fs.writeFileSync(path.join(out, 'cases.json'), JSON.stringify({
  description: 'Invalid definitions: each file has one problem. path and kind are what Parse reports. shared cases are also refused by web/forms-eval.js with the same path and kind.',
  cases,
}, null, 1) + '\n');
console.log(cases.length, 'cases');
