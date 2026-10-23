// Public browser contract. Backend validates authority, trust and filesystem changes.
export const MAX_FRAME_BYTES = 250000;
export const PLAN_PAGE_SIZE = 100;

const STAGES = Object.freeze({
  'awaiting-peer': 'Ожидается ответ на приглашение',
  verify: 'Сверьте коды на двух экранах приложения',
  paired: 'Оба устройства подтверждены',
  'awaiting-ack': 'Ожидается подтверждение применения',
  incoming: 'Входящий пакет готов к проверке',
  'merge-required': 'Требуется ручное объединение изменений',
  recovery: 'Требуется восстановление операции',
});

const KINDS = Object.freeze({ invite: 'Приглашение', reply: 'Ответ на приглашение', confirm: 'Подтверждение устройства', data: 'Изменения проекта', ack: 'Подтверждение применения' });

const text = (value, fallback = '') => typeof value === 'string' ? value : fallback;
const count = (value) => Number.isSafeInteger(value) && value >= 0;

export function stageLabel(stage) {
  return Object.hasOwn(STAGES, stage) ? STAGES[stage] : 'Неизвестное состояние: обновите состояние проекта';
}

export function kindLabel(kind) {
  return Object.hasOwn(KINDS, kind) ? KINDS[kind] : 'Сообщение протокола';
}

export function lines(value) {
  return [...new Set(String(value).split(/\r?\n/).map((line) => line.trim()).filter(Boolean))];
}

export function normalizeChannelResponse(value, action) {
  if (!value || typeof value !== 'object' || !Array.isArray(value.profiles)) throw new Error('Не удалось прочитать состояние проектов.');
  const profiles = value.profiles.map((profile) => {
    if (!profile || typeof profile.id !== 'string' || typeof profile.name !== 'string') throw new Error('Список проектов содержит некорректную запись.');
    return { id: profile.id, name: profile.name };
  });
  let profile = null;
  if (value.profile != null) {
    const source = value.profile;
    if (typeof source !== 'object' || typeof source.id !== 'string' || typeof source.stage !== 'string' || typeof source.root !== 'string') throw new Error('Некорректное состояние выбранного проекта.');
    profile = {
      id: source.id, name: text(source.name), deviceName: text(source.deviceName), root: source.root,
      excludes: Array.isArray(source.excludes) ? source.excludes.filter((entry) => typeof entry === 'string') : [],
      stage: source.stage, verificationCode: text(source.verificationCode), confirmed: source.confirmed === true,
      peerConfirmed: source.peerConfirmed === true, baseId: text(source.baseId), needsRecovery: source.needsRecovery === true,
      outgoing: null, incoming: null,
    };
    if (source.outgoing) {
      const item = source.outgoing;
      if (!count(item.parts) || !count(item.next) || item.next > item.parts) throw new Error('Некорректный счётчик исходящих частей.');
      profile.outgoing = { kind: text(item.kind), parts: item.parts, next: item.next, awaitingAck: item.awaitingAck === true };
    }
    if (source.incoming) {
      const item = source.incoming;
      if (!count(item.received) || !count(item.total) || item.received > item.total) throw new Error('Некорректный счётчик входящих частей.');
      profile.incoming = { id: text(item.id), received: item.received, total: item.total, kind: text(item.kind), ready: item.ready === true };
    }
  }
  let frame = '';
  if (value.text != null && value.text !== '') {
    if (!['next', 'repeat'].includes(action) || typeof value.text !== 'string' || new TextEncoder().encode(value.text).length > MAX_FRAME_BYTES) throw new Error('Приложение отклонило выдачу текста вне разрешённой операции или лимита.');
    frame = value.text;
  }
  let plan = null;
  if (value.plan != null) {
    const source = value.plan;
    if (typeof source.id !== 'string' || !Array.isArray(source.items) || source.items.length > 15000 || typeof source.canApply !== 'boolean') throw new Error('Некорректный план применения.');
    const items = source.items.map((item) => {
      if (!item || typeof item.path !== 'string' || typeof item.status !== 'string' || typeof item.action !== 'string' || typeof item.reason !== 'string') throw new Error('План содержит некорректную запись файла.');
      return { path: item.path, status: item.status, action: item.action, reason: item.reason };
    });
    if (action !== 'preview') throw new Error('План получен вне явного предпросмотра.');
    plan = { id: source.id, items, canApply: source.canApply };
  }
  return { message: text(value.message), profiles, profile, text: frame, plan };
}

export function controls(profile, plan = null) {
  const known = profile && Object.hasOwn(STAGES, profile.stage);
  const recovery = profile?.needsRecovery === true || profile?.stage === 'recovery';
  const usable = !!known && !recovery;
  const paired = !!(usable && profile.confirmed && profile.peerConfirmed);
  const outgoing = profile?.outgoing;
  const incoming = profile?.incoming;
  return {
    receive: usable,
    confirm: usable && !!profile.verificationCode && !profile.confirmed,
    export: paired && !incoming && !outgoing?.awaitingAck && !(outgoing && outgoing.next < outgoing.parts) && profile.stage === 'paired',
    next: usable && !!outgoing && outgoing.next < outgoing.parts,
    repeat: usable && !!outgoing && outgoing.next > 0,
    preview: paired && !!incoming?.ready,
    apply: paired && !!incoming?.ready && !!plan?.canApply,
    resolve: paired && !!incoming?.ready,
    recover: !!profile && recovery,
    discard: usable && (!!incoming || !!outgoing),
  };
}

export function planPage(items, page = 1) {
  const pages = Math.max(1, Math.ceil(items.length / PLAN_PAGE_SIZE));
  const current = Math.max(1, Math.min(pages, Math.floor(page) || 1));
  const offset = (current - 1) * PLAN_PAGE_SIZE;
  return { items: items.slice(offset, offset + PLAN_PAGE_SIZE), page: current, pages, total: items.length, from: items.length ? offset + 1 : 0, to: Math.min(offset + PLAN_PAGE_SIZE, items.length) };
}

export function actionLabel(action) {
  const names = { create: 'Создаём приглашение', join: 'Проверяем приглашение', receive: 'Проверяем входящий текст', confirm: 'Подтверждаем сверку', export: 'Готовим исходящие изменения', next: 'Выдаём следующую часть', repeat: 'Повторно выдаём часть', preview: 'Проверяем план применения', apply: 'Применяем подтверждённый план', resolve: 'Готовим результат ручного объединения', recover: 'Проверяем восстановление', discard: 'Сбрасываем незавершённый обмен', state: 'Загружаем локальное состояние' };
  return Object.hasOwn(names, action) ? names[action] : 'Выполняем действие';
}

export function planActionLabel(action) {
  const names = { create: 'Создать', update: 'Изменить', replace: 'Изменить', write: 'Записать', delete: 'Удалить', remove: 'Удалить', same: 'Без записи', skip: 'Пропустить', conflict: 'Конфликт', none: 'Без записи' };
  return Object.hasOwn(names, action) ? names[action] : `Проверить действие: ${action}`;
}
