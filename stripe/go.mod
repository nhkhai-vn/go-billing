module github.com/nhkhai-vn/go-billing/stripe

go 1.26.0

toolchain go1.27.3

require (
	github.com/nhkhai-vn/go-billing v0.1.0
	github.com/stripe/stripe-go/v87 v87.0.0
)

require github.com/shopspring/decimal v1.4.0 // indirect

replace github.com/nhkhai-vn/go-billing => ../
