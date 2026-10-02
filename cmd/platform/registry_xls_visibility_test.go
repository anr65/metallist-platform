package main

import (
	"encoding/binary"
	"reflect"
	"testing"
)

func TestBIFFHiddenRows(t *testing.T) {
	rec := func(kind uint16, data []byte) []byte {
		out := make([]byte, 4+len(data))
		binary.LittleEndian.PutUint16(out, kind)
		binary.LittleEndian.PutUint16(out[2:], uint16(len(data)))
		copy(out[4:], data)
		return out
	}
	bound := make([]byte, 8)
	// Global BOF (4), BOUNDSHEET (12), EOF (4): worksheet starts at byte 20.
	binary.LittleEndian.PutUint32(bound, 20)
	stream := rec(0x0809, nil)
	stream = append(stream, rec(0x85, bound)...)
	stream = append(stream, rec(0xa, nil)...)
	stream = append(stream, rec(0x0809, nil)...)
	for _, hidden := range []bool{false, true, false} {
		row := make([]byte, 16)
		binary.LittleEndian.PutUint16(row, uint16((len(stream)-24)/20))
		binary.LittleEndian.PutUint16(row[6:], 300)
		if hidden {
			binary.LittleEndian.PutUint16(row[12:], 0x20)
		}
		stream = append(stream, rec(0x208, row)...)
	}
	stream = append(stream, rec(0xa, nil)...)
	got, err := biffHiddenRows(stream)
	if err != nil || !reflect.DeepEqual(got, []map[int]bool{{2: true}}) {
		t.Fatalf("hidden ROW metadata=%v err=%v", got, err)
	}
	for _, bad := range [][]byte{stream[:len(stream)-1], {0x85, 0, 8, 0}, stream[:25]} {
		if _, err := biffHiddenRows(bad); err == nil {
			t.Fatal("truncated BIFF accepted")
		}
	}
}

func TestSvetaVisibleRowsAndTotal(t *testing.T) {
	sheet := spreadsheetSheet{Name: "Лист_1", HiddenRows: map[int]bool{3: true}, Rows: [][]string{
		{"№ п/п", "Номер вх.", "Сумма", "По номеру карты"},
		{"1", "one", "100", "000000******1234"},
		{"2", "hidden", "7.33", "000000******5678"},
		{"3", "three", "200", "000000******1234"},
		{"Итого", "", "300"},
	}}
	rows, err := parseSvetaSheet(sheet)
	if err != nil || len(rows) != 2 || rows[0].Number != 2 || rows[1].Number != 4 || rows[1].Amount != 20000 {
		t.Fatal("visible rows not preserved")
	}
	sheet.HiddenRows = nil
	if _, err := parseSvetaSheet(sheet); err == nil {
		t.Fatal("unreconciled total accepted")
	}
}
