export const ADMIN_PASSWORD = "gripi-e2e-password";
export const FIXTURE_MARKER = "E2E Contract Ready";

export const sessions = {
  marker: FIXTURE_MARKER,
  history: "E2E History Desktop",
  prompt: "E2E Prompt Desktop",
  fork: "E2E Fork Desktop",
  tree: "E2E Tree Desktop",
  quoteHistory: "E2E Quote Persisted Desktop",
  quoteLive: "E2E Quote Live Desktop",
  quoteMobileHistory: "E2E Quote Persisted Mobile",
  quoteMobileLive: "E2E Quote Live Mobile",
  quoteIphoneHistory: "E2E Quote Persisted iPhone",
  quoteIphoneLive: "E2E Quote Live iPhone",
  assistantError: "E2E Assistant Error Desktop",
  deltaStreaming: "E2E Delta Streaming Desktop",
  markdownTable: "E2E Markdown Table Desktop",
  markdownRawDesktop: "E2E Markdown Raw Desktop",
  markdownRawMobile: "E2E Markdown Raw Mobile",
  markdownFenceDesktop: "E2E Markdown Fence Desktop",
  markdownFenceMobile: "E2E Markdown Fence Mobile",
  markdownHeadings: "E2E Markdown Headings Desktop",
  toolSummary: "E2E Tool Summary Desktop",
  toolReload: "E2E Tool Reload Desktop",
  markRead: "E2E Mark Read Desktop",
  unfocusedRead: "E2E Unfocused Read Desktop",
  idleClient: "E2E Idle Client Desktop",
  idleTyping: "E2E Idle Typing Desktop",
  promptRetry: "E2E Prompt Retry Desktop",
  promptRetryStop: "E2E Prompt Retry Stop Desktop",
  promptRetryExhausted: "E2E Prompt Retry Exhausted Desktop",
  promptRetryCompact: "E2E Prompt Retry Compact Desktop",
  bashRetry: "E2E Bash Retry Desktop",
  clearQueue: "E2E Clear Queue Desktop",
  clearQueueMobile: "E2E Clear Queue Mobile",
  controlsSteer: "E2E Steer Desktop",
  controlsFollowUp: "E2E Follow-up Desktop",
  compactionFollowUp: "E2E Compaction Follow-up Desktop",
  sleepRecovery: "E2E Sleep Recovery Desktop",
  controlsAbort: "E2E Abort Desktop",
  parallelSubagents: "E2E Parallel Subagents Desktop",
  parallelSubagentsMobile: "E2E Parallel Subagents Mobile",
  activeSubagents: "E2E Active Subagents Desktop",
  activeSubagentsMobile: "E2E Active Subagents Mobile",
  paginatedSubagent: "E2E Paginated Subagent Desktop",
  terminal: "E2E Terminal Desktop",
  settings: "E2E Settings Desktop",
  footer: "E2E Footer Desktop",
  footerMobile: "E2E Footer Mobile",
  extension: "E2E Extension Desktop",
  extensionRace: "E2E Extension Race Desktop",
  mobile: "E2E Prompt Mobile",
  imageViewer: "E2E Image Viewer Mobile",
  imageHover: "E2E Image Hover Desktop",
  imageRead: "E2E Image Read Desktop",
  bashIncluded: "E2E Bash Included Desktop",
  bashExcluded: "E2E Bash Excluded Desktop",
  bashCancel: "E2E Bash Cancel Desktop",
  bashReload: "E2E Bash Reload Desktop",
  bashOverlap: "E2E Bash Overlap Desktop",
  bashMobile: "E2E Bash Cancel Mobile",
  environment: "E2E Environment Desktop",
  wrappedToolOutput: "E2E Wrapped Tool Output Mobile",
  writeOutput: "E2E Write Output Desktop",
  liveToolResults: "E2E Live Tool Results Desktop",
  liveErrors: "E2E Live Errors Desktop",
  liveCustomMessages: "E2E Live Custom Messages Desktop",
  liveShell: "E2E Live Shell Desktop",
  liveExtensionUi: "E2E Live Extension UI Desktop",
  liveFooter: "E2E Live Footer Desktop",
  liveCompaction: "E2E Live Compaction Desktop",
  liveThinking: "E2E Live Thinking Desktop",
  paletteCommands: "E2E Palette Commands Desktop"
};

