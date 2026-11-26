const test = require("node:test");
const assert = require("node:assert/strict");
const { execFileSync, spawn } = require("node:child_process");
const { once } = require("node:events");
const fs = require("node:fs");
const http = require("node:http");
const os = require("node:os");
const path = require("node:path");
const { authenticateWithPolkit, createPcAccess, linkRequest } = require("./pc_access");

const GATEWAY = { id: "home", name: "home-server", url: "https://home.example.test/" };
const SESSION = "/home/vitek/.pi/sessions/a.jsonl";
const LIST = Buffer.from([11]).toString("base64");
const FIVE_MINUTES = 5 * 60 * 1000;

// The fake guard forwards [11], asks to sign for [13, JSON], and refuses anything else.
const fakeGuard = {
  createConnectionGuard() {
    let listed = false;
    return {
      check(body) {
        if (body[0] === 11) return { action: "forward" };
        if (body[0] !== 13) return { action: "refuse", reason: `agent request ${body[0]} not allowed` };

        const { key, host, user } = JSON.parse(body.subarray(1));
        return { action: "sign", key: Buffer.from(key), hostKey: Buffer.from(host), user };
      },
      observe(request) {
        if (request[0] === 11) listed = true;
      },
      comment: (key) => (listed ? `${key}@laptop` : "")
    };
  },
  findKnownHost(hostKey, texts) {
    const lines = texts.join("\n").split("\n");
    if (lines.includes(`@revoked ${hostKey}`)) return { status: "revoked" };
    const line = lines.find((candidate) => candidate.endsWith(` ${hostKey}`));
    return line ? { status: "known", names: [line.split(" ")[0]] } : { status: "unknown" };
  },
  fingerprint: (blob) => `SHA256:${blob}`,
  keyType: () => "ssh-ed25519"
};

function sign({ key = "key-a", host = "host-gh", user = "git" } = {}) {
  return Buffer.concat([Buffer.from([13]), Buffer.from(JSON.stringify({ key, host, user }))]).toString("base64");
}

function ssh(id, message, fields = {}) {
  return { type: "ssh", id, session: SESSION, session_name: "Fix login redirect", connection: "c1", message, ...fields };
}

function run(id, argv, fields = {}) {
  return { type: "run", id, session: SESSION, session_name: "Fix login redirect", argv, ...fields };
}

const SH = execFileSync("sh", ["-c", "command -v sh"], { encoding: "utf8" }).trim();

// Whether a process has ended, which it has too while it waits for its parent to reap it.
function ended(pid) {
  try {
    return fs.readFileSync(`/proc/${pid}/stat`, "utf8").split(") ").at(-1).startsWith("Z");
  } catch (_error) {
    return true;
  }
}

function base64(text) {
  return Buffer.from(text).toString("base64");
}

function agentReply(message) {
  return Buffer.concat([Buffer.from("re:"), Buffer.from(message, "base64")]).toString("base64");
}

async function settle() {
  for (let round = 0; round < 5; round++) await new Promise(setImmediate);
}

function setup(t, { authenticate = async () => ({ ok: true }), holdReplies = false, hostname = "laptop.home.lan", starting = 200, lostResults = false, lostReplies = false } = {}) {
  const harness = { polls: [], posts: [], prompts: [], agents: [], spawned: [], auths: 0, time: 0 };
  const warn = t.mock.method(console, "warn", () => {});
  harness.warnings = () => warn.mock.calls.map((call) => call.arguments.join(" "));
  harness.pc = createPcAccess({
    request(_gateway, path, body, signal) {
      if (path !== "/pc/link") {
        harness.posts.push({ path, body });
        if (holdReplies && path === "/pc/link/reply") return new Promise(() => {});
        if (body.starting && starting === "late") return new Promise((resolve) => { harness.started = () => resolve({ status: 200, body: {} }); });
        if (body.starting && starting === "held") return new Promise((_resolve, reject) => signal.addEventListener("abort", () => reject(new Error("aborted"))));
        if (body.starting) return starting ? Promise.resolve({ status: starting, body: {} }) : Promise.reject(new Error("the server did not answer in time"));
        if (lostResults && body.result || lostReplies && path === "/pc/link/reply") return Promise.reject(new Error("the server did not answer in time"));
        return Promise.resolve({ status: 200, body: {} });
      }
      return new Promise((resolve, reject) => {
        const poll = { body, resolve, reject };
        harness.polls.push(poll);
        signal.addEventListener("abort", () => {
          harness.polls = harness.polls.filter((pending) => pending !== poll);
          reject(new Error("aborted"));
        });
      });
    },
    // Like Electron's message box, an aborted dialog answers with its cancel button.
    confirm: (prompt) => new Promise((resolve) => {
      harness.prompts.push({ ...prompt, answer: resolve });
      prompt.signal.addEventListener("abort", () => resolve("deny"));
    }),
    authenticate(signal) {
      harness.auths += 1;
      return authenticate(signal);
    },
    async connectAgent() {
      const agent = {
        requests: [],
        closed: false,
        async request(body) {
          agent.requests.push(body.toString("base64"));
          return Buffer.concat([Buffer.from("re:"), body]);
        },
        close() {
          agent.closed = true;
        }
      };
      harness.agents.push(agent);
      return agent;
    },
    readKnownHosts: async () => ["github.com host-gh\ngitlab.com host-gl", "@revoked host-bad"],
    now: () => harness.time,
    hostname: () => hostname,
    spawn(command, args, options) {
      const child = spawn(command, args, options);
      harness.spawned.push({ command, args, options, child, printed: once(child.stdout, "data"), closed: new Promise((resolve) => child.once("close", resolve)) });
      return child;
    },
    sshAgentGuard: fakeGuard
  });
  harness.link = () => harness.polls.at(-1).body.link;
  harness.replies = () => harness.posts.filter((post) => post.path === "/pc/link/reply").map((post) => post.body);
  harness.statuses = () => harness.posts.filter((post) => post.path === "/pc/link/status").map((post) => post.body);
  harness.deliver = async (...messages) => {
    harness.polls.shift().resolve({ status: 200, body: { messages } });
    await settle();
  };
  harness.answer = async (answer) => {
    harness.prompts.at(-1).answer(answer);
    await settle();
  };
  // Waits, for up to ms, for what the PC does with a command, which looks at files first, and sends its result only once
  // it ends.
  harness.until = async (done, ms = 10 * 1000) => {
    const deadline = Date.now() + ms;
    while (!done()) {
      if (Date.now() > deadline) throw new Error(`waited ${ms} ms for ${done}`);
      await new Promise(setImmediate);
    }
  };
  harness.prompted = (count) => harness.until(() => harness.prompts.length >= count);
  // Waits for ms of real time, also while the test mocks timers.
  harness.waited = (ms) => {
    const end = Date.now() + ms;
    return harness.until(() => Date.now() >= end);
  };
  // Waits for a process to end, sooner than a sleep 30 that nothing kills.
  harness.ended = (pid) => harness.until(() => ended(pid), 5000);
  harness.replied = async (count) => {
    await harness.until(() => harness.replies().length >= count);
    return harness.replies();
  };
  t.after(() => harness.pc.stopAll());
  harness.pc.start(GATEWAY);
  return harness;
}

