package domain

import "testing"

func ptrF(f float64) *float64 { return &f }

func TestEnglishVerdict(t *testing.T) {
	cases := []struct {
		name          string
		reqEnglish    bool
		certIsEnglish bool
		want          string
	}{
		{"inget krav, cert engelska", false, true, VerdictUnknown},
		{"inget krav, cert ej engelska", false, false, VerdictUnknown},
		{"krav + engelska", true, true, VerdictOK},
		{"krav + ej engelska", true, false, VerdictMismatch},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := EnglishVerdict(tc.reqEnglish, tc.certIsEnglish); got != tc.want {
				t.Errorf("EnglishVerdict(%v,%v) = %q, vill ha %q", tc.reqEnglish, tc.certIsEnglish, got, tc.want)
			}
		})
	}
}

func TestCertTypeVerdict(t *testing.T) {
	cases := []struct {
		name            string
		req, cert, want string
	}{
		{"lika", "3.1", "3.1", VerdictOK},
		{"lika med skräpmellanslag", " 3.1 ", "3.1", VerdictOK},
		{"lika skiftläge", "3.2 ", " 3.2", VerdictOK},
		{"olika", "3.1", "3.2", VerdictMismatch},
		{"olika 2.2 vs 3.1", "2.2", "3.1", VerdictMismatch},
		{"krav tomt", "", "3.1", VerdictUnknown},
		{"cert tomt", "3.1", "", VerdictUnknown},
		{"båda tomma", "", "", VerdictUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CertTypeVerdict(tc.req, tc.cert); got != tc.want {
				t.Errorf("CertTypeVerdict(%q,%q) = %q, vill ha %q", tc.req, tc.cert, got, tc.want)
			}
		})
	}
}

func TestParseImpact(t *testing.T) {
	cases := []struct {
		name   string
		in     string
		wantE  float64
		wantT  float64
		wantOK bool
	}{
		{"standard", "27J/-20°C", 27, -20, true},
		{"mellanslag runt slash", "27J / -20°C", 27, -20, true},
		{"utan minustecken", "27J/20°C", 27, 20, true},
		{"utan gradtecken", "27J/-20C", 27, -20, true},
		{"decimal", "40.5J/-40°C", 40.5, -40, true},
		{"komma-decimal", "27,5J/-20°C", 27.5, -20, true},
		{"gement j", "27j/-20°c", 27, -20, true},
		{"skräp", "ingen slagseghet", 0, 0, false},
		{"tomt", "", 0, 0, false},
		{"bara energi", "27J", 0, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, temp, ok := ParseImpact(tc.in)
			if ok != tc.wantOK {
				t.Fatalf("ParseImpact(%q) ok = %v, vill ha %v", tc.in, ok, tc.wantOK)
			}
			if ok && (e != tc.wantE || temp != tc.wantT) {
				t.Errorf("ParseImpact(%q) = (%g,%g), vill ha (%g,%g)", tc.in, e, temp, tc.wantE, tc.wantT)
			}
		})
	}
}

func TestImpactVerdict(t *testing.T) {
	cases := []struct {
		name       string
		req        string
		certEnergy float64
		certTemp   *float64
		want       string
	}{
		{"exakt match", "27J/-20°C", 27, ptrF(-20), VerdictOK},
		{"bättre energi", "27J/-20°C", 32, ptrF(-20), VerdictOK},
		{"kallare provtemp", "27J/-20°C", 27, ptrF(-40), VerdictOK},
		{"bättre på båda", "27J/-20°C", 40, ptrF(-50), VerdictOK},
		{"lägre energi", "27J/-20°C", 20, ptrF(-20), VerdictMismatch},
		{"varmare temp", "27J/-20°C", 27, ptrF(0), VerdictMismatch},
		{"oparsebart krav", "cert 3.1 krävs", 27, ptrF(-20), VerdictUnknown},
		{"inget krav", "", 27, ptrF(-20), VerdictUnknown},
		{"cert utan energi", "27J/-20°C", 0, ptrF(-20), VerdictUnknown},
		{"cert utan temp", "27J/-20°C", 27, nil, VerdictUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ImpactVerdict(tc.req, tc.certEnergy, tc.certTemp); got != tc.want {
				t.Errorf("ImpactVerdict(%q,%g,%v) = %q, vill ha %q", tc.req, tc.certEnergy, tc.certTemp, got, tc.want)
			}
		})
	}
}