export const footerModel = {
  id: `claude-opus-${"long-model-name-".repeat(15)}`,
  name: "Long Provider Model",
  provider: "pi-claude-cli"
};

export const footerStatus = {
  statusKey: "claude-rate-limit",
  statusText: JSON.stringify({ status: "allowed", resetsAt: 1790605800, rateLimitType: "five_hour", detail: "rate-limit-data".repeat(50) })
};

export const prompts = {
  standard: "Show the deterministic browser response",
  assistantError: "Fail with an expired authentication token",
  assistantPartialError: "Fail after a partial response",
  deltaStreaming: "Stream a delta-only response",
  retry: "Retry this deterministic browser response",
  retryExhausted: "Keep this prompt available after retry exhaustion",
  retryCancelled: "Do not send this prompt after stopping",
  clearQueueStart: "Keep running while clearing the queue",
  steerStart: "Start the steer scenario",
  steerMessage: "Use the steered direction",
  queuedAbortSteer: "Restore this steering message after stopping",
  followUpStart: "Start the follow-up scenario",
  followUpMessage: "Continue with the queued follow-up",
  abortStart: "Start the abort scenario",
  parallelSubagents: "Run parallel subagent display scenario",
  parallelSubagentsMobile: "Run mobile parallel subagent display scenario",
  paginatedSubagent: "Run paginated subagent display scenario",
  terminal: "Show terminal screen updates",
  longCommand: "Run the long tool command scenario",
  markdownTable: "Show a detailed Markdown status table",
  markdownRaw: "Show raw HTML with application classes",
  markdownFence: "Show code fences with application class languages",
  markdownHeadings: "Show headings named like application elements",
  extension: "Ask me for release approval",
  extensionRace: "Exercise queued extension requests",
  extensionKinds: "Ask with every extension dialog",
  wrappedToolOutput: "Show wrapped tool output",
  writeOutput: "Show complete write output",
  imageHover: "Keep the attached image neutral on hover",
  imageRead: "Read a large image without refreshing the conversation",
  newSession: "Create the first deterministic response",
  realPiPrefix: "Reply with exactly this token and nothing else:"
};

const terminalReset = "\x1b[3J\x1b[2J\x1b[H";
const terminalFirstHistory = Array.from({ length: 28 }, (_, index) => `Terminal history ${String(index + 1).padStart(2, "0")}`);
const terminalLatestHistory = [...terminalFirstHistory, "Terminal history 29", "Terminal history 30", "Terminal history 31", "Terminal history 32"];
const terminalFirstFrame = `${terminalReset}${terminalFirstHistory.join("\n")}\nTerminal stale screen`;
const terminalLatestFrame = `${terminalReset}${terminalLatestHistory.join("\n")}\x1b[?1049h\x1b[H\x1b[32mTerminal current screen\x1b[0m`;

