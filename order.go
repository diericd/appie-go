package appie

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// orderDetailsResponse matches the API response for order details grouped by taxonomy.
type orderDetailsResponse struct {
	OrderID      int    `json:"orderId"`
	OrderState   string `json:"orderState"`
	ClosingTime  string `json:"closingTime"`
	DeliveryDate string `json:"deliveryDate"`
	DeliveryType string `json:"deliveryType"`
	DeliveryTime struct {
		StartDateTime string `json:"startDateTime"`
		EndDateTime   string `json:"endDateTime"`
	} `json:"deliveryTimePeriod"`
	GroupedProducts []struct {
		TaxonomyName    string `json:"taxonomyName"`
		OrderedProducts []struct {
			Amount   int `json:"amount"`
			Quantity int `json:"quantity"`
			Product  struct {
				WebshopID        int      `json:"webshopId"`
				Title            string   `json:"title"`
				Brand            string   `json:"brand"`
				SalesUnitSize    string   `json:"salesUnitSize"`
				PriceBeforeBonus float64  `json:"priceBeforeBonus"`
				CurrentPrice     *float64 `json:"currentPrice"` // discounted price, nil when discount is per-group (e.g. "2e gratis")
				IsBonus          bool     `json:"isBonus"`
				BonusMechanism   string   `json:"bonusMechanism"`
			} `json:"product"`
		} `json:"orderedProducts"`
	} `json:"groupedProductsInTaxonomy"`
}

func (r *orderDetailsResponse) toOrder() Order {
	var items []OrderItem
	for _, group := range r.GroupedProducts {
		for _, op := range group.OrderedProducts {
			price := Price{Now: op.Product.PriceBeforeBonus}
			if op.Product.CurrentPrice != nil {
				price = Price{Now: *op.Product.CurrentPrice, Was: op.Product.PriceBeforeBonus}
			}
			items = append(items, OrderItem{
				ProductID: op.Product.WebshopID,
				Quantity:  op.Quantity,
				Product: &Product{
					ID:             op.Product.WebshopID,
					Title:          op.Product.Title,
					Brand:          op.Product.Brand,
					UnitSize:       op.Product.SalesUnitSize,
					Price:          price,
					IsBonus:        op.Product.IsBonus,
					BonusMechanism: op.Product.BonusMechanism,
				},
			})
		}
	}

	return Order{
		ID:          strconv.Itoa(r.OrderID),
		State:       r.OrderState,
		ClosingTime: r.ClosingTime,
		Items:       items,
		TotalCount:  len(items),
	}
}

// orderSummaryResponse matches the API response for active order summary.
type orderSummaryResponse struct {
	ID           int    `json:"id"`
	State        string `json:"state"`
	ShoppingType string `json:"shoppingType"`
	TotalPrice   struct {
		PriceBeforeDiscount float64 `json:"priceBeforeDiscount"`
		PriceAfterDiscount  float64 `json:"priceAfterDiscount"`
		PriceDiscount       float64 `json:"priceDiscount"`
		PriceTotalPayable   float64 `json:"priceTotalPayable"`
	} `json:"totalPrice"`
	DeliveryInformation struct {
		DeliveryDate      string `json:"deliveryDate"`
		DeliveryStartTime string `json:"deliveryStartTime"`
		DeliveryEndTime   string `json:"deliveryEndTime"`
		Address           struct {
			Street      string `json:"street"`
			HouseNumber int    `json:"houseNumber"`
			ZipCode     int    `json:"zipCode"`
			City        string `json:"city"`
		} `json:"address"`
	} `json:"deliveryInformation"`
	OrderedProducts []struct {
		Amount   int `json:"amount"`
		Quantity int `json:"quantity"`
		Product  struct {
			WebshopID int    `json:"webshopId"`
			Title     string `json:"title"`
			Brand     string `json:"brand"`
			Images    []struct {
				URL string `json:"url"`
			} `json:"images"`
		} `json:"product"`
	} `json:"orderedProducts"`
}

