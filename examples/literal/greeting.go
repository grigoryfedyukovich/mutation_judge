package literal

// DefaultName is used by Greeting when name is empty.
const DefaultName = "guest"

// Greeting returns a fixed greeting for name, or for DefaultName if
// name is empty.
func Greeting(name string) string {
	if name == "" {
		name = DefaultName
	}
	return "Hello, " + name
}
