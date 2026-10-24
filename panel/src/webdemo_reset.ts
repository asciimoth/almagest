const WEB_DEMO_STORAGE_KEYS = [
  "almagest.webdemo.config.v1",
  "almagest.webdemo.kv.v1",
  "almagest.webdemo.secrets.v1",
] as const;

type StorageRemover = Pick<Storage, "removeItem">;

export function installWebDemoReset(
  document: Document,
  storage: StorageRemover,
  reload: () => void,
): void {
  const sidekick = document.querySelector<HTMLElement>("#sidekick");
  if (!sidekick || sidekick.parentElement?.classList.contains("webdemo-sidekick")) return;

  const container = document.createElement("div");
  container.className = "webdemo-sidekick";

  const actions = document.createElement("div");
  actions.className = "webdemo-sidekick-actions";

  const button = document.createElement("button");
  button.id = "webdemo-reload-default";
  button.className = "webdemo-reload-default";
  button.type = "button";
  button.textContent = "Reload default";
  button.title = "Clear web demo data and reload the default graph";
  button.addEventListener("click", () => {
    for (const key of WEB_DEMO_STORAGE_KEYS) {
      try {
        storage.removeItem(key);
      } catch {
        // A reload still resets the in-memory fallback when storage is disabled.
      }
    }
    reload();
  });

  sidekick.before(container);
  actions.append(button);
  container.append(actions, sidekick);
}
