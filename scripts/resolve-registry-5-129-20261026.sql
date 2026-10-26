-- Owner-authorized historical consolidation of registry 5 and contact correction for 129.
BEGIN ISOLATION LEVEL SERIALIZABLE;
SET LOCAL lock_timeout='5s'; SET LOCAL statement_timeout='60s';
SELECT pg_advisory_xact_lock(704127);
LOCK TABLE registries,payment_requests,payment_request_rows,audit_events IN SHARE ROW EXCLUSIVE MODE;
LOCK TABLE source_documents,registry_rows,journal_entries,postings IN SHARE MODE;
CREATE TEMP TABLE expected_requests ON COMMIT DROP AS SELECT * FROM jsonb_to_recordset($plan$[{"id":"e2462eca-78a8-4baa-adce-be52582c16e8","mode":"cards","title":"\u0417\u041a-2026-0001","deleted":false,"version":1,"row_count":2,"row_digest":"641efb1e9c9160a5a9faed9e3c8aba86","merchant_id":"1c9e6f0b-4e76-5890-864d-e624b48ea895","linked_count":0},{"id":"d61ef833-f3bd-4116-99d6-c1166e052385","mode":"cards","title":"\u0422\u041e\u041b\u042f22-09-2026","deleted":false,"version":1,"row_count":2,"row_digest":"0dca675772b4fa5bcc49be52aa610bb4","merchant_id":"1c9e6f0b-4e76-5890-864d-e624b48ea895","linked_count":0},{"id":"f3f824e9-bdf4-4f31-95cc-bddb902b69f9","mode":"cards","title":"\u0422\u041e23-09-2026","deleted":false,"version":1,"row_count":2,"row_digest":"e44dba61cd3738ab60d400e8238ad594","merchant_id":"1c9e6f0b-4e76-5890-864d-e624b48ea895","linked_count":0},{"id":"40efcd16-c9e5-4dec-9e9b-426b5083101e","mode":"cards","title":"\u0422\u041e21-10-2026","deleted":false,"version":1,"row_count":2,"row_digest":"33ed388c25e9d3985ec59838f643b824","merchant_id":"1c9e6f0b-4e76-5890-864d-e624b48ea895","linked_count":0}]$plan$::jsonb)
 AS p(id uuid,version integer,mode text,merchant_id uuid,row_digest text,row_count bigint);
CREATE TEMP TABLE contact_before ON COMMIT DROP AS SELECT * FROM payment_request_rows WHERE request_id='40efcd16-c9e5-4dec-9e9b-426b5083101e';
CREATE OR REPLACE FUNCTION pg_temp.history_digest(target regclass) RETURNS text LANGUAGE plpgsql AS $func$
DECLARE digest text; BEGIN EXECUTE format('SELECT md5(COALESCE(string_agg(to_jsonb(x)::text,%L ORDER BY id),%L)) FROM %s x','','',target) INTO digest; RETURN digest; END $func$;
DO $fix$
DECLARE
 chief CONSTANT uuid := 'c51891b9-64b1-4a66-81fe-0101351d31f4';
 reg5 CONSTANT uuid := '39fbd6e4-c0af-4f3d-9b7b-9683d908eaf2';
 reg129 CONSTANT uuid := 'dac24921-f6e3-468e-b2b2-ddb138a8274a';
 req129 CONSTANT uuid := '40efcd16-c9e5-4dec-9e9b-426b5083101e';
 combined CONSTANT uuid := '0a1eb220-c2ac-490b-83ae-68123ff29b1b';
 merchant CONSTANT uuid := '1c9e6f0b-4e76-5890-864d-e624b48ea895';
 batch_key CONSTANT text := 'owner:20261026:registry5-combined-and-129-contacts';
 baseline jsonb; after_state jsonb; item record; n integer; prior_audit integer;
