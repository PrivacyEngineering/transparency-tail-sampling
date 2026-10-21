# Parts of this file were developed with the assistance of Claude (Anthropic) and reviewed by the authors.
"""Print the tilt.* attributes of every span of a Tempo trace (JSON from /api/traces/<id> on stdin)."""
import json, sys
d = json.load(sys.stdin)
for b in d.get("batches", d.get("resourceSpans", [])):
    svc = next(a["value"].get("stringValue") for a in b["resource"]["attributes"] if a["key"] == "service.name")
    for ss in b.get("scopeSpans", b.get("instrumentationLibrarySpans", [])):
        for s in ss["spans"]:
            at = {a["key"]: list(a["value"].values())[0] for a in s.get("attributes", []) if a["key"].startswith("tilt.")}
            print(f"{svc:22s} {s['name'][:40]:40s} {at}")
