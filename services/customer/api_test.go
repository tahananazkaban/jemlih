package customer_test

import (
	"bytes"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tahananazkaban/jemlih/pkg/auth"
	"github.com/tahananazkaban/jemlih/pkg/env"
	"github.com/tahananazkaban/jemlih/pkg/testkit"
	"github.com/tahananazkaban/jemlih/services/customer/internal/app"
)

type document struct {
	Type        string    `json:"type"`
	ContentType string    `json:"content_type"`
	Size        int64     `json:"size"`
	UploadedAt  time.Time `json:"uploaded_at"`
	URL         string    `json:"url"`
	ExpiresAt   time.Time `json:"expires_at"`
}

type renter struct {
	ID               string     `json:"id"`
	Email            string     `json:"email"`
	FullName         string     `json:"full_name"`
	PhoneNumber      string     `json:"phone_number"`
	Status           string     `json:"status"`
	Documents        []document `json:"documents"`
	MissingDocuments []string   `json:"missing_documents"`
	Rejection        *struct {
		Reason    string   `json:"reason"`
		Documents []string `json:"documents"`
	} `json:"rejection"`
	VerifiedAt *time.Time `json:"verified_at"`
	Version    int64      `json:"version"`
}

type upload struct {
	UploadID  string            `json:"upload_id"`
	Method    string            `json:"method"`
	URL       string            `json:"url"`
	Headers   map[string]string `json:"headers"`
	ExpiresAt time.Time         `json:"expires_at"`
	MaxSize   int64             `json:"max_size"`
}

var allDocuments = []string{"id_card", "driving_licence", "selfie"}

// rental is one fresh Rental with back-office users and a customer service started for it.
type rental struct {
	t        *testing.T
	id       string
	pool     *testkit.Pool
	customer *testkit.Client
	owner    string
	staff    string
}

func newRental(t *testing.T, readTTL time.Duration) *rental {
	t.Helper()
	pool := testkit.UserPool(t)
	handler, err := app.New(app.Config{
		AWS:            testkit.AWSConfig(t),
		Auth:           pool.Auth,
		CustomerTable:  env.String("CUSTOMER_TABLE", "jemlih-customer"),
		DocumentBucket: env.String("DOCUMENT_BUCKET", "jemlih-customer-documents"),
		ReadTTL:        readTTL,
	})
	if err != nil {
		t.Fatalf("start customer: %v", err)
	}
	id := testkit.NewRentalID()
	return &rental{
		t: t, id: id, pool: pool, customer: testkit.Serve(t, handler),
		owner: pool.NewUser(id, auth.Owner).IDToken, staff: pool.NewUser(id, auth.Staff).IDToken,
	}
}

func (r *rental) as(token string) *testkit.Client { return r.customer.As(token) }

func (r *rental) newRenter() string {
	r.t.Helper()
	return r.pool.NewUser(r.id, auth.Renter).IDToken
}

func (r *rental) me(token string) renter {
	r.t.Helper()
	var got renter
	r.as(token).Do(http.MethodGet, "/renters/me", nil).Expect(http.StatusOK).Decode(&got)
	return got
}

// upload sends content straight to the bucket through a presigned URL and returns the upload ID.
func (r *rental) upload(token, docType, contentType string, content []byte) string {
	r.t.Helper()
	u := r.startUpload(token, docType, contentType, len(content))
	if status := testkit.PutFile(r.t, u.URL, u.Headers, content); status != http.StatusOK {
		r.t.Fatalf("upload to bucket: status %d", status)
	}
	return u.UploadID
}

func (r *rental) startUpload(token, docType, contentType string, size int) upload {
	r.t.Helper()
	var u upload
	r.as(token).Do(http.MethodPost, "/renters/me/documents/"+docType+"/uploads", map[string]any{"content_type": contentType, "size": size}).
		Expect(http.StatusCreated).Decode(&u)
	return u
}

