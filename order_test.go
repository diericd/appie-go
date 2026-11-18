package appie

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestGetOrderDetails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/mobile-services/order/v1/229775812/details-grouped-by-taxonomy" {
			t.Errorf("unexpected path: %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"orderId":      229775812,
			"deliveryDate": "2025-12-09",
			"orderState":   "DELIVERED",
			"closingTime":  "2025-12-08T22:59:00Z",
			"deliveryType": "HOME",
			"deliveryTimePeriod": map[string]any{
				"startDateTime": "2025-12-09T18:00:00",
				"endDateTime":   "2025-12-09T20:00:00",
			},
			"groupedProductsInTaxonomy": []map[string]any{
				{
					"taxonomyName": "Groente, aardappelen",
					"orderedProducts": []map[string]any{
						{
							"amount":   1,
							"quantity": 2,
							"product": map[string]any{
								"webshopId":        164358,
								"title":            "AH Oranje zoete aardappel",
								"brand":            "AH",
								"salesUnitSize":    "1 kg",
								"priceBeforeBonus": 3.79,
								"isBonus":          false,
							},
						},
					},
				},
				{
					"taxonomyName": "Zuivel, eieren",
					"orderedProducts": []map[string]any{
						{
							"amount":   1,
							"quantity": 1,
							"product": map[string]any{
								"webshopId":        371880,
								"title":            "Optimel Drinkyoghurt aardbei",
								"brand":            "Optimel",
								"salesUnitSize":    "1 L",
								"priceBeforeBonus": 1.59,
								"currentPrice":     1.19,
								"isBonus":          true,
								"bonusMechanism":   "25% korting",
							},
						},
					},
				},
			},
			"invoiceId":   "2294567-00199",
			"cancellable": false,
		})
	}))
	defer srv.Close()

	client := New(WithBaseURL(srv.URL), WithTokens("test", "test"))
	ctx := context.Background()

	order, err := client.GetOrderDetails(ctx, 229775812)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if order.ID != "229775812" {
		t.Errorf("expected ID '229775812', got %q", order.ID)
	}
	if order.State != "DELIVERED" {
		t.Errorf("expected state 'DELIVERED', got %q", order.State)
	}
	if len(order.Items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(order.Items))
	}

	item := order.Items[0]
	if item.ProductID != 164358 {
		t.Errorf("expected productID 164358, got %d", item.ProductID)
	}
	if item.Quantity != 2 {
		t.Errorf("expected quantity 2, got %d", item.Quantity)
	}
	if item.Product == nil {
		t.Fatal("expected product to be populated")
	}
	if item.Product.Title != "AH Oranje zoete aardappel" {
		t.Errorf("expected title 'AH Oranje zoete aardappel', got %q", item.Product.Title)
	}
	if item.Product.Price.Now != 3.79 {
		t.Errorf("expected price 3.79, got %.2f", item.Product.Price.Now)
	}

	// Non-bonus item: Price.Now = priceBeforeBonus, Price.Was = 0
	if item.Product.Price.Was != 0 {
		t.Errorf("expected Was 0 for non-bonus item, got %.2f", item.Product.Price.Was)
	}

	item2 := order.Items[1]
	if item2.ProductID != 371880 {
		t.Errorf("expected productID 371880, got %d", item2.ProductID)
	}
	if !item2.Product.IsBonus {
		t.Error("expected IsBonus true for second item")
	}

	// Bonus item: Price.Now = currentPrice, Price.Was = priceBeforeBonus
	if item2.Product.Price.Now != 1.19 {
		t.Errorf("expected discounted price 1.19, got %.2f", item2.Product.Price.Now)
	}
	if item2.Product.Price.Was != 1.59 {
		t.Errorf("expected Was 1.59, got %.2f", item2.Product.Price.Was)
	}
	if item2.Product.BonusMechanism != "25% korting" {
		t.Errorf("expected BonusMechanism '25%% korting', got %q", item2.Product.BonusMechanism)
	}
}

