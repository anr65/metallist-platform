package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNextCardMonth(t *testing.T) {
	for _, pair := range [][2]string{{"2026-12-31T21:00:00Z", "2027-02-01T00:00:00+03:00"}, {"2026-10-31T20:59:59Z", "2026-11-01T00:00:00+03:00"}, {"2028-02-29T10:00:00Z", "2028-03-01T00:00:00+03:00"}} {
		now, _ := time.Parse(time.RFC3339, pair[0])
		if got := nextCardMonth(now).Format(time.RFC3339); got != pair[1] {
			t.Fatalf("%s: %s", pair[0], got)
		}
	}
}

func TestCardDropHandlerLifecycle(t *testing.T) {
	a := testApp(t)
	chief, _, bank, card := fixtures(t, a)
	var handlerID string
	if err := a.db.QueryRow("SELECT id FROM drop_handlers LIMIT 1").Scan(&handlerID); err != nil {
		t.Fatal(err)
	}
	var legacy sql.NullString
	if err := a.db.QueryRow("SELECT drop_handler_id FROM cards WHERE id=$1", card).Scan(&legacy); err != nil || legacy.Valid {
		t.Fatal("legacy card must remain nullable", err)
	}
	body := M{"kind": "card", "bank_id": bank, "owner_label": "Тестовый владелец", "pan": "4111111111111111"}
	if code, _ := req(t, a.catalogCreate, chief, body); code != 400 {
		t.Fatal("missing handler accepted", code)
	}
	body["drop_handler_id"] = id()
	if code, _ := req(t, a.catalogCreate, chief, body); code != 400 {
		t.Fatal("unknown handler accepted", code)
	}
	body["drop_handler_id"] = handlerID
	if code, out := req(t, a.catalogCreate, chief, body); code != 201 {
		t.Fatal(code, out)
	}
	if code, out := req(t, a.catalogUpdate, chief, M{"kind": "card", "id": card, "owner_label": "Историческая карта", "drop_handler_id": handlerID}); code != 200 {
		t.Fatal(code, out)
	}
	if code, out := req(t, a.catalogUpdate, chief, M{"kind": "drop_handler", "id": handlerID, "name": "Дроповод", "active": false}); code != 200 {
		t.Fatal(code, out)
	}
	body["pan"] = "4242424242424242"
	if code, _ := req(t, a.catalogCreate, chief, body); code != 400 {
		t.Fatal("inactive handler accepted", code)
	}
	if code, out := req(t, a.catalogUpdate, chief, M{"kind": "card", "id": card, "owner_label": "Историческая карта", "drop_handler_id": handlerID}); code != 200 {
		t.Fatal("historical assignment broken", code, out)
	}
	w := httptest.NewRecorder()
	a.catalog(w, httptest.NewRequest(http.MethodGet, "/api/catalog", nil), chief)
	var catalog struct {
		Cards []M `json:"cards"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &catalog); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range catalog.Cards {
		if c["id"] == card {
			found = c["drop_handler_name"] == "Дроповод" && c["drop_handler_id"] == handlerID
		}
	}
	if !found {
		t.Fatal("card owner missing from list", w.Body.String())
	}
	if code, out := req(t, a.catalogUpdate, chief, M{"kind": "card", "id": card, "owner_label": "Без дроповода", "drop_handler_id": nil}); code != 200 {
		t.Fatal(code, out)
	}
	var audit int
	if err := a.db.QueryRow("SELECT count(*) FROM audit_events WHERE object_id=$1 AND action='card_drop_handler_change'", card).Scan(&audit); err != nil || audit != 2 {
		t.Fatal("assignment audit", audit, err)
	}
}

func TestCardStatusBotAndMonthlyReset(t *testing.T) {
	a := testApp(t)
	chief, merchant, bank, card := fixtures(t, a)
	_, collector, custodian, _ := telegramFixture(t, a, card)
	actor := telegramActor{User: collector, CustodianID: custodian}
	// All four statuses are addressable even when the card is inactive.
	for i, status := range []string{"Блок", "Архив", "Лимит", "Активна"} {
		if code := telegramRequest(t, a, telegramMessageUpdate(int64(9800+i), 555, "/card_status 1234 "+status)); code != 200 {
			t.Fatal(code)
		}
		var got string
		if err := a.db.QueryRow("SELECT status FROM cards WHERE id=$1", card).Scan(&got); err != nil || cardStatusLabels[got] != status {
			t.Fatal(got, err)
		}
	}
	if err := a.telegramCardStatus(actor, []string{"/card_status", "1234", "unknown"}, "card_status", 9990); err == nil {
		t.Fatal("unknown status accepted")
	}
	if err := a.telegramCardStatus(telegramActor{User: User{Role: "auditor"}}, []string{"/block_card", "1234"}, "block_card", 9991); err == nil {
		t.Fatal("auditor changed status")
	}
	if err := a.telegramCardStatus(actor, []string{"/block_card", "1234"}, "block_card", 9992); err != nil {
		t.Fatal(err)
	}
	contact := id()
	if _, err := a.db.Exec("INSERT INTO payment_contacts(id,full_name,phone,created_by) VALUES($1,'Тестовый Контакт','+79990000000',$2)", contact, chief.ID); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"blocked", "limit", "retired"} {
		tx, _ := a.tx()
		if err := changeCardStatus(tx, chief.ID, "web", card, status, time.Now()); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		body := M{"external_ref": "STATUS-TEST", "mode": "cards", "merchant_id": merchant, "rows": []M{{"card_id": card, "contact_id": contact}}}
		if code, _ := req(t, a.createPaymentRequest, chief, body); code != 409 {
			t.Fatal("inactive card request accepted", status, code)
		}
	}
	// Ambiguous suffix cannot mutate either card. UUID disambiguates.
	second := id()
	if _, err := a.db.Exec("INSERT INTO cards(id,bank_id,owner_label,mask,last4) VALUES($1,$2,'Другой','111111******1234','1234')", second, bank); err != nil {
		t.Fatal(err)
	}
	if err := a.telegramCardStatus(actor, []string{"/block_card", "1234"}, "block_card", 9993); err == nil {
		t.Fatal("ambiguous suffix accepted")
	}
	if err := a.telegramCardStatus(actor, []string{"/block_card", second}, "block_card", 9994); err != nil {
		t.Fatal(err)
	}
	before, _ := time.Parse(time.RFC3339, "2026-12-31T20:59:59Z")
	boundary := before.Add(time.Second)
	tx, _ := a.tx()
	if err := changeCardStatus(tx, chief.ID, "web", card, "limit", before); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := a.resetCardLimits(before); err != nil {
		t.Fatal(err)
	}
	var beforeStatus string
	if err := a.db.QueryRow("SELECT status FROM cards WHERE id=$1", card).Scan(&beforeStatus); err != nil || beforeStatus != "limit" {
		t.Fatal("reset before Moscow boundary", beforeStatus, err)
	}
	for _, now := range []time.Time{boundary, boundary.Add(time.Hour), boundary.AddDate(0, 1, 0)} {
		if err := a.resetCardLimits(now); err != nil {
			t.Fatal(err)
		}
	}
	var state, other string
	var until sql.NullTime
	if err := a.db.QueryRow("SELECT status,limit_until FROM cards WHERE id=$1", card).Scan(&state, &until); err != nil || state != "active" || until.Valid {
		t.Fatal(state, until, err)
	}
	a.db.QueryRow("SELECT status FROM cards WHERE id=$1", second).Scan(&other)
	if other != "blocked" {
		t.Fatal("reset changed blocked card")
	}
	var resets, postings int
	if err := a.db.QueryRow("SELECT count(*) FROM audit_events WHERE object_id=$1 AND channel='system' AND action='card_status_change'", card).Scan(&resets); err != nil || resets != 1 {
		t.Fatal("reset not idempotent", resets, err)
	}
	a.db.QueryRow("SELECT count(*) FROM postings").Scan(&postings)
	if postings != 0 {
		t.Fatal("status changed ledger")
	}
	var detail string
	a.db.QueryRow("SELECT string_agg(detail::text,'') FROM audit_events").Scan(&detail)
	if strings.Contains(detail, "4111111111111111") {
		t.Fatal("PAN leaked")
	}
}

func TestCardBlockPreservesExistingRegistryAndRequest(t *testing.T) {
	a := testApp(t)
	chief, merchant, _, card := fixtures(t, a)
	contact := id()
	if _, err := a.db.Exec("INSERT INTO payment_contacts(id,full_name,phone,created_by) VALUES($1,'Тестовый Контакт','+79990000000',$2)", contact, chief.ID); err != nil {
		t.Fatal(err)
	}
	body := M{"external_ref": "OLD-CARD-PLAN", "mode": "cards", "merchant_id": merchant, "rows": []M{{"card_id": card, "contact_id": contact}}}
	code, request := req(t, a.createPaymentRequest, chief, body)
	if code != 201 {
		t.Fatal(code, request)
	}
	regBody := M{"merchant_id": merchant, "idempotency_key": id(), "payment_date": testPaymentDate(), "rows": []M{{"card_id": card, "contact_id": contact, "amount": "100.00"}}}
	code, registry := req(t, a.createManualRegistry, chief, regBody)
	if code != 201 {
		t.Fatal(code, registry)
	}
	snapshot := func() string {
		var value string
		if err := a.db.QueryRow(`SELECT (SELECT row_to_json(p)::text FROM payment_requests p WHERE id=$1)||
   (SELECT json_agg(r ORDER BY row_no)::text FROM payment_request_rows r WHERE request_id=$1)||
   (SELECT row_to_json(g)::text FROM registries g WHERE id=$2)||
   (SELECT json_agg(r ORDER BY row_no)::text FROM registry_rows r WHERE registry_id=$2)`, request["id"], registry["id"]).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	before := snapshot()
	if code, out := req(t, a.catalogUpdate, chief, M{"kind": "card", "id": card, "owner_label": "Тестовый владелец", "status": "blocked"}); code != 200 {
		t.Fatal(code, out)
	}
	if snapshot() != before {
		t.Fatal("blocking rewrote historical documents")
	}
	regBody["idempotency_key"] = id()
	if code, _ := req(t, a.createManualRegistry, chief, regBody); code != 400 {
		t.Fatal("blocked card used in new manual registry", code)
	}
}
