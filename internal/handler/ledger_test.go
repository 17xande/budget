package handler_test

import (
	"archive/zip"
	"bytes"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("../importer/testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func zipped(t *testing.T, name string, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create(name)
	w.Write(data)
	zw.Close()
	return buf.Bytes()
}

// upload posts files to /import as the browser's form does: multipart, with the
// CSRF token from the page and the Origin header.
func (b *browser) upload(files map[string][]byte) resp {
	b.e.t.Helper()
	token := b.token("/import")
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	mw.WriteField("csrf_token", token)
	for name, data := range files {
		fw, _ := mw.CreateFormFile("files", name)
		fw.Write(data)
	}
	mw.Close()
	req, _ := http.NewRequest("POST", b.e.srv.URL+"/import", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Origin", b.e.srv.URL)
	return b.do(req)
}

var rowsRe = regexp.MustCompile(`(?s)<table class="txns">.*?<tbody>(.*?)</tbody>`)

func countRows(body string) int {
	m := rowsRe.FindStringSubmatch(body)
	if m == nil {
		return 0
	}
	return strings.Count(m[1], "<tr>")
}

func TestImportJourney(t *testing.T) {
	e := newEnv(t)
	owner := e.claim()

	r := owner.upload(map[string][]byte{
		"cheque.csv": fixture(t, "cheque.csv"),
		// The ZIP exactly as FNB downloads it.
		"transaction_history_Credit_Acc.zip": zipped(t, "400000______1234.ofx", fixture(t, "card.ofx")),
		"notes.txt":                          []byte("not a bank export"),
	})
	if r.code != http.StatusSeeOther || r.loc != "/import?notice=imported" {
		t.Fatalf("upload: %d %q\n%s", r.code, r.loc, r.body)
	}

	hist := owner.get("/import").body
	for _, want := range []string{"cheque.csv", "transaction_history_Credit_Acc.zip", "notes.txt", "failed", "not an FNB CSV or OFX export"} {
		if !strings.Contains(hist, want) {
			t.Errorf("import history lacks %q", want)
		}
	}

	// Re-uploading the same export adds nothing.
	owner.upload(map[string][]byte{"cheque.ofx": fixture(t, "cheque.ofx")})
	all := owner.get("/transactions?month=all").body
	if n := countRows(all); n != 12 {
		t.Errorf("all months lists %d rows, want 12 (6 cheque + 6 card)", n)
	}

	// The page opens on the latest month with data.
	latest := owner.get("/transactions").body
	if !strings.Contains(latest, `<option value="2026-10" selected>October 2026</option>`) {
		t.Error("the default month is not the latest")
	}
	if n := countRows(latest); n != 4 {
		t.Errorf("October lists %d rows, want 4", n)
	}

	// Totals are formatted rand, computed from the same rows as the list.
	if !strings.Contains(all, "R 3 200.00") {
		t.Error("salary not shown as R 3 200.00")
	}
	if !strings.Contains(all, "−R 89.90") {
		t.Error("an outgoing amount is not shown with a minus sign")
	}

	// A search uses the description, case-insensitively.
	if n := countRows(owner.get("/transactions?month=all&q=checkers").body); n != 2 {
		t.Errorf("search for checkers: %d rows, want the identical pair", n)
	}

	home := owner.get("/").body
	if !strings.Contains(home, "Cheque account ···0001") || !strings.Contains(home, "Credit card ···1234") {
		t.Error("overview does not list both accounts")
	}
}

func TestUploadWithoutFiles(t *testing.T) {
	e := newEnv(t)
	owner := e.claim()
	r := owner.upload(nil)
	if r.code != http.StatusUnprocessableEntity || !strings.Contains(r.body, "Choose at least one file") {
		t.Fatalf("empty upload: %d", r.code)
	}
}

// Bank descriptions are written by merchants -- outside input that ends up in
// our pages. html/template escapes it; this pins that it stays that way.
func TestImportedTextIsEscaped(t *testing.T) {
	e := newEnv(t)
	owner := e.claim()
	evil := strings.Replace(string(fixture(t, "cheque.csv")), "SAVINGS TRANSFER", `<script>alert(1)</script>`, 1)
	owner.upload(map[string][]byte{"evil.csv": []byte(evil)})
	body := owner.get("/transactions?month=all").body
	if strings.Contains(body, "<script>alert") {
		t.Fatal("an imported description was rendered as markup")
	}
	if !strings.Contains(body, "&lt;script&gt;alert(1)&lt;/script&gt;") {
		t.Error("the escaped description is missing")
	}
}

func TestTransactionFilters(t *testing.T) {
	e := newEnv(t)
	owner := e.claim()
	owner.upload(map[string][]byte{"a.ofx": fixture(t, "cheque-advanced.ofx"), "b.csv": fixture(t, "card.csv")})

	// A hand-edited month falls back to the latest rather than erroring.
	if r := owner.get("/transactions?month=2026-13"); r.code != 200 || countRows(r.body) != 4 {
		t.Errorf("malformed month: %d, %d rows", r.code, countRows(r.body))
	}
	// One account shows its running balance; the card has none to show.
	page := owner.get("/accounts").body
	id := regexp.MustCompile(`action="/accounts/(\d+)"`).FindStringSubmatch(page)
	if id == nil {
		t.Fatal("no account forms")
	}
	one := owner.get("/transactions?month=all&account=" + id[1]).body
	if !strings.Contains(one, "<th class=\"num\">Balance</th>") {
		t.Error("single-account view has no balance column")
	}
	// An account id that does not exist is ignored, not trusted.
	if n := countRows(owner.get("/transactions?month=all&account=99999").body); n != 14 {
		t.Errorf("unknown account filter: %d rows, want all 14", n)
	}
}

func TestAccountUpdate(t *testing.T) {
	e := newEnv(t)
	owner := e.claim()
	owner.upload(map[string][]byte{"c.csv": fixture(t, "cheque.csv")})
	page := owner.get("/accounts").body
	m := regexp.MustCompile(`action="(/accounts/\d+)"`).FindStringSubmatch(page)
	if m == nil {
		t.Fatal("no account form")
	}
	path := m[1]

	r := owner.post("/accounts", path, url.Values{"name": {"Joint cheque"}, "kind": {"cheque"}})
	if r.code != http.StatusSeeOther || r.loc != "/accounts?notice=saved" {
		t.Fatalf("save: %d %q", r.code, r.loc)
	}
	if !strings.Contains(owner.get("/transactions?month=all").body, "Joint cheque") {
		t.Error("rename not shown on transactions")
	}
	// A missing kind is a malformed request, never a silent default.
	if r := owner.post("/accounts", path, url.Values{"name": {"X"}}); r.code != http.StatusBadRequest {
		t.Errorf("missing kind: %d, want 400", r.code)
	}
	if r := owner.post("/accounts", path, url.Values{"name": {"  "}, "kind": {"cheque"}}); r.code != http.StatusUnprocessableEntity {
		t.Errorf("blank name: %d, want 422", r.code)
	}
	if r := owner.post("/accounts", "/accounts/99999", url.Values{"name": {"X"}, "kind": {"cheque"}}); r.code != http.StatusNotFound {
		t.Errorf("unknown account: %d, want 404", r.code)
	}
}

// The new pages, populated, carry no inline style or handler either.
func TestLedgerPagesHaveNoInlineCode(t *testing.T) {
	e := newEnv(t)
	owner := e.claim()
	owner.upload(map[string][]byte{"c.csv": fixture(t, "cheque.csv"), "x.txt": []byte("junk")})
	for _, p := range []string{"/", "/transactions", "/transactions?month=all&account=1", "/import", "/accounts"} {
		body := owner.get(p).body
		if !strings.Contains(body, "</html>") {
			t.Errorf("%s did not render", p)
		}
		if loc := inlineRe.FindStringIndex(body); loc != nil {
			t.Errorf("%s renders %q", p, body[loc[0]:loc[1]])
		}
	}
}
