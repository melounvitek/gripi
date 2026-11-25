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
const MAX_RESPONSE_BYTES = 8 * 1024 * 1024;
// Longer than the gateway's 25-second link poll.
const REQUEST_TIMEOUT_MS = 40 * 1000;
const MAX_BACKOFF_MS = 30 * 1000;
const LINK_TAKEN_RETRY_MS = 10 * 1000;
const REFUSED_RETRY_MS = 60 * 1000;
const AGENT_HINT = 'start one with "systemctl --user enable --now ssh-agent" and load your keys with "ssh-add"';

function createPcAccess({
  request,
  confirm,
  authenticate = authenticateWithPolkit,
  connectAgent = connectLocalAgent,
  readKnownHosts = readLocalKnownHosts,
  now = Date.now,
  sshAgentGuard = require("./ssh_agent_guard")
}) {
  const { createConnectionGuard, findKnownHost, fingerprint, keyType } = sshAgentGuard;
  const gateways = new Map();
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

  async function stopAll() {
    await Promise.all([...gateways.keys()].map(stop));
  }

  async function runLink(entry) {
    const { signal } = entry.controller;
    let backoff = 0;
    while (!signal.aborted) {
      const link = entry.link;
      // The gateway refuses a poll that doesn't open the link once it no longer has it, such as after a restart.
      const body = link.opened ? { link: link.id } : { link: link.id, open: true };
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
    const requests = messages.filter((message) => message?.type === "ssh").length;
    return link.requests.size + link.refusals + requests <= MAX_REQUESTS_IN_PROGRESS;
  }

  function receive(entry, link, message) {
    if (!validMessage(message)) {
      console.warn("PC access: ignored a malformed message from the server");
    } else if (message.type === "ssh") {
      receiveSsh(entry, link, message);
    } else if (message.type === "ssh_close") {
      closeConnection(link, connectionKey(message));
    } else {
      link.requests.get(message.id)?.controller.abort();
    }
  }

  function receiveSsh(entry, link, message) {
    const key = connectionKey(message);
    let connection = link.connections.get(key);
    const refuse = (error) => {
      link.refusals += 1;
      reply(entry, link, message.id, { error }, link.controller.signal).finally(() => { link.refusals -= 1; });
    };
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
  async function reply(entry, link, id, fields, signal) {
    if (signal.aborted) return;
    await request(entry.gateway, "/pc/link/reply", { link: link.id, id, ...fields }, signal).catch(() => {});
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
    if (link.grants.get(grantKey) > now()) return null;
    if (waitingApprovals >= MAX_WAITING_APPROVALS) return "denied on the PC: too many requests are waiting for approval there";

    const sessionName = [...escapeForDisplay(message.session_name || message.session)];
    const prompt = {
      gateway: entry.gateway,
      sessionName: sessionName.length > MAX_SESSION_NAME_LENGTH
        ? `${sessionName.slice(0, MAX_SESSION_NAME_LENGTH - 1).join("")}…`
        : sessionName.join(""),
      key: { type: keyType(key), fingerprint: fingerprint(key), comment: connection.guard.comment(key) },
      server: { names: host.names, hostKeyFingerprint: fingerprint(hostKey) },
      login: escapeForDisplay(user)
    };
    return inTurn(async () => {
      const timeout = new AbortController();
      const timer = setTimeout(() => timeout.abort(), DIALOG_TIMEOUT_MS);
      const approval = AbortSignal.any([signal, timeout.signal]);
      try {
        const answer = approval.aborted ? "deny" : await confirm({ ...prompt, signal: approval }).catch((error) => {
          console.warn("PC access: the approval dialog failed:", error);
          return "deny";
        });
        if (timeout.signal.aborted) return "denied on the PC: nobody answered there in 2 minutes";
        if ((answer !== "once" && answer !== "grant") || approval.aborted) return "denied on the PC";

        const auth = await authenticate(approval);
        if (timeout.signal.aborted) return "denied on the PC: nobody answered there in 2 minutes";
        if (!auth.ok) return `denied on the PC: ${auth.reason}`;
        if (answer === "grant" && !signal.aborted) link.grants.set(grantKey, now() + GRANT_MS);
        return null;
      } finally {
        clearTimeout(timer);
      }
    });
  }

  // Runs one approval at a time, from its dialog to the end of polkit's, so that one never covers the other.
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

// Like the gateway's shellWords, so a session name or login can't hide or reorder what the dialog shows.
function escapeForDisplay(text) {
  return String(text).replace(/[\p{Cc}\p{Cf}\u2028\u2029]/gu, (char) => `\\u${char.codePointAt(0).toString(16).padStart(4, "0")}`);
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
