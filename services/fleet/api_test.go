package fleet_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/tahananazkaban/jemlih/pkg/auth"
	"github.com/tahananazkaban/jemlih/pkg/env"
	"github.com/tahananazkaban/jemlih/pkg/testkit"
	"github.com/tahananazkaban/jemlih/services/fleet/internal/app"
)

type unit struct {
	ID              string `json:"id"`
	Model           string `json:"model"`
	PlateNumber     string `json:"plate_number"`
	Year            int    `json:"year"`
	Transmission    string `json:"transmission"`
	DailyPrice      int64  `json:"daily_price"`
	HourlyPrice     int64  `json:"hourly_price"`
	SecurityDeposit int64  `json:"security_deposit"`
	Status          string `json:"status"`
	CreatedAt       string `json:"created_at"`
}

// serveFleet runs the real fleet service against the emulator, with no credentials attached.
func serveFleet(t *testing.T, pool *testkit.Pool) *testkit.Client {
	t.Helper()
	handler, err := app.New(app.Config{
		AWS:       testkit.AWSConfig(t),
		Auth:      pool.Auth,
		UnitTable: env.String("FLEET_TABLE", "jemlih-fleet"),
	})
	if err != nil {
		t.Fatalf("start fleet: %v", err)
	}
	return testkit.Serve(t, handler)
}

// startFleet returns a fleet client signed in as an Owner of rentalID.
func startFleet(t *testing.T, rentalID string) *testkit.Client {
	t.Helper()
	pool := testkit.UserPool(t)
	return serveFleet(t, pool).As(pool.NewUser(rentalID, auth.Owner).IDToken)
}

func validUnit() map[string]any {
	return map[string]any{
		"model":            "Honda Vario 125",
		"plate_number":     "ab 1234 xy",
		"year":             2023,
		"transmission":     "automatic",
		"daily_price":      85000,
		"hourly_price":     15000,
		"security_deposit": 300000,
	}
}

func TestCreateAndGetUnit(t *testing.T) {
	fleet := startFleet(t, testkit.NewRentalID())

	created := fleet.Do(http.MethodPost, "/units", validUnit()).Expect(http.StatusCreated)
	var u unit
	created.Decode(&u)

	if u.ID == "" || u.CreatedAt == "" {
		t.Fatalf("missing id or created_at: %s", created.Body)
	}
	if got := created.Header.Get("Location"); got != "/units/"+u.ID {
		t.Errorf("Location = %q, want /units/%s", got, u.ID)
	}
	want := unit{
		ID: u.ID, Model: "Honda Vario 125", PlateNumber: "AB 1234 XY", Year: 2023, Transmission: "automatic",
		DailyPrice: 85000, HourlyPrice: 15000, SecurityDeposit: 300000, Status: "available", CreatedAt: u.CreatedAt,
	}
	if u != want {
		t.Errorf("created unit = %+v, want %+v", u, want)
	}

	var fetched unit
	fleet.Do(http.MethodGet, "/units/"+u.ID, nil).Expect(http.StatusOK).Decode(&fetched)
	if fetched != u {
		t.Errorf("fetched unit = %+v, want %+v", fetched, u)
	}
}

func TestListUnitsReturnsOnlyTheRentalsUnits(t *testing.T) {
	rentalID := testkit.NewRentalID()
	fleet := startFleet(t, rentalID)
	other := startFleet(t, testkit.NewRentalID())

	first := validUnit()
	second := validUnit()
	second["plate_number"] = "B 77 KZ"
	var a, b unit
	fleet.Do(http.MethodPost, "/units", first).Expect(http.StatusCreated).Decode(&a)
	fleet.Do(http.MethodPost, "/units", second).Expect(http.StatusCreated).Decode(&b)
	other.Do(http.MethodPost, "/units", validUnit()).Expect(http.StatusCreated)

	var list struct {
		Units []unit `json:"units"`
	}
	fleet.Do(http.MethodGet, "/units", nil).Expect(http.StatusOK).Decode(&list)
	if len(list.Units) != 2 || list.Units[0].ID != a.ID || list.Units[1].ID != b.ID {
		t.Fatalf("units = %+v, want [%s %s] in creation order", list.Units, a.ID, b.ID)
	}
}

