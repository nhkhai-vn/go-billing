module github.com/nhkhai-vn/go-billing/postgres

go 1.26.0

toolchain go1.27.3

require (
	github.com/jackc/pgx/v5 v5.12.0
	github.com/nhkhai-vn/go-billing v0.1.0
)

require (
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.3 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/text v0.41.0 // indirect
)

replace github.com/nhkhai-vn/go-billing => ../
