import React, { useEffect, useRef, useState } from 'react';
import { request, userError } from './api-errors';
import { createRoot } from 'react-dom/client';
import { useGSAP } from '@gsap/react';
import gsap from 'gsap';
import { ScrollTrigger } from 'gsap/ScrollTrigger';
import { ArrowUpRight, BookOpen, ChartNoAxesCombined, Check, CreditCard, Files, History, LayoutDashboard, LogOut, Menu, ReceiptText, Settings, ShieldCheck, Sparkles, WalletCards } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { Input } from '@/components/ui/input';
import { Badge } from '@/components/ui/badge';
import { Breadcrumb, BreadcrumbList, BreadcrumbItem, BreadcrumbLink, BreadcrumbPage, BreadcrumbSeparator } from '@/components/ui/breadcrumb';
import { Alert, AlertDescription } from '@/components/ui/alert';
import { Sheet, SheetClose, SheetContent, SheetHeader, SheetTitle, SheetTrigger } from '@/components/ui/sheet';
import { Audit, Catalog, Money, Reports } from './pages';
import { Balances } from './balances';
import { CardPANCorrection, CatalogManager, DraftActivity, ExpenseForm, MoneyForm, PaymentRequests, RegistryUpload, TelegramLinkManager, UserManager } from './forms';
import { allowedPages, navigationByRole, pageForRole, stripQueryFromLocation } from './access';
import './styles.css';

gsap.registerPlugin(useGSAP, ScrollTrigger);
stripQueryFromLocation(window.location, window.history);

const nav = [
  ['overview', 'Обзор', LayoutDashboard], ['requests', 'Карты к оплате', CreditCard], ['registries', 'Реестры оплат', Files], ['expenses', 'Расходы', ReceiptText], ['money', 'Движение денег', WalletCards],
  ['balances', 'Остатки', WalletCards], ['catalog', 'Справочники', BookOpen], ['reports', 'Отчёты', ChartNoAxesCombined], ['audit', 'История действий', History]
];
function syncLocation(role) {
  const page = pageForRole(role, window.location.hash);
  const target = page === 'overview' ? window.location.pathname : `#${page}`;
  if (window.location.hash !== (page === 'overview' ? '' : target)) window.history.replaceState(null, '', target);
  return page;
}
const roleNames = { chief: 'Главный администратор', operator: 'Операционист', collector: 'Сборщик', accountant: 'Бухгалтер', sysadmin: 'Системный администратор', auditor: 'Аудитор' };
const rub = new Intl.NumberFormat('ru-RU', { style: 'currency', currency: 'RUB', minimumFractionDigits: 2 });
async function api(path, body) { return request(() => fetch(path, body ? { method: 'POST', credentials: 'same-origin', headers: { 'Content-Type': 'application/json', 'X-CSRF': '1' }, body: JSON.stringify(body) } : { credentials: 'same-origin', headers: { 'X-CSRF': '1' } })); }

function BrandMark({ compact = false }) {
  return <div className="flex items-center gap-3"><span className={`${compact ? 'size-9' : 'size-11'} brand-mark`} aria-hidden="true">М</span><span className="text-[13px] leading-[1.12] tracking-[-.015em] text-foreground/70">Металлист<br /><strong className="font-semibold text-foreground">Платформа</strong></span></div>;
}

