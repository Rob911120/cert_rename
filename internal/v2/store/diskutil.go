package store

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// UniquePath returnerar dir/name eller dir/name_N.ext om filen redan finns.
func UniquePath(dir, name string) string {
	full := filepath.Join(dir, name)
	if _, err := os.Stat(full); os.IsNotExist(err) {
		return full
	}
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	for i := 2; i < 100; i++ {
		try := filepath.Join(dir, fmt.Sprintf("%s_%d%s", base, i, ext))
		if _, err := os.Stat(try); os.IsNotExist(err) {
			return try
		}
	}
	return full
}

// WriteUniqueFile skriver data atomiskt till dir/name (eller dir/name_N.ext
// vid kollision) via O_EXCL. Två parallella anrop kan aldrig välja samma
// path. Returnerar slutgiltig path. Fel om alla suffix _2..._99 är upptagna.
func WriteUniqueFile(dir, name string, data []byte) (string, error) {
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	candidate := filepath.Join(dir, name)
	for i := 1; i < 100; i++ {
		f, err := os.OpenFile(candidate, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
		if err == nil {
			_, werr := f.Write(data)
			cerr := f.Close()
			if werr != nil {
				_ = os.Remove(candidate)
				return "", werr
			}
			if cerr != nil {
				_ = os.Remove(candidate)
				return "", cerr
			}
			return candidate, nil
		}
		if !os.IsExist(err) {
			return "", err
		}
		candidate = filepath.Join(dir, fmt.Sprintf("%s_%d%s", base, i+1, ext))
	}
	return "", fmt.Errorf("alla suffix upptagna för %s i %s", name, dir)
}

// SafeName avvisar tomma strängar, path-separatorer och ".." för operationer
// som tar användarinmatade fil-/mappnamn. Delas av HTTP-handlers, Sickan-verktyg
// och disk-ops så traverseringsregeln bara finns på ett ställe.
func SafeName(s string) bool {
	if s == "" {
		return false
	}
	return !strings.ContainsAny(s, `/\`) && !strings.Contains(s, "..")
}
