export const navigationByRole = {
  chief: ['overview', 'requests', 'registries', 'expenses', 'money', 'catalog', 'reports', 'audit'],
  operator: ['requests', 'registries', 'money', 'catalog'],
  accountant: ['overview', 'requests', 'registries', 'catalog', 'reports', 'audit'],
  auditor: ['overview', 'requests', 'registries', 'catalog', 'reports', 'audit'],
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
