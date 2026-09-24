import React, { useEffect, useRef, useState } from 'react';
import { ListFilter, MoreHorizontal } from 'lucide-react';
import { Alert, AlertDescription } from '@/components/ui/alert';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card';
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog';
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuTrigger } from '@/components/ui/dropdown-menu';
import { Input } from '@/components/ui/input';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select';
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table';
import { request, userError } from './api-errors';
import { MoneyForm, expenseCategories } from './forms';
import { get, PageState, Status } from './pages';

const names = { withdrawal: 'Снятие', handover: 'Передача главному администратору', transfer: 'Перевод', expense: 'Расход', repayment: 'Возврат мерчанту', injection: 'Внесение', shortage: 'Недостача', writeoff: 'Списание недостачи', surplus: 'Излишек', surplus_income: 'Прочий доход', surplus_merchant: 'Долг мерчанту', surplus_shortage: 'Закрытие недостачи', surplus_return: 'Возврат излишка', collection: 'Инкассация', recovery: 'Возврат долга', forgive_injection: 'Прощение внесения' };
const rub = new Intl.NumberFormat('ru-RU', { style: 'currency', currency: 'RUB' });
const date = new Intl.DateTimeFormat('ru-RU', { dateStyle: 'medium', timeStyle: 'short', timeZone: 'Europe/Moscow' });
const categoryNames = { ...Object.fromEntries(expenseCategories), operating: 'Операционные расходы', transport: 'Транспорт' };
function amountRub(item) { return rub.format(Number(item.payload?.amount_cents || 0) / 100); }
function operationDate(item) { return item.kind === 'expense' && item.payload?.date ? new Intl.DateTimeFormat('ru-RU', { dateStyle: 'medium', timeZone: 'Europe/Moscow' }).format(new Date(`${item.payload.date}T12:00:00+03:00`)) : date.format(new Date(item.created_at)); }
async function post(path, body) { return request(() => fetch(path, { method: 'POST', credentials: 'same-origin', headers: { 'Content-Type': 'application/json', 'X-CSRF': '1' }, body: JSON.stringify(body) })); }
function typeName(row) { return row.kind === 'expense' ? `Расход · ${categoryNames[row.payload?.category] || 'Тип не указан'}` : names[row.kind] || 'Денежная операция'; }
const emptyFilters = { kind: 'all', source: 'all', recipient: 'all', amount_from: '', amount_to: '', date_from: '', date_to: '' };
function partyName(catalog, type, id) {
  if (!id) return '—';
  if (type === 'card') { const card = catalog?.cards?.find(row => row.id === id); return card ? `Карта ${card.mask}` : 'Карта не найдена'; }
  if (type === 'merchant') return catalog?.merchants?.find(row => row.id === id)?.name || 'Мерчант не найден';
  if (type === 'category') return categoryNames[id] || 'Тип расхода не указан';
  return catalog?.custodians?.find(row => row.id === id)?.name || 'Ответственный не найден';
}
function sourceName(item, catalog) {
  const p = item.payload || {};
  if (p.from_custodian_id) return partyName(catalog, 'custodian', p.from_custodian_id);
  if (p.source_id) return partyName(catalog, p.source_kind === 'card' ? 'card' : 'custodian', p.source_id);
  if (p.card_id) return partyName(catalog, 'card', p.card_id);
  if (p.person_id) return partyName(catalog, 'custodian', p.person_id);
  if (['shortage', 'writeoff'].includes(item.kind) && p.custodian_id) return partyName(catalog, 'custodian', p.custodian_id);
  return '—';
}
function recipientName(item, catalog) {
  const p = item.payload || {};
  if (p.to_custodian_id) return partyName(catalog, 'custodian', p.to_custodian_id);
  if (item.kind === 'withdrawal' && p.custodian_id) return partyName(catalog, 'custodian', p.custodian_id);
  if (p.merchant_id) return partyName(catalog, 'merchant', p.merchant_id);
  if (item.kind === 'expense') return `Расход: ${partyName(catalog, 'category', p.category)}`;
  if (p.destination_id) return partyName(catalog, p.destination_kind === 'card' ? 'card' : 'custodian', p.destination_id);
  return '—';
}

