package main

import (
	"fmt"
	"io"
	"sort"
	"strconv"
)

// rowBytes attributes a recorded output stream to the screen rows it was
// written to: it follows the cursor (CUP, VPA, CUU/CUD, CR, LF, RI) and
// counts, per row, the bytes written while the cursor was on it and how
// many times the row was entered. Which rows a harness keeps rewriting is
// what this is for (ADR 030, F2.1).
// dumpRow > 0: also print the text written to that row.
var dumpRow int

func rowBytes(all []byte, rows int, w io.Writer) {
	type stat struct{ bytes, visits int }
	st := map[int]*stat{}
	at := func(r int) *stat {
		if st[r] == nil {
			st[r] = &stat{}
		}
		return st[r]
	}
	row := 1
	var snip []byte
	dumped := 0
	enter := func(r int) {
		r = max(1, min(rows, r))
		if r != row {
			at(r).visits++
		}
		row = r
	}
	for i := 0; i < len(all); {
		c := all[i]
		switch {
		case c == 0x1b && i+1 < len(all) && all[i+1] == '[':
			j := i + 2
			for j < len(all) && (all[j] < 0x40 || all[j] > 0x7e) {
				j++
			}
			if j >= len(all) {
				i = len(all)
				continue
			}
			params, final := string(all[i+2:j]), all[j]
			n := func(k, def int) int {
				f := splitParams(params)
				if k < len(f) && f[k] != "" {
					if v, err := strconv.Atoi(f[k]); err == nil {
						return v
					}
				}
				return def
			}
			switch final {
			case 'H', 'f':
				enter(n(0, 1))
			case 'd':
				enter(n(0, 1))
			case 'A':
				enter(row - n(0, 1))
			case 'B', 'E':
				enter(row + n(0, 1))
			case 'F':
				enter(row - n(0, 1))
			}
			at(row).bytes += j + 1 - i
			i = j + 1
		case c == '\n':
			enter(row + 1)
			at(row).bytes++
			i++
		case c == 0x1b && i+1 < len(all) && all[i+1] == 'M':
			enter(row - 1)
			i += 2
		default:
			at(row).bytes++
			if row == dumpRow && dumped < 4000 {
				snip = append(snip, c)
				dumped++
			}
			i++
		}
	}
	if dumpRow > 0 {
		fmt.Fprintf(w, "text written to row %d (first %d bytes, escapes dropped):\n%q\n", dumpRow, len(snip), snip)
	}
	keys := make([]int, 0, len(st))
	total := 0
	for r, s := range st {
		keys = append(keys, r)
		total += s.bytes
	}
	sort.Slice(keys, func(a, b int) bool { return st[keys[a]].bytes > st[keys[b]].bytes })
	fmt.Fprintf(w, "%d bytes over %d rows\nrow\tbytes\tshare\tvisits\n", total, len(keys))
	for _, r := range keys {
		s := st[r]
		fmt.Fprintf(w, "%d\t%d\t%.1f%%\t%d\n", r, s.bytes, 100*float64(s.bytes)/float64(max(total, 1)), s.visits)
	}
}

func splitParams(p string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(p); i++ {
		if i == len(p) || p[i] == ';' {
			s := p[start:i]
			for len(s) > 0 && (s[0] == '?' || s[0] == '>' || s[0] == '<' || s[0] == '=') {
				s = s[1:]
			}
			out = append(out, s)
			start = i + 1
		}
	}
	return out
}
