package main

import (
	"archive/zip"
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"

	"github.com/richardlehane/mscfb"
	"github.com/shakinm/xlsReader/xls"
	"github.com/xuri/excelize/v2"
	"golang.org/x/text/encoding/charmap"
)

type spreadsheetSheet struct {
	Name       string
	Rows       [][]string
	HiddenRows map[int]bool
}

var avangardCard = regexp.MustCompile(`(?i)карта\s*:\s*\**([0-9]{4})(?:[^0-9]|$)`)

type sourceRowExclusion struct {
	Sheet string `json:"sheet"`
	Row   int    `json:"row"`
	Code  string `json:"code"`
}

func parseRows(parser, extension string, data []byte) ([]importedRow, error) {
	rows, _, err := parseRowsWithExclusions(parser, extension, data)
	return rows, err
}

func parseRowsWithExclusions(parser, extension string, data []byte) ([]importedRow, []sourceRowExclusion, error) {
	if parser == "aliten_bank_csv_v1" {
		rows, err := parseAlitenCSV(data)
		return rows, nil, err
	}
	sheets, err := readRegistrySheets(parser, extension, data)
	if err != nil {
		return nil, nil, err
	}
	rows, err := parseMerchantSheets(parser, sheets)
	if err != nil {
		return nil, nil, err
	}
	var excluded []sourceRowExclusion
	if parser == "sveta_cards_xls_v1" {
		for _, sheet := range sheets {
			for i, row := range sheet.Rows {
				if sheet.HiddenRows[i+1] && !emptyRow(row) {
					excluded = append(excluded, sourceRowExclusion{Sheet: sheet.Name, Row: i + 1, Code: "source_row_hidden"})
				}
			}
		}
	}
	return rows, excluded, nil
}

func readRegistrySheets(parser, extension string, data []byte) ([]spreadsheetSheet, error) {
	switch parser {
	case "generic_xlsx_v1", "tolya_operations_xlsx_v1", "katya_payouts_xlsx_v1":
		if extension != ".xlsx" {
			return nil, errors.New("для выбранного мерчанта нужен файл XLSX")
		}
		return readXLSX(data)
	case "sveta_cards_xls_v1":
		if extension == ".xls" {
			return readLegacyXLS(data)
		}
		if extension == ".xlsx" {
			return readXLSX(data)
		}
		return nil, errors.New("для Светы нужен файл XLS или XLSX")
	case "narkoman_avangard_xls_v1":
		if extension != ".xls" {
			return nil, errors.New("для выбранного мерчанта нужен файл XLS")
		}
		return readLegacyXLS(data)
	default:
		return nil, errors.New("тип разбора реестра не поддержан")
	}
}

func parseAlitenCSV(data []byte) ([]importedRow, error) {
	reader := csv.NewReader(charmap.Windows1251.NewDecoder().Reader(bytes.NewReader(data)))
	reader.Comma = ';'
	reader.FieldsPerRecord = -1
	all, e := reader.ReadAll()
	if e != nil {
		return nil, e
	}
	if len(all) < 3 {
		return nil, errors.New("CSV без строк")
	}
	if len(all) > 10002 {
		return nil, errors.New("слишком много строк")
	}
	indices := headers(all[1])
	if e := rejectSensitiveHeaders([]spreadsheetSheet{{Rows: [][]string{all[1]}}}); e != nil {
		return nil, e
	}
	for _, name := range []string{"Маскированный номер карты", "Сумма операции", "Комиссия Банка", "К перечислению"} {
		if _, ok := indices[normalizeHeader(name)]; !ok {
			return nil, fmt.Errorf("нет поля %s", name)
		}
	}
	out := []importedRow{}
	for i, row := range all[2:] {
		if emptyRow(row) {
			continue
		}
		x := importedRow{Number: i + 3, Raw: row, Mask: field(row, indices, "Маскированный номер карты"), Order: field(row, indices, "Номер заказа"), RRN: field(row, indices, "RRN")}
		var e1, e2, e3 error
		x.Amount, e1 = amount(field(row, indices, "Сумма операции"))
		x.BankFee, e2 = nonnegative(field(row, indices, "Комиссия Банка"))
		x.Transfer, e3 = amount(field(row, indices, "К перечислению"))
		if e1 != nil || e2 != nil || e3 != nil || x.Transfer != x.Amount+x.BankFee {
			x.Error = "invalid_amount_or_bank_total"
		}
		out = append(out, x)
	}
	return out, nil
}

