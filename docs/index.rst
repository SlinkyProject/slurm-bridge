Slurm Bridge
============

.. container::

   |License| |Tag| |Go-Version| |Last-Commit|

Run `Slurm <https://slurm.schedmd.com/overview.html>`__ as a
`Kubernetes <https://kubernetes.io/>`__ scheduler. A
`Slinky <https://slinky.ai/>`__ project.

Table of Contents
-----------------

.. raw:: html

   <!-- mdformat-toc start --slug=github --no-anchors --maxlevel=6 --minlevel=1 -->

- `Slurm Bridge <#slurm-bridge>`__

  - `Table of Contents <#table-of-contents>`__
  - `Overview <#overview>`__
  - `Features <#features>`__

    - `Slurm <#slurm>`__

  - `Requirements <#requirements>`__
  - `Limitations <#limitations>`__
  - `Installation <#installation>`__
  - `Documentation <#documentation>`__
  - `License <#license>`__

.. raw:: html

   <!-- mdformat-toc end -->

Overview
--------

`Slurm <https://slurm.schedmd.com/overview.html>`__ and
`Kubernetes <https://kubernetes.io/>`__ are workload managers originally
designed for different kinds of workloads. In broad strokes: Kubernetes
excels at scheduling workloads that typically run for an indefinite
amount of time, with potentially vague resource requirements, on a
single node, with loose policy, but can scale its resource pool
infinitely to meet demand; Slurm excels at quickly scheduling workloads
that run for a finite amount of time, with well defined resource
requirements and topology, on multiple nodes, with strict policy, but
its resource pool is known.

This project enables the best of both workload managers. It contains a
`Kubernetes <https://kubernetes.io/>`__ scheduler to manage select
workload from Kubernetes.

For additional architectural notes, see the
`architecture <architecture.html>`__ docs.

Features
--------

Slurm
~~~~~

Slurm is a full featured HPC workload manager. To highlight a few
features:

- `Priority <https://slurm.schedmd.com/priority_multifactor.html>`__:
  assigns priorities to jobs upon submission and on an ongoing basis
  (e.g. as they age).
- `Preemption <https://slurm.schedmd.com/preempt.html>`__: stop one or
  more low-priority jobs to let a high-priority job run.
- `QoS <https://slurm.schedmd.com/qos.html>`__: sets of policies
  affecting scheduling priority, preemption, and resource limits.
- `Fairshare <https://slurm.schedmd.com/fair_tree.html>`__: distribute
  resources equitably among users and accounts based on historical
  usage.

Requirements
------------

- **Kubernetes Version**: >=
  `v1.29 <https://kubernetes.io/blog/2023/12/13/kubernetes-v1-29-release/>`__
- **Slurm Version**: >=
  `25.05 <https://www.schedmd.com/slurm-version-25-05-0-is-now-available/>`__

Limitations
-----------

- Exclusive, whole node allocations are made for each pod.

Installation
------------

Create a secret for slurm-bridge to communicate with Slurm.

.. code:: sh

   export SLURM_JWT=$(scontrol token username=slurm lifespan=infinite)
   kubectl create namespace slurm-bridge
   kubectl create secret generic slurm-bridge-jwt-token --namespace=slinky --from-literal="auth-token=$SLURM_JWT" --type=Opaque

Install the slurm-bridge scheduler:

.. code:: sh

   helm install slurm-bridge oci://ghcr.io/slinkyproject/charts/slurm-bridge \
     --namespace=slinky --create-namespace

For additional instructions, see the
`quickstart <quickstart.html>`__ guide.

Documentation
-------------

Project documentation is located in the `docs <./docs/>`__ directory of
this repository.

Slinky documentation can be found
`here <https://slinky.schedmd.com/>`__.

License
-------

Copyright (C) SchedMD LLC.

Licensed under the `Apache License, Version
2.0 <http://www.apache.org/licenses/LICENSE-2.0>`__ you may not use
project except in compliance with the license.

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an “AS IS” BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.

.. raw:: html

   <!-- Links -->

.. |License| image:: https://img.shields.io/badge/License-Apache_2.0-blue.svg?style=for-the-badge
   :target: ./LICENSES/Apache-2.0.txt
.. |Tag| image:: https://img.shields.io/github/v/tag/SlinkyProject/slurm-bridge?style=for-the-badge
   :target: https://github.com/SlinkyProject/slurm-bridge/tags/
.. |Go-Version| image:: https://img.shields.io/github/go-mod/go-version/SlinkyProject/slurm-bridge?style=for-the-badge
   :target: ./go.mod
.. |Last-Commit| image:: https://img.shields.io/github/last-commit/SlinkyProject/slurm-bridge?style=for-the-badge
   :target: https://github.com/SlinkyProject/slurm-bridge/commits/

.. toctree::
    :maxdepth: 2
    :hidden:

    admission.md
    architecture.md
    controllers.md
    quickstart.md
    scheduler.md
