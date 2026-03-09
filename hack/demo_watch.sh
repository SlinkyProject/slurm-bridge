#!/bin/sh

watch -n1 "\
    echo 'SLURM PODS'; \
    kubectl get pods -o wide -n slurm -l app.kubernetes.io/name=slurmd; echo; \
    total=\$(kubectl get pods -n slurm-bridge --field-selector='status.phase!=Succeeded,status.phase!=Failed' --no-headers 2>/dev/null | wc -l | tr -d ' '); \
    pending=\$(kubectl get pods -n slurm-bridge --field-selector=status.phase=Pending --no-headers 2>/dev/null | wc -l | tr -d ' '); \
    echo \"SLURM BRIDGE PODS (\$total total, \$pending pending)\"; \
    pods=\$(kubectl get pods -o wide -n slurm-bridge --field-selector='status.phase!=Succeeded,status.phase!=Failed' 2>/dev/null); \
    shown=\$(( \$(tput lines) / 3 )); printf '%s\n' \"\$pods\" | head -n \$shown; \
    lines_total=\$(printf '%s\n' \"\$pods\" | wc -l); more=\$(( lines_total - shown )); [ \$more -gt 0 ] && echo \"\$more more not shown\"; echo; \
    echo 'PODGROUP STATUS'; \
    kubectl get podgroup -n slurm-bridge; echo; \
    echo 'JOB STATUS / JOBSET STATUS'; \
    kubectl get jobs -n slurm-bridge > /tmp/dw_jobs; kubectl get jobset -n slurm-bridge > /tmp/dw_js; paste /tmp/dw_jobs /tmp/dw_js; rm -f /tmp/dw_jobs /tmp/dw_js; echo; \
    echo 'SINFO'; \
    kubectl exec -n slurm statefulset/slurm-controller -- sinfo; echo; \
    echo 'SQUEUE PENDING'; \
    kubectl exec -n slurm statefulset/slurm-controller -- squeue --states=pending; echo; \
    echo 'SQUEUE RUNNING'; \
    kubectl exec -n slurm statefulset/slurm-controller -- squeue --states=running ; echo; \
    echo 'SQUEUE COMPLETE'; \
    kubectl exec -n slurm statefulset/slurm-controller -- squeue  --states=BF,CA,CD,CF,CG,DL,F,NF,OOM,PR,RD,RF,RH,RQ,RS,RV,SI,SE,SO,S,TO"
