# go-billing

Subscription billing for Go services: checkout, subscriptions with quantities, usage-based prices, the customer portal, promotion codes and webhooks, kept in your own database. Stripe is the first provider; stores exist for PostgreSQL, Cloud Firestore and memory.

It is a library, not a service. Your app imports it, mounts the HTTP handlers it wants behind its own auth, and keeps the billing records next to its own data.

## Install

```sh
go get github.com/nhkhai-vn/go-billing
go get github.com/nhkhai-vn/go-billing/stripe
go get github.com/nhkhai-vn/go-billing/postgres   # or /firestore
```

The core module has no dependencies. Each provider and store is its own module, so a PostgreSQL app never downloads the Firestore client.

## Example

```go
pool, err := pgxpool.New(ctx, os.Getenv("DATABASE_URL"))
if err != nil {
	return err
}
if err := postgres.Migrate(ctx, pool); err != nil {
	return err
}

svc, err := billing.New(billing.Config{
	AppID:    "myapp",
	Store:    postgres.NewStore(pool),
	Provider: stripe.New(os.Getenv("STRIPE_SECRET_KEY"), []string{os.Getenv("STRIPE_WEBHOOK_SECRET")}),
	Notifier: slack.New(os.Getenv("SLACK_BOT_TOKEN"), "C0123456789"), // optional
})
if err != nil {
	return err
}

checkout := handler.CheckoutOptions{
	ProductID:  "prod_123",
	SuccessURL: "https://myapp.example/billing/done?session_id={CHECKOUT_SESSION_ID}",
	CancelURL:  "https://myapp.example/billing",
	TrialDays:  14,
}
mux.Handle("POST /webhooks/stripe", handler.Webhook(svc))
mux.Handle("POST /billing/checkout", requireUser(handler.Checkout(svc, checkout)))
mux.Handle("GET /billing/plan", requireUser(handler.Plan(svc, checkout)))
mux.Handle("GET /billing/confirm", handler.Confirm(svc))
mux.Handle("GET /billing/status", requireUser(handler.Status(svc)))
mux.Handle("POST /billing/portal", requireUser(handler.Portal(svc, "https://myapp.example/billing", "")))
```

`requireUser` is your auth middleware. It puts the signed-in customer in the request context with `handler.WithCustomer(ctx, handler.Customer{Email: ..., Quantity: ...})`. Prices, URLs and the trial come from `CheckoutOptions`, never from the request, so a client cannot pick its own price.

From your own code:

```go
sub, err := svc.GetSubscriptionStatus(ctx, email)          // nil when the customer has none
_, err = svc.SetQuantity(ctx, email, 3)                    // prorated
err = svc.ReportUsage(ctx, email, "pages", 12, jobID)      // a repeated jobID counts once
_, err = svc.CancelSubscription(ctx, email, reason, false) // at period end
```

## How it behaves

- **One app per `Service`.** Everything is scoped by `AppID`: the same email in two apps is two customers. Several apps can share one Stripe account. Each app's webhook ignores events whose subscription or session belongs to another app: it answers 200 and records nothing.
- **First-time perks.** A trial and a setup fee (`SetupPriceID`) are granted only on a customer's first subscription to the app. A checkout that never got paid doesn't count. When the history can't be read, the perks are withheld.
- **Checkout by product.** `ProductID` charges the product's default price at checkout time, so a price change in the dashboard needs no deploy. `PriceID` pins one price.
- **Webhooks take effect once.** A delivery claims the event before processing it. A redelivery of a completed event is acknowledged and skipped. A delivery that overlaps one still in progress gets 409, so the provider retries it later. A failed delivery releases its claim, so the retry processes the event again. A claim whose holder died expires after 5 minutes.
- **Prices are cached.** `GetPricesByIDs` reads through the store, and `price.*` webhook events keep the cache current.
- **Notifications are best effort.** Trial started, subscription started, payment succeeded or failed, cancellation scheduled and subscription ended go to the `Notifier`. A delivery failure is logged and never fails the webhook.

## Modules

| Module | What |
|---|---|
| `github.com/nhkhai-vn/go-billing` | `billing`: the `Service`, domain types and the `Provider`, `Store` and `Notifier` interfaces. `handler`: optional `net/http` handlers. `memory`: an in-process store for tests. `storetest`: the conformance suite every store passes. `slack`: a `Notifier` posting with a bot token |
| `github.com/nhkhai-vn/go-billing/stripe` | The Stripe provider (stripe-go v87): checkout, portal, meters, promotion codes, signed webhooks with several secrets accepted during a rotation |
| `github.com/nhkhai-vn/go-billing/postgres` | A store on PostgreSQL via pgx, with embedded migrations (`Migrate`) |
| `github.com/nhkhai-vn/go-billing/firestore` | A store on Cloud Firestore in native mode, using flat `billing_*` collections |

To add a store, implement the four repositories in `billing.Store` and run `storetest.Run` against it. To add a provider, implement `billing.Provider` and return `billing.ErrUnsupported` for features it lacks.

## Stripe setup

- Enable these events on the webhook endpoint: `checkout.session.completed`, `customer.subscription.updated`, `customer.subscription.deleted`, `invoice.paid`, `invoice.payment_failed`, `price.created`, `price.updated`, `price.deleted`.
- Checkout uses automatic tax and collects the billing address.
- To use usage-based billing, create a meter and a metered price, and pass the price as `MeteredPriceID`. Keep the meter's default payload keys, `stripe_customer_id` and `value`. Put free units in the price's tiers.

## Limits

- Stripe is the only provider so far.
- One subscription per customer per app. Several plans means several apps or several quantities.
- Checkout takes card payments only.

## Development

```sh
make lint             # go mod tidy -diff, golangci-lint, govulncheck in every module
make test             # postgres tests run when BILLING_POSTGRES_URL is set
make test-emulator    # Firestore tests on the emulator (firebase CLI and Java 21)
```

## License

Apache-2.0
