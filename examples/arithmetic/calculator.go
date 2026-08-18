package arithmetic

// Total adds a service fee to a subtotal.
func Total(subtotal, fee int) int {
	return subtotal + fee
}

// Product scales a value by a quantity.
func Product(value, quantity int) int {
	return value * quantity
}

// Greeting demonstrates an arithmetic mutation that cannot type-check:
// replacing string concatenation with subtraction is invalid Go.
func Greeting(name string) string {
	return "hello, " + name
}
