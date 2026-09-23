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
	"golang.org/x/crypto/bcrypt"
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
	vaultDir := t.TempDir()
	keyFile := t.TempDir() + "/pan.key"
	if e = os.WriteFile(keyFile, []byte(strings.Repeat("a", 64)), 0600); e != nil {
		t.Fatal(e)
	}
	t.Setenv("PAN_VAULT_DIR", vaultDir)
	t.Setenv("PAN_KEY_FILE", keyFile)
	loc, _ := time.LoadLocation("Europe/Moscow")
	return &App{db: db, storage: t.TempDir(), location: loc}
}
func reset(t *testing.T, a *App) {
	t.Helper()
	_, e := a.db.Exec("TRUNCATE telegram_dialogs,telegram_updates,report_approvals,audit_events,postings,journal_entries,drafts,observations,manual_rate_confirmations,tariff_confirmations,tariff_adjustments,registry_rows,registries,source_documents,payment_contacts,cards,custodians,banks,tariffs,merchants,sessions,users CASCADE")
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
		{"INSERT INTO merchant_import_profiles(merchant_id,parser_code,status) VALUES($1,'generic_xlsx_v1','configured')", []interface{}{merchant}},
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
	if e := savePAN(card, "0000000000061234"); e != nil {
		t.Fatal(e)
	}
	return u, merchant, bank, card
}

func TestManualRegistryCreationAndConfirmation(t *testing.T) {
	a := testApp(t)
	chief, merchant, _, card := fixtures(t, a)
	operator := User{ID: id(), Role: "operator"}
	contact := id()
	if _, e := a.db.Exec("INSERT INTO users(id,login,name,role,password_hash) VALUES($1,'manual-operator','Operator','operator','x')", operator.ID); e != nil {
		t.Fatal(e)
	}
	if _, e := a.db.Exec("INSERT INTO payment_contacts(id,full_name,phone,created_by) VALUES($1,'Тестовый Получатель','+79990000001',$2)", contact, chief.ID); e != nil {
		t.Fatal(e)
	}
	cardsRecorder := httptest.NewRecorder()
	a.manualRegistryCards(cardsRecorder, httptest.NewRequest("GET", "/api/registry/manual/cards", nil), operator)
	var availableCards []M
	if e := json.Unmarshal(cardsRecorder.Body.Bytes(), &availableCards); e != nil || cardsRecorder.Code != 200 || len(availableCards) != 1 || availableCards[0]["id"] != card {
		t.Fatalf("operator cards: %d %v %v", cardsRecorder.Code, availableCards, e)
	}
	key := id()
	input := M{"merchant_id": merchant, "idempotency_key": key, "rows": []M{{"card_id": card, "contact_id": contact, "amount": "1234,64"}}}
	if code, _ := req(t, a.createManualRegistry, User{ID: id(), Role: "accountant"}, input); code != 403 {
		t.Fatalf("accountant creation: %d", code)
	}
	if code, _ := req(t, a.createManualRegistry, operator, M{"merchant_id": merchant, "idempotency_key": id(), "rows": []M{{"card_id": card, "contact_id": contact, "amount": "0"}}}); code != 400 {
		t.Fatalf("invalid amount: %d", code)
	}
	code, result := req(t, a.createManualRegistry, operator, input)
	if code != 201 {
		t.Fatalf("create: %d %v", code, result)
	}
	registryID := result["id"].(string)
	if code, repeated := req(t, a.createManualRegistry, operator, input); code != 200 || repeated["id"] != registryID {
		t.Fatalf("retry: %d %v", code, repeated)
	}
	if code, _ := req(t, a.createManualRegistry, operator, M{"merchant_id": merchant, "idempotency_key": key, "rows": []M{{"card_id": card, "contact_id": contact, "amount": "1234,65"}}}); code != 409 {
		t.Fatalf("changed retry: %d", code)
	}
	if code, _ := req(t, a.createManualRegistry, operator, M{"merchant_id": merchant, "idempotency_key": id(), "rows": []M{{"card_id": card, "contact_id": id(), "amount": "100.00"}}}); code != 400 {
		t.Fatalf("unknown contact: %d", code)
	}
	var sourceKind, sourcePath, status string
	var rows, entries int
	if e := a.db.QueryRow("SELECT s.kind,s.storage_path,r.status,(SELECT count(*) FROM registry_rows WHERE registry_id=r.id),(SELECT count(*) FROM journal_entries) FROM registries r JOIN source_documents s ON s.id=r.source_id WHERE r.id=$1", registryID).Scan(&sourceKind, &sourcePath, &status, &rows, &entries); e != nil {
		t.Fatal(e)
	}
	storedSource, e := os.ReadFile(sourcePath)
	if e != nil || strings.Contains(string(storedSource), "+79990000001") {
		t.Fatalf("manual source must be stored encrypted: %v", e)
	}
	if sourceKind != "manual" || status != "preview" || rows != 1 || entries != 0 {
		t.Fatalf("unexpected preview: %s %s %d %d", sourceKind, status, rows, entries)
	}
	if code, _ := req(t, a.confirmRegistry, operator, M{"id": registryID, "version": "1", "confirm_total": "1234.64", "confirm_commission": "49.39", "confirm_rate_bp": "400"}); code != 403 {
		t.Fatalf("operator confirmation: %d", code)
	}
	code, result = req(t, a.confirmRegistry, chief, M{"id": registryID, "version": "1", "confirm_total": "1234.64", "confirm_commission": "49.39", "confirm_rate_bp": "400"})
	if code != 200 {
		t.Fatalf("chief confirmation: %d %v", code, result)
	}
	if e := a.db.QueryRow("SELECT count(*) FROM journal_entries WHERE event_type='registry' AND event_id=$1", registryID).Scan(&entries); e != nil {
		t.Fatal(e)
	}
	if entries != 1 {
		t.Fatalf("expected one posted entry, got %d", entries)
	}
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

func TestCredentialQueryIsNeverReflectedOrAccepted(t *testing.T) {
	app := &App{}
	secret := "synthetic-secret-never-in-location"
	page := httptest.NewRecorder()
	secureHeaders(http.HandlerFunc(app.ui)).ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/?login=example&password="+secret, nil))
	if page.Code != http.StatusSeeOther || page.Header().Get("Location") != "/" || page.Header().Get("Cache-Control") != "no-store" || page.Header().Get("Referrer-Policy") != "no-referrer" || strings.Contains(page.Body.String(), secret) {
		t.Fatal("credential URL not removed safely", page.Code, page.Header().Get("Location"))
	}
	called := false
	api := httptest.NewRecorder()
	secureHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true })).ServeHTTP(api, httptest.NewRequest(http.MethodGet, "/api/me?PASSWORD="+secret, nil))
	if called || api.Code != http.StatusBadRequest || strings.Contains(api.Body.String(), secret) {
		t.Fatal("credential URL reached API", api.Code)
	}
	clean := httptest.NewRecorder()
	secureHeaders(http.HandlerFunc(app.ui)).ServeHTTP(clean, httptest.NewRequest(http.MethodGet, "/", nil))
	if clean.Code != http.StatusOK {
		t.Fatal("clean page unavailable", clean.Code)
	}
}
func TestPANValidationAndEncryption(t *testing.T) {
	if e := validatePAN("0000000000061234", "000000******1234"); e != nil {
		t.Fatal(e)
	}
	if e := validatePAN("0000000000061234", "000000******9999"); e == nil {
		t.Fatal("mismatched PAN accepted")
	}
	if e := validatePAN("0000000000061230", "000000******1230"); e == nil {
		t.Fatal("invalid checksum accepted")
	}
	card := id()
	keyFile := t.TempDir() + "/pan.key"
	if e := os.WriteFile(keyFile, []byte(strings.Repeat("a", 64)), 0600); e != nil {
		t.Fatal(e)
	}
	t.Setenv("PAN_KEY_FILE", keyFile)
	t.Setenv("PAN_VAULT_DIR", t.TempDir())
	if e := savePAN(card, "0000000000061234"); e != nil {
		t.Fatal(e)
	}
	path, _ := vaultPath(card)
	data, e := os.ReadFile(path)
	if e != nil || bytes.Contains(data, []byte("0000000000061234")) {
		t.Fatal("PAN stored in plaintext", e)
	}
	read, e := loadPAN(card)
	if e != nil || read != "0000000000061234" {
		t.Fatal("PAN decrypt failed", e)
	}
	if e := savePAN(card, "0000000000061234"); e == nil {
		t.Fatal("PAN overwrite accepted")
	}
}

