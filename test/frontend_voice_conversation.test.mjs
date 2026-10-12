import assert from "node:assert/strict";
import { test } from "node:test";

import { speechSentences } from "../public/assets/speech_text.js";
import { spokenText, VOICE_PREFIX } from "../public/assets/formatting.js";
import { isStopPhrase, VoiceConversation } from "../public/assets/voice_conversation.js";
import { deferred, FakeEventTarget } from "./helpers/fake_dom.mjs";

test("a spoken turn is sent with the voice prefix and the reply is spoken sentence by sentence", async (t) => {
  const call = voiceCall(t, { transcripts: ["What's the weather like?"], recordingTypes: ["audio/mp4"] });

  const starting = call.conversation.start();
  assert.deepEqual(call.sequence, ["audio-context", "resume-audio", "get-user-media"]);
  await starting;
  assert.deepEqual(call.constraints, { audio: { echoCancellation: true, noiseSuppression: true, autoGainControl: true } });
  assert.deepEqual(call.locks.map((lock) => lock.type), ["screen"]);
  assert.equal(call.conversation.active, true);
  assert.equal(call.state(), "listening");

  call.level = 0.5;
  await call.wait(500);
  assert.equal(call.changes.at(-1).level, 0.5);
  call.level = 0;
  await call.wait(1200);
  assert.equal(call.state(), "listening");
  await call.wait(100);

  const [transcription] = call.requests;
  assert.equal(transcription.url, "/voice/transcribe");
  assert.equal(transcription.body.type, "audio/mp4");
  assert.deepEqual(transcription.headers, { "Content-Type": "audio/mp4", Accept: "application/json" });
  assert.deepEqual(call.sent, [`${VOICE_PREFIX}What's the weather like?`]);
  assert.equal(call.state(), "thinking");

  call.conversation.replyFinished("It is **sunny** in Prague right now, about 21 degrees.\n\nRain arrives tomorrow, so take an [umbrella](https://example.com/rain).");
  await flush();
  const first = "It is sunny in Prague right now, about 21 degrees.";
  const second = "Rain arrives tomorrow, so take an umbrella.";
  assert.deepEqual(call.spoken(), [first, second]);
  assert.deepEqual(call.played, [first]);
  await call.finishSentence();
  assert.deepEqual(call.played, [first, second]);
  await call.finishSentence();

  assert.deepEqual(call.states(), ["listening", "transcribing", "thinking", "speaking", "listening"]);
  assert.equal(call.recorders.at(-1).state, "recording");
});

test("silence is never sent", async (t) => {
  const call = voiceCall(t, { transcripts: ["  "] });
  await call.conversation.start();

  await call.wait(12_000);
  assert.equal(call.recorders.length, 2);
  assert.equal(call.recorders[0].state, "inactive");
  assert.deepEqual(call.requests, []);

  await call.utter();
  assert.equal(call.requests.length, 1);
  assert.deepEqual(call.sent, []);
  assert.equal(call.state(), "listening");
  assert.equal(call.recorders.at(-1).state, "recording");
});

test("a stop phrase ends the call without sending it", async (t) => {
  const call = voiceCall(t, { transcripts: ["Hey Hermes, stop!"] });
  await call.conversation.start();

  await call.utter();

  assert.deepEqual(call.sent, []);
  assert.equal(call.state(), "off");
  assert.equal(call.conversation.active, false);
  assert.equal(call.track.stopped, true);
});

test("failed requests are reported and the call listens again", async (t) => {
  const call = voiceCall(t, {
    transcripts: ["Run the tests"],
    speakError: "Piper is not running",
    send: async () => { throw new Error("Hermes is busy"); }
  });
  await call.conversation.start({ busy: () => true });

  call.conversation.replyFinished("Here is the answer you asked for a moment ago.");
  await flush();
  assert.deepEqual(call.changes.at(-1), { state: "listening", level: 0, muted: false, error: "Piper is not running" });

  await call.utter();
  assert.deepEqual(call.changes.at(-1), { state: "listening", level: 0, muted: false, error: "Hermes is busy" });
  assert.equal(call.recorders.at(-1).state, "recording");
});

