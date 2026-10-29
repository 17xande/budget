package handler

import (
	"cmp"
	"errors"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/17xande/budget/internal/budget"
	"github.com/17xande/budget/internal/importer"
	"github.com/17xande/budget/internal/ledger"
	"github.com/17xande/budget/internal/middleware"
	"github.com/justinas/nosurf"
)

// --- Transactions --------------------------------------------------------------

// maxListed caps one page of transactions. A month is a few hundred rows; the cap
// only bites on "all months", where the page says how many it is not showing.
const maxListed = 2000

var monthRe = regexp.MustCompile(`^\d{4}-(0[1-9]|1[0-2])$`)

type txnsData struct {
	page
	Months     []string
	Accounts   []ledger.Account
	Categories []budget.Category
	Month      string // "" means all months
	AccountID  int64
	// Category is the category filter as the select holds it: "", "none" for
	// uncategorised, or a category id.
	Category string
	Query    string
	Rows     []txnRow
	Totals   ledger.Totals
	// Truncated is true when Totals counts more rows than Rows holds.
	Truncated bool
	// OneAccount shows the balance column, which only means anything for a
	// single account.
	OneAccount bool
	ExportURL  string
}

// txnRow is one table row: the transaction, and what its category form needs.
// The row is also the htmx response to a categorisation, so it carries
// everything itself rather than reaching into the page.
type txnRow struct {
	ledger.Txn
	Categories []budget.Category
	CSRFToken  string
	Return     string
	OneAccount bool
	// Saved marks a row just changed by an htmx save, for a brief highlight.
	Saved bool
	// Error explains a refused change, shown in the row itself.
	Error string
}

// RowClass is the <tr> class list: "saved" for a row just changed, "uncat" for
// one with no category. Empty when neither applies.
func (r txnRow) RowClass() string {
	var c []string
	if r.Saved {
		c = append(c, "saved")
	}
	if !r.CategoryID.Valid {
		c = append(c, "uncat")
	}
	return strings.Join(c, " ")
}

// Transactions lists transactions by month, account, category and description,
// each with its category form. The month defaults to the latest with data.
func (h *Handler) Transactions(w http.ResponseWriter, r *http.Request) {
	months, err := h.ledger.Months()
	if err != nil {
		h.fail(w, "transactions: months", err)
		return
	}
	accts, err := h.ledger.Accounts()
	if err != nil {
		h.fail(w, "transactions: accounts", err)
		return
	}
	cats, err := h.budget.Categories(true)
	if err != nil {
		h.fail(w, "transactions: categories", err)
		return
	}

	f, category := txnFilter(r.URL.Query(), months, accts, cats)

	txns, err := h.ledger.Transactions(f, maxListed)
	if err != nil {
		h.fail(w, "transactions: list", err)
		return
	}
	totals, err := h.ledger.Totals(f)
	if err != nil {
		h.fail(w, "transactions: totals", err)
		return
	}
	p := newPage(r, "Transactions", "transactions")
	ret := "/transactions"
	if r.URL.RawQuery != "" {
		ret += "?" + r.URL.RawQuery
	}
	rows := make([]txnRow, len(txns))
	for i, t := range txns {
		rows[i] = txnRow{Txn: t, Categories: offered(cats, t.CategoryID.Int64), CSRFToken: p.CSRFToken, Return: ret, OneAccount: f.AccountID != 0}
	}
	h.render(w, http.StatusOK, "transactions.html", txnsData{
		page:   p,
		Months: months, Accounts: accts, Categories: active(cats),
		Month: f.Month, AccountID: f.AccountID, Category: category, Query: f.Query,
		Rows: rows, Totals: totals, Truncated: totals.Count > len(txns),
		OneAccount: f.AccountID != 0,
		ExportURL:  exportURL(f, category),
	})
}

// txnFilter reads the transaction filter from a query string, keeping only
// values that name something real. The page and its CSV export both use it, so
// "download what I am looking at" downloads exactly that.
func txnFilter(q url.Values, months []string, accts []ledger.Account, cats []budget.Category) (f ledger.Filter, category string) {
	f.Query = truncate(strings.TrimSpace(q.Get("q")), 100)
	switch m := q.Get("month"); {
	case m == "all":
	case monthRe.MatchString(m):
		f.Month = m
	case len(months) > 0:
		// Absent or malformed: the latest month, rather than an error page
		// for a hand-edited URL.
		f.Month = months[0]
	}
	if id, err := strconv.ParseInt(q.Get("account"), 10, 64); err == nil {
		for _, a := range accts {
			if a.ID == id {
				f.AccountID = id
			}
		}
	}
	switch c := q.Get("category"); c {
	case "none":
		f.Uncategorised, category = true, "none"
	default:
		if id, err := strconv.ParseInt(c, 10, 64); err == nil {
			for _, cat := range cats {
				if cat.ID == id {
					f.CategoryID, category = id, c
				}
			}
		}
	}
	return f, category
}

