// Shared UI primitives. Kept intentionally small — every page only uses a
// handful of these, and growing a design system here would dwarf the actual
// product work the v1 panel needs.

import React from "react";

export function Button({
  variant = "default",
  className = "",
  children,
  ...rest
}: React.ButtonHTMLAttributes<HTMLButtonElement> & {
  variant?: "default" | "primary" | "danger" | "ghost";
}) {
  const base =
    "inline-flex items-center justify-center gap-1.5 rounded-lg px-3 py-1.5 text-sm font-medium transition disabled:opacity-50 disabled:cursor-not-allowed";
  const styles = {
    default: "bg-white/5 hover:bg-white/10 border border-white/10 text-white",
    primary: "bg-emerald-500/90 hover:bg-emerald-500 text-black",
    danger: "bg-red-500/80 hover:bg-red-500 text-white",
    ghost: "hover:bg-white/5 text-white/70 hover:text-white",
  }[variant];
  return (
    <button className={`${base} ${styles} ${className}`} {...rest}>
      {children}
    </button>
  );
}

export function Field({
  label,
  hint,
  children,
}: {
  label: string;
  hint?: React.ReactNode;
  children: React.ReactNode;
}) {
  return (
    <label className="block">
      <div className="text-xs uppercase tracking-wide text-white/50 mb-1">
        {label}
      </div>
      {children}
      {hint && <div className="text-xs text-white/40 mt-1">{hint}</div>}
    </label>
  );
}

export function Input(
  props: React.InputHTMLAttributes<HTMLInputElement>,
) {
  const { className = "", ...rest } = props;
  return (
    <input
      className={`w-full rounded-lg bg-black/30 border border-white/10 px-3 py-2 text-sm outline-none focus:border-white/30 ${className}`}
      {...rest}
    />
  );
}

export function TextArea(
  props: React.TextareaHTMLAttributes<HTMLTextAreaElement>,
) {
  const { className = "", ...rest } = props;
  return (
    <textarea
      className={`w-full rounded-lg bg-black/30 border border-white/10 px-3 py-2 text-sm font-mono outline-none focus:border-white/30 ${className}`}
      {...rest}
    />
  );
}

export function Select(
  props: React.SelectHTMLAttributes<HTMLSelectElement>,
) {
  const { className = "", ...rest } = props;
  return (
    <select
      className={`w-full rounded-lg bg-black/30 border border-white/10 px-3 py-2 text-sm outline-none focus:border-white/30 ${className}`}
      {...rest}
    />
  );
}

export function Toggle({
  checked,
  onChange,
  label,
  disabled = false,
  pendingLabel,
}: {
  checked: boolean;
  onChange: (v: boolean) => void;
  label?: string;
  // Greys out the slider + label, blocks onClick. Set to true while the
  // caller is awaiting a network response so the user can't double-flip.
  disabled?: boolean;
  // Replaces `label` while disabled — typically "切换中…" to tell the user
  // the request is in-flight. Reverts back to `label` once disabled goes
  // false. No effect when disabled is false.
  pendingLabel?: string;
}) {
  const shownLabel = disabled && pendingLabel ? pendingLabel : label;
  return (
    <button
      type="button"
      onClick={() => {
        if (!disabled) onChange(!checked);
      }}
      disabled={disabled}
      className={`flex items-center gap-2 text-sm ${
        disabled
          ? "text-white/40 cursor-wait"
          : checked
            ? "text-emerald-300"
            : "text-white/60"
      }`}
    >
      <span
        className={`relative h-5 w-9 rounded-full transition ${
          disabled
            ? "bg-white/10"
            : checked
              ? "bg-emerald-500"
              : "bg-white/15"
        }`}
      >
        <span
          className={`absolute top-0.5 h-4 w-4 rounded-full bg-white transition ${
            checked ? "left-4" : "left-0.5"
          } ${disabled ? "opacity-60" : ""}`}
        />
      </span>
      {shownLabel}
    </button>
  );
}