test("a call started while Hermes works waits for its reply", async (t) => {
  const call = voiceCall(t);
  await call.conversation.start({ busy: () => true });

  call.level = 0.5;
  await call.wait(2000);
  assert.equal(call.state(), "thinking");
  assert.deepEqual(call.recorders, []);

  call.conversation.replyFinished("```sh\nnpm test\n```");
  assert.equal(call.state(), "listening");
  assert.equal(call.recorders.length, 1);
});

test("a call whose microphone arrives after Hermes finished listens", async (t) => {
  const microphone = deferred();
  let working = true;
  const call = voiceCall(t, { microphoneReady: microphone.promise });

  const starting = call.conversation.start({ busy: () => working });
  working = false;
  microphone.resolve();
  await starting;

  assert.equal(call.state(), "listening");
});

test("talking over the reply stops it and listens", async (t) => {
  const call = voiceCall(t);
  await call.conversation.start({ busy: () => true });
  call.conversation.replyFinished("The first sentence is long enough to stand alone. The second one should never be heard.");
  await flush();
  assert.deepEqual(call.played, ["The first sentence is long enough to stand alone."]);

  call.level = 0.1;
  await call.wait(1000);
  assert.equal(call.state(), "speaking");

  call.level = 0.3;
  await call.wait(300);
  assert.equal(call.state(), "listening");
  assert.equal(call.playing.stopped, true);
  await call.wait(100);
  assert.deepEqual(call.played, ["The first sentence is long enough to stand alone."]);
  assert.equal(call.recorders.at(-1).state, "recording");
});

test("muting stops recording until the microphone is back", async (t) => {
  const call = voiceCall(t);
  await call.conversation.start();

  call.conversation.setMuted(true);
  assert.equal(call.track.enabled, false);
  assert.equal(call.recorders[0].state, "inactive");
  assert.deepEqual(call.changes.at(-1), { state: "listening", level: 0, muted: true, error: null });

  const muted = call.changes.length;
  call.level = 0.5;
  await call.wait(3000);
  assert.deepEqual(call.requests, []);
  assert.equal(call.recorders.length, 1);
  assert.ok(call.changes.slice(muted).every((change) => change.level === 0));

  call.conversation.setMuted(false);
  assert.equal(call.track.enabled, true);
  assert.equal(call.recorders.at(-1).state, "recording");
});

test("the meter follows Hermes's voice while it speaks", async (t) => {
  const call = voiceCall(t);
  await call.conversation.start({ busy: () => true });
  call.conversation.replyFinished("A reply that is long enough to be spoken on its own.");
  await flush();

  call.playbackLevel = 0.5;
  await call.wait(500);
  assert.equal(call.changes.at(-1).level, 0.5);
  assert.equal(call.state(), "speaking");
});

test("a hidden page keeps the call going and takes the wake lock back when shown", async (t) => {
  const call = voiceCall(t, { transcripts: ["Are you still there?"] });
  await call.conversation.start();

  call.hide();
  await call.utter();
  assert.deepEqual(call.sent, [`${VOICE_PREFIX}Are you still there?`]);

  const locks = call.locks.length;
  call.show();
  await flush();
  assert.equal(call.locks.length, locks + 1);
});

test("a microphone that stops ends the call", async (t) => {
  const call = voiceCall(t);
  await call.conversation.start();

  call.track.readyState = "ended";
  await call.wait(50);
  assert.equal(call.state(), "off");
  assert.equal(call.changes.at(-1).error, "The microphone stopped. Start the call again.");
});

