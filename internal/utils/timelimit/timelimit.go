// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

// Package timelimit parses the values accepted by the Slurm job time limit
// annotation into the whole minutes that Slurm's TimeLimit field expects.
package timelimit

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/common/model"
)

const (
	secondsPerMinute = 60
	secondsPerHour   = 60 * secondsPerMinute
	secondsPerDay    = 24 * secondsPerHour
)

// Parse converts a time limit into whole minutes. Three notations are
// accepted:
//
//   - a bare integer, read as minutes ("30")
//   - a unit-suffixed duration, as understood by Prometheus ("90s", "10m",
//     "2h", "1d", "1w", "1h30m")
//   - one of Slurm's --time formats ("MM:SS", "HH:MM:SS", "D-HH", "D-HH:MM",
//     "D-HH:MM:SS"), so a manifest can mirror an equivalent sbatch invocation
//
// A non-zero value shorter than a minute rounds up to one minute rather than
// down to zero, which Slurm reads as no limit at all.
func Parse(value string) (int32, error) {
	s := strings.TrimSpace(value)
	switch {
	case s == "":
		return 0, invalidError(value, "value is empty")
	case strings.HasPrefix(s, "-"):
		// Caught here so the Slurm parser does not report it as an empty day
		// component. An interior dash is still a day separator.
		return 0, invalidError(value, "value is negative")
	}

	// A bare integer means minutes, both for compatibility with manifests
	// written before units were accepted and because Slurm reads its own
	// single-field --time the same way. ParseUint rather than ParseInt, so a
	// signed value is not mistaken for a time limit, and 31 bits because a
	// minute count wider than that cannot reach Slurm anyway.
	if minutes, err := strconv.ParseUint(s, 10, 31); err == nil {
		return toMinutes(value, int64(minutes)*secondsPerMinute)
	}

	var seconds int64
	var err error
	if strings.ContainsAny(s, ":-") {
		seconds, err = parseSlurmTime(s)
	} else {
		seconds, err = parseDuration(s)
	}
	if err != nil {
		return 0, invalidError(value, err.Error())
	}

	return toMinutes(value, seconds)
}

// toMinutes converts seconds into the whole minutes of Slurm's int32 TimeLimit.
// Any remainder rounds up, because the time limit is minute-granular and
// rounding a short request down would leave zero, which Slurm reads as no limit
// at all. Testing the remainder rather than using the usual (a+b-1)/b shorthand
// keeps a seconds count near the top of the range from overflowing the addition
// and turning negative.
func toMinutes(value string, seconds int64) (int32, error) {
	minutes := seconds / secondsPerMinute
	if seconds%secondsPerMinute != 0 {
		minutes++
	}
	if minutes > math.MaxInt32 {
		return 0, invalidError(value, fmt.Sprintf("minutes must be at most %d", math.MaxInt32))
	}
	return int32(minutes), nil //nolint:gosec // disable G115, range checked above
}

// parseDuration handles the unit-suffixed notation and returns whole seconds,
// rounding up so sub-second precision is not lost before toMinutes rounds
// again. Prometheus is used rather than time.ParseDuration because the latter
// knows nothing of "d" or "w".
func parseDuration(s string) (int64, error) {
	d, err := model.ParseDuration(s)
	if err != nil {
		return 0, err
	}
	seconds := int64(d) / int64(time.Second)
	if int64(d)%int64(time.Second) != 0 {
		seconds++
	}
	return seconds, nil
}

// parseSlurmTime handles the colon- and dash-separated notation of Slurm's
// --time option and returns the total number of seconds. The single-field "MM"
// form is handled by Parse, since it is indistinguishable from a bare integer.
//
// Ref: https://slurm.schedmd.com/sbatch.html#OPT_time
func parseSlurmTime(s string) (int64, error) {
	var seconds int64
	rest := s
	days, afterDays, hasDays := strings.Cut(s, "-")
	if hasDays {
		v, err := parseField(days)
		if err != nil {
			return 0, err
		}
		seconds = v * secondsPerDay
		rest = afterDays
	}

	// With a day component the remainder starts at hours; without one, two
	// fields are minutes and seconds.
	fields := strings.Split(rest, ":")
	units := []int64{secondsPerHour, secondsPerMinute, 1}
	switch {
	case hasDays && len(fields) > 3:
		return 0, errors.New("expected D-HH, D-HH:MM or D-HH:MM:SS")
	case !hasDays && len(fields) == 2:
		units = []int64{secondsPerMinute, 1}
	case !hasDays && len(fields) != 3:
		return 0, errors.New("expected MM:SS or HH:MM:SS")
	}

	for i, field := range fields {
		v, err := parseField(field)
		if err != nil {
			return 0, err
		}
		seconds += v * units[i]
	}
	return seconds, nil
}

// parseField parses one component of a Slurm time. ParseUint rejects the sign
// prefixes ParseInt would accept, and 32 bits keeps the widest component, days,
// from overflowing the seconds total.
func parseField(s string) (int64, error) {
	v, err := strconv.ParseUint(s, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("%q is not a whole number", s)
	}
	return int64(v), nil
}

func invalidError(value, reason string) error {
	return fmt.Errorf("invalid time limit %q: %s: expected minutes (\"30\"), a duration (\"90s\", \"2h\", \"1d\") or a Slurm time (\"1-12:00:00\")", value, reason)
}
