// Runs static/review.js against a minimal fake DOM. No dependencies: only
// Node's built-in vm and assert. Usage: node review_keys.js path/to/review.js
"use strict";
const fs = require("fs");
const vm = require("vm");
const assert = require("assert");

const src = fs.readFileSync(process.argv[2], "utf8");

function el(tag, attrs = {}) {
  const e = { tagName: tag, type: attrs.type || "", attrs, clicks: 0, form: null };
  e.hasAttribute = (n) => n in attrs;
  e.click = () => { e.clicks++; };
  e.focus = () => { env.focused = e; };
  return e;
}

let env;
function load({ keys = [], blank = false, withForm = true } = {}) {
  const handlers = {};
  const field = { value: "0" };
  const submitHandlers = [];
  env = { now: 1000, focused: null, submits: 0, field };
  const form = {
    addEventListener: (n, f) => { if (n === "submit") submitHandlers.push(f); },
    elements: { namedItem: (n) => (n === "latency_ms" ? field : null) },
    requestSubmit() { env.submits++; submitHandlers.forEach((f) => f()); },
  };
  env.form = form;
  const byKey = {};
  keys.forEach((k) => { byKey[k] = el("BUTTON", { "data-key": k }); });
  env.keyEls = byKey;
  const blankEl = blank ? el("INPUT", { type: "text" }) : null;
  const document = {
    querySelector(sel) {
      if (sel === "form[data-latency]") return withForm ? form : null;
      if (sel === ".blank-input") return blankEl;
      const m = /^\[data-key="(.+)"\]$/.exec(sel);
      return m ? byKey[m[1]] || null : null;
    },
    addEventListener(n, f) { handlers[n] = f; },
  };
  const window = { addEventListener() {} };
  vm.runInNewContext(src, { document, window, performance: { now: () => env.now } });
  env.press = (key, o = {}) => {
    const e = { key, target: o.target || el("BODY"), repeat: !!o.repeat, shiftKey: !!o.shift,
      ctrlKey: !!o.ctrl, metaKey: false, altKey: false, defaultPrevented: false,
      preventDefault() { this.defaultPrevented = true; } };
    handlers.keydown(e);
    return e;
  };
  env.blank = blankEl;
  return env;
}

const tests = {
  "digit, Space and Enter on the page click the element that declares them"() {
    const e = load({ keys: ["2", "Space", "Enter"] });
    for (const [pressed, want] of [["2", "2"], [" ", "Space"], ["Enter", "Enter"]]) {
      const ev = e.press(pressed);
      assert.strictEqual(e.keyEls[want].clicks, 1, want);
      assert.ok(ev.defaultPrevented, want);
    }
  },
  "an unmapped key is left alone"() {
    const e = load({ keys: ["1"] });
    assert.ok(!e.press("7").defaultPrevented);
    assert.ok(!e.press("a").defaultPrevented);
  },
  "digits and Space typed into a text box or textarea are typing"() {
    const e = load({ keys: ["1", "Space"] });
    for (const t of [el("INPUT", { type: "text" }), el("TEXTAREA", {})]) {
      assert.ok(!e.press("1", { target: t }).defaultPrevented);
      assert.ok(!e.press(" ", { target: t }).defaultPrevented);
    }
    assert.strictEqual(e.keyEls["1"].clicks + e.keyEls.Space.clicks, 0);
  },
  "Enter in a text box is left to native submission"() {
    const e = load({ keys: ["Enter"] });
    const ev = e.press("Enter", { target: el("INPUT", { type: "text" }) });
    assert.ok(!ev.defaultPrevented);
    assert.strictEqual(e.keyEls.Enter.clicks, 0);
  },
  "Enter in the open answer box submits its form; Shift+Enter does not"() {
    const e = load({ keys: ["Space"] });
    const ta = el("TEXTAREA", { "data-submit-enter": "" });
    ta.form = e.form;
    const ev = e.press("Enter", { target: ta });
    assert.ok(ev.defaultPrevented);
    assert.strictEqual(e.submits, 1);
    assert.ok(!e.press("Enter", { target: ta, shift: true }).defaultPrevented);
    assert.strictEqual(e.submits, 1);
    const plain = el("TEXTAREA", {});
    plain.form = e.form;
    assert.ok(!e.press("Enter", { target: plain }).defaultPrevented);
    assert.strictEqual(e.submits, 1);
  },
  "digits pick a checkbox, but Space on it is its native toggle"() {
    const e = load({ keys: ["1", "Space"] });
    const box = el("INPUT", { type: "checkbox" });
    assert.ok(e.press("1", { target: box }).defaultPrevented);
    assert.ok(!e.press(" ", { target: box }).defaultPrevented);
  },
  "Enter and Space on a focused button or link are native"() {
    const e = load({ keys: ["Enter", "Space"] });
    for (const tag of ["BUTTON", "A"]) {
      assert.ok(!e.press("Enter", { target: el(tag) }).defaultPrevented);
      assert.ok(!e.press(" ", { target: el(tag) }).defaultPrevented);
    }
  },
  "held keys and modified keys are ignored"() {
    const e = load({ keys: ["3"] });
    assert.ok(!e.press("3", { repeat: true }).defaultPrevented);
    assert.ok(!e.press("3", { ctrl: true }).defaultPrevented);
    assert.strictEqual(e.keyEls["3"].clicks, 0);
  },
  "latency_ms is the time from load to submit"() {
    const e = load();
    e.now = 3500;
    e.form.requestSubmit();
    assert.strictEqual(e.field.value, "2500");
  },
  "a page without the answer form still loads"() {
    load({ withForm: false });
  },
  "the first cloze blank is focused on load"() {
    const e = load({ blank: true });
    assert.strictEqual(e.focused, e.blank);
  },
};

let failed = 0;
for (const [name, fn] of Object.entries(tests)) {
  try { fn(); console.log("ok   " + name); } catch (err) {
    failed++;
    console.log("FAIL " + name + "\n     " + err.message);
  }
}
process.exit(failed ? 1 : 0);
