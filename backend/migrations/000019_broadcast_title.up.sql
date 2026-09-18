ALTER TABLE streams ADD COLUMN broadcast_title text NOT NULL DEFAULT '';

UPDATE streams s
SET broadcast_title=t.desired_title
FROM twitch_channels c
JOIN twitch_connections t ON t.channel_id=c.id
WHERE c.source_id=s.id AND t.desired_title<>'';