test("relays agent messages over one agent connection per relayed connection and polls again at once", async (t) => {
  const h = setup(t);
  const link = h.link();

  await h.deliver(ssh("1", LIST), ssh("2", LIST), ssh("3", LIST, { connection: "c2" }));

  assert.deepEqual(h.replies().sort((a, b) => a.id.localeCompare(b.id)), [
    { link, id: "1", message: agentReply(LIST) },
    { link, id: "2", message: agentReply(LIST) },
    { link, id: "3", message: agentReply(LIST) }
  ]);
  assert.deepEqual(h.agents.map((agent) => agent.requests.length), [2, 1]);
  assert.equal(h.polls.length, 1);
  assert.equal(h.link(), link);

  await h.deliver({ type: "ssh_close", session: SESSION, connection: "c1" });

  assert.deepEqual(h.agents.map((agent) => agent.closed), [true, false]);
});

test("opens each link with its first poll, which names the computer, and resumes it with the others", async (t) => {
  t.mock.timers.enable({ apis: ["setTimeout"] });
  const h = setup(t);
  const link = h.link();

  assert.deepEqual(h.polls[0].body, { link, open: true, name: "laptop", run: true, root: true });
  await h.deliver();
  assert.deepEqual(h.polls[0].body, { link });

  h.polls.shift().resolve({ status: 409, body: { error: "the link is not open" } });
  await expectPollAfter(t, h, 10 * 1000);

  assert.notEqual(h.link(), link);
  assert.deepEqual(h.polls[0].body, { link: h.link(), open: true, name: "laptop", run: true, root: true });
});

test("names the computer on one line of at most 64 characters", (t) => {
  const h = setup(t, { hostname: `desk\n\u202e${"x".repeat(70)}.home.lan` });

  assert.equal(h.polls[0].body.name, `desk${"x".repeat(60)}`);
});

test("passes on the guard's refusal without reaching the agent", async (t) => {
  const h = setup(t);

  await h.deliver(ssh("1", Buffer.from([17]).toString("base64")));

  assert.deepEqual(h.replies(), [{ link: h.link(), id: "1", error: "agent request 17 not allowed" }]);
  assert.equal(h.agents.length, 0);
});

