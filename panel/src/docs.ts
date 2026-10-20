import { renderNodeDescription, textElement } from "./safe_dom.js";
import { readLocaleCookie, writeLocaleCookie } from "./localization.js";

type PageSummary = { id: string; name: string; folded: boolean; order?: number; default?: boolean };
type Topic = { id: string; name: string; folded: boolean; order?: number; topics: Topic[]; pages: PageSummary[] };
type AvailableLocale = { locale: string; name: string };
type TreeResponse = { locale: string; available: AvailableLocale[]; topics: Topic[] };

const localeStorageKey = "almagest.panel.locale.v1";
const topicFoldStorageKey = "almagest.docs.topic-folds.v1";
const treeElement = document.querySelector<HTMLElement>("#docs-tree")!;
const pageElement = document.querySelector<HTMLElement>("#docs-page")!;
const localeSelect = document.querySelector<HTMLSelectElement>("#docs-locale-select")!;
const parameters = new URLSearchParams(window.location.search);
let urlLocale = parameters.get("locale") || "";
const requestedLocale = urlLocale || readLocaleCookie() || savedLocale() || navigator.language || "en";
let currentPage = parameters.get("page") || pageIDFromPath(window.location.pathname);
let effectiveLocale = requestedLocale;
const pageNames = new Map<string, string>();
const topicFolds = savedTopicFolds();

function savedLocale(): string {
  try {
    return window.localStorage.getItem(localeStorageKey) || "";
  } catch {
    return "";
  }
}

function savedTopicFolds(): Record<string, boolean> {
  try {
    const value: unknown = JSON.parse(window.localStorage.getItem(topicFoldStorageKey) || "{}");
    if (!value || typeof value !== "object" || Array.isArray(value)) return {};
    return Object.fromEntries(Object.entries(value).filter((entry): entry is [string, boolean] => typeof entry[1] === "boolean"));
  } catch {
    return {};
  }
}

function saveTopicFold(id: string, open: boolean): void {
  topicFolds[id] = open;
  try {
    window.localStorage.setItem(topicFoldStorageKey, JSON.stringify(topicFolds));
  } catch {
    // Documentation remains usable when browser storage is not available.
  }
}

function saveLocale(locale: string): void {
  writeLocaleCookie(locale);
  try {
    window.localStorage.setItem(localeStorageKey, locale);
  } catch {
    // Documentation remains usable when browser storage is not available.
  }
}

async function fetchJSON<T>(path: string): Promise<T> {
  const response = await fetch(path, { credentials: "same-origin", headers: { Accept: "application/json" } });
  if (!response.ok) throw new Error(`${response.status} ${response.statusText}`);
  return await response.json() as T;
}

async function fetchText(path: string): Promise<string> {
  const response = await fetch(path, { credentials: "same-origin", headers: { Accept: "text/markdown" } });
  if (!response.ok) throw new Error(`${response.status} ${response.statusText}`);
  return await response.text();
}

function pageIDs(topics: readonly Topic[]): string[] {
  return topics.flatMap((topic) => [
    ...topic.pages.map((page) => page.id),
    ...pageIDs(topic.topics),
  ]);
}

function defaultPageID(topics: readonly Topic[]): string {
  for (const topic of topics) {
    const page = topic.pages.find((value) => value.default);
    if (page) return page.id;
    const nested = defaultPageID(topic.topics);
    if (nested) return nested;
  }
  return "";
}

function renderTopics(topics: readonly Topic[]): DocumentFragment {
  const fragment = document.createDocumentFragment();
  for (const topic of topics) {
    const details = document.createElement("details");
    details.dataset.topicId = topic.id;
    const savedOpen = topicFolds[topic.id];
    details.open = savedOpen ?? (!topic.folded || topicContainsPage(topic, currentPage));
    let userToggle = false;
    const summary = textElement("summary", topic.name);
    summary.addEventListener("click", () => { userToggle = true; });
    details.addEventListener("toggle", () => {
      if (userToggle) saveTopicFold(topic.id, details.open);
      userToggle = false;
    });
    details.append(summary);
    const children = document.createElement("div");
    children.className = "docs-tree-children";
    for (const page of topic.pages) {
      const link = textElement("a", page.name);
      link.href = docsURL(page.id);
      link.dataset.pageId = page.id;
      link.classList.toggle("active", page.id === currentPage);
      link.addEventListener("click", (event) => {
        event.preventDefault();
        void openPage(page.id, true);
      });
      children.append(link);
    }
    children.append(renderTopics(topic.topics));
    details.append(children);
    fragment.append(details);
  }
  return fragment;
}

