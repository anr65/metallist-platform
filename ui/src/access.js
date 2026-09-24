export const navigationByRole = {
  chief: ['overview', 'requests', 'registries', 'merchants', 'expenses', 'money', 'balances', 'catalog', 'reports', 'audit'],
  operator: ['requests', 'registries', 'money', 'catalog'],
  accountant: ['overview', 'requests', 'registries', 'balances', 'catalog', 'reports', 'audit'],
  auditor: ['overview', 'requests', 'registries', 'balances', 'catalog', 'reports', 'audit'],
  sysadmin: ['catalog', 'audit'],
  collector: ['catalog'],
};

export function allowedPages(role) {
  return [...(navigationByRole[role] || []), 'account'];
}

export function pageForRole(role, hash) {
  const allowed = allowedPages(role);
  const requested = hash.replace(/^#/, '');
  if (allowed.includes('requests') && /^requests\/[0-9a-f-]{36}$/i.test(requested)) return requested;
  if (allowed.includes('requests') && ['chief', 'operator'].includes(role) && requested === 'requests/new') return requested;
  if (allowed.includes('registries') && /^registries\/[0-9a-f-]{36}$/i.test(requested)) return requested;
  if (allowed.includes('registries') && ['chief', 'operator'].includes(role) && requested === 'registries/new') return requested;
  if (allowed.includes('merchants') && /^merchants\/[0-9a-f-]{36}$/i.test(requested)) return requested;
  if (allowed.includes('merchants') && role === 'chief' && requested === 'merchants/new') return requested;
  return allowed.includes(requested) ? requested : allowed[0] || 'account';
}

export function stripQueryFromLocation(location, history) {
  if (!location.search) return false;
  history.replaceState(null, '', `${location.pathname}${location.hash}`);
  return true;
}
