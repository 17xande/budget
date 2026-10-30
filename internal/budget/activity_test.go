package budget_test

import (
	"errors"
	"testing"

	"github.com/17xande-dev/budget/internal/budget"
)

func TestUndo(t *testing.T) {
	e := newEnv(t)
	a := e.cat("A", budget.KindExpense)
	b := e.cat("B", budget.KindExpense)
	e.add("1", tx("2026-10-01", -100, "X"))
	id := e.txnID("X")
	ai := budget.Actor{Via: budget.ViaMCP, TokenName: "laptop"}

	e.b.Categorise(id, a, web) // a person chose A
	e.b.Categorise(id, b, ai)  // the AI changed it to B

	entries, _ := e.b.Activity(budget.ViaMCP, 10)
	if len(entries) != 1 || entries[0].AfterName != "B" || entries[0].BeforeName != "A" || entries[0].Description != "X" || !entries[0].CanUndo() {
		t.Fatalf("AI activity: %+v", entries)
	}
	if err := e.b.Undo(entries[0].ID, web); err != nil {
		t.Fatal(err)
	}
	if c, src := e.categoryOf("X"); c != a || src != "manual" {
		t.Errorf("after undo: category %d source %q, want A", c, src)
	}
	if err := e.b.Undo(entries[0].ID, web); !errors.Is(err, budget.ErrAlreadyUndone) {
		t.Errorf("second undo: %v", err)
	}
	all, _ := e.b.Activity("", 10)
	if all[0].Action != "undo" {
		t.Errorf("the undo itself was not recorded: %+v", all[0])
	}
}

// A later change wins: undoing an older one must not clobber it.
func TestUndoRefusedWhenChangedSince(t *testing.T) {
	e := newEnv(t)
	a := e.cat("A", budget.KindExpense)
	b := e.cat("B", budget.KindExpense)
	e.add("1", tx("2026-10-01", -100, "X"))
	id := e.txnID("X")
	e.b.Categorise(id, a, budget.Actor{Via: budget.ViaMCP})
	entries, _ := e.b.Activity(budget.ViaMCP, 10)
	e.b.Categorise(id, b, web) // a person changes it again
	if err := e.b.Undo(entries[0].ID, web); !errors.Is(err, budget.ErrChangedSince) {
		t.Fatalf("undo over a newer change: %v", err)
	}
	if c, _ := e.categoryOf("X"); c != b {
		t.Error("the newer choice was clobbered")
	}
}

// Undoing the first categorisation of a transaction returns it to uncategorised.
func TestUndoToUncategorised(t *testing.T) {
	e := newEnv(t)
	a := e.cat("A", budget.KindExpense)
	e.add("1", tx("2026-10-01", -100, "X"))
	e.b.Categorise(e.txnID("X"), a, budget.Actor{Via: budget.ViaMCP})
	entries, _ := e.b.Activity(budget.ViaMCP, 10)
	if err := e.b.Undo(entries[0].ID, web); err != nil {
		t.Fatal(err)
	}
	if c, src := e.categoryOf("X"); c != 0 || src != "" {
		t.Errorf("after undo: %d %q, want uncategorised", c, src)
	}
}

func TestOnlyCategorisationsUndo(t *testing.T) {
	e := newEnv(t)
	e.cat("A", budget.KindExpense)
	entries, _ := e.b.Activity("", 10) // category.create
	if entries[0].CanUndo() {
		t.Error("a category creation offers undo")
	}
	if err := e.b.Undo(entries[0].ID, web); !errors.Is(err, budget.ErrInvalid) {
		t.Errorf("undo of a creation: %v", err)
	}
}
