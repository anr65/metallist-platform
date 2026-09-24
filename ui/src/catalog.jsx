import React, { useEffect, useState } from 'react';
import { ArrowUpRight, MoreHorizontal } from 'lucide-react';
import { Alert, AlertDescription } from '@/components/ui/alert';
import { Button } from '@/components/ui/button';
import { Card, CardContent } from '@/components/ui/card';
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog';
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from '@/components/ui/dropdown-menu';
import { Input } from '@/components/ui/input';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select';
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table';
import { BankSelect, CardPANCorrection } from './forms';
import { get, PageState } from './pages';
import { request, userError } from './api-errors';
import { TablePagination, usePagination } from './pagination';

const sections = {
  cards: { title: 'Карты', data: 'cards', kind: 'card', columns: [['Карта', x => x.mask], ['Банк', x => x.name], ['Владелец', x => x.owner_label], ['Статус', x => ({ active: 'Активна', blocked: 'Заблокирована', retired: 'Выведена' })[x.status] || x.status]] },
  contacts: { title: 'ФИО и телефоны', data: 'payment_contacts', kind: 'payment_contact', columns: [['ФИО', x => x.full_name], ['Телефон', x => x.phone], ['Статус', x => x.active ? 'Активен' : 'Отключён']] },
  banks: { title: 'Банки', data: 'banks', kind: 'bank', columns: [['Код', x => x.code], ['Банк', x => x.name], ['Источник', x => x.source === 'nspk_sbp' ? 'НСПК' : 'Вручную'], ['Доступен', x => x.selectable ? 'Да' : 'Нет']] },
  custodians: { title: 'Ответственные', data: 'custodians', kind: 'custodian', columns: [['Имя', x => x.name], ['Функция', x => ({ chief: 'Главный администратор', collector: 'Сборщик', operator: 'Операционист' })[x.kind] || x.kind], ['Статус', x => x.active ? 'Активен' : 'Отключён']] },
};
const recordWord = count => count % 10 === 1 && count % 100 !== 11 ? 'запись' : count % 10 >= 2 && count % 10 <= 4 && (count % 100 < 12 || count % 100 > 14) ? 'записи' : 'записей';
const canCreate = (role, section) => role === 'chief' || role === 'operator' && ['cards', 'contacts'].includes(section);
const canEdit = (role, section, row) => canCreate(role, section) && (section !== 'banks' || row.source === 'manual') && (section !== 'cards' || row.status === 'active') && (section !== 'contacts' && section !== 'custodians' || row.active);
async function post(path, body) { return request(() => fetch(path, { method: 'POST', credentials: 'same-origin', headers: { 'Content-Type': 'application/json', 'X-CSRF': '1' }, body: JSON.stringify(body) })); }

