package billing

import (
	"context"
	"time"
)

// Store is where one product keeps its billing records. The storetest package checks an implementation.
type Store struct {
	Customers     CustomerRepository
	Subscriptions SubscriptionRepository
	Events        EventRepository
	Prices        PriceRepository
}

// CustomerRepository keeps customers under the ID the Service sets. Finds return ErrNotFound.
type CustomerRepository interface {
	FindByEmailAndApp(ctx context.Context, email, appID string) (*Customer, error)
	FindByProviderID(ctx context.Context, providerCustomerID string) (*Customer, error)
	// Upsert keeps the stored CreatedAt and sets CreatedAt and UpdatedAt on c.
	Upsert(ctx context.Context, c *Customer) error
}

// SubscriptionRepository keeps subscriptions under the ID the Service sets. Finds return ErrNotFound.
type SubscriptionRepository interface {
	FindByCustomerAndApp(ctx context.Context, customerID, appID string) (*Subscription, error)
	FindByProviderID(ctx context.Context, providerSubscriptionID string) (*Subscription, error)
	ListByApp(ctx context.Context, appID string) ([]*Subscription, error)
	// Upsert keeps the stored CreatedAt and sets CreatedAt and UpdatedAt on s.
	Upsert(ctx context.Context, s *Subscription) error
}

// EventRepository makes each webhook event take effect once, even when deliveries overlap.
type EventRepository interface {
	// Claim takes the event for processing. It returns ErrDuplicate once the event is complete and ErrInProgress while
	// another claim younger than lease is open; an older open claim is taken over, since its holder died.
	Claim(ctx context.Context, eventID string, lease time.Duration) error
	// Complete records the event as processed.
	Complete(ctx context.Context, e *Event) error
	// Release drops an open claim so the provider's retry processes the event again.
	Release(ctx context.Context, eventID string) error
}

// PriceRepository caches provider prices.
type PriceRepository interface {
	// Upsert keeps the stored CreatedAt and sets CreatedAt and UpdatedAt on p.
	Upsert(ctx context.Context, p *Price) error
	// FindByIDs returns the cached prices among ids; missing and empty ids are skipped.
	FindByIDs(ctx context.Context, ids []string) ([]*Price, error)
}
