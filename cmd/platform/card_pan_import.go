package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

type panImportItem struct {
	cardID string
	mask   string
	pan    string
	exists bool
}

// importCardPANs accepts one full card number per line from a protected input stream.
// It validates the entire batch before writing and can safely resume after a failure.
func (a *App) importCardPANs(input io.Reader) (int, int, error) {
	scanner := bufio.NewScanner(input)
	items := []panImportItem{}
	seen := map[string]bool{}
	for scanner.Scan() {
		pan := strings.Join(strings.Fields(scanner.Text()), "")
		if pan == "" {
			continue
		}
		if !panPattern.MatchString(pan) || !validLuhn(pan) {
			return 0, 0, fmt.Errorf("строка %d: неверная контрольная цифра или формат", len(items)+1)
		}
		mask := pan[:6] + "******" + pan[len(pan)-4:]
		if seen[mask] {
			return 0, 0, fmt.Errorf("строка %d: повтор маски карты", len(items)+1)
		}
		seen[mask] = true
		items = append(items, panImportItem{mask: mask, pan: pan})
	}
	if err := scanner.Err(); err != nil {
		return 0, 0, err
	}
	if len(items) == 0 {
		return 0, 0, errors.New("список карт пуст")
	}
	for i := range items {
		rows, err := a.db.Query("SELECT id FROM cards WHERE mask=$1 AND status='active'", items[i].mask)
		if err != nil {
			return 0, 0, err
		}
		matches := 0
		for rows.Next() {
			matches++
			if err = rows.Scan(&items[i].cardID); err != nil {
				break
			}
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			return 0, 0, err
		}
		if matches != 1 {
			return 0, 0, fmt.Errorf("карта %s: найдено %d активных записей", items[i].mask, matches)
		}
		path, err := vaultPath(items[i].cardID)
		if err != nil {
			return 0, 0, err
		}
		if _, err = os.Stat(path); err == nil {
			stored, loadErr := loadPAN(items[i].cardID)
			if loadErr != nil || stored != items[i].pan {
				return 0, 0, fmt.Errorf("карта %s: сохранён другой номер или файл повреждён", items[i].mask)
			}
			items[i].exists = true
		} else if !os.IsNotExist(err) {
			return 0, 0, fmt.Errorf("карта %s: хранилище недоступно", items[i].mask)
		}
	}
	created, existing := 0, 0
	for _, item := range items {
		if item.exists {
			existing++
			continue
		}
		if err := savePAN(item.cardID, item.pan); err != nil {
			return created, existing, fmt.Errorf("карта %s: %w", item.mask, err)
		}
		_, err := a.db.Exec("INSERT INTO audit_events(id,channel,action,object_type,object_id,outcome,detail) VALUES($1,'maintenance','card_pan_import','card',$2,'success',$3)", id(), item.cardID, encode(M{"source": "owner_supplied_batch"}))
		if err != nil {
			path, _ := vaultPath(item.cardID)
			_ = os.Remove(path)
			return created, existing, fmt.Errorf("карта %s: не удалось записать аудит", item.mask)
		}
		created++
	}
	return created, existing, nil
}
