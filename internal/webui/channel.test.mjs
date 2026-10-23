import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';
import { mountChannel } from './channel.mjs';

// Boundary double for the real controller, not a browser or native validation.
class Element {
  constructor(tag, document) {
    this.tagName = tag.toUpperCase(); this.document = document; this.children = []; this.listeners = new Map(); this.attributes = new Map();
    this.hidden = false; this.disabled = false; this.checked = false; this.required = false; this.value = ''; this.text = ''; this.selected = false;
  }
  set textContent(value) { this.text = String(value); this.children = []; }
  get textContent() { return this.text + this.children.map((child) => child.textContent).join(''); }
  setAttribute(key, value) { this.attributes.set(key, String(value)); }
  getAttribute(key) { return this.attributes.get(key) ?? null; }
  removeAttribute(key) { this.attributes.delete(key); }
  append(...children) { for (const child of children) child.tagName === '#FRAGMENT' ? this.append(...child.children) : this.children.push(child); }
  replaceChildren(...children) { this.text = ''; this.children = []; this.append(...children); }
  addEventListener(type, listener) { if (!this.listeners.has(type)) this.listeners.set(type, []); this.listeners.get(type).push(listener); }
  focus() { if (!this.disabled) this.document.activeElement = this; }
  select() { this.selected = true; }
  async dispatch(type) { await Promise.all((this.listeners.get(type) || []).map((listener) => listener({ preventDefault() {} }))); }
}

function fixture({ clipboard = null, restoredJoin = false } = {}) {
  const nodes = new Map();
  const document = {
    activeElement: null,
    getElementById(id) { assert.ok(nodes.has(id), `Missing HTML node: ${id}`); return nodes.get(id); },
    createElement(tag) { return new Element(tag, document); },
    createDocumentFragment() { return new Element('#fragment', document); },
    querySelector() { return { content: 'synthetic-session-token' }; },
  };
  document.defaultView = new Element('window', document);
  const html = readFileSync(new URL('./index.html', import.meta.url), 'utf8');
  for (const [, tag, attributes, id] of html.matchAll(/<([a-z][\w-]*)\b([^>]*\bid="([^"]+)"[^>]*)>/gi)) {
    assert.equal(nodes.has(id), false, `Duplicate HTML id: ${id}`);
    const element = new Element(tag, document);
    for (const property of ['hidden', 'disabled', 'required', 'checked']) element[property] = new RegExp(`(?:^|\\s)${property}(?:\\s|$|=)`).test(attributes);
    nodes.set(id, element);
  }
  const get = (id) => document.getElementById(id);
  get('channel-mode-join').checked = restoredJoin;
  get('channel-mode-create').checked = !restoredJoin;
  get('channel-repeat-number').value = '1';
  for (const id of ['channel-setup-form', 'channel-receive-form']) get(id).reportValidity = () => true;
  let time = 0;
  let timerId = 0;
  const timers = new Map();
  const requests = [];
  const fetch = (url, options) => {
    let resolve, reject;
    const promise = new Promise((yes, no) => { resolve = yes; reject = no; });
    requests.push({ url, options, resolve, reject });
    return promise;
  };
  const app = mountChannel(document, fetch, { clipboard, now: () => time, setTimer(callback) { timers.set(++timerId, callback); return timerId; }, clearTimer(id) { timers.delete(id); } });
  return { document, get, requests, app, async finish(body, status = 200) { const request = requests.at(-1); request.resolve({ ok: status < 400, status, async json() { return body; } }); await Promise.resolve(); await Promise.resolve(); await Promise.resolve(); }, advance(ms) { time += ms; const pending = [...timers.values()]; timers.clear(); for (const callback of pending) callback(); } };
}

const paired = (extra = {}) => ({ id: 'p1', name: 'Synthetic', deviceName: 'A', root: 'C:\\Synthetic', excludes: [], stage: 'paired', confirmed: true, peerConfirmed: true, baseId: 'b1', ...extra });
const result = (profile = null, extra = {}) => ({ profiles: profile ? [{ id: profile.id, name: profile.name }] : [], ...(profile ? { profile } : {}), ...extra });
async function loaded(profile = null, options) { const ui = fixture(options); await ui.finish(result(profile)); await ui.app.ready; return ui; }