func TestListUnitsIsEmptyForNewRental(t *testing.T) {
	fleet := startFleet(t, testkit.NewRentalID())

	resp := fleet.Do(http.MethodGet, "/units", nil).Expect(http.StatusOK)
	if got := string(resp.Body); got != "{\"units\":[]}\n" {
		t.Errorf("body = %q, want an empty units array", got)
	}
}

func TestCreateUnitRejectsInvalidFields(t *testing.T) {
	fleet := startFleet(t, testkit.NewRentalID())

	cases := []struct {
		name  string
		field string
		value any
	}{
		{"missing model", "model", "  "},
		{"malformed plate", "plate_number", "12-ABC"},
		{"year too old", "year", 1985},
		{"unknown transmission", "transmission", "cvt"},
		{"zero daily price", "daily_price", 0},
		{"hourly above daily", "hourly_price", 90000},
		{"negative deposit", "security_deposit", -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := validUnit()
			body[tc.field] = tc.value

			var e testkit.ErrorBody
			fleet.Do(http.MethodPost, "/units", body).Expect(http.StatusBadRequest).Decode(&e)
			if e.Error.Code != "validation_failed" {
				t.Errorf("code = %q, want validation_failed", e.Error.Code)
			}
			if _, ok := e.Error.Fields[tc.field]; !ok || len(e.Error.Fields) != 1 {
				t.Errorf("fields = %v, want only %q", e.Error.Fields, tc.field)
			}
		})
	}
}

func TestCreateUnitRequiresEveryField(t *testing.T) {
	fleet := startFleet(t, testkit.NewRentalID())

	for field := range validUnit() {
		t.Run(field, func(t *testing.T) {
			body := validUnit()
			delete(body, field)

			var e testkit.ErrorBody
			fleet.Do(http.MethodPost, "/units", body).Expect(http.StatusBadRequest).Decode(&e)
			if e.Error.Fields[field] != "is required" || len(e.Error.Fields) != 1 {
				t.Errorf("fields = %v, want only %q required", e.Error.Fields, field)
			}
		})
	}
}

func TestCreateUnitRejectsMalformedBody(t *testing.T) {
	fleet := startFleet(t, testkit.NewRentalID())

	for name, tc := range map[string]struct{ body, message string }{
		"empty":         {"", "body is empty"},
		"not json":      {"{", "body is not valid JSON"},
		"unknown field": {`{"model":"Honda Beat","colour":"red"}`, `unknown field "colour"`},
		"two objects":   {`{} {}`, "body must contain a single JSON object"},
		"wrong type":    {`{"year":"2023"}`, `field "year" must be a number`},
		"not an object": {`[]`, "body must be a JSON object"},
	} {
		t.Run(name, func(t *testing.T) {
			var e testkit.ErrorBody
			fleet.Do(http.MethodPost, "/units", tc.body).Expect(http.StatusBadRequest).Decode(&e)
			if e.Error.Code != "invalid_body" || e.Error.Message != tc.message {
				t.Errorf("error = %+v, want invalid_body %q", e.Error, tc.message)
			}
			if strings.Contains(e.Error.Message, "Go struct") || strings.Contains(e.Error.Message, "json:") {
				t.Errorf("message leaks decoder internals: %q", e.Error.Message)
			}
		})
	}
}

func TestCreateUnitRejectsDuplicatePlate(t *testing.T) {
	fleet := startFleet(t, testkit.NewRentalID())
	fleet.Do(http.MethodPost, "/units", validUnit()).Expect(http.StatusCreated)

	same := validUnit()
	same["plate_number"] = "AB1234XY"
	var e testkit.ErrorBody
	fleet.Do(http.MethodPost, "/units", same).Expect(http.StatusConflict).Decode(&e)
	if e.Error.Code != "duplicate_plate" {
		t.Errorf("code = %q, want duplicate_plate", e.Error.Code)
	}

	var list struct {
		Units []unit `json:"units"`
	}
	fleet.Do(http.MethodGet, "/units", nil).Expect(http.StatusOK).Decode(&list)
	if len(list.Units) != 1 {
		t.Errorf("units after duplicate = %d, want 1", len(list.Units))
	}
}