func TestGetOrderDiscount(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"id":    12345,
			"state": "OPEN",
			"totalPrice": map[string]any{
				"priceBeforeDiscount": 128.20,
				"priceAfterDiscount":  92.68,
				"priceDiscount":       35.52,
				"priceTotalPayable":   92.68,
			},
			"orderedProducts": []map[string]any{
				{
					"amount":   1,
					"quantity": 2,
					"product": map[string]any{
						"webshopId": 111,
						"title":     "Worteltjes",
						"brand":     "AH",
						"images":    []any{},
					},
				},
			},
		})
	}))
	defer srv.Close()

	client := New(WithBaseURL(srv.URL), WithTokens("test", "test"))
	order, err := client.GetOrder(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if order.TotalPrice != 92.68 {
		t.Errorf("expected TotalPrice 92.68, got %.2f", order.TotalPrice)
	}
	if order.TotalDiscount != 35.52 {
		t.Errorf("expected TotalDiscount 35.52, got %.2f", order.TotalDiscount)
	}
}

func TestOrderDetailsTotals(t *testing.T) {
	data, err := os.ReadFile("testdata/order_details.json")
	if err != nil {
		t.Fatalf("read testdata: %v", err)
	}

	var resp orderDetailsResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	order := resp.toOrder()

	if len(order.Items) != 33 {
		t.Fatalf("expected 33 items, got %d", len(order.Items))
	}

	// Verify specific item types are parsed correctly
	findItem := func(title string) *OrderItem {
		for i := range order.Items {
			if order.Items[i].Product.Title == title {
				return &order.Items[i]
			}
		}
		t.Fatalf("item %q not found", title)
		return nil
	}

	// Non-bonus item
	item := findItem("Boursin Sjalot & bieslook")
	if item.Product.Price.Now != 3.79 || item.Product.Price.Was != 0 {
		t.Errorf("non-bonus: Now=%.2f Was=%.2f, want Now=3.79 Was=0", item.Product.Price.Now, item.Product.Price.Was)
	}

	// Percentage discount (has currentPrice)
	item = findItem("AH Biologisch Bleekselderij")
	if item.Product.Price.Now != 1.70 || item.Product.Price.Was != 1.89 {
		t.Errorf("percentage: Now=%.2f Was=%.2f, want Now=1.70 Was=1.89", item.Product.Price.Now, item.Product.Price.Was)
	}

	// Group promotion "2e gratis" (no currentPrice)
	item = findItem("AH Geschrapte worteltjes grootverpakking")
	if item.Quantity != 2 {
		t.Errorf("worteltjes qty=%d, want 2", item.Quantity)
	}
	if item.Product.Price.Now != 1.79 || item.Product.Price.Was != 0 {
		t.Errorf("2e gratis: Now=%.2f Was=%.2f, want Now=1.79 Was=0", item.Product.Price.Now, item.Product.Price.Was)
	}
	if item.Product.BonusMechanism != "2e gratis" {
		t.Errorf("mechanism=%q, want %q", item.Product.BonusMechanism, "2e gratis")
	}

	// Scratch card item (no priceBeforeBonus at all)
	item = findItem("Alpro Barista haver")
	if item.Quantity != 3 {
		t.Errorf("alpro qty=%d, want 3", item.Quantity)
	}
	if item.Product.Price.Now != 0 {
		t.Errorf("scratch card: Now=%.2f, want 0", item.Product.Price.Now)
	}

	// Subtotal from line items (sum of priceBeforeBonus * qty).
	// This is less than the API's true pre-discount total because
	// scratch card items have no price in the details response.
	subtotal := order.Subtotal()
	if math.Abs(subtotal-119.97) > 0.01 {
		t.Errorf("subtotal = %.2f, want 119.97", subtotal)
	}

	// Simulate CLI merging summary data (from GetOrder)
	order.TotalPrice = 92.68
	order.TotalDiscount = 35.52

	// The displayed totals should use API-provided values, not line item math
	if order.TotalDiscount != 35.52 {
		t.Errorf("discount = %.2f, want 35.52", order.TotalDiscount)
	}
	if order.TotalPrice != 92.68 {
		t.Errorf("total to pay = %.2f, want 92.68", order.TotalPrice)
	}
}

