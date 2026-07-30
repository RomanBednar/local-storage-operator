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

package tls

import (
	"context"
	"os"

	configv1 "github.com/openshift/api/config/v1"
	crcommon "github.com/openshift/controller-runtime-common/pkg/tls"
	"k8s.io/klog/v2"
)

// NewSecurityProfileWatcher creates a SecurityProfileWatcher that triggers operator
// restart when TLS profile or adherence policy changes.
// The initial TLS profile spec and adherence policy are passed in so the watcher
// knows the baseline configuration from operator startup.
func NewSecurityProfileWatcher(
	initialProfile configv1.TLSProfileSpec,
	initialAdherence configv1.TLSAdherencePolicy,
) *crcommon.SecurityProfileWatcher {
	return &crcommon.SecurityProfileWatcher{
		InitialTLSProfileSpec:     initialProfile,
		InitialTLSAdherencePolicy: initialAdherence,
		OnProfileChange: func(ctx context.Context, oldProfile, newProfile configv1.TLSProfileSpec) {
			klog.Infof("TLS profile changed, restarting operator to apply new configuration")
			klog.V(2).Infof("Old profile: %+v, New profile: %+v", oldProfile, newProfile)
			os.Exit(0) // Kubernetes will restart the pod
		},
		OnAdherencePolicyChange: func(ctx context.Context, oldPolicy, newPolicy configv1.TLSAdherencePolicy) {
			klog.Infof("TLS adherence policy changed from %s to %s, restarting operator",
				oldPolicy, newPolicy)
			os.Exit(0)
		},
	}
}
