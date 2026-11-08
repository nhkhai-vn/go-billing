package billing_test

import (
	"context"
	"errors"
	"testing"
	"time"

	billing "github.com/nhkhai-vn/go-billing"
)

var ctx = context.Background()

func TestCreateCheckout_NewCustomer(t *testing.T) {
	e := setup(t)
	url, id, err := e.svc.CreateCheckout(ctx, billing.CheckoutInput{
		CustomerEmail: "a@example.com", CustomerName: "Alice", PriceID: "price_1", MeteredPriceID: "price_pages",
		SuccessURL: "https://app/ok", CancelURL: "https://app/no", AllowPromotionCodes: true, Locale: "ja",
	})
	if err != nil {
		t.Fatalf("CreateCheckout: %v", err)
	}
	if url != "https://pay.example/cs_1" || id != "cs_1" {
		t.Errorf("got %q, %q", url, id)
	}
	if len(e.provider.created) != 1 || e.provider.created[0] != app+"|a@example.com|Alice" {
		t.Errorf("provider customers created = %v", e.provider.created)
	}
	want := billing.CheckoutParams{
		CustomerID: "cus_1", PriceID: "price_1", Quantity: 1, MeteredPriceID: "price_pages", SuccessURL: "https://app/ok",
		CancelURL: "https://app/no", AppID: app, CustomerEmail: "a@example.com", AllowPromotionCodes: true, Locale: "ja",
	}
	if *e.provider.checkout != want {
		t.Errorf("checkout params = %+v, want %+v", *e.provider.checkout, want)
	}
	cust, err := e.store.Customers.FindByEmailAndApp(ctx, "a@example.com", app)
	if err != nil || cust.ID != app+"|a@example.com" || cust.ProviderCustomerID != "cus_1" || cust.Name != "Alice" {
		t.Errorf("stored customer = %+v, %v", cust, err)
	}
}

func TestCreateCheckout_SellsTheQuantity(t *testing.T) {
	e := setup(t)
	if _, _, err := e.svc.CreateCheckout(ctx, billing.CheckoutInput{CustomerEmail: "a@example.com", PriceID: "price_1", Quantity: 3}); err != nil {
		t.Fatalf("CreateCheckout: %v", err)
	}
	if e.provider.checkout.Quantity != 3 {
		t.Errorf("quantity = %d, want 3", e.provider.checkout.Quantity)
	}
}

func TestCreateCheckout_ReusesKnownCustomers(t *testing.T) {
	t.Run("in the store", func(t *testing.T) {
		e := setup(t)
		e.seedCustomer(t, "a@example.com", "cus_stored")
		e.provider.findErr = errDown
		if _, _, err := e.svc.CreateCheckout(ctx, billing.CheckoutInput{CustomerEmail: "a@example.com", PriceID: "price_1"}); err != nil {
			t.Fatalf("CreateCheckout: %v", err)
		}
		if e.provider.checkout.CustomerID != "cus_stored" || len(e.provider.created) != 0 {
			t.Errorf("customer %q, created %v", e.provider.checkout.CustomerID, e.provider.created)
		}
	})
	t.Run("at the provider", func(t *testing.T) {
		e := setup(t)
		e.provider.existing["a@example.com"] = "cus_remote"
		if _, _, err := e.svc.CreateCheckout(ctx, billing.CheckoutInput{CustomerEmail: "a@example.com", PriceID: "price_1"}); err != nil {
			t.Fatalf("CreateCheckout: %v", err)
		}
		if e.provider.checkout.CustomerID != "cus_remote" || len(e.provider.created) != 0 {
			t.Errorf("customer %q, created %v", e.provider.checkout.CustomerID, e.provider.created)
		}
		if c, err := e.store.Customers.FindByProviderID(ctx, "cus_remote"); err != nil || c.Email != "a@example.com" {
			t.Errorf("stored customer = %+v, %v", c, err)
		}
	})
}

func TestCreateCheckout_SameEmailInTwoAppsIsTwoCustomers(t *testing.T) {
	e := setup(t)
	other, err := billing.New(billing.Config{AppID: "ocr", Store: e.store, Provider: e.provider})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for _, svc := range []*billing.Service{e.svc, other} {
		if _, _, err := svc.CreateCheckout(ctx, billing.CheckoutInput{CustomerEmail: "a@example.com", PriceID: "price_1"}); err != nil {
			t.Fatalf("CreateCheckout: %v", err)
		}
	}
	q, _ := e.store.Customers.FindByEmailAndApp(ctx, "a@example.com", app)
	o, _ := e.store.Customers.FindByEmailAndApp(ctx, "a@example.com", "ocr")
	if q == nil || o == nil || q.ProviderCustomerID == o.ProviderCustomerID || len(e.provider.created) != 2 {
		t.Errorf("customers %+v / %+v, created %v", q, o, e.provider.created)
	}
}

