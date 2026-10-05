package handler_test

import (
	"net/http"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

var tokenRe = regexp.MustCompile(`Bearer (bgt_[A-Za-z0-9_-]+)`)

func TestTokensShownOnceAndRevocable(t *testing.T) {
	e := newEnv(t)
	owner := e.claim()

	r := owner.post("/tokens", "/tokens", url.Values{"name": {"laptop"}, "days": {"90"}})
	m := tokenRe.FindStringSubmatch(r.body)
	if r.code != 200 || m == nil || !strings.Contains(r.body, "claude mcp add --transport http budget") {
		t.Fatalf("create: %d, token shown: %v", r.code, m != nil)
	}
	tok := m[1]
	if _, _, err := e.users.APITokenUser(tok, time.Now()); err != nil {
		t.Fatalf("the shown token does not work: %v", err)
	}
	// Shown once: the list page never has it again.
	list := owner.get("/tokens").body
	if strings.Contains(list, tok) {
		t.Fatal("the token is shown again on the list page")
	}
	if !strings.Contains(list, "laptop") {
		t.Error("token not listed by name")
	}

	// Strict lifetime: absent or unknown is a bad request, not a silent default.
	for _, days := range []string{"", "7", "forever"} {
		if r := owner.post("/tokens", "/tokens", url.Values{"name": {"x"}, "days": {days}}); r.code != http.StatusBadRequest {
			t.Errorf("days=%q: %d, want 400", days, r.code)
		}
	}
	if r := owner.post("/tokens", "/tokens", url.Values{"name": {"  "}, "days": {"30"}}); r.code != http.StatusUnprocessableEntity {
		t.Errorf("blank name: %d", r.code)
	}

	// Another member cannot revoke it.
	owner.post("/members", "/members", url.Values{"email": {"p@example.com"}, "name": {"P"}, "password": {"temporary passphrase"}, "password_confirm": {"temporary passphrase"}})
	partner := e.browser()
	partner.login("p@example.com", "temporary passphrase")
	partner.post("/password", "/password", url.Values{"current": {"temporary passphrase"}, "password": {"partners passphrase"}, "password_confirm": {"partners passphrase"}})
	partner.login("p@example.com", "partners passphrase")
	id := regexp.MustCompile(`action="/tokens/(\d+)/revoke"`).FindStringSubmatch(list)[1]
	if r := partner.post("/tokens", "/tokens/"+id+"/revoke", nil); r.code != http.StatusNotFound {
		t.Errorf("another member revoking my token: %d, want 404", r.code)
	}
	if r := owner.post("/tokens", "/tokens/"+id+"/revoke", nil); r.code != http.StatusSeeOther {
		t.Fatalf("revoke: %d", r.code)
	}
	if _, _, err := e.users.APITokenUser(tok, time.Now()); err == nil {
		t.Error("a revoked token still works")
	}
}

func TestActivityAndUndo(t *testing.T) {
	_, owner := seeded(t)
	txn, groceries := firstTxnAndGroceries(t, owner)
	owner.post("/transactions?month=all", "/transactions/"+txn+"/category", url.Values{"category": {groceries}})

	// A person's change is in "everyone", not in the AI-only default view.
	if page := owner.get("/activity").body; strings.Contains(page, "Categorised") {
		t.Error("a person's change shows in the AI-only view")
	}
	all := owner.get("/activity?via=all").body
	if !strings.Contains(all, "Categorised <q>SAVINGS TRANSFER</q>: none → Groceries") {
		t.Fatalf("activity does not describe the change:\n%s", all)
	}
	id := regexp.MustCompile(`action="/activity/(\d+)/undo"`).FindStringSubmatch(all)
	if id == nil {
		t.Fatal("no undo offered")
	}
	r := owner.post("/activity?via=all", "/activity/"+id[1]+"/undo", url.Values{"return": {"/activity?via=all"}})
	if r.code != http.StatusSeeOther || r.loc != "/activity?via=all&notice=undone" {
		t.Fatalf("undo: %d %q", r.code, r.loc)
	}
	// Undoing twice is a conflict, explained, not a silent no-op.
	if r := owner.post("/activity?via=all", "/activity/"+id[1]+"/undo", nil); r.code != http.StatusConflict {
		t.Errorf("second undo: %d", r.code)
	}
}

// What is mounted outside the CSRF layer is pinned by source text: only /mcp,
// whose bearer-only authentication makes CSRF moot.
func TestOnlyMCPIsOutsideCSRF(t *testing.T) {
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	got := regexp.MustCompile(`root\.Handle\(([^)]*)\)`).FindAllStringSubmatch(string(src), -1)
	var lines []string
	for _, m := range got {
		lines = append(lines, m[1])
	}
	if want := []string{`"/mcp", mcpHandler`, `"/", csrf`}; !slices.Equal(lines, want) {
		t.Errorf("mounted outside nosurf: %q, want %q", lines, want)
	}
}
