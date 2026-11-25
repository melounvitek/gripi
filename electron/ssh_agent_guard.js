const crypto = require("node:crypto");

const AGENT_FAILURE = Buffer.from([5]);
const REQUEST_IDENTITIES = 11;
const IDENTITIES_ANSWER = 12;
const SIGN_REQUEST = 13;
const EXTENSION = 27;
const USERAUTH_REQUEST = 50;
const HOSTBOUND_METHOD = "publickey-hostbound-v00@openssh.com";
const MAX_SESSION_ID = 256;

const CURVES = new Map([
  ["ecdsa-sha2-nistp256", { name: "nistp256", crv: "P-256", hash: "sha256", size: 32 }],
  ["ecdsa-sha2-nistp384", { name: "nistp384", crv: "P-384", hash: "sha384", size: 48 }],
  ["ecdsa-sha2-nistp521", { name: "nistp521", crv: "P-521", hash: "sha512", size: 66 }]
]);
const RSA_HASHES = new Map([["rsa-sha2-256", "sha256"], ["rsa-sha2-512", "sha512"]]);
const RSA_SIGN_FLAGS = new Map([["rsa-sha2-256", 2], ["rsa-sha2-512", 4]]);
// User key types whose signature algorithm has the key type's name and needs no sign flags.
const PLAIN_USER_KEY_TYPES = new Set([
  "ssh-ed25519",
  "ecdsa-sha2-nistp256",
  "ecdsa-sha2-nistp384",
  "ecdsa-sha2-nistp521",
  "sk-ssh-ed25519@openssh.com",
  "sk-ecdsa-sha2-nistp256@openssh.com"
]);

class Reader {
  constructor(buffer) {
    this.buffer = buffer;
    this.offset = 0;
  }

  bytes(length) {
    if (length > this.buffer.length - this.offset) throw new Error("truncated");
    this.offset += length;
    return this.buffer.subarray(this.offset - length, this.offset);
  }

  byte() {
    return this.bytes(1)[0];
  }

  uint32() {
    return this.bytes(4).readUInt32BE(0);
  }

  string() {
    return this.bytes(this.uint32());
  }

  text() {
    return this.string().toString();
  }

  end() {
    if (this.offset !== this.buffer.length) throw new Error("trailing data");
  }
}

function createConnectionGuard() {
  let bindSeen = false;
  let bound = null;
  let forwarded = false;
  const comments = new Map();

  function checkMessage(reader) {
    if (forwarded) return refuse("forwarded agent connection");
    const type = reader.byte();
    if (type === REQUEST_IDENTITIES) {
      reader.end();
      return { action: "forward" };
    }
    if (type === EXTENSION) return checkSessionBind(reader);
    if (type === SIGN_REQUEST) return checkSignRequest(reader);
    return refuse(`agent request ${type} not allowed`);
  }

  // ssh -A starts a forwarded connection with the outer session's bind, flagged as forwarding, and the
  // inner ssh then sends its own bind. Only the first bind counts, so a forwarding one refuses the rest.
  function checkSessionBind(reader) {
    if (reader.text() !== "session-bind@openssh.com") return refuse("agent extension not allowed");
    if (bindSeen) return refuse("only the first session-bind is allowed");
    bindSeen = true;
    const hostKey = reader.string();
    const sessionId = reader.string();
    const signature = reader.string();
    forwarded = reader.byte() !== 0;
    reader.end();
    if (forwarded) return refuse("forwarded agent connection");
    if (sessionId.length === 0 || sessionId.length > MAX_SESSION_ID) return refuse("bad session ID");
    if (!verifyHostSignature(hostKey, signature, sessionId)) return refuse("session-bind not verified");
    bound = { hostKey, sessionId };
    return { action: "forward" };
  }

  function checkSignRequest(reader) {
    if (!bound) return refuse("sign request without session-bind");
    const key = reader.string();
    const data = reader.string();
    const flags = reader.uint32();
    reader.end();
    const user = loginUser(data, key, flags, bound);
    if (user === null) return refuse("not an SSH login for the bound session");
    return { action: "sign", key, hostKey: bound.hostKey, user };
  }

  return {
    check(message) {
      try {
        return checkMessage(new Reader(message));
      } catch {
        return refuse("malformed agent request");
      }
    },

    observe(request, reply) {
      if (request[0] !== REQUEST_IDENTITIES || reply[0] !== IDENTITIES_ANSWER) return;
      try {
        const reader = new Reader(reply.subarray(1));
        for (let count = reader.uint32(); count > 0; count--) {
          const key = reader.string();
          comments.set(key.toString("base64"), reader.text());
        }
      } catch {
        // A malformed answer only loses comments.
      }
    },

    comment(key) {
      return comments.get(key.toString("base64")) ?? "";
    }
  };
}

