package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
)

type manualRegistryRow struct {
	RequestRowNo int    `json:"payment_request_row_no,omitempty"`
	CardID       string `json:"card_id"`
	ContactID    string `json:"contact_id"`
	Amount       string `json:"amount"`
}

type manualRegistryInput struct {
	PaymentRequestID      string              `json:"payment_request_id,omitempty"`
	PaymentRequestVersion int                 `json:"payment_request_version,omitempty"`
	MerchantID            string              `json:"merchant_id"`
	IdempotencyKey        string              `json:"idempotency_key"`
	PaymentDate           string              `json:"payment_date"`
	Rows                  []manualRegistryRow `json:"rows"`
}

type preparedManualRow struct {
	cardID, mask, name, phone string
	amount                    int64
}

func (a *App) manualRegistryCards(w http.ResponseWriter, r *http.Request, u User) {
	if r.Method != http.MethodGet || !a.require(w, u, "chief", "operator") {
		return
	}
	rows, e := a.db.Query("SELECT c.id,c.mask,b.name FROM cards c JOIN banks b ON b.id=c.bank_id WHERE c.status='active' ORDER BY b.name,c.mask,c.id")
	if e != nil {
		fail(w, 500, e)
		return
	}
	defer rows.Close()
	out := []M{}
	for rows.Next() {
		var cardID, mask, bank string
		if e = rows.Scan(&cardID, &mask, &bank); e != nil {
			fail(w, 500, e)
			return
		}
		out = append(out, M{"id": cardID, "mask": mask, "bank": bank})
	}
	if e = rows.Err(); e != nil {
		fail(w, 500, e)
		return
	}
	respond(w, 200, out)
}

