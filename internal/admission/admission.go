// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package admission

import (
	"context"
	"fmt"
	"slices"

	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

type PodAdmission struct {
	SchedulerName     string
	ManagedNamespaces []string `yaml:"managedNamespaces"`
}

func (r *PodAdmission) SetupWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(&corev1.Pod{}).
		WithDefaulter(r).
		WithValidator(r).
		Complete()
}

// +kubebuilder:webhook:path=/mutate--v1-pod,mutating=true,failurePolicy=fail,sideEffects=None,groups="",resources=pods,verbs=create;update,versions=v1,name=mcluster.kb.io,admissionReviewVersions=v1

var _ webhook.CustomDefaulter = &PodAdmission{}

func (r *PodAdmission) Default(ctx context.Context, obj runtime.Object) error {
	logger := log.FromContext(ctx)
	pod, ok := obj.(*corev1.Pod)
	if !ok {
		return fmt.Errorf("expected a Pod but got a %T", obj)
	}
	logger.V(1).Info("Defaulting", "pod", klog.KObj(pod), "pod.Spec.SchedulerName", pod.Spec.SchedulerName)
	if !r.isManagedNamespace(pod.Namespace) {
		return nil
	}
	if pod.Spec.SchedulerName == corev1.DefaultSchedulerName {
		pod.Spec.SchedulerName = r.SchedulerName
	}
	return nil
}

// +kubebuilder:webhook:path=/validate--v1-pod,mutating=false,failurePolicy=fail,sideEffects=None,groups="",resources=pods,verbs=create;update,versions=v1,name=mcluster.kb.io,admissionReviewVersions=v1

var _ webhook.CustomValidator = &PodAdmission{}

func (r *PodAdmission) ValidateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	logger := log.FromContext(ctx)
	pod, ok := obj.(*corev1.Pod)
	if !ok {
		return nil, fmt.Errorf("expected a Pod but got a %T", obj)
	}
	logger.V(1).Info("ValidateCreate", "pod", klog.KObj(pod))
	if !r.isManagedNamespace(pod.Namespace) {
		return nil, nil
	}
	if pod.Labels[wellknown.LabelPlaceholderJobId] != "" {
		return nil, fmt.Errorf("can't create a pod with a slurm placeholder jobid label")
	}
	if pod.Annotations[wellknown.AnnotationPlaceholderNode] != "" {
		return nil, fmt.Errorf("can't create a pod with a slurm placeholder node annotation")
	}
	return nil, nil
}

func (r *PodAdmission) ValidateUpdate(ctx context.Context, oldObj runtime.Object, newObj runtime.Object) (admission.Warnings, error) {
	logger := log.FromContext(ctx)
	newPod := newObj.(*corev1.Pod)
	oldPod := oldObj.(*corev1.Pod)
	logger.V(1).Info("ValidateUpdate", "newPod", klog.KObj(newPod), "oldPod", klog.KObj(oldPod))
	if !r.isManagedNamespace(newPod.Namespace) {
		return nil, nil
	}
	// Once a pod has been placed by the Slurm bridge scheduler the jobid and
	// node annotations should not be modified.
	if newPod.Status.Phase == corev1.PodRunning {
		if newPod.Labels[wellknown.LabelPlaceholderJobId] !=
			oldPod.Labels[wellknown.LabelPlaceholderJobId] {
			return nil, fmt.Errorf("can't update a running pod's placeholder jobid label")
		}
		if newPod.Annotations[wellknown.AnnotationPlaceholderNode] !=
			oldPod.Annotations[wellknown.AnnotationPlaceholderNode] {
			return nil, fmt.Errorf("can't update a running pod's placeholder node annotation")
		}
	}
	return nil, nil
}

// ValidateDelete implements webhook.Validator so a webhook will be registered for the type
func (r *PodAdmission) ValidateDelete(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	return nil, nil
}

func (r *PodAdmission) isManagedNamespace(namespace string) bool {
	return slices.Contains(r.ManagedNamespaces, namespace)
}
