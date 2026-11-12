// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/cpuset"
	"k8s.io/utils/ptr"
	"pgregory.net/rapid"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/SlinkyProject/slurm-bridge/internal/dra"
	"github.com/SlinkyProject/slurm-bridge/internal/nodeinfo"
	"github.com/SlinkyProject/slurm-bridge/internal/scheduler/plugins/slurmbridge/slurmcontrol"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/bitmaputil"
	"github.com/SlinkyProject/slurm-bridge/internal/utils/controllercheck"
)

// Expectations come from generated physical CPU IDs and applied device paths,
// not the allocator's translation helpers. Each class has one requesting container.
type draResourceWorld struct {
	sb            *SlurmBridge
	pod           *corev1.Pod
	resources     *slurmcontrol.NodeResources
	inventory     *resourcev1.ResourceSlice
	cpus          map[string]cpuset.CPUSet
	devices       map[string]bool
	classes       []*resourcev1.DeviceClass
	trace         controllercheck.Trace
	faults        controllercheck.Faults
	observe       func()
	beforePublish func(context.Context, client.Client) error
}

func newDRAResourceWorld(rt *rapid.T, mode string) *draResourceWorld {
	w := &draResourceWorld{cpus: map[string]cpuset.CPUSet{}, devices: map[string]bool{}}
	w.faults.Trace = &w.trace
	effect := func(op string, commit func() error) error {
		return w.faults.Call(op, func() error {
			if err := commit(); err != nil {
				return err
			}
			if w.observe != nil {
				w.observe()
			}
			return nil
		})
	}
	w.sb, w.pod, w.resources = socketClaimFixture(rapidFixtureT{rt}, interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if _, ok := obj.(*resourcev1.ResourceClaim); !ok {
				return c.Create(ctx, obj, opts...)
			}
			return effect("create", func() error { return c.Create(ctx, obj, opts...) })
		},
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*resourcev1.ResourceClaim); !ok {
				return c.Get(ctx, key, obj, opts...)
			}
			return w.faults.Call("get", func() error { return c.Get(ctx, key, obj, opts...) })
		},
		SubResourcePatch: func(ctx context.Context, c client.Client, sub string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
			op := "allocate"
			if _, ok := obj.(*corev1.Pod); ok {
				op = "publish"
				if w.beforePublish != nil {
					if err := w.beforePublish(ctx, c); err != nil {
						return err
					}
				}
			}
			return effect(op, func() error { return c.SubResource(sub).Patch(ctx, obj, patch, opts...) })
		},
	})
	groups32 := rapid.Int32Range(1, 4).Draw(rt, "cpu-groups")
	cores32 := rapid.Int32Range(1, 8).Draw(rt, "cores-per-group")
	groups, cores := int(groups32), int(cores32)
	threads := rapid.IntRange(1, 2).Draw(rt, "threads-per-core")
	numa := rapid.Bool().Draw(rt, "numa-groups")
	w.inventory = &resourcev1.ResourceSlice{}
	cpuClass := &resourcev1.DeviceClass{}
	if err := w.sb.Get(rt.Context(), client.ObjectKey{Name: "cpus"}, cpuClass); err != nil {
		rt.Fatal(err)
	}
	w.classes = append(w.classes, cpuClass)
	if err := w.sb.Get(rt.Context(), client.ObjectKey{Name: "cpu-pool"}, w.inventory); err != nil {
		rt.Fatal(err)
	}
	w.inventory.Spec.Devices = nil
	selected := rapid.SliceOfNDistinct(rapid.IntRange(0, groups*cores-1), 1, groups*cores, func(n int) int { return n }).Draw(rt, "allocated-cores")
	for g := range groups {
		base := g * cores * threads
		all := cpuset.New()
		wanted := cpuset.New()
		for c := range cores {
			for thread := range threads {
				cpu := base + c + thread*cores
				all = all.Union(cpuset.New(cpu))
				if slices.Contains(selected, g*cores+c) {
					wanted = wanted.Union(cpuset.New(cpu))
				}
			}
		}
		name := fmt.Sprintf("socket-%d", g)
		smt := all.String()
		if threads == 2 {
			smt += fmt.Sprintf(">%d", cores)
		}
		attrs := map[resourcev1.QualifiedName]resourcev1.DeviceAttribute{
			nodeinfo.DraDriverCpu_SocketID:   {IntValue: ptr.To(int64(g))},
			nodeinfo.DraDriverCpu_CPUIDs:     {StringValue: ptr.To(all.String())},
			nodeinfo.DraDriverCpu_SMTLayout:  {StringValue: ptr.To(smt)},
			nodeinfo.DraDriverCpu_SMTEnabled: {BoolValue: ptr.To(threads == 2)},
		}
		if numa {
			name = fmt.Sprintf("numa-%d", g)
			attrs[nodeinfo.DraDriverCpu_SocketID] = resourcev1.DeviceAttribute{IntValue: ptr.To(int64(g / 2))}
			attrs[nodeinfo.DraDriverCpu_NUMANodeID] = resourcev1.DeviceAttribute{IntValue: ptr.To(int64(g))}
		}
		w.inventory.Spec.Devices = append(w.inventory.Spec.Devices, resourcev1.Device{Name: name, AllowMultipleAllocations: ptr.To(true), Attributes: attrs, Capacity: map[resourcev1.QualifiedName]resourcev1.DeviceCapacity{nodeinfo.DraDriverCpu_Capacity: {Value: *resource.NewQuantity(int64(all.Size()), resource.DecimalSI)}}})
		if !wanted.IsEmpty() {
			w.cpus[name] = wanted
		}
	}
	w.inventory.Spec.Devices = rapid.Permutation(w.inventory.Spec.Devices).Draw(rt, "inventory-order")
	if err := w.sb.Update(rt.Context(), w.inventory); err != nil {
		rt.Fatal(err)
	}
	w.resources.SocketsPerNode, w.resources.CoresPerSocket = groups32, cores32
	w.resources.CoreBitmap = bitmaputil.String(bitmaputil.New(selected...))
	w.resources.Gres = nil
	w.pod.Spec.Containers = []corev1.Container{{Name: "cpu", Image: "pause", Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceName(resourcev1.ResourceDeviceClassPrefix + "cpus"): *resource.NewQuantity(int64(len(selected)*threads), resource.DecimalSI)}}}}
	cpuProfile, _ := dra.DefaultRegistry().LookupByName("cpu")
	profiles := []dra.DeviceProfile{cpuProfile}
	applied := map[string]any{}
	offset := rapid.IntRange(0, 4).Draw(rt, "first-gres-index")
	profileCount := rapid.IntRange(1, 3).Draw(rt, "profiles")
	if mode == "cpu-only" {
		profileCount = 0
	}
	if mode == "gres-only" {
		w.pod.Spec.Containers[0].Resources.Requests = nil
		clear(w.cpus)
	}
	for i := range profileCount {
		driver, className := fmt.Sprintf("accelerator%d.example.com", i), fmt.Sprintf("accel-%d", i)
		profile := dra.DeviceProfile{Name: driver, Driver: driver, Selector: fmt.Sprintf("device.driver == '%s'", driver), Backend: dra.IndexedGRESBackend{GRESName: "gpu"}}
		profiles = append(profiles, profile)
		class := &resourcev1.DeviceClass{ObjectMeta: metav1.ObjectMeta{Name: className}, Spec: resourcev1.DeviceClassSpec{Selectors: []resourcev1.DeviceSelector{{CEL: &resourcev1.CELDeviceSelector{Expression: profile.Selector}}}}}
		if err := w.sb.Create(rt.Context(), class); err != nil {
			rt.Fatal(err)
		}
		w.classes = append(w.classes, class)
		count := rapid.IntRange(1, 4).Draw(rt, "inventory-count")
		indexes := rapid.SliceOfNDistinct(rapid.IntRange(0, count-1), 1, count, func(n int) int { return n }).Draw(rt, "gres-indexes")
		slices.Sort(indexes)
		paths, absolute := []string{}, []int{}
		for d := range count {
			paths = append(paths, fmt.Sprintf("/dra/%s/node/device-%d", driver, d))
		}
		for _, d := range indexes {
			absolute = append(absolute, offset+d)
			w.devices[fmt.Sprintf("%s/node/device-%d", driver, d)] = true
		}
		applied[driver] = map[string]any{"firstIndex": offset, "devices": paths}
		w.resources.Gres = append(w.resources.Gres, slurmcontrol.GresLayout{Name: "gpu", Type: driver, Count: int64(len(indexes)), Index: cpuset.New(absolute...).String()})
		container := corev1.Container{Name: className, Image: "pause", Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceName(resourcev1.ResourceDeviceClassPrefix + className): *resource.NewQuantity(int64(len(indexes)), resource.DecimalSI)}}}
		if rapid.Bool().Draw(rt, "init-container") {
			w.pod.Spec.InitContainers = append(w.pod.Spec.InitContainers, container)
		} else {
			w.pod.Spec.Containers = append(w.pod.Spec.Containers, container)
		}
		offset += count
	}
	var err error
	w.sb.draRegistry, err = dra.NewRegistry(profiles)
	if err != nil {
		rt.Fatal(err)
	}
	topology, err := nodeinfo.NewNodeInfoFromResourceSlices("node", []resourcev1.ResourceSlice{*w.inventory})
	if err != nil {
		rt.Fatal(err)
	}
	raw, err := json.Marshal(map[string]any{"v": 1, "profiles": applied})
	if err != nil {
		rt.Fatal(err)
	}
	w.resources.NodeExtra, err = topology.EncodeApplied(dra.AppliedInventoryExtraPrefix + string(raw))
	if err != nil {
		rt.Fatal(err)
	}
	if err := w.sb.Update(rt.Context(), w.pod); err != nil {
		rt.Fatal(err)
	}
	return w
}

