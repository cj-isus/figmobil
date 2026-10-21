const STATUSES = Object.freeze({
  same: { label: 'Совпадает', tone: 'same', attention: false },
  added: { label: 'Новый файл', tone: 'changed', attention: false },
  'local-only': { label: 'Только локально', tone: 'attention', attention: true },
  'incoming-change': { label: 'Входящее изменение', tone: 'changed', attention: false },
  'local-change': { label: 'Локальное изменение', tone: 'changed', attention: false },
  conflict: { label: 'Конфликт', tone: 'conflict', attention: true },
  'needs-review': { label: 'Нужна проверка', tone: 'attention', attention: true },
  'incoming-missing': { label: 'Нет во входящей', tone: 'attention', attention: true },
});

export function statusInfo(status) {
  return Object.hasOwn(STATUSES, status) ? STATUSES[status] : { label: 'Неизвестное состояние', tone: 'attention', attention: true };
}

export function normalizeReport(value) {
  if (!value || typeof value !== 'object' || !Array.isArray(value.items) || typeof value.complete !== 'boolean' || typeof value.hasBase !== 'boolean') {
    throw new Error('Сервер вернул непонятный формат отчёта.');
  }
  if (value.items.length > 15000) throw new Error('Отчёт превышает допустимое число строк интерфейса.');
  const items = value.items.map((item) => {
    if (!item || typeof item.path !== 'string' || typeof item.status !== 'string' || typeof item.reason !== 'string') {
      throw new Error('В отчёте есть некорректная запись файла.');
    }
    return { ...item, representationChanged: item.representationChanged === true };
  });
  const counts = { total: items.length, same: 0, changed: 0, attention: 0 };
  for (const item of items) {
    if (item.status === 'same') counts.same++;
    else counts.changed++;
    if (statusInfo(item.status).attention || item.representationChanged) counts.attention++;
  }
  const warnings = Array.isArray(value.warnings) ? value.warnings : [];
  return { items, counts, complete: value.complete, hasBase: value.hasBase, warnings };
}

export function filterItems(items, search = '', filter = 'all') {
  const query = search.trim().toLocaleLowerCase('ru');
  return items.filter((item) => {
    const statusMatch = filter === 'changed' ? item.status !== 'same' : filter === 'attention' ? statusInfo(item.status).attention || item.representationChanged : true;
    return statusMatch && item.path.toLocaleLowerCase('ru').includes(query);
  });
}

export function pageItems(items, page, pageSize = 100) {
  const size = Math.max(1, Math.min(200, Math.floor(pageSize) || 100));
  const pages = Math.max(1, Math.ceil(items.length / size));
  const current = Math.max(1, Math.min(pages, Math.floor(page) || 1));
  const start = (current - 1) * size;
  return { items: items.slice(start, start + size), page: current, pages, from: items.length ? start + 1 : 0, to: Math.min(start + size, items.length), total: items.length };
}

export function formatBytes(value) {
  if (!Number.isSafeInteger(value) || value < 0) return '—';
  if (value < 1024) return `${value} Б`;
  if (value < 1024 * 1024) return `${(value / 1024).toLocaleString('ru-RU', { maximumFractionDigits: 1 })} КБ`;
  return `${(value / (1024 * 1024)).toLocaleString('ru-RU', { maximumFractionDigits: 1 })} МБ`;
}

export function warningText(warning) {
  if (!warning || typeof warning !== 'object') return 'Неизвестное предупреждение.';
  const roots = { local: 'Локальная копия', incoming: 'Входящая копия', base: 'Общая база', comparison: 'Сравнение' };
  const root = Object.hasOwn(roots, warning.root) ? `${roots[warning.root]} · ` : '';
  const path = typeof warning.path === 'string' && warning.path ? `${warning.path}: ` : '';
  const detail = [warning.message, warning.reason, warning.code].find((entry) => typeof entry === 'string' && entry);
  return `${root}${path}${detail || 'Требуется ручная проверка.'}`;
}

export function metadataRows(metadata) {
  if (!metadata || typeof metadata !== 'object') return null;
  const printable = (value) => typeof value === 'string' || typeof value === 'number' ? String(value) : '—';
  const newline = typeof metadata.finalNewline === 'boolean' ? metadata.finalNewline ? 'Есть' : 'Нет' : printable(metadata.finalNewline);
  return [
    ['Размер', formatBytes(metadata.size)], ['Кодировка', printable(metadata.encoding)],
    ['BOM', typeof metadata.bom === 'boolean' ? metadata.bom ? 'Есть' : 'Нет' : printable(metadata.bom)],
    ['Строки', printable(metadata.eol)], ['Конец строки', newline],
    ['Режим', printable(metadata.mode)], ['SHA-256', printable(metadata.sha256)],
  ];
}
