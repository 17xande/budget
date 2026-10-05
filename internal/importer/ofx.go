package importer

import (
	"errors"
	"fmt"
	"html"
	"regexp"
	"strings"
)

// parseOFX reads both OFX dialects FNB produces. OFX 1.02 is SGML: one tag per
// line, leaf elements unclosed ("<TRNAMT>-12.34"). That makes a line reader the
// honest parser; an XML decoder would reject it.
func parseOFX(text string) (Statement, error) {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	if strings.Contains(text, "<STMTTRN>") {
		return parseQuickOFX(lines)
	}
	if strings.Contains(text, "<DTEFF>") {
		return parseAdvancedOFX(lines)
	}
	if strings.Contains(text, "<BANKTRANLIST>") {
		// A valid statement with no transactions in the window.
		st := Statement{Format: "ofx"}
		for _, l := range lines {
			if tag, val := splitTag(l); tag == "ACCTID" {
				st.AccountRef = accountRef(val)
			}
		}
		if st.AccountRef == "" {
			return st, errors.New("OFX has no ACCTID")
		}
		return st, nil
	}
	return Statement{}, errors.New("OFX has no transaction list")
}

// splitTag splits "<TAG>value" into its parts, decoding SGML entities in the
// value: OFX writes "M&S" as "M&amp;S" where the CSV of the same transaction has
// a bare "&", and the two must agree. A closing tag or a bare line returns an
// empty tag.
func splitTag(line string) (tag, value string) {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "<") || strings.HasPrefix(line, "</") {
		return "", line
	}
	tag, value, ok := strings.Cut(line[1:], ">")
	if !ok {
		return "", line
	}
	return tag, html.UnescapeString(value)
}

// parseQuickOFX reads the standard dialect, from the quick search:
//
//	<STMTTRN> <TRNTYPE>DEBIT <DTPOSTED>20261005 <TRNAMT>-12.34 <FITID>... <MEMO>... </STMTTRN>
func parseQuickOFX(lines []string) (Statement, error) {
	st := Statement{Format: "ofx"}
	var cur *Txn
	for i, l := range lines {
		tag, val := splitTag(l)
		switch {
		case strings.TrimSpace(l) == "</STMTTRN>":
			if cur == nil {
				return st, fmt.Errorf("line %d: </STMTTRN> without <STMTTRN>", i+1)
			}
			if cur.Date == "" {
				return st, fmt.Errorf("line %d: transaction without DTPOSTED", i+1)
			}
			st.Txns = append(st.Txns, *cur)
			cur = nil
		case tag == "STMTTRN":
			cur = &Txn{}
		case tag == "ACCTID":
			st.AccountRef = accountRef(val)
		case cur == nil:
			// Outside a transaction (headers, balances): nothing else wanted.
		case tag == "DTPOSTED":
			d, err := isoDate(val)
			if err != nil {
				return st, fmt.Errorf("line %d: %w", i+1, err)
			}
			cur.Date = d
		case tag == "TRNAMT":
			c, err := parseCents(val)
			if err != nil {
				return st, fmt.Errorf("line %d: %w", i+1, err)
			}
			cur.AmountCents = c
		case tag == "FITID":
			cur.FITID = strings.TrimSpace(val)
		case tag == "MEMO" || (tag == "NAME" && cur.Description == ""):
			cur.Description = collapse(val)
		}
	}
	if st.AccountRef == "" {
		return st, errors.New("OFX has no ACCTID")
	}
	return st, nil
}

var bareID = regexp.MustCompile(`^[0-9A-Fa-f]{32}$`)

// parseAdvancedOFX reads the dialect the advanced (custom date range) search
// produces. It is not valid OFX: no STMTTRN wrappers, and an untagged id line.
// Each record is seven lines:
//
//	<DTEFF>2026-10-05 <ACCTID>Cheque Acc62000000001 <TRNREF>... <SERVFEE>0.00
//	<TRNAMT>-12.34 <32 hex digits> <MEMO>...
//
// A record starts at DTEFF and ends at MEMO.
func parseAdvancedOFX(lines []string) (Statement, error) {
	st := Statement{Format: "ofx-advanced"}
	var cur *Txn
	for i, l := range lines {
		tag, val := splitTag(l)
		switch tag {
		case "DTEFF":
			if cur != nil {
				return st, fmt.Errorf("line %d: record without MEMO before it", i+1)
			}
			d, err := isoDate(val)
			if err != nil {
				return st, fmt.Errorf("line %d: %w", i+1, err)
			}
			cur = &Txn{Date: d}
		case "ACCTID":
			ref := accountRef(val)
			if st.AccountRef != "" && ref != st.AccountRef {
				return st, fmt.Errorf("line %d: a second account (%s) in one file", i+1, ref)
			}
			st.AccountRef = ref
			if label := strings.TrimSpace(strings.TrimSuffix(val, ref)); label != "" {
				st.AccountLabel = label
			}
		case "TRNAMT":
			if cur == nil {
				return st, fmt.Errorf("line %d: TRNAMT outside a record", i+1)
			}
			c, err := parseCents(val)
			if err != nil {
				return st, fmt.Errorf("line %d: %w", i+1, err)
			}
			cur.AmountCents = c
		case "MEMO":
			if cur == nil {
				return st, fmt.Errorf("line %d: MEMO outside a record", i+1)
			}
			cur.Description = collapse(val)
			st.Txns = append(st.Txns, *cur)
			cur = nil
		case "":
			if cur != nil && bareID.MatchString(strings.TrimSpace(val)) {
				cur.FITID = strings.TrimSpace(val)
			}
		}
	}
	if cur != nil {
		return st, errors.New("file ends inside a record")
	}
	if st.AccountRef == "" {
		return st, errors.New("OFX has no ACCTID")
	}
	return st, nil
}

// accountRefRe is the account identifier at the end of an ACCTID: digits with
// any masking asterisks ("62000000001", "400000******1234"). The advanced dialect
// prefixes it with a label ("Cheque Acc62000000001").
var accountRefRe = regexp.MustCompile(`[0-9*]+$`)

func accountRef(acctid string) string {
	acctid = strings.TrimSpace(acctid)
	if m := accountRefRe.FindString(acctid); m != "" {
		return m
	}
	return acctid
}
