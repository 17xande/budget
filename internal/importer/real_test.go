package importer

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRealExports parses real FNB downloads when FNB_REAL_EXPORTS names a
// directory of them (the ZIPs exactly as downloaded). It is skipped otherwise:
// real transactions never belong in the repository.
//
// Run it when FNB changes a format, or before trusting a new kind of export:
//
//	FNB_REAL_EXPORTS=~/Downloads go test -run TestRealExports -v ./internal/importer
//
// It logs only counts, never amounts or descriptions.
func TestRealExports(t *testing.T) {
	dir := os.Getenv("FNB_REAL_EXPORTS")
	if dir == "" {
		t.Skip("FNB_REAL_EXPORTS not set")
	}
	paths, _ := filepath.Glob(filepath.Join(dir, "*.zip"))
	if len(paths) == 0 {
		t.Fatalf("no .zip files in %s", dir)
	}
	byAccount := map[string][]Statement{}
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		sts, err := Parse(File{Name: filepath.Base(p), Data: data})
		if err != nil {
			t.Errorf("%s: %v", filepath.Base(p), err)
			continue
		}
		for _, st := range sts {
			first, last := "", ""
			if n := len(st.Txns); n > 0 {
				first, last = st.Txns[n-1].Date, st.Txns[0].Date
			}
			t.Logf("%s: %s, %d transactions %s..%s", filepath.Base(p), st.Format, len(st.Txns), first, last)
			byAccount[st.AccountRef] = append(byAccount[st.AccountRef], st)
		}
	}

	// Every pair of exports of one account must agree over the dates they share.
	for _, sts := range byAccount {
		for i := range sts {
			for j := i + 1; j < len(sts); j++ {
				a, b := sts[i], sts[j]
				from, to := overlap(a), overlap(b)
				lo, hi := max(from[0], to[0]), min(from[1], to[1])
				if lo > hi {
					continue
				}
				ma, mb := multiset(a.Txns, lo, hi), multiset(b.Txns, lo, hi)
				diff := 0
				for k, n := range ma {
					if mb[k] != n {
						diff++
					}
				}
				for k := range mb {
					if _, ok := ma[k]; !ok {
						diff++
					}
				}
				if diff != 0 {
					t.Errorf("%s vs %s disagree on %d rows between %s and %s", a.Format, b.Format, diff, lo, hi)
				} else {
					t.Logf("%s vs %s agree on %d distinct rows between %s and %s", a.Format, b.Format, len(ma), lo, hi)
				}
			}
		}
	}
}

func overlap(st Statement) [2]string {
	if len(st.Txns) == 0 {
		return [2]string{"9999", "0000"}
	}
	lo, hi := st.Txns[0].Date, st.Txns[0].Date
	for _, tx := range st.Txns {
		lo, hi = min(lo, tx.Date), max(hi, tx.Date)
	}
	return [2]string{lo, hi}
}