function Login({ onLogin }) {
  const root = useRef(null); const [error, setError] = useState(''); const [busy, setBusy] = useState(false);
  useGSAP(() => { if (matchMedia('(prefers-reduced-motion: reduce)').matches) return; const tl = gsap.timeline({ defaults: { ease: 'power3.out' } }); tl.from('[data-login-word]', { yPercent: 110, opacity: 0, rotate: 2, duration: 1, stagger: .08 }).from('[data-login-copy]', { y: 22, opacity: 0, duration: .7 }, '-=.45').from('[data-login-card]', { x: 40, opacity: 0, duration: .85 }, '-=.65'); }, { scope: root });
  async function submit(event) { event.preventDefault(); const form = new FormData(event.currentTarget); setBusy(true); setError(''); try { await api('/login', Object.fromEntries(form)); await onLogin(); } catch (err) { setError(userError(err)); } finally { setBusy(false); } }
  return <main ref={root} className="login-stage min-h-[100dvh] overflow-hidden px-5 py-5 sm:px-8 sm:py-8 lg:p-10"><div className="ambient-orb ambient-orb--one" /><div className="ambient-orb ambient-orb--two" /><div className="relative z-10 mx-auto grid min-h-[calc(100dvh-5rem)] max-w-[1540px] overflow-hidden rounded-[2rem] border border-white/70 bg-white/48 shadow-[0_40px_100px_rgba(28,36,33,.09)] backdrop-blur-xl lg:grid-cols-[1.2fr_.8fr]">
    <section className="relative flex min-h-[62vh] flex-col justify-between overflow-hidden p-7 sm:p-11 lg:min-h-0 lg:p-16"><BrandMark /><div className="relative z-10 my-16 max-w-6xl"><div className="mb-7 flex items-center gap-3 text-sm text-foreground/55" data-login-copy><span className="h-px w-12 bg-primary/40" />Финансовая операционная система</div><h1 className="login-title max-w-6xl font-semibold tracking-[-.07em]"><span className="block overflow-hidden"><span className="block" data-login-word>Видеть деньги.</span></span><span className="block overflow-hidden"><span className="block" data-login-word>Управлять <span className="type-window">точно.</span></span></span></h1><p className="mt-8 max-w-xl text-base leading-7 text-foreground/58 sm:text-lg" data-login-copy>Реестры, наличные, расходы и обязательства собраны в ясную систему. Каждое денежное действие подтверждается отдельно.</p></div><div className="flex flex-wrap gap-x-8 gap-y-3 text-xs text-foreground/48" data-login-copy><span className="flex items-center gap-2"><ShieldCheck className="size-4 text-primary" />Контроль ролей</span><span className="flex items-center gap-2"><Check className="size-4 text-primary" />Подтверждённые операции</span><span className="flex items-center gap-2"><History className="size-4 text-primary" />Полная история</span></div></section>
    <section className="relative grid place-items-center border-t border-white/70 bg-[#e8eee9]/58 p-5 sm:p-10 lg:border-t-0 lg:border-l"><Card data-login-card className="w-full max-w-[460px] rounded-[1.75rem] border-white/80 bg-white/78 shadow-[0_30px_80px_rgba(40,62,53,.10)] backdrop-blur-2xl"><CardHeader className="px-7 pt-8 sm:px-9 sm:pt-10"><div className="mb-5 grid size-11 place-items-center rounded-2xl bg-primary text-primary-foreground shadow-[0_10px_25px_rgba(49,91,79,.22)]"><Sparkles className="size-5" /></div><CardTitle className="text-3xl tracking-[-.045em]">С возвращением</CardTitle><CardDescription className="max-w-xs leading-6">Войдите, чтобы продолжить работу с операциями Металлиста.</CardDescription></CardHeader><CardContent className="px-7 pb-8 sm:px-9 sm:pb-10"><form className="grid gap-5" onSubmit={submit}><label className="grid gap-2 text-sm font-medium">Логин<Input name="login" autoComplete="username" required placeholder="Ваш логин" /></label><label className="grid gap-2 text-sm font-medium">Пароль<Input name="password" type="password" autoComplete="current-password" required placeholder="••••••••••••" /></label>{error && <Alert variant="destructive"><AlertDescription>{error}</AlertDescription></Alert>}<Button size="lg" type="submit" disabled={busy} className="mt-1 w-full">{busy ? 'Вход…' : <>Войти в платформу<ArrowUpRight /></>}</Button></form></CardContent></Card></section>
  </div></main>;
}

const metricMeta = [
  ['Деньги на картах и в кассах', 'card_cash', 'Сопровождаемые деньги сейчас', 'metric-card--primary'],
  ['К возврату мерчантам', 'merchant_payable', 'Подтверждённый долг по реестрам', 'metric-card--sage'],
  ['Заработанная комиссия', 'commission_revenue', 'Выручка без транзитных сумм', ''],
  ['Прибыль', 'profit', 'Доходы за вычетом расходов', 'metric-card--warm']
];