func (a *App) createManualRegistry(w http.ResponseWriter, r *http.Request, u User) {
	if r.Method != http.MethodPost || !a.require(w, u, "chief", "operator") {
		return
	}
	defer r.Body.Close()
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	var input manualRegistryInput
	if e := decoder.Decode(&input); e != nil {
		fail(w, 400, errors.New("неверные данные реестра"))
		return
	}
	if !validID(input.MerchantID) || !validID(input.IdempotencyKey) || len(input.Rows) < 1 || len(input.Rows) > 500 {
		fail(w, 400, errors.New("выберите мерчанта и добавьте от 1 до 500 строк"))
		return
	}
	paymentDate, _, dateErr := a.registryPaymentDay(input.PaymentDate)
	if dateErr != nil {
		fail(w, 400, dateErr)
		return
	}
	for _, row := range input.Rows {
		if !validID(row.CardID) || (input.PaymentRequestID == "" && !validID(row.ContactID)) || (input.PaymentRequestID != "" && row.RequestRowNo < 1) {
			fail(w, 400, errors.New("выберите карту, ФИО и телефон из справочников"))
			return
		}
	}
	ref := "РВ-" + input.IdempotencyKey
	tx, e := a.tx()
	if e != nil {
		fail(w, 500, e)
		return
	}
	defer tx.Rollback()
	var merchantActive bool
	if e = tx.QueryRow("SELECT active FROM merchants WHERE id=$1", input.MerchantID).Scan(&merchantActive); e != nil || !merchantActive {
		fail(w, 400, errors.New("мерчант не найден или отключён"))
		return
	}
	if input.PaymentRequestID != "" {
		if !validID(input.PaymentRequestID) {
			fail(w, 400, errors.New("выберите карточный реестр"))
			return
		}
		var merchant string
		var version int
		if e = tx.QueryRow("SELECT merchant_id,version FROM payment_requests WHERE id=$1 AND deleted_at IS NULL FOR SHARE", input.PaymentRequestID).Scan(&merchant, &version); e != nil || merchant != input.MerchantID {
			fail(w, 400, errors.New("карточный реестр не принадлежит выбранному мерчанту или недоступен"))
			return
		}
		if version != input.PaymentRequestVersion {
			fail(w, 409, errors.New("карточный реестр изменился; выберите его заново и проверьте суммы"))
			return
		}
	}
	prepared := make([]preparedManualRow, 0, len(input.Rows))
	var total int64
	for i, row := range input.Rows {
		v, err := amount(row.Amount)
		if err != nil || v > math.MaxInt64-total {
			fail(w, 400, fmt.Errorf("строка %d: некорректная сумма", i+1))
			return
		}
		var item preparedManualRow
		item.cardID, item.amount = row.CardID, v
		if input.PaymentRequestID != "" {
			err = tx.QueryRow("SELECT c.mask,COALESCE(p.contact_name,p.synthetic_name),COALESCE(p.contact_phone,'') FROM payment_request_rows p JOIN cards c ON c.id=p.card_id WHERE p.request_id=$1 AND p.row_no=$2 AND p.card_id=$3 AND c.status='active' FOR SHARE OF c", input.PaymentRequestID, row.RequestRowNo, row.CardID).Scan(&item.mask, &item.name, &item.phone)
		} else {
			err = tx.QueryRow("SELECT c.mask,p.full_name,p.phone FROM cards c CROSS JOIN payment_contacts p WHERE c.id=$1 AND c.status='active' AND p.id=$2 AND p.active FOR SHARE OF c", row.CardID, row.ContactID).Scan(&item.mask, &item.name, &item.phone)
		}
		if err != nil {
			fail(w, 400, fmt.Errorf("строка %d: карта или контакт недоступны", i+1))
			return
		}
		prepared = append(prepared, item)
		total += v
	}
	// This snapshot is the original manually entered source. It is encrypted on disk;
	// the registry rows keep the values reviewed by the operator at creation time.
	snapshotRows := make([]M, 0, len(prepared))
	for i, row := range prepared {
		snapshotRows = append(snapshotRows, M{"card_id": row.cardID, "card_mask": row.mask, "contact_id": input.Rows[i].ContactID, "full_name": row.name, "phone": row.phone, "amount": rub(row.amount)})
	}
	snapshot := M{"merchant_id": input.MerchantID, "external_ref": ref, "payment_date": paymentDate, "rows": snapshotRows}
	if input.PaymentRequestID != "" {
		snapshot["payment_request_id"] = input.PaymentRequestID
		snapshot["payment_request_version"] = input.PaymentRequestVersion
		for i := range snapshotRows {
			snapshotRows[i]["payment_request_row_no"] = input.Rows[i].RequestRowNo
		}
	}
	data, e := json.Marshal(snapshot)
	if e != nil {
		fail(w, 500, e)
		return
	}
	hash := sha256.Sum256(data)
	sum := hex.EncodeToString(hash[:])
	var existingID, existingUploader, existingHash string
	e = tx.QueryRow("SELECT r.id,s.uploader_id,s.sha256 FROM registries r JOIN source_documents s ON s.id=r.source_id WHERE r.external_ref=$1", ref).Scan(&existingID, &existingUploader, &existingHash)
	if e == nil {
		if existingUploader == u.ID && existingHash == sum {
			respond(w, 200, M{"id": existingID, "status": "already_created"})
		} else {
			fail(w, 409, errors.New("ключ повторного запроса уже использован"))
		}
		return
	}
	if !errors.Is(e, sql.ErrNoRows) {
		fail(w, 500, e)
		return
	}
	encrypted, e := sealSensitive(data, []byte(sum))
	if e != nil {
		fail(w, 500, errors.New("защищённое хранение недоступно"))
		return
	}
	path := filepath.Join(a.storage, sum)
	stored, e := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		fail(w, 500, errors.New("не удалось сохранить исходные данные"))
		return
	}
	keepFile := false
	defer func() {
		if !keepFile {
			_ = os.Remove(path)
		}
	}()
	if _, e = stored.Write(encrypted); e != nil {
		_ = stored.Close()
		fail(w, 500, errors.New("не удалось сохранить исходные данные"))
		return
	}
	if e = stored.Close(); e != nil {
		fail(w, 500, errors.New("не удалось сохранить исходные данные"))
		return
	}
	sourceID, registryID := id(), id()
	_, e = tx.Exec("INSERT INTO source_documents(id,kind,filename,sha256,media_type,byte_size,storage_path,uploader_id) VALUES($1,'manual',$2,$3,'application/json',$4,$5,$6)", sourceID, ref+".json", sum, len(data), path, u.ID)
	if e == nil {
		_, e = tx.Exec("INSERT INTO registries(id,merchant_id,source_id,external_ref,status,total_cents,payment_date,payment_request_id) VALUES($1,$2,$3,$4,'preview',$5,$6,$7)", registryID, input.MerchantID, sourceID, ref, total, paymentDate, nilID(input.PaymentRequestID))
	}
	for i, row := range prepared {
		if e != nil {
			break
		}
		_, e = tx.Exec("INSERT INTO registry_rows(id,registry_id,row_no,sheet_name,raw,card_id,amount_cents) VALUES($1,$2,$3,'manual',$4,$5,$6)", id(), registryID, i+1, encode([]string{row.mask, rub(row.amount), row.name, row.phone}), row.cardID, row.amount)
	}
	if e == nil {
		e = txAudit(tx, u.ID, "web", "registry_manual_create", "registry", registryID, "success", "", M{"rows": len(prepared), "total_cents": total, "source_sha256": sum, "payment_request_id": input.PaymentRequestID})
	}
	if e != nil {
		fail(w, 409, errors.New("не удалось сохранить ручной реестр"))
		return
	}
	if e = tx.Commit(); e != nil {
		fail(w, 409, errors.New("не удалось сохранить ручной реестр"))
		return
	}
	keepFile = true
	respond(w, 201, M{"id": registryID, "rows": len(prepared), "accepted_total": rub(total)})
}
