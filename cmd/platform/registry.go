package main

import (
	"crypto/sha256"
	"database/sql"
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
)

type importedRow struct {
	Number                    int
	Sheet                     string
	Raw                       []string
	Mask, ContactName         string
	Order, RRN, Error, Card   string
	Amount, BankFee, Transfer int64
}

type requestRegistryLookup struct {
	numbers, masks, names map[string]string
}

var longDigits = regexp.MustCompile(`(?:[0-9][ -]?){12,19}`)
var trailingFour = regexp.MustCompile(`([0-9]{4})\s*$`)
var personNameSeparator = regexp.MustCompile(`[^\p{L}\p{N}]+`)

func normalizePersonName(value string) string {
	value = strings.ReplaceAll(strings.ToLower(strings.TrimSpace(value)), "ё", "е")
	return strings.Join(strings.Fields(personNameSeparator.ReplaceAllString(value, " ")), " ")
}

func safeRaw(values []string) []string {
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = longDigits.ReplaceAllString(v, "[redacted]")
	}
	return out
}

func (a *App) registryPaymentDay(value string) (string, time.Time, error) {
	day := strings.TrimSpace(value)
	at, e := time.ParseInLocation("2006-01-02", day, a.location)
	if e != nil || at.Format("2006-01-02") != day || day > time.Now().In(a.location).Format("2006-01-02") {
		return "", time.Time{}, errors.New("укажите дату фактических оплат не позже сегодняшнего дня")
	}
	return day, at, nil
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
	ext := strings.ToLower(filepath.Ext(h.Filename))
	if ext != ".csv" && ext != ".xlsx" && ext != ".xls" {
		fail(w, 400, errors.New("поддержаны только .xlsx, .xls и .csv"))
		return
	}
	merchant := r.FormValue("merchant_id")
	paymentDate, _, dateErr := a.registryPaymentDay(r.FormValue("payment_date"))
	if dateErr != nil {
		fail(w, 400, dateErr)
		return
	}
	var parserCode, profileStatus string
	var parserVersion int
	e = a.db.QueryRow("SELECT COALESCE(p.parser_code,''),p.status,COALESCE(t.version,0) FROM merchant_import_profiles p JOIN merchants m ON m.id=p.merchant_id AND m.active LEFT JOIN registry_parser_types t ON t.code=p.parser_code AND t.active WHERE p.merchant_id=$1", merchant).Scan(&parserCode, &profileStatus, &parserVersion)
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		fail(w, 500, errors.New("не удалось определить тип разбора файла"))
		return
	}
	if e != nil || profileStatus != "configured" || parserCode == "" || parserVersion <= 0 {
		fail(w, 400, errors.New("для мерчанта ещё не настроен тип разбора файла"))
		return
	}
	var extensionAllowed bool
	e = a.db.QueryRow("SELECT $1=ANY(accepted_extensions) FROM registry_parser_types WHERE code=$2 AND active", ext, parserCode).Scan(&extensionAllowed)
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		fail(w, 500, errors.New("не удалось проверить тип файла"))
		return
	}
	if e != nil || !extensionAllowed {
		fail(w, 400, errors.New("расширение файла не соответствует типу разбора этого мерчанта"))
		return
	}
	rows, e := readAndParseRows(parserCode, ext, data)
	if e != nil {
		fail(w, 400, e)
		return
	}
	kind := strings.TrimPrefix(ext, ".")
	if ext == ".csv" {
		kind = "bank_csv"
	}
	external := strings.TrimSpace(r.FormValue("external_ref"))
	if external == "" {
		external = "РЕ-" + id()
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
	for _, row := range rows {
		if row.ContactName != "" && requestID == "" {
			fail(w, 400, errors.New("для реестра по ФИО выберите сформированный запрос карт"))
			return
		}
	}
	var requestLookup requestRegistryLookup
	if requestID != "" {
		requestLookup, e = verifyRequest(tx, requestID, merchant)
		if e != nil {
			fail(w, 400, e)
			return
		}
	}
	// Serialize uploads of identical bytes, including the replacement after a reversal.
	if _, e = tx.Exec("SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", sum); e != nil {
		fail(w, 500, e)
		return
	}
	var exists bool
	if e = tx.QueryRow("SELECT EXISTS(SELECT 1 FROM registries old JOIN source_documents s ON s.id=old.source_id WHERE s.sha256=$1 AND old.status IN ('preview','posted'))", sum).Scan(&exists); e != nil {
		fail(w, 500, e)
		return
	}
	if exists {
		fail(w, 409, errors.New("файл уже загружен"))
		return
	}
	var replaces sql.NullString
	e = tx.QueryRow("SELECT old.id FROM registries old JOIN source_documents s ON s.id=old.source_id WHERE s.sha256=$1 AND old.merchant_id=$2 AND old.status='reversed' ORDER BY old.created_at DESC LIMIT 1", sum, merchant).Scan(&replaces)
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		fail(w, 500, e)
		return
	}
	var total int64
	for i := range rows {
		if rows[i].Amount > 0 {
			if rows[i].Amount > math.MaxInt64-total {
				fail(w, 400, errors.New("сумма реестра слишком велика"))
				return
			}
			total += rows[i].Amount
		}
		if rows[i].Error == "" {
			var card string
			var err error
			if requestID != "" {
				if rows[i].ContactName != "" {
					card = requestLookup.names[normalizePersonName(rows[i].ContactName)]
					if card == "" {
						rows[i].Error = "request_contact_not_found_or_ambiguous"
						continue
					}
				} else {
					value := strings.TrimSpace(rows[i].Mask)
					card = requestLookup.numbers[value]
					if card == "" {
						card = requestLookup.masks[value]
					}
				}
				if card == "" {
					err = errors.New("request_card_not_found_or_ambiguous")
				}
			} else {
				card, err = a.resolveCard(tx, rows[i].Mask)
			}
			if err != nil {
				rows[i].Error = err.Error()
			} else {
				rows[i].Card = card
			}
		}
	}
	if total <= 0 {
		fail(w, 400, errors.New("нет валидных строк"))
		return
	}
	sourceID, regID := id(), id()
	path := filepath.Join(a.storage, sum+"-"+sourceID)
	encrypted, encryptErr := sealSensitive(data, []byte(sum))
	if encryptErr != nil {
		fail(w, 500, errors.New("защищённое хранение документов недоступно"))
		return
	}
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
	if written, writeErr := stored.Write(encrypted); writeErr != nil || written != len(encrypted) {
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
	_, e = tx.Exec("INSERT INTO source_documents(id,kind,filename,sha256,media_type,byte_size,storage_path,uploader_id,parser_code,parser_version) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)", sourceID, kind, safeFilename(h.Filename), sum, h.Header.Get("Content-Type"), len(data), path, u.ID, parserCode, parserVersion)
	if e != nil {
		fail(w, 409, e)
		return
	}
	_, e = tx.Exec("INSERT INTO registries(id,merchant_id,source_id,external_ref,status,total_cents,payment_request_id,payment_date,replaces_registry_id) VALUES($1,$2,$3,$4,'preview',$5,$6,$7,$8)", regID, merchant, sourceID, external, total, nilID(requestID), paymentDate, replaces)
	if e != nil {
		fail(w, 409, e)
		return
	}
	for _, row := range rows {
		_, e = tx.Exec("INSERT INTO registry_rows(id,registry_id,row_no,sheet_name,raw,card_id,amount_cents,error_code,external_order,rrn,bank_fee_cents,bank_transfer_cents,source_contact_name) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)", id(), regID, row.Number, row.Sheet, encode(safeRaw(row.Raw)), nilID(row.Card), row.Amount, nilID(row.Error), row.Order, row.RRN, row.BankFee, row.Transfer, nilID(row.ContactName))
		if e != nil {
			fail(w, 409, e)
			return
		}
	}
	if e = txAudit(tx, u.ID, "web", "registry_upload", "registry", regID, "success", "", M{"sha256": sum, "rows": len(rows), "parser_code": parserCode, "parser_version": parserVersion, "replaces_registry_id": replaces.String}); e != nil {
		fail(w, 500, e)
		return
	}
	if e = tx.Commit(); e != nil {
		fail(w, 409, e)
		return
	}
	keepFile = true
	invalidRows := 0
	for _, row := range rows {
		if row.Error != "" {
			invalidRows++
		}
	}
	respond(w, 201, M{"id": regID, "rows": len(rows), "invalid_rows": invalidRows, "accepted_total": rub(total)})
}
func nonnegative(s string) (int64, error) {
	if strings.TrimSpace(s) == "0" || strings.TrimSpace(s) == "0,00" || strings.TrimSpace(s) == "0.00" {
		return 0, nil
	}
	return amount(s)
}
func (a *App) resolveCard(tx *sql.Tx, mask string) (string, error) {
	value := strings.TrimSpace(mask)
	rows, e := tx.Query("SELECT id FROM cards WHERE mask=$1 AND status='active'", value)
	if e != nil {
		return "", e
	}
	var ids []string
	for rows.Next() {
		var x string
		if e = rows.Scan(&x); e != nil {
			rows.Close()
			return "", e
		}
		ids = append(ids, x)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return "", e
	}
	if len(ids) == 1 {
		return ids[0], nil
	}
	if len(ids) > 1 {
		return "", errors.New("card_not_unique_or_unknown")
	}
	match := trailingFour.FindStringSubmatch(value)
	if len(match) != 2 {
		return "", errors.New("card_not_unique_or_unknown")
	}
	rows, e = tx.Query("SELECT id FROM cards WHERE last4=$1 AND status='active'", match[1])
	if e != nil {
		return "", e
	}
	ids = ids[:0]
	for rows.Next() {
		var card string
		if e = rows.Scan(&card); e != nil {
			return "", e
		}
		ids = append(ids, card)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return "", e
	}
	if len(ids) != 1 {
		return "", errors.New("card_not_unique_or_unknown")
	}
	return ids[0], nil
}
func (a *App) currentRate(tx *sql.Tx, merchant, paymentDate string) (int, error) {
	var bp int
	e := tx.QueryRow("SELECT rate_bp FROM tariffs WHERE merchant_id=$1 AND active AND valid_from<=$2::date ORDER BY valid_from DESC,created_at DESC LIMIT 1", merchant, paymentDate).Scan(&bp)
	return bp, e
}
func (a *App) registries(w http.ResponseWriter, r *http.Request, u User) {
	if !a.require(w, u, "chief", "operator", "accountant", "auditor") {
		return
	}
	query := "SELECT r.id,m.name,r.merchant_id,r.external_ref,r.status,r.total_cents,COALESCE(r.commission_cents,0),COALESCE(r.rate_bp,0),r.manual_rate_bp,r.version,r.confirmed_at,COALESCE((SELECT SUM(new_commission_cents-old_commission_cents) FROM tariff_adjustments WHERE registry_id=r.id),0),(SELECT rate_bp FROM tariff_adjustments WHERE registry_id=r.id ORDER BY applied_at DESC,id DESC LIMIT 1),COALESCE(r.payment_request_id::text,''),r.number,COALESCE(r.payment_date::text,''),COALESCE(r.replaces_registry_id::text,'') FROM registries r JOIN merchants m ON m.id=r.merchant_id"
	var rows *sql.Rows
	var e error
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if u.Role == "operator" {
		if id != "" {
			rows, e = a.db.Query(query+" JOIN source_documents s ON s.id=r.source_id WHERE s.uploader_id=$1 AND r.id::text=$2 AND r.status<>'deleted'", u.ID, id)
		} else {
			rows, e = a.db.Query(query+" JOIN source_documents s ON s.id=r.source_id WHERE s.uploader_id=$1 AND r.status<>'deleted' ORDER BY r.created_at DESC LIMIT 100", u.ID)
		}
	} else {
		if id != "" {
			rows, e = a.db.Query(query+" WHERE r.id::text=$1 AND r.status<>'deleted'", id)
		} else {
			rows, e = a.db.Query(query + " WHERE r.status<>'deleted' ORDER BY r.created_at DESC LIMIT 100")
		}
	}
	if e != nil {
		fail(w, 500, e)
		return
	}
	defer rows.Close()
	out := []M{}
	for rows.Next() {
		var id, name, merchant, ref, status, requestID, paymentDate, replacesRegistryID string
		var total, comm, adjustment, number int64
		var rate, version int
		var manual, adjustedRate sql.NullInt64
		var confirmed sql.NullTime
		if e = rows.Scan(&id, &name, &merchant, &ref, &status, &total, &comm, &rate, &manual, &version, &confirmed, &adjustment, &adjustedRate, &requestID, &number, &paymentDate, &replacesRegistryID); e != nil {
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
		x := M{"id": id, "merchant": name, "merchant_id": merchant, "external_ref": ref, "number": number, "payment_date": paymentDate, "replaces_registry_id": replacesRegistryID, "status": status, "total": rub(total), "commission": rub(comm), "net": rub(total - comm), "rate_bp": rate, "manual_rate_bp": manual.Int64, "version": version, "payment_request_id": requestID}
		if status == "preview" {
			rateAvailable := manual.Valid
			if manual.Valid {
				rate = int(manual.Int64)
			} else {
				dateForRate := paymentDate
				if dateForRate == "" {
					dateForRate = time.Now().In(a.location).Format("2006-01-02")
				}
				rateErr := a.db.QueryRow("SELECT rate_bp FROM tariffs WHERE merchant_id=$1 AND active AND valid_from<=$2::date ORDER BY valid_from DESC,created_at DESC LIMIT 1", merchant, dateForRate).Scan(&rate)
				if rateErr != nil && !errors.Is(rateErr, sql.ErrNoRows) {
					fail(w, 500, rateErr)
					return
				}
				rateAvailable = rateErr == nil
			}
			x["rate_available"] = rateAvailable
			if rateAvailable {
				x["rate_bp"] = rate
				x["commission"] = rub(fee(total, rate))
				x["net"] = rub(total - fee(total, rate))
			}
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
	if !a.require(w, u, "chief", "operator", "accountant", "auditor") {
		return
	}
	if r.Method != "GET" {
		fail(w, 405, errors.New("method"))
		return
	}
	if u.Role == "operator" {
		var owned bool
		e := a.db.QueryRow("SELECT EXISTS(SELECT 1 FROM registries r JOIN source_documents s ON s.id=r.source_id WHERE r.id=$1 AND s.uploader_id=$2 AND r.status<>'deleted')", r.URL.Query().Get("id"), u.ID).Scan(&owned)
		if e != nil || !owned {
			fail(w, 403, errors.New("реестр не доступен"))
			return
		}
	}
	rows, e := a.db.Query(`SELECT rr.row_no,rr.sheet_name,rr.raw,COALESCE(rr.card_id::text,''),COALESCE(rr.amount_cents,0),
		COALESCE(rr.error_code,''),COALESCE(rr.external_order,''),COALESCE(rr.rrn,''),COALESCE(rr.bank_fee_cents,0),COALESCE(rr.bank_transfer_cents,0),
		COALESCE(rr.source_contact_name,''),COALESCE(c.mask,''),COALESCE(pr.contact_name,''),COALESCE(pr.contact_phone,'')
		FROM registry_rows rr
		JOIN registries r ON r.id=rr.registry_id
		LEFT JOIN cards c ON c.id=rr.card_id
		LEFT JOIN payment_request_rows pr ON pr.request_id=r.payment_request_id AND pr.card_id=rr.card_id
		WHERE rr.registry_id=$1 AND r.status<>'deleted' ORDER BY rr.row_no`, r.URL.Query().Get("id"))
	if e != nil {
		fail(w, 400, e)
		return
	}
	defer rows.Close()
	out := []M{}
	for rows.Next() {
		var n int
		var sheet, card, errorCode, order, rrn, sourceName, cardMask, requestName, requestPhone string
		var raw []byte
		var amount, fee, transfer int64
		if e = rows.Scan(&n, &sheet, &raw, &card, &amount, &errorCode, &order, &rrn, &fee, &transfer, &sourceName, &cardMask, &requestName, &requestPhone); e != nil {
			fail(w, 500, e)
			return
		}
		var values []string
		_ = json.Unmarshal(raw, &values)
		if u.Role != "chief" && u.Role != "operator" {
			requestName = ""
			requestPhone = ""
		}
		out = append(out, M{"row": n, "sheet": sheet, "raw": values, "card_id": card, "card_mask": cardMask, "source_contact_name": sourceName, "request_contact_name": requestName, "request_contact_phone": requestPhone, "amount": rub(amount), "error": errorCode, "order": order, "rrn": rrn, "bank_fee": rub(fee), "bank_transfer": rub(transfer)})
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
	var merchant, status, paymentDate string
	var total int64
	var manual sql.NullInt64
	var version int
	e = tx.QueryRow("SELECT merchant_id,status,total_cents,manual_rate_bp,version,COALESCE(payment_date::text,'') FROM registries WHERE id=$1 FOR UPDATE", str(m, "id")).Scan(&merchant, &status, &total, &manual, &version, &paymentDate)
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
	if paymentDate == "" {
		fail(w, 409, errors.New("у старого черновика не указана дата оплат; создайте реестр заново с датой фактических оплат"))
		return
	}
	_, paymentAt, dateErr := a.registryPaymentDay(paymentDate)
	if dateErr != nil {
		fail(w, 409, dateErr)
		return
	}
	var bad int
	_ = tx.QueryRow("SELECT count(*) FROM registry_rows WHERE registry_id=$1 AND (error_code IS NOT NULL OR card_id IS NULL)", str(m, "id")).Scan(&bad)
	if bad > 0 {
		fail(w, 409, fmt.Errorf("%d строк требуют исправления", bad))
		return
	}
	var bp int
	if manual.Valid {
		bp = int(manual.Int64)
	} else {
		bp, e = a.currentRate(tx, merchant, paymentDate)
		if errors.Is(e, sql.ErrNoRows) {
			fail(w, 409, errors.New("для мерчанта не задан действующий тариф; утвердите общий тариф или разовую ставку реестра"))
			return
		}
		if e != nil {
			fail(w, 500, e)
			return
		}
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
			rows.Close()
			fail(w, 500, e)
			return
		}
		lines = append(lines, Posting{Account: "1100", Side: "debit", Amount: v, Card: card})
	}
	e = rows.Err()
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
	_, e = put(tx, "registry", str(m, "id"), "registry:"+str(m, "id"), u.ID, paymentAt, paymentAt, lines, "")
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
func (a *App) deleteRegistry(w http.ResponseWriter, r *http.Request, u User) {
	if r.Method != http.MethodPost || !a.require(w, u, "chief", "operator") {
		return
	}
	m, e := jsonBody(r)
	if e != nil {
		fail(w, 400, e)
		return
	}
	registryID := str(m, "id")
	version, versionErr := strconv.Atoi(str(m, "version"))
	if !validID(registryID) || versionErr != nil || version < 1 {
		fail(w, 400, errors.New("обновите реестр перед удалением"))
		return
	}
	tx, e := a.tx()
	if e != nil {
		fail(w, 500, e)
		return
	}
	defer tx.Rollback()
	var status, uploader string
	var actualVersion int
	e = tx.QueryRow(`SELECT r.status,r.version,s.uploader_id FROM registries r
		JOIN source_documents s ON s.id=r.source_id WHERE r.id=$1 FOR UPDATE OF r`, registryID).Scan(&status, &actualVersion, &uploader)
	if e != nil {
		fail(w, 404, errors.New("реестр не найден"))
		return
	}
	if u.Role == "operator" && uploader != u.ID {
		fail(w, 403, errors.New("реестр не доступен"))
		return
	}
	if status != "preview" || actualVersion != version {
		fail(w, 409, errors.New("реестр уже изменён или подтверждён; обновите страницу"))
		return
	}
	if _, e = tx.Exec("UPDATE registries SET status='deleted',deleted_at=now(),deleted_by=$2,version=version+1 WHERE id=$1", registryID, u.ID); e != nil {
		fail(w, 500, e)
		return
	}
	if e = txAudit(tx, u.ID, "web", "registry_delete", "registry", registryID, "success", "", M{"version": actualVersion + 1}); e != nil {
		fail(w, 500, e)
		return
	}
	if e = tx.Commit(); e != nil {
		fail(w, 500, e)
		return
	}
	respond(w, 200, M{"id": registryID, "status": "deleted"})
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
