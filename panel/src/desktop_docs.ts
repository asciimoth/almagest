import { configureDocsClient } from "./docs_client.js";
import { DesktopDocsClient } from "./desktop_docs_client.js";
import { EmbeddedDocsClient } from "./embedded_docs_client.js";
import { EMBEDDED_DOCS_SOURCE } from "./embedded_docs_protocol.js";
import { setPanelMode } from "./panel_mode.js";

// Select both desktop adapters before docs.js evaluates. Daemon documentation
// and the web demo do not import this module and keep their HTTP/static clients.
setPanelMode(document.documentElement, "desktop");
const embedded = window.parent !== window
  && new URLSearchParams(window.location.search).get("embedded") === "1";
const client = embedded ? new EmbeddedDocsClient() : new DesktopDocsClient();
configureDocsClient(client);
if (client instanceof DesktopDocsClient) window.addEventListener("pagehide", () => client.suspend());
await import("./docs.js");
if (embedded) {
  window.parent.postMessage({
    source: EMBEDDED_DOCS_SOURCE,
    kind: "docs-ready",
  }, window.location.origin);
}
