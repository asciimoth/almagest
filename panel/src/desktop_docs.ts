import { configureDocsClient } from "./docs_client.js";
import { DesktopDocsClient } from "./desktop_docs_client.js";
import { setPanelMode } from "./panel_mode.js";

// Select both desktop adapters before docs.js evaluates. Daemon documentation
// and the web demo do not import this module and keep their HTTP/static clients.
setPanelMode(document.documentElement, "desktop");
const client = new DesktopDocsClient();
configureDocsClient(client);
window.addEventListener("pagehide", () => client.suspend());
await import("./docs.js");