export const activeRecovery = {
  project: "new-session-desktop",
  toolsPrompt: "Hold ordinary tools for activity recovery until abort",
  thinkingPrompt: {
    cumulative: "Hold cumulative thinking for activity recovery until abort",
    delta: "Hold delta-only thinking for activity recovery until abort"
  },
  preparationPrompt: {
    cumulative: "Hold cumulative preparation for activity recovery until abort",
    delta: "Hold delta-only preparation for activity recovery until abort",
    streaming: "Hold a streaming tool call for activity recovery until abort"
  },
  streamingTool: { id: "recovery-streaming-write", path: "recovery-streaming.txt", line: "First streamed line" },
  previousThinking: "Finished recovery reasoning.",
  text: "Recovery work is underway.",
  thinking: "Still considering the recovery result.",
  unfinishedThinking: "Weighing whether to retry",
  tools: [
    { id: "recovery-bash", name: "bash", arguments: { command: "sleep 300 # recovery" } },
    { id: "recovery-read", name: "read", arguments: { path: "recovery-input.txt" } },
    { id: "recovery-edit", name: "edit", arguments: { path: "recovery-edit.txt", edits: [{ oldText: "before\nsecond line", newText: "after\nreplacement line" }] } },
    { id: "recovery-write", name: "write", arguments: { path: "recovery-write.txt", content: "Recovery content\nSecond line" } },
    { id: "recovery-write-long", name: "write", arguments: { path: "recovery-long.txt", content: Array.from({ length: 40 }, (_, index) => `Recovery line ${index + 1}`).join("\n") } }
  ]
};

export const nativeBash = {
  included: {
    command: "for index in $(seq 1 25); do echo \"included native bash output $index\"; done",
    output: `${Array.from({ length: 25 }, (_, index) => `included native bash output ${index + 1}`).join("\n")}\n`
  },
  excluded: { command: "printf 'excluded native bash output'", output: "excluded native bash output\n" },
  nonzero: { command: "exit 7", output: "deterministic nonzero output\n", exitCode: 7 },
  failure: { command: "start-missing-shell # e2e-failure", error: "spawn /bin/missing-shell ENOENT" },
  truncated: { command: "seq 1 100000 # e2e-truncated", output: "99999\n100000\n", fullOutputFile: "pi-bash-e2e-truncated.log" },
  streaming: { command: "for index in 1 2; do echo \"streamed line $index\"; sleep 1; done # e2e-streaming", chunks: ["streamed line 1\n", "streamed line 2\n"] },
  cancel: { command: "sleep 30 # e2e-cancel" },
  reload: { command: "sleep 30 # e2e-reload" },
  overlap: { command: "sleep 30 # e2e-overlap" },
  mobileCancel: { command: "sleep 30 # e2e-mobile-cancel" }
};

export const subagents = {
  firstCallId: "e2e-subagent-first",
  secondCallId: "e2e-subagent-second",
  firstPrompt: "Analyze deterministic strengths",
  secondPrompt: "Analyze deterministic weaknesses",
  firstResult: "Deterministic strengths complete",
  secondProgress: "Deterministic weaknesses still running",
  secondResult: "Deterministic weaknesses aborted"
};

export const mobileSubagents = {
  firstCallId: "e2e-mobile-subagent-first",
  secondCallId: "e2e-mobile-subagent-second",
  firstPrompt: "Analyze mobile strengths",
  secondPrompt: "Analyze mobile weaknesses",
  firstResult: "Mobile strengths complete",
  secondProgress: "Mobile weaknesses still running",
  secondResult: "Mobile weaknesses aborted"
};

export const paginatedSubagent = {
  matchingCallId: "e2e-paginated-subagent",
  unrelatedCallId: "e2e-paginated-unrelated",
  persistedResult: "Persisted paginated subagent result",
  liveProgress: "Paginated subagent replay still running"
};

const writeContent = [
  "oldest-write-content",
  ...Array.from({ length: 22 }, (_, index) => `write-content-${String(index + 2).padStart(2, "0")}`),
  "latest-write-content"
].join("\n");

export const writeTool = {
  path: "generated/write-output.txt",
  content: writeContent,
  result: `Successfully wrote ${writeContent.length} bytes to generated/write-output.txt`
};

export const tool = {
  command: "printf tool-command-ran",
  longCommand: "pi --no-session -p \"You are doing an independent review of the tool summary display. Read the full diff, check server-rendered history and live-appended rendering, identify possible simplifications, verify the project philosophy, and report deterministic-review-tail-marker findings back.\"",
  result: "deterministic-tool-result",
  terminalCommand: "capture terminal screen",
  terminalUpdates: [terminalFirstFrame, `${terminalFirstFrame}${terminalLatestFrame}`],
  wrappedCommand: "print minified output",
  wrappedOutput: `oldest-wrapped-output-${"x".repeat(12_000)}-latest-wrapped-output`
};

