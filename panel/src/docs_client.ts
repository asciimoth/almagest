export interface DocsClient {
  readonly previewsUnavailableNodes: boolean;
  tree(locale: string): Promise<unknown>;
  raw(locale: string, id: string): Promise<string>;
  nodeClass(locale: string, id: string): Promise<unknown>;
  pageFromLocation(location: Location): string;
  pageURL(page: string, locale?: string, hash?: string): string;
}

export function siteBaseURL(): URL {
  const configured = document.querySelector<HTMLMetaElement>("meta[name='almagest-site-base']")?.content;
  return new URL(configured || "../../", import.meta.url);
}

let client: DocsClient | null = null;

export function configureDocsClient(value: DocsClient): void {
  client = value;
}

export function docsClient(): DocsClient {
  if (!client) throw new Error("Documentation client was not configured");
  return client;
}