function MoneyMenu({ item, role, onNavigate, onChanged, showOpen = true }) {
  const [action, setAction] = useState(''), [reason, setReason] = useState(''), [confirmAmount, setConfirmAmount] = useState(''), [busy, setBusy] = useState(false), [error, setError] = useState('');
  const canConfirm = role === 'chief' && item.status === 'draft' && !item.payload?.telegram_confirmation_required;
  const canReject = role === 'chief' && item.status === 'draft';
  const canReverse = role === 'chief' && item.status === 'posted';
  async function submit(event) {
    event.preventDefault(); setBusy(true); setError('');
    try {
      if (action === 'confirm') await post('/api/draft/confirm', { id: item.id, version: String(item.version), confirm_amount: confirmAmount });
      if (action === 'reject') await post('/api/draft/reject', { id: item.id, version: String(item.version), reason: reason.trim() });
      if (action === 'reverse') await post('/api/draft/reverse', { id: item.id, reason: reason.trim() });
      setAction(''); setReason(''); await onChanged();
    } catch (err) { setError(userError(err)); } finally { setBusy(false); }
  }
  return <>
    <DropdownMenu><DropdownMenuTrigger asChild><Button type="button" variant="ghost" size="icon" aria-label={`Действия: ${typeName(item)}`}><MoreHorizontal className="size-5" /></Button></DropdownMenuTrigger><DropdownMenuContent align="end">{showOpen && <DropdownMenuItem onSelect={() => onNavigate(`money/${item.id}`)}>Открыть операцию</DropdownMenuItem>}{showOpen && (canConfirm || canReject || canReverse) && <DropdownMenuSeparator />}{canConfirm && <DropdownMenuItem onSelect={() => { setConfirmAmount(item.payload?.amount || ''); setAction('confirm'); }}>Подтвердить</DropdownMenuItem>}{canReject && <DropdownMenuItem variant="destructive" onSelect={() => setAction('reject')}>Отменить черновик</DropdownMenuItem>}{canReverse && <DropdownMenuItem variant="destructive" onSelect={() => setAction('reverse')}>Сторнировать операцию</DropdownMenuItem>}</DropdownMenuContent></DropdownMenu>
    <Dialog open={Boolean(action)} onOpenChange={open => { if (!open && !busy) { setAction(''); setError(''); setReason(''); } }}><DialogContent><DialogHeader><DialogTitle>{action === 'confirm' ? 'Подтвердить операцию?' : action === 'reject' ? 'Отклонить черновик?' : 'Сторнировать операцию?'}</DialogTitle><DialogDescription>{typeName(item)} · {amountRub(item)}. {action === 'confirm' ? 'Проводки появятся после подтверждения точной суммы.' : 'Укажите причину для аудита.'}</DialogDescription></DialogHeader><form onSubmit={submit} className="grid gap-4">{action === 'confirm' ? <label className="grid gap-2 text-sm font-medium">Подтверждаемая сумма, ₽<Input value={confirmAmount} onChange={event => setConfirmAmount(event.target.value)} inputMode="decimal" required /></label> : <label className="grid gap-2 text-sm font-medium">Причина<Input value={reason} onChange={event => setReason(event.target.value)} maxLength={500} required /></label>}{error && <Alert variant="destructive"><AlertDescription>{error}</AlertDescription></Alert>}<DialogFooter><Button type="button" variant="secondary" onClick={() => setAction('')} disabled={busy}>Отмена</Button><Button type="submit" variant={action === 'confirm' ? 'primary' : 'destructive-secondary'} disabled={busy || action !== 'confirm' && !reason.trim()}>{busy ? 'Выполняется…' : action === 'confirm' ? 'Подтвердить' : action === 'reject' ? 'Отклонить' : 'Сторнировать'}</Button></DialogFooter></form></DialogContent></Dialog>
  </>;
}

