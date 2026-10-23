// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package controllercheck

import (
	"fmt"
	"strings"
)

// Trace includes lifecycle actions, intercepted effects, and reconciliation results.
type Trace struct{ entries []string }

func (t *Trace) Logf(format string, args ...any) {
	t.entries = append(t.entries, fmt.Sprintf(format, args...))
}

func (t *Trace) String() string { return strings.Join(t.entries, "\n") }
