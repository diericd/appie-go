package appie

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// listResponse matches the API response for favorite lists (v3).
type listResponse struct {
	ID                 string `json:"id"`
	Description        string `json:"description"`
	ItemCount          int    `json:"itemCount"`
	HasFavoriteProduct bool   `json:"hasFavoriteProduct"`
	ProductImages      [][]struct {
		Width  int    `json:"width"`
		Height int    `json:"height"`
		URL    string `json:"url"`
	} `json:"productImages"`
}

// shoppingListItem is the v2 request body format for adding items.
type shoppingListItem struct {
	Description   string `json:"description"`
	ProductID     int    `json:"productId,omitempty"`
	Quantity      int    `json:"quantity"`
	Type          string `json:"type"`
	OriginCode    string `json:"originCode"`
	SearchTerm    string `json:"searchTerm,omitempty"`
	StrikeThrough bool   `json:"strikeThrough"`
}

// GetShoppingLists retrieves all favorite lists (v3) for the authenticated user.
// The API quirk requires a productId parameter, but returns all lists regardless.
// Pass 0 to use a default product ID (recommended).
func (c *Client) GetShoppingLists(ctx context.Context, productID int) ([]ShoppingList, error) {
	if productID <= 0 {
		productID = 1 // Default product ID - API requires it but returns all lists
	}
	path := fmt.Sprintf("/mobile-services/lists/v3/lists?productId=%d", productID)

	var result []listResponse
	if err := c.DoRequest(ctx, http.MethodGet, path, nil, &result); err != nil {
		return nil, fmt.Errorf("get shopping lists failed: %w", err)
	}

	lists := make([]ShoppingList, 0, len(result))
	for _, r := range result {
		lists = append(lists, ShoppingList{
			ID:        r.ID,
			Name:      r.Description,
			ItemCount: r.ItemCount,
		})
	}

	return lists, nil
}

// GetShoppingListItems retrieves all items for a specific list (v2 GraphQL).
func (c *Client) GetShoppingListItems(ctx context.Context, listID string) ([]ListItem, error) {
	const query = `query FavoriteListV2($ids: [String!]!) {
  favoriteListV2(ids: $ids) {
    id
    description
    totalSize
    items {
      id
      productId
      quantity
    }
  }
}`

	variables := map[string]any{
		"ids": []string{strings.ToUpper(listID)},
	}

	type listItemResult struct {
		ID        string `json:"id"`
		ProductID int    `json:"productId"`
		Quantity  int    `json:"quantity"`
	}
	type listResult struct {
		ID          string           `json:"id"`
		Description string           `json:"description"`
		TotalSize   int              `json:"totalSize"`
		Items       []listItemResult `json:"items"`
	}

	// API may return a single object or array; try array first.
	var arrResult struct {
		FavoriteListV2 []listResult `json:"favoriteListV2"`
	}
	if err := c.DoGraphQL(ctx, query, variables, &arrResult); err != nil {
		return nil, fmt.Errorf("get shopping list items failed: %w", err)
	}

	if len(arrResult.FavoriteListV2) == 0 {
		return nil, fmt.Errorf("list %s not found", listID)
	}

	raw := arrResult.FavoriteListV2[0].Items
	items := make([]ListItem, 0, len(raw))
	for _, item := range raw {
		items = append(items, ListItem{
			ID:        item.ID,
			ProductID: item.ProductID,
			Quantity:  max(item.Quantity, 1),
		})
	}

	return items, nil
}

// AddToShoppingList adds products to the main shopping list (v2).
// This uses PATCH /shoppinglist/v2/items.
func (c *Client) AddToShoppingList(ctx context.Context, items []ListItem) error {
	v2Items := make([]shoppingListItem, 0, len(items))
	for _, item := range items {
		v2 := shoppingListItem{
			Quantity:      max(item.Quantity, 1),
			StrikeThrough: false,
		}
		if item.ProductID > 0 {
			v2.ProductID = item.ProductID
			v2.Type = "SHOPPABLE"
			v2.OriginCode = "PRD"
			v2.Description = item.Name
			v2.SearchTerm = item.Name
		} else {
			v2.Type = "SHOPPABLE"
			v2.OriginCode = "PRD"
			v2.Description = item.Name
		}
		v2Items = append(v2Items, v2)
	}

	body := map[string]any{
		"items": v2Items,
	}

	if err := c.DoRequest(ctx, http.MethodPatch, "/mobile-services/shoppinglist/v2/items", body, nil); err != nil {
		return fmt.Errorf("add to shopping list failed: %w", err)
	}

	return nil
}