func TestCreateCheckout_CustomerFailures(t *testing.T) {
	tests := map[string]func(*env){
		"find at provider":   func(e *env) { e.provider.findErr = errDown },
		"create at provider": func(e *env) { e.provider.createErr = errDown },
		"store":              nil,
	}
	for name, breakIt := range tests {
		t.Run(name, func(t *testing.T) {
			var e *env
			if breakIt == nil {
				e = setup(t, customersFail)
			} else {
				e = setup(t)
				breakIt(e)
			}
			_, _, err := e.svc.CreateCheckout(ctx, billing.CheckoutInput{CustomerEmail: "a@example.com", PriceID: "price_1"})
			if !errors.Is(err, errDown) || e.provider.checkout != nil {
				t.Errorf("err = %v, checkout started: %v", err, e.provider.checkout != nil)
			}
		})
	}
}

func TestCreateCheckout_FirstTimePerks(t *testing.T) {
	tests := map[string]struct {
		prior     string
		wantTrial int
		wantSetup string
	}{
		"first subscription":       {wantTrial: 14, wantSetup: "price_setup"},
		"after incomplete attempt": {prior: billing.StatusIncomplete, wantTrial: 14, wantSetup: "price_setup"},
		"after expired attempt":    {prior: billing.StatusIncompleteExpired, wantTrial: 14, wantSetup: "price_setup"},
		"returning subscriber":     {prior: billing.StatusCanceled},
		"active subscriber":        {prior: billing.StatusActive},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			e := setup(t)
			e.seedCustomer(t, "a@example.com", "cus_1")
			if tc.prior != "" {
				e.seedSubscription(t, "sub_old", tc.prior, 1)
			}
			if _, _, err := e.svc.CreateCheckout(ctx, billing.CheckoutInput{CustomerEmail: "a@example.com", PriceID: "price_1", TrialDays: 14, SetupPriceID: "price_setup"}); err != nil {
				t.Fatalf("CreateCheckout: %v", err)
			}
			if got := e.provider.checkout; got.TrialDays != tc.wantTrial || got.SetupPriceID != tc.wantSetup {
				t.Errorf("trial %d, setup %q; want %d, %q", got.TrialDays, got.SetupPriceID, tc.wantTrial, tc.wantSetup)
			}
		})
	}
}

func TestCreateCheckout_UnreadableHistoryWithholdsPerks(t *testing.T) {
	e := setup(t, subscriptionsFail)
	if _, _, err := e.svc.CreateCheckout(ctx, billing.CheckoutInput{CustomerEmail: "a@example.com", PriceID: "price_1", TrialDays: 14, SetupPriceID: "price_setup"}); err != nil {
		t.Fatalf("CreateCheckout: %v", err)
	}
	if got := e.provider.checkout; got.TrialDays != 0 || got.SetupPriceID != "" {
		t.Errorf("trial %d, setup %q; want none", got.TrialDays, got.SetupPriceID)
	}
}

func TestCreateCheckout_ProviderFailure(t *testing.T) {
	e := setup(t)
	e.provider.checkoutErr = errDown
	if _, _, err := e.svc.CreateCheckout(ctx, billing.CheckoutInput{CustomerEmail: "a@example.com", PriceID: "price_1"}); !errors.Is(err, errDown) {
		t.Errorf("err = %v, want errDown", err)
	}
}

func TestCreateCheckout_ProductDefaultPrice(t *testing.T) {
	e := setup(t)
	e.provider.products["prod_1"] = &billing.Product{ID: "prod_1", Active: true, DefaultPriceID: "price_default"}
	if _, _, err := e.svc.CreateCheckout(ctx, billing.CheckoutInput{CustomerEmail: "a@example.com", ProductID: "prod_1"}); err != nil {
		t.Fatalf("CreateCheckout: %v", err)
	}
	if e.provider.checkout.PriceID != "price_default" {
		t.Errorf("price = %q, want price_default", e.provider.checkout.PriceID)
	}
}

