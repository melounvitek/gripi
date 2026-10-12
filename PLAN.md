# Voice conversations with Hermes

A hands-free, turn-based voice conversation for Hermes sessions, using Hermes's own local speech stack (Whisper for speech-to-text, Piper for text-to-speech) through its dashboard. Pi sessions don't get it.

## Decisions

- **Speech goes through Hermes's dashboard.** Gripi's server forwards audio to the dashboard's `POST /api/audio/transcribe` and text to `POST /api/audio/speak`, the endpoints Hermes Desktop uses, so Hermes's `stt:` and `tts:` settings apply. The browser never talks to the dashboard.
- **Gripi logs in to the dashboard** with its username and password (`POST /auth/password-login`, provider `basic`), keeps the session cookies in a cookie jar, and logs in again once when a call answers 401. The dashboard refreshes the cookies itself.
- **Settings**, all gateway-only: `GRIPI_HERMES_DASHBOARD_URL` (e.g. `http://127.0.0.1:8787`), `GRIPI_HERMES_DASHBOARD_USERNAME`, `GRIPI_HERMES_DASHBOARD_PASSWORD`. Voice is on when `GRIPI_HERMES` and the URL are set; the URL needs the username and password.
- **Spoken messages carry Hermes CLI's voice prefix**, so Hermes answers briefly: `[Voice input — respond concisely and conversationally, 2-3 sentences max. No code blocks or markdown.] ` followed by the transcript. Hermes saves it as part of the message. Gripi hides the prefix and shows a quiet `voice` mark instead, in both server-rendered history and live rendering. Gripi stores nothing extra.
- **UI direction A:** a waveform button after ＋ in the composer, for Hermes sessions with voice on. During a call the textarea row becomes one mono line inside the same editor frame — a state dot, the state, a level meter, `mute` and `end` — and the editor rules turn `--accent`. Mockup: `/tmp/gripi-voice-mockups/A.html`.

## The loop (browser)

Numbers follow Hermes Desktop's voice conversation.

- **listening:** an `AnalyserNode` (fftSize 256) is sampled on a timer (not `requestAnimationFrame`, which stops in hidden tabs). Level = `min(1, rms / 42)` over byte samples centred at 128. Speech is a sample at or above 0.075. After speech, 1250 ms below it ends the utterance. No speech for 12 s restarts the recording; 60 s is the cap. `MediaRecorder` uses the first supported of `audio/webm;codecs=opus`, `audio/webm`, `audio/mp4`, `audio/ogg;codecs=opus`, `audio/ogg`, `audio/wav`.
- **transcribing:** `POST /voice/transcribe` with the recording. An empty transcript means silence: listen again. A stop phrase ends the call. Otherwise the prefixed text is sent as a normal prompt.
- **thinking:** wait for Hermes's turn to settle, then speak its final reply. A turn without a reply goes back to listening.
- **speaking:** the reply is cleaned for speech (Hermes Desktop's `sanitizeTextForSpeech` rules) and split into sentences (fragments under 24 characters join the next). Each sentence comes from `POST /voice/speak` and plays through Web Audio; the next one is fetched while the current one plays. Talking over the reply (level at or above 0.14 for most of 300 ms) stops playback and listens.
- **Stop phrases** (whole utterance, after NFKC, lowercase, punctuation to spaces, optional `hey hermes`/`hermes`/`ok`/`okay`/`hey` prefix): `stop`, `stop listening`, `stop it`, `stop please`, `please stop`, `stop stop`, `that is all`, `that's all`, `never mind`, `nevermind`, `end conversation`, `end the conversation`, `goodbye`, `good bye`, `bye`, `cancel`, and in Czech `konec`, `konec hovoru`, `ukonči hovor`, `to je vše`, `to je všechno`, `nashledanou`, `na shledanou`.
- **mute** disables the microphone track. **end** stops everything and releases the microphone.
- **Screen and visibility:** the call holds a screen wake lock and takes it back when the page is visible again. A hidden page keeps the call going where the browser lets it (a desktop window behind others). When the microphone ends (an iPhone may end it while locked), the call ends and asks to start it again.
- **Meter:** the microphone's level while listening, the reply's level while speaking.

## Server

- `POST /voice/transcribe`: the body is the recording, `Content-Type` its MIME type. Answers `{"transcript": "…"}`.
- `POST /voice/speak`: form field `text`. Answers the audio bytes with their MIME type.
- Both exist only when voice is on, sit behind Gripi's usual access checks, and answer 502 with `{"error": …}` when the dashboard fails.

## Rounds

1. Server: settings, the dashboard client (login, re-login on 401), the two routes, docs. Go tests against a fake dashboard. Manual check against the real dashboard.
2. Browser loop: `voice_conversation.js` with the state machine, VAD, stop phrases, speech cleaning and sentence splitting, barge-in, visibility and wake lock. Node unit tests with injected fakes.
3. UI: the button and call line (direction A), `app.js` wiring (send, final reply on `agent_settled`, session switch, polling while a call runs), the `voice` mark in both renderers, CSS (copied into `demo/index.html`), Electron microphone permission (audio only, gateway origin only; macOS usage description). Playwright tests with a fake microphone file and a fake dashboard, including a touch test.
4. Manual test on the iPhone over Tailscale.

## Later (TODO.md)

- Interrupting Hermes by voice while it works (Stop works).
- Speaking replies while they stream.
- Hermes Desktop's echo guard and thinking sound.
- A full-screen call view (direction C) as an option.
- Screen-off calls on iPhone need a native app.
