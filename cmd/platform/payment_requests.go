package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/xuri/excelize/v2"
)

var requestReferencePattern = regexp.MustCompile(`^[\p{L}\p{N}][\p{L}\p{N}._/-]{0,79}$`)
var autoRequestReferencePattern = regexp.MustCompile(`^ЗК-[0-9]{4}-([0-9]{4,})$`)
var paymentPhonePattern = regexp.MustCompile(`^\+[1-9][0-9]{10,14}$`)

type requestCard struct {
	cardID, mask, bank, number, name, phone, contactID string
}

func normalizePaymentPhone(raw string) (string, error) {
	s := strings.NewReplacer(" ", "", "-", "", "(", "", ")", "").Replace(strings.TrimSpace(raw))
	if strings.HasPrefix(s, "8") && len(s) == 11 {
		s = "+7" + s[1:]
	} else if !strings.HasPrefix(s, "+") {
		s = "+" + s
	}
	if !paymentPhonePattern.MatchString(s) {
		return "", errors.New("номер телефона должен содержать от 11 до 15 цифр и код страны")
	}
	return s, nil
}

type cardRequestRow struct {
	cardID, contactID string
}

func cardRequestRows(m M) ([]cardRequestRow, error) {
	raw, ok := m["rows"].([]interface{})
	if !ok || len(raw) < 1 || len(raw) > 500 {
		return nil, errors.New("добавьте от 1 до 500 строк")
	}
	rows := make([]cardRequestRow, 0, len(raw))
	seen := map[string]bool{}
	for i, value := range raw {
		item, ok := value.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("строка %d: неверные данные", i+1)
		}
		cardID, _ := item["card_id"].(string)
		contactID, _ := item["contact_id"].(string)
		if _, hasAmount := item["amount"]; hasAmount {
			return nil, fmt.Errorf("строка %d: сумму назначает мерчант в ответном реестре", i+1)
		}
		if _, hasAmount := item["planned_cents"]; hasAmount {
			return nil, fmt.Errorf("строка %d: сумму назначает мерчант в ответном реестре", i+1)
		}
		if cardID == "" || contactID == "" {
			return nil, fmt.Errorf("строка %d: выберите карту и контакт", i+1)
		}
		if seen[cardID] {
			return nil, fmt.Errorf("строка %d: карта уже есть в запросе", i+1)
		}
		seen[cardID] = true
		rows = append(rows, cardRequestRow{cardID, contactID})
	}
	return rows, nil
}

func (a *App) paymentRequestNextReference(w http.ResponseWriter, r *http.Request, u User) {
	if r.Method != http.MethodGet || !a.require(w, u, "chief", "operator") {
		return
	}
	year := time.Now().In(a.location).Year()
	prefix := fmt.Sprintf("ЗК-%d-", year)
	rows, e := a.db.Query("SELECT external_ref FROM payment_requests WHERE external_ref LIKE $1", prefix+"%")
	if e != nil {
		fail(w, 500, e)
		return
	}
	defer rows.Close()
	maxNumber := 0
	for rows.Next() {
		var ref string
		if e = rows.Scan(&ref); e != nil {
			fail(w, 500, e)
			return
		}
		match := autoRequestReferencePattern.FindStringSubmatch(ref)
		if len(match) != 2 {
			continue
		}
		n, parseError := strconv.Atoi(match[1])
		if parseError == nil && n > maxNumber {
			maxNumber = n
		}
	}
	if e = rows.Err(); e != nil {
		fail(w, 500, e)
		return
	}
	respond(w, 200, M{"next_reference": fmt.Sprintf("%s%04d", prefix, maxNumber+1)})
}

