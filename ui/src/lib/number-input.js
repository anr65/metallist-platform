export function rawNumber(value) {
  return String(value ?? '').replace(/\s/g, '');
}

export function formatNumber(value) {
  const raw = rawNumber(value);
  if (!/^-?\d*(?:[.,]\d*)?$/.test(raw)) return raw;
  const [integer, fraction] = raw.split(/[.,]/);
  const separator = raw.includes(',') ? ',' : '.';
  return integer.replace(/\B(?=(\d{3})+(?!\d))/g, ' ') + (fraction === undefined ? '' : separator + fraction);
}
