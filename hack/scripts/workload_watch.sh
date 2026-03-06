#!/bin/sh

watch -n1 "\
    echo 'USER WORKLOADS'; \
    echo '+++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++'; \
    echo 'The pods in this section represent user workloads that are scheduled'; \
    echo 'by Slurm-bridge'; \
    printf '\n'; \
    total=\$(kubectl get pods -n slurm-bridge --field-selector='status.phase!=Succeeded,status.phase!=Failed' --no-headers 2>/dev/null | wc -l | tr -d ' '); \
    pending=\$(kubectl get pods -n slurm-bridge --field-selector=status.phase=Pending --no-headers 2>/dev/null | wc -l | tr -d ' '); \
    echo \"SLURM BRIDGE PODS (\$total total, \$pending pending)\"; \
    echo '====================='; \
    echo 'These pods are all user workloads that are running in the'; \
    echo 'slurm-bridge namespace. By default, slurm-bridge will'; \
    echo 'schedule all compatible workloads submitted in this namespace.'; \
    echo '---------------------'; \
    pods=\$(kubectl get pods  -n slurm-bridge --field-selector='status.phase!=Succeeded,status.phase!=Failed' 2>/dev/null); \
    shown=\$(( \$(tput lines) / 3 )); printf '%s\n' \"\$pods\" | head -n \$shown; \
    lines_total=\$(printf '%s\n' \"\$pods\" | wc -l); more=\$(( lines_total - shown )); [ \$more -gt 0 ] && echo \"\$more more not shown\"; echo; \
    echo 'PODGROUP STATUS'; \
    echo '====================='; \
    kubectl get podgroup -n slurm-bridge; echo; \
    echo 'JOB STATUS / JOBSET STATUS'; \
    echo '====================='; \
    kubectl get jobs -n slurm-bridge > /tmp/dw_jobs; kubectl get jobset -n slurm-bridge > /tmp/dw_js; paste /tmp/dw_jobs /tmp/dw_js; rm -f /tmp/dw_jobs /tmp/dw_js; echo; \
    echo 'WORKLOAD VISIBILITY IN SLURM'; \
    echo '+++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++'; \
    printf '\n'; \
    echo 'SINFO'; \
    echo '====================='; \
    kubectl exec -n slurm statefulset/slurm-controller -- sinfo; echo; \
    echo 'SQUEUE PENDING'; \
    echo '====================='; \
    kubectl exec -n slurm statefulset/slurm-controller -- squeue --states=pending; echo; \
    echo 'SQUEUE RUNNING'; \
    echo '====================='; \
    kubectl exec -n slurm statefulset/slurm-controller -- squeue --states=running ; echo; \
    echo 'SQUEUE COMPLETE'; \
    echo '====================='; \
    kubectl exec -n slurm statefulset/slurm-controller -- squeue  --states=BF,CA,CD,CF,CG,DL,F,NF,OOM,PR,RD,RF,RH,RQ,RS,RV,SI,SE,SO,S,TO"
