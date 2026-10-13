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
const FIRST_BLIP_MS = 400;
const BLIP_PITCHES = [392, 329.6];
const RECORDING_TYPES = ["audio/webm;codecs=opus", "audio/webm", "audio/mp4", "audio/ogg;codecs=opus", "audio/ogg", "audio/wav"];
const ADDRESSES = ["hey hermes", "hermes", "ok", "okay", "hey"];
const STOP_PHRASES = new Set([
  "stop", "stop listening", "stop it", "stop please", "please stop", "stop stop", "that is all", "that's all",
  "never mind", "nevermind", "end conversation", "end the conversation", "goodbye", "good bye", "bye", "cancel",
  "konec", "konec hovoru", "ukonči hovor", "to je vše", "to je všechno", "nashledanou", "na shledanou"
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
  // stop stops Hermes's run, unlike end, which ends the call.
  constructor({ window, navigator = window.navigator, fetch = window.fetch.bind(window), send, stop, onChange }) {
    this.window = window;
    this.navigator = navigator;
    this.fetch = fetch;
    this.send = send;
    this.stop = stop;
    this.onChange = onChange;
    this.state = "off";
    this.level = 0;
    this.levelAt = 0;
    this.muted = false;
    this.error = null;
    // The sentence of the reply that is playing.
    this.sentence = null;
    // The message whose reply the call waits for while Hermes works on it: what it said, or what Hermes was working on
    // when it started. Null while Hermes doesn't work for the call.
    this.awaited = null;
    // What was said and not sent yet, an utterance at a time: its transcript, or null while it's being transcribed, and
    // the error of a failed transcription.
    this.said = [];
    // Hermes's reply, kept while you talk, and spoken unless you said something.
    this.reply = null;
    // Bumped on every state change; a reply being spoken stops once the state has moved on.
    this.generation = 0;
    this.audioContext = null;
    this.stream = null;
    this.recorder = null;
    this.heardSpeech = false;
    // Whether proceed waits for a voice that may be starting.
    this.due = false;
    this.source = null;
    this.wakeLock = null;
    this.blips = 0;
    this.blipAt = null;
    // A hidden page releases the wake lock and may suspend audio, so a visible one takes them back.
    this.onVisibilityChange = () => {
      if (this.window.document.hidden) return;
      this.lockScreen();
      this.audioContext.resume().catch(() => {});
      this.microphoneContext.resume().catch(() => {});
    };
  }

  get active() {
    return this.state !== "off";
  }

  // working tells, once the microphone is ready, what Hermes is working on, whose reply the call then waits for, or null.
  async start({ working = () => null } = {}) {
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
    // iOS may leave a Bluetooth headset, such as AirPods, out of the call until the page asks to play and record (WebKit bug 282939).
    if (this.navigator.audioSession) this.navigator.audioSession.type = "play-and-record";
    // iOS may feed silence to an analyser whose context was made before a Bluetooth headset took the microphone (WebKit
    // bug 212653), so the microphone is measured in a context of its own, made now. A capturing page may start it outside a tap.
    this.microphoneContext = new this.window.AudioContext();
    this.microphoneContext.resume().catch(() => {});
    this.analyser = this.microphoneContext.createAnalyser();
    this.analyser.fftSize = 256;
    this.samples = new Uint8Array(this.analyser.fftSize);
    this.microphoneContext.createMediaStreamSource(stream).connect(this.analyser);
    // Replies play through their own analyser, so the meter can show Hermes speaking.
    this.playbackAnalyser = audioContext.createAnalyser();
    this.playbackAnalyser.fftSize = 256;
    this.playbackAnalyser.connect(audioContext.destination);
    // A timer rather than requestAnimationFrame, which stops whenever the page isn't rendered.
    this.timer = setInterval(() => this.tick(), SAMPLE_MS);
    this.window.document.addEventListener("visibilitychange", this.onVisibilityChange);
    this.lockScreen();
    this.awaited = working();
    if (this.awaited === null) this.listen();
    else this.setState("thinking");
  }

  setMuted(muted) {
    if (!this.stream || muted === this.muted) return;
    this.muted = muted;
    this.stream.getAudioTracks().forEach((track) => { track.enabled = !muted; });
    if (muted) {
      const talking = this.heardSpeech;
      this.discardRecording();
      // What was said before muting goes on without the rest.
      if (talking) this.proceed();
    } else if (this.state !== "speaking") {
      this.record();
    }
    this.level = 0;
    this.emit();
  }

  replyFinished(text) {
    if (this.awaited === null) return;
    this.awaited = null;
    this.reply = text;
    this.proceed();
  }

  end(error = null) {
    if (!this.audioContext) return;
    clearInterval(this.timer);
    this.discardRecording();
    this.stopPlayback();
    this.stream?.getTracks().forEach((track) => track.stop());
    this.wakeLock?.release();
    this.audioContext.close().catch(() => {});
    this.microphoneContext?.close().catch(() => {});
    if (this.navigator.audioSession) this.navigator.audioSession.type = "auto";
    this.window.document.removeEventListener("visibilitychange", this.onVisibilityChange);
    this.audioContext = this.microphoneContext = this.stream = this.wakeLock = this.awaited = null;
    this.said = [];
    this.setState("off", error);
  }

  listen(error = null) {
    this.setState("listening", error);
  }

  tick() {
    // iOS may end the microphone while the screen is locked, and only a tap can ask for it again.
    if (this.stream.getAudioTracks().every((track) => track.readyState === "ended")) {
      this.end("The microphone stopped. Start the call again.");
      return;
    }
    const now = Date.now();
    const sampling = this.recorder !== null || (!this.muted && this.state === "speaking");
    const level = sampling ? this.measure(this.analyser) : 0;
    const shown = this.state === "speaking" ? this.measure(this.playbackAnalyser) : level;
    if (shown !== this.level && now - this.levelAt >= LEVEL_REPORT_MS) {
      this.level = shown;
      this.levelAt = now;
      this.emit();
    }
    if (this.recorder) {
      this.detectUtterance(level, now);
    } else if (this.state === "speaking" && this.sustained(level, now)) {
      this.stopPlayback();
      this.listen();
    }
    // Unlike Hermes Desktop's thinking sound, the blips start with transcribing, which takes a request to Hermes.
    if (!["transcribing", "thinking", "stopping"].includes(this.state)) this.blipAt = null;
    else if (this.blipAt === null) this.blipAt = now + FIRST_BLIP_MS;
    // A suspended context's clock stands still, so blips scheduled meanwhile would all play at once when it resumes.
    else if (now >= this.blipAt && this.audioContext.state === "running") this.blip(now);
  }

  measure(analyser) {
    analyser.getByteTimeDomainData(this.samples);
    let sum = 0;
    for (const sample of this.samples) sum += (sample - 128) ** 2;
    return Math.min(1, Math.sqrt(sum / this.samples.length) / 42);
  }

  detectUtterance(level, now) {
    const sustained = this.sustained(level, now);
    // Unless the call listens, blips may play and talk may stop Hermes, so only a voice that keeps on starts an utterance.
    if ((this.heardSpeech || this.state === "listening") ? level >= SPEECH_LEVEL : sustained) {
      if (this.state !== "listening") this.setState("listening");
      this.heardSpeech = true;
      this.quietSince = null;
    } else if (this.heardSpeech) {
      this.quietSince ??= now;
    } else if (this.due && !this.mayBeTalking()) {
      this.proceed();
      return;
    }
    const length = now - this.recordingSince;
    if (!this.heardSpeech && length >= IDLE_MS && !this.mayBeTalking()) {
      this.discardRecording();
      this.record();
    } else if (this.heardSpeech && ((this.quietSince !== null && now - this.quietSince >= END_SILENCE_MS) || length >= MAX_UTTERANCE_MS)) {
      this.transcribe();
    }
  }

  // Whether a voice has been loud for a while, which a blip, a short noise or an echo of Hermes's voice isn't.
  sustained(level, now) {
    this.recentLevels.push({ at: now, loud: level >= BARGE_IN_LEVEL });
    while (now - this.recentLevels[0].at > BARGE_IN_MS) this.recentLevels.shift();
    const span = now - this.recentLevels[0].at;
    const loud = this.recentLevels.filter((sample) => sample.loud).length;
    return span >= BARGE_IN_MS * BARGE_IN_SHARE && loud >= this.recentLevels.length * BARGE_IN_SHARE;
  }

  // Whether a voice may be starting, before sustained can tell.
  mayBeTalking() {
    return this.recentLevels.some((sample) => sample.loud);
  }

  // A quiet, falling tone that tells the call still waits, alternating two pitches like Hermes's thinking sound.
  blip(now) {
    const start = this.audioContext.currentTime + 0.01;
    const duration = 0.16;
    const pitch = BLIP_PITCHES[this.blips++ % BLIP_PITCHES.length];
    const oscillator = this.audioContext.createOscillator();
    const envelope = this.audioContext.createGain();
    oscillator.frequency.setValueAtTime(pitch, start);
    oscillator.frequency.exponentialRampToValueAtTime(pitch * 0.72, start + duration);
    envelope.gain.setValueAtTime(0.0001, start);
    envelope.gain.exponentialRampToValueAtTime(0.08, start + 0.02);
    envelope.gain.exponentialRampToValueAtTime(0.0001, start + duration);
    oscillator.connect(envelope).connect(this.audioContext.destination);
    oscillator.start(start);
    oscillator.stop(start + duration + 0.02);
    // A slightly uneven pace sounds less mechanical.
    this.blipAt = now + 800 + Math.random() * 400;
  }

  record() {
    const mimeType = RECORDING_TYPES.find((type) => this.window.MediaRecorder.isTypeSupported(type));
    this.recorder = new this.window.MediaRecorder(this.stream, mimeType ? { mimeType } : undefined);
    this.recorder.start();
    this.recordingSince = Date.now();
    this.heardSpeech = false;
    this.quietSince = null;
    this.recentLevels = [];
  }

  discardRecording() {
    if (this.recorder && this.recorder.state !== "inactive") this.recorder.stop();
    this.recorder = null;
    this.heardSpeech = false;
  }

  async transcribe() {
    const recorder = this.recorder;
    const said = { transcript: null };
    this.said.push(said);
    this.recorder = null;
    // The call records on, so that what is said after a pause adds to what was said before it.
    this.setState("transcribing");
    try {
      // Without a timeslice, the recorder hands over the whole recording in one blob when it stops.
      const recording = await new Promise((resolve) => {
        recorder.ondataavailable = (event) => resolve(event.data);
        recorder.stop();
      });
      if (!this.said.includes(said)) return;
      const response = await this.request("/voice/transcribe", {
        method: "POST",
        body: recording,
        headers: { "Content-Type": recording.type, Accept: "application/json" }
      });
      said.transcript = (await response.json()).transcript?.trim() ?? "";
    } catch (error) {
      said.transcript = "";
      said.error = error.message;
    }
    if (this.said.includes(said)) this.proceed();
  }

  // Moves on once nothing is being said or transcribed: sends what was said, after stopping Hermes if it works, or
  // else waits for Hermes, speaks its reply or listens.
  proceed(error = null) {
    if (this.heardSpeech || this.said.some((said) => said.transcript === null)) return;
    // A voice that may be starting is waited for, and detectUtterance moves on if it doesn't keep on.
    this.due = this.recorder !== null && this.mayBeTalking();
    if (this.due) return;
    // Without a part that failed to be transcribed, a message would say something else, so none is sent.
    const failure = this.said.find((said) => said.error)?.error ?? null;
    const said = failure ? "" : this.said.map((said) => said.transcript).filter(Boolean).join(" ");
    error ??= failure;
    if (!said) {
      this.said = [];
      if (this.awaited !== null) this.setState("thinking", error);
      else if (this.reply) this.speak();
      else this.listen(error);
    } else if (isStopPhrase(said)) {
      this.end();
    } else if (this.awaited === null) {
      this.ask(said);
    } else {
      // The stopped run's end, which replyFinished hears, sends what was said. Each time more is said, Hermes is asked
      // again, in case a stop failed.
      this.stop();
      this.setState("stopping", error);
    }
  }

  async ask(transcript) {
    this.said = [];
    this.reply = null;
    this.awaited = transcript;
    this.setState("thinking");
    try {
      await this.send(VOICE_PREFIX + transcript);
    } catch (error) {
      if (this.awaited !== transcript) return;
      this.awaited = null;
      this.proceed(error.message);
    }
  }

  async speak() {
    const sentences = speechSentences(this.reply);
    this.reply = null;
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
        this.sentence = sentences[index];
        this.emit();
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
    this.state = state;
    this.error = error;
    this.level = 0;
    this.sentence = null;
    // The call hears you all along, except while Hermes speaks, when the recording would hear Hermes too.
    if (state === "speaking" || state === "off") this.discardRecording();
    else if (!this.muted && !this.recorder) this.record();
    this.emit();
  }

  emit() {
    this.onChange({ state: this.state, level: this.level, muted: this.muted, error: this.error, sentence: this.sentence });
  }
}

function microphoneError(error) {
  if (error?.name === "NotAllowedError") return "Microphone access was denied";
  if (error?.name === "NotFoundError") return "No microphone was found";
  return "The microphone is unavailable";
}
