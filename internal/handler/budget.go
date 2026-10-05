package handler

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/17xande-dev/budget/internal/budget"
	"github.com/17xande-dev/budget/internal/fetch"
	"github.com/17xande-dev/budget/internal/ledger"
	"github.com/17xande-dev/budget/internal/middleware"
	"github.com/17xande-dev/budget/internal/money"
)

// sast is where the household's months begin and end.
var sast = func() *time.Location {
	l, err := time.LoadLocation("Africa/Johannesburg")
	if err != nil {
		return time.FixedZone("SAST", 2*60*60)
	}
	return l
}()

func webActor(r *http.Request) budget.Actor {
	me, _ := middleware.User(r)
	return budget.Actor{UserID: me.ID, Via: budget.ViaWeb}
}

// monthParam reads ?month=YYYY-MM, defaulting to the latest month with
// transactions, else the current month in South Africa.
func (h *Handler) monthParam(r *http.Request) (string, error) {
	if m := r.URL.Query().Get("month"); monthRe.MatchString(m) {
		return m, nil
	}
	months, err := h.ledger.Months()
	if err != nil {
		return "", err
	}
	if len(months) > 0 {
		return months[0], nil
	}
	return time.Now().In(sast).Format("2006-01"), nil
}

func shiftMonth(m string, n int) string {
	t, err := time.Parse("2006-01", m)
	if err != nil {
		return m
	}
	return t.AddDate(0, n, 0).Format("2006-01")
}

// --- Overview: the month against its targets -----------------------------------

type homeData struct {
	page
	Month      string
	Prev, Next string
	Summary    budget.Summary
	HasCats    bool
	HasTxns    bool
	LastImport *ledger.Import
	// FetchFailed is the last bank fetch when it failed, for a banner: a fetch
	// that silently stops would leave the budget quietly out of date.
	FetchFailed *fetch.Run
}

func (h *Handler) Home(w http.ResponseWriter, r *http.Request) {
	month, err := h.monthParam(r)
	if err != nil {
		h.fail(w, "home: month", err)
		return
	}
	sum, err := h.budget.Summary(month)
	if err != nil {
		h.fail(w, "home: summary", err)
		return
	}
	cats, err := h.budget.Categories(false)
	if err != nil {
		h.fail(w, "home: categories", err)
		return
	}
	months, err := h.ledger.Months()
	if err != nil {
		h.fail(w, "home: months", err)
		return
	}
	imports, err := h.ledger.Imports(1)
	if err != nil {
		h.fail(w, "home: imports", err)
		return
	}
	d := homeData{
		page:  newPage(r, "Overview", "home"),
		Month: month, Prev: shiftMonth(month, -1), Next: shiftMonth(month, 1),
		Summary: sum, HasCats: len(cats) > 0, HasTxns: len(months) > 0,
	}
	if len(imports) > 0 {
		d.LastImport = &imports[0]
	}
	if h.fetcher != nil {
		last, err := h.fetcher.Last()
		if err != nil {
			h.fail(w, "home: fetch status", err)
			return
		}
		if last != nil && last.Status == "failed" {
			d.FetchFailed = last
		}
	}
	h.render(w, http.StatusOK, "home.html", d)
}

// --- Targets -------------------------------------------------------------------

type targetsData struct {
	page
	Month      string
	Categories []budget.Category
	// Values are the inputs as shown: the stored target, or what was typed
	// when the form is re-rendered with an error.
	Values map[int64]string
	Errors map[int64]string
}

func (h *Handler) Targets(w http.ResponseWriter, r *http.Request) {
	month, err := h.monthParam(r)
	if err != nil {
		h.fail(w, "targets: month", err)
		return
	}
	targets, err := h.budget.Targets(month)
	if err != nil {
		h.fail(w, "targets: load", err)
		return
	}
	vals := map[int64]string{}
	for id, c := range targets {
		vals[id] = money.Decimal(c)
	}
	h.renderTargets(w, r, http.StatusOK, month, vals, nil)
}

