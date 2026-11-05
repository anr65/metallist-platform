import React, { createContext, useCallback, useContext, useEffect, useRef, useState } from 'react';
import { Button } from '@/components/ui/button';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select';
import { get } from './pages';
import { balanceLines } from './operation-balance.js';

const BalanceContext = createContext(null);
export const useOperationBalances = () => useContext(BalanceContext);

export function OperationBalancesProvider({ role, children }) {
  const allowed = ['chief', 'sysadmin'].includes(role);
  const [state, setState] = useState({ catalog: null, data: null, loading: true, error: '', catalogError: '' });
  const requestID = useRef(0);
  const refresh = useCallback(async () => {
    const current = ++requestID.current;
    setState(previous => ({ ...previous, loading: true, error: '', catalogError: '' }));
    const [catalog, balances] = await Promise.allSettled([get('/api/catalog'), allowed ? get('/api/balances') : Promise.resolve(null)]);
    if (current !== requestID.current) return;
    setState(previous => ({
      catalog: catalog.status === 'fulfilled' ? catalog.value : previous.catalog,
      data: balances.status === 'fulfilled' ? balances.value : null,
      loading: false,
      catalogError: catalog.status === 'rejected' ? 'Не удалось загрузить справочники. Обновите страницу.' : '',
      error: balances.status === 'rejected' || catalog.status === 'rejected' ? 'Не удалось обновить балансы. Повторите загрузку.' : '',
    }));
  }, [allowed]);
  useEffect(() => { refresh(); return () => { requestID.current++; }; }, [refresh]);
  return <BalanceContext.Provider value={{ ...state, allowed, refresh }}>{children}</BalanceContext.Provider>;
}

export function BalanceHint({ type, id }) {
  const { allowed, data, catalog, loading, error } = useOperationBalances();
  if (!allowed || !id) return null;
  const lines = balanceLines(type, id, data, catalog);
  return <span className="grid min-w-0 gap-1 text-xs font-normal" aria-live="polite">
    {loading ? <span className="text-muted-foreground">Загрузка баланса…</span> : error || !lines.length ? <span className="text-muted-foreground">Баланс недоступен</span> : lines.map(([label, value]) => <span key={label} className="flex flex-wrap items-baseline justify-between gap-x-3 gap-y-1"><span className="text-muted-foreground">{label}</span><span className="font-mono font-medium tabular-nums text-foreground">{value}</span></span>)}
  </span>;
}

export function BalanceSelect({ type, name, items, placeholder, defaultValue, label = item => item.mask ? `${item.mask} · ${item.name}` : item.name }) {
  const [value, setValue] = useState(defaultValue || '');
  const { allowed, data, catalog, loading, error } = useOperationBalances();
  return <>
    <Select searchSource={type === 'card' ? 'catalog/cards' : type === 'merchant' ? 'catalog/merchants' : 'catalog/custodians'} name={name} defaultValue={defaultValue} value={value} onValueChange={setValue} required>
      <SelectTrigger className="min-w-0"><SelectValue placeholder={placeholder}>{value ? label(items.find(item => item.id === value) || {}) : undefined}</SelectValue></SelectTrigger>
      <SelectContent position="popper" className="max-w-[calc(100vw-2rem)]">{items.map(item => {
        const lines = allowed && !loading && !error ? balanceLines(type, item.id, data, catalog) : [];
        return <SelectItem key={item.id} value={item.id} textValue={label(item)}><span className="grid min-w-0 gap-1 py-1"><span className="break-words whitespace-normal">{label(item)}</span>{lines.map(([title, amount]) => <span key={title} className="text-xs font-normal text-muted-foreground">{title}: <span className="font-mono tabular-nums">{amount}</span></span>)}</span></SelectItem>;
      })}</SelectContent>
    </Select>
    <BalanceHint type={type} id={value} />
  </>;
}

export function BalanceRefresh() {
  const { allowed, loading, error, data, refresh } = useOperationBalances();
  if (!allowed) return null;
  return <div className="mb-5 flex flex-wrap items-center justify-between gap-2 border-y border-border py-3 text-xs text-muted-foreground">
    <span role="status">{loading ? 'Загрузка балансов…' : error || (data?.as_of ? `Учётные остатки на ${new Date(data.as_of).toLocaleString('ru-RU', { timeZone: 'Europe/Moscow' })}` : 'Балансы недоступны')}{!loading && !error && <span className="mt-1 block">Текущие суммы, без вычета резервов. Дата операции их не меняет.</span>}</span>
    <Button type="button" variant="secondary" disabled={loading} onClick={refresh}>Обновить балансы</Button>
  </div>;
}
