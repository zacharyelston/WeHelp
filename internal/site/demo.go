package site

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

// DemoProducts is the demo supply catalogue (#97): ten made-up vitamin
// mixes and herbal supplies. Nothing here is medical advice.
func DemoProducts() []Product {
	return []Product{
		{SKU: "VIT-DAILY", Name: "Daily Foundation Multivitamin", Category: "Vitamins and minerals", Unit: "60 capsules", PriceCents: 2800,
			Description: "A whole-food multivitamin and mineral blend for everyday support."},
		{SKU: "VIT-D3K2", Name: "Vitamin D3 + K2 Drops", Category: "Vitamins and minerals", Unit: "30 ml", PriceCents: 2200,
			Description: "Liquid vitamin D3 with K2 in olive oil. About 1,000 IU per drop."},
		{SKU: "MIN-MAG", Name: "Magnesium Glycinate Calm Mix", Category: "Vitamins and minerals", Unit: "200 g powder", PriceCents: 3200,
			Description: "Unflavoured magnesium glycinate powder to stir into water in the evening."},
		{SKU: "VIT-BCOMP", Name: "Active B-Complex", Category: "Vitamins and minerals", Unit: "60 capsules", PriceCents: 2600,
			Description: "Methylated B vitamins in a gentle, low-dose capsule."},
		{SKU: "MIX-GREENS", Name: "Morning Greens Blend", Category: "Supplement mixes", Unit: "300 g powder", PriceCents: 4200,
			Description: "Spirulina, chlorella, wheatgrass and beet in an unsweetened powder."},
		{SKU: "MIX-PROBIO", Name: "Gut Balance Probiotic", Category: "Supplement mixes", Unit: "30 capsules", PriceCents: 3800,
			Description: "Ten strains, shelf stable, with a prebiotic fibre."},
		{SKU: "MIX-OMEGA", Name: "Omega-3 Fish Oil", Category: "Supplement mixes", Unit: "90 softgels", PriceCents: 3400,
			Description: "Purified EPA and DHA, lemon flavoured, tested for heavy metals."},
		{SKU: "HRB-CHAMO", Name: "Chamomile and Lemon Balm Tea", Category: "Herbal supplies", Unit: "50 g loose leaf", PriceCents: 1400,
			Description: "An organic, caffeine-free evening tea blend."},
		{SKU: "HRB-ELDER", Name: "Elderberry Syrup", Category: "Herbal supplies", Unit: "120 ml", PriceCents: 2400,
			Description: "Elderberry, ginger and cinnamon simmered in raw honey."},
		{SKU: "HRB-ASHWA", Name: "Ashwagandha Root Tincture", Category: "Herbal supplies", Unit: "50 ml", PriceCents: 2600,
			Description: "An alcohol-based extract of organic ashwagandha root."},
	}
}

// DemoContent is starter website content for a practice that hasn't
// written its own yet.
func DemoContent(practice string) Content {
	return Content{
		PracticeName: practice,
		Tagline:      "Whole-person naturopathic care, in the office and from your phone.",
		About: "We are a small naturopathic practice focused on long-term, whole-person health. " +
			"Visits are unhurried, and we work with your other clinicians.\n\n" +
			"Between visits you can message us, request appointments, join video visits and order supplies in the free WeHelp app.",
		Hours:   "Monday to Thursday: 8 am to 6 pm\nFriday: 8 am to 2 pm\nWeekends: closed",
		Address: "123 Demo Street\nSpringfield",
		Phone:   "(555) 010-0100",
		AppURL:  "https://apps.apple.com/",
		Providers: []Provider{
			{Name: "Dr. Ada Shaw", Credentials: "ND, licensed naturopathic doctor",
				Bio: "Dr. Shaw has practised naturopathic medicine for twelve years, with a focus on digestive health, sleep and stress."},
		},
		Services: []Service{
			{Name: "Initial consultation", Description: "A 75-minute visit covering your history, goals and a first plan."},
			{Name: "Follow-up visits", Description: "30-minute visits in the office or by video in the WeHelp app."},
			{Name: "Nutrition counselling", Description: "Practical eating plans that fit your week."},
			{Name: "Herbal medicine", Description: "Individual herbal formulas, prepared in the office."},
		},
		Podcasts: []Podcast{
			{Title: "Practice podcast: sleep and stress", URL: "https://example.com/podcast/sleep", Description: "A 25-minute episode on evening routines."},
		},
	}
}

// Execer is a pgx pool or transaction.
type Execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// AddDemoCatalogue inserts the demo products a tenant doesn't have yet
// (by SKU) and returns how many were added. The seed and the staff UI use it.
func AddDemoCatalogue(ctx context.Context, db Execer, tenant uuid.UUID) (int, error) {
	added := 0
	for i, p := range DemoProducts() {
		tag, err := db.Exec(ctx, `
			insert into products (tenant_id, sku, name, description, category, unit, price_cents, sort)
			values ($1, $2, $3, $4, $5, $6, $7, $8) on conflict (tenant_id, sku) do nothing`,
			tenant, p.SKU, p.Name, p.Description, p.Category, p.Unit, p.PriceCents, i)
		if err != nil {
			return added, err
		}
		added += int(tag.RowsAffected())
	}
	return added, nil
}
