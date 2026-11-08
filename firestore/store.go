// Package firestore is a billing.Store on Cloud Firestore (native mode).
package firestore

import (
	"context"
	"fmt"
	"time"

	gcfs "cloud.google.com/go/firestore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	billing "github.com/nhkhai-vn/go-billing"
)

// Collections are prefixed so billing data sits beside a product's own collections in one database.
const (
	customersCollection     = "billing_customers"
	subscriptionsCollection = "billing_subscriptions"
	eventsCollection        = "billing_events"
	pricesCollection        = "billing_prices"
)

// claimAttempts covers overlapping deliveries of one event: contenders for the document lock are aborted and retried,
// and the default of 5 attempts can run out when several arrive at once.
const claimAttempts = 10

// NewStore returns the billing repositories on the product's Firestore database.
func NewStore(client *gcfs.Client) billing.Store {
	return billing.Store{
		Customers:     &customerRepo{client: client},
		Subscriptions: &subscriptionRepo{client: client},
		Events:        &eventRepo{client: client},
		Prices:        &priceRepo{client: client},
	}
}

func notFound(err error) bool { return status.Code(err) == codes.NotFound }

// now is truncated to microseconds, Firestore's precision, so returned times equal the stored ones.
func now() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }

// upsert writes build's result under id in one transaction. build receives the stored created_at, or now for a new
// document.
func upsert[D any](ctx context.Context, client *gcfs.Client, collection, id string, createdAt func(existing *D) time.Time, build func(createdAt, now time.Time) *D) error {
	ref := client.Collection(collection).Doc(id)
	return client.RunTransaction(ctx, func(_ context.Context, tx *gcfs.Transaction) error {
		t := now()
		created := t
		snap, err := tx.Get(ref)
		switch {
		case err == nil:
			var existing D
			if err := snap.DataTo(&existing); err != nil {
				return fmt.Errorf("decode existing: %w", err)
			}
			if c := createdAt(&existing); !c.IsZero() {
				created = c
			}
		case !notFound(err):
			return err
		}
		return tx.Set(ref, build(created, t))
	})
}

// first returns the first document query matches, decoded with toModel.
func first[D any, M any](ctx context.Context, q gcfs.Query, toModel func(D, string) *M) (*M, error) {
	snaps, err := q.Limit(1).Documents(ctx).GetAll()
	if err != nil {
		return nil, err
	}
	if len(snaps) == 0 {
		return nil, billing.ErrNotFound
	}
	var d D
	if err := snaps[0].DataTo(&d); err != nil {
		return nil, fmt.Errorf("decode %s: %w", snaps[0].Ref.ID, err)
	}
	return toModel(d, snaps[0].Ref.ID), nil
}

type customerDoc struct {
	Email              string    `firestore:"email"`
	AppID              string    `firestore:"app_id"`
	Name               string    `firestore:"name"`
	ProviderCustomerID string    `firestore:"provider_customer_id"`
	CreatedAt          time.Time `firestore:"created_at"`
	UpdatedAt          time.Time `firestore:"updated_at"`
}

func (d customerDoc) toModel(id string) *billing.Customer {
	return &billing.Customer{
		ID: id, Email: d.Email, AppID: d.AppID, Name: d.Name, ProviderCustomerID: d.ProviderCustomerID,
		CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt,
	}
}

type customerRepo struct{ client *gcfs.Client }

func (r *customerRepo) FindByEmailAndApp(ctx context.Context, email, appID string) (*billing.Customer, error) {
	q := r.client.Collection(customersCollection).Where("email", "==", email).Where("app_id", "==", appID)
	c, err := first(ctx, q, customerDoc.toModel)
	if err != nil {
		return nil, fmt.Errorf("find customer by email and app: %w", err)
	}
	return c, nil
}

func (r *customerRepo) FindByProviderID(ctx context.Context, providerCustomerID string) (*billing.Customer, error) {
	q := r.client.Collection(customersCollection).Where("provider_customer_id", "==", providerCustomerID)
	c, err := first(ctx, q, customerDoc.toModel)
	if err != nil {
		return nil, fmt.Errorf("find customer by provider id: %w", err)
	}
	return c, nil
}

func (r *customerRepo) Upsert(ctx context.Context, c *billing.Customer) error {
	var created, updated time.Time
	err := upsert(ctx, r.client, customersCollection, c.ID,
		func(e *customerDoc) time.Time { return e.CreatedAt },
		func(createdAt, now time.Time) *customerDoc {
			created, updated = createdAt, now
			return &customerDoc{
				Email: c.Email, AppID: c.AppID, Name: c.Name, ProviderCustomerID: c.ProviderCustomerID,
				CreatedAt: createdAt, UpdatedAt: now,
			}
		})
	if err != nil {
		return fmt.Errorf("upsert customer: %w", err)
	}
	c.CreatedAt, c.UpdatedAt = created, updated
	return nil
}

