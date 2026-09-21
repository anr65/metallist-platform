package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const nspkBanksURL = "https://sbp.nspk.ru"

type nspkBank struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

type nspkBankPage struct {
	Data []nspkBank `json:"data"`
	Meta struct {
		Total  int `json:"total"`
		Offset int `json:"offset"`
	} `json:"meta"`
}

func fetchNSPKBanks(ctx context.Context, client *http.Client, baseURL string) ([]nspkBank, error) {
	const pageSize = 500
	var all []nspkBank
	seen := make(map[string]bool)
	total := -1
	for offset := 0; ; {
		endpoint, err := url.Parse(strings.TrimRight(baseURL, "/") + "/rest/v1/banks/list")
		if err != nil {
			return nil, err
		}
		query := endpoint.Query()
		query.Set("limit", fmt.Sprint(pageSize))
		query.Set("offset", fmt.Sprint(offset))
		endpoint.RawQuery = query.Encode()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", "Metallist-BankDirectory/1.0")
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, fmt.Errorf("справочник НСПК недоступен: HTTP %d", resp.StatusCode)
		}
		var page nspkBankPage
		decoder := json.NewDecoder(io.LimitReader(resp.Body, 5<<20))
		err = decoder.Decode(&page)
		resp.Body.Close()
		if err != nil {
			return nil, errors.New("не удалось прочитать справочник НСПК")
		}
		if page.Meta.Total < 200 || page.Meta.Total > 1000 || page.Meta.Offset != offset || (total >= 0 && page.Meta.Total != total) {
			return nil, errors.New("неполный или изменившийся справочник НСПК")
		}
		total = page.Meta.Total
		if len(page.Data) == 0 || len(all)+len(page.Data) > total {
			return nil, errors.New("неполный или повторяющийся справочник НСПК")
		}
		for _, bank := range page.Data {
			bank.ID = strings.ToLower(strings.TrimSpace(bank.ID))
			bank.Title = strings.TrimSpace(bank.Title)
			if !cardIDPattern.MatchString(bank.ID) || len([]rune(bank.Title)) == 0 || len([]rune(bank.Title)) > 200 || seen[bank.ID] {
				return nil, errors.New("некорректная запись банка в справочнике НСПК")
			}
			seen[bank.ID] = true
			all = append(all, bank)
		}
		if len(all) == total {
			return all, nil
		}
		offset += len(page.Data)
	}
}

func (a *App) syncNSPKBanks(ctx context.Context, client *http.Client, baseURL string) (int, error) {
	banks, err := fetchNSPKBanks(ctx, client, baseURL)
	if err != nil {
		return 0, err
	}
	tx, err := a.tx()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var previous int
	if err = tx.QueryRow("SELECT count(*) FROM banks WHERE source='nspk_sbp' AND selectable").Scan(&previous); err != nil {
		return 0, err
	}
	if previous > 0 && len(banks)*100 < previous*95 {
		return 0, errors.New("справочник НСПК резко сократился; требуется ручная проверка источника")
	}
	if _, err = tx.Exec("UPDATE banks SET selectable=false WHERE source='nspk_sbp' AND selectable"); err != nil {
		return 0, err
	}
	for _, bank := range banks {
		_, err = tx.Exec(`INSERT INTO banks(id,code,name,source,external_id,selectable,last_synced_at)
			VALUES($1,$2,$3,'nspk_sbp',$4,true,now())
			ON CONFLICT (external_id) DO UPDATE SET name=EXCLUDED.name,selectable=true,last_synced_at=EXCLUDED.last_synced_at`,
			id(), "NSPK-SBP-"+bank.ID, bank.Title, bank.ID)
		if err != nil {
			return 0, err
		}
	}
	if _, err = tx.Exec(`INSERT INTO audit_events(id,channel,action,object_type,outcome,detail)
		VALUES($1,'system','nspk_bank_sync','bank_directory','success',$2::jsonb)`, id(), fmt.Sprintf(`{"count":%d}`, len(banks))); err != nil {
		return 0, err
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return len(banks), nil
}

func officialNSPKClient() *http.Client {
	return &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != "https" || req.URL.Hostname() != "sbp.nspk.ru" {
			return errors.New("неожиданное перенаправление справочника НСПК")
		}
		if len(via) >= 3 {
			return errors.New("слишком много перенаправлений справочника НСПК")
		}
		return nil
	}}
}