func TestAddToOrderBySearch(t *testing.T) {
	var addedItems []struct {
		ProductID int `json:"id"`
		Quantity  int `json:"quantity"`
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/mobile-services/product/search/v2" && r.Method == http.MethodGet:
			json.NewEncoder(w).Encode(map[string]any{
				"products": []map[string]any{
					{
						"webshopId":        12345,
						"title":            "AH Halfvolle melk",
						"brand":            "AH",
						"salesUnitSize":    "1 L",
						"currentPrice":     1.15,
						"priceBeforeBonus": 1.15,
						"availableOnline":  true,
						"isOrderable":      true,
					},
				},
				"page": map[string]any{
					"totalElements": 1,
				},
			})

		// AddToOrder writes the basket via the basketItemsUpdate GraphQL mutation.
		case r.URL.Path == "/graphql" && r.Method == http.MethodPost:
			var body struct {
				Variables struct {
					Items []struct {
						ProductID int `json:"id"`
						Quantity  int `json:"quantity"`
					} `json:"items"`
				} `json:"variables"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode body: %v", err)
				http.Error(w, "bad request", 400)
				return
			}
			addedItems = body.Variables.Items
			json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{"basketItemsUpdate": map[string]any{"__typename": "Basket"}},
			})

		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client := New(WithBaseURL(srv.URL), WithTokens("test", "test"))
	client.SetOrderID(99999) // active order set, so AddToOrder skips the best-effort GetOrder
	ctx := context.Background()

	// Search → should find 1 product
	products, err := client.SearchProducts(ctx, "halfvolle melk", 5)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(products) != 1 {
		t.Fatalf("expected 1 product, got %d", len(products))
	}
	if products[0].ID != 12345 {
		t.Errorf("expected product ID 12345, got %d", products[0].ID)
	}

	// Add to order
	err = client.AddToOrder(ctx, []OrderItem{{ProductID: products[0].ID, Quantity: 3}})
	if err != nil {
		t.Fatalf("add to order: %v", err)
	}

	if len(addedItems) != 1 {
		t.Fatalf("expected 1 item added, got %d", len(addedItems))
	}
	if addedItems[0].ProductID != 12345 {
		t.Errorf("expected product ID 12345, got %d", addedItems[0].ProductID)
	}
	if addedItems[0].Quantity != 3 {
		t.Errorf("expected quantity 3, got %d", addedItems[0].Quantity)
	}
}

func TestGetOrderDetailsNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{
			"code":    "NOT_FOUND",
			"message": "Order not found",
		})
	}))
	defer srv.Close()

	client := New(WithBaseURL(srv.URL), WithTokens("test", "test"))
	ctx := context.Background()

	_, err := client.GetOrderDetails(ctx, 999999)
	if err == nil {
		t.Fatal("expected error for non-existent order")
	}
}

// noRetryDelay zeroes basketReadRetryDelay for the test.
func noRetryDelay(t *testing.T) {
	t.Helper()
	old := basketReadRetryDelay
	basketReadRetryDelay = 0
	t.Cleanup(func() { basketReadRetryDelay = old })
}

func TestAddToOrderChecked(t *testing.T) {
	noRetryDelay(t)

	summary := func(lines map[int]int) map[string]any {
		products := make([]map[string]any, 0, len(lines))
		for id, qty := range lines {
			products = append(products, map[string]any{
				"quantity": qty,
				"product":  map[string]any{"webshopId": id},
			})
		}
		return map[string]any{
			"id":              777,
			"state":           "REOPENED",
			"totalPrice":      map[string]any{"priceTotalPayable": 42.5},
			"orderedProducts": products,
		}
	}

	// 1 lands, 2 is silently rejected by the slot, 3 is capped at 1, 4 is removed,
	// 5 is untouched and must not appear in the result.
	before := map[int]int{4: 1, 5: 2}
	after := map[int]int{1: 2, 3: 1, 5: 2}

	var reads int
	var sent []map[string]int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/mobile-services/order/v1/summaries/active" && r.Method == http.MethodGet:
			reads++
			if reads == 1 {
				json.NewEncoder(w).Encode(summary(before))
				return
			}
			json.NewEncoder(w).Encode(summary(after))
		case r.URL.Path == "/graphql" && r.Method == http.MethodPost:
			var body struct {
				Query     string `json:"query"`
				Variables struct {
					Items []map[string]int `json:"items"`
				} `json:"variables"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode body: %v", err)
			}
			if strings.Contains(body.Query, "basketItemsUpdate") {
				sent = body.Variables.Items
			}
			json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{"basketItemsUpdate": map[string]any{"__typename": "Basket"}},
			})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client := New(WithBaseURL(srv.URL), WithTokens("test", "test"))
	res, err := client.AddToOrderChecked(context.Background(), []OrderItem{
		{ProductID: 1, Quantity: 1},
		{ProductID: 1, Quantity: 1}, // duplicates are merged into qty 2
		{ProductID: 2, Quantity: 1},
		{ProductID: 3, Quantity: 4},
		{ProductID: 4, Quantity: 0},
	})
	if err != nil {
		t.Fatalf("AddToOrderChecked: %v", err)
	}

	if len(sent) != 4 {
		t.Errorf("sent %d items, want 4 (duplicates merged): %v", len(sent), sent)
	}
	// Pre-write read, post-write read, and one retry read because pid 2 is missing.
	if reads != 3 {
		t.Errorf("order reads = %d, want 3", reads)
	}

	want := &BasketWriteResult{
		OrderID:     777,
		Landed:      []BasketLine{{ID: 1, Qty: 2}},
		Rejected:    []int{2},
		QtyMismatch: []BasketQtyMismatch{{ID: 3, Want: 4, Got: 1}},
		Removed:     []int{4},
		TotalItems:  3,
		TotalPrice:  42.5,
	}
	gotJSON, _ := json.Marshal(res)
	wantJSON, _ := json.Marshal(want)
	if string(gotJSON) != string(wantJSON) {
		t.Errorf("result mismatch\n got: %s\nwant: %s", gotJSON, wantJSON)
	}
}