func readXLSX(data []byte) ([]spreadsheetSheet, error) {
	z, e := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if e != nil {
		return nil, errors.New("файл не является XLSX")
	}
	var size uint64
	for _, entry := range z.File {
		size += entry.UncompressedSize64
		if size > 64<<20 || entry.UncompressedSize64 > 20<<20 {
			return nil, errors.New("слишком большой XLSX после распаковки")
		}
		name := strings.ToLower(entry.Name)
		if strings.Contains(name, "vba") || strings.Contains(name, "macros") {
			return nil, errors.New("макросы запрещены")
		}
	}
	f, e := excelize.OpenReader(bytes.NewReader(data))
	if e != nil {
		return nil, e
	}
	defer f.Close()
	list := f.GetSheetList()
	if len(list) == 0 {
		return nil, errors.New("XLSX без листов")
	}
	out := make([]spreadsheetSheet, 0, len(list))
	for _, name := range list {
		raw, e := f.GetRows(name, excelize.Options{RawCellValue: false})
		if e != nil {
			return nil, e
		}
		if len(raw) > 10001 {
			return nil, errors.New("слишком много строк")
		}
		for rowNo, row := range raw {
			for colNo := range row {
				cell, _ := excelize.CoordinatesToCellName(colNo+1, rowNo+1)
				formula, e := f.GetCellFormula(name, cell)
				if e != nil {
					return nil, e
				}
				if formula != "" {
					return nil, errors.New("формулы в реестре запрещены")
				}
			}
		}
		hidden := map[int]bool{}
		for i := range raw {
			visible, err := f.GetRowVisible(name, i+1)
			if err != nil {
				return nil, err
			}
			if !visible {
				hidden[i+1] = true
			}
		}
		out = append(out, spreadsheetSheet{Name: name, Rows: raw, HiddenRows: hidden})
	}
	if e := rejectSensitiveHeaders(out); e != nil {
		return nil, e
	}
	return out, nil
}

func readLegacyXLS(data []byte) ([]spreadsheetSheet, error) {
	if len(data) < 8 || !bytes.Equal(data[:8], []byte{0xd0, 0xcf, 0x11, 0xe0, 0xa1, 0xb1, 0x1a, 0xe1}) {
		return nil, errors.New("файл не является XLS")
	}
	doc, e := mscfb.New(bytes.NewReader(data))
	if e != nil {
		return nil, errors.New("повреждённый XLS")
	}
	for _, entry := range doc.File {
		parts := append(append([]string(nil), entry.Path...), entry.Name)
		name := strings.ToLower(strings.Join(parts, "/"))
		if strings.Contains(name, "vba") || strings.Contains(name, "macros") || strings.Contains(name, "_vba_project") {
			return nil, errors.New("макросы в XLS запрещены")
		}
	}
	hiddenRows, e := legacyXLSHiddenRows(doc)
	if e != nil {
		return nil, e
	}
	book, e := xls.OpenReader(bytes.NewReader(data))
	if e != nil {
		return nil, errors.New("не удалось прочитать XLS")
	}
	if book.GetNumberSheets() == 0 {
		return nil, errors.New("XLS без листов")
	}
	if len(hiddenRows) != book.GetNumberSheets() {
		return nil, errors.New("не совпадает число листов XLS")
	}
	out := make([]spreadsheetSheet, 0, book.GetNumberSheets())
	for i := 0; i < book.GetNumberSheets(); i++ {
		sheet, e := book.GetSheet(i)
		if e != nil {
			return nil, errors.New("не удалось прочитать лист XLS")
		}
		if sheet.GetNumberRows() > 10001 {
			return nil, errors.New("слишком много строк")
		}
		rows := make([][]string, 0, sheet.GetNumberRows())
		for _, row := range sheet.GetRows() {
			cells := row.GetCols()
			values := make([]string, len(cells))
			for col, cell := range cells {
				if strings.Contains(strings.ToLower(cell.GetType()), "formula") {
					return nil, errors.New("формулы в реестре запрещены")
				}
				values[col] = cell.GetString()
			}
			rows = append(rows, values)
		}
		out = append(out, spreadsheetSheet{Name: sheet.GetName(), Rows: rows, HiddenRows: hiddenRows[i]})
	}
	if e := rejectSensitiveHeaders(out); e != nil {
		return nil, e
	}
	return out, nil
}

