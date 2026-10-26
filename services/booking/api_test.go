package booking_test

import (
	"net/http"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/tahananazkaban/jemlih/pkg/auth"
	"github.com/tahananazkaban/jemlih/pkg/env"
	"github.com/tahananazkaban/jemlih/pkg/testkit"
	"github.com/tahananazkaban/jemlih/services/booking/internal/app"
)

type price struct {
	DailyPrice      int64 `json:"daily_price"`
	HourlyPrice     int64 `json:"hourly_price"`
	Days            int64 `json:"days"`
	ExtraHours      int64 `json:"extra_hours"`
	ExtraHoursFee   int64 `json:"extra_hours_fee"`
	RentalFee       int64 `json:"rental_fee"`
	SecurityDeposit int64 `json:"security_deposit"`
	TotalAmount     int64 `json:"total_amount"`
}

type booking struct {
	ID   string `json:"id"`
	Unit struct {
		ID          string `json:"id"`
		Model       string `json:"model"`
		PlateNumber string `json:"plate_number"`
	} `json:"unit"`
	RenterID      string    `json:"renter_id"`
	PickupAt      time.Time `json:"pickup_at"`
	ReturnAt      time.Time `json:"return_at"`
	Status        string    `json:"status"`
	HoldExpiresAt time.Time `json:"hold_expires_at"`
	CreatedAt     time.Time `json:"created_at"`
	Price         price     `json:"price"`
	Cancellation  *struct {
		By     string `json:"by"`
		Reason string `json:"reason"`
	} `json:"cancellation"`
}

type availability struct {
	Units []struct {
		ID    string `json:"id"`
		Price price  `json:"price"`
	} `json:"units"`
}

// rental is one fresh Rental with an Owner, Units in fleet and a booking service started for it.
type rental struct {
	t       *testing.T
	id      string
	pool    *testkit.Pool
	booking *testkit.Client
	owner   string
	renter  string
}

func newRental(t *testing.T, hold time.Duration) *rental {
	t.Helper()
	pool := testkit.UserPool(t)
	handler, err := app.New(app.Config{
		AWS:          testkit.AWSConfig(t),
		Auth:         pool.Auth,
		BookingTable: env.String("BOOKING_TABLE", "jemlih-booking"),
		FleetURL:     env.String("FLEET_URL", "http://localhost:8081"),
		Hold:         hold,
	})
	if err != nil {
		t.Fatalf("start booking: %v", err)
	}
	id := testkit.NewRentalID()
	return &rental{
		t: t, id: id, pool: pool, booking: testkit.Serve(t, handler),
		owner: pool.NewUser(id, auth.Owner).IDToken, renter: pool.NewUser(id, auth.Renter).IDToken,
	}
}

// addUnit creates a Unit through the running fleet container and returns its ID.
func (r *rental) addUnit(plate string, daily, hourly, deposit int64) string {
	r.t.Helper()
	var u struct {
		ID string `json:"id"`
	}
	testkit.Remote(r.t, env.String("FLEET_URL", "http://localhost:8081")).As(r.owner).
		Do(http.MethodPost, "/units", map[string]any{
			"model": "Honda Vario 125", "plate_number": plate, "year": 2023, "transmission": "automatic",
			"daily_price": daily, "hourly_price": hourly, "security_deposit": deposit,
		}).Expect(http.StatusCreated).Decode(&u)
	return u.ID
}

func (r *rental) as(token string) *testkit.Client { return r.booking.As(token) }

// base is fixed once, a whole hour well in the future, so windows never shift or start in the past.
var base = time.Now().UTC().Add(72 * time.Hour).Truncate(time.Hour)

func at(hoursFromBase int) time.Time {
	return base.Add(time.Duration(hoursFromBase) * time.Hour)
}

func request(unitID string, from, to time.Time) map[string]string {
	return map[string]string{"unit_id": unitID, "pickup_at": from.Format(time.RFC3339), "return_at": to.Format(time.RFC3339)}
}

func availabilityPath(from, to time.Time) string {
	return "/availability?pickup_at=" + url.QueryEscape(from.Format(time.RFC3339)) + "&return_at=" + url.QueryEscape(to.Format(time.RFC3339))
}

