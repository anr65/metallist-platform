package main

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"
)

func TestRegistryFilteredPagesAndOperatorAccess(t *testing.T) {
	a := testApp(t)
	chief, merchant, _, card := fixtures(t, a)
	operator := User{ID: id(), Role: "operator"}
	if _, e := a.db.Exec("INSERT INTO users(id,login,name,role,password_hash) VALUES($1,'filters-operator','Operator','operator','x')", operator.ID); e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 120; i++ {
		source := id()
		uploader := chief.ID
		if i < 18 {
			uploader = operator.ID
		}
		status := "posted"
		if i >= 18 {
			status = "preview"
		}
		if _, e := a.db.Exec("INSERT INTO source_documents(id,kind,filename,sha256,media_type,byte_size,storage_path,uploader_id) VALUES($1,'xlsx','test.xlsx',$2,'application/xlsx',1,'/tmp/test',$3)", source, id(), uploader); e != nil {
			t.Fatal(e)
		}
		if _, e := a.db.Exec("INSERT INTO registries(id,merchant_id,source_id,external_ref,status,total_cents,payment_date,created_at) VALUES($1,$2,$3,$4,$5,100,'2026-10-01',now()+$6*interval '1 second')", id(), merchant, source, fmt.Sprintf("Реестр %03d", i), status, i); e != nil {
			t.Fatal(e)
		}
	}
	fetch := func(user User, query string) M {
		t.Helper()
		w := httptest.NewRecorder()
		a.registries(w, httptest.NewRequest("GET", "/api/registries?"+query, nil), user)
		if w.Code != 200 {
			t.Fatalf("%d: %s", w.Code, w.Body.String())
		}
		var out M
		if e := json.Unmarshal(w.Body.Bytes(), &out); e != nil {
			t.Fatal(e)
		}
		return out
	}
	first := fetch(chief, "page=1&status=posted&date_from=2026-10-01&date_to=2026-10-01&merchant_id="+merchant)
	if len(first["items"].([]any)) != 15 || first["has_more"] != true {
		t.Fatal(first)
	}
	second := fetch(operator, "page=2&status=posted")
	if len(second["items"].([]any)) != 3 || second["has_more"] != false {
		t.Fatal(second)
	}
	empty := fetch(operator, "page=1&status=preview")
	if len(empty["items"].([]any)) != 0 {
		t.Fatal("operator saw another uploader's registries")
	}
	found := fetch(chief, "page=1&q=000")
	if len(found["items"].([]any)) != 1 {
		t.Fatal("search missed old registry beyond latest 100")
	}
	var shared, private string
	if e := a.db.QueryRow("UPDATE registries SET status='posted' WHERE external_ref='Реестр 018' RETURNING id").Scan(&shared); e != nil {
		t.Fatal(e)
	}
	if e := a.db.QueryRow("SELECT id FROM registries WHERE external_ref='Реестр 019'").Scan(&private); e != nil {
		t.Fatal(e)
	}
	if _, e := a.db.Exec(`INSERT INTO registry_rows(id,registry_id,row_no,raw,card_id,amount_cents)
		VALUES($1,$2,2,'[]',$3,100)`, id(), shared, card); e != nil {
		t.Fatal(e)
	}
	a.registryReadGrants = map[string][]string{operator.ID: {shared, private}}
	visible := fetch(operator, "page=1&q=018")
	items := visible["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["id"] != shared {
		t.Fatal("granted registry is missing from operator search", visible)
	}
	detail := httptest.NewRecorder()
	a.registries(detail, httptest.NewRequest("GET", "/api/registries?id="+shared, nil), operator)
	var detailItems []M
	if e := json.Unmarshal(detail.Body.Bytes(), &detailItems); e != nil || detail.Code != 200 || len(detailItems) != 1 || detailItems[0]["id"] != shared {
		t.Fatal("granted registry detail is missing", detail.Code, detailItems, e)
	}
	if hidden := fetch(operator, "page=1&q=019"); len(hidden["items"].([]any)) != 0 {
		t.Fatal("unposted registry was shared", hidden)
	}
	for _, tc := range []struct {
		user User
		id   string
		code int
	}{
		{operator, shared, 200},
		{operator, private, 403},
		{User{ID: id(), Role: "operator"}, shared, 403},
	} {
		w := httptest.NewRecorder()
		a.registryRows(w, httptest.NewRequest("GET", "/api/registry/rows?id="+tc.id, nil), tc.user)
		if w.Code != tc.code {
			t.Fatalf("row access status %d, want %d: %s", w.Code, tc.code, w.Body.String())
		}
	}
	if code, _ := req(t, a.deleteRegistry, operator, M{"id": shared, "version": "1"}); code != 403 {
		t.Fatal("read access allowed deleting another operator's registry", code)
	}
}