type draResourceState struct {
	world  *draResourceWorld
	claims []resourcev1.ResourceClaim
	pod    *corev1.Pod
}

func (w *draResourceWorld) snapshot(ctx context.Context) (draResourceState, error) {
	claims, pod := &resourcev1.ResourceClaimList{}, &corev1.Pod{}
	if err := w.sb.List(ctx, claims); err != nil {
		return draResourceState{}, err
	}
	if err := w.sb.Get(ctx, client.ObjectKeyFromObject(w.pod), pod); err != nil {
		return draResourceState{}, err
	}
	return draResourceState{w, claims.Items, pod}, nil
}

func checkDRAResources(s draResourceState) error {
	if len(s.claims) > 1 {
		return fmt.Errorf("retry duplicated claims: %d", len(s.claims))
	}
	for _, claim := range s.claims {
		allocation := claim.Status.Allocation
		if allocation == nil {
			continue
		}
		if len(claim.Status.ReservedFor) != 1 || claim.Status.ReservedFor[0].UID != s.world.pod.UID {
			return fmt.Errorf("wrong claim consumer")
		}
		if allocation.NodeSelector == nil || len(allocation.NodeSelector.NodeSelectorTerms) != 1 || len(allocation.NodeSelector.NodeSelectorTerms[0].MatchFields) != 1 || !slices.Equal(allocation.NodeSelector.NodeSelectorTerms[0].MatchFields[0].Values, []string{"node"}) {
			return fmt.Errorf("wrong allocated node")
		}
		remaining := maps.Clone(s.world.devices)
		cpus := maps.Clone(s.world.cpus)
		configs := map[string]cpuset.CPUSet{}
		for _, config := range allocation.Devices.Config {
			if config.Opaque == nil || config.Opaque.Driver != nodeinfo.DraDriverCpu {
				continue
			}
			var parsed struct {
				CPUConfig struct {
					CPUSet string `json:"cpuset"`
				} `json:"cpuConfig"`
			}
			if err := json.Unmarshal(config.Opaque.Parameters.Raw, &parsed); err != nil {
				return err
			}
			set, err := cpuset.Parse(parsed.CPUConfig.CPUSet)
			if err != nil {
				return err
			}
			for _, req := range config.Requests {
				configs[req] = set
			}
		}
		for _, result := range allocation.Devices.Results {
			if result.Driver == nodeinfo.DraDriverCpu {
				want, found := cpus[result.Device]
				capacity := result.ConsumedCapacity[nodeinfo.DraDriverCpu_Capacity]
				if !found || result.ShareID == nil || capacity.Value() != int64(want.Size()) || !configs[result.Request].Equals(want) {
					return fmt.Errorf("incorrect CPU set/capacity for %s", result.Device)
				}
				delete(cpus, result.Device)
			} else {
				key := result.Driver + "/" + result.Pool + "/" + result.Device
				if !remaining[key] {
					return fmt.Errorf("unexpected or duplicate device %s", key)
				}
				delete(remaining, key)
			}
		}
		if len(remaining)+len(cpus) > 0 {
			return fmt.Errorf("allocated resources are missing")
		}
	}
	return nil
}

