package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"golang.org/x/crypto/bcrypt"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

func (a *App) tx() (*sql.Tx, error) {
	t, e := a.db.BeginTx(context.Background(), &sql.TxOptions{Isolation: sql.LevelSerializable})
	if e != nil {
		return nil, e
	}
	if _, e = t.Exec("SELECT pg_advisory_xact_lock(704112)"); e != nil {
		t.Rollback()
		return nil, e
	}
	return t, nil
}
func put(tx *sql.Tx, eventType, eventID, key, actor string, occurred, recognized time.Time, lines []Posting, reversal string) (string, error) {
	if len(lines) < 2 {
		return "", errors.New("insufficient postings")
	}
	var d, c int64
	for _, p := range lines {
		if p.Amount <= 0 {
			return "", errors.New("nonpositive posting")
		}
		if p.Side == "debit" {
			if d > math.MaxInt64-p.Amount {
				return "", errors.New("entry total overflow")
			}
			d += p.Amount
		} else if p.Side == "credit" {
			if c > math.MaxInt64-p.Amount {
				return "", errors.New("entry total overflow")
			}
			c += p.Amount
		} else {
			return "", errors.New("invalid side")
		}
	}
	if d != c {
		return "", fmt.Errorf("unbalanced entry: %d/%d", d, c)
	}
	entry := id()
	_, e := tx.Exec("INSERT INTO journal_entries(id,event_type,event_id,occurred_at,recognition_at,actor_id,reversal_of,idempotency_key) VALUES($1,$2,$3,$4,$5,$6,$7,$8)", entry, eventType, eventID, occurred, recognized, actor, nilID(reversal), key)
	if e != nil {
		return "", e
	}
	for _, p := range lines {
		_, e = tx.Exec("INSERT INTO postings(id,entry_id,account,side,amount_cents,merchant_id,card_id,custodian_id,ref_id,category) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)", id(), entry, p.Account, p.Side, p.Amount, nilID(p.Merchant), nilID(p.Card), nilID(p.Custodian), nilID(p.Ref), nilID(p.Category))
		if e != nil {
			return "", e
		}
	}
	return entry, nil
}
func reverse(tx *sql.Tx, orig, actor, key string) error {
	var typ, event string
	var occurred, recognized time.Time
	e := tx.QueryRow("SELECT event_type,event_id,occurred_at,recognition_at FROM journal_entries WHERE id=$1", orig).Scan(&typ, &event, &occurred, &recognized)
	if e != nil {
		return e
	}
	rows, e := tx.Query("SELECT account,side,amount_cents,COALESCE(merchant_id::text,''),COALESCE(card_id::text,''),COALESCE(custodian_id::text,''),COALESCE(ref_id::text,''),COALESCE(category,'') FROM postings WHERE entry_id=$1 ORDER BY id", orig)
	if e != nil {
		return e
	}
	var ps []Posting
	for rows.Next() {
		var p Posting
		if e = rows.Scan(&p.Account, &p.Side, &p.Amount, &p.Merchant, &p.Card, &p.Custodian, &p.Ref, &p.Category); e != nil {
			rows.Close()
			return e
		}
		if p.Side == "debit" {
			p.Side = "credit"
		} else {
			p.Side = "debit"
		}
		ps = append(ps, p)
	}
	rows.Close()
	_, e = put(tx, "reversal", id(), key, actor, time.Now(), recognized, ps, orig)
	return e
}
func balance(tx *sql.Tx, account, dimension, value string) (int64, error) {
	col := ""
	switch dimension {
	case "merchant":
		col = "merchant_id"
	case "card":
		col = "card_id"
	case "custodian":
		col = "custodian_id"
	case "ref":
		col = "ref_id"
	default:
		return 0, errors.New("invalid dimension")
	}
	var v int64
	e := tx.QueryRow("SELECT COALESCE(SUM(CASE WHEN side='debit' THEN amount_cents ELSE -amount_cents END),0) FROM postings WHERE account=$1 AND "+col+"=$2", account, value).Scan(&v)
	if e != nil {
		return 0, e
	}
	if account == "2100" || account == "2110" || account == "2200" || account == "2300" || account == "4100" || account == "4200" {
		return -v, nil
	}
	return v, nil
}
func cashAccount(tx *sql.Tx, custodian string) (string, error) {
	var kind string
	e := tx.QueryRow("SELECT kind FROM custodians WHERE id=$1 AND active", custodian).Scan(&kind)
	if e != nil {
		return "", e
	}
	if kind == "chief" {
		return "1210", nil
	}
	return "1200", nil
}
func moneySource(tx *sql.Tx, kind, ref string) (Posting, error) {
	if kind == "card" {
		var active bool
		e := tx.QueryRow("SELECT status='active' FROM cards WHERE id=$1", ref).Scan(&active)
		if e != nil {
			return Posting{}, e
		}
		if !active {
			return Posting{}, errors.New("карта недоступна")
		}
		return Posting{Account: "1100", Card: ref}, nil
	}
	if kind == "cash" {
		account, e := cashAccount(tx, ref)
		return Posting{Account: account, Custodian: ref}, e
	}
	return Posting{}, errors.New("unknown money source")
}
func available(tx *sql.Tx, p Posting) (int64, error) {
	if p.Card != "" {
		return balance(tx, p.Account, "card", p.Card)
	}
	return balance(tx, p.Account, "custodian", p.Custodian)
}
func (a *App) catalog(w http.ResponseWriter, r *http.Request, u User) {
	if r.Method != "GET" {
		fail(w, 405, errors.New("method"))
		return
	}
	result := M{}
	specs := map[string]string{"merchants": "SELECT m.id,m.code,m.name,m.active,COALESCE(p.status,'awaiting_sample') AS parser_status,COALESCE(t.name,'Ожидает образец') AS parser_name,COALESCE(p.parser_code,'') AS parser_code FROM merchants m LEFT JOIN merchant_import_profiles p ON p.merchant_id=m.id LEFT JOIN registry_parser_types t ON t.code=p.parser_code ORDER BY m.name", "banks": "SELECT id,code,name FROM banks ORDER BY name", "cards": "SELECT c.id,b.name,c.owner_label,c.mask,c.status FROM cards c JOIN banks b ON b.id=c.bank_id ORDER BY b.name,c.mask", "payment_contacts": "SELECT id,full_name,phone,active FROM payment_contacts ORDER BY full_name,phone", "custodians": "SELECT id,name,kind,active FROM custodians ORDER BY name", "tariffs": "SELECT t.id,m.name,t.rate_bp,t.valid_from,t.active FROM tariffs t JOIN merchants m ON m.id=t.merchant_id ORDER BY t.created_at DESC", "users": "SELECT id,login,name,role,active,COALESCE(telegram_id::text,'') AS telegram_id,COALESCE(custodian_id::text,'') AS custodian_id FROM users ORDER BY name"}
	specs["request_cards"] = `SELECT c.id,c.mask,b.name AS bank,COALESCE(previous.contact_name,'') AS full_name,COALESCE(previous.contact_phone,'') AS phone
		FROM cards c JOIN banks b ON b.id=c.bank_id
		LEFT JOIN LATERAL (SELECT r.contact_name,r.contact_phone FROM payment_request_rows r JOIN payment_requests p ON p.id=r.request_id
		WHERE r.card_id=c.id AND r.contact_id IS NOT NULL ORDER BY p.created_at DESC,p.id DESC,r.row_no DESC LIMIT 1) previous ON true
		WHERE c.status='active' ORDER BY b.name,c.mask,c.id`
	for name, q := range specs {
		if u.Role == "collector" && name != "cards" {
			continue
		}
		if (name == "payment_contacts" || name == "request_cards") && u.Role != "chief" && u.Role != "operator" {
			continue
		}
		if name == "users" && u.Role != "sysadmin" && u.Role != "chief" {
			continue
		}
		var rows *sql.Rows
		var e error
		if name == "cards" && (u.Role == "operator" || u.Role == "collector") {
			rows, e = a.db.Query("SELECT c.id,b.name,c.owner_label,c.mask,c.status FROM cards c JOIN banks b ON b.id=c.bank_id JOIN card_assignments x ON x.card_id=c.id WHERE x.user_id=$1 ORDER BY b.name,c.mask", u.ID)
		} else {
			rows, e = a.db.Query(q)
		}
		if e != nil {
			fail(w, 500, e)
			return
		}
		cols, _ := rows.Columns()
		arr := []M{}
		for rows.Next() {
			vals := make([]interface{}, len(cols))
			ptrs := make([]interface{}, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if e = rows.Scan(ptrs...); e != nil {
				break
			}
			m := M{}
			for i, c := range cols {
				v := vals[i]
				switch x := v.(type) {
				case []byte:
					m[c] = string(x)
				case time.Time:
					m[c] = x.Format(time.RFC3339)
				default:
					m[c] = v
				}
			}
			arr = append(arr, m)
		}
		rows.Close()
		if e != nil {
			fail(w, 500, e)
			return
		}
		result[name] = arr
	}
	respond(w, 200, result)
}
func (a *App) catalogCreate(w http.ResponseWriter, r *http.Request, u User) {
	if r.Method != "POST" {
		fail(w, 405, errors.New("method"))
		return
	}
	m, e := jsonBody(r)
	if e != nil {
		fail(w, 400, e)
		return
	}
	kind := str(m, "kind")
	if kind == "user" {
		if !a.require(w, u, "sysadmin") {
			return
		}
	} else if kind == "payment_contact" {
		if !a.require(w, u, "chief", "operator") {
			return
		}
	} else if !a.require(w, u, "chief") {
		return
	}
	newID := id()
	switch kind {
	case "merchant":
		tx, beginErr := a.tx()
		if beginErr != nil {
			e = beginErr
			break
		}
		defer tx.Rollback()
		if _, e = tx.Exec("INSERT INTO merchants(id,code,name) VALUES($1,$2,$3)", newID, str(m, "code"), str(m, "name")); e == nil {
			_, e = tx.Exec("INSERT INTO merchant_import_profiles(merchant_id,status) VALUES($1,'awaiting_sample')", newID)
		}
		if e == nil {
			e = tx.Commit()
		}
	case "bank":
		_, e = a.db.Exec("INSERT INTO banks(id,code,name) VALUES($1,$2,$3)", newID, str(m, "code"), str(m, "name"))
	case "custodian":
		_, e = a.db.Exec("INSERT INTO custodians(id,name,kind) VALUES($1,$2,$3)", newID, str(m, "name"), str(m, "custodian_kind"))
	case "card":
		mask := str(m, "mask")
		if !cardMaskPattern.MatchString(mask) {
			e = errors.New("нужна маска формата 000000******1234; полный номер запрещён")
		} else if os.Getenv("APP_ENV") == "demo" && !approvedSyntheticName(str(m, "owner_label")) {
			e = errors.New("в демо выберите вымышленное ФИО из списка")
		} else {
			_, e = a.db.Exec("INSERT INTO cards(id,bank_id,owner_label,mask,last4) VALUES($1,$2,$3,$4,$5)", newID, str(m, "bank_id"), str(m, "owner_label"), mask, mask[len(mask)-4:])
		}
	case "payment_contact":
		fullName := strings.TrimSpace(str(m, "full_name"))
		phone := ""
		phone, e = normalizePaymentPhone(str(m, "phone"))
		if e == nil && len([]rune(fullName)) < 5 {
			e = errors.New("укажите полное ФИО")
		}
		if e == nil && os.Getenv("APP_ENV") == "demo" && !approvedSyntheticName(fullName) {
			e = errors.New("в демо выберите вымышленное ФИО из списка")
		}
		if e == nil {
			_, e = a.db.Exec(`INSERT INTO payment_contacts(id,full_name,phone,created_by) VALUES($1,$2,$3,$4)
				ON CONFLICT (lower(full_name),phone) DO NOTHING`, newID, fullName, phone, u.ID)
			if e == nil {
				e = a.db.QueryRow("SELECT id FROM payment_contacts WHERE lower(full_name)=lower($1) AND phone=$2 AND active", fullName, phone).Scan(&newID)
			}
		}
	case "user":
		role := str(m, "role")
		if role != "operator" && role != "collector" && role != "accountant" && role != "auditor" {
			e = errors.New("invalid role")
		} else {
			pass := token()
			hash, _ := bcryptHash(pass)
			_, e = a.db.Exec("INSERT INTO users(id,login,name,role,password_hash) VALUES($1,$2,$3,$4,$5)", newID, str(m, "login"), str(m, "name"), role, hash)
			if e == nil {
				a.logAudit(u.ID, "web", "catalog_create", "user", newID, "success", "", M{"role": role})
				respond(w, 201, M{"id": newID, "temporary_password": pass})
				return
			}
		}
	default:
		e = errors.New("unknown kind")
	}
	if e != nil {
		a.logAudit(u.ID, "web", "catalog_create", kind, newID, "rejected", e.Error(), M{})
		fail(w, 400, e)
		return
	}
	a.logAudit(u.ID, "web", "catalog_create", kind, newID, "success", "", M{})
	respond(w, 201, M{"id": newID})
}
func (a *App) linkTelegram(w http.ResponseWriter, r *http.Request, u User) {
	if r.Method != "POST" || !a.require(w, u, "sysadmin") {
		return
	}
	m, e := jsonBody(r)
	if e != nil {
		fail(w, 400, e)
		return
	}
	tid, e := strconv.ParseInt(str(m, "telegram_id"), 10, 64)
	if e != nil || tid <= 0 {
		fail(w, 400, errors.New("нужен числовой Telegram ID"))
		return
	}
	custodian := str(m, "custodian_id")
	if custodian == "" {
		fail(w, 400, errors.New("укажите ответственного за наличные"))
		return
	}
	res, e := a.db.Exec("UPDATE users SET telegram_id=$1,custodian_id=$2 FROM custodians c WHERE users.id=$3 AND users.active AND c.id=$2 AND c.active AND ((users.role IN ('collector','operator') AND c.kind=users.role) OR (users.role='chief' AND c.kind='chief'))", tid, custodian, str(m, "user_id"))
	if e != nil {
		fail(w, 409, e)
		return
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		fail(w, 400, errors.New("свяжите активного сборщика, операциониста или главного администратора с соответствующим ответственным за наличные"))
		return
	}
	a.logAudit(u.ID, "web", "telegram_link", "user", str(m, "user_id"), "success", "", M{"telegram_id": tid, "custodian_id": custodian})
	respond(w, 200, M{"linked": true})
}
func bcryptHash(p string) (string, error) {
	h, e := bcrypt.GenerateFromPassword([]byte(p), bcrypt.DefaultCost)
	return string(h), e
}
func (a *App) audit(w http.ResponseWriter, r *http.Request, u User) {
	if !a.require(w, u, "chief", "accountant", "sysadmin", "auditor") {
		return
	}
	rows, e := a.db.Query("SELECT id,at,COALESCE(actor_id::text,''),COALESCE(actor_role,''),channel,action,object_type,COALESCE(object_id::text,''),outcome,COALESCE(reason,'') FROM audit_events ORDER BY at DESC LIMIT 300")
	if e != nil {
		fail(w, 500, e)
		return
	}
	defer rows.Close()
	out := []M{}
	for rows.Next() {
		var id, actor, role, channel, action, typ, obj, outcome, reason string
		var at time.Time
		if e = rows.Scan(&id, &at, &actor, &role, &channel, &action, &typ, &obj, &outcome, &reason); e != nil {
			fail(w, 500, e)
			return
		}
		out = append(out, M{"id": id, "at": at, "actor": actor, "role": role, "channel": channel, "action": action, "object_type": typ, "object_id": obj, "outcome": outcome, "reason": reason})
	}
	respond(w, 200, out)
}
func (a *App) observation(w http.ResponseWriter, r *http.Request, u User) {
	if r.Method != "POST" || !a.require(w, u, "chief") {
		return
	}
	m, e := jsonBody(r)
	if e != nil {
		fail(w, 400, e)
		return
	}
	if !a.cardAllowed(u, str(m, "card_id")) {
		a.logAudit(u.ID, "web", "observation", "card", str(m, "card_id"), "rejected", "card_not_assigned", M{})
		fail(w, 403, errors.New("карта не назначена"))
		return
	}
	v, e := nonnegative(str(m, "amount"))
	if e != nil {
		fail(w, 400, e)
		return
	}
	newID := id()
	_, e = a.db.Exec("INSERT INTO observations(id,card_id,observed_cents,observed_at,reporter_id,source) VALUES($1,$2,$3,now(),$4,$5)", newID, str(m, "card_id"), v, u.ID, "web")
	if e != nil {
		fail(w, 400, e)
		return
	}
	a.logAudit(u.ID, "web", "observation", "card", str(m, "card_id"), "success", "", M{"observation_id": newID})
	respond(w, 201, M{"id": newID})
}
func encode(v interface{}) []byte { b, _ := json.Marshal(v); return b }
func (a *App) assignCard(w http.ResponseWriter, r *http.Request, u User) {
	if r.Method != "POST" || !a.require(w, u, "sysadmin") {
		return
	}
	m, e := jsonBody(r)
	if e != nil {
		fail(w, 400, e)
		return
	}
	res, e := a.db.Exec("INSERT INTO card_assignments(user_id,card_id,assigned_by) SELECT u.id,c.id,$3 FROM users u,cards c WHERE u.id=$1 AND u.role IN ('operator','collector') AND u.active AND c.id=$2 AND c.status='active' ON CONFLICT DO NOTHING", str(m, "user_id"), str(m, "card_id"), u.ID)
	if e != nil {
		fail(w, 409, e)
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		fail(w, 409, errors.New("карта уже назначена или пользователь недоступен"))
		return
	}
	a.logAudit(u.ID, "web", "card_assign", "card", str(m, "card_id"), "success", "", M{"user_id": str(m, "user_id")})
	respond(w, 200, M{"assigned": true})
}
func (a *App) cardAllowed(u User, card string) bool {
	if u.Role != "operator" && u.Role != "collector" {
		return true
	}
	var ok bool
	_ = a.db.QueryRow("SELECT EXISTS(SELECT 1 FROM card_assignments WHERE user_id=$1 AND card_id=$2)", u.ID, card).Scan(&ok)
	return ok
}
