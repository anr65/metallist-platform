-- One-time correction of the 21 September opening cash custodian.
-- Run only after a fresh production backup and a read-only preview of the affected entries.
-- The existing 210,000 RUB Tony opening is independent and remains posted.
BEGIN ISOLATION LEVEL SERIALIZABLE;
SELECT pg_advisory_xact_lock(704112);

DO $correction$
DECLARE
    original_id CONSTANT uuid := '019ad708-31e2-4ebc-9cfc-559cc618e8c5';
    tony_opening_id CONSTANT uuid := '64a220de-c87f-4ec8-8280-7224e6323cf3';
    target_cents CONSTANT bigint := 24700000;
    original_actor uuid;
    original_occurred_at timestamptz;
    original_recognition_at timestamptz;
    chief_id uuid;
    tony_id uuid;
    reversal_id uuid := gen_random_uuid();
    replacement_id uuid := gen_random_uuid();
    cash_before bigint;
    chief_before bigint;
    tony_before bigint;
    cash_after bigint;
    chief_after bigint;
    tony_after bigint;
BEGIN
    IF current_database() NOT IN ('metallist_demo', 'metallist_correction_disposable') THEN
        RAISE EXCEPTION 'Unexpected database: %', current_database();
    END IF;

    SELECT actor_id, occurred_at, recognition_at
      INTO STRICT original_actor, original_occurred_at, original_recognition_at
      FROM journal_entries
     WHERE id = original_id
       AND event_type = 'opening_balance'
       AND idempotency_key = 'opening:chief:2026-09-21';

    IF (SELECT count(*) FROM postings WHERE entry_id = original_id) <> 2 THEN
        RAISE EXCEPTION 'Chief opening entry does not have exactly two postings';
    END IF;
    SELECT custodian_id INTO STRICT chief_id
      FROM postings
     WHERE entry_id = original_id AND account = '1210'
       AND side = 'debit' AND amount_cents = target_cents;
    IF NOT EXISTS (
        SELECT 1 FROM custodians WHERE id = chief_id AND kind = 'chief'
    ) OR NOT EXISTS (
        SELECT 1 FROM postings WHERE entry_id = original_id
           AND account = '3100' AND side = 'credit'
           AND amount_cents = target_cents AND custodian_id IS NULL
    ) THEN
        RAISE EXCEPTION 'Chief opening entry does not match the expected 247,000 RUB';
    END IF;

    IF (SELECT count(*) FROM postings WHERE entry_id = tony_opening_id) <> 2 THEN
        RAISE EXCEPTION 'Tony opening entry does not have exactly two postings';
    END IF;
    SELECT custodian_id INTO STRICT tony_id
      FROM postings
     WHERE entry_id = tony_opening_id AND account = '1200'
       AND side = 'debit' AND amount_cents = 21000000;
    IF NOT EXISTS (
        SELECT 1 FROM custodians WHERE id = tony_id
           AND kind = 'collector' AND name = 'Тони Сопрано'
    ) OR NOT EXISTS (
        SELECT 1 FROM postings WHERE entry_id = tony_opening_id
           AND account = '3100' AND side = 'credit' AND amount_cents = 21000000
    ) THEN
        RAISE EXCEPTION 'Tony opening entry does not match the expected 210,000 RUB';
    END IF;

    IF EXISTS (SELECT 1 FROM journal_entries WHERE reversal_of = original_id)
       OR EXISTS (
           SELECT 1 FROM journal_entries
            WHERE idempotency_key IN (
                'opening:chief:2026-09-21:reversal-to-tony',
                'opening:tony:2026-09-21:chief-reallocation'
            )
       ) THEN
        RAISE EXCEPTION 'Opening correction was already posted or the original was reversed';
    END IF;

    SELECT COALESCE(sum(CASE WHEN side = 'debit' THEN amount_cents ELSE -amount_cents END), 0)
      INTO cash_before FROM postings WHERE account IN ('1100', '1200', '1210');
    SELECT COALESCE(sum(CASE WHEN side = 'debit' THEN amount_cents ELSE -amount_cents END), 0)
      INTO chief_before FROM postings WHERE account = '1210' AND custodian_id = chief_id;
    SELECT COALESCE(sum(CASE WHEN side = 'debit' THEN amount_cents ELSE -amount_cents END), 0)
      INTO tony_before FROM postings WHERE account = '1200' AND custodian_id = tony_id;
    IF chief_before < target_cents THEN
        RAISE EXCEPTION 'Chief cash is insufficient for the opening reversal';
    END IF;

    INSERT INTO journal_entries(id, event_type, event_id, occurred_at, recognition_at,
                                actor_id, reversal_of, idempotency_key)
    VALUES (reversal_id, 'reversal', gen_random_uuid(), now(), original_recognition_at,
            original_actor, original_id, 'opening:chief:2026-09-21:reversal-to-tony');
    INSERT INTO postings(id, entry_id, account, side, amount_cents, custodian_id)
    VALUES (gen_random_uuid(), reversal_id, '3100', 'debit', target_cents, NULL),
           (gen_random_uuid(), reversal_id, '1210', 'credit', target_cents, chief_id);

    INSERT INTO journal_entries(id, event_type, event_id, occurred_at, recognition_at,
                                actor_id, idempotency_key)
    VALUES (replacement_id, 'opening_balance_correction', gen_random_uuid(),
            original_occurred_at, original_recognition_at, original_actor,
            'opening:tony:2026-09-21:chief-reallocation');
    INSERT INTO postings(id, entry_id, account, side, amount_cents, custodian_id)
    VALUES (gen_random_uuid(), replacement_id, '1200', 'debit', target_cents, tony_id),
           (gen_random_uuid(), replacement_id, '3100', 'credit', target_cents, NULL);

    SELECT COALESCE(sum(CASE WHEN side = 'debit' THEN amount_cents ELSE -amount_cents END), 0)
      INTO cash_after FROM postings WHERE account IN ('1100', '1200', '1210');
    SELECT COALESCE(sum(CASE WHEN side = 'debit' THEN amount_cents ELSE -amount_cents END), 0)
      INTO chief_after FROM postings WHERE account = '1210' AND custodian_id = chief_id;
    SELECT COALESCE(sum(CASE WHEN side = 'debit' THEN amount_cents ELSE -amount_cents END), 0)
      INTO tony_after FROM postings WHERE account = '1200' AND custodian_id = tony_id;
    IF cash_after <> cash_before OR chief_after <> chief_before - target_cents
       OR tony_after <> tony_before + target_cents THEN
        RAISE EXCEPTION 'Opening correction failed the cash conservation check';
    END IF;

    INSERT INTO audit_events(id, actor_id, actor_role, channel, action, object_type,
                             object_id, outcome, reason, detail)
    VALUES (gen_random_uuid(), original_actor, 'chief', 'maintenance',
            'opening_balance_correction', 'journal_entry', original_id, 'success',
            'Owner clarified that 247,000 RUB initially belonged to Tony Soprano, in addition to his 210,000 RUB; chief initially held zero.',
            jsonb_build_object('reversal_entry_id', reversal_id,
                               'replacement_entry_id', replacement_id,
                               'from_custodian_id', chief_id,
                               'to_custodian_id', tony_id,
                               'amount_cents', target_cents,
                               'database_user', current_user));
    RAISE NOTICE 'Cash unchanged: %, chief % -> %, Tony % -> % (kopecks)',
        cash_after, chief_before, chief_after, tony_before, tony_after;
END
$correction$;
COMMIT;
