package billing

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
)

func (s *Service) notify(ctx context.Context, n Notification) {
	if s.notifier == nil {
		return
	}
	if err := s.notifier.Notify(ctx, n); err != nil {
		slog.WarnContext(ctx, "failed to send notification", "title", n.Title, "error", err)
	}
}

func (s *Service) notifyCheckout(ctx context.Context, email string, st *SubscriptionState) {
	title := "✅ Subscription started"
	if st.Status == StatusTrialing {
		title = "🆕 Trial started"
	}
	text := fmt.Sprintf("App: %s\nCustomer: %s\nStatus: %s", s.appID, email, st.Status)
	if !st.TrialEnd.IsZero() {
		text += "\nTrial ends: " + st.TrialEnd.UTC().Format("2006-01-02")
	}
	s.notify(ctx, Notification{Title: title, Text: text})
}

// notifyCancellation alerts when a subscription is scheduled to cancel or actually ends.
func (s *Service) notifyCancellation(ctx context.Context, prior *Subscription, st *SubscriptionState) {
	email := strings.TrimPrefix(prior.CustomerID, prior.AppID+"|")

	if !prior.CancelAtPeriodEnd && st.CancelAtPeriodEnd {
		text := fmt.Sprintf("App: %s\nCustomer: %s", prior.AppID, email)
		if !st.CurrentPeriodEnd.IsZero() {
			text += "\nEnds: " + st.CurrentPeriodEnd.UTC().Format("2006-01-02")
		}
		if st.CancellationReason != "" {
			text += "\nReason: " + st.CancellationReason
		}
		s.notify(ctx, Notification{Title: "⚠️ Cancellation scheduled", Text: text, Warn: true})
	}

	if prior.Status != StatusCanceled && st.Status == StatusCanceled {
		text := fmt.Sprintf("App: %s\nCustomer: %s", prior.AppID, email)
		s.notify(ctx, Notification{Title: "🚫 Subscription canceled", Text: text, Warn: true})
	}
}

func invoiceReasonSuffix(r InvoiceReason) string {
	switch r {
	case InvoiceNew:
		return " (new subscription)"
	case InvoiceRenewal:
		return " (renewal)"
	case InvoiceChange:
		return " (plan change)"
	default:
		return ""
	}
}

func (s *Service) notifyInvoice(ctx context.Context, title string, inv *Invoice, product string, warn bool) {
	amount := inv.AmountPaid
	if amount == 0 {
		amount = inv.AmountDue
	}
	text := fmt.Sprintf("App: %s\nCustomer: %s\nProduct: %s\nAmount: %s", s.appID, inv.CustomerEmail, product, formatAmount(amount, inv.Currency))
	s.notify(ctx, Notification{Title: title, Text: text, Warn: warn})
}

// productLabel names a price by its cached nickname, falling back to its id. It never fails, so a notification still goes out.
func (s *Service) productLabel(ctx context.Context, priceID string) string {
	if priceID == "" {
		return ""
	}
	prices, err := s.prices.FindByIDs(ctx, []string{priceID})
	if err != nil {
		return priceID
	}
	for _, p := range prices {
		if p.ID == priceID && p.Nickname != "" {
			return p.Nickname
		}
	}
	return priceID
}

// zeroDecimalCurrencies have no minor unit, so the amount is already in whole currency.
var zeroDecimalCurrencies = map[string]bool{"jpy": true, "krw": true, "vnd": true, "clp": true}

func formatAmount(amount int64, currency string) string {
	cur := strings.ToUpper(currency)
	if zeroDecimalCurrencies[strings.ToLower(currency)] {
		return fmt.Sprintf("%s %d", cur, amount)
	}
	return fmt.Sprintf("%s %.2f", cur, float64(amount)/100)
}
