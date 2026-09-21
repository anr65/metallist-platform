package main

import (
	"errors"
	"net/http"
	"os"
)

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
	if err = a.db.QueryRow("SELECT mask FROM cards WHERE id=$1 AND status='active'", cardID).Scan(&mask); err != nil {
		fail(w, 404, errors.New("действующая карта не найдена"))
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
