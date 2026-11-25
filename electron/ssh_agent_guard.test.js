const assert = require("node:assert/strict");
const { execFile, execFileSync, spawn } = require("node:child_process");
const crypto = require("node:crypto");
const fs = require("node:fs");
const net = require("node:net");
const os = require("node:os");
const path = require("node:path");
const { after, before, beforeEach, describe, test } = require("node:test");
const { AGENT_FAILURE, createConnectionGuard, findKnownHost, fingerprint, keyType } = require("./ssh_agent_guard");

const HOSTBOUND = "publickey-hostbound-v00@openssh.com";
const ECDSA = {
  "ecdsa-sha2-nistp256": { namedCurve: "P-256", hash: "sha256", size: 32 },
  "ecdsa-sha2-nistp384": { namedCurve: "P-384", hash: "sha384", size: 48 },
  "ecdsa-sha2-nistp521": { namedCurve: "P-521", hash: "sha512", size: 66 }
};
const rsaKeys = crypto.generateKeyPairSync("rsa", { modulusLength: 2048 });

function uint32(value) {
  const buffer = Buffer.alloc(4);
  buffer.writeUInt32BE(value);
  return buffer;
}

function string(value) {
  const bytes = Buffer.from(value);
  return Buffer.concat([uint32(bytes.length), bytes]);
}

function mpint(bytes) {
  let start = 0;
  while (start < bytes.length && bytes[start] === 0) start++;
  const value = bytes.subarray(start);
  return string(value[0] & 0x80 ? Buffer.concat([Buffer.from([0]), value]) : value);
}

function sshSignature(algorithm, bytes) {
  return Buffer.concat([string(algorithm), string(bytes)]);
}

function testHostKey(algorithm) {
  if (algorithm === "ssh-ed25519") {
    const { publicKey, privateKey } = crypto.generateKeyPairSync("ed25519");
    return {
      blob: Buffer.concat([string(algorithm), string(Buffer.from(publicKey.export({ format: "jwk" }).x, "base64url"))]),
      sign: (data) => sshSignature(algorithm, crypto.sign(null, data, privateKey))
    };
  }
  const ecdsa = ECDSA[algorithm];
  if (ecdsa) {
    const { publicKey, privateKey } = crypto.generateKeyPairSync("ec", { namedCurve: ecdsa.namedCurve });
    const { x, y } = publicKey.export({ format: "jwk" });
    const point = Buffer.concat([Buffer.from([4]), Buffer.from(x, "base64url"), Buffer.from(y, "base64url")]);
    return {
      blob: Buffer.concat([string(algorithm), string(algorithm.slice("ecdsa-sha2-".length)), string(point)]),
      sign(data) {
        const rs = crypto.sign(ecdsa.hash, data, { key: privateKey, dsaEncoding: "ieee-p1363" });
        return sshSignature(algorithm, Buffer.concat([mpint(rs.subarray(0, ecdsa.size)), mpint(rs.subarray(ecdsa.size))]));
      }
    };
  }
  const { e, n } = rsaKeys.publicKey.export({ format: "jwk" });
  return {
    blob: Buffer.concat([string("ssh-rsa"), mpint(Buffer.from(e, "base64url")), mpint(Buffer.from(n, "base64url"))]),
    sign: (data) => sshSignature(algorithm, crypto.sign(`sha${algorithm.slice(-3)}`, data, rsaKeys.privateKey))
  };
}

const edHostKey = testHostKey("ssh-ed25519");

function sessionBind(hostKey, sessionId, signature, forwarding = false) {
  return Buffer.concat([
    Buffer.from([27]),
    string("session-bind@openssh.com"),
    string(hostKey),
    string(sessionId),
    string(signature),
    Buffer.from([forwarding ? 1 : 0])
  ]);
}

function signRequest(key, data, flags = 0) {
  return Buffer.concat([Buffer.from([13]), string(key), string(data), uint32(flags)]);
}

function loginData({
  sessionId,
  key,
  algorithm = keyType(key),
  hostKey = edHostKey.blob,
  user = "git",
  method = HOSTBOUND,
  service = "ssh-connection",
  hasSignature = 1,
  messageType = 50
}) {
  return Buffer.concat([
    string(sessionId),
    Buffer.from([messageType]),
    string(user),
    string(service),
    string(method),
    Buffer.from([hasSignature]),
    string(algorithm),
    string(key),
    method === HOSTBOUND ? string(hostKey) : Buffer.alloc(0)
  ]);
}