func parseMerchantSheets(parser string, sheets []spreadsheetSheet) ([]importedRow, error) {
	switch parser {
	case "generic_xlsx_v1":
		if len(sheets) != 1 {
			return nil, errors.New("стандартный XLSX должен содержать один лист")
		}
		return parseGenericSheet(sheets[0])
	case "tolya_operations_xlsx_v1":
		sheet, e := namedSheet(sheets, "Операции")
		if e != nil {
			return nil, e
		}
		return parseTolyaSheet(sheet)
	case "katya_payouts_xlsx_v1":
		if len(sheets) != 1 {
			return nil, errors.New("выгрузка Кати должна содержать один лист")
		}
		return parseKatyaSheet(sheets[0])
	case "sveta_cards_xls_v1":
		if len(sheets) != 1 {
			return nil, errors.New("выгрузка Светы должна содержать один лист")
		}
		return parseSvetaSheet(sheets[0])
	case "narkoman_avangard_xls_v1":
		if len(sheets) != 1 {
			return nil, errors.New("выписка Авангарда должна содержать один лист")
		}
		return parseNarkomanSheet(sheets[0])
	default:
		return nil, errors.New("тип разбора не поддержан")
	}
}

func parseGenericSheet(sheet spreadsheetSheet) ([]importedRow, error) {
	if len(sheet.Rows) < 2 {
		return nil, errors.New("XLSX без строк")
	}
	head := headers(sheet.Rows[0])
	ci, cok := firstHeader(head, "карта", "card", "маска карты")
	ai, aok := firstHeader(head, "сумма", "amount", "сумма пополнения")
	if !cok || !aok {
		return nil, errors.New("нужны колонки Карта и Сумма")
	}
	return parseDirectRows(sheet, 1, ci, ai, -1, -1, ""), nil
}