export const replies = {
  standard: "Deterministic browser response complete.",
  deltaThinking: "Reasoning paragraph one.",
  deltaThinkingMore: "Reasoning paragraph two.",
  deltaTextStart: "Answer started.",
  deltaText: "Answer started.\n\nAnswer completed.",
  markdownRaw: '<span class="modal-overlay">Raw span stays in conversation</span>\n\n<code class="image-viewer">Raw code stays in conversation</code>',
  markdownFence: '```modal-overlay\nModal fence stays in conversation\n```\n\n```image-viewer\nImage fence stays in conversation\n```\n\n```javascript\nconst safeValue = 42;\n```',
  markdownHeadings: "## Live output\n\nHeadings stay in the conversation.\n\n## Command list\n\n## Abort form\n\n- [x] done task\n- [ ] open task",
  markdownTable: `| # | Revised status | What we can infer |
|---:|---|---|
| 9 | **Clear** | Table row nine remains readable alongside a detailed explanation. |
| 10 | **Clear enough** | Table row ten keeps its multi-digit identifier together while this longer prose wraps naturally within the available conversation width. |`,
  steer: "Steered direction accepted.",
  followUp: "Queued follow-up completed.",
  aborted: "Run aborted by the browser.",
  extensionApproved: "Release approval was confirmed.",
  extensionRaceComplete: "Queued extension requests completed.",
  newSession: "First session response complete."
};

// Live-rendering characterization scenarios. Each prompt starts a fake Pi run that pauses at confirmation
// dialogs titled by `gates`, so a test can inspect every stage before Pi retires and history replaces live cards.
export const live = {
  toolResults: {
    prompt: "Show live paired tool results",
    gates: { results: "Show the paired tool results?", finish: "Finish the paired tool results?" },
    readOk: { id: "live-read-ok", path: "live/read-ok.txt", result: "live read content stays hidden" },
    readError: { id: "live-read-error", path: "live/missing.txt", result: "ENOENT: no such file or directory, access 'live/missing.txt'" },
    editOk: {
      id: "live-edit-ok",
      path: "live/edit-ok.txt",
      oldText: "old line",
      newText: "new line",
      result: "Successfully replaced 1 block(s) in live/edit-ok.txt.",
      diff: " 1 alpha\n-2 old line\n+2 new line\n 3 omega"
    },
    editError: {
      id: "live-edit-error",
      path: "live/edit-error.txt",
      oldText: "missing line",
      newText: "replacement line",
      result: "Could not find the exact text in live/edit-error.txt."
    },
    writeOk: { id: "live-write-ok", path: "live/write-ok.txt", content: "written content", result: "Successfully wrote to live/write-ok.txt" },
    writeError: { id: "live-write-error", path: "live/write-error.txt", content: "blocked content", result: "EACCES: permission denied, open 'live/write-error.txt'" },
    reply: "Live paired tool results complete."
  },
  errors: {
    transientRetry: { prompt: "Retry a transient provider error", gate: "Retry the provider request?", error: "529 overloaded during the first attempt", reply: "Retried provider response complete." },
    exhaustedRetry: { prompt: "Exhaust the provider retries", error: "503 unavailable after every retry" },
    extensionErrors: {
      prompt: "Report extension handler errors",
      error: "Extension tool_call handler failed",
      terminalUiError: "This extension command requires terminal UI that Gripi does not support yet.",
      reply: "Reply after extension errors."
    },
    summarizationRetry: { prompt: "Retry summarization during automatic compaction", error: "terminated while summarizing", summary: "Summary after a summarization retry.", reply: "Reply before a summarization retry." }
  },
  customMessages: {
    prompt: "Send extension custom messages",
    gate: "Finish the custom messages?",
    customType: "e2e-note",
    shown: "**Displayed** custom note",
    hidden: "Hidden custom context",
    reply: "Custom messages complete."
  },
  extensionUi: {
    prompt: "Update extension widgets, title, and notices",
    restorePrompt: "Keep extension widgets for a page reload",
    gates: { removal: "Remove a widget and the title?", decline: "Offer editor text to decline?", restore: "Finish after the page reload?" },
    widgets: {
      above: { key: "e2e-live-above", lines: ["Above editor first line", "Above editor second line"] },
      implicit: { key: "e2e-live-default", lines: ["Default placement line"] },
      below: { key: "e2e-live-below", lines: ["Below editor line"] }
    },
    title: "E2E extension title",
    notices: { info: "Extension info notice", warning: "Extension warning notice", error: "Extension error notice" },
    editorText: { empty: "Extension text for an empty editor", replaced: "Extension text replacing the draft", declined: "Extension text the user declines" },
    reply: "Extension UI updates complete.",
    restoreReply: "Extension UI restore complete."
  },
  footer: {
    prompt: "Update the footer and session name live",
    gates: { start: "Start the footer updates?", streaming: "End the footer message?", ended: "End the footer turn?" },
    provider: "e2e-live",
    model: "live-message-model",
    thinkingLevel: "high",
    renamed: "E2E Live Footer Renamed Desktop",
    reply: "Footer updates complete."
  },
  compaction: {
    failurePrompt: "Fail automatic compaction",
    failureGate: "Fail the automatic compaction?",
    error: "Auto-compaction failed: summary request timed out",
    abortPrompt: "Hold automatic compaction until stopped",
    duplicatePrompt: "Report one automatic compaction twice",
    duplicateGate: "Finish the repeated compaction?",
    summary: "Live compaction summary.",
    reply: "Reply before automatic compaction."
  },
  thinking: {
    prompt: "Finish thinking before answering",
    gates: { thinking: "Finish the first thought?", ended: "Answer after thinking?", failing: "Fail while thinking?" },
    errorPrompt: "Fail while thinking",
    abortPrompt: "Keep thinking until stopped",
    thought: "Weighing the first option.",
    error: "Provider stream ended while thinking.",
    reply: "Thinking characterization complete."
  }
};

