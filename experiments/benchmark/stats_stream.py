"""Stream `docker stats` for one container (~1 sample/s) into a long-format CSV until killed.
Usage: python3 stats_stream.py CONTAINER OUT.csv LABEL"""
import csv, json, re, subprocess, sys, time

container, out, label = sys.argv[1], sys.argv[2], sys.argv[3]
U = {"B": 1 / 2**20, "KiB": 1 / 1024, "MiB": 1, "GiB": 1024, "kB": 1e3 / 2**20, "MB": 1e6 / 2**20, "GB": 1e9 / 2**20}
def mib(s):
    s = s.strip()
    for u in sorted(U, key=len, reverse=True):
        if s.endswith(u):
            return float(s[: -len(u)]) * U[u]
    return float("nan")
ansi = re.compile(r"\x1b\[[0-9;]*[A-Za-z]")
p = subprocess.Popen(["docker", "stats", "--format", "{{json .}}", container], stdout=subprocess.PIPE, text=True)
with open(out, "w", newline="") as f:
    w = csv.writer(f)
    w.writerow(["unix_time_s", "label", "container", "cpu_cores", "mem_mib"])
    for line in p.stdout:
        line = ansi.sub("", line).strip()
        if not line.startswith("{"):
            continue
        d = json.loads(line)
        if d.get("CPUPerc", "--") in ("--", ""):
            continue
        w.writerow([f"{time.time():.2f}", label, container, f"{float(d['CPUPerc'].rstrip('%')) / 100:.4f}", f"{mib(d['MemUsage'].split('/')[0]):.1f}"])
        f.flush()
