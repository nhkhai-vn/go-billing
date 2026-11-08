// Package billing is subscription billing for one app: checkout, subscriptions, usage, the customer portal,
// promotion codes and webhooks, over a payment Provider and a Store in the app's own database.
package billing

import (
	"context"
	"errors"
)

// Config holds everything an app passes to New. Notifier is optional.
type Config struct {
	AppID    string
	Store    Store
	Provider Provider
	Notifier Notifier
}

// Notification is a team-facing message about a billing lifecycle event.
type Notification struct {
	Title string
	Text  string
	Warn  bool
}

// Notifier delivers notifications (e.g. to Slack). Delivery is best effort and never fails webhook processing.
type Notifier interface {
	Notify(ctx context.Context, n Notification) error
}

// Service is the billing logic for one app_id. Two apps in one process are two Services.
type Service struct {
	appID     string
	provider  Provider
	customers CustomerRepository
	subs      SubscriptionRepository
	events    EventRepository
	prices    PriceRepository
	notifier  Notifier
}

// New returns a Service for cfg.AppID. It fails when a required dependency is missing.
func New(cfg Config) (*Service, error) {
	switch {
	case cfg.AppID == "":
		return nil, errors.New("billing: AppID is required")
	case cfg.Provider == nil:
		return nil, errors.New("billing: Provider is required")
	case cfg.Store.Customers == nil, cfg.Store.Subscriptions == nil, cfg.Store.Events == nil, cfg.Store.Prices == nil:
		return nil, errors.New("billing: every Store repository is required")
	}
	return &Service{
		appID:     cfg.AppID,
		provider:  cfg.Provider,
		customers: cfg.Store.Customers,
		subs:      cfg.Store.Subscriptions,
		events:    cfg.Store.Events,
		prices:    cfg.Store.Prices,
		notifier:  cfg.Notifier,
	}, nil
}
