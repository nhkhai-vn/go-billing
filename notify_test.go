package billing_test

import (
	"strings"
	"testing"

	billing "github.com/nhkhai-vn/go-billing"
)

func (e *env) only(t *testing.T) billing.Notification {
	t.Helper()
	if len(e.notifier.sent) != 1 {
		t.Fatalf("sent %d notifications, want 1: %+v", len(e.notifier.sent), e.notifier.sent)
	}
	return e.notifier.sent[0]
}

func wantText(t *testing.T, n billing.Notification, lines ...string) {
	t.Helper()
	for _, l := range lines {
		if !strings.Contains(n.Text, l) {
			t.Errorf("notification %q lacks %q", n.Text, l)
		}
	}
}

func TestNotify_CheckoutStarted(t *testing.T) {
	t.Run("trial", func(t *testing.T) {
		e := setup(t)
		state := subState(billing.StatusTrialing)
		state.TrialEnd = periodStart.AddDate(0, 0, 14)
		e.provider.subs["sub_1"] = state
		if err := e.deliver(t, checkoutEvent(app)); err != nil {
			t.Fatal(err)
		}
		n := e.only(t)
		if n.Title != "🆕 Trial started" || n.Warn {
			t.Errorf("notification = %+v", n)
		}
		wantText(t, n, "App: queue", "Customer: a@example.com", "Status: trialing", "Trial ends: 2023-11-28")
	})
	t.Run("paid", func(t *testing.T) {
		e := setup(t)
		e.provider.subs["sub_1"] = subState(billing.StatusActive)
		if err := e.deliver(t, checkoutEvent(app)); err != nil {
			t.Fatal(err)
		}
		n := e.only(t)
		if n.Title != "✅ Subscription started" || strings.Contains(n.Text, "Trial") {
			t.Errorf("notification = %+v", n)
		}
	})
}

func TestNotify_PaymentSucceeded(t *testing.T) {
	tests := map[string]struct {
		reason   billing.InvoiceReason
		currency string
		amount   int64
		title    string
		amountTx string
	}{
		"new":     {reason: billing.InvoiceNew, currency: "jpy", amount: 1000, title: "💰 Payment succeeded (new subscription)", amountTx: "Amount: JPY 1000"},
		"renewal": {reason: billing.InvoiceRenewal, currency: "usd", amount: 1234, title: "💰 Payment succeeded (renewal)", amountTx: "Amount: USD 12.34"},
		"change":  {reason: billing.InvoiceChange, currency: "krw", amount: 5000, title: "💰 Payment succeeded (plan change)", amountTx: "Amount: KRW 5000"},
		"other":   {currency: "eur", amount: 50, title: "💰 Payment succeeded", amountTx: "Amount: EUR 0.50"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			e := setup(t)
			e.provider.subs["sub_1"] = subState(billing.StatusActive)
			if err := e.store.Prices.Upsert(ctx, &billing.Price{ID: "price_1", Nickname: "SuguQ monthly"}); err != nil {
				t.Fatal(err)
			}
			inv := &billing.Invoice{SubscriptionID: "sub_1", CustomerEmail: "a@example.com", Currency: tc.currency, AmountPaid: tc.amount, Reason: tc.reason}
			if err := e.deliver(t, &billing.WebhookEvent{ID: "evt_1", Kind: billing.EventInvoicePaid, Invoice: inv}); err != nil {
				t.Fatal(err)
			}
			n := e.only(t)
			if n.Title != tc.title || n.Warn {
				t.Errorf("notification = %+v", n)
			}
			wantText(t, n, "App: queue", "Customer: a@example.com", "Product: SuguQ monthly", tc.amountTx)
		})
	}
}

func TestNotify_PaymentSucceededProductFallsBackToPriceID(t *testing.T) {
	e := setup(t)
	e.provider.subs["sub_1"] = subState(billing.StatusActive)
	inv := &billing.Invoice{SubscriptionID: "sub_1", Currency: "jpy", AmountPaid: 1000}
	if err := e.deliver(t, &billing.WebhookEvent{ID: "evt_1", Kind: billing.EventInvoicePaid, Invoice: inv}); err != nil {
		t.Fatal(err)
	}
	wantText(t, e.only(t), "Product: price_1")
}

