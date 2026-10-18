import { useEffect, useRef, type ReactNode } from 'react';
import type { Tone } from '../format';
import { Icon, type IconName } from './icons';

export function Badge({ tone, children }: { tone: Tone; children: ReactNode }) {
  return (
    <span className="badge">
      <span className={`dot ${tone}`} aria-hidden />
      <span>{children}</span>
    </span>
  );
}

export function Pill({ tone = 'muted', children, title }: { tone?: Tone; children: ReactNode; title?: string }) {
  return (
    <span className={`pill ${tone}`} title={title}>
      {children}
    </span>
  );
}

/** An in-cell bar: frac of the track filled, with a label beside it. */
export function Bar({ frac, label, tone = 'accent' }: { frac: number; label: string; tone?: 'accent' | 'accent2' | 'bad' | 'muted' }) {
  const w = `${Math.max(0, Math.min(1, frac)) * 100}%`;
  return (
    <div className="bar">
      <div className="bar-track">
        <div className={`bar-fill ${tone}`} style={{ width: w }} />
      </div>
      <span className="bar-val">{label}</span>
    </div>
  );
}

export function Card({ id, title, sub, actions, children }: { id?: string; title?: ReactNode; sub?: ReactNode; actions?: ReactNode; children: ReactNode }) {
  return (
    <section className="card" id={id}>
      {(title || actions) && (
        <div className="card-head">
          <div>
            {title && <h2>{title}</h2>}
            {sub && <p className="sub">{sub}</p>}
          </div>
          {actions && <div className="card-actions">{actions}</div>}
        </div>
      )}
      {children}
    </section>
  );
}

export function Kpi({ label, value, sub, alert, onClick }: { label: string; value: ReactNode; sub?: ReactNode; alert?: boolean; onClick?: () => void }) {
  return (
    <button type="button" className={`kpi${alert ? ' alert' : ''}`} onClick={onClick}>
      <span className="kpi-label">{label}</span>
      <span className="kpi-val">{value}</span>
      {sub && <span className="kpi-sub">{sub}</span>}
    </button>
  );
}

export function Button({
  children,
  onClick,
  danger,
  primary,
  disabled,
  title,
}: {
  children: ReactNode;
  onClick?: () => void;
  danger?: boolean;
  primary?: boolean;
  disabled?: boolean;
  title?: string;
}) {
  return (
    <button
      type="button"
      className={`btn${danger ? ' danger' : ''}${primary ? ' primary' : ''}`}
      onClick={onClick}
      disabled={disabled}
      title={title}
    >
      {children}
    </button>
  );
}

/** A compact icon-only button for table rows; label is the tooltip and accessible name. busy disables it and shows a spinner. */
export function IconButton({
  icon,
  label,
  onClick,
  danger,
  busy,
}: {
  icon: IconName;
  label: string;
  onClick?: () => void;
  danger?: boolean;
  busy?: boolean;
}) {
  return (
    <button
      type="button"
      className={`btn icon-only${danger ? ' danger' : ''}`}
      onClick={onClick}
      disabled={busy}
      title={label}
      aria-label={label}
      aria-busy={busy || undefined}
    >
      <Icon name={busy ? 'spinner' : icon} />
    </button>
  );
}

export function Skeleton({ height = 160 }: { height?: number }) {
  return <div className="skeleton" style={{ height }} aria-busy="true" />;
}

export function ErrorLine({ children }: { children: ReactNode }) {
  return (
    <div className="error-line" role="alert">
      <span className="dot bad" aria-hidden /> {children}
    </div>
  );
}

export function Modal({ title, onClose, children, wide, footer }: { title: string; onClose: () => void; children: ReactNode; wide?: boolean; footer?: ReactNode }) {
  const ref = useRef<HTMLDialogElement>(null);
  useEffect(() => {
    const d = ref.current;
    if (d && !d.open) d.showModal();
  }, []);
  return (
    <dialog
      ref={ref}
      className={`modal${wide ? ' wide' : ''}`}
      onCancel={(e) => {
        e.preventDefault();
        onClose();
      }}
      onClick={(e) => {
        if (e.target === ref.current) onClose(); // backdrop click
      }}
    >
      <div className="modal-head">
        <h2>{title}</h2>
        <button type="button" className="icon-btn" onClick={onClose} aria-label="Close">
          ×
        </button>
      </div>
      <div className="modal-body">{children}</div>
      {footer && <div className="modal-foot">{footer}</div>}
    </dialog>
  );
}