function MetricCard({ item, report, index }) {
  const [label, key, detail, tone] = item;
  return <Card data-metric className={`metric-card ${tone} ${index === 0 ? 'md:col-span-7' : index === 1 ? 'md:col-span-5' : 'md:col-span-4'}`}><CardContent className="flex h-full flex-col justify-between p-6 lg:p-7"><div className="flex items-start justify-between gap-5"><p className="max-w-[16rem] text-sm leading-5 text-current/60">{label}</p><span className="metric-index">0{index + 1}</span></div><strong className="mt-10 font-mono text-[clamp(1.55rem,3.2vw,3.1rem)] leading-none tracking-[-.07em] tabular-nums">{rub.format(Number(report.summary[key] || 0))}</strong><p className="mt-5 text-xs leading-5 text-current/48">{detail}</p></CardContent></Card>;
}

function Overview({ report, onNavigate }) {
  const root = useRef(null);
  useGSAP(() => { if (matchMedia('(prefers-reduced-motion: reduce)').matches) return; gsap.from('[data-metric]', { y: 42, opacity: 0, scale: .985, duration: .8, stagger: .09, ease: 'power3.out' }); const cards = gsap.utils.toArray('[data-stack-card]'); cards.forEach((card, index) => gsap.to(card, { y: index * -8, scale: 1 - index * .018, scrollTrigger: { trigger: card, start: 'top 78%', end: 'bottom 35%', scrub: .6 } })); }, { scope: root });
  return <div ref={root} className="pb-10"><div className="metric-grid grid grid-flow-dense gap-3 md:grid-cols-12">{metricMeta.map((item, index) => <MetricCard key={item[1]} item={item} report={report} index={index} />)}<Card data-metric className="metric-card quick-cell md:col-span-4"><CardContent className="flex h-full flex-col justify-between p-6 lg:p-7"><div><p className="text-sm text-foreground/55">Быстрый маршрут</p><h2 className="mt-3 text-2xl font-semibold tracking-[-.045em]">От события<br />к учёту.</h2></div><Button className="mt-8 w-fit" onClick={() => onNavigate('requests')}>Начать работу<ArrowUpRight /></Button></CardContent></Card></div>
    <section className="mt-6"><div className="action-accordion grid overflow-hidden rounded-[1.75rem] border border-border/70 lg:grid-cols-3">{[
      ['Карты к оплате', 'Создать запрос и подготовить файл для мерчанта.', 'requests', '01'],
      ['Новый расход', 'Зафиксировать сумму, тип и дату операции.', 'expenses', '02'],
      ['Движение денег', 'Снятие, передача, возврат или расхождение.', 'money', '03']
    ].map(([title, copy, target, number]) => <button data-stack-card className="action-panel group" key={target} onClick={() => onNavigate(target)}><span className="font-mono text-xs text-foreground/35">{number}</span><span className="mt-16 block text-left text-xl font-semibold tracking-[-.035em]">{title}</span><span className="mt-2 block max-w-[17rem] text-left text-sm leading-6 text-muted-foreground">{copy}</span><span className="mt-8 grid size-10 place-items-center rounded-full border border-border transition-all group-hover:border-primary group-hover:bg-primary group-hover:text-white"><ArrowUpRight className="size-4" /></span></button>)}</div></section>
  </div>;
}

