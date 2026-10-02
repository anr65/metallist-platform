package main

import (
	"encoding/binary"
	"errors"
	"io"

	"github.com/richardlehane/mscfb"
)

// xlsReader ignores BIFF ROW records, including the fDyZero (hidden) flag.
// Keep that metadata separately so importers can apply an explicit source policy.
func legacyXLSHiddenRows(doc *mscfb.Reader) ([]map[int]bool, error) {
	for _, entry := range doc.File {
		if entry.Name != "Workbook" && entry.Name != "Book" {
			continue
		}
		stream, err := io.ReadAll(io.LimitReader(entry, (64<<20)+1))
		if err != nil || len(stream) > 64<<20 {
			return nil, errors.New("не удалось прочитать метаданные XLS")
		}
		return biffHiddenRows(stream)
	}
	return nil, errors.New("XLS без потока Workbook")
}

func biffHiddenRows(stream []byte) ([]map[int]bool, error) {
	var offsets []int
	for p := 0; p < len(stream); {
		kind, data, next, err := biffRecord(stream, p)
		if err != nil {
			return nil, err
		}
		if kind == 0x0085 {
			if len(data) < 8 {
				return nil, errors.New("повреждённые метаданные листа XLS")
			}
			offsets = append(offsets, int(binary.LittleEndian.Uint32(data[:4])))
		}
		p = next
		if kind == 0x000a {
			break
		}
	}
	out := make([]map[int]bool, len(offsets))
	for i, offset := range offsets {
		out[i] = map[int]bool{}
		for p := offset; ; {
			kind, data, next, err := biffRecord(stream, p)
			if err != nil {
				return nil, err
			}
			if kind == 0x0208 {
				if len(data) < 16 {
					return nil, errors.New("повреждённые метаданные строки XLS")
				}
				if binary.LittleEndian.Uint16(data[12:14])&0x0020 != 0 {
					out[i][int(binary.LittleEndian.Uint16(data[:2]))+1] = true
				}
			}
			p = next
			if kind == 0x000a {
				break
			}
		}
	}
	return out, nil
}

func biffRecord(stream []byte, p int) (uint16, []byte, int, error) {
	if p < 0 || p > len(stream)-4 {
		return 0, nil, 0, errors.New("повреждённые записи XLS")
	}
	n := int(binary.LittleEndian.Uint16(stream[p+2 : p+4]))
	end := p + 4 + n
	if end > len(stream) {
		return 0, nil, 0, errors.New("обрезанная запись XLS")
	}
	return binary.LittleEndian.Uint16(stream[p : p+2]), stream[p+4 : end], end, nil
}