function userKey(type) {
  return Buffer.concat([string(type), string(crypto.randomBytes(32))]);
}

function boundGuard() {
  const guard = createConnectionGuard();
  const sessionId = crypto.randomBytes(32);
  assert.equal(guard.check(sessionBind(edHostKey.blob, sessionId, edHostKey.sign(sessionId))).action, "forward");
  return { guard, sessionId };
}

test("forwards identities and a session-bind signed by each host key algorithm, but not a tampered one", () => {
  assert.deepEqual(createConnectionGuard().check(Buffer.from([11])), { action: "forward" });

  for (const algorithm of ["ssh-ed25519", ...Object.keys(ECDSA), "rsa-sha2-256", "rsa-sha2-512"]) {
    const hostKey = testHostKey(algorithm);
    const sessionId = crypto.randomBytes(32);
    const signature = hostKey.sign(sessionId);
    assert.deepEqual(createConnectionGuard().check(sessionBind(hostKey.blob, sessionId, signature)), { action: "forward" }, algorithm);

    const tampered = Buffer.from(signature);
    tampered[tampered.length - 1] ^= 1;
    assert.equal(createConnectionGuard().check(sessionBind(hostKey.blob, sessionId, tampered)).action, "refuse", algorithm);
  }
});

test("refuses session-binds with SHA-1 RSA, certificate, unknown or invalid host keys", () => {
  const sessionId = crypto.randomBytes(32);
  const rsaBlob = testHostKey("rsa-sha2-256").blob;
  const certificate = Buffer.concat([string("ssh-ed25519-cert-v01@openssh.com"), edHostKey.blob.subarray(15)]);
  const offCurve = Buffer.concat([string("ecdsa-sha2-nistp256"), string("nistp256"), string(Buffer.alloc(65, 4))]);
  const binds = [
    sessionBind(rsaBlob, sessionId, sshSignature("ssh-rsa", crypto.sign("sha1", sessionId, rsaKeys.privateKey))),
    sessionBind(certificate, sessionId, edHostKey.sign(sessionId)),
    sessionBind(Buffer.concat([string("ssh-dss"), string("key")]), sessionId, edHostKey.sign(sessionId)),
    sessionBind(offCurve, sessionId, sshSignature("ecdsa-sha2-nistp256", Buffer.concat([mpint(Buffer.alloc(32, 1)), mpint(Buffer.alloc(32, 1))])))
  ];
  for (const bind of binds) assert.equal(createConnectionGuard().check(bind).action, "refuse");
});

test("accepts only session IDs of 32, 48 or 64 bytes", () => {
  for (const length of [32, 48, 64, 0, 31, 33, 47, 65, 256]) {
    const sessionId = crypto.randomBytes(length);
    const result = createConnectionGuard().check(sessionBind(edHostKey.blob, sessionId, edHostKey.sign(sessionId)));
    assert.equal(result.action, [32, 48, 64].includes(length) ? "forward" : "refuse", `${length} bytes`);
  }
});

test("refuses malformed and truncated messages without throwing", () => {
  const sessionId = crypto.randomBytes(32);
  const bind = sessionBind(edHostKey.blob, sessionId, edHostKey.sign(sessionId));
  for (let length = 0; length < bind.length; length++) {
    assert.equal(createConnectionGuard().check(bind.subarray(0, length)).action, "refuse");
  }
  assert.equal(createConnectionGuard().check(Buffer.concat([bind, Buffer.from([0])])).action, "refuse");
  assert.equal(createConnectionGuard().check(Buffer.from([11, 0])).action, "refuse");

  const { guard, sessionId: boundSessionId } = boundGuard();
  const key = userKey("ssh-ed25519");
  const sign = signRequest(key, loginData({ sessionId: boundSessionId, key }));
  for (let length = 0; length < sign.length; length++) {
    assert.equal(guard.check(sign.subarray(0, length)).action, "refuse");
  }
  assert.equal(guard.check(Buffer.concat([sign, Buffer.from([0])])).action, "refuse");
  assert.equal(guard.check(Buffer.from([13, 0xff, 0xff, 0xff, 0xff])).action, "refuse");
  assert.equal(guard.check(sign).action, "sign");
});

