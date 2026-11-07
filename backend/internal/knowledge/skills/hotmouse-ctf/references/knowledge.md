# Offline CTF knowledge routing

Search with the observed product, technique or blocker, then read a few full
entries. Files live in the bundle's `entries/` directory; the paths below work
from this exported reference. `hotmouse kb list --tag web` lists a category,
and `hotmouse kb search "SSRF" --output json` returns ranked summaries.

| Situation | Starting entries | Useful tags |
| --- | --- | --- |
| HTTP service / application fingerprint | [kb001](../../../entries/kb001.json), [kb052](../../../entries/kb052.json), [kb053](../../../entries/kb053.json) | web, recon, fingerprint |
| SQL / templates / upload / PHP | [kb002](../../../entries/kb002.json), [kb003](../../../entries/kb003.json), [kb004](../../../entries/kb004.json), [kb005](../../../entries/kb005.json) | sqli, upload, ssti, deser |
| SSRF / command execution / XML | [kb006](../../../entries/kb006.json), [kb007](../../../entries/kb007.json), [kb008](../../../entries/kb008.json) | ssrf, rce, xxe |
| JWT / session / access control | [kb009](../../../entries/kb009.json), [kb037](../../../entries/kb037.json), [kb039](../../../entries/kb039.json) | jwt, auth, logic |
| Product and version match | [kb016](../../../entries/kb016.json), [kb031](../../../entries/kb031.json), [kb050](../../../entries/kb050.json), [kb054](../../../entries/kb054.json) | cve, middleware |
| Credentials / privilege boundaries | [kb010](../../../entries/kb010.json), [kb011](../../../entries/kb011.json), [kb012](../../../entries/kb012.json), [kb013](../../../entries/kb013.json) | cred, privesc |
| Network / services / directories | [kb015](../../../entries/kb015.json), [kb033](../../../entries/kb033.json), [kb036](../../../entries/kb036.json), [kb048](../../../entries/kb048.json) | network, pivot, ldap |
| Containers / cloud / AI applications | [kb014](../../../entries/kb014.json), [kb029](../../../entries/kb029.json), [kb030](../../../entries/kb030.json), [kb046](../../../entries/kb046.json) | container, cloud, ai |
| Offline misc / crypto / binaries | [kb040](../../../entries/kb040.json), [kb041](../../../entries/kb041.json), [kb042](../../../entries/kb042.json) | misc, crypto, pwn |
| Current Step is a flag hunt / blind output | [kb019](../../../entries/kb019.json), [kb049](../../../entries/kb049.json), [kb056](../../../entries/kb056.json) | flag, flag-hunt, blind |
| Planning / stalled work / memory | [kb020](../../../entries/kb020.json), [kb022](../../../entries/kb022.json), [kb023](../../../entries/kb023.json), [kb024](../../../entries/kb024.json) | meta, strategy, memory |

Historical competition rules, provider choices and fixed timings in meta
entries describe their source runs. HotMouse's current shared project budget, worker
configuration and result/flag contracts take precedence. KB references are
not project facts and cannot prove completion. CVE lists need current version
and configuration checks; no entry constitutes verified applicability.