func TestCreateBookingHoldsTheUnitAtTodaysPrices(t *testing.T) {
	r := newRental(t, 2*time.Hour)
	unit := r.addUnit("AB 1 A", 85000, 15000, 300000)

	before := time.Now()
	resp := r.as(r.renter).Do(http.MethodPost, "/bookings", request(unit, at(0), at(26))).Expect(http.StatusCreated)
	var b booking
	resp.Decode(&b)

	if resp.Header.Get("Location") != "/bookings/"+b.ID {
		t.Errorf("Location = %q", resp.Header.Get("Location"))
	}
	if b.Status != "held" || b.Unit.ID != unit || b.Unit.PlateNumber != "AB 1 A" || b.RenterID == "" {
		t.Errorf("booking = %+v", b)
	}
	if !b.PickupAt.Equal(at(0)) || !b.ReturnAt.Equal(at(26)) {
		t.Errorf("window = %s..%s, want %s..%s", b.PickupAt, b.ReturnAt, at(0), at(26))
	}
	if hold := b.HoldExpiresAt.Sub(before); hold < 2*time.Hour-time.Minute || hold > 2*time.Hour+time.Minute {
		t.Errorf("hold expires %s after creation, want 2h", hold)
	}
	want := price{DailyPrice: 85000, HourlyPrice: 15000, Days: 1, ExtraHours: 2, ExtraHoursFee: 30000, RentalFee: 115000, SecurityDeposit: 300000, TotalAmount: 415000}
	if b.Price != want {
		t.Errorf("price = %+v, want %+v", b.Price, want)
	}

	var fetched booking
	r.as(r.renter).Do(http.MethodGet, "/bookings/"+b.ID, nil).Expect(http.StatusOK).Decode(&fetched)
	if fetched.ID != b.ID || fetched.Price != b.Price || fetched.Status != "held" {
		t.Errorf("fetched = %+v", fetched)
	}
}

func TestPriceBreakdown(t *testing.T) {
	r := newRental(t, 2*time.Hour)
	r.addUnit("AB 2 B", 100000, 15000, 250000)

	cases := []struct {
		name        string
		hours       int
		days, extra int64
		extraFee    int64
		fee         int64
	}{
		{"one hour", 1, 0, 1, 15000, 15000},
		{"a few hours", 5, 0, 5, 75000, 75000},
		{"hours capped at the daily price", 8, 0, 8, 100000, 100000},
		{"exactly one day", 24, 1, 0, 0, 100000},
		{"a day and two hours", 26, 1, 2, 30000, 130000},
		{"three days", 72, 3, 0, 0, 300000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var a availability
			r.as(r.renter).Do(http.MethodGet, availabilityPath(at(0), at(tc.hours)), nil).Expect(http.StatusOK).Decode(&a)
			if len(a.Units) != 1 {
				t.Fatalf("units = %+v", a.Units)
			}
			p := a.Units[0].Price
			if p.Days != tc.days || p.ExtraHours != tc.extra || p.ExtraHoursFee != tc.extraFee || p.RentalFee != tc.fee || p.TotalAmount != tc.fee+250000 {
				t.Errorf("price = %+v, want %d days %d hours fee %d", p, tc.days, tc.extra, tc.fee)
			}
		})
	}
}

func TestPartialHoursAreChargedAsStartedHours(t *testing.T) {
	r := newRental(t, 2*time.Hour)
	unit := r.addUnit("AB 3 C", 100000, 15000, 0)

	var b booking
	r.as(r.renter).Do(http.MethodPost, "/bookings", request(unit, at(0), at(2).Add(10*time.Minute))).
		Expect(http.StatusCreated).Decode(&b)
	if b.Price.ExtraHours != 3 || b.Price.RentalFee != 45000 {
		t.Errorf("price = %+v, want 3 started hours", b.Price)
	}
}

func TestAvailabilityListsOnlyUnitsFreeForTheWholeWindow(t *testing.T) {
	r := newRental(t, 2*time.Hour)
	booked := r.addUnit("AB 4 D", 85000, 15000, 300000)
	free := r.addUnit("AB 5 E", 90000, 15000, 300000)
	r.as(r.renter).Do(http.MethodPost, "/bookings", request(booked, at(10), at(20))).Expect(http.StatusCreated)

	ids := func(from, to time.Time) map[string]bool {
		var a availability
		r.as(r.owner).Do(http.MethodGet, availabilityPath(from, to), nil).Expect(http.StatusOK).Decode(&a)
		got := map[string]bool{}
		for _, u := range a.Units {
			got[u.ID] = true
		}
		return got
	}
	if got := ids(at(15), at(30)); got[booked] || !got[free] {
		t.Errorf("overlapping window: %v, want only %s", got, free)
	}
	if got := ids(at(0), at(10)); !got[booked] || !got[free] {
		t.Errorf("window ending at pickup: %v, want both", got)
	}
	if got := ids(at(20), at(25)); !got[booked] || !got[free] {
		t.Errorf("window starting at return: %v, want both", got)
	}
}

