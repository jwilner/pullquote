package codefence

// fooBar does some stuff
func fooBar() {
	nested := func() {
		// this is a nested func
	}
	_ = nested
}
