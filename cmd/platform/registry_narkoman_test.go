package main

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
)

func narkomanTestSheet(payment, total string) spreadsheetSheet {
	rows := make([][]string, 10)
	for i := range rows {
		rows[i] = make([]string, 21)
	}
	rows[6][2], rows[6][5], rows[6][17], rows[6][20] = "Дата док-та", "Номер док-та", "Обороты", "Назначение платежа"
	rows[7][17], rows[7][19] = "по дебету", "по кредиту"
	rows[8][2], rows[8][5], rows[8][17], rows[8][20] = "08.10.2026", "anonymous-order", payment, "Расчеты. Карта:*1234.\nКарта: *1234 TEST"
	rows[9][2], rows[9][17] = "Итого:", total
	return spreadsheetSheet{Name: "clb_stat_complete.jr", Rows: rows}
}

func TestNarkomanValidation(t *testing.T) {
	for _, tc := range []struct {
		name      string
		change    func(*spreadsheetSheet)
		fileError bool
		rowError  string
	}{
		{name: "same reference repeated"},
		{name: "bank card on second line", change: func(s *spreadsheetSheet) {
			s.Rows[8][20] = "Карта:*1234. Тестовый получатель.\nКарта: *9999 TEST"
		}},
		{name: "conflicting references", change: func(s *spreadsheetSheet) { s.Rows[8][20] = "Карта:*1234. Карта: *5678" }, rowError: "ambiguous_card_reference"},
		{name: "no reference", change: func(s *spreadsheetSheet) { s.Rows[8][20] = "Другое назначение" }, rowError: "missing_card_reference"},
		{name: "full PAN is not a short reference", change: func(s *spreadsheetSheet) { s.Rows[8][20] = "Карта:0000000000001234" }, rowError: "missing_card_reference"},
		{name: "invalid precision", change: func(s *spreadsheetSheet) { s.Rows[8][17] = "1.0010" }, rowError: "invalid_amount"},
		{name: "negative", change: func(s *spreadsheetSheet) { s.Rows[8][17] = "-1" }, rowError: "invalid_amount"},
		{name: "credit operation", change: func(s *spreadsheetSheet) { s.Rows[8][19] = "5" }, rowError: "unsupported_credit_operation"},
		{name: "total mismatch", change: func(s *spreadsheetSheet) { s.Rows[9][17] = "100.26" }, fileError: true},
		{name: "invalid total", change: func(s *spreadsheetSheet) { s.Rows[9][17] = "wrong" }, fileError: true},
		{name: "missing total", change: func(s *spreadsheetSheet) { s.Rows = s.Rows[:9] }, fileError: true},
		{name: "duplicate total", change: func(s *spreadsheetSheet) { s.Rows = append(s.Rows, s.Rows[9]) }, fileError: true},
		{name: "trailing payment", change: func(s *spreadsheetSheet) { s.Rows = append(s.Rows, s.Rows[8]) }, fileError: true},
		{name: "wrong debit column", change: func(s *spreadsheetSheet) { s.Rows[7][17] = "по кредиту" }, fileError: true},
		{name: "overflow", change: func(s *spreadsheetSheet) {
			s.Rows[8][17] = "999999999999999.99"
			footer := s.Rows[9]
			s.Rows = s.Rows[:9]
			for i := 0; i < 100; i++ {
				s.Rows = append(s.Rows, append([]string(nil), s.Rows[8]...))
			}
			s.Rows = append(s.Rows, footer)
		}, fileError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := narkomanTestSheet("100.25", "100.25")
			if tc.change != nil {
				tc.change(&s)
			}
			rows, e := parseNarkomanSheet(s)
			if tc.fileError {
				if e == nil {
					t.Fatal("invalid file accepted")
				}
				return
			}
			if e != nil || len(rows) != 1 || rows[0].Error != tc.rowError {
				t.Fatalf("rows=%v err=%v", rows, e)
			}
			if rows[0].Number != 9 || rows[0].Sheet != s.Name || !reflect.DeepEqual(rows[0].Raw, s.Rows[8]) {
				t.Fatal("source evidence lost")
			}
			if tc.rowError == "" && (rows[0].Mask != "****1234" || rows[0].Amount != 10025) {
				t.Fatal("wrong card or amount")
			}
		})
	}
}

func TestNarkomanAnonymousXLS(t *testing.T) {
	data, e := os.ReadFile("testdata/narkoman-avangard-anonymous.xls")
	if e != nil {
		t.Fatal(e)
	}
	rows, e := readAndParseRows("narkoman_avangard_xls_v1", ".xls", data)
	if e != nil || len(rows) != 3 {
		t.Fatal("fixture parse failed", e)
	}
	var total int64
	for i, r := range rows {
		if r.Error != "" || r.Number != i+9 {
			t.Fatal("invalid row", r.Number, r.Error)
		}
		total += r.Amount
	}
	if total != 40060 || rows[0].Mask != rows[2].Mask {
		t.Fatal("total or repeated payment lost")
	}
	if _, e := parseRows("narkoman_avangard_xls_v1", ".xlsx", data); e == nil {
		t.Fatal("wrong extension accepted")
	}
}

