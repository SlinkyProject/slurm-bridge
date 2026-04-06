// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package wellknown

const (
	// FinalizerScheduler exists to process pod deletion events. Once a pod processes
	// if an external job can be deleted, the finalizer is removed.
	FinalizerScheduler = SchedulerPrefix + "finalizer"
)
