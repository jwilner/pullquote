package exampletest

func ExampleFooBar() {
	for i := 0; i < 5; i++ {
		FooBar(i)
	}
	// Output:
	// FooBarRan 0
	// FooBarRan 1
	// FooBarRan 2
	// FooBarRan 3
	// FooBarRan 4
}
