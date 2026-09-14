package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/xuri/excelize/v2"
)

const defaultPaymentCents int64 = 25_000_000

var requestReferencePattern = regexp.MustCompile(`^[\p{L}\p{N}][\p{L}\p{N}._/-]{0,79}$`)
var syntheticNames = []string{
	"Тестов Алексей Учебович", "Демина Мария Примеровна",
	"Образцов Илья Тестович", "Учебная Анна Образцовна",
	"Примеров Павел Демович", "Тестова Елена Учебовна",
}

func approvedSyntheticName(name string) bool {
	for _, candidate := range syntheticNames {
		if name == candidate {
			return true
		}
	}
	return false
}

type plannedPayment struct {
	cardID, mask, bank, number, name string
	amount                           int64
}

func demoCardIdentity(cardID, mask string) (string, string, error) {
	if !cardMaskPattern.MatchString(mask) {
		return "", "", errors.New("неверная маска карты")
	}
	h := sha256.Sum256([]byte(cardID))
	middle := binary.BigEndian.Uint32(h[:4]) % 1_000_000
	number := mask[:6] + fmt.Sprintf("%06d", middle) + mask[len(mask)-4:]
	if validLuhn(number) {
		b := []byte(number)
		if b[6] == '9' {
			b[6] = '0'
		} else {
			b[6]++
		}
		number = string(b)
	}
	return number, syntheticNames[int(h[4])%len(syntheticNames)], nil
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

func paymentPlan(mode, countText, totalText, perText string) ([]int64, error) {
	per := defaultPaymentCents
	if perText != "" {
		var e error
		per, e = amount(perText)
		if e != nil {
			return nil, e
		}
	}
	if per <= 0 {
		return nil, errors.New("сумма платежа должна быть положительной")
	}
	if mode == "count" {
		count, e := strconv.Atoi(countText)
		if e != nil || count < 1 || count > 500 || per > math.MaxInt64/int64(count) {
			return nil, errors.New("количество платежей должно быть от 1 до 500")
		}
		out := make([]int64, count)
		for i := range out {
			out[i] = per
		}
		return out, nil
	}
	if mode != "total" {
		return nil, errors.New("выберите количество платежей или общую сумму")
	}
	total, e := amount(totalText)
	if e != nil {
		return nil, e
	}
	count := total / per
	if total%per != 0 {
		count++
	}
	if count < 1 || count > 500 {
		return nil, errors.New("для этой суммы нужно больше 500 платежей")
	}
	out := make([]int64, count)
	remaining := total
	for i := range out {
		out[i] = per
		if remaining < per {
			out[i] = remaining
		}
		remaining -= out[i]
	}
	return out, nil
}

func requestWorkbook(reference, merchant string, rows []plannedPayment) ([]byte, error) {
	f := excelize.NewFile()
	defer f.Close()
	sheet := "Карты к оплате"
	f.SetSheetName("Sheet1", sheet)
	f.SetCellStr(sheet, "A1", "ДЕМО: вымышленные данные, платежи по этим номерам невозможны")
	f.SetCellStr(sheet, "A2", "Мерчант: "+merchant+" · Запрос: "+reference)
	for i, title := range []string{"№", "НОМЕР КАРТЫ", "ФИО", "СУММА, ₽", "БАНК"} {
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
		f.SetCellStr(sheet, fmt.Sprintf("D%d", n), rub(row.amount))
		f.SetCellStr(sheet, fmt.Sprintf("E%d", n), row.bank)
	}
	f.SetColWidth(sheet, "A", "A", 7)
	f.SetColWidth(sheet, "B", "B", 24)
	f.SetColWidth(sheet, "C", "C", 35)
	f.SetColWidth(sheet, "D", "D", 20)
	f.SetColWidth(sheet, "E", "E", 25)
	b, e := f.WriteToBuffer()
	if e != nil {
		return nil, e
	}
	return b.Bytes(), nil
}

func (a *App) createPaymentRequest(w http.ResponseWriter, r *http.Request, u User) {
	if r.Method != http.MethodPost || !a.require(w, u, "chief") {
		return
	}
	if os.Getenv("APP_ENV") != "demo" && os.Getenv("APP_ENV") != "testing" {
		fail(w, 403, errors.New("выгрузка полных номеров доступна только в демонстрационном окружении"))
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
	plan, e := paymentPlan(str(m, "mode"), str(m, "payment_count"), str(m, "total"), str(m, "per_payment"))
	if e != nil {
		fail(w, 400, e)
		return
	}
	perPayment := defaultPaymentCents
	if value := str(m, "per_payment"); value != "" {
		perPayment, _ = amount(value)
	}
	tx, e := a.tx()
	if e != nil {
		fail(w, 500, e)
		return
	}
	defer tx.Rollback()
	merchantID := str(m, "merchant_id")
	var merchant string
	if e = tx.QueryRow("SELECT name FROM merchants WHERE id=$1 AND active", merchantID).Scan(&merchant); e != nil {
		fail(w, 400, errors.New("выберите действующего мерчанта"))
		return
	}
	cardRows, e := tx.Query("SELECT c.id,c.mask,b.name,c.owner_label FROM cards c JOIN banks b ON b.id=c.bank_id WHERE c.status='active' ORDER BY b.name,c.mask,c.id")
	if e != nil {
		fail(w, 500, e)
		return
	}
	cards := []plannedPayment{}
	seenNumbers := map[string]bool{}
	for cardRows.Next() {
		var card plannedPayment
		var ownerLabel string
		if e = cardRows.Scan(&card.cardID, &card.mask, &card.bank, &ownerLabel); e != nil {
			break
		}
		card.number, card.name, e = demoCardIdentity(card.cardID, card.mask)
		if e != nil {
			break
		}
		if approvedSyntheticName(ownerLabel) {
			card.name = ownerLabel
		}
		if seenNumbers[card.number] {
			e = errors.New("для двух карт получился одинаковый учебный номер; измените состав карт")
			break
		}
		seenNumbers[card.number] = true
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
	selected := []plannedPayment{}
	for i, cents := range plan {
		card := cards[i%len(cards)]
		card.amount = cents
		selected = append(selected, card)
	}
	var total int64
	for _, v := range plan {
		total += v
	}
	file, e := requestWorkbook(ref, merchant, selected)
	if e != nil {
		fail(w, 500, e)
		return
	}
	requestID := id()
	path := filepath.Join(a.storage, "payment-request-"+requestID+".xlsx")
	if e = os.WriteFile(path, file, 0600); e != nil {
		fail(w, 500, e)
		return
	}
	defer func() {
		if e != nil {
			_ = os.Remove(path)
		}
	}()
	hash := sha256.Sum256(file)
	_, e = tx.Exec("INSERT INTO payment_requests(id,merchant_id,external_ref,mode,payment_count,per_payment_cents,requested_total_cents,export_path,export_sha256,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)", requestID, merchantID, ref, str(m, "mode"), len(plan), perPayment, total, path, hex.EncodeToString(hash[:]), u.ID)
	if e != nil {
		fail(w, 409, errors.New("такой номер запроса уже есть у мерчанта"))
		return
	}
	for i, row := range selected {
		_, e = tx.Exec("INSERT INTO payment_request_rows(id,request_id,row_no,card_id,planned_cents,synthetic_number,synthetic_name) VALUES($1,$2,$3,$4,$5,$6,$7)", id(), requestID, i+1, row.cardID, row.amount, row.number, row.name)
		if e != nil {
			fail(w, 500, e)
			return
		}
	}
	if e = txAudit(tx, u.ID, "web", "payment_request_create", "payment_request", requestID, "success", "", M{"payment_count": len(plan), "planned_total_cents": total, "export_sha256": hex.EncodeToString(hash[:])}); e != nil {
		fail(w, 500, e)
		return
	}
	if e = tx.Commit(); e != nil {
		fail(w, 409, e)
		return
	}
	respond(w, 201, M{"id": requestID, "payment_count": len(plan), "planned_total": rub(total), "card_count": len(cards), "repeated_cards": len(plan) > len(cards)})
}

func (a *App) paymentRequests(w http.ResponseWriter, r *http.Request, u User) {
	if r.Method != http.MethodGet || !a.require(w, u, "chief", "operator", "accountant", "auditor") {
		return
	}
	rows, e := a.db.Query(`SELECT p.id,p.merchant_id,m.name,p.external_ref,p.mode,p.payment_count,p.requested_total_cents,p.created_at,
		COALESCE((SELECT SUM(r.total_cents) FROM registries r WHERE r.payment_request_id=p.id AND r.status='posted'),0),
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
		var planned, received int64
		var createdAt interface{}
		if e = rows.Scan(&reqID, &merchantID, &merchant, &ref, &mode, &count, &planned, &createdAt, &received, &postedCount); e != nil {
			fail(w, 500, e)
			return
		}
		out = append(out, M{"id": reqID, "merchant_id": merchantID, "merchant": merchant, "external_ref": ref, "mode": mode, "payment_count": count, "planned_total": rub(planned), "received_total": rub(received), "difference": rub(planned - received), "response_count": postedCount, "created_at": createdAt})
	}
	respond(w, 200, out)
}

func (a *App) paymentRequestRows(w http.ResponseWriter, r *http.Request, u User) {
	if r.Method != http.MethodGet || !a.require(w, u, "chief", "operator", "accountant", "auditor") {
		return
	}
	rows, e := a.db.Query("SELECT x.row_no,c.mask,b.name,x.synthetic_name,x.planned_cents FROM payment_request_rows x JOIN cards c ON c.id=x.card_id JOIN banks b ON b.id=c.bank_id WHERE x.request_id=$1 ORDER BY x.row_no", r.URL.Query().Get("id"))
	if e != nil {
		fail(w, 500, e)
		return
	}
	defer rows.Close()
	out := []M{}
	for rows.Next() {
		var n int
		var mask, bank, name string
		var cents int64
		if e = rows.Scan(&n, &mask, &bank, &name, &cents); e != nil {
			fail(w, 500, e)
			return
		}
		out = append(out, M{"row_no": n, "mask": mask, "bank": bank, "synthetic_name": name, "amount": rub(cents)})
	}
	respond(w, 200, out)
}

func (a *App) paymentRequestExport(w http.ResponseWriter, r *http.Request, u User) {
	if r.Method != http.MethodGet || !a.require(w, u, "chief") {
		return
	}
	if os.Getenv("APP_ENV") != "demo" && os.Getenv("APP_ENV") != "testing" {
		fail(w, 403, errors.New("экспорт доступен только в демо"))
		return
	}
	requestID := r.URL.Query().Get("id")
	var path, expected string
	if e := a.db.QueryRow("SELECT export_path,export_sha256 FROM payment_requests WHERE id=$1", requestID).Scan(&path, &expected); e != nil {
		fail(w, 404, errors.New("запрос не найден"))
		return
	}
	if filepath.Clean(filepath.Dir(path)) != filepath.Clean(a.storage) || filepath.Base(path) != "payment-request-"+requestID+".xlsx" {
		fail(w, 500, errors.New("недопустимый путь выгрузки"))
		return
	}
	file, e := os.ReadFile(path)
	if e != nil {
		fail(w, 500, errors.New("файл выгрузки недоступен"))
		return
	}
	hash := sha256.Sum256(file)
	if hex.EncodeToString(hash[:]) != expected {
		fail(w, 500, errors.New("контрольная сумма выгрузки не совпала"))
		return
	}
	if e = a.logExport(u.ID, requestID, expected); e != nil {
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
	rows, e := tx.Query("SELECT x.synthetic_number,c.mask,x.card_id FROM payment_request_rows x JOIN cards c ON c.id=x.card_id WHERE x.request_id=$1", requestID)
	if e != nil {
		return nil, nil, e
	}
	defer rows.Close()
	numbers, masks := map[string]string{}, map[string]string{}
	for rows.Next() {
		var number, mask, card string
		if e = rows.Scan(&number, &mask, &card); e != nil {
			return nil, nil, e
		}
		numbers[strings.TrimSpace(number)] = card
		if prior, ok := masks[mask]; ok && prior != card {
			masks[mask] = ""
		} else if !ok {
			masks[mask] = card
		}
	}
	return numbers, masks, rows.Err()
}