type subscriptionDoc struct {
	CustomerID             string     `firestore:"customer_id"`
	AppID                  string     `firestore:"app_id"`
	ProviderSubscriptionID string     `firestore:"provider_subscription_id"`
	PriceID                string     `firestore:"price_id"`
	Quantity               int64      `firestore:"quantity"`
	Status                 string     `firestore:"status"`
	CancelAtPeriodEnd      bool       `firestore:"cancel_at_period_end"`
	CurrentPeriodStart     *time.Time `firestore:"current_period_start"`
	CurrentPeriodEnd       *time.Time `firestore:"current_period_end"`
	TrialEnd               *time.Time `firestore:"trial_end"`
	CanceledAt             *time.Time `firestore:"canceled_at"`
	CancellationReason     string     `firestore:"cancellation_reason"`
	CreatedAt              time.Time  `firestore:"created_at"`
	UpdatedAt              time.Time  `firestore:"updated_at"`
}

func (d subscriptionDoc) toModel(id string) *billing.Subscription {
	return &billing.Subscription{
		ID: id, CustomerID: d.CustomerID, AppID: d.AppID, ProviderSubscriptionID: d.ProviderSubscriptionID,
		PriceID: d.PriceID, Quantity: d.Quantity, Status: d.Status, CancelAtPeriodEnd: d.CancelAtPeriodEnd,
		CurrentPeriodStart: d.CurrentPeriodStart, CurrentPeriodEnd: d.CurrentPeriodEnd, TrialEnd: d.TrialEnd, CanceledAt: d.CanceledAt,
		CancellationReason: d.CancellationReason, CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt,
	}
}

type subscriptionRepo struct{ client *gcfs.Client }

func (r *subscriptionRepo) FindByCustomerAndApp(ctx context.Context, customerID, appID string) (*billing.Subscription, error) {
	q := r.client.Collection(subscriptionsCollection).Where("customer_id", "==", customerID).Where("app_id", "==", appID)
	s, err := first(ctx, q, subscriptionDoc.toModel)
	if err != nil {
		return nil, fmt.Errorf("find subscription by customer and app: %w", err)
	}
	return s, nil
}

func (r *subscriptionRepo) FindByProviderID(ctx context.Context, providerSubscriptionID string) (*billing.Subscription, error) {
	q := r.client.Collection(subscriptionsCollection).Where("provider_subscription_id", "==", providerSubscriptionID)
	s, err := first(ctx, q, subscriptionDoc.toModel)
	if err != nil {
		return nil, fmt.Errorf("find subscription by provider id: %w", err)
	}
	return s, nil
}

func (r *subscriptionRepo) ListByApp(ctx context.Context, appID string) ([]*billing.Subscription, error) {
	snaps, err := r.client.Collection(subscriptionsCollection).Where("app_id", "==", appID).Documents(ctx).GetAll()
	if err != nil {
		return nil, fmt.Errorf("list subscriptions by app: %w", err)
	}
	subs := make([]*billing.Subscription, 0, len(snaps))
	for _, snap := range snaps {
		var d subscriptionDoc
		if err := snap.DataTo(&d); err != nil {
			return nil, fmt.Errorf("decode subscription %s: %w", snap.Ref.ID, err)
		}
		subs = append(subs, d.toModel(snap.Ref.ID))
	}
	return subs, nil
}

func (r *subscriptionRepo) Upsert(ctx context.Context, s *billing.Subscription) error {
	var created, updated time.Time
	err := upsert(ctx, r.client, subscriptionsCollection, s.ID,
		func(e *subscriptionDoc) time.Time { return e.CreatedAt },
		func(createdAt, now time.Time) *subscriptionDoc {
			created, updated = createdAt, now
			return &subscriptionDoc{
				CustomerID: s.CustomerID, AppID: s.AppID, ProviderSubscriptionID: s.ProviderSubscriptionID,
				PriceID: s.PriceID, Quantity: s.Quantity, Status: s.Status, CancelAtPeriodEnd: s.CancelAtPeriodEnd,
				CurrentPeriodStart: s.CurrentPeriodStart, CurrentPeriodEnd: s.CurrentPeriodEnd, TrialEnd: s.TrialEnd, CanceledAt: s.CanceledAt,
				CancellationReason: s.CancellationReason, CreatedAt: createdAt, UpdatedAt: now,
			}
		})
	if err != nil {
		return fmt.Errorf("upsert subscription: %w", err)
	}
	s.CreatedAt, s.UpdatedAt = created, updated
	return nil
}

// eventDoc is an open claim until ProcessedAt is set.
type eventDoc struct {
	ClaimedAt   time.Time  `firestore:"claimed_at"`
	Type        string     `firestore:"type"`
	Data        string     `firestore:"data"`
	ProcessedAt *time.Time `firestore:"processed_at"`
}