test("refuses agent requests other than identities, sign and session-bind", () => {
  const { guard } = boundGuard();
  // Adds, removes, smartcard keys, lock, unlock, constrained adds, then old or unknown types.
  for (const type of [17, 18, 19, 20, 21, 22, 23, 25, 26, 1, 99]) {
    assert.equal(guard.check(Buffer.concat([Buffer.from([type]), string("payload")])).action, "refuse", `type ${type}`);
  }
  for (const extension of ["query", "restrict-destination-v00@openssh.com", "other@example.com"]) {
    assert.equal(guard.check(Buffer.concat([Buffer.from([27]), string(extension)])).action, "refuse", extension);
  }
});

test("refuses a sign request before a session-bind, like ssh-keygen -Y sign sends", () => {
  const key = userKey("ssh-ed25519");
  const sshsig = Buffer.concat([Buffer.from("SSHSIG"), string("git"), string(""), string("sha512"), string(crypto.randomBytes(64))]);
  assert.equal(createConnectionGuard().check(signRequest(key, sshsig)).action, "refuse");
  assert.equal(createConnectionGuard().check(signRequest(key, loginData({ sessionId: crypto.randomBytes(32), key }))).action, "refuse");
});

test("returns the key, bound host key and login of a sign request for the bound session", () => {
  const cases = [
    ["ssh-ed25519", "ssh-ed25519", 0],
    ["ecdsa-sha2-nistp384", "ecdsa-sha2-nistp384", 0],
    ["ssh-rsa", "rsa-sha2-256", 2],
    ["ssh-rsa", "rsa-sha2-512", 4],
    ["sk-ssh-ed25519@openssh.com", "sk-ssh-ed25519@openssh.com", 0],
    ["sk-ecdsa-sha2-nistp256@openssh.com", "sk-ecdsa-sha2-nistp256@openssh.com", 0]
  ];
  for (const [type, algorithm, flags] of cases) {
    for (const method of ["publickey", HOSTBOUND]) {
      const { guard, sessionId } = boundGuard();
      const key = userKey(type);
      const result = guard.check(signRequest(key, loginData({ sessionId, key, algorithm, method }), flags));
      assert.deepEqual(result, { action: "sign", key, hostKey: edHostKey.blob, user: "git" }, `${algorithm} ${method}`);
    }
  }
});

test("refuses sign requests that aren't a login to the bound session with the requested key", () => {
  const { guard, sessionId } = boundGuard();
  const key = userKey("ssh-ed25519");
  const rsaKey = userKey("ssh-rsa");
  const certificate = userKey("ssh-ed25519-cert-v01@openssh.com");
  const login = (fields = {}) => loginData({ sessionId, key, ...fields });
  const sshsig = Buffer.concat([Buffer.from("SSHSIG"), string("file"), string(""), string("sha512"), string(crypto.randomBytes(64))]);
  const refused = {
    "another session": signRequest(key, login({ sessionId: crypto.randomBytes(32) })),
    "another message": signRequest(key, login({ messageType: 51 })),
    "another service": signRequest(key, login({ service: "ssh-userauth" })),
    "another method": signRequest(key, login({ method: "hostbased" })),
    "no signature flag": signRequest(key, login({ hasSignature: 0 })),
    "another key in the data": signRequest(key, login({ key: userKey("ssh-ed25519") })),
    "another host key": signRequest(key, login({ hostKey: testHostKey("ssh-ed25519").blob })),
    "trailing data": signRequest(key, Buffer.concat([login(), Buffer.from([0])])),
    "SSHSIG data": signRequest(key, sshsig),
    "another algorithm": signRequest(key, login({ algorithm: "ecdsa-sha2-nistp256" })),
    "ed25519 with RSA flags": signRequest(key, login(), 2),
    "SHA-1 RSA": signRequest(rsaKey, login({ key: rsaKey, algorithm: "ssh-rsa" })),
    "RSA SHA-2 without flags": signRequest(rsaKey, login({ key: rsaKey, algorithm: "rsa-sha2-512" })),
    "RSA with the other flag": signRequest(rsaKey, login({ key: rsaKey, algorithm: "rsa-sha2-256" }), 4),
    certificate: signRequest(certificate, login({ key: certificate }))
  };
  for (const [name, message] of Object.entries(refused)) assert.equal(guard.check(message).action, "refuse", name);
  assert.equal(guard.check(signRequest(key, login())).action, "sign");
});