func TestNarkomanOctoberSample(t *testing.T) {
	path := os.Getenv("METALLIST_SAMPLE_NARKOMAN_OCTOBER")
	if path == "" {
		t.Skip("private sample not configured")
	}
	data, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	rows, e := readAndParseRows("narkoman_avangard_xls_v1", ".xls", data)
	if e != nil {
		t.Fatal(e)
	}
	var total int64
	for _, r := range rows {
		if r.Error != "" {
			t.Fatalf("source row %d: %s", r.Number, r.Error)
		}
		total += r.Amount
	}
	if len(rows) != 20 || total != 500000000 {
		t.Fatalf("rows=%d total=%d", len(rows), total)
	}
	t.Logf("rows=%d total_cents=%d", len(rows), total)
}

func TestRequestShortCardReferences(t *testing.T) {
	lookup := requestRegistryLookup{numbers: map[string]string{"0000000000001234": "a"}, masks: map[string]string{"000000******1234": "a"}, lastFour: map[string]string{}}
	addUniqueRequestReference(lookup.lastFour, "1234", "a")
	addUniqueRequestReference(lookup.lastFour, "1234", "a")
	if lookup.cardForReference("narkoman_avangard_xls_v1", "****1234") != "a" {
		t.Fatal("unique card not resolved")
	}
	for _, parser := range []string{"generic_xlsx_v1", "sveta_cards_xls_v1", "katya_payouts_xlsx_v1"} {
		if lookup.cardForReference(parser, "****1234") != "" {
			t.Fatal("other parser gained suffix matching")
		}
	}
	if lookup.cardForReference("narkoman_avangard_xls_v1", "0000000000009999") != "" {
		t.Fatal("unknown full number resolved")
	}
	addUniqueRequestReference(lookup.lastFour, "1234", "b")
	addUniqueRequestReference(lookup.lastFour, "1234", "a")
	if lookup.cardForReference("narkoman_avangard_xls_v1", "****1234") != "" {
		t.Fatal("collision resolved")
	}
	if lookup.cardForReference("generic_xlsx_v1", "0000000000001234") != "a" {
		t.Fatal("full PAN mapping changed")
	}
}

