package appie

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// localeSearchServer fakes api.ah.be: gql productSearch returns searchIDs
// (or an error when gqlFails), the REST by-id endpoint knows restIDs, gql
// product(id:) knows only 565848 (a BE-only id), and the REST search returns
// one NL product. It records the x-locale header of each gql search.
func localeSearchServer(t *testing.T, searchIDs, restIDs []int, gqlFails bool) (*httptest.Server, *[]string) {
	t.Helper()
	var locales []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/graphql":
			var body struct {
				Query string `json:"query"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			if strings.Contains(body.Query, "productSearch") {
				locales = append(locales, r.Header.Get("x-locale"))
				if gqlFails {
					json.NewEncoder(w).Encode(map[string]any{"data": nil, "errors": []map[string]any{{"message": "Subgraph errors redacted"}}})
					return
				}
				ps := []map[string]any{}
				for _, id := range searchIDs {
					ps = append(ps, map[string]any{"id": id})
				}
				json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"productSearch": map[string]any{"products": ps}}})
				return
			}
			if strings.Contains(body.Query, "product(id: 565848)") {
				json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"product": map[string]any{
					"id": 565848, "title": "AH Witlof", "webPath": "/producten/product/wi565848/ah-witlof",
					"taxonomies": []map[string]any{{"id": 21611}}, "price": map[string]any{"now": map[string]any{"amount": 2.29}},
				}}})
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"product": nil}})
		case "/mobile-services/product/search/v2/products":
			var out []productResponse
			for _, id := range restIDs {
				out = append(out, productResponse{WebshopID: id, Title: "rest", CurrentPrice: 1, PriceBeforeBonus: 1})
			}
			json.NewEncoder(w).Encode(out)
		case "/mobile-services/product/search/v2":
			json.NewEncoder(w).Encode(searchResponse{Products: []productResponse{{WebshopID: 238834, Title: "AH Witlof"}}})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &locales
}

func ids(ps []Product) []int {
	out := make([]int, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.ID)
	}
	return out
}

func TestSearchWithLocaleKeepsOrderAndResolvesBEOnly(t *testing.T) {
	// 565848 is BE-only (not in REST), 999 resolves nowhere and is dropped.
	srv, locales := localeSearchServer(t, []int{565848, 238834, 999, 400693}, []int{238834, 400693}, false)
	c := New(WithBaseURL(srv.URL), WithTokens("a", "r"), WithLocale("nl_BE"))

	got, err := c.SearchProducts(context.Background(), "witlof", 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if want := []int{565848, 238834, 400693}; !reflect.DeepEqual(ids(got), want) {
		t.Errorf("ids = %v, want %v", ids(got), want)
	}
	if got[0].WebPath == "" || got[0].InAssortment == nil || !*got[0].InAssortment {
		t.Errorf("BE-only product not resolved through gql: %+v", got[0])
	}
	if want := []string{"nl_BE"}; !reflect.DeepEqual(*locales, want) {
		t.Errorf("x-locale on gql search = %v, want %v", *locales, want)
	}
}

func TestSearchWithLocaleFallsBackToREST(t *testing.T) {
	srv, _ := localeSearchServer(t, nil, nil, true)
	c := New(WithBaseURL(srv.URL), WithTokens("a", "r"), WithLocale("nl_BE"))

	got, err := c.SearchProducts(context.Background(), "witlof", 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if want := []int{238834}; !reflect.DeepEqual(ids(got), want) {
		t.Errorf("ids = %v, want the REST result %v", ids(got), want)
	}
}

func TestSearchWithoutLocaleUsesREST(t *testing.T) {
	srv, locales := localeSearchServer(t, []int{565848}, nil, false)
	c := New(WithBaseURL(srv.URL), WithTokens("a", "r"))

	got, err := c.SearchProducts(context.Background(), "witlof", 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if want := []int{238834}; !reflect.DeepEqual(ids(got), want) {
		t.Errorf("ids = %v, want %v", ids(got), want)
	}
	if len(*locales) != 0 {
		t.Errorf("gql search called without a locale: %v", *locales)
	}
}
