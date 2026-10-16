package main

import (
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestRegistryFiltersPreserveVisibilityAndPagination(t *testing.T) {
	r := httptest.NewRequest("GET", "/api/registries?page=2&q=100%25&merchant_id=merchant&status=posted&date_from=2026-10-01&date_to=2026-10-15", nil)
	query, args, page, err := registryListQuery(r, User{Role: "operator", ID: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	for _, clause := range []string{"s.uploader_id=$1", "r.status<>'deleted'", "POSITION(lower($2)", "r.merchant_id::text=$3", "r.status=$4", "r.payment_date>=$5::date", "r.payment_date<=$6::date", "ORDER BY r.created_at DESC,r.id DESC LIMIT 16 OFFSET $7"} {
		if !strings.Contains(query, clause) {
			t.Fatalf("missing %q in %s", clause, query)
		}
	}
	want := []any{"operator", "100%", "merchant", "posted", "2026-10-01", "2026-10-15", 15}
	if page != 2 || !reflect.DeepEqual(args, want) {
		t.Fatalf("page=%d args=%v", page, args)
	}
}

func TestRegistryFiltersRejectInvalidInput(t *testing.T) {
	for _, query := range []string{"page=0", "page=-1", "page=10001", "page=abc", "status=deleted", "date_from=2026-02-30", "date_to=bad", "date_from=2026-10-15&date_to=2026-10-01"} {
		t.Run(query, func(t *testing.T) {
			_, _, _, err := registryListQuery(httptest.NewRequest("GET", "/api/registries?"+query, nil), User{Role: "chief"})
			if err == nil {
				t.Fatal("expected invalid filter error")
			}
		})
	}
}

func TestRegistryFiltersKeepLegacyAndDetailContracts(t *testing.T) {
	query, args, page, err := registryListQuery(httptest.NewRequest("GET", "/api/registries", nil), User{Role: "chief"})
	if err != nil || page != 0 || len(args) != 0 || !strings.HasSuffix(query, "LIMIT 100") {
		t.Fatalf("legacy: %s %v %d %v", query, args, page, err)
	}
	query, args, page, err = registryListQuery(httptest.NewRequest("GET", "/api/registries?id=registry&page=bad&status=deleted", nil), User{Role: "operator", ID: "operator"})
	if err != nil || page != 0 || strings.Contains(query, "LIMIT") || !strings.Contains(query, "s.uploader_id=$1") || !strings.Contains(query, "r.id::text=$2") || !reflect.DeepEqual(args, []any{"operator", "registry"}) {
		t.Fatalf("detail: %s %v %d %v", query, args, page, err)
	}
}
