package fnb

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/chromedp/cdproto/storage"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/remote"
)

// Credentials are what logging in needs. Profile is the exact name of the
// profile to choose on FNB's "Linked Profiles" page, for a login linked to more
// than one (a business and a personal one, say).
type Credentials struct {
	Username string
	Password string
	Profile  string
}

// LoginResult is a logged-in browser's session, handed to plain HTTP.
type LoginResult struct {
	Cookies   []*http.Cookie
	UserAgent string
}

// Login logs in to online banking and returns the session. Chrome implements it;
// tests substitute a fake.
type Login interface {
	Login(ctx context.Context, c Credentials) (LoginResult, error)
}

// Chrome logs in with a headless Chrome reached over the DevTools protocol --
// the chromedp/headless-shell sidecar in compose.
type Chrome struct {
	// URL is the browser's DevTools endpoint, e.g. ws://chrome:9222/.
	URL string
	// HomeURL and BankingURL default to FNB's; tests point them at a fake site.
	HomeURL    string
	BankingURL string
}

const homeURL = "https://www.fnb.co.za/"

// Login drives FNB's own login page, step for step as recorded in
// docs/fnb-flow.md (and proven by the Phase 0 spike).
func (ch Chrome) Login(ctx context.Context, c Credentials) (LoginResult, error) {
	home, banking := ch.HomeURL, ch.BankingURL
	if home == "" {
		home = homeURL
	}
	if banking == "" {
		banking = BankingURL
	}
	mainPage := strings.TrimPrefix(strings.TrimPrefix(banking, "https://"), "http://") + "main.jsp"
	actx, cancelA := remote.NewAllocator(ctx, ch.URL)
	defer cancelA()
	// A fresh browser context per login, disposed when it returns. The sidecar
	// Chrome lives for weeks, and its tabs share one cookie jar: without this,
	// tomorrow's login would start inside today's session -- skipping the
	// profile choice, and keeping bank cookies in the browser between runs.
	bctx, cancelB := chromedp.NewContext(actx, chromedp.WithNewBrowserContext())
	defer cancelB()

	if err := chromedp.Do(bctx, chromedp.Navigate(home), chromedp.WaitVisible(chromedp.ID("user"))); err != nil {
		return LoginResult{}, fmt.Errorf("fnb: open the home page: %w", err)
	}

	// The cookie-consent overlay covers the page and swallows the Login click on
	// a fresh profile. Decline the optional categories, as the household does in
	// its own browser.
	if _, err := chromedp.Run(bctx, chromedp.Evaluate[string](`(async () => {
		const btn = document.getElementById('cookieSettingsBtn');
		if (!btn || !btn.offsetParent) return 'no banner';
		btn.click();
		await new Promise(r => setTimeout(r, 800));
		for (const i of document.querySelectorAll('#cookieSettings input[type=checkbox]')) {
			if (i.checked && !i.disabled) i.click();
		}
		document.getElementById('cookieSaveSettingsBtn').click();
		await new Promise(r => setTimeout(r, 800));
		return 'declined';
	})()`, chromedp.EvalAwaitPromise)); err != nil {
		return LoginResult{}, fmt.Errorf("fnb: cookie banner: %w", err)
	}

	ua, err := chromedp.Run(bctx, chromedp.Evaluate[string](`navigator.userAgent`))
	if err != nil {
		return LoginResult{}, err
	}

	if err := chromedp.Do(bctx,
		chromedp.SendKeys(chromedp.ID("user"), c.Username),
		chromedp.SendKeys(chromedp.ID("pass"), c.Password),
		chromedp.Click(chromedp.ID("OBSubmit")),
	); err != nil {
		return LoginResult{}, fmt.Errorf("fnb: login form: %w", err)
	}

	if err := waitBanking(bctx, mainPage, "log in", func(string) bool { return true }); err != nil {
		return LoginResult{}, err
	}

	// A login linked to several profiles lands on "Linked Profiles" first.
	// Choosing one is an SSO hop through full-page redirects carrying a
	// per-session token, so the browser does it rather than plain HTTP. "Log off"
	// is already on that page, so it alone does not mean logged in.
	time.Sleep(2 * time.Second)
	picked, err := chromedp.Run(bctx, chromedp.Evaluate[string](`((want) => {
		if (!document.body.innerText.includes('Linked Profiles')) return 'no profile page';
		if (!want) return 'profile needed';
		const norm = s => s.replace(/\s+/g, ' ').trim().toUpperCase();
		const isSel = s => s.innerText.trim() === 'Select';
		const hits = [...document.querySelectorAll('span.eziLink')].filter(isSel).filter(sel => {
			// The row is the largest ancestor still holding exactly one Select;
			// nearer "row" elements contain only the button.
			let row = sel;
			while (row.parentElement && [...row.parentElement.querySelectorAll('span.eziLink')].filter(isSel).length === 1) row = row.parentElement;
			return norm(row.innerText).includes(norm(want));
		});
		if (hits.length > 1) return 'profile ambiguous';
		if (hits.length === 1) { hits[0].click(); return 'selected'; }
		return 'profile not found';
	})(`+strconv.Quote(c.Profile)+`)`))
	if err != nil {
		return LoginResult{}, fmt.Errorf("fnb: profile page: %w", err)
	}
	switch picked {
	case "no profile page":
	case "selected":
		if err := waitBanking(bctx, mainPage, "choose the profile", func(body string) bool { return !strings.Contains(body, "Linked Profiles") }); err != nil {
			return LoginResult{}, err
		}
		time.Sleep(3 * time.Second) // dologin / HasLoginProcessCompleted round trips
	case "profile needed":
		return LoginResult{}, errors.New("fnb: this login has several profiles; set FNB_PROFILE to the one to use")
	default:
		return LoginResult{}, fmt.Errorf("fnb: Linked Profiles page: %s (FNB_PROFILE must match one profile's name exactly)", picked)
	}

	res, err := chromedp.Call(bctx, storage.GetCookies, storage.GetCookiesParams{})
	if err != nil {
		return LoginResult{}, fmt.Errorf("fnb: read cookies: %w", err)
	}
	var cookies []*http.Cookie
	bankHost := strings.Split(strings.TrimPrefix(strings.TrimPrefix(banking, "https://"), "http://"), "/")[0]
	bankHost = strings.Split(bankHost, ":")[0]
	for _, k := range res.Cookies {
		// Only the bank's (FNB's cookies are set on fnb.co.za and its
		// subdomains), and not the consent cookie, whose JSON value net/http's
		// cookie validation rejects and which banking does not need.
		domain := strings.TrimPrefix(k.Domain, ".")
		if !(strings.HasSuffix(domain, "fnb.co.za") || domain == bankHost) || k.Name == "frgCPP" {
			continue
		}
		cookies = append(cookies, &http.Cookie{Name: k.Name, Value: strings.Trim(k.Value, `"`), Path: k.Path, Domain: k.Domain, Secure: k.Secure})
	}
	return LoginResult{Cookies: cookies, UserAgent: ua}, nil
}

// waitBanking waits up to a minute for the logged-in banking page (mainPage, a
// host and path such as www.online.fnb.co.za/banking/main.jsp).
func waitBanking(ctx context.Context, mainPage, step string, ok func(body string) bool) error {
	deadline := time.Now().Add(60 * time.Second)
	for {
		loc, _ := chromedp.Run(ctx, chromedp.Location())
		body, _ := chromedp.Run(ctx, chromedp.Evaluate[string](`document.body ? document.body.innerText.slice(0, 6000) : ""`))
		if strings.Contains(loc, mainPage) && strings.Contains(body, "Log off") && ok(body) {
			return nil
		}
		if time.Now().After(deadline) {
			// The first line of the page is what tells a failed login from a
			// blocked browser; it never contains the credentials.
			first := strings.TrimSpace(body)
			if i := strings.IndexByte(first, '\n'); i > 0 {
				first = first[:i]
			}
			if len(first) > 120 {
				first = first[:120]
			}
			host := loc
			if i := strings.Index(host, "?"); i > 0 {
				host = host[:i]
			}
			return fmt.Errorf("fnb: could not %s: still at %s, page begins %q", step, host, first)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}
