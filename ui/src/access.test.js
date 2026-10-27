import test from 'node:test';
import assert from 'node:assert/strict';
import { allowedPages, pageForRole, stripQueryFromLocation } from './access.js';

test('operator starts on card requests and cannot open restricted sections by URL', () => {
  assert.equal(pageForRole('operator', ''), 'requests');
  for (const hash of ['#overview', '#expenses', '#balances', '#reports', '#audit', '#merchants', '#merchants/new', '#merchants/11111111-1111-4111-8111-111111111111']) {
    assert.equal(pageForRole('operator', hash), 'requests');
  }
  assert.equal(pageForRole('operator', '#registries'), 'registries');
  assert.equal(pageForRole('operator', '#money'), 'money');
  assert.equal(pageForRole('operator', '#money/new'), 'money/new');
  assert.equal(pageForRole('operator', '#money/11111111-1111-4111-8111-111111111111'), 'money/11111111-1111-4111-8111-111111111111');
  assert.equal(pageForRole('operator', '#catalog/cards'), 'catalog/cards');
  assert.equal(pageForRole('operator', '#catalog/contacts'), 'catalog/contacts');
  assert.notEqual(pageForRole('operator', '#catalog/access'), 'catalog/access');
  assert.deepEqual(allowedPages('operator'), ['requests', 'registries', 'money', 'catalog', 'account']);
});

test('other roles land on a permitted page', () => {
  assert.equal(pageForRole('chief', ''), 'overview');
  assert.equal(pageForRole('chief', '#balances'), 'balances');
  assert.equal(pageForRole('chief', '#merchants'), 'merchants');
  assert.equal(pageForRole('chief', '#merchants/new'), 'merchants/new');
  assert.equal(pageForRole('chief', '#money/new'), 'money/new');
  assert.equal(pageForRole('chief', '#expenses'), 'money');
  assert.ok(!allowedPages('chief').includes('expenses'));
  assert.equal(pageForRole('accountant', '#money/new'), 'overview');
  for (const role of ['accountant', 'auditor', 'collector']) {
    assert.ok(!allowedPages(role).includes('merchants'));
    for (const hash of ['#merchants', '#merchants/new', '#merchants/11111111-1111-4111-8111-111111111111']) {
      assert.notEqual(pageForRole(role, hash), hash.slice(1));
    }
  }
  assert.equal(pageForRole('accountant', '#balances'), 'balances');
  assert.equal(pageForRole('auditor', '#balances'), 'balances');
  assert.equal(pageForRole('sysadmin', ''), 'overview');
  assert.equal(pageForRole('sysadmin', '#catalog/access'), 'catalog/access');
  assert.equal(pageForRole('collector', '#catalog/cards'), 'catalog/cards');
  assert.notEqual(pageForRole('collector', '#catalog/banks'), 'catalog/banks');
  assert.equal(pageForRole('collector', '#reports'), 'catalog');
  assert.equal(pageForRole('accountant', '#expenses'), 'overview');
  assert.equal(pageForRole('unknown', ''), 'account');
});

test('query credentials are removed while keeping the current page', () => {
  const calls = [];
  const location = { pathname: '/', search: '?login=example&password=synthetic-secret', hash: '#registries' };
  assert.equal(stripQueryFromLocation(location, { replaceState: (...args) => calls.push(args) }), true);
  assert.deepEqual(calls, [[null, '', '/#registries']]);
  assert.equal(stripQueryFromLocation({ ...location, search: '' }, { replaceState: () => assert.fail('already clean') }), false);
});

test('drop-handler directory is reachable for directory readers and hidden from collectors', () => {
  for (const role of ['chief', 'operator', 'accountant', 'auditor', 'sysadmin']) {
    assert.equal(pageForRole(role, '#catalog/drop_handlers'), 'catalog/drop_handlers');
  }
  assert.equal(pageForRole('collector', '#catalog/drop_handlers'), 'catalog');
});

test('sysadmin has full chief navigation and access management', () => {
 for (const route of ['overview','merchants/new','registries/new','money/new','requests/new','balances','reports','catalog/access','catalog/contacts']) {
  assert.equal(pageForRole('sysadmin','#'+route),route);
 }
});
