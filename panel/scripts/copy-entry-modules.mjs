import { cp, mkdir } from "node:fs/promises";
import { dirname, isAbsolute, join, relative, resolve, sep } from "node:path";
import { fileURLToPath } from "node:url";
import { build } from "esbuild";

const panelDirectory = fileURLToPath(new URL("../", import.meta.url));
const sourceDirectory = fileURLToPath(new URL("../public/build/", import.meta.url));

/** Copy the compiled modules reachable from the selected browser entry points. */
export async function copyEntryModules(entries, target) {
  const targetDirectory = fileURLToPath(new URL("public/build/", target));
  const result = await build({
    absWorkingDir: panelDirectory,
    bundle: true,
    entryPoints: entries.map((name) => join(sourceDirectory, name)),
    format: "esm",
    metafile: true,
    outdir: targetDirectory,
    platform: "browser",
    write: false,
  });
  for (const input of Object.keys(result.metafile.inputs)) {
    const name = relative(sourceDirectory, resolve(panelDirectory, input));
    if (isAbsolute(name) || name === ".." || name.startsWith(`..${sep}`) || !name.endsWith(".js")) {
      throw new Error(`Browser entry point includes an unsupported module: ${input}`);
    }
    const destination = join(targetDirectory, name);
    await mkdir(dirname(destination), { recursive: true });
    await cp(join(sourceDirectory, name), destination);
  }
}