test("returns the login as ssh sent it, and refuses one that is too long, not UTF-8 or has NUL", () => {
  const key = userKey("ssh-ed25519");
  const sign = (user) => {
    const { guard, sessionId } = boundGuard();
    return guard.check(signRequest(key, loginData({ sessionId, key, user })));
  };
  for (const user of ["jiří", "\ufeffgit", "a".repeat(256)]) assert.equal(sign(user).user, user);

  const refused = {
    "257 bytes": "a".repeat(257),
    "257 bytes in UTF-8": "ř".repeat(128) + "a",
    "invalid UTF-8": Buffer.from([0x67, 0xff, 0x74]),
    "a lone surrogate": Buffer.from([0xed, 0xa0, 0x80]),
    NUL: "git\u0000"
  };
  for (const [name, user] of Object.entries(refused)) assert.equal(sign(user).action, "refuse", name);
});

test("refuses every session-bind after the first", () => {
  const { guard, sessionId } = boundGuard();
  const second = crypto.randomBytes(32);
  const key = userKey("ssh-ed25519");
  assert.equal(guard.check(sessionBind(edHostKey.blob, second, edHostKey.sign(second))).action, "refuse");
  assert.equal(guard.check(signRequest(key, loginData({ sessionId: second, key }))).action, "refuse");
  assert.equal(guard.check(signRequest(key, loginData({ sessionId, key }))).action, "sign");
});

test("refuses a forwarded connection from its first bind on", () => {
  // The frames ssh -A sends: the outer session's forwarding bind, then the inner ssh's bind and login.
  const guard = createConnectionGuard();
  const outer = crypto.randomBytes(32);
  const inner = crypto.randomBytes(32);
  const key = userKey("ssh-ed25519");
  const messages = [
    sessionBind(edHostKey.blob, outer, edHostKey.sign(outer), true),
    sessionBind(edHostKey.blob, inner, edHostKey.sign(inner)),
    Buffer.from([11]),
    signRequest(key, loginData({ sessionId: inner, key }))
  ];
  for (const message of messages) assert.equal(guard.check(message).action, "refuse");
});

test("remembers key comments from the identities answer", () => {
  const guard = createConnectionGuard();
  const laptop = userKey("ssh-ed25519");
  const work = userKey("ssh-rsa");
  guard.observe(Buffer.from([11]), Buffer.concat([Buffer.from([12]), uint32(2), string(laptop), string("vitek@laptop"), string(work), string("work")]));
  assert.equal(guard.comment(laptop), "vitek@laptop");
  assert.equal(guard.comment(work), "work");
  assert.equal(guard.comment(userKey("ssh-ed25519")), "");
});

test("finds a host key in known_hosts by its raw key", () => {
  const key = edHostKey.blob;
  const entry = `ssh-ed25519 ${key.toString("base64")}`;
  const otherEntry = `ssh-ed25519 ${userKey("ssh-ed25519").toString("base64")}`;
  const userFile = [
    "# comment",
    "",
    "malformed line",
    `#old.example ${entry}`,
    `other.example ${otherEntry}`,
    `wrongtype.example ssh-rsa ${key.toString("base64")}`,
    `github.com,140.82.112.3,!evil.example ${entry} vitek@laptop`,
    `|1|c2FsdA==|aGFzaA== ${entry}`
  ].join("\n");
  assert.deepEqual(findKnownHost(key, [userFile, `[git.example]:2222 ${entry}\r\n`]), {
    status: "known",
    names: ["github.com", "140.82.112.3", "[git.example]:2222"]
  });
  assert.deepEqual(findKnownHost(key, [`|1|c2FsdA==|aGFzaA== ${entry}`]), { status: "known", names: [] });
  assert.deepEqual(findKnownHost(key, [userFile, `@revoked * ${entry}`]), { status: "revoked" });
  assert.deepEqual(findKnownHost(key, [`@cert-authority *.example ${entry}`, `other.example ${otherEntry}`]), { status: "unknown" });
  assert.deepEqual(findKnownHost(key, []), { status: "unknown" });
});

const missingOpenSsh = !fs.existsSync("/usr/sbin/sshd") || !["ssh", "ssh-agent", "ssh-add", "ssh-keygen"].every(onPath);

