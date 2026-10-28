const integers = new Intl.NumberFormat('ru-RU');
export function cents(value) {
  if (!/^-?\d+\.\d{2}$/.test(value)) throw new Error('Некорректная сумма обзора');
  const negative = value.startsWith('-');
  const [whole, fraction] = value.replace('-', '').split('.');
  return (BigInt(whole) * 100n + BigInt(fraction)) * (negative ? -1n : 1n);
}
export function money(value) {
  const n = cents(value), abs = n < 0n ? -n : n;
  return `${n < 0n ? '−' : ''}${integers.format(abs / 100n)},${String(abs % 100n).padStart(2, '0')} ₽`;
}
export function compactMoney(value) {
  const n = cents(value), abs = n < 0n ? -n : n;
  const unit = abs >= 100000000000n ? [100000000000n, 'млрд'] : abs >= 100000000n ? [100000000n, 'млн'] : null;
  if (!unit) return money(value);
  const rounded = (abs * 100n + unit[0] / 2n) / unit[0];
  return `${n < 0n ? '−' : ''}${integers.format(rounded / 100n)},${String(rounded % 100n).padStart(2, '0')} ${unit[1]} ₽`;
}
export function ratio(numerator, denominator) {
  const n = cents(numerator), d = cents(denominator);
  if (d <= 0n) return 'Нет базы';
  const abs = n < 0n ? -n : n, bp = (abs * 10000n + d / 2n) / d;
  return `${n < 0n ? '−' : ''}${integers.format(bp / 100n)},${String(bp % 100n).padStart(2, '0')} %`;
}
export function sumMoney(...values) {
  const n = values.reduce((sum, value) => sum + cents(value), 0n), abs = n < 0n ? -n : n;
  return `${n < 0n ? '-' : ''}${abs / 100n}.${String(abs % 100n).padStart(2, '0')}`;
}
