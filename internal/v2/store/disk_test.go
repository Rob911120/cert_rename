package store

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

func Test_WriteUniqueFile_RaceSafe(t *testing.T) {
	dir := t.TempDir()
	const N = 10

	var wg sync.WaitGroup
	paths := make([]string, N)
	errs := make([]error, N)
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func(i int) {
			defer wg.Done()
			data := []byte(fmt.Sprintf("payload-%d", i))
			p, err := WriteUniqueFile(dir, "x.txt", data)
			paths[i] = p
			errs[i] = err
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: oväntat fel: %v", i, err)
		}
	}

	seen := map[string]struct{}{}
	for i, p := range paths {
		if _, dup := seen[p]; dup {
			t.Fatalf("path %q returnerades två gånger (goroutine %d)", p, i)
		}
		seen[p] = struct{}{}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != N {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		sort.Strings(names)
		t.Fatalf("förväntade %d filer, fick %d: %v", N, len(entries), names)
	}

	for i, p := range paths {
		got, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("läs %s: %v", p, err)
		}
		want := fmt.Sprintf("payload-%d", i)
		if string(got) != want {
			t.Fatalf("path %s: innehåll = %q, vill ha %q", p, string(got), want)
		}
	}
}

func Test_WriteUniqueFile_SuffixOnExisting(t *testing.T) {
	dir := t.TempDir()

	first, err := WriteUniqueFile(dir, "x.txt", []byte("ett"))
	if err != nil {
		t.Fatalf("första skriv: %v", err)
	}
	if filepath.Base(first) != "x.txt" {
		t.Fatalf("första filnamn = %q, vill ha x.txt", filepath.Base(first))
	}

	second, err := WriteUniqueFile(dir, "x.txt", []byte("två"))
	if err != nil {
		t.Fatalf("andra skriv: %v", err)
	}
	if filepath.Base(second) != "x_2.txt" {
		t.Fatalf("andra filnamn = %q, vill ha x_2.txt", filepath.Base(second))
	}

	gotFirst, _ := os.ReadFile(first)
	if string(gotFirst) != "ett" {
		t.Fatalf("första filens innehåll = %q, vill ha ett", string(gotFirst))
	}
	gotSecond, _ := os.ReadFile(second)
	if string(gotSecond) != "två" {
		t.Fatalf("andra filens innehåll = %q, vill ha två", string(gotSecond))
	}
}

func Test_StoredName_ExtremeFilenames(t *testing.T) {
	hash := "abcdef0123456789"
	cases := []struct {
		name     string
		original string
	}{
		{"normalt", "certifikat.pdf"},
		{"tomt", ""},
		{"bara punkt", "."},
		{"långt namn", strings.Repeat("a", 300) + ".pdf"},
		{"lång ändelse", "b." + strings.Repeat("x", 130)}, // panikade förr: 120-len(ext) < 0
		{"långt med åäö", strings.Repeat("å", 200) + ".pdf"},
		{"åäö vid klippgränsen", strings.Repeat("x", 115) + "åäö.pdf"},
		{"reserverade tecken", `rapport<>:"|?*.pdf`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := StoredName(hash, tc.original) // får aldrig panika
			if len(got) == 0 || !strings.HasPrefix(got, hash[:12]+"__") {
				t.Fatalf("StoredName = %q, saknar hashprefix", got)
			}
			if base := strings.TrimPrefix(got, hash[:12]+"__"); len(base) > 120 {
				t.Fatalf("basnamnet är %d byte (>120): %q", len(base), base)
			}
			if !utf8.ValidString(got) {
				t.Fatalf("StoredName gav ogiltig UTF-8: %q", got)
			}
		})
	}
}

func Test_WriteUniqueFile_FullDirReturnsErr(t *testing.T) {
	dir := t.TempDir()

	if _, err := WriteUniqueFile(dir, "x.txt", []byte("0")); err != nil {
		t.Fatalf("seed x.txt: %v", err)
	}
	for i := 2; i < 100; i++ {
		p := filepath.Join(dir, fmt.Sprintf("x_%d.txt", i))
		if err := os.WriteFile(p, []byte("seed"), 0644); err != nil {
			t.Fatalf("seed %s: %v", p, err)
		}
	}

	_, err := WriteUniqueFile(dir, "x.txt", []byte("nytt"))
	if err == nil {
		t.Fatal("förväntade fel när alla suffix är upptagna, fick nil")
	}
}
