package main

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestManualRegistryLinkedRequest(t *testing.T) {
	a := testApp(t)
	chief, merchant, bank, card := fixtures(t, a)
	requestID, otherCard, otherMerchant, contact := id(), id(), id(), id()
	for _, q := range []struct {
		sql  string
		args []interface{}
	}{
		{"INSERT INTO cards(id,bank_id,owner_label,mask,last4) VALUES($1,$2,'Другой получатель','000000******5678','5678')", []interface{}{otherCard, bank}},
		{"INSERT INTO merchants(id,code,name) VALUES($1,'OTHER','Другой мерчант')", []interface{}{otherMerchant}},
		{"INSERT INTO payment_requests(id,merchant_id,external_ref,mode,payment_count,created_by) VALUES($1,$2,'ЗК-ТЕСТ','cards',2,$3)", []interface{}{requestID, merchant, chief.ID}},
		{"INSERT INTO payment_contacts(id,full_name,phone,created_by) VALUES($1,'Контакт Справочника','+79990000003',$2)", []interface{}{contact, chief.ID}},
		{"INSERT INTO payment_request_rows(id,request_id,row_no,card_id,contact_id,contact_name,contact_phone) VALUES($1,$2,1,$3,$6,'Снимок Получателя','+79990000001'),($4,$2,2,$5,$6,'Второй Получатель','+79990000002')", []interface{}{id(), requestID, card, id(), otherCard, contact}},
	} {
		if _, err := a.db.Exec(q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}
	input := func() M {
		return M{"merchant_id": merchant, "payment_request_id": requestID, "payment_request_version": 1, "payment_date": testPaymentDate(), "idempotency_key": id(), "rows": []M{{"payment_request_row_no": 1, "card_id": card, "amount": "1234,56"}}}
	}
	for _, tc := range []struct {
		name   string
		change func(M)
		code   int
	}{
		{"wrong merchant", func(m M) { m["merchant_id"] = otherMerchant }, 400},
		{"stale version", func(m M) { m["payment_request_version"] = 2 }, 409},
		{"missing version", func(m M) { delete(m, "payment_request_version") }, 409},
		{"invalid request", func(m M) { m["payment_request_id"] = "invalid" }, 400},
		{"missing request", func(m M) { m["payment_request_id"] = id() }, 400},
		{"card outside row", func(m M) { m["rows"] = []M{{"payment_request_row_no": 1, "card_id": otherCard, "amount": "100"}} }, 400},
		{"unknown row", func(m M) { m["rows"] = []M{{"payment_request_row_no": 3, "card_id": card, "amount": "100"}} }, 400},
		{"empty amount", func(m M) { m["rows"] = []M{{"payment_request_row_no": 1, "card_id": card, "amount": ""}} }, 400},
		{"negative amount", func(m M) { m["rows"] = []M{{"payment_request_row_no": 1, "card_id": card, "amount": "-1"}} }, 400},
		{"zero amount", func(m M) { m["rows"] = []M{{"payment_request_row_no": 1, "card_id": card, "amount": "0"}} }, 400},
		{"no payments", func(m M) { m["rows"] = []M{} }, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := input()
			tc.change(m)
			if code, result := req(t, a.createManualRegistry, chief, m); code != tc.code {
				t.Fatalf("%d %v", code, result)
			}
		})
	}
	if _, err := a.db.Exec("UPDATE payment_requests SET deleted_at=now(),deleted_by=$2 WHERE id=$1", requestID, chief.ID); err != nil {
		t.Fatal(err)
	}
	if code, result := req(t, a.createManualRegistry, chief, input()); code != 400 {
		t.Fatalf("deleted request %d %v", code, result)
	}
	if _, err := a.db.Exec("UPDATE payment_requests SET deleted_at=NULL,deleted_by=NULL WHERE id=$1", requestID); err != nil {
		t.Fatal(err)
	}
	m := input()
	code, result := req(t, a.createManualRegistry, chief, m)
	if code != 201 {
		t.Fatalf("create %d %v", code, result)
	}
	registryID := result["id"].(string)
	if code, retry := req(t, a.createManualRegistry, chief, m); code != 200 || retry["id"] != registryID {
		t.Fatalf("retry %d %v", code, retry)
	}
	changed := input()
	changed["idempotency_key"] = m["idempotency_key"]
	changed["rows"] = []M{{"payment_request_row_no": 2, "card_id": otherCard, "amount": "1234,56"}}
	if code, result := req(t, a.createManualRegistry, chief, changed); code != 409 {
		t.Fatalf("changed retry %d %v", code, result)
	}
	var linked, status string
	var total int64
	var count, entries int
	if err := a.db.QueryRow("SELECT payment_request_id,status,total_cents,(SELECT count(*) FROM registry_rows WHERE registry_id=r.id),(SELECT count(*) FROM journal_entries) FROM registries r WHERE id=$1", registryID).Scan(&linked, &status, &total, &count, &entries); err != nil {
		t.Fatal(err)
	}
	if linked != requestID || status != "preview" || total != 123456 || count != 1 || entries != 0 {
		t.Fatalf("unexpected linked preview: %s %s %d %d %d", linked, status, total, count, entries)
	}
	var raw []byte
	if err := a.db.QueryRow("SELECT raw FROM registry_rows WHERE registry_id=$1", registryID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var values []string
	if err := json.Unmarshal(raw, &values); err != nil || len(values) != 4 || values[2] != "Снимок Получателя" || values[3] != "+79990000001" {
		t.Fatalf("request contact snapshot: %s %v", raw, err)
	}
	recorder := httptest.NewRecorder()
	a.paymentRequest(recorder, httptest.NewRequest("GET", "/api/payment-request?id="+requestID, nil), chief)
	var detail M
	if err := json.Unmarshal(recorder.Body.Bytes(), &detail); err != nil || recorder.Code != 200 || detail["response_count"] != float64(1) {
		t.Fatalf("linked count %d %v %v", recorder.Code, detail, err)
	}
	tx, err := a.tx()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = lockEditableRequest(tx, requestID); err == nil {
		t.Fatal("linked request must not be editable")
	}
}