func TestCreateCheckout_PriceIDSkipsProductLookup(t *testing.T) {
	e := setup(t)
	if _, _, err := e.svc.CreateCheckout(ctx, billing.CheckoutInput{CustomerEmail: "a@example.com", PriceID: "price_1", ProductID: "prod_1"}); err != nil {
		t.Fatalf("CreateCheckout: %v", err)
	}
	if e.provider.productCalls != 0 || e.provider.checkout.PriceID != "price_1" {
		t.Errorf("product calls %d, price %q", e.provider.productCalls, e.provider.checkout.PriceID)
	}
}

func TestCreateCheckout_ProductWithoutUsableDefaultPrice(t *testing.T) {
	products := map[string]*billing.Product{
		"archived":   {ID: "prod_1", Active: false, DefaultPriceID: "price_1"},
		"price-less": {ID: "prod_1", Active: true},
		"unknown":    nil,
	}
	for name, product := range products {
		t.Run(name, func(t *testing.T) {
			e := setup(t)
			if product != nil {
				e.provider.products["prod_1"] = product
			}
			_, _, err := e.svc.CreateCheckout(ctx, billing.CheckoutInput{CustomerEmail: "a@example.com", ProductID: "prod_1"})
			if !errors.Is(err, billing.ErrNoDefaultPrice) || e.provider.checkout != nil {
				t.Errorf("err = %v, want ErrNoDefaultPrice and no checkout", err)
			}
		})
	}
}

func TestCreateCheckout_ProductLookupFailureIsRetryable(t *testing.T) {
	e := setup(t)
	e.provider.productErr = errDown
	_, _, err := e.svc.CreateCheckout(ctx, billing.CheckoutInput{CustomerEmail: "a@example.com", ProductID: "prod_1"})
	if !errors.Is(err, errDown) || errors.Is(err, billing.ErrNoDefaultPrice) {
		t.Errorf("err = %v, want errDown and not ErrNoDefaultPrice", err)
	}
}

func paidSession(subStatus string) *billing.CheckoutState {
	start := time.Unix(1_700_000_000, 0)
	return &billing.CheckoutState{
		ID: "cs_1", AppID: app, CustomerEmail: "a@example.com", CustomerID: "cus_1", Paid: true, SubscriptionID: "sub_1",
		Subscription: &billing.SubscriptionState{
			ID: "sub_1", AppID: app, Status: subStatus, PriceID: "price_1", Quantity: 2,
			CurrentPeriodStart: start, CurrentPeriodEnd: start.AddDate(0, 1, 0), TrialEnd: start.AddDate(0, 0, 14),
		},
	}
}

func TestConfirmCheckout_StoresTheSubscription(t *testing.T) {
	e := setup(t)
	e.seedCustomer(t, "a@example.com", "cus_1")
	e.provider.sessions["cs_1"] = paidSession(billing.StatusTrialing)

	sub, err := e.svc.ConfirmCheckout(ctx, "cs_1")
	if err != nil {
		t.Fatalf("ConfirmCheckout: %v", err)
	}
	stored := e.subscription(t)
	start := time.Unix(1_700_000_000, 0)
	if stored.ID != app+"|a@example.com" || stored.CustomerID != stored.ID || stored.AppID != app || stored.Status != billing.StatusTrialing ||
		stored.PriceID != "price_1" || stored.Quantity != 2 || !stored.CurrentPeriodStart.Equal(start) ||
		!stored.CurrentPeriodEnd.Equal(start.AddDate(0, 1, 0)) || !stored.TrialEnd.Equal(start.AddDate(0, 0, 14)) || stored.CanceledAt != nil {
		t.Errorf("stored subscription = %+v", stored)
	}
	if sub.ProviderSubscriptionID != "sub_1" || !sub.IsActive() {
		t.Errorf("returned subscription = %+v", sub)
	}
}

func TestConfirmCheckout_Rejections(t *testing.T) {
	tests := map[string]struct {
		session func(*billing.CheckoutState)
		want    error
	}{
		"other app":       {session: func(s *billing.CheckoutState) { s.AppID = "ocr" }, want: billing.ErrAppMismatch},
		"not paid":        {session: func(s *billing.CheckoutState) { s.Paid = false }, want: billing.ErrPaymentPending},
		"no subscription": {session: func(s *billing.CheckoutState) { s.Subscription = nil }},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			e := setup(t)
			e.seedCustomer(t, "a@example.com", "cus_1")
			s := paidSession(billing.StatusActive)
			tc.session(s)
			e.provider.sessions["cs_1"] = s
			_, err := e.svc.ConfirmCheckout(ctx, "cs_1")
			if err == nil || (tc.want != nil && !errors.Is(err, tc.want)) {
				t.Errorf("err = %v, want %v", err, tc.want)
			}
			if _, err := e.store.Subscriptions.FindByProviderID(ctx, "sub_1"); !errors.Is(err, billing.ErrNotFound) {
				t.Errorf("a rejected checkout stored a subscription")
			}
		})
	}
}