func parseSvetaSheet(sheet spreadsheetSheet) ([]importedRow, error) {
	if len(sheet.Rows) < 2 {
		return nil, errors.New("выгрузка Светы без строк")
	}
	headerRow := -1
	var head map[string]int
	for i, row := range sheet.Rows {
		if sheet.HiddenRows[i+1] {
			continue
		}
		if svetaRequestHeader(sheet.Name, row) {
			return parseSvetaRequestSheet(sheet, i)
		}
		candidate := headers(row)
		_, amountOK := firstHeader(candidate, "сумма")
		_, orderOK := firstHeader(candidate, "номер вх.")
		_, cardOK := firstHeader(candidate, "по номеру карты")
		_, nameOK := firstHeader(candidate, "информация")
		if amountOK && orderOK && (cardOK || nameOK) {
			headerRow, head = i, candidate
			break
		}
	}
	if headerRow < 0 {
		return nil, errors.New("структура выгрузки Светы не распознана")
	}
	card, cok := firstHeader(head, "по номеру карты")
	name, nok := firstHeader(head, "информация")
	amountCol, aok := firstHeader(head, "сумма")
	order, ook := firstHeader(head, "номер вх.")
	if (!cok && !nok) || !aok || !ook {
		return nil, errors.New("структура выгрузки Светы не распознана")
	}
	out := []importedRow{}
	var declaredTotal, total int64
	hasTotal, invalidAmount := false, false
	for rowNo, row := range sheet.Rows[headerRow+1:] {
		if sheet.HiddenRows[rowNo+headerRow+2] {
			continue
		}
		if emptyRow(row) {
			continue
		}
		if normalizeHeader(valueAt(row, 0)) == "итого" {
			if valueAt(row, amountCol) != "" {
				var e error
				declaredTotal, e = spreadsheetAmount(valueAt(row, amountCol))
				if e != nil {
					return nil, errors.New("неверная итоговая сумма в выгрузке Светы")
				}
				hasTotal = true
			}
			break
		}
		x := importedRow{Number: rowNo + headerRow + 2, Sheet: sheet.Name, Raw: row, Order: valueAt(row, order)}
		if cok {
			x.Mask = normalizeSvetaCard(valueAt(row, card))
		} else if nok {
			x.ContactName = valueAt(row, name)
		}
		if x.Mask == "" && x.ContactName == "" {
			x.Error = "missing_card_or_contact_reference"
		}
		if value, e := spreadsheetAmount(valueAt(row, amountCol)); e == nil {
			x.Amount = value
			if value > math.MaxInt64-total {
				return nil, errors.New("сумма реестра слишком велика")
			}
			total += value
		} else {
			x.Error = "invalid_amount"
			invalidAmount = true
		}
		out = append(out, x)
	}
	if len(out) == 0 {
		return nil, errors.New("выгрузка Светы без строк пополнений")
	}
	if hasTotal && !invalidAmount && total != declaredTotal {
		return nil, errors.New("итоговая сумма в выгрузке Светы не совпадает с суммой видимых строк")
	}
	return out, nil
}

// The merchant fills column F or G of the exported request, sometimes without a header.
// Only the complete known layout permits this positional amount mapping.
func svetaRequestHeader(sheetName string, row []string) bool {
	if sheetName != "Карты к оплате" || len(row) < 5 || len(row) > 7 {
		return false
	}
	for i, title := range []string{"№", "НОМЕР КАРТЫ", "ФИО", "НОМЕР ТЕЛЕФОНА", "БАНК"} {
		if normalizeHeader(valueAt(row, i)) != normalizeHeader(title) {
			return false
		}
	}
	if len(row) == 7 {
		return valueAt(row, 5) == "" && (valueAt(row, 6) == "" || normalizeHeader(valueAt(row, 6)) == "сумма")
	}
	return valueAt(row, 5) == "" || normalizeHeader(valueAt(row, 5)) == "сумма"
}

// Select one amount column for the entire visible response, including its footer.
// Never guess between two populated columns or ignore extra source data.
func svetaRequestAmountColumn(sheet spreadsheetSheet, headerRow int) (int, error) {
	filledF, filledG := false, false
	for i, row := range sheet.Rows[headerRow:] {
		if sheet.HiddenRows[headerRow+i+1] {
			continue
		}
		if len(row) > 7 && !emptyRow(row[7:]) {
			return 0, errors.New("неподдержанные колонки в ответе Светы")
		}
		filledF = filledF || valueAt(row, 5) != ""
		filledG = filledG || valueAt(row, 6) != ""
	}
	if filledF && filledG {
		return 0, errors.New("суммы в ответе Светы должны быть в одной колонке: F или G")
	}
	if filledG {
		return 6, nil
	}
	return 5, nil
}

func normalizeSvetaCard(value string) string {
	return strings.Join(strings.Fields(value), "")
}

