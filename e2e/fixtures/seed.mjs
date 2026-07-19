import { mkdir, writeFile } from "node:fs/promises";
import path from "node:path";
import { pathToFileURL } from "node:url";
import { sessions } from "../support/contract.mjs";

const BASE_TIME = Date.parse("2026-07-20T12:00:00.000Z");

function message(role, content, timestampMs, extra = {}) {
  return { role, content, timestamp: timestampMs, ...extra };
}

function assistant(text, timestampMs) {
  return message("assistant", [{ type: "text", text }], timestampMs, {
    api: "openai-responses",
    provider: "e2e",
    model: "fixture-model",
    usage: {
      input: 10,
      output: 5,
      cacheRead: 0,
      cacheWrite: 0,
      totalTokens: 15,
      cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, total: 0 }
    },
    stopReason: "stop"
  });
}

async function writeSession(root, project, slug, title, index, history = {}) {
  const sessionDirectory = path.join(root, "sessions", "e2e");
  await mkdir(sessionDirectory, { recursive: true });
  const sessionPath = path.join(sessionDirectory, `${slug}.jsonl`);
  const startedAt = BASE_TIME + index * 60_000;
  let parentId = null;
  let sequence = 0;
  const nextEntry = (type, fields) => {
    sequence += 1;
    const id = `${String(index).padStart(2, "0")}${String(sequence).padStart(6, "0")}`;
    const entry = { type, id, parentId, timestamp: new Date(startedAt + sequence * 1000).toISOString(), ...fields };
    parentId = id;
    return entry;
  };
  const entries = [
    {
      type: "session",
      version: 3,
      id: `e2e-${slug}`,
      timestamp: new Date(startedAt).toISOString(),
      cwd: project
    },
    nextEntry("model_change", { provider: "e2e", modelId: "fixture-model" }),
    nextEntry("thinking_level_change", { thinkingLevel: "medium" }),
    nextEntry("message", {
      message: message("user", [{ type: "text", text: history.question || `Fixture question for ${title}` }], startedAt + 3000)
    }),
    nextEntry("message", {
      message: assistant(history.answer || `Fixture answer for ${title}`, startedAt + 4000)
    }),
    nextEntry("session_info", { name: title })
  ];
  await writeFile(sessionPath, `${entries.map((entry) => JSON.stringify(entry)).join("\n")}\n`);
  return sessionPath;
}

export async function seedFixtures(root) {
  const home = path.join(root, "home");
  const state = path.join(root, "state");
  const projectsRoot = path.join(root, "projects");
  const sessionsRoot = path.join(root, "sessions");
  const attachmentsRoot = path.join(root, "attachments");
  await Promise.all([
    mkdir(home, { recursive: true }),
    mkdir(state, { recursive: true }),
    mkdir(projectsRoot, { recursive: true }),
    mkdir(sessionsRoot, { recursive: true }),
    mkdir(attachmentsRoot, { recursive: true })
  ]);

  const projectNames = [
    "contract-project",
    "history-project",
    "prompt-project",
    "controls-project",
    "settings-project",
    "extension-project",
    "mobile-project",
    "new-session-desktop",
    "new-session-mobile"
  ];
  const projects = Object.fromEntries(projectNames.map((name) => [name, path.join(projectsRoot, name)]));
  await Promise.all(Object.values(projects).map((project) => mkdir(project, { recursive: true })));

  const definitions = [
    ["contract-project", "contract", sessions.marker, { question: "Contract fixture marker", answer: "The external E2E target is disposable." }],
    ["history-project", "history", sessions.history, { question: "Persisted browser question", answer: "Persisted browser answer" }],
    ["prompt-project", "prompt", sessions.prompt],
    ["controls-project", "steer", sessions.controlsSteer],
    ["controls-project", "follow-up", sessions.controlsFollowUp],
    ["controls-project", "abort", sessions.controlsAbort],
    ["settings-project", "settings", sessions.settings],
    ["extension-project", "extension", sessions.extension],
    ["mobile-project", "mobile", sessions.mobile],
    ["mobile-project", "mobile-landing", sessions.mobileLanding]
  ];
  for (const [index, [projectName, slug, title, history]] of definitions.entries()) {
    await writeSession(root, projects[projectName], slug, title, index + 1, history);
  }

  const configuredCwdsPath = path.join(state, "configured-cwds");
  await writeFile(configuredCwdsPath, `${projects["new-session-desktop"]}\n${projects["new-session-mobile"]}\n`);
  const manifestPath = path.join(root, "fixture.json");
  await writeFile(manifestPath, `${JSON.stringify({ root, projects, marker: sessions.marker }, null, 2)}\n`);

  return {
    root,
    home,
    state,
    projects,
    sessionsRoot,
    attachmentsRoot,
    configuredCwdsPath,
    manifestPath
  };
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  const root = path.resolve(process.argv[2] || ".e2e-fixture");
  console.log(JSON.stringify(await seedFixtures(root), null, 2));
}
