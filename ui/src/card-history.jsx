import React, { useEffect, useState } from 'react';
import { Alert, AlertDescription } from '@/components/ui/alert';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle, DialogTrigger } from '@/components/ui/dialog';
import { Input } from '@/components/ui/input';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select';
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table';
import { get, PageState } from './pages';
import { ServerTablePagination } from './pagination';
import { money as exactMoney } from './overview-metrics';
import { pageForRole } from './access';
import { cardStatusLabels } from './card-filters';
import { expenseCategories } from './forms';

const date = new Intl.DateTimeFormat('ru-RU', { dateStyle: 'short', timeStyle: 'short', timeZone: 'Europe/Moscow' });
const amount = value => value == null ? '—' : exactMoney(value);
const when = value => value ? date.format(new Date(value)) : '—';
const kinds = { registry: 'Пополнение по реестру', withdrawal: 'Снятие', expense: 'Расход', repayment: 'Возврат мерчанту', injection: 'Внесение', shortage: 'Недостача', writeoff: 'Списание недостачи', surplus: 'Излишек', surplus_return: 'Возврат излишка', recovery: 'Возврат долга', reversal: 'Сторно', opening: 'Начальный остаток', opening_balance: 'Начальный остаток', observation: 'Сообщённый остаток' };
const statuses = { posted: 'Проведено', reversed: 'Сторнировано', reversal: 'Сторно проведено', draft: 'Черновик', rejected: 'Отклонено', preview: 'Не подтверждено', deleted: 'Удалено без проведения', observation: 'Наблюдение' };
const categories = Object.fromEntries(expenseCategories);
const accounts = { '1100': 'Деньги на картах', '1200': 'Наличные у хранителя', '1210': 'Наличные у главного администратора', '2100': 'Долг перед мерчантом', '3100': 'Капитал', '4100': 'Комиссионная выручка', '5100': 'Расходы', '5200': 'Агентские', '5300': 'Банковская комиссия', '5900': 'Прочие расходы' };
const emptyFilters = { scope: 'all', date_from: '', date_to: '' };

function Details({ item, role, onNavigate }) {
  const d = item.details || {};
  const documentRoute = `${item.document_kind === 'registry' ? 'registries' : 'money'}/${item.document_id}`;
  return <Dialog><DialogTrigger asChild><Button type="button" variant="ghost" className="px-0 text-primary">Подробности</Button></DialogTrigger><DialogContent className="card-history-detail-panel max-h-[85dvh] overflow-y-auto sm:max-w-2xl"><DialogHeader><DialogTitle>{kinds[item.kind] || 'Корректировка'}</DialogTitle><DialogDescription>{item.cards || item.reference || 'Документ по карте'}</DialogDescription></DialogHeader><div className="grid gap-4 pb-2">
    <dl className="grid gap-2"><div><dt>Автор документа</dt><dd>{item.creator || item.actor || '—'}</dd></div><div><dt>Записано в системе</dt><dd>{when(item.recorded_at)}</dd></div><div><dt>Основание / комментарий</dt><dd>{item.reason || '—'}</dd></div><div><dt>Идентификатор записи</dt><dd className="break-all font-mono text-xs">{item.id}</dd></div>{item.reversal_of && <div><dt>Сторно записи</dt><dd className="break-all font-mono text-xs">{item.reversal_of}</dd></div>}</dl>
    {item.document_id && pageForRole(role, `#${documentRoute}`) === documentRoute && <Button type="button" variant="secondary" onClick={() => onNavigate(`${item.document_kind === 'registry' ? 'registries' : 'money'}/${item.document_id}`)}>Открыть {item.document_kind === 'registry' ? 'реестр' : 'операцию'}</Button>}
    {d.rows?.length > 0 && <div><strong>Строки реестра по карте</strong>{d.rows.map((row, i) => <p key={i} className="mt-2">Строка {row.row}: {amount(row.amount)}<br />RRN: {row.rrn || '—'}<br />Внешний заказ: {row.order || '—'}</p>)}</div>}
    {d.postings?.length > 0 && <div><strong>Проводки документа</strong>{d.postings.map((p, i) => <p key={i} className="mt-2">{p.side === 'debit' ? 'Дебет' : 'Кредит'} {p.account} · {accounts[p.account] || 'Счёт учёта'}: {amount(p.amount)}<br />{[p.merchant, p.custodian, p.card, categories[p.category]].filter(Boolean).join(', ') || 'Без участника'}</p>)}</div>}
    {d.source && <p>Источник наблюдения: <span className="break-all">{d.source}</span></p>}
  </div></DialogContent></Dialog>;
}

