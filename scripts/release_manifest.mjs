#!/usr/bin/env node

import { createHash } from "node:crypto";
import { spawnSync } from "node:child_process";
import {
  mkdtempSync,
  readFileSync,
  readdirSync,
  rmSync,
  statSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";

const expectedPlatforms = {
  "win-x86-64": "godex.exe",
  "mac-x86-64": "godex",
  "mac-apple": "godex",
  "linux-x86-64": "godex",
};
const maxExtractBytes = 256 * 1024 * 1024;

function parseOptions(args) {
  const options = {};
  for (let index = 0; index < args.length; index += 1) {
    const key = args[index];
    if (!key.startsWith("--") || index + 1 >= args.length) {
      throw new Error(`Expected --name value, received: ${key}`);
    }
    options[key.slice(2)] = args[index + 1];
    index += 1;
  }
  return options;
}

function required(options, key) {
  const value = options[key];
  if (!value) throw new Error(`Missing required option --${key}`);
  return value;
}

function sha256(data) {
  return createHash("sha256").update(data).digest("hex");
}

function jsonFile(pathname, value) {
  writeFileSync(pathname, `${JSON.stringify(value, null, 2)}\n`);
}

function extractArchiveFile(archivePath, memberPath) {
  return run("tar", ["-xOzf", archivePath, memberPath], {
    encoding: "buffer",
    maxBuffer: maxExtractBytes,
  });
}

function run(command, args, options = {}) {
  const result = spawnSync(command, args, { stdio: "pipe", ...options });
  if (result.error) throw result.error;
  if (result.status !== 0) {
    const stderr = result.stderr?.toString().trim();
    throw new Error(`${command} ${args.join(" ")} failed: ${stderr || result.status}`);
  }
  return result.stdout;
}

function createPackageManifest(options) {
  const stageDir = path.resolve(required(options, "stage-dir"));
  const app = required(options, "app");
  const version = required(options, "version");
  const commit = required(options, "commit");
  const buildDate = required(options, "build-date");
  const platform = required(options, "platform");
  const binary = required(options, "binary");
  if (path.basename(binary) !== binary) throw new Error(`Invalid binary filename: ${binary}`);
  if (expectedPlatforms[platform] !== binary) {
    throw new Error(`Unexpected binary ${binary} for platform ${platform}`);
  }

  const files = [binary, "README.md"].sort().map((relativePath) => {
    const absolutePath = path.join(stageDir, relativePath);
    const data = readFileSync(absolutePath);
    return {
      path: relativePath,
      size_bytes: data.length,
      sha256: sha256(data),
    };
  });
  jsonFile(path.join(stageDir, "manifest.json"), {
    schema_version: 1,
    app,
    version,
    commit,
    build_date: buildDate,
    platform,
    files,
  });
}

function hostPlatform() {
  if (process.platform === "darwin" && process.arch === "arm64") return "mac-apple";
  if (process.platform === "darwin" && process.arch === "x64") return "mac-x86-64";
  if (process.platform === "win32" && process.arch === "x64") return "win-x86-64";
  if (process.platform === "linux" && process.arch === "x64") return "linux-x86-64";
  return "";
}

function verifyArchive(archivePath, expectedMetadata, smokePlatform) {
  const packageName = path.basename(archivePath, ".tar.gz");
  const manifestPath = `${packageName}/manifest.json`;
  const entries = run("tar", ["-tzf", archivePath], { encoding: "utf8" })
    .split(/\r?\n/)
    .filter(Boolean)
    .filter((entry) => !entry.endsWith("/"))
    .sort();
  const manifest = JSON.parse(extractArchiveFile(archivePath, manifestPath).toString("utf8"));

  for (const key of ["schema_version", "app", "version", "commit", "build_date"]) {
    if (manifest[key] !== expectedMetadata[key]) {
      throw new Error(`${packageName}: manifest ${key} does not match the release`);
    }
  }
  if (expectedPlatforms[manifest.platform] === undefined) {
    throw new Error(`${packageName}: unsupported platform ${manifest.platform}`);
  }
  const binaryName = expectedPlatforms[manifest.platform];
  const fileNames = manifest.files.map((file) => file.path);
  if (
    new Set(fileNames).size !== fileNames.length ||
    fileNames.some((name) => path.basename(name) !== name) ||
    !fileNames.includes("README.md") ||
    !fileNames.includes(binaryName) ||
    fileNames.length !== 2
  ) {
    throw new Error(`${packageName}: manifest must contain its binary and README.md only`);
  }

  const expectedFiles = [...manifest.files.map((file) => `${packageName}/${file.path}`), manifestPath].sort();
  if (JSON.stringify(entries) !== JSON.stringify(expectedFiles)) {
    throw new Error(`${packageName}: archive contents do not match its manifest`);
  }

  let hostBinary;
  for (const file of manifest.files) {
    const data = extractArchiveFile(archivePath, `${packageName}/${file.path}`);
    if (data.length !== file.size_bytes || sha256(data) !== file.sha256) {
      throw new Error(`${packageName}: checksum or size mismatch for ${file.path}`);
    }
    if (file.path === expectedPlatforms[manifest.platform] && manifest.platform === smokePlatform) {
      hostBinary = { name: file.path, data };
    }
  }

  if (hostBinary) {
    const smokeDir = mkdtempSync(path.join(tmpdir(), "godex-release-smoke-"));
    try {
      const binaryPath = path.join(smokeDir, hostBinary.name);
      writeFileSync(binaryPath, hostBinary.data, { mode: 0o755 });
      const smoke = spawnSync(binaryPath, ["--help"], {
        encoding: "utf8",
        env: { ...process.env, GODEX_HOME: path.join(smokeDir, "home") },
      });
      if (smoke.error) throw smoke.error;
      if (smoke.status !== 0) {
        throw new Error(`${packageName}: packaged binary --help failed: ${smoke.stderr.trim()}`);
      }
      console.log(`[release] smoke passed: ${packageName}/${hostBinary.name} --help`);
    } finally {
      rmSync(smokeDir, { recursive: true, force: true });
    }
  }

  return {
    file: path.basename(archivePath),
    platform: manifest.platform,
    size_bytes: statSync(archivePath).size,
    sha256: sha256(readFileSync(archivePath)),
    files: manifest.files,
  };
}

function createReleaseManifest(options) {
  const distDir = path.resolve(required(options, "dist-dir"));
  const metadata = {
    schema_version: 1,
    app: required(options, "app"),
    version: required(options, "version"),
    commit: required(options, "commit"),
    build_date: required(options, "build-date"),
  };
  const prefix = `${metadata.app}-${metadata.version}-`;
  const archives = readdirSync(distDir)
    .filter((name) => name.startsWith(prefix) && name.endsWith(".tar.gz"))
    .sort();
  if (archives.length !== Object.keys(expectedPlatforms).length) {
    throw new Error(
      `Expected ${Object.keys(expectedPlatforms).length} release archives, found ${archives.length}`,
    );
  }

  const host = hostPlatform();
  const artifacts = archives.map((name) =>
    verifyArchive(path.join(distDir, name), metadata, host),
  );
  const releaseManifest = { ...metadata, artifacts };
  jsonFile(path.join(distDir, "release-manifest.json"), releaseManifest);
  writeFileSync(
    path.join(distDir, "SHA256SUMS"),
    `${artifacts.map((artifact) => `${artifact.sha256}  ${artifact.file}`).join("\n")}\n`,
  );
  console.log(`[release] verified ${artifacts.length} archives`);
}

const [command, ...args] = process.argv.slice(2);
const options = parseOptions(args);
if (command === "package") {
  createPackageManifest(options);
} else if (command === "index") {
  createReleaseManifest(options);
} else {
  throw new Error("Usage: release_manifest.mjs <package|index> --name value ...");
}