func checkDRAPublication(s draResourceState) error {
	status := s.pod.Status.ExtendedResourceClaimStatus
	if status == nil {
		return nil
	}
	if len(s.claims) != 1 || status.ResourceClaimName != s.claims[0].Name || s.claims[0].Status.Allocation == nil {
		return fmt.Errorf("published missing or unallocated claim")
	}
	if len(s.claims[0].Status.ReservedFor) != 1 || s.claims[0].Status.ReservedFor[0].UID != s.pod.UID {
		return fmt.Errorf("Pod UID %s references a claim reserved for a different Pod", s.pod.UID)
	}
	requests := map[string]string{}
	for _, req := range s.claims[0].Spec.Devices.Requests {
		if req.Exactly != nil {
			requests[req.Name] = req.Exactly.DeviceClassName
		}
	}
	expected := map[corev1.ContainerExtendedResourceRequest]bool{}
	for _, container := range slices.Concat(s.pod.Spec.InitContainers, s.pod.Spec.Containers) {
		for name, class := range requests {
			resourceName := resourcev1.ResourceDeviceClassPrefix + class
			q := container.Resources.Requests[corev1.ResourceName(resourceName)]
			if q.Sign() > 0 {
				expected[corev1.ContainerExtendedResourceRequest{ContainerName: container.Name, RequestName: name, ResourceName: resourceName}] = true
			}
		}
	}
	for _, mapping := range status.RequestMappings {
		if !expected[mapping] {
			return fmt.Errorf("incorrect or duplicate container mapping: %+v", mapping)
		}
		delete(expected, mapping)
	}
	if len(expected) > 0 {
		return fmt.Errorf("missing container mappings")
	}
	return nil
}

