export const navigationByRole = {
  chief: ['overview', 'requests', 'registries', 'merchants', 'money', 'balances', 'catalog', 'reports', 'audit'],
  operator: ['requests', 'registries', 'money', 'catalog'],
  accountant: ['overview', 'requests', 'registries', 'balances', 'catalog', 'reports', 'audit'],
  auditor: ['overview', 'requests', 'registries', 'balances', 'catalog', 'reports', 'audit'],
  sysadmin: ['overview', 'requests', 'registries', 'merchants', 'money', 'balances', 'catalog', 'reports', 'audit'],
  collector: ['catalog'],
};

export function allowedPages(role) {
  return [...(navigationByRole[role] || []), 'account'];
}

export function pageForRole(role, hash) {
  const allowed = allowedPages(role);
  const requested = hash.replace(/^#/, '');
  if (requested === 'expenses' && ['chief', 'sysadmin'].includes(role)) return 'money';
  if (allowed.includes('requests') && /^requests\/[0-9a-f-]{36}$/i.test(requested)) return requested;
  if (allowed.includes('requests') && ['chief', 'sysadmin', 'operator'].includes(role) && requested === 'requests/new') return requested;
  if (allowed.includes('registries') && /^registries\/[0-9a-f-]{36}$/i.test(requested)) return requested;
  if (allowed.includes('registries') && ['chief', 'sysadmin', 'operator'].includes(role) && requested === 'registries/new') return requested;
  if (allowed.includes('merchants') && /^merchants\/[0-9a-f-]{36}$/i.test(requested)) return requested;
  if (allowed.includes('merchants') && ['chief', 'sysadmin'].includes(role) && requested === 'merchants/new') return requested;
  if (allowed.includes('money') && /^money\/[0-9a-f-]{36}$/i.test(requested)) return requested;
  if (allowed.includes('money') && ['chief', 'sysadmin', 'operator'].includes(role) && requested === 'money/new') return requested;
  if (allowed.includes('catalog') && requested === 'catalog/cards') return requested;
  if (allowed.includes('catalog') && role !== 'collector' && /^catalog\/(banks|custodians|drop_handlers)$/.test(requested)) return requested;
  if (allowed.includes('catalog') && ['chief', 'sysadmin', 'operator'].includes(role) && requested === 'catalog/contacts') return requested;
  if (allowed.includes('catalog') && role === 'sysadmin' && requested === 'catalog/access') return requested;
  return allowed.includes(requested) ? requested : allowed[0] || 'account';
}

export function stripQueryFromLocation(location, history) {
  if (!location.search) return false;
  history.replaceState(null, '', `${location.pathname}${location.hash}`);
  return true;
}
