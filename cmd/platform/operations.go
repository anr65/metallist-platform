package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func valIf(ok bool, a, b string) string {
	if ok {
		return a
	}
	return b
}

func expenseAccount(category string) (string, error) {
	switch category {
	case "agent_fee":
		return "5200", nil
	case "bank_fee":
		return "5300", nil
	case "other":
		return "5900", nil
	case "salary", "warmup", "it_infrastructure", "taxes", "communication", "delivery", "operating", "transport":
		return "5100", nil
	default:
		return "", errors.New("для этого типа расхода нет утверждённого способа учёта")
	}
}

func (a *App) draft(w http.ResponseWriter, r *http.Request, u User) {
	if r.Method != "POST" || !a.require(w, u, "chief", "operator") {
		return
	}
	m, e := jsonBody(r)
	if e != nil {
		fail(w, 400, e)
		return
	}
	kind := str(m, "kind")
	allowed := map[string]bool{"withdrawal": true, "handover": true, "transfer": true, "repayment": true, "expense": true, "injection": true, "shortage": true, "writeoff": true, "surplus": true, "surplus_income": true, "surplus_merchant": true, "surplus_shortage": true, "surplus_return": true, "collection": true, "recovery": true, "forgive_injection": true}
	if !allowed[kind] {
		fail(w, 400, errors.New("неизвестный тип"))
		return
	}
	if u.Role == "operator" && kind != "withdrawal" && kind != "handover" && kind != "transfer" {
		a.logAudit(u.ID, "web", "draft_create", kind, "", "rejected", "role", M{})
		fail(w, 403, errors.New("только главный администратор"))
		return
	}
	if (kind == "shortage" || kind == "writeoff" || kind == "surplus_income" || kind == "surplus_merchant" || kind == "surplus_shortage" || kind == "recovery") && str(m, "reason") == "" {
		fail(w, 400, errors.New("нужно основание"))
		return
	}
	if kind == "transfer" {
		from, to := str(m, "from_custodian_id"), str(m, "to_custodian_id")
		if from == "" || to == "" || from == to {
			fail(w, 400, errors.New("выберите разных сборщиков"))
			return
		}
		var activeCount int
		if e = a.db.QueryRow("SELECT count(*) FROM custodians WHERE id IN ($1,$2) AND active AND kind='collector'", from, to).Scan(&activeCount); e != nil || activeCount != 2 {
			fail(w, 400, errors.New("выберите активных сборщиков"))
			return
		}
	}
	v, e := amount(str(m, "amount"))
	if e != nil {
		fail(w, 400, e)
		return
	}
	if kind == "expense" {
		if _, err := expenseAccount(str(m, "category")); err != nil {
			fail(w, 400, err)
			return
		}
		date := str(m, "date")
		if date == "" {
			date = time.Now().In(a.location).Format("2006-01-02")
		}
		if _, err := time.ParseInLocation("2006-01-02", date, a.location); err != nil {
			fail(w, 400, errors.New("неверная дата расхода"))
			return
		}
		m["date"] = date
	}
	m["amount_cents"] = v
	newID := id()
	m["draft_id"] = newID
	if kind == "surplus" {
		m["source_ref"] = newID
	}
	key := str(m, "idempotency_key")
	if key == "" {
		key = newID
	}
	_, e = a.db.Exec("INSERT INTO drafts(id,kind,payload,created_by,idempotency_key) VALUES($1,$2,$3,$4,$5)", newID, kind, encode(m), u.ID, key)
	if e != nil {
		fail(w, 409, e)
		return
	}
	a.logAudit(u.ID, "web", "draft_create", kind, newID, "success", "", M{})
	respond(w, 201, M{"id": newID})
}
func (a *App) drafts(w http.ResponseWriter, r *http.Request, u User) {
	if !a.require(w, u, "chief", "operator", "accountant", "auditor") {
		return
	}
	query := "SELECT id,kind,payload,status,version,created_at FROM drafts"
	var rows *sql.Rows
	var e error
	if requested := r.URL.Query().Get("id"); requested != "" {
		if u.Role == "operator" {
			rows, e = a.db.Query(query+" WHERE id=$1 AND created_by=$2 AND kind<>'expense'", requested, u.ID)
		} else {
			rows, e = a.db.Query(query+" WHERE id=$1", requested)
		}
	} else {
		filters := []string{}
		args := []any{}
		add := func(expr string, value any) {
			args = append(args, value)
			filters = append(filters, fmt.Sprintf(expr, len(args)))
		}
		if u.Role == "operator" {
			add("created_by=$%d", u.ID)
			filters = append(filters, "kind<>'expense'")
		}
		values := r.URL.Query()
		if kind := values.Get("kind"); kind != "" {
			add("kind=$%d", kind)
		}
		sourceExpressions := map[string]string{
			"card":      "(payload->>'card_id'=$%d OR (payload->>'source_kind'='card' AND payload->>'source_id'=$%d))",
			"custodian": "(payload->>'from_custodian_id'=$%d OR (payload->>'source_kind'='cash' AND payload->>'source_id'=$%d) OR payload->>'person_id'=$%d OR (kind IN ('shortage','writeoff') AND payload->>'custodian_id'=$%d))",
		}
		recipientExpressions := map[string]string{
			"card":      "(payload->>'destination_kind'='card' AND payload->>'destination_id'=$%d)",
			"custodian": "(payload->>'to_custodian_id'=$%d OR (kind='withdrawal' AND payload->>'custodian_id'=$%d) OR (payload->>'destination_kind'='cash' AND payload->>'destination_id'=$%d))",
			"merchant":  "payload->>'merchant_id'=$%d",
			"category":  "(kind='expense' AND payload->>'category'=$%d)",
		}
		for _, field := range []struct {
			name        string
			expressions map[string]string
		}{{"source", sourceExpressions}, {"recipient", recipientExpressions}} {
			if raw := values.Get(field.name); raw != "" {
				parts := strings.SplitN(raw, ":", 2)
				if len(parts) != 2 || parts[1] == "" || field.expressions[parts[0]] == "" {
					fail(w, 400, errors.New("неверный фильтр участника"))
					return
				}
				args = append(args, parts[1])
				placeholder := fmt.Sprintf("$%d", len(args))
				filters = append(filters, strings.ReplaceAll(field.expressions[parts[0]], "$%d", placeholder))
			}
		}
		cents := "CASE WHEN payload->>'amount_cents' ~ '^[0-9]+$' THEN (payload->>'amount_cents')::bigint ELSE 0 END"
		for _, field := range []struct{ name, op string }{{"amount_from", ">="}, {"amount_to", "<="}} {
			if raw := values.Get(field.name); raw != "" {
				normalized := strings.TrimSpace(strings.ReplaceAll(raw, ",", "."))
				var value int64
				var err error
				if normalized != "0" && normalized != "0.0" && normalized != "0.00" {
					value, err = amount(raw)
				}
				if err != nil {
					fail(w, 400, err)
					return
				}
				add("("+cents+")"+field.op+"$%d", value)
			}
		}
		day := "CASE WHEN kind='expense' AND payload->>'date' ~ '^[0-9]{4}-[0-9]{2}-[0-9]{2}$' THEN payload->>'date' ELSE to_char(created_at AT TIME ZONE 'Europe/Moscow','YYYY-MM-DD') END"
		for _, field := range []struct{ name, op string }{{"date_from", ">="}, {"date_to", "<="}} {
			if raw := values.Get(field.name); raw != "" {
				if _, err := time.Parse("2006-01-02", raw); err != nil {
					fail(w, 400, errors.New("неверная дата фильтра"))
					return
				}
				add("("+day+")"+field.op+"$%d", raw)
			}
		}
		if len(filters) > 0 {
			query += " WHERE " + strings.Join(filters, " AND ")
		}
		page := 1
		if raw := values.Get("page"); raw != "" {
			page, e = strconv.Atoi(raw)
			if e != nil || page < 1 || page > 10000 {
				fail(w, 400, errors.New("неверная страница"))
				return
			}
		}
		args = append(args, (page-1)*15)
		query += fmt.Sprintf(" ORDER BY created_at DESC,id DESC LIMIT 16 OFFSET $%d", len(args))
		rows, e = a.db.Query(query, args...)
	}
	if e != nil {
		fail(w, 500, e)
		return
	}
	defer rows.Close()
	out := []M{}
	for rows.Next() {
		var id, kind, status string
		var payload []byte
		var version int
		var at time.Time
		if e = rows.Scan(&id, &kind, &payload, &status, &version, &at); e != nil {
			fail(w, 500, e)
			return
		}
		m, _ := decodeMap(payload)
		out = append(out, M{"id": id, "kind": kind, "payload": m, "status": status, "version": version, "created_at": at})
	}
	if e = rows.Err(); e != nil {
		fail(w, 500, e)
		return
	}
	respond(w, 200, out)
}
func (a *App) confirmDraft(w http.ResponseWriter, r *http.Request, u User) {
	if r.Method != "POST" || !a.require(w, u, "chief") {
		return
	}
	m, e := jsonBody(r)
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
	var kind, status, key string
	var raw []byte
	var version int
	e = tx.QueryRow("SELECT kind,payload,status,version,idempotency_key FROM drafts WHERE id=$1 FOR UPDATE", str(m, "id")).Scan(&kind, &raw, &status, &version, &key)
	if e != nil {
		fail(w, 404, e)
		return
	}
	if status == "posted" {
		respond(w, 200, M{"status": "already_posted"})
		return
	}
	if status != "draft" {
		fail(w, 409, errors.New("неверный статус"))
		return
	}
	if fmt.Sprint(version) != str(m, "version") {
		fail(w, 409, errors.New("устаревший предпросмотр"))
		return
	}
	var p M
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if e = decoder.Decode(&p); e != nil {
		fail(w, 500, e)
		return
	}
	if p["telegram_confirmation_required"] == true && p["telegram_sender_confirmed"] != true {
		fail(w, 409, errors.New("сначала требуется подтверждение отправителя в Telegram"))
		return
	}
	number, ok := p["amount_cents"].(json.Number)
	if !ok {
		fail(w, 500, errors.New("сумма черновика повреждена"))
		return
	}
	claimed, e := number.Int64()
	if e != nil {
		fail(w, 500, e)
		return
	}
	cents, e := amount(str(m, "confirm_amount"))
	if e != nil {
		fail(w, 400, e)
		return
	}
	if kind == "handover" && cents > claimed {
		fail(w, 409, errors.New("подтверждённая сумма превышает заявленную"))
		return
	}
	if kind != "handover" && cents != claimed {
		fail(w, 409, errors.New("подтвердите точную сумму"))
		return
	}
	lines, e := a.eventLines(tx, kind, p, cents)
	if e != nil {
		a.logAudit(u.ID, "web", "draft_confirm", kind, str(m, "id"), "rejected", e.Error(), M{})
		fail(w, 409, e)
		return
	}
	occurredAt := time.Now()
	if kind == "expense" && str(p, "date") != "" {
		occurredAt, e = time.ParseInLocation("2006-01-02", str(p, "date"), a.location)
		if e != nil {
			fail(w, 409, errors.New("дата расхода не определена"))
			return
		}
	}
	_, e = put(tx, kind, str(m, "id"), "draft:"+str(m, "id"), u.ID, occurredAt, occurredAt, lines, "")
	if e != nil {
		fail(w, 409, e)
		return
	}
	if merchant := str(p, "merchant_id"); merchant != "" {
		if e = normalizeMerchant(tx, merchant, u.ID, "draft:"+str(m, "id")); e != nil {
			fail(w, 409, e)
			return
		}
	}
	_, e = tx.Exec("UPDATE drafts SET status='posted',confirmed_by=$1,confirmed_at=now(),confirmed_amount_cents=$2 WHERE id=$3", u.ID, cents, str(m, "id"))
	if e != nil {
		fail(w, 500, e)
		return
	}
	if e = txAudit(tx, u.ID, "web", "draft_confirm", kind, str(m, "id"), "success", "", M{"claimed_cents": claimed, "confirmed_cents": cents}); e != nil {
		fail(w, 500, e)
		return
	}
	if e = tx.Commit(); e != nil {
		fail(w, 409, e)
		return
	}
	respond(w, 200, M{"status": "posted"})
}
func (a *App) rejectDraft(w http.ResponseWriter, r *http.Request, u User) {
	if r.Method != "POST" || !a.require(w, u, "chief") {
		return
	}
	m, e := jsonBody(r)
	if e != nil {
		fail(w, 400, e)
		return
	}
	reason := str(m, "reason")
	if reason == "" {
		fail(w, 400, errors.New("укажите причину отклонения"))
		return
	}
	tx, e := a.tx()
	if e != nil {
		fail(w, 500, e)
		return
	}
	defer tx.Rollback()
	var kind, status string
	var version int
	e = tx.QueryRow("SELECT kind,status,version FROM drafts WHERE id=$1 FOR UPDATE", str(m, "id")).Scan(&kind, &status, &version)
	if e != nil {
		fail(w, 404, e)
		return
	}
	if status == "rejected" {
		respond(w, 200, M{"status": "already_rejected"})
		return
	}
	if status != "draft" {
		fail(w, 409, errors.New("отклонить можно только черновик"))
		return
	}
	if fmt.Sprint(version) != str(m, "version") {
		fail(w, 409, errors.New("устаревший предпросмотр"))
		return
	}
	if _, e = tx.Exec("UPDATE drafts SET status='rejected' WHERE id=$1", str(m, "id")); e != nil {
		fail(w, 500, e)
		return
	}
	if e = txAudit(tx, u.ID, "web", "draft_reject", kind, str(m, "id"), "success", reason, M{}); e != nil {
		fail(w, 500, e)
		return
	}
	if e = tx.Commit(); e != nil {
		fail(w, 409, e)
		return
	}
	respond(w, 200, M{"status": "rejected"})
}
func (a *App) eventLines(tx *sql.Tx, kind string, p M, v int64) ([]Posting, error) {
	return a.eventLinesWithCardOverdraft(tx, kind, p, v, false)
}

