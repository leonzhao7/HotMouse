"""Finite CTF preparation and durable result handoff; never executes attachments."""
import argparse
import concurrent.futures
import hashlib
import importlib
import importlib.metadata
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tarfile
import tempfile
import urllib.request
import zipfile


def atomic_bytes(path, value):
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    fd, name = tempfile.mkstemp(dir=path.parent, prefix=".pending-")
    try:
        with os.fdopen(fd, "wb") as stream:
            stream.write(value)
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(name, path)
        if hasattr(os, "O_DIRECTORY"):
            fd = os.open(path.parent, os.O_DIRECTORY)
            try:
                os.fsync(fd)
            finally:
                os.close(fd)
    finally:
        if os.path.exists(name):
            os.unlink(name)


def atomic_json(path, value):
    atomic_bytes(path, json.dumps(value, ensure_ascii=False).encode("utf-8"))


def command(args):
    try:
        with tempfile.TemporaryFile() as output:
            result = subprocess.run(args, stdin=subprocess.DEVNULL, stdout=output,
                                    stderr=subprocess.STDOUT, timeout=4, check=False)
            size = output.tell()
            output.seek(0)
            return {"exit_code": result.returncode,
                    "output": output.read(12000).decode("utf-8", "replace"),
                    "truncated": size > 12000}
    except (OSError, subprocess.TimeoutExpired) as error:
        return {"error": str(error)}


def capabilities():
    packages = {"requests": "requests", "jwt": "PyJWT", "flask": "Flask",
                "itsdangerous": "itsdangerous",
                "sympy": "sympy", "pwn": "pwntools", "scapy.all": "scapy",
                "Crypto.Cipher.ChaCha20": "pycryptodome",
                "Cryptodome.Cipher.ChaCha20": "pycryptodomex",
                "cryptography": "cryptography", "gmpy2": "gmpy2",
                "capstone": "capstone", "elftools": "pyelftools", "dpkt": "dpkt",
                "fpylll": "fpylll", "z3": "z3-solver", "numpy": "numpy",
                "pefile": "pefile", "dnfile": "dnfile", "python_jwt": "python-jwt",
                "jwcrypto": "jwcrypto"}

    def check(item):
        namespace, package = item
        out = {"package": package}
        try:
            module = importlib.import_module(namespace)
            out["import_ok"] = True
            out["version"] = importlib.metadata.version(package)
            if namespace.endswith("Cipher.ChaCha20"):
                actual = module.new(key=bytes(32), nonce=bytes(12)).encrypt(bytes(16)).hex()
                out["zero_vector_ok"] = actual == "76b8e0ada0f13d90405d6ae55386bd28"
        except Exception as error:
            out["error"] = str(error)[:200]
        return namespace, out

    with concurrent.futures.ThreadPoolExecutor(max_workers=6) as pool:
        imports = dict(pool.map(check, packages.items()))
    tool_path = str(Path(sys.prefix) / "bin") + os.pathsep + os.environ.get("PATH", "")
    tools = {name: shutil.which(name, path=tool_path) for name in
             ("curl", "rg", "jq", "nmap", "nc", "socat", "ssh", "sshpass",
              "ffuf", "sqlmap", "git-dumper", "unzip", "7z", "unsquashfs",
              "file", "strings", "readelf", "objdump", "strace", "binwalk",
              "etcdctl", "nuclei", "upx", "tshark", "tcpdump", "ROPgadget",
              "gdb", "rizin", "radare2", "dotnet")}
    return {"python": sys.executable, "prefix": sys.prefix, "version": sys.version.split()[0],
            "tools": tools, "imports": imports,
            "namespace_note": "pycryptodome imports Crypto; pycryptodomex imports Cryptodome. "
                              "Install and run with the same explicit interpreter: <python> -m pip."}


def inspect(path):
    path = Path(path).absolute()
    result = {"path": str(path), "size_bytes": path.stat().st_size}
    with path.open("rb") as stream:
        digest = hashlib.sha256()
        while block := stream.read(1024 * 1024):
            digest.update(block)
        result["sha256"] = digest.hexdigest()
    jobs = {"file": ["file", "-b", "--", str(path)]}
    with path.open("rb") as stream:
        magic = stream.read(4)
    if magic == b"\x7fELF":
        jobs.update({"symbols": ["readelf", "-Ws", "--", str(path)],
                     "headers": ["readelf", "-h", "--", str(path)],
                     "strings": ["strings", "-a", "-n", "8", "--", str(path)]})
    with concurrent.futures.ThreadPoolExecutor(max_workers=4) as pool:
        futures = {name: pool.submit(command, args) for name, args in jobs.items()}
        result.update({name: future.result() for name, future in futures.items()})
    if zipfile.is_zipfile(path):
        with zipfile.ZipFile(path) as archive:
            names = archive.namelist()
            result["archive"] = {"type": "zip", "entries": names[:100], "total_entries": len(names)}
    elif path.suffix.lower() in (".tar", ".gz", ".tgz", ".bz2", ".xz"):
        try:
            with tarfile.open(path) as archive:
                names, count = [], 0
                for item in archive:
                    count += 1
                    if len(names) < 100:
                        names.append(item.name)
                result["archive"] = {"type": "tar", "entries": names, "total_entries": count}
        except tarfile.TarError:
            pass
    return result


