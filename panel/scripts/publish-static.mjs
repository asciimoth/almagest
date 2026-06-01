import { cp, mkdir, rm } from "node:fs/promises";

const target = new URL("../../bin/almagestd/static/", import.meta.url);

await rm(target, { recursive: true, force: true });
await mkdir(target, { recursive: true });
await cp(new URL("../index.html", import.meta.url), new URL("index.html", target));
await cp(new URL("../style.css", import.meta.url), new URL("style.css", target));
await cp(new URL("../public", import.meta.url), new URL("public", target), { recursive: true });
await cp(new URL("../vendor", import.meta.url), new URL("vendor", target), { recursive: true });
