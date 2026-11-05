package main

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestDropdownIntegrationSourcesAndPermissions(t *testing.T) {
	a := testApp(t)
	chief, merchant, bank, card := fixtures(t, a)
	contact, request, source, registry := id(), id(), id(), id()
	statements := []struct {
		query string
		args  []interface{}
	}{
		{"INSERT INTO payment_contacts(id,full_name,phone) VALUES($1,'Тестовый Получатель','+79990000001')", []interface{}{contact}},
		{"INSERT INTO payment_requests(id,merchant_id,external_ref,title,mode,payment_count,created_by,created_at) VALUES($1,$2,'ЗК-СТАРЫЙ','Архивный запрос','cards',1,$3,now()-interval '1 year')", []interface{}{request, merchant, chief.ID}},
		{"INSERT INTO payment_request_rows(id,request_id,row_no,card_id,contact_id,contact_name,contact_phone) VALUES($1,$2,1,$3,$4,'Тестовый Получатель','+79990000001')", []interface{}{id(), request, card, contact}},
		{"INSERT INTO payment_requests(id,merchant_id,external_ref,mode,payment_count,created_by) SELECT md5('dropdown-'||i)::uuid,$1,'ЗК-'||i,'cards',1,$2 FROM generate_series(1,125) i", []interface{}{merchant, chief.ID}},
		{"INSERT INTO source_documents(id,kind,filename,sha256,media_type,byte_size,storage_path,uploader_id) VALUES($1,'xlsx','anonymous.xlsx',$2,'application/xlsx',1,'/tmp/anonymous',$3)", []interface{}{source, id(), chief.ID}},
		{"INSERT INTO registries(id,merchant_id,source_id,external_ref,status,total_cents,payment_date,payment_request_id) VALUES($1,$2,$3,'Тестовый реестр','preview',100,current_date,$4)", []interface{}{registry, merchant, source, request}},
	}
	for _, stmt := range statements {
		if _, err := a.db.Exec(stmt.query, stmt.args...); err != nil {
			t.Fatal(err)
		}
	}
	var custodian, handlerID string
	if err := a.db.QueryRow("SELECT id FROM custodians WHERE kind='collector'").Scan(&custodian); err != nil {
		t.Fatal(err)
	}
	if err := a.db.QueryRow("SELECT id FROM drop_handlers LIMIT 1").Scan(&handlerID); err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{"chief", "sysadmin", "operator", "accountant", "auditor", "collector"} {
		for _, tc := range []struct {
			source, q, value string
			restricted       bool
		}{
			{"catalog/merchants", "вымышленный", merchant, false},
			{"catalog/banks", "вымышленный", bank, false},
			{"catalog/cards", "1234", card, false},
			{"catalog/custodians", "Сборщик", custodian, false},
			{"catalog/drop_handlers", "Сборщик", handlerID, false},
			{"catalog/request_cards", "1234", card, true},
			{"catalog/payment_contacts", "получатель", contact, true},
			{"manual-cards", "1234", card, true},
			{"requests", "архивный", request, false},
			{"registry-candidates", "получатель", card, true},
		} {
			body, _ := json.Marshal(dropdownSearchInput{Query: tc.q, Source: tc.source, Context: registry, Options: []dropdownOption{{Value: tc.value, Label: "Вариант"}, {Value: id(), Label: tc.q}}})
			w := httptest.NewRecorder()
			a.dropdownSearch(w, httptest.NewRequest("POST", "/api/dropdown/search", bytes.NewReader(body)), User{ID: chief.ID, Role: role})
			denied := role == "collector" && tc.source != "catalog/cards" || tc.restricted && role != "chief" && role != "sysadmin" && role != "operator"
			if denied {
				if w.Code != 403 {
					t.Fatalf("%s %s: want 403, got %d", role, tc.source, w.Code)
				}
				continue
			}
			var result struct {
				Values []string `json:"values"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if w.Code != 200 || len(result.Values) != 1 || result.Values[0] != tc.value {
				t.Fatalf("%s %s: %d %s", role, tc.source, w.Code, w.Body.String())
			}
		}
	}
	var count int
	if err := a.db.QueryRow("SELECT count(*) FROM journal_entries").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("read-only search wrote %d journal entries", count)
	}
}
