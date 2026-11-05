import { useEffect, useState } from 'react';
import { request, userError } from './api-errors';

// Stable serialization avoids refetching when a parent recreates its option array.
export function useDropdownSearch(options, query, open, source = '', context = '') {
  const payload = JSON.stringify({ q: query, options, source, context });
  const [result, setResult] = useState({ key: '', values: [], error: '' });
  const [retry, setRetry] = useState(0);
  useEffect(() => {
    if (!open) return;
    const controller = new AbortController();
    let live = true;
    const timer = setTimeout(async () => {
      try {
        const data = await request(() => fetch('/api/dropdown/search', {
          method: 'POST', credentials: 'same-origin', signal: controller.signal,
          headers: { 'Content-Type': 'application/json', 'X-CSRF': '1' }, body: payload,
        }));
        if (live) setResult({ key: payload, values: data.values, error: '' });
      } catch (error) {
        if (live) setResult({ key: payload, values: [], error: userError(error) });
      }
    }, 180);
    return () => { live = false; clearTimeout(timer); controller.abort(); };
  }, [open, payload, retry]);
  return {
    values: result.key === payload ? result.values : [],
    loading: open && result.key !== payload,
    error: result.key === payload ? result.error : '',
    retry: () => { setResult({ key: '', values: [], error: '' }); setRetry(x => x + 1); },
  };
}
