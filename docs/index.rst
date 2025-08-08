   Download the slurm-bridge repository
   `here <https://github.com/SlinkyProject/slurm-bridge>`__, start using
   bridge with the `quickstart guide <quickstart.html>`__, or read on
   to learn more.

Slurm-bridge
================================

`Slurm <https://slurm.schedmd.com/overview.html>`__ and
`Kubernetes <https://kubernetes.io/>`__ are workload managers originally
designed for different kinds of workloads. Kubernetes excels at
scheduling workloads that run for an indefinite amount of time, with
potentially vague resource requirements, on a single node, with loose
policy, but can scale its resource pool infinitely to meet demand; Slurm
excels at quickly scheduling workloads that run for a finite amount of
time, with well defined resource requirements and topology, on multiple
nodes, with strict policy, and a known resource pool.

Why you need ``slurm-bridge`` and what it can do
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

--------------

This project enables users to take advantage of the best features of
both workload managers. It contains a
`Kubernetes <https://kubernetes.io/>`__ scheduler to manage select
workloads from Kubernetes, which allows for co-location of Kubernetes
and Slurm workloads within the same cluster. This means the same
hardware can be used to run both traditional HPC and cloud-like
workloads, reducing operating costs.

Using ``slurm-bridge``, workloads can be submitted from within a
Kubernetes context as a ``Pod``, ``PodGroup``, ``Job``, or ``JobSet``,
or from a Slurm context using ``salloc`` or ``sbatch``. Workloads
submitted via Slurm will execute as they would in a Slurm-only
environment, using ``slurmd``. Workloads submitted from Kubernetes will
have their resource requirements translated into a representative Slurm
job by ``slurm-bridge``. That job will serve as a placeholder and will
be scheduled by the Slurm controller. Upon resource allocation to a K8s
workload by the Slurm controller, ``slurm-bridge`` will bind the
workload’s pod(s) to the allocated node(s). At that point, the kubelet
will launch and run the pod the same as it would within a standard
Kubernetes instance.

.. image:: _static/images/slurm-bridge_big-picture.svg
   :width: 80%
   :align: center

For additional architectural notes, see the
`architecture <architecture.html>`__ docs.

Features
~~~~~~~~

--------------

``slurm-bridge`` enables scheduling of Kubernetes workloads using the
Slurm scheduler, and can take advantage of most of the scheduling
features of `Slurm <https://slurm.schedmd.com/overview.html>`__ itself.
These include:

-  `Priority <https://slurm.schedmd.com/priority_multifactor.html>`__:
   assigns priorities to jobs upon submission and on an ongoing basis
   (e.g. as they age).
-  `Preemption <https://slurm.schedmd.com/preempt.html>`__: stop one or
   more low-priority jobs to let a high-priority job run.
-  `QoS <https://slurm.schedmd.com/qos.html>`__: sets of policies
   affecting scheduling priority, preemption, and resource limits.
-  `Fairshare <https://slurm.schedmd.com/fair_tree.html>`__: distribute
   resources equitably among users and accounts based on historical
   usage.
-  `Reservations <https://slurm.schedmd.com/reservations.html>`__:
   reserve resources for select users or groups

Supported Versions
~~~~~~~~~~~~~~~~~~

--------------

-  Kubernetes Version: >= v1.29
-  Slurm Version: >= 25.05

Current Limitations
~~~~~~~~~~~~~~~~~~~

--------------

-  Exclusive, whole node allocations are made for each pod.

--------------

Get started using ``slurm-bridge`` with the `quickstart guide <quickstart.md>`__!

.. toctree::
   :maxdepth: 1
   :caption: Documentation

   admission <admission.md>
   architecture <architecture.md>
   controllers <controllers.md>
   quickstart <quickstart.md>
   scheduler <scheduler.md>
