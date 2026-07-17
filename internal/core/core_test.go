// SPDX-License-Identifier: AGPL-3.0-only
package core

import "testing"

func TestBindingNameDistinct(t *testing.T) {
	a := BindingName("team-a", "g:devs", "pod-reader")
	for _, b := range []string{
		BindingName("team-b", "g:devs", "pod-reader"),
		BindingName("team-a", "u:hash", "pod-reader"),
		BindingName("team-a", "g:devs", "secret-reader"),
	} {
		if a == b {
			t.Fatalf("distinct triples collided on %q", a)
		}
	}
}

func TestQuotaNameDistinct(t *testing.T) {
	if QuotaName("team-a") == QuotaName("team-b") {
		t.Fatal("distinct owners collided")
	}
}