test("refuses servers whose host key the PC doesn't know or has revoked", async (t) => {
  const h = setup(t);

  await h.deliver(ssh("1", sign({ host: "host-new" })), ssh("2", sign({ host: "host-bad" }), { connection: "c2" }));

  const [unknown, revoked] = h.replies().sort((a, b) => a.id.localeCompare(b.id));
  assert.match(unknown.error, /host key ssh-ed25519 SHA256:host-new is not in the PC's known_hosts.*~\/\.ssh\/known_hosts/);
  assert.match(revoked.error, /host key ssh-ed25519 SHA256:host-bad is marked @revoked/);
  assert.equal(h.prompts.length, 0);
});

test("asks with the request's details, escaped, and signs nothing when denied", async (t) => {
  const h = setup(t);

  await h.deliver(
    ssh("1", LIST),
    ssh("2", sign({ user: "git\u001b" }), { session_name: "Fix \u202elogin\n" })
  );

  const { answer, signal, ...prompt } = h.prompts[0];
  assert.deepEqual(prompt, {
    kind: "ssh",
    gateway: GATEWAY,
    sessionName: "Fix \\u202elogin\\u000a",
    key: { type: "ssh-ed25519", fingerprint: "SHA256:key-a", comment: "key-a@laptop" },
    server: { names: ["github.com"], hostKeyFingerprint: "SHA256:host-gh" },
    login: "git\\u001b"
  });

  await h.answer("deny");

  assert.deepEqual(h.replies().at(-1), { link: h.link(), id: "2", error: "denied on the PC" });
  assert.equal(h.agents[0].requests.length, 1);
  assert.equal(h.auths, 0);
});

test("shows at most 200 characters of a session name, escaped first", async (t) => {
  const h = setup(t);

  await h.deliver(ssh("1", sign(), { session_name: `Fix \u202e${"x".repeat(300)}` }));

  assert.equal(h.prompts[0].sessionName, `Fix \\u202e${"x".repeat(189)}…`);
});

test("ignores malformed messages", async (t) => {
  const h = setup(t);
  const link = h.link();

  await h.deliver(
    null,
    { type: "run", id: "1" },
    ssh(2, LIST),
    ssh("3", LIST, { session: "/".repeat(4097) }),
    ssh("4", LIST, { connection: "" }),
    ssh("5", LIST, { session_name: ["Fix"] }),
    ssh("6", "not base64!"),
    ssh("7", "Cw"),
    ssh("8", ""),
    ssh("9", Buffer.alloc(256 * 1024 + 1, 11).toString("base64")),
    ssh("10", Buffer.alloc(256 * 1024, 11).toString("base64")),
    run("11", []),
    run("12", ["", "x"]),
    run("13", Array(257).fill("x")),
    run("14", ["x".repeat(4097)]),
    run("15", "ls"),
    run("16", ["ls", 5]),
    run("17", ["ls"], { cwd: "" }),
    run("18", ["ls"], { cwd: ["/"] }),
    run("19", ["ls"], { root: "yes" })
  );

  assert.deepEqual(h.replies().map((reply) => reply.id), ["10"]);
  assert.equal(h.prompts.length, 0);
  assert.equal(h.link(), link);
});

test("refuses a request whose ID is in progress, and still cancels the first", async (t) => {
  const h = setup(t);

  await h.deliver(ssh("1", sign()));
  await h.deliver(ssh("1", LIST, { connection: "c2" }));

  assert.deepEqual(h.replies(), [{ link: h.link(), id: "1", error: "another request with this ID is in progress on the PC" }]);
  assert.equal(h.agents.length, 0);

  await h.deliver({ type: "cancel", id: "1" });

  assert.equal(h.prompts[0].signal.aborted, true);
  assert.equal(h.replies().length, 1);
});

test("refuses more than 32 agent connections at a time", async (t) => {
  const h = setup(t);

  await h.deliver(...Array.from({ length: 33 }, (_, index) => ssh(String(index), LIST, { connection: `c${index}` })));

  assert.equal(h.agents.length, 32);
  assert.deepEqual(h.replies().find((reply) => reply.id === "32"), { link: h.link(), id: "32", error: "too many SSH agent connections are open on the PC" });

  await h.deliver({ type: "ssh_close", session: SESSION, connection: "c0" }, ssh("33", LIST, { connection: "c32" }));

  assert.deepEqual(h.replies().at(-1), { link: h.link(), id: "33", message: agentReply(LIST) });
});

test("refuses more than 8 messages waiting on one agent connection", async (t) => {
  const h = setup(t);

  await h.deliver(...[1, 2, 3, 4, 5, 6, 7, 8, 9].map((id) => ssh(String(id), sign())));

  assert.deepEqual(h.replies(), [{ link: h.link(), id: "9", error: "too many SSH agent messages wait on this connection on the PC" }]);
});

test("a poll that would take the link over 256 requests in progress, refusals being sent included, loses it", async (t) => {
  t.mock.timers.enable({ apis: ["setTimeout"] });
  const h = setup(t, { holdReplies: true });
  const link = h.link();

  // 32 connections take a request each, and the requests of the others are refused.
  for (let poll = 0; poll < 4; poll++) {
    const count = poll < 3 ? 64 : 63;
    await h.deliver(...Array.from({ length: count }, (_, index) => ssh(`${poll}.${index}`, LIST, { connection: `c${poll * 64 + index}` })));
  }
  assert.equal(h.replies().length, 255);
  assert.equal(h.link(), link);

  // The whole batch is checked first, so none of it starts.
  await h.deliver(ssh("last", LIST, { connection: "c255" }), ssh("over", LIST, { connection: "c256" }));

  assert.equal(h.replies().length, 255);
  assert.deepEqual(h.posts.at(-1), { path: "/pc/link/close", body: { link } });
  assert.deepEqual(h.warnings(), ["PC access: lost the link to home-server: the poll's messages would take the link over 256 requests in progress"]);
  await expectPollAfter(t, h, 1000);
  assert.notEqual(h.link(), link);
});

test("allow once signs after the password check and asks again next time", async (t) => {
  const h = setup(t);

  await h.deliver(ssh("1", sign()));
  await h.answer("once");

  assert.equal(h.auths, 1);
  assert.deepEqual(h.replies(), [{ link: h.link(), id: "1", message: agentReply(sign()) }]);

  await h.deliver(ssh("2", sign()));

  assert.equal(h.prompts.length, 2);
});

test("a 5-minute grant covers only its session, key, host and login, until it expires", async (t) => {
  const h = setup(t);

  await h.deliver(ssh("1", sign()));
  await h.answer("grant");
  h.time += FIVE_MINUTES - 1;
  await h.deliver(ssh("2", sign(), { connection: "c2" }));

  assert.equal(h.prompts.length, 1);
  assert.equal(h.auths, 1);
  assert.deepEqual(h.replies().at(-1), { link: h.link(), id: "2", message: agentReply(sign()) });

  const others = [
    ssh("3", sign(), { session: "/home/vitek/.pi/sessions/b.jsonl" }),
    ssh("4", sign({ key: "key-b" }), { connection: "c3" }),
    ssh("5", sign({ host: "host-gl" }), { connection: "c4" }),
    ssh("6", sign({ user: "deploy" }), { connection: "c5" })
  ];
  for (const [index, message] of others.entries()) {
    await h.deliver(message);
    assert.equal(h.prompts.length, index + 2, `${message.id} asks again`);
    await h.answer("deny");
  }

  h.time += 1;
  await h.deliver(ssh("7", sign(), { connection: "c6" }));

  assert.equal(h.prompts.length, 6);
});

test("a failed password check denies and grants nothing", async (t) => {
  const h = setup(t, { authenticate: async () => ({ ok: false, reason: "the password dialog was cancelled" }) });

  await h.deliver(ssh("1", sign()));
  await h.answer("grant");

  assert.deepEqual(h.replies(), [{ link: h.link(), id: "1", error: "denied on the PC: the password dialog was cancelled" }]);
  assert.equal(h.agents.length, 0);

  await h.deliver(ssh("2", sign(), { connection: "c2" }));

  assert.equal(h.prompts.length, 2);
});

test("tells the server while it asks, from its dialog to the end of the password check, which Gripi shows elsewhere", async (t) => {
  let authenticated;
  const h = setup(t, { authenticate: () => new Promise((resolve) => { authenticated = resolve; }) });

  await h.deliver(ssh("1", sign({ user: "git\u001b" })), ssh("2", sign({ host: "host-gl" }), { connection: "c2" }));

  const asking = { link: h.link(), id: "1", asking: true, summary: "SSH login to github.com as git\\u001b", expires_in: 120 };
  assert.deepEqual(h.statuses(), [asking]);
  await h.answer("once");
  assert.deepEqual(h.statuses(), [asking]);

  authenticated({ ok: true });
  await settle();
  assert.deepEqual(h.posts.filter((post) => post.body.id === "1").map((post) => post.path), ["/pc/link/status", "/pc/link/status", "/pc/link/reply"]);
  assert.deepEqual(h.statuses(), [asking, { link: h.link(), id: "1", asking: false }, { ...asking, id: "2", summary: "SSH login to gitlab.com as git" }]);
});

test("says nothing of a login that a grant covers, also one made while it waited, nor of one cancelled meanwhile", async (t) => {
  const h = setup(t);

  await h.deliver(
    ssh("1", sign()),
    ssh("2", sign(), { connection: "c2" }),
    ssh("3", sign({ user: "deploy" }), { connection: "c3" })
  );
  await h.deliver({ type: "cancel", id: "3" });
  await h.answer("grant");
  await h.deliver(ssh("4", sign(), { connection: "c4" }));

  assert.equal(h.prompts.length, 1);
  assert.deepEqual(h.statuses().map((status) => [status.id, status.asking]), [["1", true], ["1", false]]);
  assert.deepEqual(h.replies().map((reply) => reply.id).sort(), ["1", "2", "4"]);
});

test("shows the next dialog only once the password check of the one before has ended", async (t) => {
  let authenticated;
  const h = setup(t, { authenticate: () => new Promise((resolve) => { authenticated = resolve; }) });

  await h.deliver(ssh("1", sign()), ssh("2", sign({ user: "deploy" }), { connection: "c2" }));
  await h.answer("once");

  assert.equal(h.auths, 1);
  assert.equal(h.prompts.length, 1);

  authenticated({ ok: true });
  await settle();

  assert.deepEqual(h.replies(), [{ link: h.link(), id: "1", message: agentReply(sign()) }]);
  assert.equal(h.prompts.length, 2);
});

test("refuses at once when more than 5 requests wait for approval", async (t) => {
  const h = setup(t);

  await h.deliver(...[1, 2, 3, 4, 5, 6].map((id) => ssh(String(id), sign(), { connection: `c${id}` })));

  assert.equal(h.prompts.length, 1);
  assert.deepEqual(h.replies(), [{ link: h.link(), id: "6", error: "denied on the PC: too many requests are waiting for approval there" }]);

  await h.answer("deny");

  assert.equal(h.prompts.length, 2);
});

test("a cancel closes the request's dialog, or keeps it from showing, and signs nothing", async (t) => {
  const h = setup(t);

  await h.deliver(ssh("1", sign()), ssh("2", sign(), { connection: "c2" }));
  await h.deliver({ type: "cancel", id: "2" }, { type: "cancel", id: "1" });

  assert.equal(h.prompts[0].signal.aborted, true);
  assert.equal(h.prompts.length, 1);
  assert.deepEqual(h.replies(), []);
  assert.equal(h.agents.length, 0);
});

test("a cancel during the password check signs nothing", async (t) => {
  let confirmed;
  const h = setup(t, { authenticate: () => new Promise((resolve) => { confirmed = resolve; }) });

  await h.deliver(ssh("1", sign()));
  await h.answer("grant");
  await h.deliver({ type: "cancel", id: "1" });
  confirmed({ ok: true });
  await settle();

  assert.deepEqual(h.replies(), []);
  assert.equal(h.agents.length, 0);
});

test("stopping closes the link, its dialogs and agent connections, and forgets grants", async (t) => {
  const h = setup(t);
  const link = h.link();
  await h.deliver(ssh("1", LIST), ssh("2", sign()));
  await h.answer("grant");
  await h.deliver(ssh("3", sign({ user: "deploy" }), { connection: "c2" }));

  await h.pc.stop(GATEWAY.id);

  assert.equal(h.prompts[1].signal.aborted, true);
  assert.deepEqual(h.posts.at(-1), { path: "/pc/link/close", body: { link } });
  assert.deepEqual(h.warnings(), ["PC access: lost the link to home-server: PC access was turned off"]);
  assert.equal(h.agents[0].closed, true);
  await settle();
  assert.equal(h.replies().length, 2);

  h.pc.start(GATEWAY);
  assert.notEqual(h.link(), link);
  await h.deliver(ssh("4", sign()));

  assert.equal(h.prompts.length, 3);
});

test("a dialog nobody answers in 2 minutes is denied", async (t) => {
  t.mock.timers.enable({ apis: ["setTimeout"] });
  const h = setup(t);

  await h.deliver(ssh("1", sign()));
  t.mock.timers.tick(2 * 60 * 1000);
  await settle();

  assert.deepEqual(h.replies(), [{ link: h.link(), id: "1", error: "denied on the PC: nobody answered there in 2 minutes" }]);
  assert.equal(h.agents.length, 0);
});

test("the 2 minutes cover the password check, which is stopped when they end", async (t) => {
  t.mock.timers.enable({ apis: ["setTimeout"] });
  let authSignal;
  const h = setup(t, {
    authenticate: (signal) => new Promise((resolve) => {
      authSignal = signal;
      signal.addEventListener("abort", () => resolve({ ok: false, reason: "pkexec was stopped" }));
    })
  });

  await h.deliver(ssh("1", sign()));
  t.mock.timers.tick(60 * 1000);
  await h.answer("once");
  t.mock.timers.tick(60 * 1000);
  await settle();

  assert.equal(authSignal.aborted, true);
  assert.deepEqual(h.replies(), [{ link: h.link(), id: "1", error: "denied on the PC: nobody answered there in 2 minutes" }]);
});

test("backs off on network errors from 1 second to 30, and polls at once after a success", async (t) => {
  t.mock.timers.enable({ apis: ["setTimeout"] });
  const h = setup(t);

  for (const delay of [1000, 2000, 4000, 8000, 16000, 30000, 30000]) {
    h.polls.shift().reject(new Error("offline"));
    await expectPollAfter(t, h, delay);
  }
  await h.deliver();
  assert.equal(h.polls.length, 1);
  h.polls.shift().reject(new Error("offline"));
  await expectPollAfter(t, h, 1000);
});

const failedPolls = [
  ["a network error", 1000, (poll) => poll.reject(new Error("offline")), "the poll got no answer: offline"],
  ["a refusal", 60 * 1000, (poll) => poll.resolve({ status: 403, body: { error: "refused\u001b[2J" } }), "the poll got HTTP 403: refused\\u001b[2J"],
  [
    "a conflict over another app's link or one that a restarted server forgot",
    10 * 1000,
    (poll) => poll.resolve({ status: 409, body: { error: "the link is not open" } }),
    "the poll got HTTP 409: the link is not open"
  ],
  ["a server error", 1000, (poll) => poll.resolve({ status: 502, body: null }), "the poll got HTTP 502"],
  ["a malformed answer", 1000, (poll) => poll.resolve({ status: 200, body: { messages: {} } }), "the poll's answer is malformed"],
  ["a poll of more than 64 messages", 1000, (poll) => poll.resolve({
    status: 200,
    body: { messages: Array.from({ length: 65 }, (_, index) => ssh(`x${index}`, LIST, { connection: `x${index}` })) }
  }), "the poll brought 65 messages, more than 64"]
];
for (const [failure, retryMs, fail, reason] of failedPolls) {
  test(`${failure} loses the link, saying why: it closes its dialogs and agent connections, forgets grants and retries with a new ID`, async (t) => {
    t.mock.timers.enable({ apis: ["setTimeout"] });
    const h = setup(t);
    const link = h.link();
    await h.deliver(ssh("1", LIST), ssh("2", sign()));
    await h.answer("grant");
    await h.deliver(ssh("3", sign({ user: "deploy" }), { connection: "c2" }));

    fail(h.polls.shift());
    await settle();

    assert.equal(h.prompts[1].signal.aborted, true);
    assert.equal(h.agents[0].closed, true);
    assert.deepEqual(h.posts.at(-1), { path: "/pc/link/close", body: { link } });
    assert.deepEqual(h.warnings(), [`PC access: lost the link to home-server: ${reason}`]);
    await expectPollAfter(t, h, retryMs);
    assert.notEqual(h.link(), link);
    await h.deliver(ssh("4", sign()));

    assert.equal(h.prompts.length, 3);
    assert.deepEqual(h.replies().map((reply) => reply.id), ["1", "2"]);
  });
}

test("asks to run a command with the file that runs, the folder and the session, escaped, and runs nothing when denied", async (t) => {
  const h = setup(t);
  const folder = fs.mkdtempSync(path.join(os.tmpdir(), "pc-run-\u202e"));
  t.after(() => fs.rmSync(folder, { recursive: true }));

  await h.deliver(run("1", ["sh", "-c", "echo 'a\nb' \u202e", "--plain=word"], { session_name: "Fix \u202elogin", cwd: folder }));
  await h.prompted(1);

  const { answer, signal, ...prompt } = h.prompts[0];
  assert.deepEqual(prompt, {
    kind: "run",
    gateway: GATEWAY,
    sessionName: "Fix \\u202elogin",
    command: `${SH} -c "echo 'a\\nb' \\u202e" --plain=word`,
    folder: folder.replace("\u202e", "\\u202e"),
    root: false
  });
  assert.deepEqual(h.statuses(), [{ link: h.link(), id: "1", asking: true, summary: prompt.command, expires_in: 120 }]);

  await h.answer("deny");

  assert.deepEqual(h.replies(), [{ link: h.link(), id: "1", error: "denied on the PC" }]);
  assert.equal(h.auths, 0);
  assert.equal(h.spawned.length, 0);
});

test("runs an approved command without polkit's password dialog, a shell or input in a process group of its own, once the server says that it still waits", async (t) => {
  const h = setup(t);
  const folder = fs.mkdtempSync(path.join(os.tmpdir(), "pc-run-"));
  t.after(() => fs.rmSync(folder, { recursive: true }));

  await h.deliver(run("1", ["sh", "-c", "pwd; read -r line || echo no input >&2; exit 3"], { cwd: folder }));
  await h.prompted(1);
  await h.answer("run");

  assert.equal(h.auths, 0);
  assert.deepEqual(h.spawned.map(({ command, args, options }) => ({ command, args, options })), [{
    command: SH,
    args: ["-c", "pwd; read -r line || echo no input >&2; exit 3"],
    options: { cwd: folder, stdio: ["ignore", "pipe", "pipe"], detached: true }
  }]);
  assert.deepEqual(await h.replied(1), [{
    link: h.link(),
    id: "1",
    result: { exit_code: 3, stdout: base64(`${folder}\n`), stderr: base64("no input\n"), truncated: false }
  }]);
  // The server hears that it may start before it does, and that the PC stopped asking before it ends.
  assert.deepEqual(h.posts.map(({ body }) => Object.keys(body).find((key) => ["asking", "starting", "result"].includes(key)) + (body.asking === false ? ":false" : "")), ["asking", "starting", "asking:false", "result"]);
});

test("runs a command found on the PATH in the user's home folder without a folder, and passes on the signal that killed it", async (t) => {
  const h = setup(t);

  await h.deliver(run("1", ["sh", "-c", "pwd; kill -TERM $$"]));
  await h.prompted(1);
  await h.answer("run");

  assert.equal(h.spawned[0].options.cwd, os.homedir());
  assert.deepEqual(await h.replied(1), [{
    link: h.link(),
    id: "1",
    result: { exit_code: 128 + 15, stdout: base64(`${os.homedir()}\n`), stderr: "", truncated: false }
  }]);
});

test("refuses at once a folder or command that isn't there, or a command too long to show", async (t) => {
  const h = setup(t);
  const folder = fs.mkdtempSync(path.join(os.tmpdir(), "pc-run-"));
  t.after(() => fs.rmSync(folder, { recursive: true }));
  fs.writeFileSync(path.join(folder, "notes.txt"), "");

  await h.deliver(
    run("1", ["true"], { cwd: path.join(folder, "missing") }),
    run("2", ["true"], { cwd: "relative" }),
    run("3", ["no-such-command-\u202e"]),
    run("4", ["./notes.txt"], { cwd: folder }),
    run("5", ["echo", "x".repeat(4096)])
  );
  await h.replied(5);

  assert.deepEqual(h.replies().sort((a, b) => a.id.localeCompare(b.id)).map((reply) => reply.error), [
    `the PC has no folder ${folder}/missing`,
    "the PC has no folder relative",
    "the PC has no command no-such-command-\\u202e on its PATH",
    "the PC has no command ./notes.txt",
    "the command is too long to show on the PC"
  ]);
  assert.equal(h.prompts.length, 0);
});

test("runs nothing that the server no longer waits for once the user approved it, nor asks polkit for one as root", async (t) => {
  const h = setup(t, { starting: 404 });
  const pkexec = fakePkexec(t);

  for (const [count, root] of [[1, false], [2, true]]) {
    await h.deliver(run(String(count), ["true"], { root }));
    await h.prompted(count);
    await h.answer("run");
    await h.replied(count);
  }

  assert.deepEqual(h.replies().map((reply) => reply.error), ["not run on the PC: Gripi no longer waits for it", "not run on the PC: Gripi no longer waits for it"]);
  assert.equal(h.spawned.length, 0);
  assert.deepEqual(pkexec.calls(), []);
});

test("a cancel while the server is asked whether the command may start runs nothing", async (t) => {
  const h = setup(t, { starting: "held" });

  await h.deliver(run("1", ["true"]));
  await h.prompted(1);
  await h.answer("run");
  await h.until(() => h.posts.some(({ body }) => body.starting));
  await h.deliver({ type: "cancel", id: "1" });

  assert.deepEqual(h.replies(), []);
  assert.equal(h.spawned.length, 0);
});

test("keeps 16 MiB of each output of a command and drops the rest", async (t) => {
  const h = setup(t);

  await h.deliver(run("1", ["sh", "-c", "head -c 16777217 /dev/zero; head -c 16777216 /dev/zero >&2"]));
  await h.prompted(1);
  await h.answer("run");

  const [{ result }] = await h.replied(1);
  assert.equal(Buffer.from(result.stdout, "base64").length, 16 * 1024 * 1024);
  assert.equal(Buffer.from(result.stderr, "base64").length, 16 * 1024 * 1024);
  assert.equal(result.truncated, true);
});

test("a cancel ends the command's process group, with SIGKILL 5 seconds after SIGTERM, and sends no result", async (t) => {
  t.mock.timers.enable({ apis: ["setTimeout"] });
  const h = setup(t);
  const script = (trap) => `${trap} sleep 30 & echo $!; wait`;

  // One at a time, as the PC may ask in either order for requests that came together.
  for (const [count, trap] of [[1, ""], [2, "trap '' TERM;"]]) {
    await h.deliver(run(String(count), ["sh", "-c", script(trap)]));
    await h.prompted(count);
    await h.answer("run");
    await h.until(() => h.spawned.length === count);
  }
  const sleeps = await Promise.all(h.spawned.map(async ({ printed }) => Number(String((await printed)[0]))));
  await h.deliver({ type: "cancel", id: "1" }, { type: "cancel", id: "2" });

  await h.ended(sleeps[0]);
  assert.equal(ended(sleeps[1]), false);
  t.mock.timers.tick(5000);
  await h.ended(sleeps[1]);
  await settle();
  assert.deepEqual(h.replies(), []);
});

test("stopping all, as the app quits, kills the process groups of commands that outlive SIGTERM for a second", async (t) => {
  t.mock.timers.enable({ apis: ["setTimeout"] });
  const h = setup(t);

  // The shell ends on SIGTERM, but not a process that it started in the background, which ignores it from the start
  // and keeps the group.
  await h.deliver(run("1", ["sh", "-c", "trap '' TERM; sleep 30 >/dev/null 2>&1 & trap - TERM; echo $!; wait"]));
  await h.prompted(1);
  await h.answer("run");
  await h.until(() => h.spawned.length === 1);
  const sleep = Number(String((await h.spawned[0].printed)[0]));

  const stopping = h.pc.stopAll();
  await h.spawned[0].closed;
  assert.equal(ended(sleep), false);
  t.mock.timers.tick(1000);
  await stopping;
  await h.ended(sleep);
  assert.deepEqual(h.warnings(), ["PC access: lost the link to home-server: the app quits"]);
});

test("a result that can't reach the server becomes an error, as gripi pc run would wait for it for good", async (t) => {
  const h = setup(t, { lostResults: true });

  await h.deliver(run("1", ["true"]));
  await h.prompted(1);
  await h.answer("run");

  assert.deepEqual((await h.replied(2)).at(-1), { link: h.link(), id: "1", error: "the command ran on the PC, but its result could not reach Gripi" });
});

// Once the server may have let the command start, gripi pc run waits for an answer however long it takes.
const unanswered = [
  ["a command's result", {}, false],
  ["a command that can't start", {}, true],
  ["a command whose start the server didn't answer", { starting: null }, false]
];
for (const [what, options, unrunnable] of unanswered) {
  test(`a link that takes no answer to ${what}, not even an error, ends, which ends gripi pc run's wait`, async (t) => {
    const h = setup(t, { lostReplies: true, ...options });
    const link = h.link();
    const folder = fs.mkdtempSync(path.join(os.tmpdir(), "pc-run-"));
    t.after(() => fs.rmSync(folder, { recursive: true, force: true }));

    await h.deliver(run("1", ["true"], { cwd: folder }));
    await h.prompted(1);
    // A folder that goes while the PC asks fails the spawn.
    if (unrunnable) fs.rmSync(folder, { recursive: true });
    await h.answer("run");
    await h.until(() => h.posts.some(({ path }) => path === "/pc/link/close"));

    assert.deepEqual(h.posts.at(-1), { path: "/pc/link/close", body: { link } });
    assert.deepEqual(h.warnings(), ["PC access: lost the link to home-server: the reply to a command that may have started got no answer: the server did not answer in time"]);
  });
}

test("a link keeps refusals that don't reach Gripi before the server may have let the command start", async (t) => {
  const h = setup(t, { lostReplies: true });
  const replied = (count) => h.until(() => h.replies().length === count).then(settle);

  await h.deliver(run("1", ["no-such-command"]));
  await replied(1);
  await h.deliver(run("2", ["true"]));
  await h.prompted(1);
  await h.answer("deny");
  await replied(2);

  assert.equal(h.posts.some(({ path }) => path === "/pc/link/close"), false);
  assert.deepEqual(h.polls.map((poll) => poll.body.link), [h.link()]);
});

// A fake pkexec, first on the PATH, which records its process, folder and arguments, and then waits for the test's
// answer, as polkit's dialog does. "authorize" runs the command, closing descriptor 3 as pkexec does, and "dismiss",
// "refuse" and "fail" exit with pkexec's words.
function fakePkexec(t) {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "pc-pkexec-"));
  const answer = path.join(dir, "answer");
  fs.writeFileSync(path.join(dir, "pkexec"), `#!/bin/sh
printf '%s\\n' "$(pwd)" "$@" > "${dir}/new.$$"
mv "${dir}/new.$$" "${dir}/call.$$"
while [ ! -e "${answer}" ]; do sleep 0.02; done
answer=$(cat "${answer}")
rm "${answer}"
case $answer in
dismiss) echo "Error executing command as another user: Request dismissed" >&2; exit 126 ;;
refuse) printf 'Error executing command as another user: Not authorized\\n\\nThis incident has been reported.\\n' >&2; exit 127 ;;
fail) echo "Error checking for authorization org.freedesktop.policykit.exec: no polkitd" >&2; exit 127 ;;
esac
shift 2
exec 3>&-
exec "$@"
`, { mode: 0o755 });
  const PATH = process.env.PATH;
  process.env.PATH = `${dir}:${PATH}`;
  t.after(() => {
    process.env.PATH = PATH;
    fs.rmSync(dir, { recursive: true, force: true });
  });
  const calls = () => fs.readdirSync(dir).filter((name) => name.startsWith("call.")).map((name) => {
    const [folder, ...args] = fs.readFileSync(path.join(dir, name), "utf8").split("\n").slice(0, -1);
    return { pid: Number(name.slice(5)), folder, args };
  });
  return {
    calls,
    asked: (count = 1) => calls().length >= count,
    answer(text) {
      fs.writeFileSync(`${answer}.new`, text);
      fs.renameSync(`${answer}.new`, answer);
    }
  };
}

