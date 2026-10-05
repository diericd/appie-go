package appie

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGetShoppingListItems(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/graphql" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}

		resp := graphQLResponse[json.RawMessage]{
			Data: json.RawMessage(`{
				"favoriteListV2": [{
					"id": "LIST-123",
					"description": "Boodschappen",
					"totalSize": 3,
					"items": [
						{"id": "item-1", "productId": 12345, "quantity": 2},
						{"id": "item-2", "productId": 67890, "quantity": 1},
						{"id": "item-3", "productId": 0, "quantity": 1}
					]
				}]
			}`),
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	client := New(WithBaseURL(srv.URL))
	items, err := client.GetShoppingListItems(context.Background(), "list-123")
	if err != nil {
		t.Fatal(err)
	}

	if len(items) != 3 {
		t.Fatalf("got %d items, want 3", len(items))
	}

	if items[0].ID != "item-1" {
		t.Errorf("got ID %q, want %q", items[0].ID, "item-1")
	}
	if items[0].ProductID != 12345 {
		t.Errorf("got ProductID %d, want 12345", items[0].ProductID)
	}
	if items[0].Quantity != 2 {
		t.Errorf("got Quantity %d, want 2", items[0].Quantity)
	}
	if items[2].ProductID != 0 {
		t.Errorf("got ProductID %d for free-text item, want 0", items[2].ProductID)
	}
}

func TestGetShoppingLists(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/mobile-services/lists/v3/lists" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}

		resp := []listResponse{
			{ID: "abc-123", Description: "Boodschappen", ItemCount: 5},
			{ID: "def-456", Description: "Weekmenu", ItemCount: 3},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	client := New(WithBaseURL(srv.URL))
	lists, err := client.GetShoppingLists(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}

	if len(lists) != 2 {
		t.Fatalf("got %d lists, want 2", len(lists))
	}

	if lists[0].Name != "Boodschappen" {
		t.Errorf("got Name %q, want %q", lists[0].Name, "Boodschappen")
	}
	if lists[0].ItemCount != 5 {
		t.Errorf("got ItemCount %d, want 5", lists[0].ItemCount)
	}
	if lists[1].ID != "def-456" {
		t.Errorf("got ID %q, want %q", lists[1].ID, "def-456")
	}
}

// TestRemoveFromShoppingList pins the v2 delete payload shape. The AH API rejects
// a minimal {productId, quantity:0, type, originCode} body with 400
// "Failed to read request"; the delete must carry the same fields a normal add
// sends — description, searchTerm and (crucially) strikeThrough.
func TestRemoveFromShoppingList(t *testing.T) {
	const path = "/mobile-services/shoppinglist/v2/items"

	var patchBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != path {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		switch r.Method {
		case http.MethodGet:
			// One product item (Evian) plus a free-text item.
			w.Write([]byte(`{"items":[
				{"listItemId":1,"quantity":1,"type":"SHOPPABLE","originCode":"PRD",
				 "productDetails":{"product":{"webshopId":130922,"title":"Evian Natuurlijk mineraalwater"}}},
				{"listItemId":2,"quantity":1,"type":"SHOPPABLE","originCode":"PRD",
				 "productDetails":{"product":{"webshopId":999999,"title":"AH Radijs"}}}
			]}`))
		case http.MethodPatch:
			body, _ := io.ReadAll(r.Body)
			patchBody = body
			w.WriteHeader(http.StatusOK)
		default:
			t.Fatalf("unexpected method: %s", r.Method)
		}
	}))
	defer srv.Close()

	client := New(WithBaseURL(srv.URL))
	if err := client.RemoveFromShoppingList(context.Background(), []int{130922}, nil); err != nil {
		t.Fatalf("RemoveFromShoppingList: %v", err)
	}

	if patchBody == nil {
		t.Fatal("no PATCH request was sent")
	}

	var payload struct {
		Items []map[string]json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(patchBody, &payload); err != nil {
		t.Fatalf("decode PATCH body: %v (raw=%s)", err, patchBody)
	}
	if len(payload.Items) != 1 {
		t.Fatalf("got %d items in PATCH, want 1 (only the matched product)", len(payload.Items))
	}

	item := payload.Items[0]
	// The three fields whose absence caused the 400 must be present.
	for _, key := range []string{"description", "searchTerm", "strikeThrough"} {
		if _, ok := item[key]; !ok {
			t.Errorf("PATCH item missing required field %q; body=%s", key, patchBody)
		}
	}
	if got := string(item["quantity"]); got != "0" {
		t.Errorf("got quantity %s, want 0 (0 signals delete)", got)
	}
	if got := string(item["productId"]); got != "130922" {
		t.Errorf("got productId %s, want 130922", got)
	}
}

func TestRemoveFromShoppingListNoMatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"items":[{"listItemId":1,"quantity":1,"type":"SHOPPABLE","originCode":"PRD","productDetails":{"product":{"webshopId":111,"title":"AH Melk"}}}]}`))
	}))
	defer srv.Close()

	client := New(WithBaseURL(srv.URL))
	err := client.RemoveFromShoppingList(context.Background(), []int{999}, nil)
	if err == nil {
		t.Fatal("expected error when no item matches, got nil")
	}
}

// boodschappenlijstServer fakes GET/PATCH /shoppinglist/v2/items, the GraphQL
// basket mutation and the active order read. PATCH bodies and GraphQL variables
// are captured. The order is empty before the mutation and holds pid 111 at
// qty 2 after it.
func boodschappenlijstServer(t *testing.T, listJSON string, patches *[][]byte, graphql *[][]byte) *httptest.Server {
	return boodschappenlijstSeqServer(t, []string{listJSON}, emptyOrder, order111Qty2, patches, graphql)
}

const (
	emptyOrder   = `{"id":777,"orderedProducts":[]}`
	order111Qty2 = `{"id":777,"orderedProducts":[{"quantity":2,"product":{"webshopId":111}}]}`
)

// boodschappenlijstSeqServer is boodschappenlijstServer with state: GET of the
// list returns lists[n] after n PATCHes (the last entry once exhausted), and the
// active order is orderBefore until the first GraphQL mutation, orderAfter from then on.
func boodschappenlijstSeqServer(t *testing.T, lists []string, orderBefore, orderAfter string, patches *[][]byte, graphql *[][]byte) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		switch {
		case r.URL.Path == "/mobile-services/shoppinglist/v2/items" && r.Method == http.MethodGet:
			w.Write([]byte(lists[min(len(*patches), len(lists)-1)]))
		case r.URL.Path == "/mobile-services/shoppinglist/v2/items" && r.Method == http.MethodPatch:
			*patches = append(*patches, body)
		case r.URL.Path == "/graphql":
			*graphql = append(*graphql, body)
			w.Write([]byte(`{"data":{}}`))
		case r.URL.Path == "/mobile-services/order/v1/summaries/active":
			if len(*graphql) == 0 {
				w.Write([]byte(orderBefore))
			} else {
				w.Write([]byte(orderAfter))
			}
		default:
			// Lists v3 / favorites must never be touched by these functions.
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
}

const testBoodschappenlijst = `{"items":[
	{"listItemId":1,"quantity":2,"type":"SHOPPABLE","originCode":"PRD","strikedthrough":false,
	 "productDetails":{"product":{"webshopId":111,"title":"AH Melk"}}},
	{"listItemId":2,"quantity":1,"type":"SHOPPABLE","originCode":"PRD","strikedthrough":true,
	 "productDetails":{"product":{"webshopId":222,"title":"AH Brood"}}},
	{"listItemId":3,"quantity":1,"type":"TEXT","originCode":"TXT","description":"verse bloemen"}
]}`

func TestGetBoodschappenlijst(t *testing.T) {
	var patches, gql [][]byte
	srv := boodschappenlijstServer(t, testBoodschappenlijst, &patches, &gql)
	defer srv.Close()

	items, err := New(WithBaseURL(srv.URL)).GetBoodschappenlijst(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []ListItem{
		{ID: "1", Name: "AH Melk", ProductID: 111, Quantity: 2},
		{ID: "2", Name: "AH Brood", ProductID: 222, Quantity: 1, Checked: true},
		{ID: "3", Name: "verse bloemen", Quantity: 1},
	}
	if len(items) != len(want) {
		t.Fatalf("got %d items, want %d: %+v", len(items), len(want), items)
	}
	for i := range want {
		if items[i] != want[i] {
			t.Errorf("item %d: got %+v, want %+v", i, items[i], want[i])
		}
	}
}

// TestClearBoodschappenlijst pins that clearing reads the v2 Boodschappenlijst
// (not favorites list #1) and deletes every line with the full-shape body.
func TestClearBoodschappenlijst(t *testing.T) {
	var patches, gql [][]byte
	srv := boodschappenlijstSeqServer(t, []string{testBoodschappenlijst, `{"items":[]}`}, emptyOrder, emptyOrder, &patches, &gql)
	defer srv.Close()

	n, left, err := New(WithBaseURL(srv.URL)).ClearBoodschappenlijst(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 || left != 0 {
		t.Errorf("got removed=%d left=%d, want 3 and 0", n, left)
	}
	if len(patches) != 1 {
		t.Fatalf("got %d PATCH requests, want 1", len(patches))
	}

	var payload struct {
		Items []map[string]json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(patches[0], &payload); err != nil {
		t.Fatalf("decode PATCH body: %v", err)
	}
	if len(payload.Items) != 3 {
		t.Fatalf("got %d PATCH items, want 3; body=%s", len(payload.Items), patches[0])
	}
	for i, item := range payload.Items {
		for _, key := range []string{"description", "searchTerm", "strikeThrough", "type", "originCode"} {
			if _, ok := item[key]; !ok {
				t.Errorf("item %d missing %q; body=%s", i, key, patches[0])
			}
		}
		if got := string(item["quantity"]); got != "0" {
			t.Errorf("item %d: quantity %s, want 0", i, got)
		}
	}
	// The free-text line keeps its own type/originCode and description.
	if got := string(payload.Items[2]["description"]); got != `"verse bloemen"` {
		t.Errorf("free-text description = %s, want \"verse bloemen\"", got)
	}
	if got := string(payload.Items[2]["type"]); got != `"TEXT"` {
		t.Errorf("free-text type = %s, want \"TEXT\"", got)
	}
	if _, ok := payload.Items[2]["productId"]; ok {
		t.Errorf("free-text item must not carry productId; body=%s", patches[0])
	}
}

func TestClearBoodschappenlijstEmpty(t *testing.T) {
	var patches, gql [][]byte
	srv := boodschappenlijstServer(t, `{"items":[]}`, &patches, &gql)
	defer srv.Close()

	n, left, err := New(WithBaseURL(srv.URL)).ClearBoodschappenlijst(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 || left != 0 || len(patches) != 0 {
		t.Errorf("got n=%d patches=%d, want 0 and no PATCH", n, len(patches))
	}
}

// TestClearBoodschappenlijstReportsLeftovers pins that lines AH keeps after
// a 2xx PATCH are counted from a read-back instead of being reported as removed.
func TestClearBoodschappenlijstReportsLeftovers(t *testing.T) {
	var patches, gql [][]byte
	leftover := `{"items":[{"listItemId":3,"quantity":1,"type":"TEXT","originCode":"TXT","description":"verse bloemen"}]}`
	srv := boodschappenlijstSeqServer(t, []string{testBoodschappenlijst, leftover}, emptyOrder, emptyOrder, &patches, &gql)
	defer srv.Close()

	n, left, err := New(WithBaseURL(srv.URL)).ClearBoodschappenlijst(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 || left != 1 {
		t.Errorf("got removed=%d left=%d, want 2 and 1", n, left)
	}
}

// TestShoppingListToOrderAdditive pins that the list quantity is added to what
// the cart already holds: the basket mutation sets absolute quantities, so
// sending the list quantity alone would cut the cart line.
func TestShoppingListToOrderAdditive(t *testing.T) {
	var patches, gql [][]byte
	before := `{"id":777,"orderedProducts":[{"quantity":3,"product":{"webshopId":111}}]}`
	after := `{"id":777,"orderedProducts":[{"quantity":5,"product":{"webshopId":111}}]}`
	srv := boodschappenlijstSeqServer(t, []string{testBoodschappenlijst}, before, after, &patches, &gql)
	defer srv.Close()

	res, err := New(WithBaseURL(srv.URL)).ShoppingListToOrder(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(gql) != 1 {
		t.Fatalf("got %d GraphQL requests, want 1", len(gql))
	}
	var req struct {
		Variables struct {
			Items []struct {
				ID       int `json:"id"`
				Quantity int `json:"quantity"`
			} `json:"items"`
		} `json:"variables"`
	}
	if err := json.Unmarshal(gql[0], &req); err != nil {
		t.Fatalf("decode GraphQL body: %v", err)
	}
	if len(req.Variables.Items) != 1 || req.Variables.Items[0].ID != 111 || req.Variables.Items[0].Quantity != 5 {
		t.Errorf("got basket items %+v, want [{111 5}] (3 in cart + 2 on list)", req.Variables.Items)
	}
	if res == nil || len(res.Landed) != 1 || res.Landed[0] != (BasketLine{ID: 111, Qty: 5}) {
		t.Errorf("got result %+v, want pid 111 landed at qty 5", res)
	}
}

// TestAddFreeTextToShoppingListVerified pins that a free-text add is verified
// when a free-text line with that name holds at least the requested quantity
// after the add (live AH leaves a re-added line at its quantity), never by a
// product line that happens to carry the same name.
func TestAddFreeTextToShoppingListVerified(t *testing.T) {
	const productSameName = `{"listItemId":1,"quantity":1,"type":"SHOPPABLE","originCode":"PRD",
		"productDetails":{"product":{"webshopId":111,"title":"AH Halfvolle melk"}}}`
	const freeText = `{"listItemId":%d,"quantity":%d,"type":"SHOPPABLE","originCode":"PRD","description":"ah halfvolle melk"}`
	tests := []struct {
		name  string
		after string
		want  bool
	}{
		{"free-text line added", `{"items":[` + productSameName + `,` + fmt.Sprintf(freeText, 9, 1) + `]}`, true},
		{"only a product line with that name", `{"items":[` + productSameName + `]}`, false},
		{"free-text line above requested qty", `{"items":[` + fmt.Sprintf(freeText, 9, 2) + `]}`, true},
		{"existing free-text line re-added", `{"items":[` + fmt.Sprintf(freeText, 9, 1) + `]}`, true},
		{"free-text line gone after add", `{"items":[]}`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var patches, gql [][]byte
			srv := boodschappenlijstSeqServer(t, []string{tt.after}, emptyOrder, emptyOrder, &patches, &gql)
			defer srv.Close()

			got, err := New(WithBaseURL(srv.URL)).AddFreeTextToShoppingList(context.Background(), "AH Halfvolle melk", 1)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("verified = %v, want %v", got, tt.want)
			}
			if len(patches) != 1 {
				t.Errorf("got %d PATCH requests, want 1", len(patches))
			}
		})
	}
}

// TestShoppingListToOrder pins that only unchecked product lines of the v2
// Boodschappenlijst go to the order: struck-through and free-text lines are skipped.
func TestShoppingListToOrder(t *testing.T) {
	var patches, gql [][]byte
	srv := boodschappenlijstServer(t, testBoodschappenlijst, &patches, &gql)
	defer srv.Close()

	res, err := New(WithBaseURL(srv.URL)).ShoppingListToOrder(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res == nil || len(res.Landed) != 1 || res.Landed[0] != (BasketLine{ID: 111, Qty: 2}) || len(res.Rejected) != 0 {
		t.Errorf("got result %+v, want pid 111 landed at qty 2", res)
	}
	if len(gql) != 1 {
		t.Fatalf("got %d GraphQL requests, want 1", len(gql))
	}

	var req struct {
		Variables struct {
			Items []struct {
				ID       int `json:"id"`
				Quantity int `json:"quantity"`
			} `json:"items"`
		} `json:"variables"`
	}
	if err := json.Unmarshal(gql[0], &req); err != nil {
		t.Fatalf("decode GraphQL body: %v", err)
	}
	if len(req.Variables.Items) != 1 || req.Variables.Items[0].ID != 111 || req.Variables.Items[0].Quantity != 2 {
		t.Errorf("got basket items %+v, want [{111 2}]", req.Variables.Items)
	}
}

func TestShoppingListToOrderNothingToAdd(t *testing.T) {
	var patches, gql [][]byte
	srv := boodschappenlijstServer(t, `{"items":[{"listItemId":2,"quantity":1,"strikedthrough":true,
		"productDetails":{"product":{"webshopId":222,"title":"AH Brood"}}}]}`, &patches, &gql)
	defer srv.Close()

	res, err := New(WithBaseURL(srv.URL)).ShoppingListToOrder(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res != nil || len(gql) != 0 {
		t.Errorf("got res=%+v graphql=%d, want nil and no mutation", res, len(gql))
	}
}

// TestRemoveFromShoppingListFreeTextByName pins that a free-text line (no
// product, name in the top-level description) can be removed by name.
func TestRemoveFromShoppingListFreeTextByName(t *testing.T) {
	var patches, gql [][]byte
	srv := boodschappenlijstServer(t, testBoodschappenlijst, &patches, &gql)
	defer srv.Close()

	if err := New(WithBaseURL(srv.URL)).RemoveFromShoppingList(context.Background(), nil, []string{"Verse Bloemen"}); err != nil {
		t.Fatal(err)
	}
	if len(patches) != 1 {
		t.Fatalf("got %d PATCH requests, want 1", len(patches))
	}
	var payload struct {
		Items []shoppingListItem `json:"items"`
	}
	if err := json.Unmarshal(patches[0], &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Items) != 1 || payload.Items[0].Description != "verse bloemen" {
		t.Errorf("got %+v, want one item with description \"verse bloemen\"", payload.Items)
	}
}
