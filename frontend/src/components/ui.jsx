import { useEffect, useId, useRef, useState } from "react";
import {
  AlertCircle,
  ChevronRight,
  LoaderCircle,
  Mouse,
  RefreshCw,
  Search,
  X,
} from "lucide-react";
import { label, tones } from "../utils.js";

export function Brand({ small = false }) {
  return (
    <a
      href="#/dashboard"
      className={`brand ${small ? "small" : ""}`}
      aria-label="HotMouse 首页"
    >
      <span className="brand-mark">
        <img src="/favicon.svg" alt="" aria-hidden="true" />
      </span>
      <span>
        Hot<span className="brand-accent">Mouse</span>
        <small>LOCAL SECURITY WORKBENCH</small>
      </span>
    </a>
  );
}
export function Button({
  children,
  icon: Icon,
  busy = false,
  tone = "",
  className = "",
  disabled,
  ...props
}) {
  return (
    <button
      type="button"
      {...props}
      disabled={disabled || busy}
      className={`button ${tone} ${className}`}
      aria-busy={busy || undefined}
    >
      {busy ? (
        <LoaderCircle size={15} className="spin" />
      ) : (
        Icon && <Icon size={15} />
      )}
      {children}
    </button>
  );
}
export function IconButton({ icon: Icon, label: name, ...props }) {
  return (
    <button
      type="button"
      className="icon-button"
      title={name}
      aria-label={name}
      {...props}
    >
      <Icon size={18} />
    </button>
  );
}
export function Badge({ value, children, tone }) {
  return (
    <span className={`badge ${tone || tones[value] || "muted"}`}>
      {children || label(value)}
    </span>
  );
}
export function PageHeader({ eyebrow, title, description, actions }) {
  return (
    <header className="page-header">
      <div>
        {eyebrow && <div className="eyebrow">{eyebrow}</div>}
        <h1>{title}</h1>
        {description && <p>{description}</p>}
      </div>
      <div className="page-actions">{actions}</div>
    </header>
  );
}
export function Panel({ title, extra, children, className = "" }) {
  return (
    <section className={`panel ${className}`}>
      {title && (
        <header className="panel-header">
          <h2>{title}</h2>
          {extra}
        </header>
      )}
      {children}
    </section>
  );
}
export function Metric({
  title,
  value,
  hint,
  icon: Icon,
  tone = "green",
  href,
}) {
  const content = (
    <>
      <div className="metric-top">
        <span>{title}</span>
        {Icon && <Icon size={18} />}
      </div>
      <strong>{value ?? "—"}</strong>
      <div className="metric-hint">
        {hint || "实时数据"}
        {href && <ChevronRight size={14} />}
      </div>
    </>
  );
  return href ? (
    <a href={href} className={`metric ${tone}`}>
      {content}
    </a>
  ) : (
    <div className={`metric ${tone}`}>{content}</div>
  );
}
export function Empty({
  title = "暂无数据",
  description,
  action,
  icon: Icon = Mouse,
}) {
  return (
    <div className="empty">
      <span>
        <Icon size={27} />
      </span>
      <h3>{title}</h3>
      {description && <p>{description}</p>}
      {action}
    </div>
  );
}
export function Loading({ text = "正在加载…" }) {
  return (
    <div className="loading" role="status">
      <LoaderCircle size={19} className="spin" />
      {text}
    </div>
  );
}
export function ErrorMessage({ error, onRetry }) {
  return (
    error && (
      <div className="error-message" role="alert">
        <AlertCircle size={17} />
        <span>{error.message || String(error)}</span>
        {onRetry && (
          <Button onClick={() => onRetry()} icon={RefreshCw}>
            重试
          </Button>
        )}
      </div>
    )
  );
}
export function ResourceState({ resource, children }) {
  if (!resource.data && resource.loading) return <Loading />;
  if (!resource.data && resource.error)
    return <ErrorMessage error={resource.error} onRetry={resource.reload} />;
  return (
    <>
      <ErrorMessage error={resource.error} onRetry={resource.reload} />
      {resource.data !== null && children}
    </>
  );
}
export function SearchInput({ value, onChange, placeholder = "搜索…" }) {
  return (
    <div className="search-input">
      <Search size={16} />
      <input
        aria-label={placeholder}
        placeholder={placeholder}
        value={value}
        onChange={(event) => onChange(event.target.value)}
      />
      {value && (
        <IconButton icon={X} label="清空搜索" onClick={() => onChange("")} />
      )}
    </div>
  );
}
export function Field({ label: name, hint, children, className = "" }) {
  const id = useId();
  return (
    <div className={`field ${className}`}>
      <label htmlFor={id}>{name}</label>
      {typeof children === "function" ? children(id) : children}
      {hint && <small>{hint}</small>}
    </div>
  );
}
export function TextField({ label: name, hint, multiline = false, ...props }) {
  return (
    <Field label={name} hint={hint}>
      {(id) =>
        multiline ? (
          <textarea id={id} rows={4} {...props} />
        ) : (
          <input id={id} {...props} />
        )
      }
    </Field>
  );
}
export function SelectField({ label: name, hint, children, ...props }) {
  return (
    <Field label={name} hint={hint}>
      {(id) => (
        <select id={id} {...props}>
          {children}
        </select>
      )}
    </Field>
  );
}
export function FactPicker({
  facts,
  value,
  onChange,
  label: name = "依据事实",
  required = true,
}) {
  return (
    <fieldset className="fact-picker">
      <legend>
        {name}
        {required && " *"}
      </legend>
      <div>
        {facts.map((fact) => (
          <label key={fact.id}>
            <input
              type="checkbox"
              checked={value.includes(fact.id)}
              onChange={(event) =>
                onChange(
                  event.target.checked
                    ? [...value, fact.id]
                    : value.filter((id) => id !== fact.id),
                )
              }
            />
            <code>{fact.id}</code>
            <span>{fact.description}</span>
          </label>
        ))}
      </div>
      {!value.length && required && <small>请至少选择一条事实作为依据。</small>}
    </fieldset>
  );
}
export function Modal({
  title,
  description,
  children,
  onClose,
  wide = false,
  busy = false,
}) {
  const ref = useRef(null);
  const titleID = useId();
  const close = useRef(onClose);
  close.current = onClose;
  const busyRef = useRef(busy);
  busyRef.current = busy;
  useEffect(() => {
    const previous = document.activeElement;
    const original = document.body.style.overflow;
    document.body.style.overflow = "hidden";
    const controls = () =>
      [
        ...ref.current.querySelectorAll(
          'button:not(:disabled), input:not(:disabled), select:not(:disabled), textarea:not(:disabled), a[href], [tabindex="0"]',
        ),
      ].filter((el) => el.getClientRects().length);
    (ref.current.querySelector("[autofocus]") || controls()[0])?.focus();
    const keyboard = (event) => {
      if (event.key === "Escape" && !busyRef.current) {
        event.stopPropagation();
        close.current();
      }
      if (event.key === "Tab") {
        const items = controls();
        if (!items.length) {
          event.preventDefault();
          return;
        }
        if (event.shiftKey && document.activeElement === items[0]) {
          event.preventDefault();
          items.at(-1).focus();
        } else if (!event.shiftKey && document.activeElement === items.at(-1)) {
          event.preventDefault();
          items[0].focus();
        }
      }
    };
    ref.current.addEventListener("keydown", keyboard);
    const element = ref.current;
    return () => {
      element.removeEventListener("keydown", keyboard);
      document.body.style.overflow = original;
      previous?.focus();
    };
  }, []);
  return (
    <div
      className="modal-backdrop"
      onMouseDown={(event) => {
        if (event.target === event.currentTarget && !busy) onClose();
      }}
    >
      <section
        ref={ref}
        className={`modal ${wide ? "wide" : ""}`}
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleID}
      >
        <header className="modal-header">
          <div>
            <h2 id={titleID}>{title}</h2>
            {description && <p>{description}</p>}
          </div>
          <IconButton icon={X} label="关闭" onClick={onClose} disabled={busy} />
        </header>
        {children}
      </section>
    </div>
  );
}
export function FormModal({
  title,
  description,
  children,
  onClose,
  onSubmit,
  submitLabel = "保存",
  disabled = false,
  wide = false,
  tone = "primary",
}) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState(null);
  const submit = async (event) => {
    event.preventDefault();
    setBusy(true);
    setError(null);
    try {
      await onSubmit();
      onClose();
    } catch (failure) {
      setError(failure);
    } finally {
      setBusy(false);
    }
  };
  return (
    <Modal
      title={title}
      description={description}
      wide={wide}
      busy={busy}
      onClose={onClose}
    >
      <form onSubmit={submit}>
        <div className="modal-body">
          <ErrorMessage error={error} />
          {children}
        </div>
        <footer className="modal-footer">
          <Button onClick={onClose} disabled={busy}>
            取消
          </Button>
          <Button type="submit" tone={tone} busy={busy} disabled={disabled}>
            {submitLabel}
          </Button>
        </footer>
      </form>
    </Modal>
  );
}
export function ConfirmModal({
  title,
  message,
  onConfirm,
  onClose,
  confirmLabel = "确认",
  danger = true,
}) {
  return (
    <FormModal
      title={title}
      onClose={onClose}
      onSubmit={onConfirm}
      submitLabel={confirmLabel}
      tone={danger ? "danger" : "primary"}
    >
      <p className="confirm-message">{message}</p>
    </FormModal>
  );
}
export function DataTable({ columns, children, empty, count }) {
  return (
    <div className="table-scroll">
      <table>
        <thead>
          <tr>
            {columns.map((column, index) => (
              <th key={index}>{column}</th>
            ))}
          </tr>
        </thead>
        <tbody>{children}</tbody>
      </table>
      {count === 0 && <Empty title={empty || "没有匹配的记录"} />}
    </div>
  );
}
