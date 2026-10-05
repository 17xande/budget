// Package fnbtest is a fake FNB online banking server for tests: it answers the
// requests docs/fnb-flow.md records, checks they carry what the real one needs,
// and serves fixture exports as the ZIPs FNB downloads.
package fnbtest

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/17xande/budget/internal/fnb"
)

// SessionCookie is what a fake login hands out and the fake server requires.
const SessionCookie = "JSESSIONID"

type Account struct {
	Ref, Product string
	// OFX is the export's content, zipped on download; empty means FNB's
	// "nothing in the window" empty answer.
	OFX []byte
}

// Server is the fake. Calls records the navs in order, for asserting the flow.
type Server struct {
	*httptest.Server
	t         *testing.T
	mu        sync.Mutex
	Accounts  []Account
	Calls     []string
	LoggedOff bool
	// Opened is every account reference whose history was opened.
	Opened []string
	// Token is the session cookie value the fake accepts.
	Token string
	// current is the account the last history redirect opened, as FNB keeps it
	// in the server-side session.
	current string
}

func New(t *testing.T, accounts ...Account) *Server {
	s := &Server{t: t, Accounts: accounts, Token: "session-ok"}
	s.Server = httptest.NewServer(http.HandlerFunc(s.handle))
	t.Cleanup(s.Close)
	return s
}

// Base is the banking base URL to give fnb.NewSession.
func (s *Server) Base() string { return s.URL + "/banking/" }

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.URL.Path != "/banking/Controller" {
		http.NotFound(w, r)
		return
	}
	if r.Header.Get("X-Requested-With") != "XMLHttpRequest" || r.Header.Get("User-Agent") == "" {
		s.t.Errorf("fnbtest: request without the browser's XHR headers: %v", r.Header)
	}
	if c, err := r.Cookie(SessionCookie); err != nil || c.Value != s.Token {
		// The real server answers an unauthenticated request with an empty 200.
		w.WriteHeader(http.StatusOK)
		return
	}
	r.ParseForm()
	n := r.URL.Query().Get("nav")
	if n == "" {
		n = r.PostForm.Get("nav")
	}
	s.mu.Lock()
	s.Calls = append(s.Calls, n)
	s.mu.Unlock()

	switch n {
	case "accounts.summaryofaccountbalances.navigator.SummaryOfAccountBalances":
		for _, a := range s.Accounts {
			fmt.Fprintf(w, `<a onclick="go('/banking/Controller?nav=navigator.AccountView&amp;accountRefNum=%s&amp;productCode=%s')">x</a>`, a.Ref, a.Product)
		}
	case "transactionhistory.navigator.TransactionHistoryRedirect":
		if ref := r.URL.Query().Get("anrfn"); ref != "" {
			s.current = ref
			s.Opened = append(s.Opened, ref)
		} else if r.PostForm.Get("anrfn") != s.current || r.PostForm.Get("stdQuickCustom") != "quick" || r.PostForm.Get("dateFrom") == "" {
			s.t.Errorf("fnbtest: search does not match the opened account or lacks fields: %v", r.PostForm)
		}
	case "accounts.transactionhistory.navigator.TransactionHistoryDownloadEzi":
	case "accounts.transactionhistory.navigator.TransactionHistoryDDADownload":
		if r.PostForm.Get("downloadFormat") != "ofx" || r.PostForm.Get("doDownload") != "true" {
			s.t.Errorf("fnbtest: download asked for %v", r.PostForm)
		}
		for _, a := range s.Accounts {
			if a.Ref == s.current && len(a.OFX) > 0 {
				w.Write(zipped(a.Ref+".ofx", a.OFX))
			}
		}
	case "navigator.UserLogoff":
		s.LoggedOff = true
	default:
		s.t.Errorf("fnbtest: unexpected nav %q", n)
	}
}

func zipped(name string, data []byte) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	f, _ := zw.Create(name)
	f.Write(data)
	zw.Close()
	return buf.Bytes()
}

// Cookies is a session for this server, as a successful fake login returns.
func (s *Server) Cookies() []*http.Cookie {
	host := strings.TrimPrefix(s.URL, "http://")
	return []*http.Cookie{{Name: SessionCookie, Value: s.Token, Path: "/", Domain: strings.Split(host, ":")[0]}}
}

// Login is a browser login that hands out this server's session, or fails with
// Err -- counting attempts, since how many logins happen matters.
type Login struct {
	Srv   *Server
	Err   error
	Calls int
}

func (l *Login) Login(_ context.Context, c fnb.Credentials) (fnb.LoginResult, error) {
	l.Calls++
	if c.Username == "" || c.Password == "" {
		return fnb.LoginResult{}, errors.New("fnbtest: no credentials passed")
	}
	if l.Err != nil {
		return fnb.LoginResult{}, l.Err
	}
	return fnb.LoginResult{Cookies: l.Srv.Cookies(), UserAgent: "test-agent"}, nil
}