func (a *App) paymentContacts(w http.ResponseWriter, r *http.Request, u User) {
	if r.Method != http.MethodGet || !a.require(w, u, "chief", "operator") {
		return
	}
	page, e := strconv.Atoi(r.URL.Query().Get("page"))
	if e != nil || page < 1 {
		page = 1
	}
	pageSize, e := strconv.Atoi(r.URL.Query().Get("page_size"))
	if e != nil || pageSize < 1 {
		pageSize = 10
	}
	if pageSize > 50 {
		pageSize = 50
	}
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	namePattern := "%" + query + "%"
	phoneQuery := strings.NewReplacer(" ", "", "-", "", "(", "", ")", "").Replace(query)
	phonePattern := "%" + phoneQuery + "%"
	var total int
	if e = a.db.QueryRow("SELECT count(*) FROM payment_contacts WHERE active AND (full_name ILIKE $1 OR phone ILIKE $2)", namePattern, phonePattern).Scan(&total); e != nil {
		fail(w, 500, e)
		return
	}
	rows, e := a.db.Query("SELECT id,full_name,phone FROM payment_contacts WHERE active AND (full_name ILIKE $1 OR phone ILIKE $2) ORDER BY full_name,phone,id LIMIT $3 OFFSET $4", namePattern, phonePattern, pageSize, (page-1)*pageSize)
	if e != nil {
		fail(w, 500, e)
		return
	}
	defer rows.Close()
	items := []M{}
	for rows.Next() {
		var contactID, fullName, phone string
		if e = rows.Scan(&contactID, &fullName, &phone); e != nil {
			fail(w, 500, e)
			return
		}
		items = append(items, M{"id": contactID, "full_name": fullName, "phone": phone})
	}
	if e = rows.Err(); e != nil {
		fail(w, 500, e)
		return
	}
	respond(w, 200, M{"items": items, "page": page, "page_size": pageSize, "total": total, "has_more": page*pageSize < total})
}

func validLuhn(number string) bool {
	sum := 0
	for i := len(number) - 1; i >= 0; i-- {
		v := int(number[i] - '0')
		if (len(number)-1-i)%2 == 1 {
			v *= 2
			if v > 9 {
				v -= 9
			}
		}
		sum += v
	}
	return sum%10 == 0
}

func requestWorkbook(reference string, rows []requestCard) ([]byte, error) {
	f := excelize.NewFile()
	defer f.Close()
	sheet := "Карты к оплате"
	f.SetSheetName("Sheet1", sheet)
	f.SetCellStr(sheet, "A1", "Запрос: "+reference)
	f.SetCellStr(sheet, "A2", "Суммы указывает мерчант в ответном реестре")
	for i, title := range []string{"№", "НОМЕР КАРТЫ", "ФИО", "НОМЕР ТЕЛЕФОНА", "БАНК"} {
		cell, _ := excelize.CoordinatesToCellName(i+1, 4)
		f.SetCellStr(sheet, cell, title)
	}
	textStyle, e := f.NewStyle(&excelize.Style{NumFmt: 49})
	if e != nil {
		return nil, e
	}
	for i, row := range rows {
		n := i + 5
		f.SetCellInt(sheet, fmt.Sprintf("A%d", n), int64(i+1))
		f.SetCellStr(sheet, fmt.Sprintf("B%d", n), row.number)
		f.SetCellStyle(sheet, fmt.Sprintf("B%d", n), fmt.Sprintf("B%d", n), textStyle)
		f.SetCellStr(sheet, fmt.Sprintf("C%d", n), row.name)
		f.SetCellStr(sheet, fmt.Sprintf("D%d", n), row.phone)
		f.SetCellStyle(sheet, fmt.Sprintf("D%d", n), fmt.Sprintf("D%d", n), textStyle)
		f.SetCellStr(sheet, fmt.Sprintf("E%d", n), row.bank)
	}
	f.SetColWidth(sheet, "A", "A", 7)
	f.SetColWidth(sheet, "B", "B", 24)
	f.SetColWidth(sheet, "C", "C", 35)
	f.SetColWidth(sheet, "D", "D", 22)
	f.SetColWidth(sheet, "E", "E", 25)
	b, e := f.WriteToBuffer()
	if e != nil {
		return nil, e
	}
	return b.Bytes(), nil
}