func (r *orderSummaryResponse) toOrder() Order {
	items := make([]OrderItem, 0, len(r.OrderedProducts))
	for _, op := range r.OrderedProducts {
		var images []Image
		for _, img := range op.Product.Images {
			images = append(images, Image{URL: img.URL})
		}
		items = append(items, OrderItem{
			ProductID: op.Product.WebshopID,
			Quantity:  op.Quantity,
			Product: &Product{
				ID:     op.Product.WebshopID,
				Title:  op.Product.Title,
				Brand:  op.Product.Brand,
				Images: images,
			},
		})
	}

	return Order{
		ID:            strconv.Itoa(r.ID),
		State:         r.State,
		Items:         items,
		TotalCount:    len(items),
		TotalPrice:    r.TotalPrice.PriceTotalPayable,
		TotalDiscount: r.TotalPrice.PriceDiscount,
	}
}

// GetOrder retrieves the current active order (shopping cart).
// The order ID is cached for use in subsequent order operations.
func (c *Client) GetOrder(ctx context.Context) (*Order, error) {
	var result orderSummaryResponse
	if err := c.DoRequest(ctx, http.MethodGet, "/mobile-services/order/v1/summaries/active?sortBy=DEFAULT", nil, &result); err != nil {
		return nil, fmt.Errorf("get order failed: %w", err)
	}

	// Store order ID for subsequent requests
	c.mu.Lock()
	c.orderID = strconv.Itoa(result.ID)
	c.mu.Unlock()

	order := result.toOrder()
	return &order, nil
}

// GetOrderDetails retrieves the details of a specific order by ID.
// Unlike GetOrder, this works for any order (not just the active one) and
// returns products grouped by taxonomy (category).
func (c *Client) GetOrderDetails(ctx context.Context, orderID int) (*Order, error) {
	path := fmt.Sprintf("/mobile-services/order/v1/%d/details-grouped-by-taxonomy", orderID)

	var result orderDetailsResponse
	if err := c.DoRequest(ctx, http.MethodGet, path, nil, &result); err != nil {
		return nil, fmt.Errorf("get order details failed: %w", err)
	}

	order := result.toOrder()
	return &order, nil
}

// SetOrderID manually sets the order ID for subsequent API requests.
// This is useful when selecting a specific order from fulfillments.
func (c *Client) SetOrderID(id int) {
	c.mu.Lock()
	c.orderID = strconv.Itoa(id)
	c.mu.Unlock()
}

// basketItemsUpdateMutation is the modern GraphQL basket write. It replaces the
// deprecated REST PUT /mobile-services/order/v1/items, which returns
// "Server not in order mode" on ah.be accounts. The mobile gql gateway accepts
// this mutation with the bearer token; quantity 0 removes an item.
const basketItemsUpdateMutation = `mutation basketItemsUpdate($items: [BasketMutation!]!) {
  basketItemsUpdate(items: $items) {
    __typename
  }
}`

// AddToOrder adds or updates items in the current order (basket).
// If an item already exists, its quantity is updated. Set quantity to 0 to remove.
//
// Example:
//
//	err := client.AddToOrder(ctx, []appie.OrderItem{
//	    {ProductID: 123456, Quantity: 2},
//	})
func (c *Client) AddToOrder(ctx context.Context, items []OrderItem) error {
	// Ensure the active-order context (appie-current-order-id header) is set.
	c.mu.RLock()
	hasOrder := c.orderID != ""
	c.mu.RUnlock()
	if !hasOrder {
		_, _ = c.GetOrder(ctx) // best-effort; mutation may resolve the basket from the token alone
	}

	merged := mergeOrderItems(items)
	reqItems := make([]map[string]any, 0, len(merged))
	for _, it := range merged {
		reqItems = append(reqItems, map[string]any{
			"id":       it.ProductID,
			"quantity": it.Quantity,
		})
	}

	vars := map[string]any{"items": reqItems}
	if err := c.DoGraphQL(ctx, basketItemsUpdateMutation, vars, nil); err != nil {
		return fmt.Errorf("add to order failed: %w", err)
	}

	return nil
}

