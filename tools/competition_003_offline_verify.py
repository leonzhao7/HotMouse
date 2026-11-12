#!/usr/bin/env python3
"""Replay four offline methods against competition_003 artifacts, without submissions.

Run with the repository .venv/bin/python. Reads SQLite in read-only mode, reads
saved challenge files, and writes no files. Outputs timings and comparisons with
competition_002 accepted flags; never prints flag values. No network access or
challenge executables are used. The local solve_lcg module is the inspected
competition_002 Python solver; its main() (which writes a file) is never called.
"""
import ast
import base64
import hashlib
import importlib.util
import json
import pathlib
import re
import sqlite3
import subprocess
import time

from Crypto.Cipher import AES, ChaCha20
from Crypto.Util.Padding import unpad
from Crypto.Util.number import long_to_bytes

ROOT = pathlib.Path(__file__).resolve().parents[1]
WORKERS = ROOT / 'backend/data/workspaces/hotmouse'
WORKER = 'worker-f6c9bc99e3f39505'


def workspace(project):
    return WORKERS / project / 'workers' / WORKER


def permutations():
    values = {}
    path = workspace('proj_072') / 'challenge/output.txt'
    for line in path.read_text().splitlines():
        if line.strip():
            k, v = line.split(' = ', 1)
            values[k] = ast.literal_eval(v)
    g, a, b = (values[k] for k in ('g', 'A', 'B'))
    seen = set()
    shared = [0] * len(g)
    cycles = 0
    for origin in range(len(g)):
        if origin in seen:
            continue
        cycle = []
        pos = origin
        while pos not in seen:
            cycle.append(pos)
            seen.add(pos)
            pos = g[pos]
        positions = {v: j for j, v in enumerate(cycle)}
        shift_a = positions[a[cycle[0]]]
        shift_b = positions[b[cycle[0]]]
        length = len(cycle)
        for j, v in enumerate(cycle):
            assert a[v] == cycle[(j + shift_a) % length]
            assert b[v] == cycle[(j + shift_b) % length]
            shared[v] = cycle[(j + shift_a * shift_b) % length]
        cycles += 1
    key = hashlib.sha256(long_to_bytes(hash(tuple(shared)))).digest()[16:32]
    iv = b"mg'g\xce\x08\xdbYN2\x89\xad\xedlY\xb9"
    result = unpad(AES.new(key, AES.MODE_CBC, iv).decrypt(values['c']), 16)
    return result, {'elements': len(g), 'cycles': cycles, 'all_cycle_checks': True}


def lcg():
    path = workspace('proj_045') / 'g-16/solve_lcg.py'
    spec = importlib.util.spec_from_file_location('audited_lcg_solver', path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    ct = module.parse_ct(workspace('proj_081') / 'g16/ciphertext.txt')
    result, codes = module.decode(ct)
    assert len(ct) % 7 == 0
    assert all(32 <= v < 127 for v in codes)
    return result.encode(), {'ciphertext_integers': len(ct), 'integers_per_character': 7}


def chacha():
    data = (workspace('proj_085') / 'artifacts/app').read_bytes()
    key = data[0x4010:0x4030]
    nonce = data[0x5088:0x5090]
    ct = data[0x4050:0x4094]
    result = ChaCha20.new(key=key, nonce=nonce).decrypt(ct)
    return result, {'key_bytes': len(key), 'nonce_bytes': len(nonce), 'counter': 0,
                    'ciphertext_bytes': len(ct)}


def pcap():
    path = workspace('proj_077') / 'capture.pcap'
    output = subprocess.run(
        ['/usr/bin/tshark', '-r', str(path), '-q', '-z', 'follow,tcp,ascii,5'],
        capture_output=True, text=True, check=True, timeout=30,
    ).stdout
    dk = hashlib.pbkdf2_hmac('sha1', b'VYAemVeO3zUDTL6N62kVA', b'Very_S3cr3t_S', 1000, 48)
    decoded = []
    for line in output.splitlines():
        line = line.strip()
        for match in re.finditer(r'(?:\d+\.)?([A-Za-z0-9+/]{20,}={0,2})', line):
            try:
                ct = base64.b64decode(match[1], validate=True)
                if not ct or len(ct) % 16:
                    continue
                decoded.append(unpad(AES.new(dk[:32], AES.MODE_CBC, dk[32:]).decrypt(ct), 16))
            except (ValueError, base64.binascii.Error):
                pass
    text = b'\n'.join(decoded).decode(errors='replace')
    part1 = re.search(r'HTB\{[^\s>]+', text)[0]
    part2 = re.search(r'2nd flag part:\s*(\S+)', text)[1]
    encoded = re.search(r'powershell\.exe -encoded "([A-Za-z0-9+/=]+)"', output)[1]
    powershell = base64.b64decode(encoded).decode('utf-16le')
    part3 = re.search(r'-TaskName "([^"\n]+\})"', powershell)[1]
    return (part1 + part2 + part3).encode(), {'decrypted_frames': len(decoded), 'parts': 3,
                    'pbkdf2_iterations': 1000, 'derived_bytes': 48}


def main():
    conn = sqlite3.connect(f'file:{ROOT}/backend/data/hotmouse.db?mode=ro', uri=True)
    results = []
    for challenge, solver in [('g-33', permutations), ('g-16', lcg), ('g-22', chacha), ('g-29', pcap)]:
        expected = {r[0].encode() for r in conn.execute(
            "SELECT f.value FROM flags f JOIN competition_items i ON i.project_id=f.project_id "
            "WHERE i.run_id='competition_002' AND i.challenge_id=? AND f.status='accepted'",
            (challenge,),
        )}
        verification_source = 'flag_ledger'
        if challenge == 'g-16' and not expected:
            # This older worker submitted directly; its accepted response is
            # saved on disk and competition_items records success, but the
            # central flags table has no row for this particular challenge.
            directory = workspace('proj_045') / 'g-16'
            response = json.loads((directory / 'submit.json').read_text())
            assert response['correct'] is True
            assert response['correct_flag_count'] == response['total_flag_count']
            expected = {(directory / 'flag.txt').read_text().strip().encode()}
            verification_source = 'saved_platform_submission_response'
        start = time.perf_counter()
        value, details = solver()
        elapsed = time.perf_counter() - start
        assert value in expected, f'{challenge}: differs from prior accepted flag'
        results.append({'challenge_id': challenge, 'elapsed_seconds': round(elapsed, 6),
                        'matches_competition_002_accepted': True,
                        'verification_source': verification_source,
                        'plaintext_bytes': len(value), 'details': details})
    conn.close()
    print(json.dumps(results, indent=2, ensure_ascii=False))


if __name__ == '__main__':
    main()
