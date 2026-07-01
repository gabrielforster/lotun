package main

import "testing"

func TestTCPPrivateRequiresAllowIP(t *testing.T) {
	err := validateTCPFlags( /*private=*/ true /*allowIPs=*/, nil /*password=*/, "")
	if err == nil {
		t.Fatal("private tcp with no --allow-ip must error")
	}
}

func TestTCPPasswordRejected(t *testing.T) {
	if validateTCPFlags(true, []string{"1.2.3.4"}, "pw") == nil {
		t.Fatal("--password on tcp must error")
	}
}

func TestTCPPrivateWithAllowIPOK(t *testing.T) {
	if err := validateTCPFlags(true, []string{"1.2.3.4"}, ""); err != nil {
		t.Fatalf("private tcp with --allow-ip must succeed, got %v", err)
	}
}

func TestTCPPublicOK(t *testing.T) {
	if err := validateTCPFlags(false, nil, ""); err != nil {
		t.Fatalf("public tcp must succeed, got %v", err)
	}
}

func TestHTTPPortParsing(t *testing.T) {
	if p, err := parsePort("8080"); err != nil || p != 8080 {
		t.Fatalf(`parsePort("8080") = (%d, %v), want (8080, nil)`, p, err)
	}
	for _, bad := range []string{"0", "70000", "abc", "", "-1"} {
		if _, err := parsePort(bad); err == nil {
			t.Fatalf("parsePort(%q) must error", bad)
		}
	}
}
