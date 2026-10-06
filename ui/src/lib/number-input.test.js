import test from 'node:test';
import assert from 'node:assert/strict';
import { formatNumber, rawNumber } from './number-input.js';

test('number mask groups thousands without rounding or losing decimal input', () => {
  for (const [raw, formatted] of [
    ['1234567,89', '1 234 567,89'], ['1234567.89', '1 234 567.89'],
    ['1234,', '1 234,'], ['-12345', '-12 345'], ['0,01', '0,01'], ['', ''],
    ['9007199254740993', '9 007 199 254 740 993'], ['1234,0001', '1 234,0001'],
  ]) {
    assert.equal(formatNumber(raw), formatted);
    assert.equal(rawNumber(formatted), raw);
  }
  assert.equal(rawNumber('1\u00a0234\u202f567,89'), '1234567,89');
});
