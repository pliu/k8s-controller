// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	api "github.com/pliu/rbac-controller/api/v1alpha1"
	"github.com/pliu/rbac-controller/internal/config"
	c "github.com/pliu/rbac-controller/internal/controller"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"os"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"time"
)

func main() {
	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	_ = api.AddToScheme(s)
	m, e := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{Scheme: s, LeaderElection: true, LeaderElectionID: "rbac-controller.rbac.pliu.dev", HealthProbeBindAddress: ":8081"})
	if e != nil {
		panic(e)
	}
	ns := os.Getenv("POD_NAMESPACE")
	if ns == "" {
		ns = "rbac-controller"
	}
	if e = (&c.Reconciler{Client: m.GetClient(), Resolver: config.Resolver(), Namespace: ns, Refresh: 5 * time.Minute, Stale: 15 * time.Minute}).Setup(m); e != nil {
		panic(e)
	}
	if e = (&c.MappingReconciler{Client: m.GetClient()}).Setup(m); e != nil {
		panic(e)
	}
	_ = m.AddHealthzCheck("healthz", healthz.Ping)
	_ = m.AddReadyzCheck("readyz", healthz.Ping)
	if e = m.Start(ctrl.SetupSignalHandler()); e != nil {
		panic(e)
	}
}
