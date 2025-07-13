// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package admission

import (
	"context"
	"reflect"
	"testing"

	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

const (
	SchedulerName = "slurm-bridge-scheduler"
	namespace     = "slinky"
)

var _ = Describe("Pod Controller", func() {
	Context("SetupWithManager()", func() {
		It("Should initialize successfully", func() {
			mgr, err := ctrl.NewManager(cfg, ctrl.Options{Scheme: scheme.Scheme})
			Expect(err).ToNot(HaveOccurred())

			r := &PodAdmission{}
			err = r.SetupWebhookWithManager(mgr)
			Expect(err).ToNot(HaveOccurred())
		})
	})
})

func TestPodAdmission_Default(t *testing.T) {
	type args struct {
		ctx context.Context
		obj runtime.Object
	}
	tests := []struct {
		name    string
		args    args
		wantErr bool
	}{
		{
			name: "Pod",
			args: args{
				ctx: context.TODO(),
				obj: &corev1.Pod{},
			},
			wantErr: false,
		},
		{
			name: "Not Pod",
			args: args{
				ctx: context.TODO(),
				obj: &corev1.Node{},
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &PodAdmission{}
			if err := r.Default(tt.args.ctx, tt.args.obj); (err != nil) != tt.wantErr {
				t.Errorf("PodAdmission.Default() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

var _ = Describe("Admission Controller", func() {
	Context("SetupWithManager()", func() {
		It("Should have correct maps between expected schedulers", func() {
			mgr, err := ctrl.NewManager(cfg, ctrl.Options{Scheme: scheme.Scheme})
			Expect(err).ToNot(HaveOccurred())

			r := &PodAdmission{}
			err = r.SetupWebhookWithManager(mgr)
			Expect(err).ToNot(HaveOccurred())

			// Test that the webhook is correctly registered
			Expect(mgr.GetWebhookServer()).NotTo(BeNil())
		})
	})
})

func TestPodAdmission_Namespaces(t *testing.T) {

	type args struct {
		ctx context.Context
		pod *corev1.Pod
	}
	tests := []struct {
		name    string
		args    args
		wantErr bool
		sched   string
	}{
		{
			name: "PodWithDefaultNamespace",
			args: args{
				ctx: context.TODO(),
				pod: &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pod", // Name of the Pod
						Namespace: "default",  // Namespace where the Pod is located
						Labels: map[string]string{ // Pod labels
							"app": "test-app",
						},
					},
					Spec: corev1.PodSpec{
						SchedulerName: "test-scheduler", // Custom scheduler
						Containers: []corev1.Container{ // Define containers in the pod
							{
								Name:  "test-container", // Name of the container
								Image: "test-image",     // Image to be used for the container
							},
						},
					},
				},
			},
			wantErr: false,
			sched:   "test-scheduler",
		},
		{
			name: "PodWithDefaultSchedulerAndInNamepsace",
			args: args{
				ctx: context.TODO(),
				pod: &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pod", // Name of the Pod
						Namespace: namespace,  // Namespace where the Pod is located
						Labels: map[string]string{ // Pod labels
							"app": "test-app",
						},
					},
					Spec: corev1.PodSpec{
						SchedulerName: corev1.DefaultSchedulerName,
						Containers: []corev1.Container{ // Define containers in the pod
							{
								Name:  "test-container", // Name of the container
								Image: "test-image",     // Image to be used for the container
							},
						},
					},
				},
			},
			wantErr: false,
			sched:   SchedulerName,
		},
		{
			name: "PodWithCustomSchedulerAndInNamepsace",
			args: args{
				ctx: context.TODO(),
				pod: &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pod", // Name of the Pod
						Namespace: namespace,  // Namespace where the Pod is located
						Labels: map[string]string{ // Pod labels
							"app": "test-app",
						},
					},
					Spec: corev1.PodSpec{
						SchedulerName: "custom-scheduler",
						Containers: []corev1.Container{ // Define containers in the pod
							{
								Name:  "test-container", // Name of the container
								Image: "test-image",     // Image to be used for the container
							},
						},
					},
				},
			},
			wantErr: false,
			sched:   "custom-scheduler",
		},
		{
			name: "PodInNamespace",
			args: args{
				ctx: context.TODO(),
				pod: &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pod", // Name of the Pod
						Namespace: namespace,  // Namespace where the Pod is located
						Labels: map[string]string{ // Pod labels
							"app": "test-app",
						},
					},
					Spec: corev1.PodSpec{
						SchedulerName: corev1.DefaultSchedulerName,
						Containers: []corev1.Container{ // Define containers in the pod
							{
								Name:  "test-container", // Name of the container
								Image: "test-image",     // Image to be used for the container
							},
						},
					},
				},
			},
			wantErr: false,
			sched:   SchedulerName,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &PodAdmission{
				ManagedNamespaces: []string{namespace},
				SchedulerName:     SchedulerName,
			}

			if err := r.Default(tt.args.ctx, tt.args.pod); (err != nil) != tt.wantErr {
				t.Errorf("PodAdmission.Default() error = %v, wantErr %v", err, tt.wantErr)
			}

			// Verify the schedulerName remains "existing-scheduler"
			if tt.args.pod.Spec.SchedulerName != tt.sched {
				t.Errorf("PodAdmission.Default() scheduler = %s, want scheduler %s", tt.args.pod.Spec.SchedulerName, tt.sched)
			}
		})
	}
}