def prepare(manifest, directory):
    directory = Path(directory).absolute()
    directory.mkdir(parents=True, exist_ok=True)

    def acquire(item):
        try:
            if item.get("path") and Path(item["path"]).is_file():
                path = Path(item["path"])
            else:
                url = item.get("url", "")
                if not url.startswith(("http://", "https://")):
                    raise ValueError("attachment needs an existing path or HTTP(S) URL")
                name = Path(item.get("name") or "attachment").name
                path = directory / (hashlib.sha256(url.encode()).hexdigest()[:16] + "-" + name)
                if not path.exists():
                    fd, temporary = tempfile.mkstemp(dir=directory, prefix=".download-")
                    try:
                        with os.fdopen(fd, "wb") as output, urllib.request.urlopen(url, timeout=10) as response:
                            total = 0
                            while block := response.read(65536):
                                total += len(block)
                                if total > 64 * 1024 * 1024:
                                    raise ValueError("attachment exceeds preparation limit (64 MiB)")
                                output.write(block)
                            output.flush()
                            os.fsync(output.fileno())
                        os.replace(temporary, path)
                    finally:
                        if os.path.exists(temporary):
                            os.unlink(temporary)
            return {"source": item, **inspect(path)}
        except Exception as error:
            return {"source": item, "error": str(error)[:300]}

    with concurrent.futures.ThreadPoolExecutor(max_workers=4) as pool:
        files = list(pool.map(acquire, manifest.get("attachments", [])[:20]))
    return {"attachments": files, "attachment_list_limited": len(manifest.get("attachments", [])) > 20,
            "network_interfaces": command(["ip", "-brief", "address"]),
            "note": "Interface presence is not proof of VPN reachability; reuse target_analysis/health and platform metadata."}


def checkpoint(args):
    source = Path(args.source).absolute()
    with source.open("rb") as stream:
        raw = stream.read(16 * 1024 * 1024 + 1)
    if len(raw) > 16 * 1024 * 1024:
        raise ValueError("save a bounded raw output (up to 16 MiB) as the source")
    if not args.value or args.value.encode("utf-8") not in raw:
        raise ValueError("exact candidate is absent from the source; preserve the solver/target output first")
    if args.pattern and re.fullmatch(args.pattern, args.value) is None:
        raise ValueError("candidate does not match the supplied challenge format")
    directory = Path(os.environ["HOTMOUSE_RESULT_INBOX"])
    directory.mkdir(parents=True, exist_ok=True)
    digest = hashlib.sha256(raw).hexdigest()
    snapshot = directory / ("source-" + digest + ".bin")
    if not snapshot.exists() or snapshot.read_bytes() != raw:
        atomic_bytes(snapshot, raw)
    value = {"value": args.value, "challenge_id": args.challenge_id,
             "source_path": str(snapshot), "original_source_path": str(source),
             "source_sha256": digest, "format_note": args.format_note,
             "format_pattern": args.pattern}
    identity = hashlib.sha256((args.challenge_id + "\0" + args.value).encode()).hexdigest()
    path = directory / (identity + ".candidate.json")
    atomic_json(path, value)
    return {"checkpoint": str(path), "status": "saved; HotMouse verifies provenance and captures it; "
            "the competition coordinator submits through the configured adapter"}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="action", required=True)
    cap = sub.add_parser("capabilities")
    cap.add_argument("--output")
    prep = sub.add_parser("prepare")
    prep.add_argument("--manifest", required=True)
    prep.add_argument("--directory", required=True)
    prep.add_argument("--output", required=True)
    scan = sub.add_parser("inspect")
    scan.add_argument("paths", nargs="+")
    scan.add_argument("--output")
    save = sub.add_parser("checkpoint")
    save.add_argument("--source", required=True)
    save.add_argument("--value", required=True)
    save.add_argument("--challenge-id", default="")
    save.add_argument("--format-note", required=True)
    save.add_argument("--pattern", default="")
    args = parser.parse_args()
    if args.action == "capabilities":
        result = capabilities()
    elif args.action == "prepare":
        result = prepare(json.loads(Path(args.manifest).read_text()), args.directory)
    elif args.action == "inspect":
        with concurrent.futures.ThreadPoolExecutor(max_workers=4) as pool:
            result = {"attachments": list(pool.map(inspect, args.paths))}
    else:
        result = checkpoint(args)
    if getattr(args, "output", None):
        atomic_json(args.output, result)
    print(json.dumps(result, ensure_ascii=False))


if __name__ == "__main__":
    main()
