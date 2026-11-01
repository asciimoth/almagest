import { siteBaseURL, type DocsClient } from "./docs_client.js";

export class HTTPDocsClient implements DocsClient {
  readonly previewsUnavailableNodes = false;

  async tree(locale: string): Promise<unknown> {
    return await fetchJSON(this.apiURL("tree", locale));
  }

  async raw(locale: string, id: string): Promise<string> {
    const response = await fetch(this.apiURL("raw", locale, { id }), {
      credentials: "same-origin",
      headers: { Accept: "text/markdown" },
    });
    if (!response.ok) throw new Error(`${response.status} ${response.statusText}`);
    return await response.text();
  }

  async nodeClass(locale: string, id: string): Promise<unknown> {
    return await fetchJSON(this.apiURL("node-class", locale, { id }));
  }

  pageFromLocation(location: Location): string {
    const prefix = "/docs/";
    if (!location.pathname.startsWith(prefix)) return "";
    const encoded = location.pathname.slice(prefix.length).replace(/\/$/, "");
    if (!encoded || encoded === "index.html") return "";
    try {
      return encoded.split("/").map(decodeURIComponent).join("/");
    } catch {
      return "";
    }
  }

  pageURL(page: string, locale = "", hash = ""): string {
    const url = new URL("/docs/", window.location.href);
    if (page) url.pathname += page.split("/").map(encodeURIComponent).join("/");
    if (locale) url.searchParams.set("locale", locale);
    return url.pathname + url.search + hash;
  }

  private apiURL(path: string, locale: string, values: Record<string, string> = {}): string {
    const url = new URL(`api/docs/${path}`, siteBaseURL());
    for (const [name, value] of Object.entries(values)) url.searchParams.set(name, value);
    if (locale) url.searchParams.set("locale", locale);
    return url.href;
  }
}

async function fetchJSON(path: string): Promise<unknown> {
  const response = await fetch(path, { credentials: "same-origin", headers: { Accept: "application/json" } });
  if (!response.ok) throw new Error(`${response.status} ${response.statusText}`);
  return await response.json() as unknown;
}
