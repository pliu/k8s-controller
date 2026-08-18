// SPDX-License-Identifier: Apache-2.0
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
