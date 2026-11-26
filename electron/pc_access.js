const childProcess = require("node:child_process");
const crypto = require("node:crypto");
const { once } = require("node:events");
const fs = require("node:fs");
const http = require("node:http");
const https = require("node:https");
const net = require("node:net");
const os = require("node:os");
const path = require("node:path");
const timersPromises = require("node:timers/promises");

const GRANT_MS = 5 * 60 * 1000;
const DIALOG_TIMEOUT_MS = 2 * 60 * 1000;
const MAX_WAITING_APPROVALS = 5;
const MAX_AGENT_CONNECTIONS = 32;
const MAX_WAITING_MESSAGES = 8;
const MAX_POLL_MESSAGES = 64;
// Per link, counting refusals that are still being sent and the requests of closed connections until they end.
const MAX_REQUESTS_IN_PROGRESS = 256;
const MAX_FIELD_LENGTH = 4096;
const MAX_AGENT_MESSAGE_BYTES = 256 * 1024;
const MAX_SESSION_NAME_LENGTH = 200;
const MAX_COMMAND_WORDS = 256;
// As the dialog shows a command, which must stay readable there.
const MAX_COMMAND_LENGTH = 4096;
const MAX_OUTPUT_BYTES = 16 * 1024 * 1024;
// After SIGTERM, which lets a command such as Git remove its lock files, before SIGKILL, and sooner as the app quits.
const KILL_DELAY_MS = 5 * 1000;
const QUIT_KILL_DELAY_MS = 1000;
const MAX_RESPONSE_BYTES = 8 * 1024 * 1024;
// Longer than the gateway's 25-second link poll.
const REQUEST_TIMEOUT_MS = 40 * 1000;
const MAX_BACKOFF_MS = 30 * 1000;
const LINK_TAKEN_RETRY_MS = 10 * 1000;
const REFUSED_RETRY_MS = 60 * 1000;
const AGENT_HINT = 'start one with "systemctl --user enable --now ssh-agent" and load your keys with "ssh-add"';
// Runs pkexec, whose path comes first, for a command as root, in the folder, which pkexec keeps, and without input. The
// wrapper runs as the user. When the app's pipe on its input closes, as the app stops the command or ends, also when it
// crashes, the wrapper stops pkexec, and with it polkit's dialog, which pkexec, being setuid, wouldn't do on its own.
// That works only while pkexec waits, as it runs as the user until polkit authorizes it, and then runs the command as
// root. pkexec closes descriptor 3 when it runs the command or exits, which tells the app that polkit's dialog ended,
// and the app then sends a line, after which the watchdog ends without stopping anything. The wrapper waits for it
// rather than signal it, as shells reap it meanwhile, so that its process ID may be another's by then.
const ROOT_WRAPPER = `exec 4<&0 </dev/null
pkexec=$1
shift
"$pkexec" --disable-internal-agent --keep-cwd "$@" 4<&- &
pid=$!
exec 3>&-
{ read -r _ <&4 || kill "$pid"; } >/dev/null 2>&1 &
watchdog=$!
exec 4<&-
# Some shells note there that pkexec was stopped, which isn't the command's output.
wait "$pid" 2>/dev/null
code=$?
wait "$watchdog"
exit "$code"`;

