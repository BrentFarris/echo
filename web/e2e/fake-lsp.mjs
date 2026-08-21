import process from "node:process";
import { appendFileSync } from "node:fs";

let input = Buffer.alloc(0);
const documents = new Map();
let rootURI = "";

function send(message) {
  const body = Buffer.from(JSON.stringify(message), "utf8");
  process.stdout.write(`Content-Length: ${body.length}\r\n\r\n`);
  process.stdout.write(body);
}

function response(message, result) {
  send({ jsonrpc: "2.0", id: message.id, result });
}

function notification(method, params) {
  send({ jsonrpc: "2.0", method, params });
}

function mainRange(uri) {
  const lines = String(documents.get(uri) || "").split(/\r?\n/);
  const line = Math.max(0, lines.findIndex((value) => value.includes("func main")));
  const character = Math.max(0, (lines[line] || "").indexOf("main"));
  return { start: { line, character }, end: { line, character: character + 4 } };
}

function targetRange(uri, fallbackLine = 0) {
  const lines = String(documents.get(uri) || "").split(/\r?\n/);
  const line = lines.findIndex((value) => /\bTarget\b/.test(value));
  if (line >= 0) {
    const character = lines[line].search(/\bTarget\b/);
    return { start: { line, character }, end: { line, character: character + 6 } };
  }
  return { start: { line: fallbackLine, character: fallbackLine === 2 ? 5 : 1 }, end: { line: fallbackLine, character: (fallbackLine === 2 ? 5 : 1) + 6 } };
}

function workspaceURI(file) {
  const base = rootURI.endsWith("/") ? rootURI : `${rootURI}/`;
  return new URL(file, base).toString();
}

function definitionLocation() {
  const uri = workspaceURI("definition.go");
  return { uri, range: targetRange(uri, 2) };
}

function implementationLocation() {
  return {
    uri: workspaceURI("implementation.go"),
    range: { start: { line: 8, character: 22 }, end: { line: 8, character: 25 } },
  };
}

function handle(message) {
  const method = message.method;
  if (process.env.ECHO_FAKE_LSP_LOG) appendFileSync(process.env.ECHO_FAKE_LSP_LOG, `${method}\n`, "utf8");
  const params = message.params || {};
  if (method === "initialize") {
    rootURI = params.rootUri || params.workspaceFolders?.[0]?.uri || "";
    response(message, { capabilities: {
      textDocumentSync: { openClose: true, change: 2, save: { includeText: true } },
      completionProvider: { resolveProvider: true, triggerCharacters: ["."] },
      hoverProvider: true,
      definitionProvider: true,
      declarationProvider: true,
      typeDefinitionProvider: true,
      implementationProvider: true,
      referencesProvider: true,
      renameProvider: { prepareProvider: true },
      documentFormattingProvider: true,
      workspaceSymbolProvider: true,
    } });
    return;
  }
  if (method === "shutdown") {
    response(message, null);
    return;
  }
  if (method === "exit") process.exit(0);
  if (method === "textDocument/didOpen") {
    documents.set(params.textDocument.uri, params.textDocument.text);
    notification("textDocument/publishDiagnostics", {
      uri: params.textDocument.uri,
      version: params.textDocument.version,
      diagnostics: [{
        range: { start: { line: 0, character: 0 }, end: { line: 0, character: 7 } },
        severity: 2,
        source: "Echo Fake LSP",
        message: "deterministic fake diagnostic",
      }],
    });
    return;
  }
  if (method === "textDocument/didChange") {
    const changes = params.contentChanges || [];
    const full = changes.find((change) => !change.range);
    if (full) documents.set(params.textDocument.uri, full.text);
    return;
  }
  if (message.id === undefined) return;
  const uri = params.textDocument?.uri;
  if (method === "textDocument/completion") {
    response(message, { isIncomplete: false, items: [{ label: "fakeCompletion", kind: 3, insertText: "fakeCompletion", detail: "Echo fake completion" }] });
  } else if (method === "completionItem/resolve") {
    response(message, { ...params, documentation: { kind: "markdown", value: "Resolved by the Echo fake server." } });
  } else if (method === "textDocument/hover") {
    response(message, { contents: { kind: "markdown", value: "**Echo fake hover**" } });
  } else if (method === "textDocument/definition") {
    response(message, definitionLocation());
  } else if (method === "textDocument/declaration" || method === "textDocument/typeDefinition") {
    response(message, definitionLocation());
  } else if (method === "textDocument/implementation") {
    response(message, implementationLocation());
  } else if (method === "textDocument/references") {
    const mainURI = workspaceURI("main.go");
    const usageURI = workspaceURI("usage.go");
    response(message, [
      definitionLocation(),
      { uri: mainURI, range: targetRange(mainURI, 3) },
      { uri: usageURI, range: targetRange(usageURI, 3) },
    ]);
  } else if (method === "textDocument/prepareRename") {
    response(message, { range: mainRange(uri), placeholder: "main" });
  } else if (method === "textDocument/rename") {
    response(message, { changes: { [uri]: [{ range: mainRange(uri), newText: params.newName }] } });
  } else if (method === "textDocument/formatting") {
    response(message, [{ range: { start: { line: 0, character: 0 }, end: { line: 0, character: 0 } }, newText: "// formatted by Echo fake LSP\n" }]);
  } else if (method === "workspace/symbol") {
    const definition = definitionLocation();
    response(message, [{ name: "Target", containerName: "echo-e2e-workspace", kind: 12, location: definition }]);
  } else {
    send({ jsonrpc: "2.0", id: message.id, error: { code: -32601, message: `unsupported fake method: ${method}` } });
  }
}

process.stdin.on("data", (chunk) => {
  input = Buffer.concat([input, chunk]);
  while (true) {
    const headerEnd = input.indexOf("\r\n\r\n");
    if (headerEnd < 0) return;
    const header = input.subarray(0, headerEnd).toString("ascii");
    const match = /(?:^|\r\n)Content-Length:\s*(\d+)/i.exec(header);
    if (!match) process.exit(2);
    const length = Number(match[1]);
    const bodyStart = headerEnd + 4;
    if (input.length < bodyStart + length) return;
    const body = input.subarray(bodyStart, bodyStart + length);
    input = input.subarray(bodyStart + length);
    handle(JSON.parse(body.toString("utf8")));
  }
});
