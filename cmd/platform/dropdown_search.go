package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"
)

type dropdownOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}
type dropdownSearchInput struct {
	Query   string           `json:"q"`
	Options []dropdownOption `json:"options"`
	Source  string           `json:"source"`
	Context string           `json:"context"`
}

// Capture existing read adapters so their access checks remain authoritative.
type dropdownResponse struct {
	header http.Header
	bytes.Buffer
	status int
}

func (w *dropdownResponse) Header() http.Header    { return w.header }
func (w *dropdownResponse) WriteHeader(status int) { w.status = status }
func (w *dropdownResponse) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = 200
	}
	return w.Buffer.Write(p)
}

func dropdownMatches(text, query string) bool {
	return strings.Contains(strings.ToLower(text), strings.ToLower(strings.TrimSpace(query)))
}

func (a *App) dropdownSearch(w http.ResponseWriter, r *http.Request, u User) {
	if r.Method != http.MethodPost {
		fail(w, 405, errors.New("method"))
		return
	}
	defer r.Body.Close()
	var in dropdownSearchInput
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil || utf8.RuneCountInString(in.Query) > 200 || len(in.Options) > 20000 {
		fail(w, 400, errors.New("неверные параметры поиска"))
		return
	}
	var records []M
	if in.Source != "" {
		var fn handler
		collection := ""
		switch in.Source {
		case "manual-cards":
			fn = a.manualRegistryCards
		case "requests":
			fn = a.paymentRequests
		case "registry-candidates":
			if !validID(in.Context) {
				fail(w, 400, errors.New("неверный реестр"))
				return
			}
			fn = a.registryCandidates
		default:
			collection = strings.TrimPrefix(in.Source, "catalog/")
			switch collection {
			case "merchants", "banks", "cards", "custodians", "drop_handlers", "request_cards", "payment_contacts":
				if in.Source != "catalog/"+collection {
					fail(w, 400, errors.New("неверный источник поиска"))
					return
				}
				fn = a.catalog
			default:
				fail(w, 400, errors.New("неверный источник поиска"))
				return
			}
		}
		inner := r.Clone(r.Context())
		inner.Method = http.MethodGet
		inner.URL = &url.URL{Path: "/", RawQuery: url.Values{"id": {in.Context}, "dropdown": {"1"}, "dropdown_collection": {collection}}.Encode()}
		captured := &dropdownResponse{header: make(http.Header)}
		fn(captured, inner, u)
		if captured.status != 200 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(captured.status)
			_, _ = w.Write(captured.Bytes())
			return
		}
		if collection != "" {
			var result map[string][]M
			if err := json.Unmarshal(captured.Bytes(), &result); err != nil {
				fail(w, 500, err)
				return
			}
			var ok bool
			records, ok = result[collection]
			if !ok {
				fail(w, 403, errors.New("источник поиска недоступен"))
				return
			}
		} else if err := json.Unmarshal(captured.Bytes(), &records); err != nil {
			fail(w, 500, err)
			return
		}
	}
	respond(w, 200, M{"values": dropdownValues(in, records)})
}

func dropdownValues(in dropdownSearchInput, records []M) []string {
	allowed := make(map[string]bool)
	for _, record := range records {
		id := str(record, "id")
		if id == "" {
			id = str(record, "card_id")
		}
		// Only user-visible identity fields are searchable, never PANs or balances.
		text := ""
		for _, field := range []string{"name", "bank", "mask", "owner_label", "full_name", "phone", "external_ref", "title", "contact_name", "contact_phone", "drop_handler_name"} {
			text += " " + str(record, field)
		}
		allowed[id] = dropdownMatches(text, in.Query)
	}
	values := []string{}
	for _, option := range in.Options {
		match := dropdownMatches(option.Label, in.Query)
		if in.Source != "" {
			if found, ok := allowed[option.Value]; ok {
				match = found
			} else {
				// Only these explicit non-entity choices may bypass source membership.
				match = (option.Value == "all" || option.Value == "__all" || option.Value == "none" || option.Value == "__none" || option.Value == "unassigned") && match
			}
		}
		if match {
			values = append(values, option.Value)
		}
	}
	return values
}