test('mount reads local state once; typing, restored mode and pageshow do not transmit', async () => {
  const ui = await loaded(null, { restoredJoin: true });
  assert.equal(ui.requests.length, 1);
  assert.deepEqual(JSON.parse(ui.requests[0].options.body), { action: 'state' });
  assert.equal(ui.document.activeElement, null, 'initial readonly state does not steal focus');
  assert.equal(ui.get('channel-invite-field').hidden, false);
  assert.equal(ui.get('channel-invite').required, true);
  await ui.get('channel-root').dispatch('input');
  await ui.document.defaultView.dispatch('pageshow');
  ui.advance(100000);
  assert.equal(ui.requests.length, 1);
  ui.app.destroy();
});

test('explicit create preserves root and exclusions; duplicate submissions produce one request', async () => {
  const ui = await loaded();
  ui.get('channel-name').value = 'Demo'; ui.get('channel-device').value = 'Laptop'; ui.get('channel-root').value = 'C:\\Demo'; ui.get('channel-excludes').value = 'local\nassets\nlocal';
  await ui.get('channel-setup-form').dispatch('submit');
  await ui.get('channel-setup-form').dispatch('submit');
  assert.equal(ui.requests.length, 2);
  assert.deepEqual(JSON.parse(ui.requests[1].options.body), { action: 'create', name: 'Demo', deviceName: 'Laptop', root: 'C:\\Demo', excludes: ['local', 'assets'] });
  assert.equal(ui.get('channel-cancel').hidden, true, 'state-changing request cannot promise abort');
  await ui.finish(result(paired({ stage: 'awaiting-peer', confirmed: false, peerConfirmed: false, baseId: '', outgoing: { kind: 'invite', parts: 1, next: 0, awaitingAck: false } })));
  assert.equal(ui.get('channel-output-field').hidden, true, 'invitation is queued, not issued by create');
  ui.app.destroy();
});

test('only explicit next issues text; countdown never fetches or issues the next part', async () => {
  const outgoing = { kind: 'data', parts: 2, next: 0, awaitingAck: true };
  const ui = await loaded(paired({ outgoing, stage: 'awaiting-ack' }));
  const work = ui.app.perform('next');
  assert.equal(await ui.app.perform('next'), false);
  await ui.finish(result(paired({ outgoing: { ...outgoing, next: 1 }, stage: 'awaiting-ack' }), { text: 'synthetic-frame' }));
  await work;
  assert.equal(ui.get('channel-output').value, 'synthetic-frame');
  assert.equal(ui.get('channel-next').disabled, true);
  assert.equal(await ui.app.perform('next'), false);
  ui.advance(3000);
  assert.equal(ui.get('channel-next').disabled, false);
  assert.equal(ui.requests.length, 2);
  assert.equal(ui.get('channel-output').value, 'synthetic-frame');
  ui.app.destroy();
});

test('unsafe backend output outside a gated action is never displayed', async () => {
  const ui = await loaded(paired());
  const work = ui.app.perform('state');
  await ui.finish(result(paired(), { text: '<script>unsafe</script>' }));
  await work;
  assert.equal(ui.get('channel-output').value, '');
  assert.equal(ui.get('channel-error').hidden, false);
  assert.equal(ui.get('channel-export').disabled, true, 'state must be refreshed after invalid response');
  ui.app.destroy();
});

test('preview renders text safely and caps rows; apply captures reviewed id and cannot be aborted as readonly', async () => {
  const input = { id: 'in1', received: 1, total: 1, ready: true, kind: 'data' };
  const ui = await loaded(paired({ incoming: input, stage: 'incoming' }));
  const preview = ui.app.perform('preview');
  const items = Array.from({ length: 205 }, (_, i) => ({ path: i ? `f${i}` : '<img src=x onerror=1>', status: 'incoming-change', action: 'update', reason: 'Synthetic' }));
  await ui.finish(result(paired({ incoming: input, stage: 'incoming' }), { plan: { id: 'reviewed-plan', items, canApply: true } }));
  await preview;
  assert.equal(ui.get('channel-plan-rows').children.length, 100);
  assert.equal(ui.get('channel-plan-rows').children[0].children[0].textContent, '<img src=x onerror=1>');
  assert.equal(ui.get('channel-plan-rows').children[0].children[0].children.length, 0);
  assert.equal(ui.get('channel-apply').disabled, true);
  ui.get('channel-apply-confirm').checked = true;
  await ui.get('channel-apply-confirm').dispatch('change');
  assert.equal(ui.get('channel-apply').disabled, false);
  await ui.get('channel-apply').dispatch('click');
  assert.deepEqual(JSON.parse(ui.requests.at(-1).options.body), { action: 'apply', profileId: 'p1', planId: 'reviewed-plan' });
  assert.equal(ui.get('channel-plan').hidden, true);
  await ui.get('channel-cancel').dispatch('click');
  assert.equal(ui.requests.at(-1).options.signal.aborted, false);
  await ui.finish(result(paired({ outgoing: { kind: 'ack', parts: 1, next: 0, awaitingAck: false } })));
  assert.equal(ui.get('channel-output').value, '');
  ui.app.destroy();
});

