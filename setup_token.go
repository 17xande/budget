package main

import (
	"log"
	"time"

	"github.com/17xande/budget/internal/auth"
)

// ensureSetupToken issues the one-time token that claims the first account, on a
// boot with no users, and says in the log what an operator needs to do next.
//
// supplied is SETUP_TOKEN; empty means generate one and print it.
func ensureSetupToken(store *auth.Store, supplied string, logger *log.Logger) error {
	n, err := store.CountUsers()
	if err != nil {
		return err
	}
	if n > 0 {
		return nil
	}

	token := supplied
	if token == "" {
		if token, err = auth.NewToken(); err != nil {
			return err
		}
	}
	stored, err := store.CreateSetupToken(token, time.Now())
	if err != nil {
		return err
	}
	if stored {
		if supplied != "" {
			logger.Print("no account exists; visit /setup and claim it with SETUP_TOKEN")
			return nil
		}
		// Deliberately the whole token: this line is the only place it will ever
		// exist in plain text, and someone has to be able to find it.
		logger.Printf("no account exists. Visit /setup and claim it with this one-time setup token: %s", token)
		return nil
	}

	// A token row is already there. It may be spent: setup is consumed for ever,
	// so a database whose every user was deleted by hand lands here with nothing
	// to claim. Say so rather than send someone looking for a token /setup will
	// refuse anyway.
	pending, err := store.SetupPending()
	if err != nil {
		return err
	}
	if !pending {
		logger.Print("no account exists, but setup has already been completed and cannot be " +
			"reopened. Recreate the database (make recreate-db) to start again")
		return nil
	}
	if supplied == "" {
		logger.Print("no account exists and a setup token was issued on an earlier start. " +
			"Visit /setup with the token printed then -- look further back in this log")
		return nil
	}
	// A supplied token that does not match the one already issued is the trap: it
	// looks configured, and /setup refuses it with nothing to say why.
	matches, err := store.CheckSetupToken(supplied)
	if err != nil {
		return err
	}
	if !matches {
		logger.Print("SETUP_TOKEN does not match the setup token issued on an earlier start and " +
			"is being ignored. Claim with the token from that earlier log line, or recreate the " +
			"database (make recreate-db) to have SETUP_TOKEN take effect")
		return nil
	}
	logger.Print("no account exists; visit /setup and claim it with SETUP_TOKEN")
	return nil
}
