package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/xuri/excelize/v2"
	"golang.org/x/text/encoding/charmap"
)

type importedRow struct {
	Number                        int
	Sheet                         string
	Raw                           []string
	Mask, Order, RRN, Error, Card string
	Amount, BankFee, Transfer     int64
}

var longDigits = regexp.MustCompile(`[0-9]{12,19}`)

func safeRaw(values []string) []string {
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = longDigits.ReplaceAllString(v, "[redacted]")
	}
	return out
}

func (a *App) upload(w http.ResponseWriter, r *http.Request, u User) {
	if r.Method != "POST" || !a.require(w, u, "operator", "chief") {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 12<<20)
	if e := r.ParseMultipartForm(12 << 20); e != nil {
		fail(w, 400, e)
		return
	}
	file, h, e := r.FormFile("file")
	if e != nil {
		fail(w, 400, e)
		return
	}
	defer file.Close()
	data, e := io.ReadAll(io.LimitReader(file, 12<<20))
	if e != nil {
		fail(w, 400, e)
		return
	}
	if len(data) == 0 || len(data) >= 12<<20 {
		fail(w, 400, errors.New("размер файла"))
		return
	}
	kind := "xlsx"
	ext := strings.ToLower(filepath.Ext(h.Filename))
	if ext != ".csv" && ext != ".xlsx" {
		fail(w, 400, errors.New("поддержаны только .xlsx и .csv"))
		return
	}
	if ext == ".csv" {
		kind = "bank_csv"
	}
	rows, e := parseRows(kind, data)
	if e != nil {
		fail(w, 400, e)
		return
	}
	merchant := r.FormValue("merchant_id")
	external := strings.TrimSpace(r.FormValue("external_ref"))
	if external == "" {
		fail(w, 400, errors.New("нужен номер реестра"))
		return
	}
	hash := sha256.Sum256(data)
	sum := hex.EncodeToString(hash[:])
	tx, e := a.tx()
	if e != nil {
		fail(w, 500, e)
		return
	}
	defer tx.Rollback()
	requestID := strings.TrimSpace(r.FormValue("payment_request_id"))
	var requestNumbers, requestMasks map[string]string
	if requestID != "" {
		requestNumbers, requestMasks, e = verifyRequest(tx, requestID, merchant)
		if e != nil {
			fail(w, 400, e)
			return
		}
	}
	var exists bool
	if e = tx.QueryRow("SELECT EXISTS(SELECT 1 FROM source_documents WHERE sha256=$1)", sum).Scan(&exists); e != nil {
		fail(w, 500, e)
		return
	}
	if exists {
		fail(w, 409, errors.New("файл уже загружен"))
		return
	}
	var total int64
	for i := range rows {
		if rows[i].Error == "" {
			var card string
			var err error
			if requestID != "" {
				value := strings.TrimSpace(rows[i].Mask)
				card = requestNumbers[value]
				if card == "" {
					card = requestMasks[value]
				}
				if card == "" {
					err = errors.New("карта не найдена в выбранном запросе или неоднозначна")
				}
			} else {
				card, err = a.resolveCard(tx, rows[i].Mask)
			}
			if err != nil {
				rows[i].Error = err.Error()
			} else if !a.cardAllowed(u, card) {
				rows[i].Error = "card_not_assigned"
			} else {
				rows[i].Card = card
				if rows[i].Amount > math.MaxInt64-total {
					fail(w, 400, errors.New("сумма реестра слишком велика"))
					return
				}
				total += rows[i].Amount
			}
		}
	}
	if total <= 0 {
		fail(w, 400, errors.New("нет валидных строк"))
		return
	}
	sourceID, regID := id(), id()
	path := filepath.Join(a.storage, sum)
	stored, openErr := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if openErr != nil {
		fail(w, 409, errors.New("этот файл уже обрабатывается или загружен"))
		return
	}
	keepFile := false
	defer func() {
		if !keepFile {
			_ = os.Remove(path)
		}
	}()
	if written, writeErr := stored.Write(data); writeErr != nil || written != len(data) {
		_ = stored.Close()
		if writeErr == nil {
			writeErr = io.ErrShortWrite
		}
		fail(w, 500, writeErr)
		return
	}
	if e = stored.Close(); e != nil {
		fail(w, 500, e)
		return
	}
	_, e = tx.Exec("INSERT INTO source_documents(id,kind,filename,sha256,media_type,byte_size,storage_path,uploader_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8)", sourceID, kind, safeFilename(h.Filename), sum, h.Header.Get("Content-Type"), len(data), path, u.ID)
	if e != nil {
		fail(w, 409, e)
		return
	}
	_, e = tx.Exec("INSERT INTO registries(id,merchant_id,source_id,external_ref,status,total_cents,payment_request_id) VALUES($1,$2,$3,$4,'preview',$5,$6)", regID, merchant, sourceID, external, total, nilID(requestID))
	if e != nil {
		fail(w, 409, e)
		return
	}
	for _, row := range rows {
		_, e = tx.Exec("INSERT INTO registry_rows(id,registry_id,row_no,sheet_name,raw,card_id,amount_cents,error_code,external_order,rrn,bank_fee_cents,bank_transfer_cents) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)", id(), regID, row.Number, row.Sheet, encode(safeRaw(row.Raw)), nilID(row.Card), row.Amount, nilID(row.Error), row.Order, row.RRN, row.BankFee, row.Transfer)
		if e != nil {
			fail(w, 409, e)
			return
		}
	}
	if e = txAudit(tx, u.ID, "web", "registry_upload", "registry", regID, "success", "", M{"sha256": sum, "rows": len(rows)}); e != nil {
		fail(w, 500, e)
		return
	}
	if e = tx.Commit(); e != nil {
		fail(w, 409, e)
		return
	}
	keepFile = true
	respond(w, 201, M{"id": regID, "rows": len(rows), "accepted_total": rub(total)})
}
func parseRows(kind string, data []byte) ([]importedRow, error) {
	if kind == "bank_csv" {
		reader := csv.NewReader(charmap.Windows1251.NewDecoder().Reader(strings.NewReader(string(data))))
		reader.Comma = ';'
		reader.FieldsPerRecord = -1
		all, e := reader.ReadAll()
		if e != nil {
			return nil, e
		}
		if len(all) < 3 {
			return nil, errors.New("CSV без строк")
		}
		header := all[1]
		if len(all) > 10002 {
			return nil, errors.New("слишком много строк")
		}
		indices := map[string]int{}
		for i, h := range header {
			indices[strings.TrimSpace(h)] = i
		}
		for _, name := range []string{"Маскированный номер карты", "Сумма операции", "Комиссия Банка", "К перечислению"} {
			if _, ok := indices[name]; !ok {
				return nil, fmt.Errorf("нет поля %s", name)
			}
		}
		get := func(row []string, name string) string {
			i := indices[name]
			if i >= len(row) {
				return ""
			}
			return row[i]
		}
		out := []importedRow{}
		for i, row := range all[2:] {
			if len(row) == 0 || strings.TrimSpace(strings.Join(row, "")) == "" {
				continue
			}
			x := importedRow{Number: i + 3, Raw: row, Mask: get(row, "Маскированный номер карты"), Order: get(row, "Номер заказа"), RRN: get(row, "RRN")}
			var e1, e2, e3 error
			x.Amount, e1 = amount(get(row, "Сумма операции"))
			x.BankFee, e2 = nonnegative(get(row, "Комиссия Банка"))
			x.Transfer, e3 = amount(get(row, "К перечислению"))
			if e1 != nil || e2 != nil || e3 != nil || x.Transfer != x.Amount+x.BankFee {
				x.Error = "invalid_amount_or_bank_total"
			}
			out = append(out, x)
		}
		return out, nil
	}
	z, e := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if e != nil {
		return nil, e
	}
	var size uint64
	for _, entry := range z.File {
		size += entry.UncompressedSize64
		if size > 64<<20 || entry.UncompressedSize64 > 20<<20 {
			return nil, errors.New("слишком большой XLSX после распаковки")
		}
		if strings.Contains(strings.ToLower(entry.Name), "vba") {
			return nil, errors.New("макросы запрещены")
		}
	}
	f, e := excelize.OpenReader(bytes.NewReader(data))
	if e != nil {
		return nil, e
	}
	defer f.Close()
	out := []importedRow{}
	sheets := f.GetSheetList()
	if len(sheets) != 1 {
		return nil, errors.New("поддержан один лист XLSX")
	}
	sheet := sheets[0]
	raw, e := f.GetRows(sheet, excelize.Options{RawCellValue: true})
	if e != nil {
		return nil, e
	}
	if len(raw) < 2 {
		return nil, errors.New("XLSX без строк")
	}
	if len(raw) > 10001 {
		return nil, errors.New("слишком много строк")
	}
	head := map[string]int{}
	for i, h := range raw[0] {
		lower := strings.ToLower(strings.TrimSpace(h))
		if strings.Contains(lower, "pin") || strings.Contains(lower, "пин") || strings.Contains(lower, "cvv") {
			return nil, errors.New("колонки PIN/CVV запрещены")
		}
		head[strings.ToLower(strings.TrimSpace(h))] = i
	}
	for rowNo, row := range raw {
		for colNo := range row {
			cell, _ := excelize.CoordinatesToCellName(colNo+1, rowNo+1)
			formula, err := f.GetCellFormula(sheet, cell)
			if err != nil {
				return nil, err
			}
			if formula != "" {
				return nil, errors.New("формулы в реестре запрещены")
			}
		}
	}
	find := func(keys ...string) int {
		for _, k := range keys {
			if i, ok := head[k]; ok {
				return i
			}
		}
		return -1
	}
	ci, ai := find("карта", "card", "маска карты"), find("сумма", "amount", "сумма пополнения")
	if ci < 0 || ai < 0 {
		return nil, errors.New("нужны колонки Карта и Сумма")
	}
	for i, row := range raw[1:] {
		if len(row) == 0 || strings.TrimSpace(strings.Join(row, "")) == "" {
			continue
		}
		x := importedRow{Number: i + 2, Sheet: sheet, Raw: row}
		if ci >= len(row) || ai >= len(row) {
			x.Error = "missing_cell"
		} else {
			x.Mask = row[ci]
			x.Amount, e = amount(row[ai])
			if e != nil {
				x.Error = "invalid_amount"
			}
		}
		out = append(out, x)
	}
	return out, nil
}
func nonnegative(s string) (int64, error) {
	if strings.TrimSpace(s) == "0" || strings.TrimSpace(s) == "0,00" || strings.TrimSpace(s) == "0.00" {
		return 0, nil
	}
	return amount(s)
}
func (a *App) resolveCard(tx *sql.Tx, mask string) (string, error) {
	rows, e := tx.Query("SELECT id FROM cards WHERE mask=$1 AND status='active'", strings.TrimSpace(mask))
	if e != nil {
		return "", e
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var x string
		_ = rows.Scan(&x)
		ids = append(ids, x)
	}
	if len(ids) != 1 {
		return "", errors.New("card_not_unique_or_unknown")
	}
	return ids[0], nil
}
func (a *App) currentRate(tx *sql.Tx, merchant string) (int, error) {
	var bp int
	e := tx.QueryRow("SELECT rate_bp FROM tariffs WHERE merchant_id=$1 AND active AND valid_from<= (now() AT TIME ZONE 'Europe/Moscow')::date ORDER BY created_at DESC LIMIT 1", merchant).Scan(&bp)
	return bp, e
}
func (a *App) registries(w http.ResponseWriter, r *http.Request, u User) {
	query := "SELECT r.id,m.name,r.merchant_id,r.external_ref,r.status,r.total_cents,COALESCE(r.commission_cents,0),COALESCE(r.rate_bp,0),COALESCE(r.manual_rate_bp,0),r.version,r.confirmed_at,COALESCE((SELECT SUM(new_commission_cents-old_commission_cents) FROM tariff_adjustments WHERE registry_id=r.id),0),(SELECT rate_bp FROM tariff_adjustments WHERE registry_id=r.id ORDER BY applied_at DESC,id DESC LIMIT 1),COALESCE(r.payment_request_id::text,'') FROM registries r JOIN merchants m ON m.id=r.merchant_id"
	var rows *sql.Rows
	var e error
	if u.Role == "operator" {
		rows, e = a.db.Query(query+" JOIN source_documents s ON s.id=r.source_id WHERE s.uploader_id=$1 ORDER BY r.created_at DESC LIMIT 100", u.ID)
	} else {
		rows, e = a.db.Query(query + " ORDER BY r.created_at DESC LIMIT 100")
	}
	if e != nil {
		fail(w, 500, e)
		return
	}
	defer rows.Close()
	out := []M{}
	for rows.Next() {
		var id, name, merchant, ref, status, requestID string
		var total, comm, adjustment int64
		var rate, manual, version int
		var adjustedRate sql.NullInt64
		var confirmed sql.NullTime
		if e = rows.Scan(&id, &name, &merchant, &ref, &status, &total, &comm, &rate, &manual, &version, &confirmed, &adjustment, &adjustedRate, &requestID); e != nil {
			fail(w, 500, e)
			return
		}
		if status == "posted" {
			comm += adjustment
			if adjustedRate.Valid {
				rate = int(adjustedRate.Int64)
			}
		} else if status == "reversed" {
			comm = 0
		}
		x := M{"id": id, "merchant": name, "external_ref": ref, "status": status, "total": rub(total), "commission": rub(comm), "net": rub(total - comm), "rate_bp": rate, "manual_rate_bp": manual, "version": version, "payment_request_id": requestID}
		if status == "preview" {
			tx, _ := a.db.Begin()
			rate, _ = a.currentRate(tx, merchant)
			tx.Rollback()
			if manual > 0 {
				rate = manual
			}
			x["rate_bp"] = rate
			x["commission"] = rub(fee(total, rate))
			x["net"] = rub(total - fee(total, rate))
		}
		if status == "reversed" {
			x["net"] = "0.00"
		}
		if confirmed.Valid {
			x["confirmed_at"] = confirmed.Time
		}
		out = append(out, x)
	}
	respond(w, 200, out)
}
func (a *App) registryRows(w http.ResponseWriter, r *http.Request, u User) {
	if r.Method != "GET" {
		fail(w, 405, errors.New("method"))
		return
	}
	if u.Role == "operator" {
		var owned bool
		e := a.db.QueryRow("SELECT EXISTS(SELECT 1 FROM registries r JOIN source_documents s ON s.id=r.source_id WHERE r.id=$1 AND s.uploader_id=$2)", r.URL.Query().Get("id"), u.ID).Scan(&owned)
		if e != nil || !owned {
			fail(w, 403, errors.New("реестр не доступен"))
			return
		}
	}
	rows, e := a.db.Query("SELECT row_no,sheet_name,raw,COALESCE(card_id::text,''),COALESCE(amount_cents,0),COALESCE(error_code,''),COALESCE(external_order,''),COALESCE(rrn,''),COALESCE(bank_fee_cents,0),COALESCE(bank_transfer_cents,0) FROM registry_rows WHERE registry_id=$1 ORDER BY row_no", r.URL.Query().Get("id"))
	if e != nil {
		fail(w, 400, e)
		return
	}
	defer rows.Close()
	out := []M{}
	for rows.Next() {
		var n int
		var sheet, card, errorCode, order, rrn string
		var raw []byte
		var amount, fee, transfer int64
		if e = rows.Scan(&n, &sheet, &raw, &card, &amount, &errorCode, &order, &rrn, &fee, &transfer); e != nil {
			fail(w, 500, e)
			return
		}
		var values []string
		_ = json.Unmarshal(raw, &values)
		out = append(out, M{"row": n, "sheet": sheet, "raw": values, "card_id": card, "amount": rub(amount), "error": errorCode, "order": order, "rrn": rrn, "bank_fee": rub(fee), "bank_transfer": rub(transfer)})
	}
	respond(w, 200, out)
}
func (a *App) manualRate(w http.ResponseWriter, r *http.Request, u User) {
	if r.Method != "POST" || !a.require(w, u, "chief") {
		return
	}
	m, e := jsonBody(r)
	if e != nil {
		fail(w, 400, e)
		return
	}
	bp, e := strconv.Atoi(str(m, "rate_bp"))
	if e != nil || bp < 0 || bp > 10000 || str(m, "reason") == "" {
		fail(w, 400, errors.New("ставка или основание"))
		return
	}
	res, e := a.db.Exec("UPDATE registries SET manual_rate_bp=$1,manual_reason=$2,manual_approved_by=$3,manual_approved_at=now(),version=version+1 WHERE id=$4 AND status='preview' AND version=$5", bp, str(m, "reason"), u.ID, str(m, "id"), str(m, "version"))
	if e != nil {
		fail(w, 409, e)
		return
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		fail(w, 409, errors.New("устаревший preview"))
		return
	}
	a.logAudit(u.ID, "web", "manual_rate_approve", "registry", str(m, "id"), "success", "", M{"rate_bp": bp})
	respond(w, 200, M{"ok": true})
}
func (a *App) confirmRegistry(w http.ResponseWriter, r *http.Request, u User) {
	if r.Method != "POST" || !a.require(w, u, "chief") {
		return
	}
	m, e := jsonBody(r)
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
	var merchant, status string
	var total int64
	var manual sql.NullInt64
	var version int
	e = tx.QueryRow("SELECT merchant_id,status,total_cents,manual_rate_bp,version FROM registries WHERE id=$1 FOR UPDATE", str(m, "id")).Scan(&merchant, &status, &total, &manual, &version)
	if e != nil {
		fail(w, 404, e)
		return
	}
	if status == "posted" {
		respond(w, 200, M{"status": "already_posted"})
		return
	}
	if status != "preview" || fmt.Sprint(version) != str(m, "version") {
		fail(w, 409, errors.New("устаревший preview"))
		return
	}
	var bad int
	_ = tx.QueryRow("SELECT count(*) FROM registry_rows WHERE registry_id=$1 AND (error_code IS NOT NULL OR card_id IS NULL)", str(m, "id")).Scan(&bad)
	if bad > 0 {
		fail(w, 409, fmt.Errorf("%d строк требуют исправления", bad))
		return
	}
	bp, e := a.currentRate(tx, merchant)
	if e != nil {
		fail(w, 409, e)
		return
	}
	if manual.Valid {
		bp = int(manual.Int64)
	}
	commission := fee(total, bp)
	if str(m, "confirm_total") != rub(total) || str(m, "confirm_commission") != rub(commission) || str(m, "confirm_rate_bp") != strconv.Itoa(bp) {
		fail(w, 409, errors.New("подтвердите актуальные суммы"))
		return
	}
	lines := []Posting{}
	rows, e := tx.Query("SELECT card_id,amount_cents FROM registry_rows WHERE registry_id=$1 ORDER BY row_no", str(m, "id"))
	if e != nil {
		fail(w, 500, e)
		return
	}
	for rows.Next() {
		var card string
		var v int64
		if e = rows.Scan(&card, &v); e != nil {
			break
		}
		lines = append(lines, Posting{Account: "1100", Side: "debit", Amount: v, Card: card})
	}
	rows.Close()
	if e != nil {
		fail(w, 500, e)
		return
	}
	net := total - commission
	if net > 0 {
		lines = append(lines, Posting{Account: "2100", Side: "credit", Amount: net, Merchant: merchant})
	}
	if commission > 0 {
		lines = append(lines, Posting{Account: "4100", Side: "credit", Amount: commission, Merchant: merchant})
	}
	now := time.Now()
	_, e = put(tx, "registry", str(m, "id"), "registry:"+str(m, "id"), u.ID, now, now, lines, "")
	if e != nil {
		fail(w, 409, e)
		return
	}
	_, e = tx.Exec("UPDATE registries SET status='posted',commission_cents=$1,rate_bp=$2,confirmed_at=$3 WHERE id=$4", commission, bp, now, str(m, "id"))
	if e != nil {
		fail(w, 500, e)
		return
	}
	if e = normalizeMerchant(tx, merchant, u.ID, "registry:"+str(m, "id")); e != nil {
		fail(w, 409, e)
		return
	}
	if e = txAudit(tx, u.ID, "web", "registry_confirm", "registry", str(m, "id"), "success", "", M{"gross_cents": total, "commission_cents": commission}); e != nil {
		fail(w, 500, e)
		return
	}
	if e = tx.Commit(); e != nil {
		fail(w, 409, e)
		return
	}
	respond(w, 200, M{"status": "posted"})
}
func (a *App) reverseRegistry(w http.ResponseWriter, r *http.Request, u User) {
	if r.Method != "POST" || !a.require(w, u, "chief") {
		return
	}
	m, e := jsonBody(r)
	if e != nil {
		fail(w, 400, e)
		return
	}
	if str(m, "reason") == "" {
		fail(w, 400, errors.New("нужна причина"))
		return
	}
	tx, e := a.tx()
	if e != nil {
		fail(w, 500, e)
		return
	}
	defer tx.Rollback()
	var status string
	var confirmed time.Time
	e = tx.QueryRow("SELECT status,confirmed_at FROM registries WHERE id=$1 FOR UPDATE", str(m, "id")).Scan(&status, &confirmed)
	if e != nil {
		fail(w, 404, e)
		return
	}
	if status == "reversed" {
		respond(w, 200, M{"status": "already_reversed"})
		return
	}
	if status != "posted" || time.Since(confirmed) > 72*time.Hour {
		fail(w, 409, errors.New("срок отката истёк"))
		return
	}
	rows, e := tx.Query("SELECT id FROM journal_entries WHERE (event_type='registry' AND event_id=$1) OR (event_type='tariff_adjustment' AND event_id IN (SELECT id FROM tariff_adjustments WHERE registry_id=$1)) ORDER BY posted_at DESC", str(m, "id"))
	if e != nil {
		fail(w, 500, e)
		return
	}
	var ids []string
	for rows.Next() {
		var x string
		_ = rows.Scan(&x)
		ids = append(ids, x)
	}
	rows.Close()
	for _, x := range ids {
		if e = reverse(tx, x, u.ID, "reverse:"+str(m, "id")+":"+x); e != nil {
			fail(w, 409, e)
			return
		}
	}
	_, e = tx.Exec("UPDATE registries SET status='reversed' WHERE id=$1", str(m, "id"))
	if e != nil {
		fail(w, 500, e)
		return
	}
	var merchant string
	_ = tx.QueryRow("SELECT merchant_id FROM registries WHERE id=$1", str(m, "id")).Scan(&merchant)
	if e = normalizeMerchant(tx, merchant, u.ID, "reverse-registry:"+str(m, "id")); e != nil {
		fail(w, 409, e)
		return
	}
	if e = txAudit(tx, u.ID, "web", "registry_reverse", "registry", str(m, "id"), "success", str(m, "reason"), M{}); e != nil {
		fail(w, 500, e)
		return
	}
	if e = tx.Commit(); e != nil {
		fail(w, 409, e)
		return
	}
	respond(w, 200, M{"status": "reversed"})
}

var _ = json.Marshal
