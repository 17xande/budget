// Package fnb talks to FNB online banking: a headless Chrome logs in (FNB's
// login is behind Radware bot protection, whose script a real browser runs as
// designed and a plain HTTP client cannot), then plain HTTP with the session's
// cookies lists the accounts and downloads each one's OFX export.
//
// docs/fnb-flow.md records the flow this replays. FNB changes its site without
// notice; when a fetch breaks, recapture the step that broke and update both.
//
// Never spoof: no altered user agent, fingerprint or Radware token. If the
// headless browser is refused, the fetch fails and says so.
package fnb

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// BankingURL is online banking's base; the HTTP half posts to its Controller.
const BankingURL = "https://www.online.fnb.co.za/banking/"

// Account is one account as the summary page lists it: the opaque reference the
// export requests need, and FNB's product code (DDA cheque, ZFN credit card,
// TOA savings/notice, MLS loan, ...).
type Account struct {
	Ref     string
	Product string
}

// Session is a logged-in online banking session, driven over plain HTTP.
type Session struct {
	http *http.Client
	ua   string
	base string
}

// NewSession wraps the cookies a browser login produced. ua must be the
// browser's own user agent, read from it -- the session is that browser's, and
// the requests continue it rather than impersonate anything.
func NewSession(cookies []*http.Cookie, ua, base string) (*Session, error) {
	u, err := url.Parse(base)
	if err != nil {
		return nil, err
	}
	jar, _ := cookiejar.New(nil)
	jar.SetCookies(u, cookies)
	return &Session{http: &http.Client{Jar: jar, Timeout: 60 * time.Second}, ua: ua, base: base}, nil
}

// post mirrors the browser: nav and the other query values in the URL, form
// fields in an urlencoded body, and the XHR headers online banking's own script
// sends.
func (s *Session) post(ctx context.Context, query, form url.Values) ([]byte, error) {
	target := s.base + "Controller"
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	origin, _ := url.Parse(s.base)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")
	req.Header.Set("User-Agent", s.ua)
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("Referer", s.base+"main.jsp")
	req.Header.Set("Origin", origin.Scheme+"://"+origin.Host)
	res, err := s.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	// Exports are a few KB; a bound keeps a misbehaving response from becoming
	// a memory problem.
	body, err := io.ReadAll(io.LimitReader(res.Body, 20<<20))
	if err != nil {
		return nil, err
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fnb: %s answered %d", query.Get("nav"), res.StatusCode)
	}
	return body, nil
}

func nav(name string, extra ...string) url.Values {
	q := url.Values{"nav": {name}}
	for i := 0; i+1 < len(extra); i += 2 {
		q.Set(extra[i], extra[i+1])
	}
	return q
}

var accountRe = regexp.MustCompile(`accountRefNum=(-?\d+)&(?:amp;)?productCode=(\w+)`)

// ErrNoAccounts means the summary page listed nothing -- in practice, a session
// that is not really logged in (FNB answers 200 with an empty page).
var ErrNoAccounts = errors.New("fnb: the account summary listed no accounts; the session is probably not logged in")

// Accounts lists the accounts on the summary page, deduplicated, in page order.
func (s *Session) Accounts(ctx context.Context) ([]Account, error) {
	body, err := s.post(ctx, nav("accounts.summaryofaccountbalances.navigator.SummaryOfAccountBalances",
		"FARFN", "4", "actionchild", "1", "isTopMenu", "true", "targetDiv", "pageContent"), nil)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []Account
	for _, m := range accountRe.FindAllStringSubmatch(string(body), -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, Account{Ref: m[1], Product: m[2]})
		}
	}
	if len(out) == 0 {
		return nil, ErrNoAccounts
	}
	return out, nil
}

// Export downloads an account's transactions between from and to (inclusive,
// at most about 60 days back; the quick search is what FNB serves reliably) as
// the OFX ZIP FNB produces. An account with nothing in the window answers an
// empty body, returned as nil, nil.
func (s *Session) Export(ctx context.Context, a Account, from, to time.Time) ([]byte, error) {
	if _, err := s.post(ctx, nav("transactionhistory.navigator.TransactionHistoryRedirect",
		"productCode", a.Product, "anrfn", a.Ref, "initial", "initial", "FARFN", "98", "targetDiv", "pageContent"), nil); err != nil {
		return nil, fmt.Errorf("open history: %w", err)
	}
	if _, err := s.post(ctx, url.Values{"targetDiv": {"pageContent"}}, url.Values{
		"nav": {"transactionhistory.navigator.TransactionHistoryRedirect"}, "export": {"std"},
		"anrfn": {a.Ref}, "productCode": {a.Product}, "initial": {"notinitial"}, "go": {"Search"},
		"formID": {"FORMDDAHISTSELCRIT_108"}, "stdQuickCustom": {"quick"}, "isSimple": {"true"},
		"selectedQuickSearchOption": {"60"}, "dateFrom": {from.Format("2006-01-02")}, "dateTo": {to.Format("2006-01-02")},
		"valueFrom": {"0.00"}, "valueTo": {"999999999.99"}, "chequeNumberFrom": {"0"}, "chequeNumberTo": {"9999999999"},
		"key": {""}, "downloadFormat": {""}, "function": {""}, "reference": {""},
	}); err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}
	if _, err := s.post(ctx, nav("accounts.transactionhistory.navigator.TransactionHistoryDownloadEzi",
		"targetDiv", "eziPageContent"), nil); err != nil {
		return nil, fmt.Errorf("download dialog: %w", err)
	}
	body, err := s.post(ctx, nil, url.Values{
		"nav":            {"accounts.transactionhistory.navigator.TransactionHistoryDDADownload"},
		"doDownload":     {"true"},
		"downloadFormat": {"ofx"},
	})
	if err != nil {
		return nil, fmt.Errorf("download: %w", err)
	}
	if len(body) == 0 {
		return nil, nil
	}
	if !strings.HasPrefix(string(body), "PK") {
		// Not a ZIP and not empty: an error page, or FNB changed the export.
		return nil, fmt.Errorf("fnb: the export for a %s account was not a ZIP (%d bytes)", a.Product, len(body))
	}
	return body, nil
}

// Logoff ends the session. Always called, deferred, once logged in.
func (s *Session) Logoff(ctx context.Context) error {
	_, err := s.post(ctx, nav("navigator.UserLogoff", "FARFN", "849", "actionchild", "1797", "isTopMenu", "true"), nil)
	return err
}