func TestAddToOrderCheckedNoRetryWhenAllLanded(t *testing.T) {
	noRetryDelay(t)

	var reads int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/graphql" {
			json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{"basketItemsUpdate": map[string]any{"__typename": "Basket"}},
			})
			return
		}
		reads++
		json.NewEncoder(w).Encode(map[string]any{
			"id": 777,
			"orderedProducts": []map[string]any{
				{"quantity": 1, "product": map[string]any{"webshopId": 9}},
			},
		})
	}))
	defer srv.Close()

	client := New(WithBaseURL(srv.URL), WithTokens("test", "test"))
	res, err := client.AddToOrderChecked(context.Background(), []OrderItem{{ProductID: 9, Quantity: 1}})
	if err != nil {
		t.Fatalf("AddToOrderChecked: %v", err)
	}
	if reads != 2 {
		t.Errorf("order reads = %d, want 2", reads)
	}
	if len(res.Landed) != 1 || len(res.Rejected) != 0 {
		t.Errorf("unexpected result: %+v", res)
	}
}

func TestGetFulfillmentsCutoff(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Query string `json:"query"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		for _, field := range []string{"closingDateTime", "isAfterCutOff", "reopenable"} {
			if !strings.Contains(body.Query, field) {
				t.Errorf("fulfillments query lacks %s", field)
			}
		}
		json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{"orderFulfillments": map[string]any{"result": []map[string]any{{
				"orderId":         777,
				"statusCode":      2,
				"modifiable":      true,
				"reopenable":      true,
				"isAfterCutOff":   false,
				"closingDateTime": "2026-10-06T22:59:00Z",
			}}}},
		})
	}))
	defer srv.Close()

	client := New(WithBaseURL(srv.URL), WithTokens("test", "test"))
	got, err := client.GetFulfillments(context.Background())
	if err != nil {
		t.Fatalf("GetFulfillments: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d fulfillments, want 1", len(got))
	}
	f := got[0]
	if f.ClosingTime != "2026-10-06T22:59:00Z" || !f.Reopenable || f.IsAfterCutOff {
		t.Errorf("cutoff fields not decoded: %+v", f)
	}
	if f.StatusCode != 2 {
		t.Errorf("StatusCode = %d, want 2", f.StatusCode)
	}
}

func TestGetOrderDetailsClosingTime(t *testing.T) {
	data, err := os.ReadFile("testdata/order_details.json")
	if err != nil {
		t.Fatalf("read testdata: %v", err)
	}
	var resp orderDetailsResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got := resp.toOrder().ClosingTime; got != "2026-02-23T22:59:00Z" {
		t.Errorf("ClosingTime = %q, want 2026-02-23T22:59:00Z", got)
	}
}

// basketServer serves the active-order summary from reads (one response per
// read, the last one repeated) and accepts basket mutations, recording the
// active-order header each mutation carried.
func basketServer(t *testing.T, orderID int, reads ...map[int]int) (srv *httptest.Server, readCount func() int, mutationOrder func() string) {
	t.Helper()
	var n int
	var header string
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/graphql" {
			header = r.Header.Get("appie-current-order-id")
			json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{"basketItemsUpdate": map[string]any{"__typename": "Basket"}},
			})
			return
		}
		lines := reads[min(n, len(reads)-1)]
		n++
		products := make([]map[string]any, 0, len(lines))
		for id, qty := range lines {
			products = append(products, map[string]any{"quantity": qty, "product": map[string]any{"webshopId": id}})
		}
		json.NewEncoder(w).Encode(map[string]any{"id": orderID, "orderedProducts": products})
	}))
	t.Cleanup(srv.Close)
	return srv, func() int { return n }, func() string { return header }
}

func TestAddToOrderCheckedLaggingBasket(t *testing.T) {
	noRetryDelay(t)
	// Pre-write read, read-back without the item, retry with the item.
	srv, reads, _ := basketServer(t, 777, map[int]int{}, map[int]int{}, map[int]int{9: 1})

	client := New(WithBaseURL(srv.URL), WithTokens("test", "test"))
	res, err := client.AddToOrderChecked(context.Background(), []OrderItem{{ProductID: 9, Quantity: 1}})
	if err != nil {
		t.Fatalf("AddToOrderChecked: %v", err)
	}
	if reads() != 3 {
		t.Errorf("order reads = %d, want 3", reads())
	}
	if len(res.Landed) != 1 || res.Landed[0] != (BasketLine{ID: 9, Qty: 1}) || len(res.Rejected) != 0 {
		t.Errorf("want 9 landed after the retry, got %+v", res)
	}
}

func TestAddToOrderCheckedFromSkipsPreRead(t *testing.T) {
	noRetryDelay(t)
	srv, reads, mutationOrder := basketServer(t, 777, map[int]int{9: 1})

	client := New(WithBaseURL(srv.URL), WithTokens("test", "test"))
	client.SetOrderID(555) // stale cached order must not receive the write
	res, err := client.AddToOrderCheckedFrom(context.Background(), &Order{ID: "777"}, []OrderItem{{ProductID: 9, Quantity: 1}})
	if err != nil {
		t.Fatalf("AddToOrderCheckedFrom: %v", err)
	}
	if reads() != 1 {
		t.Errorf("order reads = %d, want only the read-back", reads())
	}
	if got := mutationOrder(); got != "777" {
		t.Errorf("mutation sent for order %q, want 777", got)
	}
	if res.OrderID != 777 {
		t.Errorf("OrderID = %d, want 777", res.OrderID)
	}
}

func TestAddToOrderCheckedFromOrderChanged(t *testing.T) {
	noRetryDelay(t)
	srv, _, _ := basketServer(t, 888, map[int]int{9: 1})

	client := New(WithBaseURL(srv.URL), WithTokens("test", "test"))
	_, err := client.AddToOrderCheckedFrom(context.Background(), &Order{ID: "777"}, []OrderItem{{ProductID: 9, Quantity: 1}})
	if err == nil || !strings.Contains(err.Error(), "888") {
		t.Fatalf("want an error naming the other active order, got %v", err)
	}
}

func TestAddToOrderCheckedReadBackFailure(t *testing.T) {
	var mutated bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/graphql" {
			mutated = true
			json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{"basketItemsUpdate": map[string]any{"__typename": "Basket"}},
			})
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := New(WithBaseURL(srv.URL), WithTokens("test", "test"))
	_, err := client.AddToOrderCheckedFrom(context.Background(), &Order{ID: "777"}, []OrderItem{{ProductID: 9, Quantity: 1}})
	if !mutated {
		t.Fatal("mutation was not sent")
	}
	if err == nil || !strings.Contains(err.Error(), "items were sent") || !strings.Contains(err.Error(), "verif") {
		t.Errorf("error = %v, want it to say the items were sent and only verification failed", err)
	}
}

func TestGetFulfillmentsFallsBackOnFieldError(t *testing.T) {
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Query string `json:"query"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		queries = append(queries, body.Query)
		if strings.Contains(body.Query, "reopenable") {
			json.NewEncoder(w).Encode(map[string]any{
				"errors": []map[string]any{{"message": `Validation error (FieldUndefined@[orderFulfillments/result/reopenable]) : Field 'reopenable' in type 'OrderFulfillment' is undefined`}},
			})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{"orderFulfillments": map[string]any{"result": []map[string]any{{
				"orderId":    777,
				"modifiable": true,
				"totalPrice": map[string]any{"totalPrice": map[string]any{"amount": 12.5}},
			}}}},
		})
	}))
	defer srv.Close()

	client := New(WithBaseURL(srv.URL), WithTokens("test", "test"))
	got, err := client.GetFulfillments(context.Background())
	if err != nil {
		t.Fatalf("GetFulfillments: %v", err)
	}
	if len(queries) != 2 {
		t.Fatalf("queries = %d, want the extended query then one fallback", len(queries))
	}
	for _, field := range []string{"reopenable", "isAfterCutOff", "closingDateTime"} {
		if strings.Contains(queries[1], field) {
			t.Errorf("fallback query still selects %s", field)
		}
	}
	if len(got) != 1 || got[0].OrderID != 777 || !got[0].Modifiable || got[0].TotalPrice != 12.5 {
		t.Errorf("fallback result = %+v", got)
	}
	if got[0].ClosingTime != "" || got[0].Reopenable || got[0].IsAfterCutOff {
		t.Errorf("new fields must stay empty on fallback: %+v", got[0])
	}
}

