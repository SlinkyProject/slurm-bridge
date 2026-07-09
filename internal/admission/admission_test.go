// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package admission

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/SlinkyProject/slurm-bridge/internal/nodeinfo"
	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
		pod *corev1.Pod
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
				pod: &corev1.Pod{},
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &PodAdmission{}
			if err := r.Default(tt.args.ctx, tt.args.pod); (err != nil) != tt.wantErr {
				t.Errorf("PodAdmission.Default() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateCPUResources(t *testing.T) {
	cpuDRA := corev1.ResourceName(nodeinfo.DraDriverCpu_ExtendedResourceName)
	tests := []struct {
		name    string
		pod     *corev1.Pod
		wantErr bool
	}{
		{
			name: "native CPU only",
			pod: &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{{
				Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{
					corev1.ResourceCPU: resource.MustParse("1"),
				}},
			}}}},
		},
		{
			name: "CPU DRA only",
			pod: &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{{
				Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{
					cpuDRA: resource.MustParse("1"),
				}},
			}}}},
		},
		{
			name: "native and DRA CPU in one container",
			pod: &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{{
				Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{
					corev1.ResourceCPU: resource.MustParse("1"),
					cpuDRA:             resource.MustParse("1"),
				}},
			}}}},
			wantErr: true,
		},
		{
			name: "native CPU in init container and DRA CPU in app container",
			pod: &corev1.Pod{Spec: corev1.PodSpec{
				InitContainers: []corev1.Container{{
					Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("1"),
					}},
				}},
				Containers: []corev1.Container{{
					Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{
						cpuDRA: resource.MustParse("1"),
					}},
				}},
			}},
			wantErr: true,
		},
		{
			name: "native pod-level CPU and container DRA CPU",
			pod: &corev1.Pod{Spec: corev1.PodSpec{
				Resources: &corev1.ResourceRequirements{Requests: corev1.ResourceList{
					corev1.ResourceCPU: resource.MustParse("1"),
				}},
				Containers: []corev1.Container{{
					Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{
						cpuDRA: resource.MustParse("1"),
					}},
				}},
			}},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateCPUResources(tt.pod)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateCPUResources() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateDRADeviceClasses(t *testing.T) {
	resourceName := func(deviceClassName string) corev1.ResourceName {
		return corev1.ResourceName(resourcev1.ResourceDeviceClassPrefix + deviceClassName)
	}
	tests := []struct {
		name            string
		pod             *corev1.Pod
		wantErrContains string
	}{
		{
			name: "supported classes",
			pod: &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{{
				Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{
					resourceName(nodeinfo.DraDriverCpu):       resource.MustParse("1"),
					resourceName(nodeinfo.DraDriverGpuNvidia): resource.MustParse("1"),
					resourceName(nodeinfo.DraExampleDriver):   resource.MustParse("1"),
				}},
			}}}},
		},
		{
			name: "unsupported class in regular container request",
			pod: &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{{
				Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{
					resourceName("other.gpu.example.com"): resource.MustParse("1"),
				}},
			}}}},
			wantErrContains: `unsupported DRA device class "other.gpu.example.com"`,
		},
		{
			name: "unsupported class in init container limit",
			pod: &corev1.Pod{Spec: corev1.PodSpec{InitContainers: []corev1.Container{{
				Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{
					resourceName("other.gpu.example.com"): resource.MustParse("1"),
				}},
			}}}},
			wantErrContains: `unsupported DRA device class "other.gpu.example.com"`,
		},
		{
			name: "non-DRA extended resource",
			pod: &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{{
				Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{
					"example.com/gpu": resource.MustParse("1"),
				}},
			}}}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateDRADeviceClasses(tt.pod)
			if tt.wantErrContains == "" {
				if err != nil {
					t.Fatalf("validateDRADeviceClasses() unexpected error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErrContains) {
				t.Fatalf("validateDRADeviceClasses() error = %v, want error containing %q", err, tt.wantErrContains)
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
		name         string
		args         args
		wantErr      bool
		sched        string
		wantNodeName string
	}{
		{
			name: "PodWithDefaultNamespace",
			args: args{
				ctx: context.TODO(),
				pod: &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pod",
						Namespace: "default",
						Labels: map[string]string{
							"app": "test-app",
						},
					},
					Spec: corev1.PodSpec{
						SchedulerName: "test-scheduler",
						Containers: []corev1.Container{
							{
								Name:  "test-container",
								Image: "test-image",
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
						Name:      "test-pod",
						Namespace: namespace,
						Labels: map[string]string{
							"app": "test-app",
						},
					},
					Spec: corev1.PodSpec{
						SchedulerName: corev1.DefaultSchedulerName,
						Containers: []corev1.Container{
							{
								Name:  "test-container",
								Image: "test-image",
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
						Name:      "test-pod",
						Namespace: namespace,
						Labels: map[string]string{
							"app": "test-app",
						},
					},
					Spec: corev1.PodSpec{
						SchedulerName: "custom-scheduler",
						Containers: []corev1.Container{
							{
								Name:  "test-container",
								Image: "test-image",
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
						Name:      "test-pod",
						Namespace: namespace,
						Labels: map[string]string{
							"app": "test-app",
						},
					},
					Spec: corev1.PodSpec{
						SchedulerName: corev1.DefaultSchedulerName,
						Containers: []corev1.Container{
							{
								Name:  "test-container",
								Image: "test-image",
							},
						},
					},
				},
			},
			wantErr: false,
			sched:   SchedulerName,
		},
		{
			name: "PodWithSchedulerNameInUnmanagedNamespace",
			args: args{
				ctx: context.TODO(),
				pod: &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pod",
						Namespace: "unmanaged-ns",
						Labels: map[string]string{
							"app": "test-app",
						},
					},
					Spec: corev1.PodSpec{
						SchedulerName: SchedulerName,
						Containers: []corev1.Container{
							{
								Name:  "test-container",
								Image: "test-image",
							},
						},
					},
				},
			},
			wantErr: false,
			sched:   SchedulerName,
		},
		{
			name: "PodWithDefaultSchedulerInUnmanagedNamespace",
			args: args{
				ctx: context.TODO(),
				pod: &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pod",
						Namespace: "unmanaged-ns",
						Labels: map[string]string{
							"app": "test-app",
						},
					},
					Spec: corev1.PodSpec{
						SchedulerName: corev1.DefaultSchedulerName,
						Containers: []corev1.Container{
							{
								Name:  "test-container",
								Image: "test-image",
							},
						},
					},
				},
			},
			wantErr: false,
			sched:   corev1.DefaultSchedulerName,
		},
		{
			name: "PodWithNodeNameOnCreateInManagedNamespace_unsetsNodeName",
			args: args{
				ctx: contextWithAdmissionOperation("CREATE"),
				pod: &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pod",
						Namespace: namespace,
						Labels:    map[string]string{"app": "test-app"},
					},
					Spec: corev1.PodSpec{
						SchedulerName: corev1.DefaultSchedulerName,
						NodeName:      "some-node",
						Containers: []corev1.Container{
							{Name: "test-container", Image: "test-image"},
						},
					},
				},
			},
			wantErr:      false,
			sched:        SchedulerName,
			wantNodeName: "",
		},
		{
			name: "PodWithNodeNameOnUpdateInManagedNamespace_preservesNodeName",
			args: args{
				ctx: contextWithAdmissionOperation("UPDATE"),
				pod: &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pod",
						Namespace: namespace,
						Labels:    map[string]string{"app": "test-app"},
					},
					Spec: corev1.PodSpec{
						SchedulerName: corev1.DefaultSchedulerName,
						NodeName:      "some-node",
						Containers: []corev1.Container{
							{Name: "test-container", Image: "test-image"},
						},
					},
				},
			},
			wantErr:      false,
			sched:        SchedulerName,
			wantNodeName: "some-node",
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
			if tt.args.pod.Spec.NodeName != tt.wantNodeName {
				t.Errorf("PodAdmission.Default() nodeName = %q, want %q", tt.args.pod.Spec.NodeName, tt.wantNodeName)
			}
		})
	}
}

