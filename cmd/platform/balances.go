package main

import (
	"database/sql"
	"errors"
	"net/http"
	"time"
)

// balances returns every card and cash holder, including those with no postings.
// The single statement gives all rows the same PostgreSQL snapshot.
func (a *App) balances(w http.ResponseWriter, r *http.Request, u User) {
	if !a.require(w, u, "chief", "accountant", "auditor") {
		return
	}
	if r.Method != http.MethodGet {
		fail(w, http.StatusMethodNotAllowed, errors.New("method"))
		return
	}
	const query = `WITH card_balances AS (
		SELECT card_id, SUM(CASE WHEN side='debit' THEN amount_cents ELSE -amount_cents END) AS cents
		FROM postings WHERE account='1100' AND card_id IS NOT NULL GROUP BY card_id
	), cash_balances AS (
		SELECT custodian_id, account, SUM(CASE WHEN side='debit' THEN amount_cents ELSE -amount_cents END) AS cents
		FROM postings WHERE account IN ('1200','1210') AND custodian_id IS NOT NULL GROUP BY custodian_id,account
	)
	SELECT 'card',c.id::text,b.name,c.owner_label,c.mask,c.status,'',true,COALESCE(cb.cents,0),o.observed_cents,o.observed_at
	FROM cards c JOIN banks b ON b.id=c.bank_id LEFT JOIN card_balances cb ON cb.card_id=c.id
	LEFT JOIN LATERAL (SELECT observed_cents,observed_at FROM observations WHERE card_id=c.id ORDER BY observed_at DESC,id DESC LIMIT 1) o ON true
	UNION ALL
	SELECT 'custodian',x.id::text,x.name,'','','',x.kind,x.active,COALESCE(SUM(k.cents),0),NULL::bigint,NULL::timestamptz
	FROM custodians x LEFT JOIN cash_balances k ON k.custodian_id=x.id
		AND k.account=CASE WHEN x.kind='chief' THEN '1210' ELSE '1200' END
	GROUP BY x.id,x.name,x.kind,x.active
	ORDER BY 1,3,5,2`
	rows, err := a.db.QueryContext(r.Context(), query)
	if err != nil {
		fail(w, 500, err)
		return
	}
	defer rows.Close()
	cards, custodians := []M{}, []M{}
	var cardsTotal, collectorsTotal, chiefTotal, operatorsTotal int64
	for rows.Next() {
		var typ, id, name, owner, mask, status, kind string
		var active bool
		var cents int64
		var observed sql.NullInt64
		var observedAt sql.NullTime
		if err = rows.Scan(&typ, &id, &name, &owner, &mask, &status, &kind, &active, &cents, &observed, &observedAt); err != nil {
			fail(w, 500, err)
			return
		}
		if typ == "card" {
			card := M{"id": id, "bank": name, "owner": owner, "mask": mask, "status": status, "amount": rub(cents)}
			if observed.Valid && observedAt.Valid {
				card["observed"] = rub(observed.Int64)
				card["observed_at"] = observedAt.Time.Format(time.RFC3339)
			}
			cards = append(cards, card)
			cardsTotal += cents
			continue
		}
		custodians = append(custodians, M{"id": id, "name": name, "kind": kind, "active": active, "amount": rub(cents)})
		switch kind {
		case "chief":
			chiefTotal += cents
		case "collector":
			collectorsTotal += cents
		case "operator":
			operatorsTotal += cents
		}
	}
	if err = rows.Err(); err != nil {
		fail(w, 500, err)
		return
	}
	respond(w, 200, M{"cards": cards, "custodians": custodians, "totals": M{
		"cards": rub(cardsTotal), "collectors": rub(collectorsTotal), "chief": rub(chiefTotal),
		"operators": rub(operatorsTotal), "all": rub(cardsTotal + collectorsTotal + chiefTotal + operatorsTotal),
	}, "as_of": time.Now().UTC().Format(time.RFC3339)})
}