func (a *App) eventLinesWithCardOverdraft(tx *sql.Tx, kind string, p M, v int64, allowCardOverdraft bool) ([]Posting, error) {
	debit := func(account, merchant, card, custodian, ref, category string) Posting {
		return Posting{Account: account, Side: "debit", Amount: v, Merchant: merchant, Card: card, Custodian: custodian, Ref: ref, Category: category}
	}
	credit := func(account, merchant, card, custodian, ref, category string) Posting {
		return Posting{Account: account, Side: "credit", Amount: v, Merchant: merchant, Card: card, Custodian: custodian, Ref: ref, Category: category}
	}
	source := func() (Posting, error) {
		s, e := moneySource(tx, str(p, "source_kind"), str(p, "source_id"))
		if e != nil {
			return s, e
		}
		if !allowCardOverdraft || s.Card == "" {
			bal, e := available(tx, s)
			if e != nil {
				return s, e
			}
			if bal < v {
				return s, errors.New("недостаточно денег в источнике")
			}
		}
		s.Side = "credit"
		s.Amount = v
		return s, nil
	}
	dest := func() (Posting, error) {
		d, e := moneySource(tx, str(p, "destination_kind"), str(p, "destination_id"))
		d.Side = "debit"
		d.Amount = v
		return d, e
	}
	need := func(account, dim, val string) error {
		b, e := balance(tx, account, dim, val)
		if e != nil {
			return e
		}
		if b < v {
			return errors.New("сумма превышает остаток")
		}
		return nil
	}
	linkedCustodian := func(ref, expectedKind string) (string, error) {
		var status, actualKind string
		var cust sql.NullString
		e := tx.QueryRow("SELECT status,kind,payload->>'custodian_id' FROM drafts WHERE id=$1", ref).Scan(&status, &actualKind, &cust)
		if e != nil {
			return "", e
		}
		if status != "posted" || actualKind != expectedKind || !cust.Valid {
			return "", errors.New("недействительное основание")
		}
		return cust.String, nil
	}
	switch kind {
	case "withdrawal":
		card := str(p, "card_id")
		cust := str(p, "custodian_id")
		a, e := cashAccount(tx, cust)
		if e != nil {
			return nil, e
		}
		if !allowCardOverdraft {
			if e = need("1100", "card", card); e != nil {
				return nil, e
			}
		}
		return []Posting{debit(a, "", "", cust, "", ""), credit("1100", "", card, "", "", "")}, nil
	case "handover":
		from := str(p, "from_custodian_id")
		to := str(p, "to_custodian_id")
		var k string
		if e := tx.QueryRow("SELECT kind FROM custodians WHERE id=$1", to).Scan(&k); e != nil {
			return nil, e
		}
		if k != "chief" {
			return nil, errors.New("передача только главному администратору")
		}
		fa, e := cashAccount(tx, from)
		if e != nil {
			return nil, e
		}
		availableCash, e := balance(tx, fa, "custodian", from)
		if e != nil {
			return nil, e
		}
		if availableCash < v {
			return nil, fmt.Errorf("у отправителя учтено %s ₽, для передачи не хватает %s ₽", rub(availableCash), rub(v-availableCash))
		}
		ta, _ := cashAccount(tx, to)
		return []Posting{debit(ta, "", "", to, "", ""), credit(fa, "", "", from, "", "")}, nil
	case "transfer":
		from, to := str(p, "from_custodian_id"), str(p, "to_custodian_id")
		if from == "" || to == "" || from == to {
			return nil, errors.New("выберите разных сборщиков")
		}
		for _, custodian := range []string{from, to} {
			var kind string
			if e := tx.QueryRow("SELECT kind FROM custodians WHERE id=$1 AND active", custodian).Scan(&kind); e != nil || kind != "collector" {
				return nil, errors.New("перевод доступен только между активными сборщиками")
			}
		}
		availableCash, e := balance(tx, "1200", "custodian", from)
		if e != nil {
			return nil, e
		}
		if availableCash < v {
			return nil, errors.New("у отправителя недостаточно наличных")
		}
		return []Posting{debit("1200", "", "", to, "", ""), credit("1200", "", "", from, "", "")}, nil
	case "repayment":
		s, e := source()
		if e != nil {
			return nil, e
		}
		merchant := str(p, "merchant_id")
		b, e := balance(tx, "2100", "merchant", merchant)
		if e != nil {
			return nil, e
		}
		pay := v
		if pay > b {
			pay = b
		}
		out := []Posting{}
		if pay > 0 {
			q := debit("2100", merchant, "", "", "", "")
			q.Amount = pay
			out = append(out, q)
		}
		if v > pay {
			q := debit("1300", merchant, "", "", "", "")
			q.Amount = v - pay
			out = append(out, q)
		}
		return append(out, s), nil
	case "expense":
		s, e := source()
		if e != nil {
			return nil, e
		}
		cat := str(p, "category")
		account, e := expenseAccount(cat)
		if e != nil {
			return nil, e
		}
		return []Posting{debit(account, "", "", "", "", cat), s}, nil
	case "injection":
		d, e := dest()
		if e != nil {
			return nil, e
		}
		person := str(p, "person_id")
		if person == "" {
			return nil, errors.New("нужен вносивший")
		}
		return []Posting{d, credit("2200", "", "", person, "", "")}, nil
	case "forgive_injection":
		person := str(p, "person_id")
		if e := need("2200", "custodian", person); e != nil {
			return nil, e
		}
		return []Posting{debit("2200", "", "", person, "", ""), credit("3200", "", "", person, "", "")}, nil
	case "shortage":
		cust := str(p, "custodian_id")
		acc, e := cashAccount(tx, cust)
		if e != nil {
			return nil, e
		}
		if e = need(acc, "custodian", cust); e != nil {
			return nil, e
		}
		return []Posting{debit("1400", "", "", cust, str(p, "draft_id"), ""), credit(acc, "", "", cust, "", "")}, nil
	case "writeoff":
		cust := str(p, "custodian_id")
		shortage := str(p, "shortage_id")
		owner, e := linkedCustodian(shortage, "shortage")
		if e != nil || owner != cust {
			return nil, errors.New("нужна недостача того же сборщика")
		}
		if e := need("1400", "ref", shortage); e != nil {
			return nil, e
		}
		return []Posting{debit("5400", "", "", cust, str(p, "draft_id"), ""), credit("1400", "", "", cust, shortage, "")}, nil
	case "surplus":
		cust := str(p, "custodian_id")
		acc, e := cashAccount(tx, cust)
		if e != nil {
			return nil, e
		}
		return []Posting{debit(acc, "", "", cust, "", ""), credit("2300", "", "", "", str(p, "source_ref"), "")}, nil
	case "surplus_income", "surplus_merchant", "surplus_shortage":
		ref := str(p, "source_ref")
		if e := need("2300", "ref", ref); e != nil {
			return nil, e
		}
		cr := Posting{}
		switch kind {
		case "surplus_income":
			cr = credit("4200", "", "", "", "", "unattributed_surplus")
		case "surplus_merchant":
			cr = credit("2110", str(p, "merchant_id"), "", "", ref, "")
		case "surplus_shortage":
			cust := str(p, "custodian_id")
			owner, e := linkedCustodian(ref, "surplus")
			if e != nil || owner != cust {
				return nil, errors.New("излишек другого хранителя")
			}
			shortage := str(p, "shortage_id")
			owner, e = linkedCustodian(shortage, "shortage")
			if e != nil || owner != cust {
				return nil, errors.New("недостача другого сборщика")
			}
			if e := need("1400", "ref", shortage); e != nil {
				return nil, e
			}
			cr = credit("1400", "", "", cust, shortage, "")
		}
		return []Posting{debit("2300", "", "", "", ref, ""), cr}, nil
	case "surplus_return":
		s, e := source()
		if e != nil {
			return nil, e
		}
		ref := str(p, "source_ref")
		if e = need("2110", "ref", ref); e != nil {
			return nil, e
		}
		var matched bool
		e = tx.QueryRow("SELECT EXISTS(SELECT 1 FROM postings WHERE account='2110' AND side='credit' AND ref_id=$1 AND merchant_id=$2)", ref, str(p, "merchant_id")).Scan(&matched)
		if e != nil || !matched {
			return nil, errors.New("обязательство другого мерчанта")
		}
		return []Posting{debit("2110", str(p, "merchant_id"), "", "", ref, ""), s}, nil
	case "collection":
		d, e := dest()
		if e != nil {
			return nil, e
		}
		acct := str(p, "receivable_account")
		if acct != "1300" && acct != "1400" {
			return nil, errors.New("unsupported receivable")
		}
		dim, val, mer, cust := "merchant", str(p, "merchant_id"), str(p, "merchant_id"), ""
		if acct == "1400" {
			dim, val, mer, cust = "custodian", str(p, "custodian_id"), "", str(p, "custodian_id")
			shortage := str(p, "shortage_id")
			owner, e := linkedCustodian(shortage, "shortage")
			if e != nil || owner != cust {
				return nil, errors.New("нужна недостача того же сборщика")
			}
			dim, val = "ref", shortage
		}
		if e = need(acct, dim, val); e != nil {
			return nil, e
		}
		return []Posting{d, credit(acct, mer, "", cust, valIf(acct == "1400", val, ""), "")}, nil
	case "recovery":
		d, e := dest()
		if e != nil {
			return nil, e
		}
		ref := str(p, "writeoff_id")
		var status, actualKind string
		var written int64
		e = tx.QueryRow("SELECT status,kind,(payload->>'amount_cents')::bigint FROM drafts WHERE id=$1", ref).Scan(&status, &actualKind, &written)
		if e != nil || status != "posted" || actualKind != "writeoff" {
			return nil, errors.New("нужно подтверждённое списание")
		}
		var recovered int64
		e = tx.QueryRow("SELECT COALESCE(SUM(amount_cents),0) FROM postings WHERE account='4200' AND side='credit' AND ref_id=$1", ref).Scan(&recovered)
		if e != nil {
			return nil, e
		}
		if recovered+v > written {
			return nil, errors.New("возврат превышает списание")
		}
		return []Posting{d, credit("4200", "", "", "", ref, "written_off_recovery")}, nil
	}
	return nil, errors.New("unsupported event")
}
