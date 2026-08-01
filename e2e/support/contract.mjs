export const ADMIN_PASSWORD = "gripi-e2e-password";
export const FIXTURE_MARKER = "E2E Contract Ready";

export const sessions = {
  marker: FIXTURE_MARKER,
  history: "E2E History Desktop",
  prompt: "E2E Prompt Desktop",
  markdownTable: "E2E Markdown Table Desktop",
  toolSummary: "E2E Tool Summary Desktop",
  markRead: "E2E Mark Read Desktop",
  idleClient: "E2E Idle Client Desktop",
  promptRetry: "E2E Prompt Retry Desktop",
  promptRetryStop: "E2E Prompt Retry Stop Desktop",
  promptRetryExhausted: "E2E Prompt Retry Exhausted Desktop",
  promptRetryCompact: "E2E Prompt Retry Compact Desktop",
  bashRetry: "E2E Bash Retry Desktop",
  controlsSteer: "E2E Steer Desktop",
  controlsFollowUp: "E2E Follow-up Desktop",
  controlsAbort: "E2E Abort Desktop",
  parallelSubagents: "E2E Parallel Subagents Desktop",
  terminal: "E2E Terminal Desktop",
  settings: "E2E Settings Desktop",
  extension: "E2E Extension Desktop",
  extensionRace: "E2E Extension Race Desktop",
  mobile: "E2E Prompt Mobile",
  bashIncluded: "E2E Bash Included Desktop",
  bashExcluded: "E2E Bash Excluded Desktop",
  bashCancel: "E2E Bash Cancel Desktop",
  bashOverlap: "E2E Bash Overlap Desktop",
  bashMobile: "E2E Bash Cancel Mobile"
};

export const prompts = {
  standard: "Show the deterministic browser response",
  retry: "Retry this deterministic browser response",
  retryExhausted: "Keep this prompt available after retry exhaustion",
  retryCancelled: "Do not send this prompt after stopping",
  steerStart: "Start the steer scenario",
  steerMessage: "Use the steered direction",
  followUpStart: "Start the follow-up scenario",
  followUpMessage: "Continue with the queued follow-up",
  abortStart: "Start the abort scenario",
  parallelSubagents: "Run parallel subagent display scenario",
  terminal: "Show terminal screen updates",
  longCommand: "Run the long tool command scenario",
  markdownTable: "Show a detailed Markdown status table",
  extension: "Ask me for release approval",
  extensionRace: "Exercise queued extension requests",
  newSession: "Create the first deterministic response",
  realPiPrefix: "Reply with exactly this token and nothing else:"
};

const terminalReset = "\x1b[3J\x1b[2J\x1b[H";
const terminalFirstHistory = Array.from({ length: 28 }, (_, index) => `Terminal history ${String(index + 1).padStart(2, "0")}`);
const terminalLatestHistory = [...terminalFirstHistory, "Terminal history 29", "Terminal history 30", "Terminal history 31", "Terminal history 32"];
const terminalFirstFrame = `${terminalReset}${terminalFirstHistory.join("\n")}\nTerminal stale screen`;
const terminalLatestFrame = `${terminalReset}${terminalLatestHistory.join("\n")}\x1b[?1049h\x1b[H\x1b[32mTerminal current screen\x1b[0m`;

export const nativeBash = {
  included: {
    command: "for index in $(seq 1 25); do echo \"included native bash output $index\"; done",
    output: `${Array.from({ length: 25 }, (_, index) => `included native bash output ${index + 1}`).join("\n")}\n`
  },
  excluded: { command: "printf 'excluded native bash output'", output: "excluded native bash output\n" },
  nonzero: { command: "exit 7", output: "deterministic nonzero output\n", exitCode: 7 },
  cancel: { command: "sleep 30 # e2e-cancel" },
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

export const tool = {
  command: "printf tool-command-ran",
  longCommand: "pi --no-session -p \"You are doing an independent review of the tool summary display. Read the full diff, check server-rendered history and live-appended rendering, identify possible simplifications, verify the project philosophy, and report deterministic-review-tail-marker findings back.\"",
  result: "deterministic-tool-result",
  terminalCommand: "capture terminal screen",
  terminalUpdates: [terminalFirstFrame, `${terminalFirstFrame}${terminalLatestFrame}`]
};

export const replies = {
  standard: "Deterministic browser response complete.",
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
