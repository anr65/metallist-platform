package main

import (
	"errors"
	"net/http"
)

func (a *App) reverseDraft(w http.ResponseWriter, r *http.Request, u User) {
	if r.Method != "POST" || !a.require(w, u, "chief") {
		return
	}
	m, e := jsonBody(r)
	if e != nil {
		fail(w, 400, e)
		return
	}
	if str(m, "reason") == "" {
		fail(w, 400, errors.New("нужна причина"))
		return
	}
	tx, e := a.tx()
	if e != nil {
		fail(w, 500, e)
		return
	}
	defer tx.Rollback()
	var status, kind string
	var raw []byte
	e = tx.QueryRow("SELECT status,kind,payload FROM drafts WHERE id=$1 FOR UPDATE", str(m, "id")).Scan(&status, &kind, &raw)
	if e != nil {
		fail(w, 404, e)
		return
	}
	if status == "reversed" {
		respond(w, 200, M{"status": "already_reversed"})
		return
	}
	if status != "posted" {
		fail(w, 409, errors.New("операция не проведена"))
		return
	}
	if kind == "shortage" || kind == "writeoff" || kind == "surplus" {
		account, side := "", ""
		switch kind {
		case "shortage":
			account, side = "1400", "credit"
		case "writeoff":
			account, side = "4200", "credit"
		case "surplus":
			account, side = "2300", "debit"
		}
		var dependent bool
		e = tx.QueryRow("SELECT EXISTS(SELECT 1 FROM postings WHERE account=$1 AND side=$2 AND ref_id=$3)", account, side, str(m, "id")).Scan(&dependent)
		if e != nil {
			fail(w, 500, e)
			return
		}
		if dependent {
			fail(w, 409, errors.New("сначала исправьте связанные последующие операции"))
			return
		}
	}
	if kind == "surplus_merchant" {
		payload, _ := decodeMap(raw)
		var paid bool
		e = tx.QueryRow("SELECT EXISTS(SELECT 1 FROM postings WHERE account='2110' AND side='debit' AND ref_id=$1)", str(payload, "source_ref")).Scan(&paid)
		if e != nil {
			fail(w, 500, e)
			return
		}
		if paid {
			fail(w, 409, errors.New("сначала исправьте возврат найденных денег"))
			return
		}
	}
	var orig string
	e = tx.QueryRow("SELECT id FROM journal_entries WHERE event_type=$1 AND event_id=$2", kind, str(m, "id")).Scan(&orig)
	if e != nil {
		fail(w, 409, e)
		return
	}
	rows, e := tx.Query("SELECT account,COALESCE(card_id::text,''),COALESCE(custodian_id::text,''),amount_cents FROM postings WHERE entry_id=$1 AND side='debit' AND account IN ('1100','1200','1210')", orig)
	if e != nil {
		fail(w, 500, e)
		return
	}
	type cashLine struct {
		account, card, cust string
		v                   int64
	}
	var lines []cashLine
	for rows.Next() {
		var x cashLine
		if e = rows.Scan(&x.account, &x.card, &x.cust, &x.v); e != nil {
			rows.Close()
			fail(w, 500, e)
			return
		}
		lines = append(lines, x)
	}
	rows.Close()
	for _, x := range lines {
		account, card, cust, v := x.account, x.card, x.cust, x.v
		dim, key := "custodian", cust
		if card != "" {
			dim, key = "card", card
		}
		b, err := balance(tx, account, dim, key)
		if err != nil || b < v {
			fail(w, 409, errors.New("сторно сделает остаток денег отрицательным"))
			return
		}
	}
	if e = reverse(tx, orig, u.ID, "reverse-draft:"+str(m, "id")); e != nil {
		fail(w, 409, e)
		return
	}
	p, _ := decodeMap(raw)
	if merchant := str(p, "merchant_id"); merchant != "" {
		if e = normalizeMerchant(tx, merchant, u.ID, "reverse-draft:"+str(m, "id")); e != nil {
			fail(w, 409, e)
			return
		}
	}
	_, e = tx.Exec("UPDATE drafts SET status='reversed' WHERE id=$1", str(m, "id"))
	if e != nil {
		fail(w, 500, e)
		return
	}
	if e = txAudit(tx, u.ID, "web", "draft_reverse", kind, str(m, "id"), "success", str(m, "reason"), M{}); e != nil {
		fail(w, 500, e)
		return
	}
	if e = tx.Commit(); e != nil {
		fail(w, 409, e)
		return
	}
	respond(w, 200, M{"status": "reversed"})
}
