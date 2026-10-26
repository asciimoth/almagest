import { panelMode } from "./panel_mode.js";

export function documentationURL(page = ""): string {
  // The desktop package stores documentation in a real subdirectory. Keep the
  // query route so every document is a packaged asset and no dynamic path can
  // be interpreted as an asset name by the native protocol.
  if (panelMode(document.documentElement) === "desktop") {
    const query = new URLSearchParams();
    if (page) query.set("page", page);
    return `./docs/index.html${query.size > 0 ? `?${query}` : ""}`;
  }
  const configured = document.querySelector<HTMLMetaElement>("meta[name='almagest-site-base']")?.content;
  if (configured) {
    const base = configured.replace(/\/?$/, "/");
    const query = new URLSearchParams();
    if (page) query.set("page", page);
    return `${base}docs/${query.size > 0 ? `?${query}` : ""}`;
  }
  const url = new URL("./docs/", window.location.href);
  if (page) url.pathname += page.split("/").map(encodeURIComponent).join("/");
  return url.pathname + url.search;
}
