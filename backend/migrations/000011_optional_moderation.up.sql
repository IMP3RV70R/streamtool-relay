ALTER TABLE accounts ADD COLUMN moderation_enabled boolean NOT NULL DEFAULT false;
UPDATE accounts SET moderation_enabled=true WHERE id IN(SELECT account_id FROM moderation_policies WHERE policy->>'mode'<>'off');
