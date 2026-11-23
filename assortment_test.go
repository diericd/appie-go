package appie

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"strconv"
	"testing"
)

// assortmentServer answers aliased product(id: N) gql queries. Ids in inBE get
// taxonomies, other known ids get null taxonomies, and an unknown id fails the
// whole query the way AH does ("Subgraph errors redacted", data null).
func assortmentServer(t *testing.T, known []int, inBE map[int]bool) (*httptest.Server, *int) {
	t.Helper()
	calls := 0
	isKnown := map[int]bool{}
	for _, id := range known {
		isKnown[id] = true
	}
	re := regexp.MustCompile(`p(\d+): product\(id: \d+\)`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var body struct {
			Query string `json:"query"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		data := map[string]any{}
		for _, m := range re.FindAllStringSubmatch(body.Query, -1) {
			id, _ := strconv.Atoi(m[1])
			if !isKnown[id] {
				json.NewEncoder(w).Encode(map[string]any{"data": nil, "errors": []map[string]any{{"message": "Subgraph errors redacted", "path": []string{"p" + m[1]}}}})
				return
			}
			var tax any
			if inBE[id] {
				tax = []map[string]any{{"id": 21611}}
			}
			data["p"+m[1]] = map[string]any{"taxonomies": tax}
		}
		json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func TestInAssortment(t *testing.T) {
	srv, calls := assortmentServer(t, []int{565848, 238834, 2606}, map[int]bool{565848: true, 2606: true})
	client := New(WithBaseURL(srv.URL), WithTokens("test", "test"))

	got, err := client.InAssortment(context.Background(), []int{238834, 565848, 2606, 565848})
	if err != nil {
		t.Fatalf("InAssortment: %v", err)
	}
	want := map[int]bool{565848: true, 238834: false, 2606: true}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if *calls != 1 {
		t.Errorf("calls = %d, want one batched query", *calls)
	}
}

func TestInAssortmentUnknownIDFallsBackPerID(t *testing.T) {
	srv, _ := assortmentServer(t, []int{565848, 238834}, map[int]bool{565848: true})
	client := New(WithBaseURL(srv.URL), WithTokens("test", "test"))

	got, err := client.InAssortment(context.Background(), []int{565848, 999999, 238834})
	if err != nil {
		t.Fatalf("InAssortment: %v", err)
	}
	want := map[int]bool{565848: true, 238834: false}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v (unknown id left out)", got, want)
	}
}

func TestGetProductGQL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Shape seen live on 2026-11-23 for the BE-only witlof.
		json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"product": map[string]any{
			"id": 565848, "title": "AH Witlof", "brand": "AH", "salesUnitSize": "500 g",
			"category": "Groenten, aardappelen/Witlof en roodlof", "webPath": "/producten/product/wi565848/ah-witlof",
			"availabilityLabel": "Alleen in de winkel", "taxonomies": []map[string]any{{"id": 21611}},
			"price": map[string]any{"now": map[string]any{"amount": 3.79}, "was": map[string]any{"amount": 3.79}},
		}}})
	}))
	defer srv.Close()
	client := New(WithBaseURL(srv.URL), WithTokens("test", "test"))

	p, err := client.GetProductGQL(context.Background(), 565848)
	if err != nil {
		t.Fatalf("GetProductGQL: %v", err)
	}
	in := true
	want := &Product{
		ID: 565848, WebshopID: "565848", Title: "AH Witlof", Brand: "AH", UnitSize: "500 g",
		Category: "Groenten, aardappelen/Witlof en roodlof", WebPath: "/producten/product/wi565848/ah-witlof",
		Price: Price{Now: 3.79}, InAssortment: &in,
	}
	if !reflect.DeepEqual(p, want) {
		t.Errorf("got %+v\nwant %+v", p, want)
	}
}