func (h *Handler) renderTargets(w http.ResponseWriter, r *http.Request, status int, month string, vals, errs map[int64]string) {
	cats, err := h.budget.Categories(false)
	if err != nil {
		h.fail(w, "targets: categories", err)
		return
	}
	var budgetable []budget.Category
	for _, c := range cats {
		if c.Kind != budget.KindTransfer {
			budgetable = append(budgetable, c)
		}
	}
	h.render(w, status, "targets.html", targetsData{
		page: newPage(r, "Targets for "+monthLabel(month), "home"), Month: month, Categories: budgetable, Values: vals, Errors: errs,
	})
}

// parseRand reads an amount as people type it: "1500", "1 500.50", "R1500",
// "1500,50". An empty string is "no target" (-1).
func parseRand(s string) (int64, error) {
	s = strings.NewReplacer(" ", "", " ", "", "R", "", "r", "").Replace(strings.TrimSpace(s))
	if s == "" {
		return -1, nil
	}
	if strings.Contains(s, ",") && !strings.Contains(s, ".") {
		s = strings.Replace(s, ",", ".", 1) // a South African decimal comma
	}
	c, err := money.ParseCents(s)
	if err != nil || c < 0 {
		return 0, errors.New("enter an amount like 1500 or 1500.50")
	}
	return c, nil
}

