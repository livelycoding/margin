# Container validation and releases

Margin's container workflow tests and builds Linux amd64 images on pushes to
`main`, pull requests, and manual runs. Publishing a GitHub release uploads the
tested image to GitHub Container Registry (GHCR) and attaches a Compose download.
Validation builds can be tested before publication.

## Try a validation build on another machine

1. Open [Container image runs](https://github.com/livelycoding/margin/actions/workflows/publish-image.yml)
   and choose a successful run for the commit you want to test.
2. Under **Artifacts**, download **margin-validation-linux-amd64**. GitHub requires
   you to sign in to download workflow artifacts; they are retained for seven days.
3. Extract the entire ZIP into a dedicated folder, including its `.env` file.
   Install Docker Engine with Compose, or use Docker Desktop in Linux containers
   mode on an Intel/AMD Windows machine.
4. In that folder, run these commands in PowerShell, Command Prompt, or a Linux shell:

   ```text
   docker load --input margin-image.tar.gz
   docker compose up -d --pull never
   docker compose logs -f
   ```

Open **http://localhost:8765** after the initial model download finishes. Press
Ctrl+C to stop following logs; the service keeps running. If that port is already
in use, add `MARGIN_PORT=8767` to the extracted `.env` before starting, then open
http://localhost:8767 instead. Keep `MARGIN_IMAGE=margin:validation` in that file.

Generate a short passage, play it, seek using the transcript, and try a voice
preview. Run `docker compose restart` and confirm your recording still plays.
The `COMMIT` file identifies the source revision; `SHA256SUMS` contains checksums
for the bundle files.

Stop the instance with `docker compose down`. Keep using the same folder to reuse
its persistent volumes. **Adding `--volumes` deletes the test library and model
cache.** Loading a later validation image and running `docker compose up -d
--pull never` updates the container while retaining those volumes.

The bundle contains the tested application image, not any recordings or model
cache from the build machine. About 354 MB of speech weights download on first
startup and are then reused from the model-cache volume.

## What the workflow checks

- Go tests with the race detector, plus JavaScript syntax.
- A Linux amd64 Docker build with source revision, version, and license metadata.
- First startup with empty, isolated library and model-cache volumes using the
  same Compose settings as an installation.
- Real speech generation, non-silent WAV output, transcript timing and downloads,
  and HTTP byte ranges used for seeking.
- Container recreation with the same volumes; saved metadata, audio, and
  transcripts must remain identical.

Run the container check locally with Docker, Compose, Bash, and Python 3:

```bash
docker build --platform linux/amd64 -t margin:validation .
bash scripts/smoke-container.sh margin:validation
```

The check uses a temporary Compose project and loopback port, then removes its
own containers and volumes. Python is only used by the development check; it is
not required by Margin or included in its runtime image.

## Publish a version after validation

Publish a GitHub release using a tag such as `v0.1.0`. The workflow repeats the
checks and publishes the **same tested image** as
`ghcr.io/livelycoding/margin:0.1.0`. Stable releases also update `:latest`.
Prerelease versions such as `v0.1.0-rc.1`, or releases marked as prereleases in
GitHub, do not update `:latest`. Tags must be semantic versions without `+build`
metadata. A manual workflow run creates a validation bundle without publishing.

After the image upload succeeds, the workflow attaches `compose.yaml` to the
release. Its default image is pinned to that release's version, including for
prereleases. The README's download link selects the latest stable release.

The publishing job uses GitHub's temporary `GITHUB_TOKEN` with package write
permission for the image and repository contents write permission for the release
attachment; no personal token or Docker Hub account is needed. After the first
publication, set the package's visibility to **Public** in its GitHub package
settings and verify an anonymous pull before documenting it as available.

For registry installations, download the release's `compose.yaml` into an empty
folder, then run `docker compose up -d`. To update, replace that file with the new
release's `compose.yaml` in the same folder, then run `docker compose pull` and
`docker compose up -d`. The library and cache volumes are preserved.

If converting a validation folder, replace its `compose.yaml` with the release
download and remove the `MARGIN_IMAGE=margin:validation` override from `.env`,
then run `docker compose pull` and `docker compose up -d` in that same folder.
