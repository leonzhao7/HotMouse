# CTFy agent image

This separate image target implements CTFY-0001 schema v3. It runs one CTFy task rather than the HotMouse web workbench. The ordinary Docker build still produces the workbench image.

Build from the repository root:

    docker build --target ctfy-agent -f docker/Dockerfile -t hotmouse-ctfy:v1 .

Push the image to a registry reachable by the CTFy workers. Then run the platform's check and register the same image digest:

    docker tag hotmouse-ctfy:v1 ghcr.io/YOUR_ORG/hotmouse-ctfy:v1
    docker push ghcr.io/YOUR_ORG/hotmouse-ctfy:v1
    ctfy agent check ghcr.io/YOUR_ORG/hotmouse-ctfy:v1

CTFy mounts /ctfy/work as the current directory and supplies task.json, README.md, optional attachments, and run credentials. The container needs no exposed port, Compose service, workbench database, or personal model account. It selects Codex for OpenAI Responses or Claude Code for Anthropic, uses the supplied model gateway, and writes /ctfy/work/result.json. A dry run writes its result without calling a model. The image includes curl, Python, SSH and sshpass for challenge and AWD access.

For a local dry-run smoke test, create a writable directory named ctfy-smoke containing task.json with {"schema_version":3,"dry_run":true}, then run:

    docker run --rm -v "$PWD/ctfy-smoke:/ctfy/work" -w /ctfy/work -e CTFY_WORK_DIR=/ctfy/work -e CTFY_EVAL_DRY_RUN=1 hotmouse-ctfy:v1
    cat ctfy-smoke/result.json

The container user is UID 1000, so the mounted directory must be writable by that UID. The official ctfy agent check performs the platform's own manifest and dry-run validation. A scored run additionally requires a funded, reachable CTFy model gateway.
