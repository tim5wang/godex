#!/usr/bin/env node

import { gzipSync } from "node:zlib";
import { existsSync, readFileSync, rmSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const outDir = path.join(repoRoot, "internal", "uiassets", "embedded_dist");
const manifestPath = path.join(outDir, ".vite", "manifest.json");
const shellBudget = 900 * 1024;
const defaultRouteBudget = 1024 * 1024;
const routeBudgets = new Map([
  ["ChatPage", 2304 * 1024],
  ["OrchestrationPage", 1280 * 1024],
]);

function formatKiB(bytes) {
  return `${(bytes / 1024).toFixed(1)} KiB`;
}

function collectFiles(manifest, entryKeys) {
  const files = new Set();
  const visited = new Set();

  function visit(key) {
    if (visited.has(key)) return;
    const entry = manifest[key];
    if (!entry) throw new Error(`Vite manifest entry not found: ${key}`);
    visited.add(key);

    for (const file of [entry.file, ...(entry.css ?? []), ...(entry.assets ?? [])]) {
      if (file) files.add(file);
    }
    for (const imported of entry.imports ?? []) visit(imported);
  }

  for (const key of entryKeys) visit(key);
  return files;
}

function measureFiles(files) {
  let rawBytes = 0;
  let gzipBytes = 0;
  for (const file of files) {
    const absolutePath = path.resolve(outDir, file);
    if (!absolutePath.startsWith(`${outDir}${path.sep}`)) {
      throw new Error(`Build asset escapes output directory: ${file}`);
    }
    const data = readFileSync(absolutePath);
    rawBytes += data.length;
    gzipBytes += gzipSync(data, { level: 6, mtime: 0 }).length;
  }
  return { rawBytes, gzipBytes };
}

function checkBundle() {
  if (!existsSync(manifestPath)) {
    throw new Error("Vite manifest not found; run `make web-bundle-check` to build the Web UI first.");
  }

  const manifest = JSON.parse(readFileSync(manifestPath, "utf8"));
  const entryKey = Object.keys(manifest).find(
    (key) => manifest[key].isEntry && key === "index.html",
  );
  if (!entryKey) throw new Error("Vite manifest is missing the index.html entry");

  const shellFiles = collectFiles(manifest, [entryKey]);
  shellFiles.add("index.html");
  const shell = measureFiles(shellFiles);
  const failures = [];

  console.log("\nCold-load bundle budget (static imports only; nested dynamic imports are excluded)");
  console.log(`Shell: ${formatKiB(shell.gzipBytes)} gzip / ${formatKiB(shellBudget)} budget`);
  if (shell.gzipBytes > shellBudget) {
    failures.push(`shell is ${formatKiB(shell.gzipBytes)}, over ${formatKiB(shellBudget)}`);
  }

  console.log("\nRoute                    Cold gzip       Route delta     Budget");
  console.log("-----------------------  --------------  --------------  --------------");
  const routeKeys = (manifest[entryKey].dynamicImports ?? []).filter((key) => {
    const entry = manifest[key];
    return entry?.isDynamicEntry && entry.src?.startsWith("src/pages/");
  });
  if (routeKeys.length === 0) throw new Error("Vite manifest contains no dynamic page entries");

  for (const key of routeKeys) {
    const entry = manifest[key];
    const pageFiles = collectFiles(manifest, [entryKey, key]);
    pageFiles.add("index.html");
    const routeOnlyFiles = new Set([...pageFiles].filter((file) => !shellFiles.has(file)));
    const cold = measureFiles(pageFiles);
    const routeOnly = measureFiles(routeOnlyFiles);
    const budget = routeBudgets.get(entry.name) ?? defaultRouteBudget;
    const name = entry.name ?? path.basename(entry.src, path.extname(entry.src));

    console.log(
      `${name.padEnd(23)}  ${formatKiB(cold.gzipBytes).padStart(14)}  ` +
        `${formatKiB(routeOnly.gzipBytes).padStart(14)}  ${formatKiB(budget).padStart(14)}`,
    );
    if (cold.gzipBytes > budget) {
      failures.push(`${name} is ${formatKiB(cold.gzipBytes)}, over ${formatKiB(budget)}`);
    }
  }

  if (failures.length > 0) {
    throw new Error(`Web bundle budget exceeded:\n- ${failures.join("\n- ")}`);
  }

  console.log("\nWeb route bundle budgets passed.");
}

try {
  checkBundle();
} finally {
  rmSync(manifestPath, { force: true });
}
