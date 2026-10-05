# FNB online banking: the export flow

Captured on 2026-10-05 from a real session (Chrome DevTools), then proven with a
throwaway spike: headless Chromium logged in, plain Go `net/http` listed the
accounts and downloaded OFX for each. No credentials, account numbers or
transactions are recorded here.

FNB changes its site without notice. When a fetch breaks, recapture the step that
broke and update this file first.

## Why the login needs a browser

`www.fnb.co.za` runs **Radware Bot Manager** (`server: rdwr`, `rdwr_response:
allowed`, `__uzm*`/`uzmx` cookies). Its script fingerprints the browser and adds a
`uzlc` header to the credential POST. A plain HTTP client would have to forge
those, which is evading a bank's bot protection: we do not. A real headless
Chromium runs the script as designed and passes.

`www.online.fnb.co.za/banking/…` (the banking app, IIS/"Sun One") showed no
Radware headers, so everything after login is plain HTTP with the session cookies.

## Login (browser: chromedp + `chromedp/headless-shell`)

1. Navigate to `https://www.fnb.co.za/`; wait for `#user`.
2. **Cookie consent overlay**, shown to a fresh browser profile, covers the page and
   swallows the Login click. Click `#cookieSettingsBtn`, untick the optional
   checkboxes in `#cookieSettings` (they default to *on*), click
   `#cookieSaveSettingsBtn`. Matches the choice made in a personal browser
   (`frgCPP` all `denied`).
3. Fill `#user` and `#pass`, click `#OBSubmit`. The browser POSTs a form to
   `www.fnb.co.za/login/Controller` (`Username`, `Password`, `action=login`,
   `formname=LOGIN_FORM`, `nav=navigator.UserLogon`, browser-description fields,
   `referCode`). No phone approval was required.
4. Wait for `online.fnb.co.za/banking/main.jsp` showing "Log off".
5. **Linked Profiles.** A user linked to more than one profile (here a business and
   a personal one) lands on a profile chooser. Select by exact profile name
   (config: `FNB_PROFILE`):
   - each row's button is a `span.eziLink` with text "Select";
   - the row is the largest ancestor of that span containing exactly one such
     button (nearer "row" classes contain only the button);
   - refuse if zero or several rows match;
   - clicking runs an SSO hop (`nav=ob1sso.navigator.OB1SSORedirect&supportNumber=…
     &redirectString=<per-session token>` → `navigator.UserLogon` → `main.jsp` →
     `dologin`). Wait until "Linked Profiles" is gone, then ~3 s.
   "Log off" is already on the chooser page, so do not treat it alone as logged in.
6. Copy the `*.fnb.co.za` cookies into a Go `cookiejar` and close the browser.
   Skip `frgCPP` (JSON value with quotes; net/http rejects it). Session cookies
   are `JSESSIONID` (two, different paths) and `BANKit`. `ADRUM_BTa` is quoted —
   strip the quotes.

**Use a fresh browser context per login** (`chromedp.WithNewBrowserContext`). The
sidecar Chrome runs for weeks and its tabs share one cookie jar: without isolation,
the next day's login starts inside the previous session, skips the profile chooser,
and bank cookies sit in the browser between runs. The integration test
(`FNB_TEST_CHROME_URL`, see internal/fnb/browser_test.go) caught this.

**Chrome's DevTools refuses a `Host:` that is not an IP or localhost.**
`chromedp/remote` resolves `ws://chrome:9222/` to the container's IP first, so the
compose service name works; a hand-rolled client would need to do the same.

## After login (net/http)

Every call is `POST https://www.online.fnb.co.za/banking/Controller` with
`X-Requested-With: XMLHttpRequest`, `Referer: …/banking/main.jsp`, the
`Origin`, and the browser's own user agent (read from the browser, not invented).
`nav` and some parameters go in the query string, form fields in an urlencoded body.

1. **Accounts:** `nav=accounts.summaryofaccountbalances.navigator.SummaryOfAccountBalances`
   (`FARFN=4&actionchild=1&isTopMenu=true&targetDiv=pageContent`). HTML; scrape
   `accountRefNum=(-?\d+)&(amp;)?productCode=(\w+)`, deduplicated. Product codes
   seen: `DDA` (cheque/transactional), `ZFN` (credit card), `TOA` (savings/notice?),
   `MLS` (loan?).
2. **Open history:** `nav=transactionhistory.navigator.TransactionHistoryRedirect`
   with `productCode`, `anrfn=<accountRefNum>`, `initial=initial`, `FARFN=98`,
   `targetDiv=pageContent`.
