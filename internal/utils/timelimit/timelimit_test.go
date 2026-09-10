// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package timelimit

import (
	"math"
	"testing"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		want    int32
		wantErr bool
	}{
		// A bare integer keeps meaning minutes.
		{name: "BareMinutes", value: "10", want: 10},
		{name: "BareZero", value: "0", want: 0},
		{name: "BareMaxInt32", value: "2147483647", want: math.MaxInt32},
		{name: "Whitespace", value: "  10  ", want: 10},

		// Unit-suffixed durations.
		{name: "Seconds", value: "600s", want: 10},
		{name: "Minutes", value: "10m", want: 10},
		{name: "Hours", value: "2h", want: 120},
		{name: "Days", value: "1d", want: 1440},
		{name: "Weeks", value: "1w", want: 10080},
		{name: "Compound", value: "1h30m", want: 90},
		{name: "SubMinuteRoundsUp", value: "10s", want: 1},
		{name: "PartialMinuteRoundsUp", value: "61s", want: 2},
		{name: "SubSecondRoundsUp", value: "1ms", want: 1},
		// Truncating to whole seconds before rounding would give 1.
		{name: "SubSecondCarriesPastMinute", value: "60500ms", want: 2},
		// Near the top of the range Prometheus accepts, where a (a+b-1)/b
		// ceiling would overflow and yield a negative time limit.
		{name: "LargestPrometheusDuration", value: "9223372036854ms", want: 153722868},

		// Slurm --time formats.
		{name: "SlurmMinutesSeconds", value: "10:30", want: 11},
		{name: "SlurmHoursMinutesSeconds", value: "01:30:00", want: 90},
		{name: "SlurmDaysHours", value: "1-00", want: 1440},
		{name: "SlurmDaysHoursMinutes", value: "1-00:30", want: 1470},
		{name: "SlurmDaysHoursMinutesSeconds", value: "1-00:30:30", want: 1471},
		{name: "SlurmZeroDays", value: "0-01", want: 60},

		// Rejected values.
		{name: "Empty", value: "", wantErr: true},
		{name: "OnlyWhitespace", value: "   ", wantErr: true},
		{name: "NotANumber", value: "foo", wantErr: true},
		{name: "UnknownUnit", value: "10x", wantErr: true},
		{name: "Negative", value: "-10", wantErr: true},
		{name: "NegativeDuration", value: "-10m", wantErr: true},
		{name: "SignedMinutes", value: "+10", wantErr: true},
		{name: "SignedSlurmField", value: "1-+2", wantErr: true},
		{name: "Fractional", value: "1.5h", wantErr: true},
		{name: "UnitsOutOfOrder", value: "30m1h", wantErr: true},
		{name: "TooManyColons", value: "1:2:3:4", wantErr: true},
		{name: "TooManyColonsWithDays", value: "1-2:3:4:5", wantErr: true},
		{name: "NonNumericDays", value: "abc-12", wantErr: true},
		{name: "DaysOutOfRange", value: "99999999999-00", wantErr: true},
		{name: "TrailingDash", value: "1-", wantErr: true},
		{name: "EmptyColonField", value: "1::2", wantErr: true},
		{name: "MultipleDashes", value: "1-2-3", wantErr: true},
		{name: "BareOverMaxInt32", value: "2147483648", wantErr: true},
		{name: "DaysOverMaxInt32Minutes", value: "2147483647-00", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse(tt.value)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Parse(%q) error = %v, wantErr %v", tt.value, err, tt.wantErr)
			}
			if err == nil && got != tt.want {
				t.Errorf("Parse(%q) = %d, want %d", tt.value, got, tt.want)
			}
		})
	}
}
