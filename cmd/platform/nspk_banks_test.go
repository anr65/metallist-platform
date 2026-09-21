package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"
	"time"
)

func TestOfficialNSPKBankFeed(t *testing.T) {
	if os.Getenv("NSPK_LIVE_TEST") != "1" {
		t.Skip("opt-in read-only check of the official NSPK feed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	banks, err := fetchNSPKBanks(ctx, officialNSPKClient(), nspkBanksURL)
	if err != nil || len(banks) < 200 {
		t.Fatal("official NSPK feed unavailable or incomplete", len(banks), err)
	}
	t.Logf("official NSPK feed: %d banks", len(banks))
}

func TestNSPKBankSyncPreservesExistingCards(t *testing.T) {
	a := testApp(t)
	_, _, legacyBank, _ := fixtures(t, a)
	count := 201
	badFeed := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		if limit != 500 {
			t.Errorf("wrong page size: %d", limit)
		}
		page := nspkBankPage{}
		page.Meta.Offset = offset
		page.Meta.Total = count
		if badFeed {
			page.Meta.Total = 3
		}
		for i := offset; i < count && i < offset+limit; i++ {
			page.Data = append(page.Data, nspkBank{ID: fmt.Sprintf("%08x-0000-4000-8000-%012x", i+1, i+1), Title: fmt.Sprintf("Банк %03d", i+1)})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(page)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if imported, err := a.syncNSPKBanks(ctx, server.Client(), server.URL); err != nil || imported != 201 {
		t.Fatal("initial sync failed", imported, err)
	}
	var source, legacyName string
	if err := a.db.QueryRow("SELECT source,name FROM banks WHERE id=$1", legacyBank).Scan(&source, &legacyName); err != nil || source != "manual" {
		t.Fatal("existing bank changed", source, err)
	}
	var importedBank string
	if err := a.db.QueryRow("SELECT id FROM banks WHERE external_id=$1", "000000c9-0000-4000-8000-0000000000c9").Scan(&importedBank); err != nil {
		t.Fatal(err)
	}
	cardID := id()
	if _, err := a.db.Exec("INSERT INTO cards(id,bank_id,owner_label,mask,last4) VALUES($1,$2,'Тестовая карта','400000******0002','0002')", cardID, importedBank); err != nil {
		t.Fatal(err)
	}
	count = 200
	if imported, err := a.syncNSPKBanks(ctx, server.Client(), server.URL); err != nil || imported != 200 {
		t.Fatal("second sync failed", imported, err)
	}
	var selectable bool
	if err := a.db.QueryRow("SELECT selectable FROM banks WHERE id=$1", importedBank).Scan(&selectable); err != nil || selectable {
		t.Fatal("removed bank remained selectable", selectable, err)
	}
	var retainedCard string
	if err := a.db.QueryRow("SELECT id FROM cards WHERE id=$1 AND bank_id=$2", cardID, importedBank).Scan(&retainedCard); err != nil || retainedCard != cardID {
		t.Fatal("existing card lost its bank", err)
	}
	badFeed = true
	if _, err := a.syncNSPKBanks(ctx, server.Client(), server.URL); err == nil {
		t.Fatal("incomplete feed accepted")
	}
	var active, total, audits int
	if err := a.db.QueryRow("SELECT count(*) FILTER (WHERE selectable),count(*) FROM banks WHERE source='nspk_sbp'").Scan(&active, &total); err != nil || active != 200 || total != 201 {
		t.Fatal("failed feed changed catalog", active, total, err)
	}
	if err := a.db.QueryRow("SELECT count(*) FROM audit_events WHERE action='nspk_bank_sync' AND outcome='success'").Scan(&audits); err != nil || audits != 2 {
		t.Fatal("sync audit missing", audits, err)
	}
}
