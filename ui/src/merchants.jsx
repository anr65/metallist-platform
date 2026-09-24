import React, { useEffect, useState } from 'react';
import { MoreHorizontal } from 'lucide-react';
import { Alert, AlertDescription } from '@/components/ui/alert';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog';
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuTrigger } from '@/components/ui/dropdown-menu';
import { Input } from '@/components/ui/input';
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table';
import { request, userError } from './api-errors';
import { get, PageState } from './pages';
import { TariffManager } from './forms';
import { TablePagination, usePagination } from './pagination';

async function post(path, body) { return request(() => fetch(path, { method: 'POST', credentials: 'same-origin', headers: { 'Content-Type': 'application/json', 'X-CSRF': '1' }, body: JSON.stringify(body) })); }
function rate(merchant) { return merchant.rate_bp >= 0 ? `${(merchant.rate_bp / 100).toLocaleString('ru-RU', { minimumFractionDigits: 2, maximumFractionDigits: 2 })} %` : 'Не задана'; }

function MerchantMenu({ merchant, role, onNavigate, onChanged }) {
  const [open, setOpen] = useState(false), [busy, setBusy] = useState(false), [error, setError] = useState('');
  async function remove() {
    setBusy(true); setError('');
    try { await post('/api/merchant/delete', { id: merchant.id, version: String(merchant.version) }); setOpen(false); onChanged(); }
    catch (e) { setError(userError(e)); } finally { setBusy(false); }
  }
  return <>
    <DropdownMenu><DropdownMenuTrigger asChild><Button type="button" variant="ghost" size="icon" aria-label={`Действия с мерчантом ${merchant.name}`}><MoreHorizontal className="size-5" /></Button></DropdownMenuTrigger><DropdownMenuContent align="end"><DropdownMenuItem onSelect={() => onNavigate(`merchants/${merchant.id}`)}>Открыть мерчанта</DropdownMenuItem>{role === 'chief' && merchant.active && <><DropdownMenuSeparator /><DropdownMenuItem variant="destructive" onSelect={() => setOpen(true)}>Удалить мерчанта</DropdownMenuItem></>}</DropdownMenuContent></DropdownMenu>
    <Dialog open={open} onOpenChange={value => !busy && setOpen(value)}><DialogContent><DialogHeader><DialogTitle>Удалить мерчанта?</DialogTitle><DialogDescription>{merchant.name} будет скрыт из активного списка и недоступен для новых операций. История, реестры и проводки сохранятся.</DialogDescription></DialogHeader>{error && <Alert variant="destructive"><AlertDescription>{error}</AlertDescription></Alert>}<DialogFooter><Button type="button" variant="secondary" onClick={() => setOpen(false)} disabled={busy}>Отмена</Button><Button type="button" variant="destructive-secondary" onClick={remove} disabled={busy}>{busy ? 'Удаление…' : 'Удалить'}</Button></DialogFooter></DialogContent></Dialog>
  </>;
}