func (r *rental) approve(token, id string, version int64) *testkit.Response {
	r.t.Helper()
	return r.as(token).Do(http.MethodPost, "/renters/"+id+"/approve", map[string]int64{"version": version})
}

func (r *rental) reject(token, id string, version int64, reason string, documents ...string) *testkit.Response {
	r.t.Helper()
	return r.as(token).Do(http.MethodPost, "/renters/"+id+"/reject", map[string]any{"version": version, "reason": reason, "documents": documents})
}

func (r *rental) attach(token, docType, uploadID string) *testkit.Response {
	r.t.Helper()
	return r.as(token).Do(http.MethodPut, "/renters/me/documents/"+docType, map[string]string{"upload_id": uploadID})
}

func (r *rental) uploadDocument(token, docType string, content []byte) renter {
	r.t.Helper()
	var got renter
	r.attach(token, docType, r.upload(token, docType, "image/jpeg", content)).Expect(http.StatusOK).Decode(&got)
	return got
}

func (r *rental) saveProfile(token, name, phone string) *testkit.Response {
	r.t.Helper()
	return r.as(token).Do(http.MethodPut, "/renters/me/profile", map[string]string{"full_name": name, "phone_number": phone})
}

// pendingRenter is a Renter who saved a profile and every document, so they wait for review.
func (r *rental) pendingRenter() (token string, me renter) {
	r.t.Helper()
	token = r.newRenter()
	r.saveProfile(token, "Wayan Sudarsana", "081234567890").Expect(http.StatusOK)
	for _, d := range allDocuments {
		me = r.uploadDocument(token, d, photo(d))
	}
	if me.Status != "pending" {
		r.t.Fatalf("complete renter status = %s, want pending", me.Status)
	}
	return token, me
}

func photo(label string) []byte { return []byte("\xff\xd8\xff fake jpeg of " + label) }

func TestNewRenterStartsUnverified(t *testing.T) {
	r := newRental(t, 0)
	token := r.newRenter()

	got := r.me(token)
	if got.ID == "" || !strings.HasSuffix(got.Email, "@example.com") || got.Status != "unverified" {
		t.Errorf("me = %+v", got)
	}
	if len(got.Documents) != 0 || !slices.Equal(got.MissingDocuments, allDocuments) {
		t.Errorf("documents = %v, missing = %v", got.Documents, got.MissingDocuments)
	}
}

func TestProfileIsNormalisedAndValidated(t *testing.T) {
	r := newRental(t, 0)
	token := r.newRenter()

	var got renter
	r.saveProfile(token, "  Made   Wirawan ", "0812-3456 7890").Expect(http.StatusOK).Decode(&got)
	if got.FullName != "Made Wirawan" || got.PhoneNumber != "+6281234567890" || got.Status != "unverified" {
		t.Errorf("profile = %q %q %s", got.FullName, got.PhoneNumber, got.Status)
	}
	r.saveProfile(token, "Anna Müller", "+49 151 2345 6789").Expect(http.StatusOK).Decode(&got)
	if got.PhoneNumber != "+4915123456789" || r.me(token).FullName != "Anna Müller" {
		t.Errorf("foreign profile = %+v", got)
	}

	for name, tc := range map[string]struct {
		body  any
		field string
	}{
		"short name":      {map[string]string{"full_name": "A", "phone_number": "081234567890"}, "full_name"},
		"long name":       {map[string]string{"full_name": strings.Repeat("a", 101), "phone_number": "081234567890"}, "full_name"},
		"letters":         {map[string]string{"full_name": "Made", "phone_number": "0812-call-me"}, "phone_number"},
		"no country code": {map[string]string{"full_name": "Made", "phone_number": "81234567890"}, "phone_number"},
		"too short":       {map[string]string{"full_name": "Made", "phone_number": "0812"}, "phone_number"},
		"missing phone":   {map[string]string{"full_name": "Made"}, "phone_number"},
	} {
		t.Run(name, func(t *testing.T) {
			var e testkit.ErrorBody
			r.as(token).Do(http.MethodPut, "/renters/me/profile", tc.body).Expect(http.StatusBadRequest).Decode(&e)
			if e.Error.Code != "validation_failed" || e.Error.Fields[tc.field] == "" {
				t.Errorf("error = %+v, want a %s field error", e.Error, tc.field)
			}
		})
	}
}

