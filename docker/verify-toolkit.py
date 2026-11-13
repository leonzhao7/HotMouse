"""Verify Python dependencies and commands required by the bundled skills."""

import gzip
import importlib
import importlib.metadata
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile


def run_tool(argv, **kwargs):
    result = subprocess.run(
        argv, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
        text=True, timeout=30, **kwargs,
    )
    if result.returncode:
        raise RuntimeError(f"{argv!r} exited {result.returncode}:\n{result.stdout}")
    return result.stdout


def verify_skill_tools():
    manifest = json.loads(Path(__file__).with_name("skill-tools.json").read_text())
    skills = manifest["skills"]
    root = Path(os.environ["HOTMOUSE_SKILLS_DIR"])
    # The two role skills are maintained outside backend/skills.
    sources = {path.parent.name for path in root.glob("*/SKILL.md")} - {
        "hotmouse-ctf", "hotmouse-dispatcher",
    }
    if sources != set(skills):
        raise RuntimeError(
            f"Skill inventory mismatch: unlisted={sorted(sources - set(skills))}, "
            f"missing={sorted(set(skills) - sources)}"
        )

    commands = set(manifest["common_commands"])
    for required in skills.values():
        commands.update(required["commands"])
        for module in required.get("python_modules", []):
            importlib.import_module(module)
    missing = sorted(command for command in commands if not shutil.which(command))
    if missing:
        raise RuntimeError(f"Commands required by backend/skills are missing: {missing}")

    for interpreter in ("python", "python3"):
        prefix = run_tool([interpreter, "-c", "import sys; print(sys.prefix)"]).strip()
        if prefix != sys.prefix:
            raise RuntimeError(f"{interpreter} uses {prefix}, expected {sys.prefix}")

    # Exercise launchers as well as PATH lookup, including system-Python binwalk.
    for argv in (
        ["r2", "-v"], ["radare2", "-v"], ["binwalk", "--help"],
        ["ltrace", "--version"], ["strace", "--version"],
        ["nmap", "--version"], ["ffuf", "-V"], ["sqlmap", "--version"],
        ["git-dumper", "--help"], ["7z", "i"],
        ["checksec", "--file=/bin/true"],
        ["ROPgadget", "--binary", "/bin/true", "--depth", "2"],
        ["gdb", "--batch", "--nx", "-ex", "set disable-randomization off",
         "-ex", "starti", "-ex", "continue", "--args", "/bin/true"],
    ):
        run_tool(argv)

    if "API version: 3." not in run_tool(["etcdctl", "version"]):
        raise RuntimeError("etcdctl must default to the v3 API")
    if "--prefix" not in run_tool(["etcdctl", "get", "--help"]):
        raise RuntimeError("etcdctl get does not support --prefix")

    from flask import Flask
    from flask.sessions import SecureCookieSessionInterface

    app = Flask("toolkit-verification")
    app.secret_key = "hotmouse-build-test"
    serializer = SecureCookieSessionInterface().get_signing_serializer(app)
    session = {"role": "test", "user_id": 1}
    if serializer.loads(serializer.dumps(session)) != session:
        raise RuntimeError("Flask signed session round trip failed")

    with tempfile.TemporaryDirectory(prefix="hotmouse-skill-tools-") as directory:
        work = Path(directory)
        run_tool(["ltrace", "-o", str(work / "ltrace.txt"), "/bin/true"])
        run_tool(["strace", "-o", str(work / "strace.txt"), "/bin/true"])

        patch = work / "patch.bin"
        patch.write_bytes(b"\x75\x01\xc3")
        run_tool(["r2", "-n", "-w", "-q", "-c", "s 0; wx 9090; q", str(patch)])
        if patch.read_bytes() != b"\x90\x90\xc3":
            raise RuntimeError("r2 binary patch failed")

        payload = b"HotMouse binwalk extraction verification\n" * 8
        squash_input = work / "squash-input"
        squash_input.mkdir()
        (squash_input / "payload.txt").write_bytes(payload)
        squashfs = work / "firmware.squashfs"
        run_tool(["mksquashfs", str(squash_input), str(squashfs),
                  "-noappend", "-processors", "1"])
        squash_output = work / "squash-output"
        run_tool(["unsquashfs", "-processors", "1", "-d", str(squash_output), str(squashfs)])
        if (squash_output / "payload.txt").read_bytes() != payload:
            raise RuntimeError("unsquashfs extraction failed")

        firmware = work / "firmware.bin"
        firmware.write_bytes(gzip.compress(payload, mtime=0))
        run_tool(["binwalk", "-e", str(firmware)], cwd=work)
        extracted = work / "_firmware.bin.extracted"
        if not any(path.is_file() and path.read_bytes() == payload
                   for path in extracted.rglob("*")):
            raise RuntimeError("binwalk -e did not extract the gzip fixture")

    print(f"Skills verified: {len(skills)} skills, {len(commands)} commands, "
          "Flask sessions, ELF tools, tracing, r2 patches, SquashFS/binwalk extraction, etcd v3")


def main():
    if sys.version_info < (3, 13):
        raise RuntimeError("The toolkit requires Python 3.13 or newer")

    requirements = Path(__file__).with_name("toolkit-requirements.txt")
    count = 0
    for line in requirements.read_text().splitlines():
        line = line.strip()
        if not line or line.startswith("#"):
            continue
        package, expected = line.split("==", 1)
        actual = importlib.metadata.version(package)
        if actual != expected:
            raise RuntimeError(f"{package}: expected {expected}, installed {actual}")
        count += 1

    # Imports also check that copied extensions can load their runtime libraries.
    os.environ.setdefault("TERM", "xterm")
    for module in (
        "androguard.core.apk", "capstone", "cryptography", "dataset", "dpkt",
        "flask", "gmpy2", "IPython", "jwt", "jwcrypto.jwk", "lxml.etree",
        "nacl.public", "networkx", "paramiko", "pwn", "pyaes",
        "python_jwt", "requests", "ropgadget", "scapy.all", "sympy",
        "unicorn", "yaml", "zstandard",
    ):
        importlib.import_module(module)

    from Crypto.Cipher import AES
    from fpylll import IntegerMatrix, LLL
    import gmpy2
    from capstone import Cs, CS_ARCH_X86, CS_MODE_64
    from scapy.all import IP, UDP

    matrix = IntegerMatrix.from_matrix([[1, 1], [1, 0]])
    LLL.reduction(matrix)
    assert abs(matrix[0, 0] * matrix[1, 1] - matrix[0, 1] * matrix[1, 0]) == 1
    assert gmpy2.invert(3, 11) == 4
    key, block = b"k" * 16, b"x" * 16
    encrypted = AES.new(key, AES.MODE_ECB).encrypt(block)
    assert AES.new(key, AES.MODE_ECB).decrypt(encrypted) == block
    assert next(Cs(CS_ARCH_X86, CS_MODE_64).disasm(b"\x90", 0)).mnemonic == "nop"
    assert IP(bytes(IP(dst="127.0.0.1") / UDP(dport=9981)))[UDP].dport == 9981
    print(f"Toolkit verified: {count} pinned packages, Python {sys.version.split()[0]}")
    verify_skill_tools()


if __name__ == "__main__":
    main()
