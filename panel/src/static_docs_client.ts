import { siteBaseURL, type DocsClient } from "./docs_client.js";

export type StaticLocale = {
  tree: unknown;
  pages: Record<string, string>;
};

export type StaticCatalog = {
  available: Array<{ locale: string; name: string }>;
  locales: Record<string, StaticLocale>;
};

type StaticNodeClassCache = {
  version: number;
  order: string[];
  colormap: unknown;
  classes: Record<string, { primary_type: unknown; initial_ports: unknown }>;
};

export class StaticDocsClient implements DocsClient {
  #catalog: Promise<StaticCatalog> | null = null;
  #nodeClasses: Promise<StaticNodeClassCache> | null = null;

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

  async nodeClass(_locale: string, id: string): Promise<unknown> {
    if (!id.startsWith("node/")) throw new Error("Node documentation page was not specified");
    const className = id.slice("node/".length);
    const cache = await this.nodeClasses();
    if (!className || !Object.hasOwn(cache.classes, className)) throw new Error("Node class was not found");
    const cls = cache.classes[className];
    return {
      snapshot: {
        class: className,
        short_description: "",
        unique: false,
        primary_type: cls?.primary_type,
        initial_ports: cls?.initial_ports,
        experimental: false,
      },
      colormap: cache.colormap,
    };
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

  private nodeClasses(): Promise<StaticNodeClassCache> {
    this.#nodeClasses ??= fetch(new URL("docs/content/node-classes.json", siteBaseURL()))
      .then(async (response) => {
        if (!response.ok) throw new Error(`${response.status} ${response.statusText}`);
        const value = await response.json() as unknown;
        if (!isNodeClassCache(value)) throw new Error("Invalid node class cache");
        return value;
      });
    return this.#nodeClasses;
  }

  private locale(catalog: StaticCatalog, requested: string): [string, StaticLocale] {
    const effective = catalog.locales[requested] ? requested : "en";
    const content = catalog.locales[effective];
    if (!content) throw new Error("English documentation is not available");
    return [effective, content];
  }
}

function isNodeClassCache(input: unknown): input is StaticNodeClassCache {
  if (!input || typeof input !== "object" || Array.isArray(input)) return false;
  const value = input as Record<string, unknown>;
  return value.version === 1
    && Array.isArray(value.order) && value.order.every((className) => typeof className === "string")
    && !!value.colormap && typeof value.colormap === "object" && !Array.isArray(value.colormap)
    && !!value.classes && typeof value.classes === "object" && !Array.isArray(value.classes);
}
