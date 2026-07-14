// SPDX-License-Identifier: AGPL-3.0-only
package directory

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"github.com/go-ldap/ldap/v3"
	"net"
	"net/url"
	"sort"
	"strings"
	"time"
)

type Result struct {
	Groups []string
	At     time.Time
}
type Resolver interface {
	Resolve(context.Context, string) (Result, error)
}
type None struct{}

func (None) Resolve(context.Context, string) (Result, error) {
	return Result{At: time.Now().UTC()}, nil
}

type LDAP struct {
	URL, BindDN, Password, UserBase, GroupBase, UserAttribute, GroupAttribute string
	Timeout                                                                   time.Duration
}

func (l LDAP) Resolve(ctx context.Context, user string) (Result, error) {
	if l.Timeout == 0 {
		l.Timeout = 5 * time.Second
	}
	if l.UserAttribute == "" {
		l.UserAttribute = "sAMAccountName"
	}
	if l.GroupAttribute == "" {
		l.GroupAttribute = "objectGUID"
	}
	if l.GroupBase == "" {
		l.GroupBase = l.UserBase
	}
	u, e := url.Parse(l.URL)
	if e != nil {
		return Result{}, e
	}
	tc := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: u.Hostname()}
	opts := []ldap.DialOpt{ldap.DialWithDialer(&net.Dialer{Timeout: l.Timeout})}
	if u.Scheme == "ldaps" {
		opts = append(opts, ldap.DialWithTLSConfig(tc))
	}
	c, e := ldap.DialURL(l.URL, opts...)
	if e != nil {
		return Result{}, e
	}
	defer c.Close()
	c.SetTimeout(l.Timeout)
	if u.Scheme == "ldap" {
		if e = c.StartTLS(tc); e != nil {
			return Result{}, e
		}
	}
	if l.BindDN != "" {
		if e = c.Bind(l.BindDN, l.Password); e != nil {
			return Result{}, e
		}
	}
	f := fmt.Sprintf("(&(objectClass=user)(%s=%s))", l.UserAttribute, ldap.EscapeFilter(user))
	us, e := c.Search(ldap.NewSearchRequest(l.UserBase, ldap.ScopeWholeSubtree, ldap.NeverDerefAliases, 2, 5, false, f, []string{"dn"}, nil))
	if e != nil || len(us.Entries) != 1 {
		return Result{}, fmt.Errorf("user lookup returned %d entries: %w", len(us.Entries), e)
	}
	f = fmt.Sprintf("(&(objectClass=group)(member:1.2.840.113556.1.4.1941:=%s))", ldap.EscapeFilter(us.Entries[0].DN))
	rs, e := c.Search(ldap.NewSearchRequest(l.GroupBase, ldap.ScopeWholeSubtree, ldap.NeverDerefAliases, 2000, 5, false, f, []string{l.GroupAttribute}, nil))
	if e != nil {
		return Result{}, e
	}
	g := []string{}
	for _, x := range rs.Entries {
		for _, raw := range x.GetRawAttributeValues(l.GroupAttribute) {
			if v := groupID(l.GroupAttribute, raw); v != "" {
				g = append(g, v)
			}
		}
	}
	sort.Strings(g)
	return Result{Groups: g, At: time.Now().UTC()}, ctx.Err()
}

func groupID(attribute string, raw []byte) string {
	if !strings.EqualFold(attribute, "objectGUID") || len(raw) != 16 {
		return string(raw)
	}
	return fmt.Sprintf("%08x-%04x-%04x-%02x%02x-%02x%02x%02x%02x%02x%02x",
		binary.LittleEndian.Uint32(raw[0:4]), binary.LittleEndian.Uint16(raw[4:6]), binary.LittleEndian.Uint16(raw[6:8]),
		raw[8], raw[9], raw[10], raw[11], raw[12], raw[13], raw[14], raw[15])
}
