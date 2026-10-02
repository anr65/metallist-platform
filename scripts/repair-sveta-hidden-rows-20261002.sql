-- Targeted correction of an unposted preview. Original source/version remain immutable.
-- Execute after a verified backup and rehearsing on its restored copy.
BEGIN;
DO $$
DECLARE
 target constant uuid := 'cae41fe6-7375-4fbe-8777-5972aadb9638';
 source_hash text;
 old_version integer;
 old_status text;
 old_total bigint;
 previous_rows jsonb;
BEGIN
 SELECT r.status,r.version,r.total_cents,s.sha256 INTO old_status,old_version,old_total,source_hash
 FROM registries r JOIN source_documents s ON s.id=r.source_id WHERE r.id=target FOR UPDATE OF r;
 IF old_status IS DISTINCT FROM 'preview' OR old_version IS DISTINCT FROM 1
 OR old_total IS DISTINCT FROM 597826733
 OR source_hash IS DISTINCT FROM '957d02d0717cefc5be5f82d9e89dffda088bdd240c94d333aaace435c5bc7248'
 THEN RAISE EXCEPTION 'unexpected target preview or source'; END IF;
 IF EXISTS(SELECT 1 FROM journal_entries WHERE event_id=target)
 OR (SELECT count(*) FROM registry_rows WHERE registry_id=target)<>33
 THEN RAISE EXCEPTION 'target has postings or unexpected rows'; END IF;
 PERFORM id FROM registry_rows WHERE registry_id=target FOR UPDATE;
 IF (SELECT array_agg(row_no ORDER BY row_no) FROM registry_rows WHERE registry_id=target AND row_no BETWEEN 21 AND 29)
 IS DISTINCT FROM ARRAY[21,22,23,24,25,26,27,28,29]
 OR (SELECT array_agg(amount_cents ORDER BY row_no) FROM registry_rows WHERE registry_id=target AND row_no BETWEEN 21 AND 29)
 IS DISTINCT FROM ARRAY[3044636,444724,111600,47498,387696,121375,777924,247690,604890]::bigint[]
 OR EXISTS(SELECT 1 FROM registry_rows WHERE registry_id=target AND row_no BETWEEN 21 AND 29
   AND (card_id IS NOT NULL OR error_code IS DISTINCT FROM 'request_card_not_found_or_ambiguous'))
 THEN RAISE EXCEPTION 'unexpected hidden source rows'; END IF;
 SELECT jsonb_agg(jsonb_build_object('id',id,'source_row',row_no,'amount_cents',amount_cents,
  'previous_error',error_code,'exclusion_code','source_row_hidden') ORDER BY row_no)
 INTO previous_rows FROM registry_rows WHERE registry_id=target AND row_no BETWEEN 21 AND 29;
 DELETE FROM registry_rows WHERE registry_id=target AND row_no BETWEEN 21 AND 29;
 IF (SELECT count(*) FROM registry_rows WHERE registry_id=target)<>24
 OR (SELECT sum(amount_cents) FROM registry_rows WHERE registry_id=target)<>592038700
 OR EXISTS(SELECT 1 FROM registry_rows WHERE registry_id=target AND (card_id IS NULL OR error_code IS NOT NULL))
 THEN RAISE EXCEPTION 'corrected preview does not reconcile'; END IF;
 UPDATE registries SET total_cents=592038700,version=version+1 WHERE id=target;
 INSERT INTO audit_events(id,channel,action,object_type,object_id,outcome,detail)
 VALUES(gen_random_uuid(),'system','registry_hidden_row_correction','registry',target,'success',
 jsonb_build_object('reason','XLS BIFF hidden rows were incorrectly imported','effective_parser_version',4,
  'excluded_source_rows',previous_rows,'old_total_cents',old_total,'new_total_cents',592038700,
  'old_version',old_version,'new_version',old_version+1,'source_sha256',source_hash));
END $$;
COMMIT;
