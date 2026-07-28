import type { ReactNode } from "react";
import { AlertCircle, Check, Info, LoaderCircle, X } from "lucide-react";

export function Card({ title, description, actions, children, className = "" }: {
  title?: string;
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
            {title && <h2>{title}</h2>}
            {description && <p>{description}</p>}
          </div>
          {actions && <div className="panel-actions">{actions}</div>}
        </div>
      )}
      <div className="panel-body">{children}</div>
    </section>
  );
}

export function Field({ label, hint, children, wide = false }: { label: string; hint?: string; children: ReactNode; wide?: boolean }) {
  return (
    <label className={`field ${wide ? "field-wide" : ""}`}>
      <span className="field-label">{label}</span>
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

export function Toggle({ checked, onChange, label, description }: {
  checked: boolean;
  onChange: (checked: boolean) => void;
  label: string;
  description?: string;
}) {
  return (
    <label className="toggle-row">
      <span>
        <strong>{label}</strong>
        {description && <small>{description}</small>}
      </span>
      <button type="button" className={`switch ${checked ? "on" : ""}`} aria-pressed={checked} onClick={() => onChange(!checked)}>
        <span />
      </button>
    </label>
  );
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
