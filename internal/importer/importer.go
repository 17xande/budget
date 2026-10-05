// Package importer turns FNB export files into statements: an account and its
// transactions. It is pure -- bytes in, values out, no database -- so every quirk
// of FNB's formats is pinned by fixture tests. See docs/fnb-flow.md for the formats.
package importer

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
)

// Statement is one account's transactions from one file.
type Statement struct {
	// AccountRef identifies the account the same way in every format: the
	// account number for a cheque account, the masked card number
	// ("400000******1234") for a credit card.
	AccountRef string
	// AccountLabel is FNB's own description of the account type when the file
	// carries one ("Cheque Acc", "Credit Acc"); OFX does not.
	AccountLabel string
	// Format is "csv", "ofx" or "ofx-advanced", for the import history.
	Format string
	Txns   []Txn
}

// Txn is one transaction as the file states it.
type Txn struct {
	// Date is the posting date as YYYY-MM-DD: a calendar date in South Africa,
	// not an instant, so it is never converted between timezones.
	Date        string
	AmountCents int64
	// Description has its runs of whitespace collapsed. FNB pads fields inside
	// descriptions with spaces, and the three formats pad differently; collapsed,
	// they agree.
	Description string
	// BalanceCents is the running balance after this transaction, when the file
	// has a meaningful one. The credit card CSV's balance column is all zeros,
	// which is reported as no balance at all rather than as R0.00.
	BalanceCents *int64
	// FITID is the OFX transaction id. Informational only: it collides for
	// identical same-day transactions and differs between FNB's two OFX exports.
	FITID string
}

// File is one uploaded file.
type File struct {
	Name string
	Data []byte
}

// Limits on what a ZIP may expand to. FNB's exports are one small file; anything
// past these is not an FNB export and must not be allowed to exhaust memory.
const (
	maxZipEntries   = 20
	maxEntryBytes   = 20 << 20
	maxZipTotalSize = 50 << 20
)

// ErrNotRecognised means the file is none of the formats FNB exports.
var ErrNotRecognised = errors.New("not an FNB CSV or OFX export")

// Parse reads one uploaded file -- a CSV, an OFX, or a ZIP of them as FNB
// downloads it -- into its statements. The format is detected from the content,
// not the file name.
func Parse(f File) ([]Statement, error) {
	if bytes.HasPrefix(f.Data, []byte("PK\x03\x04")) {
		return parseZip(f)
	}
	st, err := parseOne(f.Data)
	if err != nil {
		return nil, err
	}
	return []Statement{st}, nil
}

func parseZip(f File) ([]Statement, error) {
	zr, err := zip.NewReader(bytes.NewReader(f.Data), int64(len(f.Data)))
	if err != nil {
		return nil, fmt.Errorf("read zip: %w", err)
	}
	if len(zr.File) > maxZipEntries {
		return nil, fmt.Errorf("zip has %d files; an FNB export has one", len(zr.File))
	}
	var out []Statement
	var total int64
	for _, zf := range zr.File {
		if zf.FileInfo().IsDir() || strings.HasPrefix(path.Base(zf.Name), ".") {
			continue
		}
		rc, err := zf.Open()
		if err != nil {
			return nil, fmt.Errorf("open %s in zip: %w", zf.Name, err)
		}
		// Read one byte past the limit so an oversized entry is detected rather
		// than silently truncated -- the declared size in the header is not to
		// be trusted.
		data, err := io.ReadAll(io.LimitReader(rc, maxEntryBytes+1))
		rc.Close()
		if err != nil {
			return nil, fmt.Errorf("read %s in zip: %w", zf.Name, err)
		}
		if len(data) > maxEntryBytes {
			return nil, fmt.Errorf("%s in zip is larger than %d MB", zf.Name, maxEntryBytes>>20)
		}
		if total += int64(len(data)); total > maxZipTotalSize {
			return nil, fmt.Errorf("zip expands to more than %d MB", maxZipTotalSize>>20)
		}
		st, err := parseOne(data)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", zf.Name, err)
		}
		out = append(out, st)
	}
	if len(out) == 0 {
		return nil, errors.New("zip contains no files")
	}
	return out, nil
}

func parseOne(data []byte) (Statement, error) {
	text := decodeWindows1252(data)
	trimmed := strings.TrimLeft(text, "\uFEFF \t\r\n")
	switch {
	case strings.HasPrefix(trimmed, "OFXHEADER") || strings.HasPrefix(trimmed, "<OFX>"):
		return parseOFX(text)
	case strings.Contains(text, "Date, Amount, Balance, Description") || strings.Contains(text, "Date,Amount,Balance,Description"):
		return parseCSV(text)
	default:
		return Statement{}, ErrNotRecognised
	}
}

// collapse folds every run of whitespace to one space and trims the ends.
func collapse(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
