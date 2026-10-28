import test from 'node:test';
import assert from 'node:assert/strict';
import { cents, money, compactMoney, ratio, sumMoney } from './overview-metrics.js';
test('financial arithmetic retains kopecks beyond Number safe precision', () => {
 assert.equal(sumMoney('9007199254740993.01', '0.09'), '9007199254740993.10');
 assert.equal(cents('-0.01'), -1n);
 assert.equal(money('-1234.01').replaceAll('\u00a0', ' '), '−1 234,01 ₽');
 assert.equal(compactMoney('221769497.50').replaceAll('\u00a0', ' '), '221,77 млн ₽');
});
test('ratios use the correct denominator, handle losses and absent base', () => {
 assert.equal(ratio('4.00', '100.00'), '4,00 %');
 assert.equal(ratio('-1.00', '3.00'), '−33,33 %');
 assert.equal(ratio('0.00', '0.00'), 'Нет базы');
 assert.equal(ratio('1.00', '-1.00'), 'Нет базы');
 assert.equal(ratio('0.01', '0.03'), '33,33 %');
 assert.throws(() => cents(undefined));
});
