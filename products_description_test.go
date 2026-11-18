package appie

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Verbatim descriptionHighlights of kit 564214 from search/v2/products (2026-11-18).
const koreanBowlHighlights = "<p>Met dit verspakket maak je een Koreaanse bowl, met krokante groenten. Dit pakket bevat precies de juiste hoeveelheid voor 2-3 personen. No waste, dus.</p><p><ul><li>voor 2-3 personen||Bereidingstijd 35 minuten||Zelf toevoegen: 195 g kipdijfilet, 2 el zonnebloemolie, 2 el (witte wijn) azijn, 1 tl suiker, 4 el mayonaise vegan tip: plantaardige kipstukjes</li></ul></p>"

func TestGetProductsByIDsDescription(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]map[string]any{
			{"webshopId": 564214, "title": "AH Gesneden verspakket Koreaanse bowl kip", "descriptionHighlights": koreanBowlHighlights},
			{"webshopId": 2, "title": "Plain", "descriptionHighlights": "<p>Lekker &amp; vers</p>"},
		})
	}))
	defer server.Close()

	products, err := New(WithBaseURL(server.URL)).GetProductsByIDs(context.Background(), []int{564214, 2})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(products) != 2 {
		t.Fatalf("got %d products, want 2", len(products))
	}

	kit := products[0]
	wantDesc := "Met dit verspakket maak je een Koreaanse bowl, met krokante groenten. Dit pakket bevat precies de juiste hoeveelheid voor 2-3 personen. No waste, dus.\n" +
		"voor 2-3 personen\nBereidingstijd 35 minuten\n" +
		"Zelf toevoegen: 195 g kipdijfilet, 2 el zonnebloemolie, 2 el (witte wijn) azijn, 1 tl suiker, 4 el mayonaise vegan tip: plantaardige kipstukjes"
	if kit.ShortDescription != wantDesc {
		t.Errorf("ShortDescription =\n%q\nwant\n%q", kit.ShortDescription, wantDesc)
	}
	wantSelf := "195 g kipdijfilet, 2 el zonnebloemolie, 2 el (witte wijn) azijn, 1 tl suiker, 4 el mayonaise vegan tip: plantaardige kipstukjes"
	if kit.SelfAdd != wantSelf {
		t.Errorf("SelfAdd = %q, want %q", kit.SelfAdd, wantSelf)
	}

	if products[1].ShortDescription != "Lekker & vers" {
		t.Errorf("plain ShortDescription = %q", products[1].ShortDescription)
	}
	if products[1].SelfAdd != "" {
		t.Errorf("plain SelfAdd = %q, want empty", products[1].SelfAdd)
	}
}
