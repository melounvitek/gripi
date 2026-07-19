export const ADMIN_PASSWORD = "gripi-e2e-password";
export const FIXTURE_MARKER = "E2E Contract Ready";

export const sessions = {
  marker: FIXTURE_MARKER,
  history: "E2E History Desktop",
  prompt: "E2E Prompt Desktop",
  controlsSteer: "E2E Steer Desktop",
  controlsFollowUp: "E2E Follow-up Desktop",
  controlsAbort: "E2E Abort Desktop",
  settings: "E2E Settings Desktop",
  extension: "E2E Extension Desktop",
  mobile: "E2E Prompt Mobile",
  mobileLanding: "E2E Mobile Landing"
};

export const prompts = {
  standard: "Show the deterministic browser response",
  steerStart: "Start the steer scenario",
  steerMessage: "Use the steered direction",
  followUpStart: "Start the follow-up scenario",
  followUpMessage: "Continue with the queued follow-up",
  abortStart: "Start the abort scenario",
  extension: "Ask me for release approval",
  newSession: "Create the first deterministic response",
  realPiPrefix: "Reply with exactly this token and nothing else:"
};

export const tool = {
  command: "printf tool-command-ran",
  result: "deterministic-tool-result"
};

export const replies = {
  standard: "Deterministic browser response complete.",
  steer: "Steered direction accepted.",
  followUp: "Queued follow-up completed.",
  aborted: "Run aborted by the browser.",
  extensionApproved: "Release approval was confirmed.",
  newSession: "First session response complete."
};
