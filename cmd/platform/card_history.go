package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"time"
)

var cardHistoryUUID = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func (a *App) cardHistory(w http.ResponseWriter, r *http.Request, u User) {
	if !a.require(w, u, "chief", "accountant", "auditor") {
		return
	}
	if r.Method != http.MethodGet {
		fail(w, 405, errors.New("method"))
		return
	}
	v := r.URL.Query()
	card := v.Get("card_id")
	page := 1
	var err error
	if v.Get("page") != "" {
		page, err = strconv.Atoi(v.Get("page"))
	}
	scope := v.Get("scope")
	if !cardHistoryUUID.MatchString(card) || err != nil || page < 1 || page > 10000 || (scope != "" && scope != "ledger" && scope != "draft" && scope != "registry" && scope != "observation") {
		fail(w, 400, errors.New("неверные параметры истории карты"))
		return
	}
	for _, key := range []string{"date_from", "date_to"} {
		if raw := v.Get(key); raw != "" {
			if _, err = time.Parse("2006-01-02", raw); err != nil {
				fail(w, 400, errors.New("неверная дата"))
				return
			}
		}
	}
	if v.Get("date_from") != "" && v.Get("date_to") != "" && v.Get("date_from") > v.Get("date_to") {
		fail(w, 400, errors.New("начало периода позже окончания"))
		return
	}
	var raw []byte
	err = a.db.QueryRowContext(r.Context(), cardHistorySQL, card, scope, v.Get("date_from"), v.Get("date_to"), (page-1)*15).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		fail(w, 404, errors.New("карта не найдена"))
		return
	}
	if err != nil {
		fail(w, 500, err)
		return
	}
	respond(w, 200, json.RawMessage(raw))
}

