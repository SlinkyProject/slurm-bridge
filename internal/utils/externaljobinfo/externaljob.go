// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package externaljobinfo

import (
	"bytes"
	"encoding/json"

	"k8s.io/utils/ptr"
)

type ExternalJobInfo struct {
	Pods []string `json:"pods"`
}

func (extInfo *ExternalJobInfo) Equal(cmp ExternalJobInfo) bool {
	a, _ := json.Marshal(extInfo)
	b, _ := json.Marshal(cmp)
	return bytes.Equal(a, b)
}

func (podInfo *ExternalJobInfo) ToString() string {
	b, _ := json.Marshal(podInfo)
	return string(b)
}

func ParseIntoExternalJobInfo(str *string, out *ExternalJobInfo) error {
	data := ptr.Deref(str, "")
	return json.Unmarshal([]byte(data), out)
}