// TargetsSave stores a whole month's targets from one form. Every field is
// validated before anything is written.
func (h *Handler) TargetsSave(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	month := r.PostFormValue("month")
	if !monthRe.MatchString(month) {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	targets := map[int64]int64{}
	vals := map[int64]string{}
	errs := map[int64]string{}
	for key, v := range r.PostForm {
		idText, ok := strings.CutPrefix(key, "target_")
		if !ok {
			continue
		}
		id, err := strconv.ParseInt(idText, 10, 64)
		if err != nil {
			http.Error(w, "Bad request", http.StatusBadRequest)
			return
		}
		vals[id] = v[0]
		c, err := parseRand(v[0])
		if err != nil {
			errs[id] = err.Error()
			continue
		}
		targets[id] = c
	}
	if len(errs) > 0 {
		h.renderTargets(w, r, http.StatusUnprocessableEntity, month, vals, errs)
		return
	}
	if err := h.budget.SetTargets(month, targets, webActor(r)); err != nil {
		if errors.Is(err, budget.ErrNotFound) || errors.Is(err, budget.ErrInvalid) {
			http.Error(w, "Bad request", http.StatusBadRequest)
			return
		}
		h.fail(w, "targets: save", err)
		return
	}
	http.Redirect(w, r, "/?"+url.Values{"month": {month}, "notice": {"saved"}}.Encode(), http.StatusSeeOther)
}

// TargetsCopy copies the previous month's targets into this one, keeping any
// this month already has.
func (h *Handler) TargetsCopy(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	month := r.PostFormValue("month")
	if !monthRe.MatchString(month) {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	if _, err := h.budget.CopyTargets(shiftMonth(month, -1), month, webActor(r)); err != nil {
		h.fail(w, "targets: copy", err)
		return
	}
	http.Redirect(w, r, "/budget/targets?"+url.Values{"month": {month}, "notice": {"copied"}}.Encode(), http.StatusSeeOther)
}

// --- Categories ----------------------------------------------------------------

type categoriesData struct {
	page
	Categories []budget.Category
	Kinds      []string
	Name, Kind string // the create form, kept on error
	Error      string
	ErrorID    int64 // a row's own error, 0 for the create form
}

func (h *Handler) Categories(w http.ResponseWriter, r *http.Request) {
	h.renderCategories(w, r, http.StatusOK, categoriesData{Kind: budget.KindExpense})
}

func (h *Handler) renderCategories(w http.ResponseWriter, r *http.Request, status int, d categoriesData) {
	cats, err := h.budget.Categories(true)
	if err != nil {
		h.fail(w, "categories", err)
		return
	}
	d.page = newPage(r, "Categories", "categories")
	d.Categories, d.Kinds = cats, budget.Kinds
	h.render(w, status, "categories.html", d)
}

func categoryError(err error) string {
	switch {
	case errors.Is(err, budget.ErrNameTaken):
		return "A category with that name already exists."
	case errors.Is(err, budget.ErrInvalid):
		return "Give it a name of up to 60 characters."
	}
	return ""
}

func (h *Handler) CategoryCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	name, kind := r.PostFormValue("name"), r.PostFormValue("kind")
	if !budget.ValidKind(kind) {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	if _, err := h.budget.CreateCategory(name, kind, webActor(r)); err != nil {
		if msg := categoryError(err); msg != "" {
			h.renderCategories(w, r, http.StatusUnprocessableEntity, categoriesData{Name: name, Kind: kind, Error: msg})
			return
		}
		h.fail(w, "categories: create", err)
		return
	}
	http.Redirect(w, r, "/categories?notice=saved", http.StatusSeeOther)
}

func (h *Handler) CategoryUpdate(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	kind := r.PostFormValue("kind")
	if !budget.ValidKind(kind) {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	if err := h.budget.UpdateCategory(id, r.PostFormValue("name"), kind, webActor(r)); err != nil {
		if errors.Is(err, budget.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		if msg := categoryError(err); msg != "" {
			h.renderCategories(w, r, http.StatusUnprocessableEntity, categoriesData{Kind: budget.KindExpense, Error: msg, ErrorID: id})
			return
		}
		h.fail(w, "categories: update", err)
		return
	}
	http.Redirect(w, r, "/categories?notice=saved", http.StatusSeeOther)
}

// CategoryArchive archives (archived=1) or restores (archived=0) a category.
// The value is parsed strictly: an absent or unknown one is a bad request, never
// a silent default in either direction.
func (h *Handler) CategoryArchive(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	var archived bool
	switch r.PostFormValue("archived") {
	case "1":
		archived = true
	case "0":
	default:
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	if err := h.budget.SetArchived(id, archived, webActor(r)); err != nil {
		if errors.Is(err, budget.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		h.fail(w, "categories: archive", err)
		return
	}
	http.Redirect(w, r, "/categories?notice=saved", http.StatusSeeOther)
}

func (h *Handler) CategoryStarter(w http.ResponseWriter, r *http.Request) {
	if _, err := h.budget.SeedStarter(webActor(r)); err != nil {
		h.fail(w, "categories: starter", err)
		return
	}
	http.Redirect(w, r, "/categories?notice=saved", http.StatusSeeOther)
}

// --- Rules -----------------------------------------------------------------------

type rulesData struct {
	page
	Rules      []budget.Rule
	Categories []budget.Category
	Accounts   []ledger.Account
	// The create form, prefilled from ?txn= or kept on error.
	Pattern    string
	CategoryID int64
	AccountID  int64
	Error      string
}

func (h *Handler) Rules(w http.ResponseWriter, r *http.Request) {
	d := rulesData{}
	// "Create a rule from this transaction": start from its description and
	// category; the person trims it down to the merchant.
	if id, err := strconv.ParseInt(r.URL.Query().Get("txn"), 10, 64); err == nil {
		if t, err := h.ledger.Txn(id); err == nil {
			d.Pattern, d.CategoryID = t.Description, t.CategoryID.Int64
		}
	}
	h.renderRules(w, r, http.StatusOK, d)
}

func (h *Handler) renderRules(w http.ResponseWriter, r *http.Request, status int, d rulesData) {
	rules, err := h.budget.Rules()
	if err != nil {
		h.fail(w, "rules", err)
		return
	}
	cats, err := h.budget.Categories(false)
	if err != nil {
		h.fail(w, "rules: categories", err)
		return
	}
	accts, err := h.ledger.Accounts()
	if err != nil {
		h.fail(w, "rules: accounts", err)
		return
	}
	d.page = newPage(r, "Rules", "rules")
	d.Rules, d.Categories, d.Accounts = rules, cats, accts
	h.render(w, status, "rules.html", d)
}

// ruleForm reads and validates a rule form.
func ruleForm(r *http.Request) (pattern string, cat, acct int64, ok bool) {
	pattern = r.PostFormValue("pattern")
	cat, err := strconv.ParseInt(r.PostFormValue("category"), 10, 64)
	if err != nil || cat <= 0 {
		return "", 0, 0, false
	}
	switch v := r.PostFormValue("account"); v {
	case "all":
	default:
		if acct, err = strconv.ParseInt(v, 10, 64); err != nil || acct <= 0 {
			return "", 0, 0, false
		}
	}
	return pattern, cat, acct, true
}

func (h *Handler) RuleCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	pattern, cat, acct, ok := ruleForm(r)
	if !ok {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	id, err := h.budget.SaveRule(0, pattern, cat, acct, webActor(r))
	if err != nil {
		msg := ""
		switch {
		case errors.Is(err, budget.ErrInvalid):
			msg = "Enter the text to look for, up to 60 characters."
		case errors.Is(err, budget.ErrNotFound), errors.Is(err, budget.ErrArchived):
			msg = "Choose a category."
		default:
			h.fail(w, "rules: create", err)
			return
		}
		h.renderRules(w, r, http.StatusUnprocessableEntity, rulesData{Pattern: pattern, CategoryID: cat, AccountID: acct, Error: msg})
		return
	}
	notice := "rule_saved"
	// Applying now is opt-in per submission; absent means "not now", which is
	// the safe direction -- nothing changes that the person did not ask for.
	if r.PostFormValue("apply") == "1" {
		n, err := h.budget.ApplyRules(budget.Scope{RuleID: id})
		if err != nil {
			h.fail(w, "rules: apply", err)
			return
		}
		notice = appliedNotice(n)
	}
	http.Redirect(w, r, "/rules?notice="+notice, http.StatusSeeOther)
}

func appliedNotice(n int) string {
	switch {
	case n == 0:
		return "rule_applied_none"
	case n == 1:
		return "rule_applied_one"
	default:
		return "rule_applied_many"
	}
}

func (h *Handler) RuleUpdate(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	pattern, cat, acct, ok := ruleForm(r)
	if !ok {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	if _, err := h.budget.SaveRule(id, pattern, cat, acct, webActor(r)); err != nil {
		switch {
		case errors.Is(err, budget.ErrNotFound):
			http.NotFound(w, r)
		case errors.Is(err, budget.ErrInvalid), errors.Is(err, budget.ErrArchived):
			h.renderRules(w, r, http.StatusUnprocessableEntity, rulesData{Error: "That rule needs some text and an active category."})
		default:
			h.fail(w, "rules: update", err)
		}
		return
	}
	http.Redirect(w, r, "/rules?notice=rule_saved", http.StatusSeeOther)
}

func (h *Handler) RuleDelete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := h.budget.DeleteRule(id, webActor(r)); err != nil {
		if errors.Is(err, budget.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		h.fail(w, "rules: delete", err)
		return
	}
	http.Redirect(w, r, "/rules?notice=rule_deleted", http.StatusSeeOther)
}

// RuleApply runs one rule over every uncategorised transaction.
func (h *Handler) RuleApply(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if _, err := h.budget.Rule(id); errors.Is(err, budget.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	n, err := h.budget.ApplyRules(budget.Scope{RuleID: id})
	if err != nil {
		h.fail(w, "rules: apply", err)
		return
	}
	http.Redirect(w, r, "/rules?notice="+appliedNotice(n), http.StatusSeeOther)
}

// --- Settings --------------------------------------------------------------------

func (h *Handler) Settings(w http.ResponseWriter, r *http.Request) {
	h.render(w, http.StatusOK, "settings.html", struct{ page }{newPage(r, "Settings", "settings")})
}