func (a *App) createPaymentRequest(w http.ResponseWriter, r *http.Request, u User) {
	if r.Method != http.MethodPost || !a.require(w, u, "chief", "operator") {
		return
	}
	m, e := jsonBody(r)
	if e != nil {
		fail(w, 400, e)
		return
	}
	ref := strings.TrimSpace(str(m, "external_ref"))
	if !requestReferencePattern.MatchString(ref) {
		fail(w, 400, errors.New("номер запроса: до 80 букв, цифр и знаков . _ / -"))
		return
	}
	mode := str(m, "mode")
	if mode != "cards" {
		fail(w, 400, errors.New("создайте запрос из выбранных карт без суммы"))
		return
	}
	for _, field := range []string{"total", "per_payment", "payment_count", "amount", "requested_total_cents", "per_payment_cents"} {
		if _, supplied := m[field]; supplied {
			fail(w, 400, errors.New("сумму назначает мерчант в ответном реестре"))
			return
		}
	}
	manualRows, e := cardRequestRows(m)
	if e != nil {
		fail(w, 400, e)
		return
	}
	tx, e := a.tx()
	if e != nil {
		fail(w, 500, e)
		return
	}
	defer tx.Rollback()
	merchantID := str(m, "merchant_id")
	var merchantExists bool
	if e = tx.QueryRow("SELECT EXISTS(SELECT 1 FROM merchants WHERE id=$1 AND active)", merchantID).Scan(&merchantExists); e != nil || !merchantExists {
		fail(w, 400, errors.New("выберите действующего мерчанта"))
		return
	}
	cardRows, e := tx.Query("SELECT c.id,c.mask,b.name FROM cards c JOIN banks b ON b.id=c.bank_id WHERE c.status='active' ORDER BY b.name,c.mask,c.id")
	if e != nil {
		fail(w, 500, e)
		return
	}
	cards := []requestCard{}
	for cardRows.Next() {
		var card requestCard
		if e = cardRows.Scan(&card.cardID, &card.mask, &card.bank); e != nil {
			break
		}
		cards = append(cards, card)
	}
	if e == nil {
		e = cardRows.Err()
	}
	cardRows.Close()
	if e != nil {
		fail(w, 500, e)
		return
	}
	if len(cards) == 0 {
		fail(w, 409, errors.New("нет доступных карт"))
		return
	}
	if len(manualRows) > len(cards) {
		fail(w, 409, fmt.Errorf("нужно %d разных карт, доступно %d", len(manualRows), len(cards)))
		return
	}
	cardsByID := make(map[string]requestCard, len(cards))
	for _, card := range cards {
		cardsByID[card.cardID] = card
	}
	contacts := map[string]requestCard{}
	contactRows, e := tx.Query("SELECT id,full_name,phone FROM payment_contacts WHERE active")
	if e != nil {
		fail(w, 500, e)
		return
	}
	for contactRows.Next() {
		var contact requestCard
		if e = contactRows.Scan(&contact.contactID, &contact.name, &contact.phone); e != nil {
			break
		}
		contacts[contact.contactID] = contact
	}
	if e == nil {
		e = contactRows.Err()
	}
	contactRows.Close()
	if e != nil {
		fail(w, 500, e)
		return
	}
	selected := []requestCard{}
	for i, row := range manualRows {
		card, ok := cardsByID[row.cardID]
		if !ok {
			fail(w, 400, fmt.Errorf("строка %d: карта недоступна", i+1))
			return
		}
		contact, ok := contacts[row.contactID]
		if !ok {
			fail(w, 400, fmt.Errorf("строка %d: выбранный контакт недоступен", i+1))
			return
		}
		var currentMask string
		var ciphertext []byte
		if e = tx.QueryRow("SELECT mask,pan_ciphertext FROM cards WHERE id=$1 AND status='active' FOR SHARE", card.cardID).Scan(&currentMask, &ciphertext); e != nil {
			fail(w, 409, fmt.Errorf("строка %d: карта недоступна", i+1))
			return
		}
		if _, e = readCardPAN(card.cardID, ciphertext); e != nil {
			fail(w, 409, fmt.Errorf("строка %d: полный номер карты не сохранён", i+1))
			return
		}
		card.mask = currentMask
		card.contactID, card.name, card.phone = contact.contactID, contact.name, contact.phone
		selected = append(selected, card)
	}
	requestID := id()
	_, e = tx.Exec("INSERT INTO payment_requests(id,merchant_id,external_ref,mode,payment_count,created_by) VALUES($1,$2,$3,'cards',$4,$5)", requestID, merchantID, ref, len(selected), u.ID)
	if e != nil {
		fail(w, 409, errors.New("такой номер запроса уже есть у мерчанта"))
		return
	}
	for i, row := range selected {
		_, e = tx.Exec("INSERT INTO payment_request_rows(id,request_id,row_no,card_id,contact_id,contact_name,contact_phone) VALUES($1,$2,$3,$4,$5,$6,$7)", id(), requestID, i+1, row.cardID, row.contactID, row.name, row.phone)
		if e != nil {
			fail(w, 500, e)
			return
		}
	}
	if e = txAudit(tx, u.ID, "web", "payment_request_create", "payment_request", requestID, "success", "", M{"card_count": len(selected)}); e != nil {
		fail(w, 500, e)
		return
	}
	if e = tx.Commit(); e != nil {
		fail(w, 409, e)
		return
	}
	respond(w, 201, M{"id": requestID, "card_count": len(selected)})
}

