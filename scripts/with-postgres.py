#!/usr/bin/env python3
"""Run one command against an explicit DSN or an owned ephemeral PostgreSQL 16."""
import json
import os
from pathlib import Path
import subprocess
import sys
import time
import uuid

if len(sys.argv) < 2:
    raise SystemExit("usage: with-postgres.py COMMAND [ARG ...]")
env = os.environ.copy()
if env.get("GOML_POSTGRES_TEST_DSN"):
    raise SystemExit(subprocess.call(sys.argv[1:], env=env))
root = Path(__file__).resolve().parent.parent
report_dir = root / "_artifact"
report_dir.mkdir(exist_ok=True)
name = "gomlang-postgres-test-" + uuid.uuid4().hex[:12]
record = {"name": name, "image": "postgres:16", "cleanup": "not started"}
created = False
code = 1
try:
    record["id"] = subprocess.check_output([
        "docker", "run", "-d", "--rm", "--name", name,
        "--tmpfs", "/var/lib/postgresql/data",
        "-e", "POSTGRES_PASSWORD=goml_test", "-e", "POSTGRES_DB=goml_test",
        "-p", "127.0.0.1::5432", "postgres:16",
    ], text=True).strip()
    created = True
    for _ in range(150):
        ready = subprocess.run(["docker", "exec", name, "pg_isready", "-h", "127.0.0.1", "-U", "postgres", "-d", "goml_test"], capture_output=True)
        if ready.returncode == 0:
            break
        time.sleep(0.2)
    else:
        raise RuntimeError("PostgreSQL 16 did not become ready")
    info = json.loads(subprocess.check_output(["docker", "inspect", name], text=True))[0]
    port = info["NetworkSettings"]["Ports"]["5432/tcp"][0]["HostPort"]
    record["port"] = int(port)
    record["image_id"] = info["Image"]
    record["version"] = subprocess.check_output(["docker", "exec", name, "postgres", "--version"], text=True).strip()
    env["GOML_POSTGRES_TEST_DSN"] = f"postgresql://postgres:goml_test@127.0.0.1:{port}/goml_test?sslmode=disable"
    code = subprocess.call(sys.argv[1:], env=env)
    record["command_exit"] = code
finally:
    if created:
        cleanup = subprocess.run(["docker", "rm", "-f", name], capture_output=True, text=True)
        record["cleanup"] = "removed" if cleanup.returncode == 0 else cleanup.stderr.strip()
        if cleanup.returncode != 0:
            code = 1
    (report_dir / "last-cluster-test.json").write_text(json.dumps(record, indent=2) + "\n")
raise SystemExit(code)
