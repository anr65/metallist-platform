// Transport errors use the same codes as the server's error catalog.
const fallback = {
  UNAUTHORIZED: 'Сеанс завершён. Войдите снова.',
  FORBIDDEN: 'У вас нет прав для этого действия.',
  NOT_FOUND: 'Запись не найдена. Обновите страницу.',
  CONFLICT: 'Данные изменились. Обновите страницу и повторите попытку.',
  VALIDATION_ERROR: 'Проверьте введённые данные и повторите попытку.',
  INTERNAL_ERROR: 'Не удалось выполнить действие из-за внутренней ошибки. Повторите позже.',
  NETWORK_ERROR: 'Нет связи с сервером. Проверьте подключение и повторите попытку.',
  BAD_RESPONSE: 'Сервер вернул неожиданный ответ. Повторите попытку позже.',
};

export function userError(error) {
  if (error instanceof Error && !error.code) return fallback.INTERNAL_ERROR;
  const message = typeof error === 'string' ? error : error?.message;
  return message && /[А-Яа-яЁё]/.test(message) && !/\b(?:Error|SQL|SELECT|INSERT|UPDATE)\b/i.test(message)
    ? message : fallback[error?.code] || fallback.INTERNAL_ERROR;
}

export async function readResponse(response) {
  let data;
  try { data = await response.json(); }
  catch { throw Object.assign(new Error(fallback.BAD_RESPONSE), { code: 'BAD_RESPONSE' }); }
  if (!response.ok) {
    const code = data?.code || (response.status >= 500 ? 'INTERNAL_ERROR' : response.status === 401 ? 'UNAUTHORIZED' : response.status === 403 ? 'FORBIDDEN' : response.status === 404 ? 'NOT_FOUND' : response.status === 409 ? 'CONFLICT' : 'VALIDATION_ERROR');
    const message = data?.code ? userError(data?.error) : fallback[code] || fallback.INTERNAL_ERROR;
    throw Object.assign(new Error(message === fallback.INTERNAL_ERROR ? fallback[code] || fallback.INTERNAL_ERROR : message), { code });
  }
  return data;
}

export async function request(fetcher) {
  let response;
  try { response = await fetcher(); }
  catch { throw Object.assign(new Error(fallback.NETWORK_ERROR), { code: 'NETWORK_ERROR' }); }
  return readResponse(response);
}