func TestCardPANCreationAndAccess(t *testing.T) {
	a := testApp(t)
	chief, _, bank, _ := fixtures(t, a)
	operator := User{ID: id(), Login: "new-card-operator", Name: "Операционист", Role: "operator"}
	if _, e := a.db.Exec("INSERT INTO users(id,login,name,role,password_hash) VALUES($1,$2,$3,'operator','x')", operator.ID, operator.Login, operator.Name); e != nil {
		t.Fatal(e)
	}
	body := M{"kind": "card", "bank_id": bank, "owner_label": "Тестовый владелец", "pan": "4111111111111111"}
	if status, _ := req(t, a.catalogCreate, User{Role: "accountant"}, body); status != 403 {
		t.Fatal("accountant added card", status)
	}
	if status, _ := req(t, a.catalogCreate, operator, M{"kind": "bank", "code": "NEW", "name": "Новый банк"}); status != 403 {
		t.Fatal("operator added bank", status)
	}
	if status, _ := req(t, a.catalogCreate, operator, M{"kind": "card", "bank_id": bank, "owner_label": "  ", "pan": "4111111111111111"}); status != 400 {
		t.Fatal("empty card owner accepted", status)
	}
	if _, e := a.db.Exec("UPDATE banks SET selectable=false WHERE id=$1", bank); e != nil {
		t.Fatal(e)
	}
	if status, _ := req(t, a.catalogCreate, operator, body); status != 400 {
		t.Fatal("unselectable bank accepted", status)
	}
	if _, e := a.db.Exec("UPDATE banks SET selectable=true WHERE id=$1", bank); e != nil {
		t.Fatal(e)
	}
	status, created := req(t, a.catalogCreate, operator, body)
	if status != 201 {
		t.Fatal("operator could not add card", status, created)
	}
	cardID := created["id"].(string)
	var mask string
	if e := a.db.QueryRow("SELECT mask FROM cards WHERE id=$1", cardID).Scan(&mask); e != nil || mask != "411111******1111" {
		t.Fatal("card mask mismatch", mask, e)
	}
	pan, e := loadPAN(cardID)
	if e != nil || pan != "4111111111111111" {
		t.Fatal("full number not encrypted and recoverable", e)
	}
	if status, _ := req(t, a.catalogCreate, operator, body); status != 400 {
		t.Fatal("duplicate card accepted", status)
	}
	var operatorAudit int
	if e := a.db.QueryRow("SELECT count(*) FROM audit_events WHERE actor_id=$1 AND action='catalog_create' AND object_type='card' AND outcome='success'", operator.ID).Scan(&operatorAudit); e != nil || operatorAudit != 1 {
		t.Fatal("operator card creation not audited", operatorAudit, e)
	}
	if status, _ := req(t, a.catalogCreate, chief, M{"kind": "card", "bank_id": bank, "owner_label": "Карта администратора", "pan": "4242424242424242"}); status != 201 {
		t.Fatal("chief could not add card", status)
	}
	if status, _ := req(t, a.setCardPAN, operator, M{"card_id": cardID, "pan": pan}); status != 403 {
		t.Fatal("operator changed full number", status)
	}
	if status, _ := req(t, a.setCardPAN, chief, M{"card_id": cardID, "pan": pan}); status != 409 {
		t.Fatal("full number overwrite accepted", status)
	}
	legacyID := id()
	if _, e := a.db.Exec("INSERT INTO cards(id,bank_id,owner_label,mask,last4) VALUES($1,$2,'Ранее заведённая карта','555555******4444','4444')", legacyID, bank); e != nil {
		t.Fatal(e)
	}
	if status, _ := req(t, a.setCardPAN, chief, M{"card_id": legacyID, "pan": "5555555555554444"}); status != 201 {
		t.Fatal("could not fill existing card number", status)
	}
	var auditText string
	if e := a.db.QueryRow("SELECT coalesce(string_agg(detail::text, ''),'') FROM audit_events").Scan(&auditText); e != nil || strings.Contains(auditText, pan) {
		t.Fatal("full number appeared in audit", e)
	}
}

