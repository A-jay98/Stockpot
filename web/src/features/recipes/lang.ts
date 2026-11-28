const RTL = new Set(['ar', 'fa', 'he', 'ps', 'ur', 'yi', 'ckb', 'dv', 'sd', 'ug'])

/** Base subtag of a BCP 47 tag: `fa-IR` → `fa`. */
const base = (lang: string) => lang.toLowerCase().split('-')[0] ?? lang

export const isRtl = (lang: string): boolean => RTL.has(base(lang))

/** The language's own name ("فارسی" for `fa`), falling back to the tag. */
export function nativeName(lang: string): string {
  try {
    const name = new Intl.DisplayNames([lang], { type: 'language' }).of(lang)
    return name && name !== lang ? name : lang
  } catch {
    return lang
  }
}

/** English name of the language ("Persian"), for prose like "Translated from Persian". */
export function englishName(lang: string): string {
  try {
    return new Intl.DisplayNames(['en'], { type: 'language' }).of(lang) ?? lang
  } catch {
    return lang
  }
}
