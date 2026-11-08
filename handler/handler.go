// Package handler provides optional net/http handlers over a billing.Service. A product mounts the ones it needs
// on its own router and supplies the signed-in customer through WithCustomer from its own auth middleware.
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	billing "github.com/nhkhai-vn/go-billing"
)

// maxWebhookBodySize is generous because provider events can exceed 64 KiB; a larger body is rejected, not truncated,
// since a truncated body would fail signature verification and be retried forever.
const maxWebhookBodySize = 256 << 10

// Service is what the handlers need from billing.Service.
type Service interface {
	CreateCheckout(ctx context.Context, in billing.CheckoutInput) (checkoutURL, sessionID string, err error)
	ConfirmCheckout(ctx context.Context, sessionID string) (*billing.Subscription, error)
	GetSubscriptionStatus(ctx context.Context, customerEmail string) (*billing.Subscription, error)
	CreatePortalSession(ctx context.Context, customerEmail, returnURL, configurationID string) (string, error)
	HandleWebhook(ctx context.Context, payload []byte, header http.Header) error
	DescribePlan(ctx context.Context, in billing.CheckoutInput) (*billing.Plan, error)
}

// Customer is the signed-in customer of the product, as its own auth sees them.
type Customer struct {
	Email string
	Name  string
	// Quantity is how many units of the plan a checkout sells this customer, such as their number of shops; 0 means 1.
	Quantity int64
}

type customerKey struct{}

func WithCustomer(ctx context.Context, c Customer) context.Context {
	return context.WithValue(ctx, customerKey{}, c)
}

func CustomerFrom(ctx context.Context) (Customer, bool) {
	c, ok := ctx.Value(customerKey{}).(Customer)
	return c, ok && c.Email != ""
}

// Webhook serves the provider's POST. Events of other apps are acknowledged with 200; a processing failure is 500 and
// an event another delivery is processing is 409, so the provider retries.
func Webhook(svc Service) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		defer r.Body.Close() //nolint:errcheck // request body close error is irrelevant

		body, err := io.ReadAll(io.LimitReader(r.Body, maxWebhookBodySize+1))
		if err != nil {
			slog.WarnContext(ctx, "webhook body read failed", "error", err)
			writeError(w, http.StatusBadRequest, "failed to read body")
			return
		}
		if len(body) > maxWebhookBodySize {
			slog.WarnContext(ctx, "webhook body exceeds limit", "limit", maxWebhookBodySize)
			writeError(w, http.StatusRequestEntityTooLarge, "payload too large")
			return
		}

		err = svc.HandleWebhook(ctx, body, r.Header)
		switch {
		case errors.Is(err, billing.ErrWebhookSignature):
			slog.WarnContext(ctx, "webhook signature verification failed", "error", err)
			writeError(w, http.StatusBadRequest, "invalid signature")
		case errors.Is(err, billing.ErrInProgress):
			slog.InfoContext(ctx, "webhook event is being processed by another delivery", "error", err)
			writeError(w, http.StatusConflict, "event in progress")
		case err != nil:
			slog.ErrorContext(ctx, "webhook processing failed", "error", err)
			writeError(w, http.StatusInternalServerError, "processing failed")
		default:
			w.WriteHeader(http.StatusOK)
		}
	})
}

// CheckoutOptions is fixed by the product, never taken from the request, so a client cannot pick its own price or redirect.
type CheckoutOptions struct {
	PriceID             string
	ProductID           string
	SetupPriceID        string
	MeteredPriceID      string
	SuccessURL          string
	CancelURL           string
	TrialDays           int
	Locale              string
	AllowPromotionCodes bool
}

type checkoutResponse struct {
	CheckoutURL string `json:"checkout_url"`
	SessionID   string `json:"session_id"`
}

// Checkout starts a checkout for the signed-in customer and returns the provider's URL to redirect to.
func Checkout(svc Service, opts CheckoutOptions) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		customer, ok := CustomerFrom(r.Context())
		if !ok {
			writeError(w, http.StatusUnauthorized, "sign in required")
			return
		}
		url, sessionID, err := svc.CreateCheckout(r.Context(), billing.CheckoutInput{
			CustomerEmail:       customer.Email,
			CustomerName:        customer.Name,
			PriceID:             opts.PriceID,
			ProductID:           opts.ProductID,
			Quantity:            customer.Quantity,
			SetupPriceID:        opts.SetupPriceID,
			MeteredPriceID:      opts.MeteredPriceID,
			SuccessURL:          opts.SuccessURL,
			CancelURL:           opts.CancelURL,
			TrialDays:           opts.TrialDays,
			AllowPromotionCodes: opts.AllowPromotionCodes,
			Locale:              opts.Locale,
		})
		if err != nil {
			slog.ErrorContext(r.Context(), "create checkout failed", "error", err)
			writeError(w, http.StatusInternalServerError, "could not start checkout")
			return
		}
		writeJSON(w, http.StatusOK, checkoutResponse{CheckoutURL: url, SessionID: sessionID})
	})
}

