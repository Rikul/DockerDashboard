import { useMemo, useState, type ReactNode } from 'react';

export interface Column<T> {
  key: string;
  label: string;
  render: (row: T) => ReactNode;
  /** Value to sort by; omit to make the column unsortable. */
  sort?: (row: T) => string | number | boolean | undefined | null;
  align?: 'right';
  /** Hide on narrow screens. */
  secondary?: boolean;
}

export interface Filter<T> {
  id: string;
  label: string;
  test: (row: T) => boolean;
}

interface Props<T> {
  rows: T[];
  rowKey: (row: T) => string;
  columns: Column<T>[];
  /** Text the search box matches against. */
  searchText?: (row: T) => string;
  searchPlaceholder?: string;
  filters?: Filter<T>[];
  filter?: string;
  onFilter?: (id: string) => void;
  initialSort?: { key: string; dir: 'asc' | 'desc' };
  actions?: (row: T) => ReactNode;
  toolbar?: ReactNode;
  empty?: ReactNode;
  rowClass?: (row: T) => string | undefined;
}

export function DataTable<T>({
  rows,
  rowKey,
  columns,
  searchText,
  searchPlaceholder = 'Search…',
  filters,
  filter: controlledFilter,
  onFilter,
  initialSort,
  actions,
  toolbar,
  empty = 'Nothing here',
  rowClass,
}: Props<T>) {
  const [q, setQ] = useState('');
  const [localFilter, setLocalFilter] = useState(filters?.[0]?.id ?? 'all');
  const filter = controlledFilter ?? localFilter;
  const setFilter = onFilter ?? setLocalFilter;
  const [sort, setSort] = useState(initialSort ?? { key: columns[0]?.key ?? '', dir: 'asc' as const });

  const shown = useMemo(() => {
    const f = filters?.find((x) => x.id === filter);
    let r = f ? rows.filter(f.test) : rows;
    const needle = q.trim().toLowerCase();
    if (needle && searchText) r = r.filter((row) => searchText(row).toLowerCase().includes(needle));
    const col = columns.find((c) => c.key === sort.key);
    if (col?.sort) {
      const get = col.sort;
      const dir = sort.dir === 'asc' ? 1 : -1;
      r = [...r].sort((a, b) => {
        let va = get(a);
        let vb = get(b);
        if (va == null) return 1;
        if (vb == null) return -1;
        if (typeof va === 'string') va = va.toLowerCase();
        if (typeof vb === 'string') vb = vb.toLowerCase();
        return (va < vb ? -1 : va > vb ? 1 : 0) * dir;
      });
    }
    return r;
  }, [rows, filters, filter, q, searchText, columns, sort]);

  const toggleSort = (c: Column<T>) => {
    if (!c.sort) return;
    if (sort.key === c.key) setSort({ key: c.key, dir: sort.dir === 'asc' ? 'desc' : 'asc' });
    else {
      // Text sorts A→Z first; numbers biggest first.
      const sample = rows.length ? c.sort(rows[0]!) : '';
      setSort({ key: c.key, dir: typeof sample === 'string' ? 'asc' : 'desc' });
    }
  };

  return (
    <div className="table-wrap">
      {(searchText || filters || toolbar) && (
        <div className="tools">
          {searchText && (
            <input
              className="search"
              type="search"
              value={q}
              onChange={(e) => setQ(e.target.value)}
              placeholder={searchPlaceholder}
              aria-label={searchPlaceholder}
            />
          )}
          {filters?.map((f) => (
            <button key={f.id} type="button" className="chip" aria-pressed={filter === f.id} onClick={() => setFilter(f.id)}>
              {f.label}
              <span className="chip-count">{rows.filter(f.test).length}</span>
            </button>
          ))}
          <span className="spacer" />
          {toolbar}
        </div>
      )}
      <div className="table-box">
        <table className="table">
          <thead>
            <tr>
              {columns.map((c) => (
                <th
                  key={c.key}
                  className={`${c.align === 'right' ? 'num' : ''}${c.secondary ? ' secondary' : ''}`}
                  aria-sort={sort.key === c.key ? (sort.dir === 'asc' ? 'ascending' : 'descending') : undefined}
                >
                  {c.sort ? (
                    <button type="button" onClick={() => toggleSort(c)}>
                      {c.label}
                      {sort.key === c.key ? (sort.dir === 'asc' ? ' ↑' : ' ↓') : ''}
                    </button>
                  ) : (
                    c.label
                  )}
                </th>
              ))}
              {actions && <th className="actions-col">Actions</th>}
            </tr>
          </thead>
          <tbody>
            {shown.length === 0 ? (
              <tr>
                <td className="muted empty" colSpan={columns.length + (actions ? 1 : 0)}>
                  {q ? 'No matches' : empty}
                </td>
              </tr>
            ) : (
              shown.map((row) => (
                <tr key={rowKey(row)} className={rowClass?.(row)}>
                  {columns.map((c) => (
                    <td key={c.key} className={`${c.align === 'right' ? 'num' : ''}${c.secondary ? ' secondary' : ''}`}>
                      {c.render(row)}
                    </td>
                  ))}
                  {actions && (
                    <td>
                      <div className="row-actions">{actions(row)}</div>
                    </td>
                  )}
                </tr>
              ))
            )}
          </tbody>
        </table>
      </div>
    </div>
  );
}
