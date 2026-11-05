import { useCallback, useEffect, useLayoutEffect, useRef, useState, type KeyboardEvent } from 'react';
import { createPortal } from 'react-dom';
import { Icon, type IconName } from './icons';

export interface MenuItem {
  label: string;
  icon?: IconName;
  onClick: () => void;
  danger?: boolean;
  /** The action is running: the item is disabled and its icon becomes a spinner. */
  busy?: boolean;
  disabled?: boolean;
}

/** Callers may write `cond && {…}` inline; falsy entries are dropped. */
export type MenuEntry = MenuItem | false | null | undefined;

export const menuItems = (entries: MenuEntry[]): MenuItem[] => entries.filter((e): e is MenuItem => !!e);

/** Keep in step with the animation durations in styles.css. */
const CLOSE_MS = 120;
const EDGE = 8;
/** Space between the button and the menu. */
const GAP = 4;
/** The smallest menu height worth showing before overlapping the button instead. */
const MIN_ROOM = 120;

type Phase = 'closed' | 'open' | 'closing';

/**
 * A three-dots button that opens a small menu of per-row actions.
 *
 * The menu is portalled to <body> and positioned with `fixed`, because table
 * wrappers scroll sideways and would clip an absolutely positioned popup.
 */
export function ActionMenu({ items, label = 'Actions' }: { items: MenuEntry[]; label?: string }) {
  const list = menuItems(items);
  const [phase, setPhase] = useState<Phase>('closed');
  const [pos, setPos] = useState<{ top: number; left: number; maxHeight?: number; origin: string } | null>(null);
  const trigger = useRef<HTMLButtonElement>(null);
  const menu = useRef<HTMLDivElement>(null);
  const timer = useRef<number>(undefined);
  const viaKeyboard = useRef(false);

  const close = useCallback((refocus = false) => {
    setPhase((p) => (p === 'open' ? 'closing' : p));
    window.clearTimeout(timer.current);
    timer.current = window.setTimeout(() => setPhase('closed'), CLOSE_MS);
    if (refocus) trigger.current?.focus();
  }, []);

  const open = (byKeyboard: boolean) => {
    window.clearTimeout(timer.current);
    viaKeyboard.current = byKeyboard;
    setPos(null);
    setPhase('open');
  };

  useEffect(() => () => window.clearTimeout(timer.current), []);

  // Place the menu under the button, right-aligned with it; flip above when
  // there is more room there. A menu taller than the room on that side gets
  // an internal scrollbar, and it always stays inside the viewport. Runs
  // before paint so it never flashes elsewhere.
  useLayoutEffect(() => {
    if (phase !== 'open' || pos) return;
    const t = trigger.current?.getBoundingClientRect();
    const m = menu.current;
    if (!t || !m) return;
    const vh = window.innerHeight;
    const w = m.offsetWidth;
    const h = m.offsetHeight;
    const below = vh - t.bottom - GAP - EDGE;
    const above = t.top - GAP - EDGE;
    const up = h > below && above > below;
    // With next to no room on either side, overlap the button rather than
    // squeeze the menu to a sliver.
    const room = Math.max(up ? above : below, Math.min(h, MIN_ROOM));
    const height = Math.min(h, room, vh - 2 * EDGE);
    const want = up ? t.top - GAP - height : t.bottom + GAP;
    const top = Math.max(EDGE, Math.min(want, vh - EDGE - height));
    const left = Math.max(EDGE, Math.min(t.right - w, window.innerWidth - w - EDGE));
    setPos({ top, left, maxHeight: height < h ? height : undefined, origin: `${Math.round(t.right - left)}px ${up ? 'bottom' : 'top'}` });
  }, [phase, pos]);

  // Once placed, move focus into the menu.
  useEffect(() => {
    if (phase !== 'open' || !pos) return;
    const m = menu.current;
    if (!m) return;
    if (viaKeyboard.current) m.querySelector<HTMLElement>('[role="menuitem"]:not(:disabled)')?.focus();
    else m.focus({ preventScroll: true });
  }, [phase, pos]);

  // Dismiss on outside press, scroll and resize.
  useEffect(() => {
    if (phase !== 'open') return;
    const onPointer = (e: PointerEvent) => {
      const n = e.target as Node;
      if (menu.current?.contains(n) || trigger.current?.contains(n)) return;
      close();
    };
    const onScroll = (e: Event) => {
      if (menu.current?.contains(e.target as Node)) return;
      close();
    };
    const onResize = () => close();
    document.addEventListener('pointerdown', onPointer, true);
    window.addEventListener('scroll', onScroll, true);
    window.addEventListener('resize', onResize);
    return () => {
      document.removeEventListener('pointerdown', onPointer, true);
      window.removeEventListener('scroll', onScroll, true);
      window.removeEventListener('resize', onResize);
    };
  }, [phase, close]);

  if (list.length === 0) return null;

  const busy = list.some((i) => i.busy);
  const enabled = () => Array.from(menu.current?.querySelectorAll<HTMLElement>('[role="menuitem"]:not(:disabled)') ?? []);

  const onMenuKey = (e: KeyboardEvent) => {
    const els = enabled();
    const at = els.indexOf(document.activeElement as HTMLElement);
    const go = (i: number) => {
      e.preventDefault();
      els[(i + els.length) % els.length]?.focus();
    };
    switch (e.key) {
      case 'ArrowDown':
        return go(at + 1);
      case 'ArrowUp':
        return go(at < 0 ? -1 : at - 1);
      case 'Home':
        return go(0);
      case 'End':
        return go(-1);
      case 'Escape':
        e.preventDefault();
        e.stopPropagation();
        return close(true);
      case 'Tab':
        // Put focus back on the button and let the browser's own Tab move on
        // from there, so one press reaches the next (or previous) control.
        return close(true);
    }
  };

  const choose = (item: MenuItem) => {
    close(true);
    item.onClick();
  };

  return (
    <>
      <button
        ref={trigger}
        type="button"
        className="btn icon-only menu-trigger"
        aria-label={label}
        aria-haspopup="menu"
        aria-expanded={phase === 'open'}
        aria-busy={busy || undefined}
        onClick={(e) => (phase === 'open' ? close() : open(e.detail === 0))}
        onKeyDown={(e) => {
          if (e.key === 'ArrowDown' && phase !== 'open') {
            e.preventDefault();
            open(true);
          }
        }}
      >
        <Icon name={busy ? 'spinner' : 'more'} />
      </button>
      {phase !== 'closed' &&
        createPortal(
          <div
            ref={menu}
            className="menu"
            role="menu"
            aria-label={label}
            tabIndex={-1}
            data-state={phase}
            style={pos ? { top: pos.top, left: pos.left, maxHeight: pos.maxHeight, transformOrigin: pos.origin } : { top: 0, left: 0, visibility: 'hidden' }}
            onKeyDown={onMenuKey}
          >
            {list.map((item, i) => (
              <div key={item.label + i} role="none">
                {item.danger && i > 0 && !list[i - 1]!.danger && <div className="menu-sep" role="separator" />}
                <button
                  type="button"
                  role="menuitem"
                  className={`menu-item${item.danger ? ' danger' : ''}`}
                  disabled={item.disabled || item.busy}
                  onClick={() => choose(item)}
                >
                  {item.icon || item.busy ? <Icon name={item.busy ? 'spinner' : item.icon!} /> : <span className="menu-gap" />}
                  <span>{item.label}</span>
                </button>
              </div>
            ))}
          </div>,
          document.body,
        )}
    </>
  );
}