export function CardHistory({ role, route, onNavigate }) {
  const cardID = route.split('/')[2];
  const [filters, setFilters] = useState(emptyFilters);
  const [applied, setApplied] = useState(emptyFilters);
  const [page, setPage] = useState(1);
  const [data, setData] = useState(null);
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(true);
  const [revision, setRevision] = useState(0);
  useEffect(() => {
    let active = true;
    setLoading(true); setError(''); setData(null);
    const query = new URLSearchParams({ card_id: cardID, page: String(page), ...applied, scope: applied.scope === 'all' ? '' : applied.scope });
    get(`/api/card-history?${query}`).then(result => { if (active) setData(result); }).catch(err => { if (active) setError(err.message); }).finally(() => { if (active) setLoading(false); });
    return () => { active = false; };
  }, [cardID, page, applied, revision]);
  const update = (key, value) => setFilters(current => ({ ...current, [key]: value }));
  const card = data?.card;
  return <section className="card-history"><PageState title={card ? `Операции по карте ${card.mask}` : 'Операции по карте'} subtitle={card ? `${card.bank}, ${card.owner}. ${cardStatusLabels[card.status] || card.status}.` : 'История движения денег и документы по карте.'} headingClassName="px-5 lg:px-6">
    <div className="card-history-context mx-5 mb-5 lg:mx-6"><div><span className="text-sm text-muted-foreground">{card?.shared ? 'Общий учётный остаток группы карт' : 'Текущий учётный остаток'}</span><strong className="block font-mono text-2xl tabular-nums">{data ? amount(data.balance) : '—'}</strong></div><p className="max-w-xl text-sm text-muted-foreground">{card?.shared && 'Показаны операции пластиковой и привязанных виртуальных карт. '}Черновики, отклонённые документы и сообщённые остатки не меняют учёт. Сторно показано отдельным движением.</p></div>
    <form className="mx-5 mb-5 grid items-end gap-3 sm:grid-cols-2 xl:grid-cols-[minmax(180px,1fr)_1fr_1fr_auto] lg:mx-6" onSubmit={event => { event.preventDefault(); setPage(1); setApplied({ ...filters }); }}>
      <label className="grid min-w-0 gap-2 text-sm font-medium">Записи<Select value={filters.scope} onValueChange={value => update('scope', value)}><SelectTrigger className="w-full"><SelectValue /></SelectTrigger><SelectContent><SelectItem value="all">Все записи</SelectItem><SelectItem value="ledger">Движения денег</SelectItem><SelectItem value="draft">Черновики и отклонения</SelectItem><SelectItem value="registry">Непроведённые реестры</SelectItem><SelectItem value="observation">Сообщённые остатки</SelectItem></SelectContent></Select></label>
      <label className="grid min-w-0 gap-2 text-sm font-medium">Дата от<Input type="date" value={filters.date_from} max={filters.date_to || undefined} onChange={e => update('date_from', e.target.value)} /></label>
      <label className="grid min-w-0 gap-2 text-sm font-medium">Дата до<Input type="date" value={filters.date_to} min={filters.date_from || undefined} onChange={e => update('date_to', e.target.value)} /></label>
      <div className="flex flex-wrap gap-2"><Button type="submit" variant="primary">Показать</Button><Button type="button" variant="secondary" onClick={() => { setFilters(emptyFilters); setApplied({ ...emptyFilters }); setPage(1); }}>Сбросить</Button></div>
    </form>
    {error && <Alert variant="destructive" className="mx-5 mb-5 w-auto lg:mx-6"><AlertDescription>{error}<Button type="button" variant="secondary" onClick={() => setRevision(x => x + 1)}>Повторить</Button></AlertDescription></Alert>}
    <div className="section-table-wrap border-y border-border" aria-busy={loading}>
      {loading ? <p role="status" className="p-6 text-sm text-muted-foreground">Загрузка истории…</p> : data && (data.items.length ? <Table className="card-history-table"><TableHeader><TableRow>{['Дата события / записи', 'Операция / статус', 'Приход', 'Расход', 'Остаток после', 'Участники / источник', 'Основание / детали'].map(label => <TableHead key={label}>{label}</TableHead>)}</TableRow></TableHeader><TableBody>{data.items.map(item => <TableRow key={item.id}>
        <TableCell data-label="Дата события / записи"><time>{when(item.occurred_at)}</time><span className="block text-xs text-muted-foreground">Запись: {when(item.recorded_at)}</span></TableCell>
        <TableCell data-label="Операция / статус"><strong>{kinds[item.kind] || 'Корректировка'}</strong><Badge variant="outline" className="mt-1 block w-fit">{statuses[item.status] || 'Без статуса'}</Badge>{item.scope === 'draft' || item.scope === 'registry' ? <span className="mt-1 block text-xs">Заявлено: {amount(item.amount)}</span> : null}{item.scope === 'observation' && <span className="mt-1 block text-sm">Сообщено: {amount(item.observed)}</span>}</TableCell>
        <TableCell data-label="Приход" className="card-history-number">{item.income != null && item.income !== '0.00' ? amount(item.income) : '—'}</TableCell>
        <TableCell data-label="Расход" className="card-history-number">{item.expense != null && item.expense !== '0.00' ? amount(item.expense) : '—'}</TableCell>
        <TableCell data-label="Остаток после" className="card-history-number font-semibold">{amount(item.balance)}</TableCell>
        <TableCell data-label="Участники / источник"><span className="block font-medium">{item.cards || '—'}</span><span>{[item.details?.merchant, item.details?.custodian, ...new Set((item.details?.postings || []).flatMap(p => [p.merchant, p.custodian]).filter(Boolean))].filter((x, i, all) => x && all.indexOf(x) === i).join(', ') || '—'}</span>{item.details?.category && <span className="block">{categories[item.details.category] || 'Прочий расход'}</span>}<span className="block text-xs text-muted-foreground">{item.reference || item.details?.channel || 'Документ операции'}</span><span className="block text-xs">{item.actor || item.creator || '—'}</span></TableCell>
        <TableCell data-label="Основание / детали"><p className="mb-1">{item.reason || '—'}</p><Details item={item} role={role} onNavigate={onNavigate} /></TableCell>
      </TableRow>)}</TableBody></Table> : <p className="p-6 text-sm text-muted-foreground">Записей нет. Если заданы фильтры, измените период или выберите все записи.</p>)}
      {data && <ServerTablePagination page={page} hasMore={data.has_more} onPageChange={setPage} />}
    </div>
  </PageState></section>;
}
