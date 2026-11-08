// Package storetest checks that a billing.Store behaves as the Service expects. Every store runs it:
//
//	func TestStore(t *testing.T) { storetest.Run(t, func(t *testing.T) billing.Store { return NewStore(db) }) }
//
// Records use fresh random ids, so the stores may share one database across tests and runs.
package storetest

import (
	"context"
	"crypto/rand"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	billing "github.com/nhkhai-vn/go-billing"
)

// Run runs every check against a store from newStore.
func Run(t *testing.T, newStore func(t *testing.T) billing.Store) {
	t.Run("Customers", func(t *testing.T) { testCustomers(t, newStore(t).Customers) })
	t.Run("Subscriptions", func(t *testing.T) { testSubscriptions(t, newStore(t).Subscriptions) })
	t.Run("Events", func(t *testing.T) { testEvents(t, newStore(t).Events) })
	t.Run("Prices", func(t *testing.T) { testPrices(t, newStore(t).Prices) })
}

func unique(prefix string) string { return prefix + "_" + rand.Text() }

// utc drops locations and monotonic readings so equal instants compare equal.
func utc[T any](v T) T {
	normalize(reflect.ValueOf(&v).Elem())
	return v
}

func normalize(v reflect.Value) {
	switch {
	case v.Type() == reflect.TypeFor[time.Time]():
		v.Set(reflect.ValueOf(v.Interface().(time.Time).UTC()))
	case v.Kind() == reflect.Pointer && !v.IsNil():
		normalize(v.Elem())
	case v.Kind() == reflect.Struct:
		for i := range v.NumField() {
			normalize(v.Field(i))
		}
	}
}

func same[T any](t *testing.T, what string, got, want T) {
	t.Helper()
	if !reflect.DeepEqual(utc(got), utc(want)) {
		t.Errorf("%s:\n got %+v\nwant %+v", what, got, want)
	}
}

func testCustomers(t *testing.T, repo billing.CustomerRepository) {
	ctx := context.Background()
	email := unique("u") + "@example.com"
	app1, app2 := unique("app"), unique("app")
	c1 := &billing.Customer{ID: app1 + "|" + email, Email: email, AppID: app1, Name: "A", ProviderCustomerID: unique("cus")}
	c2 := &billing.Customer{ID: app2 + "|" + email, Email: email, AppID: app2, Name: "B", ProviderCustomerID: unique("cus")}
	for _, c := range []*billing.Customer{c1, c2} {
		if err := repo.Upsert(ctx, c); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
		if c.CreatedAt.IsZero() || !c.UpdatedAt.Equal(c.CreatedAt) {
			t.Errorf("Upsert of a new customer set CreatedAt %v, UpdatedAt %v", c.CreatedAt, c.UpdatedAt)
		}
	}

	got, err := repo.FindByEmailAndApp(ctx, email, app1)
	if err != nil {
		t.Fatalf("FindByEmailAndApp: %v", err)
	}
	same(t, "FindByEmailAndApp", got, c1)
	got, err = repo.FindByEmailAndApp(ctx, email, app2)
	if err != nil {
		t.Fatalf("FindByEmailAndApp other app: %v", err)
	}
	same(t, "the same email in another app", got, c2)
	got, err = repo.FindByProviderID(ctx, c2.ProviderCustomerID)
	if err != nil {
		t.Fatalf("FindByProviderID: %v", err)
	}
	same(t, "FindByProviderID", got, c2)

	if _, err := repo.FindByEmailAndApp(ctx, email, unique("app")); !errors.Is(err, billing.ErrNotFound) {
		t.Errorf("FindByEmailAndApp unknown = %v, want ErrNotFound", err)
	}
	if _, err := repo.FindByProviderID(ctx, unique("cus")); !errors.Is(err, billing.ErrNotFound) {
		t.Errorf("FindByProviderID unknown = %v, want ErrNotFound", err)
	}

	created := c1.CreatedAt
	update := &billing.Customer{ID: c1.ID, Email: email, AppID: app1, Name: "A2", ProviderCustomerID: c1.ProviderCustomerID}
	if err := repo.Upsert(ctx, update); err != nil {
		t.Fatalf("Upsert existing: %v", err)
	}
	if !update.CreatedAt.Equal(created) || update.UpdatedAt.Before(created) {
		t.Errorf("Upsert existing set CreatedAt %v (want %v), UpdatedAt %v", update.CreatedAt, created, update.UpdatedAt)
	}
	got, _ = repo.FindByEmailAndApp(ctx, email, app1)
	same(t, "after update", got, update)
}