test("ending the call releases the microphone and wake lock and ignores late results", async (t) => {
  const transcript = deferred();
  const call = voiceCall(t, { transcripts: [transcript.promise] });
  await call.conversation.start();
  await call.utter();
  assert.equal(call.state(), "transcribing");

  call.conversation.end();
  assert.equal(call.state(), "off");
  assert.equal(call.conversation.active, false);
  assert.equal(call.track.stopped, true);
  assert.equal(call.locks[0].released, true);
  assert.equal(call.audioContext.state, "closed");

  const changes = call.changes.length;
  call.conversation.end();
  transcript.resolve("Tell me a joke");
  call.conversation.replyFinished("A reply that settles after the call ended.");
  call.hide();
  call.show();
  await call.wait(1000);
  assert.equal(call.changes.length, changes);
  assert.deepEqual(call.sent, []);
});

test("ending the call while the recording is handed over sends nothing", async (t) => {
  const recording = deferred();
  const call = voiceCall(t, { transcripts: ["Never sent"], recordingReady: recording.promise });
  await call.conversation.start();
  await call.utter();
  assert.equal(call.state(), "transcribing");

  call.conversation.end();
  recording.resolve();
  await flush();
  assert.deepEqual(call.requests, []);
});

test("a refused microphone ends the call with an error", async (t) => {
  const call = voiceCall(t, { microphoneError: new DOMException("Permission denied", "NotAllowedError") });

  await call.conversation.start();

  assert.deepEqual(call.changes, [{ state: "off", level: 0, muted: false, error: "Microphone access was denied" }]);
  assert.equal(call.audioContext.state, "closed");
});

test("replies are cleaned for speech and split into sentences", () => {
  const cases = [
    ["## Plan\n\nThis is **really** important, and _very_ urgent.", ["Plan. This is really important, and very urgent."]],
    [
      "First, install the package with npm.\n```sh\nnpm install. Then npm test.\n```\nAfter that, everything should work fine.",
      ["First, install the package with npm.", "After that, everything should work fine."]
    ],
    ["Here you go:\n```js\nconsole.log(1)", ["Here you go."]],
    ["Use `npm test` to run them all.", ["Use npm test to run them all."]],
    ["See https://example.com/docs for the details you need.", ["See for the details you need."]],
    ["Read [the guide](https://example.com/guide) before you start.", ["Read the guide before you start."]],
    ["You need two things:\n\n- a working microphone\n- a quiet room", ["You need two things. a working microphone a quiet room"]],
    ["| Model | Price |\n| --- | ---: |\n| Mini | $1 |\n| Max | $9 |\n\nMini is cheaper.", ["Model, Price. Mini is cheaper."]],
    ["The commit 3f9a2b7c fixed it.", ["The commit fixed it."]],
    ["Yes. It works now, and the tests pass too. Great!", ["Yes. It works now, and the tests pass too.", "Great!"]],
    ["```\nonly code\n```", []],
    ["", []]
  ];
  for (const [reply, sentences] of cases) assert.deepEqual(speechSentences(reply), sentences, reply);
});

test("stop phrases match whole utterances only", () => {
  const cases = [
    ["Stop.", true],
    ["Hey Hermes, stop!", true],
    ["OK, never mind.", true],
    ["That’s all", true],
    ["Ｇｏｏｄｂｙｅ", true],
    ["hermes", false],
    ["Stop the Docker container.", false],
    ["How do I stop a process?", false],
    ["", false]
  ];
  for (const [text, expected] of cases) assert.equal(isStopPhrase(text), expected, text);
});

test("spoken messages are recognised by the voice prefix", () => {
  assert.equal(spokenText(`${VOICE_PREFIX}What time is it?`), "What time is it?");
  assert.equal(spokenText("What time is it?"), null);
  assert.equal(spokenText(`${VOICE_PREFIX.trimEnd()}What time is it?`), null);
});

