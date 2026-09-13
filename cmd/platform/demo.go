package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/xuri/excelize/v2"
	"golang.org/x/text/encoding/charmap"
)

func (a *App) seedDemo() error {
	if os.Getenv("APP_ENV") != "demo" {
		return errors.New("demo environment required")
	}
	var database string
	if e := a.db.QueryRow("SELECT current_database()").Scan(&database); e != nil {
		return e
	}
	if database != "metallist_demo" {
		return errors.New("wrong database")
	}
	tx, e := a.tx()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var n int
	if e = tx.QueryRow("SELECT count(*) FROM merchants").Scan(&n); e != nil {
		return e
	}
	if n != 0 {
		return errors.New("demo already seeded")
	}
	var chief, chiefCust string
	if e = tx.QueryRow("SELECT id FROM users WHERE role='chief'").Scan(&chief); e != nil {
		return e
	}
	if e = tx.QueryRow("SELECT id FROM custodians WHERE kind='chief'").Scan(&chiefCust); e != nil {
		return e
	}
	m1, m2, m3 := id(), id(), id()
	for _, m := range []struct {
		id, code, name string
		rate           int
	}{{m1, "FERRUM", "Феррум Демо", 400}, {m2, "LIGA", "Лига Металла", 450}, {m3, "VOSTOK", "Восток Сырьё", 325}} {
		if _, e = tx.Exec("INSERT INTO merchants(id,code,name) VALUES($1,$2,$3)", m.id, m.code, m.name); e != nil {
			return e
		}
		if _, e = tx.Exec("INSERT INTO tariffs(id,merchant_id,rate_bp,valid_from,created_by) VALUES($1,$2,$3,'2026-01-01',$4)", id(), m.id, m.rate, chief); e != nil {
			return e
		}
	}
	bank1, bank2 := id(), id()
	for _, b := range []struct{ id, code, name string }{{bank1, "DEMO1", "Банк Первый · демо"}, {bank2, "DEMO2", "Банк Север · демо"}} {
		if _, e = tx.Exec("INSERT INTO banks(id,code,name) VALUES($1,$2,$3)", b.id, b.code, b.name); e != nil {
			return e
		}
	}
	c1, c2, c3, c4 := id(), id(), id(), id()
	for _, c := range []struct{ id, bank, mask, name string }{{c1, bank1, "000000******1001", "Вымышленный владелец 1"}, {c2, bank1, "000000******1002", "Вымышленный владелец 2"}, {c3, bank2, "111111******2001", "Вымышленный владелец 3"}, {c4, bank2, "111111******2002", "Вымышленный владелец 4"}} {
		if _, e = tx.Exec("INSERT INTO cards(id,bank_id,owner_label,mask,last4) VALUES($1,$2,$3,$4,$5)", c.id, c.bank, c.name, c.mask, c.mask[len(c.mask)-4:]); e != nil {
			return e
		}
	}
	collectorA, collectorB := id(), id()
	for _, c := range []struct{ id, name string }{{collectorA, "Сборщик Альфа"}, {collectorB, "Сборщик Бета"}} {
		if _, e = tx.Exec("INSERT INTO custodians(id,name,kind) VALUES($1,$2,'collector')", c.id, c.name); e != nil {
			return e
		}
	}
	now := time.Now().Add(-time.Hour)
	registry := func(merchant, card1, card2, ref string, v1, v2 int64, rate int) error {
		reg, src := id(), id()
		gross := v1 + v2
		comm := fee(gross, rate)
		var mask1, mask2 string
		if e = tx.QueryRow("SELECT mask FROM cards WHERE id=$1", card1).Scan(&mask1); e != nil {
			return e
		}
		if e = tx.QueryRow("SELECT mask FROM cards WHERE id=$1", card2).Scan(&mask2); e != nil {
			return e
		}
		file := excelize.NewFile()
		file.SetCellValue("Sheet1", "A1", "Карта")
		file.SetCellValue("Sheet1", "B1", "Сумма")
		file.SetCellValue("Sheet1", "A2", mask1)
		file.SetCellValue("Sheet1", "B2", rub(v1))
		file.SetCellValue("Sheet1", "A3", mask2)
		file.SetCellValue("Sheet1", "B3", rub(v2))
		buf, err := file.WriteToBuffer()
		file.Close()
		if err != nil {
			return err
		}
		sum := sha256.Sum256(buf.Bytes())
		hash := hex.EncodeToString(sum[:])
		path := filepath.Join(a.storage, hash)
		if err = os.WriteFile(path, buf.Bytes(), 0600); err != nil {
			return err
		}
		if _, e = tx.Exec("INSERT INTO source_documents(id,kind,filename,sha256,media_type,byte_size,storage_path,uploader_id) VALUES($1,'demo',$2,$3,'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet',$4,$5,$6)", src, ref+".xlsx", hash, buf.Len(), path, chief); e != nil {
			return e
		}
		if _, e = tx.Exec("INSERT INTO registries(id,merchant_id,source_id,external_ref,status,total_cents,commission_cents,rate_bp,confirmed_at) VALUES($1,$2,$3,$4,'posted',$5,$6,$7,$8)", reg, merchant, src, ref, gross, comm, rate, now); e != nil {
			return e
		}
		for i, x := range []struct {
			card   string
			amount int64
		}{{card1, v1}, {card2, v2}} {
			mask := mask1
			if i == 1 {
				mask = mask2
			}
			if _, e = tx.Exec("INSERT INTO registry_rows(id,registry_id,row_no,raw,card_id,amount_cents) VALUES($1,$2,$3,$4,$5,$6)", id(), reg, i+2, encode([]string{mask, rub(x.amount)}), x.card, x.amount); e != nil {
				return e
			}
		}
		_, e = put(tx, "registry", reg, "demo-registry:"+reg, chief, now, now, []Posting{{Account: "1100", Side: "debit", Amount: v1, Card: card1}, {Account: "1100", Side: "debit", Amount: v2, Card: card2}, {Account: "2100", Side: "credit", Amount: gross - comm, Merchant: merchant}, {Account: "4100", Side: "credit", Amount: comm, Merchant: merchant}}, "")
		if e != nil {
			return e
		}
		return txAudit(tx, chief, "system", "demo_registry_seed", "registry", reg, "success", "", M{"synthetic": true})
	}
	if e = registry(m1, c1, c2, "DEMO-R-001", 14000000, 9000000, 400); e != nil {
		return e
	}
	if e = registry(m2, c3, c4, "DEMO-R-002", 11000000, 6000000, 450); e != nil {
		return e
	}
	if e = registry(m3, c2, c4, "DEMO-R-003", 7000000, 5000000, 325); e != nil {
		return e
	}
	post := func(kind string, p []Posting) error {
		event := id()
		_, e := put(tx, kind, event, "demo:"+event, chief, now, now, p, "")
		if e != nil {
			return e
		}
		return txAudit(tx, chief, "system", "demo_event_seed", kind, event, "success", "", M{"synthetic": true})
	}
	if e = post("withdrawal", []Posting{{Account: "1200", Side: "debit", Amount: 9000000, Custodian: collectorA}, {Account: "1100", Side: "credit", Amount: 9000000, Card: c1}}); e != nil {
		return e
	}
	if e = post("withdrawal", []Posting{{Account: "1200", Side: "debit", Amount: 4000000, Custodian: collectorB}, {Account: "1100", Side: "credit", Amount: 4000000, Card: c3}}); e != nil {
		return e
	}
	if e = post("handover", []Posting{{Account: "1210", Side: "debit", Amount: 8500000, Custodian: chiefCust}, {Account: "1200", Side: "credit", Amount: 8500000, Custodian: collectorA}}); e != nil {
		return e
	}
	if e = post("repayment", []Posting{{Account: "2100", Side: "debit", Amount: 4500000, Merchant: m1}, {Account: "1210", Side: "credit", Amount: 4500000, Custodian: chiefCust}}); e != nil {
		return e
	}
	if e = post("expense", []Posting{{Account: "5100", Side: "debit", Amount: 250000, Category: "логистика"}, {Account: "1210", Side: "credit", Amount: 250000, Custodian: chiefCust}}); e != nil {
		return e
	}
	if e = post("shortage", []Posting{{Account: "1400", Side: "debit", Amount: 100000, Custodian: collectorA}, {Account: "1200", Side: "credit", Amount: 100000, Custodian: collectorA}}); e != nil {
		return e
	}
	if e = post("surplus", []Posting{{Account: "1210", Side: "debit", Amount: 30000, Custodian: chiefCust}, {Account: "2300", Side: "credit", Amount: 30000, Ref: id()}}); e != nil {
		return e
	}
	return tx.Commit()
}
func (a *App) sample(w http.ResponseWriter, r *http.Request, u User) {
	if os.Getenv("APP_ENV") != "demo" {
		http.NotFound(w, r)
		return
	}
	kind := r.URL.Query().Get("kind")
	if kind == "csv" {
		w.Header().Set("Content-Type", "text/csv; charset=windows-1251")
		w.Header().Set("Content-Disposition", "attachment; filename=synthetic-bank.csv")
		v, _ := charmap.Windows1251.NewEncoder().Bytes([]byte("meta;demo\nМаскированный номер карты;Сумма операции;Комиссия Банка;К перечислению\n000000******1001;1000,00;10,00;1010,00\n"))
		_, _ = w.Write(v)
		return
	}
	f := excelize.NewFile()
	defer f.Close()
	f.SetCellValue("Sheet1", "A1", "Карта")
	f.SetCellValue("Sheet1", "B1", "Сумма")
	f.SetCellValue("Sheet1", "A2", "000000******1001")
	f.SetCellValue("Sheet1", "B2", "1234.64")
	f.SetCellValue("Sheet1", "A3", "000000******1002")
	f.SetCellValue("Sheet1", "B3", "987.70")
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", "attachment; filename=synthetic-registry.xlsx")
	_ = f.Write(w)
}
