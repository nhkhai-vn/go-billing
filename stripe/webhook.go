package stripe

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	billing "github.com/nhkhai-vn/go-billing"
	gostripe "github.com/stripe/stripe-go/v87"
	"github.com/stripe/stripe-go/v87/webhook"
)

// ParseWebhook verifies the Stripe-Signature header against each webhook secret in turn and decodes the event. An
// event type the Service does not handle comes back with an empty Kind.
func (p *Provider) ParseWebhook(payload []byte, header http.Header) (*billing.WebhookEvent, error) {
	event, err := p.constructEvent(payload, header.Get("Stripe-Signature"))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", billing.ErrWebhookSignature, err)
	}

	ev := &billing.WebhookEvent{ID: event.ID, Type: string(event.Type)}
	if event.Data != nil {
		ev.Data = event.Data.Raw
	}
	if err := decodeObject(ev); err != nil {
		return nil, fmt.Errorf("decode %s %s: %w", ev.Type, ev.ID, err)
	}
	return ev, nil
}

// constructEvent accepts an event signed by any configured secret: during an API version upgrade an endpoint on each
// version delivers here.
func (p *Provider) constructEvent(payload []byte, signature string) (gostripe.Event, error) {
	err := webhook.ErrNoValidSignature
	for _, secret := range p.webhookSecrets {
		var event gostripe.Event
		event, err = webhook.ConstructEvent(payload, signature, secret)
		if err == nil {
			return event, nil
		}
		if !errors.Is(err, webhook.ErrNoValidSignature) {
			break
		}
	}
	return gostripe.Event{}, fmt.Errorf("verify signature: %w", err)
}

// eventKinds maps the Stripe event types the Service handles to their Kind.
var eventKinds = map[string]billing.EventKind{
	"checkout.session.completed":    billing.EventCheckoutCompleted,
	"customer.subscription.updated": billing.EventSubscriptionChanged,
	"customer.subscription.deleted": billing.EventSubscriptionChanged,
	"invoice.paid":                  billing.EventInvoicePaid,
	"invoice.payment_failed":        billing.EventInvoicePaymentFailed,
	"price.created":                 billing.EventPriceChanged,
	"price.updated":                 billing.EventPriceChanged,
	"price.deleted":                 billing.EventPriceDeleted,
}

// decodeObject sets ev.Kind and the field matching it from ev.Data.
func decodeObject(ev *billing.WebhookEvent) error {
	ev.Kind = eventKinds[ev.Type]
	switch ev.Kind {
	case billing.EventCheckoutCompleted:
		var sess gostripe.CheckoutSession
		if err := json.Unmarshal(ev.Data, &sess); err != nil {
			return err
		}
		ev.Checkout = checkoutState(&sess)
	case billing.EventSubscriptionChanged:
		var sub gostripe.Subscription
		if err := json.Unmarshal(ev.Data, &sub); err != nil {
			return err
		}
		ev.Subscription = subscriptionState(&sub)
	case billing.EventInvoicePaid, billing.EventInvoicePaymentFailed:
		var inv gostripe.Invoice
		if err := json.Unmarshal(ev.Data, &inv); err != nil {
			return err
		}
		ev.Invoice = invoice(&inv)
	case billing.EventPriceChanged, billing.EventPriceDeleted:
		var sp gostripe.Price
		if err := json.Unmarshal(ev.Data, &sp); err != nil {
			return err
		}
		ev.Price = price(&sp)
	}
	return nil
}
