package firestore

import (
	"context"
	"crypto/rand"
	"maps"
	"os"
	"slices"
	"testing"
	"time"

	gcfs "cloud.google.com/go/firestore"

	billing "github.com/nhkhai-vn/go-billing"
	"github.com/nhkhai-vn/go-billing/storetest"
)

func newClient(t *testing.T) *gcfs.Client {
	t.Helper()
	if os.Getenv("FIRESTORE_EMULATOR_HOST") == "" {
		t.Skip("FIRESTORE_EMULATOR_HOST not set; run via make test-emulator")
	}
	client, err := gcfs.NewClient(context.Background(), "demo-billing")
	if err != nil {
		t.Fatalf("firestore client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func TestStore(t *testing.T) {
	storetest.Run(t, func(t *testing.T) billing.Store { return NewStore(newClient(t)) })
}

// TestStoredFieldNames pins the document schema, which existing databases depend on.
func TestStoredFieldNames(t *testing.T) {
	client, ctx := newClient(t), context.Background()
	store := NewStore(client)
	id := "fields_" + rand.Text()

	if err := store.Customers.Upsert(ctx, &billing.Customer{ID: id, Email: "u@example.com", AppID: "app"}); err != nil {
		t.Fatalf("Upsert customer: %v", err)
	}
	if err := store.Subscriptions.Upsert(ctx, &billing.Subscription{ID: id, CustomerID: id, AppID: "app"}); err != nil {
		t.Fatalf("Upsert subscription: %v", err)
	}
	if err := store.Prices.Upsert(ctx, &billing.Price{ID: id}); err != nil {
		t.Fatalf("Upsert price: %v", err)
	}
	if err := store.Events.Claim(ctx, id, time.Minute); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	openFields := fields(t, client, eventsCollection, id)
	if err := store.Events.Complete(ctx, &billing.Event{ID: id, Type: "invoice.paid", ProcessedAt: time.Now()}); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	for _, tc := range []struct {
		name string
		got  []string
		want []string
	}{
		{customersCollection, fields(t, client, customersCollection, id),
			[]string{"app_id", "created_at", "email", "name", "provider_customer_id", "updated_at"}},
		{subscriptionsCollection, fields(t, client, subscriptionsCollection, id),
			[]string{"app_id", "cancel_at_period_end", "canceled_at", "cancellation_reason", "created_at", "current_period_end",
				"current_period_start", "customer_id", "price_id", "provider_subscription_id", "quantity", "status", "trial_end", "updated_at"}},
		{pricesCollection, fields(t, client, pricesCollection, id),
			[]string{"active", "amount", "created_at", "currency", "interval", "interval_count", "nickname", "product_id",
				"tax_behavior", "type", "updated_at"}},
		{"open event", openFields, []string{"claimed_at", "data", "processed_at", "type"}},
		{"completed event", fields(t, client, eventsCollection, id), []string{"claimed_at", "data", "processed_at", "type"}},
	} {
		if !slices.Equal(tc.got, tc.want) {
			t.Errorf("%s fields:\n got %v\nwant %v", tc.name, tc.got, tc.want)
		}
	}
}

func fields(t *testing.T, client *gcfs.Client, collection, id string) []string {
	t.Helper()
	snap, err := client.Collection(collection).Doc(id).Get(context.Background())
	if err != nil {
		t.Fatalf("get %s/%s: %v", collection, id, err)
	}
	return slices.Sorted(maps.Keys(snap.Data()))
}
