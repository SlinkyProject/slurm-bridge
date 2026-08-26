// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package admission

import (
	"context"
	"reflect"
	"strings"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/SlinkyProject/slurm-bridge/internal/nodeinfo"
	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
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

func TestValidateAnnotationConflicts(t *testing.T) {
	cpuDRA := corev1.ResourceName(nodeinfo.DraDriverCpu_ExtendedResourceName)
	gpuDRA := corev1.ResourceName(resourcev1.ResourceDeviceClassPrefix + "gpu.nvidia.com")

	tests := []struct {
		name            string
		pod             *corev1.Pod
		wantErrContains string
	}{
		{
			name: "no annotations no DRA",
			pod:  &corev1.Pod{},
		},
		{
			name: "cpu-per-task without CPU DRA is allowed",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
					wellknown.AnnotationCpuPerTask: "4",
				}},
				Spec: corev1.PodSpec{Containers: []corev1.Container{{
					Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("4"),
					}},
				}}},
			},
		},
		{
			name: "gres without GPU DRA is allowed",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
					wellknown.AnnotationGres: "gpu:4",
				}},
				Spec: corev1.PodSpec{Containers: []corev1.Container{{
					Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{
						"nvidia.com/gpu": resource.MustParse("4"),
					}},
				}}},
			},
		},
		{
			name: "cpu-per-task with CPU DRA in container requests is rejected",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
					wellknown.AnnotationCpuPerTask: "1",
				}},
				Spec: corev1.PodSpec{Containers: []corev1.Container{{
					Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{
						cpuDRA: resource.MustParse("4"),
					}},
				}}},
			},
			wantErrContains: wellknown.AnnotationCpuPerTask,
		},
		{
			name: "cpu-per-task with CPU DRA in init container limits is rejected",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
					wellknown.AnnotationCpuPerTask: "1",
				}},
				Spec: corev1.PodSpec{InitContainers: []corev1.Container{{
					Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{
						cpuDRA: resource.MustParse("4"),
					}},
				}}},
			},
			wantErrContains: wellknown.AnnotationCpuPerTask,
		},
		{
			name: "gres with GPU DRA in container requests is rejected",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
					wellknown.AnnotationGres: "gpu:nvidia:4",
				}},
				Spec: corev1.PodSpec{Containers: []corev1.Container{{
					Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{
						gpuDRA: resource.MustParse("4"),
					}},
				}}},
			},
			wantErrContains: wellknown.AnnotationGres,
		},
		{
			name: "cpu-per-task with GPU DRA (not CPU DRA) is allowed",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
					wellknown.AnnotationCpuPerTask: "1",
				}},
				Spec: corev1.PodSpec{Containers: []corev1.Container{{
					Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{
						gpuDRA: resource.MustParse("4"),
					}},
				}}},
			},
		},
		{
			name: "gres with CPU DRA (not GPU DRA) is allowed",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
					wellknown.AnnotationGres: "gpu:4",
				}},
				Spec: corev1.PodSpec{Containers: []corev1.Container{{
					Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{
						cpuDRA: resource.MustParse("4"),
					}},
				}}},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateAnnotationConflicts(tt.pod)
			if tt.wantErrContains == "" {
				if err != nil {
					t.Fatalf("validateAnnotationConflicts() unexpected error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErrContains) {
				t.Fatalf("validateAnnotationConflicts() error = %v, want error containing %q", err, tt.wantErrContains)
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
	topologySpreadConstraint := corev1.TopologySpreadConstraint{
		MaxSkew:           1,
		TopologyKey:       "example.com/nonexistent",
		WhenUnsatisfiable: corev1.DoNotSchedule,
		LabelSelector: &metav1.LabelSelector{
			MatchLabels: map[string]string{"app": "topology-test"},
		},
	}
	type fields struct {
		SchedulerName     string
		ManagedNamespaces []string
	}
	type args struct {
		ctx context.Context
		pod *corev1.Pod
	}
	tests := []struct {
		name                string
		fields              fields
		args                args
		want                admission.Warnings
		wantWarningContains string
		wantErr             bool
		wantErrContains     string
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
			name: "PodWithTopologySpreadConstraint",
			fields: fields{
				ManagedNamespaces: []string{namespace},
			},
			args: args{
				ctx: context.TODO(),
				pod: &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{Namespace: namespace},
					Spec: corev1.PodSpec{
						TopologySpreadConstraints: []corev1.TopologySpreadConstraint{
							topologySpreadConstraint,
						},
					},
				},
			},
			want:            nil,
			wantErr:         true,
			wantErrContains: "spec.topologySpreadConstraints",
		},
		{
			name: "PodWithEmptyTopologySpreadConstraints",
			fields: fields{
				ManagedNamespaces: []string{namespace},
			},
			args: args{
				ctx: context.TODO(),
				pod: &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{Namespace: namespace},
					Spec: corev1.PodSpec{
						TopologySpreadConstraints: []corev1.TopologySpreadConstraint{},
					},
				},
			},
			want:    nil,
			wantErr: false,
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
			wantWarningContains: `get device class "other.gpu.example.com"`,
			wantErr:             false,
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
			name: "PodWithSchedulerNameAndTopologySpreadConstraintInUnmanagedNamespace",
			fields: fields{
				SchedulerName:     SchedulerName,
				ManagedNamespaces: []string{namespace},
			},
			args: args{
				ctx: context.TODO(),
				pod: &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{Namespace: "unmanaged-ns"},
					Spec: corev1.PodSpec{
						SchedulerName: SchedulerName,
						TopologySpreadConstraints: []corev1.TopologySpreadConstraint{
							topologySpreadConstraint,
						},
					},
				},
			},
			want:            nil,
			wantErr:         true,
			wantErrContains: "spec.topologySpreadConstraints",
		},
		{
			name: "PodWithTopologySpreadConstraintAndDifferentSchedulerInUnmanagedNamespace",
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
						TopologySpreadConstraints: []corev1.TopologySpreadConstraint{
							topologySpreadConstraint,
						},
					},
				},
			},
			want:    nil,
			wantErr: false,
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
				Client:            fake.NewClientBuilder().WithScheme(scheme.Scheme).Build(),
				SchedulerName:     tt.fields.SchedulerName,
				ManagedNamespaces: tt.fields.ManagedNamespaces,
			}
			got, err := r.ValidateCreate(tt.args.ctx, tt.args.pod)
			if (err != nil) != tt.wantErr {
				t.Errorf("PodAdmission.ValidateCreate() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErrContains != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErrContains) {
					t.Errorf("PodAdmission.ValidateCreate() error = %v, want error containing %q", err, tt.wantErrContains)
				}
				return
			}
			if tt.wantWarningContains != "" {
				if len(got) != 1 || !strings.Contains(got[0], tt.wantWarningContains) {
					t.Errorf("PodAdmission.ValidateCreate() = %v, want warning containing %q", got, tt.wantWarningContains)
				}
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("PodAdmission.ValidateCreate() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPodAdmission_ValidateCreate_DRA(t *testing.T) {
	const deviceClassName = "gpu.example.com"
	deviceResource := corev1.ResourceName(resourcev1.ResourceDeviceClassPrefix + deviceClassName)
	validClass := func() *resourcev1.DeviceClass {
		return &resourcev1.DeviceClass{
			ObjectMeta: metav1.ObjectMeta{
				Name: deviceClassName,
			},
			Spec: resourcev1.DeviceClassSpec{
				Selectors: []resourcev1.DeviceSelector{{
					CEL: &resourcev1.CELDeviceSelector{
						Expression: `device.driver == 'gpu.example.com'`,
					},
				}},
			},
		}
	}
	newAdmission := func(classes ...*resourcev1.DeviceClass) *PodAdmission {
		objects := make([]runtime.Object, 0, len(classes))
		for _, class := range classes {
			objects = append(objects, class)
		}
		return &PodAdmission{
			Client:            fake.NewClientBuilder().WithScheme(scheme.Scheme).WithRuntimeObjects(objects...).Build(),
			SchedulerName:     SchedulerName,
			ManagedNamespaces: []string{namespace},
		}
	}
	newPod := func() *corev1.Pod {
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Namespace: namespace},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "work"}},
			},
		}
	}

	t.Run("request in app container", func(t *testing.T) {
		pod := newPod()
		pod.Spec.Containers[0].Resources.Requests = corev1.ResourceList{
			deviceResource: resource.MustParse("1"),
		}
		warnings, err := newAdmission(validClass()).ValidateCreate(context.Background(), pod)
		if err != nil {
			t.Fatalf("PodAdmission.ValidateCreate() error = %v", err)
		}
		if len(warnings) != 0 {
			t.Fatalf("PodAdmission.ValidateCreate() warnings = %v, want none", warnings)
		}
	})

	t.Run("limit in init container", func(t *testing.T) {
		pod := newPod()
		pod.Spec.InitContainers = []corev1.Container{{
			Name: "init",
			Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{
				deviceResource: resource.MustParse("1"),
			}},
		}}
		warnings, err := newAdmission(validClass()).ValidateCreate(context.Background(), pod)
		if err != nil {
			t.Fatalf("PodAdmission.ValidateCreate() error = %v", err)
		}
		if len(warnings) != 0 {
			t.Fatalf("PodAdmission.ValidateCreate() warnings = %v, want none", warnings)
		}
	})

	t.Run("missing class", func(t *testing.T) {
		pod := newPod()
		pod.Spec.Containers[0].Resources.Requests = corev1.ResourceList{
			deviceResource: resource.MustParse("1"),
		}
		warnings, err := newAdmission().ValidateCreate(context.Background(), pod)
		if err != nil {
			t.Fatalf("PodAdmission.ValidateCreate() error = %v, want warning", err)
		}
		if len(warnings) != 1 || !strings.Contains(warnings[0], `get device class "gpu.example.com"`) {
			t.Fatalf("PodAdmission.ValidateCreate() warnings = %v, want missing DeviceClass warning", warnings)
		}
	})

	t.Run("non-canonical selector", func(t *testing.T) {
		class := validClass()
		class.Spec.Selectors[0].CEL.Expression = `device.driver == "gpu.example.com"`
		pod := newPod()
		pod.Spec.Containers[0].Resources.Requests = corev1.ResourceList{
			deviceResource: resource.MustParse("1"),
		}
		warnings, err := newAdmission(class).ValidateCreate(context.Background(), pod)
		if err != nil {
			t.Fatalf("PodAdmission.ValidateCreate() error = %v, want warning", err)
		}
		if len(warnings) != 1 || !strings.Contains(warnings[0], "does not match a supported device profile") {
			t.Fatalf("PodAdmission.ValidateCreate() warnings = %v, want selector mismatch warning", warnings)
		}
	})

	t.Run("class configuration", func(t *testing.T) {
		class := validClass()
		class.Spec.Config = []resourcev1.DeviceClassConfiguration{{
			DeviceConfiguration: resourcev1.DeviceConfiguration{
				Opaque: &resourcev1.OpaqueDeviceConfiguration{
					Driver: "gpu.example.com",
					Parameters: runtime.RawExtension{
						Raw: []byte(`{"enabled":true}`),
					},
				},
			},
		}}
		pod := newPod()
		pod.Spec.Containers[0].Resources.Requests = corev1.ResourceList{
			deviceResource: resource.MustParse("1"),
		}
		warnings, err := newAdmission(class).ValidateCreate(context.Background(), pod)
		if err != nil {
			t.Fatalf("PodAdmission.ValidateCreate() error = %v, want warning", err)
		}
		if len(warnings) != 1 || !strings.Contains(warnings[0], "configuration is not supported") {
			t.Fatalf("PodAdmission.ValidateCreate() warnings = %v, want unsupported configuration warning", warnings)
		}
	})
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
