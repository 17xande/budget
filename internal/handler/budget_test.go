package handler_test

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

// seeded is a signed-in owner with the starter categories and the cheque fixture
// imported.
func seeded(t *testing.T) (*env, *browser) {
	t.Helper()
	e := newEnv(t)
	owner := e.claim()
	if r := owner.post("/", "/categories/starter", nil); r.code != http.StatusSeeOther {
		t.Fatalf("starter categories: %d", r.code)
	}
	owner.upload(map[string][]byte{"cheque.csv": fixture(t, "cheque.csv")})
	return e, owner
}

var (
	txnIDRe = regexp.MustCompile(`<tr id="txn-(\d+)"`)
	catRe   = regexp.MustCompile(`<option value="(\d+)">Groceries</option>`)
)

func firstTxnAndGroceries(t *testing.T, owner *browser) (txn, groceries string) {
	t.Helper()
	page := owner.get("/transactions?month=all").body
	m := txnIDRe.FindStringSubmatch(page)
	c := catRe.FindStringSubmatch(page)
	if m == nil || c == nil {
		t.Fatal("no transaction row or no Groceries option")
	}
	return m[1], c[1]
}

func TestCategoriseWithoutJavaScript(t *testing.T) {
	_, owner := seeded(t)
	txn, groceries := firstTxnAndGroceries(t, owner)

	ret := "/transactions?month=all&account=1"
	r := owner.post("/transactions?month=all", "/transactions/"+txn+"/category", url.Values{"category": {groceries}, "return": {ret}})
	if r.code != http.StatusSeeOther || r.loc != ret {
		t.Fatalf("form categorise: %d %q", r.code, r.loc)
	}
	// A return target elsewhere is not followed.
	r = owner.post("/transactions?month=all", "/transactions/"+txn+"/category", url.Values{"category": {"none"}, "return": {"https://evil.example/"}})
	if r.loc != "/transactions" {
		t.Errorf("foreign return followed: %q", r.loc)
	}
	// Strict values: absent or junk is a bad request, never a silent clear.
	for _, v := range []url.Values{{}, {"category": {""}}, {"category": {"-3"}}, {"category": {"groceries"}}} {
		if r := owner.post("/transactions?month=all", "/transactions/"+txn+"/category", v); r.code != http.StatusBadRequest {
			t.Errorf("category %v: %d, want 400", v, r.code)
		}
	}
}