func TestNarkomanRegistryByRequest(t *testing.T) {
	a := testApp(t)
	chief, merchant, bank, first := fixtures(t, a)
	_, e := a.db.Exec("UPDATE merchant_import_profiles SET parser_code='narkoman_avangard_xls_v1' WHERE merchant_id=$1", merchant)
	if e != nil {
		t.Fatal(e)
	}
	second, outside := id(), id()
	for _, c := range []struct{ id, mask, last4, pan string }{{second, "220038******5678", "5678", "2200380000005678"}, {outside, "220038******1234", "1234", "2200380000001234"}} {
		if _, e = a.db.Exec("INSERT INTO cards(id,bank_id,owner_label,mask,last4) VALUES($1,$2,'Anonymous',$3,$4)", c.id, bank, c.mask, c.last4); e != nil {
			t.Fatal(e)
		}
		if e = savePAN(c.id, c.pan); e != nil {
			t.Fatal(e)
		}
	}
	requestID := id()
	if _, e = a.db.Exec("INSERT INTO payment_requests(id,merchant_id,external_ref,mode,payment_count,created_by) VALUES($1,$2,'NARKOMAN-TEST','cards',2,$3)", requestID, merchant, chief.ID); e != nil {
		t.Fatal(e)
	}
	for i, c := range []string{first, second} {
		if _, e = a.db.Exec("INSERT INTO payment_request_rows(id,request_id,row_no,card_id) VALUES($1,$2,$3,$4)", id(), requestID, i+1, c); e != nil {
			t.Fatal(e)
		}
	}
	data, e := os.ReadFile("testdata/narkoman-avangard-anonymous.xls")
	if e != nil {
		t.Fatal(e)
	}
	upload := func(ref string) (int, M) {
		var body bytes.Buffer
		mw := multipart.NewWriter(&body)
		for k, v := range map[string]string{"merchant_id": merchant, "payment_date": testPaymentDate(), "payment_request_id": requestID, "external_ref": ref} {
			mw.WriteField(k, v)
		}
		fw, _ := mw.CreateFormFile("file", "anonymous.xls")
		fw.Write(data)
		mw.Close()
		r := httptest.NewRequest(http.MethodPost, "/api/registry/upload", &body)
		r.Header.Set("Content-Type", mw.FormDataContentType())
		w := httptest.NewRecorder()
		a.upload(w, r, chief)
		var result M
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return w.Code, result
	}
	code, result := upload("NARKOMAN-UPLOAD")
	if code != 201 {
		t.Fatal(code, result)
	}
	registryID := result["id"].(string)
	var invalid, entries, version int
	var total int64
	if e = a.db.QueryRow("SELECT count(*) FILTER (WHERE error_code IS NOT NULL OR card_id NOT IN ($2,$3)),sum(amount_cents) FROM registry_rows WHERE registry_id=$1", registryID, first, second).Scan(&invalid, &total); e != nil || invalid != 0 || total != 40060 {
		t.Fatal("request mapping failed", invalid, total, e)
	}
	if e = a.db.QueryRow("SELECT count(*) FROM journal_entries").Scan(&entries); e != nil {
		t.Fatal(e)
	}
	if entries != 0 {
		t.Fatal("preview posted money")
	}
	if code, _ = upload("NARKOMAN-DUPLICATE"); code != 409 {
		t.Fatal("duplicate file accepted", code)
	}
	if e = a.db.QueryRow("SELECT parser_version FROM source_documents s JOIN registries r ON r.source_id=s.id WHERE r.id=$1", registryID).Scan(&version); e != nil {
		t.Fatal(e)
	}
	if version != 2 {
		t.Fatal("wrong parser snapshot", version)
	}
	code, result = req(t, a.confirmRegistry, chief, M{"id": registryID, "confirm_total": "400.60", "confirm_commission": "16.02", "confirm_rate_bp": "400", "version": "1"})
	if code != 200 {
		t.Fatal("confirm", code, result)
	}
	code, result = req(t, a.confirmRegistry, chief, M{"id": registryID, "confirm_total": "400.60", "confirm_commission": "16.02", "confirm_rate_bp": "400", "version": "1"})
	if code != 200 {
		t.Fatal("idempotent confirm", code, result)
	}
	tx, _ := a.tx()
	firstBalance, _ := balance(tx, "1100", "card", first)
	secondBalance, _ := balance(tx, "1100", "card", second)
	outsideBalance, _ := balance(tx, "1100", "card", outside)
	debt, _ := balance(tx, "2100", "merchant", merchant)
	tx.Rollback()
	if firstBalance != 20050 || secondBalance != 20010 || outsideBalance != 0 || debt != 38458 {
		t.Fatal("wrong postings", firstBalance, secondBalance, outsideBalance)
	}
	if code, result = req(t, a.reverseRegistry, chief, M{"id": registryID, "reason": "Anonymous test"}); code != 200 {
		t.Fatal("reverse", code, result)
	}
	tx, e = a.tx()
	if e != nil {
		t.Fatal(e)
	}
	reversedFirst, _ := balance(tx, "1100", "card", first)
	reversedSecond, _ := balance(tx, "1100", "card", second)
	reversedDebt, _ := balance(tx, "2100", "merchant", merchant)
	tx.Rollback()
	if reversedFirst != 0 || reversedSecond != 0 || reversedDebt != 0 {
		t.Fatal("reversal did not restore balances")
	}
	// Colliding suffix inside the request must block every affected payment.
	if _, e = a.db.Exec("INSERT INTO payment_request_rows(id,request_id,row_no,card_id) VALUES($1,$2,3,$3)", id(), requestID, outside); e != nil {
		t.Fatal(e)
	}
	code, result = upload("NARKOMAN-AMBIGUOUS")
	if code != 201 {
		t.Fatal(code, result)
	}
	registryID = result["id"].(string)
	if e = a.db.QueryRow("SELECT count(*) FROM registry_rows WHERE registry_id=$1 AND error_code='request_card_not_found_or_ambiguous'", registryID).Scan(&invalid); e != nil || invalid != 2 {
		t.Fatal("ambiguous rows accepted", invalid, e)
	}
	code, result = req(t, a.confirmRegistry, chief, M{"id": registryID, "confirm_total": "400.60", "confirm_commission": "16.02", "confirm_rate_bp": "400", "version": "1"})
	if code != 409 {
		t.Fatal("ambiguous registry posted", code, result)
	}
	var unbalanced int
	if e = a.db.QueryRow("SELECT count(*) FROM (SELECT entry_id FROM postings GROUP BY entry_id HAVING sum(CASE WHEN side='debit' THEN amount_cents ELSE -amount_cents END)<>0) x").Scan(&unbalanced); e != nil || unbalanced != 0 {
		t.Fatal("unbalanced ledger", e)
	}
	if strings.Contains(strings.Join(safeRaw([]string{"0000000000001234"}), ""), "0000000000001234") {
		t.Fatal("raw PAN leaked")
	}
}