export function MoneyPage({ role, route = 'money', onNavigate }) {
  const [items, setItems] = useState([]);
  const [catalog, setCatalog] = useState(null);
  const [loading, setLoading] = useState(true);
  const [loadingMore, setLoadingMore] = useState(false);
  const [hasMore, setHasMore] = useState(false);
  const [pageNo, setPageNo] = useState(1);
  const [filters, setFilters] = useState({ ...emptyFilters });
  const [appliedFilters, setAppliedFilters] = useState({ ...emptyFilters });
  const [filterOpen, setFilterOpen] = useState(false);
  const [error, setError] = useState('');
  const requestId = useRef(0);
  const id = route !== 'money' && route !== 'money/new' ? route.slice('money/'.length) : null;

  async function load(nextPage = 1, selectedFilters = appliedFilters) {
    const currentRequest = ++requestId.current;
    if (nextPage === 1) setLoading(true);
    else setLoadingMore(true);
    setError('');
    try {
      const params = new URLSearchParams({ page: String(nextPage) });
      if (!id) for (const [key, value] of Object.entries(selectedFilters)) if (value && value !== 'all') params.set(key, value);
      const path = id ? `/api/drafts?id=${encodeURIComponent(id)}` : `/api/drafts?${params}`;
      const [rows, data] = await Promise.all([get(path), get('/api/catalog')]);
      if (currentRequest !== requestId.current) return;
      setItems(previous => nextPage === 1 ? rows : [...previous, ...rows]);
      setCatalog(data);
      setHasMore(!id && rows.length === 100);
      setPageNo(nextPage);
    } catch (err) { if (currentRequest === requestId.current) setError(userError(err)); }
    finally { if (currentRequest === requestId.current) { setLoading(false); setLoadingMore(false); } }
  }
  useEffect(() => { if (route !== 'money/new') load(1, appliedFilters); else requestId.current++; }, [route, appliedFilters]);
  const refresh = () => load(1, appliedFilters);
  function applyFilters(event) {
    event.preventDefault();
    setFilterOpen(false);
    if (JSON.stringify(filters) === JSON.stringify(appliedFilters)) refresh();
    else setAppliedFilters({ ...filters });
  }
  function resetFilters() {
    setFilterOpen(false);
    setFilters({ ...emptyFilters });
    if (JSON.stringify(appliedFilters) === JSON.stringify(emptyFilters)) refresh();
    else setAppliedFilters({ ...emptyFilters });
  }

  if (route === 'money/new') return <PageState title="Новая операция" subtitle="Создание черновика не меняет остатки. Подтверждение выполняется отдельно."><MoneyForm role={role} onCreated={value => onNavigate(`money/${value}`)} /></PageState>;
  if (id) {
    const item = items.find(row => row.id === id);
    return <PageState title={item ? typeName(item) : 'Операция'} subtitle="Сведения об операции и её текущем состоянии.">
      {error && <Alert variant="destructive" className="mb-4"><AlertDescription>{error}</AlertDescription></Alert>}
      {loading ? <p>Загрузка…</p> : !item ? <Alert variant="destructive"><AlertDescription>Операция не найдена или недоступна.</AlertDescription></Alert> : <Card className="max-w-3xl rounded-2xl">
        <CardHeader className="flex flex-row items-center justify-between gap-3"><CardTitle>{typeName(item)}</CardTitle>{role === 'chief' && (item.status === 'draft' || item.status === 'posted') && <MoneyMenu item={item} role={role} onNavigate={onNavigate} onChanged={refresh} showOpen={false} />}</CardHeader>
        <CardContent className="grid gap-4 text-sm sm:grid-cols-2">
          <div>Сумма: <strong>{amountRub(item)}</strong></div><div>Статус: <Status value={item.status} /></div>
          <div>Дата операции: {operationDate(item)}</div><div>Создано: {date.format(new Date(item.created_at))}</div>
          <div>Источник: {sourceName(item, catalog)}</div><div>Получатель / назначение: {recipientName(item, catalog)}</div>
          {item.payload?.reason && <div className="sm:col-span-2">Основание: {item.payload.reason}</div>}
          {item.payload?.telegram_confirmation_required && <p className="sm:col-span-2 text-muted-foreground">Операция подтверждается автором в Telegram.</p>}
        </CardContent>
      </Card>}
    </PageState>;
  }

  const updateFilter = (key, value) => setFilters(previous => ({ ...previous, [key]: value }));
  const sourceOptions = [
    ...(catalog?.cards || []).map(item => [ `card:${item.id}`, `Карта ${item.mask}` ]),
    ...(catalog?.custodians || []).map(item => [ `custodian:${item.id}`, item.name ]),
  ];
  const recipientOptions = [
    ...sourceOptions,
    ...(catalog?.merchants || []).map(item => [ `merchant:${item.id}`, `Мерчант · ${item.name}` ]),
    ...Object.entries(categoryNames).map(([value, label]) => [ `category:${value}`, `Расход · ${label}` ]),
  ];
  const activeFilterCount = Object.values(appliedFilters).filter(value => value && value !== 'all').length;
  return <PageState title="Операции" subtitle="Денежные движения и расходы с отдельным подтверждением." headingClassName="px-5 lg:px-6">
    <Button type="button" variant="secondary" className="mx-5 mb-5 sm:hidden" aria-controls="operation-filters" aria-expanded={filterOpen} onClick={() => setFilterOpen(value => !value)}><ListFilter className="size-4" />Фильтры{activeFilterCount > 0 ? ` · ${activeFilterCount}` : ''}</Button>
    <form id="operation-filters" className={`${filterOpen ? 'grid' : 'hidden sm:grid'} gap-3 border-b border-border px-5 pb-5 lg:px-6`} onSubmit={applyFilters}>
      <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
        <label className="grid min-w-0 gap-1.5 text-sm font-medium">Тип операции
          <Select value={filters.kind} onValueChange={value => updateFilter('kind', value)}><SelectTrigger><SelectValue /></SelectTrigger><SelectContent><SelectItem value="all">Все типы</SelectItem>{Object.entries(names).map(([value, label]) => <SelectItem key={value} value={value}>{label}</SelectItem>)}</SelectContent></Select>
        </label>
        <label className="grid min-w-0 gap-1.5 text-sm font-medium">Источник
          <Select value={filters.source} onValueChange={value => updateFilter('source', value)}><SelectTrigger><SelectValue /></SelectTrigger><SelectContent><SelectItem value="all">Все источники</SelectItem>{sourceOptions.map(([value, label]) => <SelectItem key={value} value={value}>{label}</SelectItem>)}</SelectContent></Select>
        </label>
        <label className="grid min-w-0 gap-1.5 text-sm font-medium">Получатель / назначение
          <Select value={filters.recipient} onValueChange={value => updateFilter('recipient', value)}><SelectTrigger><SelectValue /></SelectTrigger><SelectContent><SelectItem value="all">Все получатели</SelectItem>{recipientOptions.map(([value, label]) => <SelectItem key={value} value={value}>{label}</SelectItem>)}</SelectContent></Select>
        </label>
        <label className="grid min-w-0 gap-1.5 text-sm font-medium">Сумма от, ₽<Input value={filters.amount_from} onChange={event => updateFilter('amount_from', event.target.value)} inputMode="decimal" placeholder="0,00" /></label>
        <label className="grid min-w-0 gap-1.5 text-sm font-medium">Сумма до, ₽<Input value={filters.amount_to} onChange={event => updateFilter('amount_to', event.target.value)} inputMode="decimal" placeholder="Без ограничения" /></label>
        <label className="grid min-w-0 gap-1.5 text-sm font-medium">Дата от<Input type="date" value={filters.date_from} onChange={event => updateFilter('date_from', event.target.value)} /></label>
        <label className="grid min-w-0 gap-1.5 text-sm font-medium">Дата до<Input type="date" value={filters.date_to} onChange={event => updateFilter('date_to', event.target.value)} /></label>
      </div>
      <div className="flex flex-wrap gap-2"><Button type="submit" variant="primary">Показать</Button><Button type="button" variant="secondary" onClick={resetFilters}>Сбросить</Button></div>
    </form>
    {error && <Alert variant="destructive" className="mx-5 my-4 lg:mx-6"><AlertDescription>{error}</AlertDescription></Alert>}
    <div className="section-table-wrap border-b border-border">
      {loading ? <p className="p-6 text-sm text-muted-foreground">Загрузка…</p> : items.length ? <Table className="section-table section-table--money">
        <TableHeader><TableRow><TableHead>Действие</TableHead><TableHead>Источник</TableHead><TableHead>Получатель / назначение</TableHead><TableHead>Сумма</TableHead><TableHead>Дата операции</TableHead><TableHead>Статус</TableHead><TableHead>Действия</TableHead></TableRow></TableHeader>
        <TableBody>{items.map(item => <TableRow key={item.id}>
          <TableCell><button type="button" className="text-left font-medium text-primary hover:underline" onClick={() => onNavigate(`money/${item.id}`)}>{typeName(item)}</button></TableCell>
          <TableCell>{sourceName(item, catalog)}</TableCell><TableCell>{recipientName(item, catalog)}</TableCell>
          <TableCell>{amountRub(item)}</TableCell><TableCell>{operationDate(item)}</TableCell><TableCell><Status value={item.status} /></TableCell>
          <TableCell><MoneyMenu item={item} role={role} onNavigate={onNavigate} onChanged={refresh} /></TableCell>
        </TableRow>)}</TableBody>
      </Table> : <p className="p-6 text-sm text-muted-foreground">{Object.values(appliedFilters).some(value => value && value !== 'all') ? 'По заданным фильтрам операций нет.' : 'Операций пока нет.'}</p>}
    </div>
    {hasMore && !loading && <div className="flex justify-center py-5"><Button type="button" variant="secondary" disabled={loadingMore} onClick={() => load(pageNo + 1, appliedFilters)}>{loadingMore ? 'Загрузка…' : 'Показать ещё'}</Button></div>}
  </PageState>;
}
