import assert from 'node:assert/strict';
import test from 'node:test';
import { normalizeChannelResponse, controls, lines, planPage, stageLabel, MAX_FRAME_BYTES } from './channel-model.mjs';

const profile = (extra = {}) => ({ id: 'p1', name: 'Synthetic', deviceName: 'A', root: 'C:\\Synthetic', stage: 'paired', confirmed: true, peerConfirmed: true, baseId: 'base1', ...extra });
const response = (extra = {}) => ({ profiles: [{ id: 'p1', name: 'Synthetic' }], profile: profile(), ...extra });

test('protocol frames are accepted only after gated next/repeat and within UTF-8 byte budget', () => {
  for (const action of ['state', 'create', 'join', 'confirm', 'receive', 'export', 'apply', 'resolve', 'recover', 'discard']) assert.throws(() => normalizeChannelResponse(response({ text: 'frame' }), action));
  assert.equal(normalizeChannelResponse(response({ text: 'frame' }), 'next').text, 'frame');
  assert.equal(normalizeChannelResponse(response({ text: 'frame' }), 'repeat').text, 'frame');
  assert.throws(() => normalizeChannelResponse(response({ text: 'я'.repeat(MAX_FRAME_BYTES) }), 'next'));
});

test('both confirmations, pending input, unsent output and recovery gate export', () => {
  assert.equal(controls(profile()).export, true);
  for (const extra of [{ confirmed: false }, { peerConfirmed: false }, { incoming: { ready: false, received: 1 } }, { outgoing: { parts: 2, next: 1, awaitingAck: false } }, { outgoing: { parts: 1, next: 1, awaitingAck: true } }, { needsRecovery: true }, { stage: 'unknown' }]) assert.equal(controls(profile(extra)).export, false);
  assert.equal(controls(profile({ baseId: '' })).export, true, 'initial base requires the same explicit export action');
});

test('confirmation does not imply peer confirmation; apply requires a ready input and valid plan', () => {
  const localOnly = profile({ stage: 'verify', verificationCode: '123456', confirmed: true, peerConfirmed: false });
  assert.equal(controls(localOnly).confirm, false);
  assert.equal(controls(localOnly).export, false);
  const incoming = profile({ stage: 'incoming', incoming: { ready: true, received: 3, total: 3 } });
  assert.equal(controls(incoming).preview, true);
  assert.equal(controls(incoming, { canApply: false }).apply, false);
  assert.equal(controls(incoming, { canApply: true }).apply, true);
  assert.equal(controls(profile(), { canApply: true }).apply, false);
  assert.equal(controls(profile({ stage: 'recovery' })).recover, true);
});

test('malformed counters and non-preview plans fail explicitly', () => {
  assert.throws(() => normalizeChannelResponse(response({ profile: profile({ outgoing: { next: 5, parts: 2 } }) }), 'state'));
  assert.throws(() => normalizeChannelResponse(response({ profile: profile({ incoming: { received: 2, total: 1 } }) }), 'state'));
  assert.throws(() => normalizeChannelResponse(response({ plan: { id: 'plan', items: [], canApply: true } }), 'state'));
  assert.match(stageLabel('new-state'), /Неизвестное/);
});

test('exclusion lines preserve paths, deduplicate and do not invent deletion rules', () => {
  assert.deepEqual(lines(' local-data\r\n\nassets/heavy\nlocal-data\n '), ['local-data', 'assets/heavy']);
});

test('large plans are paginated and a filtered/shrunken plan clamps the page', () => {
  const items = Array.from({ length: 5000 }, (_, i) => ({ path: String(i) }));
  assert.equal(planPage(items, 20).items.length, 100);
  assert.equal(planPage(items, 20).from, 1901);
  assert.equal(planPage(items.slice(0, 1), 20).page, 1);
  assert.equal(planPage([], 1).total, 0);
});