func TestOperatorCorrectsUnusedCardPAN(t *testing.T) {
	a := testApp(t)
	chief, merchant, _, card := fixtures(t, a)
	operator := User{ID: id(), Login: "operator", Name: "Операционист", Role: "operator"}
	if _, err := a.db.Exec("INSERT INTO users(id,login,name,role,password_hash) VALUES($1,$2,$3,'operator','x')", operator.ID, operator.Login, operator.Name); err != nil {
		t.Fatal(err)
	}
	change := M{"card_id": card, "expected_mask": "000000******1234", "pan": "4111111111111111", "reason": "Исправление реквизитов"}
	if status, _ := req(t, a.replaceCardPAN, User{Role: "accountant"}, change); status != 403 {
		t.Fatal("accountant corrected card", status)
	}
	if status, _ := req(t, a.replaceCardPAN, operator, M{"card_id": card, "expected_mask": "000000******9999", "pan": "4111111111111111", "reason": "Исправление реквизитов"}); status != 409 {
		t.Fatal("stale mask accepted", status)
	}
	if status, _ := req(t, a.replaceCardPAN, operator, M{"card_id": card, "expected_mask": "000000******1234", "pan": "4111111111111111", "reason": "Номер 4111 1111 1111 1111 указан ошибочно"}); status != 400 {
		t.Fatal("PAN in audit reason accepted", status)
	}
	if status, _ := req(t, a.replaceCardPAN, operator, change); status != 200 {
		t.Fatal("operator could not correct card", status)
	}
	var mask string
	var ciphertext []byte
	if err := a.db.QueryRow("SELECT mask,pan_ciphertext FROM cards WHERE id=$1", card).Scan(&mask, &ciphertext); err != nil || mask != "411111******1111" || bytes.Contains(ciphertext, []byte("4111111111111111")) {
		t.Fatal("correction not stored securely", mask, err)
	}
	if pan, err := readCardPAN(card, ciphertext); err != nil || pan != "4111111111111111" {
		t.Fatal("corrected PAN not readable", err)
	}
	path, _ := vaultPath(card)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("old encrypted PAN file remained", err)
	}
	contact := id()
	if _, err := a.db.Exec("INSERT INTO payment_contacts(id,full_name,phone,created_by) VALUES($1,'Тестов Алексей Учебович','+70000000001',$2)", contact, chief.ID); err != nil {
		t.Fatal(err)
	}
	status, created := req(t, a.createPaymentRequest, chief, M{"merchant_id": merchant, "external_ref": "CORRECTED-PAN", "mode": "cards", "rows": []M{{"card_id": card, "contact_id": contact}}})
	if status != 201 {
		t.Fatal("corrected card request failed", status, created)
	}
	w := httptest.NewRecorder()
	a.paymentRequestExport(w, httptest.NewRequest("GET", "/api/payment-request/export?id="+created["id"].(string), nil), operator)
	if w.Code != 200 {
		t.Fatal("corrected PAN export failed", w.Code)
	}
	book, err := excelize.OpenReader(bytes.NewReader(w.Body.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	rows, err := book.GetRows("Карты к оплате")
	book.Close()
	if err != nil || len(rows) < 5 || rows[4][1] != "4111111111111111" {
		t.Fatal("corrected PAN export failed", err)
	}
	if status, _ := req(t, a.replaceCardPAN, operator, M{"card_id": card, "expected_mask": mask, "pan": "0000000000061234", "reason": "Повторное исправление"}); status != 409 {
		t.Fatal("used card was corrected", status)
	}
	var auditText string
	if err := a.db.QueryRow("SELECT coalesce(string_agg(detail::text, ''),'') FROM audit_events").Scan(&auditText); err != nil || strings.Contains(auditText, "4111111111111111") {
		t.Fatal("corrected PAN appeared in audit", err)
	}
}

func TestCardRequestRows(t *testing.T) {
	good := M{"rows": []interface{}{map[string]interface{}{"card_id": "card-1", "contact_id": "contact-1"}}}
	rows, e := cardRequestRows(good)
	if e != nil || len(rows) != 1 || rows[0].cardID != "card-1" {
		t.Fatal("valid card row rejected", rows, e)
	}
	duplicate := M{"rows": []interface{}{
		map[string]interface{}{"card_id": "card-1", "contact_id": "contact-1"},
		map[string]interface{}{"card_id": "card-1", "contact_id": "contact-1"},
	}}
	if _, e := cardRequestRows(duplicate); e == nil {
		t.Fatal("same card accepted twice")
	}
	if _, e := cardRequestRows(M{"rows": []interface{}{map[string]interface{}{"card_id": "card-1", "contact_id": "contact-1", "amount": "250000.00"}}}); e == nil {
		t.Fatal("amount accepted in card request")
	}
}

func TestPaymentRequestNextReferenceAndContactSearch(t *testing.T) {
	a := testApp(t)
	chief, merchant, _, _ := fixtures(t, a)
	w := httptest.NewRecorder()
	a.paymentRequestNextReference(w, httptest.NewRequest("GET", "/api/payment-request/next-reference", nil), chief)
	var next M
	_ = json.Unmarshal(w.Body.Bytes(), &next)
	if w.Code != 200 || next["next_reference"] != "ЗК-2026-0001" {
		t.Fatal("initial automatic reference", w.Code, next)
	}
	for _, ref := range []string{"ЗК-2026-0002", "Свободное название", "ЗК-2026-0010"} {
		if _, e := a.db.Exec("INSERT INTO payment_requests(id,merchant_id,external_ref,mode,payment_count,per_payment_cents,requested_total_cents,export_path,export_sha256,created_by) VALUES($1,$2,$3,'count',1,25000000,25000000,$4,'hash',$5)", id(), merchant, ref, t.TempDir()+"/request.xlsx", chief.ID); e != nil {
			t.Fatal(e)
		}
	}
	w = httptest.NewRecorder()
	a.paymentRequestNextReference(w, httptest.NewRequest("GET", "/api/payment-request/next-reference", nil), chief)
	next = nil
	_ = json.Unmarshal(w.Body.Bytes(), &next)
	if w.Code != 200 || next["next_reference"] != "ЗК-2026-0011" {
		t.Fatal("sequential automatic reference", w.Code, next)
	}
	for _, contact := range []struct{ name, phone string }{{"Альфа Тест Тестович", "+70000000001"}, {"Бета Тест Тестович", "+70000000002"}, {"Гамма Тест Тестович", "+70000000003"}} {
		if _, e := a.db.Exec("INSERT INTO payment_contacts(id,full_name,phone,created_by) VALUES($1,$2,$3,$4)", id(), contact.name, contact.phone, chief.ID); e != nil {
			t.Fatal(e)
		}
	}
	w = httptest.NewRecorder()
	a.paymentContacts(w, httptest.NewRequest("GET", "/api/payment-contacts?page=1&page_size=2&q=Тест", nil), chief)
	var page M
	_ = json.Unmarshal(w.Body.Bytes(), &page)
	if w.Code != 200 || page["total"] != float64(3) || page["has_more"] != true || len(page["items"].([]interface{})) != 2 {
		t.Fatal("paginated contact search", w.Code, page)
	}
	w = httptest.NewRecorder()
	a.paymentContacts(w, httptest.NewRequest("GET", "/api/payment-contacts?page=1&page_size=10&q=%2B7+000+000-00-03", nil), chief)
	page = nil
	_ = json.Unmarshal(w.Body.Bytes(), &page)
	if w.Code != 200 || page["total"] != float64(1) {
		t.Fatal("normalized phone search", w.Code, page)
	}
	accountant := User{ID: id(), Role: "accountant"}
	w = httptest.NewRecorder()
	a.paymentContacts(w, httptest.NewRequest("GET", "/api/payment-contacts", nil), accountant)
	if w.Code != 403 {
		t.Fatal("contact search exposed to accountant", w.Code)
	}
}
func TestPaymentContactPhoneNormalization(t *testing.T) {
	phone, e := normalizePaymentPhone("8 (999) 123-45-67")
	if e != nil || phone != "+79991234567" {
		t.Fatal("phone normalization", phone, e)
	}
	phone, e = normalizePaymentPhone("+7 999 123-45-67")
	if e != nil || phone != "+79991234567" {
		t.Fatal("international phone normalization", phone, e)
	}
}
func TestPaymentRequestExportAndResponse(t *testing.T) {
	a := testApp(t)
	chief, merchant, _, card := fixtures(t, a)
	operator := User{ID: id(), Login: "op", Name: "Операционист", Role: "operator"}
	accountant := User{ID: id(), Login: "accountant", Name: "Бухгалтер", Role: "accountant"}
	if _, e := a.db.Exec("INSERT INTO users(id,login,name,role,password_hash) VALUES($1,$2,$3,$4,'x'),($5,$6,$7,$8,'x')", operator.ID, operator.Login, operator.Name, operator.Role, accountant.ID, accountant.Login, accountant.Name, accountant.Role); e != nil {
		t.Fatal(e)
	}
	status, contact := req(t, a.catalogCreate, operator, M{"kind": "payment_contact", "full_name": "Тестов Алексей Учебович", "phone": "+7 000 000-00-01"})
	if status != 201 {
		t.Fatal("operator could not create payment contact", status, contact)
	}
	contactID := contact["id"].(string)
	duplicateStatus, duplicateContact := req(t, a.catalogCreate, operator, M{"kind": "payment_contact", "full_name": "Тестов Алексей Учебович", "phone": "+7 (000) 000-00-01"})
	if duplicateStatus != 201 || duplicateContact["id"] != contactID {
		t.Fatal("repeat payment contact did not reuse directory entry", duplicateStatus, duplicateContact)
	}
	requestBody := M{"merchant_id": merchant, "external_ref": "REQ-20", "mode": "cards", "rows": []M{{"card_id": card, "contact_id": contactID}}}
	if status, _ := req(t, a.createPaymentRequest, accountant, requestBody); status != 403 {
		t.Fatal("accountant issued card file")
	}
	if missingStatus, _ := req(t, a.createPaymentRequest, chief, M{"merchant_id": merchant, "external_ref": "NO-CONTACT", "mode": "cards", "rows": []M{{"card_id": card}}}); missingStatus != 400 {
		t.Fatal("request without mandatory contact accepted", missingStatus)
	}
	if amountStatus, _ := req(t, a.createPaymentRequest, chief, M{"merchant_id": merchant, "external_ref": "WITH-AMOUNT", "mode": "cards", "rows": []M{{"card_id": card, "contact_id": contactID, "amount": "250000.00"}}}); amountStatus != 400 {
		t.Fatal("request with amount accepted", amountStatus)
	}
	status, created := req(t, a.createPaymentRequest, operator, requestBody)
	if status != 201 || created["card_count"] != float64(1) {
		t.Fatal("request creation", status, created)
	}
	requestID := created["id"].(string)
	var savedTotal, savedPer, savedRowAmount sql.NullInt64
	if e := a.db.QueryRow("SELECT p.requested_total_cents,p.per_payment_cents,x.planned_cents FROM payment_requests p JOIN payment_request_rows x ON x.request_id=p.id WHERE p.id=$1", requestID).Scan(&savedTotal, &savedPer, &savedRowAmount); e != nil || savedTotal.Valid || savedPer.Valid || savedRowAmount.Valid {
		t.Fatal("new card request persisted an amount", savedTotal, savedPer, savedRowAmount, e)
	}
	defaultsRecorder := httptest.NewRecorder()
	a.catalog(defaultsRecorder, httptest.NewRequest("GET", "/api/catalog", nil), operator)
	var defaultsCatalog M
	_ = json.Unmarshal(defaultsRecorder.Body.Bytes(), &defaultsCatalog)
	defaultCards := defaultsCatalog["request_cards"].([]interface{})
	if len(defaultCards) != 1 || defaultCards[0].(map[string]interface{})["bank"] != "Вымышленный банк" || defaultCards[0].(map[string]interface{})["full_name"] != "Тестов Алексей Учебович" || defaultCards[0].(map[string]interface{})["phone"] != "+70000000001" {
		t.Fatal("previous registry contact defaults missing", defaultsCatalog["request_cards"])
	}

	status, _ = req(t, a.createPaymentRequest, chief, requestBody)
	if status != 409 {
		t.Fatal("duplicate request accepted")
	}
	w := httptest.NewRecorder()
	a.paymentRequestExport(w, httptest.NewRequest("GET", "/api/payment-request/export?id="+requestID, nil), operator)
	if w.Code != 200 || w.Header().Get("Content-Type") != "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet" {
		t.Fatal("operator could not export prepared XLSX", w.Code)
	}
	w = httptest.NewRecorder()
	a.paymentRequestExport(w, httptest.NewRequest("GET", "/api/payment-request/export?id="+requestID, nil), chief)
	if w.Code != 200 || !strings.Contains(w.Header().Get("Content-Disposition"), requestID) {
		t.Fatal("export failed", w.Code)
	}
	book, e := excelize.OpenReader(bytes.NewReader(w.Body.Bytes()))
	if e != nil {
		t.Fatal(e)
	}
	defer book.Close()
	lines, e := book.GetRows("Карты к оплате")
	if e != nil || len(lines) != 5 || lines[0][0] != "Запрос: REQ-20" || strings.Contains(strings.Join(lines[1], " "), "Вымышленный мерчант") || len(lines[3]) != 5 || lines[3][1] != "НОМЕР КАРТЫ" || lines[3][2] != "ФИО" || lines[3][3] != "НОМЕР ТЕЛЕФОНА" || lines[3][4] != "БАНК" || lines[4][2] != "Тестов Алексей Учебович" || lines[4][3] != "+70000000001" || lines[4][1] != "0000000000061234" || !validLuhn(lines[4][1]) {
		t.Fatal("unsafe or incomplete export", e)
	}
	rowsRecorder := httptest.NewRecorder()
	a.paymentRequestRows(rowsRecorder, httptest.NewRequest("GET", "/api/payment-request/rows?id="+requestID, nil), operator)
	var requestRows []M
	_ = json.Unmarshal(rowsRecorder.Body.Bytes(), &requestRows)
	if rowsRecorder.Code != 200 || len(requestRows) != 1 || requestRows[0]["contact_name"] != "Тестов Алексей Учебович" || requestRows[0]["contact_phone"] != "+70000000001" || requestRows[0]["amount"] != nil {
		t.Fatal("saved contact snapshot missing", rowsRecorder.Code, requestRows)
	}
	rowsRecorder = httptest.NewRecorder()
	a.paymentRequestRows(rowsRecorder, httptest.NewRequest("GET", "/api/payment-request/rows?id="+requestID, nil), accountant)
	requestRows = nil
	_ = json.Unmarshal(rowsRecorder.Body.Bytes(), &requestRows)
	if rowsRecorder.Code != 200 || requestRows[0]["contact_name"] != "Тестов Алексей Учебович" || requestRows[0]["contact_phone"] != "" {
		t.Fatal("accountant received payment contact phone", rowsRecorder.Code, requestRows[0])
	}
	catalogRecorder := httptest.NewRecorder()
	a.catalog(catalogRecorder, httptest.NewRequest("GET", "/api/catalog", nil), accountant)
	var accountantCatalog M
	_ = json.Unmarshal(catalogRecorder.Body.Bytes(), &accountantCatalog)
	_, exposedDefaults := accountantCatalog["request_cards"]
	if _, exposed := accountantCatalog["payment_contacts"]; catalogRecorder.Code != 200 || exposed || exposedDefaults {
		t.Fatal("accountant received payment contact directory", catalogRecorder.Code, accountantCatalog)
	}
	if _, e = a.db.Exec("UPDATE payment_contacts SET full_name='Изменённый Контакт Тестович',phone='+70000000009' WHERE id=$1", contactID); e != nil {
		t.Fatal(e)
	}
	rowsRecorder = httptest.NewRecorder()
	a.paymentRequestRows(rowsRecorder, httptest.NewRequest("GET", "/api/payment-request/rows?id="+requestID, nil), operator)
	requestRows = nil
	_ = json.Unmarshal(rowsRecorder.Body.Bytes(), &requestRows)
	if requestRows[0]["contact_name"] != "Тестов Алексей Учебович" || requestRows[0]["contact_phone"] != "+70000000001" {
		t.Fatal("directory change rewrote request snapshot", requestRows[0])
	}
	repeatExport := httptest.NewRecorder()
	a.paymentRequestExport(repeatExport, httptest.NewRequest("GET", "/api/payment-request/export?id="+requestID, nil), operator)
	if repeatExport.Code != 200 || !bytes.Equal(repeatExport.Body.Bytes(), w.Body.Bytes()) {
		t.Fatal("repeat XLSX export changed after directory edit", repeatExport.Code)
	}
	responseStatus, responseData := func() (int, M) {
		response := excelize.NewFile()
		response.SetCellStr("Sheet1", "A1", "Карта")
		response.SetCellStr("Sheet1", "B1", "Сумма")
		response.SetCellStr("Sheet1", "A2", lines[4][1])
		response.SetCellStr("Sheet1", "B2", "250000.00")
		data, _ := response.WriteToBuffer()
		response.Close()
		var body bytes.Buffer
		form := multipart.NewWriter(&body)
		_ = form.WriteField("merchant_id", merchant)
		_ = form.WriteField("external_ref", "REPLY-20")
		_ = form.WriteField("payment_request_id", requestID)
		part, _ := form.CreateFormFile("file", "response.xlsx")
		_, _ = part.Write(data.Bytes())
		form.Close()
		r := httptest.NewRequest("POST", "/api/registry/upload", &body)
		r.Header.Set("Content-Type", form.FormDataContentType())
		out := httptest.NewRecorder()
		a.upload(out, r, operator)
		var result M
		_ = json.Unmarshal(out.Body.Bytes(), &result)
		return out.Code, result
	}()
	if responseStatus != 201 || responseData["invalid_rows"] != float64(0) {
		t.Fatal("linked response rejected", responseStatus, responseData)
	}
	if editStatus, _ := req(t, a.updatePaymentRequest, operator, M{"id": requestID, "version": "1", "rows": []M{{"card_id": card, "contact_id": contactID}}}); editStatus != 409 {
		t.Fatal("linked request edited", editStatus)
	}
	if deleteStatus, _ := req(t, a.deletePaymentRequest, operator, M{"id": requestID, "version": "1"}); deleteStatus != 409 {
		t.Fatal("linked request deleted", deleteStatus)
	}
	var leaked string
	if e = a.db.QueryRow("SELECT raw::text FROM registry_rows ORDER BY row_no LIMIT 1").Scan(&leaked); e != nil || strings.Contains(leaked, lines[4][1]) {
		t.Fatal("full card number stored in import rows", e)
	}
	var sourcePath string
	if e = a.db.QueryRow("SELECT s.storage_path FROM source_documents s JOIN registries r ON r.source_id=s.id WHERE r.id=$1", responseData["id"]).Scan(&sourcePath); e != nil {
		t.Fatal(e)
	}
	storedSource, e := os.ReadFile(sourcePath)
	if e != nil || bytes.Contains(storedSource, []byte(lines[4][1])) || bytes.Equal(storedSource[:min(len(storedSource), 4)], []byte("PK\x03\x04")) {
		t.Fatal("linked response stored unencrypted", e)
	}
	w = httptest.NewRecorder()
	a.paymentRequests(w, httptest.NewRequest("GET", "/api/payment-requests", nil), chief)
	var requests []M
	_ = json.Unmarshal(w.Body.Bytes(), &requests)
	if w.Code != 200 || len(requests) != 1 || requests[0]["response_count"] != float64(1) || requests[0]["planned_total"] != nil {
		t.Fatal("preview changed financial totals", w.Body.String())
	}
	registryID := responseData["id"].(string)
	status, confirmed := req(t, a.confirmRegistry, chief, M{"id": registryID, "version": "1", "confirm_total": "250000.00", "confirm_commission": "10000.00", "confirm_rate_bp": "400"})
	if status != 200 {
		t.Fatal("linked response confirmation", status, confirmed)
	}
	w = httptest.NewRecorder()
	a.paymentRequests(w, httptest.NewRequest("GET", "/api/payment-requests", nil), chief)
	_ = json.Unmarshal(w.Body.Bytes(), &requests)
	if w.Code != 200 || len(requests) != 1 || requests[0]["response_count"] != float64(1) || requests[0]["difference"] != nil {
		t.Fatal("confirmed response changed the card request plan", w.Body.String())
	}
}

func TestOperatorCanEditAndDeleteUnlinkedCardRequest(t *testing.T) {
	a := testApp(t)
	_, merchant, bank, firstCard := fixtures(t, a)
	operator := User{ID: id(), Login: "operator", Name: "Операционист", Role: "operator"}
	accountant := User{ID: id(), Role: "accountant"}
	if _, e := a.db.Exec("INSERT INTO users(id,login,name,role,password_hash) VALUES($1,$2,$3,'operator','x'),($4,'accountant','Бухгалтер','accountant','x')", operator.ID, operator.Login, operator.Name, accountant.ID); e != nil {
		t.Fatal(e)
	}
	secondCard := id()
	pan := ""
	for digit := byte('0'); digit <= '9'; digit++ {
		candidate := "000000000006124" + string(digit)
		if validLuhn(candidate) {
			pan = candidate
			break
		}
	}
	if _, e := a.db.Exec("INSERT INTO cards(id,bank_id,owner_label,mask,last4) VALUES($1,$2,'Другой владелец',$3,$4)", secondCard, bank, "000000******"+pan[12:], pan[12:]); e != nil {
		t.Fatal(e)
	}
	if e := savePAN(secondCard, pan); e != nil {
		t.Fatal(e)
	}
	status, contact := req(t, a.catalogCreate, operator, M{"kind": "payment_contact", "full_name": "Тестов Алексей Учебович", "phone": "+70000000001"})
	if status != 201 {
		t.Fatal(status, contact)
	}
	contactID := contact["id"].(string)
	status, created := req(t, a.createPaymentRequest, operator, M{"merchant_id": merchant, "external_ref": "EDIT-1", "mode": "cards", "rows": []M{{"card_id": firstCard, "contact_id": contactID}}})
	if status != 201 {
		t.Fatal(status, created)
	}
	requestID := created["id"].(string)
	update := M{"id": requestID, "version": "1", "rows": []M{{"card_id": secondCard, "contact_id": contactID}, {"card_id": firstCard, "contact_id": contactID}}}
	if status, _ := req(t, a.updatePaymentRequest, accountant, update); status != 403 {
		t.Fatal("accountant edited request", status)
	}
	if status, _ := req(t, a.updatePaymentRequest, operator, M{"id": requestID, "version": "1", "rows": []M{{"card_id": firstCard, "contact_id": contactID}, {"card_id": firstCard, "contact_id": contactID}}}); status != 400 {
		t.Fatal("duplicate card accepted", status)
	}
	if status, updated := req(t, a.updatePaymentRequest, operator, update); status != 200 || updated["card_count"] != float64(2) || updated["version"] != float64(2) {
		t.Fatal("update failed", status, updated)
	}
	if status, _ := req(t, a.updatePaymentRequest, operator, update); status != 409 {
		t.Fatal("stale update accepted", status)
	}
	var count, version int
	if e := a.db.QueryRow("SELECT payment_count,version FROM payment_requests WHERE id=$1", requestID).Scan(&count, &version); e != nil || count != 2 || version != 2 {
		t.Fatal("request state", count, version, e)
	}
	update = M{"id": requestID, "version": "2", "rows": []M{{"card_id": secondCard, "contact_id": contactID}}}
	if status, updated := req(t, a.updatePaymentRequest, operator, update); status != 200 || updated["card_count"] != float64(1) {
		t.Fatal("card removal failed", status, updated)
	}
	var remaining string
	if e := a.db.QueryRow("SELECT card_id FROM payment_request_rows WHERE request_id=$1", requestID).Scan(&remaining); e != nil || remaining != secondCard {
		t.Fatal("removed card remains", remaining, e)
	}
	if status, _ := req(t, a.deletePaymentRequest, accountant, M{"id": requestID, "version": "3"}); status != 403 {
		t.Fatal("accountant deleted request", status)
	}
	if status, _ := req(t, a.deletePaymentRequest, operator, M{"id": requestID, "version": "3"}); status != 200 {
		t.Fatal("delete failed", status)
	}
	if status, _ := req(t, a.deletePaymentRequest, operator, M{"id": requestID, "version": "3"}); status != 409 {
		t.Fatal("repeat delete accepted", status)
	}
	w := httptest.NewRecorder()
	a.paymentRequests(w, httptest.NewRequest("GET", "/api/payment-requests", nil), operator)
	var listed []M
	_ = json.Unmarshal(w.Body.Bytes(), &listed)
	if w.Code != 200 || len(listed) != 0 {
		t.Fatal("deleted request still listed", w.Body.String())
	}
	w = httptest.NewRecorder()
	a.paymentRequestExport(w, httptest.NewRequest("GET", "/api/payment-request/export?id="+requestID, nil), operator)
	if w.Code != 404 {
		t.Fatal("deleted request exported", w.Code)
	}
	var audited int
	if e := a.db.QueryRow("SELECT count(*) FROM audit_events WHERE object_id=$1 AND action IN ('payment_request_update','payment_request_delete')", requestID).Scan(&audited); e != nil || audited != 3 {
		t.Fatal("missing edit audit", audited, e)
	}
	var postings int
	if e := a.db.QueryRow("SELECT count(*) FROM postings").Scan(&postings); e != nil || postings != 0 {
		t.Fatal("edit changed ledger", postings, e)
	}
}

func TestRequestRowLookupWithProductionStyleContactPermissions(t *testing.T) {
	a := testApp(t)
	chief, _, _, card := fixtures(t, a)
	status, contact := req(t, a.catalogCreate, chief, M{"kind": "payment_contact", "full_name": "Тестов Алексей Учебович", "phone": "+70000000001"})
	if status != 201 {
		t.Fatal(status, contact)
	}
	tx, e := a.db.Begin()
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	for _, statement := range []string{
		"CREATE ROLE metallist_request_lookup_test NOLOGIN",
		"GRANT SELECT ON cards,payment_contacts TO metallist_request_lookup_test",
		"GRANT UPDATE(mask) ON cards TO metallist_request_lookup_test",
		"SET LOCAL ROLE metallist_request_lookup_test",
	} {
		if _, e = tx.Exec(statement); e != nil {
			t.Fatal(e)
		}
	}
	var mask, name, phone string
	var encrypted []byte
	if e = tx.QueryRow(editableRowLookupSQL, card, contact["id"]).Scan(&mask, &encrypted, &name, &phone); e != nil {
		t.Fatal("lookup needs excess privileges", e)
	}
	if name != "Тестов Алексей Учебович" || phone != "+70000000001" {
		t.Fatal("wrong contact snapshot")
	}
}
func TestExpenseDateAndSource(t *testing.T) {
	a := testApp(t)
	u, _, _, card := fixtures(t, a)
	tx, e := a.tx()
	if e != nil {
		t.Fatal(e)
	}
	_, e = put(tx, "test_funding", id(), "test-funding-"+id(), u.ID, time.Now(), time.Now(), []Posting{{Account: "1100", Side: "debit", Amount: 10_000, Card: card}, {Account: "3100", Side: "credit", Amount: 10_000}}, "")
	if e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	status, out := req(t, a.draft, u, M{"kind": "expense", "source_kind": "card", "source_id": card, "category": "operating", "amount": "10.00", "date": "2026-07-11", "reason": "Вымышленный расход"})
	if status != 201 {
		t.Fatal("expense draft", out)
	}
	draftID := out["id"].(string)
	status, out = req(t, a.confirmDraft, u, M{"id": draftID, "version": "1", "confirm_amount": "10.00"})
	if status != 200 {
		t.Fatal("expense confirmation", out)
	}
	var day string
	if e = a.db.QueryRow("SELECT to_char(occurred_at AT TIME ZONE 'Europe/Moscow','YYYY-MM-DD') FROM journal_entries WHERE event_type='expense' AND event_id=$1", draftID).Scan(&day); e != nil || day != "2026-07-11" {
		t.Fatal("expense date not preserved", day, e)
	}
	month, e := a.monthReport("2026-07")
	if e != nil || month["expenses"] != "10.00" {
		t.Fatal("expense in wrong month", month, e)
	}
}
func TestExpenseTypePostingAndBlockedPayouts(t *testing.T) {
	for _, category := range []string{"salary", "warmup", "it_infrastructure", "taxes", "communication", "delivery"} {
		if account, e := expenseAccount(category); e != nil || account != "5100" {
			t.Fatal("ordinary expense mapped incorrectly", category, account, e)
		}
	}
	a := testApp(t)
	u, _, _, card := fixtures(t, a)
	tx, e := a.tx()
	if e != nil {
		t.Fatal(e)
	}
	_, e = put(tx, "test_funding", id(), "test-expense-funding-"+id(), u.ID, time.Now(), time.Now(), []Posting{{Account: "1100", Side: "debit", Amount: 3_000, Card: card}, {Account: "3100", Side: "credit", Amount: 3_000}}, "")
	if e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	for category, account := range map[string]string{"agent_fee": "5200", "bank_fee": "5300", "other": "5900"} {
		status, created := req(t, a.draft, u, M{"kind": "expense", "source_kind": "card", "source_id": card, "category": category, "amount": "10.00"})
		if status != 201 {
			t.Fatal("draft rejected", category, created)
		}
		draftID := created["id"].(string)
		status, confirmed := req(t, a.confirmDraft, u, M{"id": draftID, "version": "1", "confirm_amount": "10.00"})
		if status != 200 {
			t.Fatal("expense not confirmed", category, confirmed)
		}
		status, _ = req(t, a.confirmDraft, u, M{"id": draftID, "version": "1", "confirm_amount": "10.00"})
		if status != 200 {
			t.Fatal("expense retry not idempotent", category)
		}
		var count int
		if e = a.db.QueryRow("SELECT COUNT(*) FROM postings p JOIN journal_entries j ON j.id=p.entry_id WHERE j.event_type='expense' AND j.event_id=$1 AND p.account=$2 AND p.side='debit'", draftID, account).Scan(&count); e != nil || count != 1 {
			t.Fatal("wrong expense account or duplicate posting", category, account, count, e)
		}
	}
	for _, category := range []string{"repayment", "dividends", "losses", "unknown"} {
		status, _ := req(t, a.draft, u, M{"kind": "expense", "source_kind": "card", "source_id": card, "category": category, "amount": "1.00"})
		if status != 400 {
			t.Fatal("unsupported expense category accepted", category, status)
		}
	}
	var net int64
	if e = a.db.QueryRow("SELECT COALESCE(SUM(CASE WHEN side='debit' THEN amount_cents ELSE -amount_cents END),0) FROM postings").Scan(&net); e != nil || net != 0 {
		t.Fatal("expense ledger unbalanced", net, e)
	}
}
func TestCLIPasswordResetRevokesSessions(t *testing.T) {
	a := testApp(t)
	u, _, _, _ := fixtures(t, a)
	if _, e := a.db.Exec("INSERT INTO sessions(token_hash,user_id,expires_at) VALUES($1,$2,now()+interval '1 day')", digest("synthetic-session"), u.ID); e != nil {
		t.Fatal(e)
	}
	file := t.TempDir() + "/new-password"
	pass := "synthetic-reset-" + id()
	if e := os.WriteFile(file, []byte(pass), 0600); e != nil {
		t.Fatal(e)
	}
	if e := a.setPassword(u.Login, file); e != nil {
		t.Fatal(e)
	}
	var hash string
	var sessions, audits int
	if e := a.db.QueryRow("SELECT password_hash FROM users WHERE id=$1", u.ID).Scan(&hash); e != nil || bcrypt.CompareHashAndPassword([]byte(hash), []byte(pass)) != nil {
		t.Fatal("new password not installed", e)
	}
	if e := a.db.QueryRow("SELECT COUNT(*) FROM sessions WHERE user_id=$1", u.ID).Scan(&sessions); e != nil || sessions != 0 {
		t.Fatal("old sessions survived reset", e)
	}
	if e := a.db.QueryRow("SELECT COUNT(*) FROM audit_events WHERE action='password_reset' AND object_id=$1", u.ID).Scan(&audits); e != nil || audits != 1 {
		t.Fatal("reset not audited", e)
	}
}
func TestDashboardScriptAllowedByCSP(t *testing.T) {
	a := &App{}
	mux := http.NewServeMux()
	mux.HandleFunc("/", a.ui)
	mux.HandleFunc("/assets/app.js", a.javascript)
	h := secureHeaders(mux)

	page := httptest.NewRecorder()
	h.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/", nil))
	if page.Code != http.StatusOK {
		t.Fatal(page.Code)
	}
	if !strings.Contains(page.Header().Get("Content-Security-Policy"), "script-src 'self'") {
		t.Fatal("same-origin scripts are not allowed")
	}
	if !strings.Contains(page.Body.String(), `<script type="module" src="/assets/app.js"></script>`) || !strings.Contains(page.Body.String(), `/assets/app.css`) || strings.Contains(page.Body.String(), "<script>") || strings.Contains(page.Body.String(), "onclick=") {
		t.Fatal("page uses blocked inline JavaScript")
	}

	script := httptest.NewRecorder()
	h.ServeHTTP(script, httptest.NewRequest(http.MethodGet, "/assets/app.js", nil))
	if script.Code != http.StatusOK || !strings.HasPrefix(script.Header().Get("Content-Type"), "text/javascript") || !strings.Contains(script.Body.String(), `document.getElementById("root")`) {
		t.Fatal("dashboard script is unavailable")
	}
}
func TestLoginAndPasswordRotation(t *testing.T) {
	a := testApp(t)
	u, _, _, _ := fixtures(t, a)
	oldPassword := "synthetic-first-password-2026"
	newPassword := "synthetic-second-password-2026"
	hash, e := bcrypt.GenerateFromPassword([]byte(oldPassword), bcrypt.MinCost)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = a.db.Exec("UPDATE users SET password_hash=$1 WHERE id=$2", string(hash), u.ID); e != nil {
		t.Fatal(e)
	}
	login := func(password string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(M{"login": u.Login, "password": password})
		w := httptest.NewRecorder()
		a.login(w, httptest.NewRequest("POST", "/login", bytes.NewReader(body)))
		return w
	}
	if w := login("wrong-password"); w.Code != 401 {
		t.Fatal("incorrect password accepted", w.Code)
	}
	w := login(oldPassword)
	if w.Code != 200 || len(w.Result().Cookies()) != 1 {
		t.Fatal("login failed", w.Code, w.Body.String())
	}
	cookie := w.Result().Cookies()[0]
	if !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatal("session cookie insecure")
	}
	me := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/me", nil)
	r.AddCookie(cookie)
	a.auth(a.me)(me, r)
	if me.Code != 200 {
		t.Fatal("session rejected", me.Code)
	}
	changeBody, _ := json.Marshal(M{"current": oldPassword, "new": newPassword})
	changed := httptest.NewRecorder()
	r = httptest.NewRequest("POST", "/api/password", bytes.NewReader(changeBody))
	r.AddCookie(cookie)
	a.changePassword(changed, r, u)
	if changed.Code != 200 {
		t.Fatal("password change failed", changed.Code, changed.Body.String())
	}
	me = httptest.NewRecorder()
	r = httptest.NewRequest("GET", "/api/me", nil)
	r.AddCookie(cookie)
	a.auth(a.me)(me, r)
	if me.Code != 401 || login(oldPassword).Code != 401 || login(newPassword).Code != 200 {
		t.Fatal("password rotation did not invalidate old access")
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
	rows, e := parseRows("aliten_bank_csv_v1", ".csv", encoded)
	if e != nil || len(rows) != 2 || rows[0].Error != "" || rows[1].Error != "" {
		t.Fatal(rows, e)
	}
	bad := []byte("x\nКарта;Сумма\n000000******1234;1.001\n")
	_, e = parseRows("aliten_bank_csv_v1", ".csv", bad)
	if e == nil {
		t.Fatal("invalid headers accepted")
	}
}

func TestMerchantParserMappings(t *testing.T) {
	tests := []struct {
		name   string
		parser string
		sheet  spreadsheetSheet
		count  int
		amount int64
		error  string
	}{
		{"Света", "sveta_cards_xls_v1", spreadsheetSheet{Name: "Лист_1", Rows: [][]string{{}, {}, {}, {"№ п/п", "Сумма", "Номер вх.", "По номеру карты"}, {"1", "248063", "3999", "000000******1234"}, {"Итого", "248063"}}}, 1, 24_806_300, ""},
		{"Света по ФИО", "sveta_cards_xls_v1", spreadsheetSheet{Name: "Лист_1", Rows: [][]string{{"ООО"}, {}, {}, {}, {}, {"№ п/п", "Дата", "Номер вх.", "Сумма", "Информация"}, {"1", "12.07.2026", "4627-цм", "246402", "Потеряева Евгения Борисовна"}, {"Итого", "", "", "246402"}}}, 1, 24_640_200, ""},
		{"Катя", "katya_payouts_xlsx_v1", spreadsheetSheet{Name: "Sheet1", Rows: [][]string{{"Статус выплаты", "Id выплаты", "Выплата", "Комиссия банка", "Реквизиты вывода"}, {"оплачена", "182194058", "244541", "1100.43", "000000******1234"}}}, 1, 24_454_100, ""},
		{"Толя", "tolya_operations_xlsx_v1", spreadsheetSheet{Name: "Операции", Rows: [][]string{{"ID", "СТАТУС", "ЦЕНА"}, {"synthetic-id", "Выполнен", "248121"}}}, 1, 24_812_100, "missing_card_reference"},
		{"Наркоман", "narkoman_avangard_xls_v1", spreadsheetSheet{Name: "clb_stat_complete.jr", Rows: [][]string{{}, {}, {}, {}, {}, {}, {"", "", "Дата док-та", "", "", "Номер док-та", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "Назначение платежа"}, {}, {"", "", "14.09.2026", "", "", "6873656995", "", "", "", "", "", "", "", "", "", "", "13", "250000", "", "", "Расчеты. Карта:****1234"}, {"", "", "Итого:"}}}, 1, 25_000_000, ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rows, e := parseMerchantSheets(test.parser, []spreadsheetSheet{test.sheet})
			if e != nil || len(rows) != test.count || rows[0].Amount != test.amount || rows[0].Error != test.error {
				t.Fatalf("mapping failed: count=%d amount=%d error=%q parse=%v", len(rows), func() int64 {
					if len(rows) == 0 {
						return 0
					}
					return rows[0].Amount
				}(), func() string {
					if len(rows) == 0 {
						return ""
					}
					return rows[0].Error
				}(), e)
			}
		})
	}
}

