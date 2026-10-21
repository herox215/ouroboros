# Internal speech backend

`New(root)` uses the profile's speech directory, independently of `config.Config`.
It performs no downloads. Settings default to disabled, local model
`large-v3-turbo-q5_0`, language `auto`, and remote base URL
`https://api.openai.com/v1`. `small` and `medium` are also supported locally.
Remote configurations may name any model supported by their endpoint.

- `Save` validates and atomically stores `config.json` (0600). An empty API key
  preserves the saved key; this API intentionally has no key-clearing operation.
- `Snapshot` always redacts the key and exposes `HasKey`. Status values are
  `disabled`, `remote`, `downloading`, `error`, `missing_ffmpeg`,
  `missing_runtime`, `missing_model`, and `ready`. `remote` means configured,
  **not** that credentials or connectivity were verified. Runtime readiness
  requires successful whisper-cli and whisper-server help and ffmpeg version
  probes; missing either executable requires explicit setup. Model readiness
  checks the installed file's size, not a full hash on every snapshot.
- `StartDownload` is the only download trigger and requires `Provider == "local"`.
  Disabled and remote settings are rejected, even with a catalog model ID. It
  captures the currently selected local model. Changes to settings do not retarget
  a running download. Prerequisites are ffmpeg and either both whisper-cli and
  whisper-server on PATH (or already managed), or cmake plus C/C++ compilers
  (`cc`, `c++`). It builds and installs both executables from CPU whisper.cpp v1.8.2
  at pinned commit `4979e04f5dcaccb36057e059bbaed8a2f5288315` when needed.
  Source archives are commit-pinned and size-bounded, but have no pinned archive
  checksum. Models have pinned sizes and SHA-256 checksums from upstream LFS
  metadata. Failed downloads never replace installed files.
- Progress phases are `starting`, `runtime_download`, `runtime_build`, `model`,
  `complete`, `cancelled`, or `error`. Byte counts apply to the current/last
  transfer, not build work. Unknown lengths use total zero. Cancellation is
  asynchronous (100 ms polling). Advisory filesystem locks and atomic state files
  coordinate separate processes on a local Linux filesystem; stale active state
  is reported as interrupted after the owning process exits. A hard process kill
  can leave private build/part files; the next relevant transfer replaces its
  leftover part file. Normal failure/cancellation removes temporary work.
- `Transcribe` never downloads. Remote requests POST multipart audio/model/language
  to `<base>/audio/transcriptions` (an already complete URL is also accepted),
  send optional bearer credentials, refuse redirects, and expect JSON `text`.
  HTTP is supported for local endpoints; use HTTPS for credentials over a network.
  Server bodies and command output are not included in errors.
- Local audio, including Matrix Ogg/Opus, is converted by ffmpeg to mono 16 kHz
  PCM, then POSTed as multipart `file`, `language`, `response_format=json` to
  whisper-server's `/inference` endpoint. One process/model is started lazily and
  reused per Manager; calls, conversion and process replacement are serialized
  with cancellable waiting. A model change replaces/reaps the process on the next
  local transcription. Settings changes alone do not unload it. Startup polls
  `/health` for HTTP 200 and JSON `status=ok`, with a 2-minute ready timeout inside
  the request's 10-minute deadline. Process exit is detected; later calls restart.
  Cancellation during inference kills/reaps the process because upstream does not
  stop inference on HTTP disconnect. A later call reloads it. `Close` cancels local
  work, kills/reaps the server and permanently rejects further local work; it is
  idempotent. Callers owning transcription Managers must close them. Matrix owns
  one per connection/run and closes it on run-context cancellation or return.
  Downloads/settings and remote requests have independent lifetimes.
- The managed server binds only `127.0.0.1` on an ephemeral port. Upstream v1.8.2
  has no API-key auth: a fresh random 256-bit `--request-path` prefix protects
  **all API routes**, including `/load`, against blind localhost/browser requests.
  This is not protection against same-user processes that can inspect argv. The
  server's static public directory is private and empty; local HTTP bypasses
  environment proxies and refuses redirects. Request bodies contain only the
  converted WAV and fixed fields (never caller-provided paths). No error includes
  the secret URL, server output or response bodies. Upstream cannot inherit a
  listener: releasing the allocated port before spawn has a small bind race;
  failed startup is bounded, not silently accepted.