type planResponse struct {
	Name          string `json:"name"`
	PriceID       string `json:"price_id"`
	UnitAmount    int64  `json:"unit_amount"`
	Currency      string `json:"currency"`
	Interval      string `json:"interval"`
	IntervalCount int64  `json:"interval_count"`
	Quantity      int64  `json:"quantity"`
	TaxBehavior   string `json:"tax_behavior"`
	TrialDays     int    `json:"trial_days"`
}

// Plan describes what Checkout with the same options would sell the signed-in customer, for a confirmation step before it.
func Plan(svc Service, opts CheckoutOptions) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		customer, ok := CustomerFrom(r.Context())
		if !ok {
			writeError(w, http.StatusUnauthorized, "sign in required")
			return
		}
		plan, err := svc.DescribePlan(r.Context(), billing.CheckoutInput{
			CustomerEmail: customer.Email,
			CustomerName:  customer.Name,
			PriceID:       opts.PriceID,
			ProductID:     opts.ProductID,
			Quantity:      customer.Quantity,
			TrialDays:     opts.TrialDays,
		})
		switch {
		case errors.Is(err, billing.ErrNoDefaultPrice):
			slog.ErrorContext(r.Context(), "plan has no price", "error", err)
			writeError(w, http.StatusNotFound, "no plan on sale")
		case err != nil:
			slog.ErrorContext(r.Context(), "describe plan failed", "error", err)
			writeError(w, http.StatusInternalServerError, "could not read the plan")
		default:
			writeJSON(w, http.StatusOK, planResponse{
				Name:          plan.Name,
				PriceID:       plan.PriceID,
				UnitAmount:    plan.Amount,
				Currency:      plan.Currency,
				Interval:      plan.Interval,
				IntervalCount: plan.IntervalCount,
				Quantity:      plan.Quantity,
				TaxBehavior:   plan.TaxBehavior,
				TrialDays:     plan.TrialDays,
			})
		}
	})
}

type statusResponse struct {
	HasSubscription   bool       `json:"has_subscription"`
	Active            bool       `json:"active"`
	Status            string     `json:"status"`
	CurrentPeriodEnd  *time.Time `json:"current_period_end,omitempty"`
	TrialEnd          *time.Time `json:"trial_end,omitempty"`
	CancelAtPeriodEnd bool       `json:"cancel_at_period_end"`
}

func toStatus(sub *billing.Subscription) statusResponse {
	if sub == nil {
		return statusResponse{Status: billing.StatusNone}
	}
	return statusResponse{
		HasSubscription:   true,
		Active:            sub.IsActive(),
		Status:            sub.Status,
		CurrentPeriodEnd:  sub.CurrentPeriodEnd,
		TrialEnd:          sub.TrialEnd,
		CancelAtPeriodEnd: sub.CancelAtPeriodEnd,
	}
}

// Status reports the signed-in customer's subscription.
func Status(svc Service) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		customer, ok := CustomerFrom(r.Context())
		if !ok {
			writeError(w, http.StatusUnauthorized, "sign in required")
			return
		}
		sub, err := svc.GetSubscriptionStatus(r.Context(), customer.Email)
		if err != nil {
			slog.ErrorContext(r.Context(), "get subscription status failed", "error", err)
			writeError(w, http.StatusInternalServerError, "could not read subscription")
			return
		}
		writeJSON(w, http.StatusOK, toStatus(sub))
	})
}

// Confirm settles a checkout when the customer returns from the provider (query parameter session_id), without waiting
// for the webhook.
func Confirm(svc Service) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sessionID := r.URL.Query().Get("session_id")
		if sessionID == "" {
			writeError(w, http.StatusBadRequest, "session_id is required")
			return
		}
		sub, err := svc.ConfirmCheckout(r.Context(), sessionID)
		switch {
		case errors.Is(err, billing.ErrPaymentPending):
			writeError(w, http.StatusConflict, "payment not complete")
		case errors.Is(err, billing.ErrAppMismatch):
			writeError(w, http.StatusNotFound, "unknown session")
		case err != nil:
			slog.ErrorContext(r.Context(), "confirm checkout failed", "error", err)
			writeError(w, http.StatusInternalServerError, "could not confirm checkout")
		default:
			writeJSON(w, http.StatusOK, toStatus(sub))
		}
	})
}

type portalResponse struct {
	PortalURL string `json:"portal_url"`
}

// Portal returns the provider's customer portal URL for the signed-in customer.
func Portal(svc Service, returnURL, configurationID string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		customer, ok := CustomerFrom(r.Context())
		if !ok {
			writeError(w, http.StatusUnauthorized, "sign in required")
			return
		}
		url, err := svc.CreatePortalSession(r.Context(), customer.Email, returnURL, configurationID)
		switch {
		case errors.Is(err, billing.ErrCustomerNotFound):
			writeError(w, http.StatusNotFound, "no billing account yet")
		case err != nil:
			slog.ErrorContext(r.Context(), "create portal session failed", "error", err)
			writeError(w, http.StatusInternalServerError, "could not open the portal")
		default:
			writeJSON(w, http.StatusOK, portalResponse{PortalURL: url})
		}
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("failed to encode response", "error", err)
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
