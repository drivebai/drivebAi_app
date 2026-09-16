package handlers

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/drivebai/backend/internal/httputil"
	"github.com/drivebai/backend/internal/models"
)

// The regression this pins: SignBOS refused BOTH signatures with
// ODOMETER_REQUIRED unless odometer_reading and odometer_accuracy were set,
// and the only client that sends them shipped in a build that was never
// distributed. Every seller on every existing app was locked out of the sale
// flow, with no admin workaround. Signing must work from a client that knows
// nothing about the odometer, and a client that DOES know must be able to
// declare it in the same request.

func tinyPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for x := 0; x < 4; x++ {
		for y := 0; y < 4; y++ {
			img.Set(x, y, color.RGBA{0, 0, 0, 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

// signReq builds the multipart sign request. extra carries the optional
// odometer fields; a nil map is the OLD client's wire shape exactly.
func signReq(t *testing.T, userID, purchaseID uuid.UUID, role string, extra map[string]string) *http.Request {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("role", role)
	for k, v := range extra {
		_ = mw.WriteField(k, v)
	}
	fw, err := mw.CreateFormFile("file", "signature.png")
	if err != nil {
		t.Fatalf("form file: %v", err)
	}
	if _, err := fw.Write(tinyPNG(t)); err != nil {
		t.Fatalf("write png: %v", err)
	}
	mw.Close()

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", purchaseID.String())
	req := httptest.NewRequest(http.MethodPost, "/api/v1/purchase-requests/"+purchaseID.String()+"/bos/sign", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	ctx := context.WithValue(req.Context(), httputil.UserIDKey, userID)
	ctx = context.WithValue(ctx, chi.RouteCtxKey, rctx)
	return req.WithContext(ctx)
}

// seedSignableSale gets a purchase to the point where the Bill of Sale can be
// signed: accepted, both addresses filled, no odometer declared.
func (e *salesGateEnv) seedSignableSale(t *testing.T, tag string) (purchaseID, seller, buyer uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	run := uuid.NewString()[:8]
	seller = e.seedUser(t, "car_owner", "bos_s_"+tag+run+"@example.com")
	buyer = e.seedUser(t, "driver", "bos_b_"+tag+run+"@example.com")
	e.seedLicense(t, buyer)
	car := e.seedCar(t, seller, "available", true, false)
	e.listForSale(t, car, 9000)
	purchaseID = e.seedPurchase(t, car, seller, buyer, "bos_pending_seller", 6*24*time.Hour)
	if _, err := e.db.Pool.Exec(ctx, `
		INSERT INTO purchase_bill_of_sales (purchase_request_id, vehicle_year, vehicle_make, vehicle_model, vin,
			sale_amount_cents, currency, terms_conditions, seller_name, seller_address, buyer_name, buyer_address,
			title_condition)
		VALUES ($1, 2021, 'Honda', 'CR-V', 'VIN`+run+`', 900000, 'USD', 'as-is', 'Seller Name', '1 Seller St',
		        'Buyer Name', '2 Buyer Ave', 'clean')`, purchaseID); err != nil {
		t.Fatalf("seed bos: %v", err)
	}
	t.Cleanup(func() {
		e.db.Pool.Exec(ctx, `DELETE FROM purchase_bill_of_sales WHERE purchase_request_id = $1`, purchaseID)
	})
	return purchaseID, seller, buyer
}

// A client that never heard of the odometer can still sign — both roles.
func TestSignBOSWorksWithoutAnOdometerDeclaration(t *testing.T) {
	e := newSalesGateEnv(t)
	ctx := context.Background()
	pid, seller, buyer := e.seedSignableSale(t, "plain")

	rr := httptest.NewRecorder()
	e.purchaseH.SignBOS(rr, signReq(t, seller, pid, "seller", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("seller signing without an odometer: %d %s — the regression is back", rr.Code, rr.Body.String())
	}
	rr = httptest.NewRecorder()
	e.purchaseH.SignBOS(rr, signReq(t, buyer, pid, "buyer", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("buyer signing without an odometer: %d %s", rr.Code, rr.Body.String())
	}

	bos, err := e.purchaseRepo.GetBillOfSale(ctx, pid)
	if err != nil || bos == nil {
		t.Fatalf("load bos: %v", err)
	}
	if !bos.SellerSigned() || !bos.BuyerSigned() {
		t.Fatalf("both signatures should be recorded: seller=%v buyer=%v", bos.SellerSigned(), bos.BuyerSigned())
	}
	if bos.OdometerDeclared() {
		t.Error("an odometer appeared from nowhere — nothing may invent a legal declaration")
	}
}

// A capable client declares it in the same request as the signature.
func TestSellerCanDeclareTheOdometerWhileSigning(t *testing.T) {
	e := newSalesGateEnv(t)
	ctx := context.Background()
	pid, seller, _ := e.seedSignableSale(t, "declare")

	rr := httptest.NewRecorder()
	e.purchaseH.SignBOS(rr, signReq(t, seller, pid, "seller", map[string]string{
		"odometer_reading":  "84213",
		"odometer_accuracy": string(models.OdometerAccuracyNotActual),
	}))
	if rr.Code != http.StatusOK {
		t.Fatalf("seller signing with an odometer: %d %s", rr.Code, rr.Body.String())
	}
	bos, _ := e.purchaseRepo.GetBillOfSale(ctx, pid)
	if bos == nil || !bos.OdometerDeclared() {
		t.Fatalf("declaration not recorded: %+v", bos)
	}
	if *bos.OdometerReading != 84213 || *bos.OdometerAccuracy != string(models.OdometerAccuracyNotActual) {
		t.Errorf("recorded %v / %v", *bos.OdometerReading, *bos.OdometerAccuracy)
	}
	if bos.OdometerDeclaredAt == nil {
		t.Error("declaration time not stamped")
	}
	if !bos.SellerSigned() {
		t.Error("the signature itself was lost")
	}
}

// A malformed declaration is refused rather than silently dropped — a client
// that tries to declare must not end up signing with nothing recorded.
func TestBadOdometerAtSigningIsRefusedNotIgnored(t *testing.T) {
	e := newSalesGateEnv(t)
	ctx := context.Background()
	pid, seller, _ := e.seedSignableSale(t, "bad")

	for _, bad := range []map[string]string{
		{"odometer_reading": "-5", "odometer_accuracy": "actual"},
		{"odometer_reading": "not-a-number", "odometer_accuracy": "actual"},
		{"odometer_reading": "1000", "odometer_accuracy": "roughly"},
	} {
		rr := httptest.NewRecorder()
		e.purchaseH.SignBOS(rr, signReq(t, seller, pid, "seller", bad))
		if rr.Code != http.StatusBadRequest {
			t.Errorf("%v: %d %s, want 400", bad, rr.Code, rr.Body.String())
		}
		bos, _ := e.purchaseRepo.GetBillOfSale(ctx, pid)
		if bos != nil && bos.SellerSigned() {
			t.Fatalf("%v: the signature was recorded despite a refused declaration", bad)
		}
	}
	_ = fmt.Sprintf
}
