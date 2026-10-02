package main

import (
	"os"
	"reflect"
	"testing"

	"github.com/xuri/excelize/v2"
)

func svetaResponseFixture() spreadsheetSheet {
	return spreadsheetSheet{Name: "Карты к оплате", Rows: [][]string{
		{"Запрос: TEST-01"}, {"Суммы указывает мерчант в ответном реестре"}, {},
		{"№", "НОМЕР КАРТЫ", "ФИО", "НОМЕР ТЕЛЕФОНА", "БАНК"},
		{"1", "0000 0000 0006 1234", "Тестов Алексей Учебович", "+70000000001", "Тестовый банк", " 242,340.00 ₽ "},
		{"2", "0000000000061234", "Тестов Алексей Учебович", "+70000000001", "Тестовый банк", "1 234,56 ₽"},
		{"", "", "", "", "", "  "},
		{"", "", "оплачены успешно", "", "", "243,574.56 ₽"},
		{},
	}}
}

func TestSvetaResponseFormats(t *testing.T) {
	sheet := svetaResponseFixture()
	rows, err := parseMerchantSheets("sveta_cards_xls_v1", []spreadsheetSheet{sheet})
	if err != nil || len(rows) != 2 {
		t.Fatalf("response rows=%d err=%v", len(rows), err)
	}
	for i, want := range []int64{24234000, 123456} {
		if rows[i].Amount != want || rows[i].Error != "" || rows[i].Mask != "0000000000061234" ||
			rows[i].ContactName != "" || rows[i].Number != i+5 || !reflect.DeepEqual(rows[i].Raw, sheet.Rows[i+4]) {
			t.Fatalf("incorrect response mapping at row %d", i)
		}
	}
	// Exercise the XLSX reader as well as the mapping, without a database.
	f := excelize.NewFile()
	defer f.Close()
	f.SetSheetName("Sheet1", sheet.Name)
	for i, row := range sheet.Rows {
		for j, value := range row {
			cell, _ := excelize.CoordinatesToCellName(j+1, i+1)
			if err := f.SetCellStr(sheet.Name, cell, value); err != nil {
				t.Fatal(err)
			}
		}
	}
	data, err := f.WriteToBuffer()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := parseRows("sveta_cards_xls_v1", ".xlsx", data.Bytes())
	if err != nil || !reflect.DeepEqual(parsed, rows) {
		t.Fatal("XLSX response roundtrip failed")
	}
	cell := "F5"
	if err := f.SetCellFormula(sheet.Name, cell, "1+1"); err != nil {
		t.Fatal(err)
	}
	data, err = f.WriteToBuffer()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseRows("sveta_cards_xls_v1", ".xlsx", data.Bytes()); err == nil {
		t.Fatal("formula accepted")
	}
	if err := f.SetCellFormula(sheet.Name, "F5", ""); err != nil {
		t.Fatal(err)
	}
	if err := f.SetRowVisible(sheet.Name, 5, false); err != nil {
		t.Fatal(err)
	}
	if err := f.SetCellStr(sheet.Name, "F8", "1 234,56 ₽"); err != nil {
		t.Fatal(err)
	}
	data, err = f.WriteToBuffer()
	if err != nil {
		t.Fatal(err)
	}
	visible, excluded, err := parseRowsWithExclusions("sveta_cards_xls_v1", ".xlsx", data.Bytes())
	if err != nil || len(visible) != 1 || visible[0].Number != 6 || !reflect.DeepEqual(excluded, []sourceRowExclusion{{Sheet: sheet.Name, Row: 5, Code: "source_row_hidden"}}) {
		t.Fatal("XLSX hidden row diagnostics failed")
	}
}

