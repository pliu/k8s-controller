// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"context"
	"flag"
	"net/http"
	"time"

	api "github.com/pliu/k8s-controller/api/v1alpha1"
	c "github.com/pliu/k8s-controller/internal/controller"
	s "github.com/pliu/k8s-controller/internal/server"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
)

func main() {
	runController := flag.Bool("controller", false, "run the reconciler (default: run both when neither -controller nor -server is given)")
	runServer := flag.Bool("server", false, "run the HTTP API and UI (default: run both when neither -controller nor -server is given)")
	syncPeriod := flag.Duration("sync-period", time.Hour, "interval at which the reconciler re-syncs every ManagedNamespace as a drift-repair safety net")
	flag.Parse()
	controllerEnabled, serverEnabled := *runController, *runServer
	if !controllerEnabled && !serverEnabled {
		controllerEnabled, serverEnabled = true, true
	}

	cfg := ctrl.GetConfigOrDie()
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = api.AddToScheme(scheme)
	ctx := ctrl.SetupSignalHandler()

	// The HTTP server is stateless and needs neither a manager nor leader
	// election, so run it directly when the reconciler is not also enabled.
	if serverEnabled && !controllerEnabled {
		if e := serve(ctx, cfg, scheme); e != nil {
			panic(e)
		}
		return
	}

	mgr, e := ctrl.NewManager(cfg, ctrl.Options{Scheme: scheme, LeaderElection: true, LeaderElectionID: "k8s-controller.k8s.pliu.dev", HealthProbeBindAddress: ":8081", Cache: cache.Options{SyncPeriod: syncPeriod}})
	if e != nil {
		panic(e)
	}
	if e = (&c.Reconciler{Client: mgr.GetClient()}).Setup(mgr); e != nil {
		panic(e)
	}
	if e = (&c.ClusterAccessReconciler{Client: mgr.GetClient()}).Setup(mgr); e != nil {
		panic(e)
	}
	if serverEnabled {
		// Register as a non-leader-election runnable so the API is served by
		// every replica, not only the one holding the reconciler lease.
		if e = mgr.Add(&serverRunnable{cfg: cfg, scheme: scheme}); e != nil {
			panic(e)
		}
	}
	_ = mgr.AddHealthzCheck("healthz", healthz.Ping)
	_ = mgr.AddReadyzCheck("readyz", healthz.Ping)
	if e = mgr.Start(ctx); e != nil {
		panic(e)
	}
}

type serverRunnable struct {
	cfg    *rest.Config
	scheme *runtime.Scheme
}

func (*serverRunnable) NeedLeaderElection() bool          { return false }
func (r *serverRunnable) Start(ctx context.Context) error { return serve(ctx, r.cfg, r.scheme) }

func serve(ctx context.Context, cfg *rest.Config, scheme *runtime.Scheme) error {
	cl, e := client.New(cfg, client.Options{Scheme: scheme})
	if e != nil {
		return e
	}
	app := &s.Server{Client: cl}
	httpServer := &http.Server{Addr: ":8080", Handler: app.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 2 * time.Minute}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(sctx)
	}()
	if e := httpServer.ListenAndServe(); e != nil && e != http.ErrServerClosed {
		return e
	}
	return nil
}
