package main

import (
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