type eventRepo struct{ client *gcfs.Client }

func (r *eventRepo) Claim(ctx context.Context, eventID string, lease time.Duration) error {
	ref := r.client.Collection(eventsCollection).Doc(eventID)
	err := r.client.RunTransaction(ctx, func(_ context.Context, tx *gcfs.Transaction) error {
		t := now()
		snap, err := tx.Get(ref)
		if err != nil && !notFound(err) {
			return err
		}
		if err == nil {
			var d eventDoc
			if err := snap.DataTo(&d); err != nil {
				return fmt.Errorf("decode event: %w", err)
			}
			if d.ProcessedAt != nil {
				return billing.ErrDuplicate
			}
			if t.Before(d.ClaimedAt.Add(lease)) {
				return billing.ErrInProgress
			}
		}
		return tx.Set(ref, &eventDoc{ClaimedAt: t})
	}, gcfs.MaxAttempts(claimAttempts))
	if err != nil {
		return fmt.Errorf("claim event: %w", err)
	}
	return nil
}

func (r *eventRepo) Complete(ctx context.Context, e *billing.Event) error {
	done := map[string]any{"type": e.Type, "data": e.Data, "processed_at": e.ProcessedAt}
	if _, err := r.client.Collection(eventsCollection).Doc(e.ID).Set(ctx, done, gcfs.MergeAll); err != nil {
		return fmt.Errorf("complete event: %w", err)
	}
	return nil
}

func (r *eventRepo) Release(ctx context.Context, eventID string) error {
	ref := r.client.Collection(eventsCollection).Doc(eventID)
	err := r.client.RunTransaction(ctx, func(_ context.Context, tx *gcfs.Transaction) error {
		snap, err := tx.Get(ref)
		if notFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		var d eventDoc
		if err := snap.DataTo(&d); err != nil {
			return fmt.Errorf("decode event: %w", err)
		}
		if d.ProcessedAt != nil {
			return nil
		}
		return tx.Delete(ref)
	})
	if err != nil {
		return fmt.Errorf("release event: %w", err)
	}
	return nil
}

type priceDoc struct {
	ProductID     string    `firestore:"product_id"`
	Amount        int64     `firestore:"amount"`
	Currency      string    `firestore:"currency"`
	Interval      string    `firestore:"interval"`
	IntervalCount int64     `firestore:"interval_count"`
	Active        bool      `firestore:"active"`
	Nickname      string    `firestore:"nickname"`
	Type          string    `firestore:"type"`
	TaxBehavior   string    `firestore:"tax_behavior"`
	CreatedAt     time.Time `firestore:"created_at"`
	UpdatedAt     time.Time `firestore:"updated_at"`
}

func (d priceDoc) toModel(id string) *billing.Price {
	return &billing.Price{
		ID: id, ProductID: d.ProductID, Amount: d.Amount, Currency: d.Currency, Interval: d.Interval,
		IntervalCount: d.IntervalCount, Active: d.Active, Nickname: d.Nickname, Type: d.Type, TaxBehavior: d.TaxBehavior,
		CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt,
	}
}

type priceRepo struct{ client *gcfs.Client }

func (r *priceRepo) Upsert(ctx context.Context, p *billing.Price) error {
	var created, updated time.Time
	err := upsert(ctx, r.client, pricesCollection, p.ID,
		func(e *priceDoc) time.Time { return e.CreatedAt },
		func(createdAt, now time.Time) *priceDoc {
			created, updated = createdAt, now
			return &priceDoc{
				ProductID: p.ProductID, Amount: p.Amount, Currency: p.Currency, Interval: p.Interval,
				IntervalCount: p.IntervalCount, Active: p.Active, Nickname: p.Nickname, Type: p.Type, TaxBehavior: p.TaxBehavior,
				CreatedAt: createdAt, UpdatedAt: now,
			}
		})
	if err != nil {
		return fmt.Errorf("upsert price: %w", err)
	}
	p.CreatedAt, p.UpdatedAt = created, updated
	return nil
}

func (r *priceRepo) FindByIDs(ctx context.Context, ids []string) ([]*billing.Price, error) {
	refs := make([]*gcfs.DocumentRef, 0, len(ids))
	for _, id := range ids {
		if id != "" {
			refs = append(refs, r.client.Collection(pricesCollection).Doc(id))
		}
	}
	snaps, err := r.client.GetAll(ctx, refs)
	if err != nil {
		return nil, fmt.Errorf("find prices: %w", err)
	}
	prices := make([]*billing.Price, 0, len(snaps))
	for _, snap := range snaps {
		if !snap.Exists() {
			continue
		}
		var d priceDoc
		if err := snap.DataTo(&d); err != nil {
			return nil, fmt.Errorf("decode price %s: %w", snap.Ref.ID, err)
		}
		prices = append(prices, d.toModel(snap.Ref.ID))
	}
	return prices, nil
}