func testSubscriptions(t *testing.T, repo billing.SubscriptionRepository) {
	ctx := context.Background()
	app, other := unique("app"), unique("app")
	start := time.Now().Truncate(time.Second)
	end := start.Add(30 * 24 * time.Hour)

	full := &billing.Subscription{
		ID: unique("cust"), AppID: app, ProviderSubscriptionID: unique("sub"), PriceID: "price_1", Quantity: 3,
		Status: billing.StatusTrialing, CancelAtPeriodEnd: true, CurrentPeriodStart: &start, CurrentPeriodEnd: &end,
		TrialEnd: &end, CanceledAt: &start, CancellationReason: "too_expensive",
	}
	full.CustomerID = full.ID
	bare := &billing.Subscription{ID: unique("cust"), AppID: app, ProviderSubscriptionID: unique("sub"), Status: billing.StatusActive}
	bare.CustomerID = bare.ID
	elsewhere := &billing.Subscription{ID: unique("cust"), AppID: other, ProviderSubscriptionID: unique("sub"), Status: billing.StatusActive}
	elsewhere.CustomerID = elsewhere.ID
	for _, s := range []*billing.Subscription{full, bare, elsewhere} {
		if err := repo.Upsert(ctx, s); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
		if s.CreatedAt.IsZero() || !s.UpdatedAt.Equal(s.CreatedAt) {
			t.Errorf("Upsert of a new subscription set CreatedAt %v, UpdatedAt %v", s.CreatedAt, s.UpdatedAt)
		}
	}

	got, err := repo.FindByCustomerAndApp(ctx, full.CustomerID, app)
	if err != nil {
		t.Fatalf("FindByCustomerAndApp: %v", err)
	}
	same(t, "FindByCustomerAndApp with every field", got, full)
	got, err = repo.FindByCustomerAndApp(ctx, bare.CustomerID, app)
	if err != nil {
		t.Fatalf("FindByCustomerAndApp bare: %v", err)
	}
	same(t, "FindByCustomerAndApp with unset times", got, bare)
	got, err = repo.FindByProviderID(ctx, full.ProviderSubscriptionID)
	if err != nil {
		t.Fatalf("FindByProviderID: %v", err)
	}
	same(t, "FindByProviderID", got, full)

	if _, err := repo.FindByCustomerAndApp(ctx, full.CustomerID, other); !errors.Is(err, billing.ErrNotFound) {
		t.Errorf("FindByCustomerAndApp other app = %v, want ErrNotFound", err)
	}
	if _, err := repo.FindByProviderID(ctx, unique("sub")); !errors.Is(err, billing.ErrNotFound) {
		t.Errorf("FindByProviderID unknown = %v, want ErrNotFound", err)
	}

	list, err := repo.ListByApp(ctx, app)
	if err != nil {
		t.Fatalf("ListByApp: %v", err)
	}
	ids := map[string]bool{}
	for _, s := range list {
		ids[s.ID] = true
	}
	if len(list) != 2 || !ids[full.ID] || !ids[bare.ID] {
		t.Errorf("ListByApp = %d subscriptions %v, want %s and %s", len(list), ids, full.ID, bare.ID)
	}
	if list, err := repo.ListByApp(ctx, unique("app")); err != nil || len(list) != 0 {
		t.Errorf("ListByApp unknown app = %v, %v; want empty", list, err)
	}

	created := bare.CreatedAt
	update := *bare
	update.Status, update.Quantity, update.CurrentPeriodEnd = billing.StatusCanceled, 2, &end
	if err := repo.Upsert(ctx, &update); err != nil {
		t.Fatalf("Upsert existing: %v", err)
	}
	if !update.CreatedAt.Equal(created) || update.UpdatedAt.Before(created) {
		t.Errorf("Upsert existing set CreatedAt %v (want %v), UpdatedAt %v", update.CreatedAt, created, update.UpdatedAt)
	}
	got, _ = repo.FindByProviderID(ctx, bare.ProviderSubscriptionID)
	same(t, "after update", got, &update)
}

