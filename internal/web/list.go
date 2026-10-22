// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Ton Kersten

package web

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"photothing/internal/store"
)

var (
	pageSizes       = []int{12, 24, 48, 96, 192}
	defaultPageSize = 48
	sortModes       = []string{"name", "natural", "date", "date_desc"}
)

func validPageSize(n int) bool {
	for _, p := range pageSizes {
		if p == n {
			return true
		}
	}
	return false
}

func validSort(s string) bool {
	for _, m := range sortModes {
		if m == s {
			return true
		}
	}
	return false
}

// prefs returns the page size and sort order that apply to the (possibly anonymous) user.
func prefs(u *store.User) (size int, mode string) {
	size, mode = defaultPageSize, "name"
	if u != nil {
		if validPageSize(u.PageSize) {
			size = u.PageSize
		}
		if validSort(u.Sort) {
			mode = u.Sort
		}
	}
	return size, mode
}

// sortRefs orders photos: "name" case-insensitively, "natural" with numbers compared by value
// (d2 before d10), "date" / "date_desc" by capture time. Ties fall back to album and name.
func sortRefs(refs []store.PhotoRef, mode string) {
	byName := func(a, b store.PhotoRef) int {
		if c := strings.Compare(strings.ToLower(a.Dir), strings.ToLower(b.Dir)); c != 0 {
			return c
		}
		if c := strings.Compare(strings.ToLower(a.Filename), strings.ToLower(b.Filename)); c != 0 {
			return c
		}
		return strings.Compare(a.Filename, b.Filename)
	}
	cmp := byName
	switch mode {
	case "natural":
		cmp = func(a, b store.PhotoRef) int {
			if c := strings.Compare(strings.ToLower(a.Dir), strings.ToLower(b.Dir)); c != 0 {
				return c
			}
			if c := naturalCompare(a.Filename, b.Filename); c != 0 {
				return c
			}
			return strings.Compare(a.Filename, b.Filename)
		}
	case "date", "date_desc":
		sign := 1
		if mode == "date_desc" {
			sign = -1
		}
		cmp = func(a, b store.PhotoRef) int {
			if c := a.TakenAt.Compare(b.TakenAt); c != 0 {
				return sign * c
			}
			return byName(a, b)
		}
	}
	sort.SliceStable(refs, func(i, j int) bool { return cmp(refs[i], refs[j]) < 0 })
}

// naturalCompare compares case-insensitively, treating runs of digits as numbers.
func naturalCompare(a, b string) int {
	a, b = strings.ToLower(a), strings.ToLower(b)
	for a != "" && b != "" {
		if unicode.IsDigit(rune(a[0])) && unicode.IsDigit(rune(b[0])) {
			na, ra := digitRun(a)
			nb, rb := digitRun(b)
			if c := compareDigits(na, nb); c != 0 {
				return c
			}
			a, b = ra, rb
			continue
		}
		if a[0] != b[0] {
			if a[0] < b[0] {
				return -1
			}
			return 1
		}
		a, b = a[1:], b[1:]
	}
	switch {
	case a == "" && b == "":
		return 0
	case a == "":
		return -1
	}
	return 1
}

func digitRun(s string) (digits, rest string) {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	return s[:i], s[i:]
}

// compareDigits compares two digit strings by numeric value without overflowing on long runs.
func compareDigits(a, b string) int {
	a, b = strings.TrimLeft(a, "0"), strings.TrimLeft(b, "0")
	if len(a) != len(b) {
		if len(a) < len(b) {
			return -1
		}
		return 1
	}
	return strings.Compare(a, b)
}

// ---- pagination ----

type pageLink struct {
	N       int // 0 = ellipsis
	Current bool
	URL     string
}

type pager struct {
	Page, Pages, Total, Size int
	Prev, Next               string // URLs, "" when there is none
	Links                    []pageLink
}

// pageParam reads ?page=N; it is clamped to [1, pages] by newPager.
func pageParam(r *http.Request) int {
	n, err := strconv.Atoi(r.URL.Query().Get("page"))
	if err != nil || n < 1 {
		return 1
	}
	return n
}

// newPager describes page `page` of total items. base is a URL ending in "?" or "&".
func newPager(total, size, page int, base string) pager {
	pages := (total + size - 1) / size
	if pages < 1 {
		pages = 1
	}
	if page > pages {
		page = pages
	}
	url := func(n int) string {
		if n == 1 {
			return strings.TrimRight(base, "?&")
		}
		return base + "page=" + strconv.Itoa(n)
	}
	p := pager{Page: page, Pages: pages, Total: total, Size: size}
	if page > 1 {
		p.Prev = url(page - 1)
	}
	if page < pages {
		p.Next = url(page + 1)
	}
	last := 0
	for n := 1; n <= pages; n++ {
		if n != 1 && n != pages && (n < page-2 || n > page+2) {
			continue
		}
		if last != 0 && n != last+1 {
			p.Links = append(p.Links, pageLink{})
		}
		p.Links = append(p.Links, pageLink{N: n, Current: n == page, URL: url(n)})
		last = n
	}
	return p
}

// window returns the slice bounds of the current page within total items.
func (p pager) window() (from, to int) {
	from = (p.Page - 1) * p.Size
	to = from + p.Size
	if to > p.Total {
		to = p.Total
	}
	if from > to {
		from = to
	}
	return from, to
}

// pagerBox is the data handed to the "pager" template.
type pagerBox struct {
	Pager pager
	Sizes []int
	Size  int
	Path  string // where to return after changing the page size
	Can   bool   // the page size can be stored (signed-in users)
}

func (s *Server) pagerFor(r *http.Request, pg pager, size int) pagerBox {
	return pagerBox{Pager: pg, Sizes: pageSizes, Size: size, Path: r.URL.RequestURI(), Can: s.current(r) != nil}
}

func ids(refs []store.PhotoRef) []int64 {
	out := make([]int64, len(refs))
	for i, r := range refs {
		out[i] = r.ID
	}
	return out
}
