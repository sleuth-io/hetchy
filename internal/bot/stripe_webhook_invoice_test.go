package bot

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/sleuth-io/hetchy/internal/billing"
)

func TestStripeAutoTopupperPurchaseTopupUnit(t *testing.T) {
	var idempotencyKeys []string
	restore := useStripeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		idempotencyKeys = append(idempotencyKeys, r.Header.Get("Idempotency-Key"))
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/invoices":
			if err := r.ParseForm(); err != nil {
				http.Error(w, "parse form: "+err.Error(), http.StatusBadRequest)
				return
			}
			if got := r.Form.Get("metadata[app]"); got != "hetchy" {
				http.Error(w, "invoice metadata app = "+got, http.StatusBadRequest)
				return
			}
			fmt.Fprint(w, `{"id":"in_1","object":"invoice","status":"draft"}`)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/invoiceitems":
			if err := r.ParseForm(); err != nil {
				http.Error(w, "parse form: "+err.Error(), http.StatusBadRequest)
				return
			}
			if got := r.Form.Get("metadata[app]"); got != "hetchy" {
				http.Error(w, "invoice item metadata app = "+got, http.StatusBadRequest)
				return
			}
			fmt.Fprint(w, `{"id":"ii_1","object":"invoiceitem"}`)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/invoices/in_1/finalize":
			fmt.Fprint(w, `{"id":"in_1","object":"invoice","status":"open"}`)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/invoices/in_1/pay":
			fmt.Fprint(w, `{"id":"in_1","object":"invoice","status":"paid"}`)
		default:
			http.Error(w, "unexpected Stripe request "+r.Method+" "+r.URL.Path, http.StatusInternalServerError)
		}
	})
	defer restore()

	topupper := stripeAutoTopupper{
		secretKey: "sk_test",
		topupPriceIDs: map[string]string{
			billing.PlanTeam: "price_topup",
		},
	}
	invoiceID, err := topupper.PurchaseTopupUnit(t.Context(), billing.Account{
		OrgID:            "org_1",
		PlanCode:         billing.PlanTeam,
		StripeCustomerID: "cus_1",
	}, "idem_1")
	if err != nil {
		t.Fatalf("PurchaseTopupUnit returned error: %v", err)
	}
	if invoiceID != "in_1" {
		t.Fatalf("invoiceID = %q, want in_1", invoiceID)
	}
	wantKeys := []string{
		"hetchy-auto-topup-invoice-idem_1",
		"hetchy-auto-topup-item-idem_1",
		"hetchy-auto-topup-finalize-idem_1",
		"hetchy-auto-topup-pay-idem_1",
	}
	if fmt.Sprint(idempotencyKeys) != fmt.Sprint(wantKeys) {
		t.Fatalf("idempotency keys = %v, want %v", idempotencyKeys, wantKeys)
	}
}

func TestStripeAutoTopupperPurchaseTopupUnitUnexpectedPaidStatus(t *testing.T) {
	restore := useStripeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/invoices":
			fmt.Fprint(w, `{"id":"in_1","object":"invoice","status":"draft"}`)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/invoiceitems":
			fmt.Fprint(w, `{"id":"ii_1","object":"invoiceitem"}`)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/invoices/in_1/finalize":
			fmt.Fprint(w, `{"id":"in_1","object":"invoice","status":"open"}`)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/invoices/in_1/pay":
			fmt.Fprint(w, `{"id":"in_1","object":"invoice","status":"open"}`)
		default:
			http.Error(w, "unexpected Stripe request "+r.Method+" "+r.URL.Path, http.StatusInternalServerError)
		}
	})
	defer restore()

	topupper := stripeAutoTopupper{secretKey: "sk_test", topupPriceID: "price_topup"}
	if _, err := topupper.PurchaseTopupUnit(t.Context(), billing.Account{
		OrgID:            "org_1",
		PlanCode:         billing.PlanTeam,
		StripeCustomerID: "cus_1",
	}, "idem_1"); err == nil {
		t.Fatal("PurchaseTopupUnit returned nil error for unpaid invoice")
	}
}

func TestStripeAutoTopupperPurchaseTopupUnitAlreadyPaid(t *testing.T) {
	restore := useStripeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/invoices":
			fmt.Fprint(w, `{"id":"in_1","object":"invoice","status":"draft"}`)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/invoiceitems":
			fmt.Fprint(w, `{"id":"ii_1","object":"invoiceitem"}`)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/invoices/in_1/finalize":
			fmt.Fprint(w, `{"id":"in_1","object":"invoice","status":"paid"}`)
		default:
			http.Error(w, "unexpected Stripe request "+r.Method+" "+r.URL.Path, http.StatusInternalServerError)
		}
	})
	defer restore()

	topupper := stripeAutoTopupper{secretKey: "sk_test", topupPriceID: "price_topup"}
	invoiceID, err := topupper.PurchaseTopupUnit(t.Context(), billing.Account{
		OrgID:            "org_1",
		PlanCode:         billing.PlanTeam,
		StripeCustomerID: "cus_1",
	}, "idem_1")
	if err != nil {
		t.Fatalf("PurchaseTopupUnit returned error: %v", err)
	}
	if invoiceID != "in_1" {
		t.Fatalf("invoiceID = %q, want in_1", invoiceID)
	}
}

func TestStripeAutoTopupperPurchaseTopupUnitValidation(t *testing.T) {
	topupper := stripeAutoTopupper{secretKey: "sk_test", topupPriceID: "price_topup"}
	if _, err := topupper.PurchaseTopupUnit(t.Context(), billing.Account{OrgID: "org_1"}, "idem_1"); !errors.Is(err, billing.ErrAutoTopupNotConfigured) {
		t.Fatalf("missing customer error = %v, want ErrAutoTopupNotConfigured", err)
	}
	topupper = stripeAutoTopupper{secretKey: "sk_test"}
	if _, err := topupper.PurchaseTopupUnit(t.Context(), billing.Account{
		OrgID:            "org_1",
		PlanCode:         billing.PlanTeam,
		StripeCustomerID: "cus_1",
	}, "idem_1"); !errors.Is(err, billing.ErrAutoTopupNotConfigured) {
		t.Fatalf("missing price error = %v, want ErrAutoTopupNotConfigured", err)
	}
}
