package billing

import (
	"context"
	"net/http"
	"time"
)

// Provider is a payment provider's API as the Service uses it. A provider returns ErrUnsupported for a feature it lacks.
type Provider interface {
	// FindCustomer returns the provider's customer id for email in appID, or "" when there is none.
	FindCustomer(ctx context.Context, email, appID string) (string, error)
	CreateCustomer(ctx context.Context, email, name, appID string) (string, error)
	CreateCheckout(ctx context.Context, params CheckoutParams) (*CheckoutSession, error)
	// GetCheckout returns the session with its subscription filled in.
	GetCheckout(ctx context.Context, sessionID string) (*CheckoutState, error)
	CreatePortalSession(ctx context.Context, customerID, returnURL, configurationID string) (string, error)
	GetSubscription(ctx context.Context, subscriptionID string) (*SubscriptionState, error)
	CancelSubscription(ctx context.Context, subscriptionID string, immediate bool) (*SubscriptionState, error)
	SetSubscriptionQuantity(ctx context.Context, subscriptionID string, quantity int64) (*SubscriptionState, error)
	GetPrice(ctx context.Context, priceID string) (*Price, error)
	// GetProduct returns an error wrapping ErrNotFound for an unknown product.
	GetProduct(ctx context.Context, productID string) (*Product, error)
	// CreatePromotionCode returns ErrPromotionExists when the code is taken.
	CreatePromotionCode(ctx context.Context, in PromotionCodeInput) (*PromotionCode, error)
	// ReportUsage records value units on a meter; a repeated identifier counts once.
	ReportUsage(ctx context.Context, meter, customerID string, value int64, identifier string) error
	// ParseWebhook verifies and decodes a webhook delivery. A bad signature returns an error wrapping ErrWebhookSignature.
	ParseWebhook(payload []byte, header http.Header) (*WebhookEvent, error)
}

// CheckoutInput holds everything an app passes to start a checkout. The app owns all billing policy (trial, locale,
// promotions); the Service brokers it to the provider.
type CheckoutInput struct {
	CustomerEmail string
	CustomerName  string
	PriceID       string
	ProductID     string
	// Quantity is how many units of the price to sell; 0 means 1.
	Quantity     int64
	SetupPriceID string
	// MeteredPriceID adds a usage-based price billed from meter events; it has no quantity.
	MeteredPriceID      string
	SuccessURL          string
	CancelURL           string
	TrialDays           int
	AllowPromotionCodes bool
	Locale              string
}

// CheckoutParams is the checkout a provider creates. AppID and CustomerEmail must come back on the session and the
// subscription so webhooks can be matched to the app.
type CheckoutParams struct {
	CustomerID          string
	PriceID             string
	Quantity            int64
	SetupPriceID        string
	MeteredPriceID      string
	SuccessURL          string
	CancelURL           string
	AppID               string
	CustomerEmail       string
	TrialDays           int
	AllowPromotionCodes bool
	Locale              string
}

// CheckoutSession is a started checkout: the customer is sent to URL.
type CheckoutSession struct {
	ID  string
	URL string
}

// CheckoutState is a checkout session as the provider reports it.
type CheckoutState struct {
	ID            string
	AppID         string
	CustomerEmail string
	CustomerID    string
	// Paid is true once the payment succeeded or none was required.
	Paid           bool
	SubscriptionID string
	// Subscription is set by GetCheckout; webhook events carry only SubscriptionID.
	Subscription *SubscriptionState
}

// SubscriptionState is a subscription as the provider reports it. Zero times are unknown.
type SubscriptionState struct {
	ID                 string
	AppID              string
	Status             string
	PriceID            string
	Quantity           int64
	CancelAtPeriodEnd  bool
	CurrentPeriodStart time.Time
	CurrentPeriodEnd   time.Time
	TrialEnd           time.Time
	CanceledAt         time.Time
	// CancellationReason is the customer's feedback and comment when they cancelled.
	CancellationReason string
}

// InvoiceReason says why an invoice was raised.
type InvoiceReason string

const (
	InvoiceNew     InvoiceReason = "new"
	InvoiceRenewal InvoiceReason = "renewal"
	InvoiceChange  InvoiceReason = "change"
)

// Invoice is the part of a provider's invoice the Service acts on.
type Invoice struct {
	SubscriptionID string
	CustomerEmail  string
	Currency       string
	AmountPaid     int64
	AmountDue      int64
	Reason         InvoiceReason
}

// EventKind is what a webhook event means to the Service, whatever the provider calls it.
type EventKind string

const (
	EventCheckoutCompleted    EventKind = "checkout.completed"
	EventSubscriptionChanged  EventKind = "subscription.changed"
	EventInvoicePaid          EventKind = "invoice.paid"
	EventInvoicePaymentFailed EventKind = "invoice.payment_failed"
	EventPriceChanged         EventKind = "price.changed"
	EventPriceDeleted         EventKind = "price.deleted"
)

// WebhookEvent is a verified webhook delivery. Kind is empty for events the Service does not handle; the field
// matching Kind is set.
type WebhookEvent struct {
	ID string
	// Type is the provider's own event type.
	Type         string
	Kind         EventKind
	Data         []byte
	Checkout     *CheckoutState
	Subscription *SubscriptionState
	Invoice      *Invoice
	Price        *Price
}

// PromotionCodeInput is what an app passes to mint a discount or referral code on top of an existing coupon.
type PromotionCodeInput struct {
	Coupon               string
	Code                 string
	FirstTimeTransaction bool
	Metadata             map[string]string
}

// PromotionCode is the result of creating a promotion code.
type PromotionCode struct {
	Code          string
	PromotionID   string
	TimesRedeemed int64
}