func TestOverlappingBookingIsRejected(t *testing.T) {
	r := newRental(t, 2*time.Hour)
	unit := r.addUnit("AB 6 F", 85000, 15000, 300000)
	other := r.pool.NewUser(r.id, auth.Renter).IDToken

	r.as(r.renter).Do(http.MethodPost, "/bookings", request(unit, at(10), at(20))).Expect(http.StatusCreated)

	for name, window := range map[string][2]time.Time{
		"same window":   {at(10), at(20)},
		"starts inside": {at(19), at(30)},
		"ends inside":   {at(0), at(11)},
		"covers it":     {at(5), at(25)},
		"inside of it":  {at(12), at(14)},
	} {
		t.Run(name, func(t *testing.T) {
			var e testkit.ErrorBody
			r.as(other).Do(http.MethodPost, "/bookings", request(unit, window[0], window[1])).Expect(http.StatusConflict).Decode(&e)
			if e.Error.Code != "unit_unavailable" {
				t.Errorf("code = %q, want unit_unavailable", e.Error.Code)
			}
		})
	}
	r.as(other).Do(http.MethodPost, "/bookings", request(unit, at(20), at(24))).Expect(http.StatusCreated)
}

func TestConcurrentBookingsOfTheSameWindowHaveExactlyOneWinner(t *testing.T) {
	r := newRental(t, 2*time.Hour)
	unit := r.addUnit("AB 7 G", 85000, 15000, 300000)

	const racers = 20
	statuses := make([]int, racers)
	errs := make([]error, racers)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range racers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			resp, err := r.as(r.renter).Send(http.MethodPost, "/bookings", request(unit, at(30), at(40)))
			if err != nil {
				errs[i] = err
				return
			}
			statuses[i] = resp.Status
		}()
	}
	close(start)
	wg.Wait()

	counts := map[int]int{}
	for i, s := range statuses {
		if errs[i] != nil {
			t.Fatalf("racer %d: %v", i, errs[i])
		}
		counts[s]++
	}
	if counts[http.StatusCreated] != 1 || counts[http.StatusConflict] != racers-1 {
		t.Errorf("statuses = %v, want exactly one 201 and %d 409", counts, racers-1)
	}
}

func TestConcurrentBookingsOfDifferentWindowsAllSucceed(t *testing.T) {
	r := newRental(t, 2*time.Hour)
	unit := r.addUnit("AB 8 H", 85000, 15000, 300000)

	const racers = 6
	statuses := make([]int, racers)
	var wg sync.WaitGroup
	for i := range racers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := r.as(r.renter).Send(http.MethodPost, "/bookings", request(unit, at(i*10), at(i*10+5)))
			if err == nil {
				statuses[i] = resp.Status
			}
		}()
	}
	wg.Wait()
	for i, s := range statuses {
		if s != http.StatusCreated {
			t.Errorf("window %d: status %d, want 201 after retrying the calendar", i, s)
		}
	}
}

func TestCreateBookingRejectsInvalidRequests(t *testing.T) {
	r := newRental(t, 2*time.Hour)
	unit := r.addUnit("AB 9 J", 85000, 15000, 300000)
	past := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Hour)

	cases := map[string]struct {
		body  any
		field string
	}{
		"missing unit":     {map[string]string{"pickup_at": at(0).Format(time.RFC3339), "return_at": at(2).Format(time.RFC3339)}, "unit_id"},
		"missing return":   {map[string]string{"unit_id": unit, "pickup_at": at(0).Format(time.RFC3339)}, "return_at"},
		"not a timestamp":  {map[string]string{"unit_id": unit, "pickup_at": "tomorrow", "return_at": at(2).Format(time.RFC3339)}, "pickup_at"},
		"pickup in past":   {request(unit, past, past.Add(3*time.Hour)), "pickup_at"},
		"under an hour":    {request(unit, at(0), at(0).Add(30*time.Minute)), "return_at"},
		"return before":    {request(unit, at(5), at(1)), "return_at"},
		"over thirty days": {request(unit, at(0), at(31*24)), "return_at"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var e testkit.ErrorBody
			r.as(r.renter).Do(http.MethodPost, "/bookings", tc.body).Expect(http.StatusBadRequest).Decode(&e)
			if _, ok := e.Error.Fields[tc.field]; !ok {
				t.Errorf("fields = %v, want %q", e.Error.Fields, tc.field)
			}
		})
	}

	var e testkit.ErrorBody
	r.as(r.renter).Do(http.MethodPost, "/bookings", request("no-such-unit", at(0), at(2))).Expect(http.StatusNotFound).Decode(&e)
	if e.Error.Code != "unit_not_found" {
		t.Errorf("unknown unit code = %q", e.Error.Code)
	}
}

