// Stands in for Hermes's dashboard in tests: its password login, and the speech endpoints that voice conversations use.
import { createServer } from "node:http";
import { hermes } from "./contract.mjs";

const SESSION_COOKIE = "hermes_session_at=fake-session";
const RATE = 48_000;

export function startFakeHermesDashboard({ username, password }) {
  const server = createServer(async (request, response) => {
    let body = "";
    for await (const chunk of request) body += chunk;
    const payload = body ? JSON.parse(body) : {};
    const answer = (status, value, headers = {}) => {
      response.writeHead(status, { "Content-Type": "application/json", ...headers });
      response.end(JSON.stringify(value));
    };
    if (request.method === "POST" && request.url === "/auth/password-login") {
      if (payload.provider !== "basic" || payload.username !== username || payload.password !== password) return answer(401, { detail: "Invalid username or password" });
      return answer(200, { ok: true, next: "/" }, { "Set-Cookie": `${SESSION_COOKIE}; Path=/; HttpOnly` });
    }
    if (!request.headers.cookie?.split(/;\s*/).includes(SESSION_COOKIE)) return answer(401, { detail: "Not authenticated" });
    if (request.method === "POST" && request.url === "/api/audio/transcribe") {
      return answer(200, { ok: true, transcript: hermes.voice.transcript, provider: "fake" });
    }
    if (request.method === "POST" && request.url === "/api/audio/speak") {
      const speech = wav(new Float32Array(RATE / 5));
      return answer(200, { ok: true, data_url: `data:audio/wav;base64,${speech.toString("base64")}`, mime_type: "audio/wav", provider: "fake" });
    }
    answer(404, { detail: "Not Found" });
  });
  return new Promise((resolve) => server.listen(0, "127.0.0.1", () => resolve(server)));
}

// What the browser's fake microphone hears: a second of a voice-like hum, loud enough to count as speech, then silence.
export function microphoneRecording() {
  const samples = new Float32Array(RATE * 4);
  for (let index = 0; index < RATE; index += 1) {
    const time = index / RATE;
    const voice = Math.sin(2 * Math.PI * 180 * time) + 0.5 * Math.sin(2 * Math.PI * 360 * time) + 0.25 * Math.sin(2 * Math.PI * 720 * time);
    samples[index] = 0.3 * voice * (0.6 + 0.4 * Math.sin(2 * Math.PI * 4 * time));
  }
  return wav(samples);
}

// A mono 16-bit PCM WAV file of samples between -1 and 1.
function wav(samples) {
  const file = Buffer.alloc(44 + samples.length * 2);
  file.write("RIFF", 0);
  file.writeUInt32LE(file.length - 8, 4);
  file.write("WAVEfmt ", 8);
  file.writeUInt32LE(16, 16);
  file.writeUInt16LE(1, 20);
  file.writeUInt16LE(1, 22);
  file.writeUInt32LE(RATE, 24);
  file.writeUInt32LE(RATE * 2, 28);
  file.writeUInt16LE(2, 32);
  file.writeUInt16LE(16, 34);
  file.write("data", 36);
  file.writeUInt32LE(samples.length * 2, 40);
  samples.forEach((sample, index) => file.writeInt16LE(Math.round(Math.max(-1, Math.min(1, sample)) * 32767), 44 + index * 2));
  return file;
}
