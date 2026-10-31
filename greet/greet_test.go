package greet_test

import (
	"fmt"
	"testing"

	"github.com/craftainer/template-go-module/greet"
)

func TestHello(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name, in, want string
	}{
		{"named", "Ada", "Hello, Ada!"},
		{"empty falls back to world", "", "Hello, world!"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := greet.Hello(tc.in); got != tc.want {
				t.Errorf("Hello(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func ExampleHello() {
	fmt.Println(greet.Hello("Ada"))
	// Output: Hello, Ada!
}
