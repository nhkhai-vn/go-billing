package billing

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
)

// Plan is what a checkout with the same input would sell the customer, for a confirmation screen before it.
type Plan struct {
	ProductID     string
	PriceID       string
	Name          string
	Amount        int64
	Currency      string
	Interval      string
	IntervalCount int64
	// Quantity is the number of units checkout would sell; the total is Amount × Quantity.
	Quantity int64
	// TaxBehavior is "inclusive", "exclusive" or "unspecified".
	TaxBehavior string
	// TrialDays is what checkout would grant this customer now: 0 once the first-time trial is used.
	TrialDays int
}

// DescribePlan resolves the price a checkout with in would charge and the trial it would grant, without starting one.
func (s *Service) DescribePlan(ctx context.Context, in CheckoutInput) (*Plan, error) {
	priceID, err := s.resolvePriceID(ctx, in.PriceID, in.ProductID)
	if err != nil {
		return nil, err
	}
	price, err := s.provider.GetPrice(ctx, priceID)
	if err != nil {
		return nil, fmt.Errorf("get price: %w", err)
	}
	if price.ProductID == "" {
		return nil, ErrNoDefaultPrice
	}
	product, err := s.provider.GetProduct(ctx, price.ProductID)
	if err != nil {
		return nil, fmt.Errorf("get product: %w", err)
	}

	return &Plan{
		ProductID:     product.ID,
		PriceID:       price.ID,
		Name:          product.Name,
		Amount:        price.Amount,
		Currency:      price.Currency,
		Interval:      price.Interval,
		IntervalCount: price.IntervalCount,
		Quantity:      units(in.Quantity),
		TaxBehavior:   price.TaxBehavior,
		TrialDays:     s.trialFor(ctx, in.CustomerEmail, in.TrialDays),
	}, nil
}

func (s *Service) trialFor(ctx context.Context, email string, trialDays int) int {
	if trialDays == 0 {
		return 0
	}
	cust, err := s.customers.FindByEmailAndApp(ctx, email, s.appID)
	if errors.Is(err, ErrNotFound) {
		return trialDays
	}
	if err != nil {
		slog.WarnContext(ctx, "could not find customer; withholding first-time trial", "error", err)
		return 0
	}
	days, _ := s.resolveFirstTimePerks(ctx, cust.ID, trialDays, "")
	return days
}