function AccessPage({ catalog, onChanged }) {
  const [dialog, setDialog] = useState(null);
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);
  const [password, setPassword] = useState('');
  const [custodianID, setCustodianID] = useState('');
  const users = catalog?.users || [];
  const pagination = usePagination(users);
  useEffect(() => { const open = () => { setPassword(''); setError(''); setDialog({ mode: 'create' }); }; window.addEventListener('catalog-create', open); return () => window.removeEventListener('catalog-create', open); }, []);
  async function submit(event) {
    event.preventDefault(); setBusy(true); setError('');
    const data = Object.fromEntries(new FormData(event.currentTarget));
    try {
      if (dialog.mode === 'create') {
        const created = await post('/api/catalog/create', { ...data, kind: 'user' });
        setPassword(created.temporary_password || '');
      } else {
        await post('/api/user/telegram', { user_id: dialog.user.id, custodian_id: custodianID, telegram_id: data.telegram_id });
      }
      setDialog(null); onChanged();
    } catch (err) { setError(userError(err)); }
    finally { setBusy(false); }
  }
  const custodians = (catalog?.custodians || []).filter(x => x.active && x.kind === (dialog?.user?.role === 'chief' ? 'chief' : 'collector'));
  return <PageState title="Пользователи и Telegram" subtitle="Учётные записи и привязки к хранителям наличных." headingClassName="px-5 lg:px-6">
    {password && <Alert className="mx-5 mb-4 lg:mx-6"><AlertDescription>Одноразовый пароль: <strong className="font-mono">{password}</strong>. Передайте его пользователю защищённым способом.</AlertDescription></Alert>}
    <div className="section-table-wrap border-y border-border">{users.length ? <Table className="section-table section-table--catalog"><TableHeader><TableRow><TableHead>Логин</TableHead><TableHead>Имя</TableHead><TableHead>Роль</TableHead><TableHead>Telegram</TableHead><TableHead>Действия</TableHead></TableRow></TableHeader><TableBody>{pagination.rows.map(user => <TableRow key={user.id}><TableCell>{user.login}</TableCell><TableCell>{user.name}</TableCell><TableCell>{({ chief: 'Главный администратор', collector: 'Сборщик', operator: 'Операционист', accountant: 'Бухгалтер', auditor: 'Аудитор' })[user.role] || user.role}</TableCell><TableCell>{user.telegram_id || '—'}</TableCell><TableCell>{['collector', 'chief'].includes(user.role) ? <DropdownMenu><DropdownMenuTrigger asChild><Button type="button" variant="ghost" size="icon" aria-label={`Действия: ${user.name}`}><MoreHorizontal className="size-5" /></Button></DropdownMenuTrigger><DropdownMenuContent align="end"><DropdownMenuItem onSelect={() => { setCustodianID(user.custodian_id || ''); setError(''); setDialog({ mode: 'telegram', user }); }}>Изменить привязку Telegram</DropdownMenuItem></DropdownMenuContent></DropdownMenu> : '—'}</TableCell></TableRow>)}</TableBody></Table> : <p className="p-6 text-sm text-muted-foreground">{catalog ? 'Пользователей пока нет.' : 'Загрузка…'}</p>}{users.length > 0 && <TablePagination page={pagination.page} totalPages={pagination.totalPages} total={users.length} onPageChange={pagination.setPage} />}</div>
    <Dialog open={Boolean(dialog)} onOpenChange={open => { if (!open && !busy) setDialog(null); }}><DialogContent><DialogHeader><DialogTitle>{dialog?.mode === 'create' ? 'Новый пользователь' : 'Привязка Telegram'}</DialogTitle><DialogDescription>{dialog?.mode === 'create' ? 'Одноразовый пароль будет показан после создания.' : `Пользователь: ${dialog?.user?.name || ''}`}</DialogDescription></DialogHeader>{dialog && <form onSubmit={submit} className="grid gap-4">{dialog.mode === 'create' ? <><label className="grid gap-2 text-sm font-medium">Логин<Input name="login" autoComplete="off" required /></label><label className="grid gap-2 text-sm font-medium">Имя<Input name="name" required /></label><label className="grid gap-2 text-sm font-medium">Роль<Select name="role" required><SelectTrigger><SelectValue placeholder="Выберите роль" /></SelectTrigger><SelectContent><SelectItem value="collector">Сборщик</SelectItem><SelectItem value="operator">Операционист</SelectItem><SelectItem value="accountant">Бухгалтер</SelectItem><SelectItem value="auditor">Аудитор</SelectItem></SelectContent></Select></label></> : <><label className="grid gap-2 text-sm font-medium">Числовой Telegram ID<Input name="telegram_id" inputMode="numeric" defaultValue={dialog.user.telegram_id || ''} required /></label><label className="grid gap-2 text-sm font-medium">Ответственный за наличные<Select value={custodianID} onValueChange={setCustodianID}><SelectTrigger><SelectValue placeholder="Выберите ответственного" /></SelectTrigger><SelectContent>{custodians.map(x => <SelectItem key={x.id} value={x.id}>{x.name}</SelectItem>)}</SelectContent></Select></label></>}{error && <Alert variant="destructive"><AlertDescription>{error}</AlertDescription></Alert>}<DialogFooter><Button type="button" variant="secondary" onClick={() => setDialog(null)} disabled={busy}>Отмена</Button><Button type="submit" variant="primary" disabled={busy || dialog.mode === 'telegram' && !custodianID}>{busy ? 'Сохранение…' : 'Сохранить'}</Button></DialogFooter></form>}</DialogContent></Dialog>
  </PageState>;
}

