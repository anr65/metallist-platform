package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"time"
)

func (a *App) manualData(tx *sql.Tx, reg string, bp int, reason string) (M, error) {
	var merchant, status string
	var gross, original int64
	var manual sql.NullInt64
	var confirmed time.Time
	e := tx.QueryRow("SELECT merchant_id,status,total_cents,commission_cents,manual_rate_bp,confirmed_at FROM registries WHERE id=$1", reg).Scan(&merchant, &status, &gross, &original, &manual, &confirmed)
	if e != nil {
		return nil, e
	}
	if status != "posted" || !manual.Valid {
		return nil, errors.New("нет подтверждённой разовой ставки")
	}
	var adjustments int64
	e = tx.QueryRow("SELECT COALESCE(SUM(new_commission_cents-old_commission_cents),0) FROM tariff_adjustments WHERE registry_id=$1", reg).Scan(&adjustments)
	if e != nil {
		return nil, e
	}
	old := original + adjustments
	next := fee(gross, bp)
	payable, e := balance(tx, "2100", "merchant", merchant)
	if e != nil {
		return nil, e
	}
	receivable, e := balance(tx, "1300", "merchant", merchant)
	if e != nil {
		return nil, e
	}
	before := payable - receivable
	after := before - (next - old)
	body := M{"registry_id": reg, "merchant_id": merchant, "rate_bp": bp, "reason": reason, "gross": rub(gross), "old_commission": rub(old), "new_commission": rub(next), "old_net": rub(gross - old), "new_net": rub(gross - next), "position_before": rub(before), "position_after": rub(after), "payable_after": rub(max64(after, 0)), "receivable_after": rub(max64(-after, 0))}
	h := sha256.Sum256(encode(body))
	body["preview_hash"] = hex.EncodeToString(h[:])
	return body, nil
}
func manualParams(m M) (int, error) {
	bp, e := strconv.Atoi(str(m, "rate_bp"))
	if e != nil || bp < 0 || bp > 10000 || str(m, "reason") == "" {
		return 0, errors.New("ставка и причина обязательны")
	}
	return bp, nil
}
func (a *App) manualPreview(w http.ResponseWriter, r *http.Request, u User) {
	if r.Method != "POST" || !a.require(w, u, "chief") {
		return
	}
	m, e := jsonBody(r)
	if e != nil {
		fail(w, 400, e)
		return
	}
	bp, e := manualParams(m)
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
	data, e := a.manualData(tx, str(m, "registry_id"), bp, str(m, "reason"))
	if e != nil {
		fail(w, 409, e)
		return
	}
	respond(w, 200, data)
}
func (a *App) manualConfirm(w http.ResponseWriter, r *http.Request, u User) {
	if r.Method != "POST" || !a.require(w, u, "chief") {
		return
	}
	m, e := jsonBody(r)
	if e != nil {
		fail(w, 400, e)
		return
	}
	bp, e := manualParams(m)
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
	if e = tx.QueryRow("SELECT EXISTS(SELECT 1 FROM manual_rate_confirmations WHERE preview_hash=$1)", str(m, "preview_hash")).Scan(&already); e != nil {
		fail(w, 500, e)
		return
	}
	if already {
		respond(w, 200, M{"status": "already_posted"})
		return
	}
	reg := str(m, "registry_id")
	data, e := a.manualData(tx, reg, bp, str(m, "reason"))
	if e != nil {
		fail(w, 409, e)
		return
	}
	if data["preview_hash"] != str(m, "preview_hash") {
		fail(w, 409, errors.New("предпросмотр устарел"))
		return
	}
	_, e = tx.Exec("INSERT INTO manual_rate_confirmations(preview_hash,registry_id,rate_bp,reason,actor_id) VALUES($1,$2,$3,$4,$5)", str(m, "preview_hash"), reg, bp, str(m, "reason"), u.ID)
	if e != nil {
		fail(w, 409, errors.New("исправление уже подтверждено"))
		return
	}
	old, _ := amount(data["old_commission"].(string))
	next, _ := amount(data["new_commission"].(string))
	delta := next - old
	if delta != 0 {
		adj := id()
		_, e = tx.Exec("INSERT INTO tariff_adjustments(id,registry_id,old_commission_cents,new_commission_cents,rate_bp,reason) VALUES($1,$2,$3,$4,$5,'manual_rate_correction')", adj, reg, old, next, bp)
		if e != nil {
			fail(w, 409, e)
			return
		}
		v := delta
		if v < 0 {
			v = -v
		}
		merchant := data["merchant_id"].(string)
		lines := []Posting{}
		if delta > 0 {
			lines = []Posting{{Account: "2100", Side: "debit", Amount: v, Merchant: merchant}, {Account: "4100", Side: "credit", Amount: v, Merchant: merchant}}
		} else {
			lines = []Posting{{Account: "4100", Side: "debit", Amount: v, Merchant: merchant}, {Account: "2100", Side: "credit", Amount: v, Merchant: merchant}}
		}
		var confirmed time.Time
		_ = tx.QueryRow("SELECT confirmed_at FROM registries WHERE id=$1", reg).Scan(&confirmed)
		if _, e = put(tx, "tariff_adjustment", adj, "manual-adjustment:"+adj, u.ID, time.Now(), confirmed, lines, ""); e != nil {
			fail(w, 409, e)
			return
		}
		if e = normalizeMerchant(tx, merchant, u.ID, "manual:"+str(m, "preview_hash")); e != nil {
			fail(w, 409, e)
			return
		}
	}
	if e = txAudit(tx, u.ID, "web", "manual_rate_correction", "registry", reg, "success", str(m, "reason"), M{"rate_bp": bp, "preview_hash": str(m, "preview_hash")}); e != nil {
		fail(w, 500, e)
		return
	}
	if e = tx.Commit(); e != nil {
		fail(w, 409, e)
		return
	}
	respond(w, 200, M{"status": "posted"})
}
