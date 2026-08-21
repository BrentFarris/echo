import type { FullConfig } from "@playwright/test";
import { spawn, execFileSync, type ChildProcess } from "node:child_process";
import { mkdirSync, rmSync, writeFileSync } from "node:fs";
import { dirname, join, resolve, sep } from "node:path";
import { fileURLToPath } from "node:url";

const directory = dirname(fileURLToPath(import.meta.url));
const webRoot = resolve(directory, "..");
const resultsRoot = resolve(webRoot, "test-results");
const runtimeRoot = resolve(resultsRoot, "e2e-runtime");
const statePath = join(runtimeRoot, "state.json");

function prepareRuntime(): { binary: string; workspace: string; secondaryWorkspace: string; dataPath: string; nodePath: string; fakeLSPPath: string } {
  if (!runtimeRoot.startsWith(resultsRoot + sep)) throw new Error("Refusing to prepare an unsafe E2E runtime path");
  rmSync(runtimeRoot, { recursive: true, force: true });
  const workspace = join(runtimeRoot, "workspace");
  mkdirSync(join(workspace, "nested"), { recursive: true });
  writeFileSync(join(workspace, "go.mod"), "module echo-e2e-workspace\n\ngo 1.24\n", "utf8");
  writeFileSync(join(workspace, "main.go"), "package main\n\nfunc main() {\n\tTarget()\n}\n", "utf8");
  writeFileSync(join(workspace, "definition.go"), "package main\n\nfunc Target() {}\n", "utf8");
  writeFileSync(join(workspace, "usage.go"), "package main\n\nfunc useTarget() {\n\tTarget()\n}\n", "utf8");
  writeFileSync(join(workspace, "implementation.go"), "package main\n\ntype Runner interface {\n\tRun()\n}\n\ntype ConcreteRunner struct{}\n\nfunc (ConcreteRunner) Run() {}\n", "utf8");
  writeFileSync(join(workspace, "nested", "demo.py"), "print('echo')\n", "utf8");
  writeFileSync(join(workspace, ".gitignore"), ".echo/\n", "utf8");
  const git = (...args: string[]) => execFileSync("git", ["-C", workspace, ...args], { stdio: "inherit" });
  git("init", "-b", "main");
  git("config", "user.name", "Echo E2E");
  git("config", "user.email", "echo-e2e@example.com");
  git("add", ".");
  git("commit", "-m", "Initial E2E workspace");
  const secondaryWorkspace = join(runtimeRoot, "secondary-workspace");
  mkdirSync(secondaryWorkspace, { recursive: true });
  writeFileSync(join(secondaryWorkspace, "secondary.txt"), "secondary workspace\n", "utf8");
  const secondaryGit = (...args: string[]) => execFileSync("git", ["-C", secondaryWorkspace, ...args], { stdio: "inherit" });
  secondaryGit("init", "-b", "main");
  secondaryGit("config", "user.name", "Echo E2E");
  secondaryGit("config", "user.email", "echo-e2e@example.com");
  secondaryGit("add", ".");
  secondaryGit("commit", "-m", "Initial secondary workspace");
  const binary = join(runtimeRoot, process.platform === "win32" ? "echo-e2e.exe" : "echo-e2e");
  execFileSync("go", ["build", "-o", binary, ".."], { cwd: webRoot, stdio: "inherit" });
  return {
    binary, workspace, secondaryWorkspace, dataPath: join(runtimeRoot, "echo.json"),
    nodePath: process.execPath, fakeLSPPath: resolve(directory, "fake-lsp.mjs"),
  };
}

async function waitForSetup(child: ChildProcess): Promise<string> {
  return await new Promise<string>((resolveCode, reject) => {
    let output = "";
    const timeout = setTimeout(() => reject(new Error(`Echo did not provide a setup code. Output:\n${output}`)), 20_000);
    const inspect = (chunk: Buffer) => {
      output += chunk.toString();
      const match = output.match(/authentication setup code:\s*(\S+)/i);
      if (match) {
        clearTimeout(timeout);
        resolveCode(match[1]);
      }
    };
    child.stdout?.on("data", inspect);
    child.stderr?.on("data", inspect);
    child.once("exit", (code) => {
      clearTimeout(timeout);
      reject(new Error(`Echo exited before E2E setup (code ${code}). Output:\n${output}`));
    });
  });
}

async function waitForHealth(): Promise<void> {
  const deadline = Date.now() + 20_000;
  while (Date.now() < deadline) {
    try {
      const response = await fetch("http://127.0.0.1:3741/api/health");
      if (response.ok) return;
    } catch {
      // Server startup is still in progress.
    }
    await new Promise((resolveWait) => setTimeout(resolveWait, 100));
  }
  throw new Error("Echo E2E server did not become healthy");
}

export default async function globalSetup(_config: FullConfig): Promise<() => Promise<void>> {
  const runtime = prepareRuntime();
  const child = spawn(runtime.binary, ["-port=3741", `-data=${runtime.dataPath}`], {
    cwd: webRoot,
    windowsHide: true,
    stdio: ["ignore", "pipe", "pipe"],
  });
  const setupCode = await waitForSetup(child);
  await waitForHealth();
  writeFileSync(statePath, JSON.stringify({
    setupCode,
    workspace: runtime.workspace,
    secondaryWorkspace: runtime.secondaryWorkspace,
    nodePath: runtime.nodePath,
    fakeLSPPath: runtime.fakeLSPPath,
  }, null, 2), "utf8");

  return async () => {
    if (child.exitCode === null) {
      child.kill();
      await Promise.race([
        new Promise<void>((resolveExit) => child.once("exit", () => resolveExit())),
        new Promise<void>((resolveWait) => setTimeout(resolveWait, 3_000)),
      ]);
    }
  };
}