// mergeOrderItems sums the quantities of duplicate product IDs (the API rejects
// duplicates), keeping first-seen order.
func mergeOrderItems(items []OrderItem) []OrderItem {
	idx := make(map[int]int, len(items))
	merged := make([]OrderItem, 0, len(items))
	for _, item := range items {
		if i, ok := idx[item.ProductID]; ok {
			merged[i].Quantity += item.Quantity
			continue
		}
		idx[item.ProductID] = len(merged)
		merged = append(merged, OrderItem{ProductID: item.ProductID, Quantity: item.Quantity})
	}
	return merged
}

// basketReadRetryDelay is how long AddToOrderChecked waits before re-reading
// the order when a sent item is missing, in case the basket lags the mutation.
var basketReadRetryDelay = 500 * time.Millisecond

// AddToOrderChecked runs AddToOrder and reads the order back to report which
// items actually landed. The mutation succeeds even when the delivery slot
// silently rejects an item, so the read-back is the only reliable signal.
func (c *Client) AddToOrderChecked(ctx context.Context, items []OrderItem) (*BasketWriteResult, error) {
	before, err := c.GetOrder(ctx)
	if err != nil {
		return nil, err
	}
	return c.AddToOrderCheckedFrom(ctx, before, items)
}

// AddToOrderCheckedFrom is AddToOrderChecked for a caller that already read
// the active order as before. The write targets before's order, and it fails
// if the read-back shows a different active order.
func (c *Client) AddToOrderCheckedFrom(ctx context.Context, before *Order, items []OrderItem) (*BasketWriteResult, error) {
	orderID, err := strconv.Atoi(before.ID)
	if err != nil {
		return nil, fmt.Errorf("invalid order id %q: %w", before.ID, err)
	}
	c.SetOrderID(orderID)
	if err := c.AddToOrder(ctx, items); err != nil {
		return nil, err
	}

	merged := mergeOrderItems(items)
	after, err := c.readBack(ctx)
	if err != nil {
		return nil, err
	}
	if hasMissingItem(merged, after) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(basketReadRetryDelay):
		}
		if after, err = c.readBack(ctx); err != nil {
			return nil, err
		}
	}
	if after.ID != before.ID {
		return nil, fmt.Errorf("items were sent to order %s, but the active order is now %s; check both orders", before.ID, after.ID)
	}

	res := diffBasketWrite(merged, after)
	res.OrderID = orderID
	return res, nil
}

func (c *Client) readBack(ctx context.Context) (*Order, error) {
	o, err := c.GetOrder(ctx)
	if err != nil {
		return nil, fmt.Errorf("items were sent, but reading the order back to verify them failed: %w", err)
	}
	return o, nil
}

func orderQuantities(o *Order) map[int]int {
	qty := make(map[int]int, len(o.Items))
	for _, it := range o.Items {
		qty[it.ProductID] += it.Quantity
	}
	return qty
}

func hasMissingItem(sent []OrderItem, o *Order) bool {
	qty := orderQuantities(o)
	for _, it := range sent {
		if it.Quantity > 0 && qty[it.ProductID] == 0 {
			return true
		}
	}
	return false
}

func diffBasketWrite(sent []OrderItem, o *Order) *BasketWriteResult {
	qty := orderQuantities(o)
	res := &BasketWriteResult{TotalItems: len(o.Items), TotalPrice: o.TotalPrice}
	for _, it := range sent {
		got := qty[it.ProductID]
		switch {
		case got == it.Quantity && got == 0:
			res.Removed = append(res.Removed, it.ProductID)
		case got == it.Quantity:
			res.Landed = append(res.Landed, BasketLine{ID: it.ProductID, Qty: got})
		case got == 0:
			res.Rejected = append(res.Rejected, it.ProductID)
		default:
			res.QtyMismatch = append(res.QtyMismatch, BasketQtyMismatch{ID: it.ProductID, Want: it.Quantity, Got: got})
		}
	}
	return res
}