function createPcAccess({
  request,
  confirm,
  authenticate = authenticateWithPolkit,
  connectAgent = connectLocalAgent,
  readKnownHosts = readLocalKnownHosts,
  now = Date.now,
  hostname = os.hostname,
  spawn = childProcess.spawn,
  sshAgentGuard = require("./ssh_agent_guard")
}) {
  const { createConnectionGuard, findKnownHost, fingerprint, keyType } = sshAgentGuard;
  const gateways = new Map();
  // What signals the process groups of the commands that were stopped, until they get SIGKILL.
  const stopping = new Set();
  let approvalTurn = Promise.resolve();
  let waitingApprovals = 0;

  function start(gateway) {
    const running = gateways.get(gateway.id);
    if (running) {
      running.gateway = gateway;
      return;
    }

    const entry = { gateway, controller: new AbortController(), link: newLink() };
    gateways.set(gateway.id, entry);
    runLink(entry);
  }

  async function stop(id) {
    const entry = gateways.get(id);
    if (!entry) return;

    gateways.delete(id);
    entry.controller.abort();
    await dropLink(entry, entry.link);
  }

  // The app quits before SIGKILL's 5 seconds, so the process groups of stopped commands get it after a second.
  async function stopAll() {
    const stopped = Promise.all([...gateways.keys()].map(stop));
    if (stopping.size) await sleep(QUIT_KILL_DELAY_MS);
    for (const kill of stopping) kill("SIGKILL");
    stopping.clear();
    await stopped;
  }

  async function runLink(entry) {
    const { signal } = entry.controller;
    let backoff = 0;
    while (!signal.aborted) {
      const link = entry.link;
      // The gateway refuses a poll that doesn't open the link once it no longer has it, such as after a restart.
      const body = link.opened ? { link: link.id } : { link: link.id, open: true, name: computerName(hostname()), run: true, root: true };
      link.opened = true;
      let response;
      try {
        response = await request(entry.gateway, "/pc/link", body, link.controller.signal);
      } catch (_error) {
        response = null;
      }
      if (signal.aborted) return;

      const messages = response?.status === 200 ? response.body?.messages : null;
      if (Array.isArray(messages) && messages.length <= MAX_POLL_MESSAGES && withinBudget(link, messages)) {
        for (const message of messages) receive(entry, link, message);
        backoff = 0;
        continue;
      }

      // Any failed poll may have lost the link, for example to another app or a restarted gateway, so what it was
      // asked and allowed goes with it. So does a poll with more messages than a link may take or have in progress.
      entry.link = newLink();
      dropLink(entry, link);
      if (response?.status === 409) {
        await sleep(LINK_TAKEN_RETRY_MS, signal);
      } else if (response?.status === 403) {
        await sleep(REFUSED_RETRY_MS, signal);
      } else {
        backoff = Math.min(backoff ? backoff * 2 : 1000, MAX_BACKOFF_MS);
        await sleep(backoff, signal);
      }
    }
  }

  // A batch that would take the link over the requests it may have in progress drops the link before any of them starts.
  function withinBudget(link, messages) {
    const requests = messages.filter((message) => message?.type === "ssh" || message?.type === "run").length;
    return link.requests.size + link.refusals + requests <= MAX_REQUESTS_IN_PROGRESS;
  }

  function receive(entry, link, message) {
    if (!validMessage(message)) {
      console.warn("PC access: ignored a malformed message from the server");
    } else if (message.type === "ssh") {
      receiveSsh(entry, link, message);
    } else if (message.type === "run") {
      receiveRun(entry, link, message);
    } else if (message.type === "ssh_close") {
      closeConnection(link, connectionKey(message));
    } else {
      link.requests.get(message.id)?.controller.abort();
    }
  }

  function refuseRequest(entry, link, id, error) {
    link.refusals += 1;
    reply(entry, link, id, { error }, link.controller.signal).finally(() => { link.refusals -= 1; });
  }

  function receiveSsh(entry, link, message) {
    const key = connectionKey(message);
    let connection = link.connections.get(key);
    const refuse = (error) => refuseRequest(entry, link, message.id, error);
    if (link.requests.has(message.id)) return refuse("another request with this ID is in progress on the PC");
    if (!connection && link.connections.size >= MAX_AGENT_CONNECTIONS) return refuse("too many SSH agent connections are open on the PC");
    if (connection?.waiting >= MAX_WAITING_MESSAGES) return refuse("too many SSH agent messages wait on this connection on the PC");

    if (!connection) {
      connection = { guard: createConnectionGuard(), agent: null, queue: Promise.resolve(), waiting: 0 };
      link.connections.set(key, connection);
    }
    const controller = new AbortController();
    const signal = AbortSignal.any([link.controller.signal, controller.signal]);
    link.requests.set(message.id, { controller, connectionKey: key });
    connection.waiting += 1;
    connection.queue = connection.queue
      .then(() => handleSsh(entry, link, connection, message, signal))
      .catch((error) => console.warn("PC access: could not handle an SSH agent message:", error))
      .finally(() => {
        connection.waiting -= 1;
        link.requests.delete(message.id);
      });
  }

  // A request aborted by a cancel, a closed connection or a stopped or lost link gets no reply, as nobody waits for it.
  // Returns the status of the server's answer, or null without one.
  async function reply(entry, link, id, fields, signal) {
    if (signal.aborted) return null;
    return request(entry.gateway, "/pc/link/reply", { link: link.id, id, ...fields }, signal).then(({ status }) => status, () => null);
  }

  async function handleSsh(entry, link, connection, message, signal) {
    const answerWith = (fields) => reply(entry, link, message.id, fields, signal);
    if (signal.aborted) return;

    try {
      const body = Buffer.from(message.message, "base64");
      const decision = connection.guard.check(body);
      if (decision.action === "refuse") return answerWith({ error: decision.reason });
      if (decision.action === "sign") {
        const refusal = await approveSign(entry, link, connection, message, decision, signal);
        if (refusal) return answerWith({ error: refusal });
        if (signal.aborted) return;
      }

      connection.agent ||= connectAgent();
      const agent = await connection.agent;
      if (signal.aborted) return;
      const answer = await agent.request(body);
      connection.guard.observe(body, answer);
      await answerWith({ message: answer.toString("base64") });
    } catch (error) {
      await answerWith({ error: error.message });
    }
  }

  async function approveSign(entry, link, connection, message, { key, hostKey, user }, signal) {
    const host = findKnownHost(hostKey, await readKnownHosts());
    const hostKeyName = `${keyType(hostKey)} ${fingerprint(hostKey)}`;
    if (host.status === "revoked") return `the server's host key ${hostKeyName} is marked @revoked in the PC's known_hosts`;
    if (host.status !== "known") {
      return `the server's host key ${hostKeyName} is not in the PC's known_hosts; add it to ~/.ssh/known_hosts on the PC, for example by connecting to the server once from the PC with ssh and checking that fingerprint`;
    }

    const grantKey = JSON.stringify([message.session, fingerprint(key), fingerprint(hostKey), user]);
    const granted = () => link.grants.get(grantKey) > now();
    if (granted()) return null;

    const login = escapeForDisplay(user);
    return approve(entry, link, message, signal, {
      prompt: {
        kind: "ssh",
        key: { type: keyType(key), fingerprint: fingerprint(key), comment: connection.guard.comment(key) },
        server: { names: host.names, hostKeyFingerprint: fingerprint(hostKey) },
        login
      },
      summary: `SSH login to ${host.names[0] || "a known host"} as ${login}`,
      answers: ["once", "grant"],
      password: true,
      covered: granted,
      approved: (answer) => {
        if (answer === "grant" && !signal.aborted) link.grants.set(grantKey, now() + GRANT_MS);
        return null;
      }
    });
  }

  // Asks the user to approve a request in the approval turn, unless covered says that it needs no approval, and with
  // password, to confirm it in polkit's password dialog, and returns a refusal, or null once they approved it. approved
  // runs in the turn once they did, and may still refuse it.
  async function approve(entry, link, message, signal, { prompt, summary, answers, password = false, covered = () => false, approved }) {
    if (waitingApprovals >= MAX_WAITING_APPROVALS) return "denied on the PC: too many requests are waiting for approval there";

    const name = [...escapeForDisplay(message.session_name || message.session)];
    const sessionName = name.length > MAX_SESSION_NAME_LENGTH ? `${name.slice(0, MAX_SESSION_NAME_LENGTH - 1).join("")}…` : name.join("");
    return inTurn(async () => {
      // A request cancelled while it waited for its turn needs no dialog, nor one that a grant made meanwhile covers.
      if (signal.aborted) return "denied on the PC";
      if (covered()) return null;

      const timeout = new AbortController();
      const timer = setTimeout(() => timeout.abort(), DIALOG_TIMEOUT_MS);
      const approval = AbortSignal.any([signal, timeout.signal]);
      // Gripi shows that the PC asks on the user's other devices, until the dialog and polkit's have ended.
      status(entry, link, message.id, { asking: true, summary, expires_in: DIALOG_TIMEOUT_MS / 1000 }, signal);
      try {
        const answer = await confirm({ ...prompt, gateway: entry.gateway, sessionName, signal: approval }).catch((error) => {
          console.warn("PC access: the approval dialog failed:", error);
          return "deny";
        });
        if (timeout.signal.aborted) return "denied on the PC: nobody answered there in 2 minutes";
        if (!answers.includes(answer) || approval.aborted) return "denied on the PC";

        if (password) {
          const auth = await authenticate(approval);
          if (timeout.signal.aborted) return "denied on the PC: nobody answered there in 2 minutes";
          if (!auth.ok) return `denied on the PC: ${auth.reason}`;
          if (approval.aborted) return "denied on the PC";
        }
        return await approved(answer, approval);
      } finally {
        clearTimeout(timer);
        status(entry, link, message.id, { asking: false }, signal);
      }
    });
  }

  function receiveRun(entry, link, message) {
    if (link.requests.has(message.id)) return refuseRequest(entry, link, message.id, "another request with this ID is in progress on the PC");

    const controller = new AbortController();
    const signal = AbortSignal.any([link.controller.signal, controller.signal]);
    link.requests.set(message.id, { controller });
    handleRun(entry, link, message, signal)
      .catch((error) => console.warn("PC access: could not handle a command:", error))
      .finally(() => link.requests.delete(message.id));
  }

  async function handleRun(entry, link, message, signal) {
    const send = (fields) => reply(entry, link, message.id, fields, signal);
    // Once the server may have let the command start, gripi pc run waits for an answer however long it takes, so an
    // answer that doesn't reach Gripi then ends the link, which ends the wait.
    let starting = false;
    const answerWith = async (fields) => {
      const status = await send(fields);
      if (starting && !signal.aborted && ![200, 404, 409].includes(status)) link.controller.abort();
    };
    try {
      const root = message.root === true;
      const folder = message.cwd ?? os.homedir();
      if (!path.isAbsolute(folder) || !(await fs.promises.stat(folder).catch(() => null))?.isDirectory()) {
        return await answerWith({ error: `the PC has no folder ${escapeForDisplay(folder)}` });
      }
      const [name, ...args] = message.argv;
      const executable = await findExecutable(name, folder);
      if (!executable) return await answerWith({ error: `the PC has no command ${escapeForDisplay(name)}${name.includes("/") ? "" : " on its PATH"}` });
      const command = commandLine([executable, ...args]);
      if (command.length > MAX_COMMAND_LENGTH) return await answerWith({ error: "the command is too long to show on the PC" });
      const pkexec = root && await findExecutable("pkexec", folder);
      if (root && !pkexec) return await answerWith({ error: "running commands as root on the PC needs polkit's pkexec" });

      let running;
      const refusal = await approve(entry, link, message, signal, {
        prompt: { kind: "run", command, folder: escapeForDisplay(folder), root },
        summary: root ? `run a command as root: ${command}` : command,
        answers: ["run"],
        // Right before the command runs, the server confirms that gripi pc run still waits for it, so Cancel in Gripi
        // either stopped it or no longer can. A command as root runs once polkit's dialog, which ends the turn, authorizes
        // it, so the server hears it before that dialog shows.
        approved: async (_answer, approval) => {
          starting = true;
          const started = await request(entry.gateway, "/pc/link/status", { link: link.id, id: message.id, starting: true }, approval).catch(() => null);
          if (started?.status !== 200) return "not run on the PC: Gripi no longer waits for it";
          if (!root) return null;
          if (approval.aborted) return "denied on the PC";
          running = runCommand(executable, args, folder, approval, pkexec);
          await running.asked;
          return null;
        }
      });
      if (refusal) return await answerWith({ error: refusal });
      if (signal.aborted) return;

      const run = running ?? runCommand(executable, args, folder, signal);
      const result = await run.result;
      if (signal.aborted) return;
      const failure = root && pkexecFailure(result, executable, run.stopped);
      if (failure) return await answerWith({ error: failure });
      if (await send({ result }) === 200) return;
      await answerWith({ error: "the command ran on the PC, but its result could not reach Gripi" });
    } catch (error) {
      await answerWith({ error: error.message });
    }
  }

  // Runs an approved command without a shell or input, in a process group of its own, which signal ends: as the user,
  // with the app's environment, or with pkexec as root, once polkit's dialog authorizes it. Returns its result, and for
  // root, when polkit's dialog has ended, and whether signal stopped pkexec. The app can't stop a command as root, so
  // signal only closes the wrapper's pipe, which stops pkexec while it waits, and nothing waits to kill it as the app
  // quits.
  function runCommand(executable, args, folder, signal, pkexec) {
    const child = pkexec
      ? spawn("/bin/sh", ["-c", ROOT_WRAPPER, "sh", pkexec, executable, ...args], { cwd: folder, stdio: ["pipe", "pipe", "pipe", "pipe"], detached: true })
      : spawn(executable, args, { cwd: folder, stdio: ["ignore", "pipe", "pipe"], detached: true });
    const run = { stopped: false };
    run.result = new Promise((resolve, reject) => {
      const outputs = [child.stdout, child.stderr].map(capture);
      const kill = (name) => {
        try {
          process.kill(-child.pid, name);
        } catch (_error) {
          // The group has ended, or never started.
        }
      };
      // The group of a command as the user may outlive its first process, such as one that it started in the background.
      const end = pkexec ? () => {
        run.stopped = true;
        child.stdin.destroy();
      } : () => {
        kill("SIGTERM");
        stopping.add(kill);
        setTimeout(() => {
          kill("SIGKILL");
          stopping.delete(kill);
        }, KILL_DELAY_MS).unref();
      };
      let failure;
      signal.addEventListener("abort", end, { once: true });
      child.on("error", (error) => {
        failure ??= error;
      });
      child.on("close", (code, name) => {
        signal.removeEventListener("abort", end);
        if (failure) return reject(new Error(`could not run the command on the PC: ${failure.message}`));
        const [stdout, stderr] = outputs.map((output) => Buffer.concat(output.chunks).toString("base64"));
        resolve({ exit_code: code ?? 128 + os.constants.signals[name], stdout, stderr, truncated: outputs.some((output) => output.truncated) });
      });
    });
    if (!pkexec) return run;

    // The wrapper may have ended before the app writes to it.
    child.stdin.on("error", () => {});
    run.asked = Promise.race([new Promise((resolve) => child.stdio[3].on("close", resolve).resume()), run.result.catch(() => {})]).then(() => {
      if (!run.stopped) child.stdin.end("\n");
    });
    return run;
  }

  // Tells the server whether the PC asks the user to approve a request. The dialog doesn't wait for it, and the server
  // keeps a start that comes after the stop from showing.
  function status(entry, link, id, fields, signal) {
    if (!signal.aborted) request(entry.gateway, "/pc/link/status", { link: link.id, id, ...fields }, signal).catch(() => {});
  }

  // Runs one approval at a time, from its dialog to the end of polkit's, if any, so that one never covers the other.
  async function inTurn(approve) {
    waitingApprovals += 1;
    const approval = approvalTurn.then(approve);
    approvalTurn = approval.catch(() => {});
    try {
      return await approval;
    } finally {
      waitingApprovals -= 1;
    }
  }

  function closeConnection(link, key) {
    const connection = link.connections.get(key);
    if (!connection) return;

    link.connections.delete(key);
    for (const pending of link.requests.values()) {
      if (pending.connectionKey === key) pending.controller.abort();
    }
    closeAgent(connection);
  }

  // Releases the link on the gateway too, so the next one needn't wait for it to go stale.
  function dropLink(entry, link) {
    link.controller.abort();
    for (const connection of link.connections.values()) closeAgent(connection);
    link.requests.clear();
    link.connections.clear();
    link.grants.clear();
    return request(entry.gateway, "/pc/link/close", { link: link.id }).catch(() => {});
  }

  return { start, stop, stopAll };
}

