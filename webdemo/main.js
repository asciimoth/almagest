const track = document.querySelector(".carousel-track");
const controls = document.querySelector(".carousel-controls");
const slides = [...track.querySelectorAll(".screenshot")];
const pickers = [...controls.querySelectorAll("[data-slide]")];
const count = controls.querySelector(".slide-count");
const reducedMotion = window.matchMedia("(prefers-reduced-motion: reduce)");
let currentSlide = 0;

function updateSlide(index) {
  currentSlide = index;
  pickers.forEach((button, position) => {
    if (position === index) button.setAttribute("aria-current", "true");
    else button.removeAttribute("aria-current");
  });
  count.textContent = `${String(index + 1).padStart(2, "0")} / ${String(slides.length).padStart(2, "0")}`;
}

function showSlide(index) {
  const next = (index + slides.length) % slides.length;
  track.scrollTo({ left: next * track.clientWidth, behavior: reducedMotion.matches ? "instant" : "smooth" });
}

pickers.forEach((button, index) => button.addEventListener("click", () => showSlide(index)));
controls.querySelectorAll("[data-direction]").forEach((button) => {
  button.addEventListener("click", () => showSlide(currentSlide + Number(button.dataset.direction)));
});
track.addEventListener("scroll", () => {
  const next = Math.max(0, Math.min(slides.length - 1, Math.round(track.scrollLeft / track.clientWidth)));
  if (next !== currentSlide) updateSlide(next);
}, { passive: true });
track.addEventListener("keydown", (event) => {
  if (!["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key)) return;
  event.preventDefault();
  if (event.key === "Home") showSlide(0);
  else if (event.key === "End") showSlide(slides.length - 1);
  else showSlide(currentSlide + (event.key === "ArrowRight" ? 1 : -1));
});
controls.hidden = false;

const tablist = document.querySelector(".install-tabs");
const tabs = [...tablist.querySelectorAll("[data-install]")];
const linuxStart = document.querySelector(".linux-start");

function selectInstall(selected) {
  tabs.forEach((tab) => {
    const active = tab === selected;
    tab.setAttribute("aria-selected", String(active));
    tab.tabIndex = active ? 0 : -1;
    document.getElementById(`install-${tab.dataset.install}`).hidden = !active;
  });
  linuxStart.hidden = ["windows", "nix"].includes(selected.dataset.install);
}

tabs.forEach((tab, index) => {
  const panel = document.getElementById(`install-${tab.dataset.install}`);
  tab.setAttribute("role", "tab");
  tab.setAttribute("aria-controls", panel.id);
  panel.setAttribute("role", "tabpanel");
  panel.setAttribute("aria-labelledby", tab.id);
  panel.tabIndex = 0;
  tab.addEventListener("click", () => selectInstall(tab));
  tab.addEventListener("keydown", (event) => {
    if (!["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key)) return;
    event.preventDefault();
    let next = event.key === "ArrowRight" ? index + 1 : index - 1;
    if (event.key === "Home") next = 0;
    if (event.key === "End") next = tabs.length - 1;
    const selected = tabs[(next + tabs.length) % tabs.length];
    selectInstall(selected);
    selected.focus();
  });
});
tablist.setAttribute("role", "tablist");
selectInstall(tabs[0]);
tablist.hidden = false;

const copyStatus = document.querySelector(".copy-status");
document.querySelectorAll(".copy-button").forEach((button) => {
  const originalLabel = button.textContent;
  button.hidden = false;
  button.addEventListener("click", async () => {
    const code = button.closest(".code-block").querySelector("code");
    copyStatus.textContent = "";
    try {
      await navigator.clipboard.writeText(code.textContent);
      copyStatus.textContent = localized("landing.copy.copied", "Copied to clipboard.");
      button.textContent = localized("landing.copy.done", "Copied");
      window.setTimeout(() => { button.textContent = localized(button.dataset.i18n, originalLabel); }, 2000);
    } catch {
      copyStatus.textContent = localized("landing.copy.unavailable", "Clipboard access is unavailable. Select and copy the text from the code block.");
      code.parentElement.focus();
    }
  });
});

const textEntries = [...document.querySelectorAll("[data-i18n]")].map((element) => ({
  element, key: element.dataset.i18n, fallback: element.textContent,
}));
const attributeEntries = ["aria-label", "aria-roledescription", "alt"].flatMap((attribute) =>
  [...document.querySelectorAll(`[data-i18n-${attribute}]`)].map((element) => ({
    element, attribute, key: element.getAttribute(`data-i18n-${attribute}`), fallback: element.getAttribute(attribute),
  })));
const templateEntries = [...document.querySelectorAll("[data-i18n-template]")].map((element) => {
  const children = [...element.children];
  let index = 0;
  const fallback = [...element.childNodes].map((child) => {
    if (child.nodeType === Node.ELEMENT_NODE) return `{${index++}}`;
    return child.nodeType === Node.TEXT_NODE ? child.textContent : "";
  }).join("").replace(/\s+/g, " ").trim();
  return { element, key: element.dataset.i18nTemplate, children, fallback };
});
const localeSwitch = document.querySelector(".locale-switch");
const localeSelect = localeSwitch.querySelector("select");
let messages = {};
let localeRequest = 0;

function localized(key, fallback) {
  return messages[key] || fallback;
}

function applyTemplate(entry) {
  const translated = localized(entry.key, entry.fallback);
  const positions = [...translated.matchAll(/\{(\d+)\}/g)].map((match) => Number(match[1]));
  const valid = positions.length === entry.children.length
    && new Set(positions).size === entry.children.length
    && positions.every((position) => position < entry.children.length);
  const template = valid ? translated : entry.fallback;
  const fragment = document.createDocumentFragment();
  let offset = 0;
  for (const match of template.matchAll(/\{(\d+)\}/g)) {
    fragment.append(document.createTextNode(template.slice(offset, match.index)));
    fragment.append(entry.children[Number(match[1])]);
    offset = match.index + match[0].length;
  }
  fragment.append(document.createTextNode(template.slice(offset)));
  entry.element.replaceChildren(fragment);
}

function applyMessages(catalog, locale) {
  messages = catalog;
  document.documentElement.lang = locale;
  copyStatus.textContent = "";
  for (const { element, key, fallback } of textEntries) element.textContent = localized(key, fallback);
  for (const { element, attribute, key, fallback } of attributeEntries) {
    element.setAttribute(attribute, localized(key, fallback));
  }
  for (const entry of templateEntries) applyTemplate(entry);
}

function savedLocale() {
  const cookie = document.cookie.split(";").map((part) => part.trim())
    .find((part) => part.startsWith("almagest_locale="));
  if (cookie) {
    try { return decodeURIComponent(cookie.slice("almagest_locale=".length)); } catch { /* Use storage. */ }
  }
  try { return localStorage.getItem("almagest.panel.locale.v1") || ""; } catch { return ""; }
}

function matchLocale(available, preferences) {
  const byTag = new Map(available.map(({ locale }) => [locale.toLowerCase(), locale]));
  for (const preference of preferences) {
    let tag = preference.trim().replaceAll("_", "-");
    while (tag) {
      const match = byTag.get(tag.toLowerCase());
      if (match) return match;
      const separator = tag.lastIndexOf("-");
      if (separator < 0) break;
      tag = tag.slice(0, separator);
    }
  }
  return "en";
}

function rememberLocale(locale) {
  const secure = location.protocol === "https:" ? "; Secure" : "";
  document.cookie = `almagest_locale=${encodeURIComponent(locale)}; Path=/; Max-Age=31536000; SameSite=Lax${secure}`;
  try { localStorage.setItem("almagest.panel.locale.v1", locale); } catch { /* Storage can be disabled. */ }
}

async function setLocale(locale) {
  const request = ++localeRequest;
  try {
    const response = await fetch(`./locales/${encodeURIComponent(locale)}/panel.json`);
    if (!response.ok) throw new Error(`Locale request failed: ${response.status}`);
    const catalog = await response.json();
    if (!catalog || typeof catalog !== "object" || Array.isArray(catalog)) throw new Error("Invalid locale catalog");
    if (request !== localeRequest) return null;
    applyMessages(catalog, locale);
    localeSelect.value = locale;
    return locale;
  } catch {
    if (request !== localeRequest) return null;
    applyMessages({}, "en");
    localeSelect.value = "en";
    return "en";
  }
}

async function initializeLocale() {
  try {
    const response = await fetch("./docs/content/catalog.json");
    if (!response.ok) return;
    const catalog = await response.json();
    const available = catalog.available;
    if (!Array.isArray(available) || !available.some(({ locale }) => locale === "en")) return;
    localeSelect.replaceChildren(...available.map(({ locale, name }) => {
      const option = document.createElement("option");
      option.value = locale;
      option.lang = locale;
      option.textContent = name;
      return option;
    }));
    const requested = new URLSearchParams(location.search).get("locale") || "";
    const preferred = [requested, savedLocale(), ...(navigator.languages || [])].filter(Boolean);
    const initial = await setLocale(matchLocale(available, preferred));
    if (initial) rememberLocale(initial);
    localeSwitch.hidden = available.length < 2;
    localeSelect.addEventListener("change", async () => {
      const selected = await setLocale(localeSelect.value);
      if (!selected) return;
      rememberLocale(selected);
      const url = new URL(location.href);
      if (selected === "en") url.searchParams.delete("locale");
      else url.searchParams.set("locale", selected);
      history.replaceState(null, "", url);
    });
  } catch {
    // The English page remains usable if locale files are unavailable.
  }
}

void initializeLocale();
