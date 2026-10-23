import { configureDocsClient } from "./docs_client.js";
import { StaticDocsClient } from "./static_docs_client.js";

configureDocsClient(new StaticDocsClient());
await import("./docs.js");
