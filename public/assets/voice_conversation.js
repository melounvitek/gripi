import { VOICE_PREFIX } from "./formatting.js";
import { speechSentences } from "./speech_text.js";

// Timings and levels follow Hermes Desktop's voice conversation.
const SAMPLE_MS = 50;
const LEVEL_REPORT_MS = 100;
const SPEECH_LEVEL = 0.075;
const END_SILENCE_MS = 1250;
const IDLE_MS = 12_000;
const MAX_UTTERANCE_MS = 60_000;
const BARGE_IN_LEVEL = 0.14;
const BARGE_IN_MS = 300;
const BARGE_IN_SHARE = 0.8;
const RECORDING_TYPES = ["audio/webm;codecs=opus", "audio/webm", "audio/mp4", "audio/ogg;codecs=opus", "audio/ogg", "audio/wav"];
const ADDRESSES = ["hey hermes", "hermes", "ok", "okay", "hey"];
const STOP_PHRASES = new Set([
  "stop", "stop listening", "stop it", "stop please", "please stop", "stop stop", "that is all", "that's all",
  "never mind", "nevermind", "end conversation", "end the conversation", "goodbye", "good bye", "bye", "cancel"
].map(normalizePhrase));

export function isStopPhrase(text) {
  const phrase = normalizePhrase(text);
  const address = ADDRESSES.find((prefix) => phrase.startsWith(`${prefix} `));
  return STOP_PHRASES.has(phrase) || (address !== undefined && STOP_PHRASES.has(phrase.slice(address.length + 1)));
}

function normalizePhrase(text) {
  return text.normalize("NFKC").toLowerCase().replace(/\p{P}+/gu, " ").replace(/\s+/g, " ").trim();
}

export class VoiceConversation {
  constructor({ window, navigator = window.navigator, fetch = window.fetch.bind(window), send, onChange }) {
    this.window = window;
    this.navigator = navigator;
    this.fetch = fetch;
    this.send = send;
    this.onChange = onChange;
    this.state = "off";
    this.level = 0;
    this.levelAt = 0;
    this.muted = false;
    this.error = null;
    // Whether Hermes is working on a turn whose reply the call waits for: its prompt was taken, or it was already running.
    this.awaitingReply = false;
    // Bumped on every state change; async work started in a state is dropped once it has moved on.
    this.generation = 0;
    this.audioContext = null;
    this.stream = null;
    this.recorder = null;
    this.source = null;
    this.wakeLock = null;
    // A hidden page releases the wake lock and may suspend audio, so a visible one takes them back.
    this.onVisibilityChange = () => {
      if (this.window.document.hidden) return;
      this.lockScreen();
      this.audioContext.resume().catch(() => {});
    };
  }

  get active() {
    return this.state !== "off";
  }

  // busy tells, once the microphone is ready, whether Hermes is working, whose reply the call then waits for.
  async start({ busy = () => false } = {}) {
    if (this.audioContext) return;
    // iOS only lets a page play audio from a context created inside the user's tap, so before any await.
    const audioContext = new this.window.AudioContext();
    audioContext.resume().catch(() => {});
    this.audioContext = audioContext;
    this.muted = false;
    let stream;
    try {
      stream = await this.navigator.mediaDevices.getUserMedia({ audio: { echoCancellation: true, noiseSuppression: true, autoGainControl: true } });
    } catch (error) {
      if (this.audioContext === audioContext) this.end(microphoneError(error));
      return;
    }
    if (this.audioContext !== audioContext) {
      stream.getTracks().forEach((track) => track.stop());
      return;
    }

    this.stream = stream;
    this.analyser = audioContext.createAnalyser();
    this.analyser.fftSize = 256;
    this.samples = new Uint8Array(this.analyser.fftSize);
    audioContext.createMediaStreamSource(stream).connect(this.analyser);
    // Replies play through their own analyser, so the meter can show Hermes speaking.
    this.playbackAnalyser = audioContext.createAnalyser();
    this.playbackAnalyser.fftSize = 256;
    this.playbackAnalyser.connect(audioContext.destination);
    // A timer rather than requestAnimationFrame, which stops whenever the page isn't rendered.
    this.timer = setInterval(() => this.tick(), SAMPLE_MS);
    this.window.document.addEventListener("visibilitychange", this.onVisibilityChange);
    this.lockScreen();
    if (busy()) {
      this.setState("thinking");
      this.awaitingReply = true;
    } else {
      this.listen();
    }
  }