export function Merchants({ role, route = 'merchants', onNavigate }) {
  const [items, setItems] = useState([]), [error, setError] = useState(''), [loading, setLoading] = useState(true), [busy, setBusy] = useState(false), [showInactive, setShowInactive] = useState(false);
  const [code, setCode] = useState(''), [name, setName] = useState('');
  const canEdit = role === 'chief';
  const id = route !== 'merchants' && route !== 'merchants/new' ? route.slice('merchants/'.length) : null;
  const selected = items.find(item => item.id === id);
  const shown = showInactive ? items : items.filter(item => item.active);
  const pagination = usePagination(shown, String(showInactive));
  async function load() { setLoading(true); setError(''); try { const data = await get('/api/catalog'); setItems(data.merchants || []); } catch (e) { setError(userError(e)); } finally { setLoading(false); } }
  useEffect(() => { load(); }, []);
  useEffect(() => { if (route === 'merchants/new') { setCode(''); setName(''); } else if (selected) { setCode(selected.code); setName(selected.name); } }, [route, selected?.id, selected?.version]);
  async function submit(event) {
    event.preventDefault(); setBusy(true); setError('');
    try {
      if (route === 'merchants/new') { const result = await post('/api/catalog/create', { kind: 'merchant', code: code.trim(), name: name.trim() }); await load(); onNavigate(`merchants/${result.id}`); }
      else { await post('/api/merchant/update', { id, version: String(selected.version), code: code.trim(), name: name.trim() }); await load(); }
    } catch (e) { setError(userError(e)); } finally { setBusy(false); }
  }
  if (route === 'merchants/new') return <PageState title="Новый мерчант" subtitle="Добавьте мерчанта в справочник."><Card className="max-w-2xl rounded-2xl"><CardHeader><CardTitle>Данные мерчанта</CardTitle></CardHeader><CardContent><form className="grid gap-5" onSubmit={submit}><label className="grid gap-2 text-sm font-medium">Код<Input value={code} onChange={event => setCode(event.target.value)} maxLength={24} required /></label><label className="grid gap-2 text-sm font-medium">Название<Input value={name} onChange={event => setName(event.target.value)} maxLength={200} required /></label>{error && <Alert variant="destructive"><AlertDescription>{error}</AlertDescription></Alert>}<Button variant="primary" disabled={busy}>{busy ? 'Сохранение…' : 'Создать мерчанта'}</Button></form></CardContent></Card></PageState>;
  if (id) return <PageState title={selected?.name || 'Мерчант'} subtitle="Данные мерчанта и действующая процентная ставка.">{loading ? <p>Загрузка…</p> : !selected ? <Alert variant="destructive"><AlertDescription>{error || 'Мерчант не найден.'}</AlertDescription></Alert> : <><Card className="rounded-2xl"><CardHeader><CardTitle>Данные мерчанта</CardTitle><CardDescription>{selected.active ? 'Активен' : 'Отключён'} · текущая ставка {rate(selected)}</CardDescription></CardHeader><CardContent>{canEdit ? <form className="grid gap-5" onSubmit={submit}><div className="grid gap-5 md:grid-cols-2"><label className="grid min-w-0 gap-2 text-sm font-medium">Код<Input value={code} onChange={event => setCode(event.target.value)} maxLength={24} required /></label><label className="grid min-w-0 gap-2 text-sm font-medium">Название<Input value={name} onChange={event => setName(event.target.value)} maxLength={200} required /></label></div>{error && <Alert variant="destructive"><AlertDescription>{error}</AlertDescription></Alert>}<div className="flex flex-wrap gap-3"><Button variant="primary" disabled={busy}>{busy ? 'Сохранение…' : selected.active ? 'Сохранить' : 'Восстановить и сохранить'}</Button>{selected.active && <MerchantMenu merchant={selected} role={role} onNavigate={onNavigate} onChanged={() => { load(); onNavigate('merchants'); }} />}</div></form> : <p className="text-sm">Код: {selected.code}</p>}</CardContent></Card>{canEdit && selected.active && <TariffManager role={role} merchantID={selected.id} rateBP={selected.rate_bp >= 0 ? selected.rate_bp : null} onConfirmed={load} />}</>}</PageState>;
  return <PageState title="Мерчанты" subtitle="Справочник мерчантов и действующие ставки."><div className="mb-5 flex flex-wrap items-center justify-between gap-3"><label className="flex items-center gap-2 text-sm"><input type="checkbox" checked={showInactive} onChange={event => setShowInactive(event.target.checked)} />Показать отключённых</label></div>{error && <Alert variant="destructive" className="mb-4"><AlertDescription>{error}</AlertDescription></Alert>}<div className="section-table-wrap -mx-5 border-y border-border lg:-mx-6">{loading ? <p className="p-6 text-sm text-muted-foreground">Загрузка…</p> : shown.length ? <Table className="section-table section-table--merchants"><TableHeader><TableRow><TableHead>Код</TableHead><TableHead>Название</TableHead><TableHead>Ставка</TableHead><TableHead>Статус</TableHead><TableHead>Действия</TableHead></TableRow></TableHeader><TableBody>{pagination.rows.map(item => <TableRow key={item.id}><TableCell className="font-mono">{item.code}</TableCell><TableCell><button type="button" className="text-left text-primary underline-offset-2 hover:underline" onClick={() => onNavigate(`merchants/${item.id}`)}>{item.name}</button></TableCell><TableCell>{rate(item)}</TableCell><TableCell>{item.active ? 'Активен' : 'Отключён'}</TableCell><TableCell><MerchantMenu merchant={item} role={role} onNavigate={onNavigate} onChanged={load} /></TableCell></TableRow>)}</TableBody></Table> : <p className="p-6 text-sm text-muted-foreground">Мерчантов пока нет.</p>}{shown.length > 0 && <TablePagination page={pagination.page} totalPages={pagination.totalPages} total={shown.length} onPageChange={pagination.setPage} />}</div></PageState>;
}
