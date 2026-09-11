// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmbridge

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/SlinkyProject/slurm-bridge/internal/utils/slurmjobir"
)

// newKubeClient preserves the scheduler's content negotiation for normal
// requests and restricts JSON to the PodGroup compatibility type, which has no
// protobuf codec. Both clients share the HTTP transport and REST mapper.
func newKubeClient(config *rest.Config, scheme *runtime.Scheme) (client.Client, error) {
	httpClient, err := rest.HTTPClientFor(config)
	if err != nil {
		return nil, fmt.Errorf("create Kubernetes HTTP client: %w", err)
	}
	options := client.Options{Scheme: scheme, HTTPClient: httpClient}
	kubeClient, err := client.New(config, options)
	if err != nil {
		return nil, fmt.Errorf("create Kubernetes client: %w", err)
	}

	podGroupConfig := rest.CopyConfig(config)
	podGroupConfig.ContentType = runtime.ContentTypeJSON
	podGroupConfig.AcceptContentTypes = runtime.ContentTypeJSON
	options.Mapper = kubeClient.RESTMapper()
	podGroupClient, err := client.New(podGroupConfig, options)
	if err != nil {
		return nil, fmt.Errorf("create PodGroup client: %w", err)
	}
	return &podGroupJSONClient{Client: kubeClient, jsonClient: podGroupClient}, nil
}

type podGroupJSONClient struct {
	client.Client
	jsonClient client.Client
}

func (c *podGroupJSONClient) clientFor(obj client.Object) client.Client {
	if _, ok := obj.(*slurmjobir.PodGroup); ok {
		return c.jsonClient
	}
	// Metadata-only reads, including Workload and PodGroup metadata, support
	// protobuf and stay on the normal client.
	return c.Client
}

func (c *podGroupJSONClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	return c.clientFor(obj).Get(ctx, key, obj, opts...)
}

func (c *podGroupJSONClient) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	return c.clientFor(obj).Create(ctx, obj, opts...)
}

func (c *podGroupJSONClient) Delete(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error {
	return c.clientFor(obj).Delete(ctx, obj, opts...)
}

func (c *podGroupJSONClient) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	return c.clientFor(obj).Update(ctx, obj, opts...)
}

func (c *podGroupJSONClient) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	return c.clientFor(obj).Patch(ctx, obj, patch, opts...)
}

func (c *podGroupJSONClient) DeleteAllOf(ctx context.Context, obj client.Object, opts ...client.DeleteAllOfOption) error {
	return c.clientFor(obj).DeleteAllOf(ctx, obj, opts...)
}

func (c *podGroupJSONClient) Status() client.SubResourceWriter {
	return c.SubResource("status")
}

func (c *podGroupJSONClient) SubResource(subResource string) client.SubResourceClient {
	return &podGroupSubResourceClient{client: c, subResource: subResource}
}

type podGroupSubResourceClient struct {
	client      *podGroupJSONClient
	subResource string
}

func (c *podGroupSubResourceClient) Get(ctx context.Context, obj client.Object, subResource client.Object, opts ...client.SubResourceGetOption) error {
	return c.client.clientFor(obj).SubResource(c.subResource).Get(ctx, obj, subResource, opts...)
}

func (c *podGroupSubResourceClient) Create(ctx context.Context, obj client.Object, subResource client.Object, opts ...client.SubResourceCreateOption) error {
	return c.client.clientFor(obj).SubResource(c.subResource).Create(ctx, obj, subResource, opts...)
}

func (c *podGroupSubResourceClient) Update(ctx context.Context, obj client.Object, opts ...client.SubResourceUpdateOption) error {
	return c.client.clientFor(obj).SubResource(c.subResource).Update(ctx, obj, opts...)
}

func (c *podGroupSubResourceClient) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
	return c.client.clientFor(obj).SubResource(c.subResource).Patch(ctx, obj, patch, opts...)
}

func (c *podGroupSubResourceClient) Apply(ctx context.Context, obj runtime.ApplyConfiguration, opts ...client.SubResourceApplyOption) error {
	// Apply configurations already use JSON independently of object codecs.
	return c.client.Client.SubResource(c.subResource).Apply(ctx, obj, opts...)
}