export function Badge({
  tone = "neutral",
  children,
  // dot prepends a status indicator: a coloured dot normally, or a ✓ when
  // `solid` is set — makes on/off states readable at a glance.
  dot = false,
  // size "lg" bumps padding + text for header/status badges; default stays
  // compact so dense table tags aren't enlarged.
  size = "sm",
  // solid = filled "active/on" style: bright solid bg, bold text, ✓ indicator.
  // Use for the enabled half of an on/off pair; leave the off half non-solid.
  solid = false,
}: {
  tone?: "neutral" | "success" | "warn" | "danger" | "info";
  children: React.ReactNode;
  dot?: boolean;
  size?: "sm" | "lg";
  solid?: boolean;
}) {
  // Outline (default) styles: text stays on the -300 shades the light-theme
  // rescue layer (index.css) remaps to dark-on-light; bg tints aren't remapped
  // but read fine on white.
  const outline = {
    neutral: "bg-white/10 text-white/80 border-white/20",
    success: "bg-emerald-500/25 text-emerald-300 border-emerald-400/50",
    warn: "bg-amber-500/25 text-amber-300 border-amber-400/50",
    danger: "bg-red-500/25 text-red-300 border-red-400/50",
    info: "bg-sky-500/25 text-sky-300 border-sky-400/50",
  }[tone];
  // Solid styles: a bright filled pill. text-white is rescued to dark-on-light,
  // so it stays legible on the solid colour in both themes.
  const filled = {
    neutral: "bg-white/80 text-slate-900 border-white/80",
    success: "bg-emerald-500 text-white border-emerald-500",
    warn: "bg-amber-500 text-white border-amber-500",
    danger: "bg-red-500 text-white border-red-500",
    info: "bg-sky-500 text-white border-sky-500",
  }[tone];
  // Solid -500 dots stay vivid on both dark and light (slate for neutral, since
  // a translucent white dot vanishes on a light badge).
  const dotColor = {
    neutral: "bg-slate-400",
    success: "bg-emerald-500",
    warn: "bg-amber-500",
    danger: "bg-red-500",
    info: "bg-sky-500",
  }[tone];
  const sizing =
    size === "lg"
      ? "gap-1.5 px-2.5 py-1 text-xs"
      : "gap-1 px-1.5 py-0.5 text-[11px]";
  const iconSize = size === "lg" ? "h-3 w-3" : "h-2.5 w-2.5";
  return (
    <span
      className={`inline-flex items-center rounded-md border ${solid ? "font-bold" : "font-medium"} ${sizing} ${solid ? filled : outline}`}
    >
      {dot &&
        (solid ? (
          <svg
            viewBox="0 0 12 12"
            className={`${iconSize} shrink-0`}
            fill="none"
            stroke="currentColor"
            strokeWidth="2.5"
            strokeLinecap="round"
            strokeLinejoin="round"
            aria-hidden="true"
          >
            <path d="M2.5 6.4l2.4 2.4 4.6-5.2" />
          </svg>
        ) : (
          <span
            className={`${size === "lg" ? "h-2 w-2" : "h-1.5 w-1.5"} shrink-0 rounded-full ${dotColor}`}
          />
        ))}
      {children}
    </span>
  );
}

