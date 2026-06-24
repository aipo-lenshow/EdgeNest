// Dev-only structural gate. zh.ts is `as const`, so `typeof zh` has literal
// string leaf types — a plain `: TranslationDict` annotation would reject any
// real translation. DeepStringify widens every leaf to `string` while keeping
// the exact key shape, so `tsc --noEmit` flags any locale that drops, adds, or
// misnests a key relative to zh.ts. Not imported at runtime.

import zh from "./zh";
import en from "./en";
import zhTW from "./zh-TW";
import fa from "./fa";
import ru from "./ru";
import vi from "./vi";

type DeepStringify<T> = T extends string
  ? string
  : T extends readonly (infer U)[]
    ? readonly DeepStringify<U>[]
    : { [K in keyof T]: DeepStringify<T[K]> };

type Dict = DeepStringify<typeof zh>;

// Each assignment fails to compile if that locale's structure drifts from zh.
const _en: Dict = en;
const _zh: Dict = zh;
const _zhTW: Dict = zhTW;
const _fa: Dict = fa;
const _ru: Dict = ru;
const _vi: Dict = vi;

void [_en, _zh, _zhTW, _fa, _ru, _vi];