func TestDocumentsUploadStraightToTheBucket(t *testing.T) {
	r := newRental(t, 0)
	token := r.newRenter()

	u := r.startUpload(token, "id_card", "image/png", len(photo("id card")))
	bucket := env.String("DOCUMENT_BUCKET", "jemlih-customer-documents")
	if parsed, err := url.Parse(u.URL); err != nil || !strings.HasPrefix(parsed.Path, "/"+bucket+"/") || parsed.Query().Get("X-Amz-Signature") == "" {
		t.Errorf("upload URL %s is not a presigned URL into %s", u.URL, bucket)
	}
	if signed := strings.Split(mustQuery(t, u.URL).Get("X-Amz-SignedHeaders"), ";"); !slices.Contains(signed, "content-length") || !slices.Contains(signed, "content-type") {
		t.Errorf("signed headers = %v, want the size and type pinned", signed)
	}
	if u.Method != http.MethodPut || u.Headers["Content-Type"] != "image/png" || u.MaxSize != 10<<20 {
		t.Errorf("upload = %+v", u)
	}
	if left := time.Until(u.ExpiresAt); left < 14*time.Minute || left > 16*time.Minute {
		t.Errorf("upload URL expires in %s, want 15m", left)
	}
	if status := testkit.PutFile(t, u.URL, u.Headers, photo("id card")); status != http.StatusOK {
		t.Fatalf("upload status = %d", status)
	}

	var got renter
	r.attach(token, "id_card", u.UploadID).Expect(http.StatusOK).Decode(&got)
	if len(got.Documents) != 1 || got.Documents[0].Type != "id_card" || got.Documents[0].ContentType != "image/png" ||
		got.Documents[0].Size != int64(len(photo("id card"))) || got.Documents[0].URL != "" {
		t.Errorf("documents = %+v", got.Documents)
	}
	if !slices.Equal(got.MissingDocuments, []string{"driving_licence", "selfie"}) || got.Status != "unverified" {
		t.Errorf("missing = %v, status = %s", got.MissingDocuments, got.Status)
	}
	r.attach(token, "id_card", u.UploadID).Expect(http.StatusBadRequest)
}

func mustQuery(t *testing.T, raw string) url.Values {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return parsed.Query()
}

