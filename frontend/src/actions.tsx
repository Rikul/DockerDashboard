import { createContext, useCallback, useContext, useRef, useState, type ReactNode } from 'react';
import { Button, Modal } from './components/ui';

type ToastTone = 'ok' | 'bad';
interface Toast {
  id: number;
  tone: ToastTone;
  text: string;
}

interface ConfirmOpts {
  title: string;
  body: ReactNode;
  confirmLabel: string;
  danger?: boolean;
}

export interface PromptField {
  name: string;
  label: string;
  placeholder?: string;
  type?: 'text' | 'select' | 'checkbox';
  options?: string[];
  initial?: string;
  required?: boolean;
}

interface PromptOpts {
  title: string;
  body?: ReactNode;
  fields: PromptField[];
  submitLabel: string;
}

interface RunOpts {
  /** Ask first. */
  confirm?: ConfirmOpts;
  /** Shown on success; a function gets the result. */
  success?: string | ((result: unknown) => string);
  /** Called after success, e.g. to refresh lists. */
  after?: () => void | Promise<void>;
  /** Marks the action busy under this key while it runs (see isBusy). */
  key?: string;
}

interface Actions {
  readOnly: boolean;
  /** Run an API call with optional confirmation, a toast on success or failure, and a refresh. */
  run: (fn: () => Promise<unknown>, opts?: RunOpts) => Promise<boolean>;
  prompt: (opts: PromptOpts) => Promise<Record<string, string> | null>;
  toast: (text: string, tone?: ToastTone) => void;
  /** Whether an action run with this key is in flight. */
  isBusy: (key: string) => boolean;
}

const Ctx = createContext<Actions | null>(null);

export function useActions(): Actions {
  const a = useContext(Ctx);
  if (!a) throw new Error('useActions outside ActionsProvider');
  return a;
}

export function ActionsProvider({ readOnly, children }: { readOnly: boolean; children: ReactNode }) {
  const [toasts, setToasts] = useState<Toast[]>([]);
  const [dialog, setDialog] = useState<ReactNode>(null);
  const [busy, setBusy] = useState<Set<string>>(new Set());
  const nextId = useRef(1);

  const toast = useCallback((text: string, tone: ToastTone = 'ok') => {
    const id = nextId.current++;
    setToasts((t) => [...t.slice(-3), { id, tone, text }]);
    window.setTimeout(() => setToasts((t) => t.filter((x) => x.id !== id)), tone === 'bad' ? 9000 : 4000);
  }, []);

  const track = useCallback(<T,>(key: string, p: Promise<T>) => {
    setBusy((b) => new Set(b).add(key));
    return p.finally(() =>
      setBusy((b) => {
        const n = new Set(b);
        n.delete(key);
        return n;
      }),
    );
  }, []);

  const confirm = useCallback(
    (o: ConfirmOpts) =>
      new Promise<boolean>((resolve) => {
        const close = (v: boolean) => {
          setDialog(null);
          resolve(v);
        };
        setDialog(
          <Modal
            title={o.title}
            onClose={() => close(false)}
            footer={
              <>
                <Button onClick={() => close(false)}>Cancel</Button>
                <Button danger={o.danger} primary={!o.danger} onClick={() => close(true)}>
                  {o.confirmLabel}
                </Button>
              </>
            }
          >
            {o.body}
          </Modal>,
        );
      }),
    [],
  );

  const prompt = useCallback(
    (o: PromptOpts) =>
      new Promise<Record<string, string> | null>((resolve) => {
        const close = (v: Record<string, string> | null) => {
          setDialog(null);
          resolve(v);
        };
        setDialog(<PromptDialog opts={o} onDone={close} />);
      }),
    [],
  );

  const run = useCallback(
    async (fn: () => Promise<unknown>, opts: RunOpts = {}) => {
      if (opts.confirm && !(await confirm(opts.confirm))) return false;
      try {
        const result = await (opts.key ? track(opts.key, fn()) : fn());
        if (opts.success) toast(typeof opts.success === 'function' ? opts.success(result) : opts.success);
        await opts.after?.();
        return true;
      } catch (e) {
        toast(e instanceof Error ? e.message : String(e), 'bad');
        return false;
      }
    },
    [confirm, toast, track],
  );

  const isBusy = useCallback((key: string) => busy.has(key), [busy]);

  return (
    <Ctx.Provider value={{ readOnly, run, prompt, toast, isBusy }}>
      {children}
      {dialog}
      <div className="toasts" role="status" aria-live="polite">
        {toasts.map((t) => (
          <div key={t.id} className={`toast ${t.tone}`}>
            <span className={`dot ${t.tone}`} aria-hidden />
            <span>{t.text}</span>
          </div>
        ))}
      </div>
    </Ctx.Provider>
  );
}

function PromptDialog({ opts, onDone }: { opts: PromptOpts; onDone: (v: Record<string, string> | null) => void }) {
  const [values, setValues] = useState<Record<string, string>>(() =>
    Object.fromEntries(opts.fields.map((f) => [f.name, f.initial ?? (f.type === 'select' ? (f.options?.[0] ?? '') : '')])),
  );
  const missing = opts.fields.some((f) => f.required && !values[f.name]?.trim());
  const submit = () => {
    if (!missing) onDone(values);
  };
  return (
    <Modal
      title={opts.title}
      onClose={() => onDone(null)}
      footer={
        <>
          <Button onClick={() => onDone(null)}>Cancel</Button>
          <Button primary disabled={missing} onClick={submit}>
            {opts.submitLabel}
          </Button>
        </>
      }
    >
      {opts.body}
      <div
        className="form"
        onKeyDown={(e) => {
          if (e.key === 'Enter' && (e.target as HTMLElement).tagName === 'INPUT') submit();
        }}
      >
        {opts.fields.map((f, i) => (
          <label key={f.name} className={f.type === 'checkbox' ? 'check' : undefined}>
            {f.type !== 'checkbox' && <span>{f.label}</span>}
            {f.type === 'select' ? (
              <select value={values[f.name]} onChange={(e) => setValues({ ...values, [f.name]: e.target.value })}>
                {f.options?.map((o) => (
                  <option key={o}>{o}</option>
                ))}
              </select>
            ) : f.type === 'checkbox' ? (
              <>
                <input
                  type="checkbox"
                  checked={values[f.name] === 'true'}
                  onChange={(e) => setValues({ ...values, [f.name]: String(e.target.checked) })}
                />
                <span>{f.label}</span>
              </>
            ) : (
              <input
                autoFocus={i === 0}
                value={values[f.name]}
                placeholder={f.placeholder}
                onChange={(e) => setValues({ ...values, [f.name]: e.target.value })}
              />
            )}
          </label>
        ))}
      </div>
    </Modal>
  );
}