// The fake Hermes's saved session, and what it streams for a prompt. Only the gateway that runs with Hermes has them.
export const hermes = {
  saved: {
    id: "e2e-hermes-saved",
    title: "E2E Hermes Saved",
    prompt: "Summarize the fixture project",
    thought: "Reading the README first.",
    tool: "read_file: README.md",
    toolOutput: "# Fixture project",
    reply: "The fixture project holds the browser suite's sessions."
  },
  run: {
    thought: "Planning the answer.",
    intro: "Checking the files.",
    tool: "terminal: ls",
    toolOutput: "README.md",
    replyPrefix: "Hermes says: "
  },
  stopPrompt: "Wait for stop",
  // Its first paragraph streams while the turn holds until stopped.
  stopThought: "Waiting until stopped.",
  exitPrompt: "Exit now",
  // Streams many small pieces of thinking, then holds like stopPrompt.
  longPrompt: "Think at length, then wait for stop",
  // Hermes asks before it runs the command of this prompt, and runs it only if allowed.
  permission: {
    prompt: "Delete the build folder",
    tool: "terminal: rm -rf build",
    title: "recursive delete: rm -rf build",
    allow: "Allow once",
    deny: "Deny",
    allowed: "removed build/",
    denied: "Denied by the user",
    // Hermes stops waiting for an answer to this one at once, as after its approvals.timeout.
    expiringPrompt: "Delete the build folder unless I am too slow"
  },
  // Hermes asks before it writes the file of this prompt, as before every edit over ACP, and writes it only if allowed.
  edit: {
    prompt: "Write the notes",
    tool: "write_file: notes.md",
    path: "notes.md",
    written: "Wrote notes.md"
  },
  // What the fake dashboard hears in every recording.
  voice: { transcript: "What is in the fixture project?" }
};
