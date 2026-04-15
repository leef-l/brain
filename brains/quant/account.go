package quant

import (
	"github.com/leef-l/brain/brains/quant/exchange"
)

// Account binds an Exchange to credentials and identity.
// Each OKX sub-account, IBKR account, or paper simulator is one Account.
type Account struct {
	// ID is a unique identifier for this account (e.g. "okx-main", "paper-test").
	ID string

	// Exchange is the venue this account trades on.
	Exchange exchange.Exchange

	// Tags are arbitrary labels for grouping (e.g. "production", "test").
	Tags []string
}

// Capabilities is a convenience shortcut to the underlying exchange.
func (a *Account) Capabilities() exchange.Capabilities {
	return a.Exchange.Capabilities()
}

// CanShort returns whether this account's exchange supports short selling.
func (a *Account) CanShort() bool {
	return a.Exchange.Capabilities().CanShort
}

// MaxLeverage returns the maximum leverage allowed by this account's exchange.
func (a *Account) MaxLeverage() int {
	return a.Exchange.Capabilities().MaxLeverage
}