// contextWithAdmissionOperation returns a context with an admission request for the given operation.
func contextWithAdmissionOperation(op string) context.Context {
	req := admission.Request{}
	reflect.ValueOf(&req).Elem().FieldByName("Operation").SetString(op)
	return admission.NewContextWithRequest(context.TODO(), req)
}

// contextWithAdmissionSubresource returns a context with an admission request for the given subresource.
func contextWithAdmissionSubresource(subresource string) context.Context {
	req := admission.Request{}
	reflect.ValueOf(&req).Elem().FieldByName("SubResource").SetString(subresource)
	return admission.NewContextWithRequest(context.TODO(), req)
}

func TestPodAdmission_ValidateCreate(t *testing.T) {
	type fields struct {
		SchedulerName     string
		ManagedNamespaces []string
	}
	type args struct {
		ctx context.Context
		pod *corev1.Pod
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
				pod: &corev1.Pod{
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
				pod: &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: namespace,
						Labels: map[string]string{
							wellknown.LabelExternalJobId: "1",
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
				pod: &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: namespace,
						Annotations: map[string]string{
							wellknown.AnnotationExternalJobNode: "foo",
						},
					},
				},
			},
			want:    nil,
			wantErr: true,
		},
		{
			name: "PodWithResourceClaim",
			fields: fields{
				ManagedNamespaces: []string{namespace},
			},
			args: args{
				ctx: context.TODO(),
				pod: &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: namespace,
					},
					Spec: corev1.PodSpec{
						ResourceClaims: []corev1.PodResourceClaim{
							{Name: "gpu"},
						},
					},
				},
			},
			want:    nil,
			wantErr: true,
		},
		{
			name: "PodWithNativeAndDRACPU",
			fields: fields{
				ManagedNamespaces: []string{namespace},
			},
			args: args{
				ctx: context.TODO(),
				pod: &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{Namespace: namespace},
					Spec: corev1.PodSpec{Containers: []corev1.Container{{
						Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("1"),
							corev1.ResourceName(nodeinfo.DraDriverCpu_ExtendedResourceName): resource.MustParse("1"),
						}},
					}}},
				},
			},
			want:    nil,
			wantErr: true,
		},
		{
			name: "PodWithUnsupportedDRAClass",
			fields: fields{
				ManagedNamespaces: []string{namespace},
			},
			args: args{
				ctx: context.TODO(),
				pod: &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{Namespace: namespace},
					Spec: corev1.PodSpec{Containers: []corev1.Container{{
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								"deviceclass.resource.kubernetes.io/other.gpu.example.com": resource.MustParse("1"),
							},
							Limits: corev1.ResourceList{
								"deviceclass.resource.kubernetes.io/other.gpu.example.com": resource.MustParse("1"),
							},
						},
					}}},
				},
			},
			want:    nil,
			wantErr: true,
		},
		{
			name: "PodWithUnsupportedDRAClassInUnmanagedNamespace",
			fields: fields{
				SchedulerName:     SchedulerName,
				ManagedNamespaces: []string{namespace},
			},
			args: args{
				ctx: context.TODO(),
				pod: &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{Namespace: "unmanaged-ns"},
					Spec: corev1.PodSpec{
						SchedulerName: "other-scheduler",
						InitContainers: []corev1.Container{{
							Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{
								"deviceclass.resource.kubernetes.io/other.gpu.example.com": resource.MustParse("1"),
							}},
						}},
					},
				},
			},
			want:    nil,
			wantErr: false,
		},
		{
			name: "PodWithoutLabelOrAnnotation",
			fields: fields{
				ManagedNamespaces: []string{namespace},
			},
			args: args{
				ctx: context.TODO(),
				pod: &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: namespace,
					},
				},
			},
			want:    nil,
			wantErr: false,
		},
		{
			name: "PodWithSchedulerNameInUnmanagedNamespace",
			fields: fields{
				SchedulerName:     SchedulerName,
				ManagedNamespaces: []string{namespace},
			},
			args: args{
				ctx: context.TODO(),
				pod: &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "unmanaged-ns",
					},
					Spec: corev1.PodSpec{
						SchedulerName: SchedulerName,
					},
				},
			},
			want:    nil,
			wantErr: false,
		},
		{
			name: "PodWithSchedulerNameAndJobIDInUnmanagedNamespace",
			fields: fields{
				SchedulerName:     SchedulerName,
				ManagedNamespaces: []string{namespace},
			},
			args: args{
				ctx: context.TODO(),
				pod: &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "unmanaged-ns",
						Labels: map[string]string{
							wellknown.LabelExternalJobId: "1",
						},
					},
					Spec: corev1.PodSpec{
						SchedulerName: SchedulerName,
					},
				},
			},
			want:    nil,
			wantErr: true,
		},
		{
			name: "PodWithSchedulerNameAndResourceClaimInUnmanagedNamespace",
			fields: fields{
				SchedulerName:     SchedulerName,
				ManagedNamespaces: []string{namespace},
			},
			args: args{
				ctx: context.TODO(),
				pod: &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "unmanaged-ns",
					},
					Spec: corev1.PodSpec{
						SchedulerName: SchedulerName,
						ResourceClaims: []corev1.PodResourceClaim{
							{
								Name: "gpu",
							},
						},
					},
				},
			},
			want:    nil,
			wantErr: true,
		},
		{
			name: "PodWithDifferentSchedulerInUnmanagedNamespace",
			fields: fields{
				SchedulerName:     SchedulerName,
				ManagedNamespaces: []string{namespace},
			},
			args: args{
				ctx: context.TODO(),
				pod: &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "unmanaged-ns",
						Labels: map[string]string{
							wellknown.LabelExternalJobId: "1",
						},
					},
					Spec: corev1.PodSpec{
						SchedulerName: "other-scheduler",
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
			got, err := r.ValidateCreate(tt.args.ctx, tt.args.pod)
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
		oldPod *corev1.Pod
		newPod *corev1.Pod
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
				ctx: contextWithAdmissionSubresource(""),
				oldPod: &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: metav1.NamespaceDefault,
					},
				},
				newPod: &corev1.Pod{
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
				ctx: contextWithAdmissionSubresource(""),
				oldPod: &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: namespace,
					},
				},
				newPod: &corev1.Pod{
					Status: corev1.PodStatus{
						Phase: corev1.PodPending,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: namespace,
						Labels: map[string]string{
							wellknown.LabelExternalJobId: "1",
						},
					},
				},
			},
			want:    nil,
			wantErr: false,
		},
		{
			name: "ManagedPodWithoutAdmissionRequestFails",
			fields: fields{
				ManagedNamespaces: []string{namespace},
			},
			args: args{
				ctx: context.TODO(),
				oldPod: &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{Namespace: namespace},
				},
				newPod: &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{Namespace: namespace},
				},
			},
			want:    nil,
			wantErr: true,
		},
		{
			name: "ManagedPodCannotResize",
			fields: fields{
				ManagedNamespaces: []string{namespace},
			},
			args: args{
				ctx: contextWithAdmissionSubresource("resize"),
				oldPod: &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{Namespace: namespace},
				},
				newPod: &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{Namespace: namespace},
				},
			},
			want:    nil,
			wantErr: true,
		},
		{
			name: "PodWithSchedulerNameCannotResizeInUnmanagedNamespace",
			fields: fields{
				SchedulerName:     SchedulerName,
				ManagedNamespaces: []string{namespace},
			},
			args: args{
				ctx: contextWithAdmissionSubresource("resize"),
				oldPod: &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{Namespace: "unmanaged-ns"},
					Spec:       corev1.PodSpec{SchedulerName: SchedulerName},
				},
				newPod: &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{Namespace: "unmanaged-ns"},
					Spec:       corev1.PodSpec{SchedulerName: SchedulerName},
				},
			},
			want:    nil,
			wantErr: true,
		},
		{
			name: "UnmanagedPodCanResize",
			fields: fields{
				SchedulerName:     SchedulerName,
				ManagedNamespaces: []string{namespace},
			},
			args: args{
				ctx: contextWithAdmissionSubresource("resize"),
				oldPod: &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{Namespace: "unmanaged-ns"},
					Spec:       corev1.PodSpec{SchedulerName: "other-scheduler"},
				},
				newPod: &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{Namespace: "unmanaged-ns"},
					Spec:       corev1.PodSpec{SchedulerName: "other-scheduler"},
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
				ctx: contextWithAdmissionSubresource(""),
				oldPod: &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: namespace,
						Labels: map[string]string{
							wellknown.LabelExternalJobId: "1",
						},
					},
				},
				newPod: &corev1.Pod{
					Status: corev1.PodStatus{
						Phase: corev1.PodRunning,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: namespace,
						Labels: map[string]string{
							wellknown.LabelExternalJobId: "2",
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
				ctx: contextWithAdmissionSubresource(""),
				oldPod: &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: namespace,
						Annotations: map[string]string{
							wellknown.AnnotationExternalJobNode: "node1",
						},
					},
				},
				newPod: &corev1.Pod{
					Status: corev1.PodStatus{
						Phase: corev1.PodRunning,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: namespace,
						Annotations: map[string]string{
							wellknown.AnnotationExternalJobNode: "node2",
						},
					},
				},
			},
			want:    nil,
			wantErr: true,
		},
		{
			name: "RunningPodWithSchedulerNameCantChangeJobIDInUnmanagedNamespace",
			fields: fields{
				SchedulerName:     SchedulerName,
				ManagedNamespaces: []string{namespace},
			},
			args: args{
				ctx: contextWithAdmissionSubresource(""),
				oldPod: &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "unmanaged-ns",
						Labels: map[string]string{
							wellknown.LabelExternalJobId: "1",
						},
					},
					Spec: corev1.PodSpec{
						SchedulerName: SchedulerName,
					},
				},
				newPod: &corev1.Pod{
					Status: corev1.PodStatus{
						Phase: corev1.PodRunning,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "unmanaged-ns",
						Labels: map[string]string{
							wellknown.LabelExternalJobId: "2",
						},
					},
					Spec: corev1.PodSpec{
						SchedulerName: SchedulerName,
					},
				},
			},
			want:    nil,
			wantErr: true,
		},
		{
			name: "RunningPodWithSchedulerNameCantChangeNodeInUnmanagedNamespace",
			fields: fields{
				SchedulerName:     SchedulerName,
				ManagedNamespaces: []string{namespace},
			},
			args: args{
				ctx: contextWithAdmissionSubresource(""),
				oldPod: &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "unmanaged-ns",
						Annotations: map[string]string{
							wellknown.AnnotationExternalJobNode: "node1",
						},
					},
					Spec: corev1.PodSpec{
						SchedulerName: SchedulerName,
					},
				},
				newPod: &corev1.Pod{
					Status: corev1.PodStatus{
						Phase: corev1.PodRunning,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "unmanaged-ns",
						Annotations: map[string]string{
							wellknown.AnnotationExternalJobNode: "node2",
						},
					},
					Spec: corev1.PodSpec{
						SchedulerName: SchedulerName,
					},
				},
			},
			want:    nil,
			wantErr: true,
		},
		{
			name: "PodWithDifferentSchedulerInUnmanagedNamespace",
			fields: fields{
				SchedulerName:     SchedulerName,
				ManagedNamespaces: []string{namespace},
			},
			args: args{
				ctx: context.TODO(),
				oldPod: &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "unmanaged-ns",
						Labels: map[string]string{
							wellknown.LabelExternalJobId: "1",
						},
					},
					Spec: corev1.PodSpec{
						SchedulerName: "other-scheduler",
					},
				},
				newPod: &corev1.Pod{
					Status: corev1.PodStatus{
						Phase: corev1.PodRunning,
					},
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "unmanaged-ns",
						Labels: map[string]string{
							wellknown.LabelExternalJobId: "2",
						},
					},
					Spec: corev1.PodSpec{
						SchedulerName: "other-scheduler",
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
			got, err := r.ValidateUpdate(tt.args.ctx, tt.args.oldPod, tt.args.newPod)
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
		pod *corev1.Pod
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
			got, err := r.ValidateDelete(tt.args.ctx, tt.args.pod)
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