test("asks to run a command as root, saying so, and runs it with pkexec in its folder, whose dialog is the password step, once the server says that it may start", async (t) => {
  const h = setup(t);
  const pkexec = fakePkexec(t);
  const folder = fs.mkdtempSync(path.join(os.tmpdir(), "pc-run-"));
  t.after(() => fs.rmSync(folder, { recursive: true }));
  const script = "pwd; read -r line || echo no input >&2; exit 3";

  await h.deliver(run("1", ["sh", "-c", script], { cwd: folder, root: true }));
  await h.prompted(1);
  const { answer, signal, ...prompt } = h.prompts[0];
  assert.deepEqual(prompt, { kind: "run", gateway: GATEWAY, sessionName: "Fix login redirect", command: `${SH} -c "${script}"`, folder, root: true });
  assert.deepEqual(h.statuses(), [{ link: h.link(), id: "1", asking: true, summary: `run a command as root: ${SH} -c "${script}"`, expires_in: 120 }]);

  await h.answer("run");
  await h.until(() => pkexec.asked());
  assert.deepEqual(pkexec.calls().map(({ folder, args }) => ({ folder, args })), [{ folder, args: ["--disable-internal-agent", "--keep-cwd", SH, "-c", script] }]);
  assert.deepEqual(h.statuses().map((status) => Object.keys(status).find((key) => ["asking", "starting"].includes(key))), ["asking", "starting"]);

  pkexec.answer("authorize");
  assert.deepEqual(await h.replied(1), [{
    link: h.link(),
    id: "1",
    result: { exit_code: 3, stdout: base64(`${folder}\n`), stderr: base64("no input\n"), truncated: false }
  }]);
  assert.equal(h.auths, 0);
  assert.deepEqual(h.statuses().at(-1), { link: h.link(), id: "1", asking: false });
});

