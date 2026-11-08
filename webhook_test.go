package billing_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	billing "github.com/nhkhai-vn/go-billing"
)

type failingEvents struct {
	billing.EventRepository
	claimErr, completeErr, releaseErr error
}

func (f failingEvents) Claim(ctx context.Context, id string, lease time.Duration) error {
	if f.claimErr != nil {
		return f.claimErr
	}
	return f.EventRepository.Claim(ctx, id, lease)
}

func (f failingEvents) Complete(ctx context.Context, e *billing.Event) error {
	if f.completeErr != nil {
		return f.completeErr
	}
	return f.EventRepository.Complete(ctx, e)
}

func (f failingEvents) Release(ctx context.Context, id string) error {
	if f.releaseErr != nil {
		return f.releaseErr
	}
	return f.EventRepository.Release(ctx, id)
}

func (e *env) deliver(t *testing.T, ev *billing.WebhookEvent) error {
	t.Helper()
	e.provider.event = ev
	return e.svc.HandleWebhook(ctx, []byte(`{}`), nil)
}

// recorded reports whether evt_1 was completed, by trying to claim it.
func (e *env) recorded(t *testing.T) bool {
	t.Helper()
	const id = "evt_1"
	err := e.store.Events.Claim(ctx, id, 0)
	switch {
	case errors.Is(err, billing.ErrDuplicate):
		return true
	case err != nil:
		t.Fatalf("claim %s: %v", id, err)
	}
	if err := e.store.Events.Release(ctx, id); err != nil {
		t.Fatalf("release %s: %v", id, err)
	}
	return false
}

var periodStart = time.Unix(1_700_000_000, 0)

func subState(status string) *billing.SubscriptionState {
	return &billing.SubscriptionState{
		ID: "sub_1", AppID: app, Status: status, PriceID: "price_1", Quantity: 1,
		CurrentPeriodStart: periodStart, CurrentPeriodEnd: periodStart.AddDate(0, 1, 0),
	}
}

func checkoutEvent(appID string) *billing.WebhookEvent {
	return &billing.WebhookEvent{
		ID: "evt_1", Type: "checkout.session.completed", Kind: billing.EventCheckoutCompleted, Data: []byte(`{"id":"cs_1"}`),
		Checkout: &billing.CheckoutState{ID: "cs_1", AppID: appID, CustomerEmail: "a@example.com", CustomerID: "cus_1", Paid: true, SubscriptionID: "sub_1"},
	}
}

func TestHandleWebhook_SignatureFailure(t *testing.T) {
	e := setup(t)
	e.provider.parseErr = fmt.Errorf("verify: %w", billing.ErrWebhookSignature)
	if err := e.deliver(t, nil); !errors.Is(err, billing.ErrWebhookSignature) {
		t.Errorf("err = %v", err)
	}
}

func TestHandleWebhook_CheckoutCompletedCreatesCustomerAndSubscription(t *testing.T) {
	e := setup(t)
	e.provider.subs["sub_1"] = subState(billing.StatusActive)
	if err := e.deliver(t, checkoutEvent(app)); err != nil {
		t.Fatalf("HandleWebhook: %v", err)
	}
	cust, err := e.store.Customers.FindByProviderID(ctx, "cus_1")
	if err != nil || cust.ID != app+"|a@example.com" || cust.Email != "a@example.com" || cust.AppID != app {
		t.Errorf("customer = %+v, %v", cust, err)
	}
	sub := e.subscription(t)
	if sub.CustomerID != cust.ID || sub.Status != billing.StatusActive || !sub.CurrentPeriodStart.Equal(periodStart) {
		t.Errorf("subscription = %+v", sub)
	}
	if !e.recorded(t) {
		t.Errorf("event not recorded")
	}
	if len(e.notifier.sent) != 1 || e.notifier.sent[0].Title != "✅ Subscription started" {
		t.Errorf("notifications = %+v", e.notifier.sent)
	}
}

func TestHandleWebhook_CheckoutCompletedKeepsKnownCustomer(t *testing.T) {
	e := setup(t)
	known := e.seedCustomer(t, "a@example.com", "cus_1")
	e.provider.subs["sub_1"] = subState(billing.StatusActive)
	ev := checkoutEvent(app)
	ev.Checkout.CustomerEmail = "changed@example.com"
	if err := e.deliver(t, ev); err != nil {
		t.Fatalf("HandleWebhook: %v", err)
	}
	if e.subscription(t).CustomerID != known.ID {
		t.Errorf("subscription not on the known customer")
	}
}

