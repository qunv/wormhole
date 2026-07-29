import { useEffect, useId, useRef, useState, type ReactNode } from "react";
import { createPortal } from "react-dom";
import { AlertCircle, Check, CircleAlert, Info, LoaderCircle, X } from "lucide-react";

export function Card({ title, titleHelp, description, actions, children, className = "" }: {
  title?: string;
  titleHelp?: string;
  description?: string;
  actions?: ReactNode;
  children: ReactNode;
  className?: string;
}) {
  return (
    <section className={`panel ${className}`}>
      {(title || actions) && (
        <div className="panel-head">
          <div>
            {title && <div className="panel-title-row"><h2>{title}</h2>{titleHelp && <HelpTip text={titleHelp} />}</div>}
            {description && <p>{description}</p>}
          </div>
          {actions && <div className="panel-actions">{actions}</div>}
        </div>
      )}
      <div className="panel-body">{children}</div>
    </section>
  );
}

export function Field({ label, help, hint, children, wide = false }: { label: string; help?: string; hint?: string; children: ReactNode; wide?: boolean }) {
  return (
    <label className={`field ${wide ? "field-wide" : ""}`}>
      <span className="field-label-row"><span className="field-label">{label}</span>{help && <HelpTip text={help} />}</span>
      {children}
      {hint && <span className="field-hint">{hint}</span>}
    </label>
  );
}

export function TextInput(props: React.InputHTMLAttributes<HTMLInputElement>) {
  return <input className="control" {...props} />;
}

export function TextArea(props: React.TextareaHTMLAttributes<HTMLTextAreaElement>) {
  return <textarea className="control textarea" {...props} />;
}

export function Select(props: React.SelectHTMLAttributes<HTMLSelectElement>) {
  return <select className="control" {...props} />;
}

export function Toggle({ checked, onChange, label, help, description }: {
  checked: boolean;
  onChange: (checked: boolean) => void;
  label: string;
  help?: string;
  description?: string;
}) {
  return (
    <label className="toggle-row">
      <span className="toggle-copy">
        <span className="toggle-title"><strong>{label}</strong>{help && <HelpTip text={help} />}</span>
        {description && <small>{description}</small>}
      </span>
      <button type="button" className={`switch ${checked ? "on" : ""}`} aria-pressed={checked} onClick={() => onChange(!checked)}>
        <span />
      </button>
    </label>
  );
}

export function HelpTip({ text }: { text: string }) {
  const id = useId();
  const anchor = useRef<HTMLSpanElement>(null);
  const [open, setOpen] = useState(false);
  const [position, setPosition] = useState({ top: 0, left: 0, above: false });

  const updatePosition = () => {
    const rect = anchor.current?.getBoundingClientRect();
    if (!rect) return;
    const width = Math.min(288, window.innerWidth - 24);
    const left = Math.min(Math.max(rect.left + rect.width / 2 - width / 2, 12), window.innerWidth - width - 12);
    const above = rect.bottom + 150 > window.innerHeight && rect.top > 150;
    setPosition({ top: above ? rect.top - 8 : rect.bottom + 8, left, above });
  };

  const show = () => {
    updatePosition();
    setOpen(true);
  };

  useEffect(() => {
    if (!open) return;
    const reposition = () => updatePosition();
    window.addEventListener("resize", reposition);
    window.addEventListener("scroll", reposition, true);
    return () => {
      window.removeEventListener("resize", reposition);
      window.removeEventListener("scroll", reposition, true);
    };
  }, [open]);

  return <>
    <span
      ref={anchor}
      className="help-tip"
      role="button"
      tabIndex={0}
      aria-label={`Help: ${text}`}
      aria-describedby={open ? id : undefined}
      onMouseEnter={show}
      onMouseLeave={() => setOpen(false)}
      onFocus={show}
      onBlur={() => setOpen(false)}
      onClick={(event) => { event.preventDefault(); event.stopPropagation(); open ? setOpen(false) : show(); }}
      onKeyDown={(event) => { if (event.key === "Escape") setOpen(false); }}
    >
      <CircleAlert size={13} />
    </span>
    {open && createPortal(
      <span id={id} role="tooltip" className={`help-tooltip ${position.above ? "above" : ""}`} style={{ top: position.top, left: position.left }}>{text}</span>,
      document.body,
    )}
  </>;
}

export function Button({ children, variant = "primary", loading = false, ...props }: React.ButtonHTMLAttributes<HTMLButtonElement> & {
  variant?: "primary" | "secondary" | "danger" | "ghost";
  loading?: boolean;
}) {
  return (
    <button className={`button ${variant}`} {...props} disabled={props.disabled || loading}>
      {loading && <LoaderCircle size={15} className="spin" />}
      {children}
    </button>
  );
}

export function Badge({ children, tone = "neutral" }: { children: ReactNode; tone?: "neutral" | "success" | "warning" | "danger" | "info" }) {
  return <span className={`badge ${tone}`}>{children}</span>;
}

export function Notice({ children, tone = "info" }: { children: ReactNode; tone?: "info" | "success" | "warning" | "danger" }) {
  const Icon = tone === "success" ? Check : tone === "danger" ? X : tone === "warning" ? AlertCircle : Info;
  return <div className={`notice ${tone}`}><Icon size={18} /><div>{children}</div></div>;
}

export function EmptyState({ title, description }: { title: string; description: string }) {
  return <div className="empty"><Info size={28} /><strong>{title}</strong><p>{description}</p></div>;
}

export function PageHeader({ eyebrow, title, description, actions }: { eyebrow?: string; title: string; description: string; actions?: ReactNode }) {
  return (
    <header className="page-header">
      <div>
        {eyebrow && <span className="eyebrow">{eyebrow}</span>}
        <h1>{title}</h1>
        <p>{description}</p>
      </div>
      {actions && <div className="page-actions">{actions}</div>}
    </header>
  );
}

export function LoadingPage() {
  return <div className="loading-page"><LoaderCircle size={30} className="spin" /><span>Loading Codebridge configuration…</span></div>;
}
