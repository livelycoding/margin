# Margin guide

[Back to the overview](../README.md). Commands below run from the project directory.

A local listening room for articles. Paste text, or import a public blog/Substack URL, review the words, and turn them into a narrated reading. Choose from 28 English voices, follow the clickable transcript, and download the audio or subtitles. Speech runs on your computer with Kokoro; no TTS account or API key is required.

Margin is one service: a Go application serves the web UI/API, manages a SQLite library and durable queue, and runs CPU speech inference through ONNX Runtime. There is no Python runtime, Redis, separate database service, or GPU requirement.

## Run with Docker

Install Docker with the Compose plugin, then run these commands from the project directory:

```bash
docker compose up --build -d
docker compose logs -f margin
```

Open **http://localhost:8765** once startup finishes. The build compiles Margin and installs the native ONNX Runtime library. The first start downloads approximately 354 MB of Kokoro model and voice files, checks their pinned SHA256 hashes, and saves them in the persistent model cache. Later starts verify and reuse those files. Pronunciation assets and voice previews are included in the image.

Use Linux containers. Linux amd64 is the tested platform; an arm64 download path is provided but has not been validated end to end. Docker does not require Go, a C compiler, or Python on the host.

```bash
docker compose ps
docker compose logs -f margin
docker compose restart margin
docker compose down
```

`docker compose down` preserves the library and model-cache volumes. **`docker compose down -v` deletes those volumes, including your recordings and article text.** Keep using the same Compose project name/folder to reuse its volumes.

The container runs as a non-root user with a read-only filesystem apart from `/var/lib/margin` for the library, `/var/cache/margin` for the model cache, and `/tmp` for temporary files. Replacing or rebuilding the image leaves the volumes intact. A new Docker deployment starts with an empty library; it does not automatically import a native installation's `data/` directory.

### Configuration

Defaults work without a configuration file. To change them, copy the example and edit `.env`:

```bash
cp .env.example .env
```

| Setting | Default | Purpose |
| --- | --- | --- |
| `MARGIN_PORT` | `8765` | Published host port. The container's internal port stays 8765. |
| `MARGIN_BIND_ADDRESS` | `127.0.0.1` | Host interface to publish on. Keep loopback for local access or a host reverse proxy. |
| `MARGIN_PUBLIC_ORIGINS` | Empty | Additional exact browser origins, separated by spaces or commas, such as `https://your-host.your-tailnet.ts.net:8766`. Do not include a path or trailing slash. |
| `MARGIN_IMAGE` | `margin:local` | Image name used by Compose. |

Apply changes with `docker compose up -d`. To try Docker alongside an existing service on port 8765:

```bash
MARGIN_PORT=8767 docker compose up --build -d
```

Open **http://localhost:8767**. Save `MARGIN_PORT=8767` in `.env` if subsequent Compose commands should use that setting. The native service and Docker library remain independent.

No registry image is published as part of this source tree. Once a compatible image is available, the same Compose file supports pulling it:

```bash
MARGIN_IMAGE=ghcr.io/OWNER/margin:TAG docker compose up -d --no-build --pull always
```

`OWNER` and `TAG` are placeholders. Save the chosen `MARGIN_IMAGE` in `.env` for subsequent commands.

### Access over your tailnet

Keep `MARGIN_BIND_ADDRESS=127.0.0.1` and use Tailscale Serve on the host to proxy the published port. Add your actual HTTPS origin to `.env`:

```dotenv
MARGIN_PUBLIC_ORIGINS=https://your-host.your-tailnet.ts.net:8766
```

Recreate the container with `docker compose up -d`, then configure the host's Serve route:

```bash
sudo tailscale serve --bg --https=8766 http://127.0.0.1:8765
```

Change the proxy target if you changed `MARGIN_PORT`, and choose an unused Serve port if another application already uses 8766. Margin uses the host's Tailscale installation; it needs no Tailscale sidecar or credentials. Your Tailscale access rules control who can reach it.

All clients that can reach an allowed address share the library and can create or delete readings. There are no user accounts or per-user permissions. Keep access limited to trusted users; this is not a public internet service.

Open the HTTPS address to use **Also notify me through this browser**. Remote plain HTTP cannot request browser notifications. Notifications require a supporting browser, permission, and an open app tab; there are no closed-tab push notifications. The Docker container does not send host desktop notifications.

## Use it