func TestPodAdmission_ValidateCreate(t *testing.T) {
	type fields struct {
		SchedulerName     string
		ManagedNamespaces []string
	}
	type args struct {
		ctx context.Context
		obj runtime.Object
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		want    admission.Warnings
		wantErr bool
	}{
		{
			name: "NotAPod",
			fields: fields{
				ManagedNamespaces: []string{namespace},
			},
			args: args{
				ctx: context.TODO(),
				obj: &corev1.Node{},
			},
			want:    nil,
			wantErr: true,
		},
		{
			name: "PodWithDefaultNamespace is ignored",
			fields: fields{
				ManagedNamespaces: []string{namespace},
			},
			args: args{
				ctx: context.TODO(),
				obj: &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: metav1.NamespaceDefault,
					},
				},
			},
			want:    nil,
			wantErr: false,
		},
		{
			name: "PodWithJobID",
			fields: fields{
				ManagedNamespaces: []string{namespace},
			},
			args: args{
				ctx: context.TODO(),
				obj: &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: namespace,
						Labels: map[string]string{
							wellknown.LabelPlaceholderJobId: "1",
						},
					},
				},
			},
			want:    nil,
			wantErr: true,
		},
		{
			name: "PodWithNode",
			fields: fields{
				ManagedNamespaces: []string{namespace},
			},
			args: args{
				ctx: context.TODO(),
				obj: &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: namespace,
						Annotations: map[string]string{
							wellknown.AnnotationPlaceholderNode: "foo",
						},
					},
				},
			},
			want:    nil,
			wantErr: true,
		},
		{
			name: "PodWithoutLabelOrAnnotation",
			fields: fields{
				ManagedNamespaces: []string{namespace},
			},
			args: args{
				ctx: context.TODO(),
				obj: &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: namespace,
					},
				},
			},
			want:    nil,
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &PodAdmission{
				SchedulerName:     tt.fields.SchedulerName,
				ManagedNamespaces: tt.fields.ManagedNamespaces,
			}
			got, err := r.ValidateCreate(tt.args.ctx, tt.args.obj)
			if (err != nil) != tt.wantErr {
				t.Errorf("PodAdmission.ValidateCreate() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("PodAdmission.ValidateCreate() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPodAdmission_ValidateUpdate(t *testing.T) {
	type fields struct {
		SchedulerName     string
		ManagedNamespaces []string
	}
	type args struct {
		ctx    context.Context
		oldObj runtime.Object
		newObj runtime.Object
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		want    admission.Warnings
		wantErr bool
	}{
		{
			name: "PodWithDefaultNamespace is ignored",
			fields: fields{
				ManagedNamespaces: []string{namespace},
			},
			args: args{
				ctx: context.TODO(),
				oldObj: &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: metav1.NamespaceDefault,
					},
				},
				newObj: &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: metav1.NamespaceDefault,
					},
				},
			},
			want:    nil,
			wantErr: false,
		},
		{
			name: "PendingPodCanChange",
			fields: fields{
				ManagedNamespaces: []string{namespace},
			},
			args: args{
				ctx: context.TODO(),
				oldObj: &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: namespace,
					},
				},
				newObj: &corev1.Pod{
					Status: corev1.PodStatus{
						Phase: corev1.PodPending,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: namespace,
						Labels: map[string]string{
							wellknown.LabelPlaceholderJobId: "1",
						},
					},
				},
			},
			want:    nil,
			wantErr: false,
		},
		{
			name: "RunningPodCantChangeJobID",
			fields: fields{
				ManagedNamespaces: []string{namespace},
			},
			args: args{
				ctx: context.TODO(),
				oldObj: &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: namespace,
						Labels: map[string]string{
							wellknown.LabelPlaceholderJobId: "1",
						},
					},
				},
				newObj: &corev1.Pod{
					Status: corev1.PodStatus{
						Phase: corev1.PodRunning,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: namespace,
						Labels: map[string]string{
							wellknown.LabelPlaceholderJobId: "2",
						},
					},
				},
			},
			want:    nil,
			wantErr: true,
		},
		{
			name: "RunningPodCantChangeNode",
			fields: fields{
				ManagedNamespaces: []string{namespace},
			},
			args: args{
				ctx: context.TODO(),
				oldObj: &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: namespace,
						Annotations: map[string]string{
							wellknown.AnnotationPlaceholderNode: "node1",
						},
					},
				},
				newObj: &corev1.Pod{
					Status: corev1.PodStatus{
						Phase: corev1.PodRunning,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: namespace,
						Labels: map[string]string{
							wellknown.AnnotationPlaceholderNode: "node2",
						},
					},
				},
			},
			want:    nil,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &PodAdmission{
				SchedulerName:     tt.fields.SchedulerName,
				ManagedNamespaces: tt.fields.ManagedNamespaces,
			}
			got, err := r.ValidateUpdate(tt.args.ctx, tt.args.oldObj, tt.args.newObj)
			if (err != nil) != tt.wantErr {
				t.Errorf("PodAdmission.ValidateUpdate() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("PodAdmission.ValidateUpdate() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPodAdmission_ValidateDelete(t *testing.T) {
	type fields struct {
		SchedulerName     string
		ManagedNamespaces []string
	}
	type args struct {
		ctx context.Context
		obj runtime.Object
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		want    admission.Warnings
		wantErr bool
	}{
		{
			name:    "NoopDelete",
			want:    nil,
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &PodAdmission{
				SchedulerName:     tt.fields.SchedulerName,
				ManagedNamespaces: tt.fields.ManagedNamespaces,
			}
			got, err := r.ValidateDelete(tt.args.ctx, tt.args.obj)
			if (err != nil) != tt.wantErr {
				t.Errorf("PodAdmission.ValidateDelete() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("PodAdmission.ValidateDelete() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPodAdmission_NamespaceSelector(t *testing.T) {
	tests := []struct {
		name                     string
		managedNamespaceSelector *metav1.LabelSelector
		managedNamespaces        []string
		namespace                *corev1.Namespace
		pod                      *corev1.Pod
		expectedManaged          bool
	}{
		{
			name: "namespace matches selector",
			managedNamespaceSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"managed": "true"},
			},
			namespace: &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name:   "managed-ns",
					Labels: map[string]string{"managed": "true"},
				},
			},
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "managed-ns",
				},
				Spec: corev1.PodSpec{
					SchedulerName: corev1.DefaultSchedulerName,
				},
			},
			expectedManaged: true,
		},
		{
			name: "namespace does not match selector",
			managedNamespaceSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"managed": "true"},
			},
			namespace: &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name:   "unmanaged-ns",
					Labels: map[string]string{"managed": "false"},
				},
			},
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "unmanaged-ns",
				},
				Spec: corev1.PodSpec{
					SchedulerName: corev1.DefaultSchedulerName,
				},
			},
			expectedManaged: false,
		},
		{
			name:              "selector is nil, fallback to managedNamespaces",
			managedNamespaces: []string{"managed-ns"},
			namespace: &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "managed-ns",
				},
			},
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "managed-ns",
				},
				Spec: corev1.PodSpec{
					SchedulerName: corev1.DefaultSchedulerName,
				},
			},
			expectedManaged: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeClient := fake.NewClientBuilder().WithRuntimeObjects(tt.namespace).Build()
			r := &PodAdmission{
				Client:                   fakeClient,
				SchedulerName:            SchedulerName,
				ManagedNamespaces:        tt.managedNamespaces,
				ManagedNamespaceSelector: tt.managedNamespaceSelector,
			}

			err := r.Default(context.TODO(), tt.pod)
			if err != nil {
				t.Fatalf("Default() returned an unexpected error: %v", err)
			}

			if tt.expectedManaged {
				if tt.pod.Spec.SchedulerName != SchedulerName {
					t.Errorf("expected scheduler name to be %q, but got %q", SchedulerName, tt.pod.Spec.SchedulerName)
				}
			} else {
				if tt.pod.Spec.SchedulerName == SchedulerName {
					t.Errorf("scheduler name is %q, but should not be", SchedulerName)
				}
			}
		})
	}
}