func TestUploadsAreChecked(t *testing.T) {
	r := newRental(t, 0)
	token := r.newRenter()

	expectError := func(t *testing.T, resp *testkit.Response, status int, code string) {
		t.Helper()
		var e testkit.ErrorBody
		resp.Expect(status).Decode(&e)
		if e.Error.Code != code {
			t.Errorf("code = %s, want %s", e.Error.Code, code)
		}
	}

	t.Run("unsupported content type", func(t *testing.T) {
		expectError(t, r.as(token).Do(http.MethodPost, "/renters/me/documents/selfie/uploads", map[string]any{"content_type": "application/pdf", "size": 100}), http.StatusBadRequest, "validation_failed")
	})
	t.Run("announced too large", func(t *testing.T) {
		expectError(t, r.as(token).Do(http.MethodPost, "/renters/me/documents/selfie/uploads", map[string]any{"content_type": "image/jpeg", "size": 10<<20 + 1}), http.StatusBadRequest, "validation_failed")
		expectError(t, r.as(token).Do(http.MethodPost, "/renters/me/documents/selfie/uploads", map[string]any{"content_type": "image/jpeg", "size": 0}), http.StatusBadRequest, "validation_failed")
	})
	t.Run("unknown document type", func(t *testing.T) {
		expectError(t, r.as(token).Do(http.MethodPost, "/renters/me/documents/passport/uploads", map[string]any{"content_type": "image/jpeg", "size": 100}), http.StatusNotFound, "unknown_document_type")
	})
	t.Run("not an upload ID", func(t *testing.T) {
		expectError(t, r.attach(token, "selfie", "../../other-renter/selfie"), http.StatusBadRequest, "validation_failed")
	})
	t.Run("never uploaded", func(t *testing.T) {
		u := r.startUpload(token, "selfie", "image/jpeg", 100)
		expectError(t, r.attach(token, "selfie", u.UploadID), http.StatusBadRequest, "upload_not_found")
	})
	t.Run("uploaded for another type", func(t *testing.T) {
		id := r.upload(token, "selfie", "image/jpeg", photo("selfie"))
		expectError(t, r.attach(token, "id_card", id), http.StatusBadRequest, "upload_not_found")
	})
	t.Run("someone else's upload", func(t *testing.T) {
		id := r.upload(r.newRenter(), "selfie", "image/jpeg", photo("their selfie"))
		expectError(t, r.attach(token, "selfie", id), http.StatusBadRequest, "upload_not_found")
	})
	t.Run("larger than announced", func(t *testing.T) {
		// Real S3 refuses a body longer than the signed size; the emulator does not, so attaching checks again.
		u := r.startUpload(token, "selfie", "image/jpeg", 100)
		testkit.PutFile(t, u.URL, u.Headers, bytes.Repeat([]byte{0xff}, 10<<20+1))
		expectError(t, r.attach(token, "selfie", u.UploadID), http.StatusBadRequest, "file_too_large")
	})
	t.Run("stored as another content type", func(t *testing.T) {
		// Real S3 refuses a Content-Type the URL was not signed for; the emulator does not, so attaching checks again.
		u := r.startUpload(token, "selfie", "image/jpeg", 8)
		testkit.PutFile(t, u.URL, map[string]string{"Content-Type": "text/html"}, []byte("<script>"))
		expectError(t, r.attach(token, "selfie", u.UploadID), http.StatusBadRequest, "unsupported_file_type")
	})
	t.Run("missing fields", func(t *testing.T) {
		expectError(t, r.as(token).Do(http.MethodPost, "/renters/me/documents/selfie/uploads", map[string]string{}), http.StatusBadRequest, "validation_failed")
		expectError(t, r.as(token).Do(http.MethodPut, "/renters/me/documents/selfie", map[string]string{}), http.StatusBadRequest, "validation_failed")
	})

	if got := r.me(token); len(got.Documents) != 0 {
		t.Errorf("rejected uploads were attached: %+v", got.Documents)
	}
}

func TestCompleteRenterWaitsForReview(t *testing.T) {
	r := newRental(t, 0)
	token := r.newRenter()

	for _, d := range allDocuments {
		if got := r.uploadDocument(token, d, photo(d)); got.Status != "unverified" {
			t.Fatalf("status without a profile = %s", got.Status)
		}
	}
	var got renter
	r.saveProfile(token, "Wayan Sudarsana", "081234567890").Expect(http.StatusOK).Decode(&got)
	if got.Status != "pending" || len(got.MissingDocuments) != 0 {
		t.Fatalf("renter = %+v, want pending", got)
	}
	r.newRenter() // signed up but never saved anything, so not listed
	waiting, _ := r.pendingRenter()

	for _, token := range []string{r.owner, r.staff} {
		var list struct{ Renters []renter }
		r.as(token).Do(http.MethodGet, "/renters?status=pending", nil).Expect(http.StatusOK).Decode(&list)
		if len(list.Renters) != 2 || list.Renters[0].ID != got.ID || list.Renters[1].ID != r.me(waiting).ID {
			t.Errorf("pending renters = %+v, want the earlier one first", list.Renters)
		}
		r.as(token).Do(http.MethodGet, "/renters?status=verified", nil).Expect(http.StatusOK).Decode(&list)
		if len(list.Renters) != 0 {
			t.Errorf("verified renters = %+v", list.Renters)
		}
	}
	r.as(r.owner).Do(http.MethodGet, "/renters?status=approved", nil).Expect(http.StatusBadRequest)
}

