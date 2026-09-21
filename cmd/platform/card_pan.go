package main

import (
	"errors"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"
)

var panInReasonPattern = regexp.MustCompile(`(?:[0-9][ -]*){13,19}`)

func (a *App) setCardPAN(w http.ResponseWriter, r *http.Request, u User) {
	if r.Method != http.MethodPost || !a.require(w, u, "chief") {
		return
	}
	m, err := jsonBody(r)
	if err != nil {
		fail(w, 400, err)
		return
	}
	cardID, pan := str(m, "card_id"), str(m, "pan")
	if !cardIDPattern.MatchString(cardID) {
		fail(w, 400, errors.New("неверный ID карты"))
		return
	}
	var mask string
	var ciphertext []byte
	if err = a.db.QueryRow("SELECT mask,pan_ciphertext FROM cards WHERE id=$1 AND status='active'", cardID).Scan(&mask, &ciphertext); err != nil {
		fail(w, 404, errors.New("действующая карта не найдена"))
		return
	}
	if len(ciphertext) > 0 {
		fail(w, 409, errors.New("полный номер уже сохранён; используйте исправление номера"))
		return
	}
	if err = validatePAN(pan, mask); err != nil {
		fail(w, 400, err)
		return
	}
	if err = savePAN(cardID, pan); err != nil {
		fail(w, 409, err)
		return
	}
	path, _ := vaultPath(cardID)
	if _, err = a.db.Exec("INSERT INTO audit_events(id,actor_id,actor_role,channel,action,object_type,object_id,outcome,detail) VALUES($1,$2,$3,'web','card_pan_set','card',$4,'success','{}'::jsonb)", id(), u.ID, u.Role, cardID); err != nil {
		_ = os.Remove(path)
		fail(w, 500, errors.New("не удалось записать действие"))
		return
	}
	respond(w, 201, M{"card_id": cardID, "pan_saved": true})
}

func (a *App) replaceCardPAN(w http.ResponseWriter, r *http.Request, u User) {
	if r.Method != http.MethodPost || !a.require(w, u, "chief", "operator") {
		return
	}
	m, err := jsonBody(r)
	if err != nil {
		fail(w, 400, err)
		return
	}
	cardID, pan := str(m, "card_id"), str(m, "pan")
	expectedMask, reason := str(m, "expected_mask"), strings.TrimSpace(str(m, "reason"))
	if !cardIDPattern.MatchString(cardID) || !cardMaskPattern.MatchString(expectedMask) {
		fail(w, 400, errors.New("выберите карту для исправления"))
		return
	}
	if !panPattern.MatchString(pan) || !validLuhn(pan) {
		fail(w, 400, errors.New("укажите действительный полный номер карты"))
		return
	}
	if len([]rune(reason)) < 5 || len([]rune(reason)) > 300 {
		fail(w, 400, errors.New("укажите причину исправления от 5 до 300 символов"))
		return
	}
	if panInReasonPattern.MatchString(reason) {
		fail(w, 400, errors.New("не указывайте номер карты в причине исправления"))
		return
	}
	tx, err := a.tx()
	if err != nil {
		fail(w, 500, err)
		return
	}
	defer tx.Rollback()
	var oldMask string
	var oldCiphertext []byte
	if err = tx.QueryRow("SELECT mask,pan_ciphertext FROM cards WHERE id=$1 AND status='active' FOR UPDATE", cardID).Scan(&oldMask, &oldCiphertext); err != nil {
		fail(w, 404, errors.New("действующая карта не найдена"))
		return
	}
	if expectedMask != oldMask {
		fail(w, 409, errors.New("маска карты изменилась; обновите список и повторите действие"))
		return
	}
	oldPAN, err := readCardPAN(cardID, oldCiphertext)
	if err != nil {
		fail(w, 409, errors.New("сначала сохраните полный номер карты"))
		return
	}
	if oldPAN == pan {
		fail(w, 409, errors.New("новый номер совпадает с сохранённым"))
		return
	}
	var used bool
	if err = tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM payment_request_rows WHERE card_id=$1)
		OR EXISTS(SELECT 1 FROM registry_rows WHERE card_id=$1)
		OR EXISTS(SELECT 1 FROM postings WHERE card_id=$1)
		OR EXISTS(SELECT 1 FROM observations WHERE card_id=$1)`, cardID).Scan(&used); err != nil {
		fail(w, 500, err)
		return
	}
	if used {
		fail(w, 409, errors.New("номер нельзя изменить: карта уже использована в запросе, реестре или финансовой истории"))
		return
	}
	newMask := pan[:6] + "******" + pan[len(pan)-4:]
	ciphertext, err := sealSensitive([]byte(pan), []byte(cardID))
	if err != nil {
		fail(w, 500, errors.New("защищённое хранение номера недоступно"))
		return
	}
	if _, err = tx.Exec("UPDATE cards SET mask=$2,last4=$3,pan_ciphertext=$4 WHERE id=$1", cardID, newMask, pan[len(pan)-4:], ciphertext); err != nil {
		fail(w, 409, errors.New("номер с такой маской уже есть у этого банка или изменение недоступно"))
		return
	}
	if err = txAudit(tx, u.ID, "web", "card_pan_replace", "card", cardID, "success", reason, M{"old_mask": oldMask, "new_mask": newMask}); err != nil {
		fail(w, 500, errors.New("не удалось записать аудит исправления"))
		return
	}
	if err = tx.Commit(); err != nil {
		fail(w, 500, errors.New("не удалось сохранить исправление"))
		return
	}
	path, pathErr := vaultPath(cardID)
	if pathErr == nil {
		if removeErr := os.Remove(path); removeErr != nil && !os.IsNotExist(removeErr) {
			log.Printf("could not remove old encrypted PAN file for card %s", cardID)
		}
	}
	respond(w, 200, M{"card_id": cardID, "mask": newMask})
}
