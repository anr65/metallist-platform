// API money values are decimal strings in rubles; preserve every digit.
export function balanceMoney(value) {
  const match = String(value ?? '').match(/^(-?)(\d+)\.(\d{2})$/);
  if (!match) return null;
  return `${match[1]}${match[2].replace(/\B(?=(\d{3})+(?!\d))/g, ' ')},${match[3]} ₽`;
}

export function balanceLines(type, id, data, catalog) {
  const row = type === 'merchant'
    ? catalog?.merchants?.find(item => item.id === id)
    : data?.[type === 'card' ? 'cards' : 'custodians']?.find(item => item.id === id);
  if (!row) return [];
  const values = type === 'merchant'
    ? [['Долг мерчанту', row.payable], ['Переплата мерчанту', row.receivable]]
    : [['Учётный остаток', row.amount]];
  return values.flatMap(([label, value]) => {
    const amount = balanceMoney(value);
    return amount === null ? [] : [[label, amount]];
  });
}
