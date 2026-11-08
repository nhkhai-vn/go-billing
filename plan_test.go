package billing_test

import (
	"errors"
	"testing"

	billing "github.com/nhkhai-vn/go-billing"
)

func planEnv(t *testing.T, wrap ...func(*billing.Store)) *env {
	t.Helper()
	e := setup(t, wrap...)
	e.provider.products["prod_1"] = &billing.Product{ID: "prod_1", Name: "SuguQ", Active: true, DefaultPriceID: "price_1"}
	e.provider.prices["price_1"] = &billing.Price{
		ID: "price_1", ProductID: "prod_1", Amount: 1000, Currency: "jpy", Interval: "month", IntervalCount: 1, TaxBehavior: "inclusive",
	}
	return e
}

func TestDescribePlan_DefaultPriceOfProduct(t *testing.T) {
	e := planEnv(t)
	plan, err := e.svc.DescribePlan(ctx, billing.CheckoutInput{CustomerEmail: "a@example.com", ProductID: "prod_1", Quantity: 3, TrialDays: 14})
	if err != nil {
		t.Fatalf("DescribePlan: %v", err)
	}
	want := billing.Plan{
		ProductID: "prod_1", PriceID: "price_1", Name: "SuguQ", Amount: 1000, Currency: "jpy", Interval: "month",
		IntervalCount: 1, Quantity: 3, TaxBehavior: "inclusive", TrialDays: 14,
	}
	if *plan != want {
		t.Errorf("plan = %+v, want %+v", *plan, want)
	}
}

func TestDescribePlan_TrialFollowsCheckoutRules(t *testing.T) {
	tests := map[string]struct {
		prior   string
		wrap    []func(*billing.Store)
		offered int
		want    int
	}{
		"new customer":          {offered: 14, want: 14},
		"no trial offered":      {offered: 0, want: 0},
		"customer without plan": {prior: "none", offered: 14, want: 14},
		"returning subscriber":  {prior: billing.StatusCanceled, offered: 14, want: 0},
		"unreadable customer":   {wrap: []func(*billing.Store){customersFail}, offered: 14, want: 0},
		"unreadable history":    {prior: "none", wrap: []func(*billing.Store){subscriptionsFail}, offered: 14, want: 0},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			e := planEnv(t, tc.wrap...)
			if tc.prior != "" {
				e.seedCustomer(t, "a@example.com", "cus_1")
			}
			if tc.prior != "" && tc.prior != "none" {
				e.seedSubscription(t, "sub_1", tc.prior, 1)
			}
			plan, err := e.svc.DescribePlan(ctx, billing.CheckoutInput{CustomerEmail: "a@example.com", PriceID: "price_1", TrialDays: tc.offered})
			if err != nil {
				t.Fatalf("DescribePlan: %v", err)
			}
			if plan.TrialDays != tc.want || plan.Quantity != 1 {
				t.Errorf("trial %d, quantity %d; want %d, 1", plan.TrialDays, plan.Quantity, tc.want)
			}
		})
	}
}

func TestDescribePlan_NoPrice(t *testing.T) {
	t.Run("unknown product", func(t *testing.T) {
		e := planEnv(t)
		if _, err := e.svc.DescribePlan(ctx, billing.CheckoutInput{ProductID: "prod_missing"}); !errors.Is(err, billing.ErrNoDefaultPrice) {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("price without product", func(t *testing.T) {
		e := planEnv(t)
		e.provider.prices["price_1"].ProductID = ""
		if _, err := e.svc.DescribePlan(ctx, billing.CheckoutInput{PriceID: "price_1"}); !errors.Is(err, billing.ErrNoDefaultPrice) {
			t.Errorf("err = %v", err)
		}
	})
}

func TestDescribePlan_ProviderFailureIsNotNoPrice(t *testing.T) {
	tests := map[string]func(*fakeProvider){
		"price":            func(p *fakeProvider) { p.priceErr = errDown },
		"product":          func(p *fakeProvider) { p.productErr = errDown },
		"product of price": func(p *fakeProvider) { p.products = map[string]*billing.Product{} },
	}
	for name, breakIt := range tests {
		t.Run(name, func(t *testing.T) {
			e := planEnv(t)
			breakIt(e.provider)
			_, err := e.svc.DescribePlan(ctx, billing.CheckoutInput{PriceID: "price_1"})
			if err == nil || errors.Is(err, billing.ErrNoDefaultPrice) {
				t.Errorf("err = %v, want a failure other than ErrNoDefaultPrice", err)
			}
		})
	}
}