- Temporary working directories are private and removed after each operation
  (the empty public directory lives until server shutdown). Inputs are capped at
  25 MiB and local audio at 600 seconds; overlong audio is rejected before
  inference, never silently
  transcribed as a partial request. Conversion decodes at most 601 seconds with a
  20 MB output cap; the WAV data chunk (excluding metadata) must contain at most
  600 seconds of mono 16 kHz s16 PCM. Response text is capped at 1 MiB. Local
  operations have a 10-minute deadline, conversion and remote calls 2 minutes,
  preparation 45 minutes, individual downloads 30 minutes. Commands' whole
  process groups are killed on cancellation. Download HTTP headers have a
  30-second deadline.

Tests use httptest and fake commands, including an actual separate-process
lock/cancellation check and a tiny synthetic source archive/model. A duration
regression test uses real ffmpeg when available to generate and convert synthetic
silence: exactly 600 seconds succeeds; one sample over, 601 seconds, and 900
seconds are rejected without starting whisper-server. A subprocess HTTP fixture
checks persistent reuse, concurrent callers, model switching/reaping, cancellation
while queued, starting and inferring, Close, and missing-server setup detection.
Recognition itself is stubbed. No production model or runtime is downloaded by tests.

## Upstream verification

The [GitHub v1.8.2 tag API](https://api.github.com/repos/ggml-org/whisper.cpp/git/ref/tags/v1.8.2)
identifies commit `4979e04f5dcaccb36057e059bbaed8a2f5288315`. Its
[codeload archive](https://codeload.github.com/ggml-org/whisper.cpp/tar.gz/4979e04f5dcaccb36057e059bbaed8a2f5288315)
was successfully downloaded with a 64 MiB cap, configured with the exact
`buildRuntime` CMake flags, and built with target `whisper-cli -j 2` on Linux/amd64.
The resulting executable's `--help` exited successfully and listed the expected
model, input, language, text-output and timestamp options. This was a separate
agent-workspace check, not a production runtime installation; no model was fetched
and actual recognition remains unverified. That earlier check covered only CLI.
The persistent-server change was checked against pinned
[`examples/server/server.cpp`](https://github.com/ggml-org/whisper.cpp/blob/4979e04f5dcaccb36057e059bbaed8a2f5288315/examples/server/server.cpp):
`--host`, `--port`, `--public`, `--request-path`, `-m`, prefixed `/health`, and
multipart `/inference` with JSON `text` conform to that interface. No new upstream
build or real-server/model inference was performed by the automated tests.
A separate agent-workspace check subsequently built `whisper-server` from that
same cached source and installed it alongside the existing managed CLI. With the
existing Large v3 Turbo Q5_0 model, two consecutive HTTP inference requests using
upstream's `samples/jfk.wav` both returned the expected English transcript. The
process PID stayed identical and its log contained one model-load occurrence.
Readiness took about 0.2 seconds; inference took 22.3 and 34.9 seconds respectively.
This verifies real server reuse, not an inference-speed improvement or an
end-to-end Matrix test after rebuilding Ouro.

All three catalog sizes and SHA-256 values were checked against the upstream
[Hugging Face tree API / LFS metadata](https://huggingface.co/api/models/ggerganov/whisper.cpp/tree/main?recursive=false&limit=100),
without downloading model contents:

| Model | Bytes | SHA-256 |
| --- | ---: | --- |
| large-v3-turbo-q5_0 | 574041195 | `394221709cd5ad1f40c46e6031ca61bce88931e6e088c188294c6d5a55ffa7e2` |
| small | 487601967 | `1be3a9b2063867b937e64e2ec7483364a79917e157fa98c5d94b5c1fffea987b` |
| medium | 1533763059 | `6c14d5adee5f86394037b4e4e8b59f1673b6cee10e3cf0b11bbdbee79c156208` |

Users initiate production model/runtime setup themselves after selecting local
speech; creating the manager or transcribing never initiates a download.