func TestGetUnknownUnitReturnsNotFound(t *testing.T) {
	fleet := startFleet(t, testkit.NewRentalID())

	var e testkit.ErrorBody
	fleet.Do(http.MethodGet, "/units/does-not-exist", nil).Expect(http.StatusNotFound).Decode(&e)
	if e.Error.Code != "not_found" {
		t.Errorf("code = %q, want not_found", e.Error.Code)
	}
}

func TestUnitsOfAnotherRentalAreNotVisible(t *testing.T) {
	owner := startFleet(t, testkit.NewRentalID())
	stranger := startFleet(t, testkit.NewRentalID())

	var u unit
	owner.Do(http.MethodPost, "/units", validUnit()).Expect(http.StatusCreated).Decode(&u)
	stranger.Do(http.MethodGet, "/units/"+u.ID, nil).Expect(http.StatusNotFound)
}

func TestUnitEndpointsRequireAValidToken(t *testing.T) {
	fleet := serveFleet(t, testkit.UserPool(t))

	for _, token := range []string{"", "not-a-jwt", "eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJ4In0.c2ln"} {
		for _, req := range []struct{ method, path string }{
			{http.MethodGet, "/units"},
			{http.MethodGet, "/units/any"},
			{http.MethodPost, "/units"},
		} {
			var e testkit.ErrorBody
			fleet.As(token).Do(req.method, req.path, validUnit()).Expect(http.StatusUnauthorized).Decode(&e)
			if e.Error.Code != "unauthenticated" {
				t.Errorf("%s %s with %q: code = %q, want unauthenticated", req.method, req.path, token, e.Error.Code)
			}
		}
	}
}

func TestOnlyOwnersCreateUnits(t *testing.T) {
	pool := testkit.UserPool(t)
	fleet := serveFleet(t, pool)
	rentalID := testkit.NewRentalID()

	for _, role := range []auth.Role{auth.Staff, auth.Renter} {
		t.Run(string(role), func(t *testing.T) {
			var e testkit.ErrorBody
			fleet.As(pool.NewUser(rentalID, role).IDToken).
				Do(http.MethodPost, "/units", validUnit()).Expect(http.StatusForbidden).Decode(&e)
			if e.Error.Code != "forbidden" {
				t.Errorf("code = %q, want forbidden", e.Error.Code)
			}
		})
	}
}

func TestEveryRoleReadsTheRentalsUnits(t *testing.T) {
	pool := testkit.UserPool(t)
	fleet := serveFleet(t, pool)
	rentalID := testkit.NewRentalID()

	var created unit
	fleet.As(pool.NewUser(rentalID, auth.Owner).IDToken).
		Do(http.MethodPost, "/units", validUnit()).Expect(http.StatusCreated).Decode(&created)

	for _, role := range []auth.Role{auth.Staff, auth.Renter} {
		t.Run(string(role), func(t *testing.T) {
			reader := fleet.As(pool.NewUser(rentalID, role).IDToken)
			var list struct {
				Units []unit `json:"units"`
			}
			reader.Do(http.MethodGet, "/units", nil).Expect(http.StatusOK).Decode(&list)
			if len(list.Units) != 1 || list.Units[0].ID != created.ID {
				t.Errorf("units = %+v, want only %s", list.Units, created.ID)
			}
			reader.Do(http.MethodGet, "/units/"+created.ID, nil).Expect(http.StatusOK)
		})
	}
}

func TestAccountWithoutRoleIsForbidden(t *testing.T) {
	pool := testkit.UserPool(t)
	fleet := serveFleet(t, pool)

	fleet.As(pool.NewUser(testkit.NewRentalID(), "").IDToken).
		Do(http.MethodGet, "/units", nil).Expect(http.StatusForbidden)
}
