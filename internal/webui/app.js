// Browser controller is loaded separately from the pure report view model.
import { normalizeReport, filterItems, pageItems, statusInfo, formatBytes, warningText, metadataRows } from './view-model.mjs';

// Each mounted interface owns its request state; tests inject a DOM and transport.
export function mountApp(document, fetch = globalThis.fetch) {
  const byId = (id) => document.getElementById(id);
  const form = byId('compare-form');
  const fields = byId('roots-fields');
  const compareButton = byId('compare-button');
  const cancelButton = byId('cancel-button');
  const activity = byId('activity');
  const reportPanel = byId('report-panel');
  const errorPanel = byId('error-panel');
  const emptyPanel = byId('empty-panel');
  const hasBase = byId('has-base');
  const baseRoot = byId('base-root');
  const search = byId('path-search');
  const filter = byId('status-filter');
  const token = document.querySelector('meta[name="figmobil-token"]')?.content;
  let activeJob = null;
  let report = null;
  let currentPage = 1;

  function element(tag, className, text) {
    const node = document.createElement(tag);
    if (className) node.className = className;
    if (text !== undefined) node.textContent = text;
    return node;
  }

  function setActivity(message, busy = false) {
    activity.textContent = message;
    activity.classList.toggle('busy', busy);
  }

  function clearReport() {
    report = null;
    reportPanel.hidden = true;
    byId('file-rows').replaceChildren();
    byId('summary-grid').replaceChildren();
    byId('warning-list').replaceChildren();
    currentPage = 1;
  }

  function clearError() {
    errorPanel.hidden = true;
    byId('error-message').textContent = '';
    byId('error-code').textContent = '';
  }

  function setBusy(busy) {
    fields.disabled = busy;
    compareButton.disabled = busy;
    compareButton.textContent = busy ? 'Сравнение…' : 'Сравнить копии →';
    cancelButton.hidden = !busy;
    cancelButton.disabled = false;
    form.setAttribute('aria-busy', String(busy));
  }

  function parametersChanged() {
    if (activeJob) return;
    if (report || !errorPanel.hidden) {
      clearReport();
      clearError();
      emptyPanel.hidden = false;
      setActivity('Параметры изменились. Нажмите «Сравнить копии», чтобы получить новый отчёт.');
    }
  }

  function syncBaseField() {
    byId('base-field').hidden = !hasBase.checked;
    baseRoot.required = hasBase.checked;
  }

  syncBaseField();
  // Browsers can restore form controls on initial navigation or from bfcache.
  document.defaultView?.addEventListener('pageshow', () => {
    syncBaseField();
    parametersChanged();
  });
  hasBase.addEventListener('change', () => {
    syncBaseField();
    parametersChanged();
    if (hasBase.checked) baseRoot.focus();
  });
  for (const id of ['local-root', 'incoming-root', 'base-root']) byId(id).addEventListener('input', parametersChanged);

  form.addEventListener('submit', async (event) => {
    event.preventDefault();
    if (activeJob) return;
    syncBaseField();
    if (!form.reportValidity()) return;
    const request = {
      localRoot: byId('local-root').value.trim(),
      incomingRoot: byId('incoming-root').value.trim(),
      baseRoot: hasBase.checked ? baseRoot.value.trim() : '',
    };
    clearReport();
    clearError();
    emptyPanel.hidden = true;
    search.value = '';
    filter.value = 'all';
    const job = { controller: new AbortController() };
    activeJob = job;
    setBusy(true);
    cancelButton.focus();
    setActivity('Сравниваем файлы. Обе папки остаются без изменений…', true);
    try {
      if (!token || token === '__FIGMOBIL_TOKEN__') throw new Error('Сессия figmobil не настроена. Откройте локальный адрес из окна приложения.');
      const response = await fetch('/api/compare', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'X-Figmobil-Token': token },
        body: JSON.stringify(request),
        signal: job.controller.signal,
        credentials: 'same-origin',
        cache: 'no-store',
        redirect: 'error',
      });
      let body;
      try { body = await response.json(); }
      catch { throw new Error('Не удалось прочитать ответ локального приложения. Повторите сравнение.'); }
      if (!response.ok) {
        const failure = new Error(typeof body?.error?.message === 'string' ? body.error.message : 'Локальное приложение отклонило сравнение.');
        failure.code = typeof body?.error?.code === 'string' ? body.error.code : `HTTP_${response.status}`;
        throw failure;
      }
      if (activeJob !== job || job.controller.signal.aborted) return;
      report = normalizeReport(body);
      renderReport();
      setActivity(report.complete ? `Сравнение завершено. Файлов в отчёте: ${report.counts.total}. Файлы не изменялись.` : 'Сравнение завершено с неполным отчётом. Проверьте предупреждения.');
      byId('report-heading').focus();
    } catch (error) {
      if (activeJob !== job) return;
      clearReport();
      emptyPanel.hidden = false;
      if (!job.controller.signal.aborted) {
        byId('error-message').textContent = error instanceof TypeError ? 'Не удалось связаться с локальным приложением. Проверьте, что оно запущено, и повторите сравнение.' : error instanceof Error ? error.message : 'Не удалось выполнить сравнение.';
        byId('error-code').textContent = typeof error?.code === 'string' ? `Код: ${error.code}` : '';
        errorPanel.hidden = false;
        setActivity('Сравнение не завершено. Проверьте пути и сообщение об ошибке.');
        byId('error-heading').focus();
      }
    } finally {
      if (activeJob === job) {
        if (job.controller.signal.aborted) {
          clearReport();
          clearError();
          emptyPanel.hidden = false;
          setActivity('Сравнение отменено. Файлы не изменялись. Можно запустить новое сравнение.');
        }
        activeJob = null;
        setBusy(false);
        if (job.controller.signal.aborted) compareButton.focus();
      }
    }
  });

  cancelButton.addEventListener('click', () => {
    if (!activeJob) return;
    cancelButton.disabled = true;
    activeJob.controller.abort();
    setActivity('Отменяем сравнение…');
    // The browser request settles through the same finally block as any other job.
  });

  function renderReport() {
    reportPanel.hidden = false;
    emptyPanel.hidden = true;
    byId('report-description').textContent = report.hasBase ? 'Три копии · сравнение с общей базой' : 'Две копии · без общей базы изменения требуют проверки';
    byId('report-state').textContent = report.complete ? 'Отчёт готов' : 'Неполный отчёт';
    byId('incomplete-notice').hidden = report.complete;
    const summary = byId('summary-grid');
    for (const [key, label] of [['total', 'Всего файлов'], ['same', 'Совпадают'], ['changed', 'С различиями'], ['attention', 'Требуют внимания']]) {
      const card = element('div', `summary-card${key === 'attention' && report.counts.attention ? ' attention' : ''}`);
      card.append(element('strong', '', String(report.counts[key])), element('span', '', label));
      summary.append(card);
    }
    byId('warnings').hidden = !report.warnings.length;
    const warningList = byId('warning-list');
    for (const warning of report.warnings.slice(0, 100)) warningList.append(element('li', '', warningText(warning)));
    if (report.warnings.length > 100) warningList.append(element('li', '', `Показаны первые 100 предупреждений из ${report.warnings.length}.`));
    renderRows();
  }

  function metadataCard(title, metadata) {
    const card = element('section', 'metadata-card');
    card.append(element('h3', '', title));
    const rows = metadataRows(metadata);
    if (!rows) {
      card.append(element('p', 'metadata-absent', 'Файла нет в этой копии.'));
      return card;
    }
    const list = element('dl');
    for (const [name, value] of rows) list.append(element('dt', '', name), element('dd', '', value));
    card.append(list);
    return card;
  }

  function renderRows() {
    if (!report) return;
    const filtered = filterItems(report.items, search.value, filter.value);
    const page = pageItems(filtered, currentPage);
    currentPage = page.page;
    const rows = byId('file-rows');
    const fragment = document.createDocumentFragment();
    page.items.forEach((item, index) => {
      const info = statusInfo(item.status);
      const row = element('tr');
      const pathCell = element('td');
      pathCell.append(element('div', 'file-path', item.path));
      if (item.reason) pathCell.append(element('div', 'file-reason', item.reason));
      if (item.status === 'incoming-missing' || item.status === 'local-only') pathCell.append(element('div', 'file-reason', 'Отсутствие во входящей копии не означает удаление.'));
      const statusCell = element('td');
      statusCell.append(element('span', `status-badge ${info.tone}`, info.label));
      if (item.representationChanged) statusCell.append(element('span', 'representation', 'Изменился формат файла'));
      const actionCell = element('td');
      const toggle = element('button', 'detail-toggle', 'Подробнее');
      toggle.type = 'button';
      toggle.setAttribute('aria-label', `Метаданные: ${item.path}`);
      toggle.setAttribute('aria-expanded', 'false');
      const detailId = `file-detail-${currentPage}-${index}`;
      toggle.setAttribute('aria-controls', detailId);
      actionCell.append(toggle);
      row.append(pathCell, statusCell, element('td', 'size', formatBytes(item.local?.size)), element('td', 'size', formatBytes(item.incoming?.size)), actionCell);
      const detailRow = element('tr', 'detail-row');
      detailRow.id = detailId;
      detailRow.hidden = true;
      const detailCell = element('td');
      detailCell.colSpan = 5;
      const grid = element('div', 'metadata-grid');
      if (report.hasBase) grid.append(metadataCard('Общая база', item.base));
      grid.append(metadataCard('Локальная копия', item.local), metadataCard('Входящая копия', item.incoming));
      detailCell.append(grid);
      detailRow.append(detailCell);
      toggle.addEventListener('click', () => {
        detailRow.hidden = !detailRow.hidden;
        toggle.setAttribute('aria-expanded', String(!detailRow.hidden));
        toggle.textContent = detailRow.hidden ? 'Подробнее' : 'Свернуть';
      });
      fragment.append(row, detailRow);
    });
    rows.replaceChildren(fragment);
    byId('no-matches').hidden = !!filtered.length;
    byId('result-count').textContent = filtered.length ? `Показано ${page.from}–${page.to} из ${page.total}${page.total !== report.items.length ? ` · всего ${report.items.length}` : ''}` : `Найдено 0 · всего ${report.items.length}`;
    byId('page-count').textContent = `${page.page} / ${page.pages}`;
    byId('previous-page').disabled = page.page <= 1;
    byId('next-page').disabled = page.page >= page.pages;
  }

  function filterChanged() { currentPage = 1; renderRows(); }
  search.addEventListener('input', filterChanged);
  filter.addEventListener('change', filterChanged);
  byId('previous-page').addEventListener('click', () => { currentPage--; renderRows(); });
  byId('next-page').addEventListener('click', () => { currentPage++; renderRows(); });
}

if (typeof document !== 'undefined') mountApp(document);
