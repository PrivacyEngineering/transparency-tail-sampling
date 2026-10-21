# Parts of this file were developed with the assistance of Claude (Anthropic) and reviewed by the authors.
"""Sample `docker stats` for the experiment collectors every INTERVAL seconds until killed.
Usage: python3 docker_stats.py OUT.csv RUN_ID [INTERVAL]
Writes long-format CSV: unix_time_s,run_id,container,cpu_percent,mem_mib  (cpu_percent: 100 = one core)."""
import csv, json, subprocess, sys, time

out, run_id = sys.argv[1], sys.argv[2]
interval = float(sys.argv[3]) if len(sys.argv) > 3 else 5.0
UNITS = {"B": 1 / 2**20, "KiB": 1 / 1024, "MiB": 1, "GiB": 1024, "kB": 1e3 / 2**20, "MB": 1e6 / 2**20, "GB": 1e9 / 2**20}

def mib(s):
    s = s.strip()
    for u in sorted(UNITS, key=len, reverse=True):
        if s.endswith(u):
            return float(s[: -len(u)]) * UNITS[u]
    return float("nan")

with open(out, "w", newline="") as f:
    w = csv.writer(f)
    w.writerow(["unix_time_s", "run_id", "container", "cpu_percent", "mem_mib"])
    while True:
        t0 = time.time()
        res = subprocess.run(["docker", "stats", "--no-stream", "--format", "{{json .}}"], capture_output=True, text=True)
        for line in res.stdout.splitlines():
            d = json.loads(line)
            name = d["Name"]
            if not name.startswith(("tts-c-", "tts-c2-", "tts-fanout", "tts-sink")):
                continue
            w.writerow([f"{t0:.1f}", run_id, name.removeprefix("tts-").removesuffix("-1"),
                        d["CPUPerc"].rstrip("%"), f"{mib(d['MemUsage'].split('/')[0]):.1f}"])
        f.flush()
        time.sleep(max(0.0, interval - (time.time() - t0)))