function CatalogFields({ section, record, catalog, bankID, setBankID, custodianKind, setCustodianKind }) {
  if (section === 'cards') return <>
    {!record && <><label className="grid gap-2 text-sm font-medium">Банк<BankSelect banks={(catalog.banks || []).filter(x => x.selectable)} value={bankID} onValueChange={setBankID} /></label><label className="grid gap-2 text-sm font-medium">Полный номер карты<Input name="pan" type="password" inputMode="numeric" autoComplete="off" pattern="[0-9]{16,19}" required /></label></>}
    <label className="grid gap-2 text-sm font-medium">ФИО владельца<Input name="owner_label" defaultValue={record?.owner_label || ''} maxLength={200} required /></label>
  </>;
  if (section === 'contacts') return <><label className="grid gap-2 text-sm font-medium">ФИО<Input name="full_name" defaultValue={record?.full_name || ''} maxLength={200} required /></label><label className="grid gap-2 text-sm font-medium">Телефон<Input name="phone" type="tel" defaultValue={record?.phone || ''} required /></label></>;
  if (section === 'banks') return <><label className="grid gap-2 text-sm font-medium">Код<Input name="code" defaultValue={record?.code || ''} maxLength={24} required /></label><label className="grid gap-2 text-sm font-medium">Название банка<Input name="name" defaultValue={record?.name || ''} maxLength={200} required /></label></>;
  return <><label className="grid gap-2 text-sm font-medium">Имя или обозначение<Input name="name" defaultValue={record?.name || ''} maxLength={200} required /></label>{!record && <label className="grid gap-2 text-sm font-medium">Функция<Select value={custodianKind} onValueChange={setCustodianKind}><SelectTrigger><SelectValue placeholder="Выберите функцию" /></SelectTrigger><SelectContent><SelectItem value="collector">Сборщик</SelectItem><SelectItem value="operator">Операционист</SelectItem></SelectContent></Select></label>}</>;
}