func TestSchedulerDRAResourceRecoveryProperties(t *testing.T) {
	t.Parallel()
	contract := controllercheck.Contract[draResourceState]{
		Always: []controllercheck.Rule[draResourceState]{
			{Name: "resource-conservation", Statement: "claims contain exactly the allocated CPUs/devices with correct capacity", Check: checkDRAResources},
			{Name: "publication", Statement: "Pod status only references an allocated claim with complete container mappings", Check: checkDRAPublication},
		},
		Eventually: []controllercheck.Rule[draResourceState]{{Name: "publication-recovers", Statement: "after API and inventory recover, allocation and publication converge", Check: func(s draResourceState) error {
			if s.pod.Status.ExtendedResourceClaimStatus == nil {
				return fmt.Errorf("Pod has no claim")
			}
			if err := checkDRAResources(s); err != nil {
				return err
			}
			return checkDRAPublication(s)
		}}},
	}
	controllercheck.ExploreParallel(t, contract, func(rt *rapid.T) controllercheck.Campaign[draResourceState] {
		w := newDRAResourceWorld(rt, rapid.SampledFrom([]string{"mixed", "cpu-only", "gres-only"}).Draw(rt, "resources"))
		retry := func(context.Context) error { return w.sb.manageResourceClaim(rt.Context(), w.pod, "node", w.resources) }
		restart := func(context.Context) error {
			w.sb = &SlurmBridge{Client: w.sb.Client, draRegistry: w.sb.draRegistry}
			return nil
		}
		return controllercheck.Campaign[draResourceState]{Trace: &w.trace, Snapshot: w.snapshot, ObserveEffects: func(observe func()) { w.observe = observe }, Retryable: func(error) bool { return true }, RecoverySteps: 4,
			Actions: map[string]func(*rapid.T) error{
				"retry": func(rt *rapid.T) error {
					w.faults.Heal()
					op := rapid.SampledFrom([]string{"create", "allocate", "publish", "get", "none"}).Draw(rt, "failure-stage")
					mode := rapid.SampledFrom([]controllercheck.FailureMode{controllercheck.BeforeCommit, controllercheck.AfterCommit}).Draw(rt, "failure-mode")
					w.faults.Arm(op, mode)
					return retry(rt.Context())
				},
				"restart": func(rt *rapid.T) error { return restart(rt.Context()) },
				"class-change": func(rt *rapid.T) error {
					current := &resourcev1.DeviceClass{}
					original := w.classes[rapid.IntRange(0, len(w.classes)-1).Draw(rt, "class")]
					if err := w.sb.Get(rt.Context(), client.ObjectKeyFromObject(original), current); err != nil {
						return err
					}
					current.Spec.Selectors = []resourcev1.DeviceSelector{{CEL: &resourcev1.CELDeviceSelector{Expression: "false"}}}
					return w.sb.Update(rt.Context(), current)
				},
				"inventory": func(rt *rapid.T) error {
					current := &resourcev1.ResourceSlice{}
					if err := w.sb.Get(rt.Context(), client.ObjectKeyFromObject(w.inventory), current); err != nil {
						return err
					}
					if rapid.Bool().Draw(rt, "inventory-available") {
						current.Spec = w.inventory.Spec
					} else {
						current.Spec.Devices = nil
					}
					return w.sb.Update(rt.Context(), current)
				},
			},
			Recover: func(context.Context) error {
				w.faults.Heal()
				for _, original := range w.classes {
					current := &resourcev1.DeviceClass{}
					if err := w.sb.Get(rt.Context(), client.ObjectKeyFromObject(original), current); err != nil {
						return err
					}
					current.Spec = *original.Spec.DeepCopy()
					if err := w.sb.Update(rt.Context(), current); err != nil {
						return err
					}
				}
				current := &resourcev1.ResourceSlice{}
				if err := w.sb.Get(rt.Context(), client.ObjectKeyFromObject(w.inventory), current); err != nil {
					return err
				}
				current.Spec = w.inventory.Spec
				return w.sb.Update(rt.Context(), current)
			}, Progress: retry, Restart: restart,
		}
	})
}