func (a *App) paymentRequests(w http.ResponseWriter, r *http.Request, u User) {
	if r.Method != http.MethodGet || !a.require(w, u, "chief", "operator", "accountant", "auditor") {
		return
	}
	rows, e := a.db.Query(`SELECT p.id,p.merchant_id,m.name,p.external_ref,p.mode,p.payment_count,p.created_at,
		(SELECT COUNT(*) FROM registries r WHERE r.payment_request_id=p.id AND r.status='posted')
		FROM payment_requests p JOIN merchants m ON m.id=p.merchant_id ORDER BY p.created_at DESC LIMIT 100`)
	if e != nil {
		fail(w, 500, e)
		return
	}
	defer rows.Close()
	out := []M{}
	for rows.Next() {
		var reqID, merchantID, merchant, ref, mode string
		var count, postedCount int
		var createdAt interface{}
		if e = rows.Scan(&reqID, &merchantID, &merchant, &ref, &mode, &count, &createdAt, &postedCount); e != nil {
			fail(w, 500, e)
			return
		}
		out = append(out, M{"id": reqID, "merchant_id": merchantID, "merchant": merchant, "external_ref": ref, "mode": mode, "card_count": count, "response_count": postedCount, "created_at": createdAt})
	}
	respond(w, 200, out)
}

func (a *App) paymentRequestRows(w http.ResponseWriter, r *http.Request, u User) {
	if r.Method != http.MethodGet || !a.require(w, u, "chief", "operator", "accountant", "auditor") {
		return
	}
	rows, e := a.db.Query("SELECT x.row_no,c.mask,b.name,COALESCE(x.contact_name,x.synthetic_name),COALESCE(x.contact_phone,'') FROM payment_request_rows x JOIN cards c ON c.id=x.card_id JOIN banks b ON b.id=c.bank_id WHERE x.request_id=$1 ORDER BY x.row_no", r.URL.Query().Get("id"))
	if e != nil {
		fail(w, 500, e)
		return
	}
	defer rows.Close()
	out := []M{}
	canSeePhone := u.Role == "chief" || u.Role == "operator"
	for rows.Next() {
		var n int
		var mask, bank, name, phone string
		if e = rows.Scan(&n, &mask, &bank, &name, &phone); e != nil {
			fail(w, 500, e)
			return
		}
		if !canSeePhone {
			phone = ""
		}
		out = append(out, M{"row_no": n, "mask": mask, "bank": bank, "contact_name": name, "contact_phone": phone})
	}
	respond(w, 200, out)
}

