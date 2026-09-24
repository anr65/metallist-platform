package main

import "testing"

func TestCatalogUpdatePreservesCardNumberAndPermissions(t *testing.T) {
	a := testApp(t)
	chief, _, bankID, cardID := fixtures(t, a)
	operator := User{ID: id(), Role: "operator"}
	if _, err := a.db.Exec("INSERT INTO users(id,login,name,role,password_hash) VALUES($1,'catalog-operator','Операционист','operator','x')", operator.ID); err != nil {
		t.Fatal(err)
	}
	if code, _ := req(t, a.catalogUpdate, operator, M{"kind": "bank", "id": bankID, "code": "NEW", "name": "Новый банк"}); code != 403 {
		t.Fatalf("operator edited bank: %d", code)
	}
	if code, out := req(t, a.catalogUpdate, operator, M{"kind": "card", "id": cardID, "owner_label": "Новый владелец", "pan": "4111111111111111"}); code != 200 {
		t.Fatalf("card label update failed: %d %v", code, out)
	}
	var owner, mask string
	if err := a.db.QueryRow("SELECT owner_label,mask FROM cards WHERE id=$1", cardID).Scan(&owner, &mask); err != nil || owner != "Новый владелец" || mask != "000000******1234" {
		t.Fatalf("unexpected card state: %q %q %v", owner, mask, err)
	}
	if code, out := req(t, a.catalogUpdate, chief, M{"kind": "bank", "id": bankID, "code": "NEW", "name": "Новый банк"}); code != 200 {
		t.Fatalf("chief could not edit manual bank: %d %v", code, out)
	}
	if _, err := a.db.Exec("UPDATE banks SET source='nspk_sbp',external_id=$2 WHERE id=$1", bankID, id()); err != nil {
		t.Fatal(err)
	}
	if code, _ := req(t, a.catalogUpdate, chief, M{"kind": "bank", "id": bankID, "code": "EDIT", "name": "НСПК"}); code != 404 {
		t.Fatalf("NSPK bank was edited: %d", code)
	}
}