function newLink() {
  return {
    id: crypto.randomBytes(16).toString("hex"),
    opened: false,
    controller: new AbortController(),
    grants: new Map(),
    connections: new Map(),
    requests: new Map(),
    refusals: 0
  };
}

// The gateway may be compromised, so its messages are checked before anything acts on them.
function validMessage(message) {
  const text = (value) => typeof value === "string" && value.length <= MAX_FIELD_LENGTH;
  const field = (value) => text(value) && value !== "";
  switch (message?.type) {
    case "ssh":
      return field(message.id) && field(message.session) && field(message.connection) &&
        (message.session_name === undefined || text(message.session_name)) && validAgentMessage(message.message);
    case "run":
      return field(message.id) && field(message.session) && (message.session_name === undefined || text(message.session_name)) &&
        Array.isArray(message.argv) && message.argv.length > 0 && message.argv.length <= MAX_COMMAND_WORDS &&
        message.argv.every(text) && message.argv[0] !== "" && (message.cwd === undefined || field(message.cwd)) &&
        (message.root === undefined || typeof message.root === "boolean");
    case "ssh_close":
      return field(message.session) && field(message.connection);
    case "cancel":
      return field(message.id);
    default:
      return false;
  }
}

function validAgentMessage(text) {
  if (typeof text !== "string") return false;
  const body = Buffer.from(text, "base64");
  // Node's decoder skips what isn't base64, so only text that encodes back the same is.
  return body.length > 0 && body.length <= MAX_AGENT_MESSAGE_BYTES && body.toString("base64") === text;
}

