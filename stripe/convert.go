package stripe

import (
	"strings"
	"time"

	billing "github.com/nhkhai-vn/go-billing"
	gostripe "github.com/stripe/stripe-go/v87"
)

// checkoutState converts a session; Subscription is left nil because a webhook carries only its id.
func checkoutState(sess *gostripe.CheckoutSession) *billing.CheckoutState {
	state := &billing.CheckoutState{
		ID:            sess.ID,
		AppID:         sess.Metadata["app_id"],
		CustomerEmail: sess.Metadata["customer_email"],
		Paid: sess.PaymentStatus == gostripe.CheckoutSessionPaymentStatusPaid ||
			sess.PaymentStatus == gostripe.CheckoutSessionPaymentStatusNoPaymentRequired,
	}
	if sess.Customer != nil {
		state.CustomerID = sess.Customer.ID
	}
	if sess.Subscription != nil {
		state.SubscriptionID = sess.Subscription.ID
	}
	return state
}

// subscriptionState converts a subscription. Checkout creates the recurring price as the first item, and Stripe keeps
// the current period on the item, not the subscription.
func subscriptionState(sub *gostripe.Subscription) *billing.SubscriptionState {
	state := &billing.SubscriptionState{
		ID:                 sub.ID,
		AppID:              sub.Metadata["app_id"],
		Status:             string(sub.Status),
		CancelAtPeriodEnd:  sub.CancelAtPeriodEnd,
		TrialEnd:           unixTime(sub.TrialEnd),
		CanceledAt:         unixTime(sub.CanceledAt),
		CancellationReason: cancellationReason(sub.CancellationDetails),
	}
	if sub.Items == nil || len(sub.Items.Data) == 0 {
		return state
	}
	item := sub.Items.Data[0]
	if item.Price != nil {
		state.PriceID = item.Price.ID
	}
	state.Quantity = item.Quantity
	state.CurrentPeriodStart = unixTime(item.CurrentPeriodStart)
	state.CurrentPeriodEnd = unixTime(item.CurrentPeriodEnd)
	return state
}

func cancellationReason(d *gostripe.SubscriptionCancellationDetails) string {
	if d == nil {
		return ""
	}
	var parts []string
	if d.Feedback != "" {
		parts = append(parts, string(d.Feedback))
	}
	if d.Comment != "" {
		parts = append(parts, d.Comment)
	}
	return strings.Join(parts, " — ")
}

func invoice(inv *gostripe.Invoice) *billing.Invoice {
	out := &billing.Invoice{
		CustomerEmail: inv.CustomerEmail,
		Currency:      string(inv.Currency),
		AmountPaid:    inv.AmountPaid,
		AmountDue:     inv.AmountDue,
		Reason:        invoiceReason(inv.BillingReason),
	}
	if inv.Parent != nil && inv.Parent.SubscriptionDetails != nil && inv.Parent.SubscriptionDetails.Subscription != nil {
		out.SubscriptionID = inv.Parent.SubscriptionDetails.Subscription.ID
	}
	return out
}

func invoiceReason(r gostripe.InvoiceBillingReason) billing.InvoiceReason {
	switch r {
	case gostripe.InvoiceBillingReasonSubscriptionCreate:
		return billing.InvoiceNew
	case gostripe.InvoiceBillingReasonSubscriptionCycle:
		return billing.InvoiceRenewal
	case gostripe.InvoiceBillingReasonSubscriptionUpdate:
		return billing.InvoiceChange
	default:
		return ""
	}
}

func price(sp *gostripe.Price) *billing.Price {
	p := &billing.Price{
		ID:          sp.ID,
		Amount:      sp.UnitAmount,
		Currency:    string(sp.Currency),
		Active:      sp.Active,
		Nickname:    sp.Nickname,
		Type:        string(sp.Type),
		TaxBehavior: string(sp.TaxBehavior),
	}
	if sp.Product != nil {
		p.ProductID = sp.Product.ID
	}
	if sp.Recurring != nil {
		p.Interval = string(sp.Recurring.Interval)
		p.IntervalCount = sp.Recurring.IntervalCount
	}
	return p
}

func product(sp *gostripe.Product) *billing.Product {
	p := &billing.Product{ID: sp.ID, Name: sp.Name, Active: sp.Active}
	if sp.DefaultPrice != nil {
		p.DefaultPriceID = sp.DefaultPrice.ID
	}
	return p
}

// unixTime returns the zero time for 0, which Stripe sends for an unset timestamp.
func unixTime(sec int64) time.Time {
	if sec == 0 {
		return time.Time{}
	}
	return time.Unix(sec, 0).UTC()
}
