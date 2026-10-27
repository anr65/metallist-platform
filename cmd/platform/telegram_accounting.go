package main

import (
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Durable event recipients are captured atomically with the business transition.
func queueAccountingNotification(tx *sql.Tx, kind, object string) error {
	details := M{}
	if kind == "repayment" {
		var position int64
		if err := tx.QueryRow(`SELECT COALESCE(sum(CASE WHEN p.account='2100' THEN CASE WHEN p.side='credit' THEN p.amount_cents ELSE -p.amount_cents END
 ELSE CASE WHEN p.side='debit' THEN -p.amount_cents ELSE p.amount_cents END END),0)
 FROM postings p WHERE p.account IN ('2100','1300') AND p.merchant_id=(SELECT (payload->>'merchant_id')::uuid FROM drafts WHERE id=$1)`, object).Scan(&position); err != nil {
			return err
		}
		details["merchant_position_cents"] = position
	}
	_, err := tx.Exec(`INSERT INTO accounting_notifications(id,user_id,event_kind,object_id,details)
 SELECT gen_random_uuid(),id,$1,$2,$3 FROM users WHERE active AND role='sysadmin'
 ON CONFLICT(user_id,event_kind,object_id) DO NOTHING`, kind, object, encode(details))
	return err
}

func (a *App) accountingNotificationWorker() {
	for {
		if err := a.deliverAccountingNotifications(10); err != nil {
			log.Printf("accounting notifications: %v", err)
		}
		time.Sleep(3 * time.Second)
	}
}
func (a *App) deliverAccountingNotifications(limit int) error {
	for i := 0; i < limit; i++ {
		var nid string
		err := a.db.QueryRow(`UPDATE accounting_notifications SET attempts=attempts+1,next_attempt_at=now()+interval '1 minute'
 WHERE id=(SELECT id FROM accounting_notifications WHERE state='pending' AND next_attempt_at<=now()
 ORDER BY next_attempt_at,id LIMIT 1 FOR UPDATE SKIP LOCKED) RETURNING id`).Scan(&nid)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if err = a.deliverAccountingNotification(nid); err != nil {
			return err
		}
	}
	return nil
}

type accountingRegistryPreview struct {
	ID, MerchantID, Merchant, Status, PaymentDate, RowHash, Author string
	Version, Count, Bad, Rate                                      int
	Total                                                          int64
	RateAvailable                                                  bool
}

func (a *App) accountingRegistryPreview(tx *sql.Tx, rid string) (accountingRegistryPreview, error) {
	p := accountingRegistryPreview{ID: rid}
	var merchant string
	var manual sql.NullInt64
	err := tx.QueryRow(`SELECT r.merchant_id,m.name,r.status,r.version,r.total_cents,COALESCE(r.payment_date::text,''),r.manual_rate_bp,COALESCE(u.name,'')
 FROM registries r JOIN merchants m ON m.id=r.merchant_id JOIN source_documents s ON s.id=r.source_id LEFT JOIN users u ON u.id=s.uploader_id WHERE r.id=$1 FOR UPDATE OF r`, rid).Scan(&merchant, &p.Merchant, &p.Status, &p.Version, &p.Total, &p.PaymentDate, &manual, &p.Author)
	if err != nil {
		return p, err
	}
	err = tx.QueryRow(`SELECT count(*),count(*) FILTER(WHERE error_code IS NOT NULL OR card_id IS NULL),
 COALESCE(md5(string_agg(row_no::text||':'||COALESCE(card_id::text,'')||':'||amount_cents::text||':'||COALESCE(error_code,''),'|' ORDER BY row_no)),'') FROM registry_rows WHERE registry_id=$1`, rid).Scan(&p.Count, &p.Bad, &p.RowHash)
	if err != nil {
		return p, err
	}
	p.MerchantID = merchant
	if p.PaymentDate == "" {
		return p, nil
	}
	if manual.Valid {
		p.Rate = int(manual.Int64)
		p.RateAvailable = true
	} else {
		p.Rate, err = a.currentRate(tx, merchant, p.PaymentDate)
		p.RateAvailable = err == nil
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return p, err
		}
	}
	return p, nil
}
func (p accountingRegistryPreview) fingerprint() string {
	return hex.EncodeToString(digest(fmt.Sprintf("%s:%s:%q:%d:%d:%s:%s:%d:%t", p.ID, p.MerchantID, p.Merchant, p.Version, p.Total, p.PaymentDate, p.RowHash, p.Rate, p.RateAvailable)))
}
func (p accountingRegistryPreview) ready() bool {
	return p.Status == "preview" && p.Bad == 0 && p.Count > 0 && p.RateAvailable && p.PaymentDate != ""
}
func (p accountingRegistryPreview) message() string {
	text := fmt.Sprintf("📋 Реестр оплат\nID: %s\nМерчант: %s\nДата оплат: %s\nДобавил: %s\nСтрок: %d; требуют исправления: %d\nСумма: %s", p.ID, p.Merchant, p.PaymentDate, p.Author, p.Count, p.Bad, accountingMoney(p.Total))
	if p.RateAvailable {
		text += fmt.Sprintf("\nСтавка: %s %%\nКомиссия: %s\nЧистый долг: %s", rub(int64(p.Rate)), accountingMoney(fee(p.Total, p.Rate)), accountingMoney(p.Total-fee(p.Total, p.Rate)))
	} else {
		text += "\nТариф не задан"
	}
	if p.ready() {
		text += "\n\nНажимая «Подтвердить реестр», вы подтверждаете корректность реестра и фактическое поступление денег."
	} else {
		text += "\nПодтверждение недоступно: проверьте статус, строки, дату оплат и тариф."
	}
	return text + "\nhttps://metallcash.work/#registries/" + p.ID
}
func (a *App) deliverAccountingNotification(nid string) error {
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var uid, kind, obj, state, role string
	var active bool
	var chat sql.NullInt64
	var details []byte
	err = tx.QueryRow(`SELECT n.user_id,n.event_kind,n.object_id,n.state,u.role,u.active,pc.chat_id,n.details
 FROM accounting_notifications n JOIN users u ON u.id=n.user_id
 LEFT JOIN telegram_private_chats pc ON pc.user_id=u.id AND pc.telegram_id=u.telegram_id
 WHERE n.id=$1 FOR UPDATE OF n FOR SHARE OF u`, nid).Scan(&uid, &kind, &obj, &state, &role, &active, &chat, &details)
	if err != nil {
		return err
	}
	if state != "pending" {
		return nil
	}
	if !active || role != "sysadmin" {
		_, err = tx.Exec("UPDATE accounting_notifications SET state='cancelled' WHERE id=$1", nid)
		if err != nil {
			return err
		}
		return tx.Commit()
	}
	var text string
	var markup interface{}
	if !chat.Valid {
		err = errors.New("Личный чат не подключен: привяжите Telegram и отправьте /start")
	} else if kind == "registry" {
		var p accountingRegistryPreview
		p, err = a.accountingRegistryPreview(tx, obj)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && p.Status != "preview") {
			_, err = tx.Exec("UPDATE accounting_notifications SET state='cancelled',last_error='' WHERE id=$1", nid)
			if err != nil {
				return err
			}
			return tx.Commit()
		}
		if err == nil {
			text = p.message()
			if p.ready() {
				previewID := id()
				_, err = tx.Exec("INSERT INTO accounting_registry_previews(id,notification_id,telegram_id,fingerprint) VALUES($1,$2,$3,$4)", previewID, nid, chat.Int64, p.fingerprint())
				markup = M{"inline_keyboard": []interface{}{[]interface{}{M{"text": "Подтвердить реестр", "callback_data": "ar:" + previewID}}}}
			}
		}
	} else {
		text, err = a.accountingOperationMessage(tx, kind, obj, details)
	}
	// Database errors must roll back, rather than committing an aborted transaction.
	if err != nil && chat.Valid {
		return err
	}
	if err == nil {
		err = a.telegramReply(chat.Int64, text, markup)
	}
	if err != nil {
		_, err = tx.Exec("UPDATE accounting_notifications SET last_error=$2 WHERE id=$1", nid, err.Error())
	} else {
		_, err = tx.Exec("UPDATE accounting_notifications SET state='sent',last_error='' WHERE id=$1", nid)
		if err == nil {
			err = txAudit(tx, uid, "telegram", "accounting_notification", kind, obj, "success", "", M{"notification_id": nid})
		}
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}
func (a *App) accountingOperationMessage(tx *sql.Tx, kind, obj string, details []byte) (string, error) {
	var raw []byte
	var cents int64
	var claimed int64
	var actor, status string
	var occurred, posted time.Time
	err := tx.QueryRow(`SELECT d.payload,d.confirmed_amount_cents,u.name,j.occurred_at,j.posted_at,d.status
 FROM drafts d JOIN journal_entries j ON j.event_id=d.id AND j.event_type=d.kind AND j.reversal_of IS NULL
 JOIN users u ON u.id=j.actor_id WHERE d.id=$1 AND d.kind=$2 AND d.status IN ('posted','reversed')`, obj, kind).Scan(&raw, &cents, &actor, &occurred, &posted, &status)
	if err != nil {
		return "", err
	}
	p, err := decodeMap(raw)
	if err != nil {
		return "", err
	}
	claimed, _ = telegramInt(p["amount_cents"])
	text := fmt.Sprintf("ID: %s\nДата операции: %s\nПроведено: %s\nПодтвердил: %s", obj, occurred.In(a.location).Format("02.01.2006"), posted.In(a.location).Format("02.01.2006 15:04:05 МСК"), actor)
	if status == "reversed" {
		text += "\nВнимание: операция впоследствии сторнирована."
	}
	if kind == "handover" {
		var from, to string
		if err = tx.QueryRow("SELECT a.name,b.name FROM custodians a,custodians b WHERE a.id=$1 AND b.id=$2", str(p, "from_custodian_id"), str(p, "to_custodian_id")).Scan(&from, &to); err != nil {
			return "", err
		}
		return fmt.Sprintf("✅ Передача главному администратору проведена\n%s\nОт: %s\nКому: %s\nЗаявлено: %s\nПринято: %s\nРазница: %s", text, from, to, accountingMoney(claimed), accountingMoney(cents), accountingMoney(claimed-cents)), nil
	}
	var merchant, source string
	if err = tx.QueryRow("SELECT name FROM merchants WHERE id=$1", str(p, "merchant_id")).Scan(&merchant); err != nil {
		return "", err
	}
	if str(p, "source_kind") == "cash" {
		err = tx.QueryRow("SELECT name FROM custodians WHERE id=$1", str(p, "source_id")).Scan(&source)
	} else {
		err = tx.QueryRow("SELECT mask FROM cards WHERE id=$1", str(p, "source_id")).Scan(&source)
	}
	if err != nil {
		return "", err
	}
	position := ""
	if d, e := decodeMap(details); e == nil {
		if v, ok := d["merchant_position_cents"]; ok {
			n, e := telegramInt(v)
			if e == nil {
				payable, receivable := n, int64(0)
				if n < 0 {
					payable = 0
					receivable = -n
				}
				position = "\nПосле операции к возврату: " + accountingMoney(payable) + "\nДебиторка / переплата: " + accountingMoney(receivable)
			}
		}
	}
	return fmt.Sprintf("✅ Возврат мерчанту проведен\n%s\nМерчант: %s\nИсточник: %s\nСумма: %s", text, merchant, source, accountingMoney(cents)) + position, nil
}

