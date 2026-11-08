package billing

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

// claimLease is how long one delivery holds an event before another may take it over; it outlasts any processing.
const claimLease = 5 * time.Minute

// errOtherApp marks an event of another app on the same provider account. It is acknowledged and not recorded.
var errOtherApp = errors.New("event belongs to another app")

// HandleWebhook verifies and processes a webhook delivery. Each event takes effect once; an error asks the provider to
// retry, and ErrInProgress means another delivery of the event is being processed.
func (s *Service) HandleWebhook(ctx context.Context, payload []byte, header http.Header) error {
	ev, err := s.provider.ParseWebhook(payload, header)
	if err != nil {
		return fmt.Errorf("parse webhook: %w", err)
	}

	slog.InfoContext(ctx, "processing webhook event", "event_id", ev.ID, "event_type", ev.Type)

	err = s.events.Claim(ctx, ev.ID, claimLease)
	if errors.Is(err, ErrDuplicate) {
		slog.InfoContext(ctx, "duplicate webhook event, skipping", "event_id", ev.ID)
		return nil
	}
	if err != nil {
		return fmt.Errorf("claim event: %w", err)
	}

	if err := s.dispatch(ctx, ev); err != nil {
		s.release(ctx, ev.ID)
		if errors.Is(err, errOtherApp) {
			slog.InfoContext(ctx, "webhook event is for another app, ignoring", "event_id", ev.ID, "event_type", ev.Type)
			return nil
		}
		return err
	}

	if err := s.events.Complete(ctx, &Event{ID: ev.ID, Type: ev.Type, Data: string(ev.Data), ProcessedAt: time.Now()}); err != nil {
		slog.ErrorContext(ctx, "failed to record webhook event for idempotency", "event_id", ev.ID, "error", err)
	}
	return nil
}

func (s *Service) release(ctx context.Context, eventID string) {
	if err := s.events.Release(ctx, eventID); err != nil {
		slog.WarnContext(ctx, "failed to release webhook event claim", "event_id", eventID, "error", err)
	}
}

func (s *Service) dispatch(ctx context.Context, ev *WebhookEvent) error {
	var err error
	switch ev.Kind {
	case EventCheckoutCompleted:
		err = s.handleCheckoutCompleted(ctx, ev.ID, ev.Checkout)
	case EventSubscriptionChanged:
		err = s.handleSubscriptionChange(ctx, ev.Subscription)
	case EventInvoicePaid:
		err = s.handleInvoicePaid(ctx, ev.Invoice)
	case EventInvoicePaymentFailed:
		err = s.handleInvoicePaymentFailed(ctx, ev.ID, ev.Invoice)
	case EventPriceChanged:
		err = s.prices.Upsert(ctx, ev.Price)
	case EventPriceDeleted:
		ev.Price.Active = false
		err = s.prices.Upsert(ctx, ev.Price)
	default:
		slog.InfoContext(ctx, "unhandled webhook event type", "type", ev.Type)
	}
	if err != nil {
		return fmt.Errorf("handle %s: %w", ev.Type, err)
	}
	return nil
}

func (s *Service) handleCheckoutCompleted(ctx context.Context, eventID string, sess *CheckoutState) error {
	if sess.SubscriptionID == "" {
		return nil
	}
	if sess.AppID == "" || sess.CustomerEmail == "" {
		slog.WarnContext(ctx, "checkout completed without app_id or customer email", "event_id", eventID)
		return nil
	}
	if sess.AppID != s.appID {
		return errOtherApp
	}

	cust, err := s.customers.FindByProviderID(ctx, sess.CustomerID)
	if errors.Is(err, ErrNotFound) {
		cust = &Customer{ID: customerID(s.appID, sess.CustomerEmail), Email: sess.CustomerEmail, AppID: s.appID, ProviderCustomerID: sess.CustomerID}
		if err := s.customers.Upsert(ctx, cust); err != nil {
			return fmt.Errorf("upsert customer from webhook: %w", err)
		}
	} else if err != nil {
		return err
	}

	state, err := s.provider.GetSubscription(ctx, sess.SubscriptionID)
	if err != nil {
		return fmt.Errorf("get subscription: %w", err)
	}
	sub, err := s.upsertSubscription(ctx, cust.ID, state)
	if err != nil {
		return err
	}
	slog.InfoContext(ctx, "checkout completed", "event_id", eventID, "app_id", s.appID, "email", sess.CustomerEmail, "subscription_id", sub.ProviderSubscriptionID)

	s.notifyCheckout(ctx, sess.CustomerEmail, state)
	return nil
}

func (s *Service) handleSubscriptionChange(ctx context.Context, state *SubscriptionState) error {
	if state.AppID != s.appID {
		return errOtherApp
	}
	prior, err := s.subs.FindByProviderID(ctx, state.ID)
	if errors.Is(err, ErrNotFound) {
		slog.WarnContext(ctx, "webhook for unknown subscription", "sub_id", state.ID)
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := s.upsertSubscription(ctx, prior.CustomerID, state); err != nil {
		return err
	}
	s.notifyCancellation(ctx, prior, state)
	return nil
}

func (s *Service) handleInvoicePaid(ctx context.Context, inv *Invoice) error {
	if inv.SubscriptionID == "" {
		return nil
	}
	state, err := s.provider.GetSubscription(ctx, inv.SubscriptionID)
	if err != nil {
		return fmt.Errorf("get subscription: %w", err)
	}
	if state.AppID != s.appID {
		return errOtherApp
	}

	if err := s.updateSubscription(ctx, state); err != nil {
		return err
	}

	// A zero invoice, such as the one opening a trial, is not a payment.
	if inv.AmountPaid > 0 {
		s.notifyInvoice(ctx, "💰 Payment succeeded"+invoiceReasonSuffix(inv.Reason), inv, s.productLabel(ctx, state.PriceID), false)
	}
	return nil
}

// updateSubscription stores the state of a subscription the store knows; checkout creates the record, so an unknown
// one is skipped.
func (s *Service) updateSubscription(ctx context.Context, state *SubscriptionState) error {
	sub, err := s.subs.FindByProviderID(ctx, state.ID)
	if errors.Is(err, ErrNotFound) {
		slog.WarnContext(ctx, "webhook for unknown subscription", "sub_id", state.ID)
		return nil
	}
	if err != nil {
		return err
	}
	_, err = s.upsertSubscription(ctx, sub.CustomerID, state)
	return err
}

func (s *Service) handleInvoicePaymentFailed(ctx context.Context, eventID string, inv *Invoice) error {
	if inv.SubscriptionID == "" {
		return nil
	}
	sub, err := s.subs.FindByProviderID(ctx, inv.SubscriptionID)
	if errors.Is(err, ErrNotFound) {
		slog.WarnContext(ctx, "payment failed for unknown subscription", "sub_id", inv.SubscriptionID)
		return nil
	}
	if err != nil {
		return err
	}

	sub.Status = StatusPastDue
	if err := s.subs.Upsert(ctx, sub); err != nil {
		return err
	}
	slog.WarnContext(ctx, "subscription marked past_due due to payment failure", "sub_id", inv.SubscriptionID, "event_id", eventID)

	s.notifyInvoice(ctx, "⚠️ Payment failed", inv, s.productLabel(ctx, sub.PriceID), true)
	return nil
}
