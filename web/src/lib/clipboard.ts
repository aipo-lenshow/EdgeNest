// copyText copies a string to the clipboard, working on both HTTPS (secure
// context, where navigator.clipboard.writeText is available) and HTTP (where
// browsers disable the async Clipboard API and the textarea + execCommand
// fallback is the only thing left).
//
// EdgeNest panels are commonly served over plain HTTP on a VPS IP, so the
// async API path silently fails on every copy button. Always go through this
// helper instead of touching navigator.clipboard directly.
export async function copyText(text: string): Promise<boolean> {
  if (
    typeof navigator !== "undefined" &&
    navigator.clipboard &&
    typeof navigator.clipboard.writeText === "function" &&
    typeof window !== "undefined" &&
    window.isSecureContext
  ) {
    try {
      await navigator.clipboard.writeText(text);
      return true;
    } catch {
      // fall through to legacy
    }
  }
  if (typeof document === "undefined") return false;
  const ta = document.createElement("textarea");
  ta.value = text;
  ta.setAttribute("readonly", "");
  ta.style.position = "fixed";
  ta.style.top = "0";
  ta.style.left = "0";
  ta.style.opacity = "0";
  document.body.appendChild(ta);
  ta.focus();
  ta.select();
  let ok = false;
  try {
    ok = document.execCommand("copy");
  } catch {
    ok = false;
  }
  document.body.removeChild(ta);
  return ok;
}