// Only sysadmins enter this adapter. Collectors retain the existing private workflow.
func (a *App) telegramAccountingPrivate(w http.ResponseWriter, x telegramUpdate, sender, chat int64) bool {
	var u telegramActor
	err := a.db.QueryRow("SELECT id,login,name,role FROM users WHERE active AND role='sysadmin' AND telegram_id=$1", sender).Scan(&u.ID, &u.Login, &u.Name, &u.Role)
	if errors.Is(err, sql.ErrNoRows) {
		return false
	}
	if err != nil {
		http.Error(w, "database unavailable", 500)
		return true
	}
	if x.CallbackQuery != nil {
		data := x.CallbackQuery.Data
		var msg string
		if strings.HasPrefix(data, "ar:") {
			msg = a.accountingRegistryCallback(u, sender, chat, strings.TrimPrefix(data, "ar:"))
			if strings.HasPrefix(msg, "Реестр подтвержден") || msg == "Реестр уже обработан" {
				a.telegramRemoveButtons(chat, x.CallbackQuery.Message.MessageID)
			}
		} else if strings.HasPrefix(data, "ab:") {
			parts := strings.Split(data, ":")
			page := 0
			if len(parts) == 3 {
				page, err = strconv.Atoi(parts[2])
				if err == nil {
					err = a.accountingBalances(u, sender, chat, parts[1], page)
				}
			} else {
				err = errors.New("Неизвестная страница")
			}
			if err != nil {
				msg = "Не удалось открыть баланс"
			} else {
				msg = "Балансы обновлены"
			}
		} else {
			msg = "Неизвестная кнопка"
		}
		if a.telegramAnswer(x.CallbackQuery.ID, msg) != nil {
			http.Error(w, "telegram delivery failed", 500)
		} else {
			w.WriteHeader(200)
		}
		return true
	}
	if x.Message == nil {
		w.WriteHeader(200)
		return true
	}
	command := telegramMessageCommand(x.Message.Text)
	if command == "start" {
		tx, e := a.tx()
		if e != nil {
			http.Error(w, "database unavailable", 500)
			return true
		}
		defer tx.Rollback()
		var tid int64
		e = tx.QueryRow("SELECT telegram_id FROM users WHERE id=$1 AND active AND role='sysadmin' FOR SHARE", u.ID).Scan(&tid)
		if e == nil && tid == sender {
			_, e = tx.Exec(`INSERT INTO telegram_private_chats(user_id,telegram_id,chat_id) VALUES($1,$2,$3)
 ON CONFLICT(user_id) DO UPDATE SET telegram_id=excluded.telegram_id,chat_id=excluded.chat_id,started_at=now()`, u.ID, sender, chat)
		} else {
			http.Error(w, "access changed", 403)
			return true
		}
		if e == nil {
			_, e = tx.Exec("UPDATE accounting_notifications SET next_attempt_at=now() WHERE user_id=$1 AND state='pending'", u.ID)
		}
		if e == nil {
			e = txAudit(tx, u.ID, "telegram", "private_chat_start", "user", u.ID, "success", "", M{"update_id": x.UpdateID})
		}
		if e == nil {
			e = tx.Commit()
		}
		if e != nil {
			http.Error(w, "database unavailable", 500)
			return true
		}
		_ = a.telegramCall("setMyCommands", M{"scope": M{"type": "chat", "chat_id": chat}, "commands": []M{{"command": "start", "description": "Подключить уведомления"}, {"command": "balances", "description": "Все балансы"}, {"command": "pending", "description": "Неподтвержденные реестры"}}})
		err = a.telegramReply(chat, "Личный чат подключен. Уведомления о возвратах, передачах и новых реестрах включены.\n/balances — балансы\n/pending — неподтвержденные реестры", accountingMenu())
	} else if command == "pending" {
		err = a.accountingPending(u, sender, chat)
	} else if command == "balances" || command == "cards_balance" {
		section := "summary"
		if command == "cards_balance" {
			section = "cards"
		}
		err = a.accountingBalances(u, sender, chat, section, 0)
	} else {
		err = a.telegramReply(chat, "Доступны /start, /balances и /pending", accountingMenu())
	}
	if err != nil {
		http.Error(w, "telegram delivery failed", 500)
	} else {
		w.WriteHeader(200)
	}
	return true
}
func accountingMenu() M {
	row := []interface{}{}
	for _, s := range []struct{ Key, Name string }{{"summary", "Сводка"}, {"merchants", "Мерчанты"}, {"custodians", "Хранители"}, {"cards", "Карты"}} {
		row = append(row, M{"text": s.Name, "callback_data": "ab:" + s.Key + ":0"})
	}
	return M{"inline_keyboard": []interface{}{row}}
}
func accountingAuthorize(tx *sql.Tx, u telegramActor, sender, chat int64) error {
	var tid int64
	err := tx.QueryRow(`SELECT u.telegram_id FROM users u JOIN telegram_private_chats pc ON pc.user_id=u.id AND pc.telegram_id=u.telegram_id
 WHERE u.id=$1 AND u.active AND u.role='sysadmin' AND pc.chat_id=$2 FOR SHARE OF u`, u.ID, chat).Scan(&tid)
	if err != nil || tid != sender || chat != sender {
		return errors.New("Доступ изменился или личный чат не подключен. Отправьте /start")
	}
	return nil
}
func (a *App) accountingPending(u telegramActor, sender, chat int64) error {
	tx, err := a.tx()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = accountingAuthorize(tx, u, sender, chat); err != nil {
		return a.telegramReply(chat, err.Error(), nil)
	}
	_, err = tx.Exec(`INSERT INTO accounting_notifications(id,user_id,event_kind,object_id)
 SELECT gen_random_uuid(),$1,'registry',id FROM registries WHERE status='preview'
 ON CONFLICT(user_id,event_kind,object_id) DO UPDATE SET state='pending',next_attempt_at=now(),last_error=''`, u.ID)
	if err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return a.telegramReply(chat, "Актуальные неподтвержденные реестры поставлены в очередь доставки.", accountingMenu())
}
func (a *App) accountingRegistryCallback(u telegramActor, sender, chat int64, token string) string {
	if !validID(token) {
		return "Неизвестная кнопка"
	}
	tx, err := a.tx()
	if err != nil {
		return "Не удалось открыть реестр"
	}
	defer tx.Rollback()
	if err = accountingAuthorize(tx, u, sender, chat); err != nil {
		return err.Error()
	}
	var nid, rid, fp string
	var tid int64
	err = tx.QueryRow(`SELECT n.id,n.object_id,p.fingerprint,p.telegram_id FROM accounting_registry_previews p
 JOIN accounting_notifications n ON n.id=p.notification_id WHERE p.id=$1 AND n.user_id=$2 FOR UPDATE OF n`, token, u.ID).Scan(&nid, &rid, &fp, &tid)
	if err != nil || tid != sender {
		return "Нет доступа к этому подтверждению"
	}
	p, err := a.accountingRegistryPreview(tx, rid)
	if err != nil {
		return "Реестр недоступен"
	}
	if p.Status != "preview" {
		return "Реестр уже обработан"
	}
	if !p.ready() || fp != p.fingerprint() {
		_, err = tx.Exec("UPDATE accounting_notifications SET state='pending',next_attempt_at=now() WHERE id=$1", nid)
		if err == nil {
			err = tx.Commit()
		}
		if err != nil {
			return "Не удалось обновить реестр"
		}
		return "Данные изменились. Новый расчет придет отдельным сообщением. Подтвердите его заново"
	}
	_, _, err = a.confirmRegistryCommand(tx, u.User, M{"id": rid, "version": strconv.Itoa(p.Version), "confirm_total": rub(p.Total), "confirm_commission": rub(fee(p.Total, p.Rate)), "confirm_rate_bp": strconv.Itoa(p.Rate)}, "telegram")
	if err != nil {
		return "Не удалось подтвердить. Обновите реестр: /pending"
	}
	_, err = tx.Exec("UPDATE accounting_notifications SET state='cancelled' WHERE event_kind='registry' AND object_id=$1", rid)
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		return "Не удалось сохранить; повторите"
	}
	return "Реестр подтвержден. Поступления, комиссия и долг учтены"
}

func accountingMoney(cents int64) string {
	text := telegramMoney(cents)
	if cents%100 == 0 {
		return strings.TrimSuffix(text, " ₽") + ",00 ₽"
	}
	return text
}
