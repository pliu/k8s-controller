// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

// invalidReferences registers into controller-runtime's global registry, which
// already carries the per-controller reconcile and workqueue metrics, so one
// /metrics endpoint exposes everything.
var invalidReferences = prometheus.NewGaugeVec(prometheus.GaugeOpts{
	Name: "k8s_controller_invalid_references",
	Help: "Invalid ClusterRole references currently reported in the object's status.",
}, []string{"controller", "name"})

func init() {
	metrics.Registry.MustRegister(invalidReferences)
}
