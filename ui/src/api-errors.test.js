import test from 'node:test';
import assert from 'node:assert/strict';
import { readResponse, request, userError } from './api-errors.js';

test('shows a Russian business error from the API', async () => {
  await assert.rejects(
    readResponse({ ok: false, status: 409, json: async () => ({ code: 'INSUFFICIENT_BALANCE', error: 'Недостаточно денег у ответственного.' }) }),
    error => error.code === 'INSUFFICIENT_BALANCE' && error.message === 'Недостаточно денег у ответственного.',
  );
});

test('does not expose technical errors or a broken response', async () => {
  await assert.rejects(readResponse({ ok: false, status: 500, json: async () => ({ error: 'password secret' }) }), error => !error.message.includes('secret') && /[А-Яа-я]/.test(error.message));
  await assert.rejects(readResponse({ ok: false, status: 502, json: async () => { throw new SyntaxError('HTML'); } }), error => error.code === 'BAD_RESPONSE');
  assert.match(userError(new TypeError("Cannot read properties of null (reading 'reset')")), /внутренней ошибки/);
});

test('network failures have a Russian message', async () => {
  await assert.rejects(request(async () => { throw new TypeError('Failed to fetch'); }), error => error.code === 'NETWORK_ERROR' && /Нет связи/.test(error.message));
});