func (b *browser) htmxPost(from, path string, form url.Values) resp {
	b.e.t.Helper()
	form.Set("csrf_token", b.token(from))
	req, _ := http.NewRequest("POST", b.e.srv.URL+path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", b.e.srv.URL)
	req.Header.Set("HX-Request", "true")
	return b.do(req)
}

func TestCategoriseWithHTMX(t *testing.T) {
	_, owner := seeded(t)
	txn, groceries := firstTxnAndGroceries(t, owner)

	// The swap contract: the response replaces the whole row, on the page's own
	// row and on the one that comes back.
	page := owner.get("/transactions?month=all").body
	r := owner.htmxPost("/transactions?month=all", "/transactions/"+txn+"/category", url.Values{"category": {groceries}, "return": {"/transactions"}})
	for _, attr := range []string{`hx-target="closest tr"`, `hx-swap="outerHTML"`} {
		if !strings.Contains(page, attr) {
			t.Errorf("page row lacks %s", attr)
		}
		if !strings.Contains(r.body, attr) {
			t.Errorf("htmx response row lacks %s", attr)
		}
	}
	if r.code != 200 || !strings.HasPrefix(strings.TrimSpace(r.body), `<tr id="txn-`+txn+`" class="saved">`) {
		t.Fatalf("htmx categorise: %d\n%s", r.code, r.body)
	}
	if !strings.Contains(r.body, `<option value="`+groceries+`" selected>Groceries</option>`) {
		t.Error("returned row does not show the new category")
	}
	if strings.Contains(r.body, "<html") {
		t.Error("htmx got a whole page instead of a row")
	}

	// Archiving the category, then choosing it again for another row, is
	// refused -- in the row itself, so the table is not broken by a bare message.
	owner.post("/categories", "/categories/"+groceries+"/archive", url.Values{"archived": {"1"}})
	other := txnIDRe.FindAllStringSubmatch(owner.get("/transactions?month=all").body, -1)[1][1]
	r = owner.htmxPost("/transactions?month=all", "/transactions/"+other+"/category", url.Values{"category": {groceries}})
	if r.code != http.StatusUnprocessableEntity || !strings.Contains(r.body, "archived") || !strings.Contains(r.body, `<tr id="txn-`+other) {
		t.Errorf("archived via htmx: %d\n%s", r.code, r.body)
	}
}

func TestRulesFromTransactionAndOnImport(t *testing.T) {
	_, owner := seeded(t)
	txn, groceries := firstTxnAndGroceries(t, owner)

	// "+ rule" prefills the description.
	if page := owner.get("/rules?txn=" + txn).body; !strings.Contains(page, `value="SAVINGS TRANSFER"`) {
		t.Error("rule form not prefilled from the transaction")
	}

	r := owner.post("/rules", "/rules", url.Values{"pattern": {"checkers"}, "category": {groceries}, "account": {"all"}, "apply": {"1"}})
	if r.code != http.StatusSeeOther || r.loc != "/rules?notice=rule_applied_many" {
		t.Fatalf("create rule: %d %q", r.code, r.loc)
	}
	if n := countRows(owner.get("/transactions?month=all&category=" + groceries).body); n != 2 {
		t.Errorf("rule categorised %d rows, want the 2 Checkers rows", n)
	}

	// A new import is categorised by the rules as it arrives.
	owner.post("/rules", "/rules", url.Values{"pattern": {"uber"}, "category": {groceries}, "account": {"all"}})
	owner.upload(map[string][]byte{"card.csv": fixture(t, "card.csv")})
	if n := countRows(owner.get("/transactions?month=all&category=" + groceries).body); n != 3 {
		t.Errorf("after importing the card: %d groceries rows, want 3", n)
	}

	// Junk in the form is a bad request.
	if r := owner.post("/rules", "/rules", url.Values{"pattern": {"x"}, "category": {"abc"}, "account": {"all"}}); r.code != http.StatusBadRequest {
		t.Errorf("bad category: %d", r.code)
	}
	if r := owner.post("/rules", "/rules", url.Values{"pattern": {"   "}, "category": {groceries}, "account": {"all"}}); r.code != http.StatusUnprocessableEntity {
		t.Errorf("blank pattern: %d", r.code)
	}
}

func TestTargetsAndDashboard(t *testing.T) {
	_, owner := seeded(t)
	_, groceries := firstTxnAndGroceries(t, owner)
	owner.post("/rules", "/rules", url.Values{"pattern": {"checkers"}, "category": {groceries}, "account": {"all"}, "apply": {"1"}})

	// South African typing: a space for thousands, a comma for decimals.
	r := owner.post("/budget/targets?month=2026-10", "/budget/targets", url.Values{"month": {"2026-10"}, "target_" + groceries: {"1 50,00"}})
	if r.code != http.StatusSeeOther {
		t.Fatalf("save targets: %d\n%s", r.code, r.body)
	}
	r = owner.post("/budget/targets?month=2026-10", "/budget/targets", url.Values{"month": {"2026-10"}, "target_" + groceries: {"lots"}})
	if r.code != http.StatusUnprocessableEntity || !strings.Contains(r.body, "enter an amount") || !strings.Contains(r.body, `value="lots"`) {
		t.Errorf("bad target: %d", r.code)
	}

	// October: two Checkers rows of R 89.90 against a R 150.00 target -- over.
	home := owner.get("/?month=2026-10").body
	// The overview shows whole rand (R 179.80 -> R180, R 29.80 over -> R30).
	for _, want := range []string{"R180", "R150", "R30 over", `<tr class="over">`, `<rect class="fill over" width="100"`} {
		if !strings.Contains(home, want) {
			t.Errorf("dashboard lacks %q", want)
		}
	}
	if strings.Contains(home, "179.80") {
		t.Error("the overview should round to whole rand")
	}
	// The Total row: target R150, spent R180, and R30 over, in the footer cells.
	foot := home[strings.Index(home, "<tfoot>"):strings.Index(home, "</tfoot>")]
	for _, want := range []string{`<td class="num nowrap hide-s">R150</td>`, `<td class="num nowrap">R180</td>`, "R30 over"} {
		if !strings.Contains(foot, want) {
			t.Errorf("Total row lacks %q:\n%s", want, foot)
		}
	}

	// One cent over a R179.79 target still says over, never "R0 over".
	owner.post("/budget/targets?month=2026-10", "/budget/targets", url.Values{"month": {"2026-10"}, "target_" + groceries: {"179,79"}})
	home = owner.get("/?month=2026-10").body
	if strings.Count(home, "R1 over") != 2 || strings.Contains(home, "R0 over") {
		t.Errorf("a cent over should read R1 over in the row and the Total row:\n%s", home)
	}

	// At 92% of a R 195.00 target the line is near, not over, and has R15 left.
	owner.post("/budget/targets?month=2026-10", "/budget/targets", url.Values{"month": {"2026-10"}, "target_" + groceries: {"195,00"}})
	home = owner.get("/?month=2026-10").body
	for _, want := range []string{`<tr class="near">`, `<rect class="fill near"`, "R15 left"} {
		if !strings.Contains(home, want) {
			t.Errorf("near dashboard lacks %q", want)
		}
	}

	owner.post("/budget/targets?month=2026-10", "/budget/targets", url.Values{"month": {"2026-10"}, "target_" + groceries: {"150,00"}})

	// Copy forward fills November from October.
	owner.post("/budget/targets?month=2026-11", "/budget/targets/copy", url.Values{"month": {"2026-11"}})
	if page := owner.get("/budget/targets?month=2026-11").body; !strings.Contains(page, `value="150.00"`) {
		t.Error("copy forward did not fill November")
	}
}

func TestCategoryArchiveIsStrict(t *testing.T) {
	_, owner := seeded(t)
	_, groceries := firstTxnAndGroceries(t, owner)
	for _, v := range []url.Values{{}, {"archived": {"yes"}}} {
		if r := owner.post("/categories", "/categories/"+groceries+"/archive", v); r.code != http.StatusBadRequest {
			t.Errorf("archived=%v: %d, want 400", v, r.code)
		}
	}
	if r := owner.post("/categories", "/categories", url.Values{"name": {"groceries"}, "kind": {"expense"}}); r.code != http.StatusUnprocessableEntity {
		t.Errorf("duplicate category: %d", r.code)
	}
}

func TestBudgetPagesHaveNoInlineCode(t *testing.T) {
	_, owner := seeded(t)
	_, groceries := firstTxnAndGroceries(t, owner)
	owner.post("/rules", "/rules", url.Values{"pattern": {"checkers"}, "category": {groceries}, "account": {"all"}, "apply": {"1"}})
	for _, p := range []string{"/", "/?month=2026-10", "/budget/targets?month=2026-10", "/categories", "/rules", "/rules?txn=1", "/settings", "/transactions?category=none"} {
		body := owner.get(p).body
		if !strings.Contains(body, "</html>") {
			t.Errorf("%s did not render", p)
		}
		if loc := inlineRe.FindStringIndex(body); loc != nil {
			t.Errorf("%s renders %q", p, body[loc[0]:loc[1]])
		}
	}
}

// An uncategorised row is called out with the uncat class, on the page and in the
// htmx response alike, so the call-out clears or appears without a reload.
func TestUncategorisedRowIsCalledOut(t *testing.T) {
	_, owner := seeded(t)
	txn, groceries := firstTxnAndGroceries(t, owner)
	row := `<tr id="txn-` + txn + `" class="uncat">`
	if !strings.Contains(owner.get("/transactions?month=all").body, row) {
		t.Fatalf("an uncategorised row has no uncat class; want %s", row)
	}

	r := owner.htmxPost("/transactions?month=all", "/transactions/"+txn+"/category", url.Values{"category": {groceries}, "return": {"/transactions"}})
	if strings.Contains(r.body, "uncat") {
		t.Errorf("htmx response after categorising still says uncat:\n%s", r.body)
	}
	if strings.Contains(owner.get("/transactions?month=all").body, row) {
		t.Error("the page still calls the row out after it was categorised")
	}
	// A categorised, unsaved row has no class attribute at all, not an empty one.
	if !strings.Contains(owner.get("/transactions?month=all").body, `<tr id="txn-`+txn+`">`) {
		t.Error("a categorised row does not start with a bare <tr id=\"txn-N\">")
	}

	r = owner.htmxPost("/transactions?month=all", "/transactions/"+txn+"/category", url.Values{"category": {"none"}, "return": {"/transactions"}})
	if !strings.HasPrefix(strings.TrimSpace(r.body), `<tr id="txn-`+txn+`" class="saved uncat">`) {
		t.Errorf("clearing the category via htmx does not call the row out again:\n%s", r.body)
	}
}
