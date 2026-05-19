// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package auth

import "testing"

func TestMemoryRegistry_Resolve(t *testing.T) {
	reg := NewMemoryRegistry(
		map[string]string{
			"k1": "production",
			"k2": "production",
			"k3": "staging",
		},
		map[string]int{
			"production": 500,
		},
	)

	cases := []struct {
		key    string
		wantNS string
		wantOK bool
	}{
		{"k1", "production", true},
		{"k2", "production", true},
		{"k3", "staging", true},
		{"unknown", "", false},
		{"", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			ns, ok := reg.Resolve(tc.key)
			if ns != tc.wantNS || ok != tc.wantOK {
				t.Errorf("Resolve(%q) = (%q, %v); want (%q, %v)", tc.key, ns, ok, tc.wantNS, tc.wantOK)
			}
		})
	}
}

func TestMemoryRegistry_RateFor(t *testing.T) {
	reg := NewMemoryRegistry(
		map[string]string{
			"k1": "production",
			"k2": "staging",
		},
		map[string]int{
			"production": 500,
		},
	)

	cases := []struct {
		ns           string
		wantRate     int
		wantOverride bool
	}{
		{"production", 500, true},
		{"staging", 0, false},
		{"unknown", 0, false},
		{"", 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.ns, func(t *testing.T) {
			rate, has := reg.RateFor(tc.ns)
			if rate != tc.wantRate || has != tc.wantOverride {
				t.Errorf("RateFor(%q) = (%d, %v); want (%d, %v)", tc.ns, rate, has, tc.wantRate, tc.wantOverride)
			}
		})
	}
}

func TestMemoryRegistry_NilMapsAcceptable(t *testing.T) {
	reg := NewMemoryRegistry(nil, nil)

	if ns, ok := reg.Resolve("anything"); ok {
		t.Errorf("Resolve on empty registry returned (%q, true); want (\"\", false)", ns)
	}
	if rate, has := reg.RateFor("anything"); has {
		t.Errorf("RateFor on empty registry returned (%d, true); want (0, false)", rate)
	}
}