describe("with real OpenSSH", { skip: missingOpenSsh && "OpenSSH client, agent or server is missing" }, () => {
  let dir;
  let port;
  let sshd;
  let agent;
  let relay;
  let pcKnownHosts;
  const publicKeys = {};
  const file = (name) => path.join(dir, name);

  before(async () => {
    dir = fs.mkdtempSync(path.join(os.tmpdir(), "gripi-ssh-guard-"));
    port = await freePort();
    const keys = {
      host_ed25519: ["-t", "ed25519"],
      host_ecdsa: ["-t", "ecdsa", "-b", "256"],
      host_rsa: ["-t", "rsa", "-b", "2048"],
      other_host: ["-t", "ed25519"],
      client_ed25519: ["-t", "ed25519"],
      client_rsa: ["-t", "rsa", "-b", "2048"],
      client_constrained: ["-t", "ed25519"]
    };
    for (const [name, args] of Object.entries(keys)) {
      execFileSync("ssh-keygen", ["-q", "-N", "", "-C", name, ...args, "-f", file(name)]);
      publicKeys[name] = fs.readFileSync(file(`${name}.pub`), "utf8").split(" ").slice(0, 2).join(" ");
    }

    const clients = ["client_ed25519", "client_rsa", "client_constrained"];
    fs.writeFileSync(file("authorized_keys"), clients.map((name) => `${publicKeys[name]}\n`).join(""));
    fs.writeFileSync(
      file("known_hosts"),
      ["host_ed25519", "host_ecdsa", "host_rsa"].map((name) => `[127.0.0.1]:${port} ${publicKeys[name]}\n`).join("")
    );
    fs.writeFileSync(file("other_known_hosts"), `otherhost ${publicKeys.other_host}\n`);
    const sshdConfig = [
      "ListenAddress 127.0.0.1",
      `Port ${port}`,
      `HostKey ${file("host_ed25519")}`,
      `HostKey ${file("host_ecdsa")}`,
      `HostKey ${file("host_rsa")}`,
      `AuthorizedKeysFile ${file("authorized_keys")}`,
      `PidFile ${file("sshd.pid")}`,
      "UsePAM no",
      "StrictModes no",
      "PasswordAuthentication no",
      "KbdInteractiveAuthentication no"
    ];
    // Refused logins must not lock out the next test. sshd before 9.8 has no penalties, nor the option.
    fs.writeFileSync(file("sshd_config"), [...sshdConfig, "PerSourcePenalties no"].join("\n") + "\n");
    try {
      execFileSync("/usr/sbin/sshd", ["-t", "-f", file("sshd_config")], { stdio: "ignore" });
    } catch {
      fs.writeFileSync(file("sshd_config"), sshdConfig.join("\n") + "\n");
    }

    sshd = await startDaemon("/usr/sbin/sshd", ["-D", "-e", "-f", file("sshd_config")], /Server listening/);
    agent = await startDaemon("ssh-agent", ["-D", "-a", file("agent.sock")], /Agent pid/);
    const agentEnv = { ...process.env, SSH_AUTH_SOCK: file("agent.sock") };
    execFileSync("ssh-add", [file("client_ed25519"), file("client_rsa")], { env: agentEnv, stdio: "ignore" });
    execFileSync("ssh-add", ["-H", file("other_known_hosts"), "-h", "otherhost", file("client_constrained")], { env: agentEnv, stdio: "ignore" });
    // Without the private keys, every login must go through the agent.
    for (const name of clients) fs.rmSync(file(name));

    relay = await startRelay(file("relay.sock"), file("agent.sock"), () => pcKnownHosts);
  });

  after(() => {
    sshd?.kill();
    agent?.kill();
    relay?.server.close();
    if (dir) fs.rmSync(dir, { recursive: true, force: true });
  });

  beforeEach(() => {
    pcKnownHosts = ["host_ed25519", "host_ecdsa", "host_rsa"].map((name) => `testhost ${publicKeys[name]}\n`).join("");
    relay.connections.length = 0;
  });

  function sshArgs(options = [], identity = "client_ed25519") {
    return [
      "-F", "/dev/null",
      "-p", String(port),
      "-o", `UserKnownHostsFile=${file("known_hosts")}`,
      "-o", "GlobalKnownHostsFile=/dev/null",
      "-o", "StrictHostKeyChecking=yes",
      "-o", "BatchMode=yes",
      "-o", "IdentitiesOnly=yes",
      "-o", `IdentityFile=${file(identity)}`,
      "-o", "ControlMaster=no",
      "-o", "ControlPath=none",
      ...options,
      "127.0.0.1"
    ];
  }

  const ssh = (options, identity, command = "true") => run("ssh", [...sshArgs(options, identity), command], file("relay.sock"));
  const entries = () => relay.connections.flat();
  const signReplies = () => entries().filter((entry) => entry.result.action === "sign").map((entry) => entry.reply[0]);

  test("fingerprints keys like ssh-keygen -lf", () => {
    for (const name of ["host_ed25519", "host_ecdsa", "host_rsa"]) {
      const blob = Buffer.from(publicKeys[name].split(" ")[1], "base64");
      const output = execFileSync("ssh-keygen", ["-lf", file(`${name}.pub`)], { encoding: "utf8" });
      assert.equal(fingerprint(blob), output.split(" ")[1]);
    }
  });

  test("logs in to a known host with each host key algorithm", async () => {
    for (const algorithm of ["ssh-ed25519", "ecdsa-sha2-nistp256", "rsa-sha2-256", "rsa-sha2-512"]) {
      relay.connections.length = 0;
      const result = await ssh(["-o", `HostKeyAlgorithms=${algorithm}`]);
      assert.equal(result.code, 0, `${algorithm}: ${result.stderr}`);
      assert.deepEqual(signReplies(), [14], algorithm);
    }
  });

  test("logs in with an RSA key using either SHA-2 signature", async () => {
    for (const algorithm of ["rsa-sha2-256", "rsa-sha2-512"]) {
      const result = await ssh(["-o", `PubkeyAcceptedAlgorithms=${algorithm}`], "client_rsa");
      assert.equal(result.code, 0, `${algorithm}: ${result.stderr}`);
    }
    assert.deepEqual(signReplies(), [14, 14]);
  });

  test("logs in after key exchanges with 48 and 64 byte session IDs", async () => {
    for (const [kex, length] of [["ecdh-sha2-nistp384", 48], ["sntrup761x25519-sha512@openssh.com", 64]]) {
      relay.connections.length = 0;
      const result = await ssh(["-o", `KexAlgorithms=${kex}`]);
      assert.equal(result.code, 0, `${kex}: ${result.stderr}`);
      const bind = entries().find((entry) => entry.message[0] === 27);
      assert.equal(bindFields(bind.message).sessionId.length, length, kex);
    }
  });

  test("refuses to sign for an unknown or revoked host", async () => {
    pcKnownHosts = `otherhost ${publicKeys.other_host}\n`;
    assert.equal((await ssh(["-o", "HostKeyAlgorithms=ssh-ed25519"])).code, 255);

    pcKnownHosts = `testhost ${publicKeys.host_ed25519}\n@revoked * ${publicKeys.host_ed25519}\n`;
    assert.equal((await ssh(["-o", "HostKeyAlgorithms=ssh-ed25519"])).code, 255);
    assert.deepEqual(signReplies(), [5, 5]);
  });

  test("refuses ssh-keygen -Y sign", async () => {
    fs.writeFileSync(file("message.txt"), "hello\n");
    const result = await run("ssh-keygen", ["-Y", "sign", "-n", "file", "-f", file("client_ed25519.pub"), file("message.txt")], file("relay.sock"));
    assert.notEqual(result.code, 0);
    assert.ok(entries().some((entry) => entry.message[0] === 13 && entry.result.action === "refuse"));
    assert.equal(fs.existsSync(file("message.txt.sig")), false);
  });

  test("leaves a key added with ssh-add -h for another host to the agent's refusal", async () => {
    // The agent hides that key from this host's identities, so ssh never asks; replay a real bind and ask directly.
    assert.equal((await ssh()).code, 0);
    const bind = entries().find((entry) => entry.message[0] === 27).message;
    const { hostKey, sessionId } = bindFields(bind);
    relay.connections.length = 0;
    for (const name of ["client_constrained", "client_ed25519"]) {
      const key = Buffer.from(publicKeys[name].split(" ")[1], "base64");
      const connection = connectAgent(file("relay.sock"));
      await connection.request(bind);
      await connection.request(signRequest(key, loginData({ sessionId, key, hostKey })));
      connection.socket.destroy();
    }
    assert.deepEqual(signReplies(), [5, 14]);
  });

  test("refuses a forwarded agent connection after its first bind", async () => {
    const inner = ["ssh", ...sshArgs(), "true"].map(shellQuote).join(" ");
    const result = await ssh(["-A"], "client_ed25519", inner);
    assert.equal(result.code, 255, result.stderr);
    const [login, ...rest] = relay.connections;
    assert.deepEqual(login.map((entry) => entry.reply[0]), [6, 12, 14]);
    assert.ok(rest.some((connection) => connection.length === 0), "ssh -A checks the agent with an empty connection");
    const forwarded = rest.find((connection) => connection.length > 0);
    assert.equal(forwarded[0].result.reason, "forwarded agent connection");
    assert.ok(forwarded.length > 1 && forwarded.every((entry) => entry.reply[0] === 5));
  });
});

