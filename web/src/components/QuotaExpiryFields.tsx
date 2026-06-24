import { useTranslation } from "react-i18next";
import { Field, Select } from "./ui";

export const GB = 1024 * 1024 * 1024;
export const MB = 1024 * 1024;

export type QuotaUnit = "G" | "M";

// quotaToBytes converts a digit string + unit into a byte count (0 = unlimited).
export function quotaToBytes(value: string, unit: QuotaUnit): number {
  const n = parseInt(value || "0", 10);
  if (!n || n <= 0) return 0;
  return n * (unit === "G" ? GB : MB);
}

// bytesToQuota splits a byte count back into a {value, unit} for editing —
// prefers GB when evenly divisible, else MB.
export function bytesToQuota(bytes: number): { value: string; unit: QuotaUnit } {
  if (!bytes || bytes <= 0) return { value: "", unit: "G" };
  if (bytes % GB === 0) return { value: String(bytes / GB), unit: "G" };
  return { value: String(Math.round(bytes / MB)), unit: "M" };
}

const onlyDigits = (s: string) => s.replace(/[^0-9]/g, "");
const onlyIntMaybeNeg = (s: string) => s.replace(/[^0-9-]/g, "").replace(/(?!^)-/g, "");

// numberInputClass keeps the look identical to <Input> but as a text field so
// the browser shows no up/down spinners and we accept integers only.
const numberInputClass =
  "w-full rounded-lg bg-black/30 border border-white/10 px-3 py-2 text-sm outline-none focus:border-white/30";

// QuotaExpiryFields is the shared quota + expiry row used by multi-user create,
// multi-user edit, and the inbound user edit — so the labels, hints, integer
// hand-entry (no spinners), narrow MB/GB unit, and layout stay identical
// everywhere. Values are digit strings; convert with quotaToBytes.
//
// allowNegativeExpiry + expiryHint let the edit dialogs accept "-1" (test an
// already-passed expiry) and show a "blank = unchanged" hint.
export function QuotaExpiryFields({
  quotaValue,
  setQuotaValue,
  unit,
  setUnit,
  expiryDays,
  setExpiryDays,
  allowNegativeExpiry = false,
  expiryHint,
  expiryPlaceholder,
}: {
  quotaValue: string;
  setQuotaValue: (s: string) => void;
  unit: QuotaUnit;
  setUnit: (u: QuotaUnit) => void;
  expiryDays: string;
  setExpiryDays: (s: string) => void;
  allowNegativeExpiry?: boolean;
  expiryHint?: string;
  expiryPlaceholder?: string;
}) {
  const { t } = useTranslation();
  return (
    <div className="grid grid-cols-2 gap-4">
      <Field label={t("multiUser.fieldQuota")} hint={t("multiUser.quotaHint")}>
        <div className="flex gap-2">
          <input
            type="text"
            inputMode="numeric"
            value={quotaValue}
            onChange={(e) => setQuotaValue(onlyDigits(e.target.value))}
            placeholder="0"
            className={numberInputClass}
          />
          <div className="w-20 shrink-0">
            <Select value={unit} onChange={(e) => setUnit(e.target.value as QuotaUnit)}>
              <option value="G">GB</option>
              <option value="M">MB</option>
            </Select>
          </div>
        </div>
      </Field>
      <Field
        label={t("multiUser.fieldExpiryDays")}
        hint={expiryHint ?? t("multiUser.expiryHint")}
      >
        <input
          type="text"
          inputMode="numeric"
          value={expiryDays}
          onChange={(e) =>
            setExpiryDays(
              allowNegativeExpiry ? onlyIntMaybeNeg(e.target.value) : onlyDigits(e.target.value),
            )
          }
          placeholder={expiryPlaceholder ?? "0"}
          className={numberInputClass}
        />
      </Field>
    </div>
  );
}
