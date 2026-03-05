# User Management in Slurm-bridge

## Table of Contents

<!-- mdformat-toc start --slug=github --no-anchors --maxlevel=6 --minlevel=1 -->

- [User Management in Slurm-bridge](#user-management-in-slurm-bridge)
  - [Table of Contents](#table-of-contents)
  - [Overview](#overview)
  - [Using Kyverno Policies with Slurm-bridge](#using-kyverno-policies-with-slurm-bridge)
    - [Overview of Kyverno](#overview-of-kyverno)
    - [Configuring Kyverno for use with Slurm-Bridge](#configuring-kyverno-for-use-with-slurm-bridge)
    - [Example Kyverno Policies for Slurm-bridge](#example-kyverno-policies-for-slurm-bridge)
    - [Enforce Specification of `slurmjob.slinky.slurm.net/account` and `slurmjob.slinky.slurm.net/user-id`](#enforce-specification-of-slurmjobslinkyslurmnetaccount-and-slurmjobslinkyslurmnetuser-id)
    - [Prevent modification of Slinky annotations after workload submission](#prevent-modification-of-slinky-annotations-after-workload-submission)

<!-- mdformat-toc end -->

## Overview

Slurm-bridge uses [placeholder jobs](scheduler) to represent Kubernetes
workloads in Slurm for scheduling purposes. Placeholder jobs are submitted using
the JWT that is generated when the Slurm-bridge Helm chart is deployed. By
default, placeholder jobs for Kubernetes workloads are submitted as `SlurmUser`,
but they may be submitted as another user in Slurm by using the
`slurmjob.slinky.slurm.net/user-id` annotation.

Kubernetes workloads scheduled by Slurm-bridge always run within the user
context that they are submitted with - Slurm-bridge does not interfere with a
Kubernetes workloads' user context at runtime.

## Using Kyverno Policies with Slurm-bridge

### Overview of Kyverno

[Kyverno] is a cloud-native policy engine, which can be used to enforce security
policies using a Kubernetes admission controller. When running Slurm-bridge in a
multi-tenant production environment, it may be desirable to prevent unprivileged
users from modifying certain fields of Kubernetes objects, in order to prevent
interference with the operations of Slurm-bridge.

### Configuring Kyverno for use with Slurm-Bridge

In order to use enforce policies related to a CRD, one must first use
ClusterRoles to grant the Kyverno controller additional permissions related to
that CRD. More information on that process can be found
[here][kyverno-customize-permissions]. For example, the following ClusterRole
provides the Kyverno controller with sufficient permissions to create Pods and
PodGroups:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: kyverno:create-pods-and-podgroups
  labels:
    rbac.kyverno.io/aggregate-to-background-controller: 'true'
rules:
  - apiGroups:
      - ""
    resources:
      - pods
    verbs:
      - create
      - update
  - apiGroups:
      - podgroups.scheduling.x-k8s.io
    resources:
      - podgroups
    verbs:
      - create
      - update
```

After applying this ClusterRole, confirm that the Kyverno background
controller's top-level ClusterRole has successfully aggregated your new
permissions:

```bash
kubectl get clusterrole kyverno:background-controller -o yaml
```

### Example Kyverno Policies for Slurm-bridge

Below are some example Kyverno policies that can be used with Slurm-bridge.

### Enforce Specification of `slurmjob.slinky.slurm.net/account` and `slurmjob.slinky.slurm.net/user-id`

The following policy ensures that users specify the Slurm account and user-id
under which the Slurm-bridge workload will be submitted.

```yaml
apiVersion: kyverno.io/v1
# The `ClusterPolicy` kind applies to the entire cluster.
kind: ClusterPolicy
metadata:
  name: require-user-and-account-annotations
# The `spec` defines properties of the policy.
spec:
  # The `validationFailureAction` tells Kyverno if the resource being validated should be allowed but reported (`Audit`) or blocked (`Enforce`).
  validationFailureAction: Enforce
  # The `rules` is one or more rules which must be true.
  rules:
  - name: require-user-and-account-annotations
    # The `match` statement sets the scope of what will be checked. In this case, it is any `Pod` or `PodGroup` resource.
    match:
      any:
      - resources:
          kinds:
          - Pod
          - PodGroup
    # The `validate` statement tries to positively check what is defined. If the statement, when compared with the requested resource, is true, it is allowed. If false, it is blocked.
    validate:
      # The `message` is what gets displayed to a user if this rule fails validation.
      message: "You must have annotations `slurmjob.slinky.slurm.net/account` and `slurmjob.slinky.slurm.net/user-id` with a string value set on all new Pods and PodGroups."
      # The `pattern` object defines what pattern will be checked in the resource. In this case, it is looking for `metadata.annotations` with `slurmjob.slinky.slurm.net=account` and `slurmjob.slinky.slurm.net=user-id.
      pattern:
        metadata:
          annotations:
            slurmjob.slinky.slurm.net/account: "?*"
            slurmjob.slinky.slurm.net/user-id: "?*"
```

A user attempting to apply a `Pod` or `PodGroup` without the specified
annotations will be denied:

```bash
kubectl apply -f hack/examples/pod/sleep.yaml
Error from server: error when creating "hack/examples/pod/sleep.yaml": admission webhook "validate.kyverno.svc-fail" denied the request:

resource Pod/slurm-bridge/pod-single was blocked due to the following policies

require-user-and-account-annotations:
  require-user-and-account-annotations: 'validation error: You must have annotations
    `slurmjob.slinky.slurm.net/account` and `slurmjob.slinky.slurm.net/user-id` with
    a string value set on all new Pods and PodGroups. rule require-user-and-account-annotations
    failed at path /metadata/annotations/slurmjob.slinky.slurm.net/account/'
```

While pods with the correct annotations are passed through to slurm-bridge's
admission controller:

```bash
kubectl apply -f hack/examples/pod/sleep.yaml
pod/pod-single created
```

### Prevent modification of Slinky annotations after workload submission

The following Kyverno policy prevents users from modifying the value of certain
Slinky annotations after workload submission, without interfering with the
normal operation of Slurm-bridge.

```yaml
apiVersion: kyverno.io/v1
kind: ClusterPolicy
metadata:
  name: block-updates-to-slinky-annotations
spec:
  validationFailureAction: Enforce
  background: false
  rules:
  - name: block-updates-to-slinky-annotations
    match:
      any:
      - resources:
          kinds:
          - Pod
          - PodGroup
    preconditions:
      any:
      - key: "{{request.operation}}"
        operator: Equals
        value: UPDATE
    exclude:
      any:
      - subjects:
        - kind: ServiceAccount
          name: slurm-*
          namespace: slinky
      - clusterRoles:
        - slurm-*
    validate:
      allowExistingViolations: false
      message: Modifying the `slinky.slurm.net/slurm-node` annotation on a submitted pod or podgroup is not allowed.
      pattern:
        metadata:
          "=(annotations)":
            X(slinky.slurm.net/slurm-node): "*?"
```

<!-- Links -->

[kyverno]: https://kyverno.io/
[kyverno-customize-permissions]: https://kyverno.io/docs/installation/customization/#customizing-permissions
