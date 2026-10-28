import React, { useState } from 'react';
import { Button } from '@/components/ui/button';
import { Card, CardContent } from '@/components/ui/card';
import { TablePagination, usePagination } from './pagination';
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table';
import { cents, money, compactMoney, ratio, sumMoney } from './overview-metrics';

function Amount({ value }) {
  return <span className="overview-amount" title={money(value)}><span className="overview-exact">{money(value)}</span><span className="overview-compact" aria-hidden="true">{compactMoney(value)}</span><span className="sr-only overview-mobile-exact">{money(value)}</span></span>;
}
const day = value => value ? value.split('-').reverse().join('.') : 'Нет проводок';
const monthLabel = value => new Intl.DateTimeFormat('ru-RU', { month: 'long', year: 'numeric', timeZone: 'UTC' }).format(new Date(`${value}-01T00:00:00Z`));
function Lines({ items }) {
  return <dl className="overview-lines">{items.map(([label, value, hint]) => <div key={label}><dt>{label}{hint && <small>{hint}</small>}</dt><dd><Amount value={value} /></dd></div>)}</dl>;
}
export function Overview({ report }) {
  const [exact, setExact] = useState(false);
  const t = report.totals, a = report.activity, p = report.pending;
  const pagination = usePagination(report.months.slice().reverse());
  const revenue = sumMoney(t.commission, t.other_revenue);
  const max = report.months.reduce((max, m) => { const n = cents(m.profit); return (n < 0n ? -n : n) > max ? (n < 0n ? -n : n) : max; }, 0n);
  const currentMonth = report.as_of.slice(0, 7);
  return <div className={`overview ${exact ? 'overview-show-exact' : ''}`}>
    <div className="overview-heading"><div><p className="overview-eyebrow">Управленческая аналитика</p><h1>Бизнес в цифрах</h1><p>{report.first_day ? `Вся история с ${day(report.first_day)}` : 'История появится после первой проводки'}</p></div><div className="overview-snapshot">Снимок на {new Intl.DateTimeFormat('ru-RU', { dateStyle: 'short', timeStyle: 'short', timeZone: 'Europe/Moscow' }).format(new Date(report.as_of))} МСК<small>Только опубликованные проводки</small><Button variant="ghost" className="overview-precision" aria-pressed={exact} onClick={() => setExact(!exact)}>{exact ? 'Компактные суммы' : 'Точные суммы'}</Button></div></div>
    <section aria-label="Финансовый результат за всё время" className="overview-kpis">
      {[
        ['Оборот по реестрам', t.turnover, 'Транзит мерчантов, с учётом сторно'],
        ['Комиссионная выручка', t.commission, `Доля в обороте · ${ratio(t.commission, t.turnover)}`],
        ['Расходы', t.expenses, 'Без дивидендов и возвратов мерчантам'],
        ['Управленческая прибыль', t.profit, `Маржа по всем доходам · ${ratio(t.profit, revenue)}`],
      ].map(([label, value, hint], i) => <Card key={label} className={`overview-kpi ${i === 3 ? 'overview-kpi-profit' : ''}`}><CardContent><h2>{label}</h2><strong><Amount value={value} /></strong><p>{hint}</p></CardContent></Card>)}
    </section>
    <div className="overview-split">
      <section className="overview-panel" aria-labelledby="money-now"><div className="overview-section-heading"><h2 id="money-now">Где деньги сейчас</h2><span>Учётные остатки</span></div><div className="overview-total"><Amount value={sumMoney(t.cards, t.cash, t.chief_cash)} /></div><Lines items={[
        ['На картах', t.cards], ['У сборщиков и хранителей', t.cash], ['У главного администратора', t.chief_cash],
      ]} /></section>
      <section className="overview-panel" aria-labelledby="settlements"><div className="overview-section-heading"><h2 id="settlements">Долги и расчёты</h2><span>На момент снимка</span></div><Lines items={[
        ['К возврату мерчантам', t.merchant_payable, 'Включая отдельный долг по излишкам'],
        ['Мерчанты должны нам', t.merchant_receivable, 'Переплаты, отдельно от обязательств'],
        ['Прочая дебиторка', t.other_receivable, 'В том числе недостачи ответственных'],
      ]} /><p className="overview-note">Деньги в сопровождении включают средства мерчантов и не равны доступной прибыли.</p></section>
    </div>
    <section className="overview-panel overview-history" aria-labelledby="monthly-title"><div className="overview-section-heading"><div><h2 id="monthly-title">Динамика по месяцам</h2><p>По дате признания · сторно и перерасчёты учтены</p></div><span>За всё время</span></div>
      {report.months.length ? <><Table aria-label="Динамика по месяцам"><TableHeader><TableRow className="overview-month-labels"><TableHead>Месяц</TableHead><TableHead>Оборот</TableHead><TableHead>Комиссия</TableHead><TableHead>Расходы</TableHead><TableHead>Прибыль</TableHead></TableRow></TableHeader><TableBody className="overview-months">{pagination.rows.map(m => {
        const profit = cents(m.profit), abs = profit < 0n ? -profit : profit;
        const width = max ? Number(abs * 10000n / max) / 100 : 0;
        const partial = m.month === currentMonth || (m.month === report.first_day?.slice(0, 7) && report.first_day?.slice(-2) !== '01');
        return <TableRow key={m.month}><TableCell className="overview-month-name">{monthLabel(m.month)}{partial && <small>Неполный период</small>}</TableCell>{[['Оборот', m.turnover], ['Комиссия', m.commission], ['Расходы', m.expenses], ['Прибыль', m.profit]].map(([label, value]) => <TableCell key={label} className={label === 'Прибыль' ? 'overview-month-profit' : ''}><span className="overview-mobile-label">{label}</span><Amount value={value} />{label === 'Прибыль' && <span className={`overview-bar ${profit < 0n ? 'overview-bar-negative' : ''}`} aria-hidden="true"><i style={{ width: `${width}%` }} /></span>}</TableCell>)}</TableRow>;
      })}</TableBody></Table><TablePagination page={pagination.page} totalPages={pagination.totalPages} total={report.months.length} onPageChange={pagination.setPage} /></> : <p className="overview-note">Проведённых операций пока нет.</p>}
      <p className="overview-note">Первый и текущий месяцы могут быть неполными. Итоги не используются для автоматического сравнения темпов роста. Прочие доходы за всё время: <Amount value={t.other_revenue} />.</p>
    </section>
    <div className="overview-split overview-bottom"><section className="overview-panel"><div className="overview-section-heading"><h2>Масштаб работы</h2><span>За всю историю</span></div><dl className="overview-counts">{[['Реестров проведено', a.registries], ['Мерчантов с оплатами', a.merchants], ['Карт в оплатах', a.cards]].map(([label, value]) => <div key={label}><dd>{value}</dd><dt>{label}</dt></div>)}</dl><p className="overview-note">Уникальные мерчанты и карты в действующих проведённых реестрах.</p></section><section className="overview-panel"><div className="overview-section-heading"><h2>Ожидают подтверждения</h2><span>Сейчас</span></div><dl className="overview-counts"><div><dd>{p.registries}</dd><dt>Реестров</dt></div><div><dd>{p.operations}</dd><dt>Денежных операций</dt></div></dl><p className="overview-note">Черновики не включены в финансовые показатели.</p></section></div>
    <details className="overview-method"><summary>Как считаются показатели</summary><p>Комиссия и прочие доходы за вычетом расходов формируют управленческую прибыль. Доля комиссии — комиссия / оборот; маржа — прибыль / все доходы. При неположительной базе процент не рассчитывается. Дивиденды уменьшают капитал, а не прибыль. Учётные остатки могут отличаться от банковских наблюдений; сверка доступна в разделе «Остатки». История отражает внесённые в систему события, включая начальные остатки, и не гарантирует охват времени до начала учёта.</p><p>Методология разделяет транзит, выручку и деньги по принципам IFRS 15 и IAS 7. Это управленческий обзор; статус полной отчётности по МСФО ему не присваивается.</p></details>
  </div>;
}
