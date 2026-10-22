package api

import (
	"time"

	"github.com/tahananazkaban/jemlih/events"
	"github.com/tahananazkaban/jemlih/services/fleet/internal/domain"
)

const source = "jemlih.fleet"

type unitCreatedV1 struct {
	UnitID          string `json:"unit_id"`
	Model           string `json:"model"`
	PlateNumber     string `json:"plate_number"`
	Year            int    `json:"year"`
	Transmission    string `json:"transmission"`
	DailyPrice      int64  `json:"daily_price"`
	HourlyPrice     int64  `json:"hourly_price"`
	SecurityDeposit int64  `json:"security_deposit"`
	Status          string `json:"status"`
}

func unitCreated(rentalID string, u domain.Unit, now time.Time) (events.Envelope, error) {
	return events.New(source, rentalID, "UnitCreated", 1, unitCreatedV1{
		UnitID:          u.ID,
		Model:           u.Model,
		PlateNumber:     u.PlateNumber,
		Year:            u.Year,
		Transmission:    string(u.Transmission),
		DailyPrice:      u.DailyPrice,
		HourlyPrice:     u.HourlyPrice,
		SecurityDeposit: u.SecurityDeposit,
		Status:          string(u.Status),
	}, now)
}
