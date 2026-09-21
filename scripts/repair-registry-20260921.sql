-- One-time correction of a preview created with the erroneous card-assignment check.
-- Preconditions fail closed; original encrypted source and audit history are retained.
BEGIN;
DO $$
DECLARE
    target_registry constant uuid := '5960958a-9dc4-40c6-9b72-6f56d60b10d9';
    expected_hash constant text := 'cf2a0f1511b201fcadbaa57d38a80cbb45651df83ca0683d9b3eec6e0667f71e';
    selected_request_id uuid;
    registry_status text;
    source_hash text;
    old_total bigint;
    new_total bigint;
    old_version integer;
    row_count integer;
    error_count integer;
    match_count integer;
    matched_card uuid;
    matched_mask text;
    item record;
    fixed_rows integer[] := '{}';
BEGIN
    SELECT r.payment_request_id,r.status,r.total_cents,r.version,s.sha256
      INTO selected_request_id,registry_status,old_total,old_version,source_hash
      FROM registries r JOIN source_documents s ON s.id=r.source_id
      WHERE r.id=target_registry FOR UPDATE OF r;
    IF selected_request_id IS NULL OR registry_status <> 'preview' OR old_version <> 1 OR source_hash <> expected_hash THEN
        RAISE EXCEPTION 'unexpected registry state or source';
    END IF;
    SELECT count(*),count(*) FILTER (WHERE error_code='card_not_assigned')
      INTO row_count,error_count FROM registry_rows WHERE registry_id=target_registry;
    IF row_count <> 5 OR error_count <> 3 OR old_total <> 48826500 THEN
        RAISE EXCEPTION 'unexpected row count, error count, or accepted total';
    END IF;
    FOR item IN SELECT id,row_no,raw FROM registry_rows
                WHERE registry_id=target_registry AND error_code='card_not_assigned' AND card_id IS NULL
                ORDER BY row_no FOR UPDATE LOOP
        SELECT count(*),min(v.value) INTO match_count,matched_mask
          FROM jsonb_array_elements_text(item.raw) AS v(value)
          WHERE v.value ~ '^[0-9]{6}\*{6}[0-9]{4}$';
        IF match_count <> 1 THEN RAISE EXCEPTION 'row % has ambiguous card masks',item.row_no; END IF;
        SELECT count(*),min(x.card_id::text)::uuid INTO match_count,matched_card
          FROM payment_request_rows x JOIN cards c ON c.id=x.card_id
          WHERE x.request_id=selected_request_id AND c.mask=matched_mask;
        IF match_count <> 1 THEN RAISE EXCEPTION 'row % has ambiguous request card',item.row_no; END IF;
        UPDATE registry_rows SET card_id=matched_card,error_code=NULL WHERE id=item.id;
        fixed_rows := array_append(fixed_rows,item.row_no);
    END LOOP;
    IF fixed_rows <> ARRAY[4,5,6] THEN RAISE EXCEPTION 'unexpected corrected rows'; END IF;
    SELECT COALESCE(sum(amount_cents),0) INTO new_total FROM registry_rows WHERE registry_id=target_registry;
    IF EXISTS (SELECT 1 FROM registry_rows WHERE registry_id=target_registry AND (error_code IS NOT NULL OR card_id IS NULL))
       OR new_total <> 122270500 THEN
        RAISE EXCEPTION 'preview still has unresolved rows or wrong total';
    END IF;
    UPDATE registries SET total_cents=new_total,version=version+1 WHERE id=target_registry;
    INSERT INTO audit_events(id,channel,action,object_type,object_id,outcome,detail)
    VALUES (gen_random_uuid(),'system','registry_validation_correction','registry',target_registry,'success',
            jsonb_build_object('reason','erroneous operator card assignment check','rows',fixed_rows,
                               'previous_error','card_not_assigned','old_total_cents',old_total,
                               'new_total_cents',new_total,'old_version',old_version,'new_version',old_version+1));
END $$;
COMMIT;