func TestOnlyRentersCreateBookings(t *testing.T) {
	r := newRental(t, 2*time.Hour)
	unit := r.addUnit("AB 10 K", 85000, 15000, 300000)
	for _, role := range []auth.Role{auth.Owner, auth.Staff} {
		r.as(r.pool.NewUser(r.id, role).IDToken).Do(http.MethodPost, "/bookings", request(unit, at(0), at(2))).Expect(http.StatusForbidden)
	}
	r.booking.Do(http.MethodPost, "/bookings", request(unit, at(0), at(2))).Expect(http.StatusUnauthorized)
}

func TestRentersSeeOnlyTheirOwnBookings(t *testing.T) {
	r := newRental(t, 2*time.Hour)
	unit := r.addUnit("AB 11 L", 85000, 15000, 300000)
	other := r.pool.NewUser(r.id, auth.Renter).IDToken

	var mine, theirs booking
	r.as(r.renter).Do(http.MethodPost, "/bookings", request(unit, at(0), at(2))).Expect(http.StatusCreated).Decode(&mine)
	r.as(other).Do(http.MethodPost, "/bookings", request(unit, at(5), at(7))).Expect(http.StatusCreated).Decode(&theirs)

	var list struct {
		Bookings []booking `json:"bookings"`
	}
	r.as(r.renter).Do(http.MethodGet, "/bookings", nil).Expect(http.StatusOK).Decode(&list)
	if len(list.Bookings) != 1 || list.Bookings[0].ID != mine.ID {
		t.Errorf("renter sees %+v, want only %s", list.Bookings, mine.ID)
	}
	r.as(r.renter).Do(http.MethodGet, "/bookings/"+theirs.ID, nil).Expect(http.StatusNotFound)
	r.as(r.renter).Do(http.MethodPost, "/bookings/"+theirs.ID+"/cancel", nil).Expect(http.StatusNotFound)

	r.as(r.owner).Do(http.MethodGet, "/bookings", nil).Expect(http.StatusOK).Decode(&list)
	if len(list.Bookings) != 2 || list.Bookings[0].ID != theirs.ID {
		t.Errorf("owner sees %+v, want both, newest first", list.Bookings)
	}
	stranger := testkit.NewRentalID()
	r.as(r.pool.NewUser(stranger, auth.Owner).IDToken).Do(http.MethodGet, "/bookings/"+mine.ID, nil).Expect(http.StatusNotFound)
}

func TestOwnersFilterBookingsByDateAndStatus(t *testing.T) {
	r := newRental(t, 2*time.Hour)
	unit := r.addUnit("AB 12 M", 85000, 15000, 300000)
	var early, late booking
	r.as(r.renter).Do(http.MethodPost, "/bookings", request(unit, at(0), at(5))).Expect(http.StatusCreated).Decode(&early)
	r.as(r.renter).Do(http.MethodPost, "/bookings", request(unit, at(48), at(50))).Expect(http.StatusCreated).Decode(&late)
	r.as(r.renter).Do(http.MethodPost, "/bookings/"+early.ID+"/cancel", nil).Expect(http.StatusOK)

	list := func(query string) []string {
		var l struct {
			Bookings []booking `json:"bookings"`
		}
		r.as(r.owner).Do(http.MethodGet, "/bookings?"+query, nil).Expect(http.StatusOK).Decode(&l)
		ids := []string{}
		for _, b := range l.Bookings {
			ids = append(ids, b.ID)
		}
		return ids
	}
	q := func(k, v string) string { return k + "=" + url.QueryEscape(v) }
	if got := list(q("status", "cancelled")); len(got) != 1 || got[0] != early.ID {
		t.Errorf("cancelled = %v, want [%s]", got, early.ID)
	}
	if got := list(q("status", "held")); len(got) != 1 || got[0] != late.ID {
		t.Errorf("held = %v, want [%s]", got, late.ID)
	}
	if got := list(q("from", at(24).Format(time.RFC3339)) + "&" + q("to", at(72).Format(time.RFC3339))); len(got) != 1 || got[0] != late.ID {
		t.Errorf("from/to = %v, want [%s]", got, late.ID)
	}
	var e testkit.ErrorBody
	r.as(r.owner).Do(http.MethodGet, "/bookings?status=returned", nil).Expect(http.StatusBadRequest).Decode(&e)
	if _, ok := e.Error.Fields["status"]; !ok {
		t.Errorf("fields = %v", e.Error.Fields)
	}
}

