package main

import (
	"errors"
	"fmt"
	"strings"
)

type telegramTransferRecipient struct {
	ID   string
	Name string
}

func (a *App) telegramTransferRecipients(u telegramActor) ([]telegramTransferRecipient, error) {
	if u.Role != "collector" {
		return nil, errors.New("Перевод доступен только сборщику")
	}
	rows, err := a.db.Query("SELECT DISTINCT c.id,c.name FROM custodians c JOIN users u ON u.custodian_id=c.id AND u.active AND u.role='collector' WHERE c.active AND c.kind='collector' AND c.id<>$1 ORDER BY c.name,c.id", u.CustodianID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var recipients []telegramTransferRecipient
	for rows.Next() {
		var recipient telegramTransferRecipient
		if err = rows.Scan(&recipient.ID, &recipient.Name); err != nil {
			return nil, err
		}
		recipients = append(recipients, recipient)
	}
	return recipients, rows.Err()
}

func (a *App) telegramTransferOptions(u telegramActor, chat int64) error {
	recipients, err := a.telegramTransferRecipients(u)
	if err != nil {
		return err
	}
	if len(recipients) == 0 {
		return a.telegramReply(chat, "Других активных сборщиков пока нет.", nil)
	}
	options := make([]string, 0, len(recipients)+2)
	options = append(options, "Выберите получателя по номеру:")
	for i, recipient := range recipients {
		options = append(options, fmt.Sprintf("%d. %s", i+1, recipient.Name))
	}
	options = append(options, "Ответьте номером и суммой, например: 1 1000,00. Затем подтвердите перевод кнопкой. /cancel — отмена ввода.")
	return a.telegramReply(chat, strings.Join(options, "\n"), nil)
}
