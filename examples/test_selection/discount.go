package testselection

// EligibleForDiscount grants a discount to VIPs or coupon holders.
func EligibleForDiscount(vip, hasCoupon bool) bool {
	return vip || hasCoupon
}