// All sources and the current balance share one statement snapshot. Running
// balances precede filters and pagination; only account 1100 changes card cash.
const cardHistorySQL = `WITH group_members AS (
 SELECT member.card_id FROM card_balance_groups selected JOIN card_balance_groups member ON member.balance_card_id=selected.balance_card_id WHERE selected.card_id=$1::uuid
), family AS (
 SELECT c.id,c.mask FROM cards c JOIN group_members g ON g.card_id=c.id
), movements AS (
 SELECT e.id,e.event_type,e.event_id,e.occurred_at,e.posted_at,e.actor_id,e.reversal_of,
 sum(CASE WHEN p.side='debit' THEN p.amount_cents ELSE 0 END) AS incoming,
 sum(CASE WHEN p.side='credit' THEN p.amount_cents ELSE 0 END) AS outgoing
 FROM journal_entries e JOIN postings p ON p.entry_id=e.id
 WHERE p.card_id IN (SELECT card_id FROM group_members) AND p.account='1100'
 GROUP BY e.id
), running AS (
 SELECT *,sum(incoming-outgoing) OVER (ORDER BY occurred_at,posted_at,id ROWS UNBOUNDED PRECEDING) AS balance
 FROM movements
), history AS (
 SELECT l.id::text AS id,l.occurred_at,l.posted_at AS recorded_at,'ledger' AS scope,
 jsonb_build_object('id',l.id,'scope','ledger','kind',l.event_type,
 'status',CASE WHEN l.reversal_of IS NOT NULL THEN 'reversal' WHEN EXISTS(SELECT 1 FROM journal_entries x WHERE x.reversal_of=l.id) THEN 'reversed' ELSE 'posted' END,
 'occurred_at',l.occurred_at,'recorded_at',l.posted_at,'actor',u.name,'creator',COALESCE(creator.name,registry_creator.name,u.name),
 'cards',(SELECT string_agg(DISTINCT cc.mask,', ' ORDER BY cc.mask) FROM postings pp JOIN cards cc ON cc.id=pp.card_id WHERE pp.entry_id=l.id AND pp.account='1100' AND pp.card_id IN (SELECT id FROM family)),
 'income',(l.incoming/100.0)::numeric(24,2)::text,'expense',(l.outgoing/100.0)::numeric(24,2)::text,'balance',(l.balance/100.0)::numeric(24,2)::text,
 'reversal_of',l.reversal_of,'document_id',COALESCE(r.id,d.id),'document_kind',CASE WHEN r.id IS NOT NULL THEN 'registry' WHEN d.id IS NOT NULL THEN 'draft' END,
 'reference',CASE WHEN r.id IS NOT NULL THEN 'Реестр № '||r.number::text||' / '||r.external_ref ELSE '' END,
 'reason',concat_ws('; ',NULLIF(d.payload->>'comment',''),NULLIF(d.payload->>'reason',''),NULLIF(audit.reason,'')),
 'details',jsonb_build_object(
 'category',COALESCE(d.payload->>'category',(SELECT min(category) FROM postings WHERE entry_id=l.id)),
 'channel',CASE WHEN r.id IS NOT NULL THEN 'Реестр' WHEN d.payload->>'telegram_confirmation_required'='true' THEN 'Telegram' WHEN d.id IS NOT NULL THEN 'Кабинет' ELSE 'Учётная запись' END,
 'postings',(SELECT jsonb_agg(jsonb_build_object('account',p.account,'side',p.side,'amount',(p.amount_cents/100.0)::numeric(24,2)::text,'merchant',m.name,'custodian',c.name,'card',cc.mask,'category',p.category) ORDER BY p.account,p.side,p.id) FROM postings p LEFT JOIN merchants m ON m.id=p.merchant_id LEFT JOIN custodians c ON c.id=p.custodian_id LEFT JOIN cards cc ON cc.id=p.card_id WHERE p.entry_id=l.id),
 'rows',(SELECT jsonb_agg(jsonb_build_object('row',rr.row_no,'amount',(rr.amount_cents/100.0)::numeric(24,2)::text,'rrn',rr.rrn,'order',rr.external_order) ORDER BY rr.row_no) FROM registry_rows rr WHERE rr.registry_id=r.id AND rr.card_id IN (SELECT card_id FROM group_members)),
 'merchant',merchant.name)) AS record
 FROM running l JOIN users u ON u.id=l.actor_id
 LEFT JOIN journal_entries original ON original.id=l.reversal_of
 LEFT JOIN registries r ON r.id=COALESCE(original.event_id,l.event_id) AND COALESCE(original.event_type,l.event_type)='registry'
 LEFT JOIN merchants merchant ON merchant.id=r.merchant_id
 LEFT JOIN source_documents source ON source.id=r.source_id
 LEFT JOIN users registry_creator ON registry_creator.id=source.uploader_id
 LEFT JOIN drafts d ON d.id=COALESCE(original.event_id,l.event_id) AND COALESCE(original.event_type,l.event_type)=d.kind
 LEFT JOIN users creator ON creator.id=d.created_by
 LEFT JOIN LATERAL (SELECT reason FROM audit_events WHERE outcome='success' AND object_id IN (l.id,l.event_id,original.id,original.event_id,r.id,d.id) AND reason<>'' AND (l.reversal_of IS NOT NULL OR action NOT LIKE '%revers%') ORDER BY at DESC,id DESC LIMIT 1) audit ON true
 UNION ALL
 SELECT d.id::text,COALESCE((NULLIF(d.payload->>'date','')::date::timestamp AT TIME ZONE 'Europe/Moscow'),d.created_at),d.created_at,'draft',
 jsonb_build_object('id',d.id,'scope','draft','kind',d.kind,'status',d.status,'occurred_at',COALESCE((NULLIF(d.payload->>'date','')::date::timestamp AT TIME ZONE 'Europe/Moscow'),d.created_at),'recorded_at',d.created_at,'creator',u.name,'actor',confirmer.name,
 'amount',((CASE WHEN batch.cents IS NOT NULL THEN batch.cents ELSE COALESCE((d.payload->>'amount_cents')::numeric,0) END)/100.0)::numeric(24,2)::text,
 'cards',(SELECT string_agg(mask,', ' ORDER BY mask) FROM family WHERE id::text IN (d.payload->>'card_id',d.payload->>'source_id',d.payload->>'destination_id') OR id::text IN (SELECT item->>'card_id' FROM jsonb_array_elements(CASE WHEN jsonb_typeof(d.payload->'withdrawal_items')='array' THEN d.payload->'withdrawal_items' ELSE '[]'::jsonb END || CASE WHEN jsonb_typeof(d.payload->'expense_items')='array' THEN d.payload->'expense_items' ELSE '[]'::jsonb END) item)),
 'document_id',d.id,'document_kind','draft','reason',concat_ws('; ',NULLIF(d.payload->>'comment',''),NULLIF(d.payload->>'reason','')),
 'details',jsonb_build_object('custodian',cust.name,'merchant',merchant.name,'category',d.payload->>'category'))
 FROM drafts d JOIN users u ON u.id=d.created_by LEFT JOIN users confirmer ON confirmer.id=d.confirmed_by
 LEFT JOIN custodians cust ON cust.id::text=COALESCE(d.payload->>'custodian_id',d.payload->>'to_custodian_id')
 LEFT JOIN merchants merchant ON merchant.id::text=d.payload->>'merchant_id'
 LEFT JOIN LATERAL (SELECT sum((item->>'amount_cents')::numeric) AS cents FROM jsonb_array_elements(CASE WHEN jsonb_typeof(d.payload->'withdrawal_items')='array' THEN d.payload->'withdrawal_items' ELSE '[]'::jsonb END || CASE WHEN jsonb_typeof(d.payload->'expense_items')='array' THEN d.payload->'expense_items' ELSE '[]'::jsonb END) item WHERE item->>'card_id' IN (SELECT card_id::text FROM group_members)) batch ON true
 WHERE d.status IN ('draft','rejected') AND (d.payload->>'card_id' IN (SELECT card_id::text FROM group_members) OR (d.payload->>'source_kind'='card' AND d.payload->>'source_id' IN (SELECT card_id::text FROM group_members)) OR (d.payload->>'destination_kind'='card' AND d.payload->>'destination_id' IN (SELECT card_id::text FROM group_members)) OR batch.cents IS NOT NULL)
 UNION ALL
 SELECT rr.id::text,COALESCE(r.payment_date::timestamp AT TIME ZONE 'Europe/Moscow',r.created_at),r.created_at,'registry',
 jsonb_build_object('id',rr.id,'scope','registry','kind','registry','status',r.status,'occurred_at',COALESCE(r.payment_date::timestamp AT TIME ZONE 'Europe/Moscow',r.created_at),'recorded_at',r.created_at,'creator',u.name,'amount',(rr.amount_cents/100.0)::numeric(24,2)::text,
 'cards',(SELECT mask FROM cards WHERE id=rr.card_id),'document_id',r.id,'document_kind','registry','reference','Реестр № '||r.number::text||' / '||r.external_ref,'reason',CASE WHEN rr.error_code IS NOT NULL AND rr.error_code<>'' THEN 'Строка содержит ошибку' ELSE '' END,
 'details',jsonb_build_object('merchant',m.name,'rows',jsonb_build_array(jsonb_build_object('row',rr.row_no,'amount',(rr.amount_cents/100.0)::numeric(24,2)::text,'rrn',rr.rrn,'order',rr.external_order))))
 FROM registry_rows rr JOIN registries r ON r.id=rr.registry_id JOIN merchants m ON m.id=r.merchant_id JOIN source_documents s ON s.id=r.source_id JOIN users u ON u.id=s.uploader_id
 WHERE rr.card_id IN (SELECT card_id FROM group_members) AND r.status IN ('preview','deleted')
 UNION ALL
 SELECT o.id::text,o.observed_at,o.created_at,'observation',
 jsonb_build_object('id',o.id,'scope','observation','kind','observation','status','observation','occurred_at',o.observed_at,'recorded_at',o.created_at,'actor',u.name,'cards',(SELECT mask FROM cards WHERE id=o.card_id),'observed',(o.observed_cents/100.0)::numeric(24,2)::text,'reference',CASE WHEN o.source LIKE 'telegram:%' THEN 'Telegram' WHEN o.source='web' THEN 'Кабинет' ELSE 'Наблюдение остатка' END,'details',jsonb_build_object('source',o.source))
 FROM observations o JOIN users u ON u.id=o.reporter_id WHERE o.card_id IN (SELECT card_id FROM group_members)
), filtered AS (
 SELECT * FROM history WHERE ($2='' OR scope=$2)
 AND ($3='' OR to_char(occurred_at AT TIME ZONE 'Europe/Moscow','YYYY-MM-DD') >= $3)
 AND ($4='' OR to_char(occurred_at AT TIME ZONE 'Europe/Moscow','YYYY-MM-DD') <= $4)
), page AS (SELECT * FROM filtered ORDER BY occurred_at DESC,recorded_at DESC,id DESC LIMIT 16 OFFSET $5)
SELECT jsonb_build_object('card',jsonb_build_object('id',c.id,'mask',c.mask,'bank',b.name,'owner',c.owner_label,'status',c.status,'shared',(SELECT count(*)>1 FROM family)),
 'balance',(COALESCE((SELECT sum(incoming-outgoing) FROM movements),0)/100.0)::numeric(24,2)::text,
 'items',COALESCE((SELECT jsonb_agg(record ORDER BY occurred_at DESC,recorded_at DESC,id DESC) FROM (SELECT * FROM page ORDER BY occurred_at DESC,recorded_at DESC,id DESC LIMIT 15) visible),'[]'::jsonb),
 'has_more',(SELECT count(*)>15 FROM page))
FROM cards c JOIN banks b ON b.id=c.bank_id WHERE c.id=$1::uuid`