func TestGetFulfillmentsNoFallbackOnTransportError(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := New(WithBaseURL(srv.URL), WithTokens("test", "test"))
	if _, err := client.GetFulfillments(context.Background()); err == nil {
		t.Fatal("want an error")
	}
	if calls != 1 {
		t.Errorf("calls = %d, want no fallback retry on an HTTP error", calls)
	}
}

func TestGetFulfillmentsNoFallbackOnOtherGraphQLError(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		json.NewEncoder(w).Encode(map[string]any{
			"errors": []map[string]any{{"message": "Unauthorized"}},
		})
	}))
	defer srv.Close()

	client := New(WithBaseURL(srv.URL), WithTokens("test", "test"))
	_, err := client.GetFulfillments(context.Background())
	if err == nil || !strings.Contains(err.Error(), "Unauthorized") {
		t.Fatalf("err = %v, want the original GraphQL error", err)
	}
	if calls != 1 {
		t.Errorf("calls = %d, want no fallback retry on an unrelated GraphQL error", calls)
	}
}

// basketAvailabilityItem builds one basket line the way the mobile gql basket
// query returns it. A nil allocated encodes allocatedQuantity: null.
func basketAvailabilityItem(id, qty int, allocated *int, orderable bool, label any) map[string]any {
	return map[string]any{
		"id":                id,
		"quantity":          qty,
		"allocatedQuantity": allocated,
		"product": map[string]any{
			"title":             fmt.Sprintf("product %d", id),
			"availabilityLabel": label,
			"availability":      map[string]any{"isOrderable": orderable},
		},
	}
}

