#!/bin/sh

case "$1" in
"--explain")
	case "$2" in
	"--tmux")
		tmux new -d -s bridge-explainer \; split-window -h
		tmux send-keys -t bridge-explainer.0 ./hack/scripts/infra_watch.sh ENTER
		tmux send-keys -t bridge-explainer.1 ./hack/scripts/workload_watch.sh ENTER
		tmux a -t bridge-explainer
		;;
	*)
		watch -n1 "\
                echo 'INFRASTRUCTURE'; \
                echo '+++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++'; \
                echo 'The pods in this section are backend infrastructure to support'; \
                echo 'slurm-bridge operations'; \
                printf '\n';
                echo 'SLURM-OPERATOR PODS'; \
                echo '====================='; \
                echo 'These pods contain the controllers which reconcile the CRDs'; \
                echo 'for Slurm-operator.'; \
                echo '---------------------'; \
                kubectl get pods -o wide -n slinky -l app.kubernetes.io/instance=slurm-operator; echo; \
                echo 'SLURM-BRIDGE PODS'; \
                echo '====================='; \
                echo 'These pods contain the admission controller, scheduler, and'; \
                echo  'controllers, which are used by Slurm-bridge to interact with Kubernetes'; \
                echo '---------------------'; \
                kubectl get pods -o wide -n slinky -l app.kubernetes.io/instance=slurm-bridge; echo; \
                echo 'SLURM PODS'; \
                echo '====================='; \
                echo 'These pods are pods launched by Slurm-operator, a way to run'; \
                echo 'Slurm in Kubernetes. They are running in the Slurm namespace,'; \
                echo 'and provide the same capabilities as a baremetal Slurm'; \
                echo 'cluster, in a Kubernetes environment'; \
                echo '---------------------'; \
                kubectl get pods -o wide -n slurm -l app.kubernetes.io/name=slurmd; echo; \
                printf '\n'; \
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
                pods=\$(kubectl get pods -o wide -n slurm-bridge --field-selector='status.phase!=Succeeded,status.phase!=Failed' 2>/dev/null); \
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
		;;
	esac

	;;
"--bridge")
	watch -n1 "\
        echo 'SLURM PODS'; \
        kubectl get pods -o wide -n slurm -l app.kubernetes.io/name=slurmd; echo; \
        echo 'SLURM BRIDGE PODS'; \
        kubectl get pods -o wide -n slurm-bridge; echo; \
        echo 'PODGROUP STATUS'; \
        kubectl get podgroup -n slurm-bridge; echo; \
        echo 'JOB STATUS'; \
        kubectl get jobs -n slurm-bridge; echo; \
        echo 'JOBSET STATUS'; \
        kubectl get jobset -n slurm-bridge; echo; \
        echo 'SINFO'; \
        kubectl exec -n slurm statefulset/slurm-controller -- sinfo; echo; \
        echo 'SQUEUE PENDING'; \
        kubectl exec -n slurm statefulset/slurm-controller -- squeue --states=pending; echo; \
        echo 'SQUEUE RUNNING'; \
        kubectl exec -n slurm statefulset/slurm-controller -- squeue --states=running ; echo; \
        echo 'SQUEUE COMPLETE'; \
        kubectl exec -n slurm statefulset/slurm-controller -- squeue  --states=BF,CA,CD,CF,CG,DL,F,NF,OOM,PR,RD,RF,RH,RQ,RS,RV,SI,SE,SO,S,TO"
	;;
"--demo")
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
	;;
*)
	watch -n1 "\
        echo 'SLURM PODS'; \
        kubectl get pods -o wide -n slurm -l app.kubernetes.io/name=slurmd; echo; \
        echo 'SLURM BRIDGE PODS'; \
        kubectl get pods -o wide -n slurm-bridge; echo; \
        echo 'PODGROUP STATUS'; \
        kubectl get podgroup -n slurm-bridge; echo; \
        echo 'JOB STATUS'; \
        kubectl get jobs -n slurm-bridge; echo; \
        echo 'JOBSET STATUS'; \
        kubectl get jobset -n slurm-bridge; echo; \
        echo 'SINFO'; \
        kubectl exec -n slurm statefulset/slurm-controller -- sinfo; echo; \
        echo 'SQUEUE PENDING'; \
        kubectl exec -n slurm statefulset/slurm-controller -- squeue --states=pending; echo; \
        echo 'SQUEUE RUNNING'; \
        kubectl exec -n slurm statefulset/slurm-controller -- squeue --states=running ; echo; \
        echo 'SQUEUE COMPLETE'; \
        kubectl exec -n slurm statefulset/slurm-controller -- squeue  --states=BF,CA,CD,CF,CG,DL,F,NF,OOM,PR,RD,RF,RH,RQ,RS,RV,SI,SE,SO,S,TO"
	;;
esac
