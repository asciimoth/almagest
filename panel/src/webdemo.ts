import { configureAdminTransport } from "./admin_transport.js";
import { configureDocsClient } from "./docs_client.js";
import { setPanelMode } from "./panel_mode.js";
import { StaticDocsClient } from "./static_docs_client.js";
import { loadWebDemoAPI, loadWebDemoColorMap, WASMAdminTransport } from "./wasm_transport.js";
import { installWebDemoReset } from "./webdemo_reset.js";

setPanelMode(document.documentElement, "webdemo");
installWebDemoReset(document, {
  removeItem: (key) => window.localStorage.removeItem(key),
}, () => window.location.reload());
configureAdminTransport(() => new WASMAdminTransport(loadWebDemoAPI, loadWebDemoColorMap));
const configuredSiteBase = document.querySelector<HTMLMetaElement>("meta[name='almagest-site-base']")?.content;
configureDocsClient(new StaticDocsClient(new URL(configuredSiteBase || "../", window.location.href)));
await import("./app.js");
