export type DesktopSurface = "panel" | "docs";

const SURFACE_KEY = "almagest.desktop.surface.v1";
const PANEL_TAB_KEY = "almagest.desktop.panel-tab.v1";
const DOCS_PAGE_KEY = "almagest.desktop.docs-page.v1";
const DOCS_SCROLL_KEY = "almagest.desktop.docs-scroll.v1";
const CACHED_DOCS_KEY = "almagest.desktop.cached-docs.v1";

/** Persist only small route identifiers. No document or API data is stored. */
export function rememberDesktopSurface(storage: Storage, surface: DesktopSurface): void {
  write(storage, SURFACE_KEY, surface);
}

export function rememberedDesktopSurface(storage: Storage): DesktopSurface {
  return read(storage, SURFACE_KEY) === "docs" ? "docs" : "panel";
}

export function rememberDesktopPanelTab(storage: Storage, tab: string): void {
  if (validRoutePart(tab)) write(storage, PANEL_TAB_KEY, tab);
}

export function rememberedDesktopPanelTab(storage: Storage): string {
  const value = read(storage, PANEL_TAB_KEY);
  return value && validRoutePart(value) ? value : "";
}

export function rememberDesktopDocsPage(storage: Storage, page: string): void {
  if (validRoutePart(page)) write(storage, DOCS_PAGE_KEY, page);
}

export function rememberedDesktopDocsPage(storage: Storage): string {
  const value = read(storage, DOCS_PAGE_KEY);
  return value && validRoutePart(value) ? value : "";
}

/** Store the scroll position only for the last visible documentation page. */
export function rememberDesktopDocsScroll(storage: Storage, page: string, top: number): void {
  if (!validRoutePart(page) || !Number.isFinite(top) || top < 0 || top > Number.MAX_SAFE_INTEGER) return;
  write(storage, DOCS_SCROLL_KEY, JSON.stringify({ page, top: Math.round(top) }));
}

export function rememberedDesktopDocsScroll(storage: Storage, page: string): number | null {
  try {
    const value: unknown = JSON.parse(read(storage, DOCS_SCROLL_KEY) ?? "null");
    if (!value || typeof value !== "object" || Array.isArray(value)) return null;
    const record = value as Record<string, unknown>;
    if (record.page !== page || typeof record.top !== "number") return null;
    return Number.isSafeInteger(record.top) && record.top >= 0 ? record.top : null;
  } catch {
    return null;
  }
}

/**
 * Reuse the two desktop documents after their first navigation. Chromium keeps
 * both documents in its back-forward cache, including the parsed modules and
 * rendered DOM. Other desktop engines can reject the cache and transparently
 * fall back to a normal history navigation.
 */
export function navigateDesktopSurface(
  storage: Storage,
  history: Pick<History, "back" | "forward" | "length">,
  location: Pick<Location, "assign" | "replace">,
  from: DesktopSurface,
  to: DesktopSurface,
  url: string,
  reuseCache: boolean,
): void {
  if (!reuseCache) {
    location.replace(url);
    return;
  }
  if (from === "panel" && to === "docs") {
    if (read(storage, CACHED_DOCS_KEY) === "forward") {
      history.forward();
    } else {
      write(storage, CACHED_DOCS_KEY, "forward");
      location.assign(url);
    }
    return;
  }
  if (from === "docs" && to === "panel" && read(storage, CACHED_DOCS_KEY) === "forward" && history.length > 1) {
    history.back();
    return;
  }
  location.replace(url);
}

function validRoutePart(value: string): boolean {
  return value.length > 0 && value.length <= 1024 && !/[\u0000-\u001f\u007f]/.test(value);
}

function read(storage: Storage, key: string): string | null {
  try {
    return storage.getItem(key);
  } catch {
    return null;
  }
}

function write(storage: Storage, key: string, value: string): void {
  try {
    storage.setItem(key, value);
  } catch {
    // Route persistence is optional when the webview disables storage.
  }
}
