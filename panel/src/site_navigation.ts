export function documentationURL(page = ""): string {
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
