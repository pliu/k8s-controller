// SPDX-License-Identifier: AGPL-3.0-only
package auth

import (
	"context"
	"errors"
	"github.com/pliu/rbac-controller/internal/directory"
	"net/http"
	"strings"
)

type Identity struct {
	Username string   `json:"username"`
	ADGroups []string `json:"adGroups"`
}
type Header struct {
	Header   string
	Resolver directory.Resolver
}
type Authenticator interface {
	Authenticate(*http.Request) (Identity, error)
}

func (a Header) Authenticate(r *http.Request) (Identity, error) {
	h := a.Header
	if h == "" {
		h = "X-Remote-User"
	}
	v := r.Header.Values(h)
	if len(v) != 1 || strings.TrimSpace(v[0]) == "" || strings.Contains(v[0], ",") {
		return Identity{}, errors.New("invalid identity header")
	}
	i := Identity{Username: v[0]}
	if a.Resolver != nil {
		g, e := a.Resolver.Resolve(r.Context(), i.Username)
		if e == nil {
			i.ADGroups = g.Groups
		}
	}
	return i, nil
}

type key struct{}

func Middleware(a Authenticator, n http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i, e := a.Authenticate(r)
		if e != nil {
			http.Error(w, "unauthorized", 401)
			return
		}
		n.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), key{}, i)))
	})
}
func From(ctx context.Context) Identity { i, _ := ctx.Value(key{}).(Identity); return i }
