import React, { useEffect, useState } from 'react';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Skeleton } from '@/components/ui/skeleton';
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table';
import { get, PageState } from './pages';

const money = new Intl.NumberFormat('ru-RU', { style: 'currency', currency: 'RUB', minimumFractionDigits: 2 });
const dateTime = new Intl.DateTimeFormat('ru-RU', { dateStyle: 'medium', timeStyle: 'short', timeZone: 'Europe/Moscow' });
const amount = value => money.format(Number(value || 0));
const cardStatuses = { active: 'Активна', blocked: 'Заблокирована', retired: 'Выведена' };

function BalanceTotal({ label, value, prominent = false }) {
  return <Card className={`rounded-2xl ${prominent ? 'border-primary/25 bg-primary/5' : ''}`}><CardContent className="p-5"><p className="text-sm text-muted-foreground">{label}</p><strong className="mt-3 block font-mono text-xl font-semibold tabular-nums sm:text-2xl">{amount(value)}</strong></CardContent></Card>;
}

function CashTable({ title, rows, empty }) {
  return <Card className="overflow-hidden rounded-2xl"><CardHeader><CardTitle>{title}</CardTitle><CardDescription>{rows.length} {rows.length === 1 ? 'хранитель' : 'хранителей'}</CardDescription></CardHeader><CardContent className="p-0">{rows.length ? <div className="overflow-x-auto"><Table><TableHeader><TableRow><TableHead>Ответственный</TableHead><TableHead className="text-right">Учётный остаток</TableHead></TableRow></TableHeader><TableBody>{rows.map(row => <TableRow key={row.id}><TableCell><span className="font-medium">{row.name}</span>{!row.active && <Badge variant="outline" className="ml-2">Неактивен</Badge>}</TableCell><TableCell className="text-right font-mono font-semibold tabular-nums">{amount(row.amount)}</TableCell></TableRow>)}</TableBody></Table></div> : <p className="px-6 pb-6 text-sm text-muted-foreground">{empty}</p>}</CardContent></Card>;
}

export function Balances() {
  const [data, setData] = useState(null);
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(true);
  const [search, setSearch] = useState('');
  async function refresh() {
    setLoading(true);
    setError('');
    try { setData(await get('/api/balances')); } catch (e) { setError(e.message); } finally { setLoading(false); }
  }
  useEffect(() => { refresh(); }, []);
  const cards = data?.cards || [];
  const visibleCards = cards.filter(card => `${card.bank} ${card.owner} ${card.mask}`.toLocaleLowerCase('ru-RU').includes(search.trim().toLocaleLowerCase('ru-RU')));
  const custodians = data?.custodians || [];
  return <PageState title="Остатки" subtitle="Учётные суммы после подтверждённых операций. Передача денег меняет хранителя, но не общий остаток.">
    <div className="mb-5 flex flex-wrap items-center justify-between gap-3"><p className="text-sm text-muted-foreground">{data ? `По состоянию на ${dateTime.format(new Date(data.as_of))}` : 'Загрузка остатков…'}</p><Button type="button" variant="outline" onClick={refresh} disabled={loading}>{loading ? 'Обновление…' : 'Обновить'}</Button></div>
    {error && <Card className="mb-5"><CardContent className="p-5 text-destructive">{error}</CardContent></Card>}
    {!data && loading ? <div className="grid gap-4"><Skeleton className="h-28 rounded-2xl" /><Skeleton className="h-64 rounded-2xl" /></div> : data && <>
      <div className="mb-6 grid gap-3 sm:grid-cols-2 xl:grid-cols-4"><BalanceTotal label="Всего на картах и у хранителей" value={data.totals.all} prominent /><BalanceTotal label="На картах" value={data.totals.cards} /><BalanceTotal label="У сборщиков" value={data.totals.collectors} /><BalanceTotal label="У главного администратора" value={data.totals.chief} /></div>
      {Number(data.totals.operators) !== 0 && <p className="mb-6 text-sm text-muted-foreground">В общей сумме также учтено у операционистов: <strong className="text-foreground">{amount(data.totals.operators)}</strong>.</p>}
      <div className="grid gap-5 xl:grid-cols-2"><CashTable title="Главный администратор" rows={custodians.filter(row => row.kind === 'chief')} empty="Хранитель не задан." /><CashTable title="Сборщики" rows={custodians.filter(row => row.kind === 'collector')} empty="Сборщиков пока нет." /></div>
      {custodians.some(row => row.kind === 'operator') && <div className="mt-5"><CashTable title="Операционисты с наличными" rows={custodians.filter(row => row.kind === 'operator')} empty="Операционистов пока нет." /></div>}
      <Card className="mt-5 overflow-hidden rounded-2xl"><CardHeader className="gap-4 sm:flex-row sm:items-end sm:justify-between"><div><CardTitle>Карты</CardTitle><CardDescription>Все карты, включая карты с нулевым остатком и выведенные из работы: {cards.length}</CardDescription></div><Input className="sm:max-w-xs" aria-label="Поиск карты" placeholder="Банк, владелец или маска" value={search} onChange={event => setSearch(event.target.value)} /></CardHeader><CardContent className="p-0">{visibleCards.length ? <div className="overflow-x-auto"><Table><TableHeader><TableRow><TableHead>Карта</TableHead><TableHead>Владелец</TableHead><TableHead>Состояние</TableHead><TableHead className="text-right">Учётный остаток</TableHead><TableHead className="text-right">Последний сообщённый остаток</TableHead></TableRow></TableHeader><TableBody>{visibleCards.map(card => <TableRow key={card.id}><TableCell><span className="font-medium">{card.mask}</span><span className="block text-xs text-muted-foreground">{card.bank}</span></TableCell><TableCell>{card.owner}</TableCell><TableCell><Badge variant={card.status === 'active' ? 'secondary' : 'outline'}>{cardStatuses[card.status] || card.status}</Badge></TableCell><TableCell className="text-right font-mono font-semibold tabular-nums">{amount(card.amount)}</TableCell><TableCell className="text-right">{card.observed_at ? <><span className="font-mono tabular-nums">{amount(card.observed)}</span><span className="block text-xs text-muted-foreground">{dateTime.format(new Date(card.observed_at))}</span></> : <span className="text-muted-foreground">—</span>}</TableCell></TableRow>)}</TableBody></Table></div> : <p className="px-6 pb-6 text-sm text-muted-foreground">{search ? 'По запросу карты не найдены.' : 'Карт пока нет.'}</p>}</CardContent></Card>
      <p className="mt-4 text-xs leading-5 text-muted-foreground">Общая сумма складывается из учётных остатков. Сообщённый при снятии остаток — отдельное наблюдение на указанную дату; сам по себе он не создаёт проводку и может отличаться после следующих операций.</p>
    </>}
  </PageState>;
}
