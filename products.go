package appie

import (
	"context"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// searchResponse matches the API response for product search
type searchResponse struct {
	Products []productResponse `json:"products"`
	Page     struct {
		Number        int `json:"number"`
		Size          int `json:"size"`
		TotalElements int `json:"totalElements"`
		TotalPages    int `json:"totalPages"`
	} `json:"page"`
}

type productResponse struct {
	WebshopID            int      `json:"webshopId"`
	HqID                 int      `json:"hqId"`
	Title                string   `json:"title"`
	Brand                string   `json:"brand"`
	SalesUnitSize        string   `json:"salesUnitSize"`
	UnitPriceDescription string   `json:"unitPriceDescription"`
	Images               []Image  `json:"images"`
	CurrentPrice         float64  `json:"currentPrice"`
	PriceBeforeBonus     float64  `json:"priceBeforeBonus"`
	IsBonus              bool     `json:"isBonus"`
	BonusMechanism       string   `json:"bonusMechanism"`
	MainCategory         string   `json:"mainCategory"`
	SubCategory          string   `json:"subCategory"`
	NutriScore           string   `json:"nutriscore"`
	AvailableOnline      bool     `json:"availableOnline"`
	IsPreviouslyBought   bool     `json:"isPreviouslyBought"`
	IsOrderable          bool     `json:"isOrderable"`
	PropertyIcons        []string `json:"propertyIcons"`
	// DescriptionHighlights is HTML; kits put "Zelf toevoegen: ..." here.
	DescriptionHighlights string `json:"descriptionHighlights"`
}

var (
	htmlBreak = regexp.MustCompile(`(?i)</p>|</li>|<br\s*/?>|\|\|`)
	htmlTag   = regexp.MustCompile(`<[^>]*>`)
)

// descriptionText turns descriptionHighlights HTML into plain lines.
func descriptionText(s string) string {
	s = htmlBreak.ReplaceAllString(s, "\n")
	s = html.UnescapeString(htmlTag.ReplaceAllString(s, ""))
	var lines []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	return strings.Join(lines, "\n")
}

// selfAdd returns what follows "Zelf toevoegen:" on its line, or "".
func selfAdd(text string) string {
	const prefix = "zelf toevoegen:"
	for _, l := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.ToLower(l), prefix) {
			return strings.TrimSpace(l[len(prefix):])
		}
	}
	return ""
}

// GraphQL query for fetching product nutritional info via tradeItem
const fetchProductNutritionQuery = `query FetchProduct($productId: Int!) {
  product(id: $productId) {
    id
    tradeItem {
      nutritions {
        nutrients {
          type
          name
          value
        }
      }
    }
  }
}`

type productNutritionResponse struct {
	Product struct {
		ID        int `json:"id"`
		TradeItem *struct {
			Nutritions []struct {
				Nutrients []struct {
					Type  string `json:"type"`
					Name  string `json:"name"`
					Value string `json:"value"`
				} `json:"nutrients"`
			} `json:"nutritions"`
		} `json:"tradeItem"`
	} `json:"product"`
}

func (p *productResponse) toProduct() Product {
	price := p.CurrentPrice
	if price == 0 {
		price = p.PriceBeforeBonus
	}

	desc := descriptionText(p.DescriptionHighlights)
	return Product{
		ShortDescription:     desc,
		SelfAdd:              selfAdd(desc),
		ID:                   p.WebshopID,
		WebshopID:            strconv.Itoa(p.WebshopID),
		Title:                p.Title,
		Brand:                p.Brand,
		Category:             p.MainCategory,
		SubCategory:          p.SubCategory,
		Price:                Price{Now: price, Was: p.PriceBeforeBonus},
		Images:               p.Images,
		NutriScore:           p.NutriScore,
		IsBonus:              p.IsBonus,
		BonusMechanism:       p.BonusMechanism,
		IsAvailable:          p.AvailableOnline,
		IsOrderable:          p.IsOrderable,
		IsPreviouslyBought:   p.IsPreviouslyBought,
		UnitSize:             p.SalesUnitSize,
		UnitPriceDescription: p.UnitPriceDescription,
		PropertyIcons:        p.PropertyIcons,
	}
}