1. Paste your article, or import a public URL. Review the editable text to remove footers, correct pronunciation spellings, or add paragraph breaks before narration.
2. Give the reading a title, choose a voice, and set its narration pace. The voice browser offers 28 English voices with searchable names and three descriptive tags, American/British and female/male filters, favorites, and playable samples. Previewing does not change the selected voice. Voice preferences and favorites are remembered in this browser. All samples use the same sentence at 0.95× pace; the initial default is Heart at 0.95×.
3. Generate. The library shows queued work, progress, and estimated time remaining. You can submit more readings while one runs, cancel work, or retry a failed or cancelled reading. The queue holds up to 20 active or waiting readings and processes them one at a time; retries join the end.
4. Open a finished reading. Click a sentence or timestamp to jump there. The current transcript sentence highlights during playback, and auto-follow can keep it visible. Search the transcript or resume where you left off. The compact playback speed slider has six notches from 0.75× to 2×; it supports arrow keys and remembers your choice.
5. Download the original 24 kHz mono WAV, the sentence-timed SRT, or the normalized narration text.

Select **Rename** beside a reading's title to change it without regenerating audio or changing its transcript. Titles can contain up to 200 characters. Save with the button or Enter; Cancel or Escape keeps the original. Renaming also works while a reading is generating.

For another version of a finished reading, select **Recast with another voice**, audition voices, choose a different narrator, and select **Create new version**. The new reading preserves the saved narration text, title, pace, and source link. It receives fresh audio and transcript timestamps, and the original remains available. Cancelling the voice picker creates nothing and leaves your next-reading voice preference unchanged.

## Background work and notifications

The server owns the queue. Closing or refreshing the browser does not cancel a job, but the server must remain running and the computer must stay awake. Work interrupted by a server restart is requeued and generated from the beginning. Finished recordings remain available. Explicitly cancelled readings stay cancelled.

The time estimate appears after two segments finish and updates after each subsequent segment. It uses the measured generation time and remaining text length, excludes initial model warmup, and adapts to recent machine load. Short readings may finish before an estimate is needed. Queued work receives an estimate once generation starts; final file preparation is shown separately. Estimates are approximate and change with text complexity and machine load.

The UI shows completion messages. Optional browser notifications require an open tab. Native Linux runs can also use `notify-send` while the tab is closed, subject to the graphical session and notification settings. For a foreground run, add `--no-desktop-notifications` to `bin/margin serve` or `./start.sh`. For a managed service, add that flag to its `ExecStart`, reload the user units, and restart the service. The container disables host desktop notifications.

## Speech and timing

The runtime consists of the Go executable, the **ONNX Runtime 1.31.1** native shared library, and the same Kokoro v1.0 model and 28 voice embeddings used by the accepted speech engine. The executable uses CGO to call ONNX Runtime and SQLite; it is not a fully static executable that can run without its native runtime files.

English text preparation, pronunciation, number handling, and inference orchestration run in Go. US and British pronunciation dictionaries come from Misaki through the pinned Sherpa model assets. Small Apache-licensed neural models handle unfamiliar English words. These assets are bundled under `assets/pronunciation/`; exact sources, hashes, licenses, and conversion provenance are documented there. The runtime does not use Phonemizer, eSpeak, `kokoro-onnx`, or a Python subprocess.

Sentences are synthesized separately, retaining punctuation and longer pauses between paragraphs. Long sentences split at clauses or word boundaries. Narration pace affects synthesis; playback speed only changes the player. Common Markdown presentation is removed while link labels are retained. Margin reads the supplied material; it does not summarize it or turn it into a conversation.

Transcript times come from the actual generated PCM sample counts. Cues are sentence/phrase level, with inter-sentence silence excluded from cue durations. No speech recognition service is needed. Ambiguous words, unusual names, acronyms, and complex formatting may benefit from editing. Only English voices are exposed; unsupported non-English scripts fail visibly rather than being silently omitted. If the bounded pronunciation fallback cannot handle an ASCII word, the frontend spells it out.

Voice previews are generated locally and cached in `static/voice-previews/`, with ready-made samples included in the source and container. Go parity tests compare representative normalization, pronunciation, and generated-audio fixtures against the accepted speech engine. Samples play without entering the job queue; starting another preview, selecting a voice, or closing the voice browser stops the current sample. Preview and episode audio do not play over each other.

After changing the speech engine, regenerate all 28 samples with the Go command:

```bash
./bin/margin previews --app-dir "$PWD" --force
```

The command also accepts `--model-dir` and `--ort-library` for alternative asset locations. Normal setup reuses the bundled previews.

## Storage and privacy

- `library.sqlite3` holds the article text, voice/settings, status, and transcript cues. It lives in `/var/lib/margin` in Docker, or `data/` in a native installation.
- Each reading has an `<episode-id>/` folder beside the database with its audio and text exports. Deleting a finished, failed, or cancelled reading removes its record and files. Browser resume positions and preferences stay in browser storage.
- The existing SQLite/JSON library and WAV/SRT files remain compatible with the Go service. Stop the previous server before switching executables or copying the entire library directory. A process lock prevents two servers from generating into the same library.
- Speech generation does not send article text to a TTS service. Initial setup and URL imports need internet access. After the runtime and model cache are installed, generating from pasted text works offline.
- URL import fetches a public HTTP(S) article directly, without browser cookies, account logins, or paywall bypassing. If extraction fails or returns only a preview, paste text you can access. Always review the extracted article before generation.
- Host/origin checks and mutation tokens protect browser requests; file paths are restricted and private-network import destinations are rejected. These controls do not provide user authentication. Use loopback or a trusted tailnet, not direct public exposure.

Back up the whole library directory while the server is stopped, including the SQLite database and any WAL files, plus each episode folder. Models can be downloaded again; saved article text and audio cannot be reconstructed from the model cache alone.

## Native Linux setup

Docker Compose is the primary portable setup. For a native Linux process, run:

```bash
./setup.sh
./start.sh
```

`setup.sh` builds with **Go 1.26+ and a C compiler** if available. Otherwise it uses Docker to build and export `bin/margin`. It then verifies the bundled pronunciation assets, downloads the pinned ONNX Runtime shared library into `runtime/onnxruntime/`, and verifies/downloads the Kokoro weights into `models/`. No Python environment or pip installation is needed. Building via Docker still produces a native Linux executable; keep its runtime and application assets beside it. Native execution needs compatible system C/C++ runtime libraries, including `libstdc++` and `libgomp`.

Open **http://127.0.0.1:8765**. Keep the foreground process running while generating or listening, and press Ctrl+C to stop. If a matching `margin.service` user unit is installed, `./start.sh` starts that service instead. `./start.sh --background` also uses a matching user service; it does not create a standalone daemon. `./stop.sh` stops that service, or directs you to stop the foreground process.

The supplied `margin.service` and `margin.desktop` are editable examples with `/absolute/path/to/margin` placeholders and a loopback URL. Follow the optional installation instructions below to customize installed copies. Only one server may use a library at a time, including when trying alternative ports. Opening `static/index.html` directly cannot generate audio.

The binary exposes these commands:

```bash
# Verify/install native runtime, pronunciation assets, and speech weights.
./bin/margin setup --app-dir "$PWD"

# Serve the existing UI and library in this folder.
./bin/margin serve --app-dir "$PWD" --port 8765

# Check the local engine's readiness through the API.
./bin/margin healthcheck --url http://127.0.0.1:8765

# Regenerate the voice browser's samples with the installed speech engine.
./bin/margin previews --app-dir "$PWD" --force

# Start an installed user service, wait for its URL, and open a browser.
./bin/margin launch --service margin.service --url http://127.0.0.1:8765

# Start and check the service without opening the browser.
./bin/margin launch --service margin.service --url http://127.0.0.1:8765 --check
```

`serve` supports `--data-dir`, `--model-dir`, `--ort-library`, `--host`, `--port`, `--public-origin` (repeatable), `--tailnet-host`, `--download-models`, and `--no-desktop-notifications`. `MARGIN_APP_DIR` can select the default application directory; `MARGIN_ORT_LIBRARY` selects an explicit native library, and `MARGIN_PUBLIC_ORIGINS` adds allowed origins. Run a command with `--help` for its flags.

For a direct tailnet HTTP listener, use this machine's actual Tailscale IPv4 address and DNS name:

```bash
./bin/margin serve --app-dir "$PWD" \
  --tailnet-host 100.x.y.z \
  --public-origin http://your-host.your-tailnet.ts.net:8765
```

This adds a listener alongside loopback; it requires a loopback `--host`. For an HTTPS reverse proxy, keep loopback and allow the proxy's exact HTTPS origin. Remote browser notifications require HTTPS even when Tailscale protects the HTTP network traffic.