function topicContainsPage(topic: Topic, id: string): boolean {
  return topic.pages.some((page) => page.id === id) || topic.topics.some((child) => topicContainsPage(child, id));
}

function pageIDFromPath(pathname: string): string {
  const prefix = "/docs/";
  if (!pathname.startsWith(prefix)) return "";
  const encoded = pathname.slice(prefix.length).replace(/\/$/, "");
  if (!encoded || encoded === "index.html") return "";
  try {
    return encoded.split("/").map(decodeURIComponent).join("/");
  } catch {
    return "";
  }
}

function docsURL(page: string): string {
  const url = new URL("/docs/", window.location.href);
  if (page) url.pathname += page.split("/").map(encodeURIComponent).join("/");
  if (urlLocale) url.searchParams.set("locale", urlLocale);
  return url.pathname + url.search;
}

function docsAPIURL(path: string, values: Record<string, string> = {}): string {
  const query = new URLSearchParams(values);
  if (urlLocale) query.set("locale", urlLocale);
  const suffix = query.size > 0 ? `?${query}` : "";
  return `/api/docs/${path}${suffix}`;
}

async function openPage(id: string, updateHistory: boolean): Promise<void> {
  pageElement.replaceChildren(textElement("p", "Loading documentation."));
  try {
    const contentText = await fetchText(docsAPIURL("raw", { id }));
    const name = pageNames.get(id) || id;
    currentPage = id;
    document.title = `${name} — Almagest Documentation`;
    const title = textElement("h1", name);
    const content = document.createElement("div");
    renderNodeDescription(content, contentText);
    pageElement.replaceChildren(title, content);
    for (const link of treeElement.querySelectorAll<HTMLElement>("[data-page-id]")) {
      link.classList.toggle("active", link.dataset.pageId === id);
    }
    if (updateHistory) window.history.pushState({ page: id }, "", docsURL(id));
  } catch (error) {
    pageElement.replaceChildren(
      textElement("h1", "Documentation unavailable"),
      textElement("p", error instanceof Error ? error.message : String(error)),
    );
  }
}

function renderLocaleOptions(available: readonly AvailableLocale[], selected: string): void {
  localeSelect.replaceChildren(...available.map(({ locale, name }) => {
    const option = textElement("option", name) as HTMLOptionElement;
    option.value = locale;
    option.lang = locale;
    return option;
  }));
  localeSelect.value = selected;
  localeSelect.disabled = available.length < 2;
}

async function loadTree(locale: string): Promise<void> {
  localeSelect.disabled = true;
  try {
    if (!urlLocale) writeLocaleCookie(locale);
    const tree = await fetchJSON<TreeResponse>(docsAPIURL("tree"));
    effectiveLocale = tree.locale;
    saveLocale(tree.locale);
    document.documentElement.lang = tree.locale;
    renderLocaleOptions(tree.available, tree.locale);
    const ids = pageIDs(tree.topics);
    pageNames.clear();
    collectPageNames(tree.topics);
    if (!ids.includes(currentPage)) currentPage = defaultPageID(tree.topics) || ids[0] || "";
    treeElement.replaceChildren(renderTopics(tree.topics));
    window.history.replaceState({ page: currentPage }, "", docsURL(currentPage));
    if (currentPage) await openPage(currentPage, false);
    else pageElement.replaceChildren(textElement("p", "No documentation pages are available."));
  } catch (error) {
    treeElement.replaceChildren(textElement("p", "Documentation tree unavailable."));
    pageElement.replaceChildren(textElement("p", error instanceof Error ? error.message : String(error)));
  }
}

localeSelect.addEventListener("change", () => {
  const locale = localeSelect.value;
  urlLocale = "";
  saveLocale(locale);
  void loadTree(locale);
});

function collectPageNames(topics: readonly Topic[]): void {
  for (const topic of topics) {
    for (const page of topic.pages) pageNames.set(page.id, page.name);
    collectPageNames(topic.topics);
  }
}

window.addEventListener("popstate", () => {
  const query = new URLSearchParams(window.location.search);
  const id = query.get("page") || pageIDFromPath(window.location.pathname);
  urlLocale = query.get("locale") || "";
  const locale = urlLocale || readLocaleCookie() || savedLocale() || requestedLocale;
  currentPage = id;
  if (locale !== effectiveLocale) void loadTree(locale);
  else if (id) void openPage(id, false);
});

void loadTree(requestedLocale);