// AddProductToShoppingList adds a product to the main shopping list.
func (c *Client) AddProductToShoppingList(ctx context.Context, productID int, quantity int) error {
	return c.AddToShoppingList(ctx, []ListItem{{
		ProductID: productID,
		Quantity:  max(quantity, 1),
	}})
}

// AddFreeTextToShoppingList adds a free-text item (not linked to a product) to
// the main shopping list and reports whether a read-back shows it. AH accepts
// the PATCH even when the line never appears, so only a new free-text line with
// this name, or a grown quantity on an existing one, counts as verified.
func (c *Client) AddFreeTextToShoppingList(ctx context.Context, name string, quantity int) (bool, error) {
	before, err := c.getBoodschappenlijstRaw(ctx)
	if err != nil {
		return false, err
	}
	if err := c.AddToShoppingList(ctx, []ListItem{{
		Name:     name,
		Quantity: max(quantity, 1),
	}}); err != nil {
		return false, err
	}
	after, err := c.getBoodschappenlijstRaw(ctx)
	if err != nil {
		return false, fmt.Errorf("sent, but read back failed: %w", err)
	}

	prevQty := freeTextQuantities(before, name)
	for id, qty := range freeTextQuantities(after, name) {
		if prev, ok := prevQty[id]; !ok || qty > prev {
			return true, nil
		}
	}
	return false, nil
}

// freeTextQuantities maps listItemId to quantity for the free-text lines named
// name. Product lines are skipped even when their title matches.
func freeTextQuantities(items []v2RawListItem, name string) map[int]int {
	want := strings.ToLower(strings.TrimSpace(name))
	qty := make(map[int]int)
	for _, it := range items {
		if it.ProductDetails.Product.WebshopID == 0 && strings.ToLower(strings.TrimSpace(it.Description)) == want {
			qty[it.ListItemID] = it.Quantity
		}
	}
	return qty
}

const boodschappenlijstPath = "/mobile-services/shoppinglist/v2/items"

// v2RawListItem is one item from GET /shoppinglist/v2/items (the main Boodschappenlijst).
// A product line has its id at productDetails.product.webshopId and its name in
// the product title. Description is decoded for free-text lines, which have no product.
type v2RawListItem struct {
	ListItemID     int    `json:"listItemId"`
	Quantity       int    `json:"quantity"`
	Type           string `json:"type"`
	OriginCode     string `json:"originCode"`
	Description    string `json:"description"`
	StrikedThrough bool   `json:"strikedthrough"`
	ProductDetails struct {
		Product struct {
			WebshopID int    `json:"webshopId"`
			Title     string `json:"title"`
		} `json:"product"`
	} `json:"productDetails"`
}

func (it v2RawListItem) name() string {
	if t := it.ProductDetails.Product.Title; t != "" {
		return t
	}
	return it.Description
}

// removal builds the quantity-0 PATCH item that deletes this line.
//
// The payload MUST carry the same fields a normal add sends (description,
// searchTerm, strikeThrough); a minimal {productId, quantity:0, type, originCode}
// body is rejected with HTTP 400 "Failed to read request".
func (it v2RawListItem) removal() shoppingListItem {
	typ := it.Type
	if typ == "" {
		typ = "SHOPPABLE"
	}
	oc := it.OriginCode
	if oc == "" {
		oc = "PRD"
	}
	return shoppingListItem{
		ProductID:     it.ProductDetails.Product.WebshopID, // omitted when 0
		Quantity:      0,                                   // 0 = delete
		Type:          typ,
		OriginCode:    oc,
		Description:   it.name(),
		SearchTerm:    it.name(),
		StrikeThrough: false,
	}
}

func (c *Client) getBoodschappenlijstRaw(ctx context.Context) ([]v2RawListItem, error) {
	var current struct {
		Items []v2RawListItem `json:"items"`
	}
	if err := c.DoRequest(ctx, http.MethodGet, boodschappenlijstPath, nil, &current); err != nil {
		return nil, fmt.Errorf("read shopping list failed: %w", err)
	}
	return current.Items, nil
}