function AccountPage() { const [error, setError] = useState(''), [busy, setBusy] = useState(false), [done, setDone] = useState(false); async function submit(event) { event.preventDefault(); const form = new FormData(event.currentTarget); setBusy(true); setError(''); try { await api('/api/password', Object.fromEntries(form)); setDone(true); } catch (e) { setError(userError(e)); } finally { setBusy(false); } } return <><div className="page-heading"><h1>Настройки входа</h1><p>После смены пароля все прежние сеансы будут завершены.</p></div><Card className="max-w-xl"><CardHeader><CardTitle>Сменить пароль</CardTitle><CardDescription>Новый пароль должен состоять минимум из 16 символов.</CardDescription></CardHeader><CardContent>{done ? <Alert><AlertDescription>Пароль изменён. Войдите заново, чтобы продолжить.</AlertDescription></Alert> : <form className="grid gap-5" onSubmit={submit}><label className="grid gap-2 text-sm font-medium">Текущий пароль<Input name="current" type="password" autoComplete="current-password" required /></label><label className="grid gap-2 text-sm font-medium">Новый пароль<Input name="new" type="password" autoComplete="new-password" minLength="16" required /></label>{error && <Alert variant="destructive"><AlertDescription>{error}</AlertDescription></Alert>}<Button type="submit" disabled={busy}>{busy ? 'Сохранение…' : 'Сохранить новый пароль'}</Button></form>}</CardContent></Card></>; }