BEGIN
 IF current_database() NOT IN ('metallist_demo','metallist_registry_exceptions_20261026_testing')
 OR (SELECT max(version) FROM schema_migrations)<>29
 OR NOT EXISTS(SELECT 1 FROM users WHERE id=chief AND role='chief' AND active) THEN RAISE EXCEPTION 'Unexpected target/schema/actor'; END IF;
 SELECT count(*) INTO prior_audit FROM audit_events WHERE detail->>'correction_key'=batch_key;
 IF prior_audit>0 THEN
  IF prior_audit<>7 OR NOT EXISTS(SELECT 1 FROM registries WHERE id=reg5 AND payment_request_id=combined)
   OR NOT EXISTS(SELECT 1 FROM registries WHERE id=reg129 AND payment_request_id=req129)
   OR (SELECT count(*) FROM payment_requests WHERE id IN (SELECT id FROM expected_requests WHERE id<>req129) AND deleted_at IS NOT NULL)<>3
   OR EXISTS(SELECT 1 FROM registry_rows x JOIN registries r ON r.id=x.registry_id WHERE r.id IN (reg5,reg129)
     AND NOT EXISTS(SELECT 1 FROM payment_request_rows y WHERE y.request_id=r.payment_request_id AND y.card_id=x.card_id
       AND y.contact_name=x.raw->>2 AND y.contact_phone=x.raw->>3)) THEN RAISE EXCEPTION 'Prior correction conflicts with current state'; END IF;
  RAISE NOTICE 'Already applied; no changes'; RETURN;
 END IF;
 IF (SELECT count(*) FROM expected_requests)<>4 OR EXISTS(SELECT 1 FROM payment_requests WHERE id=combined OR (merchant_id=merchant AND external_ref='ТО21-23-09-2026')) THEN RAISE EXCEPTION 'Unexpected request set or combined request already exists'; END IF;
 IF NOT EXISTS(SELECT 1 FROM registries WHERE id=reg5 AND number=5 AND merchant_id=merchant AND status='posted' AND version=1 AND payment_request_id IS NULL AND total_cents=147952050 AND payment_date='2026-09-24')
 OR NOT EXISTS(SELECT 1 FROM registries WHERE id=reg129 AND number=129 AND merchant_id=merchant AND status='posted' AND version=1 AND payment_request_id IS NULL AND total_cents=46067400 AND payment_date='2026-10-21') THEN RAISE EXCEPTION 'Registry state changed'; END IF;
 FOR item IN SELECT * FROM expected_requests LOOP
  IF NOT EXISTS(SELECT 1 FROM payment_requests p WHERE p.id=item.id AND p.version=item.version AND p.mode='cards' AND p.merchant_id=merchant AND p.deleted_at IS NULL)
   OR EXISTS(SELECT 1 FROM registries WHERE payment_request_id=item.id AND status<>'deleted')
   OR (SELECT count(*) FROM payment_request_rows WHERE request_id=item.id)<>2
   OR (SELECT md5(string_agg(to_jsonb(x)::text,'' ORDER BY x.id)) FROM payment_request_rows x WHERE x.request_id=item.id)<>item.row_digest THEN RAISE EXCEPTION 'Request % changed',item.id; END IF;
 END LOOP;
 IF (SELECT count(*) FROM registry_rows WHERE registry_id=reg5)<>6 OR (SELECT count(*) FROM registry_rows WHERE registry_id=reg129)<>2
 OR EXISTS(SELECT 1 FROM registry_rows x WHERE x.registry_id=reg5 AND (x.error_code IS NOT NULL OR NOT EXISTS(
    SELECT 1 FROM payment_request_rows y WHERE y.request_id IN (SELECT id FROM expected_requests WHERE id<>req129)
      AND y.card_id=x.card_id AND y.contact_name=x.raw->>2 AND y.contact_phone=x.raw->>3)))
 OR EXISTS(SELECT 1 FROM payment_request_rows y WHERE y.request_id IN (SELECT id FROM expected_requests WHERE id<>req129)
      AND NOT EXISTS(SELECT 1 FROM registry_rows x WHERE x.registry_id=reg5 AND x.card_id=y.card_id)) THEN RAISE EXCEPTION 'Registry 5 composition mismatch'; END IF;
 IF (SELECT count(DISTINCT card_id) FROM payment_request_rows WHERE request_id IN (SELECT id FROM expected_requests WHERE id<>req129))<>6
 OR EXISTS(SELECT 1 FROM payment_request_rows WHERE request_id IN (SELECT id FROM expected_requests) AND (contact_id IS NULL OR contact_name IS NULL OR contact_phone IS NULL)) THEN RAISE EXCEPTION 'Invalid source contacts or overlapping cards'; END IF;
 -- Verify the exact crossed contact pattern; the financial allocation is already correct.
 IF NOT EXISTS(SELECT 1 FROM registry_rows x JOIN cards c ON c.id=x.card_id WHERE x.registry_id=reg129 AND x.row_no=1 AND c.last4='8297' AND x.amount_cents=23009000)
 OR NOT EXISTS(SELECT 1 FROM registry_rows x JOIN cards c ON c.id=x.card_id WHERE x.registry_id=reg129 AND x.row_no=2 AND c.last4='0807' AND x.amount_cents=23058400)
 OR EXISTS(SELECT 1 FROM registry_rows x JOIN payment_request_rows y ON y.request_id=req129 AND y.card_id=x.card_id
    WHERE x.registry_id=reg129 AND NOT EXISTS(SELECT 1 FROM contact_before other WHERE other.id<>y.id AND other.contact_name=x.raw->>2 AND other.contact_phone=x.raw->>3)) THEN RAISE EXCEPTION 'Registry 129 no longer has the approved crossed contacts'; END IF;
 SELECT jsonb_build_object('entries',pg_temp.history_digest('journal_entries'),'postings',pg_temp.history_digest('postings'),
  'sources',pg_temp.history_digest('source_documents'),'registry_rows',pg_temp.history_digest('registry_rows'),
  'registry_content',(SELECT md5(string_agg((to_jsonb(r)-'payment_request_id'-'version')::text,'' ORDER BY id)) FROM registries r),
  'other_registries',(SELECT md5(string_agg(to_jsonb(r)::text,'' ORDER BY id)) FROM registries r WHERE id NOT IN (reg5,reg129)),
  'request_content',(SELECT md5(string_agg((to_jsonb(p)-'version'-'deleted_at'-'deleted_by')::text,'' ORDER BY id)) FROM payment_requests p WHERE id<>combined),
  'other_requests',(SELECT md5(string_agg(to_jsonb(p)::text,'' ORDER BY id)) FROM payment_requests p WHERE id<>combined AND id NOT IN (SELECT id FROM expected_requests)),
  'row_content',(SELECT md5(string_agg((CASE WHEN x.request_id=req129 THEN to_jsonb(x)-'contact_id'-'contact_name'-'contact_phone' ELSE to_jsonb(x) END)::text,'' ORDER BY id)) FROM payment_request_rows x WHERE request_id<>combined)
 ) INTO baseline;
 INSERT INTO payment_requests(id,merchant_id,external_ref,title,mode,payment_count,created_by)
 VALUES(combined,merchant,'ТО21-23-09-2026','Толя 21–23.09.2026 · исторический объединённый для оплаты № 5','cards',6,chief);
 INSERT INTO payment_request_rows(id,request_id,row_no,card_id,contact_id,contact_name,contact_phone)
 SELECT gen_random_uuid(),combined,row_number() OVER (ORDER BY p.created_at,p.id,x.row_no),x.card_id,x.contact_id,x.contact_name,x.contact_phone
 FROM payment_request_rows x JOIN payment_requests p ON p.id=x.request_id WHERE p.id IN (SELECT id FROM expected_requests WHERE id<>req129);
 GET DIAGNOSTICS n=ROW_COUNT; IF n<>6 THEN RAISE EXCEPTION 'Expected six copied rows'; END IF;
 UPDATE payment_request_rows x SET contact_id=other.contact_id,contact_name=other.contact_name,contact_phone=other.contact_phone
 FROM contact_before own JOIN contact_before other ON other.id<>own.id WHERE x.id=own.id;
 GET DIAGNOSTICS n=ROW_COUNT; IF n<>2 THEN RAISE EXCEPTION 'Expected exactly two swapped contacts'; END IF;
 UPDATE payment_requests SET version=version+1 WHERE id=req129;
 UPDATE payment_requests SET deleted_at=now(),deleted_by=chief,version=version+1 WHERE id IN (SELECT id FROM expected_requests WHERE id<>req129);
 GET DIAGNOSTICS n=ROW_COUNT; IF n<>3 THEN RAISE EXCEPTION 'Expected three soft-deletions'; END IF;
 UPDATE registries SET payment_request_id=CASE id WHEN reg5 THEN combined ELSE req129 END,version=version+1 WHERE id IN (reg5,reg129);
 GET DIAGNOSTICS n=ROW_COUNT; IF n<>2 THEN RAISE EXCEPTION 'Expected two registry links'; END IF;
 IF EXISTS(SELECT 1 FROM registry_rows x JOIN registries r ON r.id=x.registry_id WHERE r.id IN (reg5,reg129)
    AND NOT EXISTS(SELECT 1 FROM payment_request_rows y WHERE y.request_id=r.payment_request_id AND y.card_id=x.card_id AND y.contact_name=x.raw->>2 AND y.contact_phone=x.raw->>3)) THEN RAISE EXCEPTION 'Final contacts do not match original payment evidence'; END IF;
 INSERT INTO audit_events(id,actor_id,actor_role,channel,action,object_type,object_id,outcome,reason,detail)
 VALUES(gen_random_uuid(),chief,'chief','owner_chat','payment_request_create','payment_request',combined,'success','Historical reconstruction explicitly requested by owner; not a new payment instruction.',jsonb_build_object('correction_key',batch_key,'historical_reconstruction',true,'registry_id',reg5,'source_request_ids',(SELECT jsonb_agg(id ORDER BY id) FROM expected_requests WHERE id<>req129),'card_count',6,'ledger_changed',false));
 INSERT INTO audit_events(id,actor_id,actor_role,channel,action,object_type,object_id,outcome,reason,detail)
 SELECT gen_random_uuid(),chief,'chief','owner_chat','payment_request_delete','payment_request',p.id,'success','Owner requested replacing the three original requests with one historical combined request.',jsonb_build_object('correction_key',batch_key,'superseded_by',combined,'prior_version',p.version,'version',p.version+1,'soft_delete',true,'rows_preserved',true) FROM expected_requests p WHERE id<>req129;
 INSERT INTO audit_events(id,actor_id,actor_role,channel,action,object_type,object_id,outcome,reason,detail)
 VALUES(gen_random_uuid(),chief,'chief','owner_chat','payment_request_update','payment_request',req129,'success','Owner confirmed actual card amounts and explicitly authorized swapping only request contacts.',jsonb_build_object('correction_key',batch_key,'prior_version',1,'version',2,'card_ids_unchanged',true,'ledger_changed',false,'source_rows_sha_before',(SELECT row_digest FROM expected_requests WHERE id=req129),'prior_contacts',(SELECT jsonb_agg(jsonb_build_object('row_id',id,'row_no',row_no,'card_id',card_id,'contact_id',contact_id) ORDER BY row_no) FROM contact_before),'updated_contacts',(SELECT jsonb_agg(jsonb_build_object('row_id',id,'row_no',row_no,'card_id',card_id,'contact_id',contact_id) ORDER BY row_no) FROM payment_request_rows WHERE request_id=req129)));
 INSERT INTO audit_events(id,actor_id,actor_role,channel,action,object_type,object_id,outcome,reason,detail)
 SELECT gen_random_uuid(),chief,'chief','owner_chat','registry_request_link','registry',r.id,'success','Owner approved resolution of the two remaining historical registry link exceptions.',jsonb_build_object('correction_key',batch_key,'registry_number',r.number,'prior_payment_request_id',NULL,'payment_request_id',r.payment_request_id,'prior_version',1,'version',2,'ledger_changed',false,'status_changed',false) FROM registries r WHERE id IN (reg5,reg129);
 SELECT jsonb_build_object('entries',pg_temp.history_digest('journal_entries'),'postings',pg_temp.history_digest('postings'),
  'sources',pg_temp.history_digest('source_documents'),'registry_rows',pg_temp.history_digest('registry_rows'),
  'registry_content',(SELECT md5(string_agg((to_jsonb(r)-'payment_request_id'-'version')::text,'' ORDER BY id)) FROM registries r),
  'other_registries',(SELECT md5(string_agg(to_jsonb(r)::text,'' ORDER BY id)) FROM registries r WHERE id NOT IN (reg5,reg129)),
  'request_content',(SELECT md5(string_agg((to_jsonb(p)-'version'-'deleted_at'-'deleted_by')::text,'' ORDER BY id)) FROM payment_requests p WHERE id<>combined),
  'other_requests',(SELECT md5(string_agg(to_jsonb(p)::text,'' ORDER BY id)) FROM payment_requests p WHERE id<>combined AND id NOT IN (SELECT id FROM expected_requests)),
  'row_content',(SELECT md5(string_agg((CASE WHEN x.request_id=req129 THEN to_jsonb(x)-'contact_id'-'contact_name'-'contact_phone' ELSE to_jsonb(x) END)::text,'' ORDER BY id)) FROM payment_request_rows x WHERE request_id<>combined)
 ) INTO after_state;
 IF baseline IS DISTINCT FROM after_state THEN RAISE EXCEPTION 'Unauthorized change to history or unrelated data'; END IF;
 IF EXISTS(SELECT 1 FROM postings GROUP BY entry_id HAVING sum(CASE side WHEN 'debit' THEN amount_cents ELSE -amount_cents END)<>0) THEN RAISE EXCEPTION 'Ledger imbalance'; END IF;
 IF (SELECT count(*) FROM audit_events WHERE detail->>'correction_key'=batch_key)<>7 THEN RAISE EXCEPTION 'Expected seven audit events'; END IF;
 RAISE NOTICE 'Combined request %, registry 5 linked, 3 source requests soft-deleted; registry 129 contacts swapped and linked; history digests unchanged: %',combined,after_state;
END $fix$;
COMMIT;
