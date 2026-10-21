import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';
import { mountApp } from './app.js';

// A small controller boundary double, not a browser: no layout, native keyboard
// traversal, HTML constraint validation or network. Tests exercise the real
// controller and report model, with deterministic request completion/abort.
class NodeDouble {
  constructor(tag, owner) {
    this.tagName = tag.toUpperCase();
    this.owner = owner;
    this.children = [];
    this.listeners = new Map();
    this.attributes = new Map();
    this.hidden = false;
    this.disabled = false;
    this.checked = false;
    this.required = false;
    this.value = '';
    this.content = '';
    this.classList = { toggle() {} };
    this.text = '';
  }
  set textContent(value) { this.text = String(value); this.children = []; }
  get textContent() { return this.text + this.children.map(child => child.textContent).join(''); }
  setAttribute(name, value) { this.attributes.set(name, String(value)); }
  getAttribute(name) { return this.attributes.get(name) ?? null; }
  append(...nodes) {
    for (const node of nodes) {
      if (node.tagName === '#FRAGMENT') this.append(...node.children);
      else this.children.push(node);
    }
  }
  replaceChildren(...nodes) { this.text = ''; this.children = []; this.append(...nodes); }
  addEventListener(type, listener) {
    if (!this.listeners.has(type)) this.listeners.set(type, []);
    this.listeners.get(type).push(listener);
  }
  focus() { if (!this.disabled) this.owner.activeElement = this; }
  async dispatch(type) {
    const event = { type, defaultPrevented: false, preventDefault() { this.defaultPrevented = true; } };
    await Promise.all((this.listeners.get(type) ?? []).map(listener => listener(event)));
    return event;
  }
}

function fixture({ checked = false, token = 'synthetic-session' } = {}) {
  const html = readFileSync(new URL('./index.html', import.meta.url), 'utf8');
  const nodes = new Map();
  const document = {
    activeElement: null,
    getElementById(id) {
      assert.ok(nodes.has(id), `Controller references missing HTML node ${id}`);
      return nodes.get(id);
    },
    createElement(tag) { return new NodeDouble(tag, document); },
    createDocumentFragment() { return new NodeDouble('#fragment', document); },
    querySelector(selector) {
      assert.equal(selector, 'meta[name="figmobil-token"]');
      return token === null ? null : { content: token };
    },
  };
  document.defaultView = new NodeDouble('window', document);
  for (const match of html.matchAll(/<([a-z][\w-]*)\b([^>]*\bid="([^"]+)"[^>]*)>/gi)) {
    const [, tag, attributes, id] = match;
    assert.equal(nodes.has(id), false, `Duplicate HTML id ${id}`);
    const node = new NodeDouble(tag, document);
    for (const property of ['hidden', 'disabled', 'required']) node[property] = new RegExp(`(?:^|\\s)${property}(?:\\s|$|=)`).test(attributes);
    nodes.set(id, node);
  }
  const get = id => document.getElementById(id);
  get('has-base').checked = checked;
  get('local-root').value = 'C:\\Synthetic\\local';
  get('incoming-root').value = 'C:\\Synthetic\\incoming';
  get('base-root').value = 'C:\\Synthetic\\base';
  get('status-filter').value = 'all';
  // We verify controller behaviour at this boundary, not reimplement native form validation.
  let valid = true;
  get('compare-form').reportValidity = () => valid;
  const requests = [];
  const fetch = (url, options) => {
    let resolve, reject;
    const pending = new Promise((yes, no) => { resolve = yes; reject = no; });
    requests.push({ url, options, resolve, reject });
    return pending;
  };
  mountApp(document, fetch);
  return { document, get, requests, submit: () => get('compare-form').dispatch('submit'), setValid(value) { valid = value; } };
}

const metadata = (size = 5) => ({ size, sha256: '0'.repeat(64), encoding: 'utf-8', bom: 'none', eol: 'LF', finalNewline: true, mode: '0644' });
const result = (items = [{ path: 'same.txt', status: 'same', reason: '', local: metadata(), incoming: metadata() }], extra = {}) => ({ items, complete: true, hasBase: false, warnings: [], ...extra });
const response = body => ({ ok: true, status: 200, async json() { return body; } });
const descendants = node => node.children.flatMap(child => [child, ...descendants(child)]);

