import assert from "node:assert/strict";
import { test } from "node:test";

import { speechSentences } from "../public/assets/speech_text.js";
import { spokenBars, spokenText, VOICE_PREFIX } from "../public/assets/formatting.js";
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
  assert.equal(call.changes.at(-1).sentence, first);
  await call.finishSentence();
  assert.deepEqual(call.played, [first, second]);
  assert.equal(call.changes.at(-1).sentence, second);
  await call.finishSentence();

  assert.deepEqual(call.states(), ["listening", "transcribing", "thinking", "speaking", "listening"]);
  assert.equal(call.changes.at(-1).sentence, null);
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
  await call.conversation.start({ working: () => "What is left to do?" });

  call.conversation.replyFinished("Here is the answer you asked for a moment ago.");
  await flush();
  assert.deepEqual(call.changes.at(-1), { state: "listening", level: 0, muted: false, error: "Piper is not running", sentence: null });

  await call.utter();
  assert.deepEqual(call.changes.at(-1), { state: "listening", level: 0, muted: false, error: "Hermes is busy", sentence: null });
  assert.equal(call.recorders.at(-1).state, "recording");
});

test("a Bluetooth headset's microphone is heard, which iOS asks to play and record for", async (t) => {
  const call = voiceCall(t, { transcripts: ["Can you hear me?"] });
  await call.conversation.start();
  assert.equal(call.audioSession.type, "play-and-record");

  await call.utter();

  assert.deepEqual(call.sent, [`${VOICE_PREFIX}Can you hear me?`]);
});

test("a call started while Hermes works waits for its reply", async (t) => {
  const call = voiceCall(t);
  await call.conversation.start({ working: () => "What is left to do?" });
  assert.equal(call.conversation.awaited, "What is left to do?");

  await call.wait(2000);
  assert.equal(call.state(), "thinking");
  assert.deepEqual(call.blips.slice(0, 2), [392, 329.6]);

  call.conversation.replyFinished("```sh\nnpm test\n```");
  assert.equal(call.state(), "listening");
  assert.equal(call.recorders.at(-1).state, "recording");
});

test("talking while Hermes works stops it and sends what was said once its run has ended", async (t) => {
  const call = voiceCall(t, { transcripts: ["Check the staging database instead."] });
  await call.conversation.start({ working: () => "Check the production database." });

  await call.utter(1000);
  assert.deepEqual(call.states(), ["thinking", "listening", "transcribing", "stopping"]);
  assert.equal(call.stops, 1);
  assert.deepEqual(call.sent, []);

  // The stopped run ends without a reply.
  call.conversation.replyFinished("");
  assert.deepEqual(call.sent, [`${VOICE_PREFIX}Check the staging database instead.`]);
  assert.equal(call.state(), "thinking");
  assert.equal(call.conversation.awaited, "Check the staging database instead.");
});

test("noise and talk without words leave Hermes working", async (t) => {
  const call = voiceCall(t, { transcripts: [""] });
  await call.conversation.start({ working: () => "Check the production database." });

  // A cough is too short to be taken for talk.
  call.level = 0.5;
  await call.wait(150);
  call.level = 0;
  await call.wait(2000);
  assert.deepEqual(call.states(), ["thinking"]);
  assert.deepEqual(call.requests, []);

  call.level = 0.5;
  await call.wait(1000);
  assert.equal(call.state(), "listening");
  call.conversation.replyFinished("The production database is fine.");
  call.level = 0;
  await call.wait(1300);

  assert.equal(call.stops, 0);
  assert.deepEqual(call.sent, []);
  assert.deepEqual(call.played, ["The production database is fine."]);
});

test("a reply that comes while you talk over Hermes is skipped for what you said", async (t) => {
  const call = voiceCall(t, { transcripts: ["Use the staging database."] });
  await call.conversation.start({ working: () => "Check the production database." });

  call.level = 0.5;
  await call.wait(1000);
  call.conversation.replyFinished("The production database is fine.");
  call.level = 0;
  await call.wait(1300);

  assert.equal(call.stops, 0);
  assert.deepEqual(call.sent, [`${VOICE_PREFIX}Use the staging database.`]);
  assert.deepEqual(call.spoken(), []);
});

test("talking on after a pause adds to what was said before", async (t) => {
  const first = deferred();
  const call = voiceCall(t, { transcripts: [first.promise, "on the staging server."] });
  await call.conversation.start();
  await call.utter();
  assert.equal(call.state(), "transcribing");

  call.level = 0.5;
  await call.wait(1000);
  assert.equal(call.state(), "listening");
  first.resolve("Run the migrations");
  await flush();
  assert.deepEqual(call.sent, []);

  call.level = 0;
  await call.wait(1300);
  assert.deepEqual(call.sent, [`${VOICE_PREFIX}Run the migrations on the staging server.`]);
});

test("a call whose microphone arrives after Hermes finished listens", async (t) => {
  const microphone = deferred();
  let working = "What is left to do?";
  const call = voiceCall(t, { microphoneReady: microphone.promise });

  const starting = call.conversation.start({ working: () => working });
  working = null;
  microphone.resolve();
  await starting;

  assert.equal(call.state(), "listening");
});

