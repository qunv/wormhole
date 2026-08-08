import { useEffect, useId, useMemo, useRef, useState, type ReactNode } from "react";
import { createPortal } from "react-dom";
import { AlertCircle, Check, ChevronDown, CircleAlert, Info, LoaderCircle, Search, X } from "lucide-react";

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

export interface MultiSelectOption {
  value: string;
  label: string;
  description?: string;
}

export function MultiSelect({ options, value, onChange, placeholder = "Select values", searchPlaceholder = "Search…" }: {
  options: MultiSelectOption[];
  value: string[];
  onChange: (value: string[]) => void;
  placeholder?: string;
  searchPlaceholder?: string;
}) {
  const anchor = useRef<HTMLButtonElement>(null);
  const dropdown = useRef<HTMLDivElement>(null);
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [position, setPosition] = useState({ top: 0, left: 0, width: 360, above: false });

  const allOptions = useMemo(() => {
    const merged = new Map<string, MultiSelectOption>();
    for (const selected of value) {
      merged.set(selected, { value: selected, label: selected, description: "Configured value not present in the current catalog" });
    }
    for (const option of options) merged.set(option.value, option);
    return [...merged.values()].sort((left, right) => left.label.localeCompare(right.label));
  }, [options, value]);
  const optionByValue = useMemo(() => new Map(allOptions.map((option) => [option.value, option])), [allOptions]);
  const selected = useMemo(() => new Set(value), [value]);
  const normalizedQuery = query.trim().toLowerCase();
  const filtered = allOptions.filter((option) => !normalizedQuery || `${option.label} ${option.value} ${option.description ?? ""}`.toLowerCase().includes(normalizedQuery));

  const updatePosition = () => {
    const rect = anchor.current?.getBoundingClientRect();
    if (!rect) return;
    const width = Math.min(Math.max(rect.width, 360), window.innerWidth - 24);
    const left = Math.min(Math.max(rect.left, 12), window.innerWidth - width - 12);
    const estimatedHeight = Math.min(430, 116 + allOptions.length * 48);
    const above = rect.bottom + estimatedHeight > window.innerHeight && rect.top > estimatedHeight;
    setPosition({ top: above ? rect.top - 8 : rect.bottom + 8, left, width, above });
  };

  const close = () => {
    setOpen(false);
    setQuery("");
  };

  useEffect(() => {
    if (!open) return;
    updatePosition();
    const reposition = () => updatePosition();
    const closeOnOutside = (event: PointerEvent) => {
      const target = event.target as Node;
      if (!anchor.current?.contains(target) && !dropdown.current?.contains(target)) close();
    };
    window.addEventListener("resize", reposition);
    window.addEventListener("scroll", reposition, true);
    document.addEventListener("pointerdown", closeOnOutside);
    return () => {
      window.removeEventListener("resize", reposition);
      window.removeEventListener("scroll", reposition, true);
      document.removeEventListener("pointerdown", closeOnOutside);
    };
  }, [open, allOptions.length]);

  const toggle = (option: string) => {
    onChange(selected.has(option) ? value.filter((item) => item !== option) : [...value, option].sort());
  };

  return <div className="multi-select">
    <button ref={anchor} type="button" className="control multi-select-trigger" aria-haspopup="listbox" aria-expanded={open} onClick={() => open ? close() : setOpen(true)}>
      <span className={value.length ? "" : "placeholder"}>{value.length ? `${value.length} selected` : placeholder}</span>
      <ChevronDown size={15} className={open ? "open" : ""} />
    </button>
    {!!value.length && <div className="multi-select-chips">
      {value.map((item) => <span className="multi-select-chip" key={item}>
        <span>{optionByValue.get(item)?.label ?? item}</span>
        <button type="button" aria-label={`Remove ${item}`} onClick={() => onChange(value.filter((entry) => entry !== item))}><X size={11} /></button>
      </span>)}
    </div>}
    {open && createPortal(
      <div ref={dropdown} className={`multi-select-dropdown ${position.above ? "above" : ""}`} style={{ top: position.top, left: position.left, width: position.width }} onKeyDown={(event) => { if (event.key === "Escape") close(); }}>
        <div className="multi-select-search"><Search size={14} /><input autoFocus value={query} onChange={(event) => setQuery(event.target.value)} placeholder={searchPlaceholder} /></div>
        <div className="multi-select-options" role="listbox" aria-multiselectable="true">
          {filtered.map((option) => {
            const active = selected.has(option.value);
            return <button key={option.value} type="button" role="option" aria-selected={active} className={active ? "selected" : ""} onClick={() => toggle(option.value)}>
              <span className="multi-select-check">{active && <Check size={13} />}</span>
              <span className="multi-select-option-copy"><strong>{option.label}</strong>{option.description && <small>{option.description}</small>}</span>
            </button>;
          })}
          {!filtered.length && <div className="multi-select-empty">No matching values.</div>}
        </div>
        <div className="multi-select-footer"><span>{value.length} selected</span><button type="button" disabled={!value.length} onClick={() => onChange([])}>Clear all</button></div>
      </div>,
      document.body,
    )}
  </div>;
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
  return <div className="loading-page"><LoaderCircle size={30} className="spin" /><span>Loading Wormhole configuration…</span></div>;
}