export function Modal({
  open,
  onClose,
  title,
  children,
  footer,
  size = "md",
}: {
  open: boolean;
  onClose: () => void;
  title: string;
  children: React.ReactNode;
  footer?: React.ReactNode;
  size?: "md" | "lg" | "xl";
}) {
  if (!open) return null;
  const widths = { md: "max-w-md", lg: "max-w-2xl", xl: "max-w-4xl" }[size];
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center p-4 bg-black/50">
      <div
        className={`w-full ${widths} max-h-[90vh] flex flex-col rounded-2xl border border-white/10 bg-[#10131a]`}
      >
        <div className="flex items-center justify-between px-5 py-3 border-b border-white/10">
          <h3 className="text-sm font-semibold">{title}</h3>
          <button
            onClick={onClose}
            className="text-white/40 hover:text-white text-xl leading-none"
            aria-label="Close"
          >
            ×
          </button>
        </div>
        <div className="overflow-y-auto px-5 py-4 grow">{children}</div>
        {footer && (
          <div className="flex justify-end gap-2 px-5 py-3 border-t border-white/10">
            {footer}
          </div>
        )}
      </div>
    </div>
  );
}

export function ConfirmDialog({
  open,
  title,
  body,
  confirmLabel = "OK",
  cancelLabel = "Cancel",
  variant = "default",
  onConfirm,
  onCancel,
  busy = false,
}: {
  open: boolean;
  title: string;
  body: React.ReactNode;
  confirmLabel?: string;
  cancelLabel?: string;
  variant?: "default" | "danger";
  onConfirm: () => void;
  onCancel: () => void;
  busy?: boolean;
}) {
  return (
    <Modal
      open={open}
      onClose={() => {
        if (!busy) onCancel();
      }}
      title={title}
      footer={
        <>
          <Button variant="ghost" onClick={onCancel} disabled={busy}>
            {cancelLabel}
          </Button>
          <Button
            variant={variant === "danger" ? "danger" : "primary"}
            onClick={onConfirm}
            disabled={busy}
          >
            {confirmLabel}
          </Button>
        </>
      }
    >
      <div className="text-sm text-white/80 whitespace-pre-wrap">{body}</div>
    </Modal>
  );
}

export function Card({
  title,
  action,
  children,
}: {
  title?: string;
  action?: React.ReactNode;
  children: React.ReactNode;
}) {
  return (
    <div className="rounded-2xl border border-white/10 bg-white/[0.03]">
      {(title || action) && (
        <div className="flex items-center justify-between px-5 py-3 border-b border-white/10">
          <h3 className="text-sm font-semibold">{title}</h3>
          {action}
        </div>
      )}
      <div className="p-5">{children}</div>
    </div>
  );
}

export function ErrorText({ children }: { children?: React.ReactNode }) {
  if (!children) return null;
  return <div className="text-sm text-red-400">{String(children)}</div>;
}

// Spinner is a small inline spinning ring for "in progress" feedback next to
// button labels and status lines. Inherits the current text color via
// border-current, so it reads correctly on primary / ghost / dark backgrounds.
export function Spinner({ className = "" }: { className?: string }) {
  return (
    <span
      role="status"
      aria-label="loading"
      className={
        "inline-block h-3.5 w-3.5 animate-spin rounded-full border-2 border-current border-t-transparent align-[-2px] " +
        className
      }
    />
  );
}

export function PageHeader({
  title,
  subtitle,
  action,
}: {
  title: string;
  subtitle?: string;
  action?: React.ReactNode;
}) {
  return (
    <div className="flex items-end justify-between mb-6">
      <div>
        <h1 className="text-xl font-semibold">{title}</h1>
        {subtitle && (
          <p className="text-sm text-white/50 mt-0.5">{subtitle}</p>
        )}
      </div>
      {action}
    </div>
  );
}

// fmt converts a byte count into a short human-readable string. The threshold
// 1024 (binary) is closer to what `ip` / `iftop` show — matching operator
// expectations is more useful than SI prettiness.
export function fmtBytes(n: number): string {
  if (!n) return "0 B";
  const units = ["B", "KiB", "MiB", "GiB", "TiB"];
  let i = 0;
  let v = n;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(v >= 100 || i === 0 ? 0 : 1)} ${units[i]}`;
}

// fmtTime is re-exported from lib/datetime so every consumer (imported from
// "../components/ui") renders in the operator's chosen display timezone.
export { fmtTime } from "../lib/datetime";