func TestOnlyOwnersReadDocuments(t *testing.T) {
	r := newRental(t, 0)
	token, me := r.pendingRenter()

	var links struct{ Documents []document }
	r.as(r.owner).Do(http.MethodGet, "/renters/"+me.ID+"/documents", nil).Expect(http.StatusOK).Decode(&links)
	if len(links.Documents) != 3 {
		t.Fatalf("documents = %+v", links.Documents)
	}
	for i, d := range links.Documents {
		if d.Type != allDocuments[i] {
			t.Errorf("document %d is %s, want %s", i, d.Type, allDocuments[i])
		}
		if left := time.Until(d.ExpiresAt); left > 5*time.Minute+time.Second || left < 4*time.Minute {
			t.Errorf("%s link expires in %s, want 5m", d.Type, left)
		}
		status, body := testkit.GetFile(t, d.URL)
		if status != http.StatusOK || !bytes.Equal(body, photo(d.Type)) {
			t.Errorf("%s download = %d %q", d.Type, status, body)
		}
	}

	var links2 struct{ Version int64 }
	r.as(r.owner).Do(http.MethodGet, "/renters/"+me.ID+"/documents", nil).Decode(&links2)
	if links2.Version != me.Version {
		t.Errorf("links version = %d, want %d", links2.Version, me.Version)
	}
	r.as(r.staff).Do(http.MethodGet, "/renters/"+me.ID+"/documents", nil).Expect(http.StatusForbidden)
	r.as(token).Do(http.MethodGet, "/renters/"+me.ID+"/documents", nil).Expect(http.StatusForbidden)
	r.as(token).Do(http.MethodGet, "/renters/"+me.ID, nil).Expect(http.StatusForbidden)

	// Staff see whether a renter is verified, without any way to open the documents.
	resp := r.as(r.staff).Do(http.MethodGet, "/renters/"+me.ID, nil).Expect(http.StatusOK)
	if bytes.Contains(resp.Body, []byte("X-Amz-")) || bytes.Contains(resp.Body, []byte("documents/")) {
		t.Errorf("renter view leaks document locations: %s", resp.Body)
	}
}

func TestDocumentLinksExpireQuickly(t *testing.T) {
	r := newRental(t, 2*time.Second)
	_, me := r.pendingRenter()

	var links struct{ Documents []document }
	r.as(r.owner).Do(http.MethodGet, "/renters/"+me.ID+"/documents", nil).Expect(http.StatusOK).Decode(&links)
	link := links.Documents[0].URL
	if status, _ := testkit.GetFile(t, link); status != http.StatusOK {
		t.Fatalf("fresh link status = %d", status)
	}
	time.Sleep(3 * time.Second)
	if status, _ := testkit.GetFile(t, link); status != http.StatusForbidden {
		t.Errorf("expired link status = %d, want 403", status)
	}
}

func TestApprovedRenterStaysVerified(t *testing.T) {
	r := newRental(t, 0)
	token, me := r.pendingRenter()

	var got renter
	r.approve(r.owner, me.ID, me.Version).Expect(http.StatusOK).Decode(&got)
	if got.Status != "verified" || got.VerifiedAt == nil {
		t.Fatalf("approved renter = %+v", got)
	}
	if mine := r.me(token); mine.Status != "verified" {
		t.Errorf("renter sees status %s", mine.Status)
	}

	var e testkit.ErrorBody
	r.as(token).Do(http.MethodPost, "/renters/me/documents/selfie/uploads", map[string]any{"content_type": "image/jpeg", "size": 100}).Expect(http.StatusConflict).Decode(&e)
	if e.Error.Code != "already_verified" {
		t.Errorf("new upload code = %s", e.Error.Code)
	}
	r.saveProfile(token, "Someone Else", "081234567890").Expect(http.StatusConflict).Decode(&e)
	if e.Error.Code != "name_locked" {
		t.Errorf("name change code = %s", e.Error.Code)
	}
	r.saveProfile(token, "Wayan Sudarsana", "+6285700001111").Expect(http.StatusOK).Decode(&got)
	if got.Status != "verified" || got.PhoneNumber != "+6285700001111" {
		t.Errorf("after phone change = %+v", got)
	}
	r.approve(r.owner, me.ID, got.Version).Expect(http.StatusConflict).Decode(&e)
	if e.Error.Code != "not_pending" {
		t.Errorf("second approval code = %s", e.Error.Code)
	}
}

