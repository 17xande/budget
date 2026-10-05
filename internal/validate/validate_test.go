package validate

import (
	"slices"
	"strings"
	"testing"
)

func TestNormalizeEmail(t *testing.T) {
	for in, want := range map[string]string{
		"alex@example.com":        "alex@example.com",
		"  alex@example.com ":     "alex@example.com",
		"Alex <alex@example.com>": "alex@example.com",
		"not an address":          "not an address",
	} {
		if got := NormalizeEmail(in); got != want {
			t.Errorf("NormalizeEmail(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestValidateMember(t *testing.T) {
	if errs := ValidateMember("a@example.com", "A"); len(errs) != 0 {
		t.Errorf("valid member refused: %v", errs)
	}
	errs := ValidateMember("nope", " ")
	if !errs.Has("email") || !errs.Has("name") {
		t.Errorf("want email and name errors, got %v", errs)
	}
}

func TestValidatePassword(t *testing.T) {
	long := strings.Repeat("a", MinPasswordLength)
	tests := []struct {
		name              string
		password, confirm string
		want              []string
	}{
		{"good", long, long, nil},
		{"empty", "", "", []string{"password"}},
		{"too short", "short", "short", []string{"password"}},
		{"mismatch", long, long + "x", []string{"password_confirm"}},
		// A blank pair must not produce two errors saying the same thing.
		{"blank reports once", "", "x", []string{"password"}},
		// Length is counted in characters, not bytes: twelve accented letters
		// are twenty-four bytes and still exactly long enough.
		{"runes not bytes", strings.Repeat("é", MinPasswordLength), strings.Repeat("é", MinPasswordLength), nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := make(FormErrors)
			ValidatePassword(errs, tt.password, tt.confirm)
			var got []string
			for k := range errs {
				got = append(got, k)
			}
			slices.Sort(got)
			if !slices.Equal(got, tt.want) {
				t.Errorf("fields = %v, want %v", got, tt.want)
			}
		})
	}
}
