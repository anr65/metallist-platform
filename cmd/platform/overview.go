package main

import (
	"database/sql"
	"errors"
	"net/http"
	"time"
)

// overview reads one consistent snapshot. It never posts or changes workflow state.
func (a *App) overview(w http.ResponseWriter, r *http.Request, u User) {
	if !a.require(w, u, "chief", "accountant", "auditor") {
		return
	}
	if r.Method != http.MethodGet {
		fail(w, 405, errors.New("method"))
		return
	}
	data, err := a.overviewData(r)
	if err != nil {
		fail(w, 500, err)
		return
	}
	respond(w, 200, data)
}

func (a *App) overviewData(r *http.Request) (M, error) {
	tx, err := a.db.BeginTx(r.Context(), &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	result := M{}
	totals := M{}
	months := []M{}
	activity := M{}
	pending := M{}
	var asOf time.Time
	var first, last sql.NullString
	err = tx.QueryRowContext(r.Context(), `SELECT now(),
 to_char(min(recognition_at) AT TIME ZONE 'Europe/Moscow','YYYY-MM-DD'),
 to_char(max(recognition_at) AT TIME ZONE 'Europe/Moscow','YYYY-MM-DD') FROM journal_entries`).Scan(&asOf, &first, &last)
	if err != nil {
		return nil, err
	}
	result["as_of"] = asOf.In(a.location)
	result["first_day"] = nil
	result["last_day"] = nil
	if first.Valid {
		result["first_day"] = first.String
		result["last_day"] = last.String
	}
	rows, err := tx.QueryContext(r.Context(), `SELECT account,sum(CASE WHEN side='debit' THEN amount_cents ELSE -amount_cents END)::bigint FROM postings GROUP BY account`)
	if err != nil {
		return nil, err
	}
	accounts := map[string]int64{}
	for rows.Next() {
		var acc string
		var amount int64
		if err = rows.Scan(&acc, &amount); err != nil {
			rows.Close()
			return nil, err
		}
		accounts[acc] = amount
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	totals["cards"] = rub(accounts["1100"])
	totals["cash"] = rub(accounts["1200"])
	totals["chief_cash"] = rub(accounts["1210"])
	totals["merchant_payable"] = rub(-accounts["2100"] - accounts["2110"])
	totals["merchant_receivable"] = rub(accounts["1300"])
	totals["other_receivable"] = rub(accounts["1390"] + accounts["1400"])
	// Recognition date also keeps retroactive tariff adjustments and reversals in their original period.
	rows, err = tx.QueryContext(r.Context(), `WITH monthly AS (
 SELECT date_trunc('month',j.recognition_at AT TIME ZONE 'Europe/Moscow') AS month,
 sum(CASE WHEN p.account='1100' AND coalesce(original.event_type,j.event_type)='registry' THEN CASE WHEN p.side='debit' THEN p.amount_cents ELSE -p.amount_cents END ELSE 0 END)::bigint AS turnover,
 sum(CASE WHEN p.account='4100' THEN CASE WHEN p.side='credit' THEN p.amount_cents ELSE -p.amount_cents END ELSE 0 END)::bigint AS commission,
 sum(CASE WHEN p.account='4200' THEN CASE WHEN p.side='credit' THEN p.amount_cents ELSE -p.amount_cents END ELSE 0 END)::bigint AS other_revenue,
 sum(CASE WHEN p.account IN ('5100','5200','5300','5400','5900') THEN CASE WHEN p.side='debit' THEN p.amount_cents ELSE -p.amount_cents END ELSE 0 END)::bigint AS expenses
 FROM journal_entries j JOIN postings p ON p.entry_id=j.id LEFT JOIN journal_entries original ON original.id=j.reversal_of GROUP BY 1
 ), bounds AS (SELECT date_trunc('month',min(recognition_at) AT TIME ZONE 'Europe/Moscow') AS start, date_trunc('month',max(recognition_at) AT TIME ZONE 'Europe/Moscow') AS finish FROM journal_entries)
 SELECT to_char(s.month,'YYYY-MM'),coalesce(m.turnover,0),coalesce(m.commission,0),coalesce(m.other_revenue,0),coalesce(m.expenses,0)
 FROM bounds CROSS JOIN LATERAL generate_series(bounds.start,greatest(bounds.finish,date_trunc('month',now() AT TIME ZONE 'Europe/Moscow')),'1 month') s(month)
 LEFT JOIN monthly m ON m.month=s.month ORDER BY s.month`)
	if err != nil {
		return nil, err
	}
	var turnover, commission, other, expenses int64
	for rows.Next() {
		var month string
		var t, c, o, e int64
		if err = rows.Scan(&month, &t, &c, &o, &e); err != nil {
			rows.Close()
			return nil, err
		}
		months = append(months, M{"month": month, "turnover": rub(t), "commission": rub(c), "other_revenue": rub(o), "expenses": rub(e), "profit": rub(c + o - e)})
		turnover += t
		commission += c
		other += o
		expenses += e
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	totals["turnover"] = rub(turnover)
	totals["commission"] = rub(commission)
	totals["other_revenue"] = rub(other)
	totals["expenses"] = rub(expenses)
	totals["profit"] = rub(commission + other - expenses)
	var registries, merchants, cards, waitingRegistries, operations int64
	err = tx.QueryRowContext(r.Context(), `SELECT count(*),count(distinct merchant_id) FROM registries WHERE status='posted'`).Scan(&registries, &merchants)
	if err != nil {
		return nil, err
	}
	err = tx.QueryRowContext(r.Context(), `SELECT count(distinct rr.card_id) FROM registry_rows rr JOIN registries r ON r.id=rr.registry_id WHERE r.status='posted'`).Scan(&cards)
	if err != nil {
		return nil, err
	}
	err = tx.QueryRowContext(r.Context(), `SELECT (SELECT count(*) FROM registries WHERE status='preview'),(SELECT count(*) FROM drafts WHERE status='draft')`).Scan(&waitingRegistries, &operations)
	if err != nil {
		return nil, err
	}
	activity["registries"] = registries
	activity["merchants"] = merchants
	activity["cards"] = cards
	pending["registries"] = waitingRegistries
	pending["operations"] = operations
	result["totals"] = totals
	result["months"] = months
	result["activity"] = activity
	result["pending"] = pending
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}
