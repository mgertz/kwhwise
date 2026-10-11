package domain

import "time"

// SpotPrice is an hourly wholesale price, excluding VAT and other charges.
// Start is inclusive, End exclusive. Both timestamps identify actual instants
// in UTC, so repeated local hours remain distinct.
type SpotPrice struct {
	Start           time.Time
	End             time.Time
	PriceOreExclVAT float64
}
