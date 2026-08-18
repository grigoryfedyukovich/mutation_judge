package testselection

import "testing"

func TestVIPDiscount(t *testing.T) {
	if !EligibleForDiscount(true, false) {
		t.Fatal("VIP customer should receive a discount")
	}
}

func TestCouponDiscount(t *testing.T) {
	if !EligibleForDiscount(false, true) {
		t.Fatal("coupon holder should receive a discount")
	}
}

func TestNoDiscount(t *testing.T) {
	if EligibleForDiscount(false, false) {
		t.Fatal("ordinary customer should not receive a discount")
	}
}
