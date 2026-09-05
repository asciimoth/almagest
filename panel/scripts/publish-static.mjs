import { createHash } from "node:crypto";
import { cp, mkdir, readFile, rm, writeFile } from "node:fs/promises";

const target = new URL("../../bin/almagestd/static/", import.meta.url);

await rm(target, { recursive: true, force: true });
await mkdir(target, { recursive: true });
await cp(new URL("../index.html", import.meta.url), new URL("index.html", target));
await cp(new URL("../style.css", import.meta.url), new URL("style.css", target));
await cp(new URL("../public", import.meta.url), new URL("public", target), { recursive: true });
await cp(new URL("../vendor", import.meta.url), new URL("vendor", target), { recursive: true });

const app = await readFile(new URL("public/build/app.js", target));
const formularMenu = await readFile(new URL("vendor/formular-menu/formular-menu.js", target));
const version = createHash("sha256").update(app).update(formularMenu).digest("hex").slice(0, 16);
const indexURL = new URL("index.html", target);
const index = await readFile(indexURL, "utf8");
await writeFile(indexURL, index.replace("./public/build/app.js", `./public/build/app.js?v=${version}`));
