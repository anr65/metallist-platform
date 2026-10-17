package main

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"
)

// catalogUpdate edits descriptive directory fields only. Card numbers and
// accounting identities have their own guarded workflows.
func (a *App) catalogUpdate(w http.ResponseWriter, r *http.Request, u User) {
	if r.Method != http.MethodPost {
		fail(w, http.StatusMethodNotAllowed, errors.New("method"))
		return
	}
	m, err := jsonBody(r)
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	kind, recordID := str(m, "kind"), str(m, "id")
	if !validID(recordID) {
		fail(w, http.StatusBadRequest, errors.New("неверная запись справочника"))
		return
	}
	if kind == "card" || kind == "payment_contact" {
		if !a.require(w, u, "chief", "operator") {
			return
		}
	} else if !a.require(w, u, "chief") {
		return
	}
	tx, err := a.tx()
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	defer tx.Rollback()

	var result interface{ RowsAffected() (int64, error) }
	switch kind {
	case "bank":
		code, name := strings.TrimSpace(str(m, "code")), strings.TrimSpace(str(m, "name"))
		if len(code) < 1 || len(code) > 24 || len([]rune(name)) < 1 || len([]rune(name)) > 200 {
			err = errors.New("укажите код до 24 символов и название до 200 символов")
		} else {
			result, err = tx.Exec("UPDATE banks SET code=$2,name=$3 WHERE id=$1 AND source='manual'", recordID, code, name)
		}
	case "card":
		owner := strings.TrimSpace(str(m, "owner_label"))
		if len([]rune(owner)) < 1 || len([]rune(owner)) > 200 {
			err = errors.New("укажите ФИО владельца до 200 символов")
		} else {
			var previous sql.NullString
			err = tx.QueryRow("SELECT drop_handler_id FROM cards WHERE id=$1 FOR UPDATE", recordID).Scan(&previous)
			handlerID := previous.String
			if _, present := m["drop_handler_id"]; present {
				handlerID = str(m, "drop_handler_id")
				if handlerID != "" && handlerID != previous.String && err == nil {
					err = validateDropHandler(tx, handlerID)
				}
			}
			if err == nil && str(m, "status") != "" {
				err = changeCardStatus(tx, u.ID, "web", recordID, str(m, "status"), time.Now())
			}
			if err == nil {
				result, err = tx.Exec("UPDATE cards SET owner_label=$2,drop_handler_id=$3 WHERE id=$1", recordID, owner, nilID(handlerID))
			}
			if err == nil && handlerID != previous.String {
				err = txAudit(tx, u.ID, "web", "card_drop_handler_change", "card", recordID, "success", "", M{"before": nilID(previous.String), "after": nilID(handlerID)})
			}
		}
	case "drop_handler":
		name := strings.TrimSpace(str(m, "name"))
		if len([]rune(name)) < 1 || len([]rune(name)) > 200 {
			err = errors.New("укажите имя до 200 символов")
		} else {
			var active interface{}
			if value, present := m["active"]; present {
				switch value {
				case true, "true":
					active = true
				case false, "false":
					active = false
				default:
					err = errors.New("неверная активность дроповода")
				}
			}
			if err == nil {
				result, err = tx.Exec("UPDATE drop_handlers SET name=$2,active=COALESCE($3,active) WHERE id=$1", recordID, name, active)
			}
		}
	case "payment_contact":
		name := strings.TrimSpace(str(m, "full_name"))
		var phone string
		phone, err = normalizePaymentPhone(str(m, "phone"))
		if err == nil && (len([]rune(name)) < 5 || len([]rune(name)) > 200) {
			err = errors.New("укажите полное ФИО до 200 символов")
		}
		if err == nil {
			result, err = tx.Exec("UPDATE payment_contacts SET full_name=$2,phone=$3 WHERE id=$1 AND active", recordID, name, phone)
		}
	case "custodian":
		name := strings.TrimSpace(str(m, "name"))
		if len([]rune(name)) < 1 || len([]rune(name)) > 200 {
			err = errors.New("укажите имя до 200 символов")
		} else {
			result, err = tx.Exec("UPDATE custodians SET name=$2 WHERE id=$1 AND active", recordID, name)
		}
	default:
		err = errors.New("редактирование этого справочника недоступно")
	}
	if err != nil {
		a.logAudit(u.ID, "web", "catalog_update", kind, recordID, "rejected", err.Error(), M{})
		fail(w, http.StatusBadRequest, err)
		return
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		fail(w, http.StatusNotFound, errors.New("запись не найдена или недоступна для редактирования"))
		return
	}
	if err = txAudit(tx, u.ID, "web", "catalog_update", kind, recordID, "success", "", M{}); err != nil {
		fail(w, http.StatusInternalServerError, errors.New("не удалось записать аудит изменения"))
		return
	}
	if err = tx.Commit(); err != nil {
		fail(w, http.StatusInternalServerError, errors.New("не удалось сохранить изменение"))
		return
	}
	respond(w, http.StatusOK, M{"id": recordID})
}
