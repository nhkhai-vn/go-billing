// Package postgres is a billing.Store on PostgreSQL through pgx.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	billing "github.com/nhkhai-vn/go-billing"
)

// NewStore returns a store on pool. Run Migrate first to create the tables.
func NewStore(pool *pgxpool.Pool) billing.Store {
	return billing.Store{
		Customers:     customerRepo{pool},
		Subscriptions: subscriptionRepo{pool},
		Events:        eventRepo{pool},
		Prices:        priceRepo{pool},
	}
}

// now is truncated to Postgres precision so the times Upsert writes back equal the stored ones.
func now() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }

func findOne[T any](ctx context.Context, pool *pgxpool.Pool, sql string, args ...any) (*T, error) {
	rows, err := pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	v, err := pgx.CollectOneRow(rows, pgx.RowToAddrOfStructByNameLax[T])
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, billing.ErrNotFound
	}
	return v, err
}

func findAll[T any](ctx context.Context, pool *pgxpool.Pool, sql string, args ...any) ([]*T, error) {
	rows, err := pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToAddrOfStructByNameLax[T])
}

const (
	customerColumns = `id, email, app_id, name, provider_customer_id, created_at, updated_at`

	findCustomerByEmailAndApp = `SELECT ` + customerColumns + ` FROM billing_customers WHERE email = $1 AND app_id = $2`
	findCustomerByProviderID  = `SELECT ` + customerColumns + ` FROM billing_customers WHERE provider_customer_id = $1`
	upsertCustomer            = `INSERT INTO billing_customers (` + customerColumns + `)
VALUES ($1, $2, $3, $4, $5, $6, $6)
ON CONFLICT (id) DO UPDATE SET
	email = EXCLUDED.email, app_id = EXCLUDED.app_id, name = EXCLUDED.name,
	provider_customer_id = EXCLUDED.provider_customer_id, updated_at = EXCLUDED.updated_at
RETURNING created_at, updated_at`
)

type customerRepo struct{ pool *pgxpool.Pool }

func (r customerRepo) FindByEmailAndApp(ctx context.Context, email, appID string) (*billing.Customer, error) {
	c, err := findOne[billing.Customer](ctx, r.pool, findCustomerByEmailAndApp, email, appID)
	if err != nil {
		return nil, fmt.Errorf("find customer: %w", err)
	}
	return c, nil
}

func (r customerRepo) FindByProviderID(ctx context.Context, providerCustomerID string) (*billing.Customer, error) {
	c, err := findOne[billing.Customer](ctx, r.pool, findCustomerByProviderID, providerCustomerID)
	if err != nil {
		return nil, fmt.Errorf("find customer: %w", err)
	}
	return c, nil
}

func (r customerRepo) Upsert(ctx context.Context, c *billing.Customer) error {
	err := r.pool.QueryRow(ctx, upsertCustomer, c.ID, c.Email, c.AppID, c.Name, c.ProviderCustomerID, now()).
		Scan(&c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return fmt.Errorf("upsert customer: %w", err)
	}
	return nil
}

const (
	subscriptionColumns = `id, customer_id, app_id, provider_subscription_id, price_id, quantity, status,
	cancel_at_period_end, current_period_start, current_period_end, trial_end, canceled_at, cancellation_reason,
	created_at, updated_at`

	findSubscriptionByCustomerAndApp = `SELECT ` + subscriptionColumns + `
FROM billing_subscriptions WHERE customer_id = $1 AND app_id = $2`
	findSubscriptionByProviderID = `SELECT ` + subscriptionColumns + `
FROM billing_subscriptions WHERE provider_subscription_id = $1`
	listSubscriptionsByApp = `SELECT ` + subscriptionColumns + ` FROM billing_subscriptions WHERE app_id = $1`
	upsertSubscription     = `INSERT INTO billing_subscriptions (` + subscriptionColumns + `)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $14)
ON CONFLICT (id) DO UPDATE SET
	customer_id = EXCLUDED.customer_id, app_id = EXCLUDED.app_id,
	provider_subscription_id = EXCLUDED.provider_subscription_id, price_id = EXCLUDED.price_id,
	quantity = EXCLUDED.quantity, status = EXCLUDED.status, cancel_at_period_end = EXCLUDED.cancel_at_period_end,
	current_period_start = EXCLUDED.current_period_start, current_period_end = EXCLUDED.current_period_end,
	trial_end = EXCLUDED.trial_end, canceled_at = EXCLUDED.canceled_at,
	cancellation_reason = EXCLUDED.cancellation_reason, updated_at = EXCLUDED.updated_at
RETURNING created_at, updated_at`
)

type subscriptionRepo struct{ pool *pgxpool.Pool }

func (r subscriptionRepo) FindByCustomerAndApp(ctx context.Context, customerID, appID string) (*billing.Subscription, error) {
	s, err := findOne[billing.Subscription](ctx, r.pool, findSubscriptionByCustomerAndApp, customerID, appID)
	if err != nil {
		return nil, fmt.Errorf("find subscription: %w", err)
	}
	return s, nil
}

