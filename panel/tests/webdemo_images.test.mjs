import assert from "node:assert/strict";
import { execFile } from "node:child_process";
import { access, mkdtemp, readFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { delimiter, isAbsolute, join } from "node:path";
import { promisify } from "node:util";
import { test } from "node:test";

const execute = promisify(execFile);
const script = new URL("../scripts/render-webdemo-images.mjs", import.meta.url);

test("webdemo captures render every scene through the real panel", { timeout: 90000 }, async (context) => {
  const configured = process.env.CHROMIUM_PATH || process.env.CHROME_PATH || process.env.CHROME_BIN;
  let chromium;
  for (const name of configured ? [configured] : ["chromium", "chromium-browser", "google-chrome", "google-chrome-stable"]) {
    for (const path of isAbsolute(name) ? [name] : (process.env.PATH ?? "").split(delimiter).map((dir) => join(dir, name))) {
      try { await access(path); chromium = path; break; } catch { /* Try next executable. */ }
    }
    if (chromium) break;
  }
  if (!chromium) return context.skip("Chromium is not installed");
  const directory = await mkdtemp(join(tmpdir(), "almagest-webdemo-images-test-"));
  try {
    const { stdout } = await execute(process.execPath, [script.pathname, "--chromium", chromium,
      "--output", directory, "--scale", "1"], { timeout: 85000 });
    for (const scene of ["graph", "settings", "workspace", "routing", "dns", "tls", "local-proxy"]) {
      const png = await readFile(join(directory, `${scene}.png`));
      assert.deepEqual([...png.subarray(0, 8)], [137, 80, 78, 71, 13, 10, 26, 10]);
      assert.equal(png.readUInt32BE(16), 1500);
      assert.equal(png.readUInt32BE(20), 720);
      assert.ok(png.length > 10000, `${scene} must contain rendered UI`);
    }
    assert.match(stdout, /graph\.png \(7 nodes, 6 links\)/);
    assert.match(stdout, /routing\.png \(6 nodes, 5 links\)/);
  } finally {
    await rm(directory, { recursive: true, force: true });
  }
});

test("capture tool rejects invalid viewport input before starting Chromium", async () => {
  await assert.rejects(execute(process.execPath, [script.pathname, "--size", "wide"]), /--size must use WIDTHxHEIGHT/);
});
