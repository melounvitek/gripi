export function eventPollingDelay(hidden, composerState, emptyPollCount, failed = false) {
  if (hidden) return 10000;
  if (failed) return Math.max(emptyPollDelay(emptyPollCount), 2000);
  if (["running", "stopping"].includes(composerState)) return 250;
  return emptyPollDelay(emptyPollCount);
}

function emptyPollDelay(emptyPollCount) {
  if (emptyPollCount >= 6) return 5000;
  if (emptyPollCount >= 2) return 2000;
  return 1000;
}
