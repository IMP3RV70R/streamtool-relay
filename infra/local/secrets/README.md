# Public development credentials

The allowlisted files in this directory contain deterministic, public credentials
for isolated local Compose/test fixtures. They are not production secrets and must
never be copied to an internet-facing installation. `envelope_key` is likewise a
public test encryption key. Production installation generates independent keys.

The local `setup_token` is generated on demand and ignored. Twitch application
secrets and other private files are ignored. Do not replace public fixture files
with real credentials; publication checks pin their known fixture hashes.