func TestSpreadsheetAmountWithThousandsSeparator(t *testing.T) {
	cases := map[string]int64{
		"246,402.00":      24640200,
		"246.402,00":      24640200,
		"246 402,00":      24640200,
		"246\u00a0402,00": 24640200,
		"246,402":         24640200,
	}
	for input, want := range cases {
		got, err := spreadsheetAmount(input)
		if err != nil || got != want {
			t.Errorf("spreadsheetAmount(%q) = %d, %v; want %d", input, got, err, want)
		}
	}
}

func TestSvetaRegistryReconciliationByRequest(t *testing.T) {
	a := testApp(t)
	chief, merchant, bank, firstCard := fixtures(t, a)
	operator := User{ID: id(), Role: "operator"}
	if _, e := a.db.Exec("INSERT INTO users(id,login,name,role,password_hash) VALUES($1,'sveta-operator','Operator','operator','x')", operator.ID); e != nil {
		t.Fatal(e)
	}
	if _, e := a.db.Exec("UPDATE merchant_import_profiles SET parser_code='sveta_cards_xls_v1' WHERE merchant_id=$1", merchant); e != nil {
		t.Fatal(e)
	}
	secondCard := id()
	if _, e := a.db.Exec("INSERT INTO cards(id,bank_id,owner_label,mask,last4) VALUES($1,$2,'Второй владелец','220038******5678','5678')", secondCard, bank); e != nil {
		t.Fatal(e)
	}
	if e := savePAN(secondCard, "2200380000005678"); e != nil {
		t.Fatal(e)
	}
	requestID := id()
	firstContact, secondContact := id(), id()
	if _, e := a.db.Exec(`INSERT INTO payment_contacts(id,full_name,phone,created_by) VALUES
		($1,'Иванова Ёлка Петровна','+79990000001',$3),($2,'Корректное Имя Получателя','+79990000002',$3)`, firstContact, secondContact, chief.ID); e != nil {
		t.Fatal(e)
	}
	if _, e := a.db.Exec("INSERT INTO payment_requests(id,merchant_id,external_ref,mode,payment_count,created_by) VALUES($1,$2,'СВЕТА-ТЕСТ','cards',2,$3)", requestID, merchant, chief.ID); e != nil {
		t.Fatal(e)
	}
	if _, e := a.db.Exec(`INSERT INTO payment_request_rows(id,request_id,row_no,card_id,contact_id,contact_name,contact_phone) VALUES
		($1,$2,1,$3,$4,'Иванова Ёлка Петровна','+79990000001'),($5,$2,2,$6,$7,'Корректное Имя Получателя','+79990000002')`, id(), requestID, firstCard, firstContact, id(), secondCard, secondContact); e != nil {
		t.Fatal(e)
	}
	file := excelize.NewFile()
	for cell, value := range map[string]interface{}{
		"A1": "ООО Тест", "A6": "№ п/п", "B6": "Дата", "C6": "Номер вх.", "D6": "Сумма", "E6": "Информация",
		"A7": 1, "B7": "12.07.2026", "C7": "4627", "D7": 1000, "E7": "  ИВАНОВА   ЕЛКА ПЕТРОВНА ",
		"A8": 2, "B8": "12.07.2026", "C8": "4628", "D8": 2000, "E8": "Ошибочно Заведенное Имя",
		"A9": "Итого", "D9": 3000,
	} {
		file.SetCellValue("Sheet1", cell, value)
	}
	blob, e := file.WriteToBuffer()
	file.Close()
	if e != nil {
		t.Fatal(e)
	}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("merchant_id", merchant)
	_ = mw.WriteField("external_ref", "SVETA-FIO-TEST")
	_ = mw.WriteField("payment_request_id", requestID)
	fw, _ := mw.CreateFormFile("file", "sveta.xlsx")
	_, _ = fw.Write(blob.Bytes())
	mw.Close()
	var missingBody bytes.Buffer
	missingWriter := multipart.NewWriter(&missingBody)
	_ = missingWriter.WriteField("merchant_id", merchant)
	_ = missingWriter.WriteField("external_ref", "SVETA-FIO-WITHOUT-REQUEST")
	missingFile, _ := missingWriter.CreateFormFile("file", "sveta.xlsx")
	_, _ = missingFile.Write(blob.Bytes())
	missingWriter.Close()
	withoutRequest := httptest.NewRequest(http.MethodPost, "/api/registry/upload", &missingBody)
	withoutRequest.Header.Set("Content-Type", missingWriter.FormDataContentType())
	withoutRequestRecorder := httptest.NewRecorder()
	a.upload(withoutRequestRecorder, withoutRequest, operator)
	if withoutRequestRecorder.Code != 400 || !strings.Contains(withoutRequestRecorder.Body.String(), "выберите сформированный запрос") {
		t.Fatalf("missing request accepted: %d %s", withoutRequestRecorder.Code, withoutRequestRecorder.Body.String())
	}
	r := httptest.NewRequest(http.MethodPost, "/api/registry/upload", &body)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	w := httptest.NewRecorder()
	a.upload(w, r, operator)
	var created M
	_ = json.Unmarshal(w.Body.Bytes(), &created)
	if w.Code != 201 || created["invalid_rows"] != float64(1) || created["accepted_total"] != "3000.00" {
		t.Fatalf("upload: %d %v", w.Code, created)
	}
	registryID := created["id"].(string)
	var firstResolved, secondResolved, sourceName, rowError string
	if e = a.db.QueryRow("SELECT COALESCE(card_id::text,''),source_contact_name,COALESCE(error_code,'') FROM registry_rows WHERE registry_id=$1 AND row_no=7", registryID).Scan(&firstResolved, &sourceName, &rowError); e != nil {
		t.Fatal(e)
	}
	if firstResolved != firstCard || sourceName != "ИВАНОВА   ЕЛКА ПЕТРОВНА" || rowError != "" {
		t.Fatal(firstResolved, sourceName, rowError)
	}
	if e = a.db.QueryRow("SELECT COALESCE(card_id::text,''),source_contact_name,COALESCE(error_code,'') FROM registry_rows WHERE registry_id=$1 AND row_no=8", registryID).Scan(&secondResolved, &sourceName, &rowError); e != nil {
		t.Fatal(e)
	}
	if secondResolved != "" || rowError != "request_contact_not_found_or_ambiguous" {
		t.Fatal(secondResolved, sourceName, rowError)
	}
	candidates := httptest.NewRecorder()
	a.registryCandidates(candidates, httptest.NewRequest(http.MethodGet, "/api/registry/candidates?id="+registryID, nil), chief)
	var choices []M
	_ = json.Unmarshal(candidates.Body.Bytes(), &choices)
	if candidates.Code != 200 || len(choices) != 2 {
		t.Fatalf("candidates: %d %v", candidates.Code, choices)
	}
	accountantRows := httptest.NewRecorder()
	a.registryRows(accountantRows, httptest.NewRequest(http.MethodGet, "/api/registry/rows?id="+registryID, nil), User{ID: id(), Role: "accountant"})
	var readOnlyRows []M
	_ = json.Unmarshal(accountantRows.Body.Bytes(), &readOnlyRows)
	if accountantRows.Code != 200 || len(readOnlyRows) != 2 || readOnlyRows[0]["request_contact_name"] != "" || readOnlyRows[0]["request_contact_phone"] != "" {
		t.Fatalf("read-only role received request contacts: %d %v", accountantRows.Code, readOnlyRows)
	}
	otherOperator := User{ID: id(), Role: "operator"}
	forbidden := httptest.NewRecorder()
	a.registryCandidates(forbidden, httptest.NewRequest(http.MethodGet, "/api/registry/candidates?id="+registryID, nil), otherOperator)
	if forbidden.Code != 403 {
		t.Fatalf("another operator listed candidates: %d", forbidden.Code)
	}
	outsideCard := id()
	if _, e := a.db.Exec("INSERT INTO cards(id,bank_id,owner_label,mask,last4) VALUES($1,$2,'Вне запроса','220038******9999','9999')", outsideCard, bank); e != nil {
		t.Fatal(e)
	}
	if code, _ := req(t, a.reconcileRegistryRow, operator, M{"registry_id": registryID, "row": "8", "card_id": outsideCard, "version": "1"}); code != 400 {
		t.Fatalf("card outside request accepted: %d", code)
	}
	code, result := req(t, a.reconcileRegistryRow, operator, M{"registry_id": registryID, "row": "8", "card_id": secondCard, "version": "1"})
	if code != 200 || result["version"] != float64(2) {
		t.Fatalf("reconcile: %d %v", code, result)
	}
	if e = a.db.QueryRow("SELECT card_id,COALESCE(error_code,'') FROM registry_rows WHERE registry_id=$1 AND row_no=8", registryID).Scan(&secondResolved, &rowError); e != nil || secondResolved != secondCard || rowError != "" {
		t.Fatal(secondResolved, rowError, e)
	}
	code, result = req(t, a.confirmRegistry, chief, M{"id": registryID, "version": "2", "confirm_total": "3000.00", "confirm_commission": "120.00", "confirm_rate_bp": "400"})
	if code != 200 {
		t.Fatalf("confirm: %d %v", code, result)
	}
	var auditCount int
	if e = a.db.QueryRow("SELECT count(*) FROM audit_events WHERE object_id=$1 AND action='registry_row_reconcile'", registryID).Scan(&auditCount); e != nil || auditCount != 1 {
		t.Fatal(auditCount, e)
	}
	var auditDetail string
	if e = a.db.QueryRow("SELECT detail::text FROM audit_events WHERE object_id=$1 AND action='registry_row_reconcile'", registryID).Scan(&auditDetail); e != nil || strings.Contains(auditDetail, "Получателя") || strings.Contains(auditDetail, "+7999") {
		t.Fatal("audit contains contact data", auditDetail, e)
	}
}