test("shows the next dialog once polkit's dialog of a command as root has ended, while the command still runs", async (t) => {
  const h = setup(t);
  const pkexec = fakePkexec(t);

  await h.deliver(run("1", ["sh", "-c", "echo $$; exec sleep 30"], { root: true }));
  await h.prompted(1);
  await h.answer("run");
  await h.until(() => pkexec.asked());
  await h.deliver(run("2", ["true"]));
  await h.waited(100);
  assert.equal(h.prompts.length, 1);

  pkexec.answer("authorize");
  await h.prompted(2);
  const sleep = Number(String((await h.spawned[0].printed)[0]));
  t.after(() => process.kill(sleep));
  assert.equal(ended(sleep), false);
  assert.deepEqual(h.statuses().filter((status) => status.id === "1").map((status) => status.asking ?? "starting"), [true, "starting", false]);
});

test("pkexec's refusals come back as errors with its words, unlike the command's own exit codes", async (t) => {
  const h = setup(t);
  const pkexec = fakePkexec(t);
  const commands = [
    ["dismiss", ["true"]],
    ["refuse", ["true"]],
    ["fail", ["true"]],
    ["authorize", ["sh", "-c", "echo 'sh: 1: x: not found' >&2; exit 127"]],
    ["authorize", ["sh", "-c", "echo 'Error executing /usr/bin/other: No such file or directory' >&2; exit 127"]],
    ["authorize", ["sh", "-c", "echo out; echo 'Error executing command as another user: Request dismissed' >&2; exit 126"]]
  ];

  for (const [index, [answer, argv]] of commands.entries()) {
    await h.deliver(run(String(index), argv, { root: true }));
    await h.prompted(index + 1);
    await h.answer("run");
    await h.until(() => pkexec.asked(index + 1));
    pkexec.answer(answer);
    await h.replied(index + 1);
  }

  assert.deepEqual(h.replies().map(({ error, result }) => error ?? result.exit_code), [
    "not run as root on the PC: pkexec: Error executing command as another user: Request dismissed",
    "not run as root on the PC: pkexec: Error executing command as another user: Not authorized\n\nThis incident has been reported.",
    "not run as root on the PC: pkexec: Error checking for authorization org.freedesktop.policykit.exec: no polkitd",
    127,
    127,
    126
  ]);
});

