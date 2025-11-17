/*
Copyright 2025 Gatus Operator Maintainers.

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

package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"

	appsv1 "k8s.io/api/apps/v1"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"go.yaml.in/yaml/v3"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	monitorv1 "github.com/rverspaij/Gatus-Operator/api/v1"
)

// GatusCheckReconciler reconciles a GatusCheck object
type GatusCheckReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

type GatusConfig struct {
	Enabled   bool       `yaml:"enabled,omitempty"`
	Endpoints []EndPoint `yaml:"endpoints"`
}

type EndPoint struct {
	Name       string   `yaml:"name"`
	Group      string   `yaml:"group,omitempty"`
	URL        string   `yaml:"url"`
	Interval   string   `yaml:"interval,omitempty"`
	Conditions []string `yaml:"conditions"`
}

// +kubebuilder:rbac:groups=monitor.gatus-operator.dev,resources=gatuschecks,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=monitor.gatus-operator.dev,resources=gatuschecks/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=monitor.gatus-operator.dev,resources=gatuschecks/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;update;patch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the GatusCheck object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.22.1/pkg/reconcile
func (r *GatusCheckReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	log.Info("Reconciling GatusCheck", "name", req.Name, "namespace", req.Namespace)

	var gatusList monitorv1.GatusCheckList
	if err := r.List(ctx, &gatusList); err != nil {
		log.Error(err, "failed to list GatusChecks")
		return ctrl.Result{}, nil
	}

	var gatusConfig GatusConfig

	for _, gatus := range gatusList.Items {
		if !gatus.Spec.Enabled {
			continue
		}

		ep := EndPoint{
			Name:       gatus.Name,
			Group:      gatus.Spec.Group,
			URL:        gatus.Spec.URL,
			Interval:   gatus.Spec.Interval,
			Conditions: gatus.Spec.Conditions,
		}

		gatusConfig.Endpoints = append(gatusConfig.Endpoints, ep)
	}

	if len(gatusConfig.Endpoints) > 0 {
		gatusConfig.Enabled = true
	}

	log.Info("Found GatusChecks", "count", len(gatusList.Items))

	yamlData, err := yaml.Marshal(gatusConfig)
	if err != nil {
		log.Error(err, "failed to marshal GatusConfig to YAML")
		return ctrl.Result{}, err
	}

	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "gatus",
			Namespace: "default",
		},
		Data: map[string]string{
			"config.yaml": string(yamlData),
		},
	}
	log.Info("Generated ConfigMap data", "config", string(yamlData))

	var existing corev1.ConfigMap

	if err := r.Get(ctx, client.ObjectKey{
		Namespace: "default",
		Name:      "gatus",
	}, &existing); err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("ConfigMap gatus not found -- Creating a new one")
			if err := r.Create(ctx, configMap); err != nil {
				log.Error(err, "failed to create new configmap")
				return ctrl.Result{}, err
			}
		}
	}

	if existing.Data["config.yaml"] != string(yamlData) {
		log.Info("Config changed, updating configmap")
		existing.Data["config.yaml"] = string(yamlData)
		if err := r.Update(ctx, &existing); err != nil {
			log.Error(err, "failed to update configmap")
			return ctrl.Result{}, err
		}
	}

	hash := sha256.Sum256(yamlData)
	hashString := hex.EncodeToString(hash[:])

	var deployment appsv1.Deployment
	if err := r.Get(ctx, client.ObjectKey{
		Namespace: "default",
		Name:      "gatus",
	}, &deployment); err != nil {
		log.Error(err, "failed to get Gatus Deployment for reload trigger")
		return ctrl.Result{}, err
	}

	if deployment.Spec.Template.Annotations == nil {
		deployment.Spec.Template.Annotations = map[string]string{}
	}

	oldHash := deployment.Spec.Template.Annotations["configHash"]
	if oldHash != hashString {
		log.Info("Configuration changed - triggering Gatus restart", "oldHash", oldHash, "newHash", hashString)
		deployment.Spec.Template.Annotations["configHash"] = hashString

		if err := r.Update(ctx, &deployment); err != nil {
			log.Error(err, "failed to update Gatus Deployment with configHash")
			return ctrl.Result{}, err
		}
	} else {
		log.Info("Gatus deployment is already up-to-date with current configuration")
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *GatusCheckReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&monitorv1.GatusCheck{}).
		Named("gatuscheck").
		Complete(r)
}