func TestSchedulerDRAInvalidResourceProperties(t *testing.T) {
	t.Parallel()
	for _, fault := range []string{"cpu-count", "gres-count", "gres-index", "gres-name", "bitmap", "inventory", "class", "selector", "multiple-containers", "exclusive-cpu"} {
		t.Run(fault, func(t *testing.T) {
			t.Parallel()
			rapid.Check(t, func(rt *rapid.T) {
				w := newDRAResourceWorld(rt, "mixed")
				switch fault {
				case "cpu-count":
					w.pod.Spec.Containers[0].Resources.Requests[corev1.ResourceName(resourcev1.ResourceDeviceClassPrefix+"cpus")] = resource.MustParse("10000")
				case "gres-count":
					w.resources.Gres[0].Index = ""
					w.resources.Gres[0].Count = 0
				case "gres-index":
					w.resources.Gres[0].Index = "10000"
				case "gres-name":
					w.resources.Gres[0].Name = "wrong-name"
				case "bitmap":
					w.resources.CoreBitmap = bitmaputil.String(bitmaputil.New(10000))
				case "inventory":
					w.resources.NodeExtra = ""
				case "class":
					if err := w.sb.Delete(rt.Context(), w.classes[0]); err != nil {
						rt.Fatal(err)
					}
				case "selector":
					w.classes[0].Spec.Selectors[0].CEL.Expression = "false"
					if err := w.sb.Update(rt.Context(), w.classes[0]); err != nil {
						rt.Fatal(err)
					}
				case "multiple-containers":
					copy := *w.pod.Spec.Containers[0].DeepCopy()
					copy.Name = "duplicate"
					w.pod.Spec.Containers = append(w.pod.Spec.Containers, copy)
				}
				err := w.sb.validateDeviceClassRequestsForPods(rt.Context(), []corev1.Pod{*w.pod}, fault == "exclusive-cpu")
				if err == nil {
					err = w.sb.manageResourceClaim(rt.Context(), w.pod, "node", w.resources)
				}
				if err == nil {
					rt.Fatalf("accepted invalid %s", fault)
				}
				state, err := w.snapshot(rt.Context())
				if err != nil {
					rt.Fatal(err)
				}
				if state.pod.Status.ExtendedResourceClaimStatus != nil {
					rt.Fatal("published invalid resources")
				}
				for _, claim := range state.claims {
					if claim.Status.Allocation != nil {
						rt.Fatal("allocated invalid resources")
					}
				}
			})
		})
	}
}