function App() {
  const [user, setUser] = useState(null); const [report, setReport] = useState(null); const [page, setPage] = useState(null); const [error, setError] = useState(''); const content = useRef(null);
  async function load() {
    let me;
    try { me = await api('/api/me'); } catch { setUser(null); return; }
    setUser(me);
    setPage(syncLocation(me.Role));
    setReport(null);
    if (navigationByRole[me.Role]?.includes('overview')) {
      try { setReport(await api('/api/report')); } catch (err) { setError(userError(err)); }
    }
  }
  useEffect(() => { load(); }, []);
  useEffect(() => { if (!user) return; const syncPage = () => setPage(syncLocation(user.Role)); window.addEventListener('popstate', syncPage); window.addEventListener('hashchange', syncPage); return () => { window.removeEventListener('popstate', syncPage); window.removeEventListener('hashchange', syncPage); }; }, [user]);
  useGSAP(() => { if (!user || matchMedia('(prefers-reduced-motion: reduce)').matches) return; gsap.fromTo(content.current, { opacity: 0, y: 10 }, { opacity: 1, y: 0, duration: .45, ease: 'power2.out' }); }, { dependencies: [page, user], revertOnUpdate: true });
  if (!user) return <Login onLogin={load} />;
  const go = id => { if (pageForRole(user.Role, `#${id}`) !== id || (page !== id && window.__requestLeaveGuard && !window.__requestLeaveGuard())) return; setPage(id); setError(''); const nextURL = id === 'overview' ? window.location.pathname : `#${id}`; window.history.pushState(null, '', nextURL); window.scrollTo({ top: 0, behavior: 'smooth' }); };
  const NavButton = ({ id, label, Icon, mobile }) => { const button = <Button variant="ghost" data-active={page === id || (['requests', 'registries'].includes(id) && page?.startsWith(`${id}/`))} className="nav-button justify-start" onClick={() => go(id)}><Icon className="size-4" />{label}</Button>; return mobile ? <SheetClose asChild>{button}</SheetClose> : button; };
  const visibleNav = nav.filter(([id]) => allowedPages(user.Role).includes(id));
  const Nav = ({ mobile = false }) => <nav className="grid gap-1">{visibleNav.map(([id, label, Icon]) => <NavButton key={id} id={id} label={label} Icon={Icon} mobile={mobile} />)}{mobile && <NavButton id="account" label="Настройки входа" Icon={Settings} mobile />}</nav>;
  async function logout() { try { await api('/logout', {}); location.reload(); } catch (err) { setError(userError(err)); } }
  const section = ['requests', 'registries'].find(id => page?.startsWith(`${id}/`)) || page;
  const sectionLabel = nav.find(([id]) => id === section)?.[1] || 'Настройки входа';
  const currentLabel = page === 'requests/new' ? 'Сформировать реестр' : page?.startsWith('requests/') ? 'Запрос' : page === 'registries/new' ? 'Новый реестр' : page?.startsWith('registries/') ? 'Реестр' : sectionLabel;
  const parentPage = ['requests', 'registries'].find(id => page?.startsWith(`${id}/`)) || (allowedPages(user.Role).includes('overview') ? 'overview' : null);
  const parentLabel = parentPage === 'requests' ? 'Карты к оплате' : parentPage === 'registries' ? 'Реестры оплат' : 'Обзор';
  const pages = { requests: <PaymentRequests role={user.Role} route={page} onNavigate={go} />, registries: <RegistryUpload role={user.Role} route={page} onNavigate={go} />, expenses: <><ExpenseForm /><DraftActivity role={user.Role} /></>, money: <><MoneyForm role={user.Role} /><Money /><DraftActivity role={user.Role} kind="money" title="Черновики и проведённые операции" /></>, balances: <Balances />, catalog: <><Catalog /><CatalogManager role={user.Role} /><CardPANCorrection role={user.Role} /><UserManager role={user.Role} /><TelegramLinkManager role={user.Role} /></>, reports: <Reports role={user.Role} />, audit: <Audit />, account: <AccountPage /> };
  return <div className="app-shell min-h-[100dvh] lg:grid lg:grid-cols-[268px_minmax(0,1fr)]"><aside className="sidebar hidden lg:flex lg:flex-col"><BrandMark compact /><div className="my-7 h-px bg-border/60" /><p className="mb-3 px-3 text-[10px] font-semibold tracking-[.18em] text-muted-foreground uppercase">Рабочее пространство</p><Nav /><div className="mt-auto rounded-2xl border border-white/60 bg-white/45 p-3 backdrop-blur-xl"><div className="mb-2 flex items-center gap-3 px-2 py-2"><span className="grid size-9 place-items-center rounded-full bg-primary/10 text-xs font-semibold text-primary">{user.Name?.slice(0, 1) || 'М'}</span><div className="min-w-0"><p className="truncate text-sm font-semibold">{user.Name}</p><p className="truncate text-[11px] text-muted-foreground">{roleNames[user.Role] || 'Пользователь'}</p></div></div><Button variant="ghost" className="nav-button w-full justify-start" onClick={() => go('account')}><Settings className="size-4" />Настройки</Button><Button variant="ghost" className="nav-button w-full justify-start" onClick={logout}><LogOut className="size-4" />Выйти</Button></div></aside><main className="min-w-0"><header className="app-header flex h-[72px] items-center justify-between px-5 lg:px-10"><Sheet><SheetTrigger asChild><Button variant="outline" size="icon" className="lg:hidden"><Menu /></Button></SheetTrigger><SheetContent side="left" className="w-72 bg-[#f3f5f2]/95 backdrop-blur-xl"><SheetHeader><SheetTitle><BrandMark compact /></SheetTitle></SheetHeader><div className="mt-8"><Nav mobile /></div></SheetContent></Sheet><Breadcrumb className="min-w-0 flex-1"><BreadcrumbList className="flex-nowrap"><BreadcrumbItem className="min-w-0">{parentPage && page !== parentPage ? <BreadcrumbLink className="max-w-40 truncate" onClick={() => go(parentPage)}>{parentLabel}</BreadcrumbLink> : <BreadcrumbPage>{currentLabel}</BreadcrumbPage>}</BreadcrumbItem>{parentPage && page !== parentPage && <><BreadcrumbSeparator /><BreadcrumbItem className="min-w-0"><BreadcrumbPage>{currentLabel}</BreadcrumbPage></BreadcrumbItem></>}</BreadcrumbList></Breadcrumb><Badge variant="secondary" className="ml-auto hidden sm:inline-flex">{roleNames[user.Role] || 'Пользователь'}</Badge></header><div ref={content} className={`mx-auto w-full py-7 lg:py-10 ${['requests', 'registries'].includes(page) ? 'max-w-none px-5 lg:px-6' : 'max-w-[1520px] px-5 lg:px-10'}`}>{error && <Alert variant="destructive" className="mb-5"><AlertDescription>{error}</AlertDescription></Alert>}{page?.startsWith('requests/') ? <PaymentRequests role={user.Role} route={page} onNavigate={go} /> : page?.startsWith('registries/') ? <RegistryUpload role={user.Role} route={page} onNavigate={go} /> : page === 'overview' ? report ? <Overview report={report} onNavigate={go} /> : !error && <p>Загрузка обзора…</p> : pages[page] || <AccountPage />}</div></main></div>;
}

createRoot(document.getElementById('root')).render(<App />);