func parseSvetaRequestSheet(sheet spreadsheetSheet, headerRow int) ([]importedRow, error) {
	amountCol, err := svetaRequestAmountColumn(sheet, headerRow)
	if err != nil {
		return nil, err
	}
	out := []importedRow{}
	var total, declaredTotal int64
	hasTotal, invalidAmount := false, false
	for offset, row := range sheet.Rows[headerRow+1:] {
		if sheet.HiddenRows[offset+headerRow+2] {
			continue
		}
		if emptyRow(row) {
			continue
		}
		// The supplied response has a status/total footer, not another payment.
		if valueAt(row, 0) == "" && valueAt(row, 1) == "" &&
			normalizeHeader(valueAt(row, 2)) == "оплачены успешно" &&
			valueAt(row, 3) == "" && valueAt(row, 4) == "" {
			if hasTotal {
				return nil, errors.New("в ответе Светы несколько итоговых строк")
			}
			var e error
			declaredTotal, e = svetaResponseAmount(valueAt(row, amountCol))
			if e != nil {
				return nil, errors.New("неверная итоговая сумма в ответе Светы")
			}
			hasTotal = true
			continue
		}
		x := importedRow{Number: headerRow + offset + 2, Sheet: sheet.Name, Raw: row,
			Mask: normalizeSvetaCard(valueAt(row, 1))}
		if x.Mask == "" {
			x.Error = "missing_card_reference"
		}
		// Ruble suffix is accepted only for this documented response format.
		if v, e := svetaResponseAmount(valueAt(row, amountCol)); e == nil {
			x.Amount = v
			if v > math.MaxInt64-total {
				return nil, errors.New("сумма реестра слишком велика")
			}
			total += v
		} else {
			x.Error = "invalid_amount"
			invalidAmount = true
		}
		out = append(out, x)
	}
	if len(out) == 0 {
		return nil, errors.New("выгрузка Светы без строк пополнений")
	}
	if hasTotal && !invalidAmount && total != declaredTotal {
		return nil, errors.New("итоговая сумма в ответе Светы не совпадает с суммой строк")
	}
	return out, nil
}

func svetaResponseAmount(value string) (int64, error) {
	return spreadsheetAmount(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(value), "₽")))
}

func parseKatyaSheet(sheet spreadsheetSheet) ([]importedRow, error) {
	if len(sheet.Rows) < 2 {
		return nil, errors.New("выгрузка Кати без строк")
	}
	head := headers(sheet.Rows[0])
	card, cok := firstHeader(head, "реквизиты вывода")
	amountCol, aok := firstHeader(head, "выплата")
	order, ook := firstHeader(head, "id выплаты")
	status, sok := firstHeader(head, "статус выплаты")
	fee, fok := firstHeader(head, "комиссия банка")
	if !cok || !aok || !ook || !sok || !fok {
		return nil, errors.New("структура выгрузки Кати не распознана")
	}
	out := parseDirectRows(sheet, 1, card, amountCol, order, fee, "")
	for i := range out {
		row := sheet.Rows[out[i].Number-1]
		if status >= len(row) || normalizeHeader(row[status]) != "оплачена" {
			out[i].Error = "payment_not_completed"
		}
	}
	return out, nil
}

func parseTolyaSheet(sheet spreadsheetSheet) ([]importedRow, error) {
	if len(sheet.Rows) < 2 {
		return nil, errors.New("выгрузка Толи без строк")
	}
	head := headers(sheet.Rows[0])
	amountCol, aok := firstHeader(head, "цена")
	order, ook := firstHeader(head, "id")
	status, sok := firstHeader(head, "статус")
	if !aok || !ook || !sok {
		return nil, errors.New("структура выгрузки Толи не распознана")
	}
	out := []importedRow{}
	for rowNo, row := range sheet.Rows[1:] {
		if emptyRow(row) {
			continue
		}
		x := importedRow{Number: rowNo + 2, Sheet: sheet.Name, Raw: row, Order: valueAt(row, order), Error: "missing_card_reference"}
		if normalizeHeader(valueAt(row, status)) != "выполнен" {
			x.Error = "operation_not_completed"
		}
		if v, e := spreadsheetAmount(valueAt(row, amountCol)); e == nil {
			x.Amount = v
		} else {
			x.Error = "invalid_amount"
		}
		out = append(out, x)
	}
	return out, nil
}

