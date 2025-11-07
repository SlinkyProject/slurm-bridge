# slurm-bridge

![Version: 1.0.0-rc1](https://img.shields.io/badge/Version-1.0.0--rc1-informational?style=flat-square) ![Type: application](https://img.shields.io/badge/Type-application-informational?style=flat-square) ![AppVersion: 25.05](https://img.shields.io/badge/AppVersion-25.05-informational?style=flat-square)

Slurm as a Kubernetes Scheduler

**Homepage:** <https://slinky.schedmd.com/>

## Maintainers

| Name | Email | Url |
| ---- | ------ | --- |
| SchedMD LLC. | <slinky@schedmd.com> | <https://support.schedmd.com/> |

## Source Code

* <https://github.com/SlinkyProject/slurm-bridge>

## Requirements

Kubernetes: `>= 1.34.0-0`

## Values

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| admission.affinity | object | `{}` | Set affinity for Kubernetes Pod scheduling. Ref: https://kubernetes.io/docs/concepts/scheduling-eviction/assign-pod-node/#affinity-and-anti-affinity |
| admission.certManager.duration | string | `"43800h0m0s"` | Duration of certificate life. |
| admission.certManager.enabled | bool | `true` | Enables cert-manager for certificate management. |
| admission.certManager.renewBefore | string | `"8760h0m0s"` | Certificate renewal time. Should be before the expiration. |
| admission.enabled | bool | `true` | Enables admission controller. |
| admission.image | object | `{"repository":"ghcr.io/slinkyproject/slurm-bridge-admission","tag":""}` | The image to use, `${repository}:${tag}`. Ref: https://kubernetes.io/docs/concepts/containers/images/#image-names |
| admission.managedNamespaceSelector | object | `{}` | A label selector to select namespaces to be monitored by the pod admission controller. If this is set, managedNamespaces will be ignored. Ref: https://kubernetes.io/docs/concepts/overview/working-with-objects/labels/#label-selectors |
| admission.managedNamespaces | list | `[]` | List of namespaces to be monitored by the pod admission controller. Pods created in any of these namespaces will have their `.spec.schedulerName` changed to slurm-bridge. |
| admission.nodeSelector | map[string]string | `{}` | Node label selector for pod assignment. Ref: https://kubernetes.io/docs/concepts/scheduling-eviction/assign-pod-node/#nodeselector |
| admission.priorityClassName | string | `""` | Set the priority class to use. Ref: https://kubernetes.io/docs/concepts/scheduling-eviction/pod-priority-preemption/#priorityclass |
| admission.replicas | int | `1` | Set the number of replicas to deploy. |
| admission.resources | object | `{}` | Set container resource requests and limits for Kubernetes Pod scheduling. Ref: https://kubernetes.io/docs/concepts/configuration/manage-resources-containers/#resource-requests-and-limits-of-pod-and-container |
| admission.tolerations | list | `[]` | Configure pod tolerations. Ref: https://kubernetes.io/docs/concepts/scheduling-eviction/taint-and-toleration/ |
| controllers.affinity | object | `{}` | Set affinity for Kubernetes Pod scheduling. Ref: https://kubernetes.io/docs/concepts/scheduling-eviction/assign-pod-node/#affinity-and-anti-affinity |
| controllers.image | object | `{"repository":"ghcr.io/slinkyproject/slurm-bridge-controllers","tag":""}` | The image to use, `${repository}:${tag}`. Ref: https://kubernetes.io/docs/concepts/containers/images/#image-names |
| controllers.nodeSelector | map[string]string | `{}` | Node label selector for pod assignment. Ref: https://kubernetes.io/docs/concepts/scheduling-eviction/assign-pod-node/#nodeselector |
| controllers.priorityClassName | string | `""` | Set the priority class to use. Ref: https://kubernetes.io/docs/concepts/scheduling-eviction/pod-priority-preemption/#priorityclass |
| controllers.replicas | int | `1` | Set the number of replicas to deploy. |
| controllers.resources | object | `{}` | Set container resource requests and limits for Kubernetes Pod scheduling. Ref: https://kubernetes.io/docs/concepts/configuration/manage-resources-containers/#resource-requests-and-limits-of-pod-and-container |
| controllers.tolerations | list | `[]` | Configure pod tolerations. Ref: https://kubernetes.io/docs/concepts/scheduling-eviction/taint-and-toleration/ |
| controllers.verbosity | integer | `nil` | Set the verbosity level of the controllers. |
| fullnameOverride | string | `""` | Overrides the full name of the release. |
| nameOverride | string | `""` | Overrides the name of the release. |
| namespaceOverride | string | `""` | Overrides the namespace of the release. |
| scheduler.affinity | object | `{}` | Set affinity for Kubernetes Pod scheduling. Ref: https://kubernetes.io/docs/concepts/scheduling-eviction/assign-pod-node/#affinity-and-anti-affinity |
| scheduler.image | object | `{"repository":"ghcr.io/slinkyproject/slurm-bridge-scheduler","tag":""}` | The image to use, `${repository}:${tag}`. Ref: https://kubernetes.io/docs/concepts/containers/images/#image-names |
| scheduler.leaderElect | bool | `false` | Enables leader election. |
| scheduler.nodeSelector | map[string]string | `{}` | Node label selector for pod assignment. Ref: https://kubernetes.io/docs/concepts/scheduling-eviction/assign-pod-node/#nodeselector |
| scheduler.priorityClassName | string | `""` | Set the priority class to use. Ref: https://kubernetes.io/docs/concepts/scheduling-eviction/pod-priority-preemption/#priorityclass |
| scheduler.replicaCount | int | `1` | Set the number of replicas to deploy. |
| scheduler.resources | object | `{}` | Set container resource requests and limits for Kubernetes Pod scheduling. Ref: https://kubernetes.io/docs/concepts/configuration/manage-resources-containers/#resource-requests-and-limits-of-pod-and-container |
| scheduler.tolerations | list | `[]` | Configure pod tolerations. Ref: https://kubernetes.io/docs/concepts/scheduling-eviction/taint-and-toleration/ |
| scheduler.verbosity | integer | `nil` | Set the verbosity level of the scheduler. |
| schedulerConfig.mcsLabel | string | `"kubernetes"` | Set the Slurm MCS Label to use for placeholder jobs. Ref: https://slurm.schedmd.com/sbatch.html#OPT_mcs-label |
| schedulerConfig.partition | string | `"slurm-bridge"` | Set the default Slurm partition to use for placeholder jobs. Ref: https://slurm.schedmd.com/sbatch.html#OPT_partition |
| schedulerConfig.schedulerName | string | `"slurm-bridge-scheduler"` | Set the name of the scheduler. |
| sharedConfig.slurmJwtSecret | string | `"slurm-bridge-token"` | The secret containing a SLURM_JWT token for authentication. |
| sharedConfig.slurmRestApi | string | `"http://slurm-restapi.slurm:6820"` | The Slurm REST API URL in the form of: `[protocol]://[host]:[port]` |

