// SPDX-License-Identifier: AGPL-3.0-only
package config

import (
	"github.com/pliu/rbac-controller/internal/directory"
	"os"
	"strings"
)

func Resolver() directory.Resolver {
	u := os.Getenv("LDAP_URL")
	if u == "" {
		return directory.None{}
	}
	p := os.Getenv("LDAP_BIND_PASSWORD")
	if f := os.Getenv("LDAP_BIND_PASSWORD_FILE"); f != "" {
		if b, e := os.ReadFile(f); e == nil {
			p = strings.TrimSpace(string(b))
		}
	}
	return directory.LDAP{URL: u, BindDN: os.Getenv("LDAP_BIND_DN"), Password: p, UserBase: os.Getenv("LDAP_USER_BASE_DN"), GroupBase: os.Getenv("LDAP_GROUP_BASE_DN"), UserAttribute: os.Getenv("LDAP_USER_ATTRIBUTE"), GroupAttribute: os.Getenv("LDAP_GROUP_ATTRIBUTE")}
}
