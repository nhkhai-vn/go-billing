package stripe

import (
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	billing "github.com/nhkhai-vn/go-billing"
	gostripe "github.com/stripe/stripe-go/v87"
	"github.com/stripe/stripe-go/v87/webhook"
)

var secrets = []string{"whsec_old", "whsec_new"}

func eventPayload(eventType, object, apiVersion string) []byte {
	return fmt.Appendf(nil, `{"id":"evt_1","object":"event","type":%q,"api_version":%q,"data":{"object":%s}}`, eventType, apiVersion, object)
}

func sign(payload []byte, secret string) http.Header {
	signed := webhook.GenerateTestSignedPayload(&webhook.UnsignedPayload{Payload: payload, Secret: secret})
	return http.Header{"Stripe-Signature": {signed.Header}}
}

func parse(t *testing.T, eventType, object string) *billing.WebhookEvent {
	t.Helper()
	payload := eventPayload(eventType, object, gostripe.APIVersion)
	ev, err := New("sk_test", secrets).ParseWebhook(payload, sign(payload, "whsec_old"))
	if err != nil {
		t.Fatalf("parse %s: %v", eventType, err)
	}
	return ev
}

func TestParseWebhook_CheckoutCompleted(t *testing.T) {
	object := `{"id":"cs_1","object":"checkout.session","payment_status":"paid","customer":"cus_1","subscription":"sub_1",
		"metadata":{"app_id":"queue","customer_email":"u@example.com"}}`
	ev := parse(t, "checkout.session.completed", object)

	if ev.ID != "evt_1" || ev.Type != "checkout.session.completed" || ev.Kind != billing.EventCheckoutCompleted || string(ev.Data) != object {
		t.Errorf("event = %+v", ev)
	}
	want := billing.CheckoutState{ID: "cs_1", AppID: "queue", CustomerEmail: "u@example.com", CustomerID: "cus_1", Paid: true, SubscriptionID: "sub_1"}
	if ev.Checkout == nil || *ev.Checkout != want {
		t.Errorf("checkout = %+v, want %+v", ev.Checkout, want)
	}
}

func TestParseWebhook_CheckoutPaid(t *testing.T) {
	cases := map[string]bool{"paid": true, "no_payment_required": true, "unpaid": false}
	for status, paid := range cases {
		ev := parse(t, "checkout.session.completed", fmt.Sprintf(`{"id":"cs_1","object":"checkout.session","payment_status":%q}`, status))
		if ev.Checkout.Paid != paid {
			t.Errorf("payment_status %s: Paid = %v, want %v", status, ev.Checkout.Paid, paid)
		}
	}
}

func TestParseWebhook_CheckoutWithoutSubscription(t *testing.T) {
	ev := parse(t, "checkout.session.completed", `{"id":"cs_1","object":"checkout.session"}`)
	if ev.Checkout.SubscriptionID != "" || ev.Checkout.CustomerID != "" || ev.Checkout.Subscription != nil {
		t.Errorf("checkout = %+v", ev.Checkout)
	}
}

func TestParseWebhook_SubscriptionChanged(t *testing.T) {
	object := `{"id":"sub_1","object":"subscription","status":"active","cancel_at_period_end":true,"metadata":{"app_id":"queue"},
		"trial_end":1700000000,"canceled_at":1700000100,
		"items":{"object":"list","data":[{"id":"si_1","object":"subscription_item","quantity":4,"price":{"id":"price_1","object":"price"},
			"current_period_start":1700001000,"current_period_end":1702593000}]}}`
	for _, typ := range []string{"customer.subscription.updated", "customer.subscription.deleted"} {
		ev := parse(t, typ, object)
		if ev.Kind != billing.EventSubscriptionChanged || ev.Type != typ {
			t.Errorf("%s: kind %q type %q", typ, ev.Kind, ev.Type)
		}
		want := billing.SubscriptionState{
			ID: "sub_1", AppID: "queue", Status: "active", PriceID: "price_1", Quantity: 4, CancelAtPeriodEnd: true,
			CurrentPeriodStart: time.Unix(1700001000, 0).UTC(), CurrentPeriodEnd: time.Unix(1702593000, 0).UTC(),
			TrialEnd: time.Unix(1700000000, 0).UTC(), CanceledAt: time.Unix(1700000100, 0).UTC(),
		}
		if ev.Subscription == nil || *ev.Subscription != want {
			t.Errorf("%s: subscription = %+v, want %+v", typ, ev.Subscription, want)
		}
	}
}

