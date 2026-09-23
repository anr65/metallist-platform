-- Make every existing active card available to every active collector and operator.
-- The application no longer uses card_assignments after this compatibility backfill.
DO $$
BEGIN
    IF (SELECT COALESCE(MAX(version), 0) FROM schema_migrations) <> 12 THEN
        RAISE EXCEPTION 'schema 013 requires version 12';
    END IF;
END $$;

INSERT INTO card_assignments(user_id, card_id, assigned_by)
SELECT u.id, c.id, u.id
FROM users u
CROSS JOIN cards c
WHERE u.active
  AND u.role IN ('operator', 'collector')
  AND c.status = 'active'
ON CONFLICT (user_id, card_id) DO NOTHING;

INSERT INTO schema_migrations(version) VALUES (13);
