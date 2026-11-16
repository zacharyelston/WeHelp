// forms-eval.js: the wehelp.form.v1 evaluator for the staff-web builder
// preview. It mirrors internal/forms (the Go reference) and runs the same
// shared vectors in testdata/forms/ (see web/forms-eval.test.js).
//
// Plain ES2020, no dependencies, no build step, CSP-safe: nothing is
// eval'ed and no Function is constructed. Numbers are exact rationals on
// BigInt. The server's result is authoritative; this is a preview.
//
// Precision note: JSON.parse turns numbers into doubles. Definitions and
// answers are limited to 12 integer and 6 fractional digits; values with more
// than 15 significant digits may lose precision here (never in Go).
//
// In a browser it defines window.WeHelpForms; in Node it is a CommonJS module.
(function (root, factory) {
  if (typeof module === 'object' && module.exports) {
    module.exports = factory();
  } else {
    root.WeHelpForms = factory();
  }
})(typeof self !== 'undefined' ? self : this, function () {
  'use strict';

  var FORMAT = 'wehelp.form.v1';
  var LIMITS = { maxBytes: 200000, maxItems: 500, maxDepth: 32, maxNodes: 4096, maxTextLength: 10000, defaultMaxLength: 4000 };
  var END = 'end';
  var MAX_BITS = 128;
  var OUTPUT_PLACES = 6;
  var compiledForms = new WeakSet();

  var NUM_RE = /^-?(0|[1-9][0-9]{0,11})(\.[0-9]{1,6})?$/;
  var INT_RE = /^-?(0|[1-9][0-9]{0,11})$/;
  var ID_RE = /^[A-Za-z][A-Za-z0-9_.-]{0,63}$/;

  function own(o, k) { return o !== null && typeof o === 'object' && Object.prototype.hasOwnProperty.call(o, k); }
  function dict() { return Object.create(null); }

  // --- errors ---------------------------------------------------------------

  function FormError(path, kind, msg) {
    this.name = 'FormError';
    this.path = path;
    this.kind = kind;
    this.message = path ? path + ': ' + msg : msg;
  }
  FormError.prototype = Object.create(Error.prototype);
  FormError.prototype.constructor = FormError;
  function fail(path, kind, msg) { throw new FormError(path, kind, msg); }

  function join(path, key) { return path === '' ? key : path + '.' + key; }
  function idx(path, i) { return path + '[' + i + ']'; }

  // --- exact rationals --------------------------------------------------------

  function gcd(a, b) {
    if (a < 0n) a = -a;
    if (b < 0n) b = -b;
    while (b !== 0n) { var t = a % b; a = b; b = t; }
    return a;
  }
  function rat(n, d) {
    if (d === undefined) d = 1n;
    if (d < 0n) { n = -n; d = -d; }
    var g = gcd(n, d);
    if (g > 1n) { n /= g; d /= g; }
    return { n: n, d: d };
  }
  function ratAdd(a, b) { return rat(a.n * b.d + b.n * a.d, a.d * b.d); }
  function ratSub(a, b) { return rat(a.n * b.d - b.n * a.d, a.d * b.d); }
  function ratMul(a, b) { return rat(a.n * b.n, a.d * b.d); }
  function ratDiv(a, b) { return rat(a.n * b.d, a.d * b.n); }
  function ratCmp(a, b) {
    var l = a.n * b.d, r = b.n * a.d;
    return l < r ? -1 : l > r ? 1 : 0;
  }
  function ratIsInt(a) { return a.d === 1n; }
  function bitLen(x) {
    if (x < 0n) x = -x;
    return x === 0n ? 0 : x.toString(2).length;
  }
  function inBounds(r) { return bitLen(r.n) <= MAX_BITS && bitLen(r.d) <= MAX_BITS; }

  function parseDecimal(text) {
    if (!NUM_RE.test(text)) return null;
    var neg = text[0] === '-';
    if (neg) text = text.slice(1);
    var parts = text.split('.');
    var frac = parts[1] || '';
    var n = BigInt(parts[0] + frac);
    var d = 10n ** BigInt(frac.length);
    return rat(neg ? -n : n, d);
  }

  // numberText writes a JSON number the way the Go evaluator sees it.
  function numberText(v) {
    if (typeof v !== 'number' || !isFinite(v)) return null;
    if (v === 0) return '0';
    return String(v);
  }

  function floorDiv(n, d) { // d > 0
    var q = n / d;
    if (n % d !== 0n && n < 0n) q -= 1n;
    return q;
  }

  function roundHalfEven(x, places) {
    var scale = 10n ** BigInt(places);
    var sn = x.n * scale, sd = x.d;
    var q = floorDiv(sn, sd);
    var m = sn - q * sd; // 0 <= m < sd
    var twice = 2n * m;
    if (twice > sd || (twice === sd && q % 2n !== 0n)) q += 1n;
    return rat(q, scale);
  }

  function formatDecimal(x) {
    var r = roundHalfEven(x, OUTPUT_PLACES);
    var scale = 10n ** BigInt(OUTPUT_PLACES);
    var scaled = r.n * (scale / r.d); // r.d divides scale
    var neg = scaled < 0n;
    if (neg) scaled = -scaled;
    var s = scaled.toString().padStart(OUTPUT_PLACES + 1, '0');
    var intPart = s.slice(0, s.length - OUTPUT_PLACES);
    var frac = s.slice(s.length - OUTPUT_PLACES).replace(/0+$/, '');
    var out = frac ? intPart + '.' + frac : intPart;
    if (out === '0') return '0';
    return neg ? '-' + out : out;
  }

  function validDate(s) {
    if (typeof s !== 'string' || !/^[0-9]{4}-[0-9]{2}-[0-9]{2}$/.test(s)) return false;
    var y = +s.slice(0, 4), m = +s.slice(5, 7), d = +s.slice(8, 10);
    if (y < 1 || m < 1 || m > 12 || d < 1) return false;
    var days = [31, 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31][m - 1];
    if (m === 2 && y % 4 === 0 && (y % 100 !== 0 || y % 400 === 0)) days = 29;
    return d <= days;
  }

  // --- compile (parse + validate) -------------------------------------------
  //
  // compile checks what the shared negative vectors cover: ids, option
  // values, references, rule ordering, cycles, expression types, operators,
  // number literals and the size caps. Field-level checks (unknown fields,
  // controls, min <= max and so on) are the Go validator's; the server runs it
  // on every save and publish.

  function compile(def) {
    if (def === null || typeof def !== 'object' || Array.isArray(def)) fail('', 'schema', 'definition must be a JSON object');
    if (def.format !== FORMAT) fail('format', 'schema', 'must be "' + FORMAT + '"');
    var f = {
      sections: [], scores: [], allItems: [],
      sectionById: new Map(), pageById: new Map(), itemById: new Map(), scoreById: new Map(),
      ids: new Map(), nodes: 0
    };
    if (!Array.isArray(def.sections) || def.sections.length === 0) fail('sections', 'schema', 'must be a non-empty array');

    function declare(path, id) {
      if (typeof id !== 'string' || !ID_RE.test(id)) fail(path, 'schema', 'bad id');
      if (id === END) fail(path, 'schema', 'id "end" is reserved');
      if (f.ids.has(id)) fail(path, 'duplicate', 'duplicate id "' + id + '" (first at ' + f.ids.get(id) + ')');
      f.ids.set(id, path);
    }
    function decimal(path, v) {
      var t = numberText(v);
      var r = t === null ? null : parseDecimal(t);
      if (r === null) fail(path, typeof v === 'number' ? 'range' : 'schema', 'bad number');
      return r;
    }
    function arr(path, v) {
      if (!Array.isArray(v) || v.length === 0) fail(path, 'schema', 'must be a non-empty array');
      return v;
    }

    var pageIndex = 0;
    def.sections.forEach(function (sd, i) {
      var sp = idx('sections', i);
      if (sd === null || typeof sd !== 'object') fail(sp, 'schema', 'must be an object');
      declare(join(sp, 'id'), sd.id);
      var s = { id: sd.id, index: i, pages: [], next: [], raw: sd };
      f.sectionById.set(s.id, s);
      arr(join(sp, 'pages'), sd.pages).forEach(function (pd, j) {
        var pp = idx(join(sp, 'pages'), j);
        if (pd === null || typeof pd !== 'object') fail(pp, 'schema', 'must be an object');
        declare(join(pp, 'id'), pd.id);
        var pg = { id: pd.id, index: pageIndex++, section: s, items: [], raw: pd, enableWhen: null };
        f.pageById.set(pg.id, pg);
        arr(join(pp, 'items'), pd.items).forEach(function (idf, k) {
          var ip = idx(join(pp, 'items'), k);
          if (f.allItems.length >= LIMITS.maxItems) fail(ip, 'items', 'more than ' + LIMITS.maxItems + ' items');
          if (idf === null || typeof idf !== 'object') fail(ip, 'schema', 'must be an object');
          declare(join(ip, 'id'), idf.id);
          var it = {
            id: idf.id, type: idf.type, page: pg, index: k, seq: f.allItems.length, raw: idf,
            required: idf.required === true, multiple: idf.multiple === true, options: [], enableWhen: null,
            maxLength: typeof idf.maxLength === 'number' ? idf.maxLength : LIMITS.defaultMaxLength
          };
          if (it.type === 'choice') {
            var seen = new Set();
            arr(join(ip, 'options'), idf.options).forEach(function (od, oi) {
              var op = idx(join(ip, 'options'), oi);
              if (od === null || typeof od !== 'object' || typeof od.value !== 'string' || od.value === '') fail(join(op, 'value'), 'schema', 'required');
              if (seen.has(od.value)) fail(join(ip, 'options'), 'duplicate', 'duplicate value "' + od.value + '"');
              seen.add(od.value);
              it.options.push({ value: od.value, score: own(od, 'score') ? decimal(join(op, 'score'), od.score) : null });
            });
          }
          if (it.type === 'integer' || it.type === 'decimal') {
            if (own(idf, 'min')) it.min = decimal(join(ip, 'min'), idf.min);
            if (own(idf, 'max')) it.max = decimal(join(ip, 'max'), idf.max);
          }
          if (it.type === 'date') { it.minDate = idf.min || ''; it.maxDate = idf.max || ''; }
          f.itemById.set(it.id, it);
          f.allItems.push(it);
          pg.items.push(it);
        });
        s.pages.push(pg);
      });
      (sd.next || []).forEach(function (bd) {
        s.next.push({ when: null, section: bd.section, rawWhen: bd.when, hasWhen: own(bd, 'when') });
      });
      f.sections.push(s);
    });

    (def.scores || []).forEach(function (scd, i) {
      var sp = idx('scores', i);
      if (scd === null || typeof scd !== 'object') fail(sp, 'schema', 'must be an object');
      declare(join(sp, 'id'), scd.id);
      var m = scd.missing || {};
      var sc = {
        id: scd.id, index: i, raw: scd, expr: null, maxDep: NO_DEPS,
        missing: { policy: m.policy || 'null', minAnswered: m.minAnswered || 0, places: m.places || 0 },
        bands: (scd.bands || []).map(function (b, j) { return { min: decimal(join(idx(join(sp, 'bands'), j), 'min'), b.min), id: b.id }; })
      };
      f.scoreById.set(sc.id, sc);
      f.scores.push(sc);
    });

    // Expressions: scores first, then rules in document order (as in Go).
    f.scores.forEach(function (sc, i) {
      var path = join(idx('scores', i), 'expr');
      var c = { rule: false, limit: OPEN_END, score: sc, deps: NO_DEPS };
      var r = expr(f, path, sc.raw.expr, 1, c);
      if (r.t.kind !== 'number') fail(path, 'type', 'a score must be a number');
      sc.expr = r.e;
      sc.maxDep = c.deps;
    });
    f.sections.forEach(function (s, i) {
      var sp = idx('sections', i);
      s.pages.forEach(function (pg, j) {
        var pp = idx(join(sp, 'pages'), j);
        if (own(pg.raw, 'enableWhen')) pg.enableWhen = rule(f, join(pp, 'enableWhen'), pg.raw.enableWhen, [pg.index, -1]);
        pg.items.forEach(function (it, k) {
          if (own(it.raw, 'enableWhen')) it.enableWhen = rule(f, join(idx(join(pp, 'items'), k), 'enableWhen'), it.raw.enableWhen, [pg.index, k]);
        });
      });
      var last = s.pages[s.pages.length - 1].index;
      s.next.forEach(function (b, j) {
        var bp = idx(join(sp, 'next'), j);
        if (b.hasWhen) b.when = rule(f, join(bp, 'when'), b.rawWhen, [last, Infinity]);
        if (b.section === END) return;
        var target = f.sectionById.get(b.section);
        if (!target) fail(join(bp, 'section'), 'reference', 'no section "' + b.section + '"');
        if (target.index <= s.index) fail(join(bp, 'section'), 'order', 'next may only go forward');
      });
    });
    compiledForms.add(f);
    return f;
  }

  var NO_DEPS = [-1, -1];
  var OPEN_END = [Infinity, Infinity];
  function less(a, b) { return a[0] < b[0] || (a[0] === b[0] && a[1] < b[1]); }
  function maxPos(a, b) { return less(a, b) ? b : a; }
  function itemPos(it) { return [it.page.index, it.index]; }

  function rule(f, path, raw, limit) {
    var r = expr(f, path, raw, 1, { rule: true, limit: limit, score: null, deps: NO_DEPS });
    if (r.t.kind !== 'boolean') fail(path, 'type', 'a rule must be true or false');
    return r.e;
  }

  function itemRef(f, path, v, c) {
    if (typeof v !== 'string') fail(path, 'schema', 'must be an item id');
    var it = f.itemById.get(v);
    if (!it) fail(path, 'reference', 'no item "' + v + '"');
    if (it.type === 'display') fail(path, 'type', 'item "' + v + '" is a display item and has no answer');
    if (!less(itemPos(it), c.limit)) fail(path, 'order', 'item "' + v + '" isn\'t earlier than this rule');
    c.deps = maxPos(c.deps, itemPos(it));
    return it;
  }

  function answerType(it) {
    switch (it.type) {
      case 'choice': return it.multiple ? { kind: 'set', choice: it } : { kind: 'string', choice: it };
      case 'boolean': return { kind: 'boolean' };
      case 'integer': case 'decimal': return { kind: 'number' };
      case 'date': return { kind: 'date' };
      default: return { kind: 'string' };
    }
  }

  function hasOption(it, v) {
    for (var i = 0; i < it.options.length; i++) if (it.options[i].value === v) return true;
    return false;
  }

  var ARITH = { sum: 1, add: 1, sub: 1, mul: 1, div: 1, round: 1, count: 1 };

  function expr(f, path, raw, depth, c) {
    if (depth > LIMITS.maxDepth) fail(path, 'depth', 'expression is more than ' + LIMITS.maxDepth + ' deep');
    f.nodes++;
    if (f.nodes > LIMITS.maxNodes) fail(path, 'nodes', 'more than ' + LIMITS.maxNodes + ' expression nodes');
    if (raw === null || typeof raw !== 'object' || Array.isArray(raw)) fail(path, 'schema', 'an expression must be an object');
    var keys = Object.keys(raw);
    if (keys.length !== 1) fail(path, 'operator', 'an expression must have exactly one operator');
    var op = keys[0], arg = raw[op], ap = join(path, op);
    var e = { op: op, args: [] };

    function args(min, max) {
      if (!Array.isArray(arg)) fail(ap, 'schema', 'must be an array of expressions');
      if (arg.length < min || (max >= 0 && arg.length > max)) fail(ap, 'operator', op + ' takes ' + (min === max ? '' : 'at least ') + min + ' operands');
      return arg.map(function (a, i) {
        var r = expr(f, idx(ap, i), a, depth + 1, c);
        e.args.push(r.e);
        return r.t;
      });
    }
    function need(i, t, kind) {
      if (t.kind !== kind) fail(idx(ap, i), 'type', op + " can't take a " + t.kind + ' here');
    }
    if (c.rule && own(ARITH, op)) fail(ap, 'operator', 'arithmetic (' + op + ') is only allowed in scores');

    var it, ts, i;
    switch (op) {
      case 'answer':
        it = itemRef(f, ap, arg, c);
        e.ref = it.id;
        return { e: e, t: answerType(it) };
      case 'scoreOf':
        it = itemRef(f, ap, arg, c);
        if (it.type !== 'choice') fail(ap, 'type', 'item "' + it.id + '" isn\'t a choice item');
        it.options.forEach(function (o) { if (o.score === null) fail(ap, 'type', 'item "' + it.id + '" has options without a score'); });
        e.ref = it.id;
        return { e: e, t: { kind: 'number' } };
      case 'score': {
        if (typeof arg !== 'string') fail(ap, 'schema', 'must be a score id');
        var s = f.scoreById.get(arg);
        if (!s) fail(ap, 'reference', 'no score "' + arg + '"');
        if (c.score) {
          if (s.index >= c.score.index) fail(ap, 'order', 'score "' + arg + '" isn\'t defined before this one');
        } else if (!less(s.maxDep, c.limit)) {
          fail(ap, 'order', 'score "' + arg + '" depends on items that aren\'t earlier than this rule');
        }
        c.deps = maxPos(c.deps, s.maxDep);
        e.ref = arg;
        return { e: e, t: { kind: 'number' } };
      }
      case 'num': {
        var t = numberText(arg);
        if (typeof arg !== 'number') fail(ap, 'schema', 'must be a number');
        e.num = t === null ? null : parseDecimal(t);
        if (e.num === null) fail(ap, 'range', 'number must have at most 12 integer and 6 fractional digits, and no exponent');
        return { e: e, t: { kind: 'number', lit: true } };
      }
      case 'str':
        if (typeof arg !== 'string') fail(ap, 'schema', 'must be a string');
        e.str = arg;
        return { e: e, t: { kind: 'string', lit: true } };
      case 'bool':
        if (typeof arg !== 'boolean') fail(ap, 'schema', 'must be true or false');
        e.bool = arg;
        return { e: e, t: { kind: 'boolean', lit: true } };
      case 'eq': case 'ne': case 'lt': case 'le': case 'gt': case 'ge':
        ts = args(2, 2);
        comparable(ap, e.args, ts, op !== 'eq' && op !== 'ne');
        return { e: e, t: { kind: 'boolean' } };
      case 'in': {
        ts = args(2, -1);
        var subj = ts[0], want;
        if (subj.kind === 'string' || subj.kind === 'set') want = 'string';
        else if (subj.kind === 'number') want = 'number';
        else fail(idx(ap, 0), 'type', "in can't test a " + subj.kind);
        for (i = 1; i < ts.length; i++) {
          if (!ts[i].lit || ts[i].kind !== want) fail(idx(ap, i), 'type', 'in takes ' + want + ' literals after its first operand');
          if (subj.choice && !hasOption(subj.choice, e.args[i].str)) fail(idx(ap, i), 'reference', 'item "' + subj.choice.id + '" has no option "' + e.args[i].str + '"');
        }
        return { e: e, t: { kind: 'boolean' } };
      }
      case 'answered':
        it = itemRef(f, ap, arg, c);
        e.ref = it.id;
        return { e: e, t: { kind: 'boolean' } };
      case 'and': case 'or':
        ts = args(1, -1);
        ts.forEach(function (t, j) { need(j, t, 'boolean'); });
        return { e: e, t: { kind: 'boolean' } };
      case 'not': {
        var r = expr(f, ap, arg, depth + 1, c);
        if (r.t.kind !== 'boolean') fail(ap, 'type', "not can't take a " + r.t.kind);
        e.args = [r.e];
        return { e: e, t: { kind: 'boolean' } };
      }
      case 'sum': case 'add': case 'sub': case 'mul': case 'div':
        ts = op === 'sum' ? args(1, -1) : args(2, 2);
        ts.forEach(function (t, j) { need(j, t, 'number'); });
        return { e: e, t: { kind: 'number' } };
      case 'round': {
        ts = args(2, 2);
        need(0, ts[0], 'number');
        var pl = e.args[1];
        if (pl.op !== 'num' || !ratIsInt(pl.num) || pl.num.n < 0n || pl.num.n > 6n) fail(idx(ap, 1), 'range', 'round places must be a num literal from 0 to 6');
        e.places = Number(pl.num.n);
        return { e: e, t: { kind: 'number' } };
      }
      case 'count':
        if (!Array.isArray(arg) || arg.length === 0) fail(ap, 'schema', 'must be a non-empty array of item ids');
        e.refs = arg.map(function (v, j) { return itemRef(f, idx(ap, j), v, c).id; });
        return { e: e, t: { kind: 'number' } };
    }
    fail(path, 'operator', 'unknown operator "' + op + '"');
  }

  function comparable(ap, args, ts, ordered) {
    var i;
    for (i = 0; i < 2; i++) if (ts[i].kind === 'set') fail(idx(ap, i), 'type', 'compare a multiple-choice answer with in');
    for (i = 0; i < 2; i++) {
      var other = ts[1 - i];
      if (ts[i].lit && ts[i].kind === 'string' && other.kind === 'date') {
        if (!validDate(args[i].str)) fail(idx(ap, i), 'type', '"' + args[i].str + '" isn\'t a date (YYYY-MM-DD)');
        ts[i] = { kind: 'date', lit: true };
      }
    }
    if (ts[0].kind !== ts[1].kind) fail(idx(ap, 1), 'type', "can't compare a " + ts[0].kind + ' with a ' + ts[1].kind);
    if (ordered && ts[0].kind !== 'number' && ts[0].kind !== 'date') fail(ap, 'type', 'only numbers and dates can be ordered');
    for (i = 0; i < 2; i++) {
      var o = ts[1 - i];
      if (o.choice && ts[i].lit && !hasOption(o.choice, args[i].str)) fail(idx(ap, i), 'reference', 'item "' + o.choice.id + '" has no option "' + args[i].str + '"');
    }
  }

  // --- evaluation -------------------------------------------------------------

  function nul(reason) { return { nul: true, reason: reason }; }

  function answerValue(it, raw) {
    // returns {ok, v, code}
    if (raw === undefined || raw === null) return { ok: false, code: '' };
    var j, s;
    switch (it.type) {
      case 'display':
        return { ok: false, code: 'not_answerable' };
      case 'choice':
        if (!it.multiple) {
          if (typeof raw !== 'string') return { ok: false, code: 'wrong_type' };
          if (!hasOption(it, raw)) return { ok: false, code: 'unknown_option' };
          return { ok: true, v: { str: raw } };
        }
        if (!Array.isArray(raw)) return { ok: false, code: 'wrong_type' };
        if (raw.length === 0) return { ok: false, code: '' };
        var seen = new Set();
        for (j = 0; j < raw.length; j++) {
          s = raw[j];
          if (typeof s !== 'string') return { ok: false, code: 'wrong_type' };
          if (!hasOption(it, s)) return { ok: false, code: 'unknown_option' };
          if (seen.has(s)) return { ok: false, code: 'duplicate_option' };
          seen.add(s);
        }
        return { ok: true, v: { set: raw.slice() } };
      case 'boolean':
        if (typeof raw !== 'boolean') return { ok: false, code: 'wrong_type' };
        return { ok: true, v: { b: raw } };
      case 'integer': case 'decimal': {
        var t = numberText(raw);
        if (t === null || (it.type === 'integer' && !INT_RE.test(t))) return { ok: false, code: 'wrong_type' };
        var r = parseDecimal(t);
        if (r === null) return { ok: false, code: 'wrong_type' };
        return { ok: true, v: { num: r } };
      }
      case 'date':
        if (!validDate(raw)) return { ok: false, code: 'wrong_type' };
        return { ok: true, v: { str: raw } };
      case 'text':
        if (typeof raw !== 'string') return { ok: false, code: 'wrong_type' };
        if (raw === '') return { ok: false, code: '' };
        return { ok: true, v: { str: raw } };
    }
    return { ok: false, code: 'wrong_type' };
  }

  function Evaluator(f) {
    this.f = f;
    this.eff = new Map();
    this.scores = new Map();
    this.error = null;
  }

  Evaluator.prototype.bound = function (r) {
    if (!inBounds(r)) {
      if (!this.error) this.error = new FormError('', 'overflow', 'a score result exceeds 128-bit numerator/denominator bounds');
      return nul('overflow');
    }
    return { num: r };
  };

  Evaluator.prototype.test = function (x) {
    if (!x) return true;
    var v = this.node(x, null);
    return !v.nul && v.b === true;
  };

  Evaluator.prototype.next = function (s) {
    for (var i = 0; i < s.next.length; i++) {
      var b = s.next[i];
      if (!b.when || this.test(b.when)) return b.section;
    }
    return s.index + 1 < this.f.sections.length ? this.f.sections[s.index + 1].id : END;
  };

  Evaluator.prototype.score = function (sc) {
    if (this.scores.has(sc.id)) return this.scores.get(sc.id);
    var v = this.node(sc.expr, sc.missing);
    var out = { value: null, exact: null, band: null };
    if (v.nul) {
      out.reason = v.reason;
    } else {
      out.exact = v.num;
      out.value = formatDecimal(v.num);
      sc.bands.forEach(function (b) { if (ratCmp(v.num, b.min) >= 0) out.band = b.id; });
    }
    this.scores.set(sc.id, out);
    return out;
  };

  function optionScore(it, v) {
    for (var i = 0; i < it.options.length; i++) if (it.options[i].value === v) return it.options[i].score;
    return rat(0n);
  }

  function compare(a, b) {
    if (a.num) return ratCmp(a.num, b.num);
    if (a.str !== undefined || b.str !== undefined) {
      var x = a.str || '', y = b.str || '';
      return x < y ? -1 : x > y ? 1 : 0;
    }
    return a.b === b.b ? 0 : 1;
  }

  Evaluator.prototype.truth = function (x, policy) {
    var v = this.node(x, policy);
    return !v.nul && v.b === true;
  };

  Evaluator.prototype.node = function (x, policy) {
    var self = this, a, b, v, i, j;
    switch (x.op) {
      case 'answer':
        return this.eff.has(x.ref) ? this.eff.get(x.ref) : nul('missing');
      case 'scoreOf': {
        if (!this.eff.has(x.ref)) return nul('missing');
        v = this.eff.get(x.ref);
        var it = this.f.itemById.get(x.ref);
        if (!it.multiple) return { num: optionScore(it, v.str) };
        var acc = { num: rat(0n) };
        for (i = 0; i < v.set.length; i++) {
          acc = this.bound(ratAdd(acc.num, optionScore(it, v.set[i])));
          if (acc.nul) return acc;
        }
        return acc;
      }
      case 'score': {
        var s = this.score(this.f.scoreById.get(x.ref));
        return s.exact === null ? nul(s.reason) : { num: s.exact };
      }
      case 'num': return { num: x.num };
      case 'str': return { str: x.str };
      case 'bool': return { b: x.bool };
      case 'eq': case 'ne': case 'lt': case 'le': case 'gt': case 'ge': {
        a = this.node(x.args[0], policy);
        b = this.node(x.args[1], policy);
        if (a.nul || b.nul) return { b: false };
        var c = compare(a, b);
        var r = x.op === 'eq' ? c === 0 : x.op === 'ne' ? c !== 0 : x.op === 'lt' ? c < 0 : x.op === 'le' ? c <= 0 : x.op === 'gt' ? c > 0 : c >= 0;
        return { b: r };
      }
      case 'in': {
        var subj = this.node(x.args[0], policy);
        if (subj.nul) return { b: false };
        for (i = 1; i < x.args.length; i++) {
          var lit = x.args[i];
          if (subj.num) {
            if (ratCmp(subj.num, lit.num) === 0) return { b: true };
          } else if (subj.set) {
            for (j = 0; j < subj.set.length; j++) if (subj.set[j] === lit.str) return { b: true };
          } else if (subj.str === lit.str) {
            return { b: true };
          }
        }
        return { b: false };
      }
      case 'answered': return { b: this.eff.has(x.ref) };
      case 'and':
        for (i = 0; i < x.args.length; i++) if (!this.truth(x.args[i], policy)) return { b: false };
        return { b: true };
      case 'or':
        for (i = 0; i < x.args.length; i++) if (this.truth(x.args[i], policy)) return { b: true };
        return { b: false };
      case 'not': return { b: !this.truth(x.args[0], policy) };
      case 'count': {
        var n = 0;
        x.refs.forEach(function (id) { if (self.eff.has(id)) n++; });
        return { num: rat(BigInt(n)) };
      }
      case 'sum': return this.sum(x, policy);
      case 'add': case 'sub': case 'mul': case 'div':
        a = this.node(x.args[0], policy);
        b = this.node(x.args[1], policy);
        if (a.nul) return a;
        if (b.nul) return b;
        if (x.op === 'add') return this.bound(ratAdd(a.num, b.num));
        if (x.op === 'sub') return this.bound(ratSub(a.num, b.num));
        if (x.op === 'mul') return this.bound(ratMul(a.num, b.num));
        if (b.num.n === 0n) return nul('divideByZero');
        return this.bound(ratDiv(a.num, b.num));
      case 'round':
        a = this.node(x.args[0], policy);
        if (a.nul) return a;
        return this.bound(roundHalfEven(a.num, x.places));
    }
    throw new Error('forms-eval: unvalidated operator ' + x.op);
  };

  Evaluator.prototype.sum = function (x, policy) {
    var self = this;
    var vals = x.args.map(function (a) { return self.node(a, policy); });
    var p = policy || { policy: 'null', minAnswered: 0, places: 0 };
    var acc = { num: rat(0n) };
    var answered = 0;
    for (var i = 0; i < vals.length; i++) {
      var v = vals[i];
      if (v.nul) {
        if (p.policy === 'null' || v.reason === 'overflow') return v;
        continue;
      }
      answered++;
      acc = this.bound(ratAdd(acc.num, v.num));
      if (acc.nul) return acc;
    }
    if (answered < p.minAnswered) return nul('belowMinAnswered');
    if (p.policy !== 'prorate') return acc;
    if (answered === 0) return nul('missing');
    if (answered < vals.length) {
      var scaled = this.bound(ratMul(acc.num, rat(BigInt(vals.length))));
      if (scaled.nul) return scaled;
      acc = this.bound(ratDiv(scaled.num, rat(BigInt(answered))));
      if (acc.nul) return acc;
    }
    return this.bound(roundHalfEven(acc.num, p.places));
  };

  function form(defOrForm) {
    return compiledForms.has(defOrForm) ? defOrForm : compile(defOrForm);
  }

  // evaluate walks the form once (see the Go package doc for the rules) and
  // returns {visible, next, path, scores, answers, error}. visible, next and
  // scores are keyed by id; error is a FormError of kind "overflow", or null.
  function evaluate(defOrForm, answers) {
    var f = form(defOrForm);
    answers = answers || {};
    var e = new Evaluator(f);
    var res = { visible: dict(), next: dict(), path: [], scores: dict(), answers: dict(), error: null };
    var target = f.sections[0].id;
    f.sections.forEach(function (s) {
      var on = s.id === target;
      res.visible[s.id] = on;
      s.pages.forEach(function (pg) {
        var pv = on && e.test(pg.enableWhen);
        res.visible[pg.id] = pv;
        pg.items.forEach(function (it) {
          var iv = pv && e.test(it.enableWhen);
          res.visible[it.id] = iv;
          if (!iv || !own(answers, it.id)) return;
          var av = answerValue(it, answers[it.id]);
          if (av.ok) {
            e.eff.set(it.id, av.v);
            res.answers[it.id] = answers[it.id];
          }
        });
      });
      var next = e.next(s);
      res.next[s.id] = next;
      if (on) {
        res.path.push(s.id);
        target = next;
      }
    });
    f.scores.forEach(function (sc) {
      var s = e.score(sc);
      var out = { value: s.value, band: s.band };
      if (s.reason) out.reason = s.reason;
      res.scores[sc.id] = out;
    });
    res.error = e.error;
    return res;
  }

  function rangeProblem(it, v, today) {
    if (it.type === 'integer' || it.type === 'decimal') {
      if (it.min && ratCmp(v.num, it.min) < 0) return ['out_of_range', 'below the minimum ' + formatDecimal(it.min)];
      if (it.max && ratCmp(v.num, it.max) > 0) return ['out_of_range', 'above the maximum ' + formatDecimal(it.max)];
    } else if (it.type === 'date') {
      var min = it.minDate === 'today' ? today : it.minDate;
      var max = it.maxDate === 'today' ? today : it.maxDate;
      if (min && v.str < min) return ['out_of_range', 'before ' + min];
      if (max && v.str > max) return ['out_of_range', 'after ' + max];
    } else if (it.type === 'text') {
      if (Array.from(v.str).length > it.maxLength) return ['too_long', 'longer than ' + it.maxLength + ' characters'];
    }
    return null;
  }

  var PROBLEMS = { not_answerable: 'a display item takes no answer', unknown_option: "not one of the item's options", duplicate_option: 'an option is chosen twice', wrong_type: 'wrong type' };

  function localToday() {
    var d = new Date();
    function two(n) { return (n < 10 ? '0' : '') + n; }
    return d.getFullYear() + '-' + two(d.getMonth() + 1) + '-' + two(d.getDate());
  }

  // checkAnswers mirrors forms.CheckAnswersOn: {answers, errors, result}.
  function checkAnswers(defOrForm, answers, today) {
    var f = form(defOrForm);
    answers = answers || {};
    today = today || localToday();
    var errs = [], bad = new Set(), valid = {};
    Object.keys(answers).sort().forEach(function (id) {
      var raw = answers[id];
      var it = f.itemById.get(id);
      if (!it) {
        errs.push({ item: id, code: 'unknown_item', message: 'no such item' });
        bad.add(id);
        return;
      }
      var av = answerValue(it, raw);
      if (av.code) {
        errs.push({ item: id, code: av.code, message: PROBLEMS[av.code] });
        bad.add(id);
        return;
      }
      if (!av.ok) return;
      var p = rangeProblem(it, av.v, today);
      if (p) {
        errs.push({ item: id, code: p[0], message: p[1] });
        bad.add(id);
        return;
      }
      valid[id] = raw;
    });
    var res = evaluate(f, valid);
    errs = errs.filter(function (e) { return !f.itemById.has(e.item) || res.visible[e.item]; });
    f.allItems.forEach(function (it) {
      if (it.required && res.visible[it.id] && !bad.has(it.id) && !(it.id in res.answers)) {
        errs.push({ item: it.id, code: 'required', message: 'an answer is required' });
      }
    });
    errs.sort(function (x, y) {
      var a = f.itemById.get(x.item), b = f.itemById.get(y.item);
      if (a && b) return a.seq - b.seq;
      if (!!a !== !!b) return a ? -1 : 1;
      return x.item < y.item ? -1 : x.item > y.item ? 1 : 0;
    });
    if (res.error) {
      f.scores.forEach(function (sc) {
        if (res.scores[sc.id].reason === 'overflow') errs.push({ item: sc.id, code: 'overflow', message: "the score can't be computed exactly within bounds" });
      });
    }
    return { answers: res.answers, errors: errs, result: res };
  }

  // compile returns an opaque compiled form that the other functions accept
  // in place of the definition, so a preview doesn't revalidate per keystroke.
  function compileForm(def) {
    return compile(def);
  }

  return {
    FORMAT: FORMAT,
    END: END,
    LIMITS: LIMITS,
    FormError: FormError,
    compile: compileForm,
    evaluate: evaluate,
    visible: function (def, answers, id) { return evaluate(def, answers).visible[id] === true; },
    nextSection: function (def, answers, sectionID) { var n = evaluate(def, answers).next[sectionID]; return n === undefined ? '' : n; },
    scores: function (def, answers) { return evaluate(def, answers).scores; },
    checkAnswers: checkAnswers,
    // exposed for tests
    _formatDecimal: function (text, places) { var r = parseDecimal(text); return formatDecimal(places === undefined ? r : roundHalfEven(r, places)); },
    _validDate: validDate
  };
});
