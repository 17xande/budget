package fnb_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/17xande-dev/budget/internal/fnb"
)

// TestChromeLogin drives a real headless Chrome through a fake of FNB's login --
// the consent overlay that swallows the Login click on a fresh profile, the
// login form, and the "Linked Profiles" chooser -- and checks the session comes
// out as cookies. It needs a browser that can reach this test's server:
//
//	docker run -d --rm --name chrome-test --network host chromedp/headless-shell:155.0.8059.26
//	FNB_TEST_CHROME_URL=ws://127.0.0.1:9222/ go test -run TestChromeLogin -v ./internal/fnb
//
// It never contacts FNB.
func TestChromeLogin(t *testing.T) {
	chromeURL := os.Getenv("FNB_TEST_CHROME_URL")
	if chromeURL == "" {
		t.Skip("FNB_TEST_CHROME_URL not set")
	}
	srv := httptest.NewServer(fakeSite(t))
	defer srv.Close()
	ch := fnb.Chrome{URL: chromeURL, HomeURL: srv.URL + "/", BankingURL: srv.URL + "/banking/"}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	res, err := ch.Login(ctx, fnb.Credentials{Username: "someone", Password: "right", Profile: "ALEX PERSONAL"})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	got := map[string]string{}
	for _, c := range res.Cookies {
		got[c.Name] = c.Value
	}
	if got["JSESSIONID"] != "session-ok" || got["profile"] != "personal" {
		t.Errorf("cookies %v: want the session and the personal profile", got)
	}
	if _, ok := got["frgCPP"]; ok {
		t.Error("the consent cookie was passed on")
	}
	if !strings.Contains(res.UserAgent, "Chrome") {
		t.Errorf("user agent %q", res.UserAgent)
	}

	// A profile name matching nothing is refused, not guessed.
	_, err = ch.Login(ctx, fnb.Credentials{Username: "someone", Password: "right", Profile: "NOBODY"})
	if err == nil || !strings.Contains(err.Error(), "profile not found") {
		t.Errorf("unknown profile: %v", err)
	}
	// A wrong password never reaches the banking page.
	_, err = ch.Login(ctx, fnb.Credentials{Username: "someone", Password: "wrong", Profile: "ALEX PERSONAL"})
	if err == nil || !strings.Contains(err.Error(), "could not log in") {
		t.Errorf("wrong password: %v", err)
	}
}

// fakeSite is the shape of FNB's login as recorded in docs/fnb-flow.md.
func fakeSite(t *testing.T) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		// The overlay covers the whole page until consent is saved, as FNB's does.
		w.Write([]byte(`<!doctype html><html><body>
<style>#overlay{position:fixed;inset:0;background:#000a;z-index:10}#cookieSettings{display:none}</style>
<div id="overlay"><button id="cookieSettingsBtn" onclick="document.getElementById('cookieSettings').style.display='block'">Manage your settings</button>
<div id="cookieSettings"><input type="checkbox" checked disabled><input type="checkbox" id="analyticsStorage" checked>
<button id="cookieSaveSettingsBtn" onclick="document.cookie='frgCPP={&quot;a&quot;:&quot;denied&quot;}; path=/';document.getElementById('overlay').remove()">Save settings</button></div></div>
<form id="LOGIN_FORM" method="post" action="/login"><input id="user" name="Username"><input id="pass" name="Password" type="password"><input id="OBSubmit" type="submit" value="Login"></form>
Register</body></html>`))
	})
	mux.HandleFunc("POST /login", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.PostFormValue("Username") != "someone" || r.PostFormValue("Password") != "right" {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "JSESSIONID", Value: "session-ok", Path: "/"})
		http.Redirect(w, r, "/banking/main.jsp", http.StatusSeeOther)
	})
	mux.HandleFunc("GET /banking/main.jsp", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		if _, err := r.Cookie("profile"); err != nil {
			// Each row's nearest "row" element holds only its button, as on FNB.
			w.Write([]byte(`<!doctype html><html><body><span>Log off</span><h1>Linked Profiles</h1>
<div class="tableRow"><div class="cell">*ALEX T/A BUSINESS</div><div class="cell"><div class="tableRowGroupInner"><span class="eziLink" onclick="location='/banking/choose?p=business'">Select</span></div></div></div>
<div class="tableRow"><div class="cell">ALEX PERSONAL</div><div class="cell"><div class="tableRowGroupInner"><span class="eziLink" onclick="location='/banking/choose?p=personal'">Select</span></div></div></div>
</body></html>`))
			return
		}
		w.Write([]byte(`<!doctype html><html><body><span>Log off</span><h1>My Bank Accounts</h1></body></html>`))
	})
	mux.HandleFunc("GET /banking/choose", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "profile", Value: r.URL.Query().Get("p"), Path: "/"})
		http.Redirect(w, r, "/banking/main.jsp", http.StatusSeeOther)
	})
	return mux
}