func (a *App) paymentRequestExport(w http.ResponseWriter, r *http.Request, u User) {
	if r.Method != http.MethodGet || !a.require(w, u, "chief", "operator") {
		return
	}
	requestID := r.URL.Query().Get("id")
	var reference string
	if e := a.db.QueryRow("SELECT external_ref FROM payment_requests WHERE id=$1 AND mode='cards'", requestID).Scan(&reference); e != nil {
		fail(w, 404, errors.New("запрос не найден"))
		return
	}
	rows, e := a.db.Query("SELECT x.card_id,c.mask,b.name,x.contact_name,x.contact_phone,c.pan_ciphertext FROM payment_request_rows x JOIN cards c ON c.id=x.card_id JOIN banks b ON b.id=c.bank_id WHERE x.request_id=$1 ORDER BY x.row_no", requestID)
	if e != nil {
		fail(w, 500, e)
		return
	}
	selected := []requestCard{}
	for rows.Next() {
		var card requestCard
		var ciphertext []byte
		if e = rows.Scan(&card.cardID, &card.mask, &card.bank, &card.name, &card.phone, &ciphertext); e != nil {
			break
		}
		card.number, e = readCardPAN(card.cardID, ciphertext)
		if e != nil {
			break
		}
		selected = append(selected, card)
	}
	if e == nil {
		e = rows.Err()
	}
	rows.Close()
	if e != nil || len(selected) == 0 {
		fail(w, 500, errors.New("полные номера карт недоступны для экспорта"))
		return
	}
	file, e := requestWorkbook(reference, selected)
	if e != nil {
		fail(w, 500, e)
		return
	}
	hash := sha256.Sum256(file)
	if e = a.logExport(u.ID, requestID, hex.EncodeToString(hash[:])); e != nil {
		fail(w, 500, e)
		return
	}
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", "attachment; filename=payment-request-"+requestID+".xlsx")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(file)
}

func (a *App) logExport(actor, requestID, hash string) error {
	_, e := a.db.Exec("INSERT INTO audit_events(id,actor_id,actor_role,channel,action,object_type,object_id,outcome,detail) VALUES($1,$2,(SELECT role FROM users WHERE id=$2),'web','payment_request_export','payment_request',$3,'success',$4)", id(), actor, requestID, encode(M{"sha256": hash}))
	return e
}

func verifyRequest(tx *sql.Tx, requestID, merchantID string) (map[string]string, map[string]string, error) {
	var owner string
	if e := tx.QueryRow("SELECT merchant_id FROM payment_requests WHERE id=$1", requestID).Scan(&owner); e != nil || owner != merchantID {
		return nil, nil, errors.New("запрос на карты не принадлежит выбранному мерчанту")
	}
	rows, e := tx.Query("SELECT c.mask,x.card_id,c.pan_ciphertext FROM payment_request_rows x JOIN cards c ON c.id=x.card_id WHERE x.request_id=$1", requestID)
	if e != nil {
		return nil, nil, e
	}
	defer rows.Close()
	numbers, masks := map[string]string{}, map[string]string{}
	for rows.Next() {
		var mask, card string
		var ciphertext []byte
		if e = rows.Scan(&mask, &card, &ciphertext); e != nil {
			return nil, nil, e
		}
		pan, panErr := readCardPAN(card, ciphertext)
		if panErr != nil {
			return nil, nil, errors.New("полный номер карты из запроса недоступен")
		}
		numbers[pan] = card
		if prior, ok := masks[mask]; ok && prior != card {
			masks[mask] = ""
		} else if !ok {
			masks[mask] = card
		}
	}
	return numbers, masks, rows.Err()
}