function connectionKey(message) {
  return JSON.stringify([message.session, message.connection]);
}

function closeAgent(connection) {
  connection.agent?.then((agent) => agent.close(), () => {});
}

// Node's client, unlike Electron's, sends no Sec-Fetch-* headers (the gateway refuses requests that have them), and never
// follows a redirect, which would take the cookies and the link header elsewhere.
function linkRequest(url, body, { cookie, signal } = {}) {
  const payload = JSON.stringify(body);
  const headers = { "Content-Type": "application/json", "Content-Length": Buffer.byteLength(payload), "X-Gripi-PC-Link": "1" };
  if (cookie) headers.Cookie = cookie;

  let timer;
  return new Promise((resolve, reject) => {
    const client = url.protocol === "https:" ? https : http;
    const request = client.request(url, { method: "POST", headers, signal }, (response) => {
      const chunks = [];
      let size = 0;
      response.on("data", (chunk) => {
        size += chunk.length;
        if (size > MAX_RESPONSE_BYTES) request.destroy(new Error("the server's answer is too large"));
        else chunks.push(chunk);
      });
      response.on("error", reject);
      response.on("end", () => {
        try {
          resolve({ status: response.statusCode, body: JSON.parse(Buffer.concat(chunks).toString("utf8")) });
        } catch (_error) {
          resolve({ status: response.statusCode, body: null });
        }
      });
    });
    // A deadline for the whole request, as an idle timeout wouldn't stop an answer that keeps trickling in.
    timer = setTimeout(() => request.destroy(new Error("the server did not answer in time")), REQUEST_TIMEOUT_MS);
    request.on("error", reject);
    request.end(payload);
  }).finally(() => clearTimeout(timer));
}

