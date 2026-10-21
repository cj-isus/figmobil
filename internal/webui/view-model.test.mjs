import assert from 'node:assert/strict';
import test from 'node:test';
import { normalizeReport, filterItems, pageItems, statusInfo, formatBytes, warningText, metadataRows } from './view-model.mjs';

const item = (path, status, representationChanged = false) => ({ path, status, reason: '', representationChanged });

test('report derives overlapping attention counts from actual items, never server summary', () => {
  const result = normalizeReport({ items: [item('same.ts', 'same'), item('conflict.ts', 'conflict'), item('lines.ts', 'incoming-change', true)], complete: true, hasBase: true, summary: { total: 99 } });
  assert.deepEqual(result.counts, { total: 3, same: 1, changed: 2, attention: 2 });
});

test('unknown backend state stays visible and needs attention', () => {
  assert.equal(statusInfo('__proto__').label, 'Неизвестное состояние');
  assert.equal(statusInfo('new-state').attention, true);
  assert.equal(filterItems([item('file', 'new-state')], '', 'attention').length, 1);
});

test('bad report is rejected rather than displayed as empty success', () => {
  for (const input of [null, {}, { items: [], complete: true }, { items: [null], complete: true, hasBase: false }, { items: [item(42, 'same')], complete: true, hasBase: false }]) assert.throws(() => normalizeReport(input));
  assert.equal(normalizeReport({ items: [], complete: false, hasBase: false }).complete, false);
});

test('search and change filter compose and do not mutate data', () => {
  const items = [item('SRC/Button.ts', 'same'), item('src/Button.css', 'incoming-change'), item('package.json', 'conflict')];
  assert.deepEqual(filterItems(items, ' SRC/button ', 'changed').map((entry) => entry.path), ['src/Button.css']);
  assert.equal(items.length, 3);
  assert.equal(filterItems(items, 'missing').length, 0);
});

test('pagination bounds large reports and clamps stale page after filtering', () => {
  const items = Array.from({ length: 5000 }, (_, index) => item(String(index), 'same'));
  const page = pageItems(items, 3);
  assert.equal(page.items.length, 100);
  assert.equal(page.from, 201);
  assert.equal(page.to, 300);
  assert.equal(page.pages, 50);
  assert.equal(pageItems(items.slice(0, 2), 50).page, 1);
  assert.equal(pageItems(items, 1, 100000).items.length, 200);
  assert.deepEqual(pageItems([], -2).items, []);
});

test('metadata distinguishes zero bytes and absent fields', () => {
  assert.equal(formatBytes(0), '0 Б');
  assert.equal(formatBytes(undefined), '—');
  assert.equal(formatBytes(-1), '—');
  assert.equal(metadataRows(null), null);
  assert.deepEqual(metadataRows({ size: 0, finalNewline: false, bom: true }).slice(0, 5), [['Размер', '0 Б'], ['Кодировка', '—'], ['BOM', 'Есть'], ['Строки', '—'], ['Конец строки', 'Нет']]);
});

test('warning string uses backend message variants and handles absent message', () => {
  assert.equal(warningText({ path: 'src/a.ts', reason: 'Unreadable' }), 'src/a.ts: Unreadable');
  assert.equal(warningText({ code: 'SKIPPED' }), 'SKIPPED');
  assert.equal(warningText({ root: 'local', message: 'Ссылка пропущена' }), 'Локальная копия · Ссылка пропущена');
  assert.equal(warningText({ path: '<script>' }), '<script>: Требуется ручная проверка.');
});
