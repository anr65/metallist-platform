package main

import (
	"os"
	"strings"
	"testing"
)

func TestImportCardPANsValidatesBatchAndResumes(t *testing.T) {
	a := testApp(t)
	_, _, _, card := fixtures(t, a)
	path, err := vaultPath(card)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.importCardPANs(strings.NewReader("0000000000061234\n0000000000061230\n")); err == nil {
		t.Fatal("invalid batch accepted")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("batch preflight wrote a card", err)
	}
	created, existing, err := a.importCardPANs(strings.NewReader("0000 0000 0006 1234\n"))
	if err != nil || created != 1 || existing != 0 {
		t.Fatal(created, existing, err)
	}
	created, existing, err = a.importCardPANs(strings.NewReader("0000000000061234\n"))
	if err != nil || created != 0 || existing != 1 {
		t.Fatal(created, existing, err)
	}
	var audits int
	if err := a.db.QueryRow("SELECT count(*) FROM audit_events WHERE action='card_pan_import' AND object_id=$1", card).Scan(&audits); err != nil || audits != 1 {
		t.Fatal(audits, err)
	}
}