// SearchProducts searches for products by query string and returns up to limit results.
// If limit is 0 or negative, defaults to 30 products.
//
// Example:
//
//	products, err := client.SearchProducts(ctx, "melk", 10)
func (c *Client) SearchProducts(ctx context.Context, query string, limit int) ([]Product, error) {
	return c.SearchProductsFiltered(ctx, SearchOptions{Query: query, Limit: limit})
}

// GetProduct retrieves a single product by its webshopId.
// For nutritional information, use GetProductFull instead.
func (c *Client) GetProduct(ctx context.Context, productID int) (*Product, error) {
	path := fmt.Sprintf("/mobile-services/product/detail/v4/fir/%d", productID)

	var result struct {
		ProductID   int             `json:"productId"`
		ProductCard productResponse `json:"productCard"`
	}

	if err := c.DoRequest(ctx, http.MethodGet, path, nil, &result); err != nil {
		return nil, fmt.Errorf("get product failed: %w", err)
	}

	product := result.ProductCard.toProduct()
	return &product, nil
}

// GetProductFull retrieves a product with full details including nutritional info.
// This makes an additional GraphQL call for nutritional data.
func (c *Client) GetProductFull(ctx context.Context, productID int) (*Product, error) {
	product, err := c.GetProduct(ctx, productID)
	if err != nil {
		return nil, err
	}

	nutritionInfo, err := c.fetchNutritionalInfo(ctx, productID)
	if err != nil {
		return nil, fmt.Errorf("get product nutrition failed: %w", err)
	}
	product.NutritionalInfo = nutritionInfo

	return product, nil
}

// fetchNutritionalInfo fetches nutritional data for a product via GraphQL.
func (c *Client) fetchNutritionalInfo(ctx context.Context, productID int) ([]NutritionalInfo, error) {
	variables := map[string]any{
		"productId": productID,
	}

	var resp productNutritionResponse
	if err := c.DoGraphQL(ctx, fetchProductNutritionQuery, variables, &resp); err != nil {
		return nil, err
	}

	if resp.Product.TradeItem == nil || len(resp.Product.TradeItem.Nutritions) == 0 {
		return nil, nil
	}

	var nutritionalInfo []NutritionalInfo
	for _, nutrition := range resp.Product.TradeItem.Nutritions {
		for _, n := range nutrition.Nutrients {
			nutritionalInfo = append(nutritionalInfo, NutritionalInfo{
				Name:  n.Name,
				Type:  n.Type,
				Value: n.Value,
			})
		}
	}

	return nutritionalInfo, nil
}

// SearchOptions configures a product search.
type SearchOptions struct {
	Query string // Search term
	Limit int    // Max results (default 30)
	Bonus bool   // Only return products currently on bonus/promotion
}

// SearchProductsFiltered searches for products. With WithLocale set it takes
// the result list from the gql search in that locale, else from the REST API.
// When Bonus is true, over-fetches and filters client-side to return
// up to Limit bonus products.
func (c *Client) SearchProductsFiltered(ctx context.Context, opts SearchOptions) ([]Product, error) {
	limit := opts.Limit
	if limit <= 0 {
		limit = 30
	}
	if c.locale != "" {
		products, err := c.searchProductsLocale(ctx, opts, limit)
		if err == nil {
			return products, nil
		}
		c.logger.Printf("locale search failed, falling back to REST: %v", err)
	}

	var products []Product
	page := 0
	pageSize := limit
	if opts.Bonus {
		pageSize = limit * 5
	}

	for len(products) < limit {
		params := url.Values{}
		params.Set("query", opts.Query)
		params.Set("page", strconv.Itoa(page))
		params.Set("size", strconv.Itoa(pageSize))
		params.Set("sortOn", "RELEVANCE")

		path := "/mobile-services/product/search/v2?" + params.Encode()

		var result searchResponse
		if err := c.DoRequest(ctx, http.MethodGet, path, nil, &result); err != nil {
			return nil, fmt.Errorf("search products failed: %w", err)
		}

		for _, p := range result.Products {
			prod := p.toProduct()
			if opts.Bonus && !prod.IsBonus {
				continue
			}
			products = append(products, prod)
			if len(products) >= limit {
				break
			}
		}

		// Stop if we've exhausted all results
		if (page+1)*pageSize >= result.Page.TotalElements {
			break
		}
		page++
	}

	return products, nil
}

