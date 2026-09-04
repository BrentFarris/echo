import process from "node:process";

let input = Buffer.alloc(0);
let sequence = 1;
let program = "main.go";
let variableValue = "42";
let stoppedLine = 4;

function send(message) {
  const body = Buffer.from(JSON.stringify({ seq: sequence++, ...message }), "utf8");
  process.stdout.write(`Content-Length: ${body.length}\r\n\r\n`);
  process.stdout.write(body);
}

function respond(request, body = {}, success = true, message = "") {
  send({
    type: "response", request_seq: request.seq, command: request.command,
    success, ...(message ? { message } : {}), body,
  });
}

function event(name, body = {}) {
  send({ type: "event", event: name, body });
}

function stop(reason = "step") {
  event("stopped", { reason, description: reason === "breakpoint" ? "Paused on the E2E breakpoint" : "Paused after stepping", threadId: 1, allThreadsStopped: true });
  event("output", { category: "console", output: `fake adapter stopped at line ${stoppedLine}\n`, source: { name: "main.go", path: program }, line: stoppedLine, column: 1 });
}

function breakpointResults(request) {
  const requested = request.arguments?.breakpoints || [];
  return requested.map((breakpoint, index) => ({
    id: 1000 + index, verified: true, line: breakpoint.line || stoppedLine,
    column: breakpoint.column || 1, source: { name: "main.go", path: program },
  }));
}