func testEvents(t *testing.T, repo billing.EventRepository) {
	ctx := context.Background()
	const lease = time.Minute

	t.Run("claim, complete, duplicate", func(t *testing.T) {
		id := unique("evt")
		if err := repo.Claim(ctx, id, lease); err != nil {
			t.Fatalf("first Claim: %v", err)
		}
		if err := repo.Claim(ctx, id, lease); !errors.Is(err, billing.ErrInProgress) {
			t.Errorf("Claim while open = %v, want ErrInProgress", err)
		}
		if err := repo.Complete(ctx, &billing.Event{ID: id, Type: "checkout.session.completed", Data: `{"id":"cs_1"}`, ProcessedAt: time.Now()}); err != nil {
			t.Fatalf("Complete: %v", err)
		}
		if err := repo.Claim(ctx, id, lease); !errors.Is(err, billing.ErrDuplicate) {
			t.Errorf("Claim after Complete = %v, want ErrDuplicate", err)
		}
		if err := repo.Release(ctx, id); err != nil {
			t.Fatalf("Release of a completed event: %v", err)
		}
		if err := repo.Claim(ctx, id, lease); !errors.Is(err, billing.ErrDuplicate) {
			t.Errorf("Claim after releasing a completed event = %v, want ErrDuplicate", err)
		}
	})

	t.Run("release lets a retry claim", func(t *testing.T) {
		id := unique("evt")
		if err := repo.Claim(ctx, id, lease); err != nil {
			t.Fatalf("Claim: %v", err)
		}
		if err := repo.Release(ctx, id); err != nil {
			t.Fatalf("Release: %v", err)
		}
		if err := repo.Claim(ctx, id, lease); err != nil {
			t.Errorf("Claim after Release = %v, want nil", err)
		}
		if err := repo.Release(ctx, unique("evt")); err != nil {
			t.Errorf("Release of an unknown event = %v, want nil", err)
		}
	})

	t.Run("expired claim is taken over", func(t *testing.T) {
		id := unique("evt")
		if err := repo.Claim(ctx, id, lease); err != nil {
			t.Fatalf("Claim: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
		if err := repo.Claim(ctx, id, 10*time.Millisecond); err != nil {
			t.Errorf("Claim after the lease = %v, want nil", err)
		}
		if err := repo.Claim(ctx, id, lease); !errors.Is(err, billing.ErrInProgress) {
			t.Errorf("Claim after a takeover = %v, want ErrInProgress", err)
		}
	})

	t.Run("one of concurrent claims wins", func(t *testing.T) {
		id := unique("evt")
		const n = 8
		errs := make([]error, n)
		var wg sync.WaitGroup
		for i := range n {
			wg.Go(func() { errs[i] = repo.Claim(ctx, id, lease) })
		}
		wg.Wait()
		won := 0
		for _, err := range errs {
			switch {
			case err == nil:
				won++
			case !errors.Is(err, billing.ErrInProgress):
				t.Errorf("concurrent Claim = %v, want nil or ErrInProgress", err)
			}
		}
		if won != 1 {
			t.Errorf("%d concurrent claims won, want 1", won)
		}
	})
}

func testPrices(t *testing.T, repo billing.PriceRepository) {
	ctx := context.Background()
	monthly := &billing.Price{
		ID: unique("price"), ProductID: "prod_1", Amount: 1000, Currency: "jpy", Interval: "month", IntervalCount: 1,
		Active: true, Nickname: "Monthly", Type: "recurring", TaxBehavior: "inclusive",
	}
	setup := &billing.Price{ID: unique("price"), ProductID: "prod_1", Amount: 500, Currency: "usd", Type: "one_time"}
	for _, p := range []*billing.Price{monthly, setup} {
		if err := repo.Upsert(ctx, p); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
		if p.CreatedAt.IsZero() || !p.UpdatedAt.Equal(p.CreatedAt) {
			t.Errorf("Upsert of a new price set CreatedAt %v, UpdatedAt %v", p.CreatedAt, p.UpdatedAt)
		}
	}

	got, err := repo.FindByIDs(ctx, []string{monthly.ID, "", unique("price"), setup.ID})
	if err != nil {
		t.Fatalf("FindByIDs: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("FindByIDs = %d prices, want 2", len(got))
	}
	byID := map[string]*billing.Price{got[0].ID: got[0], got[1].ID: got[1]}
	same(t, "monthly price", byID[monthly.ID], monthly)
	same(t, "setup price", byID[setup.ID], setup)

	if got, err := repo.FindByIDs(ctx, nil); err != nil || len(got) != 0 {
		t.Errorf("FindByIDs(nil) = %v, %v; want empty", got, err)
	}

	created := monthly.CreatedAt
	update := *monthly
	update.Active = false
	if err := repo.Upsert(ctx, &update); err != nil {
		t.Fatalf("Upsert existing: %v", err)
	}
	if !update.CreatedAt.Equal(created) || update.UpdatedAt.Before(created) {
		t.Errorf("Upsert existing set CreatedAt %v (want %v), UpdatedAt %v", update.CreatedAt, created, update.UpdatedAt)
	}
	got, _ = repo.FindByIDs(ctx, []string{monthly.ID})
	if len(got) != 1 {
		t.Fatalf("FindByIDs after update = %d prices", len(got))
	}
	same(t, "after update", got[0], &update)
}
