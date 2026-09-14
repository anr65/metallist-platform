package main

import (
	"database/sql"
	"errors"
	"strings"
	"time"
)

func (a *App) telegramCallback(u telegramActor, chat int64, data string) (string, bool) {
	action, draftID, ok := strings.Cut(data, ":")
	if !ok || (action != "confirm" && action != "reject") || len(draftID) != 36 {
		return "Неизвестная кнопка", false
	}
	tx, e := a.tx()
	if e != nil {
		return "Не удалось открыть операцию", false
	}
	defer tx.Rollback()
	var kind, status, creator string
	var raw []byte
	e = tx.QueryRow("SELECT kind,status,created_by,payload FROM drafts WHERE id=$1 FOR UPDATE", draftID).Scan(&kind, &status, &creator, &raw)
	if e != nil {
		return "Операция не найдена", false
	}
	p, e := decodeMap(raw)
	if e != nil || creator != u.ID || p["telegram_confirmation_required"] != true || (kind != "withdrawal" && kind != "expense") {
		return "Нет доступа к этой операции", false
	}
	storedChat, e := telegramInt(p["telegram_chat_id"])
	if e != nil || storedChat != chat {
		return "Нет доступа к этой операции", false
	}
	if status == "posted" || status == "rejected" {
		return "Операция уже обработана", true
	}
	if status != "draft" {
		return "Операция недоступна", false
	}
	if p["telegram_preview_sent"] != true {
		return "Предпросмотр ещё не доставлен. Повторите команду после получения сообщения.", false
	}
	if action == "reject" {
		if _, e = tx.Exec("UPDATE drafts SET status='rejected',payload=jsonb_set(payload,'{telegram_sender_confirmed}','false'::jsonb) WHERE id=$1", draftID); e != nil {
			return "Не удалось отклонить", false
		}
		if e = txAudit(tx, u.ID, "telegram", "draft_reject", kind, draftID, "success", "", M{}); e != nil || tx.Commit() != nil {
			return "Не удалось отклонить", false
		}
		_ = a.telegramReply(chat, "Операция отклонена. Балансы не изменились.", nil)
		return "Отклонено", true
	}
	amountCents, e := telegramInt(p["amount_cents"])
	if e != nil || amountCents <= 0 {
		return "Сумма черновика некорректна", false
	}
	if e = a.telegramValidateConfirmation(tx, u, kind, p); e != nil {
		a.logAudit(u.ID, "telegram", "draft_confirm", kind, draftID, "rejected", e.Error(), M{})
		return e.Error(), false
	}
	if kind == "withdrawal" {
		if _, e = telegramInt(p["observed_cents"]); e != nil {
			return "Остаток черновика некорректен", false
		}
	}
	lines, e := a.eventLines(tx, kind, p, amountCents)
	if e != nil {
		a.logAudit(u.ID, "telegram", "draft_confirm", kind, draftID, "rejected", e.Error(), M{})
		return "Не удалось провести: " + e.Error(), false
	}
	now := time.Now()
	if _, e = put(tx, kind, draftID, "draft:"+draftID, u.ID, now, now, lines, ""); e != nil {
		return "Не удалось провести операцию", false
	}
	if kind == "withdrawal" {
		observed, _ := telegramInt(p["observed_cents"])
		if _, e = tx.Exec("INSERT INTO observations(id,card_id,observed_cents,observed_at,reporter_id,source) VALUES($1,$2,$3,$4,$5,$6)", id(), str(p, "card_id"), observed, now, u.ID, "telegram:"+draftID); e != nil {
			return "Не удалось сохранить остаток", false
		}
	}
	p["telegram_sender_confirmed"] = true
	if _, e = tx.Exec("UPDATE drafts SET status='posted',payload=$1,confirmed_by=$2,confirmed_at=$3,confirmed_amount_cents=$4 WHERE id=$5", encode(p), u.ID, now, amountCents, draftID); e != nil {
		return "Не удалось завершить операцию", false
	}
	if e = txAudit(tx, u.ID, "telegram", "draft_confirm", kind, draftID, "success", "", M{"confirmed_cents": amountCents}); e != nil {
		return "Не удалось записать аудит", false
	}
	if e = tx.Commit(); e != nil {
		return "Не удалось провести операцию", false
	}
	label := "Снятие"
	if kind == "expense" {
		label = "Расход"
	}
	_ = a.telegramReply(chat, label+" подтверждён: "+telegramMoney(amountCents)+". Балансы обновлены.", nil)
	return "Подтверждено", true
}

func (a *App) telegramValidateConfirmation(tx *sql.Tx, u telegramActor, kind string, p M) error {
	var currentCustodian, role, cardStatus string
	e := tx.QueryRow("SELECT role,COALESCE(custodian_id::text,'') FROM users WHERE id=$1 AND active FOR SHARE", u.ID).Scan(&role, &currentCustodian)
	if e != nil || currentCustodian != u.CustodianID || (role != "collector" && role != "chief") {
		return errors.New("Связь Telegram с ответственным изменилась")
	}
	cardID := str(p, "card_id")
	if e = tx.QueryRow("SELECT status FROM cards WHERE id=$1", cardID).Scan(&cardStatus); e != nil || cardStatus != "active" {
		return errors.New("Карта недоступна")
	}
	if role == "collector" {
		var assigned bool
		if e = tx.QueryRow("SELECT EXISTS(SELECT 1 FROM card_assignments WHERE user_id=$1 AND card_id=$2)", u.ID, cardID).Scan(&assigned); e != nil || !assigned {
			return errors.New("Карта больше не назначена сборщику")
		}
	}
	if kind == "withdrawal" {
		if str(p, "custodian_id") != currentCustodian {
			return errors.New("Получатель наличных изменился")
		}
		var kind string
		if e = tx.QueryRow("SELECT kind FROM custodians WHERE id=$1 AND active", currentCustodian).Scan(&kind); e != nil || (role == "collector" && kind != "collector") || (role == "chief" && kind != "chief") {
			return errors.New("Ответственный за наличные недоступен")
		}
	} else {
		if str(p, "source_kind") != "card" || str(p, "source_id") != cardID {
			return errors.New("Источник расхода изменился")
		}
		category := str(p, "category")
		if _, e = expenseAccount(category); e != nil {
			return errors.New("Категория недоступна")
		}
		if role == "collector" && category != "warmup" && category != "bank_fee" {
			return errors.New("Сборщику доступен только прогрев и банковская комиссия")
		}
	}
	return nil
}