func TestHandleWebhook_CheckoutCompletedSkips(t *testing.T) {
	tests := map[string]func(*billing.CheckoutState){
		"no subscription": func(c *billing.CheckoutState) { c.SubscriptionID = "" },
		"no app":          func(c *billing.CheckoutState) { c.AppID = "" },
		"no email":        func(c *billing.CheckoutState) { c.CustomerEmail = "" },
	}
	for name, change := range tests {
		t.Run(name, func(t *testing.T) {
			e := setup(t)
			ev := checkoutEvent(app)
			change(ev.Checkout)
			if err := e.deliver(t, ev); err != nil {
				t.Fatalf("HandleWebhook: %v", err)
			}
			if _, err := e.store.Customers.FindByProviderID(ctx, "cus_1"); !errors.Is(err, billing.ErrNotFound) || e.provider.getSubCalls != 0 {
				t.Errorf("a skipped checkout stored a customer or read the subscription")
			}
			if !e.recorded(t) {
				t.Errorf("a skipped event of this endpoint is still recorded")
			}
		})
	}
}

func TestHandleWebhook_IgnoresOtherAppsEvents(t *testing.T) {
	other := subState(billing.StatusCanceled)
	other.AppID = "ocr"
	tests := map[string]*billing.WebhookEvent{
		"checkout":     checkoutEvent("ocr"),
		"subscription": {ID: "evt_1", Kind: billing.EventSubscriptionChanged, Subscription: other},
		"invoice":      {ID: "evt_1", Kind: billing.EventInvoicePaid, Invoice: &billing.Invoice{SubscriptionID: "sub_1", AmountPaid: 1000}},
	}
	for name, ev := range tests {
		t.Run(name, func(t *testing.T) {
			e := setup(t)
			e.seedCustomer(t, "a@example.com", "cus_1")
			e.seedSubscription(t, "sub_1", billing.StatusActive, 1)
			e.provider.subs["sub_1"] = other
			if err := e.deliver(t, ev); err != nil {
				t.Fatalf("HandleWebhook: %v", err)
			}
			if e.subscription(t).Status != billing.StatusActive || len(e.notifier.sent) != 0 || e.recorded(t) {
				t.Errorf("another app's event changed state, notified or was recorded")
			}
		})
	}
}

func TestHandleWebhook_DuplicateSkipped(t *testing.T) {
	e := setup(t)
	e.provider.subs["sub_1"] = subState(billing.StatusActive)
	for range 2 {
		if err := e.deliver(t, checkoutEvent(app)); err != nil {
			t.Fatalf("HandleWebhook: %v", err)
		}
	}
	if e.provider.getSubCalls != 1 || len(e.notifier.sent) != 1 {
		t.Errorf("a redelivery was processed again: %d reads, %d notifications", e.provider.getSubCalls, len(e.notifier.sent))
	}
}