function voiceCall(t, { transcripts = [], recordingTypes = ["audio/webm;codecs=opus"], send, speakError, microphoneError, microphoneReady, recordingReady } = {}) {
  t.mock.timers.enable({ apis: ["setInterval", "Date"] });
  const call = { level: 0, playbackLevel: 0, sequence: [], requests: [], sent: [], played: [], changes: [], recorders: [], locks: [] };
  const track = { enabled: true, readyState: "live", stopped: false, stop() { this.stopped = true; } };
  const stream = { getTracks: () => [track], getAudioTracks: () => [track] };
  const document = new FakeEventTarget();
  document.hidden = false;

  class AudioContext {
    constructor() {
      call.sequence.push("audio-context");
      call.audioContext = this;
      this.state = "suspended";
      this.destination = {};
    }
    async resume() { call.sequence.push("resume-audio"); this.state = "running"; }
    async close() { this.state = "closed"; }
    createAnalyser() {
      const analyser = {
        // The analyser that plays into the speakers measures the reply, the other the microphone.
        connect(node) { analyser.playback = node === call.audioContext.destination; },
        getByteTimeDomainData: (samples) => samples.fill(128 + Math.round((analyser.playback ? call.playbackLevel : call.level) * 42))
      };
      return analyser;
    }
    createMediaStreamSource() { return { connect() {} }; }
    async decodeAudioData(buffer) { return { text: new TextDecoder().decode(buffer) }; }
    createBufferSource() {
      return {
        connect() {},
        start() { call.playing = this; call.played.push(this.buffer.text); },
        stop() { this.stopped = true; queueMicrotask(() => this.onended()); }
      };
    }
  }

  class MediaRecorder {
    static isTypeSupported(type) { return recordingTypes.includes(type); }
    constructor(recordedStream, options) {
      assert.equal(recordedStream, stream);
      this.mimeType = options?.mimeType ?? "audio/webm";
      this.state = "inactive";
      call.recorders.push(this);
    }
    start() { this.state = "recording"; }
    stop() {
      this.state = "inactive";
      Promise.resolve(recordingReady).then(() => this.ondataavailable?.({ data: new Blob(["voice"], { type: this.mimeType }) }));
    }
  }

  const navigator = {
    mediaDevices: {
      async getUserMedia(constraints) {
        call.sequence.push("get-user-media");
        call.constraints = constraints;
        await microphoneReady;
        if (microphoneError) throw microphoneError;
        return stream;
      }
    },
    wakeLock: {
      async request(type) {
        const lock = { type, released: false, async release() { lock.released = true; } };
        call.locks.push(lock);
        return lock;
      }
    }
  };

  async function fetch(url, { body, headers }) {
    call.requests.push({ url, body, headers });
    if (url === "/voice/transcribe") return response({ transcript: await transcripts.shift() });
    if (speakError) return response({ error: speakError }, 502);
    return { ok: true, status: 200, arrayBuffer: async () => new TextEncoder().encode(body.get("text")).buffer };
  }

  return Object.assign(call, {
    track,
    conversation: new VoiceConversation({
      window: { AudioContext, MediaRecorder, document },
      navigator,
      fetch,
      send: send ?? (async (text) => { call.sent.push(text); }),
      onChange: (change) => call.changes.push(change)
    }),
    state: () => call.changes.at(-1)?.state,
    states: () => call.changes.map((change) => change.state).filter((state, index, states) => state !== states[index - 1]),
    spoken: () => call.requests.filter((request) => request.url === "/voice/speak").map((request) => request.body.get("text")),
    async wait(milliseconds) {
      for (let elapsed = 0; elapsed < milliseconds; elapsed += 50) {
        t.mock.timers.tick(50);
        await flush();
      }
    },
    async utter() {
      call.level = 0.5;
      await call.wait(300);
      call.level = 0;
      await call.wait(1300);
    },
    async finishSentence() {
      call.playing.onended();
      await flush();
    },
    hide() {
      document.hidden = true;
      document.listeners.get("visibilitychange")?.forEach((listener) => listener());
    },
    show() {
      document.hidden = false;
      document.listeners.get("visibilitychange")?.forEach((listener) => listener());
    }
  });
}

function response(body, status = 200) {
  return { ok: status >= 200 && status < 300, status, async json() { return body; } };
}

function flush() {
  return new Promise((resolve) => setImmediate(resolve));
}
