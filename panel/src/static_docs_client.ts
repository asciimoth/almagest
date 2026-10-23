import { siteBaseURL, type DocsClient } from "./docs_client.js";

export type StaticLocale = {
  tree: unknown;
  pages: Record<string, string>;
};

export type StaticCatalog = {
  available: Array<{ locale: string; name: string }>;
  locales: Record<string, StaticLocale>;
};

export class StaticDocsClient implements DocsClient {
  #catalog: Promise<StaticCatalog> | null = null;

  async tree(locale: string): Promise<unknown> {
    const catalog = await this.catalog();
    const [effective, content] = this.locale(catalog, locale);
    return {
      locale: effective,
      available: catalog.available,
      topics: content.tree,
      node_unavailable_message: "This node is not available in this web demo.",
      mark_messages: {
        unavailable: "This node is not available in this web demo.",
        deprecated: "This node is deprecated.",
      },
    };
  }

  async raw(locale: string, id: string): Promise<string> {
    const catalog = await this.catalog();
    const [, content] = this.locale(catalog, locale);
    const path = content.pages[id];
    if (!path) throw new Error("Documentation page was not found");
    const response = await fetch(new URL(path, new URL("docs/", siteBaseURL())));
    if (!response.ok) throw new Error(`${response.status} ${response.statusText}`);
    return await response.text();
  }

  nodeClass(_locale: string, _id: string): Promise<unknown> {
    return Promise.reject(new Error("Node previews require the browser engine"));
  }

  pageFromLocation(location: Location): string {
    return new URLSearchParams(location.search).get("page") || "";
  }

  pageURL(page: string, locale = "", hash = ""): string {
    const query = new URLSearchParams();
    if (page) query.set("page", page);
    if (locale) query.set("locale", locale);
    return `./${query.size > 0 ? `?${query}` : ""}${hash}`;
  }

  private catalog(): Promise<StaticCatalog> {
    this.#catalog ??= fetch(new URL("docs/content/catalog.json", siteBaseURL()))
      .then(async (response) => {
        if (!response.ok) throw new Error(`${response.status} ${response.statusText}`);
        return await response.json() as StaticCatalog;
      });
    return this.#catalog;
  }

  private locale(catalog: StaticCatalog, requested: string): [string, StaticLocale] {
    const effective = catalog.locales[requested] ? requested : "en";
    const content = catalog.locales[effective];
    if (!content) throw new Error("English documentation is not available");
    return [effective, content];
  }
}
