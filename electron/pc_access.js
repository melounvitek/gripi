const crypto = require("node:crypto");
const { once } = require("node:events");
const fs = require("node:fs");
const net = require("node:net");
const os = require("node:os");
const path = require("node:path");
const timersPromises = require("node:timers/promises");

const GRANT_MS = 5 * 60 * 1000;
const DIALOG_TIMEOUT_MS = 2 * 60 * 1000;
const MAX_WAITING_DIALOGS = 5;
const MAX_BACKOFF_MS = 30 * 1000;
const LINK_TAKEN_RETRY_MS = 10 * 1000;
const REFUSED_RETRY_MS = 60 * 1000;
const AGENT_HINT = 'start one with "systemctl --user enable --now ssh-agent" and load your keys with "ssh-add"';

function createPcAccess({
  request,
  confirm,
  authenticate,
  connectAgent = connectLocalAgent,
  readKnownHosts = readLocalKnownHosts,
  now = Date.now,
  sshAgentGuard = require("./ssh_agent_guard")
}) {
  const { createConnectionGuard, findKnownHost, fingerprint, keyType } = sshAgentGuard;
  const gateways = new Map();
  let dialogTurn = Promise.resolve();
  let waitingDialogs = 0;

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
    dropLink(entry.link);
    await request(entry.gateway, "/pc/link/close", { link: entry.link.id }).catch(() => {});
  }

  async function stopAll() {
    await Promise.all([...gateways.keys()].map(stop));
  }

  async function runLink(entry) {
    const { signal } = entry.controller;
    let backoff = 0;
    while (!signal.aborted) {
      const link = entry.link;
      let response;
      try {
        response = await request(entry.gateway, "/pc/link", { link: link.id }, signal);
      } catch (_error) {
        response = null;
      }
      if (signal.aborted) return;

      if (response?.status === 200 && Array.isArray(response.body?.messages)) {
        backoff = 0;
        for (const message of response.body.messages) receive(entry, link, message);
      } else if (response?.status === 409) {
        // Another link holds the gateway, so this one is lost: what it was asked and allowed goes with it.
        dropLink(link);
        entry.link = newLink();
        await sleep(LINK_TAKEN_RETRY_MS, signal);
      } else if (response?.status === 403) {
        await sleep(REFUSED_RETRY_MS, signal);
      } else {
        backoff = Math.min(backoff ? backoff * 2 : 1000, MAX_BACKOFF_MS);
        await sleep(backoff, signal);
      }
    }
  }

  function receive(entry, link, message) {
    if (message?.type === "ssh") {
      receiveSsh(entry, link, message);
    } else if (message?.type === "ssh_close") {
      closeConnection(link, connectionKey(message));
    } else if (message?.type === "cancel") {
      link.requests.get(message.id)?.controller.abort();
    }
  }

  function receiveSsh(entry, link, message) {
    const key = connectionKey(message);
    let connection = link.connections.get(key);
    if (!connection) {
      connection = { guard: createConnectionGuard(), agent: null, queue: Promise.resolve() };
      link.connections.set(key, connection);
    }

    const controller = new AbortController();
    link.requests.set(message.id, { controller, connectionKey: key });
    connection.queue = connection.queue
      .then(() => handleSsh(entry, link, connection, message, controller.signal))
      .catch((error) => console.warn("PC access: could not handle an SSH agent message:", error))
      .finally(() => link.requests.delete(message.id));
  }

  // A request aborted by a cancel, a closed connection or a stopped or lost link gets no reply, as nobody waits for it.
  async function handleSsh(entry, link, connection, message, signal) {
    const reply = async (fields) => {
      if (signal.aborted) return;
      await request(entry.gateway, "/pc/link/reply", { link: link.id, id: message.id, ...fields }).catch(() => {});
    };
    if (signal.aborted) return;

    try {
      const body = Buffer.from(message.message, "base64");
      const decision = connection.guard.check(body);
      if (decision.action === "refuse") return reply({ error: decision.reason });
      if (decision.action === "sign") {
        const refusal = await approveSign(entry, link, connection, message, decision, signal);
        if (refusal) return reply({ error: refusal });
        if (signal.aborted) return;
      }

      connection.agent ||= connectAgent();
      const answer = await (await connection.agent).request(body);
      connection.guard.observe(body, answer);
      await reply({ message: answer.toString("base64") });
    } catch (error) {
      await reply({ error: error.message });
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
    if (waitingDialogs >= MAX_WAITING_DIALOGS) return "denied on the PC: too many requests are waiting for approval there";

    const timeout = new AbortController();
    const timer = setTimeout(() => timeout.abort(), DIALOG_TIMEOUT_MS);
    let answer;
    try {
      answer = await askInTurn({
        gateway: entry.gateway,
        sessionName: escapeForDisplay(message.session_name || message.session),
        key: { type: keyType(key), fingerprint: fingerprint(key), comment: connection.guard.comment(key) },
        server: { names: host.names, hostKeyFingerprint: fingerprint(hostKey) },
        login: escapeForDisplay(user),
        signal: AbortSignal.any([signal, timeout.signal])
      });
    } finally {
      clearTimeout(timer);
    }
    if (timeout.signal.aborted) return "denied on the PC: nobody answered there in 2 minutes";
    if (answer !== "once" && answer !== "grant") return "denied on the PC";

    const auth = await authenticate(signal);
    if (!auth.ok) return `denied on the PC: ${auth.reason}`;
    if (answer === "grant" && !signal.aborted) link.grants.set(grantKey, now() + GRANT_MS);
    return null;
  }

  // Shows one dialog at a time. A request aborted while it waits for its turn is never shown.
  async function askInTurn(prompt) {
    waitingDialogs += 1;
    const answer = dialogTurn.then(() => (prompt.signal.aborted ? "deny" : confirm(prompt)));
    dialogTurn = answer.catch(() => {});
    try {
      return await answer;
    } catch (error) {
      console.warn("PC access: the approval dialog failed:", error);
      return "deny";
    } finally {
      waitingDialogs -= 1;
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

  function dropLink(link) {
    for (const pending of link.requests.values()) pending.controller.abort();
    for (const connection of link.connections.values()) closeAgent(connection);
    link.requests.clear();
    link.connections.clear();
    link.grants.clear();
  }

  return { start, stop, stopAll };
}

function newLink() {
  return { id: crypto.randomBytes(16).toString("hex"), grants: new Map(), connections: new Map(), requests: new Map() };
}

function connectionKey(message) {
  return JSON.stringify([message.session, message.connection]);
}

function closeAgent(connection) {
  connection.agent?.then((agent) => agent.close(), () => {});
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

async function readLocalKnownHosts() {
  const files = [path.join(os.homedir(), ".ssh", "known_hosts"), "/etc/ssh/ssh_known_hosts"];
  const texts = await Promise.all(files.map((file) => fs.promises.readFile(file, "utf8").catch((error) => {
    if (error.code === "ENOENT") return null;
    throw error;
  })));
  return texts.filter((text) => text !== null);
}

module.exports = { createPcAccess };