function sleep(ms, signal) {
  return timersPromises.setTimeout(ms, undefined, { signal }).catch(() => {});
}

// The computer's short hostname, which Gripi names it by, on one line and in at most 64 characters, as the gateway takes
// it.
function computerName(hostname) {
  return [...hostname.split(".")[0].replace(/[^\p{L}\p{M}\p{N}\p{P}\p{S}\p{Zs}]/gu, "")].slice(0, 64).join("");
}

// Like the gateway's shellWords, so a session name or login can't hide or reorder what the dialog shows.
function escapeForDisplay(text) {
  return String(text).replace(/[\p{Cc}\p{Cf}\u2028\u2029]/gu, (char) => `\\u${char.codePointAt(0).toString(16).padStart(4, "0")}`);
}

// Keeps the first 16 MiB of an output and drops the rest.
function capture(stream) {
  const output = { chunks: [], size: 0, truncated: false };
  stream.on("data", (chunk) => {
    const kept = chunk.subarray(0, MAX_OUTPUT_BYTES - output.size);
    output.truncated ||= kept.length < chunk.length;
    output.size += kept.length;
    // An empty view would still keep the chunk.
    if (kept.length) output.chunks.push(kept);
  });
  return output;
}

// Finds the file that a command names as a shell would: one with a slash in the folder, and others on the app's PATH,
// whose relative folders it skips.
async function findExecutable(name, folder) {
  const candidates = name.includes("/")
    ? [path.resolve(folder, name)]
    : (process.env.PATH || "").split(path.delimiter).filter((directory) => path.isAbsolute(directory)).map((directory) => path.join(directory, name));
  for (const candidate of candidates) {
    const file = (await fs.promises.stat(candidate).catch(() => null))?.isFile();
    if (file && await fs.promises.access(candidate, fs.constants.X_OK).then(() => true, () => false)) return candidate;
  }
  return null;
}

