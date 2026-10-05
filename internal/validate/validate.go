// Package validate holds form validation shared by the handlers and the
// reset-password command, so the web forms and the command line enforce one policy.
package validate

import (
	"fmt"
	"net/mail"
	"strings"
)

// FormErrors maps a field name to the message shown beside it.
type FormErrors map[string]string

func (e FormErrors) Has(field string) bool {
	_, ok := e[field]
	return ok
}

// MinPasswordLength is the shortest password accepted.
//
// Length is the only rule: composition requirements ("one digit, one symbol") push
// people towards short mangled words, while argon2id already makes an offline guess
// expensive. Twelve characters admits a short passphrase, which is what we would
// rather people chose.
const MinPasswordLength = 12

// NormalizeEmail reduces an address to its bare form. mail.ParseAddress accepts the
// display-name form, so "Alex <alex@example.com>" validates -- stored raw, it would
// be a second identity for one mailbox that the login form's type=email input can
// never type back.
func NormalizeEmail(s string) string {
	s = strings.TrimSpace(s)
	addr, err := mail.ParseAddress(s)
	if err != nil {
		return s
	}
	return addr.Address
}

// ValidateMember checks the identity fields of a new account. The email must
// already be normalised.
func ValidateMember(email, name string) FormErrors {
	errs := make(FormErrors)
	if email == "" {
		errs["email"] = "Email address is required."
	} else if _, err := mail.ParseAddress(email); err != nil {
		errs["email"] = "That does not look like an email address."
	}
	if strings.TrimSpace(name) == "" {
		errs["name"] = "Name is required."
	}
	return errs
}

// ValidatePassword checks a new password and its confirmation, writing problems
// into errs under "password" and "password_confirm", so a caller validating a whole
// form reports every problem in one pass.
func ValidatePassword(errs FormErrors, password, confirm string) {
	switch {
	case password == "":
		errs["password"] = "Password is required."
	case len([]rune(password)) < MinPasswordLength:
		errs["password"] = fmt.Sprintf("Use at least %d characters.", MinPasswordLength)
	}
	// Only worth reporting once the password itself is acceptable; otherwise a
	// blank pair produces two errors saying the same thing.
	if !errs.Has("password") && password != confirm {
		errs["password_confirm"] = "The two passwords do not match."
	}
}
