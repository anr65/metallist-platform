package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/xuri/excelize/v2"
	"golang.org/x/text/encoding/charmap"
)

func testApp(t *testing.T) *App {
	t.Helper()
	if os.Getenv("APP_ENV") != "testing" {
		t.Fatal("APP_ENV must be testing")
	}
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" || dsn != os.Getenv("DATABASE_URL") {
		t.Fatal("explicit test database required")
	}
	cfg, e := pgx.ParseConfig(dsn)
	if e != nil {
		t.Fatal(e)
	}
	if cfg.Host != "127.0.0.1" || cfg.Port != 55434 || cfg.Database != "metallist_platform_test" || cfg.User != "metallist_qa" {
		t.Fatal("unsafe database identity")
	}
	db := sql.OpenDB(stdlib.GetConnector(*cfg))
	t.Cleanup(func() { db.Close() })
	var name, user string
	if e = db.QueryRow("SELECT current_database(),current_user").Scan(&name, &user); e != nil {
		t.Fatal(e)
	}
	if name != "metallist_platform_test" || user != "metallist_qa" {
		t.Fatal("effective database mismatch")
	}
	loc, _ := time.LoadLocation("Europe/Moscow")
	return &App{db: db, storage: t.TempDir(), location: loc}
}
func reset(t *testing.T, a *App) {
	t.Helper()
	_, e := a.db.Exec("TRUNCATE telegram_updates,report_approvals,audit_events,postings,journal_entries,drafts,observations,manual_rate_confirmations,tariff_confirmations,tariff_adjustments,registry_rows,registries,source_documents,cards,custodians,banks,tariffs,merchants,sessions,users CASCADE")
	if e != nil {
		t.Fatal(e)
	}
}
func req(t *testing.T, fn handler, u User, m M) (int, M) {
	t.Helper()
	b, _ := json.Marshal(m)
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/", bytes.NewReader(b))
	fn(w, r, u)
	var out M
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}
func fixtures(t *testing.T, a *App) (User, string, string, string) {
	t.Helper()
	reset(t, a)
	u := User{ID: id(), Login: "chief", Name: "Chief", Role: "chief"}
	merchant, bank, card := id(), id(), id()
	for _, q := range []struct {
		sql  string
		args []interface{}
	}{
		{"INSERT INTO users(id,login,name,role,password_hash) VALUES($1,'chief','Chief','chief','x')", []interface{}{u.ID}},
		{"INSERT INTO merchants(id,code,name) VALUES($1,'FAKE','Вымышленный мерчант')", []interface{}{merchant}},
		{"INSERT INTO tariffs(id,merchant_id,rate_bp,valid_from,created_by) VALUES($1,$2,400,'2026-01-01',$3)", []interface{}{id(), merchant, u.ID}},
		{"INSERT INTO banks(id,code,name) VALUES($1,'FAKEBANK','Вымышленный банк')", []interface{}{bank}},
		{"INSERT INTO cards(id,bank_id,owner_label,mask,last4) VALUES($1,$2,'Тестовый владелец','000000******1234','1234')", []interface{}{card, bank}},
		{"INSERT INTO custodians(id,name,kind) VALUES($1,'Главный','chief')", []interface{}{id()}},
		{"INSERT INTO custodians(id,name,kind) VALUES($1,'Сборщик А','collector')", []interface{}{id()}},
	} {
		if _, e := a.db.Exec(q.sql, q.args...); e != nil {
			t.Fatal(e)
		}
	}
	return u, merchant, bank, card
}
func TestMoneyExact(t *testing.T) {
	if fee(123464, 400) != 4939 || fee(98770, 500) != 4939 {
		t.Fatal("half up")
	}
	for _, s := range []string{"1.001", "-1", "0.00", "1e5"} {
		if _, e := amount(s); e == nil {
			t.Fatalf("accepted %s", s)
		}
	}
	v, e := amount("49,39")
	if e != nil || v != 4939 {
		t.Fatal(v, e)
	}
}
func TestLedgerInvariantAndImmutability(t *testing.T) {
	a := testApp(t)
	u, merchant, _, card := fixtures(t, a)
	tx, e := a.tx()
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	entry, e := put(tx, "test", id(), "unique-entry", u.ID, time.Now(), time.Now(), []Posting{{Account: "1100", Side: "debit", Amount: 10000, Card: card}, {Account: "2100", Side: "credit", Amount: 9600, Merchant: merchant}, {Account: "4100", Side: "credit", Amount: 400, Merchant: merchant}}, "")
	if e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	if _, e = a.db.Exec("UPDATE postings SET amount_cents=1 WHERE entry_id=$1", entry); e == nil {
		t.Fatal("posted rows mutable")
	}
	tx, e = a.tx()
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	if _, e = put(tx, "test", id(), "unique-entry", u.ID, time.Now(), time.Now(), []Posting{{Account: "1100", Side: "debit", Amount: 100, Card: card}, {Account: "2100", Side: "credit", Amount: 100, Merchant: merchant}}, ""); e == nil {
		t.Fatal("duplicate accepted")
	}
}
func TestRegistryRateReversalAndReports(t *testing.T) {
	a := testApp(t)
	u, merchant, _, card := fixtures(t, a)
	source, reg := id(), id()
	_, e := a.db.Exec("INSERT INTO source_documents(id,kind,filename,sha256,media_type,byte_size,storage_path,uploader_id) VALUES($1,'xlsx','fake.xlsx',$2,'application/xlsx',1,'/tmp/fake',$3)", source, id(), u.ID)
	if e != nil {
		t.Fatal(e)
	}
	_, e = a.db.Exec("INSERT INTO registries(id,merchant_id,source_id,external_ref,status,total_cents) VALUES($1,$2,$3,'R1','preview',100000000)", reg, merchant, source)
	if e != nil {
		t.Fatal(e)
	}
	for i := 1; i <= 2; i++ {
		_, e = a.db.Exec("INSERT INTO registry_rows(id,registry_id,row_no,raw,card_id,amount_cents) VALUES($1,$2,$3,'[]',$4,50000000)", id(), reg, i, card)
		if e != nil {
			t.Fatal(e)
		}
	}
	code, _ := req(t, a.confirmRegistry, u, M{"id": reg, "version": "1", "confirm_total": "1000000.00", "confirm_commission": "40000.00", "confirm_rate_bp": "400"})
	if code != 200 {
		t.Fatal("confirm", code)
	}
	code, _ = req(t, a.confirmRegistry, u, M{"id": reg, "version": "1", "confirm_total": "1000000.00", "confirm_commission": "40000.00", "confirm_rate_bp": "400"})
	if code != 200 {
		t.Fatal("retry", code)
	}
	tx, _ := a.tx()
	debt, _ := balance(tx, "2100", "merchant", merchant)
	cash, _ := balance(tx, "1100", "card", card)
	tx.Rollback()
	if debt != 96000000 || cash != 100000000 {
		t.Fatal(debt, cash)
	}
	month := time.Now().In(a.location).Format("2006-01")
	before, e := a.monthReport(month)
	if e != nil {
		t.Fatal(e)
	}
	accountant := User{ID: id(), Role: "accountant"}
	_, e = a.db.Exec("INSERT INTO users(id,login,name,role,password_hash) VALUES($1,'accountant','Accountant','accountant','x')", accountant.ID)
	if e != nil {
		t.Fatal(e)
	}
	code, approved := req(t, a.approveReport, accountant, M{"month": month, "digest": before["digest"]})
	if code != 200 {
		t.Fatal(approved)
	}
	previewCode, preview := req(t, a.tariffPreview, u, M{"merchant_id": merchant, "rate_bp": "500", "valid_from": "2026-01-01"})
	if previewCode != 200 {
		t.Fatal(preview)
	}
	code, result := req(t, a.tariffConfirm, u, M{"merchant_id": merchant, "rate_bp": "500", "valid_from": "2026-01-01", "preview_hash": preview["preview_hash"]})
	if code != 200 {
		t.Fatal(result)
	}
	tx, _ = a.tx()
	debt, _ = balance(tx, "2100", "merchant", merchant)
	tx.Rollback()
	if debt != 95000000 {
		t.Fatal("tariff debt", debt)
	}
	after, e := a.monthReport(month)
	if e != nil {
		t.Fatal(e)
	}
	if after["approved"] != false || after["digest"] == before["digest"] {
		t.Fatal("report approval was incorrectly inherited")
	}
	var prior int
	_ = a.db.QueryRow("SELECT count(*) FROM report_approvals WHERE month=$1::date", month+"-01").Scan(&prior)
	if prior != 1 {
		t.Fatal("historical approval lost")
	}
	code, result = req(t, a.reverseRegistry, u, M{"id": reg, "reason": "тестовая ошибка"})
	if code != 200 {
		t.Fatal(result)
	}
	tx, _ = a.tx()
	debt, _ = balance(tx, "2100", "merchant", merchant)
	cash, _ = balance(tx, "1100", "card", card)
	tx.Rollback()
	if debt != 0 || cash != 0 {
		t.Fatal("reverse", debt, cash)
	}
	rows, e := a.db.Query("SELECT entry_id FROM postings GROUP BY entry_id HAVING SUM(CASE WHEN side='debit' THEN amount_cents ELSE -amount_cents END)<>0")
	if e != nil {
		t.Fatal(e)
	}
	if rows.Next() {
		t.Fatal("unbalanced entry")
	}
	rows.Close()
}
func TestImportRepeatedRowsAndMalformed(t *testing.T) {
	data := []byte("ignored;meta\nМаскированный номер карты;Сумма операции;Комиссия Банка;К перечислению\n000000******1234;100,00;1,00;101,00\n000000******1234;100,00;1,00;101,00\n")
	encoded, err := charmap.Windows1251.NewEncoder().Bytes(data)
	if err != nil {
		t.Fatal(err)
	}
	rows, e := parseRows("bank_csv", encoded)
	if e != nil || len(rows) != 2 || rows[0].Error != "" || rows[1].Error != "" {
		t.Fatal(rows, e)
	}
	bad := []byte("x\nКарта;Сумма\n000000******1234;1.001\n")
	_, e = parseRows("bank_csv", bad)
	if e == nil {
		t.Fatal("invalid headers accepted")
	}
}
func TestRoleDenied(t *testing.T) {
	a := testApp(t)
	fixtures(t, a)
	operator := User{ID: id(), Role: "operator"}
	code, _ := req(t, a.tariffConfirm, operator, M{})
	if code != 403 {
		t.Fatal(code)
	}
}
func TestManualRateAndDraftMoney(t *testing.T) {
	a := testApp(t)
	u, merchant, _, card := fixtures(t, a)
	source, reg := id(), id()
	_, e := a.db.Exec("INSERT INTO source_documents(id,kind,filename,sha256,media_type,byte_size,storage_path,uploader_id) VALUES($1,'xlsx','fake.xlsx',$2,'application/xlsx',1,'/tmp/fake',$3)", source, id(), u.ID)
	if e != nil {
		t.Fatal(e)
	}
	_, e = a.db.Exec("INSERT INTO registries(id,merchant_id,source_id,external_ref,status,total_cents,manual_rate_bp,manual_reason,manual_approved_by,manual_approved_at) VALUES($1,$2,$3,'R2','preview',10000000,400,'one off',$4,now())", reg, merchant, source, u.ID)
	if e != nil {
		t.Fatal(e)
	}
	_, e = a.db.Exec("INSERT INTO registry_rows(id,registry_id,row_no,raw,card_id,amount_cents) VALUES($1,$2,2,'[]',$3,10000000)", id(), reg, card)
	if e != nil {
		t.Fatal(e)
	}
	code, x := req(t, a.confirmRegistry, u, M{"id": reg, "version": "1", "confirm_total": "100000.00", "confirm_commission": "4000.00", "confirm_rate_bp": "400"})
	if code != 200 {
		t.Fatal(x)
	}
	code, x = req(t, a.manualPreview, u, M{"registry_id": reg, "rate_bp": "450", "reason": "исправление"})
	if code != 200 {
		t.Fatal(x)
	}
	code, y := req(t, a.manualConfirm, u, M{"registry_id": reg, "rate_bp": "450", "reason": "исправление", "preview_hash": x["preview_hash"]})
	if code != 200 {
		t.Fatal(y)
	}
	tx, _ := a.tx()
	debt, _ := balance(tx, "2100", "merchant", merchant)
	tx.Rollback()
	if debt != 9550000 {
		t.Fatal(debt)
	}
	code, y = req(t, a.tariffPreview, u, M{"merchant_id": merchant, "rate_bp": "500", "valid_from": "2026-01-01"})
	if code != 200 {
		t.Fatal(y)
	}
	items := y["items"].([]interface{})
	if items[0].(map[string]interface{})["excluded"] != "manual_rate" {
		t.Fatal(y)
	}
	var collector string
	_ = a.db.QueryRow("SELECT id FROM custodians WHERE kind='collector'").Scan(&collector)
	code, y = req(t, a.draft, u, M{"kind": "withdrawal", "amount": "30000.00", "card_id": card, "custodian_id": collector})
	if code != 201 {
		t.Fatal(y)
	}
	draftID := y["id"].(string)
	code, y = req(t, a.confirmDraft, u, M{"id": draftID, "version": "1", "confirm_amount": "30000.00"})
	if code != 200 {
		t.Fatal(y)
	}
	code, y = req(t, a.confirmDraft, u, M{"id": draftID, "version": "1", "confirm_amount": "30000.00"})
	if code != 200 {
		t.Fatal(y)
	}
	tx, _ = a.tx()
	cash, _ := balance(tx, "1200", "custodian", collector)
	tx.Rollback()
	if cash != 3000000 {
		t.Fatal(cash)
	}
	code, y = req(t, a.reverseDraft, u, M{"id": draftID, "reason": "тест"})
	if code != 200 {
		t.Fatal(y)
	}
	tx, _ = a.tx()
	cash, _ = balance(tx, "1200", "custodian", collector)
	tx.Rollback()
	if cash != 0 {
		t.Fatal(cash)
	}
}
func TestShortageWriteoffRecoveryAndSurplus(t *testing.T) {
	a := testApp(t)
	u, _, _, _ := fixtures(t, a)
	var collector, chief string
	_ = a.db.QueryRow("SELECT id FROM custodians WHERE kind='collector'").Scan(&collector)
	_ = a.db.QueryRow("SELECT id FROM custodians WHERE kind='chief'").Scan(&chief)
	tx, _ := a.tx()
	_, e := put(tx, "test_funding", id(), id(), u.ID, time.Now(), time.Now(), []Posting{{Account: "1200", Side: "debit", Amount: 1000000, Custodian: collector}, {Account: "2200", Side: "credit", Amount: 1000000, Custodian: collector}}, "")
	if e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	postDraft := func(kind string, p M) string {
		p["kind"] = kind
		code, out := req(t, a.draft, u, p)
		if code != 201 {
			t.Fatal(out)
		}
		draftID := out["id"].(string)
		code, out = req(t, a.confirmDraft, u, M{"id": draftID, "version": "1", "confirm_amount": p["amount"]})
		if code != 200 {
			t.Fatal(kind, out)
		}
		return draftID
	}
	shortage := postDraft("shortage", M{"custodian_id": collector, "amount": "2000.00", "reason": "демо"})
	postDraft("collection", M{"receivable_account": "1400", "custodian_id": collector, "shortage_id": shortage, "destination_kind": "cash", "destination_id": chief, "amount": "500.00"})
	writeoff := postDraft("writeoff", M{"custodian_id": collector, "shortage_id": shortage, "amount": "1500.00", "reason": "невзыскуемо"})
	postDraft("recovery", M{"writeoff_id": writeoff, "destination_kind": "cash", "destination_id": chief, "amount": "500.00", "reason": "поздний возврат"})
	code, out := req(t, a.draft, u, M{"kind": "recovery", "writeoff_id": writeoff, "destination_kind": "cash", "destination_id": chief, "amount": "1100.00", "reason": "лишнее"})
	if code != 201 {
		t.Fatal(out)
	}
	code, _ = req(t, a.confirmDraft, u, M{"id": out["id"], "version": "1", "confirm_amount": "1100.00"})
	if code != 409 {
		t.Fatal("excess recovery accepted")
	}
	surplus := postDraft("surplus", M{"custodian_id": chief, "amount": "300.00"})
	postDraft("surplus_income", M{"source_ref": surplus, "amount": "300.00", "reason": "не установлен"})
	tx, _ = a.tx()
	other, _ := balance(tx, "4200", "ref", writeoff)
	unexplained, _ := balance(tx, "2300", "ref", surplus)
	tx.Rollback()
	if other != 50000 || unexplained != 0 {
		t.Fatal(other, unexplained)
	}
}
func TestPartialHandoverAndObservedDifference(t *testing.T) {
	a := testApp(t)
	u, _, _, card := fixtures(t, a)
	var collector, chief string
	_ = a.db.QueryRow("SELECT id FROM custodians WHERE kind='collector'").Scan(&collector)
	_ = a.db.QueryRow("SELECT id FROM custodians WHERE kind='chief'").Scan(&chief)
	tx, _ := a.tx()
	_, e := put(tx, "test_funding", id(), id(), u.ID, time.Now(), time.Now(), []Posting{{Account: "1200", Side: "debit", Amount: 100000, Custodian: collector}, {Account: "2200", Side: "credit", Amount: 100000, Custodian: collector}}, "")
	if e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	code, out := req(t, a.draft, u, M{"kind": "handover", "from_custodian_id": collector, "to_custodian_id": chief, "amount": "1000.00"})
	if code != 201 {
		t.Fatal(out)
	}
	draftID := out["id"].(string)
	code, out = req(t, a.confirmDraft, u, M{"id": draftID, "version": "1", "confirm_amount": "980.00"})
	if code != 200 {
		t.Fatal(out)
	}
	tx, _ = a.tx()
	left, _ := balance(tx, "1200", "custodian", collector)
	received, _ := balance(tx, "1210", "custodian", chief)
	tx.Rollback()
	if left != 2000 || received != 98000 {
		t.Fatal(left, received)
	}
	code, out = req(t, a.observation, u, M{"card_id": card, "amount": "0.00"})
	if code != 201 {
		t.Fatal(out)
	}
	w := httptest.NewRecorder()
	a.report(w, httptest.NewRequest("GET", "/api/report", nil), u)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var report M
	_ = json.Unmarshal(w.Body.Bytes(), &report)
	if len(report["handover_differences"].([]interface{})) != 1 || len(report["observations"].([]interface{})) != 1 {
		t.Fatal(report)
	}
}
func TestXLSXUploadPreviewAndDuplicate(t *testing.T) {
	a := testApp(t)
	u, merchant, _, _ := fixtures(t, a)
	file := excelize.NewFile()
	file.SetCellValue("Sheet1", "A1", "Карта")
	file.SetCellValue("Sheet1", "B1", "Сумма")
	file.SetCellValue("Sheet1", "A2", "000000******1234")
	file.SetCellValue("Sheet1", "B2", "1234.64")
	file.SetCellValue("Sheet1", "A3", "000000******1234")
	file.SetCellValue("Sheet1", "B3", "987.70")
	blob, e := file.WriteToBuffer()
	if e != nil {
		t.Fatal(e)
	}
	file.Close()
	upload := func(ref string) (int, M) {
		var body bytes.Buffer
		mw := multipart.NewWriter(&body)
		_ = mw.WriteField("merchant_id", merchant)
		_ = mw.WriteField("external_ref", ref)
		fw, _ := mw.CreateFormFile("file", "synthetic.xlsx")
		_, _ = fw.Write(blob.Bytes())
		mw.Close()
		r := httptest.NewRequest("POST", "/api/registry/upload", &body)
		r.Header.Set("Content-Type", mw.FormDataContentType())
		w := httptest.NewRecorder()
		a.upload(w, r, u)
		var result M
		_ = json.Unmarshal(w.Body.Bytes(), &result)
		return w.Code, result
	}
	code, out := upload("X1")
	if code != 201 {
		t.Fatal(out)
	}
	reg := out["id"].(string)
	rows := httptest.NewRecorder()
	a.registryRows(rows, httptest.NewRequest("GET", "/api/registry/rows?id="+reg, nil), u)
	if rows.Code != 200 {
		t.Fatal(rows.Body.String())
	}
	var items []M
	_ = json.Unmarshal(rows.Body.Bytes(), &items)
	if len(items) != 2 || items[0]["amount"] != "1234.64" || items[1]["amount"] != "987.70" {
		t.Fatal(items)
	}
	code, _ = upload("X2")
	if code != 409 {
		t.Fatal("duplicate file accepted")
	}
	code, out = req(t, a.confirmRegistry, u, M{"id": reg, "version": "1", "confirm_total": "2222.34", "confirm_commission": "88.89", "confirm_rate_bp": "400"})
	if code != 200 {
		t.Fatal(out)
	}
}
func TestAssignedCardsAndReadPermissions(t *testing.T) {
	a := testApp(t)
	_, _, _, card := fixtures(t, a)
	operator := User{ID: id(), Role: "operator"}
	admin := User{ID: id(), Role: "sysadmin"}
	_, e := a.db.Exec("INSERT INTO users(id,login,name,role,password_hash) VALUES($1,'op','Op','operator','x'),($2,'admin','Admin','sysadmin','x')", operator.ID, admin.ID)
	if e != nil {
		t.Fatal(e)
	}
	w := httptest.NewRecorder()
	a.catalog(w, httptest.NewRequest("GET", "/api/catalog", nil), operator)
	var c M
	_ = json.Unmarshal(w.Body.Bytes(), &c)
	if len(c["cards"].([]interface{})) != 0 {
		t.Fatal("unassigned card visible")
	}
	code, _ := req(t, a.observation, operator, M{"card_id": card, "amount": "0.00"})
	if code != 403 {
		t.Fatal(code)
	}
	code, out := req(t, a.assignCard, admin, M{"user_id": operator.ID, "card_id": card})
	if code != 200 {
		t.Fatal(out)
	}
	w = httptest.NewRecorder()
	a.catalog(w, httptest.NewRequest("GET", "/api/catalog", nil), operator)
	_ = json.Unmarshal(w.Body.Bytes(), &c)
	if len(c["cards"].([]interface{})) != 1 {
		t.Fatal("assigned card hidden")
	}
	code, out = req(t, a.observation, operator, M{"card_id": card, "amount": "0.00"})
	if code != 201 {
		t.Fatal(out)
	}
	w = httptest.NewRecorder()
	a.report(w, httptest.NewRequest("GET", "/api/report", nil), operator)
	if w.Code != 403 {
		t.Fatal("operator financial report allowed")
	}
}
func TestRetroactiveOverpaymentAndRegistryRollback(t *testing.T) {
	a := testApp(t)
	u, merchant, _, card := fixtures(t, a)
	var chief, collector string
	_ = a.db.QueryRow("SELECT id FROM custodians WHERE kind='chief'").Scan(&chief)
	_ = a.db.QueryRow("SELECT id FROM custodians WHERE kind='collector'").Scan(&collector)
	src, reg := id(), id()
	_, e := a.db.Exec("INSERT INTO source_documents(id,kind,filename,sha256,media_type,byte_size,storage_path,uploader_id) VALUES($1,'xlsx','fiction.xlsx',$2,'application/xlsx',1,'/tmp/fake',$3)", src, id(), u.ID)
	if e != nil {
		t.Fatal(e)
	}
	_, e = a.db.Exec("INSERT INTO registries(id,merchant_id,source_id,external_ref,status,total_cents) VALUES($1,$2,$3,'R3','preview',100000)", reg, merchant, src)
	if e != nil {
		t.Fatal(e)
	}
	_, e = a.db.Exec("INSERT INTO registry_rows(id,registry_id,row_no,raw,card_id,amount_cents) VALUES($1,$2,2,'[]',$3,100000)", id(), reg, card)
	if e != nil {
		t.Fatal(e)
	}
	code, out := req(t, a.confirmRegistry, u, M{"id": reg, "version": "1", "confirm_total": "1000.00", "confirm_commission": "40.00", "confirm_rate_bp": "400"})
	if code != 200 {
		t.Fatal(out)
	}
	post := func(kind string, m M) {
		m["kind"] = kind
		code, out := req(t, a.draft, u, m)
		if code != 201 {
			t.Fatal(out)
		}
		code, out = req(t, a.confirmDraft, u, M{"id": out["id"], "version": "1", "confirm_amount": m["amount"]})
		if code != 200 {
			t.Fatal(out)
		}
	}
	post("withdrawal", M{"card_id": card, "custodian_id": collector, "amount": "800.00"})
	post("handover", M{"from_custodian_id": collector, "to_custodian_id": chief, "amount": "800.00"})
	post("repayment", M{"merchant_id": merchant, "source_kind": "cash", "source_id": chief, "amount": "800.00"})
	code, preview := req(t, a.tariffPreview, u, M{"merchant_id": merchant, "rate_bp": "5000", "valid_from": "2026-01-01"})
	if code != 200 {
		t.Fatal(preview)
	}
	code, out = req(t, a.tariffConfirm, u, M{"merchant_id": merchant, "rate_bp": "5000", "valid_from": "2026-01-01", "preview_hash": preview["preview_hash"]})
	if code != 200 {
		t.Fatal(out)
	}
	code, out = req(t, a.tariffConfirm, u, M{"merchant_id": merchant, "rate_bp": "5000", "valid_from": "2026-01-01", "preview_hash": preview["preview_hash"]})
	if code != 200 || out["status"] != "already_posted" {
		t.Fatal(out)
	}
	tx, _ := a.tx()
	pay, _ := balance(tx, "2100", "merchant", merchant)
	rec, _ := balance(tx, "1300", "merchant", merchant)
	tx.Rollback()
	if pay != 0 || rec != 30000 {
		t.Fatal(pay, rec)
	}
	code, out = req(t, a.reverseRegistry, u, M{"id": reg, "reason": "тест"})
	if code != 200 {
		t.Fatal(out)
	}
	tx, _ = a.tx()
	pay, _ = balance(tx, "2100", "merchant", merchant)
	rec, _ = balance(tx, "1300", "merchant", merchant)
	cardBalance, _ := balance(tx, "1100", "card", card)
	tx.Rollback()
	if pay != 0 || rec != 80000 || cardBalance != -80000 {
		t.Fatal(pay, rec, cardBalance)
	}
	w := httptest.NewRecorder()
	a.report(w, httptest.NewRequest("GET", "/api/report", nil), u)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var report M
	_ = json.Unmarshal(w.Body.Bytes(), &report)
	if report["summary"].(map[string]interface{})["balance_difference"] != "0.00" {
		t.Fatal(report["summary"])
	}
}

var _ = http.MethodPost
var _ = strings.TrimSpace