3. **Quick search (last 60 days):** body `nav=transactionhistory.navigator.TransactionHistoryRedirect`,
   `export=std`, `anrfn`, `productCode`, `initial=notinitial`, `go=Search`,
   `formID=FORMDDAHISTSELCRIT_108`, `stdQuickCustom=quick`, `isSimple=true`,
   `selectedQuickSearchOption=60`, `dateFrom`/`dateTo` (`YYYY-MM-DD`),
   `valueFrom=0.00`, `valueTo=999999999.99`, `chequeNumberFrom=0`,
   `chequeNumberTo=9999999999`, and empty `key`, `downloadFormat`, `function`,
   `reference`. Query `targetDiv=pageContent`.
4. **Download dialog:** `nav=accounts.transactionhistory.navigator.TransactionHistoryDownloadEzi`,
   `targetDiv=eziPageContent`.
5. **File:** body `nav=accounts.transactionhistory.navigator.TransactionHistoryDDADownload`,
   `doDownload=true`, `downloadFormat=ofx` (also `csv`, `qif`, `ofc`, `pdf`). The same
   nav serves the credit card.
6. **Log off:** `nav=navigator.UserLogoff&FARFN=849&actionchild=1797&isTopMenu=true`.
   Always, deferred.

The response to 5 is a **ZIP containing one file** (`<account>.ofx`). An account
with no transactions in the window answered an **empty 200** (not a ZIP): treat it
as zero transactions for that account, record it, carry on.

The spike's whole run took about 35 s for 7 accounts.

### Custom date range (advanced search)

Body `nav=transactionhistory.TransactionHistoryAdvancedSearch`, `from`/`to`
(`YYYY-MM-DD`), `export=adv`, `input1=<account number>`, `input2..5=-1`,
`debit=true`, `credit=true`, `archivedDays=60`, `actionSearch=Search`,
`transactSearch=Search`, `initial=initial`. Download via the same step 5; the ZIP is
named `AdvancedTransactionHistoryYYYYMMDD.zip`.

**Capped at about three months** (a request from May returned from 1 July). Older
history only exists in exports saved at the time. Its OFX is a broken dialect (see
below), so automation uses the quick search.

## File formats

**OFX (quick search)** — OFX 1.02 SGML, `CHARSET:1252`. Each `<STMTTRN>` has
`TRNTYPE` (`DEBIT`/`CREDIT`), `DTPOSTED` (`YYYYMMDD`), `TRNAMT`, `FITID`, `MEMO`. The
card uses `BANKACCTFROM` too. `FITID` is stable across repeated downloads but
**collides for identical same-day transactions** (148 unique of 150 on the card),
since it appears to be a hash of the transaction's content.

**OFX (advanced search)** — no `<STMTTRN>` wrappers. Each record is seven lines:
`<DTEFF>YYYY-MM-DD`, `<ACCTID>`, `<TRNREF>`, `<SERVFEE>`, `<TRNAMT>`, a **bare
untagged 32-hex id**, `<MEMO>`. The ids do not match the quick export's FITIDs.

**CSV** — Latin-1, LF. Six preamble lines (title, `Name:`, `Account:`, `Balance:`,
blanks), then `Date, Amount, Balance, Description` and rows
`YYYY/MM/DD, -123.45, 1234.56, text` — fields separated by `, `, descriptions padded
with spaces. On the credit card the Balance column is `0`, not a running balance.

**Quirks the real exports taught us** (2026-10-05, Phase 2):

- **OFX escapes SGML entities, CSV does not**: a merchant "A & B" is
  `A &amp; B` in OFX and `A & B` in the CSV. Undecoded, the same transaction
  looks different in the two formats and imports twice. The OFX parser decodes
  entities.
- **Row order differs by account**: the cheque export is newest first, the
  credit card's oldest first. Nothing may depend on order.
- **The card's quick export is not 60 days**: it returned about four months
  (150 rows, June to October) where the cheque returned 60 days. It looks capped
  at 150 rows rather than by date.

`FNB_REAL_EXPORTS=~/Downloads go test -run TestRealExports -v ./internal/importer`
re-checks all of this against real downloads (it logs counts only). Run it
whenever FNB changes anything.

**Cross-check:** over the window they share, quick OFX, CSV and advanced OFX hold
identical multisets of (date, amount, whitespace-normalised description). Hence
deduplication is content-based — account + date + amount + normalised description +
occurrence index within that tuple — never FITID, so uploads of old CSVs and
automated OFX fetches never double-count.