func TestSvetaResponseValidation(t *testing.T) {
	for _, value := range []string{"", "0", "-1", "bad", "1.0011 ₽", "1 USD", "₽1"} {
		sheet := svetaResponseFixture()
		sheet.Rows[4][5] = value
		rows, err := parseSvetaSheet(sheet)
		if err != nil || len(rows) != 2 || rows[0].Error != "invalid_amount" {
			t.Errorf("amount %q not blocked", value)
		}
	}
	sheet := svetaResponseFixture()
	sheet.Rows[4][1] = ""
	rows, err := parseSvetaSheet(sheet)
	if err != nil || rows[0].Error != "missing_card_reference" {
		t.Fatal("missing card not blocked")
	}
	for _, mutate := range []func(*spreadsheetSheet){
		func(s *spreadsheetSheet) { s.Name = "Other" },
		func(s *spreadsheetSheet) { s.Rows[3][4] = "Другое" },
		func(s *spreadsheetSheet) { s.Rows[3] = append(s.Rows[3], "Другая сумма") },
		func(s *spreadsheetSheet) { s.Rows[3] = append(s.Rows[3], "Сумма", "Сумма") },
	} {
		sheet := svetaResponseFixture()
		mutate(&sheet)
		if _, err := parseSvetaSheet(sheet); err == nil {
			t.Fatal("ambiguous layout accepted")
		}
	}
	sheet = svetaResponseFixture()
	sheet.Rows[3] = append(sheet.Rows[3], "Сумма")
	if _, err := parseSvetaSheet(sheet); err != nil {
		t.Fatal(err)
	}
	sheet.Rows = sheet.Rows[:4]
	if _, err := parseSvetaSheet(sheet); err == nil {
		t.Fatal("empty request accepted")
	}
	for _, value := range []string{"1 ₽", "bad"} {
		sheet := svetaResponseFixture()
		sheet.Rows[7][5] = value
		if _, err := parseSvetaSheet(sheet); err == nil {
			t.Fatal("invalid footer total accepted")
		}
	}
}

func TestSvetaSpacedLegacyCards(t *testing.T) {
	sheet := spreadsheetSheet{Name: "Лист_1", Rows: [][]string{
		{}, {}, {}, {"№ п/п", "Номер вх.", "Сумма", "По номеру карты"},
		{"1", "test-order", "1234.56", "0000\u00a00000\u202f0006 1234"},
		{"Итого", "", "1234.56"},
	}}
	rows, err := parseSvetaSheet(sheet)
	if err != nil || len(rows) != 1 || rows[0].Mask != "0000000000061234" || rows[0].Order != "test-order" || rows[0].Amount != 123456 {
		t.Fatal("spaced legacy card mapping failed")
	}
	if safeRaw(rows[0].Raw)[3] != "[redacted]" {
		t.Fatal("raw card with Unicode separators was not redacted")
	}
}

func TestSvetaNewSourceSamples(t *testing.T) {
	for _, sample := range []struct {
		env, ext string
		count    int
		total    int64
	}{
		{"METALLIST_SAMPLE_SVETA_SEP30", ".xls", 24, 592038700},
		{"METALLIST_SAMPLE_SVETA_OCT01", ".xlsx", 24, 591340600},
	} {
		path := os.Getenv(sample.env)
		if path == "" {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		rows, excluded, err := parseRowsWithExclusions("sveta_cards_xls_v1", sample.ext, data)
		if err != nil || len(rows) != sample.count {
			t.Fatalf("sample count=%d error=%v", len(rows), err)
		}
		var total int64
		for _, row := range rows {
			if row.Error != "" {
				t.Fatalf("sample validation code=%s", row.Error)
			}
			total += row.Amount
		}
		if total != sample.total {
			t.Fatalf("sample total=%d want=%d", total, sample.total)
		}
		if sample.ext == ".xls" {
			if len(excluded) != 9 {
				t.Fatal("source hidden row diagnostics missing")
			}
			for i, x := range excluded {
				if x.Row != 21+i || x.Code != "source_row_hidden" {
					t.Fatal("wrong hidden source row")
				}
			}
		}
	}
}
