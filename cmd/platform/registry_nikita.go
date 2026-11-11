package main

import (
	"errors"
	"fmt"
	"regexp"
)

const nikitaPayoutsParser = "nikita_payouts_xlsx_v1"

var nikitaPayoutMask = regexp.MustCompile(`^[0-9]{6}\*{6}[0-9]{4}$`)
var nikitaPayoutID = regexp.MustCompile(`^[0-9]+$`)

func parseNikitaSheet(sheet spreadsheetSheet) ([]importedRow, error) {
	if len(sheet.Rows) < 2 {
		return nil, errors.New("выгрузка Никиты без строк")
	}
	head := map[string]int{}
	for i, title := range sheet.Rows[0] {
		key := normalizeHeader(title)
		if key == "" {
			continue
		}
		if _, exists := head[key]; exists {
			return nil, fmt.Errorf("в выгрузке Никиты повторяется заголовок в колонке %d", i+1)
		}
		head[key] = i
	}
	fields := [...]string{"Статус выплаты", "Id выплаты", "Выплата", "Комиссия банка", "Тип реквизитов", "Реквизиты вывода"}
	var columns [len(fields)]int
	for i, title := range fields {
		index, ok := firstHeader(head, title)
		if !ok {
			return nil, fmt.Errorf("в выгрузке Никиты отсутствует колонка «%s»", title)
		}
		columns[i] = index
	}

	status, order, payout, fee, kind, mask := columns[0], columns[1], columns[2], columns[3], columns[4], columns[5]
	var out []importedRow
	for i, row := range sheet.Rows[1:] {
		if emptyRow(row) {
			continue
		}
		x := importedRow{Number: i + 2, Sheet: sheet.Name, Raw: make([]string, len(row))}
		// Keep the source positions of the mapped fields. The encrypted XLSX holds the other cells.
		for _, col := range columns {
			if col < len(row) {
				x.Raw[col] = row[col]
			}
		}
		cardOK := normalizeHeader(valueAt(row, kind)) == "по номеру карты" && nikitaPayoutMask.MatchString(valueAt(row, mask))
		if cardOK {
			x.Mask = valueAt(row, mask)
		} else if mask < len(x.Raw) {
			x.Raw[mask] = ""
		}
		if value := valueAt(row, order); nikitaPayoutID.MatchString(value) {
			x.Order = value
		}
		paid := normalizeHeader(valueAt(row, status)) == "оплачена"
		paidAmount, amountErr := amount(trimExactTrailingZeros(valueAt(row, payout)))
		if paid {
			x.Amount = paidAmount
		}
		bankFee := trimExactTrailingZeros(valueAt(row, fee))
		if bankFee == "0.0" || bankFee == "0,0" {
			bankFee = "0.00"
		}
		var feeErr error
		x.BankFee, feeErr = nonnegative(bankFee)
		switch {
		case !paid:
			x.Error = "payment_not_completed"
		case amountErr != nil:
			x.Error = "invalid_amount"
		case feeErr != nil:
			x.Error = "invalid_bank_fee"
		case x.Order == "":
			x.Error = "invalid_payout_id"
		case !cardOK:
			x.Error = "missing_card_reference"
		}
		out = append(out, x)
	}
	return out, nil
}