function onPath(command) {
  return process.env.PATH.split(path.delimiter).some((dir) => fs.existsSync(path.join(dir, command)));
}

function shellQuote(value) {
  return `'${value.replaceAll("'", "'\\''")}'`;
}

function bindFields(bind) {
  let offset = 5 + bind.readUInt32BE(1);
  const field = () => {
    const length = bind.readUInt32BE(offset);
    offset += 4 + length;
    return bind.subarray(offset - length, offset);
  };
  return { hostKey: field(), sessionId: field() };
}

async function freePort() {
  const server = net.createServer();
  await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
  const { port } = server.address();
  await new Promise((resolve) => server.close(resolve));
  return port;
}

async function startDaemon(command, args, ready) {
  const child = spawn(command, args, { stdio: ["ignore", "pipe", "pipe"] });
  let output = "";
  await new Promise((resolve, reject) => {
    const read = (chunk) => {
      output += chunk;
      if (ready.test(output)) resolve();
    };
    child.stdout.on("data", read);
    child.stderr.on("data", read);
    child.on("exit", (code) => reject(new Error(`${command} exited with ${code}: ${output}`)));
  });
  return child;
}

function run(command, args, authSocket) {
  return new Promise((resolve) => {
    execFile(command, args, { env: { ...process.env, SSH_AUTH_SOCK: authSocket }, timeout: 10_000 }, (error, _stdout, stderr) => {
      resolve({ code: error ? error.code : 0, stderr });
    });
  });
}

