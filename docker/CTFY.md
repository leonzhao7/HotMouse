# CTFy agent image

This separate image target implements CTFY-0001 schema v3. It runs one CTFy task rather than the HotMouse web workbench. The ordinary Docker build still produces the workbench image.

Build from the repository root:

    docker build --target ctfy-agent -f docker/Dockerfile -t hotmouse-ctfy:v1 .

The CTFy target declares no Docker volumes, so writes remain within the platform's disk budget. After building, verify that the image's volume metadata is `null`:

    docker image inspect hotmouse-ctfy:v1 --format '{{json .Config.Volumes}}'

Push the image to a registry reachable by the CTFy workers. Then run the platform's check and register the same image digest:

    docker tag hotmouse-ctfy:v1 ghcr.io/YOUR_ORG/hotmouse-ctfy:v1
    docker push ghcr.io/YOUR_ORG/hotmouse-ctfy:v1
    ctfy agent check ghcr.io/YOUR_ORG/hotmouse-ctfy:v1

CTFy mounts /ctfy/work as the current directory and supplies task.json, README.md, optional attachments, and run credentials. The container needs no exposed port, Compose service, workbench database, or personal model account. It selects Codex for OpenAI Responses or Claude Code for Anthropic, uses the supplied model gateway, and writes /ctfy/work/result.json. A dry run writes its result without calling a model. The image includes curl, Python, SSH and sshpass for challenge and AWD access.

For a local dry-run smoke test, create a writable directory named ctfy-smoke containing task.json with {"schema_version":3,"dry_run":true}, then run:

    docker run --rm -v "$PWD/ctfy-smoke:/ctfy/work" -w /ctfy/work -e CTFY_WORK_DIR=/ctfy/work -e CTFY_EVAL_DRY_RUN=1 hotmouse-ctfy:v1
    cat ctfy-smoke/result.json

The container user is UID 1000, so the mounted directory must be writable by that UID. The official ctfy agent check performs the platform's own manifest and dry-run validation. A scored run additionally requires a funded, reachable CTFy model gateway.

The image emits detailed terminal diagnostics on stderr by default (`HOTMOUSE_LOG_LEVEL=debug`, `HOTMOUSE_LOG_ENV=1`). Logs include the container runtime and environment, task schema and mode, input-file availability and permissions, selected driver/model/gateway, effective child environment, offline knowledge export, live CLI session/tool events, process stderr, 30-second progress heartbeats, token usage, result-file location, and exit reason. Credentials and authenticated URLs are redacted; task prompts are summarized by byte length. Logs do not replace or modify `result.json`.

Use `docker logs --tail=200 -f <container>` when running locally. Set `HOTMOUSE_LOG_LEVEL=info` for less output, or `HOTMOUSE_LOG_ENV=0` to omit environment snapshots. These settings can also be supplied by the external container runner. Rebuild and push the image after changing logging code, then update CTFy's registered image digest.
