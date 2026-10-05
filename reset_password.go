package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/17xande-dev/budget/internal/auth"
	"github.com/17xande-dev/budget/internal/validate"
)

// resetPassword is lockout recovery: it sets email's password to the first line of
// in, and ends that account's sessions.
//
// The password comes on stdin, never as an argument, so it stays out of shell
// history and `ps`. The Makefile's reset-password target reads it with `read -rs`
// (no echo) and pipes it in. The same length policy as the web forms applies:
// this sets the password of an account that can see everything.
//
// It cannot create an account -- that is what /setup and the members page are for
// -- so a mistyped email is an error rather than a stray new user.
func resetPassword(store *auth.Store, email string, in io.Reader, out io.Writer) error {
	email = validate.NormalizeEmail(email)
	u, ok, err := store.FindByEmail(email)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("no account for %q", email)
	}

	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("read password: %w", err)
	}
	password := strings.TrimRight(line, "\r\n")
	errs := make(validate.FormErrors)
	validate.ValidatePassword(errs, password, password)
	if msg, bad := errs["password"]; bad {
		return errors.New(msg)
	}

	hash, err := auth.HashPassword(password, auth.DefaultParams)
	if err != nil {
		return err
	}
	// mustChange false: the person running this is the one who will use it.
	if _, err := store.SetPassword(u.ID, hash, false); err != nil {
		return err
	}
	fmt.Fprintf(out, "password reset for %s; their existing sessions have been signed out\n", u.Email)
	return nil
}
