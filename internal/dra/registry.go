// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package dra

import (
	"cmp"
	"fmt"
	"regexp"
	"slices"
	"strings"

	resourcev1 "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apiserver/pkg/cel/environment"
	dracel "k8s.io/dynamic-resource-allocation/cel"
)

var deviceProfileNamePattern = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9_.-]*[A-Za-z0-9])?$`)

// Registry indexes supported DeviceProfiles by name and canonical selector.
type Registry struct {
	byName            map[string]DeviceProfile
	bySelector        map[string]DeviceProfile
	byDriver          map[string][]DeviceProfile
	byIndexedGRESType map[string]DeviceProfile
}

// NewRegistry validates profiles and indexes them by name and canonical
// selector.
func NewRegistry(profiles []DeviceProfile) (*Registry, error) {
	registry := &Registry{
		byName:            make(map[string]DeviceProfile, len(profiles)),
		bySelector:        make(map[string]DeviceProfile, len(profiles)),
		byDriver:          make(map[string][]DeviceProfile),
		byIndexedGRESType: make(map[string]DeviceProfile),
	}
	coreBitmapProfile := ""
	for i, profile := range profiles {
		if profile.Name == "" {
			return nil, fmt.Errorf("device profile %d has an empty name", i)
		}
		if !deviceProfileNamePattern.MatchString(profile.Name) {
			return nil, fmt.Errorf("device profile name %q must start and end with an alphanumeric character and contain only alphanumeric characters, '.', '_' or '-'", profile.Name)
		}
		if _, exists := registry.byName[profile.Name]; exists {
			return nil, fmt.Errorf("duplicate device profile name %q", profile.Name)
		}
		if profile.Driver == "" {
			return nil, fmt.Errorf("device profile %q has an empty driver", profile.Name)
		}
		if len(profile.Driver) > resourcev1.DriverNameMaxLength {
			return nil, fmt.Errorf("device profile %q driver %q exceeds the maximum length of %d characters", profile.Name, profile.Driver, resourcev1.DriverNameMaxLength)
		}
		if problems := validation.IsDNS1123Subdomain(profile.Driver); len(problems) > 0 {
			return nil, fmt.Errorf("device profile %q has invalid driver %q: %s", profile.Name, profile.Driver, strings.Join(problems, "; "))
		}
		if profile.Selector == "" {
			return nil, fmt.Errorf("device profile %q has an empty selector", profile.Name)
		}
		if len(profile.Selector) > resourcev1.CELSelectorExpressionMaxLength {
			return nil, fmt.Errorf("selector for device profile %q exceeds the maximum length of %d bytes", profile.Name, resourcev1.CELSelectorExpressionMaxLength)
		}
		envType := environment.NewExpressions
		compiled := dracel.GetCompiler(deviceProfileCELFeatures).CompileCELExpression(profile.Selector, dracel.Options{EnvType: &envType})
		if compiled.Error != nil {
			return nil, fmt.Errorf("compile selector for device profile %q: %w", profile.Name, compiled.Error)
		}
		if compiled.MaxCost > resourcev1.CELSelectorExpressionMaxCost {
			return nil, fmt.Errorf("selector for device profile %q is too complex: estimated cost %d exceeds limit %d", profile.Name, compiled.MaxCost, resourcev1.CELSelectorExpressionMaxCost)
		}
		if existing, exists := registry.bySelector[profile.Selector]; exists {
			return nil, fmt.Errorf("device profiles %q and %q have the same selector", existing.Name, profile.Name)
		}
		if profile.Backend == nil {
			return nil, fmt.Errorf("device profile %q has no backend", profile.Name)
		}
		switch profile.Backend.(type) {
		case CoreBitmapBackend:
			if coreBitmapProfile != "" {
				return nil, fmt.Errorf("device profiles %q and %q both use the core-bitmap backend", coreBitmapProfile, profile.Name)
			}
			coreBitmapProfile = profile.Name
		case IndexedGRESBackend:
			if _, err := profile.GRES(); err != nil {
				return nil, err
			}
			registry.byIndexedGRESType[profile.Name] = profile
		default:
			return nil, fmt.Errorf("device profile %q has unsupported backend %T", profile.Name, profile.Backend)
		}

		registry.byName[profile.Name] = profile
		registry.bySelector[profile.Selector] = profile
		registry.byDriver[profile.Driver] = append(registry.byDriver[profile.Driver], profile)
	}
	for driver := range registry.byDriver {
		slices.SortFunc(registry.byDriver[driver], func(a, b DeviceProfile) int {
			return cmp.Compare(a.Name, b.Name)
		})
	}
	return registry, nil
}

// DefaultRegistry returns a registry containing the profiles currently
// supported by slurm-bridge.
func DefaultRegistry() *Registry {
	// Upstream DeviceClass:
	// https://github.com/kubernetes-sigs/dra-driver-cpu/blob/main/deployment/helm/dra-driver-cpu/templates/deviceclass.yaml
	cpu := DeviceProfile{
		Name:     "cpu",
		Driver:   "dra.cpu",
		Selector: `device.driver == "dra.cpu"`,
		Backend:  CoreBitmapBackend{},
	}
	// Upstream DeviceClass:
	// https://github.com/kubernetes-sigs/dra-example-driver/blob/v0.4.0/deployments/helm/dra-example-driver/templates/deviceclass.yaml
	exampleGPU := DeviceProfile{
		Name:     "gpu-example",
		Driver:   "gpu.example.com",
		Selector: `device.driver == 'gpu.example.com'`,
		Backend: IndexedGRESBackend{
			GRESName: "gpu",
		},
	}
	// Upstream DeviceClass:
	// https://github.com/kubernetes-sigs/dra-driver-nvidia-gpu/blob/v0.4.0/deployments/helm/dra-driver-nvidia-gpu/templates/deviceclass-gpu.yaml
	nvidiaGPU := DeviceProfile{
		Name:     "gpu-nvidia",
		Driver:   "gpu.nvidia.com",
		Selector: `device.driver == 'gpu.nvidia.com' && device.attributes['gpu.nvidia.com'].type == 'gpu'`,
		Backend: IndexedGRESBackend{
			GRESName: "gpu",
		},
	}
	// DRANET v1.4 publishes pciAddress and rdma as device attributes:
	// https://github.com/kubernetes-sigs/dranet/blob/v1.4.0/pkg/apis/attributes.go
	dranetRDMA := DeviceProfile{
		Name:   "dranet-rdma",
		Driver: "dra.net",
		Selector: `device.driver == 'dra.net' && ` +
			`has(device.attributes['dra.net'].pciAddress) && ` +
			`has(device.attributes['dra.net'].rdma) && ` +
			`device.attributes['dra.net'].rdma == true`,
		Backend: IndexedGRESBackend{
			GRESName: "nic",
		},
	}
	registry, err := NewRegistry([]DeviceProfile{cpu, exampleGPU, nvidiaGPU, dranetRDMA})
	if err != nil {
		panic(err)
	}
	return registry
}

// LookupByName returns the profile with the given stable profile name.
func (r *Registry) LookupByName(name string) (DeviceProfile, bool) {
	profile, ok := r.byName[name]
	return profile, ok
}

// LookupBySelector returns the profile with the exact canonical CEL selector.
// Selector matching is deliberately byte-for-byte.
func (r *Registry) LookupBySelector(selector string) (DeviceProfile, bool) {
	profile, ok := r.bySelector[selector]
	return profile, ok
}

// MatchIndexedGRES returns the profile which owns gres. Profiles using other
// backends do not claim the Slurm GRES type namespace. A known indexed-GRES
// type with the wrong GRES name is rejected.
func (r *Registry) MatchIndexedGRES(gres GRES) (DeviceProfile, bool, error) {
	profile, owned := r.byIndexedGRESType[gres.Type]
	if !owned {
		return DeviceProfile{}, false, nil
	}
	expected, err := profile.GRES()
	if err != nil {
		return DeviceProfile{}, true, err
	}
	if gres.Name != expected.Name {
		return DeviceProfile{}, true, fmt.Errorf(
			"slurm GRES type %q uses name %q, expected %q for DeviceProfile %q",
			gres.Type, gres.Name, expected.Name, profile.Name,
		)
	}
	return profile, true, nil
}

// SupportsDriver reports whether the registry contains a profile for driver.
func (r *Registry) SupportsDriver(driver string) bool {
	_, ok := r.byDriver[driver]
	return ok
}

// profilesForDriver returns profiles for driver ordered by profile name.
func (r *Registry) profilesForDriver(driver string) []DeviceProfile {
	return slices.Clone(r.byDriver[driver])
}

// MatchDeviceClass validates a DeviceClass and returns its matching profile.
func (r *Registry) MatchDeviceClass(deviceClass *resourcev1.DeviceClass) (DeviceProfile, error) {
	if deviceClass == nil {
		return DeviceProfile{}, fmt.Errorf("device class must not be nil")
	}
	if len(deviceClass.Spec.Config) != 0 {
		return DeviceProfile{}, fmt.Errorf("device class %q configuration is not supported", deviceClass.Name)
	}
	if len(deviceClass.Spec.Selectors) != 1 {
		return DeviceProfile{}, fmt.Errorf("device class %q must have exactly one selector", deviceClass.Name)
	}
	selector := deviceClass.Spec.Selectors[0]
	if selector.CEL == nil {
		return DeviceProfile{}, fmt.Errorf("device class %q selector must be a CEL selector", deviceClass.Name)
	}
	profile, ok := r.LookupBySelector(selector.CEL.Expression)
	if !ok {
		return DeviceProfile{}, fmt.Errorf("device class %q selector does not match a supported device profile", deviceClass.Name)
	}
	return profile, nil
}