func TestSchedulerDRAAllocationIdentityProperties(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(rt *rapid.T) {
		w := newDRAResourceWorld(rt, "mixed")
		if err := w.sb.manageResourceClaim(rt.Context(), w.pod, "node", w.resources); err != nil {
			rt.Fatal(err)
		}
		before, err := w.snapshot(rt.Context())
		if err != nil {
			rt.Fatal(err)
		}
		node := "node"
		changed := *w.resources
		changed.Gres = slices.Clone(changed.Gres)
		switch rapid.SampledFrom([]string{"node", "cores", "gres"}).Draw(rt, "allocation-change") {
		case "node":
			node = "other-node"
		case "cores":
			changed.CoreBitmap = bitmaputil.String(bitmaputil.New(10000))
		case "gres":
			changed.Gres[0].Count++
		}
		if err := w.sb.manageResourceClaim(rt.Context(), w.pod, node, &changed); err == nil {
			rt.Fatal("reused claim for changed allocation")
		}
		after, err := w.snapshot(rt.Context())
		if err != nil {
			rt.Fatal(err)
		}
		if !equality.Semantic.DeepEqual(before.claims, after.claims) || !equality.Semantic.DeepEqual(before.pod.Status, after.pod.Status) {
			rt.Fatal("changed allocation modified persisted claim or Pod")
		}
	})
}

func TestSchedulerDRAPublicationIdentityProperties(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(rt *rapid.T) {
		w := newDRAResourceWorld(rt, "mixed")
		replace := rapid.Bool().Draw(rt, "replace-pod")
		w.beforePublish = func(ctx context.Context, c client.Client) error {
			w.beforePublish = nil
			current := w.pod.DeepCopy()
			if !replace {
				current.Status.NominatedNodeName = "unrelated-status-update"
				return c.Status().Update(ctx, current)
			}
			if err := c.Delete(ctx, current); err != nil {
				return err
			}
			current.UID = "replacement-pod"
			current.ResourceVersion = ""
			current.Status = corev1.PodStatus{}
			return c.Create(ctx, current)
		}
		err := w.sb.manageResourceClaim(rt.Context(), w.pod, "node", w.resources)
		if (err != nil) != replace {
			rt.Fatalf("publication error=%v, replaced=%t", err, replace)
		}
		state, err := w.snapshot(rt.Context())
		if err != nil {
			rt.Fatal(err)
		}
		if err := checkDRAPublication(state); err != nil {
			rt.Fatal(err)
		}
		if !replace {
			if state.pod.Status.ExtendedResourceClaimStatus == nil {
				rt.Fatal("claim was not published")
			}
			if state.pod.Status.NominatedNodeName != "unrelated-status-update" {
				rt.Fatal("publication overwrote unrelated status")
			}
		}
	})
}
