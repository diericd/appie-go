package appie

import (
	"context"
	"fmt"
	"slices"
	"strings"
)

// The mobile REST catalog (search, by-id, detail) is the NL catalog, even on
// api.ah.be. The gql product field resolves every id, BE-only ones included,
// and fills taxonomies only for products in the account's assortment: on
// 2026-11-23 all 40 recently delivered BE ids had taxonomies, and the 36
// permanent slot rejects among 42 known rejects had none.

const gqlProductFields = `id title brand salesUnitSize category webPath availabilityLabel taxonomies { id } price { now { amount } was { amount } }`

type gqlProduct struct {
	ID                int    `json:"id"`
	Title             string `json:"title"`
	Brand             string `json:"brand"`
	SalesUnitSize     string `json:"salesUnitSize"`
	Category          string `json:"category"`
	WebPath           string `json:"webPath"`
	AvailabilityLabel string `json:"availabilityLabel"`
	Taxonomies        []struct {
		ID int `json:"id"`
	} `json:"taxonomies"`
	Price struct {
		Now *struct {
			Amount float64 `json:"amount"`
		} `json:"now"`
		Was *struct {
			Amount float64 `json:"amount"`
		} `json:"was"`
	} `json:"price"`
}

func (g gqlProduct) toProduct() Product {
	in := g.Taxonomies != nil
	p := Product{
		ID:           g.ID,
		WebshopID:    fmt.Sprint(g.ID),
		Title:        g.Title,
		Brand:        g.Brand,
		Category:     g.Category,
		UnitSize:     g.SalesUnitSize,
		WebPath:      g.WebPath,
		InAssortment: &in,
	}
	if g.Price.Now != nil {
		p.Price.Now = g.Price.Now.Amount
	}
	if g.Price.Was != nil && g.Price.Was.Amount > p.Price.Now {
		p.Price.Was = g.Price.Was.Amount
	}
	return p
}

// GetProductGQL reads one product through the gql gateway. It resolves BE-only
// products (wiNNNNNN ids that the REST endpoints 404), with title, unit size,
// category, BE price and InAssortment, but without images, nutrition or bonus.
func (c *Client) GetProductGQL(ctx context.Context, productID int) (*Product, error) {
	var resp struct {
		Product *gqlProduct `json:"product"`
	}
	q := fmt.Sprintf("query Product { product(id: %d) { %s } }", productID, gqlProductFields)
	if err := c.DoGraphQL(ctx, q, nil, &resp); err != nil {
		return nil, fmt.Errorf("get product %d via gql failed: %w", productID, err)
	}
	if resp.Product == nil {
		return nil, fmt.Errorf("get product %d via gql: not found", productID)
	}
	p := resp.Product.toProduct()
	return &p, nil
}

// assortmentBatchSize caps the aliased product fields per gql query.
const assortmentBatchSize = 30

// InAssortment reports, per id, whether the product is in the account's
// assortment (gql taxonomies set). One unknown id fails a whole aliased query,
// so a failed batch is retried per id; ids that still fail are left out.
func (c *Client) InAssortment(ctx context.Context, ids []int) (map[int]bool, error) {
	out := make(map[int]bool, len(ids))
	ids = slices.Compact(slices.Sorted(slices.Values(ids)))
	var lastErr error
	for chunk := range slices.Chunk(ids, assortmentBatchSize) {
		got, err := c.inAssortmentBatch(ctx, chunk)
		if err != nil && len(chunk) > 1 {
			for _, id := range chunk {
				one, err := c.inAssortmentBatch(ctx, []int{id})
				if err != nil {
					lastErr = err
					continue
				}
				out[id] = one[id]
			}
			continue
		}
		if err != nil {
			lastErr = err
			continue
		}
		for id, in := range got {
			out[id] = in
		}
	}
	if len(out) == 0 && lastErr != nil {
		return nil, fmt.Errorf("assortment check failed: %w", lastErr)
	}
	return out, nil
}

func (c *Client) inAssortmentBatch(ctx context.Context, ids []int) (map[int]bool, error) {
	var b strings.Builder
	b.WriteString("query Assortment {")
	for _, id := range ids {
		fmt.Fprintf(&b, " p%d: product(id: %d) { taxonomies { id } }", id, id)
	}
	b.WriteString(" }")
	var resp map[string]*struct {
		Taxonomies []struct {
			ID int `json:"id"`
		} `json:"taxonomies"`
	}
	if err := c.DoGraphQL(ctx, b.String(), nil, &resp); err != nil {
		return nil, err
	}
	out := make(map[int]bool, len(ids))
	for _, id := range ids {
		if p := resp[fmt.Sprintf("p%d", id)]; p != nil {
			out[id] = p.Taxonomies != nil
		}
	}
	return out, nil
}