// Says why pkexec didn't run a command as root: 126 or 127 with its words when polkit's dialog was dismissed, polkit
// refused or failed, or pkexec couldn't run the file, and 143 without output once the 2 minutes stopped it. Without a
// privileged helper, Gripi can't tell these apart from a command's own exits for sure, but those rarely come without
// output and with pkexec's words. Returns null for the command's own.
function pkexecFailure({ exit_code: code, stdout, stderr, truncated }, executable, stopped) {
  const said = Buffer.from(stderr, "base64").toString("utf8").trimEnd();
  if (stdout || truncated) return null;
  if (stopped && code === 128 + os.constants.signals.SIGTERM && !said) return "denied on the PC: nobody answered there in 2 minutes";
  const words = [
    "Error executing command as another user: ",
    "Error checking for authorization ",
    "Error getting authority: ",
    "pkexec must be setuid root",
    "Refusing to render service to dead parents.",
    ...["Cannot run program", "Error accessing", "Error executing"].map((start) => `${start} ${executable}: `)
  ];
  if ((code === 126 || code === 127) && words.some((start) => said.startsWith(start))) return `not run as root on the PC: pkexec: ${said}`;
  return null;
}

// Like the gateway's shellWords, so the dialog shows the command as a shell would take it, with nothing hidden.
function commandLine(words) {
  return words.map((word) => (/^[A-Za-z0-9_@%+=:,./-]+$/.test(word) ? word : escapeForDisplay(JSON.stringify(word)))).join(" ");
}