test("talking over the reply stops it and listens", async (t) => {
  const call = voiceCall(t);
  await call.conversation.start({ working: () => "What is left to do?" });
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

test("quiet blips fill the wait from the end of an utterance until the reply is spoken", async (t) => {
  const transcript = deferred();
  const call = voiceCall(t, { transcripts: [transcript.promise] });
  await call.conversation.start();
  await call.wait(2000);
  assert.deepEqual(call.blips, []);

  await call.utter();
  await call.wait(1000);
  assert.equal(call.state(), "transcribing");
  assert.deepEqual(call.blips, [392]);

  transcript.resolve("How long will the build take?");
  await call.wait(2000);
  assert.equal(call.state(), "thinking");
  assert.deepEqual(call.blips.slice(0, 3), [392, 329.6, 392]);

  call.conversation.replyFinished("About ten minutes, give or take a few.");
  const blips = call.blips.length;
  await call.wait(3000);
  assert.equal(call.state(), "speaking");
  assert.equal(call.blips.length, blips);
});

test("blips wait for suspended audio instead of piling up", async (t) => {
  const call = voiceCall(t);
  await call.conversation.start({ working: () => "What is left to do?" });
  await call.wait(1000);
  assert.deepEqual(call.blips, [392]);

  call.audioContext.state = "suspended";
  await call.wait(5000);
  assert.deepEqual(call.blips, [392]);

  call.audioContext.state = "running";
  await call.wait(50);
  assert.deepEqual(call.blips, [392, 329.6]);
});

test("muting stops recording until the microphone is back", async (t) => {
  const call = voiceCall(t);
  await call.conversation.start();

  call.conversation.setMuted(true);
  assert.equal(call.track.enabled, false);
  assert.equal(call.recorders[0].state, "inactive");
  assert.deepEqual(call.changes.at(-1), { state: "listening", level: 0, muted: true, error: null, sentence: null });

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
  await call.conversation.start({ working: () => "What is left to do?" });
  call.conversation.replyFinished("A reply that is long enough to be spoken on its own.");
  await flush();

  call.playbackLevel = 0.5;
  await call.wait(500);
  assert.equal(call.changes.at(-1).level, 0.5);
  assert.equal(call.state(), "speaking");

  // Muting silences the microphone, not Hermes.
  call.conversation.setMuted(true);
  call.playbackLevel = 1;
  await call.wait(500);
  assert.equal(call.changes.at(-1).level, 1);
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
  assert.deepEqual(call.audioContexts.map((context) => context.state), ["closed", "closed"]);
  assert.equal(call.audioSession.type, "auto");

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

  assert.deepEqual(call.changes, [{ state: "off", level: 0, muted: false, error: "Microphone access was denied", sentence: null }]);
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
    ["Hermes, to je všechno.", true],
    ["Konec hovoru!", true],
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

// The gateway draws the same bars, in spokenBars in internal/server/views.go.
test("spoken messages show bars that follow the words that were said", () => {
  const cases = [
    ["What time is it?", "█▄ ▇▇ ▇ ▅"],
    ["ok přesuň Pavla na čtyři, jestli může, a řekni mu že it's about the gripy release and the the charger", "▃ ▆▇▅ ▃▇▄ █ ▂▅▄ ▁▃▄ ▃▇ ▄ ▂█▄ █ ▇ ▅ ▆ ▅▄▂ ▄▇ ▄▇▆ ▃▇▃▄ ▆▆ ▄▇ ▄▇ ▃▄"]
  ];
  for (const [text, bars] of cases) assert.equal(spokenBars(text), bars, text);
});

function voiceCall(t, { transcripts = [], recordingTypes = ["audio/webm;codecs=opus"], send, speakError, microphoneError, microphoneReady, recordingReady } = {}) {
  t.mock.timers.enable({ apis: ["setInterval", "Date"] });
  const call = { level: 0, playbackLevel: 0, sequence: [], requests: [], sent: [], stops: 0, played: [], blips: [], changes: [], recorders: [], locks: [], audioContexts: [] };
  const track = { enabled: true, readyState: "live", stopped: false, stop() { this.stopped = true; } };
  const stream = { getTracks: () => [track], getAudioTracks: () => [track] };
  const document = new FakeEventTarget();
  document.hidden = false;

  class AudioContext {
    constructor() {
      call.sequence.push("audio-context");
      call.audioContexts.push(this);
      // The first context, made in the tap, plays the call.
      call.audioContext ??= this;
      this.madeAfterMicrophone = call.microphoneGranted === true;
      this.state = "suspended";
      this.destination = {};
      this.currentTime = 0;
    }
    async resume() { call.sequence.push("resume-audio"); this.state = "running"; }
    async close() { this.state = "closed"; }
    createAnalyser() {
      const analyser = {
        // The analyser that plays into the speakers measures the reply, the other the microphone.
        connect(node) { analyser.playback = node === call.audioContext.destination; },
        getByteTimeDomainData: (samples) => {
          const level = analyser.playback ? call.playbackLevel : (analyser.hearsMicrophone ? call.level : 0);
          samples.fill(128 + Math.round(level * 42));
        }
      };
      return analyser;
    }
    createMediaStreamSource() {
      // Like iOS with AirPods (WebKit bug 212653), a context made before the microphone was granted hears silence.
      return { connect: (analyser) => { analyser.hearsMicrophone = this.madeAfterMicrophone; } };
    }
    createOscillator() {
      const oscillator = {
        frequency: { setValueAtTime(value) { oscillator.pitch = value; }, exponentialRampToValueAtTime() {} },
        connect: (node) => node,
        start() { call.blips.push(oscillator.pitch); },
        stop() {}
      };
      return oscillator;
    }
    createGain() {
      return { gain: { setValueAtTime() {}, exponentialRampToValueAtTime() {} }, connect: (node) => node };
    }
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
        call.microphoneGranted = true;
        return stream;
      }
    },
    audioSession: { type: "auto" },
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
    audioSession: navigator.audioSession,
    conversation: new VoiceConversation({
      window: { AudioContext, MediaRecorder, document },
      navigator,
      fetch,
      send: send ?? (async (text) => { call.sent.push(text); }),
      stop: () => { call.stops += 1; },
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
    async utter(milliseconds = 300) {
      call.level = 0.5;
      await call.wait(milliseconds);
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
