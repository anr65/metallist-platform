package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http/httptest"
	"testing"

	"github.com/xuri/excelize/v2"
)

func nikitaTestWorkbook(t *testing.T, rows ...[]any) []byte {
	t.Helper()
	file := excelize.NewFile()
	defer file.Close()
	head := []any{"Реквизиты вывода", "Выплата", "Id выплаты", "Тип реквизитов", "Статус выплаты", "Комиссия банка", "Фамилия", "Заявка"}
	for i, row := range append([][]any{head}, rows...) {
		if err := file.SetSheetRow("Sheet1", fmt.Sprintf("A%d", i+1), &row); err != nil {
			t.Fatal(err)
		}
	}
	style, err := file.NewStyle(&excelize.Style{NumFmt: 2})
	if err != nil {
		t.Fatal(err)
	}
	if err = file.SetCellStyle("Sheet1", "B2", fmt.Sprintf("B%d", len(rows)+1), style); err != nil {
		t.Fatal(err)
	}
	blob, err := file.WriteToBuffer()
	if err != nil {
		t.Fatal(err)
	}
	return blob.Bytes()
}

func TestNikitaPayoutXLSX(t *testing.T) {
	data := nikitaTestWorkbook(t,
		[]any{"000000******1234", 81.25, 101, "По номеру карты", " оплачена ", 1.3, "Тестов", 99},
		[]any{},
		[]any{"000000******1234", 50, 102, "По номеру карты", "в обработке", 0},
		[]any{"000000******1234", 1.001, 103, "По номеру карты", "оплачена", 0},
		[]any{"000000******1234", 20, 104, "По номеру телефона", "оплачена", 0},
		[]any{"000000******1234", 20, nil, "По номеру карты", "оплачена", 0},
	)
	rows, err := readAndParseRows(nikitaPayoutsParser, ".xlsx", data)
	if err != nil || len(rows) != 5 {
		t.Fatalf("rows=%d: %v", len(rows), err)
	}
	first := rows[0]
	if first.Number != 2 || first.Sheet != "Sheet1" || first.Mask != "000000******1234" || first.Order != "101" || first.Amount != 8125 || first.BankFee != 130 || first.Raw[6] != "" || first.Raw[7] != "" || first.Error != "" {
		t.Fatal("incorrect payout mapping", first)
	}
	if rows[1].Number != 4 || rows[1].Error != "payment_not_completed" || rows[1].Amount != 0 || rows[2].Error != "invalid_amount" || rows[3].Error != "missing_card_reference" || rows[4].Error != "invalid_payout_id" {
		t.Fatal("incorrect payout validation", rows[1:])
	}
	if _, err = readAndParseRows(nikitaPayoutsParser, ".csv", data); err == nil {
		t.Fatal("accepted a different extension")
	}
	if _, err = parseNikitaSheet(spreadsheetSheet{Rows: [][]string{{"Выплата", " ВЫПЛАТА "}, {"100", "200"}}}); err == nil {
		t.Fatal("accepted duplicate headers")
	}
}

func TestNikitaRegistryUploadMatchesFullMask(t *testing.T) {
	a := testApp(t)
	u, merchant, _, card := fixtures(t, a)
	if _, err := a.db.Exec(`INSERT INTO registry_parser_types(code,name,version,accepted_extensions)
		VALUES ($1,'Никита: выгрузка выплат',1,ARRAY['.xlsx']) ON CONFLICT (code) DO NOTHING`, nikitaPayoutsParser); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec("UPDATE merchant_import_profiles SET parser_code=$1 WHERE merchant_id=$2", nikitaPayoutsParser, merchant); err != nil {
		t.Fatal(err)
	}
	data := nikitaTestWorkbook(t,
		[]any{"000000******1234", 81.25, 101, "По номеру карты", "оплачена", 1.30},
		[]any{"111111******1234", 40, 102, "По номеру карты", "оплачена", 0},
	)
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	_ = form.WriteField("merchant_id", merchant)
	_ = form.WriteField("payment_date", testPaymentDate())
	file, err := form.CreateFormFile("file", "payouts.xlsx")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = file.Write(data); err != nil {
		t.Fatal(err)
	}
	if err = form.Close(); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/api/registry/upload", &body)
	r.Header.Set("Content-Type", form.FormDataContentType())
	w := httptest.NewRecorder()
	a.upload(w, r, u)
	var out M
	if err = json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != 201 || out["invalid_rows"] != float64(1) {
		t.Fatal("upload failed", w.Code, out, err)
	}
	var parser, status, matchedCard, unmatchedError string
	var version, entries int
	err = a.db.QueryRow(`SELECT s.parser_code,s.parser_version,r.status,ok.card_id::text,bad.error_code,(SELECT count(*) FROM journal_entries)
		FROM registries r JOIN source_documents s ON s.id=r.source_id
		JOIN registry_rows ok ON ok.registry_id=r.id AND ok.row_no=2
		JOIN registry_rows bad ON bad.registry_id=r.id AND bad.row_no=3 AND bad.card_id IS NULL
		WHERE r.id=$1`, out["id"]).Scan(&parser, &version, &status, &matchedCard, &unmatchedError, &entries)
	if err != nil || parser != nikitaPayoutsParser || version != 1 || status != "preview" || matchedCard != card || unmatchedError != "card_not_unique_or_unknown" || entries != 0 {
		t.Fatal("incorrect registry preview", parser, version, status, matchedCard, unmatchedError, entries, err)
	}
}
