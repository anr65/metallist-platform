import test from 'node:test';
import assert from 'node:assert/strict';
import { filterCards } from './card-filters.js';

const cards = [
  { id: '1', mask: '220000******4496', bank_id: 'a', name: 'Банк Север', owner_label: 'Владелец не указан', status: 'active' },
  { id: '2', mask: '220000******1234', bank_id: 'b', name: 'Банк Юг', owner_label: 'Семёнов Иван', drop_handler_id: 'd', drop_handler_name: 'Илья', status: 'blocked' },
  { id: '3', mask: '220000******4496', bank_id: 'b', name: 'Банк Юг', owner_label: 'Тестовый владелец', drop_handler_id: 'e', drop_handler_name: 'Илья', status: 'limit' },
];
const ids = filters => filterCards(cards, filters).map(card => card.id);

test('search spans displayed columns with Russian case and ё normalization', () => {
  assert.deepEqual(ids({ search: '  СЕМЕНОВ   блок ' }), ['2']);
  assert.deepEqual(ids({ search: '4496 север' }), ['1']);
  assert.deepEqual(ids({ search: 'не назначен' }), ['1']);
  assert.deepEqual(ids({ search: 'лимит' }), ['3']);
});
test('all column filters combine and distinguish equal masks or handler names by IDs', () => {
  assert.deepEqual(ids({ mask: '4496', bank: 'b', owner: 'владелец', ownerPresence: 'specified', handler: 'e', status: 'limit' }), ['3']);
  assert.deepEqual(ids({ mask: '4496', handler: 'd' }), []);
  assert.deepEqual(ids({ bank: 'b', status: 'active' }), []);
});
test('missing owners and handlers can be selected independently, reset restores all cards', () => {
  assert.deepEqual(ids({ ownerPresence: 'unspecified', handler: 'unassigned' }), ['1']);
  assert.deepEqual(filterCards([{ owner_label: '  ' }, { owner_label: null }], { ownerPresence: 'unspecified' }).length, 2);
  assert.deepEqual(ids({}), ['1', '2', '3']);
});