// RemoveFromOrder removes an item from the current order by setting quantity to 0.
func (c *Client) RemoveFromOrder(ctx context.Context, productID int) error {
	return c.AddToOrder(ctx, []OrderItem{{ProductID: productID, Quantity: 0}})
}

// UpdateOrderItem updates the quantity of an item in the order.
func (c *Client) UpdateOrderItem(ctx context.Context, productID, quantity int) error {
	return c.AddToOrder(ctx, []OrderItem{{ProductID: productID, Quantity: quantity}})
}

// ClearOrder removes all items from the current order.
func (c *Client) ClearOrder(ctx context.Context) error {
	order, err := c.GetOrder(ctx)
	if err != nil {
		return err
	}

	if len(order.Items) == 0 {
		return nil
	}

	removals := make([]OrderItem, 0, len(order.Items))
	for _, item := range order.Items {
		removals = append(removals, OrderItem{ProductID: item.ProductID, Quantity: 0})
	}

	return c.AddToOrder(ctx, removals)
}

// GetOrderSummary retrieves the order summary/totals.
func (c *Client) GetOrderSummary(ctx context.Context) (*OrderSummary, error) {
	var result orderSummaryResponse
	if err := c.DoRequest(ctx, http.MethodGet, "/mobile-services/order/v1/summaries/active?sortBy=DEFAULT", nil, &result); err != nil {
		return nil, fmt.Errorf("get order summary failed: %w", err)
	}

	return &OrderSummary{
		TotalItems:    len(result.OrderedProducts),
		TotalPrice:    result.TotalPrice.PriceTotalPayable,
		TotalDiscount: result.TotalPrice.PriceDiscount,
	}, nil
}

const reopenOrderMutation = `mutation OrderReopen($id: Int!) {
  orderReopen(id: $id) {
    status
    errorMessage
  }
}`

// ReopenOrder reopens a submitted order so items can be added or modified.
// This is required before calling AddToOrder on a fulfillment-selected order.
func (c *Client) ReopenOrder(ctx context.Context, orderID int) error {
	type reopenResponse struct {
		OrderReopen struct {
			Status       string `json:"status"`
			ErrorMessage string `json:"errorMessage"`
		} `json:"orderReopen"`
	}

	var resp reopenResponse
	vars := map[string]any{"id": orderID}
	if err := c.DoGraphQL(ctx, reopenOrderMutation, vars, &resp); err != nil {
		return fmt.Errorf("reopen order failed: %w", err)
	}

	if resp.OrderReopen.Status != "SUCCESS" {
		return fmt.Errorf("reopen order failed: %s", resp.OrderReopen.ErrorMessage)
	}

	return nil
}

const revertOrderMutation = `mutation OrderRevert($id: Int!) {
  orderRevert(id: $id) {
    status
    errorMessage
  }
}`

// RevertOrder reverts a reopened order to its pre-reopen submitted state,
// discarding edits made since ReopenOrder, and clears the active order.
func (c *Client) RevertOrder(ctx context.Context, orderID int) error {
	type revertResponse struct {
		OrderRevert struct {
			Status       string `json:"status"`
			ErrorMessage string `json:"errorMessage"`
		} `json:"orderRevert"`
	}

	var resp revertResponse
	vars := map[string]any{"id": orderID}
	if err := c.DoGraphQL(ctx, revertOrderMutation, vars, &resp); err != nil {
		return fmt.Errorf("revert order failed: %w", err)
	}

	if resp.OrderRevert.Status != "SUCCESS" {
		return fmt.Errorf("revert order failed: %s", resp.OrderRevert.ErrorMessage)
	}

	// Clear client-side order state
	c.mu.Lock()
	c.orderID = ""
	c.orderHash = ""
	c.mu.Unlock()

	return nil
}