test('restored base checkbox matches visibility and validation before any submit', async () => {
  for (const checked of [true, false]) {
    const ui = fixture({ checked });
    assert.equal(ui.get('base-field').hidden, !checked);
    assert.equal(ui.get('base-root').required, checked);
    assert.equal(ui.document.activeElement, null, 'initialization must not steal focus');
    assert.equal(ui.requests.length, 0);
    ui.get('has-base').checked = !checked;
    await ui.get('has-base').dispatch('change');
    assert.equal(ui.get('base-field').hidden, checked);
    assert.equal(ui.get('base-root').required, !checked);
    assert.equal(ui.requests.length, 0);
  }
});

test('pageshow restoration synchronizes base and invalidates a stale report without requesting', async () => {
  const ui = fixture();
  const work = ui.submit();
  ui.requests[0].resolve(response(result()));
  await work;
  ui.get('has-base').checked = true;
  await ui.document.defaultView.dispatch('pageshow');
  assert.equal(ui.get('base-field').hidden, false);
  assert.equal(ui.get('base-root').required, true);
  assert.equal(ui.get('report-panel').hidden, true);
  assert.equal(ui.requests.length, 1);
});

test('idle and parameter changes never request; invalid form and duplicate submit are suppressed', async () => {
  const ui = fixture();
  await ui.get('local-root').dispatch('input');
  assert.equal(ui.requests.length, 0);
  ui.setValid(false);
  await ui.submit();
  assert.equal(ui.requests.length, 0);
  ui.setValid(true);
  const work = ui.submit();
  assert.equal(ui.requests.length, 1);
  assert.equal(ui.get('roots-fields').disabled, true);
  assert.equal(ui.get('compare-button').disabled, true);
  assert.equal(ui.get('cancel-button').hidden, false);
  assert.equal(ui.document.activeElement, ui.get('cancel-button'));
  assert.equal(ui.get('compare-form').getAttribute('aria-busy'), 'true');
  await ui.submit();
  assert.equal(ui.requests.length, 1);
  assert.deepEqual(JSON.parse(ui.requests[0].options.body), { localRoot: 'C:\\Synthetic\\local', incomingRoot: 'C:\\Synthetic\\incoming', baseRoot: '' });
  ui.requests[0].resolve(response(result()));
  await work;
  assert.equal(ui.get('report-panel').hidden, false);
  assert.equal(ui.get('roots-fields').disabled, false);
  assert.equal(ui.get('compare-form').getAttribute('aria-busy'), 'false');
  assert.equal(ui.document.activeElement, ui.get('report-heading'));
});

test('error response clears old rows, preserves safe error code and restores usable controls', async () => {
  const ui = fixture();
  const success = ui.submit();
  ui.requests[0].resolve(response(result()));
  await success;
  assert.equal(ui.get('file-rows').children.length, 2);
  const failed = ui.submit();
  assert.equal(ui.get('report-panel').hidden, true);
  assert.equal(ui.get('file-rows').children.length, 0);
  ui.requests[1].resolve({ ok: false, status: 400, async json() { return { error: { code: 'INVALID_ROOT', message: 'Проверьте путь.' } }; } });
  await failed;
  assert.equal(ui.get('error-panel').hidden, false);
  assert.equal(ui.get('error-message').textContent, 'Проверьте путь.');
  assert.equal(ui.get('error-code').textContent, 'Код: INVALID_ROOT');
  assert.equal(ui.document.activeElement, ui.get('error-heading'));
  assert.equal(ui.get('compare-button').disabled, false);
  assert.equal(ui.get('cancel-button').hidden, true);
});

test('cancel aborts the request and restores focus; a later request can succeed', async () => {
  const ui = fixture();
  const work = ui.submit();
  await ui.get('cancel-button').dispatch('click');
  assert.equal(ui.requests[0].options.signal.aborted, true);
  ui.requests[0].reject(new DOMException('Aborted', 'AbortError'));
  await work;
  assert.equal(ui.get('report-panel').hidden, true);
  assert.equal(ui.get('error-panel').hidden, true);
  assert.match(ui.get('activity').textContent, /отменено/);
  assert.equal(ui.document.activeElement, ui.get('compare-button'));
  const next = ui.submit();
  ui.requests[1].resolve(response(result([])));
  await next;
  assert.equal(ui.get('report-panel').hidden, false);
  assert.equal(ui.get('no-matches').hidden, false);
});