func parseNarkomanSheet(sheet spreadsheetSheet) ([]importedRow, error) {
	if len(sheet.Rows) < 9 {
		return nil, errors.New("выписка Авангарда без строк")
	}
	if normalizeHeader(valueAt(sheet.Rows[6], 2)) != "дата док-та" || normalizeHeader(valueAt(sheet.Rows[6], 5)) != "номер док-та" || normalizeHeader(valueAt(sheet.Rows[6], 20)) != "назначение платежа" || normalizeHeader(valueAt(sheet.Rows[6], 17)) != "обороты" || normalizeHeader(valueAt(sheet.Rows[7], 17)) != "по дебету" {
		return nil, errors.New("структура выписки Авангарда не распознана")
	}
	out := []importedRow{}
	var total, declaredTotal int64
	hasTotal, invalidAmount := false, false
	for offset, row := range sheet.Rows[8:] {
		if emptyRow(row) {
			continue
		}
		if normalizeHeader(valueAt(row, 2)) == "итого:" {
			if hasTotal {
				return nil, errors.New("в выписке Авангарда несколько итоговых строк")
			}
			var err error
			declaredTotal, err = spreadsheetNonnegative(valueAt(row, 17))
			if err != nil {
				return nil, errors.New("неверная итоговая сумма в выписке Авангарда")
			}
			hasTotal = true
			continue
		}
		if hasTotal {
			return nil, errors.New("неподдержанные строки после итога выписки Авангарда")
		}
		x := importedRow{Number: offset + 9, Sheet: sheet.Name, Raw: row, Order: valueAt(row, 5)}
		// Avangard appends its own card/location on a second line. The recipient
		// reference belongs to the first line of the payment purpose.
		purpose, _, _ := strings.Cut(valueAt(row, 20), "\n")
		matches := avangardCard.FindAllStringSubmatch(purpose, -1)
		if len(matches) == 0 {
			x.Error = "missing_card_reference"
		} else {
			x.Mask = "****" + matches[0][1]
			for _, match := range matches[1:] {
				if match[1] != matches[0][1] {
					x.Mask = ""
					x.Error = "ambiguous_card_reference"
					break
				}
			}
		}
		if v, err := spreadsheetAmount(valueAt(row, 17)); err == nil {
			x.Amount = v
			if v > math.MaxInt64-total {
				return nil, errors.New("сумма реестра слишком велика")
			}
			total += v
		} else {
			x.Error = "invalid_amount"
			invalidAmount = true
		}
		if credit := valueAt(row, 19); credit != "" {
			if v, err := spreadsheetNonnegative(credit); err != nil || v != 0 {
				x.Error = "unsupported_credit_operation"
			}
		}
		out = append(out, x)
	}
	if len(out) == 0 {
		return nil, errors.New("выписка Авангарда без строк пополнений")
	}
	if !hasTotal {
		return nil, errors.New("в выписке Авангарда нет итоговой суммы")
	}
	if !invalidAmount && total != declaredTotal {
		return nil, errors.New("итоговая сумма выписки Авангарда не совпадает с суммой строк")
	}
	return out, nil
}

func parseDirectRows(sheet spreadsheetSheet, start, cardCol, amountCol, orderCol, feeCol int, stop string) []importedRow {
	out := []importedRow{}
	for rowNo, row := range sheet.Rows[start:] {
		if emptyRow(row) {
			continue
		}
		if stop != "" && normalizeHeader(valueAt(row, 0)) == stop {
			break
		}
		x := importedRow{Number: rowNo + start + 1, Sheet: sheet.Name, Raw: row, Mask: valueAt(row, cardCol)}
		if orderCol >= 0 {
			x.Order = valueAt(row, orderCol)
		}
		if strings.TrimSpace(x.Mask) == "" {
			x.Error = "missing_card_reference"
		}
		if v, e := spreadsheetAmount(valueAt(row, amountCol)); e == nil {
			x.Amount = v
		} else {
			x.Error = "invalid_amount"
		}
		if feeCol >= 0 {
			if v, e := spreadsheetNonnegative(valueAt(row, feeCol)); e == nil {
				x.BankFee = v
			} else {
				x.Error = "invalid_bank_fee"
			}
		}
		out = append(out, x)
	}
	return out
}

