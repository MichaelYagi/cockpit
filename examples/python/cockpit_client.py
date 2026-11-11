#!/usr/bin/env python3
"""Minimal Cockpit API client using only the Python standard library.

    COCKPIT_API_KEY=$(cat my.key) python3 cockpit_client.py [--events N]

Reads COCKPIT_API_URL (default http://127.0.0.1:7878) and COCKPIT_API_KEY.
The key needs metrics:read (and system:read for the host line). Keys are
sent only in the Authorization header, never in URLs.
"""
import argparse
import json
import os
import sys
import urllib.error
import urllib.request

BASE = os.environ.get("COCKPIT_API_URL", "http://127.0.0.1:7878").rstrip("/") + "/api/v1"


def get(path, key, stream=False, timeout=10):
    req = urllib.request.Request(BASE + path, headers={"Authorization": "Bearer " + key, "Accept": "application/json"})
    try:
        resp = urllib.request.urlopen(req, timeout=timeout)
    except urllib.error.HTTPError as e:
        body = json.load(e)
        raise SystemExit(f"{path}: HTTP {e.code} {body['error']['code']}: {body['error']['message']}")
    return resp if stream else json.load(resp)


def section(s):
    """Return a section's data, or None with the reason printed."""
    if s["status"] != "available" or s["data"] is None:
        print(f"  ({s['status']}: {s.get('reason', '')})")
        return None
    return s["data"]


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--events", type=int, default=0, help="also print this many live events (SSE)")
    args = ap.parse_args()
    key = os.environ.get("COCKPIT_API_KEY", "")
    if not key:
        sys.exit("Set COCKPIT_API_KEY")

    try:
        system = get("/system", key)["system"]["data"]
        print(f"host: {system['hostname']} ({system['os']['pretty_name']})")
    except SystemExit as e:  # a metrics-only key cannot read /system
        print(f"host: unknown ({e})")

    health = get("/health", key)
    print(f"health: {health['status']}")
    for c in health["checks"]:
        print(f"  {c['id']:18} {c['status']:8} {c.get('summary') or c.get('reason', '')}")

    m = get("/metrics?sections=cpu,memory", key)
    print("cpu:")
    cpu = section(m["cpu"])
    if cpu and cpu["usage_percent"] is not None:
        print(f"  usage {cpu['usage_percent']:.1f}% of {cpu['logical_cores']} cores")
    print("memory:")
    mem = section(m["memory"])
    if mem:
        print(f"  used {mem['used_percent']:.1f}% of {mem['total_bytes'] / 2**30:.1f} GiB")

    if args.events:
        print("events:")
        resp = get("/events/stream", key, stream=True, timeout=60)
        seen, event = 0, None
        for raw in resp:
            line = raw.decode().rstrip("\n")
            if line.startswith("event: "):
                event = line[7:]
            elif line.startswith("data: ") and event == "event":
                e = json.loads(line[6:])
                print(f"  {e['time']} {e['severity']:8} {e['type']}: {e['message']}")
                seen += 1
                if seen >= args.events:
                    break


if __name__ == "__main__":
    main()