func (r subscriptionRepo) FindByProviderID(ctx context.Context, providerSubscriptionID string) (*billing.Subscription, error) {
	s, err := findOne[billing.Subscription](ctx, r.pool, findSubscriptionByProviderID, providerSubscriptionID)
	if err != nil {
		return nil, fmt.Errorf("find subscription: %w", err)
	}
	return s, nil
}

func (r subscriptionRepo) ListByApp(ctx context.Context, appID string) ([]*billing.Subscription, error) {
	subs, err := findAll[billing.Subscription](ctx, r.pool, listSubscriptionsByApp, appID)
	if err != nil {
		return nil, fmt.Errorf("list subscriptions: %w", err)
	}
	return subs, nil
}

func (r subscriptionRepo) Upsert(ctx context.Context, s *billing.Subscription) error {
	err := r.pool.QueryRow(ctx, upsertSubscription,
		s.ID, s.CustomerID, s.AppID, s.ProviderSubscriptionID, s.PriceID, s.Quantity, s.Status,
		s.CancelAtPeriodEnd, s.CurrentPeriodStart, s.CurrentPeriodEnd, s.TrialEnd, s.CanceledAt, s.CancellationReason,
		now(),
	).Scan(&s.CreatedAt, &s.UpdatedAt)
	if err != nil {
		return fmt.Errorf("upsert subscription: %w", err)
	}
	return nil
}

const (
	claimEvent = `INSERT INTO billing_events (id, claimed_at) VALUES ($1, $2)
ON CONFLICT (id) DO UPDATE SET claimed_at = EXCLUDED.claimed_at
WHERE billing_events.processed_at IS NULL AND billing_events.claimed_at < $3`
	eventCompleted = `SELECT EXISTS (SELECT 1 FROM billing_events WHERE id = $1 AND processed_at IS NOT NULL)`
	completeEvent  = `INSERT INTO billing_events (id, type, data, claimed_at, processed_at) VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (id) DO UPDATE SET type = EXCLUDED.type, data = EXCLUDED.data, processed_at = EXCLUDED.processed_at`
	releaseEvent = `DELETE FROM billing_events WHERE id = $1 AND processed_at IS NULL`
)

type eventRepo struct{ pool *pgxpool.Pool }

func (r eventRepo) Claim(ctx context.Context, eventID string, lease time.Duration) error {
	t := now()
	tag, err := r.pool.Exec(ctx, claimEvent, eventID, t, t.Add(-lease))
	if err != nil {
		return fmt.Errorf("claim event: %w", err)
	}
	if tag.RowsAffected() == 1 {
		return nil
	}
	var completed bool
	if err := r.pool.QueryRow(ctx, eventCompleted, eventID).Scan(&completed); err != nil {
		return fmt.Errorf("check event: %w", err)
	}
	if completed {
		return billing.ErrDuplicate
	}
	return billing.ErrInProgress
}

func (r eventRepo) Complete(ctx context.Context, e *billing.Event) error {
	if _, err := r.pool.Exec(ctx, completeEvent, e.ID, e.Type, e.Data, now(), e.ProcessedAt); err != nil {
		return fmt.Errorf("complete event: %w", err)
	}
	return nil
}

func (r eventRepo) Release(ctx context.Context, eventID string) error {
	if _, err := r.pool.Exec(ctx, releaseEvent, eventID); err != nil {
		return fmt.Errorf("release event: %w", err)
	}
	return nil
}

const (
	priceColumns = `id, product_id, amount, currency, interval, interval_count, active, nickname, type, tax_behavior,
	created_at, updated_at`

	findPricesByIDs = `SELECT ` + priceColumns + ` FROM billing_prices WHERE id = ANY($1) AND id <> ''`
	upsertPrice     = `INSERT INTO billing_prices (` + priceColumns + `)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $11)
ON CONFLICT (id) DO UPDATE SET
	product_id = EXCLUDED.product_id, amount = EXCLUDED.amount, currency = EXCLUDED.currency,
	interval = EXCLUDED.interval, interval_count = EXCLUDED.interval_count, active = EXCLUDED.active,
	nickname = EXCLUDED.nickname, type = EXCLUDED.type, tax_behavior = EXCLUDED.tax_behavior,
	updated_at = EXCLUDED.updated_at
RETURNING created_at, updated_at`
)

type priceRepo struct{ pool *pgxpool.Pool }

func (r priceRepo) Upsert(ctx context.Context, p *billing.Price) error {
	err := r.pool.QueryRow(ctx, upsertPrice,
		p.ID, p.ProductID, p.Amount, p.Currency, p.Interval, p.IntervalCount, p.Active, p.Nickname, p.Type,
		p.TaxBehavior, now(),
	).Scan(&p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return fmt.Errorf("upsert price: %w", err)
	}
	return nil
}

func (r priceRepo) FindByIDs(ctx context.Context, ids []string) ([]*billing.Price, error) {
	prices, err := findAll[billing.Price](ctx, r.pool, findPricesByIDs, ids)
	if err != nil {
		return nil, fmt.Errorf("find prices: %w", err)
	}
	return prices, nil
}
