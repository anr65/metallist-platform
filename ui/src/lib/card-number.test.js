import test from 'node:test';
import assert from 'node:assert/strict';
import { cardNumberError, stripCardNumberSpaces } from './card-number.js';

test('PAN validation preserves textual identifiers and checks length and checksum', () => {
  // Synthetic, anonymized fixtures; no customer card numbers.
  for (const pan of ['0000000000061234', '0000000000000000000']) assert.equal(cardNumberError(pan), '');
  assert.match(cardNumberError('0000000000061235'), /контрольной суммы/);
  for (const pan of ['', '1234', '0000 0000 0006 1234', '000000000006123X', '0'.repeat(20)]) {
    assert.match(cardNumberError(pan), /от 16 до 19 цифр/);
  }
});

test('card creation strips pasted and typed whitespace without changing digits', () => {
  for (const value of ['0000 0000 0006 1234', ' 0000\u00a00000\u202f0006\t1234\n', '0000000000061234']) {
    assert.equal(stripCardNumberSpaces(value), '0000000000061234');
    assert.equal(cardNumberError(stripCardNumberSpaces(value)), '');
  }
  assert.equal(stripCardNumberSpaces(''), '');
  assert.equal(stripCardNumberSpaces('0000-0000'), '0000-0000');
});