test("refuses a command as root at once on a PC without pkexec", async (t) => {
  const h = setup(t);
  const PATH = process.env.PATH;
  process.env.PATH = fs.mkdtempSync(path.join(os.tmpdir(), "pc-path-"));
  t.after(() => {
    fs.rmSync(process.env.PATH, { recursive: true });
    process.env.PATH = PATH;
  });

  await h.deliver(run("1", ["/bin/true"], { root: true }));

  assert.deepEqual(await h.replied(1), [{ link: h.link(), id: "1", error: "running commands as root on the PC needs polkit's pkexec" }]);
  assert.equal(h.prompts.length, 0);
});

// The wrapper's pipe closes also when the app crashes, which stops pkexec while polkit's dialog is open.
test("the wrapper stops pkexec while polkit's dialog is open once the app's pipe closes, and the command never runs", async (t) => {
  const h = setup(t);
  const pkexec = fakePkexec(t);
  const touched = path.join(fs.mkdtempSync(path.join(os.tmpdir(), "pc-run-")), "ran");
  t.after(() => fs.rmSync(path.dirname(touched), { recursive: true }));

  await h.deliver(run("1", ["touch", touched], { root: true }));
  await h.prompted(1);
  await h.answer("run");
  await h.until(() => pkexec.asked());
  h.spawned[0].child.stdin.destroy();

  await h.ended(pkexec.calls()[0].pid);
  await h.spawned[0].closed;
  pkexec.answer("authorize");
  await h.waited(100);
  assert.equal(fs.existsSync(touched), false);
});

