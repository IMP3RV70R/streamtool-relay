#!/usr/bin/env python3
"""Check tracked + unignored candidate files without printing credential contents."""
import hashlib
import re
import subprocess
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
# Known public fixture bytes; replacing these with private credentials is forbidden.
FIXTURES = {'infra/local/secrets/admin_token': 'df5ca3f1040564c7c1b1d6c1fcae4058b493bd879cdd60aecfdb4ad1d6beb198', 'infra/local/secrets/destination_key': 'e80b71cd14d3cbd65f4173abcbfcf01a545dbca32a72d575108b553a648cc96f', 'infra/local/secrets/destination_key_2': '3386395a6d8e015d58b75e8f1593836b05cfd6923ce7e1d7f9e55d7eee5bb6dc', 'infra/local/secrets/destination_key_3': 'eb0573908be35d913a7ce6814ebeac4f5a12dcb61d1c9087a4e99b1850956900', 'infra/local/secrets/edge_control_token': '2f6473c9eb94537b21c0ddc2c5ea9c2a0381fe8e3fbff12b975459802fc3f397', 'infra/local/secrets/edge_read_key': '02fb37252f66ddd35459c8acaa384c014fbd047cad836812b627d02cfe98c9c6', 'infra/local/secrets/envelope_key': '7d3a1c8c0fb3f26a4cb1cd78f37751d39dd8d37addb78103d8a346d010eeccdc', 'infra/local/secrets/postgres_password': '1697ef1a8cea6a37562267678c7a675805ba69757d8432fba39cacf5a642d9d8', 'infra/local/secrets/cabinet_postgres_password': 'c263b3482d81d912ba02281a012442e596cbf46c54d5e83fea3a5316a1ffa2d7', 'infra/local/secrets/source_token': 'e3ac31c5ad01b0a88571c433818e535cb9db107d06b79559f001caa5b7010907', 'infra/local/secrets/worker_control_token': '9a31835b2e9399c8dfd06fc8ff5d572897157cebb569b4492cfc60fbdb953639'}
PRIVATE = re.compile(r"-----BEGIN (?:RSA |EC |OPENSSH |DSA |ENCRYPTED )?PRIVATE KEY-----|gh[pousr]_[A-Za-z0-9]{36,}|github_pat_[A-Za-z0-9_]{60,}|AKIA[0-9A-Z]{16}")

def check():
    result = subprocess.run(
        ["git", "ls-files", "--cached", "--others", "--exclude-standard", "-z"],
        cwd=ROOT, check=True, stdout=subprocess.PIPE,
    )
    errors = []
    for name in sorted(set(result.stdout.decode().strip("\0").split("\0"))):
        path = ROOT / name
        if not name or not path.exists():
            continue  # staged deletions / removed historical files
        if path.is_symlink():
            errors.append(f"{name}: publication symlink requires review")
            continue
        parts = Path(name).parts
        forbidden = any(p in {".artifacts", ".gradle", ".kotlin", "node_modules", "__pycache__"} for p in parts)
        forbidden |= name.startswith("apps/") and "build" in parts
        forbidden |= path.suffix.lower() in {".apk", ".aab", ".jks", ".keystore", ".pem", ".key", ".p12", ".pfx", ".db", ".sqlite", ".sqlite3", ".pyc"}
        forbidden |= path.name in {"local.properties", ".DS_Store"} or path.name.endswith(("-wal", "-shm"))
        forbidden |= (path.name == ".env" or path.name.startswith(".env.")) and not path.name.endswith(".example")
        if forbidden:
            errors.append(f"{name}: private/generated file in publication set")
            continue
        if name.startswith("infra/local/secrets/") and name != "infra/local/secrets/README.md":
            if name not in FIXTURES or hashlib.sha256(path.read_bytes()).hexdigest() != FIXTURES[name]:
                errors.append(f"{name}: not a known public development fixture")
            continue
        if path.stat().st_size > 5 * 1024 * 1024:
            errors.append(f"{name}: large file requires explicit publication review")
            continue
        data = path.read_bytes()
        if b"\0" not in data and PRIVATE.search(data.decode("utf-8", errors="replace")):
            errors.append(f"{name}: possible private credential; contents suppressed")
    if errors:
        raise SystemExit("\n".join(errors))
    print("Repository hygiene passed (tracked and unignored candidate files).")

if __name__ == "__main__":
    check()
