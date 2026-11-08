CREATE TABLE billing_customers (
	id                   text PRIMARY KEY,
	email                text NOT NULL DEFAULT '',
	app_id               text NOT NULL DEFAULT '',
	name                 text NOT NULL DEFAULT '',
	provider_customer_id text NOT NULL DEFAULT '',
	created_at           timestamptz NOT NULL,
	updated_at           timestamptz NOT NULL
);
CREATE UNIQUE INDEX billing_customers_app_id_email ON billing_customers (app_id, email);
CREATE INDEX billing_customers_provider_customer_id ON billing_customers (provider_customer_id);

CREATE TABLE billing_subscriptions (
	id                       text PRIMARY KEY,
	customer_id              text NOT NULL DEFAULT '',
	app_id                   text NOT NULL DEFAULT '',
	provider_subscription_id text NOT NULL DEFAULT '',
	price_id                 text NOT NULL DEFAULT '',
	quantity                 bigint NOT NULL DEFAULT 0,
	status                   text NOT NULL DEFAULT '',
	cancel_at_period_end     boolean NOT NULL DEFAULT false,
	current_period_start     timestamptz,
	current_period_end       timestamptz,
	trial_end                timestamptz,
	canceled_at              timestamptz,
	cancellation_reason      text NOT NULL DEFAULT '',
	created_at               timestamptz NOT NULL,
	updated_at               timestamptz NOT NULL
);
CREATE INDEX billing_subscriptions_customer_id_app_id ON billing_subscriptions (customer_id, app_id);
CREATE INDEX billing_subscriptions_provider_subscription_id ON billing_subscriptions (provider_subscription_id);
CREATE INDEX billing_subscriptions_app_id ON billing_subscriptions (app_id);

CREATE TABLE billing_events (
	id           text PRIMARY KEY,
	type         text NOT NULL DEFAULT '',
	data         text NOT NULL DEFAULT '',
	claimed_at   timestamptz NOT NULL,
	processed_at timestamptz
);

CREATE TABLE billing_prices (
	id             text PRIMARY KEY,
	product_id     text NOT NULL DEFAULT '',
	amount         bigint NOT NULL DEFAULT 0,
	currency       text NOT NULL DEFAULT '',
	interval       text NOT NULL DEFAULT '',
	interval_count bigint NOT NULL DEFAULT 0,
	active         boolean NOT NULL DEFAULT false,
	nickname       text NOT NULL DEFAULT '',
	type           text NOT NULL DEFAULT '',
	tax_behavior   text NOT NULL DEFAULT '',
	created_at     timestamptz NOT NULL,
	updated_at     timestamptz NOT NULL
);