const stoppedDialogs = [
  ["a cancel", (_t, h) => h.deliver({ type: "cancel", id: "1" }), []],
  ["the 2 minutes", (t) => t.mock.timers.tick(2 * 60 * 1000), ["denied on the PC: nobody answered there in 2 minutes"]]
];
for (const [stop, act, errors] of stoppedDialogs) {
  test(`${stop} while polkit's dialog of a command as root is open stops pkexec, and the command never runs`, async (t) => {
    t.mock.timers.enable({ apis: ["setTimeout"] });
    const h = setup(t);
    const pkexec = fakePkexec(t);
    const touched = path.join(fs.mkdtempSync(path.join(os.tmpdir(), "pc-run-")), "ran");
    t.after(() => fs.rmSync(path.dirname(touched), { recursive: true }));

    await h.deliver(run("1", ["touch", touched], { root: true }));
    await h.prompted(1);
    await h.answer("run");
    await h.until(() => pkexec.asked());
    await act(t, h);

    await h.ended(pkexec.calls()[0].pid);
    await h.spawned[0].closed;
    await settle();
    assert.deepEqual(h.replies().map((reply) => reply.error), errors);
    pkexec.answer("authorize");
    await h.waited(100);
    assert.equal(fs.existsSync(touched), false);
  });
}

