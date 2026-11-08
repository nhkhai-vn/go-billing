package memory

import (
	"testing"

	billing "github.com/nhkhai-vn/go-billing"
	"github.com/nhkhai-vn/go-billing/storetest"
)

func TestStore(t *testing.T) {
	storetest.Run(t, func(*testing.T) billing.Store { return NewStore() })
}
