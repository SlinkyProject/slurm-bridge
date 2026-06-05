#!/bin/sh

watch -n1 "\
    echo 'INFRASTRUCTURE'; \
    echo '+++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++'; \
    echo 'The pods in this section are backend infrastructure to support'; \
    echo 'slurm-bridge operations'; \
    echo 'SLURM-OPERATOR PODS'; \
    echo '====================='; \
    echo 'These pods contain the controllers which reconcile the CRDs'; \
    echo 'for Slurm-operator.'; \
    echo '---------------------'; \
    kubectl get pods  -n slinky -l app.kubernetes.io/instance=slurm-operator; echo; \
    echo 'SLURM-BRIDGE PODS'; \
    echo '====================='; \
    echo 'These pods contain the admission controller, scheduler, and'; \
    echo  'controllers, which are used by Slurm-bridge to interact with Kubernetes'; \
    echo '---------------------'; \
    kubectl get pods  -n slurm -l app.kubernetes.io/instance=slurm-bridge; echo; \
    echo 'SLURM PODS'; \
    echo '====================='; \
    echo 'These pods are pods launched by Slurm-operator, a way to run'; \
    echo 'Slurm in Kubernetes. They are running in the Slurm namespace,'; \
    echo 'and provide the same capabilities as a baremetal Slurm'; \
    echo 'cluster, in a Kubernetes environment'; \
    echo '---------------------'; \
    kubectl get pods  -n slurm -l app.kubernetes.io/part-of=slurm; echo; \
    printf '\n'; "
