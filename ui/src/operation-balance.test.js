import test from 'node:test';
import assert from 'node:assert/strict';
import { balanceMoney, balanceLines } from './operation-balance.js';

test('balances preserve exact rubles, signs and kopecks, including amounts above Number precision', () => {
  assert.equal(balanceMoney('0.00'), '0,00 ₽');
  assert.equal(balanceMoney('-1234567.89'), '-1 234 567,89 ₽');
  assert.equal(balanceMoney('9007199254740993.01'), '9 007 199 254 740 993,01 ₽');
  for (const value of [null, undefined, '', 'bad']) assert.equal(balanceMoney(value), null);
});

test('entity IDs are resolved within their own financial dimension; observed values do not replace ledger balances', () => {
  const data = { cards: [{ id: 'same', amount: '10.50', observed: '99.99' }], custodians: [{ id: 'same', amount: '-5.25' }] };
  assert.deepEqual(balanceLines('card', 'same', data), [['Учётный остаток', '10,50 ₽']]);
  assert.deepEqual(balanceLines('custodian', 'same', data), [['Учётный остаток', '-5,25 ₽']]);
  assert.deepEqual(balanceLines('card', 'missing', data), []);
  assert.deepEqual(balanceLines('custodian', 'same', null), []);
});

test('merchant liabilities and overpayments stay separate; missing or unauthorized data is not displayed as zero', () => {
  const catalog = { merchants: [{ id: 'm', payable: '150.00', receivable: '20.01', position: '129.99' }, { id: 'restricted' }] };
  assert.deepEqual(balanceLines('merchant', 'm', null, catalog), [['Долг мерчанту', '150,00 ₽'], ['Переплата мерчанту', '20,01 ₽']]);
  assert.deepEqual(balanceLines('merchant', 'restricted', null, catalog), []);
});