// fulfillmentsCutoffFields come from a 2026-01 schema dump, not a live
// response, so GetFulfillments retries without them if AH rejects the query.
const fulfillmentsCutoffFields = `
      reopenable
      isAfterCutOff
      closingDateTime`

const fulfillmentsQueryTemplate = `query OrderFulfillments {
  orderFulfillments(status: OPEN) {
    result {
      orderId
      statusCode
      statusDescription
      shoppingType
      transactionCompleted
      modifiable%s
      totalPrice {
        totalPrice { amount }
      }
      delivery {
        status
        method
        slot {
          date
          dateDisplay
          timeDisplay
          startTime
          endTime
        }
        address {
          street
          houseNumber
          houseNumberExtra
          city
          postalCode
        }
      }
    }
  }
}`

var (
	fulfillmentsQuery       = fmt.Sprintf(fulfillmentsQueryTemplate, fulfillmentsCutoffFields)
	fulfillmentsQueryLegacy = fmt.Sprintf(fulfillmentsQueryTemplate, "")
)

type fulfillmentsResponse struct {
	OrderFulfillments struct {
		Result []fulfillmentResult `json:"result"`
	} `json:"orderFulfillments"`
}

type fulfillmentResult struct {
	OrderID              int    `json:"orderId"`
	StatusCode           int    `json:"statusCode"`
	StatusDescription    string `json:"statusDescription"`
	ShoppingType         string `json:"shoppingType"`
	TransactionCompleted bool   `json:"transactionCompleted"`
	Modifiable           bool   `json:"modifiable"`
	Reopenable           bool   `json:"reopenable"`
	IsAfterCutOff        bool   `json:"isAfterCutOff"`
	ClosingDateTime      string `json:"closingDateTime"`
	TotalPrice           struct {
		TotalPrice struct {
			Amount float64 `json:"amount"`
		} `json:"totalPrice"`
	} `json:"totalPrice"`
	Delivery FulfillmentDelivery `json:"delivery"`
}

// isCutoffFieldError reports whether a GraphQL error is AH rejecting the
// cutoff fields, so other GraphQL errors are not hidden by the fallback.
func isCutoffFieldError(e graphQLError) bool {
	msg := strings.ToLower(e.Message)
	for _, s := range []string{"reopenable", "isaftercutoff", "closingdatetime", "fieldundefined", "validation"} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

// GetFulfillments retrieves all open (scheduled) order fulfillments.
// These are orders that have been submitted and are awaiting delivery.
// If AH rejects the cutoff fields, Reopenable, IsAfterCutOff and ClosingTime
// are left empty.
func (c *Client) GetFulfillments(ctx context.Context) ([]Fulfillment, error) {
	var resp fulfillmentsResponse
	err := c.DoGraphQL(ctx, fulfillmentsQuery, nil, &resp)
	if gqlErr := (graphQLError{}); errors.As(err, &gqlErr) && isCutoffFieldError(gqlErr) {
		c.logger.Printf("fulfillments: retrying without cutoff fields: %v", err)
		resp = fulfillmentsResponse{}
		err = c.DoGraphQL(ctx, fulfillmentsQueryLegacy, nil, &resp)
	}
	if err != nil {
		return nil, fmt.Errorf("get fulfillments failed: %w", err)
	}

	results := resp.OrderFulfillments.Result
	fulfillments := make([]Fulfillment, 0, len(results))

	for _, r := range results {
		fulfillments = append(fulfillments, Fulfillment{
			OrderID:              r.OrderID,
			Status:               r.Delivery.Status,
			StatusCode:           r.StatusCode,
			StatusDescription:    r.StatusDescription,
			ShoppingType:         r.ShoppingType,
			TotalPrice:           r.TotalPrice.TotalPrice.Amount,
			TransactionCompleted: r.TransactionCompleted,
			Modifiable:           r.Modifiable,
			Reopenable:           r.Reopenable,
			IsAfterCutOff:        r.IsAfterCutOff,
			ClosingTime:          r.ClosingDateTime,
			Delivery:             r.Delivery,
		})
	}

	return fulfillments, nil
}
