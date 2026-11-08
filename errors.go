package billing

import "errors"

var (
	ErrNotFound         = errors.New("not found")
	ErrDuplicate        = errors.New("duplicate")
	ErrInProgress       = errors.New("event is being processed by another delivery")
	ErrUnsupported      = errors.New("not supported by this provider")
	ErrPaymentPending   = errors.New("payment not complete")
	ErrAppMismatch      = errors.New("session app_id mismatch")
	ErrCustomerNotFound = errors.New("customer not found")
	ErrSubNotFound      = errors.New("subscription not found")
	ErrWebhookSignature = errors.New("webhook signature verification failed")
	ErrPromotionExists  = errors.New("promotion code already exists")
	ErrNoDefaultPrice   = errors.New("product not found or has no active default price")
)