// exportURL is the CSV of a resolved filter. Built from the filter, not copied
// from the request, so a page that defaulted to the latest month exports that
// month rather than everything.
func exportURL(f ledger.Filter, category string) string {
	v := url.Values{}
	v.Set("month", cmp.Or(f.Month, "all"))
	if f.AccountID != 0 {
		v.Set("account", strconv.FormatInt(f.AccountID, 10))
	}
	if category != "" {
		v.Set("category", category)
	}
	if f.Query != "" {
		v.Set("q", f.Query)
	}
	return "/transactions.csv?" + v.Encode()
}

// active is the categories a form offers: the unarchived ones.
func active(cats []budget.Category) []budget.Category {
	var out []budget.Category
	for _, c := range cats {
		if !c.Archived {
			out = append(out, c)
		}
	}
	return out
}

// offered is active plus the transaction's current category if it has since
// been archived, so the select still shows the truth rather than a blank.
func offered(cats []budget.Category, current int64) []budget.Category {
	out := active(cats)
	for _, c := range cats {
		if c.Archived && c.ID == current {
			out = append(out, c)
		}
	}
	return out
}

// Categorise sets one transaction's category from its row form. Through htmx it
// answers with the updated row; without JavaScript it is an ordinary form post
// that redirects back to the list it came from.
func (h *Handler) Categorise(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	// Strict: "none" clears, a number sets, and anything else -- including an
	// absent field -- is a malformed request, never a silent clear.
	var catID int64
	switch v := r.PostFormValue("category"); v {
	case "none":
	default:
		if catID, err = strconv.ParseInt(v, 10, 64); err != nil || catID <= 0 {
			http.Error(w, "Bad request", http.StatusBadRequest)
			return
		}
	}
	me, _ := middleware.User(r)
	_, err = h.budget.Categorise(id, catID, budget.Actor{UserID: me.ID, Via: budget.ViaWeb})
	msg, status := "", http.StatusOK
	switch {
	case errors.Is(err, budget.ErrArchived):
		msg, status = "That category is archived; choose another.", http.StatusUnprocessableEntity
	case errors.Is(err, budget.ErrNotFound):
		// The transaction, or the chosen category, no longer exists.
		if _, terr := h.ledger.Txn(id); terr != nil {
			http.Error(w, "That transaction no longer exists.", http.StatusNotFound)
			return
		}
		msg, status = "That category no longer exists.", http.StatusUnprocessableEntity
	case err != nil:
		h.fail(w, "categorise", err)
		return
	}

	ret := safeReturn(r.PostFormValue("return"), "/transactions")
	if r.Header.Get("HX-Request") != "true" {
		if msg != "" {
			http.Error(w, msg, status)
			return
		}
		http.Redirect(w, r, ret, http.StatusSeeOther)
		return
	}
	// htmx: answer with the row itself, carrying the refusal if there was one,
	// so the table never has a bare error message swapped into it.
	t, err := h.ledger.Txn(id)
	if err != nil {
		h.fail(w, "categorise: reload", err)
		return
	}
	cats, err := h.budget.Categories(true)
	if err != nil {
		h.fail(w, "categorise: categories", err)
		return
	}
	h.renderPartial(w, status, "txn_row", txnRow{
		Txn: t, Categories: offered(cats, t.CategoryID.Int64), CSRFToken: nosurf.Token(r), Return: ret,
		OneAccount: r.PostFormValue("one_account") == "1", Saved: msg == "", Error: msg,
	})
}

// safeReturn allows a return target only under prefix on this server, so a
// form's return field cannot become an open redirect.
func safeReturn(v, prefix string) string {
	if v != prefix && !strings.HasPrefix(v, prefix+"?") || strings.ContainsAny(v, "\r\n\\") {
		return prefix
	}
	return v
}

// --- Import ------------------------------------------------------------------

// MaxUploadBytes bounds one import POST. FNB exports are a few KB; this allows a
// batch of years of old CSVs. It is enforced in front of the CSRF layer (see
// Server), because nosurf parses a multipart body before any handler runs.
const MaxUploadBytes = 30 << 20

const maxUploadFiles = 30

type importData struct {
	page
	Imports []ledger.Import
	Error   string
}

func (h *Handler) ImportPage(w http.ResponseWriter, r *http.Request) {
	h.renderImport(w, r, http.StatusOK, "")
}

func (h *Handler) renderImport(w http.ResponseWriter, r *http.Request, status int, msg string) {
	imports, err := h.ledger.Imports(30)
	if err != nil {
		h.fail(w, "import: history", err)
		return
	}
	h.render(w, status, "import.html", importData{page: newPage(r, "Import", "import"), Imports: imports, Error: msg})
}

