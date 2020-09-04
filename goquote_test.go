package main

import (
	"fmt"
	"testing"
)

func Test_realignTabs(t *testing.T) {
	for _, tt := range []struct {
		name, in, out string
	}{
		{
			"empty",
			``,
			``,
		},
		{
			"with comment excess indent",
			`// hi
	func main() {
		// cool
	}`,
			`// hi
func main() {
	// cool
}`,
		},
		{
			"with comment no excess indent",
			`// hi
func main() {
	// cool
}`,
			`// hi
func main() {
	// cool
}`,
		},
		{
			"no comment no excess indent",
			`func main() {
	// cool
}`,
			`func main() {
	// cool
}`,
		},
		{
			"indented inner",
			`bar := func() {
		// cool
	}`,
			`bar := func() {
	// cool
}`,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := string(realignTabs([]byte(tt.in))); got != tt.out {
				t.Errorf("realignTabs() = %q, want %q", got, tt.out)
			}
		})
	}
}

func Test_parseExampleTest(t *testing.T) {
	for _, c := range []struct {
		name    string
		in      string
		out     []string
		wantErr bool
	}{
		{
			"splits",
			`// Some stuff before the func
func ExampleFooBar() {
	FooBar()
	FooBaz()
	// Output:
	// FooBarRan
	// FooBazRan
}`,
			[]string{
				"FooBar()\nFooBaz()",
				"FooBarRan\nFooBazRan",
			},
			false,
		},
		{
			"Indent savvy",
			`// Some stuff before the func
func ExampleFooBar() {
	for i := 0; i < 5; i++ {
		FooBar()
	}
	// Output:
	// FooBarRan
	// 	FooBazRan
}`,
			[]string{
				"for i := 0; i < 5; i++ {\n\tFooBar()\n}",
				"FooBarRan\n\tFooBazRan",
			},
			false,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			res, err := parseExampleTest([]byte(c.in))
			if (err != nil) != c.wantErr {
				t.Fatalf("wantErr %v but %v", c.wantErr, err)
			}
			if err != nil {
				return
			}
			if len(res) != len(c.out) {
				t.Fatalf("want %d but got %d", len(c.out), len(res))
			}
			for i, b := range res {
				if s := string(b); c.out[i] != s {
					t.Errorf("Wanted:\n%v\nGot:\n%v", fmt.Sprintf("%q", c.out[i]), fmt.Sprintf("%q", s))
				}
			}
		})
	}
}
