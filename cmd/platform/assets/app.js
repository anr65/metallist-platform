'use strict';

const $ = (selector, root = document) => root.querySelector(selector);
const $$ = (selector, root = document) => [...root.querySelectorAll(selector)];
const esc = value => String(value ?? '').replace(/[&<>"']/g, char => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[char]));
const money = value => {
  const [whole, fraction = '00'] = String(value ?? '0.00').split('.');
  return `${esc(whole.replace(/\B(?=(\d{3})+(?!\d))/g, ' '))},${esc(fraction.padEnd(2, '0'))} ₽`;
};
const rate = bp => `${(Number(bp || 0) / 100).toFixed(2).replace('.', ',')} %`;
const dateLabel = value => value ? new Intl.DateTimeFormat('ru-RU', {dateStyle:'medium', timeZone:'Europe/Moscow'}).format(new Date(value)) : '—';
const todayMoscow = () => {
  const parts = new Intl.DateTimeFormat('en-GB', {timeZone:'Europe/Moscow', year:'numeric', month:'2-digit', day:'2-digit'}).formatToParts(new Date());
  const get = type => parts.find(part => part.type === type).value;
  return `${get('year')}-${get('month')}-${get('day')}`;
};
const values = form => Object.fromEntries(new FormData(form).entries());
const option = (items, label) => (items || []).map(item => `<option value="${esc(item.id)}">${esc(label(item))}</option>`).join('');
const entityName = (items, id, fallback = 'Не указано') => (items || []).find(item => item.id === id)?.name || fallback;
const cardName = id => (state.catalog.cards || []).find(card => card.id === id)?.mask || 'Карта не найдена';
const custodianName = id => entityName(state.catalog.custodians, id, 'Ответственный не найден');
const roleNames = {chief:'Главный администратор',operator:'Операционист',collector:'Сборщик',accountant:'Бухгалтер',sysadmin:'Системный администратор',auditor:'Аудитор'};
const statusNames = {draft:'Черновик',preview:'Ожидает подтверждения',posted:'Подтверждено',reversed:'Сторнировано',rejected:'Отклонено'};
const kindNames = {withdrawal:'Снятие с карты',handover:'Передача наличных',repayment:'Возврат мерчанту',expense:'Расход',injection:'Внесение собственных средств',shortage:'Недостача',writeoff:'Списание недостачи',surplus:'Излишек наличных',surplus_income:'Признание излишка доходом',surplus_merchant:'Излишек мерчанта',surplus_shortage:'Излишек в погашение недостачи',surplus_return:'Возврат излишка мерчанту',collection:'Погашение задолженности',recovery:'Возврат после списания',forgive_injection:'Прощение долга по внесению'};
const accountNames = {'1100':'На картах','1200':'Наличные у ответственных','1210':'Наличные у главного администратора','1220':'Деньги в пути','1300':'Долг мерчанта нам','1390':'Прочая дебиторская задолженность','1400':'Недостачи к взысканию','2100':'Долг мерчантам','2110':'Отдельный долг по излишку','2200':'Долг за собственные внесения','2290':'Прочая кредиторская задолженность','2300':'Невыясненные поступления','3100':'Начальный капитал','3200':'Капитал от прощения долга','4100':'Комиссионная выручка','4200':'Прочие доходы','5100':'Операционные расходы','5200':'Агентские расходы','5300':'Банковские расходы','5400':'Потери и недостачи','5900':'Прочие расходы'};
const actionNames = {registry_upload:'Загрузка реестра',registry_confirm:'Подтверждение реестра',registry_reverse:'Сторно реестра',draft_create:'Создание черновика',draft_confirm:'Подтверждение операции',draft_reverse:'Сторно операции',payment_request_create:'Создание запроса на карты',payment_request_export:'Скачивание реестра карт',password_change:'Смена пароля',password_reset:'Сброс пароля',observation:'Наблюдение остатка',report_approve:'Утверждение отчёта',catalog_create:'Создание записи',card_assign:'Назначение карты',telegram_link:'Привязка Telegram',login:'Вход',manual_rate_approve:'Разовая ставка',manual_rate_correction:'Исправление разовой ставки',tariff_confirm:'Изменение тарифа',tariff_adjustment:'Перерасчёт тарифа',demo_registry_seed:'Демонстрационный реестр'};
const reasonNames = {role:'Недостаточно прав',card_not_assigned:'Карта не назначена пользователю',card_not_unique_or_unknown:'Карта не найдена или неоднозначна'};
const state = {user:null, page:'overview', catalog:{}, registries:[], drafts:[], requests:[]};
const operatorHiddenPages = new Set(['overview','reports','expenses','audit']);
const pages = new Set(['overview','requests','registries','expenses','money','catalog','reports','audit','account']);
function setFormError(form, message = '') {
  let box = $('.form-error', form);
  if (!box) {
    box = document.createElement('p');
    box.className = 'form-error';
    box.setAttribute('role', 'alert');
    form.prepend(box);
  }
  box.textContent = message;
  box.hidden = !message;
}
function setSubmitBusy(form, busy, label = '') {
  const button = $('button[type="submit"], .form-actions button, .button-row button', form);
  if (!button) return;
  if (busy) {
    button.dataset.label = button.textContent;
    button.disabled = true;
    button.setAttribute('aria-busy', 'true');
    button.textContent = label || 'Сохранение…';
  } else {
    button.disabled = false;
    button.removeAttribute('aria-busy');
    button.textContent = button.dataset.label || button.textContent;
  }
}
async function submitForm(form, work, label) {
  if (!form.reportValidity()) return;
  setFormError(form);
  setSubmitBusy(form, true, label);
  try { await work(); }
  catch (error) { setFormError(form, error.message); form.querySelector(':invalid')?.focus(); }
  finally { setSubmitBusy(form, false); }
}
function enhanceSelects(root = document) {
  $$('select[data-searchable]', root).forEach(select => {
    if (select.dataset.enhanced) return;
    select.dataset.enhanced = 'true';
    const label = document.querySelector(`label[for="${CSS.escape(select.id)}"]`)?.textContent || 'Поиск по списку';
    const search = document.createElement('input');
    search.type = 'search'; search.className = 'select-search'; search.autocomplete = 'off'; search.spellcheck = false;
    search.placeholder = 'Поиск…'; search.setAttribute('aria-label', `${label}: поиск`); search.setAttribute('aria-controls', select.id);
    const sync = () => { search.value = select.selectedOptions[0]?.textContent || ''; };
    search.addEventListener('focus', () => { search.value = ''; [...select.options].forEach(item => { item.hidden = false; }); });
    search.addEventListener('input', () => {
      const query = search.value.trim().toLocaleLowerCase('ru-RU');
      [...select.options].forEach(item => { item.hidden = !!query && !item.textContent.toLocaleLowerCase('ru-RU').includes(query); });
    });
    search.addEventListener('keydown', event => { if (event.key === 'ArrowDown') { event.preventDefault(); select.focus(); } });
    select.addEventListener('change', sync); select.before(search); sync();
  });
}
function friendlyError(message) {
  const known = {card_not_assigned:'Эта карта не назначена вам.',card_not_unique_or_unknown:'Карта не найдена или её маска неоднозначна.',invalid_amount:'Проверьте сумму.',"invalid input syntax for type uuid":'Выберите запись из списка.',"duplicate key value violates unique constraint":'Такая запись уже существует.',"permission denied":'У вас нет прав на это действие.'};
  const text = String(message || '');
  for (const [code,translation] of Object.entries(known)) if (text.includes(code)) return translation;
  return text || 'Не удалось выполнить действие';
}

async function api(path, body, form = false) {
  const options = {credentials:'same-origin', headers:{'X-CSRF':'1'}};
  if (body !== undefined) {
    options.method = 'POST';
    if (form) options.body = body;
    else { options.headers['Content-Type'] = 'application/json'; options.body = JSON.stringify(body); }
  }
  const response = await fetch(path, options);
  const result = await response.json();
  if (!response.ok) throw new Error(friendlyError(result.error));
  return result;
}
function notify(message, error = false) {
  const box = $('#notice');
  box.textContent = message;
  box.classList.toggle('error', error);
  box.hidden = false;
  clearTimeout(notify.timer);
  notify.timer = setTimeout(() => box.hidden = true, 6000);
}
function table(columns, rows, emptyTitle = 'Записей пока нет', emptyText = 'Они появятся после первого действия.') {
  if (!rows?.length) return `<div class="empty-state"><strong>${esc(emptyTitle)}</strong>${esc(emptyText)}</div>`;
  return `<div class="table-wrap"><table><thead><tr>${columns.map(c => `<th>${esc(c.title)}</th>`).join('')}</tr></thead><tbody>${rows.map(row => `<tr>${columns.map(c => `<td>${c.render ? c.render(row) : esc(row[c.key] ?? '—')}</td>`).join('')}</tr>`).join('')}</tbody></table></div>`;
}
function page(title, subtitle, body, actions = '') {
  $('#main-content').innerHTML = `<div class="page-enter"><div class="page-head"><div><h1>${esc(title)}</h1><p>${esc(subtitle)}</p></div>${actions ? `<div class="page-actions">${actions}</div>` : ''}</div>${body}</div>`;
  $('#breadcrumb').textContent = title;
  $$('.side-nav button').forEach(button => {
    const active = button.dataset.page === state.page;
    button.classList.toggle('active', active);
    button.setAttribute('aria-current', active ? 'page' : 'false');
  });
  $('#sidebar').classList.remove('open');
  if (matchMedia('(max-width: 768px)').matches) {
    $('#sidebar').setAttribute('aria-hidden', 'true');
    $('#menu-backdrop').hidden = true;
    $('.workspace').inert = false;
    $('#open-menu').setAttribute('aria-expanded', 'false');
  }
  enhanceSelects($('#main-content'));
  $('#main-content').focus({preventScroll:true});
}
function section(title, content, caption = '') {
  return `<section class="section"><div class="section-title"><h2>${esc(title)}</h2>${caption ? `<p>${esc(caption)}</p>` : ''}</div>${content}</section>`;
}
function field(name, title, input, hint = '', wide = false) {
  return `<div class="field${wide ? ' wide' : ''}"><label for="${esc(name)}">${esc(title)}</label>${input}${hint ? `<small>${esc(hint)}</small>` : ''}</div>`;
}
function input(name, type = 'text', attrs = '') { return `<input id="${esc(name)}" ${/\bname=/.test(attrs) ? '' : `name="${esc(name)}"`} type="${esc(type)}" ${attrs}>`; }
function select(name, items, label, attrs = '') { return `<select id="${esc(name)}" name="${esc(name)}" data-searchable ${attrs}>${option(items, label)}</select>`; }
function status(value) { return `<span class="status ${value === 'posted' ? 'good' : value === 'reversed' || value === 'rejected' ? 'bad' : 'pending'}">${esc(statusNames[value] || 'Требует проверки')}</span>`; }
function sourceLabel(kind, id) { return kind === 'card' ? `Карта ${cardName(id)}` : `Наличные · ${custodianName(id)}`; }
async function loadCatalog() { state.catalog = await api('/api/catalog'); return state.catalog; }
async function loadDrafts() { state.drafts = await api('/api/drafts'); return state.drafts; }
async function loadRegistries() { state.registries = await api('/api/registries'); return state.registries; }
async function loadRequests() { state.requests = await api('/api/payment-requests'); return state.requests; }

async function start() {
  try {
    state.user = await api('/api/me');
    $('#login-view').hidden = true;
    $('#app-shell').hidden = false;
    $('#sidebar-user').textContent = state.user.Name;
    $('#role-label').textContent = roleNames[state.user.Role] || 'Пользователь';
    const requestedPage = location.hash.slice(1);
    if (pages.has(requestedPage)) state.page = requestedPage;
    if (state.user.Role === 'operator') {
      $$('[data-page]').filter(button => operatorHiddenPages.has(button.dataset.page)).forEach(button => button.hidden = true);
      if (operatorHiddenPages.has(state.page)) state.page = 'registries';
    }
    if (state.user.Role === 'sysadmin') {
      $$('[data-page="overview"], [data-page="reports"], [data-page="expenses"], [data-page="money"], [data-page="requests"], [data-page="registries"]').forEach(button => button.hidden = true);
      state.page = 'catalog';
    }
    if (state.user.Role === 'collector') {
      $$('[data-page]').filter(button => button.dataset.page !== 'account').forEach(button => button.hidden = true);
      state.page = 'account';
    }
    await show(state.page, false);
  } catch { $('#login-view').hidden = false; }
}
async function show(name, syncURL = true) {
  if (state.user?.Role === 'collector' && name !== 'account') {
    notify('Сборщик работает с назначенными картами через Telegram.', true);
    return;
  }
  if (state.user?.Role === 'operator' && operatorHiddenPages.has(name)) {
    notify('Этот раздел недоступен операционисту.', true);
    return;
  }
  state.page = name;
  if (syncURL && location.hash !== `#${name}`) history.pushState({page:name}, '', `#${name}`);
  try {
    if (name === 'overview') await overviewPage();
    else if (name === 'requests') await requestsPage();
    else if (name === 'registries') await registriesPage();
    else if (name === 'expenses') await expensesPage();
    else if (name === 'money') await moneyPage();
    else if (name === 'catalog') await catalogPage();
    else if (name === 'reports') await reportsPage();
    else if (name === 'audit') await auditPage();
    else if (name === 'account') await accountPage();
  } catch (error) { notify(error.message, true); }
}
$('#login-form').addEventListener('submit', async event => {
  event.preventDefault();
  $('#login-error').hidden = true;
  const form = event.target;
  if (!form.reportValidity()) return;
  setSubmitBusy(form, true, 'Вход…');
  try { await api('/login', values(form)); await start(); }
  catch (error) { $('#login-error').textContent = error.message; $('#login-error').hidden = false; $('#login-name').focus(); }
  finally { setSubmitBusy(form, false); }
});
$$('[data-page]').forEach(button => button.addEventListener('click', () => show(button.dataset.page)));
$('#logout-button').addEventListener('click', async () => { try { await api('/logout', {}); location.reload(); } catch (error) { notify(error.message, true); } });
function setMenu(open) {
  if (!matchMedia('(max-width: 768px)').matches) return;
  $('#sidebar').classList.toggle('open', open);
  $('#sidebar').setAttribute('aria-hidden', String(!open));
  $('#open-menu').setAttribute('aria-expanded', String(open));
  $('#menu-backdrop').hidden = !open;
  $('.workspace').inert = open;
  if (open) $('#close-menu').focus(); else $('#open-menu').focus();
}
function syncMenuState() {
  const mobile = matchMedia('(max-width: 768px)').matches;
  if (mobile) $('#sidebar').setAttribute('aria-hidden', 'true'); else $('#sidebar').removeAttribute('aria-hidden');
  $('#menu-backdrop').hidden = true;
  $('.workspace').inert = false;
  $('#open-menu').setAttribute('aria-expanded', 'false');
}
$('#open-menu').addEventListener('click', () => setMenu(true));
$('#close-menu').addEventListener('click', () => setMenu(false));
$('#menu-backdrop').addEventListener('click', () => setMenu(false));
document.addEventListener('keydown', event => { if (event.key === 'Escape' && $('#sidebar').classList.contains('open')) setMenu(false); });
addEventListener('resize', () => { $('#sidebar').classList.remove('open'); syncMenuState(); });
addEventListener('popstate', () => { const page = location.hash.slice(1); if (pages.has(page) && page !== state.page) show(page, false); });

async function overviewPage() {
  const report = await api('/api/report');
  const summary = report.summary;
  const metrics = [
    ['Деньги на картах и в кассах', summary.card_cash, 'Где находятся сопровождаемые деньги'],
    ['К возврату мерчантам', summary.merchant_payable, 'Подтверждённый долг по реестрам'],
    ['Заработанная комиссия', summary.commission_revenue, 'Выручка без транзитных сумм'],
    ['Прибыль', summary.profit, 'Доходы за вычетом расходов']
  ];
  page('Обзор', 'Главные суммы из подтверждённых операций. Черновики не меняют эти показатели.',
    `<div class="metric-grid">${metrics.map(([label, value, detail]) => `<div class="metric"><span class="metric-label">${esc(label)}</span><strong class="metric-value">${money(value)}</strong><small>${esc(detail)}</small></div>`).join('')}</div>` +
    section('Что сделать сейчас', `<div class="quick-actions"><button class="action-tile" data-action="requests"><strong>Подготовить карты к оплате</strong><span>Создать запрос и скачать файл для мерчанта</span></button><button class="action-tile" data-action="expenses"><strong>Добавить расход</strong><span>Сумма, тип и дата — остальные поля по необходимости</span></button></div>`) +
    section('Требует внимания', `<div class="surface"><p>Расхождений по наблюдаемым остаткам: <strong>${report.observations.filter(item => item.difference !== '0.00').length}</strong>. Передач с разницей между заявленной и принятой суммой: <strong>${report.handover_differences.length}</strong>.</p><button class="button secondary" id="open-reports">Открыть подробный отчёт</button></div>`));
  $$('[data-action]').forEach(button => button.onclick = () => show(button.dataset.action));
  $('#open-reports').onclick = () => show('reports');
}

async function requestsPage(selectedID = '') {
  await Promise.all([loadCatalog(), loadRequests()]);
  const canCreate = state.user.Role === 'chief' || state.user.Role === 'operator';
  const contacts = (state.catalog.payment_contacts || []).filter(item => item.active);
  const form = canCreate ? `<div class="surface"><h2>Новый запрос мерчанта</h2><p class="hint">Один платеж по умолчанию — 250 000 ₽. В режиме общей суммы последняя строка может быть меньше.</p><form id="request-form">
    <div class="form-grid">${field('merchant_id','Мерчант',select('merchant_id',state.catalog.merchants,item => item.name,'required'))}${field('external_ref','Номер запроса',input('external_ref','text','required placeholder="Например, ЗК-2026-01"'))}</div>
    <div class="choice-grid" style="margin-top:18px"><label class="choice"><input type="radio" name="mode" value="count" checked> По количеству платежей</label><label class="choice"><input type="radio" name="mode" value="total"> По общей сумме</label></div>
    <div class="form-grid">${field('payment_count','Сколько платежей',input('payment_count','number','min="1" max="500" value="20" required'))}${field('total','Общая сумма',input('total','text','inputmode="decimal" placeholder="1 500 000,00" disabled'))}${field('per_payment','План на один платеж',input('per_payment','text','inputmode="decimal" value="250000.00" required'),'Можно изменить для этого запроса.')}</div>
    <div id="request-preview" class="callout" style="margin-top:20px">20 платежей по 250 000 ₽. Всего 5 000 000 ₽.</div>
    <p class="hint" style="margin-top:20px">ФИО и телефон подставлены из последнего реестра для каждой карты. Можно изменить каждое поле или выбрать подсказку. Новые значения сохранятся в справочнике.</p>
    <datalist id="request-name-options">${[...new Set([...syntheticFIO,...contacts.map(item=>item.full_name)])].map(value=>`<option value="${esc(value)}"></option>`).join('')}</datalist>
    <datalist id="request-phone-options">${[...new Set(contacts.map(item=>item.phone))].map(value=>`<option value="${esc(value)}"></option>`).join('')}</datalist>
    <div id="request-contact-rows" style="margin-top:20px"></div>
    <div class="form-actions"><button class="button primary">Создать и подготовить XLSX</button></div><p class="form-note">XLSX содержит вымышленные номер карты, ФИО и телефон. Создание запроса не меняет баланс.</p>
  </form></div>` : '<div class="callout">Реестр карт для отправки создают главный администратор и операционист. Вы можете связать ответный реестр с готовым запросом.</div>';
  const list = table([
    {title:'Запрос',render:r => `<strong>${esc(r.external_ref)}</strong><br><small>${esc(r.merchant)}</small>`},
    {title:'Платежей',render:r => `<span class="numeric">${esc(r.payment_count)}</span>`},
    {title:'План',render:r => `<span class="numeric">${money(r.planned_total)}</span>`},
    {title:'Поступило',render:r => `<span class="numeric">${money(r.received_total)}</span>`},
    {title:'',render:r => `<button class="button quiet" data-request="${esc(r.id)}">Открыть</button>`}
  ], state.requests, 'Запросов пока нет', 'Создайте первый запрос на карты для мерчанта.');
  page('Карты к оплате','Подготовьте для мерчанта файл с картами, затем привяжите его ответный реестр.',
    form + section('Подготовленные запросы', list) + `<div id="request-detail"></div>`);
  if (canCreate) bindRequestForm();
  $$('[data-request]').forEach(button => button.onclick = () => showRequest(button.dataset.request));
  if (selectedID) await showRequest(selectedID);
}
function bindRequestForm() {
  const form = $('#request-form');
  const readContacts = () => $$('.request-contact-row').map(row=>({full_name:$('.request-row-name',row).value,phone:$('.request-row-phone',row).value}));
  const renderContacts = plan => {
    const prior = readContacts();
    const cards = state.catalog.request_cards || [];
    $('#request-contact-rows').innerHTML = plan.length ? `<div class="request-rows">${plan.map((amount,index)=>{
      const card = cards[index % cards.length] || {};
      const contact = prior[index] || card;
      return `<div class="request-contact-row"><div class="request-row-summary"><strong>Платёж ${index+1}</strong><span class="mono">${esc(card.mask || 'Нет доступных карт')}</span><span class="numeric">${money((amount/100n).toString()+'.'+(amount%100n).toString().padStart(2,'0'))}</span></div>${field('request-name-'+index,'ФИО',`<input id="request-name-${index}" class="request-row-name" list="request-name-options" value="${esc(contact.full_name||'')}" maxlength="200" required autocomplete="off" placeholder="Выберите или введите ФИО">`)}${field('request-phone-'+index,'Номер телефона',`<input id="request-phone-${index}" class="request-row-phone" type="tel" list="request-phone-options" value="${esc(contact.phone||'')}" required autocomplete="off" placeholder="+7 000 000-00-01">`)}</div>`;
    }).join('')}</div>` : '';
  };
  const refresh = () => {
    const v = values(form);
    const totalMode = v.mode === 'total';
    $('#payment_count').disabled = totalMode;
    $('#payment_count').required = !totalMode;
    $('#total').disabled = !totalMode;
    $('#total').required = totalMode;
    const per = parseCents(v.per_payment);
    const enteredCount = Number(v.payment_count);
    const validCount = Number.isInteger(enteredCount) && enteredCount >= 1 && enteredCount <= 500 ? enteredCount : 0;
    const total = totalMode ? parseCents(v.total) : per === null ? null : BigInt(validCount) * per;
    const count = totalMode ? per && total ? Number((total + per - 1n) / per) : 0 : validCount;
    $('#request-preview').textContent = per && total && count > 0 && count <= 500 ? `${count} платежей. План: ${money((total / 100n).toString() + '.' + (total % 100n).toString().padStart(2,'0'))}. Карты будут распределены по строкам.` : 'Введите количество или общую сумму до 500 платежей.';
    const plan = [];
    if (per && total && count > 0 && count <= 500) {
      let remaining = total;
      for (let i=0;i<count;i++) { const value = remaining < per ? remaining : per; plan.push(value); remaining -= value; }
    }
    renderContacts(plan);
  };
  const refreshIfPlanChanged = event => { if (['mode','payment_count','total','per_payment'].includes(event.target.name)) refresh(); };
  form.addEventListener('input', refreshIfPlanChanged);
  form.addEventListener('change', refreshIfPlanChanged);
  form.onsubmit = async event => {
    event.preventDefault();
    submitForm(form, async () => {
      const body = values(form);
      const entries = readContacts();
      const saved = new Map();
      body.contact_ids = [];
      for (const entry of entries) {
        const key = JSON.stringify(entry);
        if (!saved.has(key)) {
          const contact = await api('/api/catalog/create',{kind:'payment_contact',...entry});
          saved.set(key,contact.id);
        }
        body.contact_ids.push(saved.get(key));
      }
      const created = await api('/api/payment-request/create', body);
      notify('Запрос создан. Проверьте раскладку и скачайте файл.');
      await requestsPage(created.id);
    }, 'Создание…');
  };
  refresh();
}
function parseCents(value) {
  const text = String(value || '').replace(/\s/g,'').replace(',','.');
  if (!/^\d+(\.\d{1,2})?$/.test(text)) return null;
  const [whole, fraction = ''] = text.split('.');
  return BigInt(whole) * 100n + BigInt(fraction.padEnd(2,'0'));
}
async function showRequest(id) {
  const item = state.requests.find(request => request.id === id);
  if (!item) return;
  const rows = await api('/api/payment-request/rows?id=' + encodeURIComponent(id));
  $('#request-detail').innerHTML = section(`Запрос ${item.external_ref}`, `<div class="surface"><div class="summary-strip" style="border-top:0;padding-top:0"><div><small>Мерчант</small><strong>${esc(item.merchant)}</strong></div><div><small>План</small><strong>${money(item.planned_total)}</strong></div><div><small>Подтверждено по ответам</small><strong>${money(item.received_total)}</strong></div></div><div class="button-row" style="margin:22px 0">${state.user.Role === 'chief'||state.user.Role === 'operator' ? `<a class="button primary" href="/api/payment-request/export?id=${encodeURIComponent(id)}">Скачать XLSX для мерчанта</a>` : ''}<button class="button secondary" id="load-reply">Загрузить ответ</button></div>${table([{title:'№',key:'row_no'},{title:'Карта',render:r=>esc(r.mask)},{title:'ФИО',render:r=>esc(r.contact_name)},{title:'Телефон',render:r=>esc(r.contact_phone||'—')},{title:'Плановая сумма',render:r=>money(r.amount)}],rows)}</div>`);
  $('#load-reply').onclick = () => registriesPage(id);
  $('#request-detail').scrollIntoView({behavior:'smooth',block:'start'});
}

async function registriesPage(selectedRequest = '', selectedRegistry = '') {
  await Promise.all([loadCatalog(), loadRequests(), loadRegistries()]);
  const requestOptions = `<option value="">Без связи с запросом</option>${option(state.requests, item => `${item.external_ref} · ${item.merchant}`)}`;
  const upload = `<div class="surface"><h2>Получен ответный реестр</h2><p class="hint">Загрузите XLSX от мерчанта или банковский CSV. Сначала появится предпросмотр; деньги не изменятся.</p><form id="upload-form" enctype="multipart/form-data"><div class="form-grid">
    ${field('upload-merchant','Мерчант',`<select id="upload-merchant" name="merchant_id" data-searchable required>${option(state.catalog.merchants,item => item.name)}</select>`)}
    ${field('payment_request_id','Запрос на карты',`<select id="payment_request_id" name="payment_request_id" data-searchable>${requestOptions}</select>`,'Если файл относится к подготовленному запросу, выберите его.')}
    ${field('external_ref','Номер ответного реестра',input('external_ref','text','required placeholder="Номер от мерчанта"'))}
    ${field('file','Файл XLSX, XLS или CSV',input('file','file','accept=".xlsx,.xls,.csv" required'))}
    </div><div class="form-actions"><button class="button primary">Загрузить и проверить</button></div></form>
    <p class="form-note"><a href="/api/demo/sample">Скачать учебный XLSX</a> · <a href="/api/demo/sample?kind=csv">Скачать учебный банковский CSV</a></p></div>`;
  const list = table([
    {title:'Реестр',render:r=>`<strong>${esc(r.external_ref)}</strong><br><small>${esc(r.merchant)}</small>`},
    {title:'Пополнено',render:r=>`<span class="numeric">${money(r.total)}</span>`},
    {title:'Комиссия',render:r=>`<span class="numeric">${money(r.commission)}</span>`},
    {title:'Статус',render:r=>status(r.status)},
    {title:'',render:r=>`<button class="button quiet" data-registry="${esc(r.id)}">Открыть</button>`}
  ],state.registries,'Реестров пока нет','Загрузите первый ответный файл для проверки.');
  page('Реестры оплат','Факт поступления подтверждается отдельно после проверки строк, суммы и комиссии.',
    `<div class="split">${upload}<div class="surface tinted"><h2>Перед подтверждением</h2><p>Проверьте число строк, карты и итоговую сумму. Если данные не совпадают с запросом, выясните причину до решения о фактическом поступлении.</p><p class="hint">Банковский сбор из CSV сохраняется отдельно и не записывается автоматически в расходы.</p></div></div>` + section('Загруженные реестры',list) + `<div id="registry-detail"></div>` + (state.user.Role === 'chief' ? tariffControls() : ''));
  if (selectedRequest) {
    $('#payment_request_id').value = selectedRequest;
    const item = state.requests.find(x => x.id === selectedRequest);
    if (item) { $('#upload-merchant').value = item.merchant_id; $('#upload-merchant').dispatchEvent(new Event('change')); }
  }
  $('#payment_request_id').onchange = event => {
    const item = state.requests.find(x => x.id === event.target.value);
    if (item) { $('#upload-merchant').value = item.merchant_id; $('#upload-merchant').dispatchEvent(new Event('change')); }
  };
  $('#upload-form').onsubmit = async event => {
    event.preventDefault();
    submitForm(event.currentTarget, async () => {
      const result = await api('/api/registry/upload', new FormData(event.target), true);
      notify('Файл загружен. Проверьте строки перед подтверждением.');
      await registriesPage('', result.id);
    }, 'Загрузка…');
  };
  $$('[data-registry]').forEach(button => button.onclick = () => showRegistry(button.dataset.registry));
  if (state.user.Role === 'chief') bindTariffControls();
  if (selectedRegistry) await showRegistry(selectedRegistry);
}
async function showRegistry(id) {
  const item = state.registries.find(registry => registry.id === id);
  if (!item) return;
  const rows = await api('/api/registry/rows?id=' + encodeURIComponent(id));
  const errors = rows.filter(row => row.error);
  const linkedRequest = state.requests.find(request => request.id === item.payment_request_id);
  const rowTable = table([
    {title:'Строка',key:'row'},
    {title:'Карта',render:r=>esc(cardName(r.card_id))},
    {title:'Сумма',render:r=>money(r.amount)},
    {title:'Банковский сбор',render:r=>money(r.bank_fee)},
    {title:'Проверка',render:r=>r.error ? `<span class="status bad">${esc(errorName(r.error))}</span>` : '<span class="status good">Готово</span>'}
  ],rows);
  let actions = '';
  if (state.user.Role === 'chief' && item.status === 'preview') actions = `<div class="review-box"><h3>Подтвердить фактическое поступление</h3><p>После подтверждения деньги появятся на картах, возникнет долг мерчанту и будет признана комиссия.</p><div class="summary-strip"><div><small>Пополнения</small><strong>${money(item.total)}</strong></div><div><small>Комиссия · ${rate(item.rate_bp)}</small><strong>${money(item.commission)}</strong></div><div><small>К возврату мерчанту</small><strong>${money(item.net)}</strong></div></div><form id="confirm-registry"><div class="form-grid" style="margin-top:18px">${field('confirm_total','Подтвердите сумму пополнений',input('confirm_total','text','required placeholder="Введите сумму из предпросмотра"'))}${field('confirm_commission','Подтвердите комиссию',input('confirm_commission','text','required placeholder="Введите сумму комиссии"'))}</div><div class="form-actions"><button class="button primary">Подтвердить поступление</button></div></form></div>
  <details><summary>Разовая ставка для этого реестра</summary><div class="details-body"><p class="hint">Применяется только к этому реестру и утверждается до поступления.</p><form id="manual-rate-form"><div class="form-grid">${field('manual_rate_bp','Ставка в сотых долях процента',input('manual_rate_bp','number','min="0" max="10000" required'),'Например, 450 = 4,50 %.')}${field('manual_reason','Основание договорённости',input('manual_reason','text','required'))}</div><div class="form-actions"><button class="button secondary">Утвердить разовую ставку</button></div></form></div></details>`;
  if (state.user.Role === 'chief' && item.status === 'posted') actions = `<details><summary>Исправить разовую ставку</summary><div class="details-body"><form id="manual-correction-form"><div class="form-grid">${field('correction-rate','Новая ставка в сотых долях процента',input('correction-rate','number','min="0" max="10000" required'))}${field('correction-reason','Причина исправления',input('correction-reason','text','required'))}</div><div class="form-actions"><button class="button secondary">Показать изменение долга</button></div></form><div id="correction-review"></div></div></details><details><summary>Сторнировать реестр</summary><div class="details-body"><p class="hint">Возможно в течение 72 часов после подтверждения. История снятий и возвратов сохраняется.</p><form id="reverse-registry-form">${field('reverse-reason','Причина сторно',input('reverse-reason','text','required'))}<div class="form-actions"><button class="button danger">Сторнировать реестр</button></div></form></div></details>`;
  $('#registry-detail').innerHTML = section(`Реестр ${item.external_ref}`,`<div class="surface"><div class="summary-strip" style="border-top:0;padding-top:0"><div><small>Мерчант</small><strong>${esc(item.merchant)}</strong></div><div><small>Статус</small><strong>${status(item.status)}</strong></div><div><small>Сумма</small><strong>${money(item.total)}</strong></div><div><small>Ошибки</small><strong>${errors.length}</strong></div>${linkedRequest ? `<div><small>Запрос на карты</small><strong>${esc(linkedRequest.external_ref)}</strong></div>` : ''}</div><div style="margin-top:22px">${rowTable}</div>${actions}</div>`);
  if ($('#confirm-registry')) $('#confirm-registry').onsubmit = async event => {
    event.preventDefault();
    submitForm(event.currentTarget, async () => { await api('/api/registry/confirm',{id,version:String(item.version),confirm_total:$('#confirm_total').value,confirm_commission:$('#confirm_commission').value,confirm_rate_bp:String(item.rate_bp)}); notify('Поступление подтверждено. Проводки опубликованы.'); await registriesPage('',id); }, 'Подтверждение…');
  };
  if ($('#manual-rate-form')) $('#manual-rate-form').onsubmit = async event => {
    event.preventDefault();
    submitForm(event.currentTarget, async () => { await api('/api/registry/manual-rate',{id,version:String(item.version),rate_bp:$('#manual_rate_bp').value,reason:$('#manual_reason').value}); notify('Разовая ставка сохранена. Предпросмотр обновлён.'); await registriesPage('',id); }, 'Сохранение…');
  };
  if ($('#reverse-registry-form')) $('#reverse-registry-form').onsubmit = async event => {
    event.preventDefault();
    submitForm(event.currentTarget, async () => { await api('/api/registry/reverse',{id,reason:$('#reverse-reason').value}); notify('Реестр сторнирован.'); await registriesPage(); }, 'Сторнирование…');
  };
  if ($('#manual-correction-form')) $('#manual-correction-form').onsubmit = async event => {
    event.preventDefault();
    try {
      const preview = await api('/api/manual/preview',{registry_id:id,rate_bp:$('#correction-rate').value,reason:$('#correction-reason').value});
      $('#correction-review').innerHTML = `<div class="review-box"><h3>Что изменится</h3><div class="summary-strip"><div><small>Прежняя комиссия</small><strong>${money(preview.old_commission)}</strong></div><div><small>Новая комиссия</small><strong>${money(preview.new_commission)}</strong></div><div><small>Долг мерчанту после</small><strong>${money(preview.payable_after)}</strong></div></div><div class="button-row"><button id="apply-correction" class="button primary">Подтвердить исправление</button></div></div>`;
      $('#apply-correction').onclick = async () => { try { await api('/api/manual/confirm',{registry_id:id,rate_bp:String(preview.rate_bp),reason:preview.reason,preview_hash:preview.preview_hash}); notify('Ставка исправлена.'); await registriesPage('',id); } catch (error) { notify(error.message,true); } };
    } catch (error) { notify(error.message,true); }
  };
  $('#registry-detail').scrollIntoView({behavior:'smooth',block:'start'});
}
function errorName(code) { return {invalid_amount:'Неверная сумма',missing_cell:'Нет обязательного значения',card_not_assigned:'Карта не назначена',card_not_unique_or_unknown:'Карта не найдена или неоднозначна',invalid_amount_or_bank_total:'Расхождение с итогом банка'}[code] || 'Нужна проверка строки'; }
function tariffControls() {
  return `<details class="section"><summary>Изменить общий тариф мерчанта</summary><div class="details-body"><p class="hint">Перед применением система покажет перерасчёт прежних обычных реестров и новый долг. Реестры с разовой ставкой не меняются.</p><form id="tariff-form"><div class="form-grid">${field('tariff-merchant','Мерчант',select('tariff-merchant',state.catalog.merchants,item=>item.name,'required'))}${field('tariff-rate','Новая ставка в сотых долях процента',input('tariff-rate','number','min="0" max="10000" required'),'Например, 400 = 4,00 %.')}${field('tariff-date','Действует с даты',input('tariff-date','date','required'))}</div><div class="form-actions"><button class="button secondary">Показать перерасчёт</button></div></form><div id="tariff-review"></div></div></details>`;
}
function bindTariffControls() {
  $('#tariff-form').onsubmit = async event => {
    event.preventDefault();
    try {
      const preview = await api('/api/tariff/preview',{merchant_id:$('#tariff-merchant').value,rate_bp:$('#tariff-rate').value,valid_from:$('#tariff-date').value});
      const affected = preview.items.filter(item => !item.excluded).length;
      $('#tariff-review').innerHTML = `<div class="review-box"><h3>Перерасчёт до подтверждения</h3><p>Затронуто реестров: <strong>${affected}</strong>. Изменение комиссии: <strong>${money(preview.delta_commission)}</strong>.</p><div class="summary-strip"><div><small>Долг до</small><strong>${money(preview.position_before)}</strong></div><div><small>Долг после</small><strong>${money(preview.position_after)}</strong></div></div><div class="button-row"><button id="apply-tariff" class="button primary">Подтвердить тариф и пересчёт</button></div></div>`;
      $('#apply-tariff').onclick = async () => { try { await api('/api/tariff/confirm',{merchant_id:preview.merchant_id,rate_bp:String(preview.rate_bp),valid_from:preview.valid_from,preview_hash:preview.preview_hash}); notify('Тариф подтверждён.'); await registriesPage(); } catch (error) { notify(error.message,true); } };
    } catch (error) { notify(error.message,true); }
  };
}

const expenseTypes = {agent_fee:'Агентские',bank_fee:'Банк. Комиссия',repayment:'Выдача мерчанту',dividends:'Дивиденды',salary:'Зарплата',warmup:'Прогрев',it_infrastructure:'ИТ Инфраструктура',taxes:'Налоги',losses:'Потери',communication:'Связь',delivery:'Доставка',other:'Прочие РАСХОДЫ',operating:'Операционный расход (ранее)',transport:'Транспорт (ранее)'};
const newExpenseTypes = ['agent_fee','bank_fee','repayment','dividends','salary','warmup','it_infrastructure','taxes','losses','communication','delivery','other'];
async function expensesPage(selectedDraft = '') {
  await Promise.all([loadCatalog(),loadDrafts()]);
  const chiefCash = (state.catalog.custodians || []).find(item => item.kind === 'chief');
  const firstCard = (state.catalog.cards || [])[0];
  const defaultKind = chiefCash ? 'cash' : 'card';
  const defaultID = chiefCash?.id || firstCard?.id || '';
  const expenseList = state.drafts.filter(item => item.kind === 'expense');
  const form = `<div class="surface"><h2>Новый расход</h2><p class="hint">Заполнение занимает несколько секунд. После сохранения сумма ещё не списана.</p><form id="expense-form"><div class="form-grid">
    ${field('expense-amount','Сумма, ₽',input('expense-amount','text','name="amount" inputmode="decimal" autocomplete="off" placeholder="Например, 1 500,00" required'))}
    ${field('expense-category','Тип расхода',`<select id="expense-category" name="category" data-searchable required><option value="">Выберите тип</option>${newExpenseTypes.map(key=>`<option value="${key}">${esc(expenseTypes[key])}</option>`).join('')}</select>`)}
    ${field('expense-reason','Комментарий',input('expense-reason','text','name="reason" placeholder="Можно не заполнять"'))}
    ${field('expense-date','Дата расхода',input('expense-date','date',`name="date" value="${todayMoscow()}" required`))}
  </div><div id="expense-type-guidance" class="callout" hidden></div><div class="source-line"><div><small>Списать из</small><strong id="expense-source-label">${esc(sourceLabel(defaultKind,defaultID))}</strong></div><button type="button" class="text-button" id="change-expense-source">Изменить</button></div><div id="expense-source-fields" hidden><div class="form-grid">${field('expense-source-kind','Где находятся деньги',`<select id="expense-source-kind"><option value="cash">Наличные</option><option value="card">На карте</option></select>`)}${field('expense-source-id','Ответственный или карта',`<select id="expense-source-id"></select>`)}</div></div><div class="form-actions"><button id="save-expense" class="button primary">Сохранить расход</button></div></form></div>`;
  const list = table([
    {title:'Дата',render:d=>esc(d.payload?.date || dateLabel(d.created_at))},
    {title:'Тип',render:d=>esc(expenseTypes[d.payload?.category] || 'Тип не указан')},
    {title:'Сумма',render:d=>`<span class="numeric">${money(d.payload?.amount)}</span>`},
    {title:'Статус',render:d=>status(d.status)},
    {title:'',render:d=>`<button class="button quiet" data-expense="${esc(d.id)}">${d.status === 'draft' && state.user.Role === 'chief' ? 'Проверить' : 'Открыть'}</button>`}
  ],expenseList,'Расходов пока нет','Сохраните первый расход.');
  page('Расходы','Быстро внесите расход. Денежное списание происходит только после подтверждения главным администратором.',
    `<div class="split">${form}<div class="surface tinted"><h2>Что происходит дальше</h2><p>Сначала создаётся черновик. Главный администратор отдельно проверяет тип, место списания, дату и точную сумму.</p><p>Ошибку в проведённом расходе исправляют сторно, сохраняя историю.</p></div></div>` + section('Последние расходы',list) + `<div id="expense-detail"></div>`);
  let sourceKind = defaultKind, sourceID = defaultID;
  const drawSources = () => {
    const items = sourceKind === 'cash' ? state.catalog.custodians : state.catalog.cards;
    $('#expense-source-id').innerHTML = option(items,item=>sourceKind === 'cash' ? item.name : `${item.mask} · ${item.name}`);
    sourceID = items?.find(item=>item.id===sourceID)?.id || (sourceKind==='cash' ? chiefCash?.id : '') || items?.[0]?.id || '';
    $('#expense-source-id').value = sourceID;
    $('#expense-source-label').textContent = sourceLabel(sourceKind,sourceID);
  };
  $('#expense-source-kind').value = sourceKind;
  drawSources();
  $('#expense-category').onchange = event => {
    const type = event.target.value;
    if (type === 'repayment' || type === 'losses') {
      state.page = 'money';
      moneyPage(type === 'repayment' ? 'repayment' : 'advanced', '', $('#expense-amount').value, type === 'losses' ? 'writeoff' : '')
        .then(()=>notify(type === 'repayment' ? 'Открыта выдача мерчанту: она погашает долг и не уменьшает прибыль.' : 'Открыто списание подтверждённой недостачи. Укажите исходную недостачу.'))
        .catch(error=>notify(error.message,true));
      return;
    }
    const explanation = type === 'dividends' ? 'Дивиденды не уменьшают прибыль. Для их выплаты требуется отдельное правило о получателе и источнике капитала.' : '';
    $('#expense-type-guidance').textContent = explanation;
    $('#expense-type-guidance').hidden = !explanation;
    $('#save-expense').disabled = !!explanation;
  };
  $('#change-expense-source').onclick = () => { $('#expense-source-fields').hidden = !$('#expense-source-fields').hidden; };
  $('#expense-source-kind').onchange = event => { sourceKind = event.target.value; drawSources(); };
  $('#expense-source-id').onchange = event => { sourceID = event.target.value; $('#expense-source-label').textContent = sourceLabel(sourceKind,sourceID); };
  $('#expense-form').onsubmit = async event => {
    event.preventDefault();
    if (['repayment','dividends','losses'].includes($('#expense-category').value)) { notify('Для этого типа требуется отдельный сценарий.',true); return; }
    if (!sourceID) { notify('Сначала добавьте карту или ответственного за наличные.',true); return; }
    submitForm(event.currentTarget, async () => {
      const body = {kind:'expense',amount:$('#expense-amount').value,category:$('#expense-category').value,reason:$('#expense-reason').value,date:$('#expense-date').value,source_kind:sourceKind,source_id:sourceID};
      const result = await api('/api/draft',body);
      notify('Расход сохранён как черновик. Деньги ещё не списаны.');
      await expensesPage(result.id);
    }, 'Сохранение…');
  };
  $$('[data-expense]').forEach(button => button.onclick = () => showExpense(button.dataset.expense));
  if (selectedDraft) await showExpense(selectedDraft);
}
async function showExpense(id) {
  const draft = state.drafts.find(item => item.id === id);
  if (!draft) return;
  const p = draft.payload || {};
  const source = sourceLabel(p.source_kind,p.source_id);
  const review = draft.status === 'draft' && state.user.Role === 'chief' ? `<form id="confirm-expense" class="review-box"><h3>Проверка перед списанием</h3><p>С карты или наличных будет списано ровно <strong class="amount">${money(p.amount)}</strong>.</p>${field('expense-confirm-amount','Введите точную сумму для подтверждения',input('expense-confirm-amount','text','inputmode="decimal" placeholder="Сумма из черновика…" required'))}<div class="button-row"><button type="submit" class="button primary">Подтвердить расход</button></div></form>` : draft.status === 'draft' ? '<div class="callout">Ожидает подтверждения главного администратора. Баланс пока не меняется.</div>' : '';
  const reverse = draft.status === 'posted' && state.user.Role === 'chief' ? `<details><summary>Исправить через сторно</summary><div class="details-body"><p class="hint">Исходная запись сохранится в истории. Новый правильный расход создайте отдельно.</p><form id="reverse-expense">${field('expense-reverse-reason','Причина сторно',input('expense-reverse-reason','text','required'))}<div class="form-actions"><button type="submit" class="button danger">Сторнировать расход</button></div></form></div></details>` : '';
  $('#expense-detail').innerHTML = section('Детали расхода',`<div class="surface"><div class="summary-strip" style="border-top:0;padding-top:0"><div><small>Сумма</small><strong>${money(p.amount)}</strong></div><div><small>Тип</small><strong>${esc(expenseTypes[p.category] || 'Тип не указан')}</strong></div><div><small>Дата</small><strong>${esc(p.date || 'Дата подтверждения')}</strong></div><div><small>Источник</small><strong>${esc(source)}</strong></div></div>${p.reason ? `<p style="margin-top:20px">Комментарий: ${esc(p.reason)}</p>` : ''}<p style="margin-top:15px">${status(draft.status)}</p>${review}${reverse}</div>`);
  if ($('#confirm-expense')) $('#confirm-expense').onsubmit = event => { event.preventDefault(); submitForm(event.currentTarget, async () => { await api('/api/draft/confirm',{id,version:String(draft.version),confirm_amount:$('#expense-confirm-amount').value}); notify('Расход подтверждён и учтён.'); await expensesPage(id); }, 'Подтверждение…'); };
  if ($('#reverse-expense')) $('#reverse-expense').onsubmit = event => { event.preventDefault(); submitForm(event.currentTarget, async () => { await api('/api/draft/reverse',{id,reason:$('#expense-reverse-reason').value}); notify('Расход сторнирован.'); await expensesPage(); }, 'Сторнирование…'); };
  $('#expense-detail').scrollIntoView({behavior:'smooth',block:'start'});
}

const operationFields = {
  withdrawal:[['card_id','Карта','card'],['custodian_id','Кто получил наличные','custodian']],
  handover:[['from_custodian_id','Кто передаёт','custodian'],['to_custodian_id','Кто принимает','chief']],
  repayment:[['merchant_id','Мерчант','merchant'],['source_kind','Откуда выдаём','source_kind'],['source_id','Карта или ответственный','source_id']],
  injection:[['person_id','Кто внёс деньги','custodian'],['destination_kind','Куда поступили','destination_kind'],['destination_id','Карта или ответственный','destination_id']],
  shortage:[['custodian_id','Ответственный за недостачу','custodian']],
  writeoff:[['custodian_id','Ответственный','custodian'],['shortage_id','Исходная недостача','shortage']],
  surplus:[['custodian_id','У кого обнаружен излишек','custodian']],
  surplus_income:[['source_ref','Исходный излишек','surplus']],
  surplus_merchant:[['source_ref','Исходный излишек','surplus'],['merchant_id','Мерчант','merchant']],
  surplus_shortage:[['source_ref','Исходный излишек','surplus'],['custodian_id','Ответственный','custodian'],['shortage_id','Недостача','shortage']],
  surplus_return:[['source_ref','Основание по излишку','surplus_merchant'],['merchant_id','Мерчант','merchant'],['source_kind','Откуда выдаём','source_kind'],['source_id','Карта или ответственный','source_id']],
  collection:[['receivable_account','Какой долг погашают','receivable'],['merchant_id','Мерчант, если долг мерчанта','merchant'],['custodian_id','Ответственный, если недостача','custodian'],['shortage_id','Недостача, если применимо','shortage'],['destination_kind','Куда поступили деньги','destination_kind'],['destination_id','Карта или ответственный','destination_id']],
  recovery:[['writeoff_id','Исходное списание','writeoff'],['destination_kind','Куда поступили деньги','destination_kind'],['destination_id','Карта или ответственный','destination_id']],
  forgive_injection:[['person_id','Чей долг прощён','custodian']]
};
const operationGroups = {withdrawal:'Снятие',handover:'Передача',repayment:'Возврат мерчанту',observation:'Остаток карты',advanced:'Другие операции'};
function draftOption(items) { return option(items,item=>`${kindNames[item.kind] || 'Операция'} · ${item.payload?.amount || ''} ₽ · ${dateLabel(item.created_at)}`); }
function operationField([key,label,type], form) {
  let control;
  const c = state.catalog;
  if (type === 'card') control = select(key,c.cards,item=>`${item.mask} · ${item.name}`,'required');
  else if (type === 'merchant') control = select(key,c.merchants,item=>item.name);
  else if (type === 'custodian') control = select(key,c.custodians,item=>item.name,'required');
  else if (type === 'chief') control = select(key,(c.custodians || []).filter(item=>item.kind==='chief'),item=>item.name,'required');
  else if (type === 'source_kind' || type === 'destination_kind') control = `<select name="${key}" id="${key}" data-searchable><option value="cash">Наличные</option><option value="card">Карта</option></select>`;
  else if (type === 'source_id' || type === 'destination_id') control = `<select name="${key}" id="${key}" data-searchable></select>`;
  else if (type === 'receivable') control = `<select name="${key}" id="${key}" data-searchable><option value="1300">Переплата мерчанту</option><option value="1400">Недостача ответственного</option></select>`;
  else {
    const matches = state.drafts.filter(item=>item.status==='posted' && (type==='surplus' ? item.kind==='surplus' : type==='shortage' ? item.kind==='shortage' : type==='writeoff' ? item.kind==='writeoff' : item.kind==='surplus_merchant'));
    control = type === 'surplus_merchant'
      ? `<select id="${key}" name="${key}">${matches.map(item=>`<option value="${esc(item.payload?.source_ref)}">${esc(kindNames[item.kind])} · ${esc(item.payload?.amount || '')} ₽</option>`).join('')}</select>`
      : select(key,matches,item=>`${kindNames[item.kind]} · ${item.payload?.amount || ''} ₽`);
  }
  return field(key,label,control);
}
async function moneyPage(selectedKind = 'withdrawal', selectedDraft = '', initialAmount = '', advancedKind = '') {
  await Promise.all([loadCatalog(),loadDrafts()]);
  const kind = operationGroups[selectedKind] && !(state.user.Role === 'operator' && selectedKind === 'observation') ? selectedKind : 'withdrawal';
  const tabs = `<nav class="tabs" aria-label="Типы денежных операций">${Object.entries(operationGroups).filter(([key])=>!(state.user.Role === 'operator' && key === 'observation')).map(([key,label])=>`<button data-money-tab="${key}" class="${kind===key?'active':''}" aria-pressed="${kind===key}">${esc(label)}</button>`).join('')}</nav>`;
  const active = kind === 'advanced' ? 'injection' : kind;
  const form = kind === 'observation' ? `<div class="surface"><h2>Фактический остаток карты</h2><p class="hint">Наблюдение помогает найти расхождение, но само по себе не меняет баланс.</p><form id="observation-form"><div class="form-grid">${field('card_id','Карта',select('card_id',state.catalog.cards,item=>item.mask,'required'))}${field('amount','Остаток, ₽',input('amount','text','inputmode="decimal" required'))}</div><div class="form-actions"><button class="button primary">Сохранить остаток</button></div></form></div>` : `<div class="surface"><h2>${esc(kind === 'advanced' ? 'Другие операции' : kindNames[active])}</h2><p class="hint">Сохраните черновик. Главный администратор проверит точную сумму перед проведением.</p><form id="money-form">${kind==='advanced' ? field('advanced_kind','Операция',`<select id="advanced_kind">${Object.keys(operationFields).filter(k=>!['withdrawal','handover','repayment'].includes(k)).map(k=>`<option value="${k}">${esc(kindNames[k])}</option>`).join('')}</select>`) : ''}<div id="operation-fields" class="form-grid"></div><div class="form-grid" style="margin-top:16px">${field('op-amount','Сумма, ₽',input('op-amount','text','name="amount" inputmode="decimal" required'))}${field('op-reason','Основание или комментарий',input('op-reason','text','name="reason" placeholder="При необходимости"'))}</div><div class="form-actions"><button class="button primary">Сохранить черновик</button></div></form></div>`;
  const relevantDrafts = state.drafts.filter(item=>item.kind!=='expense');
  const list = table([
    {title:'Действие',render:d=>esc(kindNames[d.kind] || 'Денежная операция')},
    {title:'Сумма',render:d=>money(d.payload?.amount)},
    {title:'Создано',render:d=>esc(dateLabel(d.created_at))},
    {title:'Статус',render:d=>status(d.status)},
    {title:'',render:d=>`<button class="button quiet" data-money-draft="${esc(d.id)}">Открыть</button>`}
  ],relevantDrafts,'Операций пока нет','Создайте первый черновик.');
  page('Движение денег','Снятия, передачи, возвраты и отдельные решения по расхождениям.',tabs + `<div class="split">${form}<div class="surface tinted"><h2>Правило подтверждения</h2><p>Создание черновика не меняет остатки. Точную сумму и фактическое движение денег подтверждает главный администратор.</p><p class="hint">История подтверждённых операций не редактируется; исправление оформляется сторно.</p></div></div>` + section('Черновики и проведённые операции',list) + `<div id="money-detail"></div>`);
  $$('[data-money-tab]').forEach(button=>button.onclick=()=>moneyPage(button.dataset.moneyTab));
  if (kind === 'observation') $('#observation-form').onsubmit = async event => { event.preventDefault(); try { await api('/api/observation',values(event.target)); notify('Фактический остаток сохранён.'); await moneyPage('observation'); } catch(error) { notify(error.message,true); } };
  else {
    if (kind === 'advanced' && advancedKind) $('#advanced_kind').value = advancedKind;
    bindOperationForm(kind);
  }
  if (initialAmount && $('#op-amount')) $('#op-amount').value = initialAmount;
  $$('[data-money-draft]').forEach(button=>button.onclick=()=>showMoneyDraft(button.dataset.moneyDraft));
  if (selectedDraft) await showMoneyDraft(selectedDraft);
}
function bindOperationForm(tab) {
  const form = $('#money-form');
  const draw = () => {
    const kind = tab === 'advanced' ? $('#advanced_kind').value : tab;
    $('#operation-fields').innerHTML = (operationFields[kind] || []).map(f=>operationField(f,form)).join('');
    for (const prefix of ['source','destination']) {
      const kindField = $(`#${prefix}_kind`);
      const idField = $(`#${prefix}_id`);
      if (kindField && idField) {
        const refresh = () => {
          const items = kindField.value === 'card' ? state.catalog.cards : state.catalog.custodians;
          idField.innerHTML = option(items,item=>kindField.value==='card'?item.mask:item.name);
          if (kindField.value==='cash') {
            const chief = (state.catalog.custodians||[]).find(item=>item.kind==='chief');
            if (chief) idField.value = chief.id;
          }
        };
        kindField.onchange = refresh;
        refresh();
      }
    }
    enhanceSelects(form);
    if (kind==='handover') {
      const from=$('#from_custodian_id');
      const firstCollector=(state.catalog.custodians||[]).find(item=>item.kind==='collector');
      if(from&&firstCollector)from.value=firstCollector.id;
    }
  };
  if ($('#advanced_kind')) $('#advanced_kind').onchange=draw;
  draw();
  form.onsubmit = async event => {
    event.preventDefault();
    const kind = tab==='advanced' ? $('#advanced_kind').value : tab;
    const body = values(form);
    body.kind = kind;
    submitForm(form, async () => { const result = await api('/api/draft',body); notify('Черновик сохранён. Деньги ещё не перемещены.'); await moneyPage(tab,result.id); }, 'Сохранение…');
  };
}
async function showMoneyDraft(id) {
  const draft = state.drafts.find(item=>item.id===id);
  if (!draft) return;
  const p = draft.payload || {};
  const fields = [];
  if (p.card_id) fields.push(['Карта',cardName(p.card_id)]);
  if (p.custodian_id) fields.push(['Ответственный',custodianName(p.custodian_id)]);
  if (p.from_custodian_id) fields.push(['Передаёт',custodianName(p.from_custodian_id)]);
  if (p.to_custodian_id) fields.push(['Принимает',custodianName(p.to_custodian_id)]);
  if (p.source_kind) fields.push(['Источник',sourceLabel(p.source_kind,p.source_id)]);
  if (p.destination_kind) fields.push(['Получатель денег',sourceLabel(p.destination_kind,p.destination_id)]);
  if (p.merchant_id) fields.push(['Мерчант',entityName(state.catalog.merchants,p.merchant_id)]);
  const details = `<div class="summary-strip" style="border-top:0;padding-top:0"><div><small>Действие</small><strong>${esc(kindNames[draft.kind] || 'Операция')}</strong></div><div><small>Заявленная сумма</small><strong>${money(p.amount)}</strong></div>${fields.map(([label,value])=>`<div><small>${esc(label)}</small><strong>${esc(value)}</strong></div>`).join('')}</div>${p.reason ? `<p style="margin-top:18px">Основание: ${esc(p.reason)}</p>`:''}`;
  const confirm = draft.status==='draft' && state.user.Role==='chief' ? `<form id="confirm-money" class="review-box"><h3>Подтвердить фактическое движение</h3><p>${draft.kind==='handover'?'Укажите фактически принятую сумму. Она может отличаться от заявленной.':'Введите точную сумму из черновика.'}</p>${field('money-confirm','Подтверждаемая сумма, ₽',input('money-confirm','text','inputmode="decimal" required'))}<div class="button-row"><button type="submit" class="button primary">Подтвердить ${esc(kindNames[draft.kind] || 'операцию').toLowerCase()}</button></div></form>` : draft.status==='draft' ? '<div class="callout">Ожидает подтверждения главным администратором.</div>' : '';
  const reverse = draft.status==='posted' && state.user.Role==='chief' ? `<details><summary>Сторнировать эту операцию</summary><div class="details-body"><form id="reverse-money">${field('money-reverse-reason','Причина',input('money-reverse-reason','text','required'))}<div class="form-actions"><button type="submit" class="button danger">Сторнировать</button></div></form></div></details>` : '';
  $('#money-detail').innerHTML = section('Детали операции',`<div class="surface">${details}<p style="margin-top:20px">${status(draft.status)}</p>${confirm}${reverse}</div>`);
  if ($('#confirm-money')) $('#confirm-money').onsubmit = event => { event.preventDefault(); submitForm(event.currentTarget, async () => { await api('/api/draft/confirm',{id,version:String(draft.version),confirm_amount:$('#money-confirm').value}); notify('Операция подтверждена.'); await moneyPage('withdrawal',id); }, 'Подтверждение…'); };
  if ($('#reverse-money')) $('#reverse-money').onsubmit = event => { event.preventDefault(); submitForm(event.currentTarget, async () => { await api('/api/draft/reverse',{id,reason:$('#money-reverse-reason').value}); notify('Операция сторнирована.'); await moneyPage('withdrawal'); }, 'Сторнирование…'); };
  $('#money-detail').scrollIntoView({behavior:'smooth',block:'start'});
}

const catalogTabs = {merchants:'Мерчанты',cards:'Карты',payment_contacts:'ФИО и телефоны',banks:'Банки',custodians:'Ответственные',users:'Пользователи',tariffs:'Тарифы'};
const syntheticFIO = ['Тестов Алексей Учебович','Демина Мария Примеровна','Образцов Илья Тестович','Учебная Анна Образцовна','Примеров Павел Демович','Тестова Елена Учебовна'];
function catalogForm(tab) {
  const c = state.catalog;
  if (state.user.Role === 'sysadmin' && tab !== 'users') return '';
  if (state.user.Role !== 'chief' && state.user.Role !== 'sysadmin' && !(state.user.Role === 'operator' && tab === 'payment_contacts')) return '';
  if (tab === 'merchants') return `<div class="surface"><h2>Добавить мерчанта</h2><form id="catalog-form" data-kind="merchant"><div class="form-grid">${field('code','Короткий код',input('code','text','required'))}${field('name','Название',input('name','text','required'))}</div><div class="form-actions"><button class="button primary">Добавить мерчанта</button></div></form></div>`;
  if (tab === 'banks') return `<div class="surface"><h2>Добавить банк</h2><form id="catalog-form" data-kind="bank"><div class="form-grid">${field('code','Короткий код',input('code','text','required'))}${field('name','Название банка',input('name','text','required'))}</div><div class="form-actions"><button class="button primary">Добавить банк</button></div></form></div>`;
  if (tab === 'cards') return `<div class="surface"><h2>Добавить учебную карту</h2><p class="hint">Хранится маска. Для демонстрационной выгрузки система создаст заведомо недействительный полный номер.</p><form id="catalog-form" data-kind="card"><div class="form-grid">${field('bank_id','Банк',select('bank_id',c.banks,item=>item.name,'required'))}${field('mask','Маска карты',input('mask','text','placeholder="000000******1234" pattern="[0-9]{6}\\*{6}[0-9]{4}" required'))}${field('owner_label','Вымышленное ФИО',`<select id="owner_label" name="owner_label" required>${syntheticFIO.map(name=>`<option>${esc(name)}</option>`).join('')}</select>`)}</div><div class="form-actions"><button class="button primary">Добавить карту</button></div></form></div>`;
  if (tab === 'payment_contacts') return `<div class="surface"><h2>Добавить ФИО и телефон</h2><p class="hint">В демоверсии разрешены только вымышленные данные. Запись станет доступна при формировании реестра карт.</p><form id="catalog-form" data-kind="payment_contact"><div class="form-grid">${field('full_name','Вымышленное ФИО',`<select id="full_name" name="full_name" required>${syntheticFIO.map(name=>`<option>${esc(name)}</option>`).join('')}</select>`)}${field('phone','Вымышленный телефон',input('phone','tel','placeholder="+7 000 000-00-01" required'))}</div><div class="form-actions"><button class="button primary">Добавить в справочник</button></div></form></div>`;
  if (tab === 'custodians') return `<div class="surface"><h2>Добавить ответственного за наличные</h2><form id="catalog-form" data-kind="custodian"><div class="form-grid">${field('name','Имя или обозначение',input('name','text','required'))}${field('custodian_kind','Функция',`<select id="custodian_kind" name="custodian_kind"><option value="collector">Сборщик</option><option value="operator">Операционист</option></select>`)}</div><div class="form-actions"><button class="button primary">Добавить ответственного</button></div></form></div>`;
  if (tab === 'users' && state.user.Role === 'sysadmin') return `<div class="surface"><h2>Новый пользователь</h2><form id="catalog-form" data-kind="user"><div class="form-grid">${field('login','Логин',input('login','text','required'))}${field('name','Имя',input('name','text','required'))}${field('role','Роль',`<select id="role" name="role"><option value="collector">Сборщик</option><option value="operator">Операционист</option><option value="accountant">Бухгалтер</option><option value="auditor">Аудитор</option></select>`)}</div><div class="form-actions"><button class="button primary">Создать пользователя</button></div></form><div id="new-password"></div></div><div class="surface"><h2>Назначить карту</h2><form id="card-assign-form"><div class="form-grid">${field('assign-user','Сборщик или операционист',`<select id="assign-user" name="user_id">${option((c.users||[]).filter(u=>u.role==='operator'||u.role==='collector'),u=>u.name)}</select>`)}${field('assign-card','Карта',`<select id="assign-card" name="card_id">${option(c.cards,item=>item.mask)}</select>`)}</div><div class="form-actions"><button class="button secondary">Назначить</button></div></form></div><details><summary>Привязать Telegram ID и остаток наличных</summary><div class="details-body"><form id="telegram-link-form"><div class="form-grid">${field('telegram-user','Пользователь',`<select id="telegram-user" name="user_id">${option((c.users||[]).filter(u=>u.role==='collector'||u.role==='chief'),u=>u.name)}</select>`)}${field('telegram_id','Числовой Telegram ID',input('telegram_id','text','inputmode="numeric" required'))}${field('telegram-custodian','Ответственный за наличные',`<select id="telegram-custodian" name="custodian_id" required>${option((c.custodians||[]).filter(item=>item.kind==='collector'||item.kind==='chief'),item=>item.name)}</select>`)}</div><div class="form-actions"><button class="button secondary">Сохранить связь</button></div></form></div></details>`;
  return '';
}
function catalogRows(tab) {
  const c = state.catalog;
  if (tab === 'merchants') return table([{title:'Название',key:'name'},{title:'Код',key:'code'},{title:'Разбор реестра',render:r=>r.parser_status==='configured'?esc(r.parser_name):'<span class="muted">Ожидает образец</span>'},{title:'Состояние',render:r=>r.active?'Работает':'Неактивен'}],c.merchants,'Мерчантов пока нет','Добавьте мерчанта перед созданием запроса на карты.');
  if (tab === 'cards') return table([{title:'Карта',render:r=>`<span class="mono">${esc(r.mask)}</span>`},{title:'Банк',key:'name'},{title:'Владелец',key:'owner_label'},{title:'Состояние',render:r=>r.status==='active'?'Активна':r.status==='blocked'?'Заблокирована':'Выведена'}],c.cards,'Карт пока нет','Добавьте учебную карту для формирования запроса.');
  if (tab === 'payment_contacts') return table([{title:'ФИО',key:'full_name'},{title:'Телефон',render:r=>`<span class="mono">${esc(r.phone)}</span>`},{title:'Состояние',render:r=>r.active?'Доступен':'Неактивен'}],c.payment_contacts,'Контактов пока нет','Добавьте ФИО и телефон здесь или прямо при формировании реестра карт.');
  if (tab === 'banks') return table([{title:'Банк',key:'name'},{title:'Код',key:'code'}],c.banks,'Банков пока нет','Добавьте банк, чтобы создать карту.');
  if (tab === 'custodians') return table([{title:'Ответственный',key:'name'},{title:'Функция',render:r=>r.kind==='chief'?'Главный администратор':r.kind==='collector'?'Сборщик':'Операционист'},{title:'Состояние',render:r=>r.active?'Работает':'Неактивен'}],c.custodians,'Ответственных пока нет','Добавьте сборщика или операциониста.');
  if (tab === 'users') return table([{title:'Имя',key:'name'},{title:'Логин',key:'login'},{title:'Роль',render:r=>esc(roleNames[r.role]||'Пользователь')},{title:'Telegram ID',render:r=>esc(r.telegram_id||'—')},{title:'Наличные у',render:r=>esc(r.custodian_id ? custodianName(r.custodian_id) : 'Не привязан')},{title:'Состояние',render:r=>r.active?'Доступ открыт':'Доступ закрыт'}],c.users,'Пользователей пока нет','Системный администратор создаёт учётные записи.');
  return table([{title:'Мерчант',key:'name'},{title:'Ставка',render:r=>rate(r.rate_bp)},{title:'С даты',render:r=>esc(String(r.valid_from||'').slice(0,10))},{title:'Состояние',render:r=>r.active?'Действует':'Заменён'}],c.tariffs,'Тарифов пока нет','Установите тариф в разделе «Реестры оплат».');
}
async function catalogPage(tab = 'merchants') {
  await loadCatalog();
  if (state.user.Role === 'sysadmin') tab = 'users';
  if (!catalogTabs[tab]) tab = 'merchants';
  const tabs = `<nav class="tabs" aria-label="Разделы справочников">${Object.entries(catalogTabs).filter(([key])=>key!=='users'||state.catalog.users).map(([key,label])=>`<button data-catalog-tab="${key}" class="${tab===key?'active':''}" aria-pressed="${tab===key}">${esc(label)}</button>`).join('')}</nav>`;
  const form = catalogForm(tab);
  page('Справочники','Отдельные формы для мерчантов, карт, ФИО и телефонов, банков и ответственных. Здесь нет денежных проводок.',tabs + (form ? `<div class="stack">${form}</div>` : '') + section(catalogTabs[tab],catalogRows(tab)));
  $$('[data-catalog-tab]').forEach(button=>button.onclick=()=>catalogPage(button.dataset.catalogTab));
  if ($('#catalog-form')) $('#catalog-form').onsubmit = async event => {
    event.preventDefault();
    const body = values(event.target);
    body.kind = event.target.dataset.kind;
    try {
      const result = await api('/api/catalog/create',body);
      if (result.temporary_password) {
        $('#new-password').innerHTML = `<div class="review-box"><h3>Одноразовый пароль</h3><p>Передайте его пользователю защищённым способом. После ухода со страницы он не показывается.</p><strong class="mono">${esc(result.temporary_password)}</strong></div>`;
        await loadCatalog();
      } else { notify('Запись добавлена.'); await catalogPage(tab); }
    } catch(error) { notify(error.message,true); }
  };
  if ($('#card-assign-form')) $('#card-assign-form').onsubmit = async event => { event.preventDefault(); try { await api('/api/card/assign',values(event.target)); notify('Карта назначена.'); } catch(error) { notify(error.message,true); } };
  if ($('#telegram-link-form')) {
    const refreshCustodian = () => {
      const user = (state.catalog.users||[]).find(item=>item.id===$('#telegram-user').value);
      const kind = user?.role === 'chief' ? 'chief' : 'collector';
      $('#telegram-custodian').innerHTML = option((state.catalog.custodians||[]).filter(item=>item.kind===kind&&item.active),item=>item.name);
    };
    $('#telegram-user').onchange = refreshCustodian;
    refreshCustodian();
    $('#telegram-link-form').onsubmit = async event => { event.preventDefault(); try { await api('/api/user/telegram',values(event.target)); notify('Telegram ID и ответственный за наличные связаны.'); await catalogPage('users'); } catch(error) { notify(error.message,true); } };
  }
}

async function reportsPage() {
  await loadCatalog();
  const report = await api('/api/report');
  const s = report.summary;
  const finance = `<div class="metric-grid"><div class="metric"><span class="metric-label">Деньги на картах и в кассах</span><strong class="metric-value">${money(s.card_cash)}</strong><small>Подтверждённые остатки</small></div><div class="metric"><span class="metric-label">Долг перед мерчантами</span><strong class="metric-value">${money(s.merchant_payable)}</strong><small>После удержания комиссии</small></div><div class="metric"><span class="metric-label">Нам должны</span><strong class="metric-value">${money(s.merchant_receivable)}</strong><small>Переплаты мерчантам</small></div><div class="metric"><span class="metric-label">Прибыль</span><strong class="metric-value">${money(s.profit)}</strong><small>Доходы за вычетом расходов</small></div></div>`;
  const month = `<div class="surface"><h2>Результат за месяц</h2><form id="month-form" class="button-row">${field('month','Выберите месяц',input('month','month','required'))}<button class="button secondary">Показать</button></form><div id="month-detail"></div></div>`;
  const flow = table([{title:'Дата',key:'day'},{title:'Событие',render:r=>esc(kindNames[r.event] || actionNames[r.event] || 'Изменение денежных средств')},{title:'Изменение денег',render:r=>`<span class="numeric">${money(r.net_cash)}</span>`}],report.cashflow,'Движений пока нет','Подтверждённые операции появятся здесь.');
  const balance = table([{title:'Что учитываем',render:r=>esc(accountNames[r.account] || 'Прочая позиция')},{title:'К кому относится',render:r=>esc(r.card_id ? cardName(r.card_id) : r.custodian_id ? custodianName(r.custodian_id) : r.merchant_id ? entityName(state.catalog.merchants,r.merchant_id) : 'Компания')},{title:'Сумма',render:r=>`<span class="numeric">${money(r.amount)}</span>`}],report.balances);
  const observations = table([{title:'Карта',render:r=>esc(r.mask)},{title:'По учёту',render:r=>money(r.ledger)},{title:'Наблюдали',render:r=>money(r.observed)},{title:'Разница',render:r=>money(r.difference)}],report.observations,'Наблюдений пока нет','Остаток карты можно внести в разделе «Движение денег».');
  const handovers = table([{title:'Ответственный',render:r=>esc(custodianName(r.collector_id))},{title:'Заявлено',render:r=>money(r.claimed)},{title:'Принято',render:r=>money(r.received)},{title:'Разница',render:r=>money(r.difference)}],report.handover_differences,'Расхождений нет','Принятые суммы совпадают с заявленными.');
  page('Отчёты','Сначала главные показатели. Подробные движения и разницы открываются ниже.',finance + section('Месячный результат',month) + `<details class="section"><summary>Движение денег по датам</summary><div class="details-body">${flow}</div></details><details><summary>Остатки по картам, людям и обязательствам</summary><div class="details-body">${balance}</div></details><details><summary>Разницы по картам и передачам</summary><div class="details-body"><h3>Наблюдаемые остатки карт</h3>${observations}<h3 style="margin-top:23px">Передачи наличных</h3>${handovers}</div></details>`);
  $('#month').value = todayMoscow().slice(0,7);
  $('#month-form').onsubmit = async event => {
    event.preventDefault();
    try {
      const month = (await api('/api/report?month='+encodeURIComponent($('#month').value))).month;
      $('#month-detail').innerHTML = `<div class="summary-strip"><div><small>Выручка</small><strong>${money(month.revenue)}</strong></div><div><small>Расходы</small><strong>${money(month.expenses)}</strong></div><div><small>Прибыль</small><strong>${money(month.profit)}</strong></div><div><small>Состояние</small><strong>${month.approved?'Утверждён':'Ожидает утверждения'}</strong></div></div>${!month.approved && state.user.Role==='accountant' ? '<div class="form-actions"><button id="approve-month" class="button primary">Утвердить эту версию отчёта</button></div>' : ''}`;
      if ($('#approve-month')) $('#approve-month').onclick = async () => { try { await api('/api/report/approve',{month:month.month,digest:month.digest}); notify('Отчёт утверждён.'); await reportsPage(); } catch(error) { notify(error.message,true); } };
    } catch(error) { notify(error.message,true); }
  };
}
async function auditPage() {
  const rows = await api('/api/audit');
  const history = table([
    {title:'Когда',render:r=>esc(dateLabel(r.at))},
    {title:'Действие',render:r=>esc(actionNames[r.action] || kindNames[r.action] || 'Действие в системе')},
    {title:'Кто',render:r=>esc(roleNames[r.role] || 'Пользователь')},
    {title:'Результат',render:r=>r.outcome==='success'?'<span class="status good">Выполнено</span>':'<span class="status bad">Отклонено</span>'},
    {title:'Причина',render:r=>esc(reasonNames[r.reason] || (r.reason ? friendlyError(r.reason) : '—'))}
  ],rows,'История пока пуста','Действия пользователей появятся здесь.');
  page('История действий','Подтверждения, исправления и отклонённые попытки сохраняются для проверки.',history);
}
async function accountPage() {
  page('Настройки входа','Смените пароль; после смены все прежние сеансы завершатся.',`<div class="surface" style="max-width:610px"><h2>Сменить пароль</h2><form id="password-form"><div class="stack">${field('current','Текущий пароль',input('current','password','autocomplete="current-password" required'))}${field('new','Новый пароль',input('new','password','autocomplete="new-password" minlength="16" required'),'Не короче 16 символов.')}</div><div class="form-actions"><button class="button primary">Сохранить новый пароль</button></div></form></div>`);
  $('#password-form').onsubmit = async event => { event.preventDefault(); try { await api('/api/password',values(event.target)); notify('Пароль изменён. Войдите заново.'); location.reload(); } catch(error) { notify(error.message,true); } };
}
syncMenuState();
start();
