export type AvailableLocale = { locale: string; name: string };

export type LocaleSnapshot = {
  requested: string;
  effective: string;
  available: AvailableLocale[];
  panel: Record<string, string>;
  revision: number;
};

export const LOCALE_COOKIE_NAME = "almagest_locale";
const LOCALE_COOKIE_MAX_AGE = 365 * 24 * 60 * 60;

export function readLocaleCookie(source: Pick<Document, "cookie"> = document): string {
  const prefix = `${LOCALE_COOKIE_NAME}=`;
  for (const part of source.cookie.split(";")) {
    const value = part.trim();
    if (!value.startsWith(prefix)) continue;
    try {
      return decodeURIComponent(value.slice(prefix.length));
    } catch {
      return "";
    }
  }
  return "";
}

export function writeLocaleCookie(locale: string, target: Pick<Document, "cookie" | "location"> = document): void {
  if (!locale) return;
  const secure = target.location.protocol === "https:" ? "; Secure" : "";
  target.cookie = `${LOCALE_COOKIE_NAME}=${encodeURIComponent(locale)}; Path=/; Max-Age=${LOCALE_COOKIE_MAX_AGE}; SameSite=Lax${secure}`;
}

export function matchPreferredLocale(available: AvailableLocale[], preferred: readonly string[]): string | null {
  const localeByTag = new Map(available.map(({ locale }) => [locale.toLowerCase(), locale]));
  for (const preference of preferred) {
    let tag = preference.trim().replaceAll("_", "-");
    while (tag) {
      const match = localeByTag.get(tag.toLowerCase());
      if (match) return match;
      const separator = tag.lastIndexOf("-");
      if (separator < 0) break;
      tag = tag.slice(0, separator);
    }
  }
  return null;
}

let catalog: Record<string, string> = {};

export function text(key: string, builtin: string, parameters: Record<string, string | number> = {}): string {
  const template = catalog[key] || builtin;
  return template.replace(/\{([A-Za-z0-9_.-]+)\}/g, (match, name: string) => {
    const value = parameters[name];
    return value === undefined ? match : String(value);
  });
}

export function popupText(value: string): string {
  if (!value.startsWith("@") || value.length === 1) return value;
  return text(`popup.${value.slice(1)}`, value);
}

export function applyLocale(snapshot: LocaleSnapshot, root: ParentNode = document): void {
  catalog = { ...snapshot.panel };
	const localeDocument = root.nodeType === 9 ? root as Document : root.ownerDocument;
	if (localeDocument) localeDocument.documentElement.lang = snapshot.effective;
  for (const element of root.querySelectorAll<HTMLElement>("[data-i18n]")) {
    const key = element.dataset.i18n;
    if (!key) continue;
    const builtin = element.dataset.i18nBuiltin ?? element.textContent ?? "";
    element.dataset.i18nBuiltin = builtin;
    element.textContent = text(key, builtin);
  }
	for (const attribute of ["aria-label", "placeholder", "title"] as const) {
		const keyAttribute = `data-i18n-${attribute}`;
		const builtinAttribute = `${keyAttribute}-builtin`;
		for (const element of root.querySelectorAll<HTMLElement>(`[${keyAttribute}]`)) {
			const key = element.getAttribute(keyAttribute);
			if (!key) continue;
			const builtin = element.getAttribute(builtinAttribute) ?? element.getAttribute(attribute) ?? "";
			element.setAttribute(builtinAttribute, builtin);
			element.setAttribute(attribute, text(key, builtin));
		}
	}
}

export function parseLocaleSnapshot(input: unknown): LocaleSnapshot | null {
  if (!record(input) || typeof input.requested !== "string" || typeof input.effective !== "string"
    || !Array.isArray(input.available) || !record(input.panel) || !Number.isSafeInteger(input.revision)) return null;
  const available: AvailableLocale[] = [];
  for (const value of input.available) {
    if (!record(value) || typeof value.locale !== "string" || typeof value.name !== "string") return null;
    available.push({ locale: value.locale, name: value.name });
  }
  const panel: Record<string, string> = {};
  for (const [key, value] of Object.entries(input.panel)) {
    if (typeof value !== "string") return null;
    panel[key] = value;
  }
  return { requested: input.requested, effective: input.effective, available, panel, revision: Number(input.revision) };
}

function record(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}
