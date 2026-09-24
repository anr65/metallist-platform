import React, { useEffect, useState } from 'react';
import { MoreHorizontal } from 'lucide-react';
import { Alert, AlertDescription } from '@/components/ui/alert';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card';
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog';
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuTrigger } from '@/components/ui/dropdown-menu';
import { Input } from '@/components/ui/input';
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table';
import { request, userError } from './api-errors';
import { MoneyForm } from './forms';
import { get, PageState, Status } from './pages';

const names = { withdrawal: 'Снятие', handover: 'Передача главному администратору', transfer: 'Перевод', repayment: 'Возврат мерчанту', injection: 'Внесение', shortage: 'Недостача', writeoff: 'Списание недостачи', surplus: 'Излишек', surplus_income: 'Прочий доход', surplus_merchant: 'Долг мерчанту', surplus_shortage: 'Закрытие недостачи', surplus_return: 'Возврат излишка', collection: 'Инкассация', recovery: 'Возврат долга', forgive_injection: 'Прощение внесения' };
const rub = new Intl.NumberFormat('ru-RU', { style: 'currency', currency: 'RUB' });
const date = new Intl.DateTimeFormat('ru-RU', { dateStyle: 'medium', timeStyle: 'short', timeZone: 'Europe/Moscow' });
const moneyTypes = row => row.kind !== 'expense';
function amountRub(item) { return rub.format(Number(item.payload?.amount_cents || 0) / 100); }
async function post(path, body) { return request(() => fetch(path, { method: 'POST', credentials: 'same-origin', headers: { 'Content-Type': 'application/json', 'X-CSRF': '1' }, body: JSON.stringify(body) })); }
function typeName(row) { return names[row.kind] || 'Денежная операция'; }

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
  const [items, setItems] = useState([]), [catalog, setCatalog] = useState(null), [loading, setLoading] = useState(true), [error, setError] = useState('');
  const id = route !== 'money' && route !== 'money/new' ? route.slice('money/'.length) : null;
  async function load() {
    setLoading(true); setError('');
    try {
      const [rows, data] = await Promise.all([get(id ? `/api/drafts?id=${encodeURIComponent(id)}` : '/api/drafts'), get('/api/catalog')]);
      setItems(rows.filter(moneyTypes)); setCatalog(data);
    } catch (err) { setError(userError(err)); } finally { setLoading(false); }
  }
  useEffect(() => { if (route !== 'money/new') load(); }, [route]);
  if (route === 'money/new') return <PageState title="Новая денежная операция" subtitle="Создание черновика не меняет остатки. Подтверждение выполняется отдельно."><MoneyForm role={role} onCreated={value => onNavigate(`money/${value}`)} /></PageState>;
  if (id) {
    const item = items.find(row => row.id === id);
    const custodian = value => catalog?.custodians?.find(row => row.id === value)?.name || 'Не указан';
    return <PageState title={item ? typeName(item) : 'Денежная операция'} subtitle="Сведения об операции и её текущем состоянии.">{error && <Alert variant="destructive" className="mb-4"><AlertDescription>{error}</AlertDescription></Alert>}{loading ? <p>Загрузка…</p> : !item ? <Alert variant="destructive"><AlertDescription>Операция не найдена или недоступна.</AlertDescription></Alert> : <Card className="max-w-3xl rounded-2xl"><CardHeader className="flex flex-row items-center justify-between gap-3"><CardTitle>{typeName(item)}</CardTitle>{role === 'chief' && (item.status === 'draft' || item.status === 'posted') && <MoneyMenu item={item} role={role} onNavigate={onNavigate} onChanged={load} showOpen={false} />}</CardHeader><CardContent className="grid gap-4 text-sm sm:grid-cols-2"><div>Сумма: <strong>{amountRub(item)}</strong></div><div>Статус: <Status value={item.status} /></div><div>Создано: {date.format(new Date(item.created_at))}</div>{item.payload?.from_custodian_id && <div>Отправитель: {custodian(item.payload.from_custodian_id)}</div>}{item.payload?.to_custodian_id && <div>Получатель: {custodian(item.payload.to_custodian_id)}</div>}{item.payload?.custodian_id && <div>Получил наличные: {custodian(item.payload.custodian_id)}</div>}{item.payload?.reason && <div className="sm:col-span-2">Основание: {item.payload.reason}</div>}{item.payload?.telegram_confirmation_required && <p className="sm:col-span-2 text-muted-foreground">Операция подтверждается автором в Telegram.</p>}</CardContent></Card>}</PageState>;
  }
  return <PageState title="Движение денег" subtitle="Операции с наличными, картами и обязательствами." headingClassName="px-5 lg:px-6">{error && <Alert variant="destructive" className="mx-5 mb-4 lg:mx-6"><AlertDescription>{error}</AlertDescription></Alert>}<div className="section-table-wrap border-y border-border">{loading ? <p className="p-6 text-sm text-muted-foreground">Загрузка…</p> : items.length ? <Table className="section-table section-table--money"><TableHeader><TableRow><TableHead>Действие</TableHead><TableHead>Сумма</TableHead><TableHead>Создано</TableHead><TableHead>Статус</TableHead><TableHead>Действия</TableHead></TableRow></TableHeader><TableBody>{items.map(item => <TableRow key={item.id}><TableCell><button type="button" className="text-left font-medium text-primary hover:underline" onClick={() => onNavigate(`money/${item.id}`)}>{typeName(item)}</button></TableCell><TableCell>{amountRub(item)}</TableCell><TableCell>{date.format(new Date(item.created_at))}</TableCell><TableCell><Status value={item.status} /></TableCell><TableCell><MoneyMenu item={item} role={role} onNavigate={onNavigate} onChanged={load} /></TableCell></TableRow>)}</TableBody></Table> : <p className="p-6 text-sm text-muted-foreground">Операций пока нет.</p>}</div></PageState>;
}