func (c *Client) patchBoodschappenlijst(ctx context.Context, items []shoppingListItem) error {
	return c.DoRequest(ctx, http.MethodPatch, boodschappenlijstPath, map[string]any{"items": items}, nil)
}

// GetBoodschappenlijst returns the main shopping list (v2), the list
// AddToShoppingList writes to. Checked is the line's struck-through state.
func (c *Client) GetBoodschappenlijst(ctx context.Context) ([]ListItem, error) {
	raw, err := c.getBoodschappenlijstRaw(ctx)
	if err != nil {
		return nil, err
	}
	items := make([]ListItem, 0, len(raw))
	for _, it := range raw {
		items = append(items, ListItem{
			ID:        strconv.Itoa(it.ListItemID),
			Name:      it.name(),
			ProductID: it.ProductDetails.Product.WebshopID,
			Quantity:  it.Quantity,
			Checked:   it.StrikedThrough,
		})
	}
	return items, nil
}

// RemoveFromShoppingList removes items from the main Boodschappenlijst (v2).
// Items are matched either by product webshopId (productIDs) or by their
// displayed name (names, case-insensitive): the product title, or the
// description of a free-text line.
func (c *Client) RemoveFromShoppingList(ctx context.Context, productIDs []int, names []string) error {
	current, err := c.getBoodschappenlijstRaw(ctx)
	if err != nil {
		return err
	}

	wantPID := make(map[int]bool, len(productIDs))
	for _, id := range productIDs {
		wantPID[id] = true
	}
	wantName := make(map[string]bool, len(names))
	for _, n := range names {
		if t := strings.ToLower(strings.TrimSpace(n)); t != "" {
			wantName[t] = true
		}
	}

	removeItems := make([]shoppingListItem, 0, len(current))
	for _, it := range current {
		pid := it.ProductDetails.Product.WebshopID
		if (pid == 0 || !wantPID[pid]) && !wantName[strings.ToLower(strings.TrimSpace(it.name()))] {
			continue
		}
		removeItems = append(removeItems, it.removal())
	}

	if len(removeItems) == 0 {
		return fmt.Errorf("no matching items on the shopping list")
	}

	if err := c.patchBoodschappenlijst(ctx, removeItems); err != nil {
		return fmt.Errorf("remove from shopping list failed: %w", err)
	}

	return nil
}

// ClearBoodschappenlijst removes every line from the main Boodschappenlijst (v2).
// It returns how many lines were removed and how many a read-back still shows,
// since AH can answer 2xx and keep lines. Favorite lists are not touched.
func (c *Client) ClearBoodschappenlijst(ctx context.Context) (removed, left int, err error) {
	current, err := c.getBoodschappenlijstRaw(ctx)
	if err != nil {
		return 0, 0, err
	}
	if len(current) == 0 {
		return 0, 0, nil
	}

	removeItems := make([]shoppingListItem, 0, len(current))
	for _, it := range current {
		removeItems = append(removeItems, it.removal())
	}
	if err := c.patchBoodschappenlijst(ctx, removeItems); err != nil {
		return 0, 0, fmt.Errorf("clear shopping list failed: %w", err)
	}

	remaining, err := c.getBoodschappenlijstRaw(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("cleared, but read back failed: %w", err)
	}
	return max(len(current)-len(remaining), 0), len(remaining), nil
}

// AddToFavoriteList adds products to a named favorite list (v3) using GraphQL.
// Each item's ProductID and Quantity are sent. Use GetShoppingLists to get list IDs.
func (c *Client) AddToFavoriteList(ctx context.Context, listID string, items []ListItem) error {
	const mutation = `mutation AddProductsToFavoriteList($favoriteListId: String!, $products: [FavoriteListProductMutation!]!) {
  favoriteListProductsAddV2(id: $favoriteListId, products: $products) {
    __typename
    status
    errorMessage
  }
}`

	products := make([]map[string]int, 0, len(items))
	for _, item := range items {
		products = append(products, map[string]int{
			"productId": item.ProductID,
			"quantity":  max(item.Quantity, 1),
		})
	}

	variables := map[string]any{
		"favoriteListId": strings.ToUpper(listID),
		"products":       products,
	}

	var result struct {
		FavoriteListProductsAddV2 struct {
			Status       string `json:"status"`
			ErrorMessage string `json:"errorMessage"`
		} `json:"favoriteListProductsAddV2"`
	}

	if err := c.DoGraphQL(ctx, mutation, variables, &result); err != nil {
		return fmt.Errorf("add to favorite list failed: %w", err)
	}

	if result.FavoriteListProductsAddV2.Status != "SUCCESS" {
		return fmt.Errorf("add to favorite list failed: %s", result.FavoriteListProductsAddV2.ErrorMessage)
	}

	return nil
}

