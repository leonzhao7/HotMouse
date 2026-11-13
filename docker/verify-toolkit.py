"""Verify pinned packages and native toolkit functionality in the final image."""

import importlib
import importlib.metadata
import os
from pathlib import Path
import sys


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


if __name__ == "__main__":
    main()