func TestRejectedRenterUploadsAgain(t *testing.T) {
	r := newRental(t, 0)
	token, me := r.pendingRenter()

	var got renter
	r.reject(r.owner, me.ID, me.Version, "Selfie is blurry", "selfie").Expect(http.StatusOK).Decode(&got)
	if got.Status != "rejected" || got.Rejection == nil || got.Rejection.Reason != "Selfie is blurry" || !slices.Equal(got.Rejection.Documents, []string{"selfie"}) {
		t.Fatalf("rejected renter = %+v", got)
	}
	if mine := r.me(token); mine.Rejection == nil || mine.Rejection.Reason != "Selfie is blurry" {
		t.Errorf("renter sees %+v", mine)
	}

	r.saveProfile(token, "Wayan Sudarsana", "081234567890").Expect(http.StatusOK).Decode(&got)
	if got.Status != "rejected" {
		t.Errorf("status after a profile save = %s, want rejected until the selfie is replaced", got.Status)
	}
	sharper := []byte("\xff\xd8\xff a much sharper selfie")
	if got = r.uploadDocument(token, "selfie", sharper); got.Status != "pending" || got.Rejection != nil {
		t.Errorf("after re-upload = %+v, want pending", got)
	}

	var links struct{ Documents []document }
	r.as(r.owner).Do(http.MethodGet, "/renters/"+me.ID+"/documents", nil).Expect(http.StatusOK).Decode(&links)
	if _, body := testkit.GetFile(t, links.Documents[2].URL); !bytes.Equal(body, sharper) {
		t.Errorf("owner sees selfie %q, want the new one", body)
	}
	r.approve(r.owner, me.ID, got.Version).Expect(http.StatusOK)
}

func TestOwnersOnlyApproveWhatTheyReviewed(t *testing.T) {
	r := newRental(t, 0)
	token, me := r.pendingRenter()

	var links struct{ Version int64 }
	r.as(r.owner).Do(http.MethodGet, "/renters/"+me.ID+"/documents", nil).Expect(http.StatusOK).Decode(&links)
	replaced := r.uploadDocument(token, "selfie", []byte("\xff\xd8\xff a selfie the owner never opened"))

	for name, resp := range map[string]*testkit.Response{
		"approve": r.approve(r.owner, me.ID, links.Version),
		"reject":  r.reject(r.owner, me.ID, links.Version, "Blurry", "selfie"),
	} {
		var e testkit.ErrorBody
		resp.Expect(http.StatusConflict).Decode(&e)
		if e.Error.Code != "renter_changed" {
			t.Errorf("%s with a stale version: code = %s", name, e.Error.Code)
		}
	}
	if got := r.me(token); got.Status != "pending" {
		t.Errorf("status = %s, want still pending", got.Status)
	}
	r.approve(r.owner, me.ID, replaced.Version).Expect(http.StatusOK)
}

func TestRejectionWithoutDocumentsWaitsForAProfileFix(t *testing.T) {
	r := newRental(t, 0)
	token, me := r.pendingRenter()

	r.reject(r.owner, me.ID, me.Version, "Name does not match the ID card").Expect(http.StatusOK)
	var got renter
	r.saveProfile(token, "Wayan Sudarsana", "081234567890").Expect(http.StatusOK).Decode(&got)
	if got.Status != "rejected" {
		t.Errorf("status after saving an unchanged profile = %s, want rejected", got.Status)
	}
	r.saveProfile(token, "I Wayan Sudarsana", "081234567890").Expect(http.StatusOK).Decode(&got)
	if got.Status != "pending" {
		t.Errorf("status after fixing the name = %s, want pending", got.Status)
	}
}

