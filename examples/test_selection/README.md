# Selected-test scope

The complete test suite observes both sides of `vip || hasCoupon`, so both deletion mutants are killed:

```bash
./bin/mutation-judge --no-cache --operators boolean ./examples/test_selection
```

Selecting only the VIP test leaves coupon-only behavior unobserved:

```bash
./bin/mutation-judge \
  --no-cache \
  --operators boolean \
  --test-run '^TestVIPDiscount$' \
  ./examples/test_selection
```

The expected result is one killed and one survived mutant.