func TestConfirmCheckout_Failures(t *testing.T) {
	t.Run("provider", func(t *testing.T) {
		e := setup(t)
		e.provider.getCheckoutEr = errDown
		if _, err := e.svc.ConfirmCheckout(ctx, "cs_1"); !errors.Is(err, errDown) {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("store", func(t *testing.T) {
		e := setup(t, customersFail)
		e.provider.sessions["cs_1"] = paidSession(billing.StatusActive)
		if _, err := e.svc.ConfirmCheckout(ctx, "cs_1"); !errors.Is(err, errDown) {
			t.Errorf("err = %v", err)
		}
	})
}

func TestGetSubscriptionStatus(t *testing.T) {
	e := setup(t)
	e.seedCustomer(t, "a@example.com", "cus_1")
	e.seedSubscription(t, "sub_1", billing.StatusActive, 1)
	e.seedCustomer(t, "b@example.com", "cus_2")

	if sub, err := e.svc.GetSubscriptionStatus(ctx, "a@example.com"); err != nil || sub.ProviderSubscriptionID != "sub_1" {
		t.Errorf("subscriber = %+v, %v", sub, err)
	}
	if sub, err := e.svc.GetSubscriptionStatus(ctx, "b@example.com"); err != nil || sub != nil {
		t.Errorf("customer without subscription = %+v, %v; want nil, nil", sub, err)
	}
	if sub, err := e.svc.GetSubscriptionStatus(ctx, "nobody@example.com"); err != nil || sub != nil {
		t.Errorf("unknown customer = %+v, %v; want nil, nil", sub, err)
	}
}

func TestGetSubscriptionStatus_StoreFailures(t *testing.T) {
	for name, wrap := range map[string]func(*billing.Store){"customers": customersFail, "subscriptions": subscriptionsFail} {
		t.Run(name, func(t *testing.T) {
			e := setup(t, wrap)
			e.seedCustomer(t, "a@example.com", "cus_1")
			if _, err := e.svc.GetSubscriptionStatus(ctx, "a@example.com"); !errors.Is(err, errDown) {
				t.Errorf("err = %v", err)
			}
		})
	}
}

func TestListSubscriptions_OnlyThisApp(t *testing.T) {
	e := setup(t)
	e.seedSubscription(t, "sub_1", billing.StatusActive, 1)
	if err := e.store.Subscriptions.Upsert(ctx, &billing.Subscription{ID: "ocr|b", CustomerID: "ocr|b", AppID: "ocr", Status: billing.StatusActive}); err != nil {
		t.Fatal(err)
	}
	subs, err := e.svc.ListSubscriptions(ctx)
	if err != nil || len(subs) != 1 || subs[0].ProviderSubscriptionID != "sub_1" {
		t.Errorf("ListSubscriptions = %+v, %v", subs, err)
	}
}

func TestCancelSubscription(t *testing.T) {
	t.Run("immediately", func(t *testing.T) {
		e := setup(t)
		e.seedCustomer(t, "a@example.com", "cus_1")
		e.seedSubscription(t, "sub_1", billing.StatusActive, 1)
		sub, err := e.svc.CancelSubscription(ctx, "a@example.com", "too_expensive", true)
		if err != nil {
			t.Fatalf("CancelSubscription: %v", err)
		}
		stored := e.subscription(t)
		if stored.Status != billing.StatusCanceled || stored.CanceledAt == nil || stored.CancellationReason != "too_expensive" || sub.Status != billing.StatusCanceled {
			t.Errorf("stored %+v", stored)
		}
	})
	t.Run("at period end", func(t *testing.T) {
		e := setup(t)
		e.seedCustomer(t, "a@example.com", "cus_1")
		e.seedSubscription(t, "sub_1", billing.StatusActive, 1)
		if _, err := e.svc.CancelSubscription(ctx, "a@example.com", "", false); err != nil {
			t.Fatalf("CancelSubscription: %v", err)
		}
		stored := e.subscription(t)
		if stored.Status != billing.StatusActive || !stored.CancelAtPeriodEnd || stored.CanceledAt != nil || stored.CancellationReason != "" {
			t.Errorf("stored %+v", stored)
		}
	})
}

func TestCancelSubscription_NothingToCancel(t *testing.T) {
	e := setup(t)
	if _, err := e.svc.CancelSubscription(ctx, "a@example.com", "", true); !errors.Is(err, billing.ErrCustomerNotFound) {
		t.Errorf("unknown customer: %v", err)
	}
	e.seedCustomer(t, "a@example.com", "cus_1")
	if _, err := e.svc.CancelSubscription(ctx, "a@example.com", "", true); !errors.Is(err, billing.ErrSubNotFound) {
		t.Errorf("no subscription: %v", err)
	}
}

func TestCancelSubscription_Failures(t *testing.T) {
	for name, wrap := range map[string]func(*billing.Store){"customers": customersFail, "subscriptions": subscriptionsFail} {
		t.Run(name, func(t *testing.T) {
			e := setup(t, wrap)
			e.seedCustomer(t, "a@example.com", "cus_1")
			if _, err := e.svc.CancelSubscription(ctx, "a@example.com", "", true); !errors.Is(err, errDown) {
				t.Errorf("err = %v", err)
			}
		})
	}
	t.Run("provider", func(t *testing.T) {
		e := setup(t)
		e.seedCustomer(t, "a@example.com", "cus_1")
		e.seedSubscription(t, "sub_1", billing.StatusActive, 1)
		e.provider.cancelErr = errDown
		if _, err := e.svc.CancelSubscription(ctx, "a@example.com", "", true); !errors.Is(err, errDown) {
			t.Errorf("err = %v", err)
		}
		if e.subscription(t).Status != billing.StatusActive {
			t.Errorf("a failed cancel changed the record")
		}
	})
}

func TestSetQuantity_UpdatesProviderAndRecord(t *testing.T) {
	e := setup(t)
	e.seedCustomer(t, "a@example.com", "cus_1")
	e.seedSubscription(t, "sub_1", billing.StatusActive, 1)
	sub, err := e.svc.SetQuantity(ctx, "a@example.com", 3)
	if err != nil {
		t.Fatalf("SetQuantity: %v", err)
	}
	if sub.Quantity != 3 || e.subscription(t).Quantity != 3 || e.provider.quantityCalls != 1 {
		t.Errorf("returned %d, stored %d, provider calls %d", sub.Quantity, e.subscription(t).Quantity, e.provider.quantityCalls)
	}
}

func TestSetQuantity_NoProviderCallWhenNothingChanges(t *testing.T) {
	tests := map[string]struct {
		status       string
		stored, want int64
	}{
		"same quantity":            {status: billing.StatusActive, stored: 2, want: 2},
		"unset stored means one":   {status: billing.StatusActive, stored: 0, want: 1},
		"zero requested means one": {status: billing.StatusActive, stored: 1, want: 0},
		"canceled":                 {status: billing.StatusCanceled, stored: 1, want: 5},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			e := setup(t)
			e.seedCustomer(t, "a@example.com", "cus_1")
			e.seedSubscription(t, "sub_1", tc.status, tc.stored)
			if _, err := e.svc.SetQuantity(ctx, "a@example.com", tc.want); err != nil {
				t.Fatalf("SetQuantity: %v", err)
			}
			if e.provider.quantityCalls != 0 {
				t.Errorf("provider called %d times", e.provider.quantityCalls)
			}
		})
	}
}

func TestSetQuantity_NothingToUpdate(t *testing.T) {
	e := setup(t)
	if _, err := e.svc.SetQuantity(ctx, "a@example.com", 2); !errors.Is(err, billing.ErrCustomerNotFound) {
		t.Errorf("unknown customer: %v", err)
	}
	e.seedCustomer(t, "a@example.com", "cus_1")
	if _, err := e.svc.SetQuantity(ctx, "a@example.com", 2); !errors.Is(err, billing.ErrSubNotFound) {
		t.Errorf("no subscription: %v", err)
	}
}

func TestSetQuantity_ProviderFailureKeepsRecord(t *testing.T) {
	e := setup(t)
	e.seedCustomer(t, "a@example.com", "cus_1")
	e.seedSubscription(t, "sub_1", billing.StatusActive, 1)
	e.provider.quantityErr = errDown
	if _, err := e.svc.SetQuantity(ctx, "a@example.com", 3); !errors.Is(err, errDown) {
		t.Errorf("err = %v", err)
	}
	if e.subscription(t).Quantity != 1 {
		t.Errorf("a failed update changed the record")
	}
}

func TestCreatePortalSession(t *testing.T) {
	e := setup(t)
	if _, err := e.svc.CreatePortalSession(ctx, "a@example.com", "https://app", ""); !errors.Is(err, billing.ErrCustomerNotFound) {
		t.Errorf("unknown customer: %v", err)
	}
	e.seedCustomer(t, "a@example.com", "cus_1")
	url, err := e.svc.CreatePortalSession(ctx, "a@example.com", "https://app", "bpc_1")
	if err != nil || url != "https://pay.example/portal" {
		t.Fatalf("CreatePortalSession = %q, %v", url, err)
	}
	if got := e.provider.portal; got[0] != "cus_1" || got[1] != "https://app" || got[2] != "bpc_1" {
		t.Errorf("provider got %v", got)
	}
	e.provider.portalErr = errDown
	if _, err := e.svc.CreatePortalSession(ctx, "a@example.com", "https://app", ""); !errors.Is(err, errDown) {
		t.Errorf("provider failure: %v", err)
	}
}

func TestCreatePortalSession_StoreFailure(t *testing.T) {
	e := setup(t, customersFail)
	if _, err := e.svc.CreatePortalSession(ctx, "a@example.com", "https://app", ""); !errors.Is(err, errDown) {
		t.Errorf("err = %v", err)
	}
}

func TestGetPricesByIDs_ReadThroughCache(t *testing.T) {
	e := setup(t)
	if err := e.store.Prices.Upsert(ctx, &billing.Price{ID: "price_cached", Amount: 100}); err != nil {
		t.Fatal(err)
	}
	e.provider.prices["price_new"] = &billing.Price{ID: "price_new", Amount: 200}

	got, err := e.svc.GetPricesByIDs(ctx, []string{"price_cached", "", "price_new", "price_missing"})
	if err != nil {
		t.Fatalf("GetPricesByIDs: %v", err)
	}
	if len(got) != 2 || e.provider.priceCalls != 2 {
		t.Fatalf("got %d prices with %d provider calls, want 2 and 2", len(got), e.provider.priceCalls)
	}
	if _, err := e.svc.GetPricesByIDs(ctx, []string{"price_new"}); err != nil || e.provider.priceCalls != 2 {
		t.Errorf("second read of a fetched price called the provider again (%d calls)", e.provider.priceCalls)
	}
}

func TestCreatePromotionCode(t *testing.T) {
	e := setup(t)
	pc, err := e.svc.CreatePromotionCode(ctx, billing.PromotionCodeInput{Coupon: "c", Code: "FRIEND"})
	if err != nil || pc.Code != "FRIEND" || pc.PromotionID != "promo_1" {
		t.Errorf("CreatePromotionCode = %+v, %v", pc, err)
	}
	e.provider.promoErr = billing.ErrPromotionExists
	if _, err := e.svc.CreatePromotionCode(ctx, billing.PromotionCodeInput{Code: "FRIEND"}); !errors.Is(err, billing.ErrPromotionExists) {
		t.Errorf("taken code: %v", err)
	}
	e.provider.promoErr = errDown
	if _, err := e.svc.CreatePromotionCode(ctx, billing.PromotionCodeInput{Code: "X"}); !errors.Is(err, errDown) || errors.Is(err, billing.ErrPromotionExists) {
		t.Errorf("provider failure: %v", err)
	}
}

func TestReportUsage(t *testing.T) {
	e := setup(t)
	if err := e.svc.ReportUsage(ctx, "a@example.com", "pages", 3, "job_1"); !errors.Is(err, billing.ErrNotFound) {
		t.Errorf("unknown customer: %v", err)
	}
	e.seedCustomer(t, "a@example.com", "cus_1")
	if err := e.svc.ReportUsage(ctx, "a@example.com", "pages", 3, "job_1"); err != nil {
		t.Fatalf("ReportUsage: %v", err)
	}
	if want := (usageCall{meter: "pages", customerID: "cus_1", value: 3, identifier: "job_1"}); len(e.provider.usage) != 1 || e.provider.usage[0] != want {
		t.Errorf("usage = %+v", e.provider.usage)
	}
	e.provider.usageErr = errDown
	if err := e.svc.ReportUsage(ctx, "a@example.com", "pages", 1, "job_2"); !errors.Is(err, errDown) {
		t.Errorf("provider failure: %v", err)
	}
}
