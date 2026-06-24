// CopyButton — wraps the project Button with copy-to-clipboard + a 1.5s
// "Copied ✓" inline state. Works on HTTP panels (where navigator.clipboard
// silently fails) via the lib/clipboard fallback. Pages should use this
// instead of wiring navigator.clipboard.writeText into ad-hoc buttons.

import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Button } from "./ui";
import { copyText } from "../lib/clipboard";

interface Props
  extends Omit<React.ButtonHTMLAttributes<HTMLButtonElement>, "onClick"> {
  text: string | (() => string);
  variant?: "default" | "primary" | "danger" | "ghost";
  label?: React.ReactNode;
  copiedLabel?: React.ReactNode;
  onCopied?: () => void;
}

export default function CopyButton({
  text,
  variant = "ghost",
  label,
  copiedLabel,
  onCopied,
  className,
  ...rest
}: Props) {
  const { t } = useTranslation();
  const [copied, setCopied] = useState(false);
  const resolvedLabel = label ?? t("inbounds.copy");
  const resolvedCopiedLabel = copiedLabel ?? t("inbounds.copied");
  return (
    <Button
      variant={variant}
      className={className}
      {...rest}
      onClick={async () => {
        const value = typeof text === "function" ? text() : text;
        const ok = await copyText(value);
        if (ok) {
          setCopied(true);
          onCopied?.();
          window.setTimeout(() => setCopied(false), 1500);
        }
      }}
    >
      {copied ? resolvedCopiedLabel : resolvedLabel}
    </Button>
  );
}
