-- Existing owner sources remain usable without a separate routing switch.
-- Preserve source keys, configuration and session completion/fencing state.
UPDATE streams SET enabled=true,generation=generation+1,
    updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
WHERE NOT enabled AND id IN (SELECT stream_id FROM account_sources);