func TestMerchantParserRejectsFormulaAndUnsafePrecision(t *testing.T) {
	file := excelize.NewFile()
	file.SetCellValue("Sheet1", "A1", "Карта")
	file.SetCellValue("Sheet1", "B1", "Сумма")
	file.SetCellValue("Sheet1", "A2", "000000******1234")
	file.SetCellFormula("Sheet1", "B2", "=100+1")
	blob, e := file.WriteToBuffer()
	if e != nil {
		t.Fatal(e)
	}
	file.Close()
	if _, e = parseRows("generic_xlsx_v1", ".xlsx", blob.Bytes()); e == nil || !strings.Contains(e.Error(), "формулы") {
		t.Fatal("formula was accepted", e)
	}
	if _, e = spreadsheetAmount("1.0010"); e == nil {
		t.Fatal("non-zero sub-kopeck precision was accepted")
	}
	if value, e := spreadsheetAmount("1.2300"); e != nil || value != 123 {
		t.Fatal("exact trailing zeros were rejected", value, e)
	}
}

func TestAttachedMerchantSamples(t *testing.T) {
	tests := []struct {
		env, parser, ext string
		count, invalid   int
		total            int64
	}{
		{"METALLIST_SAMPLE_SVETA", "sveta_cards_xls_v1", ".xls", 24, 0, 593_656_900},
		{"METALLIST_SAMPLE_NARKOMAN", "narkoman_avangard_xls_v1", ".xls", 20, 0, 500_000_000},
		{"METALLIST_SAMPLE_TOLYA", "tolya_operations_xlsx_v1", ".xlsx", 18, 18, 440_155_900},
		{"METALLIST_SAMPLE_KATYA", "katya_payouts_xlsx_v1", ".xlsx", 6, 0, 146_337_400},
	}
	ran := false
	for _, test := range tests {
		path := os.Getenv(test.env)
		if path == "" {
			continue
		}
		ran = true
		data, e := os.ReadFile(path)
		if e != nil {
			t.Fatal(e)
		}
		rows, e := parseRows(test.parser, test.ext, data)
		if e != nil {
			t.Fatalf("%s: %v", test.parser, e)
		}
		var total int64
		invalid := 0
		errorsByCode := map[string]int{}
		for _, row := range rows {
			total += row.Amount
			if row.Error != "" {
				invalid++
				errorsByCode[row.Error]++
			}
		}
		if len(rows) != test.count || invalid != test.invalid || total != test.total {
			t.Fatalf("%s: rows=%d invalid=%d total=%d errors=%v", test.parser, len(rows), invalid, total, errorsByCode)
		}
	}
	if !ran {
		t.Skip("local merchant samples are not configured")
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
func TestOperatorCannotAccessExpensesOrChangeBalances(t *testing.T) {
	a := testApp(t)
	chief, _, _, card := fixtures(t, a)
	operator := User{ID: id(), Login: "op", Name: "Операционист", Role: "operator"}
	if _, e := a.db.Exec("INSERT INTO users(id,login,name,role,password_hash) VALUES($1,$2,$3,$4,'x')", operator.ID, operator.Login, operator.Name, operator.Role); e != nil {
		t.Fatal(e)
	}
	if _, e := a.db.Exec("INSERT INTO card_assignments(user_id,card_id,assigned_by) VALUES($1,$2,$3)", operator.ID, card, chief.ID); e != nil {
		t.Fatal(e)
	}
	for _, kind := range []string{"expense", "repayment", "shortage", "writeoff"} {
		code, _ := req(t, a.draft, operator, M{"kind": kind, "amount": "10.00", "category": "salary", "source_kind": "card", "source_id": card})
		if code != 403 {
			t.Fatal("operator created restricted draft", kind, code)
		}
	}
	if status, _ := req(t, a.observation, operator, M{"card_id": card, "amount": "100.00"}); status != 403 {
		t.Fatal("operator changed observed balance", status)
	}
	code, created := req(t, a.draft, operator, M{"kind": "withdrawal", "amount": "10.00", "card_id": card, "custodian_id": id()})
	if code != 201 {
		t.Fatal("operator could not prepare withdrawal", created)
	}
	draftID := created["id"].(string)
	for _, check := range []struct {
		name string
		fn   handler
		body M
	}{
		{"confirm", a.confirmDraft, M{"id": draftID, "version": "1", "confirm_amount": "10.00"}},
		{"reverse", a.reverseDraft, M{"id": draftID, "reason": "test"}},
		{"registry confirmation", a.confirmRegistry, M{}},
	} {
		status, _ := req(t, check.fn, operator, check.body)
		if status != 403 {
			t.Fatal("operator changed money", check.name, status)
		}
	}
	if _, e := a.db.Exec("INSERT INTO drafts(id,kind,payload,created_by,idempotency_key) VALUES($1,'expense',$2,$3,$4)", id(), encode(M{"amount": "99.00", "category": "salary"}), operator.ID, id()); e != nil {
		t.Fatal(e)
	}
	w := httptest.NewRecorder()
	a.drafts(w, httptest.NewRequest("GET", "/api/drafts", nil), operator)
	var listed []M
	if e := json.Unmarshal(w.Body.Bytes(), &listed); e != nil || w.Code != 200 || len(listed) != 1 || listed[0]["kind"] != "withdrawal" {
		t.Fatal("operator sees expense history or lost operational draft", w.Code, w.Body.String(), e)
	}
	w = httptest.NewRecorder()
	a.report(w, httptest.NewRequest("GET", "/api/report", nil), operator)
	if w.Code != 403 {
		t.Fatal("operator sees statistics", w.Code)
	}
	w = httptest.NewRecorder()
	a.report(w, httptest.NewRequest("GET", "/api/report", nil), chief)
	if w.Code != 200 {
		t.Fatal("chief lost statistics", w.Code)
	}
	var postings int
	if e := a.db.QueryRow("SELECT count(*) FROM postings").Scan(&postings); e != nil || postings != 0 {
		t.Fatal("operator action changed balances", postings, e)
	}
}
func TestSystemAdminCreatesOperatorRole(t *testing.T) {
	a := testApp(t)
	chief, _, _, _ := fixtures(t, a)
	admin := User{ID: id(), Login: "sysadmin", Name: "Системный администратор", Role: "sysadmin"}
	if _, e := a.db.Exec("INSERT INTO users(id,login,name,role,password_hash) VALUES($1,$2,$3,$4,'x')", admin.ID, admin.Login, admin.Name, admin.Role); e != nil {
		t.Fatal(e)
	}
	body := M{"kind": "user", "login": "operator_demo", "name": "Учебный операционист", "role": "operator"}
	if status, _ := req(t, a.catalogCreate, chief, body); status != 403 {
		t.Fatal("chief created a user", status)
	}
	status, result := req(t, a.catalogCreate, admin, body)
	if status != 201 {
		t.Fatal("sysadmin could not create operator", status)
	}
	password, ok := result["temporary_password"].(string)
	if !ok || len(password) < 16 {
		t.Fatal("one-time password missing")
	}
	var role, hash string
	if e := a.db.QueryRow("SELECT role,password_hash FROM users WHERE login=$1", "operator_demo").Scan(&role, &hash); e != nil || role != "operator" {
		t.Fatal("operator role not stored", e)
	}
	if e := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)); e != nil {
		t.Fatal("one-time password does not authenticate")
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
func TestHandoverConfirmationAmountAndDraftRejection(t *testing.T) {
	a := testApp(t)
	u, _, _, _ := fixtures(t, a)
	var collector, chief string
	if e := a.db.QueryRow("SELECT id FROM custodians WHERE kind='collector'").Scan(&collector); e != nil {
		t.Fatal(e)
	}
	if e := a.db.QueryRow("SELECT id FROM custodians WHERE kind='chief'").Scan(&chief); e != nil {
		t.Fatal(e)
	}
	tx, e := a.tx()
	if e != nil {
		t.Fatal(e)
	}
	_, e = put(tx, "test_funding", id(), "handover-funding-"+id(), u.ID, time.Now(), time.Now(), []Posting{{Account: "1200", Side: "debit", Amount: 156000000, Custodian: collector}, {Account: "3100", Side: "credit", Amount: 156000000}}, "")
	if e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	code, created := req(t, a.draft, u, M{"kind": "handover", "from_custodian_id": collector, "to_custodian_id": chief, "amount": "1560000"})
	if code != 201 {
		t.Fatal(created)
	}
	draftID := created["id"].(string)
	code, _ = req(t, a.confirmDraft, u, M{"id": draftID, "version": "1", "confirm_amount": "1560001"})
	if code != 409 {
		t.Fatal("handover greater than requested amount accepted")
	}
	code, result := req(t, a.confirmDraft, u, M{"id": draftID, "version": "1", "confirm_amount": "1560000"})
	if code != 200 || result["status"] != "posted" {
		t.Fatal("whole-ruble amount rejected", code, result)
	}
	code, result = req(t, a.confirmDraft, u, M{"id": draftID, "version": "1", "confirm_amount": "1560000.00"})
	if code != 200 || result["status"] != "already_posted" {
		t.Fatal("confirmation retry failed", code, result)
	}
	tx, e = a.tx()
	if e != nil {
		t.Fatal(e)
	}
	left, _ := balance(tx, "1200", "custodian", collector)
	received, _ := balance(tx, "1210", "custodian", chief)
	tx.Rollback()
	if left != 0 || received != 156000000 {
		t.Fatal("handover balance mismatch", left, received)
	}
	code, _ = req(t, a.rejectDraft, u, M{"id": draftID, "version": "1", "reason": "ошибка"})
	if code != 409 {
		t.Fatal("posted draft rejected")
	}

	code, created = req(t, a.draft, u, M{"kind": "handover", "from_custodian_id": collector, "to_custodian_id": chief, "amount": "100.00"})
	if code != 201 {
		t.Fatal(created)
	}
	rejectedID := created["id"].(string)
	code, result = req(t, a.confirmDraft, u, M{"id": rejectedID, "version": "1", "confirm_amount": "100"})
	if code != 409 || !strings.Contains(result["error"].(string), "не хватает 100.00 ₽") {
		t.Fatal("cash shortfall not explained", code, result)
	}
	code, _ = req(t, a.rejectDraft, u, M{"id": rejectedID, "version": "2", "reason": "дубль"})
	if code != 409 {
		t.Fatal("stale rejection accepted")
	}
	code, result = req(t, a.rejectDraft, u, M{"id": rejectedID, "version": "1", "reason": "дубль"})
	if code != 200 || result["status"] != "rejected" {
		t.Fatal("draft rejection failed", code, result)
	}
	var rejectedVersion int
	if e = a.db.QueryRow("SELECT version FROM drafts WHERE id=$1", rejectedID).Scan(&rejectedVersion); e != nil || rejectedVersion != 1 {
		t.Fatal("rejection changed the draft version", rejectedVersion, e)
	}
	code, result = req(t, a.rejectDraft, u, M{"id": rejectedID, "version": "1", "reason": "дубль"})
	if code != 200 || result["status"] != "already_rejected" {
		t.Fatal("draft rejection retry failed", code, result)
	}
	code, _ = req(t, a.confirmDraft, u, M{"id": rejectedID, "version": "1", "confirm_amount": "100"})
	if code != 409 {
		t.Fatal("rejected draft posted")
	}
	var entries, audits int
	if e = a.db.QueryRow("SELECT count(*) FROM journal_entries WHERE event_id=$1", rejectedID).Scan(&entries); e != nil || entries != 0 {
		t.Fatal("rejection created ledger entry", entries, e)
	}
	if e = a.db.QueryRow("SELECT count(*) FROM audit_events WHERE object_id=$1 AND action='draft_reject' AND outcome='success' AND reason='дубль'", rejectedID).Scan(&audits); e != nil || audits != 1 {
		t.Fatal("rejection audit mismatch", audits, e)
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
	file.SetCellValue("Sheet1", "A2", "0000000000001234")
	file.SetCellValue("Sheet1", "B2", "1234.64")
	file.SetCellValue("Sheet1", "A3", "000000******1234")
	file.SetCellValue("Sheet1", "B3", "987.70")
	blob, e := file.WriteToBuffer()
	if e != nil {
		t.Fatal(e)
	}
	file.Close()
	upload := func(ref, merchantID, filename string) (int, M) {
		var body bytes.Buffer
		mw := multipart.NewWriter(&body)
		_ = mw.WriteField("merchant_id", merchantID)
		_ = mw.WriteField("external_ref", ref)
		fw, _ := mw.CreateFormFile("file", filename)
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
	pendingMerchant := id()
	if _, e = a.db.Exec("INSERT INTO merchants(id,code,name) VALUES($1,'PENDING','Без образца')", pendingMerchant); e != nil {
		t.Fatal(e)
	}
	if _, e = a.db.Exec("INSERT INTO merchant_import_profiles(merchant_id,status) VALUES($1,'awaiting_sample')", pendingMerchant); e != nil {
		t.Fatal(e)
	}
	if code, _ := upload("PENDING", pendingMerchant, "synthetic.xlsx"); code != 400 {
		t.Fatal("merchant without parser accepted", code)
	}
	if code, _ := upload("WRONG-EXT", merchant, "synthetic.csv"); code != 400 {
		t.Fatal("wrong parser extension accepted", code)
	}
	code, out := upload("X1", merchant, "synthetic.xlsx")
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
	var raw string
	var parser string
	var parserVersion int
	if e := a.db.QueryRow("SELECT rr.raw::text,s.parser_code,s.parser_version FROM registry_rows rr JOIN registries r ON r.id=rr.registry_id JOIN source_documents s ON s.id=r.source_id WHERE r.id=$1 AND rr.row_no=2", reg).Scan(&raw, &parser, &parserVersion); e != nil || strings.Contains(raw, "0000000000001234") || parser != "generic_xlsx_v1" || parserVersion != 1 {
		t.Fatal("parser snapshot or PAN redaction failed", e)
	}
	code, _ = upload("X2", merchant, "synthetic.xlsx")
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
	code, _ = req(t, a.observation, operator, M{"card_id": card, "amount": "0.00"})
	if code != 403 {
		t.Fatal("operator changed observed balance", code)
	}
	var observations int
	if e = a.db.QueryRow("SELECT count(*) FROM observations").Scan(&observations); e != nil || observations != 0 {
		t.Fatal("operator persisted a balance observation", observations, e)
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
