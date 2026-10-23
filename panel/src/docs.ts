import {
  classPreviewPorts,
  createClassPreview,
  renderNodeDescription,
  safeCSSColor,
  textElement,
} from "./safe_dom.js";
import { readLocaleCookie, writeLocaleCookie } from "./localization.js";
import { adminPanelWindowName, openOrFocusAdminPanel } from "./window_navigation.js";
import { handoffBoundaryScroll, initialTopicOpen, selectPageInTree } from "./docs_tree.js";
import {
  DEFAULT_NODE_TEXT_COLOR,
  DEFAULT_NODE_TITLE_COLOR,
  parseNodeClassSnapshot,
  parseTypePaletteMap,
  setTypeColors,
  type NodeClassSnapshot,
  type TypePalette,
} from "./pasta_model.js";
import { docsClient } from "./docs_client.js";

type PageMark = "unavailable" | "deprecated";
type PageSummary = { id: string; name: string; folded: boolean; order?: number; default?: boolean; available?: boolean; marks?: PageMark[] };
type Topic = { id: string; name: string; folded: boolean; order?: number; topics: Topic[]; pages: PageSummary[] };
type AvailableLocale = { locale: string; name: string };
type TreeResponse = {
  locale: string;
  available: AvailableLocale[];
  topics: Topic[];
  node_unavailable_message: string;
  mark_messages?: Partial<Record<PageMark, string>>;
};
type NodeClassResponse = { snapshot: NodeClassSnapshot; colormap: Record<string, TypePalette> };

const localeStorageKey = "almagest.panel.locale.v1";
const topicFoldStorageKey = "almagest.docs.topic-folds.v1";
const treeElement = document.querySelector<HTMLElement>("#docs-tree")!;
const pageElement = document.querySelector<HTMLElement>("#docs-page")!;
const localeSelect = document.querySelector<HTMLSelectElement>("#docs-locale-select")!;
const adminPanelLink = document.querySelector<HTMLAnchorElement>("#admin-panel-link")!;
const navigationToggle = document.querySelector<HTMLButtonElement>("#docs-navigation-toggle")!;
const navigationScrim = document.querySelector<HTMLButtonElement>("#docs-navigation-scrim")!;
const parameters = new URLSearchParams(window.location.search);
let urlLocale = parameters.get("locale") || "";
const requestedLocale = urlLocale || readLocaleCookie() || savedLocale() || navigator.language || "en";
let currentPage = parameters.get("page") || docsClient().pageFromLocation(window.location);
let effectiveLocale = requestedLocale;
let orderedPageIDs: string[] = [];
const pageNames = new Map<string, string>();
const pageAvailability = new Map<string, boolean>();
const topicFolds = savedTopicFolds();
let nodeUnavailableMessage = "This node is not available in the current daemon environment.";
let markMessages: Record<PageMark, string> = {
  unavailable: nodeUnavailableMessage,
  deprecated: "This node is deprecated.",
};

const markLabels: Record<PageMark, string> = { unavailable: "", deprecated: "D" };

function setNavigationOpen(open: boolean): void {
  const returnFocus = !open && (treeElement.contains(document.activeElement) || navigationScrim === document.activeElement);
  treeElement.classList.toggle("open", open);
  navigationScrim.classList.toggle("open", open);
  navigationToggle.setAttribute("aria-expanded", String(open));
  navigationScrim.tabIndex = open ? 0 : -1;
  if (returnFocus) navigationToggle.focus();
}

navigationToggle.addEventListener("click", () => {
  setNavigationOpen(navigationToggle.getAttribute("aria-expanded") !== "true");
});
navigationScrim.addEventListener("click", () => { setNavigationOpen(false); });
document.addEventListener("keydown", (event) => {
  if (event.key === "Escape") setNavigationOpen(false);
});

treeElement.addEventListener("wheel", (event) => {
  if (event.ctrlKey || Math.abs(event.deltaX) > Math.abs(event.deltaY)) return;
  const lineHeight = Number.parseFloat(window.getComputedStyle(treeElement).lineHeight) || 16;
  const deltaY = event.deltaY * (event.deltaMode === WheelEvent.DOM_DELTA_LINE
    ? lineHeight
    : event.deltaMode === WheelEvent.DOM_DELTA_PAGE ? treeElement.clientHeight : 1);
  if (handoffBoundaryScroll(treeElement, pageElement, deltaY)) event.preventDefault();
}, { passive: false });