  setMuted(muted) {
    if (!this.stream || muted === this.muted) return;
    this.muted = muted;
    this.stream.getAudioTracks().forEach((track) => { track.enabled = !muted; });
    if (this.state === "listening") {
      if (muted) this.discardRecording();
      else this.record();
    }
    this.level = 0;
    this.emit();
  }

  replyFinished(text) {
    if (this.state === "thinking") this.speak(text);
  }

  end(error = null) {
    if (!this.audioContext) return;
    clearInterval(this.timer);
    this.discardRecording();
    this.stopPlayback();
    this.stream?.getTracks().forEach((track) => track.stop());
    this.wakeLock?.release();
    this.audioContext.close().catch(() => {});
    this.window.document.removeEventListener("visibilitychange", this.onVisibilityChange);
    this.audioContext = this.stream = this.wakeLock = null;
    this.setState("off", error);
  }

  listen(error = null) {
    this.setState("listening", error);
    if (!this.muted) this.record();
  }

  tick() {
    // iOS may end the microphone while the screen is locked, and only a tap can ask for it again.
    if (this.stream.getAudioTracks().every((track) => track.readyState === "ended")) {
      this.end("The microphone stopped. Start the call again.");
      return;
    }
    const now = Date.now();
    const sampling = !this.muted && (this.state === "listening" || this.state === "speaking");
    const level = sampling ? this.measure(this.analyser) : 0;
    const shown = this.state === "speaking" ? this.measure(this.playbackAnalyser) : level;
    if (shown !== this.level && now - this.levelAt >= LEVEL_REPORT_MS) {
      this.level = shown;
      this.levelAt = now;
      this.emit();
    }
    if (this.state === "listening" && this.recorder) this.detectUtterance(level, now);
    else if (this.state === "speaking") this.detectBargeIn(level, now);
  }

  measure(analyser) {
    analyser.getByteTimeDomainData(this.samples);
    let sum = 0;
    for (const sample of this.samples) sum += (sample - 128) ** 2;
    return Math.min(1, Math.sqrt(sum / this.samples.length) / 42);
  }

  detectUtterance(level, now) {
    if (level >= SPEECH_LEVEL) {
      this.heardSpeech = true;
      this.quietSince = null;
    } else if (this.heardSpeech) {
      this.quietSince ??= now;
    }
    const length = now - this.recordingSince;
    if (!this.heardSpeech && length >= IDLE_MS) {
      this.discardRecording();
      this.record();
    } else if (this.heardSpeech && ((this.quietSince !== null && now - this.quietSince >= END_SILENCE_MS) || length >= MAX_UTTERANCE_MS)) {
      this.transcribe();
    }
  }

  detectBargeIn(level, now) {
    this.recentLevels.push({ at: now, loud: level >= BARGE_IN_LEVEL });
    while (now - this.recentLevels[0].at > BARGE_IN_MS) this.recentLevels.shift();
    const span = now - this.recentLevels[0].at;
    const loud = this.recentLevels.filter((sample) => sample.loud).length;
    if (span >= BARGE_IN_MS * BARGE_IN_SHARE && loud >= this.recentLevels.length * BARGE_IN_SHARE) {
      this.stopPlayback();
      this.listen();
    }
  }

