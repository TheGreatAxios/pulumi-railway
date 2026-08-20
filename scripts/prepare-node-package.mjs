import { cp, readFile, writeFile } from "node:fs/promises";
import { resolve } from "node:path";

const root = resolve(import.meta.dirname, "..");
const version = process.argv[2];

if (!/^\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$/.test(version ?? "")) {
  throw new Error(`invalid package version: ${version ?? "<missing>"}`);
}

const sourcePackagePath = resolve(root, "sdk/nodejs/package.json");
const outputDirectory = resolve(root, "sdk/nodejs/bin");
const outputPackagePath = resolve(outputDirectory, "package.json");
const packageJson = JSON.parse(await readFile(sourcePackagePath, "utf8"));

packageJson.version = version;
// The Pulumi engine reads pulumi.version to resolve the plugin binary, so it
// must match the release version exactly.
if (packageJson.pulumi) {
  packageJson.pulumi.version = version;
}
packageJson.main = "index.js";
packageJson.types = "index.d.ts";
packageJson.author = "Sawyer Cutler";
packageJson.bugs = "https://github.com/thegreataxios/pulumi-railway/issues";
packageJson.publishConfig = { access: "public" };
packageJson.engines = { node: ">=20" };
delete packageJson.scripts;
delete packageJson.devDependencies;

await writeFile(outputPackagePath, `${JSON.stringify(packageJson, null, 2)}\n`);
await cp(resolve(root, "README.md"), resolve(outputDirectory, "README.md"));
await cp(resolve(root, "LICENSE"), resolve(outputDirectory, "LICENSE"));
