import { normalizeChannelResponse, controls, lines, stageLabel, kindLabel, planPage, planActionLabel, actionLabel } from './channel-model.mjs';

const ACTIONS = new Set(['state', 'create', 'join', 'receive', 'confirm', 'export', 'next', 'repeat', 'preview', 'apply', 'resolve', 'recover', 'discard']);
const READ_ACTIONS = new Set(['state', 'preview']);

// Mounting reads local state once. Every later request belongs to a user action.
export function mountChannel(document, fetch = globalThis.fetch, options = {}) {
  const byId = (id) => document.getElementById(id);
  const clock = options.now || Date.now;
  const schedule = options.setTimer || globalThis.setTimeout;
  const unschedule = options.clearTimer || globalThis.clearTimeout;
  const clipboard = options.clipboard === undefined ? globalThis.navigator?.clipboard : options.clipboard;
  const token = document.querySelector('meta[name="figmobil-token"]')?.content;
  let profiles = [];
  let profile = null;
  let selectedId = '';
  let plan = null;
  let planNumber = 1;
  let active = null;
  let uncertain = false;
  let retryUntil = 0;
  let timer = null;
  let destroyed = false;

  function node(tag, className, content) {
    const element = document.createElement(tag);
    if (className) element.className = className;
    if (content !== undefined) element.textContent = content;
    return element;
  }

  function announce(message) { byId('channel-activity').textContent = message; }
  function clearError() {
    byId('channel-error').hidden = true;
    byId('channel-error-message').textContent = '';
    byId('channel-error-code').textContent = '';
  }
  function showError(message, code = '', focus = true) {
    byId('channel-error-message').textContent = message;
    byId('channel-error-code').textContent = code ? `Код: ${code}` : '';
    byId('channel-error').hidden = false;
    if (focus) byId('channel-error-heading').focus();
  }
  function clearTransient() {
    plan = null;
    planNumber = 1;
    byId('channel-plan').hidden = true;
    byId('channel-plan-rows').replaceChildren();
    byId('channel-apply-confirm').checked = false;
    byId('channel-output').value = '';
    byId('channel-output-field').hidden = true;
    byId('channel-copy-status').textContent = '';
  }
  function remaining() { return Math.max(0, retryUntil - clock()); }

  function allowed() {
    const state = controls(profile, plan);
    if (uncertain) for (const key of Object.keys(state)) state[key] = false;
    return state;
  }

  function renderControls() {
    const busy = !!active;
    const state = allowed();
    byId('channel-app').setAttribute('aria-busy', String(busy));
    byId('channel-setup-fields').disabled = busy;
    for (const id of ['channel-create', 'channel-new', 'channel-profile', 'channel-refresh']) byId(id).disabled = busy;
    byId('channel-input').disabled = busy;
    byId('channel-deletes').disabled = busy;
    for (const action of ['receive', 'confirm', 'export', 'next', 'repeat', 'preview', 'apply', 'resolve', 'recover', 'discard']) {
      let enabled = !!state[action];
      if (['next', 'repeat'].includes(action)) enabled = enabled && remaining() === 0;
      if (action === 'confirm') enabled = enabled && byId('channel-code-matches').checked;
      if (action === 'resolve') enabled = enabled && byId('channel-resolve-confirm').checked;
      if (action === 'discard') enabled = enabled && byId('channel-discard-confirm').checked;
      if (action === 'apply') enabled = enabled && byId('channel-apply-confirm').checked;
      byId(`channel-${action}`).disabled = busy || !enabled;
    }
    byId('channel-repeat-number').disabled = busy || !state.repeat;
    byId('channel-repeat-number').max = String(profile?.outgoing?.next || 1);
    byId('channel-copy').disabled = busy || !byId('channel-output').value;
    byId('channel-plan-close').disabled = busy;
    byId('channel-code-matches').disabled = busy || !state.confirm;
    byId('channel-resolve-confirm').disabled = busy || !state.resolve;
    byId('channel-discard-confirm').disabled = busy || !state.discard;
    byId('channel-apply-confirm').disabled = busy || !state.apply;
    byId('channel-running').hidden = !busy;
    byId('channel-running-message').textContent = busy ? `${actionLabel(active.action)}…${READ_ACTIONS.has(active.action) ? '' : ' Операция уже отправлена. Закрытие страницы не гарантирует её отмену; дождитесь результата.'}` : '';
    byId('channel-cancel').hidden = !busy || !READ_ACTIONS.has(active.action);
    byId('channel-cancel').disabled = !!active?.controller.signal.aborted;
  }

  function tick() {
    if (timer !== null) unschedule(timer);
    timer = null;
    const milliseconds = remaining();
    byId('channel-countdown').textContent = milliseconds > 0 ? `Следующая выдача не раньше чем через ${Math.ceil(milliseconds / 1000)} с` : '';
    renderControls();
    if (milliseconds > 0 && !destroyed) timer = schedule(tick, Math.min(1000, milliseconds));
  }

  function renderProfiles() {
    const select = byId('channel-profile');
    const fragment = document.createDocumentFragment();
    const placeholder = node('option', '', profiles.length ? 'Выберите проект' : 'Локальных проектов пока нет');
    placeholder.value = '';
    fragment.append(placeholder);
    for (const entry of profiles) {
      const option = node('option', '', entry.name);
      option.value = entry.id;
      fragment.append(option);
    }
    select.replaceChildren(fragment);
    select.value = selectedId;
  }

  function renderPlan() {
    byId('channel-plan').hidden = !plan;
    if (!plan) return;
    const page = planPage(plan.items, planNumber);
    planNumber = page.page;
    const rows = document.createDocumentFragment();
    for (const item of page.items) {
      const row = node('tr');
      row.append(node('td', 'file-path', item.path), node('td', '', planActionLabel(item.action)), node('td', 'plan-reason', item.reason));
      rows.append(row);
    }
    byId('channel-plan-rows').replaceChildren(rows);
    byId('channel-plan-count').textContent = `${page.from}–${page.to} из ${page.total} файлов`;
    byId('channel-plan-page').textContent = `${page.page} / ${page.pages}`;
    byId('channel-plan-prev').disabled = page.page <= 1;
    byId('channel-plan-next').disabled = page.page >= page.pages;
    byId('channel-plan-message').textContent = plan.canApply ? 'План прошёл предварительные проверки. Перед записью приложение повторно проверит локальные файлы.' : 'Применение заблокировано. Изучите конфликты и причины; автоматического объединения нет.';
  }

  function render() {
    renderProfiles();
    byId('channel-setup').hidden = !!profile;
    byId('channel-current').hidden = !profile;
    byId('channel-exchange').hidden = !profile;
    const step = !profile ? 'project' : !profile.verificationCode ? 'pair' : !profile.confirmed || !profile.peerConfirmed ? 'verify' : 'exchange';
    for (const name of ['project', 'pair', 'verify', 'exchange']) {
      const item = byId(`channel-step-${name}`);
      if (name === step) item.setAttribute('aria-current', 'step');
      else item.removeAttribute('aria-current');
    }
    if (profile) {
      byId('channel-current-heading').textContent = profile.name;
      byId('channel-stage').textContent = uncertain ? 'Обновите состояние проекта перед следующим действием' : stageLabel(profile.stage);
      byId('channel-device-value').textContent = profile.deviceName;
      byId('channel-root-value').textContent = profile.root;
      byId('channel-excludes-value').textContent = profile.excludes.length ? profile.excludes.join(', ') : 'Только обязательные исключения приложения';
      byId('channel-base-state').textContent = profile.baseId ? 'Есть согласованная база' : 'Общая база ещё не подтверждена';
      byId('channel-recovery').hidden = !allowed().recover;
      byId('channel-verification').hidden = !profile.verificationCode;
      byId('channel-code').textContent = profile.verificationCode;
      byId('channel-confirmation-state').textContent = `Здесь: ${profile.confirmed ? 'подтверждено' : 'не подтверждено'}. Второе устройство: ${profile.peerConfirmed ? 'подтверждено' : 'подтверждение ещё не принято'}. После сверки передайте подтверждение через текст для Figma.`;
      byId('channel-confirm').hidden = profile.confirmed;
      byId('channel-export').textContent = profile.baseId ? 'Подготовить экспорт изменений' : 'Подготовить начальную базу';
      byId('channel-export-hint').textContent = !profile.confirmed || !profile.peerConfirmed ? 'Экспорт доступен после сверки и обмена подтверждениями обоих устройств.' : profile.outgoing?.awaitingAck ? 'Сначала примите подтверждение применения предыдущего пакета.' : profile.incoming ? 'Сначала завершите обработку входящего пакета.' : !profile.baseId ? 'Первая отправка устанавливает общую базу. Различающиеся существующие файлы на другом ноутбуке потребуют проверки.' : 'Изменения сравниваются с последней согласованной базой.';
      byId('channel-outgoing').hidden = !profile.outgoing;
      byId('channel-outgoing-state').textContent = profile.outgoing ? `${kindLabel(profile.outgoing.kind)} · выдано ${profile.outgoing.next} из ${profile.outgoing.parts} частей${profile.outgoing.awaitingAck ? ' · ожидается подтверждение применения' : ''}` : '';
      byId('channel-next').textContent = profile.outgoing ? `Выдать часть ${Math.min(profile.outgoing.next + 1, profile.outgoing.parts)} из ${profile.outgoing.parts}` : 'Выдать следующую часть';
      byId('channel-incoming-state').textContent = profile.incoming ? `Принято ${profile.incoming.received} из ${profile.incoming.total} частей. ${profile.incoming.ready ? 'Пакет готов: откройте предпросмотр.' : 'Вставьте недостающие части и добавьте их отдельной кнопкой.'}` : 'Входящих частей пока нет. Подтверждение применения также принимается через это поле.';
      byId('channel-manual-merge').hidden = !profile.incoming?.ready;
    }
    renderPlan();
    tick();
  }

  async function perform(action, args = {}, focus = true) {
    if (active || destroyed) return false;
    if (!ACTIONS.has(action)) throw new Error('Неизвестное действие интерфейса.');
    if (!['state', 'create', 'join'].includes(action) && !allowed()[action]) {
      showError('Это действие сейчас недоступно. Проверьте шаг настройки и обновите состояние проекта.');
      return false;
    }
    if (['next', 'repeat'].includes(action) && remaining() > 0) return false;
    clearTransient();
    clearError();
    const job = { action, controller: new AbortController() };
    active = job;
    renderControls();
    announce(`${actionLabel(action)}…`);
    if (focus && READ_ACTIONS.has(action)) byId('channel-cancel').focus();
    try {
      if (!token || token === '__FIGMOBIL_TOKEN__') throw new Error('Откройте локальный адрес из текущего окна figmobil.');
      const body = { ...args, action };
      if (profile && !['create', 'join'].includes(action)) body.profileId = profile.id;
      const response = await fetch('/api/channel', {
        method: 'POST', headers: { 'Content-Type': 'application/json', 'X-Figmobil-Token': token },
        body: JSON.stringify(body), signal: job.controller.signal, credentials: 'same-origin', cache: 'no-store', redirect: 'error',
      });
      let raw;
      try { raw = await response.json(); }
      catch { throw new Error('Локальное приложение вернуло нечитаемый ответ. Обновите состояние проекта.'); }
      if (!response.ok) {
        const error = new Error(typeof raw?.error?.message === 'string' ? raw.error.message : 'Локальное приложение отклонило действие.');
        error.code = typeof raw?.error?.code === 'string' ? raw.error.code : `HTTP_${response.status}`;
        error.retryAfterMs = raw?.error?.retryAfterMs;
        throw error;
      }
      if (active !== job || job.controller.signal.aborted || destroyed) return false;
      const result = normalizeChannelResponse(raw, action);
      profiles = result.profiles;
      profile = result.profile;
      selectedId = profile?.id || '';
      uncertain = false;
      plan = result.plan;
      if (['next', 'repeat'].includes(action) && result.text) retryUntil = Math.max(retryUntil, clock() + 3000);
      byId('channel-code-matches').checked = false;
      byId('channel-resolve-confirm').checked = false;
      byId('channel-discard-confirm').checked = false;
      if (action === 'receive') byId('channel-input').value = '';
      if (result.text) {
        byId('channel-output').value = result.text;
        byId('channel-output-field').hidden = false;
      }
      render();
      announce(result.message || 'Действие завершено. Следующий шаг запускается отдельной кнопкой.');
      if (focus && plan) byId('channel-plan-heading').focus();
      else if (focus && result.text) { byId('channel-output').focus(); byId('channel-output').select(); }
      else if (focus && profile) byId('channel-current-heading').focus();
      return true;
    } catch (error) {
      if (active !== job || destroyed) return false;
      clearTransient();
      if (!job.controller.signal.aborted) {
        if (Number.isFinite(error?.retryAfterMs) && error.retryAfterMs > 0 && error.retryAfterMs <= 86400000) retryUntil = Math.max(retryUntil, clock() + error.retryAfterMs);
        uncertain = uncertain || !['preview', 'next', 'repeat'].includes(action);
        showError(error instanceof TypeError ? 'Не удалось связаться с локальным приложением. Проверьте его окно и обновите состояние проекта.' : error instanceof Error ? error.message : 'Действие не завершено.', typeof error?.code === 'string' ? error.code : '', focus);
        announce('Действие не завершено. Текст и предыдущий план очищены.');
      }
      return false;
    } finally {
      if (active === job) {
        active = null;
        render();
        if (job.controller.signal.aborted) {
          clearTransient();
          announce('Ожидание отменено. Применение файлов этой операцией не запускалось.');
          byId('channel-refresh').focus();
        }
      }
    }
  }

  function setupMode() {
    const joining = byId('channel-mode-join').checked;
    byId('channel-invite-field').hidden = !joining;
    byId('channel-invite').required = joining;
    byId('channel-create').textContent = joining ? 'Проверить и принять приглашение' : 'Создать приглашение';
  }
  for (const id of ['channel-mode-create', 'channel-mode-join']) byId(id).addEventListener('change', setupMode);
  setupMode();
  document.defaultView?.addEventListener('pageshow', setupMode);

  byId('channel-setup-form').addEventListener('submit', (event) => {
    event.preventDefault();
    if (active) return;
    setupMode();
    if (!byId('channel-setup-form').reportValidity()) return;
    const joining = byId('channel-mode-join').checked;
    void perform(joining ? 'join' : 'create', { name: byId('channel-name').value.trim(), deviceName: byId('channel-device').value.trim(), root: byId('channel-root').value.trim(), excludes: lines(byId('channel-excludes').value), ...(joining ? { text: byId('channel-invite').value } : {}) });
  });
  byId('channel-new').addEventListener('click', () => {
    if (active) return;
    profile = null;
    selectedId = '';
    uncertain = false;
    clearTransient(); clearError(); render(); setupMode();
    announce('Укажите свою папку для нового проекта. Существующие проекты сохраняются.');
    byId('channel-setup-heading').focus();
  });
  byId('channel-profile').addEventListener('change', () => {
    if (active) return;
    selectedId = byId('channel-profile').value;
    profile = null;
    clearTransient(); clearError();
    if (selectedId) void perform('state', { profileId: selectedId });
    else render();
  });
  byId('channel-refresh').addEventListener('click', () => { void perform('state', selectedId ? { profileId: selectedId } : {}); });
  byId('channel-cancel').addEventListener('click', () => {
    if (!active || !READ_ACTIONS.has(active.action)) return;
    active.controller.abort();
    announce('Отменяем ожидание…');
    renderControls();
  });
  byId('channel-receive-form').addEventListener('submit', (event) => {
    event.preventDefault();
    if (!active && byId('channel-receive-form').reportValidity()) void perform('receive', { text: byId('channel-input').value });
  });
  byId('channel-confirm').addEventListener('click', () => { if (byId('channel-code-matches').checked) void perform('confirm', { code: profile?.verificationCode }); });
  byId('channel-export').addEventListener('click', () => { void perform('export', { deletes: lines(byId('channel-deletes').value) }); });
  byId('channel-next').addEventListener('click', () => { void perform('next'); });
  byId('channel-repeat').addEventListener('click', () => {
    const partNumber = Number(byId('channel-repeat-number').value);
    if (!Number.isInteger(partNumber) || partNumber < 1 || partNumber > (profile?.outgoing?.next || 0)) { showError('Укажите номер уже выданной части.'); return; }
    void perform('repeat', { partNumber });
  });
  byId('channel-preview').addEventListener('click', () => { void perform('preview'); });
  byId('channel-apply').addEventListener('click', () => { if (plan && byId('channel-apply-confirm').checked) void perform('apply', { planId: plan.id }); });
  byId('channel-resolve').addEventListener('click', () => { if (byId('channel-resolve-confirm').checked) void perform('resolve', { deletes: lines(byId('channel-deletes').value) }); });
  byId('channel-recover').addEventListener('click', () => { void perform('recover'); });
  byId('channel-discard').addEventListener('click', () => { if (byId('channel-discard-confirm').checked) void perform('discard'); });
  for (const id of ['channel-code-matches', 'channel-resolve-confirm', 'channel-discard-confirm', 'channel-apply-confirm']) byId(id).addEventListener('change', renderControls);
  byId('channel-plan-prev').addEventListener('click', () => { planNumber--; renderPlan(); });
  byId('channel-plan-next').addEventListener('click', () => { planNumber++; renderPlan(); });
  byId('channel-plan-close').addEventListener('click', () => { if (!active) { plan = null; byId('channel-apply-confirm').checked = false; render(); byId('channel-preview').focus(); } });
  byId('channel-copy').addEventListener('click', async () => {
    const value = byId('channel-output').value;
    if (!value || active) return;
    try {
      if (!clipboard?.writeText) throw new Error('Clipboard unavailable');
      await clipboard.writeText(value);
      byId('channel-copy-status').textContent = 'Текст скопирован. Вставьте его в Figma вручную.';
    } catch {
      byId('channel-output').focus(); byId('channel-output').select();
      byId('channel-copy-status').textContent = 'Автоматическое копирование недоступно. Текст выделен: нажмите Ctrl+C.';
    }
  });

  render();
  const ready = perform('state', {}, false);
  return {
    ready,
    // Explicit method allows the controller's real request path to be tested offline.
    perform,
    destroy() { destroyed = true; active?.controller.abort(); if (timer !== null) unschedule(timer); },
  };
}

if (typeof document !== 'undefined') mountChannel(document);
