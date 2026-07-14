// SPDX-License-Identifier: AGPL-3.0-only
package directory

import "testing"

func TestObjectGUIDFormatting(t *testing.T) {
	raw := []byte{0xff, 0x19, 0x96, 0x6f, 0x86, 0x8b, 0x11, 0xd0, 0xb4, 0x2d, 0x00, 0xc0, 0x4f, 0xc9, 0x64, 0xff}
	if got, want := groupID("objectGUID", raw), "6f9619ff-8b86-d011-b42d-00c04fc964ff"; got != want {
		t.Fatalf("groupID() = %q, want %q", got, want)
	}
}