function frame(message) {
  return Buffer.concat([uint32(message.length), message]);
}

function onMessages(socket, callback) {
  let buffer = Buffer.alloc(0);
  socket.on("data", (chunk) => {
    buffer = Buffer.concat([buffer, chunk]);
    while (buffer.length >= 4 && buffer.length >= 4 + buffer.readUInt32BE(0)) {
      const end = 4 + buffer.readUInt32BE(0);
      callback(buffer.subarray(4, end));
      buffer = buffer.subarray(end);
    }
  });
}

function connectAgent(socketPath) {
  const socket = net.connect(socketPath);
  const waiting = [];
  onMessages(socket, (reply) => waiting.shift().resolve(reply));
  socket.on("error", () => {});
  socket.on("close", () => waiting.splice(0).forEach(({ reject }) => reject(new Error("agent closed"))));
  return {
    socket,
    request: (message) => new Promise((resolve, reject) => {
      waiting.push({ resolve, reject });
      socket.write(frame(message));
    })
  };
}

// Plays the desktop app: one upstream agent connection and guard per relayed connection, signing only for known hosts.
async function startRelay(socketPath, agentPath, knownHosts) {
  const connections = [];
  const server = net.createServer((client) => {
    const guard = createConnectionGuard();
    const agent = connectAgent(agentPath);
    const log = [];
    connections.push(log);
    let queue = Promise.resolve();
    onMessages(client, (message) => {
      queue = queue.then(async () => {
        const result = guard.check(message);
        const allowed = result.action === "forward" ||
          (result.action === "sign" && findKnownHost(result.hostKey, [knownHosts()]).status === "known");
        const reply = allowed ? await agent.request(message) : AGENT_FAILURE;
        if (allowed) guard.observe(message, reply);
        log.push({ message, result, reply });
        client.write(frame(reply));
      }).catch(() => client.destroy());
    });
    client.on("error", () => {});
    client.on("close", () => agent.socket.destroy());
    agent.socket.on("close", () => client.destroy());
  });
  await new Promise((resolve) => server.listen(socketPath, resolve));
  return { server, connections };
}
