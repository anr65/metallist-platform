import test from 'node:test';
import assert from 'node:assert/strict';
import { allowedPages, pageForRole, stripQueryFromLocation } from './access.js';

test('operator starts on card requests and cannot open restricted sections by URL', () => {
  assert.equal(pageForRole('operator', ''), 'requests');
  for (const hash of ['#overview', '#expenses', '#reports', '#audit']) {
    assert.equal(pageForRole('operator', hash), 'requests');
  }
  assert.equal(pageForRole('operator', '#registries'), 'registries');
  assert.equal(pageForRole('operator', '#money'), 'money');
  assert.deepEqual(allowedPages('operator'), ['requests', 'registries', 'money', 'catalog', 'account']);
});

test('other roles land on a permitted page', () => {
  assert.equal(pageForRole('chief', ''), 'overview');
  assert.equal(pageForRole('sysadmin', ''), 'catalog');
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
