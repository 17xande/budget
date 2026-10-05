package handler

import (
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/17xande-dev/budget/internal/importer"
	"github.com/17xande-dev/budget/internal/ledger"
	"github.com/17xande-dev/budget/internal/middleware"
)

// --- Overview ----------------------------------------------------------------

type homeData struct {
	page
	Accounts []ledger.Account
	Imports  []ledger.Import
}

func (h *Handler) Home(w http.ResponseWriter, r *http.Request) {
	accts, err := h.ledger.Accounts()
	if err != nil {
		h.fail(w, "home: accounts", err)
		return
	}
	imports, err := h.ledger.Imports(3)
	if err != nil {
		h.fail(w, "home: imports", err)
		return
	}
	h.render(w, http.StatusOK, "home.html", homeData{page: newPage(r, "Overview", "home"), Accounts: accts, Imports: imports})
}

// --- Transactions --------------------------------------------------------------

// maxListed caps one page of transactions. A month is a few hundred rows; the cap
// only bites on "all months", where the page says how many it is not showing.
const maxListed = 2000

var monthRe = regexp.MustCompile(`^\d{4}-(0[1-9]|1[0-2])$`)

type txnsData struct {
	page
	Months    []string
	Accounts  []ledger.Account
	Month     string // "" means all months
	AccountID int64
	Query     string
	Txns      []ledger.Txn
	Totals    ledger.Totals
	// Truncated is true when Totals counts more rows than Txns holds.
	Truncated bool
	// OneAccount shows the balance column, which only means anything for a
	// single account.
	OneAccount bool
}

// Transactions lists transactions by month, account and description. The month
// defaults to the latest one with data, so the page opens on something useful.
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

	q := r.URL.Query()
	f := ledger.Filter{Query: truncate(strings.TrimSpace(q.Get("q")), 100)}
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
	h.render(w, http.StatusOK, "transactions.html", txnsData{
		page:   newPage(r, "Transactions", "transactions"),
		Months: months, Accounts: accts,
		Month: f.Month, AccountID: f.AccountID, Query: f.Query,
		Txns: txns, Totals: totals, Truncated: totals.Count > len(txns),
		OneAccount: f.AccountID != 0,
	})
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
					if _, ierr := h.ledger.Import(st, ledger.SourceUpload, name, me.ID, now); ierr != nil {
						log.Printf("import %s: %v", name, ierr)
						h.recordFailure(name, me.ID, ierr, now)
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
