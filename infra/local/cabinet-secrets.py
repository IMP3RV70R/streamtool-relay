"""Generate persistent secrets for the isolated local cabinet (never overwrite)."""
import base64
from pathlib import Path
import secrets

root = Path(__file__).resolve().parents[2]
directory = root / ".artifacts" / "cabinet-secrets"
directory.mkdir(parents=True, exist_ok=True, mode=0o700)
directory.chmod(0o700)
for name in ("setup_token", "envelope_key", "admin_token", "edge_control_token", "edge_read_key"):
    path = directory / name
    if not path.exists():
        with path.open("x") as output:
            output.write(base64.b64encode(secrets.token_bytes(32)).decode() + "\n")
        # Readable by the non-root container user; parent is private on the host.
        path.chmod(0o644)