async function connectLocalAgent() {
  const fallback = process.env.XDG_RUNTIME_DIR && path.join(process.env.XDG_RUNTIME_DIR, "ssh-agent.socket");
  const socketPath = process.env.SSH_AUTH_SOCK || (fallback && fs.existsSync(fallback) ? fallback : null);
  if (!socketPath) throw new Error(`the PC has no SSH agent; ${AGENT_HINT}`);

  const socket = net.createConnection(socketPath);
  const agent = agentClient(socket);
  try {
    await once(socket, "connect");
  } catch (error) {
    throw new Error(`could not reach the PC's SSH agent at ${socketPath} (${error.code || error.message}); ${AGENT_HINT}`);
  }
  return agent;
}

function agentClient(socket) {
  const waiting = [];
  let buffered = Buffer.alloc(0);
  const fail = (error) => {
    for (const pending of waiting.splice(0)) pending.reject(error);
  };

  socket.on("data", (chunk) => {
    buffered = Buffer.concat([buffered, chunk]);
    while (buffered.length >= 4 && buffered.length >= 4 + buffered.readUInt32BE(0)) {
      const end = 4 + buffered.readUInt32BE(0);
      waiting.shift()?.resolve(buffered.subarray(4, end));
      buffered = buffered.subarray(end);
    }
  });
  socket.on("error", fail);
  socket.on("close", () => fail(new Error("the PC's SSH agent closed the connection")));

  return {
    request(body) {
      return new Promise((resolve, reject) => {
        if (socket.destroyed) {
          reject(new Error("the PC's SSH agent closed the connection"));
          return;
        }
        waiting.push({ resolve, reject });
        const length = Buffer.alloc(4);
        length.writeUInt32BE(body.length);
        socket.write(Buffer.concat([length, body]));
      });
    },
    close() {
      socket.destroy();
    }
  };
}

// polkit's own dialog confirms that it is the user, so Gripi never asks for the password itself. Node reports a cancel
// as it signals pkexec, so the check ends only once pkexec has exited, and with it polkit's dialog.
function authenticateWithPolkit(signal, spawn = childProcess.spawn) {
  return new Promise((resolve) => {
    const child = spawn("pkexec", ["--disable-internal-agent", "/usr/bin/true"], { stdio: "ignore", signal });
    let failure;
    child.on("error", (error) => {
      failure ??= error;
    });
    child.on("close", (code) => {
      if (failure) resolve({ ok: false, reason: failure.code === "ENOENT" ? "PC access needs polkit's pkexec" : failure.message });
      else if (code === 0) resolve({ ok: true });
      else resolve({ ok: false, reason: code === 126 ? "the password dialog was cancelled" : `pkexec failed with exit code ${code}` });
    });
  });
}

async function readLocalKnownHosts() {
  const files = [path.join(os.homedir(), ".ssh", "known_hosts"), "/etc/ssh/ssh_known_hosts"];
  const texts = await Promise.all(files.map((file) => fs.promises.readFile(file, "utf8").catch((error) => {
    if (error.code === "ENOENT") return null;
    throw error;
  })));
  return texts.filter((text) => text !== null);
}

module.exports = { authenticateWithPolkit, createPcAccess, linkRequest };
