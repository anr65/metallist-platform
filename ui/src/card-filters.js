export const cardStatusLabels = { active: 'Активна', limit: 'Лимит', blocked: 'Блок', retired: 'Архив' };
const normalize = value => String(value ?? '').trim().toLocaleLowerCase('ru-RU').replaceAll('ё', 'е');
export const hasCardOwner = card => Boolean(normalize(card.owner_label)) && normalize(card.owner_label) !== 'владелец не указан';

export function filterCards(cards, filters = {}) {
  const words = normalize(filters.search).split(/\s+/).filter(Boolean);
  return cards.filter(card => {
    const searchable = normalize([card.mask, card.name, card.owner_label, card.drop_handler_name || 'Не назначен', cardStatusLabels[card.status] || card.status].join(' '));
    return words.every(word => searchable.includes(word))
      && normalize(card.mask).includes(normalize(filters.mask))
      && (!filters.bank || card.bank_id === filters.bank)
      && normalize(card.owner_label).includes(normalize(filters.owner))
      && (!filters.ownerPresence || hasCardOwner(card) === (filters.ownerPresence === 'specified'))
      && (!filters.handler || (filters.handler === 'unassigned' ? !card.drop_handler_id : card.drop_handler_id === filters.handler))
      && (!filters.status || card.status === filters.status);
  });
}
