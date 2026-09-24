package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

func (a *App) tariffData(tx *sql.Tx, merchant string, bp int, from string) (M, error) {
	rows, e := tx.Query("SELECT r.id,r.total_cents,r.commission_cents,r.manual_rate_bp,COALESCE(r.payment_date,(r.confirmed_at AT TIME ZONE 'Europe/Moscow')::date)::text,COALESCE((SELECT SUM(a.new_commission_cents-a.old_commission_cents) FROM tariff_adjustments a WHERE a.registry_id=r.id),0) FROM registries r WHERE r.merchant_id=$1 AND r.status='posted' AND COALESCE(r.payment_date,(r.confirmed_at AT TIME ZONE 'Europe/Moscow')::date) >= $2::date ORDER BY COALESCE(r.payment_date,(r.confirmed_at AT TIME ZONE 'Europe/Moscow')::date),r.id", merchant, from)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	items := []M{}
	var delta int64
	for rows.Next() {
		var rid string
		var total, original, adjustment int64
		var manual sql.NullInt64
		var paymentDate string
		if e = rows.Scan(&rid, &total, &original, &manual, &paymentDate, &adjustment); e != nil {
			return nil, e
		}
		if manual.Valid {
			items = append(items, M{"id": rid, "excluded": "manual_rate"})
			continue
		}
		old := original + adjustment
		newFee := fee(total, bp)
		delta += newFee - old
		items = append(items, M{"id": rid, "old_commission": rub(old), "new_commission": rub(newFee), "old_net": rub(total - old), "new_net": rub(total - newFee), "delta_cents": newFee - old, "month": paymentDate[:7]})
	}
	if e = rows.Err(); e != nil {
		return nil, e
	}
	rows.Close()
	debt, e := balance(tx, "2100", "merchant", merchant)
	if e != nil {
		return nil, e
	}
	receivable, e := balance(tx, "1300", "merchant", merchant)
	if e != nil {
		return nil, e
	}
	before := debt - receivable
	after := before - delta
	body := M{"merchant_id": merchant, "rate_bp": bp, "valid_from": from, "items": items, "delta_commission": rub(delta), "position_before": rub(before), "position_after": rub(after), "payable_after": rub(max64(after, 0)), "receivable_after": rub(max64(-after, 0))}
	h := sha256.Sum256(encode(body))
	body["preview_hash"] = hex.EncodeToString(h[:])
	return body, nil
}
func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
func tariffParams(m M) (int, error) {
	bp, e := strconv.Atoi(str(m, "rate_bp"))
	if e != nil || bp < 0 || bp > 10000 {
		return 0, errors.New("некорректная ставка")
	}
	if _, e = time.Parse("2006-01-02", str(m, "valid_from")); e != nil {
		return 0, errors.New("некорректная дата")
	}
	return bp, nil
}
func (a *App) tariffPreview(w http.ResponseWriter, r *http.Request, u User) {
	if r.Method != "POST" || !a.require(w, u, "chief") {
		return
	}
	m, e := jsonBody(r)
	if e != nil {
		fail(w, 400, e)
		return
	}
	bp, e := tariffParams(m)
	if e != nil {
		fail(w, 400, e)
		return
	}
	tx, e := a.tx()
	if e != nil {
		fail(w, 500, e)
		return
	}
	defer tx.Rollback()
	data, e := a.tariffData(tx, str(m, "merchant_id"), bp, str(m, "valid_from"))
	if e != nil {
		fail(w, 400, e)
		return
	}
	respond(w, 200, data)
}
func (a *App) tariffConfirm(w http.ResponseWriter, r *http.Request, u User) {
	if r.Method != "POST" || !a.require(w, u, "chief") {
		return
	}
	m, e := jsonBody(r)
	if e != nil {
		fail(w, 400, e)
		return
	}
	bp, e := tariffParams(m)
	if e != nil {
		fail(w, 400, e)
		return
	}
	tx, e := a.tx()
	if e != nil {
		fail(w, 500, e)
		return
	}
	defer tx.Rollback()
	var already bool
	if e = tx.QueryRow("SELECT EXISTS(SELECT 1 FROM tariff_confirmations WHERE preview_hash=$1)", str(m, "preview_hash")).Scan(&already); e != nil {
		fail(w, 500, e)
		return
	}
	if already {
		respond(w, 200, M{"status": "already_posted"})
		return
	}
	data, e := a.tariffData(tx, str(m, "merchant_id"), bp, str(m, "valid_from"))
	if e != nil {
		fail(w, 409, e)
		return
	}
	if str(m, "preview_hash") != data["preview_hash"] {
		fail(w, 409, errors.New("предпросмотр устарел"))
		return
	}
	merchant := str(m, "merchant_id")
	_, e = tx.Exec("INSERT INTO tariff_confirmations(preview_hash,merchant_id,rate_bp,valid_from,actor_id) VALUES($1,$2,$3,$4,$5)", str(m, "preview_hash"), merchant, bp, str(m, "valid_from"), u.ID)
	if e != nil {
		fail(w, 409, errors.New("перерасчёт уже подтверждён"))
		return
	}
	_, e = tx.Exec("UPDATE tariffs SET active=false WHERE merchant_id=$1 AND valid_from >= $2::date", merchant, str(m, "valid_from"))
	if e != nil {
		fail(w, 500, e)
		return
	}
	_, e = tx.Exec("UPDATE registries SET version=version+1 WHERE merchant_id=$1 AND status='preview' AND manual_rate_bp IS NULL", merchant)
	if e != nil {
		fail(w, 500, e)
		return
	}
	_, e = tx.Exec("INSERT INTO tariffs(id,merchant_id,rate_bp,valid_from,created_by) VALUES($1,$2,$3,$4,$5)", id(), merchant, bp, str(m, "valid_from"), u.ID)
	if e != nil {
		fail(w, 409, e)
		return
	}
	for _, raw := range data["items"].([]M) {
		if _, ok := raw["excluded"]; ok {
			continue
		}
		delta := int64(raw["delta_cents"].(int64))
		if delta == 0 {
			continue
		}
		rid := raw["id"].(string)
		oldC, _ := amount(raw["old_commission"].(string))
		newC, _ := amount(raw["new_commission"].(string))
		adj := id()
		_, e = tx.Exec("INSERT INTO tariff_adjustments(id,registry_id,old_commission_cents,new_commission_cents,rate_bp,reason) VALUES($1,$2,$3,$4,$5,'retroactive_tariff')", adj, rid, oldC, newC, bp)
		if e != nil {
			fail(w, 500, e)
			return
		}
		var recognized time.Time
		_ = tx.QueryRow("SELECT COALESCE(payment_date::timestamp AT TIME ZONE 'Europe/Moscow',confirmed_at) FROM registries WHERE id=$1", rid).Scan(&recognized)
		v := delta
		if v < 0 {
			v = -v
		}
		lines := []Posting{}
		if delta > 0 {
			lines = []Posting{{Account: "2100", Side: "debit", Amount: v, Merchant: merchant}, {Account: "4100", Side: "credit", Amount: v, Merchant: merchant}}
		} else {
			lines = []Posting{{Account: "4100", Side: "debit", Amount: v, Merchant: merchant}, {Account: "2100", Side: "credit", Amount: v, Merchant: merchant}}
		}
		if _, e = put(tx, "tariff_adjustment", adj, "tariff-adjustment:"+adj, u.ID, time.Now(), recognized, lines, ""); e != nil {
			fail(w, 409, e)
			return
		}
	}
	if e = normalizeMerchant(tx, merchant, u.ID, "tariff:"+str(m, "preview_hash")); e != nil {
		fail(w, 409, e)
		return
	}
	if e = txAudit(tx, u.ID, "web", "tariff_confirm", "merchant", merchant, "success", "", M{"rate_bp": bp, "valid_from": str(m, "valid_from"), "preview_hash": str(m, "preview_hash")}); e != nil {
		fail(w, 500, e)
		return
	}
	if e = tx.Commit(); e != nil {
		fail(w, 409, e)
		return
	}
	respond(w, 200, M{"status": "posted"})
}
func normalizeMerchant(tx *sql.Tx, merchant, actor, key string) error {
	debt, e := balance(tx, "2100", "merchant", merchant)
	if e != nil {
		return e
	}
	rec, e := balance(tx, "1300", "merchant", merchant)
	if e != nil {
		return e
	}
	var lines []Posting
	var v int64
	if debt < 0 {
		v = -debt
		lines = []Posting{{Account: "1300", Side: "debit", Amount: v, Merchant: merchant}, {Account: "2100", Side: "credit", Amount: v, Merchant: merchant}}
	} else if debt > 0 && rec > 0 {
		v = debt
		if rec < v {
			v = rec
		}
		lines = []Posting{{Account: "2100", Side: "debit", Amount: v, Merchant: merchant}, {Account: "1300", Side: "credit", Amount: v, Merchant: merchant}}
	}
	if v == 0 {
		return nil
	}
	_, e = put(tx, "merchant_reclass", id(), "reclass:"+key, actor, time.Now(), time.Now(), lines, "")
	return e
}
func (a *App) report(w http.ResponseWriter, r *http.Request, u User) {
	if !a.require(w, u, "chief", "accountant", "auditor") {
		return
	}
	if r.Method != "GET" {
		fail(w, 405, errors.New("method"))
		return
	}
	rows, e := a.db.Query("SELECT account,COALESCE(merchant_id::text,''),COALESCE(card_id::text,''),COALESCE(custodian_id::text,''),COALESCE(ref_id::text,''),SUM(CASE WHEN side='debit' THEN amount_cents ELSE -amount_cents END) FROM postings GROUP BY 1,2,3,4,5 ORDER BY 1,2,3,4,5")
	if e != nil {
		fail(w, 500, e)
		return
	}
	defer rows.Close()
	balances := []M{}
	totals := map[string]int64{}
	for rows.Next() {
		var acc, merchant, card, cust, ref string
		var value int64
		if e = rows.Scan(&acc, &merchant, &card, &cust, &ref, &value); e != nil {
			fail(w, 500, e)
			return
		}
		if acc == "2100" || acc == "2110" || acc == "2200" || acc == "2300" || acc == "4100" || acc == "4200" || acc == "3100" || acc == "3200" {
			value = -value
		}
		balances = append(balances, M{"account": acc, "merchant_id": merchant, "card_id": card, "custodian_id": cust, "ref_id": ref, "amount": rub(value)})
		totals[acc] += value
	}
	summary := M{"card_cash": rub(totals["1100"] + totals["1200"] + totals["1210"]), "merchant_payable": rub(totals["2100"]), "merchant_receivable": rub(totals["1300"]), "other_receivable": rub(totals["1400"] + totals["1390"]), "commission_revenue": rub(totals["4100"]), "other_revenue": rub(totals["4200"]), "expenses": rub(totals["5100"] + totals["5200"] + totals["5300"] + totals["5400"] + totals["5900"]), "profit": rub(totals["4100"] + totals["4200"] - totals["5100"] - totals["5200"] - totals["5300"] - totals["5400"] - totals["5900"])}
	assets := totals["1100"] + totals["1200"] + totals["1210"] + totals["1220"] + totals["1300"] + totals["1390"] + totals["1400"]
	liabilities := totals["2100"] + totals["2110"] + totals["2200"] + totals["2290"] + totals["2300"]
	equity := totals["3100"] + totals["3200"]
	profit := totals["4100"] + totals["4200"] - totals["5100"] - totals["5200"] - totals["5300"] - totals["5400"] - totals["5900"]
	summary["assets"] = rub(assets)
	summary["liabilities"] = rub(liabilities)
	summary["equity"] = rub(equity)
	summary["balance_difference"] = rub(assets - liabilities - equity - profit)
	month := r.URL.Query().Get("month")
	var monthly M
	if month != "" {
		monthly, e = a.monthReport(month)
		if e != nil {
			fail(w, 400, e)
			return
		}
	}
	cashflow := []M{}
	flowRows, e := a.db.Query("SELECT to_char((CASE WHEN j.event_type='registry' THEN j.occurred_at ELSE j.posted_at END) AT TIME ZONE 'Europe/Moscow','YYYY-MM-DD'),j.event_type,SUM(CASE WHEN p.side='debit' THEN p.amount_cents ELSE -p.amount_cents END) FROM postings p JOIN journal_entries j ON j.id=p.entry_id WHERE p.account IN ('1100','1200','1210') GROUP BY 1,2 ORDER BY 1 DESC,2 LIMIT 200")
	if e != nil {
		fail(w, 500, e)
		return
	}
	for flowRows.Next() {
		var day, kind string
		var v int64
		if e = flowRows.Scan(&day, &kind, &v); e != nil {
			flowRows.Close()
			fail(w, 500, e)
			return
		}
		cashflow = append(cashflow, M{"day": day, "event": kind, "net_cash": rub(v)})
	}
	flowRows.Close()
	observed := []M{}
	obsRows, e := a.db.Query("SELECT c.id,c.mask,o.observed_cents,o.observed_at,COALESCE((SELECT SUM(CASE WHEN p.side='debit' THEN p.amount_cents ELSE -p.amount_cents END) FROM postings p WHERE p.account='1100' AND p.card_id=c.id),0) FROM cards c JOIN LATERAL (SELECT observed_cents,observed_at FROM observations WHERE card_id=c.id ORDER BY observed_at DESC,id DESC LIMIT 1) o ON true ORDER BY c.mask")
	if e != nil {
		fail(w, 500, e)
		return
	}
	for obsRows.Next() {
		var card, mask string
		var amount, expected int64
		var at time.Time
		if e = obsRows.Scan(&card, &mask, &amount, &at, &expected); e != nil {
			obsRows.Close()
			fail(w, 500, e)
			return
		}
		observed = append(observed, M{"card_id": card, "mask": mask, "observed": rub(amount), "ledger": rub(expected), "difference": rub(amount - expected), "observed_at": at})
	}
	obsRows.Close()
	handoverDiffs := []M{}
	diffRows, e := a.db.Query("SELECT id,payload->>'from_custodian_id',(payload->>'amount_cents')::bigint,confirmed_amount_cents FROM drafts WHERE kind='handover' AND status='posted' AND confirmed_amount_cents<>(payload->>'amount_cents')::bigint ORDER BY confirmed_at DESC LIMIT 100")
	if e != nil {
		fail(w, 500, e)
		return
	}
	for diffRows.Next() {
		var event, cust string
		var claimed, received int64
		if e = diffRows.Scan(&event, &cust, &claimed, &received); e != nil {
			diffRows.Close()
			fail(w, 500, e)
			return
		}
		handoverDiffs = append(handoverDiffs, M{"id": event, "collector_id": cust, "claimed": rub(claimed), "received": rub(received), "difference": rub(claimed - received)})
	}
	diffRows.Close()
	respond(w, 200, M{"summary": summary, "balances": balances, "cashflow": cashflow, "observations": observed, "handover_differences": handoverDiffs, "month": monthly})
}
func (a *App) monthReport(month string) (M, error) {
	if _, e := time.Parse("2006-01", month); e != nil {
		return nil, e
	}
	rows, e := a.db.Query("SELECT p.account,COALESCE(SUM(CASE WHEN p.side='debit' THEN p.amount_cents ELSE -p.amount_cents END),0) FROM postings p JOIN journal_entries j ON j.id=p.entry_id WHERE to_char(j.recognition_at AT TIME ZONE 'Europe/Moscow','YYYY-MM')=$1 AND (p.account LIKE '4%' OR p.account LIKE '5%') GROUP BY p.account", month)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	m := map[string]int64{}
	for rows.Next() {
		var account string
		var v int64
		if e = rows.Scan(&account, &v); e != nil {
			return nil, e
		}
		if account[0] == '4' {
			v = -v
		}
		m[account] = v
	}
	revenue := m["4100"] + m["4200"]
	expenses := m["5100"] + m["5200"] + m["5300"] + m["5400"] + m["5900"]
	v := M{"month": month, "revenue": rub(revenue), "expenses": rub(expenses), "profit": rub(revenue - expenses)}
	h := sha256.Sum256(encode(v))
	v["digest"] = hex.EncodeToString(h[:])
	var approved bool
	_ = a.db.QueryRow("SELECT EXISTS(SELECT 1 FROM report_approvals WHERE month=$1::date AND digest=$2)", month+"-01", v["digest"]).Scan(&approved)
	v["approved"] = approved
	return v, nil
}
func (a *App) approveReport(w http.ResponseWriter, r *http.Request, u User) {
	if r.Method != "POST" || !a.require(w, u, "accountant") {
		return
	}
	m, e := jsonBody(r)
	if e != nil {
		fail(w, 400, e)
		return
	}
	data, e := a.monthReport(str(m, "month"))
	if e != nil {
		fail(w, 400, e)
		return
	}
	if str(m, "digest") != data["digest"] {
		fail(w, 409, errors.New("отчёт изменился"))
		return
	}
	_, e = a.db.Exec("INSERT INTO report_approvals(id,month,snapshot,digest,actor_id) VALUES($1,$2::date,$3,$4,$5) ON CONFLICT(month,digest) DO NOTHING", id(), str(m, "month")+"-01", encode(data), str(m, "digest"), u.ID)
	if e != nil {
		fail(w, 409, e)
		return
	}
	a.logAudit(u.ID, "web", "report_approve", "report", "", "success", "", M{"month": str(m, "month"), "digest": str(m, "digest")})
	respond(w, 200, M{"approved": true})
}

var _ = fmt.Sprint
var _ = json.Marshal
