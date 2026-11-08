package billing

import "time"

// Subscription is a customer's subscription to one app, as last reported by the provider.
type Subscription struct {
	ID                     string `json:"id"`
	CustomerID             string `json:"customer_id"`
	AppID                  string `json:"app_id"`
	ProviderSubscriptionID string `json:"provider_subscription_id,omitempty"`
	PriceID                string `json:"price_id,omitempty"`
	// Quantity is how many units of the price the subscription bills.
	Quantity           int64      `json:"quantity,omitempty"`
	Status             string     `json:"status"`
	CancelAtPeriodEnd  bool       `json:"cancel_at_period_end"`
	CurrentPeriodStart *time.Time `json:"current_period_start,omitempty"`
	CurrentPeriodEnd   *time.Time `json:"current_period_end,omitempty"`
	TrialEnd           *time.Time `json:"trial_end,omitempty"`
	CanceledAt         *time.Time `json:"canceled_at,omitempty"`
	CancellationReason string     `json:"cancellation_reason,omitempty"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
}

// Subscription status constants.
const (
	StatusActive            = "active"
	StatusTrialing          = "trialing"
	StatusPastDue           = "past_due"
	StatusCanceled          = "canceled"
	StatusIncomplete        = "incomplete"
	StatusIncompleteExpired = "incomplete_expired"
	StatusNone              = "none"
)

func (s *Subscription) IsActive() bool {
	switch s.Status {
	case StatusActive, StatusTrialing, StatusPastDue:
		return true
	default:
		return false
	}
}
