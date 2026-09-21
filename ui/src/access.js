export const navigationByRole = {
  chief: ['overview', 'requests', 'registries', 'expenses', 'money', 'balances', 'catalog', 'reports', 'audit'],
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
  return allowed.includes(requested) ? requested : allowed[0] || 'account';
}

export function stripQueryFromLocation(location, history) {
  if (!location.search) return false;
  history.replaceState(null, '', `${location.pathname}${location.hash}`);
  return true;
}
