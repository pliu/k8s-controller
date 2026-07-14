// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	api "github.com/pliu/rbac-controller/api/v1alpha1"
	"github.com/pliu/rbac-controller/internal/auth"
	"github.com/pliu/rbac-controller/internal/config"
	s "github.com/pliu/rbac-controller/internal/server"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"net/http"
	"os"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"strings"
	"time"
)

func main() {
	cfg := ctrl.GetConfigOrDie()
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = api.AddToScheme(scheme)
	c, e := client.New(cfg, client.Options{Scheme: scheme})
	if e != nil {
		panic(e)
	}
	k, e := kubernetes.NewForConfig(cfg)
	if e != nil {
		panic(e)
	}
	ns := os.Getenv("POD_NAMESPACE")
	if ns == "" {
		ns = "rbac-controller"
	}
	resolver := config.Resolver()
	admins := map[string]bool{}
	for _, v := range strings.Split(os.Getenv("ADMIN_IDENTITIES"), ",") {
		if v = strings.TrimSpace(v); v != "" {
			admins[v] = true
		}
	}
	if v := os.Getenv("ADMIN_USERNAME"); v != "" {
		admins[v] = true
	}
	endpoint := os.Getenv("KUBERNETES_PUBLIC_SERVER")
	if endpoint == "" {
		endpoint = cfg.Host
	}
	app := &s.Server{Client: c, Kube: k, Auth: auth.Header{Header: os.Getenv("AUTH_USERNAME_HEADER"), Resolver: resolver}, Resolver: resolver, Namespace: ns, APIServer: endpoint, CA: cfg.CAData, Admins: admins}
	httpServer := &http.Server{Addr: ":8080", Handler: app.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 2 * time.Minute}
	if e = httpServer.ListenAndServe(); e != nil {
		panic(e)
	}
}