  record() {
    const mimeType = RECORDING_TYPES.find((type) => this.window.MediaRecorder.isTypeSupported(type));
    this.recorder = new this.window.MediaRecorder(this.stream, mimeType ? { mimeType } : undefined);
    this.recorder.start();
    this.recordingSince = Date.now();
    this.heardSpeech = false;
    this.quietSince = null;
  }

  discardRecording() {
    if (this.recorder && this.recorder.state !== "inactive") this.recorder.stop();
    this.recorder = null;
  }

  async transcribe() {
    const recorder = this.recorder;
    this.recorder = null;
    this.setState("transcribing");
    const generation = this.generation;
    try {
      // Without a timeslice, the recorder hands over the whole recording in one blob when it stops.
      const recording = await new Promise((resolve) => {
        recorder.ondataavailable = (event) => resolve(event.data);
        recorder.stop();
      });
      if (generation !== this.generation) return;
      const response = await this.request("/voice/transcribe", {
        method: "POST",
        body: recording,
        headers: { "Content-Type": recording.type, Accept: "application/json" }
      });
      const transcript = (await response.json()).transcript?.trim();
      if (generation !== this.generation) return;
      if (!transcript) this.listen();
      else if (isStopPhrase(transcript)) this.end();
      else this.ask(transcript);
    } catch (error) {
      if (generation === this.generation) this.listen(error.message);
    }
  }

  async ask(transcript) {
    this.setState("thinking");
    const generation = this.generation;
    try {
      await this.send(VOICE_PREFIX + transcript);
      if (generation === this.generation) this.awaitingReply = true;
    } catch (error) {
      if (generation === this.generation) this.listen(error.message);
    }
  }

  async speak(text) {
    const sentences = speechSentences(text);
    if (!sentences.length) {
      this.listen();
      return;
    }
    this.recentLevels = [];
    this.setState("speaking");
    const generation = this.generation;
    try {
      let next = this.speech(sentences[0]);
      for (let index = 0; index < sentences.length; index += 1) {
        const audio = await next;
        if (generation !== this.generation) return;
        if (index + 1 < sentences.length) {
          next = this.speech(sentences[index + 1]);
          // Nobody awaits it when playback stops early, so its failure must not go unhandled.
          next.catch(() => {});
        }
        await this.play(audio);
        if (generation !== this.generation) return;
      }
      this.listen();
    } catch (error) {
      if (generation === this.generation) this.listen(error.message);
    }
  }

  async speech(text) {
    const response = await this.request("/voice/speak", { method: "POST", body: new URLSearchParams({ text }) });
    return this.audioContext.decodeAudioData(await response.arrayBuffer());
  }

  play(buffer) {
    return new Promise((resolve) => {
      this.source = this.audioContext.createBufferSource();
      this.source.buffer = buffer;
      this.source.connect(this.playbackAnalyser);
      this.source.onended = resolve;
      this.source.start();
    });
  }

  stopPlayback() {
    this.source?.stop();
    this.source = null;
  }

  async lockScreen() {
    const audioContext = this.audioContext;
    const lock = await this.navigator.wakeLock?.request("screen").catch(() => null);
    if (this.audioContext === audioContext) this.wakeLock = lock;
    else lock?.release();
  }

  async request(url, options) {
    const response = await this.fetch(url, options);
    if (response.ok) return response;
    const body = await response.json().catch(() => null);
    throw new Error(body?.error || `Voice request failed (${response.status})`);
  }

  setState(state, error = null) {
    this.generation += 1;
    this.awaitingReply = false;
    this.state = state;
    this.error = error;
    this.level = 0;
    this.emit();
  }

  emit() {
    this.onChange({ state: this.state, level: this.level, muted: this.muted, error: this.error });
  }
}

function microphoneError(error) {
  if (error?.name === "NotAllowedError") return "Microphone access was denied";
  if (error?.name === "NotFoundError") return "No microphone was found";
  return "The microphone is unavailable";
}
