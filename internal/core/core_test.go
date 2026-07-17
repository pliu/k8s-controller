// SPDX-License-Identifier: AGPL-3.0-only
package core

import "testing"

func TestBindingName(t *testing.T) {
	a := BindingName("team-readers", "pod-reader", "team-a")
	if a != BindingName("team-readers", "pod-reader", "team-a") {
		t.Fatal("BindingName is not deterministic")
	}
	for _, b := range []string{
		BindingName("team-writers", "pod-reader", "team-a"),
		BindingName("team-readers", "secret-reader", "team-a"),
		BindingName("team-readers", "pod-reader", "team-b"),
		BindingName("team-readers", "pod-reader", "*"),
	} {
		if a == b {
			t.Fatalf("distinct triples collided on %q", a)
		}
	}
}
