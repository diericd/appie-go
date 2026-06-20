package appie

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// When an order is active (e.g. after ReopenOrder), the client tracks its ID and
// sends it as appie-current-order-id. That header scopes the API to the order's
// delivery assortment. Product search MUST NOT inherit it: a REOPENED order has no
// valid delivery context, so a scoped search returns zero results. Order/basket
// operations MUST keep it.
func TestActiveOrderHeaderSkippedForProductSearch(t *testing.T) {
	cases := []struct {
		name     string
		path     string
		wantSent bool
	}{
		{"search by query", "/mobile-services/product/search/v2", false},
		{"search by ids", "/mobile-services/product/search/v2/products", false},
		{"order mutation keeps header", "/mobile-services/order/v1/123", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotHeader string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotHeader = r.Header.Get("appie-current-order-id")
				w.Write([]byte(`{}`))
			}))
			defer srv.Close()

			c := New(WithBaseURL(srv.URL), WithTokens("acc", "ref"))
			c.SetOrderID(451465143) // simulate an active/reopened order

			var out map[string]any
			if err := c.DoRequest(context.Background(), http.MethodGet, tc.path, nil, &out); err != nil {
				t.Fatalf("request: %v", err)
			}

			sent := gotHeader != ""
			if sent != tc.wantSent {
				t.Fatalf("path %s: order header sent=%v (value %q), want sent=%v",
					tc.path, sent, gotHeader, tc.wantSent)
			}
		})
	}
}
