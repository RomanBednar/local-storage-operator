/*
Copyright 2021 The Local Storage Operator Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"context"
	"crypto/tls"
	"flag"
	"os"
	"runtime"
	"time"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.

	_ "k8s.io/client-go/plugin/pkg/client/auth"
	"k8s.io/klog/v2"

	configv1 "github.com/openshift/api/config/v1"
	crcommon "github.com/openshift/controller-runtime-common/pkg/tls"
	libcrypto "github.com/openshift/library-go/pkg/crypto"
	localv1 "github.com/openshift/local-storage-operator/api/v1"
	localv1alpha1 "github.com/openshift/local-storage-operator/api/v1alpha1"
	"github.com/openshift/local-storage-operator/pkg/common"
	lvcontroller "github.com/openshift/local-storage-operator/pkg/controllers/localvolume"
	lvdcontroller "github.com/openshift/local-storage-operator/pkg/controllers/localvolumediscovery"
	lvscontroller "github.com/openshift/local-storage-operator/pkg/controllers/localvolumeset"
	nodedaemoncontroller "github.com/openshift/local-storage-operator/pkg/controllers/nodedaemon"
	lsotls "github.com/openshift/local-storage-operator/pkg/tls"
	"github.com/openshift/local-storage-operator/pkg/utils"
	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	zaplog "go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	apiruntime "k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	//+kubebuilder:scaffold:imports
)

var (
	scheme  = apiruntime.NewScheme()
	version = "unknown"
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(configv1.AddToScheme(scheme))
	utilruntime.Must(localv1.AddToScheme(scheme))
	utilruntime.Must(localv1alpha1.AddToScheme(scheme))
	utilruntime.Must(monitoringv1.AddToScheme(scheme))
	//+kubebuilder:scaffold:scheme
}

func printVersion() {
	klog.Infof("Go Version: %s", runtime.Version())
	klog.Infof("Go OS/Arch: %s/%s", runtime.GOOS, runtime.GOARCH)
	klog.Infof("local-storage-diskmaker Version: %v", version)
}

func main() {
	var metricsAddr string
	var enableLeaderElection bool
	var probeAddr string

	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	klogFlags := flag.NewFlagSet("local-storage-operator", flag.ExitOnError)
	klog.InitFlags(klogFlags)
	opts := zap.Options{
		Development: true,
		ZapOpts:     []zaplog.Option{zaplog.AddCaller()},
		TimeEncoder: zapcore.ISO8601TimeEncoder,
	}

	opts.BindFlags(flag.CommandLine)
	flag.Parse()
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))
	printVersion()

	namespace, err := common.GetWatchNamespace()
	if err != nil {
		klog.ErrorS(err, "Failed to get watch namespace")
		os.Exit(1)
	}

	restConfig := ctrl.GetConfigOrDie()
	le := utils.GetLeaderElectionConfig(restConfig, enableLeaderElection)

	ctx, cancel := context.WithCancel(ctrl.SetupSignalHandler())
	defer cancel()

	startupCtx, startupCancel := context.WithTimeout(ctx, 60*time.Second)
	defer startupCancel()

	configClient, err := client.New(restConfig, client.Options{Scheme: scheme})
	if err != nil {
		klog.ErrorS(err, "unable to create config client for TLS profile")
		os.Exit(1)
	}

	adherence, err := crcommon.FetchAPIServerTLSAdherencePolicy(startupCtx, configClient)
	if err != nil {
		klog.ErrorS(err, "failed to fetch TLS adherence policy")
		os.Exit(1)
	}
	klog.Infof("TLS adherence policy: %s", adherence)

	tlsProfile, err := crcommon.FetchAPIServerTLSProfile(startupCtx, configClient)
	if err != nil {
		klog.ErrorS(err, "failed to fetch TLS profile")
		os.Exit(1)
	}

	var tlsConfigFn func(*tls.Config)
	if libcrypto.ShouldHonorClusterTLSProfile(adherence) {
		klog.Infof("Cluster TLS profile adherence is active, applying cluster TLS configuration")
		var unsupportedCiphers []string
		tlsConfigFn, unsupportedCiphers = crcommon.NewTLSConfigFromProfile(tlsProfile)
		if len(unsupportedCiphers) > 0 {
			klog.Warningf("TLS profile contains %d unsupported cipher suites: %v",
				len(unsupportedCiphers), unsupportedCiphers)
		}
	} else {
		klog.Infof("Cluster TLS profile adherence is not active, applying default Intermediate profile")
		defaultProfile, _ := crcommon.GetTLSProfileSpec(nil)
		tlsConfigFn, _ = crcommon.NewTLSConfigFromProfile(defaultProfile)
	}

	metricsOpts := metricsserver.Options{
		BindAddress:   metricsAddr,
		SecureServing: true,
		TLSOpts:       []func(*tls.Config){tlsConfigFn},
	}

	mgr, err := ctrl.NewManager(restConfig, ctrl.Options{
		Cache: cache.Options{
			DefaultNamespaces: map[string]cache.Config{namespace: {}},
		},
		Scheme:                 scheme,
		Metrics:                metricsOpts,
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		RenewDeadline:          &le.RenewDeadline.Duration,
		RetryPeriod:            &le.RetryPeriod.Duration,
		LeaseDuration:          &le.LeaseDuration.Duration,
		LeaderElectionID:       "98d5776d.storage.openshift.io",
	})
	if err != nil {
		klog.ErrorS(err, "unable to start manager")
		os.Exit(1)
	}

	if err = (&lvcontroller.LocalVolumeReconciler{
		Client: mgr.GetClient(),
		LvMap:  &common.StorageClassOwnerMap{},
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		klog.ErrorS(err, "unable to create LocalVolume controller")
		os.Exit(1)
	}
	if err = (&lvdcontroller.LocalVolumeDiscoveryReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		klog.ErrorS(err, "unable to create LocalVolumeDiscovery controller")
		os.Exit(1)
	}
	if err = (&lvscontroller.LocalVolumeSetReconciler{
		Client:   mgr.GetClient(),
		LvSetMap: &common.StorageClassOwnerMap{},
		Scheme:   mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		klog.ErrorS(err, "unable to create LocalVolumeSet controller")
		os.Exit(1)
	}

	if err = (&nodedaemoncontroller.DaemonReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		klog.ErrorS(err, "unable to create NodeDaemon controller")
		os.Exit(1)
	}

	// Setup TLS security profile watcher to restart operator on TLS profile/adherence changes
	tlsWatcher := lsotls.NewSecurityProfileWatcher(cancel, tlsProfile, adherence)
	tlsWatcher.Client = mgr.GetClient()
	if err = tlsWatcher.SetupWithManager(mgr); err != nil {
		klog.ErrorS(err, "unable to create TLS security profile watcher")
		os.Exit(1)
	}

	//+kubebuilder:scaffold:builder

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		klog.ErrorS(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		klog.ErrorS(err, "unable to set up ready check")
		os.Exit(1)
	}

	klog.Info("starting manager")
	if err := mgr.Start(ctx); err != nil {
		klog.ErrorS(err, "problem running manager")
		os.Exit(1)
	}
}
