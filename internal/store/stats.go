package store

import (
	"os"
	"sync/atomic"
)

type Stats struct {
	OK atomic.Int64
}

// CountSubdirs returnerar antalet underkataloger i dir, eller 0 vid fel.
func CountSubdirs(dir string) int64 {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	var n int64
	for _, e := range entries {
		if e.IsDir() {
			n++
		}
	}
	return n
}

type QueueItem struct {
	Filename    string   `json:"filename"`
	Charge      string   `json:"charge"`
	Material    string   `json:"material"`
	ProductForm string   `json:"product_form"`
	Dimensions  string   `json:"dimensions"`
	BNumbers    []string `json:"b_numbers"`
	Confidence  string   `json:"confidence"`
	Issues      []string `json:"issues"`
}

type ReviewItem struct {
	Base   string   `json:"base"`
	Reason string   `json:"reason"`
	Files  []string `json:"files"`
}