test('cancelled preview ignores late success and leaves no stale plan', async () => {
  const incoming = { id: 'in1', received: 1, total: 1, ready: true, kind: 'data' };
  const ui = await loaded(paired({ incoming, stage: 'incoming' }));
  const work = ui.app.perform('preview');
  await ui.get('channel-cancel').dispatch('click');
  assert.equal(ui.requests.at(-1).options.signal.aborted, true);
  await ui.finish(result(paired({ incoming, stage: 'incoming' }), { plan: { id: 'late', items: [], canApply: true } }));
  await work;
  assert.equal(ui.get('channel-plan').hidden, true);
  assert.match(ui.get('channel-activity').textContent, /отменено/);
  assert.equal(ui.document.activeElement, ui.get('channel-refresh'));
  ui.app.destroy();
});

test('rate rejection clears old text, preserves pasted input, and waits for a new click', async () => {
  const outgoing = { kind: 'confirm', parts: 1, next: 1, awaitingAck: false };
  const ui = await loaded(paired({ outgoing }));
  ui.get('channel-input').value = 'not-yet-imported';
  const work = ui.app.perform('repeat', { partNumber: 1 });
  await ui.finish({ error: { code: 'RATE_LIMIT', message: 'Подождите', retryAfterMs: 12000 } }, 429);
  await work;
  assert.equal(ui.get('channel-input').value, 'not-yet-imported');
  assert.equal(ui.get('channel-output').value, '');
  assert.equal(ui.get('channel-repeat').disabled, true);
  ui.advance(12000);
  assert.equal(ui.get('channel-repeat').disabled, false);
  assert.equal(ui.requests.length, 2);
  ui.app.destroy();
});

test('explicit import sends pasted text without trimming or silently importing on input', async () => {
  const ui = await loaded(paired());
  const raw = '  synthetic-frame\n';
  ui.get('channel-input').value = raw;
  await ui.get('channel-input').dispatch('input');
  assert.equal(ui.requests.length, 1);
  await ui.get('channel-receive-form').dispatch('submit');
  assert.deepEqual(JSON.parse(ui.requests.at(-1).options.body), { action: 'receive', text: raw, profileId: 'p1' });
  await ui.finish(result(paired()));
  assert.equal(ui.get('channel-input').value, '');
  ui.app.destroy();
});

test('clipboard rejection selects the existing gated text for manual copy without network', async () => {
  const ui = await loaded(paired({ outgoing: { kind: 'data', parts: 1, next: 0, awaitingAck: true } }));
  const work = ui.app.perform('next');
  await ui.finish(result(paired({ outgoing: { kind: 'data', parts: 1, next: 1, awaitingAck: true } }), { text: 'frame' }));
  await work;
  await ui.get('channel-copy').dispatch('click');
  assert.equal(ui.get('channel-output').selected, true);
  assert.match(ui.get('channel-copy-status').textContent, /Ctrl\+C/);
  assert.equal(ui.requests.length, 2);
  ui.app.destroy();
});

test('explicit profile selection clears text and issues a single readonly state request', async () => {
  const ui = await loaded(paired({ outgoing: { kind: 'data', parts: 1, next: 0, awaitingAck: true } }));
  const work = ui.app.perform('next');
  await ui.finish(result(paired({ outgoing: { kind: 'data', parts: 1, next: 1, awaitingAck: true } }), { text: 'frame' })); await work;
  ui.get('channel-profile').value = 'p2';
  await ui.get('channel-profile').dispatch('change');
  assert.equal(ui.get('channel-output').value, '');
  assert.deepEqual(JSON.parse(ui.requests.at(-1).options.body), { action: 'state', profileId: 'p2' });
  await ui.finish(result(paired({ id: 'p2', name: 'Second' })));
  assert.equal(ui.requests.length, 3);
  assert.equal(ui.get('channel-current-heading').textContent, 'Second');
  ui.app.destroy();
});