// ImportUpload imports uploaded FNB exports: CSV, OFX, or the ZIPs exactly as FNB
// downloads them. Every file gets a row in the history, including the ones that
// failed and why, so nothing is silently dropped.
func (h *Handler) ImportUpload(w http.ResponseWriter, r *http.Request) {
	// The whole body is already capped at MaxUploadBytes, so it stays in memory:
	// nothing spills to temp files, which the distroless image need not have
	// anywhere writable. (nosurf has usually parsed it already; this reuses that.)
	if err := r.ParseMultipartForm(MaxUploadBytes); err != nil {
		h.renderImport(w, r, http.StatusBadRequest, "The upload could not be read. Is it under 30 MB?")
		return
	}
	defer r.MultipartForm.RemoveAll()
	files := r.MultipartForm.File["files"]
	switch {
	case len(files) == 0:
		h.renderImport(w, r, http.StatusUnprocessableEntity, "Choose at least one file to import.")
		return
	case len(files) > maxUploadFiles:
		h.renderImport(w, r, http.StatusUnprocessableEntity, "That is more than 30 files; import them in batches.")
		return
	}

	me, _ := middleware.User(r)
	now := time.Now()
	for _, fh := range files {
		name := truncate(filepath.Base(fh.Filename), 200)
		data, err := readUpload(fh)
		if err == nil {
			var sts []importer.Statement
			if sts, err = importer.Parse(importer.File{Name: name, Data: data}); err == nil {
				for _, st := range sts {
					im, ierr := h.ledger.Import(st, ledger.SourceUpload, name, me.ID, now)
					if ierr != nil {
						log.Printf("import %s: %v", name, ierr)
						h.recordFailure(name, me.ID, ierr, now)
						continue
					}
					// New transactions get their categories from the rules
					// straight away; a rule failure leaves them uncategorised,
					// which is visible and fixable, so it does not fail the import.
					if _, err := h.budget.ApplyRules(budget.Scope{ImportID: im.ID}); err != nil {
						log.Printf("import %s: apply rules: %v", name, err)
					}
				}
				continue
			}
		}
		log.Printf("import %s: %v", name, err)
		h.recordFailure(name, me.ID, err, now)
	}
	http.Redirect(w, r, "/import?notice=imported", http.StatusSeeOther)
}

func readUpload(fh *multipart.FileHeader) ([]byte, error) {
	f, err := fh.Open()
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}

func (h *Handler) recordFailure(name string, userID int64, cause error, now time.Time) {
	if err := h.ledger.RecordFailure(ledger.SourceUpload, name, userID, cause, now); err != nil {
		log.Printf("record failed import %s: %v", name, err)
	}
}

// --- Accounts ----------------------------------------------------------------

type accountsData struct {
	page
	Accounts []ledger.Account
	Kinds    []string
	// ErrorID and Error report a refused save against its own row.
	ErrorID int64
	Error   string
}

func (h *Handler) Accounts(w http.ResponseWriter, r *http.Request) {
	h.renderAccounts(w, r, http.StatusOK, 0, "")
}

func (h *Handler) renderAccounts(w http.ResponseWriter, r *http.Request, status int, errID int64, msg string) {
	accts, err := h.ledger.Accounts()
	if err != nil {
		h.fail(w, "accounts: list", err)
		return
	}
	h.render(w, status, "accounts.html", accountsData{
		page: newPage(r, "Accounts", "accounts"), Accounts: accts, Kinds: ledger.Kinds, ErrorID: errID, Error: msg,
	})
}

// AccountUpdate renames and reclassifies an account.
func (h *Handler) AccountUpdate(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(r.PostFormValue("name"))
	kind := r.PostFormValue("kind")
	switch {
	case name == "":
		h.renderAccounts(w, r, http.StatusUnprocessableEntity, id, "Give the account a name.")
		return
	case utf8.RuneCountInString(name) > 60:
		h.renderAccounts(w, r, http.StatusUnprocessableEntity, id, "Keep the name under 60 characters.")
		return
	case !ledger.ValidKind(kind):
		// Strict: an absent or unknown kind is a malformed request, never a
		// silent default.
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	ok, err := h.ledger.UpdateAccount(id, name, kind)
	if err != nil {
		h.fail(w, "accounts: update", err)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	http.Redirect(w, r, "/accounts?notice=saved", http.StatusSeeOther)
}

// --- helpers -----------------------------------------------------------------

func (h *Handler) fail(w http.ResponseWriter, what string, err error) {
	log.Printf("%s: %v", what, err)
	http.Error(w, "Internal error", http.StatusInternalServerError)
}

func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// AccountFetch switches the daily fetch on (fetch=1) or off (fetch=0) for an
// account. Strict: anything else is a bad request, never a silent default.
func (h *Handler) AccountFetch(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	var on bool
	switch r.PostFormValue("fetch") {
	case "1":
		on = true
	case "0":
	default:
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	ok, err := h.ledger.SetFetch(id, on)
	if err != nil {
		h.fail(w, "accounts: fetch", err)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	http.Redirect(w, r, "/accounts?notice=saved", http.StatusSeeOther)
}
