// Command moco-controller reconciles one namespace against one Moco instance.
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	v1 "github.com/araihu/moco/controller/api/v1alpha1"
	"github.com/araihu/moco/controller/internal/reconcile"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metrics "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

func run() error {
	var config reconcile.Config
	flag.StringVar(&config.Namespace, "namespace", os.Getenv("POD_NAMESPACE"), "Only namespace to manage; one installation per Moco instance")
	flag.StringVar(&config.Secret, "credentials-secret", "moco-controller-credentials", "Same-namespace Secret with token and ca.pem keys")
	flag.StringVar(&config.PublicURL, "public-url", "", "Moco HTTPS public origin")
	flag.StringVar(&config.AdminURL, "admin-url", "", "Moco HTTPS private administrative origin")
	flag.StringVar(&config.Subject, "subject", "moco-controller", "Principal ID associated with the management token")
	flag.DurationVar(&config.Interval, "reconcile-interval", 30*time.Second, "Drift, dependency, and credential refresh interval (at least 1s)")
	opts := zap.Options{}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))
	if config.Namespace == "" || config.Secret == "" || config.Subject == "" || config.PublicURL == "" || config.AdminURL == "" || config.Interval < time.Second {
		return fmt.Errorf("namespace, credentials-secret, subject, HTTPS origins and interval >=1s are required")
	}
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		return err
	}
	if err := v1.AddToScheme(scheme); err != nil {
		return err
	}
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{Scheme: scheme,
		Cache:          cache.Options{DefaultNamespaces: map[string]cache.Config{config.Namespace: {}}},
		LeaderElection: true, LeaderElectionID: "moco-controller", LeaderElectionNamespace: config.Namespace,
		Metrics: metrics.Options{BindAddress: "0"}, HealthProbeBindAddress: ":8081"})
	if err != nil {
		return err
	}
	for _, kind := range []string{"tenant", "vault", "authorization"} {
		r := &reconcile.Reconciler{Client: mgr.GetClient(), Reader: mgr.GetAPIReader(), Config: config, Kind: kind}
		if err := r.Setup(mgr); err != nil {
			return err
		}
	}
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return err
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		return err
	}
	return mgr.Start(ctrl.SetupSignalHandler())
}

func main() {
	if err := run(); err != nil {
		ctrl.Log.Error(err, "controller stopped")
		os.Exit(1)
	}
}
