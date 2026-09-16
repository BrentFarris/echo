#!/usr/bin/env node

import { request } from "node:http";

const socketPath = "/run/echo/browser/launcher.sock";
const url = process.argv.slice(2).find((argument) => !argument.startsWith("--")) || "";
const payload = Buffer.from(JSON.stringify({ url }));

function openBrowser() {
  return new Promise((resolve, reject) => {
    const outgoing = request({
      socketPath,
      path: "/open",
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        "Content-Length": payload.length,
      },
      timeout: 15_000,
    }, (response) => {
      const chunks = [];
      response.on("data", (chunk) => chunks.push(chunk));
      response.on("end", () => {
        if (response.statusCode >= 200 && response.statusCode < 300) resolve();
        else reject(new Error(Buffer.concat(chunks).toString("utf8") || `browser bridge returned ${response.statusCode}`));
      });
    });
    outgoing.on("timeout", () => outgoing.destroy(new Error("browser bridge timed out")));
    outgoing.on("error", reject);
    outgoing.end(payload);
  });
}

let lastError;
for (let attempt = 0; attempt < 50; attempt += 1) {
  try {
    await openBrowser();
    process.exit(0);
  } catch (error) {
    lastError = error;
    if (error?.code !== "ENOENT" && error?.code !== "ECONNREFUSED") break;
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
}

console.error(`Could not open the managed Echo browser: ${String(lastError?.message || lastError)}`);
process.exit(1);
