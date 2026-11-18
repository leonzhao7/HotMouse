#!/usr/bin/env python3
"""Exercise the TSec image's default entrypoint and native Pi entirely locally."""

import argparse
import json
import os
import sqlite3
import subprocess
import tempfile
import threading
import time
import uuid
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--image", default="hotmouse-tsec:latest")
    parser.add_argument("--timeout", type=int, default=120)
    parser.add_argument("--target-probe-enabled", choices=("0", "1"), default="0")
    args = parser.parse_args()
    state = {"accepted": set(), "starts": 0, "closes": 0, "target_reads": 0, "model_calls": 0, "probe_calls": 0}
    mutex = threading.Lock()
    flags = ["flag{tsec_docker_first}", "flag{tsec_docker_second}"]

    class Handler(BaseHTTPRequestHandler):
        def log_message(self, *_args):
            pass

        def respond(self, value, status=200):
            self.send_response(status)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(json.dumps(value).encode())

        def do_GET(self):
            if self.path == "/target":
                with mutex:
                    state["target_reads"] += 1
                self.send_response(200)
                self.end_headers()
                self.wfile.write(("\n".join(flags) + "\n").encode())
            elif self.path == "/health":
                self.respond({"ready": True})
            elif self.path == "/openapi/v1/challenges":
                if self.headers.get("BENCHMARK_TOKEN") != "local-smoke-task":
                    self.respond({"code": "task_not_found"}, 404)
                    return
                with mutex:
                    count = len(state["accepted"])
                    active = state["starts"] > state["closes"]
                self.respond([{"unique_code": "docker_smoke", "description": "Retrieve both flag strings from " + target_url + ". Preserve the raw target output.", "difficulty": "easy", "flag_count": 2, "correct_flag_count": count, "is_completed": count == 2, "container_status": "available" if active else "stopped", "container_addr": [target_address] if active else []}])
            else:
                self.respond({"error": "unexpected endpoint"}, 404)

        def do_POST(self):
            body = json.loads(self.rfile.read(int(self.headers.get("Content-Length", 0))) or b"{}")
            if self.path.startswith("/openapi/v1/challenges/start?"):
                with mutex:
                    state["starts"] += 1
                self.respond({"unique_code": "docker_smoke", "container_addr": [target_address]})
            elif self.path.startswith("/openapi/v1/challenges/close?"):
                with mutex:
                    state["closes"] += 1
                self.respond({"unique_code": "docker_smoke", "closed": True})
            elif self.path == "/openapi/v1/challenges/submit":
                with mutex:
                    value = body.get("flag")
                    duplicate = value in state["accepted"]
                    correct = body.get("unique_code") == "docker_smoke" and value in flags
                    if correct:
                        state["accepted"].add(value)
                    count = len(state["accepted"])
                self.respond({"code": "duplicate", "message": "already accepted"} if duplicate else {"correct": correct, "correct_flag_count": count, "total_flag_count": 2}, 409 if duplicate else 200)
            elif self.path == "/v1/chat/completions":
                with mutex:
                    state["model_calls"] += 1
                messages = body.get("messages", [])
                prompt = json.dumps(messages)
                tool_call = None
                if "You are the HotMouse background target analyst" in prompt:
                    with mutex:
                        state["probe_calls"] += 1
                    answer = {"accepted": True, "data": {"required": False, "reason": "local smoke fixture", "targets": []}}
                elif "You are the HotMouse bounded step execution worker" in prompt:
                    if any(message.get("role") == "tool" for message in messages):
                        answer = {"accepted": True, "data": {"description": "Retrieved both flags from the delivered HTTP target", "result": {"type": "progress"}}}
                    else:
                        answer = None
                        tool_call = {"index": 0, "id": "call_smoke_target", "type": "function", "function": {"name": "bash", "arguments": json.dumps({"command": "curl --fail --silent " + target_url})}}
                else:
                    answer = {"accepted": True, "data": {"steps": [{"from": ["origin"], "goal_id": "g001", "description": "Retrieve the delivered target at " + target_url + " and checkpoint both flags"}]}}
                delta = {"role": "assistant", "tool_calls": [tool_call]} if tool_call else {"role": "assistant", "content": json.dumps(answer)}
                finish = "tool_calls" if tool_call else "stop"
                self.send_response(200)
                self.send_header("Content-Type", "text/event-stream")
                self.end_headers()
                try:
                    for content, reason in [(delta, None), ({}, finish)]:
                        chunk = {"id": "chatcmpl-local-smoke", "object": "chat.completion.chunk", "created": int(time.time()), "model": "smoke-model", "choices": [{"index": 0, "delta": content, "finish_reason": reason}]}
                        self.wfile.write(("data: " + json.dumps(chunk) + "\n\n").encode())
                    self.wfile.write(b"data: [DONE]\n\n")
                    self.wfile.flush()
                except (BrokenPipeError, ConnectionResetError):
                    pass  # The coordinator may cancel Pi once both flags pass.
            else:
                self.respond({"error": "unexpected endpoint"}, 404)

    target = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    gateway = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    target_address = "127.0.0.1:" + str(target.server_port)
    target_url = "http://" + target_address + "/target"
    gateway_url = "http://127.0.0.1:" + str(gateway.server_port)
    for server in (target, gateway):
        threading.Thread(target=server.serve_forever, daemon=True).start()
    name = "hotmouse-tsec-smoke-" + uuid.uuid4().hex[:10]
    try:
        with tempfile.TemporaryDirectory(prefix="hotmouse-tsec-smoke-") as directory:
            os.chmod(directory, 0o777)  # The image's node user owns its files.
            settings = {"BENCHMARK_BASE_URL": gateway_url, "BENCHMARK_TOKEN": "local-smoke-task", "HOTMOUSE_LLM_BASE_URL": gateway_url + "/v1", "HOTMOUSE_LLM_API_KEY": "local-smoke-key", "HOTMOUSE_LLM_MODEL": "smoke-model", "HOTMOUSE_LLM_API": "openai", "HOTMOUSE_WORKER_COUNT": "1", "HOTMOUSE_PROJECT_CONCURRENCY": "1", "PI_OPTCHAT_EXTENSION": "", "TSEC_NETWORK_CHECK_URL": gateway_url + "/health", "TSEC_TARGET_PROBE_ENABLED": args.target_probe_enabled, "HOTMOUSE_LOG_LEVEL": "debug", "HOTMOUSE_LOG_ENV": "0"}
            command = ["docker", "run", "--rm", "--name", name, "--network", "host", "--volume", directory + ":/data"]
            for key, value in settings.items():
                command.extend(["--env", key + "=" + value])
            command.append(args.image)
            completed = subprocess.run(command, stdin=subprocess.DEVNULL, capture_output=True, text=True, timeout=args.timeout)
            if completed.returncode:
                raise RuntimeError("container failed:\n" + completed.stderr[-8000:])
            report = json.loads(Path(directory, "tsec-result.json").read_text())
            if report["status"] != "completed" or any(item["status"] != "succeeded" for item in report["items"]):
                raise RuntimeError("unexpected competition result: " + json.dumps(report))
            with sqlite3.connect(Path(directory, "hotmouse.db")) as database:
                recorded = database.execute("SELECT COUNT(*) FROM flags WHERE status='accepted'").fetchone()[0]
            with mutex:
                if len(state["accepted"]) != 2 or recorded != 2 or state["starts"] != 1 or state["closes"] != 1 or not state["target_reads"]:
                    raise RuntimeError("incomplete container lifecycle: " + str(state))
                if bool(state["probe_calls"]) != (args.target_probe_enabled == "1"):
                    raise RuntimeError("target probe startup setting was not applied")
                print(json.dumps({"image": args.image, "status": report["status"], "accepted_flags": recorded, "starts": state["starts"], "closes": state["closes"], "target_reads": state["target_reads"], "native_model_calls": state["model_calls"], "target_probe_enabled": args.target_probe_enabled == "1", "target_probe_calls": state["probe_calls"]}))
    finally:
        subprocess.run(["docker", "rm", "--force", name], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=False)
        for server in (target, gateway):
            server.shutdown()
            server.server_close()


if __name__ == "__main__":
    main()