func TestGetBasketUnavailable(t *testing.T) {
	zero := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Query string `json:"query"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		for _, field := range []string{"basket", "allocatedQuantity", "isOrderable"} {
			if !strings.Contains(body.Query, field) {
				t.Errorf("basket query lacks %s", field)
			}
		}
		// Shapes seen live on 2026-10-28 for "Alleen in de winkel" (null
		// allocation) and "Tijdelijk uitverkocht" (allocated 0, not orderable).
		json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"basket": map[string]any{"items": []map[string]any{
			basketAvailabilityItem(367237, 1, nil, false, "Uit het assortiment"),
			basketAvailabilityItem(368480, 1, &zero, true, nil),
			basketAvailabilityItem(588943, 2, &zero, false, nil),
			basketAvailabilityItem(606837, 1, nil, false, nil),
		}}}})
	}))
	defer srv.Close()

	client := New(WithBaseURL(srv.URL), WithTokens("test", "test"))
	got, err := client.GetBasketUnavailable(context.Background())
	if err != nil {
		t.Fatalf("GetBasketUnavailable: %v", err)
	}
	want := []BasketUnavailable{
		{ProductID: 367237, Title: "product 367237", Quantity: 1, Reason: UnavailableStoreOnly, Label: "Uit het assortiment"},
		{ProductID: 588943, Title: "product 588943", Quantity: 2, Reason: UnavailableSoldOut},
		{ProductID: 606837, Title: "product 606837", Quantity: 1, Reason: UnavailableStoreOnly},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("unavailable mismatch\n got: %+v\nwant: %+v", got, want)
	}
}

// soldOutServer serves an order that holds lines, and a gql basket in which
// soldOut lines are allocated 0 and not orderable. A non-empty basketErr makes
// the basket query fail with that GraphQL error.
func soldOutServer(t *testing.T, lines map[int]int, soldOut map[int]bool, basketErr string) *httptest.Server {
	t.Helper()
	zero := 0
	reads := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/graphql" {
			products := make([]map[string]any, 0, len(lines))
			if reads > 0 {
				for id, qty := range lines {
					products = append(products, map[string]any{"quantity": qty, "product": map[string]any{"webshopId": id}})
				}
			}
			reads++
			json.NewEncoder(w).Encode(map[string]any{"id": 777, "orderedProducts": products})
			return
		}
		var body struct {
			Query string `json:"query"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		if strings.Contains(body.Query, "basketItemsUpdate") {
			json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{"basketItemsUpdate": map[string]any{"__typename": "Basket"}},
			})
			return
		}
		if basketErr != "" {
			json.NewEncoder(w).Encode(map[string]any{"errors": []map[string]any{{"message": basketErr}}})
			return
		}
		items := make([]map[string]any, 0, len(lines))
		for id, qty := range lines {
			items = append(items, basketAvailabilityItem(id, qty, &zero, !soldOut[id], nil))
		}
		json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"basket": map[string]any{"items": items}}})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestAddToOrderCheckedSoldOut(t *testing.T) {
	noRetryDelay(t)
	srv := soldOutServer(t, map[int]int{1: 1, 2: 2}, map[int]bool{2: true}, "")

	client := New(WithBaseURL(srv.URL), WithTokens("test", "test"))
	res, err := client.AddToOrderChecked(context.Background(), []OrderItem{
		{ProductID: 1, Quantity: 1},
		{ProductID: 2, Quantity: 2},
	})
	if err != nil {
		t.Fatalf("AddToOrderChecked: %v", err)
	}
	if !reflect.DeepEqual(res.Landed, []BasketLine{{ID: 1, Qty: 1}}) {
		t.Errorf("Landed = %+v, want only 1", res.Landed)
	}
	if !reflect.DeepEqual(res.SoldOut, []int{2}) {
		t.Errorf("SoldOut = %v, want [2]", res.SoldOut)
	}
	if res.AvailabilityError != "" {
		t.Errorf("AvailabilityError = %q, want empty", res.AvailabilityError)
	}
}

func TestAddToOrderCheckedAvailabilityLookupFails(t *testing.T) {
	noRetryDelay(t)
	srv := soldOutServer(t, map[int]int{1: 1}, nil, "basket unavailable")

	client := New(WithBaseURL(srv.URL), WithTokens("test", "test"))
	res, err := client.AddToOrderChecked(context.Background(), []OrderItem{{ProductID: 1, Quantity: 1}})
	if err != nil {
		t.Fatalf("a failed availability lookup must not fail the write: %v", err)
	}
	if !reflect.DeepEqual(res.Landed, []BasketLine{{ID: 1, Qty: 1}}) || len(res.SoldOut) != 0 {
		t.Errorf("want 1 landed and nothing sold out, got %+v", res)
	}
	if !strings.Contains(res.AvailabilityError, "basket unavailable") {
		t.Errorf("AvailabilityError = %q, want the gql error", res.AvailabilityError)
	}
}