function handle(request) {
  const args = request.arguments || {};
  switch (request.command) {
    case "initialize":
      // Deliberately send initialized first; Echo must tolerate adapters that
      // race this event with the initialize response.
      event("initialized");
      respond(request, {
        supportsConfigurationDoneRequest: true,
        supportsConditionalBreakpoints: true,
        supportsHitConditionalBreakpoints: true,
        supportsLogPoints: true,
        supportsFunctionBreakpoints: true,
        supportsExceptionFilterOptions: true,
        exceptionBreakpointFilters: [{ filter: "raised", label: "Raised Exceptions" }],
        supportsSetVariable: true,
        supportsSetExpression: true,
        supportsCompletionsRequest: true,
        supportsRestartRequest: true,
        supportsTerminateRequest: true,
        supportsRestartFrame: true,
        supportsTerminateThreadsRequest: true,
        supportsStepBack: true,
        supportsGotoTargetsRequest: true,
        supportsModulesRequest: true,
        supportsLoadedSourcesRequest: true,
        supportsReadMemoryRequest: true,
        supportsWriteMemoryRequest: true,
        supportsDisassembleRequest: true,
        supportsInstructionBreakpoints: true,
        supportsDataBreakpoints: true,
        supportsBreakpointLocationsRequest: true,
        supportsExceptionInfoRequest: true,
        supportsInlineValues: true,
        supportsCancelRequest: true,
      });
      break;
    case "launch":
    case "attach":
      program = String(args.program || args.path || program);
      respond(request);
      break;
    case "setBreakpoints":
      if (args.source?.path) program = String(args.source.path);
      respond(request, { breakpoints: breakpointResults(request) });
      break;
    case "setFunctionBreakpoints":
    case "setDataBreakpoints":
    case "setInstructionBreakpoints":
      respond(request, { breakpoints: (args.breakpoints || []).map((_, index) => ({ id: 2000 + index, verified: true })) });
      break;
    case "setExceptionBreakpoints":
      respond(request, { breakpoints: [] });
      break;
    case "configurationDone":
      respond(request);
      setTimeout(() => stop("breakpoint"), 20);
      break;
    case "threads":
      respond(request, { threads: [{ id: 1, name: "Main Thread" }] });
      break;
    case "stackTrace": {
      const frames = [
        { id: 10, name: "main", source: { name: "main.go", path: program }, line: stoppedLine, column: 1 },
        { id: 11, name: "runtime", source: { name: "main.go", path: program }, line: 1, column: 1 },
      ];
      const start = Number(args.startFrame || 0);
      const levels = Number(args.levels || frames.length);
      respond(request, { stackFrames: frames.slice(start, start + levels), totalFrames: frames.length });
      break;
    }
    case "scopes":
      respond(request, { scopes: [{ name: "Locals", presentationHint: "locals", variablesReference: 100, namedVariables: 2, expensive: false }] });
      break;
    case "variables": {
      const values = Number(args.variablesReference) === 101
        ? [{ name: "message", value: "echo", type: "string", variablesReference: 0 }]
        : [
          { name: "x", value: variableValue, type: "number", variablesReference: 0, evaluateName: "x", memoryReference: "0x1000" },
          { name: "object", value: "Object", type: "object", variablesReference: 101, namedVariables: 1, evaluateName: "object" },
        ];
      const start = Number(args.start || 0);
      const count = Number(args.count || values.length);
      respond(request, { variables: values.slice(start, start + count) });
      break;
    }
    case "evaluate":
      respond(request, { result: args.expression === "x" ? variableValue : `evaluated(${String(args.expression || "")})`, type: args.expression === "x" ? "number" : "string", variablesReference: 0 });
      break;
    case "setVariable":
    case "setExpression":
      variableValue = String(args.value ?? variableValue);
      respond(request, { value: variableValue, type: "number", variablesReference: 0 });
      break;
    case "continue":
    case "next":
    case "stepIn":
    case "stepOut":
    case "stepBack":
    case "reverseContinue":
    case "goto":
      respond(request, { allThreadsContinued: true });
      event("continued", { threadId: 1, allThreadsContinued: true });
      stoppedLine = request.command === "continue" ? 4 : Math.min(stoppedLine + 1, 8);
      setTimeout(() => stop(request.command), 25);
      break;
    case "pause":
      respond(request);
      setTimeout(() => stop("pause"), 10);
      break;
    case "restart":
      stoppedLine = 4;
      respond(request);
      setTimeout(() => stop("restart"), 20);
      break;
    case "restartFrame":
    case "terminateThreads":
    case "cancel":
      respond(request);
      break;
    case "gotoTargets":
      respond(request, { targets: [{ id: 1, label: `Line ${args.line}`, line: args.line, column: args.column || 1 }] });
      break;
    case "dataBreakpointInfo":
      respond(request, { dataId: `e2e:${String(args.name || "value")}`, description: String(args.name || "value"), accessTypes: ["read", "write", "readWrite"], canPersist: true });
      break;
    case "breakpointLocations":
      respond(request, { breakpoints: [{ line: args.line, column: args.column || 1 }] });
      break;
    case "completions":
      respond(request, { targets: [{ label: "x", text: "x", detail: "number" }, { label: "object", text: "object", detail: "object" }] });
      break;
    case "modules":
      respond(request, { modules: [{ id: "fake", name: "fake-e2e", path: program, version: "1.0" }], totalModules: 1 });
      break;
    case "loadedSources":
      respond(request, { sources: [{ name: "main.go", path: program }] });
      break;
    case "source":
      respond(request, { content: "package main\n\nfunc main() {\n\tTarget()\n}\n", mimeType: "text/x-go" });
      break;
    case "readMemory":
      respond(request, { address: "0x1000", data: Buffer.from("Echo DAP memory", "utf8").toString("base64"), unreadableBytes: 0 });
      break;
    case "writeMemory":
      respond(request, { offset: Number(args.offset || 0), bytesWritten: Buffer.from(String(args.data || ""), "base64").length });
      break;
    case "disassemble":
      respond(request, { instructions: Array.from({ length: Number(args.instructionCount || 8) }, (_, index) => ({ address: `0x${(4096 + index * 4).toString(16)}`, instructionBytes: "90", instruction: index ? "nop" : "mov x, 42", symbol: index ? "" : "main" })) });
      break;
    case "inlineValues":
      respond(request, { areas: [{ range: { startLine: stoppedLine, startColumn: 1, endLine: stoppedLine, endColumn: 2 }, text: `x = ${variableValue}` }] });
      break;
    case "exceptionInfo":
      respond(request, { exceptionId: "FakeException", description: "Deterministic E2E exception", breakMode: "always" });
      break;
    case "terminate":
      respond(request);
      break;
    case "disconnect":
      respond(request);
      event("terminated", { restart: false });
      setTimeout(() => process.exit(0), 30);
      break;
    default:
      respond(request);
  }
}

function parse() {
  while (true) {
    const headerEnd = input.indexOf("\r\n\r\n");
    if (headerEnd < 0) return;
    const header = input.subarray(0, headerEnd).toString("ascii");
    const match = header.match(/(?:^|\r\n)Content-Length:\s*(\d+)/i);
    if (!match) process.exit(2);
    const length = Number(match[1]);
    const messageEnd = headerEnd + 4 + length;
    if (input.length < messageEnd) return;
    const body = input.subarray(headerEnd + 4, messageEnd);
    input = input.subarray(messageEnd);
    try { handle(JSON.parse(body.toString("utf8"))); }
    catch (error) { event("output", { category: "stderr", output: `${error?.stack || error}\n` }); }
  }
}

process.stdin.on("data", (chunk) => {
  input = Buffer.concat([input, chunk]);
  parse();
});
process.stdin.on("end", () => process.exit(0));