// RemoveFromFavoriteList removes products from a named favorite list using GraphQL.
// It looks up item IDs by product ID, then calls favoriteListProductsDeleteV2.
func (c *Client) RemoveFromFavoriteList(ctx context.Context, listID string, productIDs []int) error {
	items, err := c.GetShoppingListItems(ctx, listID)
	if err != nil {
		return err
	}

	want := make(map[int]bool, len(productIDs))
	for _, id := range productIDs {
		want[id] = true
	}

	var itemIDs []string
	for _, item := range items {
		if want[item.ProductID] {
			itemIDs = append(itemIDs, item.ID)
		}
	}

	if len(itemIDs) == 0 {
		return fmt.Errorf("none of the specified products found in list")
	}

	const mutation = `mutation DeleteProductsFromFavoriteList($favoriteListId: String!, $itemIds: [String!]!) {
  favoriteListProductsDeleteV2(id: $favoriteListId, itemIds: $itemIds) {
    status
    errorMessage
  }
}`

	variables := map[string]any{
		"favoriteListId": strings.ToUpper(listID),
		"itemIds":        itemIDs,
	}

	var result struct {
		FavoriteListProductsDeleteV2 struct {
			Status       string `json:"status"`
			ErrorMessage string `json:"errorMessage"`
		} `json:"favoriteListProductsDeleteV2"`
	}

	if err := c.DoGraphQL(ctx, mutation, variables, &result); err != nil {
		return fmt.Errorf("remove from favorite list failed: %w", err)
	}

	if result.FavoriteListProductsDeleteV2.Status != "SUCCESS" {
		return fmt.Errorf("remove from favorite list failed: %s", result.FavoriteListProductsDeleteV2.ErrorMessage)
	}

	return nil
}

// CheckShoppingListItem marks an item as checked (picked) or unchecked.
// Checked items are typically displayed differently in the app UI.
func (c *Client) CheckShoppingListItem(ctx context.Context, itemID string, checked bool) error {
	body := map[string]any{
		"checked": checked,
	}

	path := fmt.Sprintf("/mobile-services/lists/v3/lists/items/%s", itemID)
	if err := c.DoRequest(ctx, http.MethodPatch, path, body, nil); err != nil {
		return fmt.Errorf("check shopping list item failed: %w", err)
	}

	return nil
}

// ShoppingListToOrder adds the unchecked product lines of the main
// Boodschappenlijst (v2) to the active order and reports which landed.
// Struck-through and free-text lines are skipped. The basket mutation sets
// absolute quantities, so each list quantity is added to what the cart already
// holds; landed quantities are the resulting cart totals. It returns a nil
// result when there is nothing to add.
func (c *Client) ShoppingListToOrder(ctx context.Context) (*BasketWriteResult, error) {
	current, err := c.getBoodschappenlijstRaw(ctx)
	if err != nil {
		return nil, err
	}

	var orderItems []OrderItem
	for _, it := range current {
		pid := it.ProductDetails.Product.WebshopID
		if it.StrikedThrough || pid == 0 {
			continue
		}
		orderItems = append(orderItems, OrderItem{ProductID: pid, Quantity: max(it.Quantity, 1)})
	}

	if len(orderItems) == 0 {
		return nil, nil
	}

	order, err := c.GetOrder(ctx)
	if err != nil {
		return nil, err
	}
	inCart := orderQuantities(order)
	orderItems = mergeOrderItems(orderItems)
	for i := range orderItems {
		orderItems[i].Quantity += inCart[orderItems[i].ProductID]
	}

	return c.AddToOrderCheckedFrom(ctx, order, orderItems)
}