func TestParseWebhook_SubscriptionZeroTimes(t *testing.T) {
	ev := parse(t, "customer.subscription.updated", `{"id":"sub_1","object":"subscription","status":"active","trial_end":0,"canceled_at":null,
		"items":{"object":"list","data":[{"id":"si_1","object":"subscription_item","current_period_start":0,"current_period_end":0}]}}`)
	s := ev.Subscription
	if !s.TrialEnd.IsZero() || !s.CanceledAt.IsZero() || !s.CurrentPeriodStart.IsZero() || !s.CurrentPeriodEnd.IsZero() {
		t.Errorf("times = %v %v %v %v, want all zero", s.TrialEnd, s.CanceledAt, s.CurrentPeriodStart, s.CurrentPeriodEnd)
	}
}

func TestParseWebhook_SubscriptionWithoutItems(t *testing.T) {
	ev := parse(t, "customer.subscription.updated", `{"id":"sub_1","object":"subscription","status":"canceled"}`)
	s := ev.Subscription
	if s.PriceID != "" || s.Quantity != 0 || !s.CurrentPeriodStart.IsZero() || !s.CurrentPeriodEnd.IsZero() {
		t.Errorf("subscription = %+v", s)
	}
}

func TestParseWebhook_CancellationReason(t *testing.T) {
	cases := map[string]string{
		`{"feedback":"too_expensive","comment":"budget cut"}`: "too_expensive — budget cut",
		`{"feedback":"unused","comment":null}`:                "unused",
		`{"feedback":null,"comment":"moving"}`:                "moving",
		`{"reason":"cancellation_requested"}`:                 "",
		`null`:                                                "",
	}
	for details, want := range cases {
		ev := parse(t, "customer.subscription.updated", `{"id":"sub_1","object":"subscription","cancellation_details":`+details+`}`)
		if ev.Subscription.CancellationReason != want {
			t.Errorf("%s: reason = %q, want %q", details, ev.Subscription.CancellationReason, want)
		}
	}
}

func TestParseWebhook_Invoice(t *testing.T) {
	object := `{"id":"in_1","object":"invoice","customer_email":"u@example.com","currency":"jpy","amount_paid":1000,"amount_due":1000,
		"billing_reason":"subscription_cycle","parent":{"type":"subscription_details","subscription_details":{"subscription":"sub_1"}}}`
	cases := map[string]billing.EventKind{"invoice.paid": billing.EventInvoicePaid, "invoice.payment_failed": billing.EventInvoicePaymentFailed}
	for typ, kind := range cases {
		ev := parse(t, typ, object)
		if ev.Kind != kind {
			t.Errorf("%s: kind = %q, want %q", typ, ev.Kind, kind)
		}
		want := billing.Invoice{SubscriptionID: "sub_1", CustomerEmail: "u@example.com", Currency: "jpy", AmountPaid: 1000, AmountDue: 1000, Reason: billing.InvoiceRenewal}
		if ev.Invoice == nil || *ev.Invoice != want {
			t.Errorf("%s: invoice = %+v, want %+v", typ, ev.Invoice, want)
		}
	}
}

func TestParseWebhook_InvoiceReason(t *testing.T) {
	cases := map[string]billing.InvoiceReason{
		"subscription_create": billing.InvoiceNew,
		"subscription_cycle":  billing.InvoiceRenewal,
		"subscription_update": billing.InvoiceChange,
		"manual":              "",
	}
	for reason, want := range cases {
		ev := parse(t, "invoice.paid", fmt.Sprintf(`{"id":"in_1","object":"invoice","billing_reason":%q}`, reason))
		if ev.Invoice.Reason != want {
			t.Errorf("billing_reason %s: Reason = %q, want %q", reason, ev.Invoice.Reason, want)
		}
	}
}

func TestParseWebhook_InvoiceWithoutParent(t *testing.T) {
	ev := parse(t, "invoice.paid", `{"id":"in_1","object":"invoice","amount_paid":500}`)
	if ev.Invoice.SubscriptionID != "" || ev.Invoice.AmountPaid != 500 {
		t.Errorf("invoice = %+v", ev.Invoice)
	}
}