func TestNotify_PaymentSucceededSkipsZeroAmount(t *testing.T) {
	e := setup(t)
	e.provider.subs["sub_1"] = subState(billing.StatusTrialing)
	inv := &billing.Invoice{SubscriptionID: "sub_1", Currency: "jpy", Reason: billing.InvoiceNew}
	if err := e.deliver(t, &billing.WebhookEvent{ID: "evt_1", Kind: billing.EventInvoicePaid, Invoice: inv}); err != nil {
		t.Fatal(err)
	}
	if len(e.notifier.sent) != 0 {
		t.Errorf("a zero invoice notified: %+v", e.notifier.sent)
	}
}

func TestNotify_PaymentFailed(t *testing.T) {
	e := setup(t)
	e.seedCustomer(t, "a@example.com", "cus_1")
	e.seedSubscription(t, "sub_1", billing.StatusActive, 1)
	inv := &billing.Invoice{SubscriptionID: "sub_1", CustomerEmail: "a@example.com", Currency: "jpy", AmountDue: 1000}
	if err := e.deliver(t, &billing.WebhookEvent{ID: "evt_1", Kind: billing.EventInvoicePaymentFailed, Invoice: inv}); err != nil {
		t.Fatal(err)
	}
	n := e.only(t)
	if n.Title != "⚠️ Payment failed" || !n.Warn {
		t.Errorf("notification = %+v", n)
	}
	wantText(t, n, "Amount: JPY 1000", "Product: price_1")
}

func TestNotify_Cancellation(t *testing.T) {
	t.Run("scheduled", func(t *testing.T) {
		e := setup(t)
		e.seedSubscription(t, "sub_1", billing.StatusActive, 1)
		state := subState(billing.StatusActive)
		state.CancelAtPeriodEnd, state.CancellationReason = true, "too_expensive — need fewer shops"
		if err := e.deliver(t, &billing.WebhookEvent{ID: "evt_1", Kind: billing.EventSubscriptionChanged, Subscription: state}); err != nil {
			t.Fatal(err)
		}
		n := e.only(t)
		if n.Title != "⚠️ Cancellation scheduled" || !n.Warn {
			t.Errorf("notification = %+v", n)
		}
		wantText(t, n, "App: queue", "Customer: a@example.com", "Ends: 2023-12-14", "Reason: too_expensive — need fewer shops")
	})
	t.Run("ended", func(t *testing.T) {
		e := setup(t)
		e.seedSubscription(t, "sub_1", billing.StatusActive, 1)
		if err := e.deliver(t, &billing.WebhookEvent{ID: "evt_1", Kind: billing.EventSubscriptionChanged, Subscription: subState(billing.StatusCanceled)}); err != nil {
			t.Fatal(err)
		}
		n := e.only(t)
		if n.Title != "🚫 Subscription canceled" || !n.Warn {
			t.Errorf("notification = %+v", n)
		}
		wantText(t, n, "Customer: a@example.com")
	})
	t.Run("no transition", func(t *testing.T) {
		e := setup(t)
		e.seedSubscription(t, "sub_1", billing.StatusActive, 1)
		if err := e.deliver(t, &billing.WebhookEvent{ID: "evt_1", Kind: billing.EventSubscriptionChanged, Subscription: subState(billing.StatusActive)}); err != nil {
			t.Fatal(err)
		}
		if len(e.notifier.sent) != 0 {
			t.Errorf("notified without a cancellation: %+v", e.notifier.sent)
		}
	})
}

func TestNotify_FailureDoesNotFailTheWebhook(t *testing.T) {
	e := setup(t)
	e.notifier.err = errDown
	e.provider.subs["sub_1"] = subState(billing.StatusActive)
	if err := e.deliver(t, checkoutEvent(app)); err != nil {
		t.Errorf("err = %v", err)
	}
}

func TestNotify_WithoutNotifier(t *testing.T) {
	e := setup(t)
	svc, err := billing.New(billing.Config{AppID: app, Store: e.store, Provider: e.provider})
	if err != nil {
		t.Fatal(err)
	}
	e.provider.subs["sub_1"] = subState(billing.StatusActive)
	e.provider.event = checkoutEvent(app)
	if err := svc.HandleWebhook(ctx, nil, nil); err != nil {
		t.Errorf("err = %v", err)
	}
}