// GetProductsByIDs retrieves multiple products by their webshopIds in a single request.
// Unknown ids, such as BE web-only products, are left out without an error, so
// match results to input by Product.ID rather than by position.
func (c *Client) GetProductsByIDs(ctx context.Context, productIDs []int) ([]Product, error) {
	if len(productIDs) == 0 {
		return nil, nil
	}

	params := url.Values{}
	for _, id := range productIDs {
		params.Add("ids", strconv.Itoa(id))
	}
	params.Set("sortOn", "INPUT_PRODUCT_IDS")

	path := "/mobile-services/product/search/v2/products?" + params.Encode()

	var result []productResponse
	if err := c.DoRequest(ctx, http.MethodGet, path, nil, &result); err != nil {
		return nil, fmt.Errorf("get products by ids failed: %w", err)
	}

	products := make([]Product, 0, len(result))
	for _, p := range result {
		products = append(products, p.toProduct())
	}

	return products, nil
}

// localeKey carries the x-locale value from a call to setHeaders.
type localeKey struct{}

// maxLocalePages bounds the paging of a bonus-filtered locale search.
const maxLocalePages = 10

const productSearchQuery = `query ProductSearch($input: ProductSearchInput!) { productSearch(input: $input) { products { id } } }`

// searchProductsLocale runs the gql productSearch with x-locale, which on
// api.ah.be returns the same list as the www.ah.be search (BE-only products
// included). The gql result has ids only, so details come from the REST by-id
// endpoint; ids it does not know (BE-only) come from GetProductGQL, and ids
// that resolve nowhere are left out.
func (c *Client) searchProductsLocale(ctx context.Context, opts SearchOptions, limit int) ([]Product, error) {
	pageSize := limit
	if opts.Bonus {
		pageSize = min(limit*5, 150)
	}
	gqlCtx := context.WithValue(ctx, localeKey{}, c.locale)

	var products []Product
	for page := 0; page < maxLocalePages && len(products) < limit; page++ {
		var resp struct {
			ProductSearch struct {
				Products []struct {
					ID int `json:"id"`
				} `json:"products"`
			} `json:"productSearch"`
		}
		input := map[string]any{"query": opts.Query, "page": page, "size": pageSize}
		if err := c.DoGraphQL(gqlCtx, productSearchQuery, map[string]any{"input": input}, &resp); err != nil {
			return nil, fmt.Errorf("gql product search failed: %w", err)
		}
		ids := make([]int, 0, len(resp.ProductSearch.Products))
		for _, p := range resp.ProductSearch.Products {
			ids = append(ids, p.ID)
		}
		if len(ids) == 0 {
			break
		}

		rest, err := c.GetProductsByIDs(ctx, ids)
		if err != nil {
			return nil, err
		}
		byID := make(map[int]Product, len(rest))
		for _, p := range rest {
			byID[p.ID] = p
		}
		for _, id := range ids {
			p, ok := byID[id]
			if !ok {
				gp, err := c.GetProductGQL(ctx, id)
				if err != nil {
					c.logger.Printf("locale search: skip %d: %v", id, err)
					continue
				}
				p = *gp
			}
			if opts.Bonus && !p.IsBonus {
				continue
			}
			products = append(products, p)
			if len(products) >= limit {
				break
			}
		}
		if len(ids) < pageSize {
			break
		}
	}
	return products, nil
}
