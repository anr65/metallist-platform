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
	const query = `WITH cash_balances AS (
		SELECT custodian_id, account, SUM(CASE WHEN side='debit' THEN amount_cents ELSE -amount_cents END) AS cents
		FROM postings WHERE account IN ('1200','1210') AND custodian_id IS NOT NULL GROUP BY custodian_id,account
	)
	SELECT 'card',c.id::text,b.name,c.owner_label,c.mask,c.status,'',true,COALESCE(cb.amount_cents,0),o.observed_cents,o.observed_at,cb.balance_card_id::text,COALESCE(pc.mask,'')
	FROM cards c JOIN banks b ON b.id=c.bank_id LEFT JOIN shared_card_balances cb ON cb.card_id=c.id LEFT JOIN cards pc ON pc.id=c.physical_card_id
	LEFT JOIN shared_card_observations o ON o.card_id=c.id
	UNION ALL
	SELECT 'custodian',x.id::text,x.name,'','','',x.kind,x.active,COALESCE(SUM(k.cents),0),NULL::bigint,NULL::timestamptz,'',''
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
	var cardsObservedTotal int64
	var cardsObservedCount, cardGroupCount int
	for rows.Next() {
		var typ, id, name, owner, mask, status, kind, balanceCardID, physicalMask string
		var active bool
		var cents int64
		var observed sql.NullInt64
		var observedAt sql.NullTime
		if err = rows.Scan(&typ, &id, &name, &owner, &mask, &status, &kind, &active, &cents, &observed, &observedAt, &balanceCardID, &physicalMask); err != nil {
			fail(w, 500, err)
			return
		}
		if typ == "card" {
			card := M{"id": id, "bank": name, "owner": owner, "mask": mask, "status": status, "amount": rub(cents), "balance_card_id": balanceCardID, "physical_card_mask": physicalMask}
			if observed.Valid && observedAt.Valid {
				card["observed"] = rub(observed.Int64)
				card["observed_at"] = observedAt.Time.Format(time.RFC3339)
				if id == balanceCardID {
					cardsObservedTotal += observed.Int64
					cardsObservedCount++
				}
			}
			cards = append(cards, card)
			if id == balanceCardID {
				cardsTotal += cents
				cardGroupCount++
			}
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
	var cardsObserved any
	if cardsObservedCount > 0 {
		cardsObserved = rub(cardsObservedTotal)
	}
	respond(w, 200, M{"cards": cards, "custodians": custodians, "totals": M{
		"card_balance_groups": cardGroupCount, "cards_observed": cardsObserved, "cards_observed_count": cardsObservedCount,
		"cards": rub(cardsTotal), "collectors": rub(collectorsTotal), "chief": rub(chiefTotal),
		"operators": rub(operatorsTotal), "all": rub(cardsTotal + collectorsTotal + chiefTotal + operatorsTotal),
	}, "as_of": time.Now().UTC().Format(time.RFC3339)})
}
