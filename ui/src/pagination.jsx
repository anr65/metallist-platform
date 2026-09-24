import React, { useEffect, useState } from 'react';
import { Button } from '@/components/ui/button';

export const PAGE_SIZE = 15;

export function usePagination(rows, key = '') {
  const [page, setPage] = useState(1);
  useEffect(() => setPage(1), [key]);
  const totalPages = Math.max(1, Math.ceil(rows.length / PAGE_SIZE));
  const currentPage = Math.min(page, totalPages);
  return {
    rows: rows.slice((currentPage - 1) * PAGE_SIZE, currentPage * PAGE_SIZE),
    page: currentPage,
    totalPages,
    setPage,
  };
}

export function TablePagination({ page, totalPages, total, onPageChange }) {
  if (totalPages <= 1) return null;
  return <nav aria-label="Страницы таблицы" className="flex flex-wrap items-center justify-between gap-3 border-t border-border px-4 py-3 text-sm text-muted-foreground sm:px-6">
    <span>Показано {(page - 1) * PAGE_SIZE + 1}–{Math.min(page * PAGE_SIZE, total)} из {total}</span>
    <div className="flex items-center gap-2">
      <Button type="button" variant="secondary" size="sm" disabled={page <= 1} onClick={() => onPageChange(page - 1)}>Назад</Button>
      <span aria-live="polite">{page} / {totalPages}</span>
      <Button type="button" variant="secondary" size="sm" disabled={page >= totalPages} onClick={() => onPageChange(page + 1)}>Далее</Button>
    </div>
  </nav>;
}

export function ServerTablePagination({ page, hasMore, onPageChange }) {
  if (page === 1 && !hasMore) return null;
  return <nav aria-label="Страницы таблицы" className="flex items-center justify-end gap-3 border-t border-border px-4 py-3 text-sm sm:px-6">
    <Button type="button" variant="secondary" size="sm" disabled={page <= 1} onClick={() => onPageChange(page - 1)}>Назад</Button>
    <span aria-live="polite">Страница {page}</span>
    <Button type="button" variant="secondary" size="sm" disabled={!hasMore} onClick={() => onPageChange(page + 1)}>Далее</Button>
  </nav>;
}
