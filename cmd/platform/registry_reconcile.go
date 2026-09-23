package main

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"
)

func (a *App) registryCandidates(w http.ResponseWriter, r *http.Request, u User) {
	if r.Method != http.MethodGet || !a.require(w, u, "chief", "operator") {
		return
	}
	registryID := r.URL.Query().Get("id")
	if !validID(registryID) {
		fail(w, 400, errors.New("неверный реестр"))
		return
	}
	var requestID, status, uploader string
	e := a.db.QueryRow(`SELECT COALESCE(r.payment_request_id::text,''),r.status,s.uploader_id
		FROM registries r JOIN source_documents s ON s.id=r.source_id WHERE r.id=$1`, registryID).Scan(&requestID, &status, &uploader)
	if e != nil {
		fail(w, 404, errors.New("реестр не найден"))
		return
	}
	if u.Role == "operator" && uploader != u.ID {
		fail(w, 403, errors.New("реестр не доступен"))
		return
	}
	if status != "preview" || requestID == "" {
		fail(w, 409, errors.New("для реестра недоступна сверка с запросом карт"))
		return
	}
	rows, e := a.db.Query(`SELECT x.row_no,x.card_id,c.mask,b.name,COALESCE(x.contact_name,''),COALESCE(x.contact_phone,'')
		FROM payment_request_rows x JOIN payment_requests p ON p.id=x.request_id AND p.deleted_at IS NULL
		JOIN cards c ON c.id=x.card_id JOIN banks b ON b.id=c.bank_id
		WHERE x.request_id=$1 ORDER BY x.row_no`, requestID)
	if e != nil {
		fail(w, 500, e)
		return
	}
	defer rows.Close()
	out := []M{}
	for rows.Next() {
		var rowNo int
		var cardID, mask, bank, name, phone string
		if e = rows.Scan(&rowNo, &cardID, &mask, &bank, &name, &phone); e != nil {
			fail(w, 500, e)
			return
		}
		out = append(out, M{"row": rowNo, "card_id": cardID, "mask": mask, "bank": bank, "contact_name": name, "contact_phone": phone})
	}
	if e = rows.Err(); e != nil {
		fail(w, 500, e)
		return
	}
	respond(w, 200, out)
}

func (a *App) reconcileRegistryRow(w http.ResponseWriter, r *http.Request, u User) {
	if r.Method != http.MethodPost || !a.require(w, u, "chief", "operator") {
		return
	}
	m, e := jsonBody(r)
	if e != nil {
		fail(w, 400, e)
		return
	}
	registryID, cardID := str(m, "registry_id"), str(m, "card_id")
	rowNo, rowErr := strconv.Atoi(str(m, "row"))
	version, versionErr := strconv.Atoi(str(m, "version"))
	if !validID(registryID) || !validID(cardID) || rowErr != nil || rowNo < 1 || versionErr != nil || version < 1 {
		fail(w, 400, errors.New("неверные данные сопоставления"))
		return
	}
	tx, e := a.tx()
	if e != nil {
		fail(w, 500, e)
		return
	}
	defer tx.Rollback()
	var requestID, status, uploader string
	var actualVersion int
	e = tx.QueryRow(`SELECT COALESCE(r.payment_request_id::text,''),r.status,r.version,s.uploader_id
		FROM registries r JOIN source_documents s ON s.id=r.source_id WHERE r.id=$1 FOR UPDATE`, registryID).Scan(&requestID, &status, &actualVersion, &uploader)
	if e != nil {
		fail(w, 404, errors.New("реестр не найден"))
		return
	}
	if u.Role == "operator" && uploader != u.ID {
		fail(w, 403, errors.New("реестр не доступен"))
		return
	}
	if status != "preview" || requestID == "" || version != actualVersion {
		fail(w, 409, errors.New("предпросмотр реестра устарел"))
		return
	}
	var candidateExists bool
	if e = tx.QueryRow("SELECT EXISTS(SELECT 1 FROM payment_request_rows WHERE request_id=$1 AND card_id=$2)", requestID, cardID).Scan(&candidateExists); e != nil || !candidateExists {
		fail(w, 400, errors.New("карта отсутствует в выбранном запросе"))
		return
	}
	var priorCard string
	var priorError sql.NullString
	var amount int64
	e = tx.QueryRow("SELECT COALESCE(card_id::text,''),error_code,COALESCE(amount_cents,0) FROM registry_rows WHERE registry_id=$1 AND row_no=$2 FOR UPDATE", registryID, rowNo).Scan(&priorCard, &priorError, &amount)
	if e != nil {
		fail(w, 404, errors.New("строка реестра не найдена"))
		return
	}
	if amount <= 0 || priorError.Valid && priorError.String != "request_contact_not_found_or_ambiguous" && priorError.String != "request_card_not_found_or_ambiguous" {
		fail(w, 409, errors.New("в строке есть ошибка, не связанная с выбором карты"))
		return
	}
	if _, e = tx.Exec("UPDATE registry_rows SET card_id=$1,error_code=NULL WHERE registry_id=$2 AND row_no=$3", cardID, registryID, rowNo); e != nil {
		fail(w, 500, e)
		return
	}
	if _, e = tx.Exec("UPDATE registries SET version=version+1 WHERE id=$1", registryID); e != nil {
		fail(w, 500, e)
		return
	}
	detail := M{"row": rowNo, "prior_card_id": priorCard, "card_id": cardID, "version": actualVersion + 1}
	if priorError.Valid {
		detail["prior_error"] = priorError.String
	}
	if e = txAudit(tx, u.ID, "web", "registry_row_reconcile", "registry", registryID, "success", "", detail); e != nil {
		fail(w, 500, e)
		return
	}
	if e = tx.Commit(); e != nil {
		fail(w, 409, e)
		return
	}
	respond(w, 200, M{"id": registryID, "row": rowNo, "version": actualVersion + 1})
}