## Development checks

With Go 1.26+ and a C compiler installed:

```bash
go test -race ./...
node --check static/app.js
```

Most tests use fakes and local fixtures. They cover text preparation, public-URL import boundaries, measured audio/SRT timing, queue/restart/cancellation behavior, lossless legacy JSON updates, concurrent mutations, HTTP controls and byte ranges, and verified model/runtime installation.

Run real pronunciation and audio parity checks after installing assets:

```bash
MARGIN_ORT_LIBRARY="$PWD/runtime/onnxruntime/lib/libonnxruntime.so.1.31.1" \
MARGIN_TEST_MODEL_DIR="$PWD/models" \
go test -race ./internal/speech
```

The native model tests skip when those environment variables are absent. Automated checks complement listening to representative text and testing browser playback after speech or UI changes.

`go.mod` and `go.sum` pin the Go dependency graph. Optional Python tools under `tools/` reproduce pronunciation asset conversions; they are developer tooling, require their own conversion environment, and are not used by setup, the server, or the container runtime. See `assets/pronunciation/README.md` and its provenance data before rebuilding model assets.

## License

Application code: **[MIT](../LICENSE)**. Models and dependencies retain their own licenses; see [third-party notices](../THIRD_PARTY_NOTICES.md).

## Optional native user service and desktop launcher

After native setup, you can run Margin as a systemd user service and add it to your desktop's application menu. The source files `margin.service` and `margin.desktop` are generic examples. Keep personal paths, hostnames, and tailnet addresses in the installed copies, not in these tracked templates.

From the project directory, copy each example only if an installed file does not already exist:

```bash
if [ ! -e ~/.config/systemd/user/margin.service ]; then
    install -Dm644 margin.service ~/.config/systemd/user/margin.service
fi
if [ ! -e ~/.local/share/applications/margin.desktop ]; then
    install -Dm644 margin.desktop ~/.local/share/applications/margin.desktop
fi
```

Edit `~/.config/systemd/user/margin.service` and `~/.local/share/applications/margin.desktop` in your editor. Replace every `/absolute/path/to/margin` with the absolute project directory containing `bin/margin`, `static/`, and `assets/`. Update `WorkingDirectory` and `ExecStart` in the service, and `Exec`, `TryExec`, `Path`, and `Icon` in the desktop entry. The examples use `http://127.0.0.1:8765` and bind only to loopback.

Installed configurations are independent copies. Updating the source checkout, building the application, or running `setup.sh` does not overwrite them. If you already have a working installation, keep its configured paths and addresses; review template changes before applying them manually.

Stop any foreground Margin process using the same library, then enable the edited service:

```bash
systemctl --user daemon-reload
systemctl --user enable --now margin.service
update-desktop-database ~/.local/share/applications
```

`update-desktop-database` is optional if your desktop does not provide it. On Ubuntu, press **Super** (the Windows key), search for **Margin**, and click its icon. The launcher starts the installed user service if needed, checks the configured URL, and opens your browser. Closing the browser leaves the service running. You can add the application to Favorites to keep it on the dock.

Manage the service and inspect its logs with:

```bash
systemctl --user status margin.service
systemctl --user restart margin.service
systemctl --user stop margin.service
journalctl --user -u margin.service -f
```

`./start.sh` and `./stop.sh` also manage the service when its `WorkingDirectory` matches the project directory. The enabled user service starts with your user session; running after logout or before login additionally requires user lingering, configured according to your system's policy.

Check the local launcher without opening a browser:

```bash
./bin/margin launch --url http://127.0.0.1:8765 --check
```

For tailnet HTTPS, follow [Access over your tailnet](#access-over-your-tailnet) above. In a native deployment, add your actual `--public-origin https://your-host.your-tailnet.ts.net:8766` to the **installed service's** `ExecStart` instead of configuring Compose's `.env`. Keep the loopback listener for the host's Tailscale Serve proxy. Set the **installed desktop entry's** `Exec` URL to that same HTTPS address, then reload the user units and restart Margin. If you use a direct HTTP tailnet listener instead, configure `--tailnet-host` and the allowed HTTP origin in the installed service as described under Native Linux setup.

Browser notifications require the HTTPS page, permission, and an open app tab; mobile browser support varies. The launcher starts the existing service and opens its URL without sudo or reading credentials. Tailscale Serve remains a separate host configuration.