function pageMark(value: unknown): PageMark | null {
  return value === "unavailable" || value === "deprecated" ? value : null;
}

function renderPageMarks(values: readonly unknown[] | undefined): HTMLElement | null {
  const marks = (values ?? []).map(pageMark).filter((value): value is PageMark => value !== null);
  if (marks.length === 0) return null;
  const container = document.createElement("span");
  container.className = "docs-page-marks";
  for (const mark of marks) {
    const status = textElement("span", markLabels[mark], `docs-page-mark docs-page-mark-${mark}`);
    status.title = markMessages[mark];
    status.setAttribute("aria-label", markMessages[mark]);
    container.append(status);
  }
  return container;
}

adminPanelLink.target = adminPanelWindowName(window.location.origin);
adminPanelLink.addEventListener("click", (event) => {
  // Keep standard browser behavior when the user requests a separate tab or window.
  if (event.button !== 0 || event.ctrlKey || event.metaKey || event.shiftKey || event.altKey) return;
  // If the pop-up is blocked, do not cancel the link's native new-tab fallback.
  if (openOrFocusAdminPanel(window, adminPanelLink.href)) event.preventDefault();
});

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
    details.open = initialTopicOpen(topic.folded, savedOpen, topicContainsPage(topic, currentPage));
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
      const link = document.createElement("a");
      link.append(document.createTextNode(page.name));
      const marks = renderPageMarks(page.marks ?? (page.available === false ? ["unavailable"] : []));
      if (marks) link.append(marks);
      link.href = docsURL(page.id);
      link.dataset.pageId = page.id;
      link.classList.toggle("active", page.id === currentPage);
      link.addEventListener("click", (event) => {
        event.preventDefault();
        setNavigationOpen(false);
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

function docsURL(page: string, hash = ""): string {
  return docsClient().pageURL(page, urlLocale, hash);
}

function pageNavigationLink(id: string, direction: "prev" | "next"): HTMLAnchorElement {
  const name = pageNames.get(id) || id;
  const directionLabel = direction === "prev" ? "← Previous" : "Next →";
  const link = document.createElement("a");
  link.className = `docs-page-navigation-link docs-page-navigation-${direction}`;
  link.href = docsURL(id);
  link.rel = direction;
  link.setAttribute("aria-label", `${direction === "prev" ? "Previous" : "Next"} page: ${name}`);
  link.append(
    textElement("span", directionLabel, "docs-page-navigation-direction"),
    textElement("strong", name),
  );
  link.addEventListener("click", (event) => {
    if (event.button !== 0 || event.ctrlKey || event.metaKey || event.shiftKey || event.altKey) return;
    event.preventDefault();
    void openPage(id, true);
  });
  return link;
}

function renderPageNavigation(id: string): HTMLElement | null {
  const index = orderedPageIDs.indexOf(id);
  if (index < 0 || orderedPageIDs.length < 2) return null;
  const navigation = document.createElement("nav");
  navigation.className = "docs-page-navigation";
  navigation.setAttribute("aria-label", "Page navigation");
  const previousID = orderedPageIDs[index - 1];
  const nextID = orderedPageIDs[index + 1];
  if (previousID) navigation.append(pageNavigationLink(previousID, "prev"));
  if (nextID) navigation.append(pageNavigationLink(nextID, "next"));
  return navigation;
}

async function openPage(id: string, updateHistory: boolean): Promise<void> {
  pageElement.replaceChildren(textElement("p", "Loading documentation."));
  try {
    const contentText = await docsClient().raw(effectiveLocale, id);
    const name = pageNames.get(id) || id;
    currentPage = id;
    document.title = `${name} — Almagest Documentation`;
    const title = textElement("h1", name);
    const content = document.createElement("div");
    renderNodeDescription(content, contentText, "docs", id, highlightPreviewPort, (destination) => docsURL(destination));
    const unavailable = pageAvailability.get(id) === false
      ? textElement("div", nodeUnavailableMessage, "docs-node-unavailable")
      : null;
    const navigation = renderPageNavigation(id);
    if (unavailable) unavailable.setAttribute("role", "status");
    pageElement.replaceChildren(
      title,
      ...(unavailable ? [unavailable] : []),
      content,
      ...(navigation ? [navigation] : []),
    );
    if (updateHistory) window.history.pushState({ page: id }, "", docsURL(id));
    scrollToCurrentAnchor();
    if (!unavailable) void loadNodePreview(id, title);
    selectPageInTree(treeElement, id);
  } catch (error) {
    pageElement.replaceChildren(
      textElement("h1", "Documentation unavailable"),
      textElement("p", error instanceof Error ? error.message : String(error)),
    );
  }
}

function highlightPreviewPort(side: "left" | "right", name: string, highlighted: boolean): void {
  for (const port of pageElement.querySelectorAll<HTMLElement>(".docs-node-preview .class-preview-port")) {
    if (port.dataset.portSide === side && port.dataset.portName === name) {
      port.classList.toggle("class-preview-port-highlighted", highlighted);
    }
  }
}

function scrollToCurrentAnchor(): void {
  if (!window.location.hash) return;
  try {
    document.getElementById(decodeURIComponent(window.location.hash.slice(1)))?.scrollIntoView();
  } catch {
    // Ignore malformed percent escapes in an external URL fragment.
  }
}

async function loadNodePreview(id: string, title: HTMLElement): Promise<void> {
  if (!id.startsWith("node/")) return;
  try {
    const response = parseNodeClassResponse(await docsClient().nodeClass(effectiveLocale, id));
    if (!response) throw new Error("Invalid node class response");
    if (currentPage !== id || !title.isConnected) return;

    const colors: Record<string, { color: string; textColor: string }> = {};
    for (const [type, palette] of Object.entries(response.colormap)) {
      const color = palette.type ?? palette.color;
      const textColor = palette.text ?? palette.textColor ?? DEFAULT_NODE_TEXT_COLOR;
      if (color) colors[type] = { color, textColor };
    }
    setTypeColors(colors);

    const container = document.createElement("div");
    container.className = "docs-node-preview";
    const primaryColor = colors[response.snapshot.primary_type];
    if (primaryColor) {
      container.classList.add("class-preview-primary");
      container.style.setProperty("--class-primary-color", safeCSSColor(primaryColor.color, DEFAULT_NODE_TITLE_COLOR));
      container.style.setProperty("--class-primary-text-color", safeCSSColor(primaryColor.textColor, DEFAULT_NODE_TEXT_COLOR));
    }
    container.append(createClassPreview(
      response.snapshot,
      classPreviewPorts(response.snapshot, "left"),
      classPreviewPorts(response.snapshot, "right"),
    ));
    title.after(container);
  } catch (error) {
    console.error(`Could not load node preview for ${id}.`, error);
  }
}

function parseNodeClassResponse(input: unknown): NodeClassResponse | null {
  if (!input || typeof input !== "object" || Array.isArray(input)) return null;
  const value = input as Record<string, unknown>;
  const snapshot = parseNodeClassSnapshot(value.snapshot);
  const colormap = parseTypePaletteMap(value.colormap);
  return snapshot && colormap ? { snapshot, colormap } : null;
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
    const tree = await docsClient().tree(locale) as TreeResponse;
    effectiveLocale = tree.locale;
    nodeUnavailableMessage = tree.node_unavailable_message || nodeUnavailableMessage;
    markMessages = {
      unavailable: tree.mark_messages?.unavailable || nodeUnavailableMessage,
      deprecated: tree.mark_messages?.deprecated || markMessages.deprecated,
    };
    saveLocale(tree.locale);
    document.documentElement.lang = tree.locale;
    renderLocaleOptions(tree.available, tree.locale);
    const ids = pageIDs(tree.topics);
    orderedPageIDs = ids;
    pageNames.clear();
    pageAvailability.clear();
    collectPageNames(tree.topics);
    if (!ids.includes(currentPage)) currentPage = defaultPageID(tree.topics) || ids[0] || "";
    treeElement.replaceChildren(renderTopics(tree.topics));
    window.history.replaceState({ page: currentPage }, "", docsURL(currentPage, window.location.hash));
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
    for (const page of topic.pages) {
      pageNames.set(page.id, page.name);
      if (typeof page.available === "boolean") pageAvailability.set(page.id, page.available);
    }
    collectPageNames(topic.topics);
  }
}

window.addEventListener("popstate", () => {
  const query = new URLSearchParams(window.location.search);
  const id = query.get("page") || docsClient().pageFromLocation(window.location);
  urlLocale = query.get("locale") || "";
  const locale = urlLocale || readLocaleCookie() || savedLocale() || requestedLocale;
  currentPage = id;
  if (locale !== effectiveLocale) void loadTree(locale);
  else if (id) void openPage(id, false);
});

void loadTree(requestedLocale);
