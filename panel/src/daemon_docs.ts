import { configureDocsClient } from "./docs_client.js";
import { HTTPDocsClient } from "./http_docs_client.js";

configureDocsClient(new HTTPDocsClient());
await import("./docs.js");