func TestCancellingReleasesTheUnit(t *testing.T) {
	r := newRental(t, 2*time.Hour)
	unit := r.addUnit("AB 13 N", 85000, 15000, 300000)

	var b booking
	r.as(r.renter).Do(http.MethodPost, "/bookings", request(unit, at(0), at(10))).Expect(http.StatusCreated).Decode(&b)
	var cancelled booking
	r.as(r.renter).Do(http.MethodPost, "/bookings/"+b.ID+"/cancel", nil).Expect(http.StatusOK).Decode(&cancelled)
	if cancelled.Status != "cancelled" || cancelled.Cancellation == nil || cancelled.Cancellation.By != "renter" {
		t.Errorf("cancelled = %+v", cancelled)
	}

	var e testkit.ErrorBody
	r.as(r.renter).Do(http.MethodPost, "/bookings/"+b.ID+"/cancel", nil).Expect(http.StatusConflict).Decode(&e)
	if e.Error.Code != "not_cancellable" {
		t.Errorf("second cancel code = %q", e.Error.Code)
	}
	r.as(r.pool.NewUser(r.id, auth.Renter).IDToken).Do(http.MethodPost, "/bookings", request(unit, at(0), at(10))).Expect(http.StatusCreated)
}

func TestOwnersCancelWithAReason(t *testing.T) {
	r := newRental(t, 2*time.Hour)
	unit := r.addUnit("AB 14 P", 85000, 15000, 300000)
	var b booking
	r.as(r.renter).Do(http.MethodPost, "/bookings", request(unit, at(0), at(10))).Expect(http.StatusCreated).Decode(&b)

	var e testkit.ErrorBody
	r.as(r.owner).Do(http.MethodPost, "/bookings/"+b.ID+"/cancel", map[string]string{"reason": "  "}).Expect(http.StatusBadRequest).Decode(&e)
	if _, ok := e.Error.Fields["reason"]; !ok {
		t.Errorf("fields = %v, want reason", e.Error.Fields)
	}
	r.as(r.pool.NewUser(r.id, auth.Staff).IDToken).Do(http.MethodPost, "/bookings/"+b.ID+"/cancel", map[string]string{"reason": "x"}).Expect(http.StatusForbidden)

	var cancelled booking
	r.as(r.owner).Do(http.MethodPost, "/bookings/"+b.ID+"/cancel", map[string]string{"reason": "Unit needs repair"}).
		Expect(http.StatusOK).Decode(&cancelled)
	if cancelled.Cancellation == nil || cancelled.Cancellation.By != "owner" || cancelled.Cancellation.Reason != "Unit needs repair" {
		t.Errorf("cancellation = %+v", cancelled.Cancellation)
	}
}

func TestAnExpiredHoldNoLongerBlocksTheUnit(t *testing.T) {
	r := newRental(t, 5*time.Second)
	unit := r.addUnit("AB 15 Q", 85000, 15000, 300000)
	var b booking
	r.as(r.renter).Do(http.MethodPost, "/bookings", request(unit, at(0), at(10))).Expect(http.StatusCreated).Decode(&b)
	r.as(r.renter).Do(http.MethodPost, "/bookings", request(unit, at(0), at(10))).Expect(http.StatusConflict)

	time.Sleep(time.Until(b.HoldExpiresAt) + 500*time.Millisecond)

	var expired booking
	r.as(r.renter).Do(http.MethodGet, "/bookings/"+b.ID, nil).Expect(http.StatusOK).Decode(&expired)
	if expired.Status != "expired" {
		t.Errorf("status = %q, want expired", expired.Status)
	}
	r.as(r.renter).Do(http.MethodPost, "/bookings/"+b.ID+"/cancel", nil).Expect(http.StatusConflict)
	r.as(r.renter).Do(http.MethodPost, "/bookings", request(unit, at(0), at(10))).Expect(http.StatusCreated)
}

func TestBookingsOfAnotherRentalsUnitAreImpossible(t *testing.T) {
	a := newRental(t, 2*time.Hour)
	b := newRental(t, 2*time.Hour)
	unit := a.addUnit("AB 16 R", 85000, 15000, 300000)

	var e testkit.ErrorBody
	b.as(b.renter).Do(http.MethodPost, "/bookings", request(unit, at(0), at(2))).Expect(http.StatusNotFound).Decode(&e)
	if e.Error.Code != "unit_not_found" {
		t.Errorf("code = %q, want unit_not_found", e.Error.Code)
	}
}