test("stopping all, as the app quits, leaves a command as root to run once polkit authorized it, as the app can't stop it", async (t) => {
  t.mock.timers.enable({ apis: ["setTimeout"] });
  const h = setup(t);
  const pkexec = fakePkexec(t);

  await h.deliver(run("1", ["sh", "-c", "echo $$; exec sleep 30"], { root: true }));
  await h.prompted(1);
  await h.answer("run");
  await h.until(() => pkexec.asked());
  pkexec.answer("authorize");
  const sleep = Number(String((await h.spawned[0].printed)[0]));
  t.after(() => process.kill(sleep));

  // Without waiting to kill it, which it would do for a command as the user.
  let stopped = false;
  h.pc.stopAll().then(() => { stopped = true; });
  await h.until(() => stopped, 5000);
  await h.waited(100);
  assert.equal(ended(sleep), false);
});

test("a cancel that comes before the server's answer that a command as root may start runs nothing, also when that answer is yes", async (t) => {
  const h = setup(t, { starting: "late" });
  const pkexec = fakePkexec(t);

  await h.deliver(run("1", ["true"], { root: true }));
  await h.prompted(1);
  await h.answer("run");
  await h.until(() => h.started);
  await h.deliver({ type: "cancel", id: "1" });
  h.started();
  await h.waited(100);

  assert.deepEqual(pkexec.calls(), []);
  assert.equal(h.spawned.length, 0);
  assert.deepEqual(h.replies(), []);
});

test("a cancelled password check ends only once pkexec has exited", async () => {
  let pkexec;
  // Like pkexec while polkit's dialog takes a moment to close.
  const spawnShell = (_command, _args, options) => {
    pkexec = spawn("sh", ["-c", "trap '' TERM; echo asking; sleep 0.2"], { ...options, stdio: ["ignore", "pipe", "ignore"] });
    return pkexec;
  };
  const controller = new AbortController();
  const checked = authenticateWithPolkit(controller.signal, spawnShell);
  await once(pkexec.stdout, "data");
  controller.abort();

  assert.equal((await checked).ok, false);
  assert.equal(pkexec.exitCode, 0);
});

async function listen(t, handler) {
  const server = http.createServer(handler);
  await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
  t.after(() => {
    server.closeAllConnections();
    server.close();
  });
  return `http://127.0.0.1:${server.address().port}`;
}

test("linkRequest posts JSON with the link header and cookies, and doesn't follow redirects", async (t) => {
  const received = [];
  const origin = await listen(t, (request, response) => {
    let body = "";
    request.on("data", (chunk) => { body += chunk; });
    request.on("end", () => {
      received.push({ url: request.url, headers: request.headers, body });
      if (request.url === "/moved") response.writeHead(302, { Location: "/pc/link" }).end();
      else response.end(JSON.stringify({ messages: [] }));
    });
  });

  assert.deepEqual(await linkRequest(new URL("/pc/link", origin), { link: "l1" }, { cookie: "a=1; b=2" }), { status: 200, body: { messages: [] } });
  assert.deepEqual(await linkRequest(new URL("/moved", origin), { link: "l1" }), { status: 302, body: null });

  assert.deepEqual(received.map(({ url }) => url), ["/pc/link", "/moved"]);
  const { headers, body } = received[0];
  assert.equal(body, '{"link":"l1"}');
  assert.equal(headers["content-type"], "application/json");
  assert.equal(headers["x-gripi-pc-link"], "1");
  assert.equal(headers.cookie, "a=1; b=2");
  assert.equal(received[1].headers.cookie, undefined);
});

test("linkRequest gives up on an answer over 8 MiB, or one that takes longer than 40 seconds", { timeout: 10 * 1000 }, async (t) => {
  let trickling;
  const origin = await listen(t, (request, response) => {
    request.resume();
    if (request.url === "/large") {
      response.end(Buffer.alloc(8 * 1024 * 1024 + 1, " "));
      return;
    }
    response.write("{");
    trickling = setInterval(() => response.write(" "), 5);
    t.after(() => clearInterval(trickling));
  });

  await assert.rejects(linkRequest(new URL("/large", origin), {}), /the server's answer is too large/);

  t.mock.timers.enable({ apis: ["setTimeout"] });
  const slow = linkRequest(new URL("/slow", origin), {});
  while (!trickling) await new Promise(setImmediate);
  t.mock.timers.tick(40 * 1000);

  await assert.rejects(slow, /the server did not answer in time/);
});

async function expectPollAfter(t, h, delay) {
  await settle();
  t.mock.timers.tick(delay - 1);
  await settle();
  assert.equal(h.polls.length, 0, `no poll before ${delay} ms`);
  t.mock.timers.tick(1);
  await settle();
  assert.equal(h.polls.length, 1, `a poll after ${delay} ms`);
}
