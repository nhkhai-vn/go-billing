package billing

import "time"

// Price is a provider's price, cached so pricing pages need no provider call.
type Price struct {
	ID            string `json:"id"`
	ProductID     string `json:"product_id"`
	Amount        int64  `json:"amount"`
	Currency      string `json:"currency"`
	Interval      string `json:"interval,omitempty"`
	IntervalCount int64  `json:"interval_count,omitempty"`
	Active        bool   `json:"active"`
	Nickname      string `json:"nickname,omitempty"`
	Type          string `json:"type"`
	// TaxBehavior is "inclusive", "exclusive" or "unspecified".
	TaxBehavior string    `json:"tax_behavior,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Product is what a provider sells; DefaultPriceID is the price a checkout by product charges.
type Product struct {
	ID             string
	Name           string
	Active         bool
	DefaultPriceID string
}