func TestParseWebhook_Price(t *testing.T) {
	object := `{"id":"price_1","object":"price","product":"prod_1","unit_amount":1000,"currency":"jpy","active":true,
		"recurring":{"interval":"year","interval_count":1},"nickname":"Pro","type":"recurring","tax_behavior":"exclusive"}`
	cases := map[string]billing.EventKind{
		"price.created": billing.EventPriceChanged,
		"price.updated": billing.EventPriceChanged,
		"price.deleted": billing.EventPriceDeleted,
	}
	for typ, kind := range cases {
		ev := parse(t, typ, object)
		if ev.Kind != kind {
			t.Errorf("%s: kind = %q, want %q", typ, ev.Kind, kind)
		}
		want := billing.Price{ID: "price_1", ProductID: "prod_1", Amount: 1000, Currency: "jpy", Interval: "year", IntervalCount: 1, Active: true, Nickname: "Pro", Type: "recurring", TaxBehavior: "exclusive"}
		if ev.Price == nil || *ev.Price != want {
			t.Errorf("%s: price = %+v, want %+v", typ, ev.Price, want)
		}
	}
}

func TestParseWebhook_UnknownType(t *testing.T) {
	ev := parse(t, "customer.created", `{"id":"cus_1","object":"customer"}`)
	if ev.Kind != "" || ev.Type != "customer.created" || ev.Checkout != nil || ev.Subscription != nil || ev.Invoice != nil || ev.Price != nil {
		t.Errorf("event = %+v", ev)
	}
	if string(ev.Data) != `{"id":"cus_1","object":"customer"}` {
		t.Errorf("data = %s", ev.Data)
	}
}

func TestParseWebhook_SecondSecret(t *testing.T) {
	payload := eventPayload("invoice.paid", `{"id":"in_1","object":"invoice"}`, gostripe.APIVersion)
	ev, err := New("sk_test", secrets).ParseWebhook(payload, sign(payload, "whsec_new"))
	if err != nil || ev.ID != "evt_1" {
		t.Fatalf("event %+v, err %v", ev, err)
	}
}

func TestParseWebhook_SignatureErrors(t *testing.T) {
	payload := eventPayload("invoice.paid", `{"id":"in_1","object":"invoice"}`, gostripe.APIVersion)
	cases := map[string]struct {
		secrets []string
		header  http.Header
	}{
		"missing header": {secrets, http.Header{}},
		"wrong secret":   {secrets, sign(payload, "whsec_other")},
		"no secrets":     {nil, sign(payload, "whsec_old")},
		"garbled header": {secrets, http.Header{"Stripe-Signature": {"t=x,v1=y"}}},
	}
	for name, c := range cases {
		_, err := New("sk_test", c.secrets).ParseWebhook(payload, c.header)
		if !errors.Is(err, billing.ErrWebhookSignature) {
			t.Errorf("%s: err = %v, want billing.ErrWebhookSignature", name, err)
		}
	}
}

func TestParseWebhook_TamperedPayload(t *testing.T) {
	payload := eventPayload("invoice.paid", `{"id":"in_1","object":"invoice","amount_paid":1}`, gostripe.APIVersion)
	header := sign(payload, "whsec_old")
	tampered := eventPayload("invoice.paid", `{"id":"in_1","object":"invoice","amount_paid":9}`, gostripe.APIVersion)
	if _, err := New("sk_test", secrets).ParseWebhook(tampered, header); !errors.Is(err, billing.ErrWebhookSignature) {
		t.Errorf("err = %v, want billing.ErrWebhookSignature", err)
	}
}

func TestParseWebhook_APIVersionMismatch(t *testing.T) {
	payload := eventPayload("invoice.paid", `{"id":"in_1","object":"invoice"}`, "2020-01-01.other")
	_, err := New("sk_test", secrets).ParseWebhook(payload, sign(payload, "whsec_old"))
	if !errors.Is(err, billing.ErrWebhookSignature) {
		t.Errorf("err = %v, want billing.ErrWebhookSignature", err)
	}
	if errors.Is(err, webhook.ErrNoValidSignature) {
		t.Errorf("err = %v, want the API version error, not ErrNoValidSignature", err)
	}
}

func TestParseWebhook_MalformedObjectIsNotSignatureError(t *testing.T) {
	for _, typ := range []string{"checkout.session.completed", "customer.subscription.updated", "invoice.paid", "price.updated"} {
		payload := eventPayload(typ, `{"id":5}`, gostripe.APIVersion)
		_, err := New("sk_test", secrets).ParseWebhook(payload, sign(payload, "whsec_old"))
		if err == nil || errors.Is(err, billing.ErrWebhookSignature) {
			t.Errorf("%s: err = %v, want a decode error other than billing.ErrWebhookSignature", typ, err)
		}
	}
}