func TestHandleWebhook_InProgressIsRetryable(t *testing.T) {
	e := setup(t)
	if err := e.store.Events.Claim(ctx, "evt_1", time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := e.deliver(t, checkoutEvent(app)); !errors.Is(err, billing.ErrInProgress) {
		t.Errorf("err = %v, want ErrInProgress", err)
	}
	if e.provider.getSubCalls != 0 {
		t.Errorf("an event held by another delivery was processed")
	}
}

func TestHandleWebhook_ClaimFailure(t *testing.T) {
	e := setup(t, func(s *billing.Store) { s.Events = failingEvents{EventRepository: s.Events, claimErr: errDown} })
	if err := e.deliver(t, checkoutEvent(app)); !errors.Is(err, errDown) || e.provider.getSubCalls != 0 {
		t.Errorf("err = %v, reads %d", err, e.provider.getSubCalls)
	}
}

func TestHandleWebhook_FailureReleasesSoTheRetryProcesses(t *testing.T) {
	e := setup(t)
	e.provider.getSubErr = errDown
	if err := e.deliver(t, checkoutEvent(app)); !errors.Is(err, errDown) {
		t.Fatalf("err = %v, want errDown", err)
	}
	e.provider.getSubErr = nil
	e.provider.subs["sub_1"] = subState(billing.StatusActive)
	if err := e.deliver(t, checkoutEvent(app)); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if _, err := e.store.Subscriptions.FindByProviderID(ctx, "sub_1"); err != nil {
		t.Errorf("the retry did not process the event: %v", err)
	}
}

func TestHandleWebhook_StoreFailuresAreLoggedNotReturned(t *testing.T) {
	e := setup(t, func(s *billing.Store) {
		s.Events = failingEvents{EventRepository: s.Events, completeErr: errDown, releaseErr: errDown}
	})
	if err := e.deliver(t, &billing.WebhookEvent{ID: "evt_1", Type: "customer.created"}); err != nil {
		t.Errorf("a failed Complete failed the delivery: %v", err)
	}
	e.provider.getSubErr = errDown
	ev := checkoutEvent(app)
	ev.ID = "evt_2"
	if err := e.deliver(t, ev); !errors.Is(err, errDown) {
		t.Errorf("a failed Release hid the processing error: %v", err)
	}
}

func TestHandleWebhook_UnhandledTypeIsRecorded(t *testing.T) {
	e := setup(t)
	if err := e.deliver(t, &billing.WebhookEvent{ID: "evt_1", Type: "customer.created"}); err != nil {
		t.Fatalf("HandleWebhook: %v", err)
	}
	if !e.recorded(t) {
		t.Errorf("unhandled event not recorded")
	}
}

func TestHandleWebhook_SubscriptionChanged(t *testing.T) {
	e := setup(t)
	e.seedCustomer(t, "a@example.com", "cus_1")
	prior := e.seedSubscription(t, "sub_1", billing.StatusActive, 1)
	state := subState(billing.StatusCanceled)
	state.Quantity, state.TrialEnd, state.CanceledAt = 4, periodStart.AddDate(0, 0, 7), periodStart.AddDate(0, 0, 20)
	if err := e.deliver(t, &billing.WebhookEvent{ID: "evt_1", Kind: billing.EventSubscriptionChanged, Subscription: state}); err != nil {
		t.Fatalf("HandleWebhook: %v", err)
	}
	sub := e.subscription(t)
	if sub.ID != prior.ID || sub.Status != billing.StatusCanceled || sub.Quantity != 4 || !sub.CreatedAt.Equal(prior.CreatedAt) ||
		!sub.CurrentPeriodStart.Equal(periodStart) || !sub.CurrentPeriodEnd.Equal(periodStart.AddDate(0, 1, 0)) ||
		!sub.TrialEnd.Equal(state.TrialEnd) || !sub.CanceledAt.Equal(state.CanceledAt) {
		t.Errorf("subscription = %+v", sub)
	}
}

func TestHandleWebhook_UnknownSubscriptionSkipped(t *testing.T) {
	e := setup(t)
	if err := e.deliver(t, &billing.WebhookEvent{ID: "evt_1", Kind: billing.EventSubscriptionChanged, Subscription: subState(billing.StatusActive)}); err != nil {
		t.Fatalf("subscription changed: %v", err)
	}
	if err := e.deliver(t, &billing.WebhookEvent{ID: "evt_2", Kind: billing.EventInvoicePaymentFailed, Invoice: &billing.Invoice{SubscriptionID: "sub_1"}}); err != nil {
		t.Fatalf("payment failed: %v", err)
	}
	if _, err := e.store.Subscriptions.FindByProviderID(ctx, "sub_1"); !errors.Is(err, billing.ErrNotFound) || len(e.notifier.sent) != 0 {
		t.Errorf("an unknown subscription was stored or notified")
	}
}

func TestHandleWebhook_InvoicePaidUpdatesSubscription(t *testing.T) {
	e := setup(t)
	e.seedCustomer(t, "a@example.com", "cus_1")
	e.seedSubscription(t, "sub_1", billing.StatusTrialing, 1)
	e.provider.subs["sub_1"] = subState(billing.StatusActive)
	inv := &billing.Invoice{SubscriptionID: "sub_1", CustomerEmail: "a@example.com", Currency: "jpy", AmountPaid: 1000, Reason: billing.InvoiceRenewal}
	if err := e.deliver(t, &billing.WebhookEvent{ID: "evt_1", Kind: billing.EventInvoicePaid, Invoice: inv}); err != nil {
		t.Fatalf("HandleWebhook: %v", err)
	}
	if e.subscription(t).Status != billing.StatusActive {
		t.Errorf("status not updated")
	}
}

func TestHandleWebhook_InvoiceWithoutSubscription(t *testing.T) {
	for _, kind := range []billing.EventKind{billing.EventInvoicePaid, billing.EventInvoicePaymentFailed} {
		e := setup(t)
		if err := e.deliver(t, &billing.WebhookEvent{ID: "evt_1", Kind: kind, Invoice: &billing.Invoice{AmountPaid: 500}}); err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		if e.provider.getSubCalls != 0 || len(e.notifier.sent) != 0 {
			t.Errorf("%s: a one-off invoice was treated as a subscription's", kind)
		}
	}
}

func TestHandleWebhook_InvoicePaymentFailedMarksPastDue(t *testing.T) {
	e := setup(t)
	e.seedCustomer(t, "a@example.com", "cus_1")
	e.seedSubscription(t, "sub_1", billing.StatusActive, 1)
	if err := e.deliver(t, &billing.WebhookEvent{ID: "evt_1", Kind: billing.EventInvoicePaymentFailed, Invoice: &billing.Invoice{SubscriptionID: "sub_1"}}); err != nil {
		t.Fatalf("HandleWebhook: %v", err)
	}
	if e.subscription(t).Status != billing.StatusPastDue {
		t.Errorf("status = %s", e.subscription(t).Status)
	}
}

func TestHandleWebhook_Prices(t *testing.T) {
	e := setup(t)
	price := func() *billing.Price {
		return &billing.Price{ID: "price_1", ProductID: "prod_1", Amount: 1000, Currency: "jpy", Active: true, Nickname: "Monthly"}
	}
	if err := e.deliver(t, &billing.WebhookEvent{ID: "evt_1", Kind: billing.EventPriceChanged, Price: price()}); err != nil {
		t.Fatalf("price changed: %v", err)
	}
	got, _ := e.store.Prices.FindByIDs(ctx, []string{"price_1"})
	if len(got) != 1 || !got[0].Active || got[0].Nickname != "Monthly" {
		t.Errorf("cached = %+v", got)
	}
	if err := e.deliver(t, &billing.WebhookEvent{ID: "evt_2", Kind: billing.EventPriceDeleted, Price: price()}); err != nil {
		t.Fatalf("price deleted: %v", err)
	}
	got, _ = e.store.Prices.FindByIDs(ctx, []string{"price_1"})
	if len(got) != 1 || got[0].Active {
		t.Errorf("a deleted price is still active: %+v", got)
	}
}

func TestHandleWebhook_ProcessingErrorNamesTheEventType(t *testing.T) {
	e := setup(t)
	e.provider.getSubErr = errDown
	err := e.deliver(t, checkoutEvent(app))
	if err == nil || !strings.Contains(err.Error(), "checkout.session.completed") {
		t.Errorf("err = %v", err)
	}
}

type brokenSubscriptions struct{ billing.SubscriptionRepository }

func (brokenSubscriptions) FindByProviderID(context.Context, string) (*billing.Subscription, error) {
	return nil, errDown
}

func (brokenSubscriptions) Upsert(context.Context, *billing.Subscription) error { return errDown }

func TestHandleWebhook_StoreFailureIsRetried(t *testing.T) {
	tests := map[string]*billing.WebhookEvent{
		"checkout":       checkoutEvent(app),
		"subscription":   {ID: "evt_1", Kind: billing.EventSubscriptionChanged, Subscription: subState(billing.StatusActive)},
		"invoice paid":   {ID: "evt_1", Kind: billing.EventInvoicePaid, Invoice: &billing.Invoice{SubscriptionID: "sub_1"}},
		"payment failed": {ID: "evt_1", Kind: billing.EventInvoicePaymentFailed, Invoice: &billing.Invoice{SubscriptionID: "sub_1"}},
	}
	for name, ev := range tests {
		t.Run(name, func(t *testing.T) {
			e := setup(t, func(s *billing.Store) { s.Subscriptions = brokenSubscriptions{s.Subscriptions} })
			e.provider.subs["sub_1"] = subState(billing.StatusActive)
			if err := e.deliver(t, ev); !errors.Is(err, errDown) {
				t.Errorf("err = %v, want errDown", err)
			}
			if e.recorded(t) {
				t.Errorf("a failed event was recorded")
			}
		})
	}
}