export function CatalogPage({ role, route = 'catalog', onNavigate }) {
  const section = route.startsWith('catalog/') ? route.slice('catalog/'.length) : '';
  const config = sections[section];
  const [catalog, setCatalog] = useState(null);
  const [error, setError] = useState('');
  const [dialog, setDialog] = useState(null);
  const [busy, setBusy] = useState(false);
  const [bankID, setBankID] = useState('');
  const [custodianKind, setCustodianKind] = useState('');
  const [revision, setRevision] = useState(0);
  useEffect(() => { let live = true; setError(''); get('/api/catalog').then(data => { if (live) setCatalog(data); }).catch(err => { if (live) setError(userError(err)); }); return () => { live = false; }; }, [revision, section]);
  const rows = config ? catalog?.[config.data] || [] : [];
  const pagination = usePagination(rows, section);
  function openCreate() { setBankID(''); setCustodianKind(''); setError(''); setDialog({ mode: 'create' }); }
  useEffect(() => { if (!config || !canCreate(role, section)) return; const open = () => openCreate(); window.addEventListener('catalog-create', open); return () => window.removeEventListener('catalog-create', open); }, [role, section]);
  function openEdit(record) { setError(''); setDialog({ mode: 'edit', record }); }
  async function submit(event) {
    event.preventDefault();
    const form = event.currentTarget;
    const fields = Object.fromEntries(new FormData(form));
    if (section === 'cards' && !dialog.record && !bankID) { setError('Выберите банк из справочника НСПК.'); return; }
    if (section === 'custodians' && !dialog.record && !custodianKind) { setError('Выберите функцию.'); return; }
    setBusy(true); setError('');
    try {
      const editing = dialog.mode === 'edit';
      await post(editing ? '/api/catalog/update' : '/api/catalog/create', { ...fields, kind: config.kind, ...(editing ? { id: dialog.record.id } : section === 'cards' ? { bank_id: bankID } : section === 'custodians' ? { custodian_kind: custodianKind } : {}) });
      setDialog(null); setRevision(value => value + 1);
    } catch (err) { setError(userError(err)); }
    finally { if (form.isConnected) { const pan = form.querySelector('[name="pan"]'); if (pan) pan.value = ''; } setBusy(false); }
  }
  if (!section) return <PageState title="Справочники" subtitle="Выберите раздел для просмотра и управления записями.">
    {error && <Alert variant="destructive" className="mb-4"><AlertDescription>{error}</AlertDescription></Alert>}
    <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">{Object.entries(sections).filter(([key]) => role !== 'collector' || key === 'cards').filter(([key]) => key !== 'contacts' || ['chief', 'operator'].includes(role)).map(([key, item]) => <button key={key} type="button" onClick={() => onNavigate(`catalog/${key}`)} className="flex min-h-28 items-center justify-between gap-4 rounded-xl border border-border bg-card px-5 py-4 text-left transition-colors hover:bg-accent focus-visible:outline-2 focus-visible:outline-primary"><span><strong className="block text-base">{item.title}</strong><span className="mt-2 block text-sm text-muted-foreground">{catalog?.[item.data]?.length ?? '…'} {recordWord(catalog?.[item.data]?.length || 0)}</span></span><ArrowUpRight className="size-5 shrink-0 text-primary" /></button>)}{role === 'sysadmin' && <button type="button" onClick={() => onNavigate('catalog/access')} className="flex min-h-28 items-center justify-between rounded-xl border border-border bg-card px-5 py-4 text-left hover:bg-accent"><strong>Пользователи и Telegram</strong><ArrowUpRight className="size-5 text-primary" /></button>}</div>
  </PageState>;
  if (section === 'access' && role === 'sysadmin') return <AccessPage catalog={catalog} onChanged={() => setRevision(value => value + 1)} />;
  if (!config || section === 'contacts' && !['chief', 'operator'].includes(role)) return <PageState title="Справочник недоступен" subtitle="У вас нет доступа к этому разделу." />;
  return <PageState title={config.title} subtitle={`${rows.length} ${recordWord(rows.length)} в справочнике.`} headingClassName="px-5 lg:px-6">
    {error && !dialog && <Alert variant="destructive" className="mx-5 mb-4 lg:mx-6"><AlertDescription>{error}</AlertDescription></Alert>}
    <div className="section-table-wrap border-y border-border">{!catalog && !error ? <p className="p-6 text-sm text-muted-foreground">Загрузка…</p> : rows.length ? <Table className="section-table section-table--catalog"><TableHeader><TableRow>{config.columns.map(([label]) => <TableHead key={label}>{label}</TableHead>)}<TableHead>Действия</TableHead></TableRow></TableHeader><TableBody>{pagination.rows.map(row => <TableRow key={row.id}>{config.columns.map(([label, render]) => <TableCell key={label}>{render(row)}</TableCell>)}<TableCell>{canEdit(role, section, row) ? <DropdownMenu><DropdownMenuTrigger asChild><Button type="button" variant="ghost" size="icon" aria-label={`Действия: ${row.mask || row.full_name || row.name}`}><MoreHorizontal className="size-5" /></Button></DropdownMenuTrigger><DropdownMenuContent align="end"><DropdownMenuItem onSelect={() => openEdit(row)}>Редактировать</DropdownMenuItem>{section === 'cards' && (role === 'chief' ? row.pan_saved : (catalog?.request_cards || []).some(card => card.id === row.id)) && <DropdownMenuItem onSelect={() => setDialog({ mode: 'correction' })}>Исправить номер карты</DropdownMenuItem>}</DropdownMenuContent></DropdownMenu> : <span className="text-muted-foreground">—</span>}</TableCell></TableRow>)}</TableBody></Table> : <p className="p-6 text-sm text-muted-foreground">Записей пока нет.</p>}{rows.length > 0 && <TablePagination page={pagination.page} totalPages={pagination.totalPages} total={rows.length} onPageChange={pagination.setPage} />}</div>
    <Dialog open={Boolean(dialog)} onOpenChange={open => { if (!open && !busy) { if (dialog?.mode === 'correction') setRevision(value => value + 1); setDialog(null); setError(''); } }}><DialogContent className="max-h-[90dvh] overflow-y-auto"><DialogHeader><DialogTitle>{dialog?.mode === 'correction' ? 'Исправить номер карты' : dialog?.mode === 'edit' ? 'Редактировать запись' : 'Добавить запись'}</DialogTitle><DialogDescription>{dialog?.mode === 'correction' ? 'Исправление доступно только до использования карты.' : config.title}</DialogDescription></DialogHeader>{dialog?.mode === 'correction' ? <CardPANCorrection role={role} /> : dialog && <form key={dialog.record?.id || 'new'} onSubmit={submit} className="grid gap-4"><CatalogFields section={section} record={dialog.record} catalog={catalog} bankID={bankID} setBankID={setBankID} custodianKind={custodianKind} setCustodianKind={setCustodianKind} />{error && <Alert variant="destructive"><AlertDescription>{error}</AlertDescription></Alert>}<DialogFooter><Button type="button" variant="secondary" onClick={() => setDialog(null)} disabled={busy}>Отмена</Button><Button type="submit" variant="primary" disabled={busy}>{busy ? 'Сохранение…' : 'Сохранить'}</Button></DialogFooter></form>}</DialogContent></Dialog>
  </PageState>;
}