function refuse(reason) {
  return { action: "refuse", reason };
}

// The data must be exactly what ssh signs for public key auth (RFC 4252 section 7, with OpenSSH's
// host-bound variant), so the signature can only log in to the bound session.
function loginUser(data, key, flags, bound) {
  const reader = new Reader(data);
  if (!reader.string().equals(bound.sessionId) || reader.byte() !== USERAUTH_REQUEST) return null;
  const user = reader.text();
  if (reader.text() !== "ssh-connection") return null;
  const method = reader.text();
  if (method !== "publickey" && method !== HOSTBOUND_METHOD) return null;
  if (reader.byte() !== 1) return null;
  const algorithm = reader.text();
  if (!reader.string().equals(key) || !signatureMatchesKey(keyType(key), algorithm, flags)) return null;
  if (method === HOSTBOUND_METHOD && !reader.string().equals(bound.hostKey)) return null;
  reader.end();
  return user;
}

function signatureMatchesKey(type, algorithm, flags) {
  if (type === "ssh-rsa") return RSA_SIGN_FLAGS.get(algorithm) === flags;
  return PLAIN_USER_KEY_TYPES.has(type) && algorithm === type && flags === 0;
}

function verifyHostSignature(hostKey, signature, data) {
  const key = new Reader(hostKey);
  const type = key.text();
  const sig = new Reader(signature);
  const algorithm = sig.text();
  const sigBytes = sig.string();
  sig.end();

  if (type === "ssh-ed25519" && algorithm === type) {
    const x = key.string();
    key.end();
    return x.length === 32 && crypto.verify(null, data, jwkKey({ kty: "OKP", crv: "Ed25519", x: base64url(x) }), sigBytes);
  }

  const curve = CURVES.get(type);
  if (curve && algorithm === type) {
    const name = key.text();
    const point = key.string();
    key.end();
    if (name !== curve.name || point.length !== 1 + 2 * curve.size || point[0] !== 4) return false;
    const pair = new Reader(sigBytes);
    const r = unsigned(pair.string());
    const s = unsigned(pair.string());
    pair.end();
    if (r.length > curve.size || s.length > curve.size) return false;
    const publicKey = jwkKey({
      kty: "EC",
      crv: curve.crv,
      x: base64url(point.subarray(1, 1 + curve.size)),
      y: base64url(point.subarray(1 + curve.size))
    });
    const p1363 = Buffer.concat([Buffer.alloc(curve.size - r.length), r, Buffer.alloc(curve.size - s.length), s]);
    return crypto.verify(curve.hash, data, { key: publicKey, dsaEncoding: "ieee-p1363" }, p1363);
  }

  const rsaHash = RSA_HASHES.get(algorithm);
  if (type === "ssh-rsa" && rsaHash) {
    const e = key.string();
    const n = key.string();
    key.end();
    return crypto.verify(rsaHash, data, jwkKey({ kty: "RSA", e: base64url(unsigned(e)), n: base64url(unsigned(n)) }), sigBytes);
  }

  return false;
}

function jwkKey(jwk) {
  return crypto.createPublicKey({ key: jwk, format: "jwk" });
}

function base64url(bytes) {
  return bytes.toString("base64url");
}

function unsigned(mpint) {
  let start = 0;
  while (start < mpint.length - 1 && mpint[start] === 0) start++;
  return mpint.subarray(start);
}

function findKnownHost(hostKey, knownHostsTexts) {
  const type = keyType(hostKey);
  const key = hostKey.toString("base64");
  const names = new Set();
  let known = false;

  for (const text of knownHostsTexts) {
    for (const rawLine of text.split("\n")) {
      const line = rawLine.trim();
      if (!line || line.startsWith("#")) continue;
      const fields = line.split(/\s+/);
      const marker = fields[0].startsWith("@") ? fields.shift() : null;
      if (fields.length < 3 || fields[1] !== type || fields[2] !== key) continue;
      if (marker === "@revoked") return { status: "revoked" };
      if (marker !== null) continue;
      known = true;
      for (const pattern of fields[0].split(",")) {
        if (pattern && !pattern.startsWith("|") && !pattern.startsWith("!")) names.add(pattern);
      }
    }
  }

  return known ? { status: "known", names: [...names] } : { status: "unknown" };
}

function fingerprint(blob) {
  return `SHA256:${crypto.createHash("sha256").update(blob).digest("base64").replace(/=+$/, "")}`;
}

function keyType(blob) {
  return new Reader(blob).text();
}

module.exports = { AGENT_FAILURE, createConnectionGuard, findKnownHost, fingerprint, keyType };