func TestReviewNeedsAPendingRenterAndAReason(t *testing.T) {
	r := newRental(t, 0)
	_, pending := r.pendingRenter()
	unfinished := r.newRenter()
	r.saveProfile(unfinished, "Ketut Arya", "081234567890").Expect(http.StatusOK)
	unfinishedID := r.me(unfinished).ID

	r.approve(r.owner, unfinishedID, r.me(unfinished).Version).Expect(http.StatusConflict)
	r.reject(r.owner, unfinishedID, r.me(unfinished).Version, "Incomplete").Expect(http.StatusConflict)

	v := pending.Version
	for name, body := range map[string]any{
		"no reason":      map[string]any{"version": v, "documents": []string{"selfie"}},
		"no version":     map[string]any{"reason": "Blurry"},
		"blank reason":   map[string]any{"version": v, "reason": "   "},
		"long reason":    map[string]any{"version": v, "reason": strings.Repeat("x", 501)},
		"unknown type":   map[string]any{"version": v, "reason": "Blurry", "documents": []string{"passport"}},
		"documents type": fmt.Sprintf(`{"version":%d,"reason":"Blurry","documents":"selfie"}`, v),
	} {
		t.Run(name, func(t *testing.T) {
			r.as(r.owner).Do(http.MethodPost, "/renters/"+pending.ID+"/reject", body).Expect(http.StatusBadRequest)
		})
	}
	var got renter
	r.as(r.owner).Do(http.MethodGet, "/renters/"+pending.ID, nil).Expect(http.StatusOK).Decode(&got)
	if got.Status != "pending" {
		t.Errorf("status after invalid rejections = %s", got.Status)
	}
}

func TestRolesAndRentals(t *testing.T) {
	r := newRental(t, 0)
	_, me := r.pendingRenter()
	renterToken := r.newRenter()
	other := newRental(t, 0)

	for name, tc := range map[string]struct {
		token, method, path string
		status              int
	}{
		"owner has no renter record":   {r.owner, http.MethodGet, "/renters/me", http.StatusForbidden},
		"staff has no renter record":   {r.staff, http.MethodGet, "/renters/me", http.StatusForbidden},
		"renters cannot list renters":  {renterToken, http.MethodGet, "/renters", http.StatusForbidden},
		"staff cannot approve":         {r.staff, http.MethodPost, "/renters/" + me.ID + "/approve", http.StatusForbidden},
		"renters cannot approve":       {renterToken, http.MethodPost, "/renters/" + me.ID + "/approve", http.StatusForbidden},
		"anonymous":                    {"", http.MethodGet, "/renters/me", http.StatusUnauthorized},
		"other rental's renter":        {other.owner, http.MethodGet, "/renters/" + me.ID, http.StatusNotFound},
		"other rental's documents":     {other.owner, http.MethodGet, "/renters/" + me.ID + "/documents", http.StatusNotFound},
		"other rental's approval":      {other.owner, http.MethodPost, "/renters/" + me.ID + "/approve", http.StatusNotFound},
		"unknown renter":               {r.owner, http.MethodGet, "/renters/" + fmt.Sprint(time.Now().UnixNano()), http.StatusNotFound},
		"other rental lists no renter": {other.owner, http.MethodGet, "/renters", http.StatusOK},
	} {
		t.Run(name, func(t *testing.T) {
			resp := r.as(tc.token).Do(tc.method, tc.path, map[string]int64{"version": me.Version}).Expect(tc.status)
			if tc.status == http.StatusOK && !bytes.Equal(bytes.TrimSpace(resp.Body), []byte(`{"renters":[]}`)) {
				t.Errorf("body = %s", resp.Body)
			}
		})
	}
}