test('late success after cancellation never renders and settles cancellation status', async () => {
  const ui = fixture();
  const work = ui.submit();
  await ui.get('cancel-button').dispatch('click');
  // Intentionally resolve despite the signal: the controller must check the
  // result itself, independently of a cooperative transport rejecting abort.
  ui.requests[0].resolve(response(result()));
  await work;
  assert.equal(ui.get('report-panel').hidden, true);
  assert.equal(ui.get('file-rows').children.length, 0);
  assert.equal(ui.get('empty-panel').hidden, false);
  assert.match(ui.get('activity').textContent, /отменено/);
  assert.equal(ui.document.activeElement, ui.get('compare-button'));
});

test('missing session, network failure, invalid JSON and invalid report are errors, never empty success', async () => {
  for (const kind of ['token', 'network', 'json', 'report']) {
    const ui = fixture({ token: kind === 'token' ? null : 'synthetic-session' });
    const work = ui.submit();
    if (kind === 'token') assert.equal(ui.requests.length, 0);
    if (kind === 'network') ui.requests[0].reject(new TypeError('transport failure'));
    if (kind === 'json') ui.requests[0].resolve({ ok: true, async json() { throw new SyntaxError('invalid JSON'); } });
    if (kind === 'report') ui.requests[0].resolve(response({ items: [] }));
    await work;
    assert.equal(ui.get('error-panel').hidden, false, kind);
    assert.equal(ui.get('report-panel').hidden, true, kind);
    assert.equal(ui.get('compare-button').disabled, false, kind);
    assert.equal(ui.document.activeElement, ui.get('error-heading'), kind);
  }
});

test('metadata toggles, filters compose and changed parameters clear the report without requesting', async () => {
  const ui = fixture();
  const work = ui.submit();
  const hostilePath = '<img src=x onerror=alert(1)>.txt';
  ui.requests[0].resolve(response(result([
    { path: hostilePath, status: 'same', reason: '', local: metadata(), incoming: metadata() },
    { path: 'src/conflict.ts', status: 'conflict', reason: '', local: metadata(), incoming: metadata(7) },
  ])));
  await work;
  const rows = ui.get('file-rows').children;
  assert.equal(rows.length, 4);
  assert.ok(rows[0].textContent.includes(hostilePath), 'untrusted path stays text');
  assert.equal(descendants(rows[0]).some(node => node.tagName === 'IMG'), false);
  const toggle = descendants(rows[0]).find(node => node.tagName === 'BUTTON');
  assert.equal(toggle.getAttribute('aria-expanded'), 'false');
  assert.equal(rows[1].hidden, true);
  await toggle.dispatch('click');
  assert.equal(rows[1].hidden, false);
  assert.equal(toggle.getAttribute('aria-expanded'), 'true');
  await toggle.dispatch('click');
  assert.equal(rows[1].hidden, true);
  ui.get('status-filter').value = 'attention';
  await ui.get('status-filter').dispatch('change');
  assert.equal(ui.get('file-rows').children.length, 2);
  assert.match(ui.get('file-rows').textContent, /src\/conflict.ts/);
  ui.get('path-search').value = 'not-present';
  await ui.get('path-search').dispatch('input');
  assert.equal(ui.get('file-rows').children.length, 0);
  assert.equal(ui.get('no-matches').hidden, false);
  assert.equal(ui.requests.length, 1);
  await ui.get('local-root').dispatch('input');
  assert.equal(ui.get('report-panel').hidden, true);
  assert.equal(ui.get('empty-panel').hidden, false);
  assert.equal(ui.requests.length, 1);
});

test('incomplete empty report is explicit; request fields include restored base', async () => {
  const ui = fixture({ checked: true });
  const work = ui.submit();
  assert.equal(JSON.parse(ui.requests[0].options.body).baseRoot, 'C:\\Synthetic\\base');
  ui.requests[0].resolve(response(result([], { complete: false, hasBase: true, warnings: [{ root: 'local', path: 'link', code: 'LINK_SKIPPED', message: 'Не прочитан.' }] })));
  await work;
  assert.equal(ui.get('report-panel').hidden, false);
  assert.equal(ui.get('incomplete-notice').hidden, false);
  assert.equal(ui.get('warnings').hidden, false);
  assert.match(ui.get('warning-list').textContent, /link/);
  assert.match(ui.get('report-state').textContent, /Неполный/);
});