func namedSheet(sheets []spreadsheetSheet, name string) (spreadsheetSheet, error) {
	for _, sheet := range sheets {
		if normalizeHeader(sheet.Name) == normalizeHeader(name) {
			return sheet, nil
		}
	}
	return spreadsheetSheet{}, fmt.Errorf("нет листа %s", name)
}

func headers(row []string) map[string]int {
	out := map[string]int{}
	for i, name := range row {
		key := normalizeHeader(name)
		if key != "" {
			out[key] = i
		}
	}
	return out
}

func firstHeader(head map[string]int, names ...string) (int, bool) {
	for _, name := range names {
		if i, ok := head[normalizeHeader(name)]; ok {
			return i, true
		}
	}
	return -1, false
}

func field(row []string, head map[string]int, name string) string {
	i, ok := head[normalizeHeader(name)]
	if !ok {
		return ""
	}
	return valueAt(row, i)
}

func valueAt(row []string, index int) string {
	if index < 0 || index >= len(row) {
		return ""
	}
	return strings.TrimSpace(row[index])
}

func emptyRow(row []string) bool {
	for _, value := range row {
		if strings.TrimSpace(value) != "" {
			return false
		}
	}
	return true
}

func normalizeHeader(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(value)), " "))
}

func spreadsheetAmount(value string) (int64, error) {
	return amount(trimExactTrailingZeros(normalizeSpreadsheetNumber(value)))
}

func spreadsheetNonnegative(value string) (int64, error) {
	return nonnegative(trimExactTrailingZeros(normalizeSpreadsheetNumber(value)))
}

func normalizeSpreadsheetNumber(value string) string {
	s := strings.NewReplacer(" ", "", "\u00a0", "", "\u202f", "", "'", "").Replace(strings.TrimSpace(value))
	comma, dot := strings.LastIndex(s, ","), strings.LastIndex(s, ".")
	if comma >= 0 && dot >= 0 {
		if comma > dot {
			s = strings.ReplaceAll(s, ".", "")
		} else {
			s = strings.ReplaceAll(s, ",", "")
		}
	} else if groupedSpreadsheetNumber.MatchString(s) {
		s = strings.NewReplacer(",", "", ".", "").Replace(s)
	}
	return s
}

var groupedSpreadsheetNumber = regexp.MustCompile(`^[0-9]{1,3}(?:[,.][0-9]{3})+$`)

func trimExactTrailingZeros(value string) string {
	s := strings.TrimSpace(value)
	separator := strings.LastIndexAny(s, ".,")
	if separator < 0 || len(s)-separator-1 <= 2 {
		return s
	}
	fraction := s[separator+1:]
	for len(fraction) > 2 && strings.HasSuffix(fraction, "0") {
		fraction = strings.TrimSuffix(fraction, "0")
	}
	return s[:separator+1] + fraction
}

func rejectSensitiveHeaders(sheets []spreadsheetSheet) error {
	for _, sheet := range sheets {
		for _, row := range sheet.Rows {
			for _, value := range row {
				lower := normalizeHeader(value)
				if lower == "pin" || lower == "пин" || lower == "cvv" || strings.Contains(lower, "pin-код") || strings.Contains(lower, "пин-код") {
					return errors.New("колонки PIN/CVV запрещены")
				}
			}
		}
	}
	return nil
}

func readAndParseRows(parser, extension string, data []byte) ([]importedRow, error) {
	rows, _, e := readAndParseRowsWithExclusions(parser, extension, data)
	return rows, e
}

func readAndParseRowsWithExclusions(parser, extension string, data []byte) ([]importedRow, []sourceRowExclusion, error) {
	rows, excluded, err := parseRowsWithExclusions(parser, extension, data)
	if err != nil {
		return nil, nil, err
	}
	if len(rows) == 0 {
		return nil, nil, errors.New("в файле нет строк операций")
	}
	return rows, excluded, nil
}
