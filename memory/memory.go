// Package memory is a billing.Store held in process memory, for tests and examples. Records are lost on exit.
package memory

import (
	"context"
	"sync"
	"time"

	billing "github.com/nhkhai-vn/go-billing"
)

// NewStore returns an empty in-memory store.
func NewStore() billing.Store {
	return billing.Store{
		Customers:     &customerRepo{byID: map[string]billing.Customer{}},
		Subscriptions: &subscriptionRepo{byID: map[string]billing.Subscription{}},
		Events:        &eventRepo{byID: map[string]event{}},
		Prices:        &priceRepo{byID: map[string]billing.Price{}},
	}
}

func now() time.Time { return time.Now().UTC() }

type customerRepo struct {
	mu   sync.Mutex
	byID map[string]billing.Customer
}

func (r *customerRepo) FindByEmailAndApp(_ context.Context, email, appID string) (*billing.Customer, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.byID {
		if c.Email == email && c.AppID == appID {
			return &c, nil
		}
	}
	return nil, billing.ErrNotFound
}

func (r *customerRepo) FindByProviderID(_ context.Context, providerCustomerID string) (*billing.Customer, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.byID {
		if c.ProviderCustomerID == providerCustomerID {
			return &c, nil
		}
	}
	return nil, billing.ErrNotFound
}

func (r *customerRepo) Upsert(_ context.Context, c *billing.Customer) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	t := now()
	c.CreatedAt, c.UpdatedAt = t, t
	if old, ok := r.byID[c.ID]; ok {
		c.CreatedAt = old.CreatedAt
	}
	r.byID[c.ID] = *c
	return nil
}

type subscriptionRepo struct {
	mu   sync.Mutex
	byID map[string]billing.Subscription
}

func (r *subscriptionRepo) FindByCustomerAndApp(_ context.Context, customerID, appID string) (*billing.Subscription, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range r.byID {
		if s.CustomerID == customerID && s.AppID == appID {
			return &s, nil
		}
	}
	return nil, billing.ErrNotFound
}

func (r *subscriptionRepo) FindByProviderID(_ context.Context, providerSubscriptionID string) (*billing.Subscription, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range r.byID {
		if s.ProviderSubscriptionID == providerSubscriptionID {
			return &s, nil
		}
	}
	return nil, billing.ErrNotFound
}

func (r *subscriptionRepo) ListByApp(_ context.Context, appID string) ([]*billing.Subscription, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	subs := []*billing.Subscription{}
	for _, s := range r.byID {
		if s.AppID == appID {
			subs = append(subs, &s)
		}
	}
	return subs, nil
}

func (r *subscriptionRepo) Upsert(_ context.Context, s *billing.Subscription) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	t := now()
	s.CreatedAt, s.UpdatedAt = t, t
	if old, ok := r.byID[s.ID]; ok {
		s.CreatedAt = old.CreatedAt
	}
	r.byID[s.ID] = *s
	return nil
}

type event struct {
	claimedAt time.Time
	done      *billing.Event
}

type eventRepo struct {
	mu   sync.Mutex
	byID map[string]event
}

func (r *eventRepo) Claim(_ context.Context, eventID string, lease time.Duration) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.byID[eventID]
	switch {
	case ok && e.done != nil:
		return billing.ErrDuplicate
	case ok && now().Before(e.claimedAt.Add(lease)):
		return billing.ErrInProgress
	}
	r.byID[eventID] = event{claimedAt: now()}
	return nil
}

func (r *eventRepo) Complete(_ context.Context, e *billing.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	done := *e
	r.byID[e.ID] = event{claimedAt: r.byID[e.ID].claimedAt, done: &done}
	return nil
}

func (r *eventRepo) Release(_ context.Context, eventID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e, ok := r.byID[eventID]; ok && e.done == nil {
		delete(r.byID, eventID)
	}
	return nil
}

type priceRepo struct {
	mu   sync.Mutex
	byID map[string]billing.Price
}

func (r *priceRepo) Upsert(_ context.Context, p *billing.Price) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	t := now()
	p.CreatedAt, p.UpdatedAt = t, t
	if old, ok := r.byID[p.ID]; ok {
		p.CreatedAt = old.CreatedAt
	}
	r.byID[p.ID] = *p
	return nil
}

func (r *priceRepo) FindByIDs(_ context.Context, ids []string) ([]*billing.Price, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	prices := make([]*billing.Price, 0, len(ids))
	for _, id := range ids {
		if p, ok := r.byID[id]; ok {
			prices = append(prices, &p)
		}
	}
	return prices, nil
}
